package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The Phase 0 definition of done, as a test: build the real binary, run it on
// an ephemeral loopback port with no proxy and no external IdP, sign in
// through the development issuer, create a project, restart, and see it again.

type proc struct {
	cmd  *exec.Cmd
	base string
	logs *syncBuf
}

// syncBuf is a goroutine-safe buffer: exec copies stderr on its own goroutine.
type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func buildBinary(t *testing.T, tags string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "scriobh")
	args := []string{"build", "-o", bin}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args, ".")
	out, err := exec.Command("go", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func start(t *testing.T, bin, dataDir string, extra ...string) *proc {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_LISTEN=127.0.0.1:0", "SCRIOBH_DATA_DIR=" + dataDir, "SCRIOBH_OIDC_STUB=1"}, extra...)
	stdout, _ := cmd.StdoutPipe()
	logs := &syncBuf{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &proc{cmd: cmd, logs: logs}
	t.Cleanup(func() { p.stop(t) })
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "LISTEN "); ok {
				found <- rest
				break
			}
		}
		io.Copy(io.Discard, stdout)
	}()
	select {
	case p.base = <-found:
	case <-time.After(20 * time.Second):
		t.Fatalf("server did not print LISTEN; logs:\n%s", logs)
	}
	return p
}

func (p *proc) stop(t *testing.T) {
	if p.cmd.Process == nil || p.cmd.ProcessState != nil {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
	}
}

func client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 20 * time.Second}
}

// status drains the response and returns its status code.
func status(t *testing.T, r *http.Response) int {
	t.Helper()
	body(t, r)
	return r.StatusCode
}

func body(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

var (
	reForm   = regexp.MustCompile(`(?s)<form method="post" action="([^"]+)">(.*?)</form>`)
	reHidden = regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`)
	reCSRF   = regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)">`)
)

// signIn drives the real OIDC flow against the in-process development issuer.
func signIn(t *testing.T, c *http.Client, base, sub string) {
	t.Helper()
	resp, err := c.Get(base + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	page := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(page, "Development sign-in") {
		t.Fatalf("expected the issuer's picker, got %d:\n%s", resp.StatusCode, page)
	}
	var action string
	form := url.Values{}
	for _, m := range reForm.FindAllStringSubmatch(page, -1) {
		if strings.Contains(m[2], `value="`+sub+`"`) {
			action = strings.ReplaceAll(m[1], "&amp;", "&")
			for _, h := range reHidden.FindAllStringSubmatch(m[2], -1) {
				form.Set(h[1], strings.NewReplacer("&amp;", "&", "&#34;", `"`).Replace(h[2]))
			}
		}
	}
	if action == "" {
		t.Fatalf("no form for %s in:\n%s", sub, page)
	}
	resp, err = c.PostForm(action, form)
	if err != nil {
		t.Fatal(err)
	}
	if b := body(t, resp); resp.StatusCode != 200 {
		t.Fatalf("callback ended at %d:\n%s", resp.StatusCode, b)
	}
}

func TestEndToEnd(t *testing.T) {
	bin := buildBinary(t, "")
	data := t.TempDir()
	p := start(t, bin, data)
	if !strings.HasPrefix(p.base, "http://127.0.0.1:") {
		t.Fatalf("base %q is not an ephemeral loopback URL", p.base)
	}
	c := client()

	// Signed out: the sign-in page, and the API refuses.
	resp, _ := c.Get(p.base + "/")
	if s := body(t, resp); !strings.Contains(s, "Sign in") {
		t.Fatalf("signed-out home should offer sign-in:\n%s", s)
	}
	resp, _ = c.Get(p.base + "/api/v1/projects")
	if status(t, resp) != 401 {
		t.Errorf("anonymous API call = %d, want 401", resp.StatusCode)
	}

	signIn(t, c, p.base, "dev-writer")
	resp, _ = c.Get(p.base + "/")
	lib := body(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(lib, "No projects yet") {
		t.Fatalf("empty library expected, got %d:\n%s", resp.StatusCode, lib)
	}
	csrf := reCSRF.FindStringSubmatch(lib)
	if csrf == nil {
		t.Fatal("no csrf token on the page")
	}

	// A cross-site POST is refused even with a valid session and token.
	req, _ := http.NewRequest("POST", p.base+"/ui/projects", strings.NewReader(url.Values{"title": {"Evil"}, "_csrf": {csrf[1]}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, _ = c.Do(req)
	if st := status(t, resp); st != 403 {
		t.Errorf("cross-site POST = %d, want 403", st)
	}
	// A same-origin POST without the token is refused too.
	resp, _ = c.PostForm(p.base+"/ui/projects", url.Values{"title": {"No token"}})
	if st := status(t, resp); st != 403 {
		t.Errorf("POST without csrf token = %d, want 403", st)
	}

	// Create a project through the UI route.
	resp, err := c.PostForm(p.base+"/ui/projects", url.Values{"title": {"The Salt Road"}, "kind": {"novel"}, "author_name": {"Máire Nolan"}, "_csrf": {csrf[1]}})
	if err != nil {
		t.Fatal(err)
	}
	if page := body(t, resp); resp.StatusCode != 200 || !strings.Contains(page, "The Salt Road") || resp.Request.URL.Path != "/p/the-salt-road" {
		t.Fatalf("expected the project page, got %d at %s:\n%s", resp.StatusCode, resp.Request.URL.Path, page)
	}

	listProjects := func(c *http.Client, base string) []string {
		resp, err := c.Get(base + "/api/v1/projects")
		if err != nil {
			t.Fatal(err)
		}
		raw := body(t, resp)
		var out struct {
			Items []struct{ Title, Slug string }
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatalf("bad JSON %q: %v", raw, err)
		}
		var titles []string
		for _, i := range out.Items {
			titles = append(titles, i.Title)
		}
		return titles
	}
	if got := listProjects(c, p.base); len(got) != 1 || got[0] != "The Salt Road" {
		t.Fatalf("projects = %v", got)
	}

	// Another writer cannot see it, and gets a 404, not a 403.
	other := client()
	signIn(t, other, p.base, "dev-admin") // admins see everything by design, so use a reader below
	reader := client()
	signIn(t, reader, p.base, "dev-reader")
	if got := listProjects(reader, p.base); len(got) != 0 {
		t.Errorf("a reader sees %v", got)
	}
	resp, _ = reader.Get(p.base + "/p/the-salt-road")
	if st := status(t, resp); st != 404 {
		t.Errorf("outsider opening a project = %d, want 404", st)
	}
	req, _ = http.NewRequest("POST", p.base+"/api/v1/projects", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, _ = reader.Do(req)
	if st := status(t, resp); st != 403 {
		t.Errorf("reader creating a project = %d, want 403", st)
	}

	// Restart on a fresh ephemeral port. The cookie jar is reused: the session
	// lives in the database, so it survives, and so does the project.
	p.stop(t)
	p2 := start(t, bin, data)
	if p2.base == p.base {
		t.Log("same port reused; fine")
	}
	if got := listProjects(c, p2.base); len(got) != 1 || got[0] != "The Salt Road" {
		t.Fatalf("after restart projects = %v\nlogs:\n%s", got, p2.logs)
	}
	// The same person signs in again after a restart on a NEW port: identity is
	// bound to the issuer, which must not change when the port does.
	again := client()
	signIn(t, again, p2.base, "dev-writer")
	if got := listProjects(again, p2.base); len(got) != 1 {
		t.Fatalf("signing in again on a new port lost the account: %v\nlogs:\n%s", got, p2.logs)
	}
	resp, _ = c.Get(p2.base + "/healthz")
	if h := body(t, resp); !strings.Contains(h, `"ok"`) {
		t.Errorf("healthz = %s", h)
	}

	// Security headers are present on a page.
	resp, _ = c.Get(p2.base + "/")
	body(t, resp)
	for _, h := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy", "Permissions-Policy"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("missing %s", h)
		}
	}
	if csp := resp.Header.Get("Content-Security-Policy"); strings.Contains(csp, "unsafe") || !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("weak CSP: %s", csp)
	}
	// The request log names the matched route and the signed-in principal, and
	// never a query string or a body.
	time.Sleep(200 * time.Millisecond)
	logs := p2.logs.String()
	if !strings.Contains(logs, `"route":"GET /api/v1/projects"`) || !strings.Contains(logs, `"principal":"usr_`) {
		t.Errorf("request log lacks a route or a principal:\n%s", logs)
	}
	if strings.Contains(logs, "Salt Road") || strings.Contains(logs, "csrf") {
		t.Errorf("the request log leaks content:\n%s", logs)
	}
}

func TestProductionBuildRefusesTheStubIssuer(t *testing.T) {
	bin := buildBinary(t, "prod")
	cmd := exec.Command(bin)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_LISTEN=127.0.0.1:0", "SCRIOBH_DATA_DIR=" + t.TempDir(), "SCRIOBH_OIDC_STUB=1"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "development issuer") {
		t.Fatalf("a prod build must refuse SCRIOBH_OIDC_STUB; err=%v out=%s", err, out)
	}
}

func TestStubRefusedOnPublicBaseURL(t *testing.T) {
	bin := buildBinary(t, "")
	cmd := exec.Command(bin)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_LISTEN=127.0.0.1:0", "SCRIOBH_DATA_DIR=" + t.TempDir(), "SCRIOBH_OIDC_STUB=1", "SCRIOBH_BASE_URL=https://write.example.com"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "loopback") {
		t.Fatalf("stub on a public base URL must be refused; err=%v out=%s", err, out)
	}
}

// The server starts with an unknown SCRIOBH_ name and says so once, in the log, naming the nearest setting and never
// the value; a mistyped boolean stops it before anything is opened.
func TestStartupWarnsAboutAnUnknownNameAndRefusesAMistypedBoolean(t *testing.T) {
	bin := buildBinary(t, "")
	p := start(t, bin, t.TempDir(), typo+"=very-private-value")
	logs := p.logs.String()
	if !strings.Contains(logs, `"msg":"unknown environment variable ignored"`) || !strings.Contains(logs, `"name":"`+typo+`"`) || !strings.Contains(logs, "did you mean SCRIOBH_TRUST_PROXY?") {
		t.Errorf("no warning in the log:\n%s", logs)
	}
	if strings.Contains(logs, "very-private-value") {
		t.Errorf("the value reached the log:\n%s", logs)
	}
	if n := strings.Count(logs, "unknown environment variable ignored"); n != 1 {
		t.Errorf("%d warnings for one name", n)
	}

	cmd := exec.Command(bin)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_LISTEN=127.0.0.1:0", "SCRIOBH_DATA_DIR=" + t.TempDir(), "SCRIOBH_OIDC_STUB=1", "SCRIOBH_TRUST_PROXY=ture"}
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "SCRIOBH_TRUST_PROXY") || !strings.Contains(string(out), "use one of") {
		t.Fatalf("a mistyped boolean must stop the server naming it; err=%v out=%s", err, out)
	}
}

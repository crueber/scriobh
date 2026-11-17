package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Desktop mode, as the shell runs it: the production build (which has no development issuer in it), started with
// SCRIOBH_DESKTOP=1 and a launch token, no data directory named, driven over loopback HTTP and by signals.

const (
	dtToken = "5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e"
	dtKey   = "keychain-secret-keychain-secret-0123456789abcdef"
)

type dproc struct {
	cmd    *exec.Cmd
	base   string
	stdout *syncBuf
	logs   *syncBuf
	done   chan struct{}
}

// desktopEnv is the environment the shell gives the sidecar, with HOME at home. extra adds or, with an empty value
// after the "=", removes.
func desktopEnv(home, token string, extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "APPDATA=" + filepath.Join(home, "AppData"), "SCRIOBH_DESKTOP=1", "SCRIOBH_LISTEN=127.0.0.1:0",
		"SCRIOBH_LAUNCH_TOKEN=" + token, "SCRIOBH_SECRET_KEY=" + dtKey}
	for _, e := range extra {
		name, val, _ := strings.Cut(e, "=")
		kept := env[:0]
		for _, x := range env {
			if !strings.HasPrefix(x, name+"=") {
				kept = append(kept, x)
			}
		}
		env = kept
		if val != "" {
			env = append(env, e)
		}
	}
	return env
}

// desktopDataDir is where the binary must put its data when no directory is named.
func desktopDataDir(home string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Scriobh")
	case "windows":
		return filepath.Join(home, "AppData", "Scriobh")
	}
	return filepath.Join(home, ".local", "share", "scriobh")
}

func startDesktop(t *testing.T, bin string, env []string) *dproc {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	out, _ := cmd.StdoutPipe()
	p := &dproc{cmd: cmd, stdout: &syncBuf{}, logs: &syncBuf{}, done: make(chan struct{})}
	cmd.Stderr = p.logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			p.stdout.Write([]byte(sc.Text() + "\n"))
			if rest, ok := strings.CutPrefix(sc.Text(), "LISTEN "); ok {
				select {
				case found <- rest:
				default:
				}
			}
		}
		_ = cmd.Wait()
		close(p.done)
	}()
	t.Cleanup(func() { p.signal(t, syscall.SIGKILL) })
	select {
	case p.base = <-found:
	case <-p.done:
		t.Fatalf("the desktop server exited before it listened (%v); output:\n%s\n%s", cmd.ProcessState, p.stdout, p.logs)
	case <-time.After(30 * time.Second):
		t.Fatalf("no LISTEN line; logs:\n%s", p.logs)
	}
	return p
}

// signal sends sig and waits for the exit; it returns the exit status (-1 when the process had to be killed).
func (p *dproc) signal(t *testing.T, sig syscall.Signal) int {
	t.Helper()
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode()
	default:
	}
	_ = p.cmd.Process.Signal(sig)
	select {
	case <-p.done:
		return p.cmd.ProcessState.ExitCode()
	case <-time.After(10 * time.Second): // the shell's own patience
		_ = p.cmd.Process.Kill()
		<-p.done
		return -1
	}
}

var reToken = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

// desktopFirstRun opens the window's address and, when the profile form is shown, fills it in. It returns the page
// the window ends on.
func desktopFirstRun(t *testing.T, c *http.Client, base, token, name string) (string, string) {
	t.Helper()
	resp, err := c.Get(base + "/desktop/start?t=" + token)
	if err != nil {
		t.Fatal(err)
	}
	at, page := resp.Request.URL.Path, body(t, resp)
	if at == "/desktop/profile" {
		m := reToken.FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("the profile form has no token:\n%.300s", page)
		}
		resp, err = c.PostForm(base+"/desktop/profile", url.Values{"_csrf": {m[1]}, "name": {name}})
		if err != nil {
			t.Fatal(err)
		}
		at, page = resp.Request.URL.Path, body(t, resp)
	}
	return at, page
}

func TestDesktopBinary(t *testing.T) {
	bin := buildBinary(t, "prod")

	t.Run("refuses to start without what desktop mode needs", func(t *testing.T) {
		for name, c := range map[string]struct {
			extra []string
			want  string
		}{
			"no launch token":        {[]string{"SCRIOBH_LAUNCH_TOKEN="}, "SCRIOBH_LAUNCH_TOKEN"},
			"a short launch token":   {[]string{"SCRIOBH_LAUNCH_TOKEN=" + dtToken[:16]}, "SCRIOBH_LAUNCH_TOKEN"},
			"every interface":        {[]string{"SCRIOBH_LISTEN=0.0.0.0:0"}, "SCRIOBH_LISTEN"},
			"a base URL":             {[]string{"SCRIOBH_BASE_URL=https://writing.example.org"}, "SCRIOBH_BASE_URL"},
			"the development issuer": {[]string{"SCRIOBH_OIDC_STUB=1"}, "SCRIOBH_OIDC_STUB"},
		} {
			home := t.TempDir()
			cmd := exec.Command(bin)
			cmd.Env = desktopEnv(home, dtToken, c.extra...)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(string(out), c.want) {
				t.Errorf("%s: err=%v out=%s; want exit 1 naming %s", name, err, out, c.want)
			}
			if strings.Contains(string(out), dtToken[:16]) || strings.Contains(string(out), dtKey) {
				t.Errorf("%s: the refusal repeats a secret: %s", name, out)
			}
			if strings.Contains(string(out), "LISTEN http") {
				t.Errorf("%s: it listened", name)
			}
			if _, err := os.Stat(desktopDataDir(home)); err == nil {
				t.Errorf("%s: a data directory was made by a start that was refused", name)
			}
		}
	})

	home := t.TempDir()
	dir := desktopDataDir(home)
	p := startDesktop(t, bin, desktopEnv(home, dtToken))

	t.Run("listens on loopback and keeps its data in the system's place", func(t *testing.T) {
		if !regexp.MustCompile(`^http://127\.0\.0\.1:[1-9][0-9]*$`).MatchString(p.base) {
			t.Fatalf("LISTEN %q", p.base)
		}
		resp, err := http.Get(p.base + "/healthz")
		if err != nil || !strings.Contains(body(t, resp), `"status":"ok"`) {
			t.Fatalf("/healthz: %v", err)
		}
		st, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("the data directory is not at %s: %v", dir, err)
		}
		if runtime.GOOS != "windows" {
			if st.Mode().Perm() != 0o700 {
				t.Errorf("the data directory's mode is %v, want 0700", st.Mode().Perm())
			}
			for file, want := range map[string]os.FileMode{"scriobh.db": 0o600, filepath.Join("desktop", "issuer-key.json"): 0o600, "desktop": 0o700} {
				fst, err := os.Stat(filepath.Join(dir, file))
				if err != nil || fst.Mode().Perm()&0o077 != 0 {
					t.Errorf("%s: %v mode %v, want it private (%v)", file, err, fst.Mode().Perm(), want)
				}
			}
		}
		if _, err := os.Stat(filepath.Join(dir, "secret.key")); err == nil {
			t.Error("a key file was written although the key came from the environment")
		}
	})

	t.Run("a second instance says ALREADY_RUNNING and exits 75", func(t *testing.T) {
		cmd := exec.Command(bin)
		cmd.Env = desktopEnv(home, strings.Repeat("ab", 32))
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 75 {
			t.Fatalf("second instance: %v, want exit status 75\n%s", err, stderr.String())
		}
		if stdout.String() != "ALREADY_RUNNING\n" {
			t.Errorf("second instance stdout %q, want the one line ALREADY_RUNNING", stdout.String())
		}
		// The first is untouched.
		if resp, err := http.Get(p.base + "/healthz"); err != nil || status(t, resp) != 200 {
			t.Errorf("the first instance after the second tried: %v", err)
		}
	})

	c := client()
	t.Run("the first run signs in through the embedded issuer", func(t *testing.T) {
		at, page := desktopFirstRun(t, c, p.base, dtToken, "Peig Sayers")
		if at != "/" || !strings.Contains(page, `class="signed-in`) || !strings.Contains(page, "Peig Sayers") {
			t.Fatalf("the first run ended at %s:\n%.400s\nlogs:\n%s", at, page, p.logs)
		}
		resp, err := c.Get(p.base + "/api/v1/desktop/info")
		if err != nil {
			t.Fatal(err)
		}
		var info struct {
			DataDir   string `json:"data_dir"`
			Mode      string `json:"mode"`
			Outbound  int64  `json:"outbound_connections"`
			Elsewhere *int64 `json:"outbound_elsewhere"`
		}
		raw := body(t, resp)
		if err := json.Unmarshal([]byte(raw), &info); err != nil {
			t.Fatalf("desktop.info: %s", raw)
		}
		want, _ := filepath.EvalSymlinks(dir)
		got, _ := filepath.EvalSymlinks(info.DataDir)
		if got != want || info.Mode != "embedded" || !filepath.IsAbs(info.DataDir) {
			t.Errorf("desktop.info: %s, want the data directory %s", raw, dir)
		}
		// The sign-in's own requests to the issuer were counted, and nothing went anywhere else.
		if info.Outbound == 0 || info.Elsewhere == nil || *info.Elsewhere != 0 {
			t.Errorf("outbound connections: %s; want some to this computer (the sign-in) and none elsewhere", raw)
		}
		// A stranger with no launch cookie cannot sign in, and the token does not work twice.
		stranger := client()
		resp, _ = stranger.Get(p.base + "/auth/login")
		if status(t, resp) != http.StatusForbidden {
			t.Error("a client without the launch cookie reached the sign-in")
		}
		resp, _ = stranger.Get(p.base + "/desktop/start?t=" + dtToken)
		if status(t, resp) != http.StatusNotFound {
			t.Error("the launch token worked twice")
		}
		// The production build has no development issuer under any name.
		for _, path := range []string{"/dev-idp/.well-known/openid-configuration", "/dev-idp/authorize"} {
			if resp, _ := c.Get(p.base + path); status(t, resp) != http.StatusNotFound {
				t.Errorf("%s exists in the production build", path)
			}
		}
	})

	t.Run("no secret is in the arguments, the log or the data directory", func(t *testing.T) {
		if ps, err := exec.LookPath("ps"); err == nil {
			out, err := exec.Command(ps, "-o", "args=", "-p", strconv.Itoa(p.cmd.Process.Pid)).Output()
			if err != nil {
				t.Logf("ps: %v", err)
			} else if strings.Contains(string(out), dtToken) || strings.Contains(string(out), dtKey) {
				t.Errorf("a secret is in the process arguments: %s", out)
			}
		}
		if len(p.cmd.Args) != 1 {
			t.Errorf("the sidecar is started with arguments: %v", p.cmd.Args)
		}
	})

	t.Run("SIGTERM is a clean exit with the log checkpointed", func(t *testing.T) {
		if code := p.signal(t, syscall.SIGTERM); code != 0 {
			t.Fatalf("exit status %d after SIGTERM, want 0; logs:\n%s", code, p.logs)
		}
		logs := p.logs.String()
		if !strings.Contains(logs, `"msg":"stopped"`) {
			t.Error("no 'stopped' line: the shutdown was not the graceful one")
		}
		if st, err := os.Stat(filepath.Join(dir, "scriobh.db-wal")); err == nil && st.Size() != 0 {
			t.Errorf("the write-ahead log holds %d bytes after a clean exit: it was not checkpointed", st.Size())
		}
		for name, secret := range map[string]string{"the launch token": dtToken, "the secret key": dtKey} {
			if strings.Contains(logs, secret) || strings.Contains(p.stdout.String(), secret) {
				t.Errorf("%s is in the output", name)
			}
			_ = filepath.WalkDir(dir, func(path string, e os.DirEntry, err error) error {
				if err != nil || e.IsDir() {
					return nil
				}
				if b, rerr := os.ReadFile(path); rerr == nil && bytes.Contains(b, []byte(secret)) {
					t.Errorf("%s is in %s", name, path)
				}
				return nil
			})
		}
		if strings.Contains(logs, "start?") {
			t.Error("the start address's query is in the log")
		}
	})

	t.Run("the next launch is the same person, and SIGINT is clean too", func(t *testing.T) {
		token2 := strings.Repeat("0f", 32)
		p2 := startDesktop(t, bin, desktopEnv(home, token2))
		c2 := client()
		at, page := desktopFirstRun(t, c2, p2.base, token2, "Someone Else")
		if at != "/" || !strings.Contains(page, "Peig Sayers") || strings.Contains(page, "Someone Else") {
			t.Fatalf("the second launch ended at %s, not signed in as the profile:\n%.300s", at, page)
		}
		// The first launch's token is nothing to this process.
		if resp, _ := client().Get(p2.base + "/desktop/start?t=" + dtToken); status(t, resp) != http.StatusNotFound {
			t.Error("the last launch's token was accepted")
		}
		if code := p2.signal(t, syscall.SIGINT); code != 0 {
			t.Errorf("exit status %d after SIGINT, want 0", code)
		}
	})

	t.Run("the recovery command", func(t *testing.T) {
		run := func(args ...string) (string, int) {
			cmd := exec.Command(bin, args...)
			cmd.Env = desktopEnv(home, "", "SCRIOBH_LAUNCH_TOKEN=")
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return string(out), ee.ExitCode()
			}
			return string(out), 0
		}
		if out, code := run("desktop", "use-local-profile"); code != 0 || !strings.Contains(out, "already signs in through its embedded issuer") {
			t.Errorf("on an embedded instance: %d %s", code, out)
		}
		if out, code := run("desktop"); code != 2 || !strings.Contains(out, "use-local-profile") {
			t.Errorf("with no subcommand: %d %s", code, out)
		}
		// While the application runs, it is refused: it must not change the database under a running process.
		p3 := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0e", 32)))
		if out, code := run("desktop", "use-local-profile"); code != 1 || !strings.Contains(out, "stop the application") {
			t.Errorf("while the application runs: %d %s", code, out)
		}
		p3.signal(t, syscall.SIGTERM)
	})
}

// TestDesktopStopsWhenTheShellIsGone: the shell holds the server's standard input as a pipe; when it closes (the
// shell quit, crashed or was killed) the server stops as it does on SIGTERM. Standard input that was never a pipe
// (every other test here starts the binary with none) does not stop it.
func TestDesktopStopsWhenTheShellIsGone(t *testing.T) {
	bin := buildBinary(t, "prod")
	home := t.TempDir()
	cmd := exec.Command(bin)
	cmd.Env = desktopEnv(home, dtToken)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := cmd.StdoutPipe()
	logs := &syncBuf{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	listening := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "LISTEN "); ok {
				listening <- rest
			}
		}
		done <- cmd.Wait()
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var base string
	select {
	case base = <-listening:
	case <-time.After(30 * time.Second):
		t.Fatalf("no LISTEN line:\n%s", logs)
	}
	// While the pipe is open the server serves, however long nothing is written to it.
	if resp, err := http.Get(base + "/healthz"); err != nil || status(t, resp) != 200 {
		t.Fatalf("/healthz with the pipe open: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("the server stopped with its standard input still open: %v\n%s", err, logs)
	default:
	}
	stdin.Close() // the shell is gone
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exit after standard input closed: %v, want status 0\n%s", err, logs)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the server outlived its shell\n%s", logs)
	}
	if l := logs.String(); !strings.Contains(l, "standard input closed") || !strings.Contains(l, `"msg":"stopped"`) {
		t.Errorf("the stop was not the graceful one, or does not say why:\n%s", l)
	}
	// The data directory is free again: the next launch starts.
	p := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0a", 32)))
	if code := p.signal(t, syscall.SIGTERM); code != 0 {
		t.Errorf("the next launch: exit %d", code)
	}
}

// TestDesktopKeepsItsPort: the port bound last time is bound again, so the browser's per-origin storage (unsaved
// drafts) is still there; when it is taken a free one is used, said in the log, and remembered.
func TestDesktopKeepsItsPort(t *testing.T) {
	bin := buildBinary(t, "prod")
	home := t.TempDir()
	portFile := filepath.Join(desktopDataDir(home), "desktop", "port")
	p1 := startDesktop(t, bin, desktopEnv(home, dtToken))
	first := p1.base
	if raw, err := os.ReadFile(portFile); err != nil || "http://127.0.0.1:"+strings.TrimSpace(string(raw)) != first {
		t.Fatalf("the port file holds %q (%v), LISTEN %s", raw, err, first)
	}
	p1.signal(t, syscall.SIGTERM)
	for i := 0; i < 3; i++ {
		p := startDesktop(t, bin, desktopEnv(home, strings.Repeat("1"+strconv.Itoa(i), 32)))
		if p.base != first {
			// Another program may take the port between a stop and a start (the tests of other packages run beside
			// this one). That is the documented fallback, and the log must say so; anything else is a failure.
			if !strings.Contains(p.logs.String(), "the port used last time is in use") {
				t.Fatalf("start %d listens on %s, want the last port (%s), and the log does not say the port was taken", i+2, p.base, first)
			}
			t.Logf("port %s was taken by another program; the server moved to %s and said so", first, p.base)
			first = p.base
		}
		p.signal(t, syscall.SIGTERM)
	}
	// Taken by something else: another port, a warning, and the new one remembered.
	busy, err := net.Listen("tcp", strings.TrimPrefix(first, "http://"))
	if err != nil {
		t.Skipf("could not take the port: %v", err)
	}
	p2 := startDesktop(t, bin, desktopEnv(home, strings.Repeat("2a", 32)))
	if p2.base == first || !strings.HasPrefix(p2.base, "http://127.0.0.1:") {
		t.Fatalf("with the port taken, LISTEN %s", p2.base)
	}
	if !strings.Contains(p2.logs.String(), "the port used last time is in use") {
		t.Errorf("no warning about the port:\n%s", p2.logs)
	}
	p2.signal(t, syscall.SIGTERM)
	busy.Close()
	p3 := startDesktop(t, bin, desktopEnv(home, strings.Repeat("2b", 32)))
	if p3.base != p2.base && !strings.Contains(p3.logs.String(), "the port used last time is in use") {
		t.Errorf("after a change of port the next start listens on %s, want the new one (%s)", p3.base, p2.base)
	}
	p3.signal(t, syscall.SIGTERM)
	// A port file that is not a port is ignored, and an explicit port is never overridden by it.
	for _, junk := range []string{"80\n", "notaport", "99999999999999999999", ""} {
		if err := os.WriteFile(portFile, []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		p := startDesktop(t, bin, desktopEnv(home, strings.Repeat("2c", 32)))
		if !strings.HasPrefix(p.base, "http://127.0.0.1:") || p.base == "http://127.0.0.1:80" {
			t.Errorf("port file %q: LISTEN %s", junk, p.base)
		}
		p.signal(t, syscall.SIGTERM)
	}
}

// TestDesktopExternalProviderFixedPort: an instance that signs in through an external provider binds the port stored
// with the provider instead of the "any port" the shell passes, does not mount the embedded issuer, and falls back to
// a free port, saying so, when the fixed one is taken.
func TestDesktopExternalProviderFixedPort(t *testing.T) {
	bin := buildBinary(t, "prod")
	home := t.TempDir()
	first := startDesktop(t, bin, desktopEnv(home, dtToken))
	if code := first.signal(t, syscall.SIGTERM); code != 0 {
		t.Fatalf("first start: exit %d", code)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	d, err := db.Open(filepath.Join(desktopDataDir(home), "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	state := `{"mode":"external","external":{"issuer":"https://idp.example.org","client_id":"scriobh","port":` + strconv.Itoa(port) + `}}`
	err = d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('desktop.idp', ?, ?)`, state, db.Now())
		return err
	})
	d.Close()
	if err != nil {
		t.Fatal(err)
	}

	p := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0d", 32)))
	if p.base != "http://127.0.0.1:"+strconv.Itoa(port) {
		if strings.Contains(p.logs.String(), "the fixed port of the external provider's settings is in use") {
			t.Skipf("port %d was taken by another program before the server could bind it; the fallback was taken and said", port)
		}
		t.Fatalf("LISTEN %s, want the provider's fixed port %d", p.base, port)
	}
	for _, path := range []string{"/desktop-idp/jwks", "/desktop-idp/.well-known/openid-configuration", "/desktop/profile"} {
		if resp, _ := http.Get(p.base + path); status(t, resp) != http.StatusNotFound {
			t.Errorf("%s answers on an instance that signs in through an external provider", path)
		}
	}
	// The sign-in goes to the provider, with this port's callback as the redirect address; nothing is asked of the
	// provider to find that out here, because it is not there: the 502 page says so and names it.
	c := client()
	resp, err := c.Get(p.base + "/desktop/start?t=" + strings.Repeat("0d", 32))
	if page := body(t, resp); err != nil || !strings.Contains(page, `id="dx-address"`) || !strings.Contains(page, p.base) || strings.Contains(page, `class="signed-in`) {
		t.Errorf("the start address in external mode must say where to sign in: %v", err)
	}
	if code := p.signal(t, syscall.SIGTERM); code != 0 {
		t.Errorf("exit %d", code)
	}

	// The fixed port is taken: a free one is used, and the log says why sign-in may be refused.
	busy, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Skipf("could not take port %d back: %v", port, err)
	}
	defer busy.Close()
	p2 := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0c", 32)))
	if p2.base == "http://127.0.0.1:"+strconv.Itoa(port) || !strings.HasPrefix(p2.base, "http://127.0.0.1:") {
		t.Errorf("with the fixed port taken, LISTEN %s", p2.base)
	}
	if !strings.Contains(p2.logs.String(), "the fixed port of the external provider's settings is in use") {
		t.Errorf("no warning about the fixed port:\n%s", p2.logs)
	}
	p2.signal(t, syscall.SIGTERM)

	// And the recovery command puts the instance back on its embedded issuer at the next start.
	cmd := exec.Command(bin, "desktop", "use-local-profile")
	cmd.Env = desktopEnv(home, "", "SCRIOBH_LAUNCH_TOKEN=")
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "at its next start") {
		t.Fatalf("recovery: %v %s", err, out)
	}
	p3 := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0b", 32)))
	if resp, _ := http.Get(p3.base + "/desktop-idp/jwks"); status(t, resp) != http.StatusOK {
		t.Errorf("after the recovery the embedded issuer is not mounted\n%s", p3.logs)
	}
	p3.signal(t, syscall.SIGTERM)
}

// TestWithoutDesktopModeASecondInstanceIsAsBefore: exit status 75 and ALREADY_RUNNING belong to desktop mode.
func TestWithoutDesktopModeASecondInstanceIsAsBefore(t *testing.T) {
	bin := buildBinary(t, "")
	data := t.TempDir()
	start(t, bin, data)
	cmd := exec.Command(bin)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "SCRIOBH_LISTEN=127.0.0.1:0", "SCRIOBH_DATA_DIR=" + data, "SCRIOBH_OIDC_STUB=1"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "already using") {
		t.Errorf("a second instance outside desktop mode: %v stdout %q stderr %q; want exit 1 and the old message", err, stdout.String(), stderr.String())
	}
	_ = io.Discard
}

// TestDesktopAdoptAdminCommand: a data directory restored from a server instance. The production binary refuses the
// local profile (no account of its own), `scriobh desktop use-local-profile --adopt-admin` binds it to the named
// administrator with the application stopped, and the next start signs the window in as that account.
func TestDesktopAdoptAdminCommand(t *testing.T) {
	bin := buildBinary(t, "prod")
	home := t.TempDir()
	// A start makes the database (the migrations); it is then given the accounts of another server.
	first := startDesktop(t, bin, desktopEnv(home, dtToken))
	if code := first.signal(t, syscall.SIGTERM); code != 0 {
		t.Fatalf("first start: exit %d", code)
	}
	d, err := db.Open(filepath.Join(desktopDataDir(home), "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	now := db.Now()
	for _, u := range [][]string{{"usr_old", "admin", "old@example.org", "s1"}, {"usr_w", "writer", "w@example.org", "s2"}} {
		if _, err := d.Writer().Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, avatar_color, role, created_at, updated_at, last_seen_at)
			VALUES (?,'https://idp.example.org',?,?,?,'#000000',?,?,?,?)`, u[0], u[3], u[2], "Old "+u[0], u[1], now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	d.Close()

	p := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0a", 32)))
	c := client()
	at, page := desktopFirstRun(t, c, p.base, strings.Repeat("0a", 32), "Me")
	if at == "/" || !strings.Contains(page, "scriobh desktop use-local-profile --adopt-admin") || strings.Contains(page, "scriobh-writers") {
		t.Fatalf("the refusal of a restored server database ended at %s:\n%.600s", at, page)
	}
	run := func(args ...string) (string, int) {
		cmd := exec.Command(bin, args...)
		cmd.Env = desktopEnv(home, "", "SCRIOBH_LAUNCH_TOKEN=")
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode()
		}
		return string(out), 0
	}
	// With the application running the command is refused.
	if out, code := run("desktop", "use-local-profile", "--adopt-admin", "old@example.org"); code != 1 || !strings.Contains(out, "stop the application") {
		t.Errorf("while the application runs: %d %s", code, out)
	}
	p.signal(t, syscall.SIGTERM)
	if out, code := run("desktop", "use-local-profile"); code != 0 || !strings.Contains(out, "--adopt-admin") {
		t.Errorf("plain use-local-profile on a restored server database: %d %s", code, out)
	}
	for _, args := range [][]string{{"desktop", "use-local-profile", "--adopt-admin"}, {"desktop", "use-local-profile", "--adopt-admin", ""}, {"desktop", "use-local-profile", "--adopt-admin", "a", "b"}, {"desktop", "use-local-profile", "--adopt"}} {
		if out, code := run(args...); code != 2 || !strings.Contains(out, "usage") {
			t.Errorf("%v: %d %s", args, code, out)
		}
	}
	for _, who := range []string{"w@example.org", "usr_nobody", "example.org"} {
		if out, code := run("desktop", "use-local-profile", "--adopt-admin", who); code != 1 {
			t.Errorf("--adopt-admin %s: %d %s", who, code, out)
		}
	}
	if out, code := run("desktop", "use-local-profile", "--adopt-admin", "Old@Example.org"); code != 0 || !strings.Contains(out, "usr_old") {
		t.Fatalf("--adopt-admin: %d %s", code, out)
	}
	if out, code := run("desktop", "use-local-profile", "--adopt-admin", "usr_old"); code != 1 {
		t.Errorf("a second adoption: %d %s", code, out)
	}

	p2 := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0b", 32)))
	c2 := client()
	at, page = desktopFirstRun(t, c2, p2.base, strings.Repeat("0b", 32), "Me")
	if at != "/" || !strings.Contains(page, `class="signed-in`) {
		t.Fatalf("after the adoption the window ended at %s:\n%.400s", at, page)
	}
	if code := p2.signal(t, syscall.SIGTERM); code != 0 {
		t.Errorf("exit status %d", code)
	}
}

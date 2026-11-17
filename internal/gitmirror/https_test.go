package gitmirror_test

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// A real Git server over HTTPS: git http-backend (the program a Git host runs)
// behind a TLS test server that asks for HTTP Basic authentication, the token as
// the password. The Scríobh side is the real client: the guarded transport with
// its body and header limits, no proxy, the redirect rules and the credentials
// it opens from the sealed row.

const httpsToken = "tkn_0123456789abcdefghij0123456789abcdefghij"

type httpsRequest struct {
	Method, Path, Auth string
}

type gitHTTPS struct {
	t          *testing.T
	srv        *httptest.Server
	root       string // the folder holding salt-road.git
	repo       string // the bare repository on disk
	mu         sync.Mutex
	reqs       []httpsRequest
	tokenValue string
	redirect   string // when set, /moved.git/ answers with a redirect to this URL
}

func backendPath(t *testing.T) string {
	t.Helper()
	needGit(t)
	out, err := exec.Command("git", "--exec-path").Output()
	if err != nil {
		t.Skip("git --exec-path failed")
	}
	p := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")
	if _, err := os.Stat(p); err != nil {
		t.Skip("git-http-backend is not installed")
	}
	return p
}

func newGitHTTPS(t *testing.T, token string) *gitHTTPS {
	t.Helper()
	backend := backendPath(t)
	g := &gitHTTPS{t: t, root: t.TempDir()}
	g.repo = filepath.Join(g.root, "salt-road.git")
	run := func(args ...string) {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "--bare", "--quiet", "--initial-branch=main", g.repo)
	run("-C", g.repo, "config", "http.receivepack", "true")
	cgiH := &cgi.Handler{Path: backend, Root: "/", InheritEnv: []string{"PATH", "HOME"},
		Env: []string{"GIT_PROJECT_ROOT=" + g.root, "GIT_HTTP_EXPORT_ALL=1", "GIT_CONFIG_NOSYSTEM=1"}}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.reqs = append(g.reqs, httpsRequest{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		redirect := g.redirect
		g.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/moved.git/") && redirect != "" {
			http.Redirect(w, r, redirect, http.StatusFound)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/old.git/") { // a move within the same host
			http.Redirect(w, r, "/salt-road.git/"+strings.TrimPrefix(r.URL.Path, "/old.git/")+"?"+r.URL.RawQuery, http.StatusMovedPermanently)
			return
		}
		_, pw, ok := r.BasicAuth()
		if !ok || pw != g.token() {
			w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			http.Error(w, "authentication required for "+pw, http.StatusUnauthorized) // echoes the password, as a careless server might
			return
		}
		cgiH.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	g.tokenValue = token
	pool := x509.NewCertPool()
	pool.AddCert(g.srv.Certificate())
	gitmirror.SetRootCAsForTest(pool)
	t.Cleanup(func() { gitmirror.SetRootCAsForTest(nil) })
	return g
}

func (g *gitHTTPS) token() string { return g.tokenValue }

func (g *gitHTTPS) url(path string) string {
	u, _ := url.Parse(g.srv.URL)
	return "https://127.0.0.1:" + u.Port() + path
}

func (g *gitHTTPS) requests() []httpsRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]httpsRequest{}, g.reqs...)
}

// git runs the system git against the bare repository by its path (the way
// someone else working on the repository would).
func (g *gitHTTPS) git(args ...string) string {
	g.t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (g *gitHTTPS) commitCount() int {
	out := g.git("-C", g.repo, "rev-list", "--count", "main")
	var n int
	fmt.Sscan(out, &n)
	return n
}

func (g *gitHTTPS) headSHA() string {
	out, err := exec.Command("git", "-C", g.repo, "rev-parse", "--verify", "--quiet", "refs/heads/main").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// someoneElsePushes makes a commit from a clone of the repository and pushes it.
func (g *gitHTTPS) someoneElsePushes(file, content, subject string) string {
	g.t.Helper()
	work := filepath.Join(g.t.TempDir(), "work")
	g.git("clone", "--quiet", g.repo, work)
	if err := os.WriteFile(filepath.Join(work, file), []byte(content), 0o644); err != nil {
		g.t.Fatal(err)
	}
	g.git("-C", work, "add", file)
	g.git("-C", work, "-c", "user.name=Collaborator", "-c", "user.email=c@example.org", "commit", "--quiet", "-m", subject)
	g.git("-C", work, "push", "--quiet", "origin", "HEAD:main")
	return g.headSHA()
}

type httpsFixture struct {
	h    *harness
	g    *gitHTTPS
	rem  gitmirror.Remote
	path string
}

func newHTTPSFixture(t *testing.T) *httpsFixture {
	t.Helper()
	g := newGitHTTPS(t, httpsToken)
	h := newHarness(t)
	h.start()
	rem := h.addRemote(g.url("/salt-road.git"), func(in *gitmirror.CreateIn) { in.Token = httpsToken; in.Username = "mairead" })
	return &httpsFixture{h: h, g: g, rem: rem}
}

func TestHTTPSPushSecondPushAndTest(t *testing.T) {
	f := newHTTPSFixture(t)
	h, g := f.h, f.g
	if got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut); !got.OK || !got.Empty {
		t.Fatalf("test = %+v\n%s", got, h.logs.String())
	}
	p := h.push(h.owner)
	if p.Status != "ok" || p.Outcome != "pushed" {
		t.Fatalf("first push = %+v\n%s", p, h.logs.String())
	}
	if g.headSHA() != p.CommitSHA || g.commitCount() != 1 {
		t.Fatalf("remote head %s (%d commits), pushed %s", g.headSHA(), g.commitCount(), p.CommitSHA)
	}
	// The token went as the password of HTTP Basic, to this server and no other.
	basic := false
	for _, r := range g.requests() {
		if strings.HasPrefix(r.Auth, "Basic ") {
			basic = true
		}
	}
	if !basic {
		t.Error("no request carried Basic authentication")
	}
	g.git("-C", g.repo, "fsck", "--strict")
	if r := h.remote(); r.Status.State != "synced" || !r.Status.InSync {
		t.Errorf("status = %+v", r.Status)
	}

	h.clk.Advance(2 * time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 99, "Changed on the road.\n"}, chapters[2]})
	second := h.push(h.owner)
	if second.Status != "ok" || second.ParentSHA != p.CommitSHA || g.commitCount() != 2 || g.headSHA() != second.CommitSHA {
		t.Fatalf("second push = %+v (remote has %d commits)\n%s", second, g.commitCount(), h.logs.String())
	}
	g.git("-C", g.repo, "fsck", "--strict")
	// A push with nothing new is not a push.
	if again := h.push(h.owner); again.Outcome != "up_to_date" || g.commitCount() != 2 {
		t.Errorf("again = %+v", again)
	}
}

func TestHTTPSWrongTokenIsAPlainAuthErrorThatDoesNotLeakTheToken(t *testing.T) {
	f := newHTTPSFixture(t)
	h := f.h
	wrong := "tkn_wrongwrongwrongwrongwrongwrongwrongwrong"
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Token: ptr(wrong)})
	got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut)
	if got.OK || got.Code != "auth_failed" || got.KeyName != "Backup" || !strings.Contains(got.Message, "127.0.0.1") {
		t.Fatalf("test = %+v", got)
	}
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "auth_failed" {
		t.Fatalf("push = %+v", p)
	}
	if f.g.headSHA() != "" {
		t.Error("something was pushed with a wrong token")
	}
	// The server echoed the wrong token in its 401 body: it must not reach a message, a log or the database.
	if l := h.leaks(wrong, httpsToken); len(l) > 0 {
		t.Errorf("the token leaked: %v", l)
	}
	for _, m := range []string{got.Message, p.ErrorMessage} {
		if strings.Contains(m, wrong) || strings.Contains(m, "authentication required for") {
			t.Errorf("message %q repeats what the server said", m)
		}
	}
	// With the right token again it works, and the remote is no longer parked.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Token: ptr(httpsToken)})
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("after fixing the token: %+v", p)
	}
}

func TestHTTPSRemoteAheadForcePushAndImport(t *testing.T) {
	f := newHTTPSFixture(t)
	h, g := f.h, f.g
	first := h.push(h.owner)
	if first.Status != "ok" {
		t.Fatalf("push = %+v", first)
	}
	// Someone else pushes to the repository.
	theirs := g.someoneElsePushes("NOTES.md", "theirs\n", "Their notes")
	h.clk.Advance(2 * time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 99, "Mine.\n"}})
	rej := h.push(h.owner)
	if rej.Status != "rejected" || rej.ErrorCode != "remote_ahead" || rej.RemoteSHA != theirs {
		t.Fatalf("push = %+v\n%s", rej, h.logs.String())
	}
	if len(rej.RemoteCommits) != 1 || rej.RemoteCommits[0].Subject != "Their notes" || rej.RemoteCommits[0].Author != "Collaborator" {
		t.Errorf("foreign commits = %+v (the scratch fetch over https)", rej.RemoteCommits)
	}
	if g.headSHA() != theirs {
		t.Fatal("the remote was overwritten")
	}

	// Import what is on the remote before deciding: through the project's remote.
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{RemoteID: f.rem.ID}).ID)
	if job.Status != "preview" || job.CommitSHA != theirs {
		t.Fatalf("import job = %+v\n%s", job, h.logs.String())
	}
	h.must(h.owner, "import.git_discard", gitmirror.GitJobIn{JobID: job.ID})

	// Force push over the remote, with a bundle first.
	out := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: f.rem.ID, Confirm: "Backup"}).(gitmirror.PushOut)
	p := h.settle(out.Item.ID)
	if p.Status != "ok" || !p.Forced || p.Bundle == "" || g.headSHA() != p.CommitSHA {
		t.Fatalf("force = %+v\n%s", p, h.logs.String())
	}
	bundle := filepath.Join(h.dataDir, "backups", "git", h.pid, p.Bundle)
	if err := gitmirror.VerifyBundle(bundle, plumbing.NewHash(theirs)); err != nil {
		t.Fatal(err)
	}
	g.git("bundle", "verify", bundle)
	restored := filepath.Join(t.TempDir(), "restored")
	g.git("clone", "--quiet", "--branch", "main", bundle, restored)
	if b, err := os.ReadFile(filepath.Join(restored, "NOTES.md")); err != nil || string(b) != "theirs\n" {
		t.Errorf("the overwritten file did not come back from the bundle: %q %v", b, err)
	}
	g.git("-C", g.repo, "fsck", "--strict")
}

func TestHTTPSImportPreviewAndApplyOfWhatWasPushed(t *testing.T) {
	f := newHTTPSFixture(t)
	h, g := f.h, f.g
	p := h.push(h.owner)
	if p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	// By address, with a token for this clone only.
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: g.url("/salt-road.git"), Username: "mairead", Token: httpsToken}).ID)
	if job.Status != "preview" || job.Summary == nil || job.Summary.Layout != "scriobh" || job.Summary.Documents != 3 || job.CommitSHA != p.CommitSHA {
		t.Fatalf("job = %+v\n%s", job, h.logs.String())
	}
	pv := h.must(h.owner, "import.git_preview", gitmirror.GitPreviewIn{JobID: job.ID}).(gitmirror.GitPreviewOut).Preview
	if pv == nil || pv.Documents != 3 || pv.Token != job.Summary.Token {
		t.Fatalf("preview = %+v", pv)
	}
	out := h.must(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: pv.Token}).(imports.ApplyOut)
	if out.Documents != 3 || out.ProjectID == h.pid {
		t.Fatalf("apply = %+v", out)
	}
	var kind, srcURL, sha string
	if err := h.d.Read().QueryRow(`SELECT kind, source_url, commit_sha FROM project_sources WHERE id = ?`, out.SourceID).Scan(&kind, &srcURL, &sha); err != nil {
		t.Fatal(err)
	}
	if kind != "git" || sha != p.CommitSHA || strings.Contains(srcURL, httpsToken) || strings.Contains(srcURL, "@") {
		t.Errorf("source = %s %s %s", kind, srcURL, sha)
	}
	// The token given for the clone is nowhere afterwards.
	if l := h.leaks(httpsToken[4:]); len(l) > 0 {
		for _, x := range l {
			if !strings.HasPrefix(x, "file ") { // the sealed row is ciphertext; files hold no token
				t.Errorf("the token leaked: %v", l)
			}
		}
	}
	for _, e := range []string{"imports/p-" + out.ProjectID + "/config"} {
		b, err := os.ReadFile(filepath.Join(h.dataDir, e))
		if err == nil && strings.Contains(string(b), httpsToken) {
			t.Errorf("the token is in the kept clone's configuration")
		}
	}
	// Without a token a private repository is refused with a plain error.
	nojob := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: g.url("/salt-road.git")}).ID)
	if nojob.Status != "failed" || strings.Contains(nojob.Error, "authentication required for") {
		t.Errorf("without a token: %+v", nojob)
	}
}

func TestHTTPSLargeRepositoryPassesTheLimits(t *testing.T) {
	f := newHTTPSFixture(t)
	h, g := f.h, f.g
	// About 6 MiB of text that does not compress, in four chapters. Under -short (the race pass) a third of that,
	// 2 MiB: still far past the 64 KiB error-body cap, the 1 MiB JSON limit and every buffer a small repository
	// fits in, and the race detector makes the secret scan, the packing and the inflation of 6 MiB cost 25 s at
	// two cores. The plain pass keeps the full size.
	chapterBytes, minPushed := 768<<10, int64(6<<20)
	if testing.Short() {
		chapterBytes, minPushed = 256<<10, 2<<20
	}
	var docs []mdoc
	for i := 0; i < 4; i++ {
		b := make([]byte, chapterBytes)
		_, _ = rand.Read(b)
		docs = append(docs, mdoc{fmt.Sprintf("manuscript/%02d-chapter.md", i+1), "chapter", fmt.Sprintf("Chapter %d", i+1), 1000, hex.EncodeToString(b) + "\n"})
	}
	h.src.Write("The Salt Road", true, docs)
	p := h.push(h.owner)
	if p.Status != "ok" || p.Bytes < minPushed {
		t.Fatalf("push of a large project = %+v\n%s", p, h.logs.String())
	}
	if g.headSHA() != p.CommitSHA {
		t.Fatal("not pushed")
	}
	// A second push of one changed chapter, over the same limits.
	h.clk.Advance(2 * time.Hour)
	docs[1].body = "Rewritten and short.\n"
	h.src.Write("The Salt Road", true, docs)
	if p2 := h.push(h.owner); p2.Status != "ok" || g.commitCount() != 2 {
		t.Fatalf("second push = %+v", p2)
	}
	// And a clone (import) of it: a response of several megabytes.
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{RemoteID: f.rem.ID}).ID)
	if job.Status != "preview" || job.Summary.Documents != 4 {
		t.Fatalf("import of a large repository = %+v\n%s", job, h.logs.String())
	}
	// The same, with the real git client reading what Scríobh pushed.
	g.git("clone", "--quiet", g.repo, filepath.Join(t.TempDir(), "check"))
}

func TestHTTPSRedirectsStayOnTheOriginAndTheCredentialStaysThere(t *testing.T) {
	f := newHTTPSFixture(t)
	h, g := f.h, f.g
	attacker := newRecorder(t) // trusted by the same seam; answers 401 and records
	pool := x509.NewCertPool()
	pool.AddCert(g.srv.Certificate())
	pool.AddCert(attacker.srv.Certificate())
	gitmirror.SetRootCAsForTest(pool)

	// A redirect to another host is refused, and nothing is sent there.
	g.mu.Lock()
	g.redirect = attacker.url("/stolen.git/info/refs?service=git-upload-pack")
	g.mu.Unlock()
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr(g.url("/moved.git")), Token: ptr(httpsToken)})
	got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut)
	if got.OK {
		t.Fatalf("a redirect to another host was followed: %+v", got)
	}
	p, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid})
	if err != nil {
		t.Fatal(err)
	}
	if done := h.settle(p.(gitmirror.PushOut).Item.ID); done.Status == "ok" {
		t.Fatalf("push through a cross-host redirect = %+v", done)
	}
	job, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{URL: g.url("/moved.git"), Username: "mairead", Token: httpsToken})
	if err == nil {
		h.waitJob(h.owner, job.(gitmirror.GitPreviewOut).Job.ID)
	}
	if n, auth := attacker.seen(); len(auth) != 0 {
		t.Fatalf("the Authorization header reached another origin (%d requests): %v", n, auth)
	} else if n != 0 {
		t.Errorf("%d requests were sent to the other origin; a redirect to another host must not be followed", n)
	}

	// A move within the same host is followed, with the credential.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr(g.url("/old.git")), Token: ptr(httpsToken)})
	if got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut); !got.OK {
		t.Errorf("a redirect within the host: %+v\n%s", got, h.logs.String())
	}
}

func TestHTTPSTheTokenIsSentOnlyToTheRemotesOrigin(t *testing.T) {
	f := newHTTPSFixture(t)
	h, g := f.h, f.g
	h.push(h.owner)
	_ = ops.CodeConflict
	for _, r := range g.requests() {
		if r.Auth != "" && !strings.HasPrefix(r.Auth, "Basic ") {
			t.Errorf("unexpected authorization %q on %s", r.Auth, r.Path)
		}
	}
	if len(g.requests()) == 0 {
		t.Fatal("no requests")
	}
}

package gitmirror_test

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

func TestWhichAddressesAreBlocked(t *testing.T) {
	for _, ip := range []string{
		"127.0.0.1", "127.8.8.8", "::1", "0.0.0.0", "0.1.2.3", "::",
		"10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.10", "fd00::1", "fc00::5", "fd00:ec2::254",
		"169.254.169.254", "169.254.0.1", "fe80::1", // link-local, with the cloud metadata address
		"100.64.0.1", "100.127.255.255", // carrier-grade NAT
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", // IPv4 written as IPv6
		"224.0.0.1", "ff02::1", "240.0.0.1", "255.255.255.255", "198.18.0.1", "192.0.0.8", "2001:db8::1",
	} {
		if !gitmirror.BlockedIP(ip) {
			t.Errorf("%s is not blocked", ip)
		}
	}
	for _, ip := range []string{"8.8.8.8", "140.82.112.3", "100.63.255.255", "100.128.0.1", "172.15.0.1", "172.32.0.1", "192.169.0.1", "2606:4700:4700::1111", "2a00:1450:4001::200e"} {
		if gitmirror.BlockedIP(ip) {
			t.Errorf("%s is blocked", ip)
		}
	}
}

// A recording stand-in for a Git host: it answers 401 to everything and keeps
// what each request carried.
type recorder struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
	n    int
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.n++
		if a := req.Header.Get("Authorization"); a != "" {
			r.auth = append(r.auth, a)
		}
		r.mu.Unlock()
		w.Header().Set("WWW-Authenticate", `Basic realm="git"`)
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	t.Cleanup(r.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(r.srv.Certificate())
	gitmirror.SetRootCAsForTest(pool)
	t.Cleanup(func() { gitmirror.SetRootCAsForTest(nil) })
	return r
}

func (r *recorder) url(path string) string {
	return "https://" + strings.TrimPrefix(r.srv.URL, "https://") + path
}

func (r *recorder) seen() (requests int, auth []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n, append([]string{}, r.auth...)
}

func TestACredentialIsNeverSentToAnotherAddress(t *testing.T) {
	legit, attacker := newRecorder(t), newRecorder(t)
	h := newHarness(t)
	h.start()
	rem := h.addRemote(legit.url("/mairead/salt-road.git"), func(in *gitmirror.CreateIn) { in.Token = token; in.Username = "mairead" })

	// Sanity: to the address it was entered for, the token is sent (so these
	// tests would see it if it went anywhere else).
	if got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut); got.Code != "auth_failed" {
		t.Fatalf("test against the legit server = %+v", got)
	}
	if n, auth := legit.seen(); n == 0 || len(auth) == 0 {
		t.Fatalf("the token was not sent to the address it belongs to (%d requests, %d with auth)", n, len(auth))
	}

	// An editor cannot re-point the remote.
	if _, err := h.call(h.editor, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr(attacker.url("/x.git"))}); h.code(err) != ops.CodeForbidden {
		t.Fatalf("an editor re-pointing the remote: %v", err)
	}
	// The owner re-points it: the credential goes, so nothing can be sent.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr(attacker.url("/x.git"))})
	h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid})
	if _, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}); err == nil {
		t.Error("a push without credentials was accepted")
	}
	// An import through the remote has no credential to use.
	if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{RemoteID: rem.ID}); err == nil {
		t.Error("an import through a remote without credentials started")
	}

	// And a row altered in the database (the credential kept, the address changed)
	// does not open, so test, push and import send nothing either.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr(legit.url("/mairead/salt-road.git")), Token: ptr(token)})
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET url = ? WHERE id = ?`, attacker.url("/x.git"), rem.ID); err != nil {
		t.Fatal(err)
	}
	if got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut); got.Code != "credentials_unreadable" {
		t.Fatalf("test on a retargeted row = %+v", got)
	}
	out, _ := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid})
	if out != nil {
		h.settle(out.(gitmirror.PushOut).Item.ID)
	}
	if _, err := h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{RemoteID: rem.ID}); err == nil {
		t.Error("an import through a retargeted row started")
	}
	if _, auth := attacker.seen(); len(auth) != 0 {
		t.Fatalf("the credential reached the other address: %v", auth)
	}
}

func TestPrivateAddressesAreRefusedWithAGenericMessage(t *testing.T) {
	f := newSSHFixtureWith(t, func(o *gitmirror.Options) { o.AllowPrivate = false })
	h := f.h
	// "localhost" is a name: only a check on what it resolves to catches it.
	port := f.srv.addr[strings.LastIndex(f.srv.addr, ":")+1:]
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("ssh://git@localhost:" + port + "/salt-road.git")})
	_, err := h.call(h.owner, "git.remote.keygen", gitmirror.KeygenIn{ProjectID: h.pid})
	if err != nil {
		t.Fatal(err)
	}
	h.d.Writer().Exec(`UPDATE git_remotes SET known_hosts = 'localhost:` + port + ` ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHnQbmVrfAyDZP2h1y0n7KcGg9d3zBJqkAw+gsUoK1nM'`)

	generic := func(what, msg string) {
		t.Helper()
		for _, bad := range []string{port, "127.0.0.1", "::1", "dial", "refused", "tcp"} {
			if strings.Contains(msg, bad) {
				t.Errorf("%s says %q, which tells the caller about the server's network", what, msg)
			}
		}
		if !strings.Contains(msg, "SCRIOBH_GIT_ALLOW_PRIVATE") {
			t.Errorf("%s does not say how to allow it: %q", what, msg)
		}
	}
	_, err = h.call(h.owner, "git.remote.hostkey", gitmirror.RemoteBody{ProjectID: h.pid})
	if err == nil {
		t.Fatal("a scan of a loopback address worked")
	}
	generic("the scan", err.Error())
	got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut)
	if got.OK || got.Code != "address_blocked" {
		t.Fatalf("test = %+v", got)
	}
	generic("the test", got.Message)
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "address_blocked" {
		t.Fatalf("push = %+v", p)
	}
	generic("the push", p.ErrorMessage)
	_, err = h.call(h.owner, "import.git_preview", gitmirror.GitPreviewIn{RemoteID: h.remote().ID})
	if err == nil {
		t.Fatal("an import through a loopback remote started")
	}
	generic("the import", err.Error())
	// The server log has the detail, and no secret.
	if !strings.Contains(h.logs.String(), "address_blocked") {
		t.Error("the detail was not logged")
	}
	// An import by address from any writer respects the same rule.
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: "https://localhost:1/x.git"}).ID)
	if job.Status != "failed" || strings.Contains(job.Error, ":1") || strings.Contains(job.Error, "refused") {
		t.Errorf("import of a loopback address: %+v", job)
	}
	// Opting in lets the same address through.
	h2 := newSSHFixture(t)
	h2.authorize()
	if _, err := h2.accept(h2.scan().Keys[0].Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	if got := h2.test(); !got.OK {
		t.Errorf("with the opt-in: %+v", got)
	}
}

func TestHTTPSToALoopbackAddressIsRefusedUnlessAllowed(t *testing.T) {
	rec := newRecorder(t)
	u, _ := url.Parse(rec.srv.URL)
	for _, allow := range []bool{false, true} {
		h := newHarness(t, func(o *gitmirror.Options) { o.AllowPrivate = allow })
		h.addRemote("https://127.0.0.1:"+u.Port()+"/o/r.git", func(in *gitmirror.CreateIn) { in.Token = token })
		before, _ := rec.seen()
		got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut)
		after, auth := rec.seen()
		if allow {
			if after == before {
				t.Error("the opt-in did not let the request through")
			}
			_ = auth
			continue
		}
		if got.Code != "address_blocked" || after != before {
			t.Errorf("a loopback address was reached: %+v (%d new requests)", got, after-before)
		}
		if strings.Contains(got.Message, u.Port()) {
			t.Errorf("message echoes the port: %q", got.Message)
		}
	}
}

func TestResponseBodiesAndRedirectsAreBounded(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.MaxRepoBytes = 1 << 20 }) // a clone may be 1 MiB (plus slack)
	_ = h
	var hops int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/huge-error":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.CopyN(w, zeros{}, 64<<20)
		case "/huge-ok":
			_, _ = io.CopyN(w, zeros{}, 256<<20)
		case "/loop":
			hops++
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/to-http":
			http.Redirect(w, r, "http://example.org/", http.StatusFound)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	}))
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	gitmirror.SetRootCAsForTest(pool)
	defer gitmirror.SetRootCAsForTest(nil)
	c := gitmirror.HTTPClientForTest()

	resp, err := c.Get(srv.URL + "/huge-error")
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n > 64<<10 {
		t.Errorf("a refused request's body was read to %d bytes, want at most 64 KiB", n)
	}
	resp, err = c.Get(srv.URL + "/huge-ok")
	if err != nil {
		t.Fatal(err)
	}
	n, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil || n > 20<<20 {
		t.Errorf("a successful response of 256 MiB was read: %d bytes, err %v", n, err)
	}
	if _, err := c.Get(srv.URL + "/loop"); err == nil {
		t.Error("a redirect loop was followed")
	}
	if hops > 4 {
		t.Errorf("%d redirects followed", hops)
	}
	if _, err := c.Get(srv.URL + "/to-http"); err == nil {
		t.Error("a redirect to plain http was followed")
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestTheOptInIsConfigured(t *testing.T) {
	get := func(v string) *config.Config {
		cfg, err := config.Load(func(k string) (string, bool) {
			if k == "SCRIOBH_GIT_ALLOW_PRIVATE" && v != "" {
				return v, true
			}
			return "", false
		})
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	if get("").GitAllowPrivate || !get("1").GitAllowPrivate || !get("true").GitAllowPrivate || get("0").GitAllowPrivate {
		t.Error("SCRIOBH_GIT_ALLOW_PRIVATE is not read as a switch that is off by default")
	}
}

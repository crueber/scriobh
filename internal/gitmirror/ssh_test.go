package gitmirror_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// An in-process SSH server that speaks just enough of what Git needs: public
// key authentication against one authorized key, and exec requests for
// git-upload-pack and git-receive-pack, which it hands to the system git. It
// lets the tests drive the real client end to end (host key checks, key
// authentication, a push) against a repository on disk.

type sshServer struct {
	t       *testing.T
	root    string
	addr    string
	mu      sync.Mutex
	hostKey []ssh.Signer
	allowed ssh.PublicKey
	ln      net.Listener
	wg      sync.WaitGroup
	conns   map[net.Conn]bool
	gate    chan struct{}
}

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the system git binary is not available; the SSH round trip uses it as the server side")
	}
}

func signer(t *testing.T, kind string) ssh.Signer {
	t.Helper()
	var key any
	switch kind {
	case "ed25519":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	case "ecdsa":
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	case "rsa":
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	}
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func startSSH(t *testing.T, root string, addr string, hostKeys ...ssh.Signer) *sshServer {
	t.Helper()
	s := &sshServer{t: t, root: root, hostKey: hostKeys, conns: map[net.Conn]bool{}}
	s.listen(addr)
	t.Cleanup(s.stop)
	return s
}

func (s *sshServer) listen(addr string) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.t.Fatal(err)
	}
	s.ln = ln
	s.addr = ln.Addr().String()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns[c] = true
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(c)
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}()
		}
	}()
}

func (s *sshServer) stop() {
	if s.ln != nil {
		_ = s.ln.Close()
		s.ln = nil
	}
	s.mu.Lock()
	for c := range s.conns {
		_ = c.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// restart stops the server and starts it again on the same address, with other
// host keys if given.
func (s *sshServer) restart(hostKeys ...ssh.Signer) {
	s.stop()
	if len(hostKeys) > 0 {
		s.hostKey = hostKeys
	}
	s.listen(s.addr)
}

func (s *sshServer) allow(k ssh.PublicKey) { s.mu.Lock(); s.allowed = k; s.mu.Unlock() }

// holdHandshakes makes the server wait before answering a handshake until the
// returned function is called, so a test can change things while a slow call
// is in flight.
func (s *sshServer) holdHandshakes() (release func()) {
	g := make(chan struct{})
	s.mu.Lock()
	s.gate = g
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.gate = nil
			s.mu.Unlock()
			close(g)
		})
	}
}

func (s *sshServer) serve(c net.Conn) {
	s.mu.Lock()
	g := s.gate
	s.mu.Unlock()
	if g != nil {
		<-g
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.allowed != nil && bytes.Equal(s.allowed.Marshal(), key.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("unknown key")
	}}
	for _, k := range s.hostKey {
		cfg.AddHostKey(k)
	}
	conn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go s.session(ch, creqs)
	}
}

func (s *sshServer) session(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		if len(req.Payload) < 4 {
			_ = req.Reply(false, nil)
			return
		}
		n := binary.BigEndian.Uint32(req.Payload)
		cmdline := string(req.Payload[4 : 4+n])
		_ = req.Reply(true, nil)
		name, rest, _ := strings.Cut(cmdline, " ")
		path := strings.Trim(rest, "'")
		var sub string
		switch name {
		case "git-upload-pack":
			sub = "upload-pack"
		case "git-receive-pack":
			sub = "receive-pack"
		default:
			_, _ = io.WriteString(ch.Stderr(), "no such command\n")
			_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 127})
			return
		}
		cmd := exec.Command("git", sub, filepath.Join(s.root, path))
		cmd.Stdin = ch
		cmd.Stdout = ch
		cmd.Stderr = ch.Stderr()
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+s.root)
		code := byte(0)
		if err := cmd.Run(); err != nil {
			code = 1
		}
		_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, code})
		return
	}
}

// ---- tests ---------------------------------------------------------------------

type sshFixture struct {
	h      *harness
	srv    *sshServer
	repo   string // the bare repository's directory
	remote gitmirror.Remote
	hostED ssh.Signer
}

func newSSHFixture(t *testing.T, hostKeys ...ssh.Signer) *sshFixture {
	t.Helper()
	return newSSHFixtureWith(t, nil, hostKeys...)
}

func newSSHFixtureWith(t *testing.T, tune func(*gitmirror.Options), hostKeys ...ssh.Signer) *sshFixture {
	t.Helper()
	needGit(t)
	var tunes []func(*gitmirror.Options)
	if tune != nil {
		tunes = append(tunes, tune)
	}
	h := newHarness(t, tunes...)
	h.start()
	root := t.TempDir()
	repo := filepath.Join(root, "salt-road.git")
	r, err := git.PlainInit(repo, true)
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main")))
	var ed ssh.Signer
	if len(hostKeys) == 0 {
		ed = signer(t, "ed25519")
		hostKeys = []ssh.Signer{ed}
	} else {
		ed = hostKeys[0]
	}
	srv := startSSH(t, root, "127.0.0.1:0", hostKeys...)
	url := "ssh://git@" + srv.addr + "/salt-road.git"
	rem := h.addRemote(url)
	return &sshFixture{h: h, srv: srv, repo: repo, remote: rem, hostED: ed}
}

func (f *sshFixture) authorize() {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(f.remote.PublicKey))
	if err != nil {
		f.h.t.Fatal(err)
	}
	f.srv.allow(k)
}

func (f *sshFixture) scan() gitmirror.HostKeyOut {
	return f.h.must(f.h.owner, "git.remote.hostkey", gitmirror.RemoteBody{ProjectID: f.h.pid}).(gitmirror.HostKeyOut)
}

func (f *sshFixture) accept(fp string, replace bool) (gitmirror.RemoteItem, error) {
	out, err := f.h.call(f.h.owner, "git.remote.hostkey_accept", gitmirror.HostKeyAcceptIn{ProjectID: f.h.pid, Fingerprint: fp, Replace: replace})
	if err != nil {
		return gitmirror.RemoteItem{}, err
	}
	return out.(gitmirror.RemoteItem), nil
}

func (f *sshFixture) test() gitmirror.TestOut {
	return f.h.must(f.h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: f.h.pid}).(gitmirror.TestOut)
}

func TestTheDeployKeyIsAnAuthorizedKeysLine(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("ssh://git@git.example.org/owner/salt-road.git")
	if !rem.HasSecret || rem.AuthKind != "ssh_deploy_key" {
		t.Fatalf("remote = %+v", rem)
	}
	k, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(rem.PublicKey))
	if err != nil || len(rest) != 0 {
		t.Fatalf("not an authorized_keys line: %v %q", err, rem.PublicKey)
	}
	if k.Type() != "ssh-ed25519" || comment != "scriobh-backup" || !strings.HasPrefix(rem.PublicKey, "ssh-ed25519 AAAA") || strings.Contains(rem.PublicKey, "\n") {
		t.Errorf("key = %q (%s, %q)", rem.PublicKey, k.Type(), comment)
	}
	if rem.KeyFingerprint != ssh.FingerprintSHA256(k) {
		t.Errorf("fingerprint = %s", rem.KeyFingerprint)
	}
	if strings.Join(rem.Setup, ",") != "host_key" {
		t.Errorf("setup = %v (the key exists; the host key is still to accept)", rem.Setup)
	}
	// The private half is sealed in the database and never comes back.
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
	if !strings.HasPrefix(cipher, "v1.") || strings.Contains(cipher, "OPENSSH") {
		t.Errorf("cipher = %q", cipher)
	}
	// Regenerating needs an explicit replace and changes the key.
	if _, err := h.call(h.owner, "git.remote.keygen", gitmirror.KeygenIn{ProjectID: h.pid}); h.code(err) != ops.CodeConflict {
		t.Errorf("keygen over a key: %v", err)
	}
	out := h.must(h.owner, "git.remote.keygen", gitmirror.KeygenIn{ProjectID: h.pid, Replace: true}).(gitmirror.KeygenOut)
	if out.PublicKey == rem.PublicKey || out.Title != "scriobh-backup" || out.Fingerprint == rem.KeyFingerprint {
		t.Errorf("keygen = %+v", out)
	}
}

func TestSSHRoundTripWithPinnedHostKeyAndDeployKey(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h

	// Before anything is accepted, pushing says what is missing.
	_, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid})
	if ae := ops.AsError(err); ae.Code != ops.CodeConflict || !strings.Contains(ae.Message, "accept the host key") {
		t.Fatalf("push before setup: %v", err)
	}
	if got := f.test(); got.OK || got.Code != "credentials_missing" {
		t.Fatalf("test before setup = %+v", got)
	}
	// Scan: the fingerprint the server really has; nothing is pinned by scanning.
	scan := f.scan()
	want := ssh.FingerprintSHA256(f.hostED.PublicKey())
	if len(scan.Keys) != 1 || scan.Keys[0].Fingerprint != want || scan.Keys[0].Type != "ssh-ed25519" || scan.Pinned != nil {
		t.Fatalf("scan = %+v, want %s", scan, want)
	}
	if h.remote().HostKey != nil {
		t.Fatal("scanning pinned a key")
	}
	// Accepting a fingerprint the host does not offer is refused.
	if _, err := f.accept("SHA256:"+strings.Repeat("A", 43), false); h.code(err) != ops.CodeConflict {
		t.Errorf("accepting a key the host does not have: %v", err)
	}
	ri, err := f.accept(want, false)
	if err != nil {
		t.Fatal(err)
	}
	rem := ri.Item
	if rem.HostKey == nil || rem.HostKey.Fingerprint != want || len(rem.Setup) != 0 {
		t.Fatalf("after accept: %+v", rem)
	}

	// The key is not yet authorized at the host: a typed error naming the host and the key.
	got := f.test()
	if got.OK || got.Code != "auth_failed" || !strings.Contains(got.Message, "Backup") || got.KeyName != "Backup" || !strings.Contains(got.Message, "127.0.0.1") {
		t.Fatalf("test with an unauthorized key = %+v", got)
	}
	if strings.Contains(got.Message, "PRIVATE") {
		t.Error("the message holds key material")
	}
	f.authorize()
	got = f.test()
	if !got.OK || !got.Empty || got.Warning != "" {
		t.Fatalf("test = %+v", got)
	}

	// A push goes through the real protocol.
	p := h.push(h.owner)
	if p.Status != "ok" || p.Outcome != "pushed" {
		t.Fatalf("push = %+v\n%s", p, h.logs.String())
	}
	if head(t, f.repo, "main").String() != p.CommitSHA {
		t.Fatal("the server's branch does not point at the pushed commit")
	}
	if n := len(commitsOf(t, f.repo, "main")); n != 1 {
		t.Fatalf("%d commits", n)
	}
	// The sealed private key, opened with the instance key, is nowhere in what was
	// pushed, in the commit messages, in the mirror folder, the log or the audit.
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, f.remote.ID).Scan(&cipher)
	pem, err := secrets.New([]byte("test-secret-key-test-secret-key-0123456789")).Open(cipher, gitmirror.SealContext(f.remote.ID, f.remote.URL))
	if err != nil {
		t.Fatal(err)
	}
	keyBody := strings.Split(strings.TrimSpace(string(pem)), "\n")[1]
	for _, c := range commitsOf(t, f.repo, "main") {
		if strings.Contains(c.Message, keyBody) || strings.Contains(c.Author.Email, keyBody) {
			t.Error("the private key is in a commit")
		}
		for name, content := range filesAt(t, f.repo, c) {
			if strings.Contains(content, keyBody) || strings.Contains(content, "PRIVATE KEY") {
				t.Errorf("the private key is in the pushed file %s", name)
			}
		}
	}
	if l := h.leaks(keyBody); len(l) > 0 {
		t.Errorf("the private key leaked: %v", l)
	}
	_ = filepath.WalkDir(h.src.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), keyBody) {
				t.Errorf("the private key is in the mirror: %s", p)
			}
		}
		return nil
	})
	// The repository passes a strict consistency check by real git (tree order,
	// object format).
	if out, err := exec.Command("git", "-C", f.repo, "fsck", "--strict").CombinedOutput(); err != nil {
		t.Fatalf("git fsck --strict: %v\n%s", err, out)
	}
	if got := f.test(); !got.OK || got.Empty || !got.BranchExists || got.Warning != "" {
		t.Errorf("test after a push = %+v", got)
	}
}

func TestAChangedHostKeyIsAHardError(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h
	f.authorize()
	orig := f.scan().Keys[0].Fingerprint
	if _, err := f.accept(orig, false); err != nil {
		t.Fatal(err)
	}
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("setup push: %+v", p)
	}
	before := head(t, f.repo, "main")

	// The server's key changes (reinstalled, or something in the middle).
	f.srv.restart(signer(t, "ed25519"))
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0]})
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "host_key_changed" || !strings.Contains(p.ErrorMessage, orig) {
		t.Fatalf("push = %+v", p)
	}
	if head(t, f.repo, "main") != before {
		t.Error("something was sent to a host whose key changed")
	}
	r := h.remote()
	if r.Status.State != "blocked" || r.Status.LastErrorCode != "host_key_changed" {
		t.Errorf("status = %+v", r.Status)
	}
	if got := f.test(); got.OK || got.Code != "host_key_changed" {
		t.Errorf("test = %+v", got)
	}
	// The scan says so too.
	if sc := f.scan(); !sc.Changed || sc.PinnedFound || sc.Pinned.Fingerprint != orig {
		t.Errorf("scan = %+v", sc)
	}
	// Automatic triggers stay away.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, PushMode: ptr("on_save")})
	h.svc.Notify(h.pid)
	h.clk.Advance(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	if n := len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items); n != 2 {
		t.Errorf("%d push rows; a blocked remote must not be retried", n)
	}
	// Accepting the new key needs a deliberate replace.
	fresh := f.scan().Keys[0].Fingerprint
	if _, err := f.accept(fresh, false); h.code(err) != ops.CodeConflict {
		t.Errorf("replacing a pinned key without replace=true: %v", err)
	}
	if _, err := f.accept(fresh, true); err != nil {
		t.Fatal(err)
	}
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("after accepting the new key: %+v", p)
	}
}

func TestPinningWorksWhateverTheHostOffers(t *testing.T) {
	// A host with several key types: the client offers only the pinned key's
	// type, so the host cannot look like it changed its key.
	ed, ec, rs := signer(t, "ed25519"), signer(t, "ecdsa"), signer(t, "rsa")
	f := newSSHFixture(t, ed, ec, rs)
	f.authorize()
	scan := f.scan()
	if len(scan.Keys) != 3 {
		t.Fatalf("scan found %d keys, want 3: %+v", len(scan.Keys), scan.Keys)
	}
	types := map[string]string{}
	for _, k := range scan.Keys {
		types[k.Type] = k.Fingerprint
	}
	for _, s := range []ssh.Signer{ec, rs, ed} { // pin each in turn
		fp := ssh.FingerprintSHA256(s.PublicKey())
		if types[s.PublicKey().Type()] != fp {
			t.Fatalf("scan lists %v, want %s for %s", types, fp, s.PublicKey().Type())
		}
		if _, err := f.accept(fp, true); err != nil {
			t.Fatal(err)
		}
		if got := f.test(); !got.OK {
			t.Fatalf("pinned %s: %+v", s.PublicKey().Type(), got)
		}
	}
}

func TestUnreachableHostRetriesWithBackoffAndPushesTheKeptCommit(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h
	f.authorize()
	if _, err := f.accept(f.scan().Keys[0].Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, PushMode: ptr("on_save")})
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("setup push: %+v", p)
	}
	f.srv.stop()
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 100, "Changed.\n"}, chapters[2]})
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "unreachable" || !strings.Contains(p.ErrorMessage, "127.0.0.1") {
		t.Fatalf("push = %+v", p)
	}
	r := h.remote()
	if r.Status.State != "behind" || !r.Status.Behind || r.Status.FailCount != 1 || r.Status.NextAttemptAt == "" {
		t.Fatalf("status = %+v", r.Status)
	}
	count := func() int {
		return len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items)
	}
	tick := func(d time.Duration) {
		h.clk.Advance(d)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.svc.WaitIdle(ctx)
	}
	n := count()
	h.svc.Notify(h.pid)    // someone is writing: a save must not skip the backoff
	tick(30 * time.Second) // the debounce ends inside the first backoff (1 minute)
	if count() != n {
		t.Fatal("a save during the backoff caused an attempt")
	}
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 101, "Changed again.\n"}, chapters[2]})
	h.svc.Notify(h.pid)
	tick(20 * time.Second) // 50 s after the failure, a second debounce has ended
	if count() != n {
		t.Fatal("a second save during the backoff caused an attempt")
	}
	tick(40 * time.Second) // the backoff is over: one retry, which fails
	if count() != n+1 || h.remote().Status.FailCount != 2 {
		t.Fatalf("expected one retry; history %d -> %d, fails %d", n, count(), h.remote().Status.FailCount)
	}
	tick(90 * time.Second) // the next backoff is two minutes
	if count() != n+1 {
		t.Fatal("retried before the doubled backoff ended")
	}
	tick(60 * time.Second)
	if count() != n+2 {
		t.Fatalf("expected the second retry; history %d", count())
	}
	// The host comes back: the next retry pushes the kept commit, once.
	f.srv.restart()
	tick(10 * time.Minute)
	r = h.remote()
	if r.Status.State != "synced" || head(t, f.repo, "main").String() != r.Status.LocalSHA {
		t.Fatalf("after the host came back: %+v\n%s", r.Status, h.logs.String())
	}
	// The first commit, kept while the host was down, then the one for the second
	// save: each pushed once, in order.
	if got := len(commitsOf(t, f.repo, "main")); got != 3 {
		t.Errorf("remote has %d commits, want 3", got)
	}
}

func TestHostKeyCheckUnit(t *testing.T) {
	needGit(t)
	// A server that offers no usable host key algorithm gives a clear error
	// from the scan rather than hanging.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(c, "not ssh\r\n")
			c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := gitmirror.ScanHostKeys(ctx, ln.Addr().String()); err == nil {
		t.Error("a server that is not SSH gave host keys")
	}
	// A closed port is an error at once.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := closed.Addr().String()
	closed.Close()
	if _, err := gitmirror.ScanHostKeys(ctx, addr); err == nil {
		t.Error("a closed port gave host keys")
	}
}

// Real servers take shallow fetches, which the in-process file transport does
// not: this is where the shallow paths (adopting a lost history, listing a
// refused remote's commits, a clone for an import) are exercised, with a
// stored deploy key and a pinned host key all the way through.
func TestShallowPathsAgainstARealGitServerOverSSH(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h
	f.authorize()
	if _, err := f.accept(f.scan().Keys[0].Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	first := h.push(h.owner)
	if first.Status != "ok" {
		t.Fatalf("push = %+v\n%s", first, h.logs.String())
	}

	// A lost local repository is adopted from the remote (a shallow fetch of our
	// own last commit), and the history goes on.
	if err := os.RemoveAll(h.svc.RepoDir(h.pid, f.remote.ID)); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 90, "Rewritten.\n"}, chapters[2]})
	second := h.push(h.owner)
	if second.Status != "ok" || second.ParentSHA != first.CommitSHA {
		t.Fatalf("push after losing the local repository = %+v\n%s", second, h.logs.String())
	}
	if n := len(commitsOf(t, f.repo, "main")); n != 2 {
		t.Fatalf("remote has %d commits, want 2", n)
	}
	if out, err := exec.Command("git", "-C", f.repo, "fsck", "--strict").CombinedOutput(); err != nil {
		t.Fatalf("git fsck --strict: %v\n%s", err, out)
	}

	// Import through the project's remote: the stored key and the pinned host
	// key are used, and the clone is shallow.
	job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{RemoteID: f.remote.ID}).ID)
	if job.Status != "preview" || job.Summary == nil || job.Summary.Layout != "scriobh" || job.Summary.Documents != 3 || job.CommitSHA != second.CommitSHA {
		t.Fatalf("import job = %+v\n%s", job, h.logs.String())
	}
	if _, err := h.call(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: job.Summary.Token}); err != nil {
		t.Fatal(err)
	}
	if rowCount(t, h, `SELECT count(*) FROM project_sources WHERE kind = 'git' AND commit_sha = ?`, second.CommitSHA) != 1 {
		t.Error("the import's source was not recorded")
	}
	_ = imports.MaxListed

	// Someone else pushes to the remote: refused, with their commits listed
	// from a shallow scratch fetch, and nothing overwritten.
	other := filepath.Join(t.TempDir(), "other")
	clone, err := git.PlainClone(other, false, &git.CloneOptions{URL: f.repo})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "NOTES.md"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := clone.Worktree()
	_, _ = wt.Add("NOTES.md")
	theirs, err := wt.Commit("Their notes", &git.CommitOptions{Author: &object.Signature{Name: "Collaborator", Email: "c@example.org", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0]})
	third := h.push(h.owner)
	if third.Status != "rejected" || third.RemoteSHA != theirs.String() || len(third.RemoteCommits) != 1 || third.RemoteCommits[0].Subject != "Their notes" {
		t.Fatalf("push = %+v", third)
	}
	if head(t, f.repo, "main") != theirs {
		t.Error("the remote was overwritten")
	}
}

// A force push through a real git server over SSH: the bundle is the fetched
// pack behind a header (the path that does not re-encode), real git accepts it,
// and it brings the overwritten history back.
func TestForcePushOverSSHWritesABundleRealGitAccepts(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h
	f.authorize()
	if _, err := f.accept(f.scan().Keys[0].Fingerprint, false); err != nil {
		t.Fatal(err)
	}
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	other := filepath.Join(t.TempDir(), "other")
	clone, err := git.PlainClone(other, false, &git.CloneOptions{URL: f.repo})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(other, "NOTES.md"), []byte("theirs\n"), 0o644)
	wt, _ := clone.Worktree()
	_, _ = wt.Add("NOTES.md")
	theirs, err := wt.Commit("Their notes", &git.CommitOptions{Author: &object.Signature{Name: "C", Email: "c@e.org", When: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0]})
	if p := h.push(h.owner); p.Status != "rejected" {
		t.Fatalf("setup: %+v", p)
	}
	out := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: f.remote.ID, Confirm: "Backup"}).(gitmirror.PushOut)
	p := h.settle(out.Item.ID)
	if p.Status != "ok" || p.Bundle == "" {
		t.Fatalf("force = %+v\n%s", p, h.logs.String())
	}
	if gitmirror.LastBundlePath() != "pack" {
		t.Errorf("the bundle was written by the %q path, want the streamed pack", gitmirror.LastBundlePath())
	}
	bundle := filepath.Join(h.dataDir, "backups", "git", h.pid, p.Bundle)
	if err := gitmirror.VerifyBundle(bundle, theirs); err != nil {
		t.Fatal(err)
	}
	if out, err := verifyBundleCmd(t, "git", bundle).CombinedOutput(); err != nil {
		t.Fatalf("git bundle verify: %v\n%s", err, out)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if out, err := exec.Command("git", "clone", "--quiet", "--branch", "main", bundle, restored).CombinedOutput(); err != nil {
		t.Fatalf("git clone of the bundle: %v\n%s", err, out)
	}
	if b, err := os.ReadFile(filepath.Join(restored, "NOTES.md")); err != nil || string(b) != "theirs\n" {
		t.Errorf("the overwritten file did not come back: %q %v", b, err)
	}
	if head(t, f.repo, "main").String() != p.CommitSHA {
		t.Error("the remote was not overwritten after the bundle")
	}
}

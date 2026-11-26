//go:build !prod

package devgit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func need(t *testing.T) {
	t.Helper()
	for _, p := range []string{"git", "ssh", "ssh-keygen"} {
		if _, err := exec.LookPath(p); err != nil {
			t.Skipf("%s is not available", p)
		}
	}
}

func call(t *testing.T, s *Server, method, path string, in, out any) int {
	t.Helper()
	var body bytes.Buffer
	if in != nil {
		_ = json.NewEncoder(&body).Encode(in)
	}
	req, _ := http.NewRequest(method, s.ControlURL()+path, &body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func run(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func TestDevGitServerEndToEnd(t *testing.T) {
	need(t)
	tmp := t.TempDir()
	s, err := Start(Options{Root: filepath.Join(tmp, "repos")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	key := filepath.Join(tmp, "id")
	if out, err := run(t, tmp, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key); err != nil {
		t.Fatal(out, err)
	}
	pub, _ := os.ReadFile(key + ".pub")
	known := filepath.Join(tmp, "known_hosts")
	host, port, _ := strings.Cut(s.SSHAddr(), ":")
	// -F /dev/null: the test talks to its own loopback server, so the person's ~/.ssh/config (which may rewrite any host) must not apply.
	sshCmd := fmt.Sprintf("ssh -F /dev/null -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s -o BatchMode=yes -o ConnectTimeout=10 -p %s", key, known, port)
	env := []string{"GIT_SSH_COMMAND=" + sshCmd, "GIT_AUTHOR_NAME=Me", "GIT_AUTHOR_EMAIL=me@example.com", "GIT_COMMITTER_NAME=Me", "GIT_COMMITTER_EMAIL=me@example.com"}
	url := fmt.Sprintf("ssh://git@%s:%s/book.git", host, port)

	var info struct {
		HostKeys []hostKeyInfo `json:"host_keys"`
	}
	if call(t, s, "GET", "/info", nil, &info) != 200 || len(info.HostKeys) != 1 || !strings.HasPrefix(info.HostKeys[0].Fingerprint, "SHA256:") {
		t.Fatalf("info: %+v", info)
	}
	if code := call(t, s, "POST", "/repos", map[string]string{"name": "book"}, nil); code != 201 {
		t.Fatalf("create repo: %d", code)
	}
	if code := call(t, s, "POST", "/repos", map[string]string{"name": "book"}, nil); code != 409 {
		t.Fatalf("create twice: %d", code)
	}
	if code := call(t, s, "POST", "/repos", map[string]string{"name": "../x"}, nil); code != 400 {
		t.Fatalf("bad name: %d", code)
	}

	// Before the key is authorised nothing works.
	if out, err := run(t, tmp, env, "git", "ls-remote", url); err == nil {
		t.Fatalf("an unauthorised key was accepted: %s", out)
	}
	var az struct {
		Fingerprint string `json:"fingerprint"`
	}
	if call(t, s, "POST", "/authorize", map[string]string{"public_key": string(pub)}, &az) != 200 || az.Fingerprint == "" {
		t.Fatal("authorize")
	}
	// The user must be git unless any user is allowed.
	if out, err := run(t, tmp, env, "git", "ls-remote", strings.Replace(url, "git@", "someone@", 1)); err == nil {
		t.Fatalf("another user name was accepted: %s", out)
	}

	// A first foreign commit on an empty repository creates the branch.
	var fc struct {
		SHA string `json:"sha"`
	}
	if call(t, s, "POST", "/foreign-commit", map[string]any{"repo": "book", "branch": "main", "message": "From outside", "files": map[string]string{"a/b.md": "hello\n"}}, &fc) != 201 || len(fc.SHA) != 40 {
		t.Fatalf("foreign commit on an empty repository: %+v", fc)
	}
	var head struct {
		SHA string `json:"sha"`
	}
	if call(t, s, "GET", "/head?repo=book&branch=main", nil, &head) != 200 || head.SHA != fc.SHA {
		t.Fatalf("head %q, want %q", head.SHA, fc.SHA)
	}
	if call(t, s, "GET", "/head?repo=book&branch=nope", nil, nil) != 404 {
		t.Fatal("a missing branch is a 404")
	}

	// Clone over SSH, add a commit, push it, then a second foreign commit on top of history.
	work := filepath.Join(tmp, "work")
	if out, err := run(t, tmp, env, "git", "clone", "-q", url, work); err != nil {
		t.Fatal(out, err)
	}
	if err := os.WriteFile(filepath.Join(work, "mine.md"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "Mine"}, {"push", "-q", "origin", "main"}} {
		if out, err := run(t, work, env, "git", a...); err != nil {
			t.Fatal(out, err)
		}
	}
	mine, _ := run(t, work, env, "git", "rev-parse", "HEAD")
	call(t, s, "GET", "/head?repo=book&branch=main", nil, &head)
	if head.SHA != mine {
		t.Fatalf("the push did not arrive: %s vs %s", head.SHA, mine)
	}
	var fc2 struct {
		SHA string `json:"sha"`
	}
	if call(t, s, "POST", "/foreign-commit", map[string]any{"repo": "book", "branch": "main", "message": "Second from outside"}, &fc2) != 201 {
		t.Fatal("foreign commit on history")
	}
	var lg struct {
		Items []struct{ SHA, Subject, Author string } `json:"items"`
	}
	if call(t, s, "GET", "/log?repo=book&branch=main", nil, &lg) != 200 || len(lg.Items) != 3 || lg.Items[0].Subject != "Second from outside" || lg.Items[0].Author != "Someone Else" {
		t.Fatalf("log: %+v", lg)
	}
	// A push from the stale clone is refused (non-fast-forward): the rejected-push scenario.
	if err := os.WriteFile(filepath.Join(work, "again.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "Stale"}} {
		if out, err := run(t, work, env, "git", a...); err != nil {
			t.Fatal(out, err)
		}
	}
	if out, err := run(t, work, env, "git", "push", "-q", "origin", "main"); err == nil {
		t.Fatalf("a non-fast-forward push was accepted: %s", out)
	}

	// Rotating the host key makes the pinned key wrong; restoring brings it back.
	var rot struct {
		HostKeys []hostKeyInfo `json:"host_keys"`
	}
	if call(t, s, "POST", "/rotate-host-key", map[string]string{}, &rot) != 200 || rot.HostKeys[0].Fingerprint == info.HostKeys[0].Fingerprint {
		t.Fatalf("rotate: %+v", rot)
	}
	if out, err := run(t, tmp, env, "git", "ls-remote", url); err == nil || !strings.Contains(out, "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("a changed host key was not noticed: %v %s", err, out)
	}
	if call(t, s, "POST", "/restore-host-key", nil, &rot) != 200 || rot.HostKeys[0].Fingerprint != info.HostKeys[0].Fingerprint {
		t.Fatal("restore")
	}
	if out, err := run(t, tmp, env, "git", "ls-remote", url); err != nil {
		t.Fatalf("the restored key was refused: %s", out)
	}

	// Deauthorising stops the key.
	call(t, s, "POST", "/deauthorize", map[string]string{"fingerprint": az.Fingerprint}, nil)
	if out, err := run(t, tmp, env, "git", "ls-remote", url); err == nil {
		t.Fatalf("a deauthorised key was accepted: %s", out)
	}

	// Bundles: a real one verifies and lists its head, a text file does not.
	bundle := filepath.Join(tmp, "b.bundle")
	if out, err := run(t, work, env, "git", "bundle", "create", bundle, "main"); err != nil {
		t.Fatal(out, err)
	}
	var vb struct {
		OK    bool                        `json:"ok"`
		Heads []struct{ SHA, Ref string } `json:"heads"`
	}
	if call(t, s, "POST", "/verify-bundle", map[string]string{"path": bundle}, &vb) != 200 || !vb.OK || len(vb.Heads) != 1 || vb.Heads[0].Ref != "refs/heads/main" {
		t.Fatalf("verify bundle: %+v", vb)
	}
	junk := filepath.Join(tmp, "junk")
	_ = os.WriteFile(junk, []byte("not a bundle"), 0o644)
	vb.OK = true
	if call(t, s, "POST", "/verify-bundle", map[string]string{"path": junk}, &vb) != 200 || vb.OK {
		t.Fatalf("a text file passed as a bundle: %+v", vb)
	}

	if call(t, s, "POST", "/shutdown", nil, nil) != 200 {
		t.Fatal("shutdown")
	}
	<-s.Done()
}

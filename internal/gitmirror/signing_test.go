package gitmirror_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

func (h *harness) signingKey() gitmirror.SigningOut {
	h.t.Helper()
	return h.must(h.owner, "git.signing.get", gitmirror.ProjectIn{ProjectID: h.pid}).(gitmirror.SigningOut)
}

func TestTheSigningKeyIsTheProjectsAndNeverLeavesTheServer(t *testing.T) {
	h := newHarness(t)
	if k := h.signingKey(); k.Exists || k.Key != nil {
		t.Fatalf("a key before one was made: %+v", k)
	}
	if _, err := h.call(h.owner, "git.remote.create", gitmirror.CreateIn{ProjectID: h.pid, Name: "X", URL: "https://forge.example.org/a/b.git", Token: "tok_abcdefghijklmnop", SignCommits: ptr(true)}); h.code(err) != ops.CodeConflict {
		t.Errorf("signing without a key: %v", err)
	}
	out := h.must(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid}).(gitmirror.SigningOut)
	if !out.Exists || !strings.HasPrefix(out.Key.PublicKey, "ssh-ed25519 ") || !strings.HasPrefix(out.Key.Fingerprint, "SHA256:") ||
		!strings.HasPrefix(out.Key.AllowedSigners, `* namespaces="git" ssh-ed25519 `) {
		t.Fatalf("key = %+v", out)
	}
	if b := mustJSON(t, out); strings.Contains(b, "PRIVATE KEY") {
		t.Errorf("the private key is in the answer")
	}
	if _, err := h.call(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid}); h.code(err) != ops.CodeConflict {
		t.Errorf("replacing without saying so: %v", err)
	}
	again := h.must(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid, Replace: true}).(gitmirror.SigningOut)
	if again.Key.Fingerprint == out.Key.Fingerprint {
		t.Errorf("a replaced key is the same")
	}
	// The stored private half is sealed: nothing of it in the row.
	var sealed string
	_ = h.d.Read().QueryRow(`SELECT key_sealed FROM git_signing_keys WHERE project_id = ?`, h.pid).Scan(&sealed)
	if sealed == "" || strings.Contains(sealed, "BEGIN OPENSSH") {
		t.Errorf("sealed = %.40q", sealed)
	}
}

func TestSigningIsDeterministicAndStructured(t *testing.T) {
	signer := testSigner(t)
	a, err := gitmirror.SignSSH(signer, []byte("tree x\n\nmessage\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := gitmirror.SignSSH(signer, []byte("tree x\n\nmessage\n"))
	if a != b {
		t.Errorf("an ed25519 signature should be the same twice")
	}
	if !strings.HasPrefix(a, "-----BEGIN SSH SIGNATURE-----\n") || !strings.HasSuffix(a, "\n-----END SSH SIGNATURE-----") {
		t.Errorf("armor = %q", a)
	}
	for _, l := range strings.Split(a, "\n") {
		if len(l) > 70 && !strings.HasPrefix(l, "-----") {
			t.Errorf("a line of %d characters", len(l))
		}
	}
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen: %v %s", err, out)
	}
	b, _ := os.ReadFile(key)
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A signed commit is verified the way anyone verifies one: git verify-commit with an allowed-signers file, and
// ssh-keygen -Y verify over the commit as git hands it to the tool.
func TestSignedCommitsVerifyWithGitAndSSHKeygen(t *testing.T) {
	needGit(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	key := h.must(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid}).(gitmirror.SigningOut).Key
	signed := h.addRemote(url, func(in *gitmirror.CreateIn) {
		in.SignCommits = ptr(true)
		in.AuthorName = "Máire"
		in.AuthorEmail = "maire@example.org"
	})
	if !signed.SignCommits {
		t.Fatal("sign_commits is not on")
	}
	if p := h.pushRemote(signed.ID); p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	// A second commit, to see a signature over a commit with a parent.
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	if p := h.pushRemote(signed.ID); p.Status != "ok" {
		t.Fatalf("second push = %+v", p)
	}
	tip := head(t, dir, "main").String()
	rawBytes, err := exec.Command("git", "-C", dir, "cat-file", "commit", tip).Output()
	if err != nil {
		t.Fatal(err)
	}
	raw := string(rawBytes)
	if !strings.Contains(raw, "\ngpgsig -----BEGIN SSH SIGNATURE-----\n ") || !strings.Contains(raw, "\n -----END SSH SIGNATURE-----\n\n") {
		t.Fatalf("the commit has no folded gpgsig header:\n%s", raw)
	}

	work := t.TempDir()
	allowed := filepath.Join(work, "allowed_signers")
	if err := os.WriteFile(allowed, []byte(key.AllowedSigners+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sha := range []string{tip, gitOut(t, dir, "rev-parse", tip+"~1")} {
		out, err := exec.Command("git", "-C", dir, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", "--raw", sha).CombinedOutput()
		if err != nil {
			t.Fatalf("git verify-commit %s: %v\n%s", sha[:8], err, out)
		}
	}
	// ssh-keygen over the commit without its signature, as git hands it over.
	body, sig := splitSigned(t, raw)
	sigFile := filepath.Join(work, "sig")
	_ = os.WriteFile(sigFile, []byte(sig), 0o600)
	cmd := exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", "maire@example.org", "-n", "git", "-s", sigFile)
	cmd.Stdin = strings.NewReader(body)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y verify: %v\n%s", err, out)
	}
	// A changed commit does not verify, and neither does another key.
	cmd = exec.Command("ssh-keygen", "-Y", "verify", "-f", allowed, "-I", "maire@example.org", "-n", "git", "-s", sigFile)
	cmd.Stdin = strings.NewReader(body + "x")
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("a changed commit verified: %s", out)
	}
	other := h.must(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid, Replace: true}).(gitmirror.SigningOut).Key
	_ = os.WriteFile(allowed, []byte(other.AllowedSigners+"\n"), 0o600)
	if out, err := exec.Command("git", "-C", dir, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", tip).CombinedOutput(); err == nil {
		t.Errorf("a commit verified against another key: %s", out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// splitSigned takes a commit object apart: the commit without its gpgsig header, and the signature unfolded.
func splitSigned(t *testing.T, raw string) (body, sig string) {
	t.Helper()
	lines := strings.Split(raw, "\n")
	var out, sigLines []string
	in := false
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "gpgsig "):
			in = true
			sigLines = append(sigLines, strings.TrimPrefix(l, "gpgsig "))
		case in && strings.HasPrefix(l, " "):
			sigLines = append(sigLines, strings.TrimPrefix(l, " "))
		default:
			in = false
			out = append(out, l)
		}
	}
	if len(sigLines) == 0 {
		t.Fatal("no gpgsig header")
	}
	return strings.Join(out, "\n"), strings.Join(sigLines, "\n") + "\n"
}

func TestUnsignedByDefaultAndSigningNeedsTheOwner(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	r := h.addRemote(url)
	if r.SignCommits {
		t.Fatal("signing on by default")
	}
	h.pushRemote(r.ID)
	if raw := gitOut(t, dir, "cat-file", "commit", head(t, dir, "main").String()); strings.Contains(raw, "gpgsig") {
		t.Errorf("an unsigned remote made a signed commit")
	}
	if _, err := h.call(h.editor, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, SignCommits: ptr(true)}); h.code(err) != ops.CodeForbidden {
		t.Errorf("an editor turning signing on: %v", err)
	}
	// With the key gone from under a remote that signs, the push stops with a typed error and nothing is pushed unsigned.
	h.must(h.owner, "git.signing.generate", gitmirror.SigningGenerateIn{ProjectID: h.pid})
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, SignCommits: ptr(true)})
	_, _ = h.d.Writer().Exec(`DELETE FROM git_signing_keys`)
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	before := head(t, dir, "main")
	p := h.pushRemote(r.ID)
	if p.Status != "error" || p.ErrorCode != "signing_failed" || head(t, dir, "main") != before {
		t.Errorf("push = %+v", p)
	}
}

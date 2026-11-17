package gitmirror_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"golang.org/x/crypto/ssh"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// ---- who may touch what a credential is sent to -------------------------------------

func TestEditorsCannotRepointARemoteOrTouchItsCredential(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/mairead/salt-road.git", func(in *gitmirror.CreateIn) { in.Token = token })

	forbidden := map[string]any{
		"git.remote.create":         gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "Other", URL: "https://attacker.example.net/x.git", Token: "stolen"},
		"git.remote.update":         gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("https://attacker.example.net/x.git")},
		"git.remote.delete":         gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: rem.ID},
		"git.remote.keygen":         gitmirror.KeygenIn{ProjectID: h.pid, Replace: true},
		"git.remote.hostkey_accept": gitmirror.HostKeyAcceptIn{ProjectID: h.pid, Fingerprint: "SHA256:" + strings.Repeat("A", 43)},
	}
	for op, in := range forbidden {
		if _, err := h.call(h.editor, op, in); h.code(err) != ops.CodeForbidden {
			t.Errorf("an editor's %s: %v", op, err)
		}
	}
	// Any field of an update, not only the address: what is published is the owner's call.
	for _, in := range []gitmirror.UpdateIn{
		{ProjectID: h.pid, IncludeNotes: ptr(false)}, {ProjectID: h.pid, PathPrefix: ptr("x")}, {ProjectID: h.pid, Branch: ptr("other")},
		{ProjectID: h.pid, Token: ptr("another-token-value")}, {ProjectID: h.pid, PushMode: ptr("manual")},
	} {
		if _, err := h.call(h.editor, "git.remote.update", in); h.code(err) != ops.CodeForbidden {
			t.Errorf("an editor's update %+v: %v", in, err)
		}
	}
	if r := h.remote(); r.URL != "https://forge.example.org/mairead/salt-road.git" || !r.HasSecret || r.Branch != "main" {
		t.Errorf("the remote changed: %+v", r)
	}
	// What an editor may do: look, test, push now, read history, cancel.
	if _, err := h.call(h.editor, "git.remote.get", gitmirror.RemoteQuery{ProjectID: h.pid}); err != nil {
		t.Errorf("editor get: %v", err)
	}
	if _, err := h.call(h.editor, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}); err != nil {
		t.Errorf("editor test: %v", err)
	}
	if _, err := h.call(h.editor, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}); err != nil {
		t.Errorf("editor history: %v", err)
	}
	// Importing through a remote uses its stored credential: the owner's call.
	if _, err := h.call(h.editor, "import.git_preview", gitmirror.GitPreviewIn{RemoteID: rem.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("an editor importing through the owner's remote: %v", err)
	}
}

func TestChangingTheAddressDropsTheCredentialAndTheHostKey(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/mairead/salt-road.git", func(in *gitmirror.CreateIn) { in.Token = token })

	// The same place, written differently, keeps the credential.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("https://FORGE.example.org/mairead/salt-road")})
	if r := h.remote(); !r.HasSecret {
		t.Fatal("a rewrite of the same address dropped the credential")
	}
	for _, u := range []string{
		"https://forge.example.org/mairead/another-book.git",   // another repository
		"https://forge.example.org:8443/mairead/salt-road.git", // another port
		"https://elsewhere.example.org/mairead/salt-road.git",  // another host
	} {
		h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("https://forge.example.org/mairead/salt-road.git"), Token: ptr(token)})
		if !h.remote().HasSecret {
			t.Fatal("could not set up")
		}
		r := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: &u}).(gitmirror.RemoteItem).Item
		if r.HasSecret || strings.Join(r.Setup, ",") != "token" {
			t.Errorf("after moving to %s: has_secret=%v setup=%v", u, r.HasSecret, r.Setup)
		}
		var cipher string
		_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
		if cipher != "" {
			t.Errorf("the ciphertext survived a move to %s", u)
		}
		// Nothing is sent: there is nothing to send, and a test says what is missing.
		if got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut); got.OK || got.Code != "credentials_missing" {
			t.Errorf("test after the move = %+v", got)
		}
		if _, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}); h.code(err) != ops.CodeConflict {
			t.Errorf("push after the move: %v", err)
		}
	}
	// A new token may be given with the new address, and then it is for that address.
	r := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("https://git.example.net/mairead/salt-road.git"), Token: ptr("fresh-token-for-the-new-host")}).(gitmirror.RemoteItem).Item
	if !r.HasSecret || len(r.Setup) != 0 {
		t.Errorf("remote = %+v", r)
	}
}

func TestADeployKeyAndItsHostKeyArePerHost(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("ssh://git@forge.example.org/mairead/salt-road.git")
	h.d.Writer().Exec(`UPDATE git_remotes SET known_hosts = 'forge.example.org:22 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHnQbmVrfAyDZP2h1y0n7KcGg9d3zBJqkAw+gsUoK1nM' WHERE id = ?`, rem.ID)
	same := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("ssh://git@forge.example.org/mairead/salt-road")}).(gitmirror.RemoteItem).Item
	if !same.HasSecret || same.PublicKey == "" || same.HostKey == nil {
		t.Fatalf("a rewrite of the same address dropped the key or the pin: %+v", same)
	}
	moved := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("ssh://git@attacker.example.net/mairead/salt-road.git")}).(gitmirror.RemoteItem).Item
	if moved.HasSecret || moved.PublicKey != "" || moved.HostKey != nil || strings.Join(moved.Setup, ",") != "deploy_key,host_key" {
		t.Fatalf("after moving to another host: %+v", moved)
	}
}

func TestACredentialRowCannotBeRetargetedInTheDatabase(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/mairead/salt-road.git", func(in *gitmirror.CreateIn) { in.Token = token })
	h.start()
	// Someone with write access to the database re-points the row. The sealed
	// token is bound to the place it was entered for, so it does not open.
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET url = 'https://attacker.example.net/x.git' WHERE id = ?`, rem.ID); err != nil {
		t.Fatal(err)
	}
	got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut)
	if got.OK || got.Code != "credentials_unreadable" {
		t.Fatalf("test = %+v", got)
	}
	p, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid})
	if err != nil {
		t.Fatal(err)
	}
	done := h.settle(p.(gitmirror.PushOut).Item.ID)
	if done.Status != "error" || done.ErrorCode != "credentials_unreadable" {
		t.Fatalf("push = %+v", done)
	}
	// The same ciphertext in another row does not open either.
	other := h.addRemoteOn("ssh://git@forge.example.org/o/r.git")
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
	if secrets.New([]byte("test-secret-key-test-secret-key-0123456789")).CanOpen(cipher, gitmirror.SealContext(other, "ssh://git@forge.example.org/o/r.git")) {
		t.Error("a credential opened for another row")
	}
}

// addRemoteOn adds a second project's remote and returns its id (a second remote
// on one project is refused).
func (h *harness) addRemoteOn(url string) string {
	h.t.Helper()
	pid := h.newProject("Another Book")
	return h.must(h.owner, "git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: pid, Name: "Backup", URL: url}).(gitmirror.RemoteItem).Item.ID
}

func TestRowsSealedBeforeTheAddressWasBoundAreSealedAgainOnceAtStart(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/mairead/salt-road.git", func(in *gitmirror.CreateIn) { in.Token = token })
	box := secrets.New([]byte("test-secret-key-test-secret-key-0123456789"))
	old, err := box.Seal([]byte(token), gitmirror.LegacySealContext(rem.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET secret_cipher = ? WHERE id = ?`, old, rem.ID); err != nil {
		t.Fatal(err)
	}
	// Not accepted at use time: a legacy ciphertext does not open for a push.
	if got := h.must(h.owner, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.TestOut); got.Code != "credentials_unreadable" {
		t.Fatalf("test with a legacy row = %+v", got)
	}
	if _, err := gitmirror.CheckCredentials(context.Background(), h.d, box, false); err != nil {
		t.Fatal(err)
	}
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
	if cipher == old || !box.CanOpen(cipher, gitmirror.SealContext(rem.ID, "https://forge.example.org/mairead/salt-road.git")) || box.CanOpen(cipher, gitmirror.LegacySealContext(rem.ID)) {
		t.Error("the row was not sealed again for its address")
	}
	if r := h.remote(); !r.Enabled || r.Status.LastErrorCode != "" {
		t.Errorf("remote = %+v", r)
	}
}

// ---- the row changed under an op ------------------------------------------------------

func TestASlowHostKeyAcceptDoesNotRevertAConcurrentEdit(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h
	release := f.srv.holdHandshakes()
	defer release()
	fp := ssh.FingerprintSHA256(f.hostED.PublicKey())

	var wg sync.WaitGroup
	var acceptErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, acceptErr = f.accept(fp, false)
	}()
	time.Sleep(150 * time.Millisecond) // the accept has read the row and is waiting on the host
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, PushMode: ptr("interval"), IntervalMinutes: ptr(30), IncludeNotes: ptr(false), AuthorName: ptr("Máire"), AuthorEmail: ptr("m@example.org")})
	release()
	wg.Wait()
	if acceptErr != nil {
		t.Fatal(acceptErr)
	}
	r := h.remote()
	if r.HostKey == nil || r.PushMode != "interval" || r.IntervalMinutes != 30 || r.IncludeNotes || r.AuthorName != "Máire" {
		t.Errorf("the accept reverted a concurrent edit, or did not pin: %+v", r)
	}
}

func TestASlowScanThatOutlivesARepointingPinsNothing(t *testing.T) {
	f := newSSHFixture(t)
	h := f.h
	release := f.srv.holdHandshakes()
	defer release()
	fp := ssh.FingerprintSHA256(f.hostED.PublicKey())
	var wg sync.WaitGroup
	var acceptErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, acceptErr = f.accept(fp, false)
	}()
	time.Sleep(150 * time.Millisecond)
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, URL: ptr("ssh://git@" + f.srv.addr + "/another.git")})
	release()
	wg.Wait()
	if h.code(acceptErr) != ops.CodeConflict {
		t.Errorf("accept after a re-pointing: %v", acceptErr)
	}
	if r := h.remote(); r.HostKey != nil {
		t.Error("a host key scanned for the old address was pinned for the new one")
	}
}

// ---- force pushes ---------------------------------------------------------------------

func TestAForcePushIsForTheRemoteTheOwnerConfirmed(t *testing.T) {
	h := newHarness(t)
	url, _, dir := h.bareRemote()
	r := h.addRemote(url)
	h.start()
	h.push(h.owner)
	// Rejected setup: a foreign commit on the remote.
	other := filepath.Join(t.TempDir(), "other")
	clone, _ := git.PlainClone(other, false, &git.CloneOptions{URL: dir})
	_ = os.WriteFile(filepath.Join(other, "NOTES.md"), []byte("theirs\n"), 0o644)
	wt, _ := clone.Worktree()
	_, _ = wt.Add("NOTES.md")
	theirs, _ := wt.Commit("Their work", &git.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e.org", When: time.Now()}})
	if err := clone.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0]})

	// The owner confirms "Backup"; before the worker runs, the remote is renamed
	// and its branch changed in the database (the editor-reachable path is closed,
	// but the run itself must not trust the row it loads later).
	h.useUnstartedService()
	out := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: r.ID, Confirm: "Backup"}).(gitmirror.PushOut)
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET name = 'Something else', branch = 'other' WHERE id = ?`, r.ID); err != nil {
		t.Fatal(err)
	}
	h.start()
	p := h.settle(out.Item.ID)
	if p.Status != "error" || p.ErrorCode != "remote_changed" {
		t.Fatalf("push = %+v", p)
	}
	if head(t, dir, "main") != theirs || !head(t, dir, "other").IsZero() {
		t.Error("something was pushed after the remote was changed")
	}
}

func TestAForceWithoutASeenHeadIsNeverAForce(t *testing.T) {
	h := newHarness(t)
	url, _, dir := h.bareRemote()
	r := h.addRemote(url)
	h.start()
	h.push(h.owner)
	other := filepath.Join(t.TempDir(), "other")
	clone, _ := git.PlainClone(other, false, &git.CloneOptions{URL: dir})
	_ = os.WriteFile(filepath.Join(other, "NOTES.md"), []byte("theirs\n"), 0o644)
	wt, _ := clone.Worktree()
	_, _ = wt.Add("NOTES.md")
	theirs, _ := wt.Commit("Their work", &git.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e.org", When: time.Now()}})
	if err := clone.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
	// The check saw no branch (exists=false), and the branch then appeared: a
	// force request must not overwrite it, bundle or no bundle.
	err := h.svc.PushToForTest(context.Background(), h.pid, r.ID, plumbing.ZeroHash, false, true)
	if err == nil {
		t.Fatal("a force push without a seen head went through")
	}
	if head(t, dir, "main") != theirs {
		t.Fatal("the branch that appeared was overwritten")
	}
}

func TestChangingTheAddressCancelsAQueuedPush(t *testing.T) {
	h := newHarness(t)
	url, _, _ := h.bareRemote()
	h.addRemote(url)
	// Not started: the push is queued and holds the slot.
	out := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Branch: ptr("elsewhere")})
	hist := h.must(h.owner, "git.push_get", gitmirror.PushGetIn{ProjectID: h.pid, PushID: out.Item.ID}).(gitmirror.PushItem).Item
	if hist.Status != "cancelled" {
		t.Errorf("the push queued for the old address is %s, want cancelled", hist.Status)
	}
	// The slot is free: a push for the new address can be asked for at once.
	if again := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut); again.AlreadyRunning || again.Item.ID == out.Item.ID {
		t.Errorf("again = %+v", again)
	}
}

func TestAFinishedPushIsNotReportedAsStillRunning(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.PollEvery = time.Hour })
	h.start()
	url, _, _ := h.bareRemote()
	h.addRemote(url)
	for i := 0; i < 40; i++ {
		first := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
		// As soon as the row says it ended, a new request must start a push of its own.
		for {
			p := h.must(h.owner, "git.push_get", gitmirror.PushGetIn{ProjectID: h.pid, PushID: first.Item.ID}).(gitmirror.PushItem).Item
			if p.Status != "running" {
				break
			}
			time.Sleep(time.Millisecond)
		}
		second := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
		if second.AlreadyRunning && second.Item.ID == first.Item.ID {
			t.Fatalf("round %d: a finished push was reported as running", i)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = h.svc.WaitIdle(ctx)
		cancel()
	}
}

// ---- the pre-push check ---------------------------------------------------------------

func TestProseThatQuotesKeysIsNotRefused(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	h.src.Write("The Salt Road", true, []mdoc{
		{"manuscript/01-thriller.md", "chapter", "The Vault", 40, "The note read \"-----BEGIN PRIVATE KEY-----\" and nothing more.\n"},
		{"manuscript/02-docs.md", "chapter", "Setup", 20, "Use AKIAIOSFODNN7EXAMPLE and sk-proj-xxxxxxxxxxxxxxxxxxxxxxxxxxxx as the docs show.\n"},
	})
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	if len(commitsOf(t, dir, "main")) != 1 {
		t.Error("nothing was pushed")
	}
}

func TestACredentialInATitleOrAFileNameIsCaughtToo(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	h.src.Write("The Salt Road", true, []mdoc{{"manuscript/01-a.md", "chapter", "The key sk-abcdefghijklmnopqrstuvwxyz0123", 4, "Plain text.\n"}})
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "secret_in_tree" || !strings.Contains(p.ErrorMessage, "the title") || strings.Contains(p.ErrorMessage, "sk-abcdef") {
		t.Fatalf("push = %+v", p)
	}
	h.src.Write("The Salt Road", true, []mdoc{{"manuscript/ghp_abcdefghijklmnopqrstuvwxyz0123456789.md", "chapter", "Fine", 4, "Plain text.\n"}})
	if p := h.push(h.owner); p.Status != "error" || !strings.Contains(p.ErrorMessage, "the file name") {
		t.Fatalf("push = %+v", p)
	}
	if !head(t, dir, "main").IsZero() {
		t.Error("something was pushed")
	}
}

func TestAnAcknowledgementCoversOneFileWithOneContent(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	body := "Chapter text. -----BEGIN PRIVATE KEY-----\n" + keyBody + "\n"
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-letter.md", "chapter", "The Letter", 9, body}})
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "secret_in_tree" {
		t.Fatalf("push = %+v", p)
	}
	r := h.remote()
	if len(r.Status.Secrets) != 1 || r.Status.Secrets[0].Path != "manuscript/02-the-letter.md" || len(r.Status.Secrets[0].SHA) != 64 {
		t.Fatalf("status.secrets = %+v", r.Status.Secrets)
	}
	f := r.Status.Secrets[0]
	ack := gitmirror.SecretAckIn{ProjectID: h.pid, Path: f.Path, SHA256: f.SHA}
	if _, err := h.call(h.editor, "git.remote.secret_ack", ack); h.code(err) != ops.CodeForbidden {
		t.Errorf("an editor's acknowledgement: %v", err)
	}
	// Only what was reported can be acknowledged, with its hash.
	wrong := ack
	wrong.SHA256 = strings.Repeat("0", 64)
	if _, err := h.call(h.owner, "git.remote.secret_ack", wrong); h.code(err) != ops.CodeInvalid {
		t.Errorf("a wrong hash: %v", err)
	}
	other := ack
	other.Path = "manuscript/01-the-harbour.md"
	if _, err := h.call(h.owner, "git.remote.secret_ack", other); h.code(err) != ops.CodeInvalid {
		t.Errorf("a file that was not reported: %v", err)
	}
	got := h.must(h.owner, "git.remote.secret_ack", ack).(gitmirror.RemoteItem).Item
	if len(got.Acknowledged) != 1 || got.Acknowledged[0].SHA256 != f.SHA || got.Status.State == "blocked" {
		t.Fatalf("after the acknowledgement: %+v", got)
	}
	var detail string
	if err := h.d.Read().QueryRow(`SELECT detail_json FROM audit_log WHERE action = 'git.remote.secret_ack'`).Scan(&detail); err != nil || !strings.Contains(detail, f.SHA) || strings.Contains(detail, keyBody[:30]) {
		t.Errorf("audit = %q %v", detail, err)
	}
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("push after the acknowledgement = %+v", p)
	}
	if len(commitsOf(t, dir, "main")) != 1 {
		t.Fatal("not pushed")
	}
	// The file changes: it is checked again, and the old acknowledgement does not cover it.
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-letter.md", "chapter", "The Letter", 9, body + "One more line.\n"}})
	if p := h.push(h.owner); p.Status != "error" || p.ErrorCode != "secret_in_tree" {
		t.Fatalf("a changed file: %+v", p)
	}
	// Revoking withdraws it for the old content too.
	old := h.remote().Status.Secrets[0]
	h.must(h.owner, "git.remote.secret_ack", gitmirror.SecretAckIn{ProjectID: h.pid, Path: old.Path, SHA256: f.SHA, Revoke: true})
	if n := rowCount(t, h, `SELECT count(*) FROM git_scan_acks`); n != 0 {
		t.Errorf("%d acknowledgements after revoking", n)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-letter.md", "chapter", "The Letter", 9, body}})
	if p := h.push(h.owner); p.Status != "error" {
		t.Errorf("the original content after revoking: %+v", p)
	}
}

func TestARefusalForASecretLiftsWhenTheTextChangesAndIsNotRepeated(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.PollEvery = time.Hour })
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url, func(in *gitmirror.CreateIn) { in.PushMode = "on_save" })
	bad := []mdoc{chapters[0], {"manuscript/02-keys.md", "chapter", "Keys", 4, "-----BEGIN PRIVATE KEY-----\n" + keyBody + "\n"}}
	h.src.Write("The Salt Road", true, bad)
	h.svc.Notify(h.pid)
	settleAuto := func() {
		h.clk.Advance(35 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = h.svc.WaitIdle(ctx)
	}
	settleAuto()
	rows := func() int {
		return len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items)
	}
	if rows() != 1 || h.remote().Status.LastErrorCode != "secret_in_tree" {
		t.Fatalf("after the first save: %d rows, %+v", rows(), h.remote().Status)
	}
	// Saves that leave the offending file alone write nothing more.
	for i := 0; i < 3; i++ {
		h.src.Write("The Salt Road", true, []mdoc{{"manuscript/01-the-harbour.md", "chapter", "The Harbour", 120 + i, fmt.Sprintf("Edit %d.\n", i)}, bad[1]})
		h.svc.Notify(h.pid)
		settleAuto()
	}
	if rows() != 1 {
		t.Errorf("%d rows after saves that did not touch the offending file, want 1", rows())
	}
	// Fixing the text pushes by itself on the next save.
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-keys.md", "chapter", "Keys", 4, "Nothing secret here.\n"}})
	h.svc.Notify(h.pid)
	settleAuto()
	if r := h.remote(); r.Status.State != "synced" || r.Status.LastError != "" {
		t.Fatalf("after fixing the text: %+v", r.Status)
	}
	if len(commitsOf(t, dir, "main")) != 1 {
		t.Error("the fixed text was not pushed")
	}
}

// The bible stays out of the push. The mirror's compendium/ folder (entities,
// research) is never read by the push and never reaches the secret check, so no
// finding, error or history entry names an entity: what a person without access
// to the Compendium could learn from the Git ops is nothing about it.
func TestTheBibleIsNeitherPushedNorScannedNorNamedInAnError(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	const slug = "zed-the-secret-name"
	h.src.Write("The Salt Road", true, chapters)
	// Beside the manuscript the mirror has the bible's projection, with a "key"
	// in an entity that the secret check must never see.
	bible := filepath.Join(h.src.dir, "compendium", "entities", "character")
	if err := os.MkdirAll(bible, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bible, slug+".md"), []byte("-----BEGIN PRIVATE KEY-----\n"+keyBody+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := h.push(h.owner)
	if p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	for name := range filesAt(t, dir, commitsOf(t, dir, "main")[0]) {
		if strings.Contains(name, "compendium") || strings.Contains(name, slug) {
			t.Errorf("pushed %s", name)
		}
	}
	full := h.must(h.owner, "git.push_get", gitmirror.PushGetIn{ProjectID: h.pid, PushID: p.ID}).(gitmirror.PushItem).Item
	for path := range full.Manifest {
		if strings.Contains(path, "compendium") {
			t.Errorf("the manifest of what was sent lists %s", path)
		}
	}
	// If the mirror ever listed a bible file as a document, the push refuses it,
	// and the error does not name the file.
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, append([]mdoc{{"compendium/entities/character/" + slug + ".md", "note", "Zed", 3, "x\n"}}, chapters...))
	bad := h.push(h.owner)
	if bad.Status != "error" || strings.Contains(bad.ErrorMessage, slug) || strings.Contains(bad.ErrorMessage, "compendium") {
		t.Fatalf("push = %+v", bad)
	}
	for _, v := range []any{h.remote(), bad, h.must(h.editor, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid})} {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), slug) {
			t.Errorf("a Git op names the entity: %.200s", b)
		}
	}
	if strings.Contains(h.logs.String(), "BEGIN PRIVATE") {
		t.Error("key material in the log")
	}
}

func TestTheOldSealContextIsAcceptedOnceAndNeverAgain(t *testing.T) {
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/mairead/salt-road.git", func(in *gitmirror.CreateIn) { in.Token = token })
	box := secrets.New([]byte("test-secret-key-test-secret-key-0123456789"))
	legacy := func() {
		old, err := box.Seal([]byte(token), gitmirror.LegacySealContext(rem.ID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET secret_cipher = ? WHERE id = ?`, old, rem.ID); err != nil {
			t.Fatal(err)
		}
	}
	legacy()
	if _, err := gitmirror.CheckCredentials(context.Background(), h.d, box, false); err != nil {
		t.Fatal(err)
	}
	if !h.remote().Enabled || h.remote().Status.LastErrorCode != "" {
		t.Fatal("the first check did not seal the old row again")
	}
	// A later start: an old-context ciphertext (put back with a stolen row, say)
	// gets no second chance, and is not sealed for whatever address the row has.
	legacy()
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET url = 'https://attacker.example.net/x.git' WHERE id = ?`, rem.ID); err != nil {
		t.Fatal(err)
	}
	msg, err := gitmirror.CheckCredentials(context.Background(), h.d, box, false)
	if err != nil || !strings.Contains(msg, "switched off") {
		t.Fatalf("second check = %q %v", msg, err)
	}
	var cipher string
	_ = h.d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = ?`, rem.ID).Scan(&cipher)
	if box.CanOpen(cipher, gitmirror.SealContext(rem.ID, "https://attacker.example.net/x.git")) {
		t.Error("an old-context ciphertext was sealed for another address")
	}
	if h.remote().Enabled {
		t.Error("the remote with an unreadable credential is still on")
	}
}

func TestAnAcknowledgedTitleStaysAcknowledgedWhenTheManifestChanges(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	title := "The key sk-abcdefghijklmnopqrstuvwxyz0123"
	docs := func(words int) []mdoc {
		return []mdoc{{"manuscript/01-a.md", "chapter", title, words, "Plain text.\n"}, chapters[1]}
	}
	h.src.Write("The Salt Road", true, docs(4))
	if p := h.push(h.owner); p.Status != "error" {
		t.Fatalf("push = %+v", p)
	}
	secs := h.remote().Status.Secrets
	if len(secs) != 1 || !strings.Contains(secs[0].Path, "(the title)") {
		t.Fatalf("findings = %+v: the manifest, which repeats every title, must not be a finding of its own", secs)
	}
	h.must(h.owner, "git.remote.secret_ack", gitmirror.SecretAckIn{ProjectID: h.pid, Path: secs[0].Path, SHA256: secs[0].SHA})
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("after the acknowledgement: %+v", p)
	}
	// Another save changes the manifest (a word count); the acknowledgement still holds.
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, docs(9))
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("a save after the acknowledgement: %+v", p)
	}
	if len(commitsOf(t, dir, "main")) != 2 {
		t.Error("the second save was not pushed")
	}
}

func TestDiscardingAFinishedImportRemovesItsKeptCloneAndExpiryDoesNotNeedTheJob(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.KeepImportDays = 7; o.PollEvery = 1000 * time.Hour })
	h.start()
	url := makeRepo(t, map[string]string{"01-a.md": "Some text here.\n"})
	apply := func() (string, string) {
		job := h.waitJob(h.owner, h.startImport(h.owner, gitmirror.GitPreviewIn{URL: url}).ID)
		out := h.must(h.owner, "import.git_apply", gitmirror.GitApplyIn{JobID: job.ID, Token: job.Summary.Token}).(imports.ApplyOut)
		return job.ID, out.ProjectID
	}
	jobID, pid := apply()
	kept := filepath.Join(h.dataDir, "imports", "p-"+pid)
	if _, err := os.Stat(kept); err != nil {
		t.Fatal(err)
	}
	h.must(h.owner, "import.git_discard", gitmirror.GitJobIn{JobID: jobID})
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Errorf("discarding a finished import left its kept clone: %v", err)
	}
	var path string
	_ = h.d.Read().QueryRow(`SELECT kept_copy_path FROM project_sources WHERE project_id = ?`, pid).Scan(&path)
	if path != "" {
		t.Errorf("the source still points at %q", path)
	}
	// Expiry works from the source records: with the job row gone, the clone still expires.
	_, pid2 := apply()
	if _, err := h.d.Writer().Exec(`DELETE FROM import_jobs`); err != nil {
		t.Fatal(err)
	}
	kept2 := filepath.Join(h.dataDir, "imports", "p-"+pid2)
	h.stampSources()
	h.clk.Advance(8 * 24 * time.Hour)
	h.svc.ExpireKeptImportsForTest(context.Background())
	if _, err := os.Stat(kept2); !os.IsNotExist(err) {
		t.Errorf("a kept clone with no job row never expires: %v", err)
	}
}

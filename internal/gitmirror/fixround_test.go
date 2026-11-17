package gitmirror_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// ---- F2, F3: the restore pause -------------------------------------------------------------------------------

func TestARestorePauseSurvivesAnUnreadableRemoteWhoseKeyComesBack(t *testing.T) {
	// A remote that was already off for unreadable credentials when the backup was made, restored with the right
	// key and started: it stays off, however many times the start-up check runs.
	h := newHarness(t)
	r := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Name = "Old"; in.Token = "tok_abcdefghijklmnopqrstuvwxyz" })
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET enabled = 0, state = 'blocked', last_error_code = 'credentials_unreadable', last_error = 'x' WHERE id = ?`, r.ID); err != nil {
		t.Fatal(err)
	}
	pausedByRestore(t, h)
	orig := secrets.New([]byte("test-secret-key-test-secret-key-0123456789"))
	for i := 0; i < 3; i++ {
		if _, err := gitmirror.CheckCredentials(t.Context(), h.d, orig, false); err != nil {
			t.Fatal(err)
		}
		if got := h.remoteByID(r.ID); got.Enabled || !got.RestorePaused {
			t.Fatalf("start %d: the remote is %+v", i+1, got)
		}
	}
	// Only the owner switching it on lifts the pause (the credentials are readable, so nothing else stops it).
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Token: ptr("tok_a_new_one_abcdefghijklmnopqrstuvwxyz")})
	if got := h.remoteByID(r.ID); got.Enabled || !got.RestorePaused {
		t.Fatalf("entering a token lifted the pause: %+v", got)
	}
	on := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Enabled: ptr(true)}).(gitmirror.RemoteItem).Item
	if !on.Enabled || on.RestorePaused {
		t.Errorf("after the owner switched it on: %+v", on)
	}
}

func TestAFailingStepNeverLeavesRemotesAndTriggersOn(t *testing.T) {
	// Through the real restore, with a step that fails and is ordered before every other.
	h := newHarness(t)
	rem := h.addRemote("https://forge.example.org/o/r.git", func(in *gitmirror.CreateIn) { in.Token = "tok_abcdefghijklmnopqrstuvwxyz" })
	h.setTriggerUnsent(rem.ID)
	var dbPath string
	if err := h.d.Read().QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&dbPath); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := backup.Create(context.Background(), dbPath, h.dataDir, backup.Options{Out: archive}); err != nil {
		t.Fatal(err)
	}
	saved := backup.PostRestore
	t.Cleanup(func() { backup.PostRestore = saved })
	backup.PostRestore = append([]func(context.Context, *db.DB, []byte) (string, error){
		func(context.Context, *db.DB, []byte) (string, error) { return "", errors.New("a step that fails") },
	}, saved...)
	dir := t.TempDir()
	res, err := backup.Restore(context.Background(), archive, dir, backup.RestoreOptions{SecretKey: []byte("test-secret-key-test-secret-key-0123456789")})
	if err == nil || !strings.Contains(err.Error(), "a step that fails") {
		t.Fatalf("the failing step is reported: %v", err)
	}
	_ = res
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var en, paused, ten int
	if err := d.Read().QueryRow(`SELECT enabled, restore_paused FROM git_remotes WHERE id = ?`, rem.ID).Scan(&en, &paused); err != nil {
		t.Fatal(err)
	}
	_ = d.Read().QueryRow(`SELECT enabled FROM git_triggers WHERE remote_id = ?`, rem.ID).Scan(&ten)
	if en != 0 || paused != 1 || ten != 0 {
		t.Errorf("after a restore whose first step failed: remote enabled=%d paused=%d, trigger enabled=%d", en, paused, ten)
	}
}

func (h *harness) setTriggerUnsent(remoteID string) {
	h.t.Helper()
	h.setTrigger(remoteID, "https://ci.example.org/hook")
}

// ---- F4: a public push needs a current mirror ---------------------------------------------------------------

func (f *fakeSource) Current(context.Context, string) (bool, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return !f.behind, nil
}

func (f *fakeSource) setBehind(b bool) {
	f.mu.Lock()
	f.behind = b
	f.mu.Unlock()
}

func TestAPublicPushWaitsForAMirrorThatIsCurrent(t *testing.T) {
	h := newHarness(t)
	h.start()
	pubURL, _, pubDir := h.bareRemote()
	privURL, _, privDir := h.bareRemote()
	pub := h.addRemote(pubURL, func(in *gitmirror.CreateIn) { in.Name = "Public"; in.Audience = "" })
	priv := h.addRemote(privURL, func(in *gitmirror.CreateIn) { in.Name = "Private" })
	h.src.setBehind(true)
	p := h.pushRemote(pub.ID)
	if p.Status != "error" || p.ErrorCode != "mirror_behind" || !head(t, pubDir, "main").IsZero() {
		t.Fatalf("a public push from a mirror that is behind: %+v", p)
	}
	if got := h.remoteByID(pub.ID); got.Status.NextAttemptAt == "" || got.Status.State == "blocked" {
		t.Errorf("it is retried, not parked: %+v", got.Status)
	}
	// The full copy is the owner's own, and is not held back.
	if p := h.pushRemote(priv.ID); p.Status != "ok" || head(t, privDir, "main").IsZero() {
		t.Errorf("private push: %+v", p)
	}
	// Force push and the save trigger too.
	if _, err := h.call(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: pub.ID, Confirm: "Public"}); err != nil {
		t.Fatal(err)
	}
	_ = h.svc.WaitIdle(t.Context())
	if !head(t, pubDir, "main").IsZero() {
		t.Errorf("a force push from a stale mirror")
	}
	// A plan says so.
	h.src.setBehind(false)
	if p := h.pushRemote(pub.ID); p.Status != "ok" {
		t.Errorf("once current: %+v", p)
	}
}

func TestAPublishPlanAndAnEditionRefuseAStaleMirror(t *testing.T) {
	h, f, rem, st := setupRelease(t, "forgejo")
	acct := h.must(h.owner, "repo.account.list", struct{}{}).(gitmirror.AccountList).Items[0]
	h.src.setBehind(true)
	pl := h.plan(h.planIn(acct.ID, func(i *gitmirror.RepoPlanIn) { i.Name = "another" }))
	if len(pl.Blockers) == 0 || pl.Blockers[0].Code != "mirror_not_ready" {
		t.Errorf("plan = %+v", pl.Blockers)
	}
	rel := h.release(rem.ID, "v1", "md_bundle")
	if rel.Status != "error" || rel.ErrorCode != "mirror_behind" || f.release("mairead", "salt-road", "v1") != nil {
		t.Errorf("an edition from a stale mirror: %+v", rel)
	}
	_ = st
}

// ---- F6 ---------------------------------------------------------------------------------------------------------

func TestOnlyDoneIsForAPublicRemoteAndANoOpDoesNotWipeHistory(t *testing.T) {
	h := newHarness(t)
	h.start()
	u, _, _ := h.bareRemote()
	if _, err := h.call(h.owner, "git.remote.create", gitmirror.CreateIn{ProjectID: h.pid, Name: "X", URL: u, Audience: "private", OnlyDone: ptr(true)}); h.code(err) != ops.CodeInvalid {
		t.Errorf("only_done on a private remote: %v", err)
	}
	r := h.addRemote(u, func(in *gitmirror.CreateIn) { in.Name = "Backup" })
	if _, err := h.call(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, OnlyDone: ptr(true)}); h.code(err) != ops.CodeInvalid {
		t.Errorf("only_done set on a private remote: %v", err)
	}
	h.pushRemote(r.ID)
	sha := h.remoteByID(r.ID).Status.LocalSHA
	// Saying what is already so changes nothing and keeps the history.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Audience: ptr("private"), OnlyDone: ptr(false)})
	if got := h.remoteByID(r.ID); got.Status.LocalSHA != sha || got.Status.State != "synced" {
		t.Errorf("a no-op update wiped the history: %+v", got.Status)
	}
	// Making a public remote private takes only_done with it, and is one change.
	u2, _, _ := h.bareRemote()
	pub := h.addRemote(u2, func(in *gitmirror.CreateIn) { in.Name = "Pub"; in.Audience = ""; in.OnlyDone = ptr(true) })
	got := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: pub.ID, Audience: ptr("private"), ConfirmName: "Pub"}).(gitmirror.RemoteItem).Item
	if got.Audience != "private" || got.OnlyDone {
		t.Errorf("after making it private: %+v", got)
	}
}

// ---- F7 -----------------------------------------------------------------------------------------------------------

func TestConnectingAnAccountAnswersTheSameWhateverWentWrong(t *testing.T) {
	type answer struct {
		code    ops.Code
		field   string
		message string
		retry   int
		data    string
	}
	get := func(h *harness, in gitmirror.AccountConnectIn) answer {
		_, err := h.call(h.owner, "repo.account.connect", in)
		if err == nil {
			t.Fatal("connected")
		}
		e := ops.AsError(err)
		return answer{e.Code, e.Field, e.Message, e.RetryAfterSeconds, mustJSON(t, e.Data)}
	}
	var all []answer
	for _, mode := range []string{"unauth", "5xx", "huge", "malformed", "ratelimit", "ratelimit429", "sso", "redirect"} {
		f := newFakeHost(t, "forgejo")
		f.mu.Lock()
		f.mode, f.redirectTo = mode, "https://127.0.0.1:1/x"
		f.mu.Unlock()
		all = append(all, get(newHarness(t), gitmirror.AccountConnectIn{Provider: "forgejo", Host: f.host(), Token: f.token}))
	}
	h := newHarness(t)
	all = append(all, get(h, gitmirror.AccountConnectIn{Provider: "forgejo", Host: "127.0.0.1:1", Token: "tok_abcdefghijklmnopqrstuvwxyz"})) // nothing listens
	all = append(all, get(h, gitmirror.AccountConnectIn{Provider: "forgejo", Host: "no-such-host.invalid", Token: "tok_abcdefghijklmnopqrstuvwxyz"}))
	hp := newHarness(t, func(o *gitmirror.Options) { o.AllowPrivate = false })
	all = append(all, get(hp, gitmirror.AccountConnectIn{Provider: "forgejo", Host: "127.0.0.1:1", Token: "tok_abcdefghijklmnopqrstuvwxyz"})) // not allowed
	for i, a := range all {
		if a != all[0] {
			t.Errorf("answer %d differs: %+v from %+v", i, a, all[0])
		}
	}
}

// ---- F9 -------------------------------------------------------------------------------------------------------------

func TestAPlanTokenIsForThisProjectThisPersonAndAShortTime(t *testing.T) {
	h, _, acct := setupPublish(t, "forgejo")
	in := h.planIn(acct.ID)
	pl := h.plan(in)
	// Another project with the same layout: its plan token is not this project's.
	pid2 := h.newProject("Another")
	in2 := in
	in2.ProjectID = pid2
	pl2 := h.must(h.owner, "repo.plan", in2).(gitmirror.RepoPlan)
	if pl2.PlanToken == pl.PlanToken {
		t.Errorf("the same token for two projects")
	}
	other := publishFrom(in, pl2.PlanToken, "")
	if _, err := h.call(h.owner, "repo.publish", other); h.code(err) != ops.CodeConflict {
		t.Errorf("another project's token: %v", err)
	}
	if _, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken[:len(pl.PlanToken)-4]+"AAAA", "")); h.code(err) != ops.CodeConflict {
		t.Errorf("a damaged token: %v", err)
	}
	// Too old.
	h.clk.Advance(31 * time.Minute)
	if _, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, "")); h.code(err) != ops.CodeConflict || !strings.Contains(err.Error(), "different now") {
		t.Errorf("an old plan: %v", err)
	}
	// A fresh one goes.
	h.publish(in, "")
}

func TestATestOfAPausedTriggerSendsNothing(t *testing.T) {
	rc := newReceiver(t)
	h := triggerHarness(t, rc, 2*time.Second)
	u, _, _ := h.bareRemote()
	r := h.addRemote(u)
	h.setTrigger(r.ID, rc.url("/hook"))
	pausedByRestore(t, h)
	if _, err := h.call(h.owner, "git.trigger.test", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: r.ID}); h.code(err) != ops.CodeConflict {
		t.Errorf("a test of a paused trigger: %v", err)
	}
	if n := len(rc.received()); n != 0 {
		t.Errorf("the receiver was called %d times", n)
	}
}

func TestTheCreatedButIncompleteMessageSaysWhatWasAndWasNotChanged(t *testing.T) {
	h, _, acct := setupPublish(t, "forgejo")
	in := h.planIn(acct.ID)
	pl := h.plan(in)
	gitmirror.SetAfterCreateHook(func() error { return errTest })
	t.Cleanup(func() { gitmirror.SetAfterCreateHook(nil) })
	_, err := h.call(h.owner, "repo.publish", publishFrom(in, pl.PlanToken, ""))
	e := ops.AsError(err)
	if !strings.Contains(e.Message, "was created") || !strings.Contains(e.Message, "No remote was registered") || !strings.Contains(e.Message, "Nothing was pushed") || strings.Contains(e.Message, "Nothing else was changed") || e.Data["remote_registered"] != false {
		t.Errorf("message = %q data %v", e.Message, e.Data)
	}
}

// ---- the cancel and save race --------------------------------------------------------------------------------------

func TestAPushThatStartedUnderTheOldAudienceLeavesNothingAfterTheChange(t *testing.T) {
	h := newHarness(t)
	h.start()
	u, _, dir := h.bareRemote()
	r := h.addRemote(u, func(in *gitmirror.CreateIn) { in.Name = "Backup" })
	// A job has read the remote (audience private) and is paused there; the owner then makes it public.
	var once sync.Once
	reached, release := make(chan struct{}), make(chan struct{})
	hook := func(stage string) {
		if stage == "loaded" {
			once.Do(func() { close(reached); <-release })
		}
	}
	gitmirror.SetExecHook(hook)
	t.Cleanup(func() { gitmirror.SetExecHook(nil) })
	h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: r.ID})
	<-reached
	done := make(chan error, 1)
	go func() {
		_, err := h.call(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Audience: ptr("public")})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond) // the update is waiting for the job to leave
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = h.svc.WaitIdle(t.Context())
	if repo, err := h.svc.LocalRepo(h.pid, r.ID); err == nil {
		if ref, rerr := repo.Reference("refs/heads/scriobh", true); rerr == nil {
			t.Errorf("the local repository holds a commit (%s) built under the old audience", ref.Hash())
		}
	}
	if got := h.remoteByID(r.ID); got.Status.LocalSHA != "" || got.Status.LastCommitSHA != "" || got.Audience != "public" {
		t.Errorf("remote = %+v", got.Status)
	}
	if !head(t, dir, "main").IsZero() {
		t.Errorf("the cancelled job pushed")
	}
	// The next push is built under the new audience and carries no note.
	p := h.pushRemote(r.ID)
	if p.Status != "ok" || strings.Contains(everythingIn(t, dir), "Maybe a storm") {
		t.Errorf("push = %+v", p)
	}
}

func TestAPushQueuedUnderTheOldAudienceDoesNothingAfterTheChange(t *testing.T) {
	h := newHarness(t)
	u, _, dir := h.bareRemote()
	r := h.addRemote(u, func(in *gitmirror.CreateIn) { in.Name = "Backup" })
	h.useUnstartedService()
	h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: r.ID}) // queued, not running
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Audience: ptr("public")})
	h.start()
	_ = h.svc.WaitIdle(t.Context())
	if _, err := h.svc.LocalRepo(h.pid, r.ID); err == nil {
		t.Errorf("a local repository exists for a push that was queued under the old audience")
	}
	if !head(t, dir, "main").IsZero() {
		t.Errorf("it pushed")
	}
}

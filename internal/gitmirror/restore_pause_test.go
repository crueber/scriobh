package gitmirror_test

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// A restore makes the copy that was restored a place that must not publish until its owner says so: the
// remotes and the triggers come back switched off, as the webhooks do.
func TestARestorePausesEveryRemoteAndTriggerUntilTheOwnerSwitchesThemOn(t *testing.T) {
	rc := newReceiver(t)
	h := triggerHarness(t, rc, 2*time.Second)
	u1, _, d1 := h.bareRemote()
	u2, _, d2 := h.bareRemote()
	a := h.addRemote(u1, func(in *gitmirror.CreateIn) { in.Name = "A"; in.PushMode = "on_save" })
	b := h.addRemote(u2, func(in *gitmirror.CreateIn) { in.Name = "B"; in.Audience = ""; in.PushMode = "on_save" })
	h.setTrigger(a.ID, rc.url("/hook"))
	rc.set(500, "")
	h.pushRemote(a.ID)
	h.pushRemote(b.ID)
	waitFor(t, "a pending delivery", func() bool { ds := h.deliveries(a.ID); return len(ds) == 1 && ds[0].State == "pending" })
	// One remote already off for another reason keeps that reason.
	u3, _, _ := h.bareRemote()
	c := h.addRemote(u3, func(in *gitmirror.CreateIn) { in.Name = "C" })
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET enabled = 0, state = 'blocked', last_error_code = 'credentials_unreadable', last_error = 'x' WHERE id = ?`, c.ID); err != nil {
		t.Fatal(err)
	}
	before1, before2 := head(t, d1, "main"), head(t, d2, "main")

	msg := pausedByRestore(t, h)
	if !strings.Contains(msg, "2 remote(s) and 1 trigger(s) were paused") {
		t.Errorf("the restore says: %q", msg)
	}
	for _, id := range []string{a.ID, b.ID} {
		r := h.remoteByID(id)
		if r.Enabled || r.Status.State != "blocked" || r.Status.LastErrorCode != "paused_by_restore" || r.Status.LastError != gitmirror.PausedRemoteByRestore {
			t.Errorf("remote %s after a restore: %+v", r.Name, r)
		}
	}
	if got := h.remoteByID(c.ID); got.Status.LastErrorCode != "credentials_unreadable" {
		t.Errorf("a remote that was off for another reason: %+v", got.Status)
	}
	// Writing goes on; nothing is pushed, by the save trigger or by hand, and no release is made.
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	h.svc.Notify(h.pid)
	h.clk.Advance(gitmirror.DefaultDebounce + time.Minute)
	_ = h.svc.WaitIdle(t.Context())
	if head(t, d1, "main") != before1 || head(t, d2, "main") != before2 {
		t.Errorf("a paused remote pushed")
	}
	if _, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: a.ID}); h.code(err) != ops.CodeConflict {
		t.Errorf("a push by hand to a paused remote: %v", err)
	}
	if _, err := h.call(h.owner, "repo.release", gitmirror.RepoReleaseIn{ProjectID: h.pid, RemoteID: a.ID, Tag: "v1", Format: "epub"}); h.code(err) != ops.CodeConflict {
		t.Errorf("a release from a paused remote: %v", err)
	}
	n := len(rc.received())
	rc.set(204, "")
	h.clk.Advance(2 * time.Hour)
	h.svc.Check(t.Context())
	if len(rc.received()) != n {
		t.Errorf("a paused trigger called its receiver")
	}
	// Only the owner switches one on again, and then it pushes.
	if _, err := h.call(h.editor, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: a.ID, Enabled: ptr(true)}); h.code(err) != ops.CodeForbidden {
		t.Errorf("an editor switching a paused remote on: %v", err)
	}
	on := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: a.ID, Enabled: ptr(true)}).(gitmirror.RemoteItem).Item
	if !on.Enabled || on.Status.LastErrorCode != "" || on.Status.State == "blocked" {
		t.Fatalf("after switching on: %+v", on.Status)
	}
	if p := h.pushRemote(a.ID); p.Status != "ok" || head(t, d1, "main") == before1 {
		t.Errorf("push after switching on: %+v", p)
	}
	if h.remoteByID(b.ID).Enabled {
		t.Errorf("B came back by itself")
	}
}

func TestNoOneButSwitchingItOnLiftsARestorePause(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, _ := h.bareRemote()
	r := h.addRemote(url, func(in *gitmirror.CreateIn) { in.Name = "A"; in.Token = ""; in.URL = url })
	pausedByRestore(t, h)
	if _, err := h.call(h.editor, "git.remote.test", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: r.ID}); err != nil {
		t.Fatal(err)
	}
	if got := h.remoteByID(r.ID); got.Enabled || got.Status.LastErrorCode != "paused_by_restore" || got.Status.State != "blocked" {
		t.Errorf("a connection test lifted the pause: %+v", got)
	}
	// A key made, a token entered: the remote is still paused and still says why.
	u2 := "https://forge.example.org/o/r.git"
	r2 := h.addRemote(u2, func(in *gitmirror.CreateIn) { in.Name = "B"; in.Token = "tok_abcdefghijklmnopqrstuvwxyz" })
	pausedByRestore(t, h)
	got := h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r2.ID, Token: ptr("tok_another_abcdefghijklmnopqrstuvwxyz")}).(gitmirror.RemoteItem).Item
	if got.Enabled || got.Status.LastErrorCode != "paused_by_restore" {
		t.Errorf("a new token lifted the pause: %+v", got.Status)
	}
}

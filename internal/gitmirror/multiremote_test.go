package gitmirror_test

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

func TestARemoteEachWithItsOwnBranchFolderAndHistory(t *testing.T) {
	h := newHarness(t)
	h.start()
	aURL, _, aDir := h.bareRemote()
	bURL, _, bDir := h.bareRemote()
	a := h.addRemote(aURL, func(in *gitmirror.CreateIn) { in.Name = "Backup"; in.Branch = "backup" })
	b := h.addRemote(bURL, func(in *gitmirror.CreateIn) { in.Name = "Mirror"; in.Audience = ""; in.PathPrefix = "book" })
	for _, r := range []gitmirror.Remote{a, b} {
		if p := h.pushRemote(r.ID); p.Status != "ok" {
			t.Fatalf("%s: %+v", r.Name, p)
		}
	}
	if got := strings.Join(treeFilesOf2(t, aDir, "backup"), ","); !strings.Contains(got, "notes/01-ideas.md") || strings.Contains(got, "book/") {
		t.Errorf("backup tree = %s", got)
	}
	if got := strings.Join(treeFilesOf2(t, bDir, "main"), ","); got != "book/manuscript/01-the-harbour.md,book/manuscript/02-the-road.md,book/scriobh.json" {
		t.Errorf("mirror tree = %s", got)
	}
	if !head(t, aDir, "main").IsZero() || !head(t, bDir, "backup").IsZero() { // neither has the other's branch
		t.Errorf("a branch of one remote is in the other")
	}
	// The sixth is refused, and so is a name that is taken.
	for i := 3; i <= 5; i++ {
		u, _, _ := h.bareRemote()
		h.addRemote(u, func(in *gitmirror.CreateIn) { in.Name = string(rune('A'+i)) + " remote" })
	}
	u, _, _ := h.bareRemote()
	if _, err := h.call(h.owner, "git.remote.create", gitmirror.CreateIn{Audience: "private", ProjectID: h.pid, Name: "Sixth", URL: u}); h.code(err) != ops.CodeConflict {
		t.Errorf("a sixth remote: %v", err)
	}
	if _, err := h.call(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, Branch: ptr("x")}); h.code(err) != ops.CodeInvalid {
		t.Errorf("an update that names no remote while there are several: %v", err)
	}
}

func treeFilesOf2(t *testing.T, dir, branch string) []string {
	t.Helper()
	out := gitOut(t, dir, "ls-tree", "-r", "--name-only", branch)
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files
}

func TestOneRemoteFailingNeverBlocksAnother(t *testing.T) {
	h := newHarness(t)
	h.start()
	okURL, _, okDir := h.bareRemote()
	good := h.addRemote(okURL, func(in *gitmirror.CreateIn) { in.Name = "Good"; in.PushMode = "on_save" })
	bad := h.addRemote("file:///does/not/exist.git", func(in *gitmirror.CreateIn) { in.Name = "Down"; in.PushMode = "on_save" })

	step := func() {
		t.Helper()
		h.svc.Notify(h.pid)
		h.clk.Advance(gitmirror.DefaultDebounce + time.Second)
		if err := h.svc.WaitIdle(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	step()
	g, b := h.remoteByID(good.ID), h.remoteByID(bad.ID)
	if g.Status.State != "synced" || g.Status.FailCount != 0 || head(t, okDir, "main").IsZero() {
		t.Fatalf("good = %+v", g.Status)
	}
	if b.Status.State == "synced" || b.Status.FailCount != 1 || b.Status.LastError == "" {
		t.Fatalf("bad = %+v", b.Status)
	}
	first := head(t, okDir, "main")

	// More writing: the good remote keeps pushing while the failing one waits out its own backoff, untouched.
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	step()
	g, b2 := h.remoteByID(good.ID), h.remoteByID(bad.ID)
	if head(t, okDir, "main") == first || g.Status.State != "synced" {
		t.Errorf("the good remote did not push while the other was down: %+v", g.Status)
	}
	if b2.Status.FailCount != b.Status.FailCount || b2.Status.NextAttemptAt != b.Status.NextAttemptAt {
		t.Errorf("the failing remote was tried again inside its backoff: %+v then %+v", b.Status, b2.Status)
	}
	// Each remote's failure is its own: nothing about it on the good one, and the two histories differ.
	if g.Status.LastError != "" || len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid, RemoteID: good.ID}).(gitmirror.PushHistoryOut).Items) != 2 {
		t.Errorf("good remote's history: %+v", g.Status)
	}
	// Past the backoff the failing one is tried once more and fails again with a longer wait; the good one is not pushed again for nothing.
	h.clk.Advance(2 * time.Minute)
	h.svc.Check(t.Context())
	if err := h.svc.WaitIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
	b3 := h.remoteByID(bad.ID)
	if b3.Status.FailCount != b.Status.FailCount+1 {
		t.Errorf("after the backoff: %+v", b3.Status)
	}
	if h := h.remoteByID(good.ID); h.Status.FailCount != 0 {
		t.Errorf("good = %+v", h.Status)
	}
}

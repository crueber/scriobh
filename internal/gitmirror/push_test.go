package gitmirror_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
)

// commitsOf lists a repository's branch history, newest first.
func commitsOf(t *testing.T, dir, branch string) []*object.Commit {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return nil
	}
	it, err := repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	var out []*object.Commit
	_ = it.ForEach(func(c *object.Commit) error { out = append(out, c); return nil })
	return out
}

// filesAt reads a commit's tree: path -> content.
func filesAt(t *testing.T, dir string, c *object.Commit) map[string]string {
	t.Helper()
	tree, err := c.Tree()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	_ = tree.Files().ForEach(func(f *object.File) error {
		s, err := f.Contents()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = s
		return nil
	})
	return out
}

func TestFirstPushPublishesTheLayoutAndNothingElse(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	p := h.push(h.owner)
	if p.Status != "ok" || p.Outcome != "pushed" {
		t.Fatalf("first push = %+v", p)
	}
	cs := commitsOf(t, dir, "main")
	if len(cs) != 1 {
		t.Fatalf("remote has %d commits, want 1", len(cs))
	}
	files := filesAt(t, dir, cs[0])
	var names []string
	for n := range files {
		names = append(names, n)
	}
	want := []string{"manuscript/01-the-harbour.md", "manuscript/02-the-road.md", "notes/01-ideas.md", "scriobh.json"}
	if strings.Join(sortedKeys(files), ",") != strings.Join(want, ",") {
		t.Fatalf("pushed files = %v, want %v", sortedKeys(files), want)
	}
	if files["manuscript/01-the-harbour.md"] != "The quiet harbour.\n" {
		t.Errorf("chapter text = %q", files["manuscript/01-the-harbour.md"])
	}
	for _, n := range names {
		if strings.Contains(n, "stray") || strings.Contains(n, ".state") || strings.HasSuffix(n, "README.md") || n == "project.json" {
			t.Errorf("pushed %s, which the manifest does not list", n)
		}
	}
	// The manifest is the mirror's project.json, published under the importer's name.
	mirrorManifest, _ := os.ReadFile(filepath.Join(h.src.dir, "project.json"))
	if files["scriobh.json"] != string(mirrorManifest) {
		t.Error("scriobh.json is not the mirror's project.json, byte for byte")
	}
	c := cs[0]
	if c.Author.Name != "Scríobh" || c.Author.Email != "noreply@scriobh" || c.Committer.Email != "noreply@scriobh" {
		t.Errorf("identity = %q <%s>", c.Author.Name, c.Author.Email)
	}
	if !strings.HasPrefix(c.Message, "The Salt Road · first push: 205 words\n") {
		t.Errorf("message = %q", c.Message)
	}
	if p.CommitSHA != c.Hash.String() || p.FilesChanged != 4 {
		t.Errorf("push row = %+v", p)
	}
	for _, it := range h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items {
		if len(it.Manifest) != 0 {
			t.Error("the history list carries manifests")
		}
	}
	full := h.must(h.owner, "git.push_get", gitmirror.PushGetIn{ProjectID: h.pid, PushID: p.ID}).(gitmirror.PushItem).Item
	if len(full.Manifest) != 4 || full.Manifest["manuscript/02-the-road.md"] == "" {
		t.Errorf("manifest = %v", full.Manifest)
	}
	r := h.remote()
	if r.Status.State != "synced" || !r.Status.InSync || r.Status.LastCommitSHA != c.Hash.String() || r.Status.Behind {
		t.Errorf("status = %+v", r.Status)
	}
}

func TestNoChangeNoPushAndASecondPushAddsOneCommit(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	first := h.push(h.owner)

	again := h.push(h.owner)
	if again.Status != "ok" || again.Outcome != "up_to_date" || again.FilesChanged != 0 {
		t.Fatalf("a push with no change = %+v", again)
	}
	if n := len(commitsOf(t, dir, "main")); n != 1 {
		t.Fatalf("a no-op push left %d commits, want 1", n)
	}

	h.clk.Advance(time.Hour)
	changed := append([]mdoc{}, chapters...)
	changed[1] = mdoc{"manuscript/02-the-road.md", "chapter", "The Road", 294, "Salt on the wind and a long road.\n"}
	h.src.Write("The Salt Road", true, changed)
	second := h.push(h.owner)
	if second.Status != "ok" || second.Outcome != "pushed" {
		t.Fatalf("second push = %+v", second)
	}
	cs := commitsOf(t, dir, "main")
	if len(cs) != 2 {
		t.Fatalf("remote has %d commits, want 2", len(cs))
	}
	if cs[0].ParentHashes[0].String() != first.CommitSHA || second.ParentSHA != first.CommitSHA {
		t.Errorf("parent = %v, want %s", cs[0].ParentHashes, first.CommitSHA)
	}
	wantSubject := "The Salt Road · ch 2: +214 words"
	if !strings.HasPrefix(cs[0].Message, wantSubject+"\n\n") {
		t.Errorf("message = %q, want it to start with %q", cs[0].Message, wantSubject)
	}
	if !strings.Contains(cs[0].Message, "changed: manuscript/02-the-road.md (“The Road”) +214\n") {
		t.Errorf("body = %q", cs[0].Message)
	}
	if second.FilesChanged != 2 { // the chapter and the manifest
		t.Errorf("files changed = %d, want 2", second.FilesChanged)
	}
	if !cs[0].Committer.When.After(cs[1].Committer.When) {
		t.Error("the second commit is not later than the first")
	}
	// Removing a document and adding another is described, in a fixed order.
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/03-new.md", "chapter", "New", 10, "Fresh.\n"}, chapters[2]})
	third := h.push(h.owner)
	msg := commitsOf(t, dir, "main")[0].Message
	if !strings.HasPrefix(msg, "The Salt Road · 2 chapters: -284 words\n") || !strings.Contains(msg, "added: manuscript/03-new.md") ||
		strings.Index(msg, "added:") > strings.Index(msg, "removed:") || !strings.Contains(msg, "removed: manuscript/02-the-road.md (“The Road”) -294") {
		t.Errorf("message = %q (%+v)", msg, third)
	}
}

func TestSnapshotsAreDeterministic(t *testing.T) {
	files := map[string][]byte{"scriobh.json": []byte("{}\n"), "manuscript/01-a.md": []byte("a\n"), "manuscript/01-a/00-a.md": []byte("b\n"), "manuscript/01-a-b.md": []byte("c\n"), "notes/n.md": []byte("n\n")}
	h1, err := gitmirror.TreeHashOf(files)
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := gitmirror.TreeHashOf(map[string][]byte{"notes/n.md": []byte("n\n"), "manuscript/01-a-b.md": []byte("c\n"), "manuscript/01-a/00-a.md": []byte("b\n"), "manuscript/01-a.md": []byte("a\n"), "scriobh.json": []byte("{}\n")})
	if h1 != h2 {
		t.Errorf("the same files gave two trees: %s %s", h1, h2)
	}
	files["manuscript/01-a.md"] = []byte("a changed\n")
	if h3, _ := gitmirror.TreeHashOf(files); h3 == h1 {
		t.Error("a change did not change the tree")
	}
	// Two remotes built from one snapshot hold the same tree, and the commit
	// message does not depend on who or where.
	var trees, msgs []string
	for i := 0; i < 2; i++ {
		h := newHarness(t)
		h.start()
		url, _, dir := h.bareRemote()
		h.addRemote(url)
		h.push(h.owner)
		c := commitsOf(t, dir, "main")[0]
		trees = append(trees, c.TreeHash.String())
		msgs = append(msgs, c.Message)
	}
	if trees[0] != trees[1] || msgs[0] != msgs[1] {
		t.Errorf("one snapshot gave two results: %v %q", trees, msgs)
	}
}

func TestRemoteAheadIsRefusedAndNothingIsOverwritten(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, remoteRepo, dir := h.bareRemote()
	h.addRemote(url)
	h.push(h.owner)

	// Someone else commits to the remote: a clone adds a commit and pushes it.
	other := filepath.Join(t.TempDir(), "other")
	clone, err := git.PlainClone(other, false, &git.CloneOptions{URL: dir})
	if err != nil {
		t.Fatal(err)
	}
	_ = remoteRepo
	if err := os.WriteFile(filepath.Join(other, "manuscript", "01-the-harbour.md"), []byte("Someone else rewrote this.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := clone.Worktree()
	_, _ = wt.Add("manuscript/01-the-harbour.md")
	sig := &object.Signature{Name: "Collaborator", Email: "c@example.org", When: time.Now()}
	theirs, err := wt.Commit("Fix the harbour scene", &git.CommitOptions{Author: sig})
	if err != nil {
		t.Fatal(err)
	}
	if err := clone.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}

	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{{"manuscript/01-the-harbour.md", "chapter", "The Harbour", 130, "The quiet harbour, again.\n"}, chapters[1], chapters[2]})
	p := h.push(h.owner)
	if p.Status != "rejected" || p.ErrorCode != "remote_ahead" || p.RemoteSHA != theirs.String() {
		t.Fatalf("push = %+v", p)
	}
	if got := head(t, dir, "main"); got != theirs {
		t.Fatalf("the remote head moved to %s; it must stay %s", got, theirs)
	}
	if len(p.RemoteCommits) == 0 || p.RemoteCommits[0].SHA != theirs.String() || p.RemoteCommits[0].Subject != "Fix the harbour scene" || p.RemoteCommits[0].Author != "Collaborator" {
		t.Errorf("remote commits = %+v\n%s", p.RemoteCommits, h.logs.String())
	}
	for _, c := range p.RemoteCommits {
		if c.SHA == p.ParentSHA {
			t.Error("a commit of ours is listed as foreign")
		}
	}
	r := h.remote()
	if r.Status.State != "rejected" || r.Status.Rejected == nil || r.Status.Rejected.RemoteHead != theirs.String() || len(r.Status.Rejected.Commits) == 0 {
		t.Errorf("status = %+v", r.Status)
	}
	// While rejected, automatic triggers do nothing: no row per save.
	before := len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items)
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, PushMode: ptr("on_save")})
	h.svc.Notify(h.pid)
	h.clk.Advance(2 * time.Minute)
	h.clk.Advance(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	after := len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items)
	if after != before {
		t.Errorf("a rejected remote was tried again by an automatic trigger (%d rows, was %d)", after, before)
	}
	// There is no scratch left behind and nothing in the mirror was touched.
	if _, err := os.Stat(filepath.Join(h.dataDir, "git", ".scratch")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(h.dataDir, "git", ".scratch"))
		if len(entries) != 0 {
			t.Errorf("scratch fetches left behind: %v", entries)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(h.src.dir, "manuscript", "01-the-harbour.md")); string(b) != "The quiet harbour, again.\n" {
		t.Error("the mirror was changed")
	}
}

func ptr[T any](v T) *T { return &v }

func TestForcePushWritesAVerifiedBundleFirst(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	h.push(h.owner)
	// The remote moves on without us.
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
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], chapters[1]})
	if p := h.push(h.owner); p.Status != "rejected" {
		t.Fatalf("setup: %+v", p)
	}

	// Authorization: editors cannot force; a wrong confirmation is refused.
	_, err := h.call(h.editor, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: h.remote().ID, Confirm: "Backup"})
	if h.code(err) != ops.CodeForbidden {
		t.Errorf("an editor's force push: %v", err)
	}
	for _, bad := range []string{"", "backup", "The Salt Road", "Backup please"} {
		_, err = h.call(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: h.remote().ID, Confirm: bad})
		if h.code(err) != ops.CodeInvalid {
			t.Errorf("confirmation %q: %v", bad, err)
		}
	}
	if head(t, dir, "main") != theirs {
		t.Fatal("a refused force push changed the remote")
	}
	out := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: h.remote().ID, Confirm: "Backup"}).(gitmirror.PushOut)
	p := h.settle(out.Item.ID)
	if p.Status != "ok" || !p.Forced || p.Bundle == "" {
		t.Fatalf("force push = %+v\n%s", p, h.logs.String())
	}
	if got := head(t, dir, "main"); got.String() != p.CommitSHA {
		t.Fatalf("remote head = %s, want ours %s", got, p.CommitSHA)
	}
	bundle := filepath.Join(h.dataDir, "backups", "git", h.pid, p.Bundle)
	if err := gitmirror.VerifyBundle(bundle, theirs); err != nil {
		t.Fatalf("the bundle does not verify: %v", err)
	}
	if fi, err := os.Stat(bundle); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("bundle mode = %v %v", fi, err)
	}
	// The bundle is a real bundle: real git verifies it and clones their commit
	// and their file back from it.
	if gitPath, err := exec.LookPath("git"); err == nil {
		if out, err := verifyBundleCmd(t, gitPath, bundle).CombinedOutput(); err != nil {
			t.Fatalf("git bundle verify: %v\n%s", err, out)
		}
		restored := filepath.Join(t.TempDir(), "restored")
		if out, err := exec.Command(gitPath, "clone", "--quiet", "--branch", "main", bundle, restored).CombinedOutput(); err != nil {
			t.Fatalf("git clone of the bundle: %v\n%s", err, out)
		}
		if b, err := os.ReadFile(filepath.Join(restored, "NOTES.md")); err != nil || string(b) != "theirs\n" {
			t.Errorf("the overwritten file did not come back from the bundle: %q %v", b, err)
		}
		if out, err := exec.Command(gitPath, "-C", restored, "rev-parse", "HEAD").Output(); err != nil || strings.TrimSpace(string(out)) != theirs.String() {
			t.Errorf("the bundle's head = %q, want %s", out, theirs)
		}
	} else {
		t.Log("git is not installed: the bundle was verified by its header and pack only")
	}
	r := h.remote()
	if r.Status.State != "synced" || r.Status.LastCommitSHA != p.CommitSHA {
		t.Errorf("status after force = %+v", r.Status)
	}
	// Audit: the request and the result.
	n := 0
	rows, _ := h.d.Read().Query(`SELECT action, detail_json FROM audit_log WHERE action LIKE 'git.push_force%' ORDER BY at`)
	for rows.Next() {
		var a, d string
		_ = rows.Scan(&a, &d)
		n++
		if strings.Contains(d, "BEGIN") {
			t.Errorf("audit leaks: %s", d)
		}
	}
	rows.Close()
	if n != 2 {
		t.Errorf("%d force push audit lines, want 2", n)
	}
}

func TestForcePushRefusesWhenTheBundleCannotBeWritten(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	h.push(h.owner)
	other := filepath.Join(t.TempDir(), "other")
	clone, _ := git.PlainClone(other, false, &git.CloneOptions{URL: dir})
	_ = os.WriteFile(filepath.Join(other, "NOTES.md"), []byte("theirs\n"), 0o644)
	wt, _ := clone.Worktree()
	_, _ = wt.Add("NOTES.md")
	theirs, _ := wt.Commit("Their work", &git.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e.org", When: time.Now()}})
	_ = clone.Push(&git.PushOptions{})
	// backups/git is a file, so no bundle can be written there.
	if err := os.MkdirAll(filepath.Join(h.dataDir, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dataDir, "backups", "git"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0]})
	out := h.must(h.owner, "git.push_force", gitmirror.ForceIn{ProjectID: h.pid, RemoteID: h.remote().ID, Confirm: "Backup"}).(gitmirror.PushOut)
	p := h.settle(out.Item.ID)
	if p.Status != "error" || p.ErrorCode != "bundle_failed" {
		t.Fatalf("push = %+v", p)
	}
	if got := head(t, dir, "main"); got != theirs {
		t.Fatalf("the remote was overwritten without a bundle: %s", got)
	}
	if r := h.remote(); r.Status.State != "blocked" {
		t.Errorf("state = %s", r.Status.State)
	}
}

// cloneAt clones a repository by path into dir.
func cloneAt(dir, from string) (*git.Repository, error) {
	return git.PlainClone(dir, false, &git.CloneOptions{URL: from})
}

// commitFile adds a file to a clone, commits it and pushes it to origin.
func commitFile(t *testing.T, repo *git.Repository, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, _ := repo.Worktree()
	if _, err := wt.Add(name); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit("Their "+name, &git.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e.org", When: time.Now()}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(&git.PushOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveBurstsAreCoalescedIntoOnePush(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.PollEvery = time.Hour })
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url, func(in *gitmirror.CreateIn) { in.PushMode = "on_save" })
	h.push(h.owner)
	base := len(commitsOf(t, dir, "main"))

	for i := 0; i < 6; i++ {
		h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 80 + i, strings.Repeat("word ", i+1) + "\n"}, chapters[2]})
		h.svc.Notify(h.pid)
		h.clk.Advance(10 * time.Second) // each save restarts the 30 s wait
	}
	if got := len(commitsOf(t, dir, "main")); got != base {
		t.Fatalf("pushed during the burst (%d commits)", got)
	}
	h.clk.Advance(25 * time.Second) // 35 s after the last save
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	if got := len(commitsOf(t, dir, "main")); got != base+1 {
		t.Fatalf("the burst made %d commits, want exactly one", got-base)
	}
	if !strings.HasPrefix(commitsOf(t, dir, "main")[0].Message, "The Salt Road · ch 2: ") {
		t.Errorf("message = %q", commitsOf(t, dir, "main")[0].Message)
	}
}

func TestSteadyWritingStillPushesWithinTheMaxWait(t *testing.T) {
	h := newHarness(t, func(o *gitmirror.Options) { o.PollEvery = time.Hour })
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url, func(in *gitmirror.CreateIn) { in.PushMode = "on_save" })
	h.push(h.owner)
	base := len(commitsOf(t, dir, "main"))
	for i := 0; i < 40; i++ { // a save every 10 s for 400 s: the debounce never lapses
		h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 80 + i, strings.Repeat("w ", i+1) + "\n"}, chapters[2]})
		h.svc.Notify(h.pid)
		h.clk.Advance(10 * time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	if got := len(commitsOf(t, dir, "main")); got <= base {
		t.Fatal("steady writing starved the push")
	}
}

func TestOnlyOnePushRunsPerRemote(t *testing.T) {
	h := newHarness(t)
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	// The service has not started, so the first push stays queued and holds
	// the remote's one slot.
	a := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
	b := h.must(h.editor, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
	if a.AlreadyRunning || !b.AlreadyRunning || a.Item.ID != b.Item.ID {
		t.Fatalf("a=%+v b=%+v", a, b)
	}
	if a.Item.Status != "running" {
		t.Errorf("status = %s", a.Item.Status)
	}
	// The database refuses a second running row whoever asks.
	_, err := h.d.Writer().Exec(`INSERT INTO git_pushes(id, remote_id, project_id, trigger, status, started_at) VALUES ('gps_x', ?, ?, 'manual', 'running', '2026-01-01T00:00:00.000Z')`, h.remote().ID, h.pid)
	if err == nil {
		t.Error("two running pushes for one remote were accepted")
	}
	h.start()
	done := h.settle(a.Item.ID)
	if done.Status != "ok" || len(commitsOf(t, dir, "main")) != 1 {
		t.Errorf("done = %+v", done)
	}
}

func TestAStalePushRowFromACrashIsClosedAtStart(t *testing.T) {
	h := newHarness(t)
	url, _, _ := h.bareRemote()
	h.addRemote(url)
	h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}) // queued, never run: as if the process died
	// A new process: a new service over the same database, with the ops registered on it. The old service
	// died with its in-memory job set; leaving the ops on it made the last call below wait 3 s (the 1,500
	// spins of enqueue) for a worker that no longer exists.
	svc := gitmirror.New(gitmirror.Options{DataDir: h.dataDir, DB: h.d, Source: h.src, Clock: h.clk, AllowFile: true})
	h.reg.Env().SetService(gitmirror.ServiceName, svc)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); svc.Wait() }()
	svc.Start(ctx)
	hist := h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut)
	if len(hist.Items) != 1 || hist.Items[0].Status != "error" || hist.Items[0].ErrorCode != "interrupted" {
		t.Fatalf("history = %+v", hist.Items)
	}
	// The slot is free again.
	if _, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}); err != nil {
		t.Fatal(err)
	}
}

func TestIntervalMode(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url, func(in *gitmirror.CreateIn) { in.PushMode = "interval"; in.IntervalMinutes = 10 })
	h.push(h.owner)
	base := len(commitsOf(t, dir, "main"))
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 81, "Edited.\n"}, chapters[2]})
	h.svc.Notify(h.pid) // an interval remote ignores saves
	h.clk.Advance(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	if len(commitsOf(t, dir, "main")) != base {
		t.Fatal("an interval remote pushed on a save")
	}
	h.clk.Advance(9 * time.Minute) // ten minutes since the last attempt
	_ = h.svc.WaitIdle(ctx)
	if len(commitsOf(t, dir, "main")) != base+1 {
		t.Fatalf("the interval did not push (%d commits)", len(commitsOf(t, dir, "main")))
	}
}

func TestASecretInTheTextBlocksThePushAndNamesTheFile(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"notes/02-keys.md", "note", "Keys", 4, "Remember: -----BEGIN OPENSSH PRIVATE KEY-----\n" + keyBody + "\n"}})
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "secret_in_tree" || !strings.Contains(p.ErrorMessage, "notes/02-keys.md") {
		t.Fatalf("push = %+v", p)
	}
	if strings.Contains(p.ErrorMessage, "BEGIN") || strings.Contains(p.ErrorMessage, keyBody[:30]) {
		t.Errorf("the refusal repeats the secret: %s", p.ErrorMessage)
	}
	if head(t, dir, "main") != plumbing.ZeroHash {
		t.Error("something was pushed")
	}
	r := h.remote()
	if r.Status.State != "blocked" || r.Status.LastErrorCode != "secret_in_tree" {
		t.Errorf("status = %+v", r.Status)
	}
	// Parked: saves do not pile up refusals.
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, PushMode: ptr("on_save")})
	h.svc.Notify(h.pid)
	h.clk.Advance(time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.svc.WaitIdle(ctx)
	if n := len(h.must(h.owner, "git.push_history", gitmirror.PushHistoryIn{ProjectID: h.pid}).(gitmirror.PushHistoryOut).Items); n != 1 {
		t.Errorf("%d push rows, want 1", n)
	}
	// Fix the text and push by hand: it goes through.
	h.src.Write("The Salt Road", true, chapters)
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("after the fix: %+v", p)
	}
}

func TestIncludeNotesAndPathPrefix(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	notes := false
	h.addRemote(url, func(in *gitmirror.CreateIn) { in.IncludeNotes = &notes; in.PathPrefix = "book/draft" })
	p := h.push(h.owner)
	if p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	files := filesAt(t, dir, commitsOf(t, dir, "main")[0])
	if strings.Join(sortedKeys(files), ",") != "book/draft/manuscript/01-the-harbour.md,book/draft/manuscript/02-the-road.md,book/draft/scriobh.json" {
		t.Fatalf("files = %v", sortedKeys(files))
	}
	if strings.Contains(files["book/draft/scriobh.json"], "notes/") || !strings.Contains(files["book/draft/scriobh.json"], `"includes_notes": false`) {
		t.Errorf("the published manifest still lists notes:\n%s", files["book/draft/scriobh.json"])
	}
}

func TestMirrorNotWrittenYetIsATypedRetryableError(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, _ := h.bareRemote()
	h.addRemote(url)
	h.src.noMirror = true
	p := h.push(h.owner)
	if p.Status != "error" || p.ErrorCode != "no_mirror" {
		t.Fatalf("push = %+v", p)
	}
	if r := h.remote(); r.Status.NextAttemptAt == "" {
		t.Error("no retry is scheduled")
	}
}

func TestRemovingARemoteRemovesItsRepository(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	h.push(h.owner)
	if _, err := os.Stat(h.svc.RepoDir(h.pid, r.ID)); err != nil {
		t.Fatal(err)
	}
	h.must(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID})
	if _, err := os.Stat(h.svc.RepoDir(h.pid, r.ID)); !os.IsNotExist(err) {
		t.Errorf("the local repository survived: %v", err)
	}
	if n := rowCount(t, h, `SELECT count(*) FROM git_pushes`); n != 0 {
		t.Errorf("%d push rows survived", n)
	}
}

func rowCount(t *testing.T, h *harness, q string, args ...any) int {
	t.Helper()
	var n int
	if err := h.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPurgingAProjectRemovesRemotesAndRepositories(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	h.push(h.owner)
	dir := h.svc.RepoDir(h.pid, r.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	// The same steps the purge op takes: the rows cascade, then the hooks run.
	if _, err := h.d.Writer().Exec(`DELETE FROM projects WHERE id = ?`, h.pid); err != nil {
		t.Fatal(err)
	}
	h.reg.Env().ProjectPurged(context.Background(), h.pid)
	if _, err := os.Stat(filepath.Join(h.dataDir, "git", h.pid)); !os.IsNotExist(err) {
		t.Errorf("the project's repositories survived: %v", err)
	}
	if rowCount(t, h, `SELECT count(*) FROM git_remotes`) != 0 || rowCount(t, h, `SELECT count(*) FROM git_pushes`) != 0 {
		t.Error("remote rows survived the purge")
	}
}

func TestALostLocalRepositoryIsAdoptedFromTheRemoteNotTreatedAsAStranger(t *testing.T) {
	h := newHarness(t)
	h.start()
	url, _, dir := h.bareRemote()
	r := h.addRemote(url)
	first := h.push(h.owner)
	// The data directory is restored without its local repositories (backups leave them out).
	if err := os.RemoveAll(h.svc.RepoDir(h.pid, r.ID)); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0], {"manuscript/02-the-road.md", "chapter", "The Road", 90, "Rewritten.\n"}, chapters[2]})
	p := h.push(h.owner)
	if p.Status != "ok" || p.Outcome != "pushed" || p.ParentSHA != first.CommitSHA {
		t.Fatalf("push = %+v\n%s", p, h.logs.String())
	}
	if n := len(commitsOf(t, dir, "main")); n != 2 {
		t.Errorf("remote has %d commits, want 2: the history continues", n)
	}
	// If the remote moved on while the record was older, nothing is adopted and
	// the push is refused as usual.
	if err := os.RemoveAll(h.svc.RepoDir(h.pid, r.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Writer().Exec(`UPDATE git_remotes SET last_commit_sha = ? WHERE id = ?`, first.CommitSHA, r.ID); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.src.Write("The Salt Road", true, []mdoc{chapters[0]})
	if p := h.push(h.owner); p.Status != "rejected" {
		t.Fatalf("an older record must not be adopted over a newer remote: %+v", p)
	}
}

// keyBody looks like the base64 of a private key.
var keyBody = strings.Repeat("b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZWQy", 2)

// verifyBundleCmd returns a `git bundle verify` command that runs inside a
// throwaway repository: verify needs one, and the package directory is not
// always inside a checkout (an exported tree, a CI cache, a scratch copy).
func verifyBundleCmd(t *testing.T, gitPath, bundle string) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command(gitPath, "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	cmd := exec.Command(gitPath, "bundle", "verify", bundle)
	cmd.Dir = dir
	return cmd
}

// A trashed project is paused: a push is refused and the remote receives nothing until the project is restored.
// Asking for one is refused by the registry (409); a push that was already queued when the project went to the trash
// is the engine's to refuse, and an automatic one is dropped without a word. (The engine used to look at the project's
// reserved deleted_at, which nothing sets, so a queued push still went out.)
func TestTrashedProjectPushesNothing(t *testing.T) {
	h := newHarness(t)
	url, _, dir := h.bareRemote()
	h.addRemote(url)
	setTrash := func(on bool) {
		t.Helper()
		var v any
		if on {
			v = "2026-11-12T00:00:00.000Z"
		}
		if _, err := h.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, v, h.pid); err != nil {
			t.Fatal(err)
		}
	}
	noBranch := func(when string) {
		t.Helper()
		if refs, err := os.ReadDir(filepath.Join(dir, "refs", "heads")); err == nil && len(refs) > 0 {
			t.Errorf("%s: the remote received a branch: %v", when, refs)
		}
	}

	// Queued before the trash, run after it.
	h.useUnstartedService()
	out := h.must(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid}).(gitmirror.PushOut)
	setTrash(true)
	h.start()
	p := h.settle(out.Item.ID)
	if p.Status == "ok" || !strings.Contains(p.ErrorMessage, "trash") {
		t.Fatalf("a push queued before the trash ran: %+v", p)
	}
	noBranch("a push queued before the trash")

	// Asked for in the trash: the registry answers 409 and names the trash.
	_, err := h.call(h.owner, "git.push", gitmirror.RemoteBody{ProjectID: h.pid})
	if e := ops.AsError(err); e.Code != ops.CodeConflict || e.Data["reason"] != ops.ReasonProjectTrashed {
		t.Fatalf("git.push on a trashed project: %v", err)
	}
	noBranch("a push asked for in the trash")

	setTrash(false)
	if p := h.push(h.owner); p.Status != "ok" {
		t.Fatalf("after restoring the project: %+v", p)
	}
}

package mirror_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/stash"
)

func stashFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, f := range tree(t, root) {
		if strings.HasPrefix(f, "stash/") {
			out = append(out, f)
		}
	}
	return out
}

// The stash is written under stash/, one file per live item, follows every
// change and goes with the project. It is not in the manifest (which is what a
// Git push carries): the stash is never published.
func TestStashFolderFollowsTheStash(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Salt Book")
	e.settle(pid)
	root := e.root(pid)
	if len(stashFiles(t, root)) != 0 {
		t.Fatal("no stash, no folder")
	}
	item := e.call(e.owner, "stash.create", stash.CreateIn{ID: pid, Title: "A cut scene", Content: "The lighthouse stairs.\n", Tags: []string{"cut"}}).(stash.ItemOut).Item
	e.call(e.owner, "stash.create", stash.CreateIn{ID: pid, Title: "A cut scene", Content: "Same title.\n"})
	e.settle(pid)
	files := stashFiles(t, root)
	if len(files) != 2 || !strings.HasPrefix(files[0], "stash/a-cut-scene--") {
		t.Fatalf("files %v", files)
	}
	var mine string
	for _, f := range files {
		if strings.Contains(read(t, filepath.Join(root, f)), "The lighthouse stairs.") {
			mine = f
		}
	}
	got := read(t, filepath.Join(root, mine))
	want := "---\nid: \"" + item.ID + "\"\nkind: paragraph\ntitle: \"A cut scene\"\ntags:\n  - \"cut\"\n---\n\nThe lighthouse stairs.\n"
	if got != want {
		t.Fatalf("file:\n%q\nwant:\n%q", got, want)
	}
	// Not in the manifest, which is what the Git push reads.
	if m := read(t, filepath.Join(root, "project.json")); strings.Contains(m, "stash") || strings.Contains(m, "lighthouse") {
		t.Errorf("the manifest names the stash: %s", m)
	}
	// An edit rewrites the file; trash removes it; restore brings it back.
	body := "Rewritten.\n"
	e.call(e.owner, "stash.update", stash.UpdateIn{ID: pid, ItemID: item.ID, BaseRevision: item.Revision, Content: &body})
	e.settle(pid)
	if got := read(t, filepath.Join(root, mine)); !strings.HasSuffix(got, "\nRewritten.\n") {
		t.Fatalf("after an edit: %q", got)
	}
	e.call(e.owner, "stash.trash", stash.ItemIn{ID: pid, ItemID: item.ID})
	e.settle(pid)
	if fs := stashFiles(t, root); len(fs) != 1 {
		t.Fatalf("a trashed item is not written: %v", fs)
	}
	e.call(e.owner, "stash.restore", stash.ItemIn{ID: pid, ItemID: item.ID})
	e.settle(pid)
	if fs := stashFiles(t, root); len(fs) != 2 {
		t.Fatalf("restored: %v", fs)
	}
	// A forced rebuild of an unchanged stash writes nothing.
	e.fs.reset()
	if _, err := e.m.Rebuild(e.ctx, pid); err != nil {
		t.Fatal(err)
	}
	if n := e.fs.writes(); n != 0 {
		t.Errorf("an unchanged stash was rewritten (%d writes)", n)
	}
	// Purging the project removes the folder with it.
	e.call(e.owner, "project.trash", projects.StateIn{ID: pid})
	e.call(e.owner, "project.purge", projects.PurgeIn{ID: pid, ConfirmTitle: "Salt Book"})
}

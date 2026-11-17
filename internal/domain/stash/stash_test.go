package stash_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t   *testing.T
	d   *db.DB
	reg *ops.Registry
	own ops.Principal
	pid string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	d := testutil.DB(t)
	f := &fx{t: t, d: d, reg: registry.Build(d, nil, t.TempDir())}
	f.own = ops.Principal{UserID: testutil.User(t, d, "writer", "s@t.local"), Role: ops.RoleWriter}
	out, err := f.reg.Call(context.Background(), f.own, "project.create", projects.CreateIn{Title: "Stashy"})
	if err != nil {
		t.Fatal(err)
	}
	f.pid = out.(projects.Item).Item.ID
	return f
}

func (f *fx) call(op string, in any) (any, error) {
	return f.reg.Call(context.Background(), f.own, op, in)
}

func (f *fx) create(in stash.CreateIn) stash.Item {
	f.t.Helper()
	in.ID = f.pid
	out, err := f.call("stash.create", in)
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(stash.ItemOut).Item
}

func (f *fx) list(in stash.ListIn) stash.ListOut {
	f.t.Helper()
	in.ID = f.pid
	out, err := f.call("stash.list", in)
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(stash.ListOut)
}

func titles(l stash.ListOut) string {
	var t []string
	for _, it := range l.Items {
		t = append(t, it.Title)
	}
	return strings.Join(t, ",")
}

func TestStashCRUDOrderAndTrash(t *testing.T) {
	f := newFx(t)
	a := f.create(stash.CreateIn{Title: "A", Content: "The cut paragraph about the harbour.", Tags: []string{"harbour", "Cut", "cut"}})
	b := f.create(stash.CreateIn{Title: "B", Content: "Notes on chapter three.", Kind: "note"})
	c := f.create(stash.CreateIn{Title: "C", Content: "- outline one\n- outline two", Kind: "outline"})
	if a.Revision != 1 || a.WordCount != 6 || a.Kind != "paragraph" || len(a.Tags) != 2 {
		t.Fatalf("created %+v", a)
	}
	if got := titles(f.list(stash.ListIn{})); got != "A,B,C" {
		t.Fatalf("order %s", got)
	}
	// Reorder: C first, A last.
	if _, err := f.call("stash.reorder", stash.ReorderIn{ID: f.pid, ItemID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("stash.reorder", stash.ReorderIn{ID: f.pid, ItemID: a.ID, AfterItemID: b.ID}); err != nil {
		t.Fatal(err)
	}
	if got := titles(f.list(stash.ListIn{})); got != "C,B,A" {
		t.Fatalf("after reorder %s", got)
	}
	if got := titles(f.list(stash.ListIn{Kind: "note"})); got != "B" {
		t.Fatalf("kind %s", got)
	}
	if got := titles(f.list(stash.ListIn{Tag: "harbour"})); got != "A" {
		t.Fatalf("tag %s", got)
	}
	// Update with a stale revision is a conflict; a fresh one writes.
	title := "A, revised"
	if _, err := f.call("stash.update", stash.UpdateIn{ID: f.pid, ItemID: a.ID, BaseRevision: 7, Title: &title}); ops.AsError(err).Code != ops.CodeConflict {
		t.Fatalf("stale: %v", err)
	}
	cur, _ := f.call("stash.get", stash.GetIn{ID: f.pid, ItemID: a.ID})
	rev := cur.(stash.ItemOut).Item.Revision
	body := "Rewritten text of the cut paragraph, now longer."
	out, err := f.call("stash.update", stash.UpdateIn{ID: f.pid, ItemID: a.ID, BaseRevision: rev, Title: &title, Content: &body})
	if err != nil {
		t.Fatal(err)
	}
	if u := out.(stash.ItemOut).Item; u.Revision != rev+1 || u.WordCount != 8 || u.Title != title {
		t.Fatalf("updated %+v", u)
	}
	// An update that changes nothing keeps the revision.
	out, _ = f.call("stash.update", stash.UpdateIn{ID: f.pid, ItemID: a.ID, BaseRevision: rev + 1, Title: &title})
	if out.(stash.ItemOut).Item.Revision != rev+1 {
		t.Error("idempotent update")
	}
	// Trash, list the trash, restore to the end, delete only trashed items.
	if _, err := f.call("stash.delete", stash.ItemIn{ID: f.pid, ItemID: b.ID}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("delete a live item: %v", err)
	}
	if _, err := f.call("stash.trash", stash.ItemIn{ID: f.pid, ItemID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if got := titles(f.list(stash.ListIn{})); got != "B,A, revised" {
		t.Fatalf("after trash %s", got)
	}
	if got := titles(f.list(stash.ListIn{Trash: true})); got != "C" {
		t.Fatalf("trash %s", got)
	}
	if _, err := f.call("stash.update", stash.UpdateIn{ID: f.pid, ItemID: c.ID, BaseRevision: 1, Title: &title}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("edit a trashed item: %v", err)
	}
	if _, err := f.call("stash.restore", stash.ItemIn{ID: f.pid, ItemID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if got := titles(f.list(stash.ListIn{})); got != "B,A, revised,C" {
		t.Fatalf("restored to the end: %s", got)
	}
	f.call("stash.trash", stash.ItemIn{ID: f.pid, ItemID: c.ID})
	if _, err := f.call("stash.delete", stash.ItemIn{ID: f.pid, ItemID: c.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("stash.get", stash.GetIn{ID: f.pid, ItemID: c.ID}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("deleted: %v", err)
	}
	// Validation.
	for name, in := range map[string]stash.CreateIn{
		"empty":    {},
		"kind":     {Title: "x", Kind: "chapter"},
		"tag":      {Title: "x", Tags: []string{`bad"tag`}},
		"source":   {Title: "x", SourceDocumentID: "doc_nothere"},
		"too long": {Content: strings.Repeat("x", 600<<10)},
	} {
		in.ID = f.pid
		if _, err := f.call("stash.create", in); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.call("stash.get", stash.GetIn{ID: f.pid}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("no id: %v", err)
	}
}

func TestStashSearchIsItsOwn(t *testing.T) {
	f := newFx(t)
	f.create(stash.CreateIn{Title: "Cut scene", Content: "Máire climbed the lighthouse stairs in the storm.", Tags: []string{"lighthouse"}})
	f.create(stash.CreateIn{Title: "Aside", Content: "A quiet note about rope and knots."})
	gone := f.create(stash.CreateIn{Title: "Dropped", Content: "The lighthouse keeper's cat."})
	f.call("stash.trash", stash.ItemIn{ID: f.pid, ItemID: gone.ID})
	// Diacritics fold, the last word is a prefix, trashed items are not found, tags are searched.
	if l := f.list(stash.ListIn{Q: "maire lighthou"}); l.Total != 1 || l.Items[0].Title != "Cut scene" || l.Items[0].Snippet == "" || !strings.Contains(l.Items[0].SnippetHTML, "<mark>") {
		t.Fatalf("search: %+v", l)
	}
	if l := f.list(stash.ListIn{Q: "lighthouse"}); l.Total != 1 || l.Items[0].MatchedIn != "title" && l.Items[0].MatchedIn != "tags" && l.Items[0].MatchedIn != "body" {
		t.Fatalf("one live match: %+v", l)
	}
	if l := f.list(stash.ListIn{Q: "lighthouse", Trash: true}); l.Total != 1 || l.Items[0].Title != "Dropped" {
		t.Fatalf("the trash is searchable as the trash: %+v", l)
	}
	if l := f.list(stash.ListIn{Q: "rope", Kind: "note"}); l.Total != 0 {
		t.Fatalf("kind filter with a query: %+v", l)
	}
	if _, err := f.call("stash.list", stash.ListIn{ID: f.pid, Q: "!!!"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a query with no word: %v", err)
	}
	// Editing the text updates the index in the same transaction.
	l := f.list(stash.ListIn{Q: "rope"})
	it := l.Items[0]
	body := "A note about sails instead."
	if _, err := f.call("stash.update", stash.UpdateIn{ID: f.pid, ItemID: it.ID, BaseRevision: it.Revision, Content: &body}); err != nil {
		t.Fatal(err)
	}
	if f.list(stash.ListIn{Q: "rope"}).Total != 0 || f.list(stash.ListIn{Q: "sails"}).Total != 1 {
		t.Fatal("the index follows an edit")
	}
	// The manuscript search never sees stash text, and the stash is not in any count.
	res, err := f.call("search.query", search.QueryIn{ProjectID: f.pid, Q: "lighthouse"})
	if err != nil || res.(search.QueryOut).Total != 0 {
		t.Fatalf("manuscript search found stashed text: %v %+v", err, res)
	}
	// A rebuild brings the index back from the items.
	if _, err := f.d.Writer().Exec(`DELETE FROM stash_text`); err != nil {
		t.Fatal(err)
	}
	if f.list(stash.ListIn{Q: "sails"}).Total != 0 {
		t.Fatal("index emptied")
	}
	if err := search.Rebuild(context.Background(), f.d, nil); err != nil {
		t.Fatal(err)
	}
	if f.list(stash.ListIn{Q: "sails"}).Total != 1 || f.list(stash.ListIn{Q: "lighthouse"}).Total != 1 {
		t.Fatal("rebuild restores the stash index")
	}
}

func TestStashIsNotPartOfTheManuscript(t *testing.T) {
	f := newFx(t)
	d, err := f.call("doc.create", docs.CreateIn{ProjectID: f.pid, Title: "Ch", Content: "Three plain words."})
	if err != nil {
		t.Fatal(err)
	}
	doc := d.(docs.Item).Item
	f.create(stash.CreateIn{Title: "Big", Content: strings.Repeat("stashed words ", 500), SourceDocumentID: doc.ID})
	out, _ := f.call("doc.list", docs.ListIn{ProjectID: f.pid})
	total := 0
	for _, m := range out.(docs.ListOut).Items {
		total += m.WordCount
	}
	if total != 3 {
		t.Fatalf("project word count %d: the stash is not in any document", total)
	}
	var n int
	_ = f.d.Read().QueryRow(`SELECT count(*) FROM documents WHERE project_id = ? AND content LIKE '%stashed%'`, f.pid).Scan(&n)
	if n != 0 {
		t.Fatal("stash text leaked into documents")
	}
	// Deleting the source document keeps the item, and a purge removes the stash.
	f.call("doc.trash", docs.DocIn{ID: doc.ID})
	if f.list(stash.ListIn{}).Total != 1 {
		t.Fatal("the item outlives its source")
	}
	if _, err := f.call("project.trash", projects.StateIn{ID: f.pid}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call("project.purge", projects.PurgeIn{ID: f.pid, ConfirmTitle: "Stashy"}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`stash_items`, `stash_text`} {
		var c int
		_ = f.d.Read().QueryRow(`SELECT count(*) FROM ` + q).Scan(&c)
		if c != 0 {
			t.Errorf("%s: %d rows left after a purge", q, c)
		}
	}
}

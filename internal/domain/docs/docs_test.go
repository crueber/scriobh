package docs_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type fx struct {
	t    *testing.T
	d    *db.DB
	reg  *ops.Registry
	dir  string
	ctx  context.Context
	ownA ops.Principal
	ownB ops.Principal
	pidA string
	pidB string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	d := testutil.DB(t)
	dir := t.TempDir()
	f := &fx{t: t, d: d, dir: dir, ctx: context.Background(), reg: registry.Build(d, nil, dir)}
	f.ownA = ops.Principal{UserID: testutil.User(t, d, "writer", "a@t.local"), Role: ops.RoleWriter}
	f.ownB = ops.Principal{UserID: testutil.User(t, d, "writer", "b@t.local"), Role: ops.RoleWriter}
	f.pidA = f.project(f.ownA, "Alpha")
	f.pidB = f.project(f.ownB, "Beta")
	return f
}

func (f *fx) project(p ops.Principal, title string) string {
	out, err := f.reg.Call(f.ctx, p, "project.create", projects.CreateIn{Title: title})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(projects.Item).Item.ID
}

func (f *fx) doc(p ops.Principal, pid string, in docs.CreateIn) docs.Doc {
	in.ProjectID = pid
	out, err := f.reg.Call(f.ctx, p, "doc.create", in)
	if err != nil {
		f.t.Fatalf("create %q: %v", in.Title, err)
	}
	return out.(docs.Item).Item
}

func (f *fx) call(p ops.Principal, op string, in any) (any, error) {
	return f.reg.Call(f.ctx, p, op, in)
}

func code(err error) ops.Code { return ops.AsError(err).Code }

func (f *fx) order(p ops.Principal, pid string) []string {
	out, err := f.call(p, "doc.list", docs.ListIn{ProjectID: pid})
	if err != nil {
		f.t.Fatal(err)
	}
	var titles []string
	byID := map[string]string{}
	for _, m := range out.(docs.ListOut).Items {
		byID[m.ID] = m.Title
	}
	for _, id := range out.(docs.ListOut).Order {
		if byID[id] != "" { // skip the untitled first page every project starts with
			titles = append(titles, byID[id])
		}
	}
	return titles
}

func TestSaveLoop(t *testing.T) {
	f := newFx(t)
	f.reg = registry.Build(f.d, events.NewStore(f.d, slog.Default()), f.dir) // with the real event store, so the last check can fail
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "One", Content: "The harbour at low tide."})
	if d.Revision != 1 || d.WordCount != 5 || d.Kind != "chapter" {
		t.Fatalf("created %+v", d.Meta)
	}
	body := "The harbour at low tide, and Máire waited."
	out, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 1, Content: &body})
	if err != nil {
		t.Fatal(err)
	}
	u := out.(docs.Item).Item
	if u.Revision != 2 || u.WordCount != 8 || u.Content != body {
		t.Fatalf("updated %+v", u.Meta)
	}
	// Saving the same text again is idempotent: no new revision.
	out, err = f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 2, Content: &body})
	if err != nil || out.(docs.Item).Item.Revision != 2 {
		t.Fatalf("an unchanged save must not bump the revision: %v %v", out, err)
	}
	// A stale base is a 409 that says where the document is now, and writes nothing.
	other := "Something else entirely."
	_, err = f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 1, Content: &other})
	e := ops.AsError(err)
	if e.Code != ops.CodeConflict || e.Data["current_revision"] != 2 {
		t.Fatalf("stale save: %v %+v", err, e.Data)
	}
	got, _ := f.call(f.ownA, "doc.get", docs.DocIn{ID: d.ID})
	if got.(docs.Item).Item.Content != body {
		t.Error("a refused save must not change the document")
	}
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 0, Content: &body}); code(err) != ops.CodeInvalid {
		t.Errorf("missing base revision: %v", err)
	}
	// The search text follows the document in the same transaction.
	var n int
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'maire'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("search index has %d rows for maire, %v", n, err)
	}
	// Events were written in the same transaction as the content.
	if err := f.d.Read().QueryRow(`SELECT count(*) FROM events WHERE type = 'doc.updated' AND document_id = ?`, d.ID).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d doc.updated events for one content change (an idempotent save and a refused one write none): %v", n, err)
	}
}

func TestLeaseKeepsTwoTabsApart(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "One"})
	a, b := "A text", "B text"
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 1, TabID: "tabA", Content: &a}); err != nil {
		t.Fatal(err)
	}
	_, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 2, TabID: "tabB", Content: &b})
	if e := ops.AsError(err); e.Code != ops.CodeConflict || e.Data["reason"] != "lease" {
		t.Fatalf("a second tab must be refused with a lease conflict: %v", err)
	}
	out, _ := f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabB"})
	if out.(docs.LeaseOut).Held {
		t.Error("without take, the lease stays with the first tab")
	}
	out, _ = f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabB", Take: true})
	if !out.(docs.LeaseOut).Held {
		t.Error("take must override")
	}
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 2, TabID: "tabB", Content: &b}); err != nil {
		t.Errorf("the new holder saves: %v", err)
	}
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 3, TabID: "tabA", Content: &a}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("the old holder is now refused: %v", err)
	}
	// API clients send no tab id and are not subject to the lease.
	c := "C text"
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 3, Content: &c}); err != nil {
		t.Errorf("an API client without a tab id: %v", err)
	}
}

func TestLeaseRelease(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "One"})
	f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabA"})
	// A tab that does not hold the lease cannot release it.
	f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabB", Release: true})
	out, _ := f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabB"})
	if out.(docs.LeaseOut).Held {
		t.Error("another tab's release must not free the lease")
	}
	f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabA", Release: true})
	out, _ = f.call(f.ownA, "doc.lease", docs.LeaseIn{ID: d.ID, TabID: "tabB"})
	if !out.(docs.LeaseOut).Held {
		t.Error("after the holder releases, the next tab gets the lease at once")
	}
}

func TestSplitCutsAtTheCursor(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Long", Content: "First half. 😀 emoji.\n\nSecond half here."})
	f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "After"})
	cut := len(utf16.Encode([]rune("First half. 😀 emoji.\n\n")))
	inside := len(utf16.Encode([]rune("First half. "))) + 1 // between the two halves of the emoji
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 1, Offset: inside}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a cut inside a surrogate pair: %v", err)
	}
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 9, Offset: cut}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("a stale revision must conflict: %v", err)
	}
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 1, Offset: 10_000}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("an offset past the end: %v", err)
	}
	out, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 1, Offset: cut, Title: "Part two"})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(docs.SplitOut)
	if o.Head.Content != "First half. 😀 emoji.\n" || o.Tail.Content != "Second half here." || o.Head.Revision != 2 || o.Tail.Title != "Part two" {
		t.Errorf("head %q (rev %d) tail %q %q", o.Head.Content, o.Head.Revision, o.Tail.Content, o.Tail.Title)
	}
	got := f.order(f.ownA, f.pidA)
	want := []string{"Long", "Part two", "After"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("order %v, want %v", got, want)
	}
	// Nothing after the cut: refused.
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: o.Tail.ID, BaseRevision: 1, Offset: len(utf16.Encode([]rune(o.Tail.Content)))}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("cutting at the end: %v", err)
	}
}

// Ids from another project must never be usable, whichever field carries them.
func TestForeignIDsAreRefused(t *testing.T) {
	f := newFx(t)
	inA := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "A part", Kind: "part"})
	inB := f.doc(f.ownB, f.pidB, docs.CreateIn{Title: "B chapter"})
	x := "hijack"

	// B's owner has no access to A's document at all.
	for name, call := range map[string]func() error{
		"get": func() error { _, e := f.call(f.ownB, "doc.get", docs.DocIn{ID: inA.ID}); return e },
		"update": func() error {
			_, e := f.call(f.ownB, "doc.update", docs.UpdateIn{ID: inA.ID, BaseRevision: 1, Content: &x})
			return e
		},
		"move":  func() error { _, e := f.call(f.ownB, "doc.move", docs.MoveIn{ID: inA.ID}); return e },
		"trash": func() error { _, e := f.call(f.ownB, "doc.trash", docs.DocIn{ID: inA.ID}); return e },
		"lease": func() error { _, e := f.call(f.ownB, "doc.lease", docs.LeaseIn{ID: inA.ID, TabID: "t"}); return e },
	} {
		if code(call()) != ops.CodeNotFound {
			t.Errorf("%s of another project's document must be not_found", name)
		}
	}
	// Using A's document as a parent in B, in create or move, is refused.
	if _, err := f.call(f.ownB, "doc.create", docs.CreateIn{ProjectID: f.pidB, Title: "x", ParentID: inA.ID}); code(err) != ops.CodeInvalid {
		t.Errorf("create under a foreign parent: %v", err)
	}
	if _, err := f.call(f.ownB, "doc.move", docs.MoveIn{ID: inB.ID, ParentID: inA.ID}); code(err) != ops.CodeInvalid {
		t.Errorf("move under a foreign parent: %v", err)
	}
	// A foreign sibling as the 'after' anchor is refused too.
	if _, err := f.call(f.ownB, "doc.move", docs.MoveIn{ID: inB.ID, After: &inA.ID}); code(err) != ops.CodeInvalid {
		t.Errorf("move after a foreign sibling: %v", err)
	}
	// Nothing changed in A.
	got, _ := f.call(f.ownA, "doc.get", docs.DocIn{ID: inA.ID})
	if g := got.(docs.Item).Item; g.Revision != 1 || g.Trashed || g.Content != "" {
		t.Errorf("A's document was touched: %+v", g.Meta)
	}
	// A project member without the editor role is refused at the registry.
	reader := ops.Principal{UserID: testutil.User(t, f.d, "reader", "r@t.local"), Role: ops.RoleReader}
	if _, err := f.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, f.pidA, reader.UserID, "reader", db.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(reader, "doc.update", docs.UpdateIn{ID: inA.ID, BaseRevision: 1, Content: &x}); code(err) != ops.CodeForbidden {
		t.Errorf("reader editing: %v", err)
	}
	if _, err := f.call(reader, "doc.get", docs.DocIn{ID: inA.ID}); err != nil {
		t.Errorf("reader reading: %v", err)
	}
}

func TestMoveRules(t *testing.T) {
	f := newFx(t)
	part := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Part", Kind: "part"})
	ch := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Chapter", ParentID: part.ID})
	sc := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Scene", Kind: "scene", ParentID: ch.ID})

	for name, in := range map[string]docs.MoveIn{
		"into itself":     {ID: part.ID, ParentID: part.ID},
		"into a child":    {ID: part.ID, ParentID: ch.ID},
		"into grandchild": {ID: part.ID, ParentID: sc.ID},
	} {
		if _, err := f.call(f.ownA, "doc.move", in); code(err) != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A legal move keeps the subtree together.
	if _, err := f.call(f.ownA, "doc.move", docs.MoveIn{ID: ch.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := f.call(f.ownA, "doc.get", docs.DocIn{ID: sc.ID})
	if g := got.(docs.Item).Item; len(g.Ancestors) != 1 || g.Ancestors[0].ID != ch.ID {
		t.Errorf("the scene must stay in its chapter: %+v", g.Ancestors)
	}
}

func TestOrderingAndRebalance(t *testing.T) {
	f := newFx(t)
	f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "b"})
	first := ""
	f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "c"})
	f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "a", After: &first})
	if got := strings.Join(f.order(f.ownA, f.pidA), ""); got != "abc" {
		t.Fatalf("order = %s", got)
	}
	// Insert at the front 60 times: the gaps run out and siblings are rebalanced
	// without ever changing the order.
	var want []string
	for i := 0; i < 60; i++ {
		title := fmt.Sprintf("n%02d", i)
		f.doc(f.ownA, f.pidA, docs.CreateIn{Title: title, After: &first})
		want = append([]string{title}, want...)
	}
	got := f.order(f.ownA, f.pidA)
	want = append(want, "a", "b", "c")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order lost after rebalancing:\n got %v\nwant %v", got, want)
	}
}

func TestTrashAndRestoreSubtree(t *testing.T) {
	f := newFx(t)
	part := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Part", Kind: "part"})
	c1 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "C1", ParentID: part.ID})
	c2 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "C2", ParentID: part.ID})
	// C2 is trashed on its own first, and must stay trashed when the part returns.
	if _, err := f.call(f.ownA, "doc.trash", docs.DocIn{ID: c2.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(f.ownA, "doc.trash", docs.DocIn{ID: part.ID}); err != nil {
		t.Fatal(err)
	}
	if got := f.order(f.ownA, f.pidA); len(got) != 0 {
		t.Fatalf("the live tree should be empty, got %v", got)
	}
	trashed, _ := f.call(f.ownA, "doc.list", docs.ListIn{ProjectID: f.pidA, Trash: true})
	if n := len(trashed.(docs.ListOut).Items); n != 3 {
		t.Errorf("trash holds %d, want 3", n)
	}
	if _, err := f.call(f.ownA, "doc.restore", docs.DocIn{ID: part.ID}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.order(f.ownA, f.pidA), ","); got != "Part,C1" {
		t.Errorf("restore brought back %q, want Part,C1", got)
	}
	// Restoring a child whose folder is still in the trash lands at the top level.
	if _, err := f.call(f.ownA, "doc.trash", docs.DocIn{ID: part.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(f.ownA, "doc.restore", docs.DocIn{ID: c1.ID}); err != nil {
		t.Fatal(err)
	}
	g, _ := f.call(f.ownA, "doc.get", docs.DocIn{ID: c1.ID})
	if x := g.(docs.Item).Item; x.Trashed || x.ParentID != "" {
		t.Errorf("a child restored out of a trashed folder goes to the top level: %+v", x.Meta)
	}
	// A trashed document cannot be edited.
	x := "late edit"
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: c2.ID, BaseRevision: 1, Content: &x}); code(err) != ops.CodeConflict {
		t.Errorf("editing the trash: %v", err)
	}
}

func TestConflictCopyIsWrittenPrivately(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "One"})
	out, err := f.call(f.ownA, "doc.conflict", docs.ConflictIn{ID: d.ID, Content: "my unsaved words", BaseRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	rel := out.(docs.ConflictOut).Path
	full := filepath.Join(f.dir, rel)
	b, err := os.ReadFile(full)
	if err != nil || string(b) != "my unsaved words" {
		t.Fatalf("copy: %q %v", b, err)
	}
	if st, _ := os.Stat(full); st.Mode().Perm() != 0o600 {
		t.Errorf("conflict copies must be private, mode %v", st.Mode())
	}
	if !strings.HasPrefix(rel, filepath.Join("conflicts", f.pidA)) {
		t.Errorf("path %s", rel)
	}
}

func TestLimitsAndGuards(t *testing.T) {
	f := newFx(t)
	if _, err := f.call(f.ownA, "doc.create", docs.CreateIn{ProjectID: f.pidA, Title: strings.Repeat("t", 301)}); code(err) != ops.CodeInvalid {
		t.Errorf("long title: %v", err)
	}
	if _, err := f.call(f.ownA, "doc.create", docs.CreateIn{ProjectID: f.pidA, Kind: "epic"}); code(err) != ops.CodeInvalid {
		t.Errorf("kind: %v", err)
	}
	big := strings.Repeat("word ", 16000)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Big", Content: big})
	if d.Guard != "warn" {
		t.Errorf("16,000 words must warn, got %q", d.Guard)
	}
	huge := strings.Repeat("word ", 26000)
	d2 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Huge", Content: huge})
	if d2.Guard != "readonly" {
		t.Errorf("26,000 words must be read-only, got %q", d2.Guard)
	}
	tooBig := strings.Repeat("x", 3<<20)
	if _, err := f.call(f.ownA, "doc.create", docs.CreateIn{ProjectID: f.pidA, Title: "x", Content: tooBig}); code(err) != ops.CodeTooLarge {
		t.Errorf("3 MiB: %v", err)
	}
}

func TestSplitKeepsFlagsAndRefusesSourceBlocks(t *testing.T) {
	f := newFx(t)
	body := "Intro line.\n\n```\ncode a\ncode b\n```\n\nOutro words here.\n"
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Flags", Content: body})
	inc := false
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: 1, IncludeInCompile: &inc}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Writer().Exec(`UPDATE documents SET ai_exclude = 1 WHERE id = ?`, d.ID); err != nil {
		t.Fatal(err)
	}
	u16 := func(s string) int { return len(utf16.Encode([]rune(s))) }
	for name, off := range map[string]int{
		"inside the fence":   u16("Intro line.\n\n```\ncode"),
		"on the fence close": u16("Intro line.\n\n```\ncode a\ncode b\n`"),
	} {
		if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 2, Offset: off}); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	out, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 2, Offset: u16("Intro line.\n\n```\ncode a\ncode b\n```\n\n")})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(docs.SplitOut)
	if !strings.HasPrefix(o.Head.Content, "Intro line.\n\n```\ncode a\ncode b\n```") || o.Tail.Content != "Outro words here.\n" {
		t.Errorf("head %q tail %q", o.Head.Content, o.Tail.Content)
	}
	var ai int
	_ = f.d.Read().QueryRow(`SELECT ai_exclude FROM documents WHERE id = ?`, o.Tail.ID).Scan(&ai)
	if o.Tail.IncludeInCompile || ai != 1 {
		t.Errorf("the new document must keep the compile and AI-exclusion flags: compile=%v ai_exclude=%d", o.Tail.IncludeInCompile, ai)
	}
	// A cut inside the alternatives comment is refused; splitting a document that
	// carries alternatives is covered by TestSplitDividesAlternatives.
	a := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Alts", Content: "One two three.\n\n<!-- scriobh:alts [] -->\n"})
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: a.ID, BaseRevision: 1, Offset: 22}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a cut inside the comment: %v", err)
	}
}

func TestSplitOffsetsIgnoreCRLF(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Windows", Content: "One.\r\n\r\nTwo.\r\n\r\nThree words here."})
	// As an editor shows it: "One.\n\nTwo.\n\nThree words here." The caret sits before "words".
	off := len(utf16.Encode([]rune("One.\n\nTwo.\n\nThree ")))
	out, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: 1, Offset: off})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(docs.SplitOut)
	if o.Tail.Content != "words here." || !strings.HasSuffix(o.Head.Content, "Three\n") && !strings.HasSuffix(strings.TrimRight(o.Head.Content, "\n"), "Three") {
		t.Errorf("head %q tail %q", o.Head.Content, o.Tail.Content)
	}
}

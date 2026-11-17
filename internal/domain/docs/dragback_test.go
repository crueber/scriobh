package docs_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/ops"
)

func (f *fx) stash(pid, title, content string) stash.Item {
	f.t.Helper()
	out, err := f.call(f.ownA, "stash.create", stash.CreateIn{ID: pid, Title: title, Content: content})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(stash.ItemOut).Item
}

func (f *fx) drag(in docs.DragBackIn) (docs.DragBackOut, error) {
	f.t.Helper()
	if in.BaseRevision == 0 {
		in.BaseRevision = f.get(in.DocumentID).Revision
	}
	out, err := f.call(f.ownA, "stash.drag_back", in)
	if err != nil {
		return docs.DragBackOut{}, err
	}
	return out.(docs.DragBackOut), nil
}

func TestDragBackIsAnExplicitRevisionSafeEdit(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Ch", Content: "First paragraph.\n\nSecond paragraph.\n\nThird paragraph.\n"})
	it := f.stash(f.pidA, "Cut", "A paragraph that was cut.")
	before := f.get(d.ID)
	if got := f.get(d.ID); got.Content != before.Content {
		t.Fatal("stashing changes nothing in a document")
	}
	// At the end by default; the item stays (copy).
	out, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it.ID, DocumentID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	got := f.get(d.ID)
	if got.Content != "First paragraph.\n\nSecond paragraph.\n\nThird paragraph.\n\nA paragraph that was cut.\n" || got.Revision != before.Revision+1 || out.WordCount != before.WordCount+5 {
		t.Fatalf("append: %q %+v", got.Content, out)
	}
	if got.Content[out.Start:out.End] != "A paragraph that was cut." {
		t.Errorf("the range says where it went: %d..%d", out.Start, out.End)
	}
	if list, _ := f.call(f.ownA, "stash.list", stash.ListIn{ID: f.pidA}); list.(stash.ListOut).Total != 1 {
		t.Error("copy leaves the item in the stash")
	}
	// Before the block that starts at the position; after the block that contains it.
	body := got.Content
	atSecond := strings.Index(body, "Second")
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it.ID, DocumentID: d.ID, At: ip(atSecond)}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(d.ID).Content; !strings.HasPrefix(got, "First paragraph.\n\nA paragraph that was cut.\n\nSecond paragraph.") {
		t.Fatalf("before a block: %q", got)
	}
	cur := f.get(d.ID)
	mid := strings.Index(cur.Content, "Third") + 3
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it.ID, DocumentID: d.ID, At: ip(mid), Mode: "move"}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(d.ID).Content; !strings.Contains(got, "Third paragraph.\n\nA paragraph that was cut.\n\nA paragraph that was cut.\n") {
		t.Fatalf("after the containing block: %q", got)
	}
	if list, _ := f.call(f.ownA, "stash.list", stash.ListIn{ID: f.pidA}); list.(stash.ListOut).Total != 0 {
		t.Error("move trashes the item")
	}
	// A trashed item cannot be dragged; a stale revision is a conflict; history keeps the text before.
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it.ID, DocumentID: d.ID}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("trashed item: %v", err)
	}
	it2 := f.stash(f.pidA, "More", "Second cut.")
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it2.ID, DocumentID: d.ID, BaseRevision: 1}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("stale revision: %v", err)
	}
	// Inline: one paragraph into a paragraph, at a position.
	short := f.stash(f.pidA, "Phrase", "and then some")
	cur = f.get(d.ID)
	at := strings.Index(cur.Content, "First paragraph") + len("First paragraph")
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: short.ID, DocumentID: d.ID, At: ip(at), Placement: "inline"}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(d.ID).Content; !strings.HasPrefix(got, "First paragraphand then some.\n\n") {
		t.Fatalf("inline: %q", got)
	}
	// Inline cannot go between paragraphs or hold a blank line.
	blank := strings.Index(f.get(d.ID).Content, "\n\n") + 1
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: short.ID, DocumentID: d.ID, At: ip(blank), Placement: "inline"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("inline between paragraphs: %v", err)
	}
	two := f.stash(f.pidA, "Two", "one\n\ntwo")
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: two.ID, DocumentID: d.ID, At: ip(3), Placement: "inline"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("inline with a blank line: %v", err)
	}
	// Another project's document is not found.
	other := f.doc(f.ownB, f.pidB, docs.CreateIn{Title: "Elsewhere", Content: "x"})
	if _, err := f.call(f.ownA, "stash.drag_back", docs.DragBackIn{ID: f.pidA, ItemID: it2.ID, DocumentID: other.ID, BaseRevision: 1}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a document of another project: %v", err)
	}
	// It does not count as words written.
	var added int
	_ = f.d.Read().QueryRow(`SELECT COALESCE(sum(words_added),0) FROM writing_sessions`).Scan(&added)
	if added > 40 { // the created paragraphs only
		t.Errorf("words moved from the stash earned goal credit: %d", added)
	}
}

func TestDragBackKeepsAlternativesAnchored(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Ch", Content: altBody})
	m := f.createAlt(d, "word", "window", "pane")
	it := f.stash(f.pidA, "Cut", "A paragraph before.")
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it.ID, DocumentID: d.ID, At: ip(0)}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	a := f.alts(d.ID).Items[0]
	if a.Status != "active" || a.Start != m.Alternative.Start+len("A paragraph before.\n\n") {
		t.Fatalf("anchor after a drag: %+v", a)
	}
	// Inline into an anchor is refused; next to it is fine.
	cur := f.get(d.ID)
	into := strings.Index(cur.Content, "window") + 2
	phrase := f.stash(f.pidA, "p", "xx")
	if _, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: phrase.ID, DocumentID: d.ID, At: ip(into), Placement: "inline"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("into an alternative: %v", err)
	}
	_ = alts.Marker
}

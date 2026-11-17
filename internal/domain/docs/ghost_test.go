package docs_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

func (f *fx) ghost(d docs.Doc, ranges ...docs.Range) docs.GhostMut {
	f.t.Helper()
	out, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Ranges: ranges})
	if err != nil {
		f.t.Fatalf("ghost.create: %v", err)
	}
	f.invariant(d.ID)
	return out.(docs.GhostMut)
}

func (f *fx) searchBody(id string) string {
	f.t.Helper()
	var b string
	if err := f.d.Read().QueryRow(`SELECT body FROM doc_text WHERE document_id = ?`, id).Scan(&b); err != nil {
		f.t.Fatal(err)
	}
	return b
}

func TestGhostingExcludesTextFromCountAndSearchButKeepsIt(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Ghost", Content: altBody})
	full := d.WordCount
	g := f.ghost(d, docs.Range{Quote: "She waited by the old window."})
	if g.WordCount != full-6 || g.GhostedWords != 6 || len(g.Ghosts) != 1 {
		t.Fatalf("count %d (was %d), ghosted %d, ghosts %d", g.WordCount, full, g.GhostedWords, len(g.Ghosts))
	}
	cur := f.get(d.ID)
	if !strings.Contains(cur.Content, "<scriobh-ghost>She waited by the old window.</scriobh-ghost>") || cur.WordCount != g.WordCount {
		t.Fatalf("the words stay in the text, tagged: %q", cur.Content)
	}
	if sb := f.searchBody(d.ID); strings.Contains(sb, "waited") || !strings.Contains(sb, "Nothing moved") {
		t.Fatalf("the search text leaves out ghosted words only: %q", sb)
	}
	l, err := f.call(f.ownA, "ghost.list", docs.GhostListIn{ID: d.ID})
	if err != nil {
		t.Fatal(err)
	}
	gl := l.(docs.GhostListOut)
	if gl.RawWordCount != full || gl.WordCount != full-6 || gl.GhostedWords != 6 || len(gl.Items) != 1 || gl.Items[0].Preview != "She waited by the old window." {
		t.Fatalf("ghost.list: %+v", gl)
	}
	// Export reads the text as it is: the ghosted words are exported by default.
	// Ghosting earns no goal credit and is not a deletion in history.
	id := gl.Items[0].ID
	// The ghost keeps its id when the text around it changes.
	f.mustSave(cur, "Opening line.\n\n"+cur.Content)
	l2, _ := f.call(f.ownA, "ghost.list", docs.GhostListIn{ID: d.ID})
	if got := l2.(docs.GhostListOut).Items; len(got) != 1 || got[0].ID != id || got[0].CreatedBy == "" {
		t.Fatalf("the ghost's identity survives a save: %+v", got)
	}
	// Revive: the tags go, the words count again.
	cur = f.get(d.ID)
	r, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: cur.Revision, GhostIDs: []string{id}})
	if err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	rm := r.(docs.GhostMut)
	if rm.GhostedWords != 0 || len(rm.Ghosts) != 0 || rm.WordCount != full+2 || strings.Contains(f.get(d.ID).Content, "scriobh-ghost") {
		t.Fatalf("revive: %+v (full %d)", rm, full)
	}
	if !strings.Contains(f.searchBody(d.ID), "waited") {
		t.Error("revived words are searchable again")
	}
}

func TestGhostRangesAndRefusals(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "G", Content: "First paragraph here.\n\n- a list item\n\n```\ncode here\n```\n\nSecond one.\n"})
	try := func(r ...docs.Range) error {
		_, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Ranges: r})
		return err
	}
	for name, r := range map[string][]docs.Range{
		"none":             nil,
		"across blocks":    {{Quote: "here.\n\n- a list"}},
		"in code":          {{Quote: "code here"}},
		"blank":            {{Start: ip(21), End: ip(22)}},
		"missing text":     {{Quote: "nothing like it"}},
		"half a quote":     {{Start: ip(0)}},
		"overlap":          {{Quote: "First paragraph"}, {Quote: "paragraph here"}},
		"already a marker": {{Quote: "<scriobh-ghost>"}},
	} {
		if err := try(r...); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A list item is a text block: fine. Two paragraphs: one range each.
	g := f.ghost(d, docs.Range{Quote: "a list item"}, docs.Range{Quote: "First paragraph here."}, docs.Range{Quote: "Second one."})
	if len(g.Ghosts) != 3 {
		t.Fatalf("%+v", g.Ghosts)
	}
	cur := f.get(d.ID)
	if got := text.WordCount(cur.Content); got != 0 || cur.WordCount != 0 {
		t.Errorf("every word ghosted: %d %d", got, cur.WordCount)
	}
	// Ghosting what is ghosted is refused (the markers are in the text now).
	if err := try(docs.Range{Quote: "First paragraph here."}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("ghost again: %v", err)
	}
	// Reviving by a range, then all.
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: cur.Revision, Start: ip(0), End: ip(5)}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if got := f.get(d.ID); strings.Count(got.Content, text.GhostOpen) != 2 {
		t.Fatalf("revived the one the range touches: %q", got.Content)
	}
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: cur.Revision, All: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.get(d.ID); strings.Contains(got.Content, "ghost") || got.WordCount == 0 {
		t.Fatalf("all revived: %q", got.Content)
	}
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: cur.Revision, All: true}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("nothing to revive: %v", err)
	}
	if _, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: cur.Revision}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("revive needs a target: %v", err)
	}
}

func TestGhostAndAlternativesDoNotCollide(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "GA", Content: altBody})
	m := f.createAlt(d, "word", "window", "pane")
	try := func(q string) error {
		_, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Ranges: []docs.Range{{Quote: q}}})
		return err
	}
	// Starting or ending inside an anchor is refused; ending exactly at its edge is fine.
	if err := try("old wind"); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("ends inside an anchor: %v", err)
	}
	if err := try("dow. Nothing"); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("starts inside an anchor: %v", err)
	}
	// A ghost may hold an alternative; choosing inside it keeps the tags.
	g := f.ghost(d, docs.Range{Quote: "She waited by the old window."})
	_ = g
	f.choose(d, m.Alternative.ID, m.Alternative.Variants[1].ID, "")
	if got := f.get(d.ID).Content; !strings.Contains(got, "<scriobh-ghost>She waited by the old pane.</scriobh-ghost>") {
		t.Fatalf("a ghost holding an alternative: %q", got)
	}
	// A ghost inside the chosen text of an alternative cannot be made by quote...
	// ...and an edit that puts a marker inside an anchor orphans it rather than adopting it.
	cur := f.get(d.ID)
	f.mustSave(cur, strings.Replace(cur.Content, "old pane.", "old pa<scriobh-ghost>n</scriobh-ghost>e.", 1))
	if a := f.alts(d.ID).Items[0]; a.Status == "active" && strings.Contains(a.Quote, "scriobh") {
		t.Fatalf("a marker inside a variant: %+v", a)
	}
}

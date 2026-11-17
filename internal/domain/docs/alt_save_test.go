package docs_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

func (f *fx) withAlts(content string, quote string, variants ...string) (docs.Doc, docs.AltMut) {
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "A", Content: content})
	m := f.createAlt(d, "word", quote, variants...)
	return f.get(d.ID), m
}

// An editor saves the whole text it holds, comment line included. Typing far
// from an anchor must not change that line, so the stored text is exactly what
// was sent (no churn in history, the mirror or Git).
func TestSaveFarFromAnAnchorLeavesTheCommentByteIdentical(t *testing.T) {
	f := newFx(t)
	d, _ := f.withAlts(altBody, "window", "pane")
	line := alts.Split(d.Content).Line
	for i, edit := range []func(string) string{
		func(s string) string { return "A new first line.\n\n" + s },
		func(s string) string {
			return strings.Replace(s, "Last paragraph here.", "Last paragraph here, and a little more.", 1)
		},
		func(s string) string { return strings.Replace(s, "the quiet stayed.", "the quiet lingered on.", 1) },
	} {
		next := edit(d.Content)
		got := f.mustSave(d, next)
		if got.Content != next {
			t.Fatalf("edit %d: the stored text differs from what the editor sent\nsent:   %q\nstored: %q", i, next, got.Content)
		}
		if alts.Split(got.Content).Line != line {
			t.Errorf("edit %d changed the comment line", i)
		}
		d = got
	}
	// A stale copy of the comment (anchors, chosen) is harmless: same variants, nothing to apply.
	items, _ := altstore.Load(f.d.Read(), d.ID)
	stale := items
	stale[0].Start, stale[0].End = 999, 1004
	stale[0].Before, stale[0].After = "x", "y"
	next := alts.Split(d.Content).Compose(alts.Serialize(stale))
	got := f.mustSave(d, next)
	if alts.Split(got.Content).Line != alts.Serialize(mustLoad(t, f, d.ID)) {
		t.Fatal("the server's comment is canonical")
	}
	if got.Content == next {
		t.Error("a stale anchor in the comment is replaced with the true one")
	}
}

func mustLoad(t *testing.T, f *fx, id string) []alts.Item {
	t.Helper()
	items, err := altstore.Load(f.d.Read(), id)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestSavingPlainMarkdownKeepsAlternatives(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	// A client that knows nothing of alternatives saves the body only.
	plain := "Preface line.\n\n" + alts.Split(d.Content).Body
	got := f.mustSave(d, plain)
	if !strings.HasSuffix(got.Content, "\n") || !alts.Split(got.Content).HasLine {
		t.Fatalf("the comment is added back: %q", got.Content)
	}
	l := f.alts(d.ID)
	if len(l.Items) != 1 || l.Items[0].ID != m.Alternative.ID || l.Items[0].Start != m.Alternative.Start+len("Preface line.\n\n") {
		t.Fatalf("alternatives are kept and re-anchored: %+v", l.Items)
	}
	// And a plain save that changes nothing else is idempotent.
	same := f.mustSave(got, alts.Split(got.Content).Body)
	if same.Revision != got.Revision {
		t.Errorf("an unchanged body with no comment is not a change: %d vs %d", same.Revision, got.Revision)
	}
}

func TestEditingAnAnchoredWordInPlace(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	// Type inside the word: it is adopted as the wording, a new variant since the original is immutable.
	got := f.mustSave(d, strings.Replace(d.Content, "The quiet house", "The quiXet house", 1))
	l := f.alts(d.ID)
	a := l.Items[0]
	if a.Status != "active" || a.Quote != "quiXet" || len(a.Variants) != 3 || a.Variants[0].Text != "quiet" || !a.Variants[0].Original {
		t.Fatalf("adopted: %+v", a)
	}
	_ = got
	// The original is still one step away.
	f.choose(d, m.Alternative.ID, "", "original")
	if !strings.HasPrefix(f.get(d.ID).Content, "The quiet house") {
		t.Fatal("original")
	}
	// Select the word and type another one that is already a variant: that variant is chosen.
	cur := f.get(d.ID)
	f.mustSave(cur, strings.Replace(cur.Content, "The quiet house", "The still house", 1))
	if a := f.alts(d.ID).Items[0]; a.Quote != "still" || len(a.Variants) != 3 && len(a.Variants) != 2 {
		t.Fatalf("typing a known wording selects it: %+v", a)
	}
}

func TestDeletingAnAnchoredWordOrphansItAndReanchorRepairs(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still", "hushed")
	cur := f.mustSave(d, strings.Replace(d.Content, "The quiet house", "The house", 1))
	l := f.alts(d.ID)
	if len(l.Items) != 1 || l.Items[0].Status != "orphaned" || l.Orphaned != 1 || l.Active != 0 || len(l.Items[0].Variants) != 3 {
		t.Fatalf("orphaned, with its wordings kept: %+v", l)
	}
	if !strings.Contains(cur.Content, `"orphan":true`) {
		t.Error("an orphan is marked in the comment")
	}
	if l := f.alts(d.ID); len(l.Items) != 1 {
		t.Fatal("never dropped silently")
	}
	// Orphans cannot be chosen.
	if _, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Step: "next"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("choose on an orphan: %v", err)
	}
	// An orphan filter lists them.
	out, _ := f.call(f.ownA, "alternative.list", docs.AltListIn{ID: d.ID, Status: "orphaned"})
	if len(out.(docs.AltListOut).Items) != 1 {
		t.Error("status filter")
	}
	// Attach it to another word: it becomes a new wording and is chosen; nothing is rewritten.
	cur = f.get(d.ID)
	r, err := f.call(f.ownA, "alternative.reanchor", docs.AltReanchorIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Quote: "empty", Occurrence: ip(0)})
	if err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	a := r.(docs.AltMut).Alternative
	if a.Status != "active" || a.Quote != "empty" || len(a.Variants) != 4 || f.get(d.ID).Content[:len(alts.Split(f.get(d.ID).Content).Body)] != alts.Split(cur.Content).Body {
		t.Fatalf("reanchored: %+v", a)
	}
}

func TestUndoingTheDeletionReattachesTheOrphan(t *testing.T) {
	f := newFx(t)
	d, _ := f.withAlts(altBody, "quiet", "still")
	orig := d.Content
	f.mustSave(d, strings.Replace(d.Content, "The quiet house", "The house", 1))
	cur := f.get(d.ID)
	// The writer types the word back where it was (an undo): the exact text and
	// context are found again on the next save.
	f.mustSave(cur, strings.Replace(cur.Content, "The house", "The quiet house", 1))
	l := f.alts(d.ID)
	if l.Items[0].Status != "active" || l.Items[0].Start != 4 {
		t.Fatalf("an orphan whose exact text returns is reattached: %+v", l.Items[0])
	}
	_ = orig
}

func TestRemovedVariantTextIsKeptInHistoryInsideTheThrottleWindow(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still", "a distinctive wording")
	// Two quick saves: the autosave throttle would not record the text that held the wording.
	cur := f.mustSave(d, "Opening.\n\n"+d.Content)
	_ = cur
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.delete", docs.AltDeleteIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if got := f.get(d.ID); alts.Split(got.Content).HasLine || !strings.HasPrefix(got.Content, "Opening.\n\nThe quiet house") {
		t.Fatalf("resolving keeps the chosen wording and drops the annotation: %q", got.Content)
	}
	var found int
	rows, err := f.d.Read().Query(`SELECT content FROM revision_blobs WHERE document_id = ?`, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		_ = rows.Scan(&b)
		if strings.Contains(string(b), "a distinctive wording") {
			found++
		}
	}
	if found == 0 {
		t.Fatal("the wording is gone from history: nothing holds the text that carried it")
	}
	// The same through a save that drops the item by editing the comment.
	d2, m2 := f.withAlts(altBody, "quiet", "another unique wording")
	_ = m2
	f.mustSave(d2, alts.Split(d2.Content).Body+"\n"+alts.Marker+`{"version":1,"sig":"`+alts.Sig(mustLoad(t, f, d2.ID))+`","items":[]} -->`+"\n")
	if len(f.alts(d2.ID).Items) != 0 {
		t.Fatal("an item missing from the comment is resolved")
	}
	found = 0
	rows2, _ := f.d.Read().Query(`SELECT content FROM revision_blobs WHERE document_id = ?`, d2.ID)
	defer rows2.Close()
	for rows2.Next() {
		var b []byte
		_ = rows2.Scan(&b)
		if strings.Contains(string(b), "another unique wording") {
			found++
		}
	}
	if found == 0 {
		t.Fatal("dropping an item through the comment must keep the text that carried it")
	}
}

func TestSourceViewEditsOfTheComment(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	items := mustLoad(t, f, d.ID)
	body := alts.Split(d.Content).Body
	edit := func(mut func(it *alts.Item)) error {
		its := mustLoad(t, f, d.ID)
		mut(&its[0])
		cur := f.get(d.ID)
		c := alts.Split(cur.Content).Compose(f.handEdited(d.ID, alts.Serialize(its)))
		_, err := f.save(cur, c)
		return err
	}
	// A new wording typed into the comment is added.
	if err := edit(func(it *alts.Item) { it.Variants = append(it.Variants, alts.Variant{Text: "calm", Source: "manual"}) }); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if got := f.alts(d.ID).Items[0]; len(got.Variants) != 3 || got.Variants[2].Text != "calm" || got.Variants[2].ID == "" {
		t.Fatalf("added in the comment: %+v", got.Variants)
	}
	// The chosen wording is the text's to say: a comment that rewords or drops it
	// is not applied (the original is chosen here, so it is kept as it is).
	if err := edit(func(it *alts.Item) { it.Variants[0].Text = "QUIET" }); err != nil {
		t.Fatalf("a stale chosen wording is harmless: %v", err)
	}
	f.invariant(d.ID)
	if got := f.alts(d.ID).Items[0]; got.Variants[0].Text != "quiet" || !got.Variants[0].Original {
		t.Fatalf("the tables keep the chosen wording: %+v", got.Variants)
	}
	// The original cannot be changed or removed once it is not the chosen one.
	f.choose(f.get(d.ID), m.Alternative.ID, f.alts(d.ID).Items[0].Variants[1].ID, "")
	if err := edit(func(it *alts.Item) { it.Variants[0].Text = "QUIET" }); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("original edited: %v", err)
	}
	// Nor the chosen wording be removed, nor the scope changed.
	if err := edit(func(it *alts.Item) { it.Variants = it.Variants[1:] }); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("original removed: %v", err)
	}
	if err := edit(func(it *alts.Item) { it.Scope = "sentence" }); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("scope changed: %v", err)
	}
	// A comment that is not valid JSON is refused with a message, and nothing is written.
	cur := f.get(d.ID)
	bad := body + "\n" + alts.Marker + `{"version":1,"items":[oops]} -->` + "\n"
	if _, err := f.save(cur, bad); ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("invalid comment: %v", err)
	}
	if f.get(d.ID).Revision != cur.Revision {
		t.Error("a refused save writes nothing")
	}
	_ = items
	_ = m
}

// Documents whose comment was written by hand in the first sketch's shape, or
// that the editor has been preserving with no tables behind them, must still be
// saveable and are read into alternatives.
func TestHandWrittenAndLegacyComments(t *testing.T) {
	f := newFx(t)
	short := "The quiet house was quiet.\n\n" + alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still","low"],"chosen":"quiet"}]} -->` + "\n"
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Short", Content: short})
	f.invariant(d.ID)
	l := f.alts(d.ID)
	if len(l.Items) != 1 || l.Items[0].Start != 4 || len(l.Items[0].Variants) != 3 || !l.Items[0].Variants[0].Original || l.Items[0].Variants[0].Text != "quiet" {
		t.Fatalf("short form: %+v", l.Items)
	}
	if strings.Contains(f.get(d.ID).Content, `"quote"`) {
		t.Error("stored in the long form")
	}
	// The editor corpus' no-scope comment, with nothing to anchor to: an orphan, never an error.
	tricky := "Text.\n\n" + alts.Marker + `{"version":1,"items":[{"quote":"a_b_c *x* [y] # z > w","variants":["1.","- 2","[[3]]","%%4%%"],"chosen":"\\"}]} -->` + "\n"
	d2 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Tricky", Content: tricky})
	f.invariant(d2.ID)
	if l := f.alts(d2.ID); len(l.Items) != 1 || l.Items[0].Status != "orphaned" {
		t.Fatalf("no scope, quote not in the text: %+v", l.Items)
	}
	f.mustSave(d2, "Text, edited.\n\n"+alts.Split(f.get(d2.ID).Content).Line+"\n")
	empty := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Empty", Content: "Only text.\n\n" + alts.Marker + `{"version":1,"items":[]} -->` + "\n"})
	f.invariant(empty.ID)
	if got := f.get(empty.ID); alts.Split(got.Content).HasLine {
		t.Errorf("an empty comment is dropped: %q", got.Content)
	}
	// A broken comment on create is just text: not an error.
	broken := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Broken", Content: "Text.\n\n" + alts.Marker + `{nope} -->` + "\n"})
	if !strings.Contains(f.get(broken.ID).Content, "{nope}") {
		t.Error("kept as an ordinary comment")
	}
}

func TestSplitDividesAlternatives(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Split", Content: altBody})
	a := f.createAlt(d, "word", "quiet", "still")
	b := f.createAlt(d, "word", "window", "pane")
	f.createAlt(d, "word", "paragraph", "chapter")
	body := alts.Split(f.get(d.ID).Content).Body
	// Inside an anchor: refused with its name.
	cut := strings.Index(body, "window") + 2
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Offset: cut}); ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "window") {
		t.Fatalf("a cut inside an anchor: %v", err)
	}
	// Between the paragraphs: each half keeps its own.
	cut = strings.Index(body, "She waited")
	out, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Offset: cut, Title: "Tail"})
	if err != nil {
		t.Fatal(err)
	}
	o := out.(docs.SplitOut)
	f.invariant(o.Head.ID)
	f.invariant(o.Tail.ID)
	hl, tl := f.alts(o.Head.ID), f.alts(o.Tail.ID)
	if len(hl.Items) != 1 || hl.Items[0].Quote != "quiet" || hl.Items[0].ID != a.Alternative.ID {
		t.Fatalf("head: %+v", hl.Items)
	}
	if len(tl.Items) != 2 || tl.Items[0].Quote != "window" || tl.Items[0].ID == b.Alternative.ID {
		t.Fatalf("tail keeps its alternatives under new ids: %+v", tl.Items)
	}
	// Choosing in the tail works on the tail's text.
	f.choose(o.Tail, tl.Items[0].ID, tl.Items[0].Variants[1].ID, "")
	if !strings.Contains(f.get(o.Tail.ID).Content, "old pane.") {
		t.Fatal("tail choose")
	}
	// A cut inside the comment is refused.
	if _, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: o.Head.ID, BaseRevision: f.get(o.Head.ID).Revision, Offset: len(f.get(o.Head.ID).Content) - 5}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("cut in the comment: %v", err)
	}
}

func TestRestoreFollowsTheAlternativesOfTheRestoredText(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Restore", Content: altBody})
	// A revision of the text before it had any alternatives.
	if _, err := f.call(f.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "plain"}); err != nil {
		t.Fatal(err)
	}
	m := f.createAlt(d, "word", "quiet", "still")
	f.choose(d, m.Alternative.ID, m.Alternative.Variants[1].ID, "")
	var plain string
	if err := f.d.Read().QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? AND label = 'plain'`, d.ID).Scan(&plain); err != nil {
		t.Fatal(err)
	}
	cur := f.get(d.ID)
	if _, err := f.call(f.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: plain, BaseRevision: cur.Revision}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if got := f.get(d.ID); got.Content != altBody || len(f.alts(d.ID).Items) != 0 {
		t.Fatalf("restoring text without alternatives leaves none: %q", got.Content)
	}
	// Restoring the version that had them brings them back, with the chosen wording in the text.
	var withAlts string
	if err := f.d.Read().QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? AND reason = 'alternative' ORDER BY recorded_at DESC LIMIT 1`, d.ID).Scan(&withAlts); err != nil {
		t.Fatal(err)
	}
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: withAlts, BaseRevision: cur.Revision}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	l := f.alts(d.ID)
	if len(l.Items) != 1 || l.Items[0].Quote != "still" || len(l.Items[0].Variants) != 2 || !strings.HasPrefix(f.get(d.ID).Content, "The still house") {
		t.Fatalf("restored alternatives: %+v", l.Items)
	}
}

func TestAlternativeLimitsAndTrashedDocument(t *testing.T) {
	f := newFx(t)
	f.reg.Env().SetService(altstore.ServiceLimits, alts.Limits{PerDoc: 2, VariantsPer: 3})
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Limits", Content: altBody})
	m := f.createAlt(d, "word", "quiet", "still", "low")
	f.createAlt(d, "word", "window", "pane")
	cur := f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: cur.Revision, Scope: "word", Quote: "street"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("past the per-document limit: %v", err)
	}
	if _, err := f.call(f.ownA, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Text: "fourth"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("past the variant limit: %v", err)
	}
	// Duplicate wordings are refused.
	if _, err := f.call(f.ownA, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: f.alts(d.ID).Items[1].ID, Text: "pane"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("duplicate: %v", err)
	}
	// The original is never removed or edited.
	orig := m.Alternative.Variants[0].ID
	if _, err := f.call(f.ownA, "alternative.remove_variant", docs.AltVariantRemoveIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, VariantID: orig}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("remove original: %v", err)
	}
	if _, err := f.call(f.ownA, "alternative.edit_variant", docs.AltVariantEditIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, VariantID: orig, Text: "x"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("edit original: %v", err)
	}
	// The database refuses it too.
	if _, err := f.d.Writer().Exec(`UPDATE alternative_variants SET text = 'x' WHERE id = ?`, orig); err == nil {
		t.Error("the trigger must refuse an edit of the original")
	}
	// A stale revision is a conflict; a trashed document is refused.
	if _, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: 1, AlternativeID: m.Alternative.ID, Step: "next"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("stale: %v", err)
	}
	if _, err := f.call(f.ownA, "doc.trash", docs.DocIn{ID: d.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, AlternativeID: m.Alternative.ID, Step: "next"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("trashed: %v", err)
	}
}

func TestEditVariantRemoveVariantAndReorder(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still", "low")
	a := m.Alternative
	cur := f.get(d.ID)
	// Edit a variant that is not in the text.
	if _, err := f.call(f.ownA, "alternative.edit_variant", docs.AltVariantEditIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: a.ID, VariantID: a.Variants[2].ID, Text: "lower"}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	// Choose it, then edit the chosen wording: the text changes with it.
	f.choose(d, a.ID, a.Variants[2].ID, "")
	cur = f.get(d.ID)
	r, err := f.call(f.ownA, "alternative.edit_variant", docs.AltVariantEditIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: a.ID, VariantID: a.Variants[2].ID, Text: "lowest"})
	if err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if !strings.HasPrefix(f.get(d.ID).Content, "The lowest house") || r.(docs.AltMut).Replaced.NewEnd != 4+6 {
		t.Fatalf("editing the chosen wording rewrites the text: %q", f.get(d.ID).Content[:30])
	}
	// Removing the chosen wording puts the original back.
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.remove_variant", docs.AltVariantRemoveIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: a.ID, VariantID: a.Variants[2].ID}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if !strings.HasPrefix(f.get(d.ID).Content, "The quiet house") || len(f.alts(d.ID).Items[0].Variants) != 2 {
		t.Fatal("remove chosen")
	}
	// Reorder: put "still" first.
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.reorder_variant", docs.AltVariantReorderIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: a.ID, VariantID: a.Variants[1].ID}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if v := f.alts(d.ID).Items[0].Variants; v[0].Text != "still" || v[1].Text != "quiet" || !v[1].Original {
		t.Fatalf("reorder: %+v", v)
	}
	// Add a removed wording back where it was.
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: a.ID, Text: "lowest", AfterVariantID: a.Variants[1].ID}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if v := f.alts(d.ID).Items[0].Variants; len(v) != 3 || v[1].Text != "lowest" {
		t.Fatalf("add after: %+v", v)
	}
}

func TestPurgeLeavesNoAlternativeRows(t *testing.T) {
	f := newFx(t)
	d, _ := f.withAlts(altBody, "quiet", "still")
	cur := f.get(d.ID)
	g, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: cur.Revision, Ranges: []docs.Range{{Quote: "Last paragraph here."}}})
	if err != nil || len(g.(docs.GhostMut).Ghosts) != 1 {
		t.Fatalf("ghost: %v", err)
	}
	count := func() (n int) {
		for _, q := range []string{`alternatives`, `alternative_variants`, `ghosts`} {
			var c int
			if err := f.d.Read().QueryRow(`SELECT count(*) FROM ` + q).Scan(&c); err != nil {
				t.Fatal(err)
			}
			n += c
		}
		return n
	}
	if count() == 0 {
		t.Fatal("rows expected")
	}
	if _, err := f.call(f.ownA, "project.trash", projects.StateIn{ID: f.pidA}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.call(f.ownA, "project.purge", projects.PurgeIn{ID: f.pidA, ConfirmTitle: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("%d rows left after a purge", n)
	}
}

// Choosing between wordings, ghosting and moving text from the stash are not
// writing: the goal earns nothing from them (and nothing is subtracted).
func TestChoosingEarnsNoGoalCredit(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Goal", Content: "Short.\n"})
	added := func() int {
		var n int
		if err := f.d.Read().QueryRow(`SELECT COALESCE(sum(words_added),0) FROM writing_sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	f.mustSave(d, altBody)
	base := added()
	if base == 0 {
		t.Fatal("typing earns credit")
	}
	cur := f.get(d.ID)
	m := f.createAlt(cur, "sentence", "She waited by the old window.", "She stood at the window and watched the empty street for a very long time indeed, hoping.")
	if added() != base {
		t.Fatalf("creating an alternative earned %d", added()-base)
	}
	f.choose(f.get(d.ID), m.Alternative.ID, m.Alternative.Variants[1].ID, "")
	if added() != base {
		t.Fatalf("choosing a longer wording earned %d", added()-base)
	}
	f.choose(f.get(d.ID), m.Alternative.ID, "", "original")
	if added() != base {
		t.Fatalf("undoing earned %d", added()-base)
	}
	f.ghost(f.get(d.ID), docs.Range{Quote: "Last paragraph here."})
	if added() != base {
		t.Fatalf("ghosting earned %d", added()-base)
	}
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "ghost.revive", docs.GhostReviveIn{ID: d.ID, BaseRevision: cur.Revision, All: true}); err != nil {
		t.Fatal(err)
	}
	if added() != base {
		t.Fatalf("reviving earned %d", added()-base)
	}
}

// An editor keeps sending the comment line it loaded. After the server adopts a
// wording the writer typed in place, that line is stale about the chosen wording;
// the next save with it must go through and change nothing it should not.
func TestStaleCommentAfterAnAdoptionIsHarmless(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	stale := alts.Split(d.Content).Line // as the editor loaded it
	// Save 1: typing inside the word is adopted as a new wording of the alternative.
	one := f.mustSave(d, strings.Replace(d.Content, "The quiet house", "The quiXet house", 1))
	if a := f.alts(d.ID).Items[0]; a.Quote != "quiXet" || len(a.Variants) != 3 {
		t.Fatalf("adopted: %+v", a)
	}
	// Save 2: a further body edit, still carrying the old line.
	body := strings.Replace(alts.Split(one.Content).Body, "was empty", "was very empty", 1)
	two := f.mustSave(one, alts.Split(d.Content).Compose(stale)[:0]+body+"\n"+stale+"\n")
	if a := f.alts(d.ID).Items[0]; a.Quote != "quiXet" || len(a.Variants) != 3 || a.ID != m.Alternative.ID {
		t.Fatalf("the stale line must not undo the adoption: %+v", a)
	}
	if !strings.Contains(two.Content, "was very empty") {
		t.Fatal("the edit was lost")
	}
	// The same after the writer reworded a non-original chosen wording in place.
	three := f.mustSave(two, strings.Replace(two.Content, "quiXet", "quiYYet", 1))
	f.mustSave(three, alts.Split(three.Content).Body+"\n"+alts.Split(two.Content).Line+"\n")
	if a := f.alts(d.ID).Items[0]; a.Quote != "quiYYet" {
		t.Fatalf("a stale copy of a reworded chosen wording: %+v", a)
	}
}

// A last line that no alternatives stand behind is an ordinary comment: it
// must survive every op and every save, and never make a document unsaveable.
func TestALineWithNoAlternativesBehindItIsLeftAlone(t *testing.T) {
	f := newFx(t)
	broken := "The quiet house was empty.\n\nSecond paragraph here.\n\n" + alts.Marker + `{nope} -->` + "\n"
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Broken", Content: broken})
	line := alts.Marker + `{nope} -->`
	// A save that keeps the line works, even though the line is not valid.
	got := f.mustSave(d, strings.Replace(d.Content, "was empty", "was very empty", 1))
	if !strings.HasSuffix(got.Content, line+"\n") {
		t.Fatalf("the line must stay: %q", got.Content)
	}
	// A new, different invalid line is refused.
	if _, err := f.save(got, strings.Replace(got.Content, "{nope}", "{nope again}", 1)); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a changed invalid line: %v", err)
	}
	// Ops keep it.
	cur := f.get(d.ID)
	if _, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d.ID, BaseRevision: cur.Revision, Ranges: []docs.Range{{Quote: "Second paragraph here."}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.get(d.ID).Content, line) {
		t.Fatalf("ghost.create dropped the line: %q", f.get(d.ID).Content)
	}
	m := f.createAlt(f.get(d.ID), "word", "quiet", "still")
	f.invariant(d.ID)
	if c := f.get(d.ID).Content; !strings.Contains(c, line) || !strings.HasSuffix(c, m.Comment+"\n") {
		t.Fatalf("alternative.create must keep the old line and end with its own: %q", c)
	}
	// And split.
	b2 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Broken 2", Content: broken})
	cut := strings.Index(broken, "Second")
	out, err := f.call(f.ownA, "doc.split", docs.SplitIn{ID: b2.ID, BaseRevision: 1, Offset: cut})
	if err != nil {
		t.Fatal(err)
	}
	if o := out.(docs.SplitOut); !strings.Contains(o.Tail.Content, line) {
		t.Fatalf("split dropped the line: head %q tail %q", o.Head.Content, o.Tail.Content)
	}
	// A valid short-form line written straight into the row (no tables behind it) is the same.
	short := "The quiet house.\n\n" + alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still"]}]} -->` + "\n"
	d3 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Short", Content: "x"})
	if _, err := f.d.Writer().Exec(`UPDATE documents SET content = ? WHERE id = ?`, short, d3.ID); err != nil {
		t.Fatal(err)
	}
	cur = f.get(d3.ID)
	if _, err := f.call(f.ownA, "ghost.create", docs.GhostCreateIn{ID: d3.ID, BaseRevision: cur.Revision, Ranges: []docs.Range{{Quote: "house"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.get(d3.ID).Content, `"quote":"quiet"`) {
		t.Fatalf("a row-less line was overwritten: %q", f.get(d3.ID).Content)
	}
}

// The comment carries no author, so what an import or a hand-written line says
// can never disagree with the tables about who wrote a wording.
func TestForeignAuthorsAndHandWrittenLinesKeepTheInvariant(t *testing.T) {
	f := newFx(t)
	for name, line := range map[string]string{
		"short": `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["still"]}]}`,
		"long, foreign author": `{"version":1,"items":[{"id":"alt_foreign","scope":"word","chosen":"alv_f1","variants":[` +
			`{"id":"alv_f1","text":"quiet","original":true,"at":"2020-01-01T00:00:00.000Z","by":"usr_nobody"},{"id":"alv_f2","text":"still","by":"usr_nobody"}]}]}`,
		"ids that exist elsewhere": `{"version":1,"items":[{"id":"alt_x","scope":"word","chosen":"v1","variants":[{"id":"v1","text":"quiet","original":true},{"id":"v2","text":"still"}]}]}`,
	} {
		for i := 0; i < 2; i++ { // twice: the same ids in a second document are not a collision
			d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: name, Content: "The quiet house.\n\n" + alts.Marker + line + " -->\n"})
			f.invariant(d.ID)
			if l := f.alts(d.ID); len(l.Items) != 1 || l.Active != 1 || len(l.Items[0].Variants) != 2 {
				t.Fatalf("%s: %+v", name, l)
			}
			// And an edit of the text through the editor's path keeps it.
			f.mustSave(f.get(d.ID), "A line first.\n\n"+f.get(d.ID).Content)
		}
	}
}

// handEdited is what a person leaves behind by editing the comment in the
// Source view: the content changes and the sig, which they do not know to
// update, is still the one the comment was loaded with (the stored items').
func (f *fx) handEdited(id, line string) string {
	i := strings.Index(line, `"sig":"`)
	if i < 0 {
		return line
	}
	at := i + len(`"sig":"`)
	return line[:at] + alts.Sig(mustLoad(f.t, f, id)) + line[at+len(alts.Sig(nil)):]
}

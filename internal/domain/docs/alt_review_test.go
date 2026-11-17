package docs_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

func (f *fx) revisionCount(id string) (all, snapshots int) {
	f.t.Helper()
	if err := f.d.Read().QueryRow(`SELECT count(*), COALESCE(sum(reason = 'snapshot'),0) FROM document_revisions WHERE document_id = ?`, id).Scan(&all, &snapshots); err != nil {
		f.t.Fatal(err)
	}
	return
}

// Item 3: a comment that reads as JSON but fails the checks (a wording with a
// blank line in it) is kept as ordinary text, and the document stays saveable.
func TestACommentThatFailsTheChecksIsOrdinaryTextAndSavesKeepWorking(t *testing.T) {
	f := newFx(t)
	line := alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["a\n\nb","still"]}]} -->`
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Odd", Content: "The quiet house.\n\n" + line + "\n"})
	if !strings.Contains(f.get(d.ID).Content, `a\n\nb`) {
		t.Fatalf("kept as it was: %q", f.get(d.ID).Content)
	}
	if n := len(f.alts(d.ID).Items); n != 0 {
		t.Fatalf("no alternatives were made: %d", n)
	}
	cur := d
	for i := 0; i < 3; i++ { // ordinary saves keep working, line and all
		cur = f.mustSave(cur, strings.Replace(f.get(d.ID).Content, "house", "house, house", 1))
		if !strings.HasSuffix(cur.Content, line+"\n") {
			t.Fatalf("save %d lost the line: %q", i, cur.Content)
		}
	}
	// A different bad line is refused, the same one is not.
	if _, err := f.save(cur, strings.Replace(cur.Content, `"still"`, `"stiller"`, 1)); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a changed line that fails the checks: %v", err)
	}
	// And the CLI-style path: a plain Markdown client that sends only the body.
	f.mustSave(cur, "Only the body now.\n")
}

// Item 8: typing inside a chosen, non-original wording is the ordinary history
// of the text: one revision per autosave interval, not one per save.
func TestTypingInAChosenWordingDoesNotWriteARevisionPerSave(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	f.choose(d, m.Alternative.ID, m.Alternative.Variants[1].ID, "")
	before, snapBefore := f.revisionCount(d.ID)
	cur := f.get(d.ID)
	for i := 0; i < 8; i++ { // eight autosaves, each one more letter inside "still"
		cur = f.mustSave(cur, strings.Replace(cur.Content, "The st"+strings.Repeat("i", i+1)+"ll house", "The st"+strings.Repeat("i", i+2)+"ll house", 1))
		if i == 0 {
			cur = f.get(d.ID)
		}
	}
	after, snapAfter := f.revisionCount(d.ID)
	if snapAfter != snapBefore {
		t.Errorf("%d extra safety revisions for typing", snapAfter-snapBefore)
	}
	if after-before > 1 {
		t.Errorf("%d new revisions for 8 autosaves inside the throttle window", after-before)
	}
	if a := f.alts(d.ID).Items[0]; len(a.Variants) != 2 {
		t.Fatalf("a retyped non-original wording stays one wording: %+v", a.Variants)
	}
	// Removing a wording still keeps the text that held it.
	cur = f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.remove_variant", docs.AltVariantRemoveIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, VariantID: m.Alternative.Variants[1].ID}); err != nil {
		t.Fatal(err)
	}
	if _, s := f.revisionCount(d.ID); s <= snapAfter {
		t.Error("removing a wording must keep the text that held it")
	}
}

// Item 9: a save that carries an older copy of the comment (a cached client, a
// "keep mine" resend) must not remove what was added since.
func TestAnOlderCopyOfTheCommentDoesNotRemoveWhatWasAddedSince(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	old := alts.Split(d.Content).Line // what a client loaded
	// Someone adds a wording after that.
	cur := f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Text: "added later"}); err != nil {
		t.Fatal(err)
	}
	// The client saves, at the current revision, with its old comment.
	cur = f.get(d.ID)
	content := "Opening line.\n\n" + alts.Split(cur.Content).Body + "\n" + old + "\n"
	rev := cur.Revision
	out, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: rev, Content: &content})
	if err != nil {
		t.Fatal(err)
	}
	item := out.(docs.Item)
	f.invariant(d.ID)
	if a := f.alts(d.ID).Items[0]; len(a.Variants) != 3 || a.Variants[2].Text != "added later" {
		t.Fatalf("the wording added since was removed: %+v", a.Variants)
	}
	if len(item.Notices) != 1 || !strings.Contains(item.Notices[0], "older") {
		t.Errorf("the response says so: %v", item.Notices)
	}
	if !strings.HasPrefix(item.Item.Content, "Opening line.") {
		t.Error("the text of the save was kept")
	}
	// An edit made by hand in the Source view is not an old copy: it applies.
	cur = f.get(d.ID)
	its := mustLoad(t, f, d.ID)
	its[0].Variants = its[0].Variants[:2] // drop "added later" on purpose
	hand := alts.Split(cur.Content).Compose(f.handEdited(d.ID, alts.Serialize(its)))
	out, err = f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: cur.Revision, Content: &hand})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.alts(d.ID).Items[0]; len(got.Variants) != 2 || len(out.(docs.Item).Notices) != 0 {
		t.Fatalf("a hand edit applies: %+v %v", got.Variants, out.(docs.Item).Notices)
	}
}

// Item 11: text after the comment, and a text that only mentions the marker.
func TestOneCanonicalCommentAtTheEnd(t *testing.T) {
	f := newFx(t)
	d, _ := f.withAlts(altBody, "quiet", "still")
	line := alts.Split(d.Content).Line
	// Text appended after the comment: one comment at the end, the text kept in the body.
	got := f.mustSave(d, d.Content+"Appended after the comment.\n")
	if strings.Count(got.Content, alts.Marker) != 1 || !strings.HasSuffix(got.Content, line+"\n") || !strings.Contains(got.Content, "Appended after the comment.") {
		t.Fatalf("collapsed: %q", got.Content)
	}
	if strings.Index(got.Content, "Appended") > strings.Index(got.Content, alts.Marker) {
		t.Errorf("the appended text belongs before the comment: %q", got.Content)
	}
	if len(f.alts(d.ID).Items) != 1 {
		t.Fatal("alternatives kept")
	}
	// A dead duplicate in the middle of the text goes, whichever it was.
	dup := "Intro.\n\n" + line + "\n\nMore text.\n\n" + line + "\n"
	again := f.mustSave(got, dup)
	if strings.Count(again.Content, alts.Marker) != 1 {
		t.Fatalf("a duplicate comment was kept: %q", again.Content)
	}
	// A code sample that shows the marker is code, and is never rewritten.
	sample := "A sample:\n\n```\n" + line + "\n```\n\nAfter.\n"
	d2 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Sample", Content: strings.ReplaceAll(sample, "\n", "\r\n")})
	if !strings.Contains(f.get(d2.ID).Content, "\r\n") {
		t.Fatal("a document that only mentions the marker keeps its line endings")
	}
	g2 := f.mustSave(d2, strings.ReplaceAll(sample+"More.\n", "\n", "\r\n"))
	if !strings.Contains(g2.Content, "\r\n") || strings.Contains(g2.Content, "\r\n\r\n\r\n") {
		t.Fatalf("CRLF must stay for a document with no alternatives: %q", g2.Content)
	}
	if n := len(f.alts(d2.ID).Items); n != 0 {
		t.Fatalf("a code sample is not an alternatives comment: %d", n)
	}
}

// Item 5 on the save path: a comment typed into the Source view that names a
// word inside another word, or text in code, is not placed.
func TestCommentPlacedItemsAreCheckedOnSave(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "P", Content: "Another thing.\n\n```\ncode here\n```\n"})
	line := alts.Marker + `{"version":1,"items":[{"quote":"the","variants":["a"]},{"scope":"word","quote":"here","variants":["there"]}]} -->`
	got := f.mustSave(d, f.get(d.ID).Content+"\n"+line+"\n")
	l := f.alts(d.ID)
	if l.Active != 0 || l.Orphaned != 2 {
		t.Fatalf("neither is placed: %+v", l)
	}
	// Choosing on an orphan is refused, so code can never be rewritten.
	if _, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: got.Revision, AlternativeID: l.Items[1].ID, Step: "next"}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("choose on an orphan: %v", err)
	}
	_ = altstore.ServiceLimits
}

// A wording may be a phrase ("very quiet" for a word): that is a legal place, and
// no way of finding it again may orphan it for having a space in it.
func TestAPhraseWordingStaysActiveEverywhere(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "very quiet")
	cur := f.get(d.ID)
	if _, err := f.call(f.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "phrase"}); err != nil {
		t.Fatal(err)
	}
	f.choose(f.get(d.ID), m.Alternative.ID, m.Alternative.Variants[1].ID, "")
	active := func(when string) alts.Item {
		t.Helper()
		items := mustLoad(t, f, d.ID)
		if len(items) != 1 || items[0].Orphan || items[0].ChosenText() != "very quiet" {
			t.Fatalf("%s: %+v", when, items)
		}
		return items[0]
	}
	active("after choosing")
	_ = cur
	// A save that rewrites two places far apart, so the anchor is relocated by its context.
	c := f.get(d.ID)
	next := strings.Replace(strings.Replace(c.Content, "The very quiet", "A very quiet", 1), "Last paragraph here.", "Final paragraph there.", 1)
	f.mustSave(c, next)
	active("after a save that spans it")
	// Typing on at its edge.
	c = f.get(d.ID)
	f.mustSave(c, strings.Replace(c.Content, "very quiet house", "very quiets house", 1))
	it := mustLoad(t, f, d.ID)[0]
	if it.Orphan || it.ChosenText() != "very quiets" {
		t.Fatalf("typing on at the edge: %+v", it)
	}
	f.mustSave(f.get(d.ID), strings.Replace(f.get(d.ID).Content, "very quiets house", "very quiet house", 1))
	// A snapshot restored from before it was chosen, and one from after.
	var snap string
	if err := f.d.Read().QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? AND label = 'phrase'`, d.ID).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	c = f.get(d.ID)
	if _, err := f.call(f.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: snap, BaseRevision: c.Revision}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	if items := mustLoad(t, f, d.ID); len(items) != 1 || items[0].Orphan || items[0].ChosenText() != "quiet" {
		t.Fatalf("restored: %+v", items)
	}
	var withPhrase string
	if err := f.d.Read().QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? AND reason = 'alternative' ORDER BY recorded_at DESC, id DESC LIMIT 1`, d.ID).Scan(&withPhrase); err != nil {
		t.Fatal(err)
	}
	c = f.get(d.ID)
	if _, err := f.call(f.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: withPhrase, BaseRevision: c.Revision}); err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
}

// A hand edit of an older copy of the comment can add and change, never remove.
func TestAHandEditedOlderCopyRemovesNothing(t *testing.T) {
	f := newFx(t)
	d, m := f.withAlts(altBody, "quiet", "still")
	oldLine := alts.Split(d.Content).Line
	oldSig := alts.Sig(mustLoad(t, f, d.ID))
	cur := f.get(d.ID)
	if _, err := f.call(f.ownA, "alternative.add_variant", docs.AltVariantAddIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: m.Alternative.ID, Text: "added later"}); err != nil {
		t.Fatal(err)
	}
	// The older copy, with a wording of the client's own added by hand (the sig is the old one).
	items, err := alts.Parse(oldLine, alts.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	items[0].Variants = append(items[0].Variants, alts.Variant{Text: "mine", Source: "manual"})
	line := alts.Serialize(items)
	at := strings.Index(line, `"sig":"`) + len(`"sig":"`)
	line = line[:at] + oldSig + line[at+len(oldSig):]
	cur = f.get(d.ID)
	content := alts.Split(cur.Content).Body + "\n" + line + "\n"
	out, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: cur.Revision, Content: &content})
	if err != nil {
		t.Fatal(err)
	}
	f.invariant(d.ID)
	got := f.alts(d.ID).Items[0]
	var texts []string
	for _, v := range got.Variants {
		texts = append(texts, v.Text)
	}
	if strings.Join(texts, ",") != "quiet,still,mine,added later" && strings.Join(texts, ",") != "quiet,still,added later,mine" {
		t.Fatalf("added is applied and nothing is removed: %v", texts)
	}
	if len(out.(docs.Item).Notices) != 1 {
		t.Errorf("the response says so: %v", out.(docs.Item).Notices)
	}
}

// Dropping a wording needs a comment that shows it saw the stored ones.
func TestInlineDragBackRefusesAGhostTagAndACharacter(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Inline", Content: "One <scriobh-ghost>two three</scriobh-ghost> four 👍🏽 five, 🇮🇪🇫🇷 six.\n"})
	it := f.stash(f.pidA, "p", "inserted")
	body := f.get(d.ID).Content
	try := func(at int) error {
		_, err := f.drag(docs.DragBackIn{ID: f.pidA, ItemID: it.ID, DocumentID: d.ID, At: ip(at), Placement: "inline"})
		return err
	}
	u16 := func(byteAt int) int { return alts.Len16(body[:byteAt]) }
	for name, at := range map[string]int{
		"inside the opening tag": strings.Index(body, "<scriobh-ghost>") + 5,
		"inside the closing tag": strings.Index(body, "</scriobh-ghost>") + 4,
		"before a skin tone":     strings.Index(body, "🏽"),
		"between flag halves":    strings.Index(body, "🇮🇪") + len("🇮"),
		"between flags":          strings.Index(body, "🇫🇷"),
	} {
		if name == "between flags" {
			continue // a position between two whole flags is fine
		}
		if err := try(u16(at)); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := try(u16(strings.Index(body, " four"))); err != nil {
		t.Errorf("an ordinary place: %v", err)
	}
}

// The "Odd" document: a line kept as ordinary text stays when a comment of its own is made.
func TestAKeptOddLineSurvivesAnAlternativeBeingMade(t *testing.T) {
	f := newFx(t)
	odd := alts.Marker + `{"version":1,"items":[{"scope":"word","quote":"quiet","variants":["a\n\nb","still"]}]} -->`
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Odd", Content: "The quiet house.\n\n" + odd + "\n"})
	m := f.createAlt(f.get(d.ID), "word", "quiet", "calm")
	_ = m
	got := f.mustSave(f.get(d.ID), strings.Replace(f.get(d.ID).Content, "house", "house, house", 1))
	if !strings.Contains(got.Content, odd) || strings.Count(got.Content, alts.Marker) != 2 {
		t.Fatalf("the odd line must stay, with the real comment after it: %q", got.Content)
	}
	if len(f.alts(d.ID).Items) != 1 {
		t.Fatal("one alternative")
	}
}

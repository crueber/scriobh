package docs_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

const altBody = "The quiet house was empty, and the quiet stayed.\n\nShe waited by the old window. Nothing moved in the street.\n\nLast paragraph here.\n"

func (f *fx) get(id string) docs.Doc {
	f.t.Helper()
	out, err := f.call(f.ownA, "doc.get", docs.DocIn{ID: id})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(docs.Item).Item
}

func (f *fx) alts(id string) docs.AltListOut {
	f.t.Helper()
	out, err := f.call(f.ownA, "alternative.list", docs.AltListIn{ID: id})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.(docs.AltListOut)
}

// invariant checks the rule the whole phase rests on, from the database: the
// comment that ends the stored text is exactly the serialisation of the tables,
// the body holds the chosen wording of every active alternative at its anchor,
// and a document with no alternatives has no comment.
func (f *fx) invariant(id string) {
	f.t.Helper()
	d := f.get(id)
	items, err := altstore.Load(f.d.Read(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	parts := alts.Split(d.Content)
	if len(items) == 0 {
		if parts.HasLine && altstore.CheckComment(d.Content, alts.Limits{}) == "" {
			f.t.Fatalf("no alternatives but the text ends with an alternatives comment: %q", d.Content)
		} // a last line that is not valid is an ordinary comment
		return
	}
	if !parts.HasLine || parts.Line != alts.Serialize(items) {
		f.t.Fatalf("the comment is not the serialisation of the tables\ncomment: %q\ntables:  %q", parts.Line, alts.Serialize(items))
	}
	if strings.Contains(d.Content, "\r") {
		f.t.Fatal("a document with alternatives is stored with LF line endings")
	}
	ix := alts.NewIndex(parts.Body)
	last := -1
	for _, it := range items {
		if it.Orphan {
			continue
		}
		a, ok1 := ix.Byte(it.Start)
		b, ok2 := ix.Byte(it.End)
		if !ok1 || !ok2 || b > len(parts.Body) || parts.Body[a:b] != it.ChosenText() {
			f.t.Fatalf("anchor %d..%d of %s does not hold %q", it.Start, it.End, it.ID, it.ChosenText())
		}
		if a < last {
			f.t.Fatal("anchors overlap")
		}
		last = b
		if n := countOriginals(it); n != 1 {
			f.t.Fatalf("%d originals", n)
		}
	}
	if d.WordCount != text.WordCount(d.Content) {
		f.t.Fatalf("word count %d, text says %d", d.WordCount, text.WordCount(d.Content))
	}
}

func countOriginals(it alts.Item) int {
	n := 0
	for _, v := range it.Variants {
		if v.Original {
			n++
		}
	}
	return n
}

func ip(n int) *int { return &n }

func (f *fx) createAlt(d docs.Doc, scope, quote string, variants ...string) docs.AltMut {
	f.t.Helper()
	cur := f.get(d.ID)
	out, err := f.call(f.ownA, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: cur.Revision, Scope: scope, Quote: quote, Occurrence: ip(0), Variants: variants})
	if err != nil {
		f.t.Fatalf("create %q: %v", quote, err)
	}
	f.invariant(d.ID)
	return out.(docs.AltMut)
}

func (f *fx) choose(d docs.Doc, altID, variantID, step string) docs.AltMut {
	f.t.Helper()
	cur := f.get(d.ID)
	out, err := f.call(f.ownA, "alternative.choose", docs.AltChooseIn{ID: d.ID, BaseRevision: cur.Revision, AlternativeID: altID, VariantID: variantID, Step: step})
	if err != nil {
		f.t.Fatalf("choose: %v", err)
	}
	f.invariant(d.ID)
	return out.(docs.AltMut)
}

func (f *fx) save(d docs.Doc, content string) (docs.Doc, error) {
	cur := f.get(d.ID)
	out, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: cur.Revision, Content: &content})
	if err != nil {
		return docs.Doc{}, err
	}
	return out.(docs.Item).Item, nil
}

func (f *fx) mustSave(d docs.Doc, content string) docs.Doc {
	f.t.Helper()
	got, err := f.save(d, content)
	if err != nil {
		f.t.Fatal(err)
	}
	f.invariant(d.ID)
	return got
}

func TestAlternativeCreateChooseAndUndo(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Alt", Content: altBody})
	m := f.createAlt(d, "word", "quiet", "still", "hushed")
	if m.Alternative == nil || len(m.Alternative.Variants) != 3 || m.Alternative.Quote != "quiet" || m.Alternative.Start != 4 || m.Alternative.End != 9 {
		t.Fatalf("created: %+v", m.Alternative)
	}
	if !strings.HasPrefix(m.Comment, alts.Marker) || !strings.HasSuffix(f.get(d.ID).Content, m.Comment+"\n") {
		t.Fatalf("the comment must end the stored text: %q", f.get(d.ID).Content)
	}
	before := f.get(d.ID)
	if before.WordCount != d.WordCount {
		t.Errorf("an alternative adds no words: %d vs %d", before.WordCount, d.WordCount)
	}
	a := m.Alternative
	still := a.Variants[1].ID

	// Cycle to "still": the body changes, the history says why, the count follows.
	c := f.choose(d, a.ID, still, "")
	cur := f.get(d.ID)
	if !strings.HasPrefix(cur.Content, "The still house was empty") || c.Replaced == nil || c.Replaced.Text != "still" || c.Replaced.Start != 4 || c.Replaced.End != 9 || c.Replaced.NewEnd != 9 {
		t.Fatalf("choose: %q %+v", cur.Content[:40], c.Replaced)
	}
	var reason, note string
	if err := f.d.Read().QueryRow(`SELECT reason, note FROM document_revisions WHERE document_id = ? AND reason = 'alternative' ORDER BY recorded_at DESC, id DESC LIMIT 1`, d.ID).Scan(&reason, &note); err != nil {
		t.Fatalf("choosing records a revision with reason alternative: %v", err)
	}
	if !strings.Contains(note, "word") || !strings.Contains(note, "2 of 3") || strings.Contains(note, "still") {
		t.Errorf("note %q: scope and place, no text", note)
	}
	// Stepping wraps; the original is one step away; undo restores the text exactly.
	f.choose(d, a.ID, "", "next")
	if !strings.HasPrefix(f.get(d.ID).Content, "The hushed house") {
		t.Fatal("next")
	}
	f.choose(d, a.ID, "", "next")
	if !strings.HasPrefix(f.get(d.ID).Content, "The quiet house") {
		t.Fatal("next wraps to the original")
	}
	f.choose(d, a.ID, still, "")
	f.choose(d, a.ID, "", "original")
	back := f.get(d.ID)
	if alts.BodyOf(back.Content) != alts.BodyOf(before.Content) {
		t.Fatalf("choosing the original restores the text:\n%q\n%q", alts.BodyOf(back.Content), alts.BodyOf(before.Content))
	}
	// Choosing what is chosen writes nothing.
	again := f.choose(d, a.ID, "", "original")
	if !again.Unchanged || again.Revision != back.Revision {
		t.Errorf("a no-op choice: %+v", again)
	}
	// A longer wording moves everything after it.
	m2 := f.createAlt(d, "sentence", "She waited by the old window.", "She stood at the window, watching.")
	long := m2.Alternative.Variants[1].ID
	third := f.createAlt(d, "word", "Last", "Final")
	f.choose(d, m2.Alternative.ID, long, "")
	if got := f.alts(d.ID); len(got.Items) != 3 || got.Items[2].Quote != "Last" {
		t.Fatalf("anchors after a longer choice: %+v", got.Items)
	}
	f.choose(d, third.Alternative.ID, third.Alternative.Variants[1].ID, "")
	if !strings.Contains(f.get(d.ID).Content, "Final paragraph here.") {
		t.Fatal("the later alternative still lands on its text")
	}
}

func TestAlternativeScopeAndRangeValidation(t *testing.T) {
	f := newFx(t)
	d := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Scope", Content: "Café naïve 😀 bright.\r\nSecond line, with 😀 two.\r\n\r\n> a quote line\r\n\r\n```\r\ncode quiet\r\n```\r\n"})
	cur := f.get(d.ID)
	try := func(in docs.AltCreateIn) error {
		in.ID, in.BaseRevision = d.ID, f.get(d.ID).Revision
		_, err := f.call(f.ownA, "alternative.create", in)
		return err
	}
	if cur.Revision != 1 {
		t.Fatal(cur.Revision)
	}
	for name, in := range map[string]docs.AltCreateIn{
		"inside a word":    {Scope: "word", Quote: "afé"},
		"space in a word":  {Scope: "word", Quote: "naïve 😀"},
		"in code":          {Scope: "word", Quote: "code"},
		"not a paragraph":  {Scope: "paragraph", Quote: "Café naïve"},
		"unknown scope":    {Scope: "chapter", Quote: "Café"},
		"missing":          {Scope: "word", Quote: "absent"},
		"empty":            {Scope: "word"},
		"half a pair":      {Scope: "word", Start: ip(11), End: ip(12)},
		"past the end":     {Scope: "word", Start: ip(0), End: ip(9999)},
		"a quote's marker": {Scope: "sentence", Quote: "> a quote"},
	} {
		if err := try(in); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Offsets are UTF-16 over the LF text: CRLF counts as one character, an emoji
	// as two. "Café naïve 😀 bright.\n" is 22 units, so the second line starts at 22.
	if err := try(docs.AltCreateIn{Scope: "word", Start: ip(12), End: ip(13)}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("half a surrogate pair: %v", err)
	}
	out, err := f.call(f.ownA, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Scope: "word", Start: ip(11), End: ip(13), Variants: []string{"🙂"}})
	if err != nil {
		t.Fatal(err)
	}
	if q := out.(docs.AltMut).Alternative; q.Quote != "😀" || q.Start != 11 || q.End != 13 {
		t.Fatalf("emoji: %+v", q)
	}
	f.invariant(d.ID) // the CRLF text is now stored with LF only
	out, err = f.call(f.ownA, "alternative.create", docs.AltCreateIn{ID: d.ID, BaseRevision: f.get(d.ID).Revision, Scope: "word", Start: ip(22), End: ip(28)})
	if err != nil || out.(docs.AltMut).Alternative.Quote != "Second" {
		t.Fatalf("second line: %v", err)
	}
	f.invariant(d.ID)
	// The same range twice is an overlap, not a second alternative.
	if err := try(docs.AltCreateIn{Scope: "word", Start: ip(22), End: ip(28)}); ops.AsError(err).Code != ops.CodeConflict {
		t.Errorf("overlap: %v", err)
	}
	// A whole paragraph, and a variant that is not one.
	d2 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Para", Content: "One paragraph\nwrapped over lines.\n\nAnother.\n"})
	create := func(in docs.AltCreateIn) error {
		in.ID, in.BaseRevision = d2.ID, f.get(d2.ID).Revision
		_, err := f.call(f.ownA, "alternative.create", in)
		return err
	}
	if err := create(docs.AltCreateIn{Scope: "paragraph", Quote: "One paragraph\nwrapped over lines.", Variants: []string{"- a list"}}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a list is not a paragraph variant: %v", err)
	}
	if err := create(docs.AltCreateIn{Scope: "paragraph", Quote: "One paragraph\nwrapped", Variants: []string{"x"}}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("part of a paragraph: %v", err)
	}
	if err := create(docs.AltCreateIn{Scope: "paragraph", Quote: "One paragraph\nwrapped over lines.", Variants: []string{"A different\nparagraph, wrapped too."}}); err != nil {
		t.Errorf("a paragraph with a wrapped variant: %v", err)
	}
	f.invariant(d2.ID)
}

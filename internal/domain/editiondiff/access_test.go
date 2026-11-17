package editiondiff_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editiondiff"
	"github.com/crueber/scriobh/internal/ops"
)

// A role x scope x op table for the four ops. The edition's text (the book as it was received) is for editors, and for
// a key the write scope; what is not text is for anyone who can read the project.
func TestRoleScopeAndOpTable(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(80, 4)
	d := f.docByTitle(pid, ch[1].title)
	f.setBody(d.ID, "Rewritten for the table.\n")
	doc := f.doc(d.ID)
	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	// the same book as a second project's edition, for the comparison
	other, oeid, _ := f.imported(81, 4)

	person := func(name, role string) ops.Principal {
		p := f.user(name, ops.RoleWriter)
		f.member(pid, p, role)
		f.member(other, p, role)
		return p
	}
	reader, reviewer, editor := person("t-reader", ops.ProjectReader), person("t-reviewer", ops.ProjectReviewer), person("t-editor", ops.ProjectEditor)
	stranger := f.user("t-stranger", ops.RoleWriter)
	asKey := func(p ops.Principal, id, scope string) ops.Principal {
		p.Via, p.KeyID, p.Scope = "apikey", id, scope
		return p
	}

	type call struct {
		op string
		in func() any
	}
	calls := []call{
		{"edition.diff", func() any { return editiondiff.DiffIn{ProjectID: pid, EditionID: eid} }},
		{"edition.diff+chapter", func() any { return editiondiff.DiffIn{ProjectID: pid, EditionID: eid, Chapter: key} }},
		{"edition.compare", func() any {
			return editiondiff.CompareIn{ProjectID: pid, EditionID: eid, WithProject: other, WithEdition: oeid, Source: "record"}
		}},
		{"edition.compare+chapter", func() any {
			return editiondiff.CompareIn{ProjectID: pid, EditionID: eid, WithProject: other, WithEdition: oeid, Source: "record", Chapter: "b1:he1"}
		}},
		{"edition.suggest", func() any {
			return editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision, Hunks: []string{"nosuchhunk"}}
		}},
		{"edition.take", func() any {
			return editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: "b99:hnone", BaseRevision: doc.Revision}
		}},
	}
	// what a caller who passes the checks of the op gets for these inputs is a 404 or a 422 about the input (the chapter
	// or the hunk is not there): that is "allowed" for this table; the table is about who is refused before that
	allowed := map[ops.Code]bool{"": true, ops.CodeNotFound: true, ops.CodeInvalid: true, ops.CodeConflict: true}
	who := []struct {
		name string
		p    ops.Principal
		want map[string]string // op -> "" (reaches the op) or a code
	}{
		{"owner (session)", f.w, nil},
		{"editor (session)", editor, nil},
		{"reviewer (session)", reviewer, map[string]string{"edition.diff+chapter": "forbidden", "edition.compare": "forbidden", "edition.compare+chapter": "forbidden", "edition.suggest": "forbidden", "edition.take": "forbidden"}},
		{"reader (session)", reader, map[string]string{"edition.diff+chapter": "forbidden", "edition.compare": "forbidden", "edition.compare+chapter": "forbidden", "edition.suggest": "forbidden", "edition.take": "forbidden"}},
		{"stranger", stranger, map[string]string{"edition.diff": "not_found", "edition.diff+chapter": "not_found", "edition.compare": "not_found", "edition.compare+chapter": "not_found", "edition.suggest": "not_found", "edition.take": "not_found"}},
		{"editor's write key", asKey(editor, "key_ew", ops.ScopeWrite), nil},
		{"editor's read key", asKey(editor, "key_er", ops.ScopeRead), map[string]string{"edition.diff+chapter": "forbidden", "edition.compare": "forbidden", "edition.compare+chapter": "forbidden", "edition.suggest": "forbidden", "edition.take": "forbidden"}},
		{"reader's write key", asKey(reader, "key_rw", ops.ScopeWrite), map[string]string{"edition.diff+chapter": "forbidden", "edition.compare": "forbidden", "edition.compare+chapter": "forbidden", "edition.suggest": "forbidden", "edition.take": "forbidden"}},
	}
	for _, w := range who {
		for _, c := range calls {
			name := c.op
			op := strings.SplitN(name, "+", 2)[0]
			_, err := f.call(w.p, op, c.in())
			got := ""
			if err != nil {
				got = string(ops.AsError(err).Code)
			}
			want, restricted := w.want[name]
			if !restricted {
				if !allowed[ops.Code(got)] {
					t.Errorf("%s calling %s: %q, want it to reach the op", w.name, name, got)
				}
				continue
			}
			if got != want {
				t.Errorf("%s calling %s: %q, want %q ", w.name, name, got, want)
			}
		}
	}
	// the text itself: a write key of an editor gets it, the read key of the same editor gets none
	rs := f.must(asKey(editor, "key_ew2", ops.ScopeWrite), "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid, Chapter: key}).(editiondiff.Result)
	if st := stepFor(t, rs, ch[1].title); len(st.Hunks) == 0 || st.Hunks[0].Base == "" {
		t.Errorf("the editor's write key got no text: %+v", st)
	}
}

// A guessed pairing is not taken without the person's confirmation.
func TestTakeOfAGuessedPairingNeedsConfirmation(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(82, 4)
	orig := f.doc(f.docByTitle(pid, ch[1].title).ID).Content
	// the chapter's document goes to the trash and the writer makes another with the same title and nearly the same text
	f.must(f.w, "doc.trash", docs.DocIn{ID: f.docByTitle(pid, ch[1].title).ID})
	nd := f.must(f.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: ch[1].title, Kind: "chapter", Content: strings.Replace(orig, "a", "b", 3)}).(docs.Item).Item
	st := stepFor(t, f.diff(pid, eid, ""), ch[1].title)
	if st.Match == nil || !st.Match.Guess || st.Match.Method == editiondiff.MethodRecorded || st.Offer == nil || !st.Offer.Take || !st.Offer.Confirm {
		t.Fatalf("the pairing is not a guess the offer says to confirm: %+v offer %+v", st.Match, st.Offer)
	}
	in := editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: nd.Revision}
	_, err := f.call(f.w, "edition.take", in)
	if err == nil || ops.AsError(err).Code != ops.CodeConflict || ops.AsError(err).Data["reason"] != "guess_unconfirmed" {
		t.Fatalf("an unconfirmed guess: %v", err)
	}
	if f.doc(nd.ID).Content == orig {
		t.Fatal("the chapter was taken without the confirmation")
	}
	in.ConfirmGuess = true
	f.must(f.w, "edition.take", in)
	if f.doc(nd.ID).Content != orig {
		t.Error("a confirmed take did not put the chapter back")
	}
	// a recorded pairing needs none
	pid2, eid2, ch2 := f.imported(83, 3)
	d := f.docByTitle(pid2, ch2[1].title)
	f.setBody(d.ID, "Changed.\n")
	st2 := stepFor(t, f.diff(pid2, eid2, ""), ch2[1].title)
	if st2.Offer == nil || st2.Offer.Confirm {
		t.Errorf("a recorded pairing asks for confirmation: %+v", st2.Offer)
	}
}

// A call that offers differences tells the owner and editors once, whatever number of suggestions it made; a repeat is
// reported as already suggested, and a person's calls are capped.
func TestSuggestTellsOnceSkipsRepeatsAndIsCapped(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(84, 4)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	text := doc.Content
	for _, i := range []int{1, 3} { // two differences in one chapter
		w := strings.Fields(ch[1].paras[i])
		w[0] = fmt.Sprintf("REWORDED%d", i)
		text = strings.Replace(text, ch[1].paras[i], strings.Join(w, " "), 1)
	}
	f.setBody(d.ID, text)
	doc = f.doc(d.ID)
	editor2 := f.user("editor2", ops.RoleWriter)
	f.member(pid, editor2, ops.ProjectEditor)
	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	in := editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision}
	told := func() int {
		return count(t, f, `SELECT count(*) FROM notifications WHERE user_id = '`+editor2.UserID+`' AND kind = 'suggestion'`)
	}
	out := f.must(f.w, "edition.suggest", in).(editiondiff.SuggestOut)
	if len(out.Created) != 2 {
		t.Fatalf("%+v", out)
	}
	if n := told(); n != 1 {
		t.Errorf("two suggestions told editor2 %d times, want once", n)
	}
	again := f.must(f.w, "edition.suggest", in).(editiondiff.SuggestOut)
	if len(again.Created) != 0 || len(again.Skipped) != 2 || !strings.Contains(again.Skipped[0].Reason, "Already suggested") {
		t.Errorf("a repeat: %+v", again)
	}
	if n := told(); n != 1 {
		t.Errorf("a call that made nothing told editor2: %d", n)
	}
	// the other editor offering the same differences is also told they are already there
	f.setRole(pid, editor2, ops.ProjectEditor)
	if out := f.must(editor2, "edition.suggest", in).(editiondiff.SuggestOut); len(out.Created) != 0 {
		t.Errorf("a second person made the same suggestions again: %+v", out)
	}
	// the cap: ten calls in the window, the eleventh is refused calmly, a dry run costs nothing, and it is per person
	for i := 2; i < 10; i++ {
		f.must(f.w, "edition.suggest", in)
	}
	_, err := f.call(f.w, "edition.suggest", in)
	oe := ops.AsError(err)
	if err == nil || oe.Code != ops.CodeRateLimited || oe.RetryAfterSeconds < 1 {
		t.Fatalf("the eleventh call: %v", err)
	}
	if _, err := f.reg.CallDry(t.Context(), editor2, "edition.suggest", in); err != nil {
		t.Errorf("another person is refused (or a dry run failed): %v", err)
	}
}

func TestOneComparisonAtATimeForAPerson(t *testing.T) {
	f := newFx(t)
	p1, e1, _ := f.imported(85, 3)
	p2, e2, _ := f.imported(86, 3)
	in := editiondiff.CompareIn{ProjectID: p1, EditionID: e1, WithProject: p2, WithEdition: e2, Source: "record"}
	release, err := editiondiff.AcquireCompare(f.w.UserID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.call(f.w, "edition.compare", in)
	if oe := ops.AsError(err); err == nil || oe.Code != ops.CodeRateLimited || oe.RetryAfterSeconds < 1 {
		t.Errorf("a second comparison while one runs: %v", err)
	}
	// someone else is not held up by it
	other := f.user("other-cmp", ops.RoleWriter)
	f.member(p1, other, ops.ProjectEditor)
	f.member(p2, other, ops.ProjectEditor)
	f.must(other, "edition.compare", in)
	release()
	f.must(f.w, "edition.compare", in)
}

// A book that talks about the alternatives marker (in a code span, or in an escaped comment) is text, and its chapter
// can be taken back; only a live comment line at the end is refused.
func TestTakeAChapterThatMentionsTheAlternativesMarker(t *testing.T) {
	f := newFx(t)
	ch := book(87, 3, 4, 30)
	ch[1].paras[2] = "The file keeps its wordings in a line that starts `<!-- scriobh:alts v1 -->` and the importer escapes it."
	ch[1].paras = append(ch[1].paras, "\\<!-- scriobh:alts v1 -->")
	ap := f.importBook(f.epub(f.makeProject("Marker", ch)), "First")
	pid, eid := ap.ProjectID, f.edition(ap.ProjectID)
	d := f.docByTitle(pid, ch[1].title)
	orig := f.doc(d.ID).Content
	if !strings.Contains(orig, alts.Marker) {
		t.Fatalf("the fixture does not carry the marker as text (the old check would not have refused it): %q", orig)
	}
	f.setBody(d.ID, "Rewritten.\n")
	st := stepFor(t, f.diff(pid, eid, ""), ch[1].title)
	f.must(f.w, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: f.doc(d.ID).Revision})
	if f.doc(d.ID).Content != orig {
		t.Errorf("the chapter was not put back:\n%q\n%q", f.doc(d.ID).Content, orig)
	}
	for text, want := range map[string]bool{
		"A sentence about <!-- scriobh:alts in prose.\n":               false,
		"A `<!-- scriobh:alts v1 -->` code span.\n":                    false,
		"Text.\n\n\\<!-- scriobh:alts v1 -->\n":                        false,
		"Text.\n\n<!-- scriobh:alts v1 {\"items\":[]} -->\n":           true,
		"Text.\r\n\r\n<!-- scriobh:alts v1 {\"items\":[]} -->\r\n\r\n": true,
		"": false,
	} {
		if got := editiondiff.EndsWithAltsComment(text); got != want {
			t.Errorf("EndsWithAltsComment(%q) = %v", text, got)
		}
	}
}

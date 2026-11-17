package editiondiff_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editiondiff"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// A chapter that was left out of the project at import is withheld from everybody by edition.get (no text) and from
// everybody but editors with a write-scope key by edition.download. edition.diff must not hand it to a reader or to a
// read-scope key just because the project later got a document of the same title.
func TestReviewLeftOutChapterTextDoesNotReachAReader(t *testing.T) {
	f := newFx(t)
	ch := book(21, 5, 5, 40)
	src := f.makeProject("Source", ch)
	data := f.epub(src)
	pv := f.preview(data)
	ap := f.importBook(data, "First edition", imports.Edit{Key: keyOf(pv, ch[2].title), Skip: true})
	pid, eid := ap.ProjectID, f.edition(ap.ProjectID)
	// the writer later writes a chapter of their own with that title
	f.must(f.w, "doc.create", docs.CreateIn{ProjectID: pid, Title: ch[2].title, Kind: "chapter", Content: "My own different words, nothing like the book.\n"})

	secret := ch[2].paras[1]
	reader := f.user("reader", ops.RoleWriter)
	f.member(pid, reader, ops.ProjectReader)
	readKey := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "key_ro", Scope: ops.ScopeRead}

	// controls: the edition record carries no text, and the download is refused to these callers
	got := f.must(reader, "edition.get", editions.EditionIn{ProjectID: pid, EditionID: eid})
	if strings.Contains(fmt.Sprintf("%+v", got), secret[:30]) {
		t.Fatal("edition.get carries text")
	}
	if code := f.code(reader, "edition.download", editions.EditionIn{ProjectID: pid, EditionID: eid}); code != ops.CodeForbidden {
		t.Fatalf("reader download: %s", code)
	}
	if code := f.code(readKey, "edition.download", editions.EditionIn{ProjectID: pid, EditionID: eid}); code != ops.CodeForbidden {
		t.Fatalf("read-scope key download: %s", code)
	}

	r := f.must(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid}).(editiondiff.Result)
	st := stepFor(t, r, ch[2].title)
	if len(st.Base) == 0 {
		t.Fatalf("the left-out chapter was not paired with the new document (the fixture is wrong): %+v", st)
	}
	for _, who := range []struct {
		name string
		p    ops.Principal
	}{{"reader", reader}, {"read-scope key", readKey}} {
		// the text of an edition is the editors' (with the write scope for a key): refused, not filtered
		if code := f.code(who.p, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid, Chapter: st.Key}); code != ops.CodeForbidden {
			t.Errorf("%s stepping into a chapter: %s", who.name, code)
		}
		// and what they may see says nothing of the chapter that was left out: not its title, not that it exists, and it
		// takes no part in the pairing
		rs := f.must(who.p, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid}).(editiondiff.Result)
		if rs.Base.Chapters != 4 {
			t.Errorf("%s: the edition shows %d chapters, want the 4 that are in the project", who.name, rs.Base.Chapters)
		}
		for _, s := range rs.Steps {
			for _, b := range s.Base {
				if b.Title == ch[2].title {
					t.Errorf("%s was shown the title of a chapter left out at import: %+v", who.name, s)
				}
			}
			if s.Status == editiondiff.StatusAdded && s.Head[0].Title == ch[2].title && s.Match != nil {
				t.Errorf("%s: the new document is paired with the left-out chapter: %+v", who.name, s)
			}
		}
		if st2 := stepFor(t, rs, ch[2].title); st2.Status != editiondiff.StatusAdded {
			t.Errorf("%s: the new chapter is %s, want added (the left-out chapter is not in what they may see)", who.name, st2.Status)
		}
		// the edition's own record hides it too
		got := f.must(who.p, "edition.get", editions.EditionIn{ProjectID: pid, EditionID: eid}).(editions.GetOut)
		for _, c := range got.Chapters {
			if c.Title == ch[2].title {
				t.Errorf("%s: edition.get names the left-out chapter", who.name)
			}
		}
		if got.Item.Chapters != 4 || len(got.Chapters) != 4 {
			t.Errorf("%s: edition.get counts %d/%d chapters, want 4", who.name, got.Item.Chapters, len(got.Chapters))
		}
		list := f.must(who.p, "edition.list", editions.ProjectIn{ProjectID: pid}).(editions.ListOut)
		if list.Items[0].Chapters != 4 {
			t.Errorf("%s: edition.list counts %d chapters, want 4", who.name, list.Items[0].Chapters)
		}
	}
	// the editor sees all five, and the left-out chapter's text
	full := f.must(f.w, "edition.get", editions.EditionIn{ProjectID: pid, EditionID: eid}).(editions.GetOut)
	if len(full.Chapters) != 5 {
		t.Errorf("an editor sees %d chapters, want 5", len(full.Chapters))
	}
	rsE := f.must(f.w, "edition.diff", editiondiff.DiffIn{ProjectID: pid, EditionID: eid, Chapter: st.Key}).(editiondiff.Result)
	seen := false
	for _, s := range rsE.Steps {
		for _, h := range s.Hunks {
			seen = seen || strings.Contains(h.Base, secret)
		}
	}
	if !seen {
		t.Error("control: the editor does not see the left-out chapter's text")
	}
}

func (f *fx) credited() int {
	f.t.Helper()
	var n int
	if err := f.d.Read().QueryRow(`SELECT COALESCE(sum(words_added),0) FROM writing_sessions WHERE user_id = ?`, f.w.UserID).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// The author's own "no goal credit" check in TestTakeAChapterIsAnUndoableRevision asserts nothing (it reads the
// total and throws it away). This one has the control: a save by hand earns words, a take of the same size does not.
func TestReviewTakeEarnsNoGoalCredit(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(4, 5)
	d := f.docByTitle(pid, ch[2].title)
	before := f.credited()
	f.setBody(d.ID, strings.Repeat("A freshly written sentence of nine words, right here.\n\n", 40))
	afterHand := f.credited()
	if afterHand <= before {
		t.Fatalf("control: a save by hand credited nothing (%d -> %d)", before, afterHand)
	}
	doc := f.doc(d.ID)
	st := stepFor(t, f.diff(pid, eid, ""), ch[2].title)
	f.must(f.w, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision})
	if got := f.credited(); got != afterHand {
		t.Errorf("a take credited %d words", got-afterHand)
	}
}

// An edition id of project A used with the path of project B (the caller is a member of both) answers 404 on all four ops.
func TestReviewEditionOfAnotherProjectIs404(t *testing.T) {
	f := newFx(t)
	pa, ea, cha := f.imported(40, 3)
	pb, _, _ := f.imported(41, 3)
	k := stepFor(t, f.diff(pa, ea, ""), cha[0].title).Key
	for op, in := range map[string]any{
		"edition.diff":    editiondiff.DiffIn{ProjectID: pb, EditionID: ea},
		"edition.compare": editiondiff.CompareIn{ProjectID: pb, EditionID: ea, WithEdition: ea, WithProject: pa},
		"edition.suggest": editiondiff.SuggestIn{ProjectID: pb, EditionID: ea, Chapter: k, BaseRevision: 1},
		"edition.take":    editiondiff.TakeIn{ProjectID: pb, EditionID: ea, Chapter: k, BaseRevision: 1},
	} {
		if code := f.code(f.w, op, in); code != ops.CodeNotFound {
			t.Errorf("%s: %s", op, code)
		}
	}
}

// edition.suggest has nothing that stops the same difference being offered again: a retry of a call that worked (a
// dropped response, an agent that loops) leaves one more open suggestion per hunk, and every owner and editor is told
// of each.
func TestReviewSuggestDuplicatesOnRetry(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(50, 4)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	w := strings.Fields(ch[1].paras[1])
	w[0] = "REWORDED"
	f.setBody(d.ID, strings.Replace(doc.Content, ch[1].paras[1], strings.Join(w, " "), 1))
	doc = f.doc(d.ID)
	other := f.user("editor2", ops.RoleWriter)
	f.member(pid, other, ops.ProjectEditor)
	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	in := editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision}
	for i := 0; i < 3; i++ {
		f.must(f.w, "edition.suggest", in)
	}
	if n := count(t, f, `SELECT count(*) FROM suggestions WHERE project_id = '`+pid+`' AND status = 'open'`); n != 1 {
		t.Errorf("%d open suggestions for one difference after three identical calls", n)
	}
}

// A rename that changes only the case or the punctuation of a title is a change of the text of the title, and the
// step says "unchanged".
func TestReviewTitleCaseRenameIsNotUnchanged(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(60, 3)
	d := f.docByTitle(pid, ch[1].title)
	f.must(f.w, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: f.doc(d.ID).Revision, Title: ptr(strings.ToUpper(ch[1].title) + ".")})
	st := stepFor(t, f.diff(pid, eid, ""), strings.ToUpper(ch[1].title)+".")
	if st.Status == editiondiff.StatusUnchanged {
		t.Errorf("a changed title is reported as %q (renamed=%v)", st.Status, st.Renamed)
	}
}

// Offsets are UTF-16 units in the suggestion and bytes in the diff: a chapter with astral characters ahead of the
// difference must still be placed exactly, accepted, and leave the chapter unchanged against the edition.
func TestReviewSuggestPlacesTextAfterAstralCharacters(t *testing.T) {
	f := newFx(t)
	ch := book(70, 3, 5, 30)
	ch[1].paras[0] = "😀😀 café 日本語 " + ch[1].paras[0]
	ch[1].paras[1] = "𝔘𝔫𝔦𝔠𝔬𝔡𝔢 " + ch[1].paras[1]
	src := f.makeProject("Source", ch)
	ap := f.importBook(f.epub(src), "First edition")
	pid, eid := ap.ProjectID, f.edition(ap.ProjectID)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	f.setBody(d.ID, strings.Replace(doc.Content, ch[1].paras[3], "Rewritten whole paragraph here.", 1))
	doc = f.doc(d.ID)
	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	out := f.must(f.w, "edition.suggest", editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision}).(editiondiff.SuggestOut)
	if len(out.Created) != 1 {
		t.Fatalf("%+v", out)
	}
	f.must(f.w, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: pid, SuggestionID: out.Created[0].SuggestionID, BaseRevision: doc.Revision})
	if s := stepFor(t, f.diff(pid, eid, ""), ch[1].title); s.Status != editiondiff.StatusUnchanged {
		t.Errorf("after accepting: %+v", s)
	}
}

// Two takes with the same base revision: one wins, the other is a conflict, and the history holds one restore.
func TestReviewTwoTakesRace(t *testing.T) {
	f := newFx(t)
	pid, eid, ch := f.imported(80, 4)
	d := f.docByTitle(pid, ch[1].title)
	f.setBody(d.ID, "Everything was rewritten.\n")
	doc := f.doc(d.ID)
	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	in := editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision}
	res := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() { _, err := f.call(f.w, "edition.take", in); res <- err }()
	}
	ok := 0
	for i := 0; i < 4; i++ {
		if err := <-res; err == nil {
			ok++
		} else if ops.AsError(err).Code != ops.CodeConflict {
			t.Errorf("%v", err)
		}
	}
	if ok != 1 {
		t.Errorf("%d takes succeeded", ok)
	}
}

// Taking a chapter the import merged puts back the merged text exactly as the import made it, and the chapter is then
// unchanged against the edition.
func TestReviewTakeAMergedChapter(t *testing.T) {
	f := newFx(t)
	ch := book(90, 4, 4, 30)
	src := f.makeProject("Source", ch)
	data := f.epub(src)
	pv := f.preview(data)
	ap := f.importBook(data, "First", imports.Edit{Key: keyOf(pv, ch[2].title), MergeInto: keyOf(pv, ch[1].title)})
	pid, eid := ap.ProjectID, f.edition(ap.ProjectID)
	d := f.docByTitle(pid, ch[1].title)
	orig := f.doc(d.ID).Content
	f.setBody(d.ID, "Gone.\n")
	doc := f.doc(d.ID)
	st := stepFor(t, f.diff(pid, eid, ""), ch[1].title)
	if st.Status != editiondiff.StatusMerged && st.Status != editiondiff.StatusEdited {
		t.Fatalf("%+v", st)
	}
	f.must(f.w, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision})
	if got := f.doc(d.ID).Content; got != orig {
		t.Errorf("merged text not put back:\n%q\n%q", got, orig)
	}
}

// edition.download is the editors' and needs the write scope because it is the book as it was received. The same text
// comes out of edition.compare (and edition.diff) through a key that has only the read scope.
func TestReviewReadScopeKeyCannotReadEditionText(t *testing.T) {
	f := newFx(t)
	ch := book(95, 4, 5, 40)
	a := f.makeProject("The Book", ch)
	e1 := f.epub(a)
	d := f.docByTitle(a, ch[1].title)
	f.setBody(d.ID, "A wholly new second chapter.\n")
	e2 := f.epub(a)
	p1 := f.importBook(e1, "First").ProjectID
	p2 := f.importBook(e2, "Second").ProjectID
	ed1, ed2 := f.edition(p1), f.edition(p2)
	readKey := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "key_ro", Scope: ops.ScopeRead}
	if code := f.code(readKey, "edition.download", editions.EditionIn{ProjectID: p1, EditionID: ed1}); code != ops.CodeForbidden {
		t.Fatalf("control: download with a read key: %s", code)
	}
	in := editiondiff.CompareIn{ProjectID: p1, EditionID: ed1, WithProject: p2, WithEdition: ed2, Source: "record"}
	// a comparison is for editors with the write scope, with or without chapter
	for _, chapter := range []string{"", "b2:he2"} {
		in.Chapter = chapter
		if code := f.code(readKey, "edition.compare", in); code != ops.CodeForbidden {
			t.Errorf("a read-scope key, chapter %q: %s", chapter, code)
		}
	}
	writeKey := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "key_rw", Scope: ops.ScopeWrite}
	in.Chapter = ""
	r := f.must(writeKey, "edition.compare", in).(editiondiff.Result)
	in.Chapter = stepFor(t, r, ch[1].title).Key
	rs := f.must(writeKey, "edition.compare", in).(editiondiff.Result)
	seen := false
	for _, s := range rs.Steps {
		for _, h := range s.Hunks {
			seen = seen || strings.Contains(h.Base, ch[1].paras[0])
		}
	}
	if !seen {
		t.Error("control: a write-scope key sees no text through edition.compare")
	}
}

// Offering the same difference again makes another suggestion and tells every owner and editor again, and a person's
// notifications are capped (500, oldest dropped): a loop of identical calls, which an agent holding the tool can make,
// pushes a reviewer's real notification out of an editor's list and fills the project's 2,000-suggestion cap.
func TestReviewSuggestLoopEvictsRealNotifications(t *testing.T) {
	if testing.Short() {
		t.Skip("520 calls")
	}
	f := newFx(t)
	pid, eid, ch := f.imported(51, 4)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	w := strings.Fields(ch[1].paras[1])
	w[0] = "REWORDED"
	f.setBody(d.ID, strings.Replace(doc.Content, ch[1].paras[1], strings.Join(w, " "), 1))
	doc = f.doc(d.ID)
	editor2 := f.user("editor2", ops.RoleWriter)
	f.member(pid, editor2, ops.ProjectEditor)
	reviewer := f.user("reviewer", ops.RoleWriter)
	f.member(pid, reviewer, ops.ProjectReviewer)
	// a reviewer's own suggestion tells editor2
	text := ch[1].paras[2]
	rs := f.must(reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: pid, DocumentID: d.ID, BaseRevision: doc.Revision,
		Changes: []review.ChangeIn{{Quote: text[:20], Text: "Changed opening of this paragraph"}}}).(review.SuggestionItem)
	forMe := func() int {
		return count(t, f, `SELECT count(*) FROM notifications WHERE user_id = '`+editor2.UserID+`' AND suggestion_id = '`+rs.Item.ID+`'`)
	}
	if forMe() != 1 {
		t.Fatalf("control: editor2 was not told of the reviewer's suggestion")
	}
	key := stepFor(t, f.diff(pid, eid, ""), ch[1].title).Key
	in := editiondiff.SuggestIn{ProjectID: pid, EditionID: eid, Chapter: key, BaseRevision: doc.Revision}
	limited := 0
	for i := 0; i < 520; i++ {
		_, err := f.call(f.w, "edition.suggest", in)
		if err != nil {
			if ops.AsError(err).Code != ops.CodeRateLimited {
				t.Fatalf("call %d: %v", i, err)
			}
			limited++
		}
	}
	if limited < 500 {
		t.Errorf("only %d of 520 calls were refused: there is no cap on calls", limited)
	}
	if forMe() != 0 {
		return
	}
	t.Errorf("after 520 identical calls editor2 no longer has the reviewer's notification; open suggestions: %d",
		count(t, f, `SELECT count(*) FROM suggestions WHERE project_id = '`+pid+`' AND status = 'open'`))
}

func TestReviewReaderSeesPrivateNotesInDocGetToo(t *testing.T) {
	f := newFx(t)
	pid, _, ch := f.imported(101, 3)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	f.setBody(d.ID, doc.Content+"\nVisible. %%a private aside%% <scriobh-ghost>ghosted</scriobh-ghost>\n")
	reader := f.user("reader", ops.RoleWriter)
	f.member(pid, reader, ops.ProjectReader)
	got := f.must(reader, "doc.get", docs.DocIn{ID: d.ID}).(docs.Item).Item.Content
	if !strings.Contains(got, "a private aside") {
		t.Errorf("doc.get strips private notes for a reader: %q", got[len(got)-80:])
	}
}

// Keys pinned elsewhere (another project, a Compendium; an instance administrator is an implicit owner of every project by design) and a pinned key
// naming a second project in a comparison all get 404 on every edition op: nothing of the edition, not even its
// existence.
func TestReviewOutsidersGet404(t *testing.T) {
	f := newFx(t)
	pa, ea, cha := f.imported(110, 3)
	pb, eb, _ := f.imported(111, 3)
	k := stepFor(t, f.diff(pa, ea, ""), cha[0].title).Key
	pinnedB := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "k1", Scope: ops.ScopeAdmin, ProjectID: pb}
	pinnedC := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "k2", Scope: ops.ScopeAdmin, CompendiumID: "cmp_x"}
	pinnedA := ops.Principal{UserID: f.w.UserID, Role: ops.RoleWriter, Via: "apikey", KeyID: "k3", Scope: ops.ScopeAdmin, ProjectID: pa}
	calls := map[string]any{
		"edition.diff":    editiondiff.DiffIn{ProjectID: pa, EditionID: ea},
		"edition.compare": editiondiff.CompareIn{ProjectID: pa, EditionID: ea, WithEdition: ea},
		"edition.suggest": editiondiff.SuggestIn{ProjectID: pa, EditionID: ea, Chapter: k, BaseRevision: 1},
		"edition.take":    editiondiff.TakeIn{ProjectID: pa, EditionID: ea, Chapter: k, BaseRevision: 1},
	}
	for op, in := range calls {
		for who, p := range map[string]ops.Principal{"key pinned to B": pinnedB, "compendium key": pinnedC} {
			_, err := f.call(p, op, in)
			if err == nil || ops.AsError(err).Code != ops.CodeNotFound {
				t.Errorf("%s as %s: %v", op, who, err)
			}
		}
	}
	// a key pinned to A may not name B as the second project
	if code := f.code(pinnedA, "edition.compare", editiondiff.CompareIn{ProjectID: pa, EditionID: ea, WithProject: pb, WithEdition: eb, Source: "record"}); code != ops.CodeNotFound {
		t.Errorf("pinned key naming a second project: %s", code)
	}
}

// Control for the duplicate test: suggestion.create itself does not refuse an identical open suggestion either, so the
// finding about edition.suggest is the amplification (up to 20 suggestions and their notifications per call, from a
// tool offered to agents), not a new kind of hole.
func TestReviewControlSuggestionCreateDuplicatesToo(t *testing.T) {
	f := newFx(t)
	pid, _, ch := f.imported(52, 3)
	d := f.docByTitle(pid, ch[1].title)
	doc := f.doc(d.ID)
	q := ch[1].paras[2][:20]
	for i := 0; i < 3; i++ {
		f.must(f.w, "suggestion.create", review.SuggestionCreateIn{ProjectID: pid, DocumentID: d.ID, BaseRevision: doc.Revision,
			Changes: []review.ChangeIn{{Quote: q, Text: "Changed opening of this paragraph"}}})
	}
	if n := count(t, f, `SELECT count(*) FROM suggestions WHERE project_id = '`+pid+`' AND status = 'open'`); n != 3 {
		t.Errorf("control: %d open (expected the existing behaviour, 3)", n)
	}
}

type recSink struct {
	mu     sync.Mutex
	events []ops.Event
	audit  []ops.AuditEntry
}

func (r *recSink) FlushTx(_ *sql.Tx, _ string, ev []ops.Event, au []ops.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events, r.audit = append(r.events, ev...), append(r.audit, au...)
	return nil
}
func (r *recSink) Flush(ctx context.Context, a string, ev []ops.Event, au []ops.AuditEntry) error {
	return r.FlushTx(nil, a, ev, au)
}

// A take tells the mirror (and everything that follows documents) exactly once, as a content change, and is audited.
func TestReviewTakeEmitsOneDocUpdatedAndAnAudit(t *testing.T) {
	d := testutil.DB(t)
	dir := t.TempDir()
	sink := &recSink{}
	f := &fx{t: t, d: d, dir: dir, reg: registry.Build(d, sink, dir)}
	f.w = f.user("writer", ops.RoleWriter)
	pid, eid, ch := f.imported(120, 4)
	doc0 := f.docByTitle(pid, ch[1].title)
	f.setBody(doc0.ID, "Rewritten.\n")
	doc := f.doc(doc0.ID)
	st := stepFor(t, f.diff(pid, eid, ""), ch[1].title)
	sink.mu.Lock()
	sink.events, sink.audit = nil, nil
	sink.mu.Unlock()
	f.must(f.w, "edition.take", editiondiff.TakeIn{ProjectID: pid, EditionID: eid, Chapter: st.Key, BaseRevision: doc.Revision})
	n, a := 0, 0
	for _, e := range sink.events {
		if e.Type == "doc.updated" && e.DocumentID == doc0.ID && e.Payload["content"] == true {
			n++
		}
	}
	for _, x := range sink.audit {
		if x.Action == "edition.take" {
			a++
		}
	}
	if n != 1 || a != 1 {
		t.Errorf("doc.updated x%d, audit x%d; events: %+v", n, a, sink.events)
	}
}

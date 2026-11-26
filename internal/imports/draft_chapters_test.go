package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// The review of a Word document's cut, on a staged draft: a change of rule keeps the edits of the chapters that remain and says which
// do not, a chapter can be started by hand, a title in capitals can be renamed, a contents page can be put back, and Undo restores.

func stageSample(t *testing.T, e *denv, prefix string) (imports.StageOut, imports.DraftOut) {
	t.Helper()
	s := corpusByName(t, prefix)
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx"}, corpusDoc(s.paras))
	return st, e.get(e.w, st.StageID, true)
}

func titles(pv *imports.PreviewOut) string {
	var out []string
	for _, n := range pv.Nodes {
		out = append(out, n.Title)
	}
	return strings.Join(out, "|")
}

func TestDraftKeepsEditsOfChaptersThatRemainAcrossAChangeOfRule(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	// 14: two chapters at Heading 1, one a bold label and one in capitals
	st, d := stageSample(t, e, "14-")
	if got := titles(d.Preview); got != "Chapter 1|Chapter 2|Chapter 3|CHAPTER 4" {
		t.Fatalf("auto: %s", got)
	}
	key := func(pv *imports.PreviewOut, title string) string {
		for _, n := range pv.Nodes {
			if n.Title == title {
				return n.Key
			}
		}
		t.Fatalf("no %s in %s", title, titles(pv))
		return ""
	}
	one, two, yes := "One", "Two", true
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: key(d.Preview, "Chapter 1"), Title: &one}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: key(u.Preview, "Chapter 2"), Title: &two}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: key(u.Preview, "CHAPTER 4"), Skip: &yes}})
	// cut at the headings only: the two that are headings stay, with their edits; the label's chapter is gone
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "heading1"}})
	if got := titles(u.Preview); got != "One|Chapter 3" {
		t.Fatalf("heading1 keeps the rename of the chapter that remains: %s", got)
	}
	if u.Note == nil || u.Note.Kept != 1 || strings.Join(u.Note.Dropped, "|") != "Two|CHAPTER 4" {
		t.Fatalf("what no longer applies is listed by title: %+v", u.Note)
	}
	// the chapter in capitals, which the writer left out, is not in this cut either: its leaving out is listed with the rename
	// Undo brings every edit back
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true})
	if got := titles(u.Preview); got != "One|Two|Chapter 3" {
		t.Fatalf("undo: %s", got)
	}
	// choosing auto when auto is what is in use changes nothing, and the writer's edits stay
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "auto"}})
	if got := titles(u.Preview); got != "One|Two|Chapter 3" {
		t.Fatalf("auto again: %s %+v", got, u.Note)
	}
}

func TestDraftStartsAChapterHereAndKeepsTheOthersEdits(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "06b")
	if !d.Preview.Breaks.NoneFound || d.Preview.Documents != 1 || len(d.Preview.Breaks.Suggestions) != 2 {
		t.Fatalf("a page-break-only book is one document and two suggestions: %+v", d.Preview.Breaks)
	}
	first := d.Preview.Breaks.Suggestions[0].Ordinal
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &first})
	if u.Preview.Documents != 2 || u.Note == nil || u.Note.Code != "break" || u.Note.Title != "Chapter 2" || u.Note.Other != "Chapter 1" {
		t.Fatalf("one press, one new chapter: %d %+v", u.Preview.Documents, u.Note)
	}
	// rename the second, then start the other: the rename stays with its chapter
	key := u.Preview.Nodes[1].Key
	name := "The Mill"
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: key, Title: &name}})
	second := u.Preview.Breaks.Suggestions[0].Ordinal
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &second})
	if got := titles(u.Preview); got != "Chapter 1|The Mill|Chapter 3" {
		t.Fatalf("a chapter added after it leaves the rename where it was: %s", got)
	}
	if u.Note.Kept != 1 {
		t.Fatalf("kept: %+v", u.Note)
	}
	// the whole-book rule makes the same three, and "none" keeps the writer's own breaks
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "page_breaks"}})
	if got := titles(u.Preview); got != "Chapter 1|The Mill|Chapter 3" {
		t.Fatalf("page breaks: %s", got)
	}
	// a break that is not a paragraph is refused
	zero := 0
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Break: &zero}); code(err) != ops.CodeInvalid {
		t.Fatalf("break 0: %v", err)
	}
}

func TestDraftRenamesCapitalsToTitleCaseOnlyWhenAsked(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "04")
	if got := titles(d.Preview); got != "THE ROAD|THE MILL|SNOW" {
		t.Fatalf("capitals are kept: %s", got)
	}
	// one by hand, as a rename
	one := "The Road"
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &one}})
	// then the rest
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, TitleCaseAll: true})
	if got := titles(u.Preview); got != "The Road|The Mill|Snow" || u.Note.Code != "title_case" || u.Note.Count != 2 {
		t.Fatalf("title case for the rest: %s %+v", got, u.Note)
	}
	// nothing is left to change
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, TitleCaseAll: true}); reason(err) != "no_caps" {
		t.Fatalf("again: %v", err)
	}
}

func TestDraftPutsBackAContentsPage(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "11-")
	if len(d.Preview.LeftOut) != 1 || d.Preview.Documents != 6 {
		t.Fatalf("contents is left out: %+v", d.Preview.LeftOut)
	}
	key := d.Preview.LeftOut[0].Key
	no := false
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: key, Skip: &no}})
	if u.Preview.Documents != 7 || len(u.Preview.LeftOut) != 0 || u.Note == nil || u.Note.Code != "put_back" || u.Note.Title != "Contents" {
		t.Fatalf("put back: %d %+v %+v", u.Preview.Documents, u.Preview.LeftOut, u.Note)
	}
	// and left out again by the writer
	yes := true
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: key, Skip: &yes}})
	if u.Preview.Documents != 6 {
		t.Fatalf("left out again: %d", u.Preview.Documents)
	}
	// the stage applies with the contents page out
	ao := e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut)
	if ao.Documents != 6 {
		t.Fatalf("applied %d", ao.Documents)
	}
}

// What the writer chose to cut by is what is applied: the staged apply reads the same options the preview did.
func TestDraftAppliesTheCutThatWasPreviewed(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "06b")
	first := d.Preview.Breaks.Suggestions[0].Ordinal
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Break: &first})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "page_breaks"}})
	if u.Preview.Documents != 3 {
		t.Fatalf("preview: %d", u.Preview.Documents)
	}
	ao := e.must(e.w, "import.apply", imports.ApplyIn{StageID: st.StageID, Rev: u.Item.Revision, Token: u.Item.Token}).(imports.ApplyOut)
	if ao.Documents != 3 {
		t.Fatalf("applied %d documents, previewed 3", ao.Documents)
	}
}

// An edit set aside by a change of rule comes back with its chapter: sample 14 through Heading 1, page breaks and auto again.
func TestDraftEditsComeBackWithTheirChapter(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "14-")
	var k2, k4 string
	for _, n := range d.Preview.Nodes {
		switch n.Title {
		case "Chapter 2":
			k2 = n.Key
		case "CHAPTER 4":
			k4 = n.Key
		}
	}
	two, yes := "Two", true
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: k2, Title: &two}})
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Edit: &imports.DraftEdit{Key: k4, Skip: &yes}})
	if got := titles(u.Preview); got != "Chapter 1|Two|Chapter 3" {
		t.Fatalf("edited: %s", got)
	}
	for _, rule := range []string{"heading1", "page_breaks"} {
		u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": rule}})
		if len(u.Item.Edits.Stash) != 2 {
			t.Fatalf("%s: the two edits are set aside, not lost: %+v", rule, u.Item.Edits)
		}
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "auto"}})
	if got := titles(u.Preview); got != "Chapter 1|Two|Chapter 3" || len(u.Note.Restored) != 2 || len(u.Item.Edits.Stash) != 0 {
		t.Fatalf("auto again: %s %+v", got, u.Note)
	}
	// and the left-out one is still left out, not back in
	if len(u.Item.Edits.Gone) != 1 {
		t.Errorf("gone: %+v", u.Item.Edits.Gone)
	}
}

// A leaving out is for the text it was made on: when the chapter's extent widens, the edit is set aside and listed, not kept to swallow text.
func TestDraftSkipIsTiedToTheChaptersText(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	paras := corpusCat(one(cPh1("Alpha")), corpusBody(2), one(cPh1("Beta")), corpusBody(2), one(cPbc("Chapter 3")), corpusBody(4), one(cPh1("Delta")), corpusBody(2))
	st := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "book.docx"}, corpusDoc(paras))
	d := e.get(e.w, st.StageID, true)
	var key string
	for _, n := range d.Preview.Nodes {
		if n.Title == "Beta" {
			key = n.Key
		}
	}
	yes := true
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Edit: &imports.DraftEdit{Key: key, Skip: &yes}})
	if got := titles(u.Preview); got != "Alpha|Chapter 3|Delta" {
		t.Fatalf("skipped: %s", got)
	}
	words := u.Preview.Words
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "heading1"}})
	if len(u.Note.Dropped) != 1 || u.Note.Dropped[0] != "Beta" || len(u.Item.Edits.Stash) != 1 {
		t.Fatalf("the edit is set aside and listed: %+v", u.Note)
	}
	if got := titles(u.Preview); got != "Alpha|Beta|Delta" || u.Preview.Words < words {
		t.Errorf("the widened chapter keeps all its text: %s, %d words (was %d)", got, u.Preview.Words, words)
	}
	// back to auto: Beta is the chapter it was, and the edit is back
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "auto"}})
	if got := titles(u.Preview); got != "Alpha|Chapter 3|Delta" || len(u.Note.Restored) != 1 {
		t.Errorf("auto again: %s %+v", got, u.Note)
	}
}

// The tick for a long chapter was for the cut the writer saw.
func TestDraftTickForALongChapterIsAskedAgainAfterAnotherCut(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	st, d := stageSample(t, e, "14-")
	u := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: d.Item.Revision, Options: map[string]string{"ack_big": "true"}})
	if !u.Item.Options.AckBig {
		t.Fatal("not ticked")
	}
	u = e.mustUpdate(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Options: map[string]string{"docx_split": "heading1"}})
	if u.Item.Options.AckBig {
		t.Error("the tick stayed after the cut changed")
	}
	// Undo is a change of its own
	two := "x"
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true, Edit: &imports.DraftEdit{Key: "k", Title: &two}}); code(err) != ops.CodeInvalid {
		t.Errorf("undo with an edit: %v", err)
	}
	n := 5
	if _, err := e.update(e.w, imports.DraftUpdateIn{ID: st.StageID, BaseRevision: u.Item.Revision, Undo: true, Break: &n}); code(err) != ops.CodeInvalid {
		t.Errorf("undo with a break: %v", err)
	}
}

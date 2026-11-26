package imports_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// The fixes of an earlier review come back when the same file is staged again: the writer's own open drafts only, through the
// same stash rules as a change of cut. Applied imports keep counts only (the plan), so their fixes are not offered.

func TestReuseOffersTheFixesOfAnEarlierDraftOfTheSameFile(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	raw := book(t, "a")
	first := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road"}, raw)
	d := e.get(e.w, first.StageID, true)
	title := "Opening"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: first.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &title}})

	// the same bytes with another option: not the draft already open, so a second review of the same file
	second := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road", DocxChanges: "accept"}, raw)
	if second.StageID == first.StageID {
		t.Fatal("a draft with other options should be a second draft")
	}
	offers := e.get(e.w, second.StageID, false).Item.Reuse
	if len(offers) != 1 || offers[0].ID != first.StageID || offers[0].Changes != 1 {
		t.Fatalf("offers: %+v, want the first draft with one change", offers)
	}

	out := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: second.StageID, BaseRevision: 1, Reuse: first.StageID})
	if out.Note == nil || out.Note.Code != "reuse" || out.Note.Count != 1 {
		t.Fatalf("note: %+v", out.Note)
	}
	found := false
	for _, n := range out.Preview.Nodes {
		if n.Title == "Opening" {
			found = true
		}
	}
	if !found {
		t.Errorf("the fix did not reach the second review: %+v", out.Preview.Nodes)
	}
	// the offer is spent: this review now has changes of its own
	for _, o := range e.get(e.w, second.StageID, false).Item.Reuse {
		if o.ID == first.StageID {
			t.Errorf("a review with its own changes still offers the first one again")
		}
	}
}

func TestReuseSetsAsideAFixWhoseChapterIsNotInTheCut(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	raw := book(t, "a")
	first := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road"}, raw)
	d := e.get(e.w, first.StageID, true)
	title := "Opening"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: first.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &title}})

	// one document, so the chapter the fix was made on is not in the cut
	second := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road", DocxSplit: "none"}, raw)
	out := e.mustUpdate(e.w, imports.DraftUpdateIn{ID: second.StageID, BaseRevision: 1, Reuse: first.StageID})
	if len(out.Note.Dropped) != 1 || out.Note.Dropped[0] != "Opening" {
		t.Fatalf("dropped: %v, want the fix listed", out.Note.Dropped)
	}
	if len(out.Item.Edits.Stash) != 1 {
		t.Errorf("the fix was not set aside: %+v", out.Item.Edits.Stash)
	}
}

func TestReuseIsOnlyTheOwnersOwnDraft(t *testing.T) {
	e := newDraftEnv(t, imports.StageLimits{})
	raw := book(t, "a")
	other := e.user("other", ops.RoleWriter)
	first := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road"}, raw)
	d := e.get(e.w, first.StageID, true)
	title := "Opening"
	e.mustUpdate(e.w, imports.DraftUpdateIn{ID: first.StageID, BaseRevision: 1, Edit: &imports.DraftEdit{Key: d.Preview.Nodes[0].Key, Title: &title}})

	// another person stages the same file: they are offered nothing, and cannot take the writer's fixes
	theirs := e.mustStage(other, imports.StageIn{Kind: "docx", Name: "Theirs"}, raw)
	if offers := e.get(other, theirs.StageID, false).Item.Reuse; len(offers) != 0 {
		t.Errorf("another person is offered %+v", offers)
	}
	if _, err := e.update(other, imports.DraftUpdateIn{ID: theirs.StageID, BaseRevision: 1, Reuse: first.StageID}); code(err) != ops.CodeNotFound {
		t.Errorf("reuse of someone else's draft: %v, want not found", err)
	}
	// and the writer is not offered the other person's draft either
	mine := e.mustStage(e.w, imports.StageIn{Kind: "docx", Name: "The Salt Road", DocxChanges: "accept"}, raw)
	for _, o := range e.get(e.w, mine.StageID, false).Item.Reuse {
		if o.ID == theirs.StageID {
			t.Errorf("the writer is offered another person's draft %+v", o)
		}
	}
}

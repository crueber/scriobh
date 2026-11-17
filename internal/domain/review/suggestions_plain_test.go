package review_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
)

// A reading view shows rendered text; a suggestion made on a selection there is found in the visible text only,
// placed in the source with its formatting, and listed back in the same plain form.
func TestSuggestionsInPlainText(t *testing.T) {
	x := newH(t)
	text := "The *quiet* house stood [[Hill|alone]] on the hill. %%SECRETWORD quiet%%\n\nIts windows were dark.\n"
	doc := x.doc("One", text)
	d := x.get(doc)
	mk := func(p ops.Principal, plain bool, ch ...review.ChangeIn) (review.SuggestionOut, error) {
		out, err := x.reg.Call(x.ctx, p, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: d.Revision, Changes: ch, Plain: plain})
		if err != nil {
			return review.SuggestionOut{}, err
		}
		return out.(review.SuggestionItem).Item, nil
	}
	// the rendered words "quiet house" span the emphasis marks in the source
	s, err := mk(x.reviewer, true, review.ChangeIn{Quote: "quiet house", Text: "silent house"})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Changes[0].Original; got != "*quiet* house" {
		t.Errorf("the source range is %q, want the whole formatted run", got)
	}
	// a selection that starts inside the formatting and cannot be widened is refused, not left half-formatted
	if _, err := mk(x.reviewer, true, review.ChangeIn{Quote: "uiet house", Text: "x"}); err == nil || !strings.Contains(err.Error(), "formatting") {
		t.Errorf("a selection that cuts the formatting: %v", err)
	}
	// accepted, no mark is left behind
	x.accept(x.editor, s.ID)
	if got := x.get(doc).Content; !strings.HasPrefix(got, "The silent house stood [[Hill|alone]]") {
		t.Errorf("accepted: %q", got)
	}
	d = x.get(doc)
	// a phrase only in a private note cannot be found, and says what any missing phrase says
	_, e1 := mk(x.reviewer, true, review.ChangeIn{Quote: "SECRETWORD", Text: "x"})
	_, e2 := mk(x.reviewer, true, review.ChangeIn{Quote: "nothing at all", Text: "x"})
	if e1 == nil || e2 == nil || ops.AsError(e1).Message != ops.AsError(e2).Message {
		t.Errorf("hidden text is not like absent text: %v / %v", e1, e2)
	}
	// offsets with plain are refused
	zero, five := 0, 5
	if _, err := mk(x.reviewer, true, review.ChangeIn{Start: &zero, End: &five, Text: "x"}); err == nil {
		t.Error("offsets accepted in plain mode")
	}
	// an insertion beside rendered words
	if _, err := mk(x.reviewer, true, review.ChangeIn{Quote: "dark", Where: "after", Text: " and cold"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mk(x.reviewer, true, review.ChangeIn{Quote: "stood alone", Text: "waited"}); err != nil {
		t.Fatal(err)
	}
	// listed in plain form for one document
	list := x.call(x.reviewer, "suggestion.list", review.SuggestionListIn{ProjectID: x.project, DocumentID: doc, Plain: true}).(review.SuggestionListOut)
	if len(list.Items) != 2 {
		t.Fatalf("%d suggestions", len(list.Items))
	}
	var rep, ins review.ChangeOut
	for _, it := range list.Items {
		if it.Changes[0].Kind == "replace" {
			rep = it.Changes[0]
		} else {
			ins = it.Changes[0]
		}
	}
	if rep.OriginalPlain != "stood alone" || !strings.HasSuffix(rep.BeforePlain, "silent house") || !strings.HasPrefix(rep.AfterPlain, "on the hill") {
		t.Errorf("replace hints: %+v", rep)
	}
	if ins.OriginalPlain != "" || !strings.HasSuffix(ins.BeforePlain, "dark") || ins.AfterPlain != "." {
		t.Errorf("insert hints: %+v", ins)
	}
	if strings.Contains(rep.AfterPlain+rep.BeforePlain, "SECRET") || strings.Contains(ins.AfterPlain, "SECRET") {
		t.Error("a private note is in a hint")
	}
	// without plain there are no hints, and a stale suggestion has none
	plainless := x.call(x.reviewer, "suggestion.list", review.SuggestionListIn{ProjectID: x.project, DocumentID: doc}).(review.SuggestionListOut)
	for _, it := range plainless.Items {
		if it.Changes[0].OriginalPlain != "" || it.Changes[0].BeforePlain != "" {
			t.Error("hints without plain=true")
		}
	}
	x.set(doc, strings.Replace(text, "dark", "black", 1))
	stale := x.call(x.reviewer, "suggestion.list", review.SuggestionListIn{ProjectID: x.project, DocumentID: doc, Plain: true}).(review.SuggestionListOut)
	for _, it := range stale.Items {
		if !it.Stale || it.Changes[0].OriginalPlain != "" {
			t.Errorf("a stale suggestion has hints: %+v", it)
		}
	}
	// a reader sees none of this
	x.expect(x.reader, "suggestion.list", review.SuggestionListIn{ProjectID: x.project, DocumentID: doc, Plain: true}, ops.CodeForbidden)
}

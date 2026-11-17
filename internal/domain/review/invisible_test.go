package review_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
)

// A suggestion made on a selection takes away the words that were selected and nothing the reviewer could not see: not an
// owner's HTML comment between them, not the address of a link that the selection covers only in part.
func TestAPlainSuggestionNeverSwallowsWhatThePageDoesNotShow(t *testing.T) {
	x := newH(t)
	text := "The quiet <!-- OWNERASIDE: cut this scene before spring --> house stood alone.\n\nSee [the harbour](https://example.org/secret-address) now and again.\n"
	doc := x.doc("One", text)
	d := x.get(doc)
	try := func(quote, repl string) (review.SuggestionOut, error) {
		out, err := x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: d.Revision, Plain: true, Changes: []review.ChangeIn{{Quote: quote, Text: repl}}})
		if err != nil {
			return review.SuggestionOut{}, err
		}
		return out.(review.SuggestionItem).Item, nil
	}
	// across the comment: refused, in words, and nothing is stored
	if _, err := try("quiet house stood", "silent house stood"); err == nil || !strings.Contains(err.Error(), "does not show") {
		t.Errorf("a selection across an HTML comment: %v", err)
	}
	// across the end of a link and past it: refused
	if _, err := try("harbour now", "harbour then"); err == nil || !strings.Contains(err.Error(), "link") {
		t.Errorf("a selection that covers part of a link and its address: %v", err)
	}
	if n := x.count(`SELECT count(*) FROM suggestions`); n != 0 {
		t.Errorf("%d suggestions were stored", n)
	}
	// the words on either side are fine, and so are words inside a link and the whole link with its neighbours
	for _, c := range [][2]string{{"stood alone", "stood quiet"}, {"The quiet", "A quiet"}, {"the harbour", "the old harbour"}, {"See the harbour now", "Go to the harbour now"}} {
		s, err := try(c[0], c[1])
		if err != nil {
			t.Errorf("%q: %v", c[0], err)
			continue
		}
		if strings.Contains(s.Changes[0].Original, "OWNERASIDE") {
			t.Errorf("%q swallowed the comment", c[0])
		}
	}
	// by offsets too: a range that holds the comment is refused, as an AI suggestion's would be
	a, b := strings.Index(text, "quiet"), strings.Index(text, "house")+len("house")
	_ = a
	if _, err := x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: d.Revision, Changes: []review.ChangeIn{{Start: &a, End: &b, Text: "x"}}}); err == nil {
		t.Error("a range by offsets that holds an HTML comment was accepted")
	}
	_ = ops.CodeInvalid
}

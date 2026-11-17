package review_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/review"
)

// The plain hint of a comment anchored on words that start inside formatting is what the reading view shows.
func TestCommentPlainHintIsTheRenderedText(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The *quiet* house stood alone.\n")
	c := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "quiet house", Plain: true, Body: "hm"}).(review.ThreadItem).Item
	if c.Anchor == nil || c.Anchor.State != "active" {
		t.Fatalf("anchor: %+v", c.Anchor)
	}
	l := x.call(x.reviewer, "comment.list", review.ListIn{ProjectID: x.project, DocumentID: doc, Plain: true}).(review.ListOut)
	a := l.Items[0].Anchor
	if a.QuotePlain != "quiet house" || a.BeforePlain != "The" || !strings.HasPrefix(a.AfterPlain, "stood") {
		t.Errorf("hints: %+v", a)
	}
}

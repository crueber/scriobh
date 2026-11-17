package review_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/review"
)

// Someone the administrator has switched off is not offered in the mention picker, and cannot be mentioned.
func TestADisabledPersonIsNotInTheMentionPicker(t *testing.T) {
	x := newH(t)
	list := func() map[string]bool {
		out, err := x.reg.Call(x.ctx, x.owner, "comment.people", review.PeopleIn{ProjectID: x.project})
		if err != nil {
			t.Fatal(err)
		}
		m := map[string]bool{}
		for _, p := range out.(review.PeopleOut).Items {
			m[p.UserID] = true
		}
		return m
	}
	if !list()[x.reviewer.UserID] {
		t.Fatal("the reviewer is not in the list to begin with")
	}
	if _, err := x.d.Writer().Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, db.Now(), x.reviewer.UserID); err != nil {
		t.Fatal(err)
	}
	if list()[x.reviewer.UserID] {
		t.Error("a disabled person is offered in the mention picker")
	}
}

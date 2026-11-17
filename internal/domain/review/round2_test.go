package review_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
)

// An insertion at the start of a replaced range and the replacement are two answers to one place: accept_all
// skips the later one, whichever order they were written in, and a preview says exactly what the write does.
func TestAcceptAllTreatsAnInsertionAtTheStartOfAReplacementAsAClash(t *testing.T) {
	for _, replaceFirst := range []bool{true, false} {
		t.Run(fmt.Sprint("replace first: ", replaceFirst), func(t *testing.T) {
			x := newH(t)
			doc := x.doc("One", sugText)
			rev := x.get(doc).Revision
			var ids []string
			mkReplace := func() { ids = append(ids, x.suggest(x.reviewer, doc, rev, rep("quiet", "silent")).ID) }
			mkInsert := func() {
				ids = append(ids, x.suggest(x.reviewer2, doc, rev, review.ChangeIn{Quote: "quiet", Where: "before", Text: "very "}).ID)
			}
			if replaceFirst {
				mkReplace()
				mkInsert()
			} else {
				mkInsert()
				mkReplace()
			}
			in := review.SuggestionAcceptAllIn{ProjectID: x.project, DocumentID: doc, SuggestionIDs: ids}
			pre := in
			pre.Preview = true
			plan := x.call(x.editor, "suggestion.accept_all", pre).(review.SuggestionAcceptAllOut)
			if len(plan.Applied) != 1 || len(plan.Skipped) != 1 || plan.Skipped[0].Reason != "overlap" || plan.Applied[0] != ids[0] {
				t.Fatalf("plan: %+v (ids %v)", plan, ids)
			}
			out := x.call(x.editor, "suggestion.accept_all", in).(review.SuggestionAcceptAllOut)
			if strings.Join(out.Applied, ",") != strings.Join(plan.Applied, ",") || len(out.Skipped) != 1 || out.Skipped[0].SuggestionID != plan.Skipped[0].SuggestionID {
				t.Errorf("the write did not do what the preview said: %+v against %+v", out, plan)
			}
			want := strings.Replace(sugText, "quiet", "silent", 1)
			if !replaceFirst {
				want = strings.Replace(sugText, "quiet", "very quiet", 1)
			}
			if got := x.get(doc).Content; got != want {
				t.Errorf("text %q, want %q", got, want)
			}
		})
	}
}

// Deleted comments are tombstones: they do not count towards the limits of a project, a thread or a document, and the
// sweep removes the old ones.
func TestDeletedCommentsDoNotCountTowardsTheCaps(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		now := db.Now()
		for i := 0; i < review.MaxCommentsPerProject; i++ {
			if _, err := tx.Exec(`INSERT INTO comments(id, project_id, document_id, kind, body, status, anchor_state, created_at, updated_at, deleted_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, db.NewID("cmt"), x.project, doc, "comment", "", "open", "none", now, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	th := x.comment(x.reviewer, doc, "quiet", "still possible")
	if th.Anchor == nil {
		t.Fatal("no thread")
	}
	// the live ones are what count
	if _, err := x.d.Writer().Exec(`UPDATE comments SET deleted_at = NULL, body = 'x' WHERE id IN (SELECT id FROM comments WHERE body = '' LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	// replies: tombstones in a thread do not fill it
	err = x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		now := db.Now()
		for i := 0; i < review.MaxRepliesPerThread; i++ {
			if _, err := tx.Exec(`INSERT INTO comments(id, project_id, document_id, parent_id, kind, body, status, anchor_state, created_at, updated_at, deleted_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)`, db.NewID("cmt"), x.project, doc, th.ID, "comment", "", "open", "none", now, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	x.call(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, ParentID: th.ID, Body: "a reply among tombstones"})
	// the old tombstones go with the sweep, the live ones stay
	old := db.Format(db.NowTime().AddDate(0, 0, -40))
	if _, err := x.d.Writer().Exec(`UPDATE comments SET deleted_at = ? WHERE deleted_at IS NOT NULL`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := review.Sweep(x.ctx, x.d); err != nil {
		t.Fatal(err)
	}
	if n := x.count(`SELECT count(*) FROM comments WHERE deleted_at IS NOT NULL`); n != 0 {
		t.Errorf("%d old tombstones left", n)
	}
	if x.thread(x.owner, th.ID).Replies[0].Body != "a reply among tombstones" {
		t.Error("the sweep took a live comment")
	}
}

// The anchored cap counts open threads: resolving makes room, and a thread that is resolved stays anchored.
func TestTheAnchoredCapCountsOpenThreads(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	bulk := func(status string, n int) {
		err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
			now := db.Now()
			for i := 0; i < n; i++ {
				if _, err := tx.Exec(`INSERT INTO comments(id, project_id, document_id, kind, body, status, anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_revision, created_at, updated_at)
					VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, db.NewID("cmt"), x.project, doc, "comment", "b", status, "active", 4, 9, "quiet", "quiet", 1, now, now); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	bulk("resolved", 1000)
	x.comment(x.reviewer, doc, "quiet", "room for one: only open threads count")
	bulk("open", 998) // 999 open now
	x.call(x.reviewer2, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "stood", Body: "the thousandth"})
	e := x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "alone", Body: "one too many"}, ops.CodeConflict)
	if !strings.Contains(e.Message, "open anchored") {
		t.Errorf("the message: %q", e.Message)
	}
}

// One person writing on one project is bounded per hour, in words that say so.
func TestAMemberCannotWriteWithoutEnd(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	now := time.Now()
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	for i := 0; i < review.MaxCommentsPerMemberPerHour; i++ {
		x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: fmt.Sprint("comment ", i)})
		now = now.Add(time.Second)
	}
	e := x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "one more"}, ops.CodeRateLimited)
	if e.RetryAfterSeconds < 1 || !strings.Contains(e.Message, "last hour") {
		t.Errorf("the answer: %+v", e)
	}
	// someone else on the same project, and the same person elsewhere, are not held up
	x.call(x.reviewer2, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "mine"})
	now = now.Add(time.Hour)
	x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "an hour later"})
}

// What guests write in one thread within an hour is one notification with a count, and guest traffic never
// pushes a person's own notifications out.
func TestGuestCommentsAreDigestedAndCannotEvictMentions(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "hello @Olive, please look"})
	guest := func(parent, body string) review.Created {
		var cr review.Created
		err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
			c, evs, err := review.InsertComment(context.Background(), tx, review.NewComment{ProjectID: x.project, DocumentID: doc, ParentID: parent, Body: body,
				Actor: review.Actor{ShareID: "shl_00000000000000000000000001", GuestName: "Sam"}})
			cr = c
			_ = evs
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return cr
	}
	first := guest("", "a first thought")
	for i := 0; i < 4; i++ {
		guest(first.ThreadID, fmt.Sprint("and another ", i))
	}
	list := x.call(x.owner, "notification.list", review.NotificationListIn{}).(review.NotificationListOut)
	var digest, mention int
	for _, n := range list.Items {
		switch n.Kind {
		case "comment":
			if n.Count == 5 && n.ThreadID == first.ThreadID {
				digest++
			}
		case "mention":
			mention++
		}
	}
	if digest != 1 {
		t.Errorf("five guest comments in a thread: %+v", list.Items)
	}
	// a flood of guest threads
	for i := 0; i < 250; i++ {
		guest("", fmt.Sprint("thread ", i))
	}
	if n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND share_id IS NOT NULL`, x.owner.UserID); n > 100 {
		t.Errorf("%d guest notifications are kept, more than the 100 allowed", n)
	}
	// and the one from a person is still there (the reviewer's comment notified the owner, who is the project's owner)
	if n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND share_id IS NULL`, x.owner.UserID); n != 1 {
		t.Errorf("the owner's own notification count is %d, want 1", n)
	}
	// after the notification is read, the next guest comment is a new one
	x.call(x.owner, "notification.read_all", review.ReadAllIn{})
	guest(first.ThreadID, "again after reading")
	n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND comment_id = ? AND read_at IS NULL`, x.owner.UserID, first.ThreadID)
	if n != 1 {
		t.Errorf("a comment after the read: %d unread rows", n)
	}
}

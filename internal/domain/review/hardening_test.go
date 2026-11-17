package review_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
)

const longPassage = "the old lighthouse keeper counted every ship that passed" // well over 24 bytes

// A comment on one of two like passages, whose passage is rewritten, is orphaned. It is not
// found again on the other copy, however long and however unique that copy has become.
func TestACommentNeverHopsOntoTheOtherCopyOfALongPassage(t *testing.T) {
	x := newH(t)
	text := "First paragraph: " + longPassage + " in the morning.\n\nA middle paragraph with other things in it.\n\nLast paragraph: " + longPassage + " at night.\n"
	doc := x.doc("One", text)
	c := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: longPassage, Before: "Last paragraph: ", Body: "this copy"}).(review.ThreadItem).Item
	cur := x.get(doc).Content
	second := strings.LastIndex(cur, longPassage)
	if c.Anchor.Start != second {
		t.Fatalf("anchored to the wrong copy: %d, want %d", c.Anchor.Start, second)
	}
	// the commented copy is rewritten
	x.set(doc, strings.Replace(cur, "Last paragraph: "+longPassage+" at night.", "Last paragraph: and then the lamp went dark for good.", 1))
	got := x.anchor(c.ID)
	if got.State != "orphaned" {
		t.Fatalf("the comment moved onto the other copy: %+v", got)
	}
	// an orphan does not hop either: touching the surviving copy (so the edit window is there) leaves it lost
	cur = x.get(doc).Content
	x.set(doc, strings.Replace(cur, longPassage+" in the morning", longPassage+", truly, in the morning", 1))
	if got := x.anchor(c.ID); got.State != "orphaned" {
		t.Fatalf("an orphan moved onto the surviving copy: %+v", got)
	}
	// but when the original words and their surroundings come back (an undo), it re-attaches where it was
	x.set(doc, text)
	if got := x.anchor(c.ID); got.State != "active" || got.Start != strings.LastIndex(x.get(doc).Content, longPassage) {
		t.Fatalf("an undo did not bring the comment back: %+v", got)
	}
	// and the same where the edit is in the commented words themselves, near the copy that stays
	x2 := x.doc("Two", text)
	c2 := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: x2, Quote: longPassage, Before: "Last paragraph: ", Body: "this copy"}).(review.ThreadItem).Item
	cur = x.get(x2).Content
	x.set(x2, strings.Replace(cur, longPassage+" at night", "the lamp went dark for good at night", 1))
	if got := x.anchor(c2.ID); got.State != "orphaned" {
		t.Fatalf("a rewrite of the commented copy: %+v", got)
	}
	// but a passage that was the only copy, and is moved, is the same passage: it follows
	x3 := x.doc("Three", "Alpha paragraph.\n\nLast paragraph: "+longPassage+" at night.\n")
	c3 := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: x3, Quote: longPassage, Body: "unique"}).(review.ThreadItem).Item
	x.set(x3, "Last paragraph: "+longPassage+" at night.\n\nAlpha paragraph, and a new line in it.\n")
	if got := x.anchor(c3.ID); got.State != "active" || got.Quote != longPassage {
		t.Errorf("a unique passage that moved: %+v", got)
	}
}

// The same on a split: a comment on the copy that moved to the new document is orphaned in the
// first one, never taken onto the like passage that stayed.
func TestSplitDoesNotMoveACommentOntoALookalike(t *testing.T) {
	x := newH(t)
	text := "Head paragraph: " + longPassage + " in the morning.\n\nMore head text.\n\nTail paragraph: " + longPassage + " at night.\n\nMore tail text.\n"
	doc := x.doc("One", text)
	tail := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: longPassage, Before: "Tail paragraph: ", Body: "on the tail copy"}).(review.ThreadItem).Item
	head := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: longPassage, Before: "Head paragraph: ", Body: "on the head copy"}).(review.ThreadItem).Item
	d := x.get(doc)
	cut := strings.Index(d.Content, "Tail paragraph")
	x.call(x.owner, "doc.split", docs.SplitIn{ID: doc, BaseRevision: d.Revision, Offset: cut, Title: "Tail"})
	if a := x.anchor(tail.ID); a.State != "orphaned" {
		t.Errorf("the comment on the moved copy: %+v", a)
	}
	a := x.anchor(head.ID)
	cur := x.get(doc).Content
	if a.State != "active" || cur[a.Start:a.End] != longPassage || a.Start != strings.Index(cur, longPassage) {
		t.Errorf("the comment on the copy that stayed: %+v", a)
	}
	// it can be put where the text went
	var tailDoc string
	if err := x.d.Read().QueryRow(`SELECT id FROM documents WHERE project_id = ? AND title = 'Tail'`, x.project).Scan(&tailDoc); err != nil {
		t.Fatal(err)
	}
	moved := x.call(x.editor, "comment.reanchor", review.ReanchorIn{ProjectID: x.project, CommentID: tail.ID, DocumentID: tailDoc, Quote: longPassage}).(review.ThreadItem).Item
	if moved.DocumentID != tailDoc || moved.Anchor.State != "active" {
		t.Errorf("reanchor to the new document: %+v", moved)
	}
}

// A reader of a project no longer hears of it once it is in the trash or gone: the notification
// list and the badge are as blind as every other op.
func TestNotificationsFollowAccessToTheProject(t *testing.T) {
	x := newH(t)
	d := x.doc("One", sugText)
	x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: d, Quote: "quiet", Body: "hi @Edith"})
	count := func(p ops.Principal) int {
		return x.call(p, "notification.count", review.NotificationCountIn{}).(review.NotificationCountOut).Unread
	}
	list := func(p ops.Principal) int {
		return len(x.call(p, "notification.list", review.NotificationListIn{}).(review.NotificationListOut).Items)
	}
	if count(x.editor) != 1 || list(x.editor) != 1 || count(x.owner) != 1 {
		t.Fatalf("before: %d %d %d", count(x.editor), list(x.editor), count(x.owner))
	}
	x.call(x.owner, "project.trash", projects.StateIn{ID: x.project})
	if count(x.editor) != 0 || list(x.editor) != 0 {
		t.Errorf("an editor still hears of a trashed project: %d %d", count(x.editor), list(x.editor))
	}
	if count(x.owner) != 1 || list(x.owner) != 1 {
		t.Errorf("the owner of a trashed project: %d %d", count(x.owner), list(x.owner))
	}
	x.call(x.owner, "project.restore", projects.StateIn{ID: x.project})
	if count(x.editor) != 1 {
		t.Errorf("after the restore: %d", count(x.editor))
	}
	// and a person taken off the project no longer sees its comment text through notifications
	x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: x.project, UserID: x.editor.UserID})
	if list(x.editor) != 0 {
		t.Error("a removed member still sees notifications of the project")
	}
}

// span is the shape of a comment's anchor, not a scope of an alternative: asking for it is a 422, not a
// constraint error from the table.
func TestAlternativesRefuseTheCommentScope(t *testing.T) {
	x := newH(t)
	d := x.doc("One", sugText)
	rev := x.get(d).Revision
	x.expect(x.owner, "alternative.create", docs.AltCreateIn{ID: d, BaseRevision: rev, Scope: "span", Quote: "quiet house", Variants: []string{"still house"}}, ops.CodeInvalid)
	if x.get(d).Revision != rev {
		t.Error("the refused alternative changed the document")
	}
}

// Moving a thread to a document that already holds the most anchored comments is refused, as writing one is.
func TestReanchorRespectsThePerDocumentCap(t *testing.T) {
	x := newH(t)
	full := x.doc("Full", sugText)
	other := x.doc("Other", sugText)
	c := x.comment(x.reviewer, other, "quiet", "to move")
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		now := db.Now()
		for i := 0; i < comanchor.MaxAnchoredPerDocument; i++ {
			if _, err := tx.Exec(`INSERT INTO comments(id, project_id, document_id, kind, author_id, body, status, anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_revision, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, db.NewID("cmt"), x.project, full, "comment", nil, "bulk "+fmt.Sprint(i), "open", "active", 4, 9, "quiet", "quiet", 1, now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	x.expect(x.editor, "comment.reanchor", review.ReanchorIn{ProjectID: x.project, CommentID: c.ID, DocumentID: full, Quote: "quiet"}, ops.CodeConflict)
	x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: full, Quote: "windows", Body: "one too many"}, ops.CodeConflict)
	// a save of a document at the cap still works, and keeps every anchor honest (the limit is on the work, not the save)
	x.set(full, "A new line.\n\n"+sugText)
	if x.count(`SELECT count(*) FROM comments WHERE document_id = ? AND anchor_state = 'active' AND anchor_start < 10`, full) > 0 {
		// the bulk rows say start 4..9 of a text where that is no longer "quiet": they must have moved or been lost
		t.Error("anchors stayed where the text no longer is")
	}
}

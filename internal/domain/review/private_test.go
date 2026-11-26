package review_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// "Only me": a private comment is the author's alone. Not the owner, not another editor, not an instance administrator.
const canary = "PRIVATE-COMMENT-CANARY"

func (x *h) private(p ops.Principal, doc, quote, body string) review.CommentOut {
	x.t.Helper()
	return x.call(p, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: quote, Body: body, Visibility: "private"}).(review.ThreadItem).Item
}

func (x *h) list(p ops.Principal, in review.ListIn) review.ListOut {
	x.t.Helper()
	in.ProjectID = x.project
	return x.call(p, "comment.list", in).(review.ListOut)
}

func (x *h) unread(p ops.Principal) int {
	x.t.Helper()
	return x.call(p, "notification.count", review.NotificationCountIn{}).(review.NotificationCountOut).Unread
}

func TestPrivateCreateTellsNoOneAndHasNoMentions(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	events0 := x.count(`SELECT count(*) FROM events WHERE type LIKE 'comment.%'`)
	th := x.private(x.editor, doc, "stood alone", "note to self @Olive "+canary)
	if th.Visibility != "private" || th.Anchor == nil || th.Anchor.Quote != "stood alone" || len(th.Mentions) != 0 {
		t.Fatalf("thread: %+v", th)
	}
	if x.count(`SELECT count(*) FROM comment_mentions`) != 0 {
		t.Error("@name in a private body was resolved to a mention")
	}
	for _, p := range []ops.Principal{x.owner, x.editor, x.reviewer, x.admin} {
		if n := x.unread(p); n != 0 {
			t.Errorf("%s was notified of a private comment (%d)", p.Name, n)
		}
	}
	if got := x.count(`SELECT count(*) FROM events WHERE type LIKE 'comment.%'`); got != events0 {
		t.Errorf("a private create emitted %d comment events", got-events0)
	}
	if x.count(`SELECT count(*) FROM notifications`) != 0 {
		t.Error("a notification row exists")
	}
	var shared any
	if err := x.d.Read().QueryRow(`SELECT shared_at FROM comments WHERE id = ?`, th.ID).Scan(&shared); err != nil || shared != nil {
		t.Errorf("shared_at of a private comment: %v %v", shared, err)
	}
	// mentions are refused, with the rule in words
	e := x.expect(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "x", Visibility: "private", Mentions: []string{x.owner.UserID}}, ops.CodeInvalid)
	if !strings.Contains(e.Message, "only you can see") {
		t.Errorf("message: %q", e.Message)
	}
	x.expect(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "x", Visibility: "everyone"}, ops.CodeInvalid)
	// every by-person action of the author goes on without a word to anyone
	x.call(x.editor, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: th.ID, Body: "again " + canary})
	x.call(x.editor, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: th.ID})
	x.call(x.editor, "comment.reopen", review.GetIn{ProjectID: x.project, CommentID: th.ID})
	whole := x.call(x.editor, "comment.reanchor", review.ReanchorIn{ProjectID: x.project, CommentID: th.ID, Detach: true}).(review.ThreadItem).Item
	if whole.Anchor != nil {
		t.Errorf("detached: %+v", whole.Anchor)
	}
	x.call(x.editor, "comment.delete", review.GetIn{ProjectID: x.project, CommentID: th.ID})
	if got := x.count(`SELECT count(*) FROM events WHERE type LIKE 'comment.%'`); got != events0 {
		t.Errorf("a private thread's life emitted %d comment events", got-events0)
	}
	if x.count(`SELECT count(*) FROM notifications`) != 0 {
		t.Error("a notification row exists after the private thread's life")
	}
}

func TestPrivateList(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	shared := x.comment(x.reviewer, doc, "quiet house", "a shared one")
	mine := x.private(x.editor, doc, "stood alone", canary+" body")
	// the editor sees both; counts include their own
	l := x.list(x.editor, review.ListIn{DocumentID: doc})
	if l.Total != 2 || l.Open != 2 || len(l.Items) != 2 {
		t.Fatalf("author's list: %+v", l)
	}
	if l := x.list(x.editor, review.ListIn{DocumentID: doc, Visibility: "private"}); l.Total != 1 || l.Items[0].ID != mine.ID {
		t.Errorf("private filter: %+v", l)
	}
	if l := x.list(x.editor, review.ListIn{DocumentID: doc, Visibility: "shared"}); l.Total != 1 || l.Items[0].ID != shared.ID {
		t.Errorf("shared filter: %+v", l)
	}
	x.expect(x.editor, "comment.list", review.ListIn{ProjectID: x.project, Visibility: "bogus"}, ops.CodeInvalid)
	// nobody else sees it, counts it, or can find it by a word or by its author: the owner and an administrator included
	for _, p := range []ops.Principal{x.owner, x.admin, x.reviewer, x.reviewer2} {
		for _, in := range []review.ListIn{{}, {DocumentID: doc}, {Q: "CANARY"}, {Q: canary}, {Author: x.editor.UserID}, {Visibility: "private"}, {Status: "open"}, {Kind: "comment", State: "active"}} {
			l := x.list(p, in)
			if l.Total != 0 && (in.Q != "" || in.Author != "" || in.Visibility == "private") {
				t.Errorf("%s: %+v found something it must not: %+v", p.Name, in, l)
			}
			for _, it := range l.Items {
				if it.ID == mine.ID || strings.Contains(it.Body, "CANARY") {
					t.Errorf("%s sees the private thread through %+v", p.Name, in)
				}
			}
		}
		l := x.list(p, review.ListIn{DocumentID: doc})
		if l.Total != 1 || l.Open != 1 || l.Resolved != 0 || len(l.Items) != 1 || l.Items[0].ID != shared.ID {
			t.Errorf("%s: total/open/resolved count what they cannot see: %+v", p.Name, l)
		}
	}
	// replies of shared threads are not hidden: another editor's private reply cannot exist, so a thread's replies are all visible
	if got := x.list(x.owner, review.ListIn{DocumentID: doc}).Items[0]; got.Visibility != "shared" {
		t.Errorf("visibility of a shared one: %q", got.Visibility)
	}
}

// Another person's private thread is not found by any op by id, with the answer an id that does not exist gets.
func TestPrivateByIdIsNotFound(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	mine := x.private(x.editor, doc, "stood alone", canary)
	gone := "cmt_does_not_exist"
	calls := map[string]func(p ops.Principal, id string) error{
		"get": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.get", review.GetIn{ProjectID: x.project, CommentID: id})
			return err
		},
		"delete": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.delete", review.GetIn{ProjectID: x.project, CommentID: id})
			return err
		},
		"resolve": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: id})
			return err
		},
		"reopen": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.reopen", review.GetIn{ProjectID: x.project, CommentID: id})
			return err
		},
		"update": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: id, Body: "x"})
			return err
		},
		"reanchor": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.reanchor", review.ReanchorIn{ProjectID: x.project, CommentID: id, Detach: true})
			return err
		},
		"reply": func(p ops.Principal, id string) error {
			_, err := x.reg.Call(x.ctx, p, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: id, Body: "x"})
			return err
		},
	}
	for name, call := range calls {
		for _, p := range []ops.Principal{x.owner, x.admin, x.reviewer} {
			want := ops.AsError(call(p, gone))
			got := ops.AsError(call(p, mine.ID))
			if want == nil || got == nil || want.Code != ops.CodeNotFound {
				t.Fatalf("%s as %s: missing id gives %v", name, p.Name, want)
			}
			if got.Code != want.Code || got.Message != want.Message || got.Field != want.Field {
				t.Errorf("%s as %s: private %+v differs from missing %+v", name, p.Name, got, want)
			}
		}
	}
	// the owner's right to delete any comment does not reach a thread they cannot see, and the thread is untouched
	if th := x.thread(x.editor, mine.ID); th.Deleted || th.Status != "open" || th.Body != canary {
		t.Errorf("the thread was changed: %+v", th)
	}
}

func TestPrivateRepliesTakeTheirThreadsVisibility(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	mine := x.private(x.editor, doc, "stood alone", "mine")
	pub := x.comment(x.reviewer, doc, "quiet house", "shared")
	// a reply with none takes the thread's
	r := x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: mine.ID, Body: "more"}).(review.ThreadItem).Item
	if len(r.Replies) != 1 || r.Visibility != "private" {
		t.Fatalf("private thread with its reply: %+v", r)
	}
	if x.count(`SELECT count(*) FROM comments WHERE parent_id = ? AND visibility = 'private'`, mine.ID) != 1 {
		t.Error("the reply is not stored private")
	}
	// one that disagrees is a 422 naming the rule
	e := x.expect(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: mine.ID, Body: "x", Visibility: "shared"}, ops.CodeInvalid)
	if !strings.Contains(e.Message, "visibility of its thread") {
		t.Errorf("message: %q", e.Message)
	}
	x.expect(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: pub.ID, Body: "x", Visibility: "private"}, ops.CodeInvalid)
	// a reply to a shared thread is shared and tells people as before
	x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: pub.ID, Body: "ok"})
	if x.unread(x.reviewer) != 1 {
		t.Errorf("the author of a shared thread was not told of a reply: %d", x.unread(x.reviewer))
	}
	before := x.count(`SELECT count(*) FROM notifications`)
	x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: mine.ID, Body: "again"})
	if x.count(`SELECT count(*) FROM notifications`) != before {
		t.Error("a reply to a private thread notified someone")
	}
	// nobody else can reply under it (404, before any other check)
	x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: mine.ID, Body: "x", Visibility: "shared"}, ops.CodeNotFound)
}

func TestPrivateCapsAreTheirOwn(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	// no hourly limit for a private comment, and the shared budget is untouched by them
	for i := 0; i < review.MaxCommentsPerMemberPerHour+5; i++ {
		x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: fmt.Sprintf("private %d", i), Visibility: "private"})
	}
	x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "shared still goes through"})
	// the per-author cap, counted over their private rows only
	now := db.Now()
	fill := func(author string, n int, anchored bool) {
		t.Helper()
		state := "none"
		if anchored {
			state = "active"
		}
		_, err := x.d.Writer().Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
			INSERT INTO comments(id, project_id, document_id, author_id, body, visibility, anchor_state, created_at, updated_at) SELECT 'cmt_fill_'||?||i, ?, ?, ?, 'x', 'private', ?, ?, ? FROM n`,
			n, author, x.project, doc, author, state, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	fill(x.editor.UserID, review.MaxPrivatePerAuthor, false)
	e := x.expect(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "one too many", Visibility: "private"}, ops.CodeConflict)
	if !strings.Contains(e.Message, "only you can see") {
		t.Errorf("message: %q", e.Message)
	}
	// ... and does not reach another person, or the shared limit
	x.private(x.owner, doc, "quiet house", "the owner's own")
	x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "shared"})
	// anchored private threads: 100 open per author per chapter, and shared threads are not blocked by them
	if _, err := x.d.Writer().Exec(`DELETE FROM comments WHERE id LIKE 'cmt_fill_%'`); err != nil {
		t.Fatal(err)
	}
	fill(x.editor.UserID, review.MaxPrivateAnchoredPerAuthor, true)
	x.expect(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "quiet house", Body: "x", Visibility: "private"}, ops.CodeConflict)
	x.comment(x.reviewer, doc, "quiet house", "a shared anchored one is not blocked")
	x.private(x.owner, doc, "stood alone", "nor is another person's")
}

func TestPrivateEditHasNoWindowAndASharedOneRunsFromSharing(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	mine := x.private(x.editor, doc, "stood alone", "mine")
	pub := x.comment(x.editor, doc, "quiet house", "shared")
	old := db.Format(time.Now().Add(-3 * time.Hour))
	if _, err := x.d.Writer().Exec(`UPDATE comments SET created_at = ?, shared_at = CASE WHEN visibility = 'shared' THEN ? END`, old, old); err != nil {
		t.Fatal(err)
	}
	if th := x.thread(x.editor, mine.ID); !th.CanEdit {
		t.Error("a private comment cannot be edited after an hour")
	}
	if th := x.thread(x.editor, pub.ID); th.CanEdit {
		t.Error("a shared comment can be edited after three hours")
	}
	got := x.call(x.editor, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: mine.ID, Body: "reworded"}).(review.ThreadItem).Item
	if got.Body != "reworded" || got.EditedAt == "" {
		t.Errorf("private edit: %+v", got)
	}
	x.expect(x.editor, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: pub.ID, Body: "x"}, ops.CodeForbidden)
	x.expect(x.editor, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: mine.ID, Body: "x", Mentions: []string{x.owner.UserID}}, ops.CodeInvalid)
	// the window runs from shared_at, not from created_at
	if _, err := x.d.Writer().Exec(`UPDATE comments SET shared_at = ? WHERE id = ?`, db.Now(), pub.ID); err != nil {
		t.Fatal(err)
	}
	if th := x.thread(x.editor, pub.ID); !th.CanEdit {
		t.Error("the window does not run from shared_at")
	}
	// the owner can delete anyone's shared comment, never a private one
	if th := x.thread(x.editor, mine.ID); !th.CanDelete {
		t.Error("the author cannot delete their private comment")
	}
	if th := x.thread(x.owner, pub.ID); !th.CanDelete {
		t.Error("the owner cannot delete a shared comment")
	}
}

func TestPrivateQueueAndResolveAll(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	shared := x.comment(x.reviewer, doc, "quiet house", "shared")
	mine := x.private(x.editor, doc, "stood alone", canary)
	theirs := x.private(x.owner, doc, "the hill", "the owner's")
	q := func(p ops.Principal, in review.QueueIn) review.QueueOut {
		in.ProjectID = x.project
		return x.call(p, "review.queue", in).(review.QueueOut)
	}
	// the default is the shared review everyone has: the same set as before
	for _, p := range []ops.Principal{x.owner, x.editor} {
		out := q(p, review.QueueIn{})
		if out.Total != 1 || out.Items[0].Thread.ID != shared.ID || len(out.Counts) != 1 || out.Counts[0].Open != 1 {
			t.Errorf("%s: default queue: %+v", p.Name, out)
		}
	}
	// all adds the person's own, and only theirs
	if out := q(x.editor, review.QueueIn{Visibility: "all"}); out.Total != 2 || out.Counts[0].Open != 2 {
		t.Errorf("editor, all: %+v", out)
	}
	if out := q(x.owner, review.QueueIn{Visibility: "all"}); out.Total != 2 {
		t.Errorf("owner, all: %+v", out)
	} else {
		for _, it := range out.Items {
			if it.Thread.ID == mine.ID {
				t.Error("the owner's queue lists the editor's private thread")
			}
		}
	}
	// after= another person's private thread is a 404, as for a missing id
	e1 := ops.AsError(func() error {
		_, err := x.reg.Call(x.ctx, x.owner, "review.queue", review.QueueIn{ProjectID: x.project, After: mine.ID})
		return err
	}())
	e2 := ops.AsError(func() error {
		_, err := x.reg.Call(x.ctx, x.owner, "review.queue", review.QueueIn{ProjectID: x.project, After: "cmt_nope"})
		return err
	}())
	if e1 == nil || e2 == nil || e1.Code != ops.CodeNotFound || e1.Message != e2.Message {
		t.Errorf("after= a private id: %v / %v", e1, e2)
	}
	// Resolve all: the preview counts shared ones only, and a private thread made between preview and confirm does not trip expect_count
	ra := func(p ops.Principal, in review.ResolveAllIn) (review.ResolveAllOut, error) {
		in.ProjectID = x.project
		out, err := x.reg.Call(x.ctx, p, "review.resolve_all", in)
		if err != nil {
			return review.ResolveAllOut{}, err
		}
		return out.(review.ResolveAllOut), nil
	}
	pv, err := ra(x.owner, review.ResolveAllIn{Preview: true})
	if err != nil || pv.Count != 1 {
		t.Fatalf("owner preview: %+v %v", pv, err)
	}
	x.private(x.editor, doc, "quiet", "made between preview and confirm")
	n := pv.Count
	if out, err := ra(x.owner, review.ResolveAllIn{ExpectCount: &n}); err != nil || out.Resolved != 1 {
		t.Fatalf("confirm: %+v %v", out, err)
	}
	if th := x.thread(x.editor, mine.ID); th.Status != "open" {
		t.Error("resolve all resolved a private thread")
	}
	// thread_ids that name another's private thread are skipped as a missing id is; the default never touches one's own
	for _, p := range []ops.Principal{x.owner, x.editor} {
		out, err := ra(p, review.ResolveAllIn{ThreadIDs: []string{mine.ID, theirs.ID, "cmt_nope"}})
		if err != nil || out.Count != 0 || out.Resolved != 0 {
			t.Errorf("%s: thread_ids of private threads: %+v %v", p.Name, out, err)
		}
	}
	// private mode: only the person's own, nobody told, nothing recorded
	notes := x.count(`SELECT count(*) FROM notifications`)
	audits := x.count(`SELECT count(*) FROM audit_log WHERE action = 'review.resolve_all'`)
	events := x.count(`SELECT count(*) FROM events WHERE type = 'review.resolved'`)
	out, err := ra(x.editor, review.ResolveAllIn{Visibility: "private"})
	if err != nil || out.Resolved != 2 {
		t.Fatalf("private resolve: %+v %v", out, err)
	}
	if x.count(`SELECT count(*) FROM notifications`) != notes || x.count(`SELECT count(*) FROM audit_log WHERE action = 'review.resolve_all'`) != audits || x.count(`SELECT count(*) FROM events WHERE type = 'review.resolved'`) != events {
		t.Error("resolving private threads left a notification, an audit row or an event")
	}
	if th := x.thread(x.owner, theirs.ID); th.Status != "open" {
		t.Error("the editor's private resolve touched the owner's thread")
	}
	if _, err := ra(x.owner, review.ResolveAllIn{Visibility: "all"}); err == nil {
		t.Error("resolve_all accepted all")
	}
}

func TestSweepRemovesPrivateCommentsOfGoneAuthorsOnly(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	gone := x.private(x.reviewer, doc, "stood alone", "from a person who left")
	orphan := x.private(x.editor, doc, "quiet", "no author at all")
	kept := x.private(x.owner, doc, "the hill", "still here")
	word := x.comment(x.owner, doc, "quiet house", "a Word import has no author, and is shared")
	if _, err := x.d.Writer().Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), x.reviewer.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := x.d.Writer().Exec(`UPDATE comments SET author_id = NULL, guest_name = 'Word Author' WHERE id IN (?, ?)`, orphan.ID, word.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := review.Sweep(x.ctx, x.d); err != nil {
		t.Fatal(err)
	}
	has := func(id string) bool { return x.count(`SELECT count(*) FROM comments WHERE id = ?`, id) == 1 }
	if has(gone.ID) || has(orphan.ID) {
		t.Error("a private comment of a gone author survived the sweep")
	}
	if !has(kept.ID) || !has(word.ID) {
		t.Error("the sweep removed a comment it must keep (a living author's private one, or a shared Word comment with no account)")
	}
}

func TestPrivateAnchorsFollowEditsAndNeverBlockSharedOnes(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	mine := x.private(x.editor, doc, "stood alone", "mine")
	shared := x.comment(x.reviewer, doc, "quiet house", "shared")
	x.set(doc, "Before it all. The quiet house stood alone on the hill.")
	for _, id := range []string{mine.ID, shared.ID} {
		th := x.thread(x.editor, id)
		if th.Anchor == nil || th.Anchor.State != "active" {
			t.Fatalf("anchor of %s after an edit: %+v", id, th.Anchor)
		}
	}
	th := x.thread(x.editor, mine.ID)
	if got := x.get(doc).Content[th.Anchor.Start:th.Anchor.End]; got != "stood alone" {
		t.Errorf("the private anchor now covers %q", got)
	}
	x.set(doc, "A different sentence entirely.")
	if th := x.thread(x.editor, mine.ID); th.Anchor == nil || th.Anchor.State != "orphaned" {
		t.Errorf("a private anchor whose words are gone: %+v", th.Anchor)
	}
}

// Nothing of a private comment is in any other table, any export or any search: one canary, every table but comments.
func TestPrivateCommentLeavesNoTraceOutsideItsRow(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	th := x.private(x.editor, doc, "stood alone", canary+" body")
	x.call(x.editor, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: th.ID, Body: canary + " reply"})
	x.call(x.editor, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: th.ID, Body: canary + " reworded"})
	x.call(x.editor, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: th.ID})
	x.call(x.editor, "comment.reopen", review.GetIn{ProjectID: x.project, CommentID: th.ID})
	x.set(doc, "Before it all. The quiet house stood alone on the hill.")
	if tb := testutil.TablesHolding(t, x.d, canary, "comments"); len(tb) != 0 {
		t.Errorf("the canary is in %v", tb)
	}
	if tb := testutil.TablesHolding(t, x.d, "stood alone", "comments", "documents", "document_revisions", "revision_blobs", "documents_fts", "documents_fts_data", "documents_fts_idx", "documents_fts_content", "documents_fts_docsize", "documents_fts_config"); len(tb) != 0 {
		t.Logf("the quoted words (also manuscript text) are in %v", tb)
	}
	if x.count(`SELECT count(*) FROM comments WHERE visibility = 'private' AND body LIKE ?`, canary+"%") == 0 {
		t.Error("the author's own row is gone")
	}
	// and the author sees every word of it
	if got := x.thread(x.editor, th.ID); !strings.Contains(got.Body, canary) || len(got.Replies) != 1 {
		t.Errorf("author's read: %+v", got)
	}
}

// One person's private threads, resolved ones included, can be counted past what a save re-anchors; what goes over is orphaned for that
// person alone. Someone else's private thread and the shared threads keep their places.
func TestOnePersonsManyPrivateThreadsOrphanNoOneElsesAnchors(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "Alpha beta gamma delta.")
	shared := x.comment(x.reviewer, doc, "gamma", "shared")
	bens := x.private(x.editor, doc, "delta", "Ben's")
	const many = 1100 // over comanchor's per-author bound of 1,000
	if _, err := x.d.Writer().Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
		INSERT INTO comments(id, project_id, document_id, author_id, body, status, visibility, anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_revision, created_at, updated_at)
		SELECT 'cmt_many_'||i, ?, ?, ?, 'x', 'resolved', 'private', 'active', 0, 5, 'Alpha', 'Alpha', 1, ?, ? FROM n`, many, x.project, doc, x.owner.UserID, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	x.set(doc, "Intro. Alpha beta gamma delta.")
	for _, id := range []string{shared.ID, bens.ID} {
		if th := x.thread(x.editor, id); th.Anchor == nil || th.Anchor.State != "active" {
			t.Errorf("the anchor of %s was lost to someone else's private threads: %+v", id, th.Anchor)
		}
	}
	if got := x.count(`SELECT count(*) FROM comments WHERE id LIKE 'cmt_many_%' AND anchor_state = 'orphaned'`); got != many-1000 {
		t.Errorf("%d of the author's own threads were orphaned, want %d (what went over the bound)", got, many-1000)
	}
}

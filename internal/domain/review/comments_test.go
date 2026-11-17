package review_test

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
)

func TestRolesDecideWhoCanComment(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone.")
	in := review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "quiet house", Body: "Nice."}
	x.expect(x.reader, "comment.create", in, ops.CodeForbidden)
	x.expect(x.stranger, "comment.create", in, ops.CodeNotFound)
	x.expect(x.reader, "comment.list", review.ListIn{ProjectID: x.project}, ops.CodeForbidden)
	th := x.call(x.reviewer, "comment.create", in).(review.ThreadItem).Item
	if th.Anchor == nil || th.Anchor.Quote != "quiet house" || th.Anchor.State != "active" || th.Status != "open" || th.Author.Name != "Rita" {
		t.Fatalf("thread: %+v", th)
	}
	if !th.CanEdit || !th.CanDelete || !th.CanResolve {
		t.Errorf("hints for the author (a reviewer): %+v", th)
	}
	// An editor, the owner and an administrator can all read it.
	for _, p := range []ops.Principal{x.editor, x.owner, x.admin, x.reviewer2} {
		if got := x.thread(p, th.ID); got.ID != th.ID {
			t.Errorf("%s cannot read the thread", p.Name)
		}
	}
	other := x.thread(x.reviewer2, th.ID)
	if other.CanEdit || other.CanDelete || other.CanResolve {
		t.Errorf("another reviewer must not be offered edit, delete or resolve: %+v", other)
	}
}

func TestThreadsRepliesMentionsAndNotifications(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone on the hill.")
	th := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "stood alone", Body: "Why alone? @Edith what do you think?", Kind: "question"}).(review.ThreadItem).Item
	if len(th.Mentions) != 1 || th.Mentions[0].Name != "Edith" || th.Kind != "question" {
		t.Fatalf("mentions: %+v", th)
	}
	// The editor and the owner are told; the author and the reader are not.
	count := func(p ops.Principal) int {
		return x.call(p, "notification.count", review.NotificationCountIn{}).(review.NotificationCountOut).Unread
	}
	if count(x.editor) != 1 || count(x.owner) != 1 || count(x.reviewer) != 0 || count(x.reader) != 0 {
		t.Fatalf("notifications: editor %d owner %d author %d reader %d", count(x.editor), count(x.owner), count(x.reviewer), count(x.reader))
	}
	list := x.call(x.editor, "notification.list", review.NotificationListIn{}).(review.NotificationListOut)
	if len(list.Items) != 1 || list.Items[0].Kind != "mention" || list.Items[0].Actor != "Rita" || !strings.HasPrefix(list.Items[0].Excerpt, "Why alone?") || list.Items[0].ThreadID != th.ID {
		t.Fatalf("list: %+v", list.Items)
	}
	// A reply notifies the author of the thread, once, and the mentioned person is not told twice.
	re := x.call(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, ParentID: th.ID, Body: "Because it is lonely. @Rita @Edith"}).(review.ThreadItem).Item
	if len(re.Replies) != 1 || re.Replies[0].ThreadID != th.ID {
		t.Fatalf("reply: %+v", re)
	}
	if count(x.reviewer) != 1 || count(x.editor) != 2 {
		t.Errorf("after the reply: author %d editor %d", count(x.reviewer), count(x.editor))
	}
	rl := x.call(x.reviewer, "notification.list", review.NotificationListIn{}).(review.NotificationListOut).Items
	if len(rl) != 1 || rl[0].Kind != "mention" {
		t.Errorf("a mention outranks a reply: %+v", rl)
	}
	// A reply to a reply is refused: threads are one level deep.
	x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: re.Replies[0].ID, Body: "x"}, ops.CodeInvalid)
	// Mentioning a reader (who cannot read comments) is refused for an explicit id and ignored by name.
	x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "hi", Mentions: []string{x.reader.UserID}}, ops.CodeInvalid)
	nm := x.call(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "hi @Reed"}).(review.ThreadItem).Item
	if len(nm.Mentions) != 0 {
		t.Errorf("a reader was mentioned by name: %+v", nm.Mentions)
	}
	// Mark read.
	x.call(x.editor, "notification.mark_read", review.MarkReadIn{IDs: []string{list.Items[0].ID}})
	if count(x.editor) != 1 {
		t.Errorf("mark_read: %d unread", count(x.editor))
	}
	x.call(x.editor, "notification.read_all", review.ReadAllIn{})
	if count(x.editor) != 0 {
		t.Error("read_all left some")
	}
	// Notifications are private: another person's ids change nothing.
	if n := x.call(x.reviewer2, "notification.mark_read", review.MarkReadIn{IDs: []string{list.Items[0].ID}}).(review.MarkReadOut).Changed; n != 0 {
		t.Error("marked someone else's notification")
	}
	// Document-level and project-level comments.
	x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Body: "Whole chapter: slow start."})
	pc := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, Body: "Overall: love it."}).(review.ThreadItem).Item
	if pc.DocumentID != "" || pc.Anchor != nil {
		t.Errorf("project-level: %+v", pc)
	}
	lst := x.call(x.owner, "comment.list", review.ListIn{ProjectID: x.project, DocumentID: "project"}).(review.ListOut)
	if lst.Total != 1 || lst.Items[0].ID != pc.ID {
		t.Errorf("project-level list: %+v", lst)
	}
	// A body is plain text, bounded, and carries no invisible characters.
	x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, Body: "  "}, ops.CodeInvalid)
	x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, Body: strings.Repeat("a", review.MaxBody+1)}, ops.CodeInvalid)
	got := x.call(x.owner, "comment.create", review.CreateIn{ProjectID: x.project, Body: "a‮b<script>"}).(review.ThreadItem).Item
	if got.Body != "ab<script>" {
		t.Errorf("a direction override survived, or the text was changed: %q", got.Body)
	}
}

func TestResolveReopenAndWhoMay(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone.")
	mine := x.comment(x.reviewer, doc, "quiet", "Mine.")
	theirs := x.comment(x.reviewer2, doc, "alone", "Theirs.")
	x.expect(x.reviewer, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: theirs.ID}, ops.CodeForbidden)
	r := x.call(x.reviewer, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: mine.ID}).(review.ThreadItem).Item
	if r.Status != "resolved" || r.ResolvedBy == nil || r.ResolvedBy.Name != "Rita" {
		t.Fatalf("resolved: %+v", r)
	}
	// The editor resolves anyone's, and answering a reply id resolves its thread.
	rep := x.call(x.reviewer2, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: theirs.ID, Body: "More."}).(review.ThreadItem).Item.Replies[0]
	if got := x.call(x.editor, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: rep.ID}).(review.ThreadItem).Item; got.ID != theirs.ID || got.Status != "resolved" {
		t.Fatalf("resolve by reply id: %+v", got)
	}
	// Resolving twice changes nothing; the author is told once per resolution.
	x.call(x.editor, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: theirs.ID})
	if n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND kind = 'resolved'`, x.reviewer2.UserID); n != 1 {
		t.Errorf("%d resolved notifications", n)
	}
	re := x.call(x.reviewer2, "comment.reopen", review.GetIn{ProjectID: x.project, CommentID: theirs.ID}).(review.ThreadItem).Item
	if re.Status != "open" || re.ResolvedBy != nil {
		t.Errorf("reopened: %+v", re)
	}
	open := x.call(x.owner, "comment.list", review.ListIn{ProjectID: x.project, Status: "open"}).(review.ListOut)
	if open.Total != 1 || open.Open != 1 || open.Resolved != 1 {
		t.Errorf("counts: %+v", open)
	}
}

func TestEditWindowAndTombstones(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone.")
	now := time.Now()
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	th := x.comment(x.reviewer, doc, "quiet", "First thought.")
	x.expect(x.reviewer2, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: th.ID, Body: "hijack"}, ops.CodeForbidden)
	up := x.call(x.reviewer, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: th.ID, Body: "Second thought. @Edith"}).(review.ThreadItem).Item
	if up.Body != "Second thought. @Edith" || up.EditedAt == "" || len(up.Mentions) != 1 {
		t.Fatalf("update: %+v", up)
	}
	if n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND kind = 'mention'`, x.editor.UserID); n != 1 {
		t.Errorf("newly mentioned person notified %d times", n)
	}
	now = now.Add(review.EditWindow + time.Minute)
	x.expect(x.reviewer, "comment.update", review.UpdateIn{ProjectID: x.project, CommentID: th.ID, Body: "too late"}, ops.CodeForbidden)
	if x.thread(x.reviewer, th.ID).CanEdit {
		t.Error("can_edit after the window")
	}
	// A reply by someone else keeps the thread when the root is deleted.
	x.call(x.reviewer2, "comment.create", review.CreateIn{ProjectID: x.project, ParentID: th.ID, Body: "A reply."})
	x.expect(x.reviewer2, "comment.delete", review.GetIn{ProjectID: x.project, CommentID: th.ID}, ops.CodeForbidden)
	x.call(x.owner, "comment.delete", review.GetIn{ProjectID: x.project, CommentID: th.ID}) // the owner may delete anything
	tomb := x.thread(x.owner, th.ID)
	if !tomb.Deleted || tomb.Body != "" || tomb.Anchor != nil || len(tomb.Replies) != 1 || tomb.Replies[0].Body != "A reply." {
		t.Fatalf("tombstone: %+v", tomb)
	}
	if n := x.count(`SELECT count(*) FROM comment_mentions WHERE comment_id = ?`, th.ID); n != 0 {
		t.Error("mentions of a deleted comment remain")
	}
	if n := x.count(`SELECT count(*) FROM notifications WHERE comment_id = ?`, th.ID); n != 0 {
		t.Error("notifications of a deleted comment remain")
	}
	// Deleting again is harmless; a thread with nothing live is not listed.
	x.call(x.owner, "comment.delete", review.GetIn{ProjectID: x.project, CommentID: th.ID})
	rep := tomb.Replies[0]
	x.call(x.reviewer2, "comment.delete", review.GetIn{ProjectID: x.project, CommentID: rep.ID})
	if l := x.call(x.owner, "comment.list", review.ListIn{ProjectID: x.project}).(review.ListOut); l.Total != 0 {
		t.Errorf("an empty thread is listed: %+v", l)
	}
}

func TestListFilters(t *testing.T) {
	x := newH(t)
	d1 := x.doc("One", "Alpha beta gamma delta.")
	d2 := x.doc("Two", "Epsilon zeta eta theta.")
	a := x.comment(x.reviewer, d1, "beta", "About beta, needs work.")
	b := x.comment(x.reviewer2, d1, "gamma", "About gamma. @Rita")
	c := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: d2, Body: "Chapter note", Kind: "question"}).(review.ThreadItem).Item
	x.call(x.editor, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: a.ID})
	list := func(in review.ListIn) []string {
		in.ProjectID = x.project
		var ids []string
		for _, it := range x.call(x.reviewer, "comment.list", in).(review.ListOut).Items {
			ids = append(ids, it.ID)
		}
		return ids
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: got %v want %v", name, got, want)
		}
	}
	eq("all", list(review.ListIn{}), a.ID, b.ID, c.ID)
	eq("doc one in document order", list(review.ListIn{DocumentID: d1}), a.ID, b.ID)
	eq("open", list(review.ListIn{Status: "open"}), b.ID, c.ID)
	eq("resolved", list(review.ListIn{Status: "resolved"}), a.ID)
	eq("author me", list(review.ListIn{Author: "me"}), a.ID, c.ID)
	eq("mentions me", list(review.ListIn{MentionsMe: true}), b.ID)
	eq("question", list(review.ListIn{Kind: "question"}), c.ID)
	eq("doc-level only", list(review.ListIn{State: "none"}), c.ID)
	eq("q", list(review.ListIn{Q: "needs"}), a.ID)
	eq("q with a wildcard is literal", list(review.ListIn{Q: "%"}))
	eq("paging", list(review.ListIn{Limit: 1, Offset: 1}), b.ID)
	x.expect(x.reviewer, "comment.list", review.ListIn{ProjectID: x.project, Status: "bogus"}, ops.CodeInvalid)
	// A trashed document's comments are not listed until it is restored.
	x.call(x.owner, "doc.trash", docs.DocIn{ID: d2})
	eq("trashed document", list(review.ListIn{}), a.ID, b.ID)
	x.call(x.owner, "doc.restore", docs.DocIn{ID: d2})
	eq("restored document", list(review.ListIn{}), a.ID, b.ID, c.ID)
}

// anchored returns the anchor of a thread.
func (x *h) anchor(id string) review.AnchorOut {
	x.t.Helper()
	a := x.thread(x.owner, id).Anchor
	if a == nil {
		x.t.Fatalf("thread %s has no anchor", id)
	}
	return *a
}

func TestAnchorsFollowEditsAndNeverGuess(t *testing.T) {
	x := newH(t)
	text := "First paragraph about the sea.\n\nSecond paragraph: the quiet house stood alone on the hill, and the quiet house said nothing.\n\nThird paragraph ends here.\n"
	doc := x.doc("One", text)
	// Two like phrases: the second "quiet house" is told apart by its context.
	c := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "quiet house", Before: "and the ", Body: "second one"}).(review.ThreadItem).Item
	if c.Anchor.Start != strings.Index(text[strings.Index(text, "quiet house")+1:], "quiet house")+strings.Index(text, "quiet house")+1 {
		t.Fatalf("anchored to the wrong one: %+v", c.Anchor)
	}
	// An ambiguous quote is refused, not guessed.
	x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Quote: "quiet house", Body: "which?"}, ops.CodeInvalid)
	// Before: typing at the top moves the anchor.
	x.set(doc, "A new first line.\n\n"+text)
	a := x.anchor(c.ID)
	cur := x.get(doc).Content
	if a.State != "active" || cur[a.Start:a.End] != "quiet house" || a.Start != strings.LastIndex(cur, "quiet house") {
		t.Fatalf("after typing before: %+v", a)
	}
	// After: typing below changes nothing.
	x.set(doc, cur+"\nA closing line.\n")
	if b := x.anchor(c.ID); b.Start != a.Start || b.State != "active" {
		t.Fatalf("after typing after: %+v vs %+v", b, a)
	}
	// Inside: the writer fixes a word within the commented text; the comment follows.
	cur = x.get(doc).Content
	x.set(doc, strings.Replace(cur, "and the quiet house said", "and the quiet houses said", 1))
	in := x.anchor(c.ID)
	cur = x.get(doc).Content
	if in.State != "active" || cur[in.Start:in.End] != "quiet houses" || in.Original != "quiet house" {
		t.Fatalf("inside edit: %+v", in)
	}
	// Gone: the sentence is rewritten; the comment is orphaned and keeps its last place.
	x.set(doc, strings.Replace(cur, "and the quiet houses said nothing", "and silence fell", 1))
	gone := x.anchor(c.ID)
	if gone.State != "orphaned" {
		t.Fatalf("a deleted anchor: %+v", gone)
	}
	// A lookalike elsewhere is not adopted: put the same words in another paragraph.
	cur = x.get(doc).Content
	x.set(doc, cur+"\nThe quiet houses are elsewhere.\n")
	if still := x.anchor(c.ID); still.State != "orphaned" {
		t.Fatalf("an orphan was moved onto a lookalike: %+v", still)
	}
	// It is listed in the queue as lost, and can be re-attached by hand.
	q := x.call(x.editor, "review.queue", review.QueueIn{ProjectID: x.project}).(review.QueueOut)
	if q.Total != 1 || q.Orphaned != 1 || q.Items[0].Thread.Anchor.State != "orphaned" {
		t.Fatalf("queue: %+v", q)
	}
	re := x.call(x.editor, "comment.reanchor", review.ReanchorIn{ProjectID: x.project, CommentID: c.ID, Quote: "silence fell"}).(review.ThreadItem).Item
	if re.Anchor.State != "active" || re.Anchor.Quote != "silence fell" || re.Anchor.Original != "quiet house" {
		t.Fatalf("reanchor: %+v", re.Anchor)
	}
	// Detach: the thread is on the document as a whole.
	if d := x.call(x.editor, "comment.reanchor", review.ReanchorIn{ProjectID: x.project, CommentID: c.ID, Detach: true}).(review.ThreadItem).Item; d.Anchor != nil {
		t.Errorf("detach: %+v", d.Anchor)
	}
}

func TestAnchorsRefuseHiddenAndStructuralPlaces(t *testing.T) {
	x := newH(t)
	text := "Visible words here. %%a private aside about the plot%% More visible words.\n\n```\ncode block text\n```\n\nThe <scriobh-ghost>cut sentence</scriobh-ghost> stays.\n"
	doc := x.doc("One", text)
	for name, in := range map[string]review.CreateIn{
		"a private note":  {Quote: "private aside"},
		"across a note":   {Quote: "here. %%a private"},
		"code":            {Quote: "code block"},
		"ghosted text":    {Quote: "cut sentence"},
		"a ghost marker":  {Quote: "<scriobh-ghost>cut"},
		"a space at edge": {Quote: " words"},
	} {
		in.ProjectID, in.DocumentID, in.Body = x.project, doc, "x"
		if e := x.expect(x.reviewer, "comment.create", in, ops.CodeInvalid); e.Message == "" {
			t.Errorf("%s: no message", name)
		}
	}
	// A comment on visible text that a later edit wraps in a private note is orphaned, never shown there.
	c := x.comment(x.reviewer, doc, "More visible", "ok")
	cur := x.get(doc).Content
	x.set(doc, strings.Replace(cur, "More visible words.", "%%More visible words.%%", 1))
	if a := x.anchor(c.ID); a.State != "orphaned" {
		t.Errorf("a comment inside a note after an edit: %+v", a)
	}
	// An unanchored comment on a document that does not exist, or on another project's.
	x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: "doc_00000000000000000000000000", Body: "x"}, ops.CodeNotFound)
	// Offsets need the current revision.
	d := x.get(doc)
	s, e := 0, 7
	x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Start: &s, End: &e, BaseRevision: d.Revision - 1, Body: "x"}, ops.CodeConflict)
	if ok := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: doc, Start: &s, End: &e, BaseRevision: d.Revision, Body: "x"}).(review.ThreadItem).Item; ok.Anchor.Quote != "Visible" {
		t.Errorf("offsets: %+v", ok.Anchor)
	}
}

func TestQueueIsInReadingOrderWithNextUnresolved(t *testing.T) {
	x := newH(t)
	d1 := x.doc("One", "Alpha beta gamma.\n\nDelta epsilon zeta.")
	d2 := x.doc("Two", "Eta theta iota.")
	pc := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, Body: "Overall."}).(review.ThreadItem).Item
	c2 := x.comment(x.reviewer, d2, "theta", "two")
	c1b := x.comment(x.reviewer, d1, "epsilon", "one b")
	c1a := x.comment(x.reviewer, d1, "beta", "one a")
	whole := x.call(x.reviewer, "comment.create", review.CreateIn{ProjectID: x.project, DocumentID: d1, Body: "whole one"}).(review.ThreadItem).Item
	q := x.call(x.editor, "review.queue", review.QueueIn{ProjectID: x.project}).(review.QueueOut)
	var got []string
	for _, it := range q.Items {
		got = append(got, it.Thread.ID)
	}
	want := []string{pc.ID, whole.ID, c1a.ID, c1b.ID, c2.ID}
	if strings.Join(got, ",") != strings.Join(want, ",") || q.Total != 5 || q.ProjectLevel != 1 {
		t.Fatalf("order %v, want %v (%+v)", got, want, q)
	}
	if q.Items[0].Document != nil || q.Items[2].Document == nil || q.Items[2].Document.Title != "One" || q.Items[2].Position != 3 {
		t.Errorf("documents: %+v / %+v", q.Items[0].Document, q.Items[2])
	}
	if len(q.Counts) != 2 || q.Counts[0].Title != "One" || q.Counts[0].Open != 3 || q.Counts[1].Open != 1 {
		t.Errorf("counts: %+v", q.Counts)
	}
	// Next unresolved after one that was just resolved.
	x.call(x.editor, "comment.resolve", review.GetIn{ProjectID: x.project, CommentID: c1a.ID})
	n := x.call(x.editor, "review.queue", review.QueueIn{ProjectID: x.project, After: c1a.ID, Limit: 1}).(review.QueueOut)
	if n.Next != c1b.ID || len(n.Items) != 1 || n.Total != 4 {
		t.Errorf("next after a resolved one: %+v", n)
	}
	// A reviewer has no queue.
	x.expect(x.reviewer, "review.queue", review.QueueIn{ProjectID: x.project}, ops.CodeForbidden)
	// resolve_all: preview, then with the count; a changed set is refused.
	pre := x.call(x.editor, "review.resolve_all", review.ResolveAllIn{ProjectID: x.project, DocumentID: d1, Preview: true}).(review.ResolveAllOut)
	if pre.Count != 2 || pre.Resolved != 0 {
		t.Fatalf("preview: %+v", pre)
	}
	if x.thread(x.owner, c1b.ID).Status != "open" {
		t.Error("a preview resolved something")
	}
	two := 3
	x.expect(x.editor, "review.resolve_all", review.ResolveAllIn{ProjectID: x.project, DocumentID: d1, ExpectCount: &two}, ops.CodeConflict)
	exp := pre.Count
	done := x.call(x.editor, "review.resolve_all", review.ResolveAllIn{ProjectID: x.project, DocumentID: d1, ExpectCount: &exp}).(review.ResolveAllOut)
	if done.Resolved != 2 || x.thread(x.owner, c1b.ID).Status != "resolved" || x.thread(x.owner, c2.ID).Status != "open" {
		t.Fatalf("resolve_all: %+v", done)
	}
	if n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND kind = 'resolved'`, x.reviewer.UserID); n != 3 {
		t.Errorf("author told %d times", n)
	}
}

func TestCommentsAreNotDocumentContent(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", "The quiet house stood alone.")
	before := x.get(doc)
	c := x.comment(x.reviewer, doc, "quiet", "SECRETCOMMENTTEXT")
	after := x.get(doc)
	if before.Revision != after.Revision || before.ContentHash != after.ContentHash || before.WordCount != after.WordCount {
		t.Fatal("a comment changed the document")
	}
	if strings.Contains(after.Content, "SECRETCOMMENTTEXT") {
		t.Fatal("comment text in the document")
	}
	for _, q := range []string{`SELECT count(*) FROM doc_text WHERE body LIKE '%SECRETCOMMENTTEXT%'`, `SELECT count(*) FROM events WHERE payload_json LIKE '%SECRETCOMMENTTEXT%'`,
		`SELECT count(*) FROM audit_log WHERE detail_json LIKE '%SECRETCOMMENTTEXT%'`} {
		if x.count(q) != 0 {
			t.Errorf("comment text reached: %s", q)
		}
	}
	// Events about it carry ids only.
	rows, err := x.d.Read().Query(`SELECT type, payload_json FROM events WHERE type LIKE 'comment.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var typ, p string
		_ = rows.Scan(&typ, &p)
		seen++
		if !strings.Contains(p, c.ID) || strings.ContainsAny(strings.ReplaceAll(strings.ReplaceAll(p, `"`, ""), "{", ""), " ") {
			t.Errorf("%s payload: %s", typ, p)
		}
	}
	if seen == 0 {
		t.Error("no comment event was written")
	}
	_ = comanchor.StateActive
}

package review_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

const sugText = "The quiet house stood alone on the hill.\n\nIts windows were dark and the door was shut tight.\n\nNobody had lived there for years.\n"

func (x *h) suggest(p ops.Principal, doc string, rev int, changes ...review.ChangeIn) review.SuggestionOut {
	x.t.Helper()
	return x.call(p, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: rev, Changes: changes}).(review.SuggestionItem).Item
}

func (x *h) accept(p ops.Principal, id string) review.SuggestionAcceptOut {
	x.t.Helper()
	return x.call(p, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: id}).(review.SuggestionAcceptOut)
}

func rep(quote, text string) review.ChangeIn { return review.ChangeIn{Quote: quote, Text: text} }

func (x *h) wordsAdded(p ops.Principal) int {
	x.t.Helper()
	return x.call(p, "stats.today", goals.StatsIn{ProjectID: x.project}).(goals.TodayOut).WordsAdded
}

func TestSuggestionLifecycleLeavesTheTextAloneUntilAccepted(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	d := x.get(doc)
	// A reader cannot suggest; a reviewer can.
	x.expect(x.reader, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: d.Revision, Changes: []review.ChangeIn{rep("quiet", "silent")}}, ops.CodeForbidden)
	s := x.suggest(x.reviewer, doc, d.Revision, rep("quiet", "silent"), review.ChangeIn{Quote: "hill", Where: "after", Text: " by the sea"})
	if s.Status != "open" || len(s.Changes) != 2 || s.Changes[0].Kind != "replace" || s.Changes[1].Kind != "insert" || s.Stale || s.Author.Name != "Rita" {
		t.Fatalf("suggestion: %+v", s)
	}
	// Open suggestions do not change the document at all.
	if g := x.get(doc); g.Revision != d.Revision || g.Content != d.Content {
		t.Fatal("an open suggestion changed the text")
	}
	// The owner and the editor are asked; a reviewer cannot decide.
	if x.call(x.editor, "notification.count", review.NotificationCountIn{}).(review.NotificationCountOut).Unread != 1 {
		t.Error("the editor was not told of the suggestion")
	}
	x.expect(x.reviewer, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: s.ID}, ops.CodeForbidden)
	x.expect(x.reviewer, "suggestion.reject", review.SuggestionRef{ProjectID: x.project, SuggestionID: s.ID}, ops.CodeForbidden)
	x.expect(x.reviewer2, "suggestion.withdraw", review.SuggestionRef{ProjectID: x.project, SuggestionID: s.ID}, ops.CodeForbidden)
	before := x.wordsAdded(x.editor)
	out := x.accept(x.editor, s.ID)
	got := x.get(doc)
	want := strings.Replace(strings.Replace(sugText, "quiet", "silent", 1), "hill.", "hill by the sea.", 1)
	if got.Content != want || out.Revision != got.Revision || out.Item.Status != "accepted" || out.Item.AppliedRevision != got.Revision {
		t.Fatalf("accepted text:\n%q\nwant\n%q\n%+v", got.Content, want, out)
	}
	// One revision with its own reason, naming the suggestion and never the text, and the text before is kept.
	var reason, note string
	if err := x.d.Read().QueryRow(`SELECT reason, note FROM document_revisions WHERE id = ?`, out.RevisionID).Scan(&reason, &note); err != nil {
		t.Fatal(err)
	}
	if reason != "suggestion-accepted" || !strings.Contains(note, s.ID) || strings.Contains(note, "silent") {
		t.Errorf("revision %q %q", reason, note)
	}
	if x.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND content_hash = ?`, doc, revisions.Hash(sugText)) == 0 {
		t.Error("the text before the accept was not kept")
	}
	// No credit for words a collaborator wrote.
	if x.wordsAdded(x.editor) != before {
		t.Errorf("an accepted suggestion earned goal credit: %d to %d", before, x.wordsAdded(x.editor))
	}
	// The author hears of it; deciding twice is a conflict.
	if n := x.count(`SELECT count(*) FROM notifications WHERE user_id = ? AND kind = 'suggestion-decided'`, x.reviewer.UserID); n != 1 {
		t.Errorf("author notified %d times", n)
	}
	x.expect(x.editor, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: s.ID}, ops.CodeConflict)
	x.expect(x.editor, "suggestion.reject", review.SuggestionRef{ProjectID: x.project, SuggestionID: s.ID}, ops.CodeConflict)
	// restoring the revision before undoes the accept
	var prev string
	if err := x.d.Read().QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? AND content_hash = ?`, doc, revisions.Hash(sugText)).Scan(&prev); err != nil {
		t.Fatal(err)
	}
	// Reject and withdraw.
	d = x.get(doc)
	s2 := x.suggest(x.reviewer, doc, d.Revision, rep("shut tight", "locked"))
	r := x.call(x.editor, "suggestion.reject", review.SuggestionRef{ProjectID: x.project, SuggestionID: s2.ID}).(review.SuggestionItem).Item
	if r.Status != "rejected" || x.get(doc).Content != d.Content {
		t.Errorf("reject: %+v", r)
	}
	s3 := x.suggest(x.reviewer, doc, d.Revision, rep("years", "ages"))
	w := x.call(x.reviewer, "suggestion.withdraw", review.SuggestionRef{ProjectID: x.project, SuggestionID: s3.ID}).(review.SuggestionItem).Item
	if w.Status != "withdrawn" || x.get(doc).Content != d.Content {
		t.Errorf("withdraw: %+v", w)
	}
	open := x.call(x.owner, "suggestion.list", review.SuggestionListIn{ProjectID: x.project}).(review.SuggestionListOut)
	all := x.call(x.owner, "suggestion.list", review.SuggestionListIn{ProjectID: x.project, Status: "all"}).(review.SuggestionListOut)
	if open.Total != 0 || all.Total != 3 {
		t.Errorf("list: open %d all %d", open.Total, all.Total)
	}
	_ = prev
}

func TestSuggestionAtAnOldRevisionAppliesOnlyWhereTheTextIsFoundAgain(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	d := x.get(doc)
	s := x.suggest(x.reviewer, doc, d.Revision, rep("windows were dark", "windows were black"))
	// The writer adds a paragraph above: the text moved, the context is the same: it still applies.
	x.set(doc, "A new opening paragraph.\n\n"+sugText)
	list := x.call(x.owner, "suggestion.list", review.SuggestionListIn{ProjectID: x.project}).(review.SuggestionListOut)
	if !list.Items[0].Stale {
		t.Error("the suggestion is not marked stale")
	}
	x.accept(x.editor, s.ID)
	if got := x.get(doc).Content; !strings.Contains(got, "Its windows were black and") || !strings.HasPrefix(got, "A new opening paragraph.") {
		t.Fatalf("moved text: %q", got)
	}
	// A rewrite of the words around it makes it stale: 409, nothing applied, the text untouched.
	x2 := x.suggest(x.reviewer, doc, x.get(doc).Revision, rep("door was shut tight", "door was bolted"))
	cur := x.get(doc)
	x.set(doc, strings.Replace(cur.Content, "and the door was shut tight", "and the gate was shut tight", 1))
	cur = x.get(doc)
	e := x.expect(x.editor, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: x2.ID}, ops.CodeConflict)
	if e.Data["reason"] != "stale" {
		t.Errorf("conflict data: %+v", e.Data)
	}
	if x.get(doc).Content != cur.Content {
		t.Error("a stale suggestion changed the text")
	}
	// A lookalike elsewhere is not the place: the same words under other context are not found.
	x3 := x.suggest(x.reviewer, doc, x.get(doc).Revision, rep("years", "decades"))
	x.set(doc, strings.Replace(x.get(doc).Content, "for years.", "for ages.", 1)+"\nIt was empty for years and years.\n")
	x.expect(x.editor, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: x3.ID}, ops.CodeConflict)
	// The base_revision of the editor, when given, must be current.
	x4 := x.suggest(x.reviewer, doc, x.get(doc).Revision, rep("empty", "bare"))
	x.expect(x.editor, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: x.project, SuggestionID: x4.ID, BaseRevision: 1}, ops.CodeConflict)
	// A suggestion is made at the current revision only.
	x.expect(x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: 1, Changes: []review.ChangeIn{rep("empty", "bare")}}, ops.CodeConflict)
}

func TestSuggestionsAreHeldToTheSafetyRulesOfAcceptedText(t *testing.T) {
	x := newH(t)
	text := "The quiet house stood alone. %%a secret plan%% It had a [link](https://example.com/a) in it.\n\nSecond paragraph here.\n"
	doc := x.doc("One", text)
	rev := x.get(doc).Revision
	hostile := map[string]string{
		"private marker":     "quiet %% x",
		"alts marker":        "<!-- scriobh:alts {} -->",
		"ghost":              "<scriobh-ghost>x</scriobh-ghost>",
		"html comment":       "x <!-- y -->",
		"bidi override":      "x ‮evil",
		"zero width space":   "x​y",
		"control":            "x\x01y",
		"new link":           "see [here](https://evil.example/x)",
		"bare address":       "see https://evil.example/x",
		"image":              "![x](https://evil.example/i.png)",
		"a new heading":      "x\n# Heading",
		"a new list":         "x\n- item",
		"invalid utf8":       "x\xffy",
		"a blank line":       "x\n\ny",
		"a quote line":       "x\n> quoted",
		"fence":              "x\n```\ncode",
		"table":              "x\n| a | b |",
		"a replacement same": "quiet",
	}
	for name, txt := range hostile {
		_, err := x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: rev, Changes: []review.ChangeIn{rep("quiet", txt)}})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A link the original already had may stay.
	x.suggest(x.reviewer, doc, rev, rep("[link](https://example.com/a)", "[a link](https://example.com/a)"))
	// Ranges: a private note, across its marker, ghosted text, and a marker are refused.
	for name, q := range map[string]string{"in a note": "secret plan", "across a note": "alone. %%a secret"} {
		_, err := x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: rev, Changes: []review.ChangeIn{rep(q, "x")}})
		if err == nil {
			t.Errorf("%s: a suggestion was made over it", name)
		}
	}
	// A range through an alternative is refused as an accepted AI suggestion's would be.
	alt := x.call(x.owner, "alternative.create", docs.AltCreateIn{ID: doc, BaseRevision: rev, Scope: "word", Quote: "quiet", Variants: []string{"still"}}).(docs.AltMut)
	_, err := x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: alt.Revision, Changes: []review.ChangeIn{rep("quiet house", "x house")}})
	if err == nil {
		t.Error("a suggestion was made over an alternative's anchor")
	}
	// Changes that overlap each other, and too many, are refused; so is a note that is too long.
	_, err = x.reg.Call(x.ctx, x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: alt.Revision,
		Changes: []review.ChangeIn{rep("Second paragraph", "Third"), rep("paragraph here", "x")}})
	if err == nil {
		t.Error("overlapping changes were accepted")
	}
	x.expect(x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: alt.Revision, Changes: make([]review.ChangeIn, 51)}, ops.CodeInvalid)
	x.expect(x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: x.project, DocumentID: doc, BaseRevision: alt.Revision, Note: strings.Repeat("n", 2001), Changes: []review.ChangeIn{rep("Second", "Third")}}, ops.CodeInvalid)
}

func TestAcceptAllIsOneRevisionWithDeterministicOverlaps(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	rev := x.get(doc).Revision
	a := x.suggest(x.reviewer, doc, rev, rep("quiet", "silent"))
	b := x.suggest(x.reviewer2, doc, rev, rep("windows were dark", "windows were black"))
	c := x.suggest(x.reviewer, doc, rev, rep("windows", "panes")) // overlaps b, which starts at the same place and is made first
	d := x.suggest(x.reviewer2, doc, rev, rep("Nobody", "No one"), review.ChangeIn{Quote: "years", Text: "ages"})
	in := review.SuggestionAcceptAllIn{ProjectID: x.project, DocumentID: doc, SuggestionIDs: []string{d.ID, c.ID, b.ID, a.ID}}
	pre := in
	pre.Preview = true
	plan := x.call(x.editor, "suggestion.accept_all", pre).(review.SuggestionAcceptAllOut)
	if len(plan.Applied) != 3 || len(plan.Skipped) != 1 || plan.Skipped[0].SuggestionID != c.ID || plan.Skipped[0].Reason != "overlap" || plan.Revision != rev {
		t.Fatalf("plan: %+v", plan)
	}
	if x.get(doc).Revision != rev {
		t.Fatal("a preview wrote")
	}
	out := x.call(x.editor, "suggestion.accept_all", in).(review.SuggestionAcceptAllOut)
	got := x.get(doc)
	want := "The silent house stood alone on the hill.\n\nIts windows were black and the door was shut tight.\n\nNo one had lived there for ages.\n"
	if got.Content != want || got.Revision != rev+1 || out.Revision != got.Revision || out.RevisionID == "" {
		t.Fatalf("accept_all:\n%q\nwant\n%q\n%+v", got.Content, want, out)
	}
	if n := x.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND reason = 'suggestion-accepted'`, doc); n != 1 {
		t.Errorf("%d suggestion-accepted revisions, want 1", n)
	}
	st := func(id string) string {
		return x.call(x.owner, "suggestion.get", review.SuggestionRef{ProjectID: x.project, SuggestionID: id}).(review.SuggestionItem).Item.Status
	}
	if st(a.ID) != "accepted" || st(b.ID) != "accepted" || st(d.ID) != "accepted" || st(c.ID) != "open" {
		t.Errorf("statuses: %s %s %s %s", st(a.ID), st(b.ID), st(c.ID), st(d.ID))
	}
	// the one that overlapped is now stale and cannot be found again: skipped, listed
	again := x.call(x.editor, "suggestion.accept_all", review.SuggestionAcceptAllIn{ProjectID: x.project, DocumentID: doc, SuggestionIDs: []string{c.ID, a.ID}}).(review.SuggestionAcceptAllOut)
	if len(again.Applied) != 0 || len(again.Skipped) != 2 || x.get(doc).Revision != got.Revision {
		t.Errorf("second accept_all: %+v", again)
	}
	// a suggestion of another document is not found here
	other := x.doc("Two", "Other text entirely.")
	x.expect(x.editor, "suggestion.accept_all", review.SuggestionAcceptAllIn{ProjectID: x.project, DocumentID: other, SuggestionIDs: []string{c.ID}}, ops.CodeNotFound)
}

func TestSuggestionsAndCommentsBelongToTheirProject(t *testing.T) {
	x := newH(t)
	doc := x.doc("One", sugText)
	s := x.suggest(x.reviewer, doc, x.get(doc).Revision, rep("quiet", "silent"))
	c := x.comment(x.reviewer, doc, "quiet", "x")
	// A second project the same people are on, with the reviewer on it as well.
	pid := x.call(x.owner, "project.create", projects.CreateIn{Title: "Second"}).(projects.Item).Item.ID
	for _, p := range []ops.Principal{x.reviewer, x.editor} {
		role := ops.ProjectReviewer
		if p.UserID == x.editor.UserID {
			role = ops.ProjectEditor
		}
		if _, err := x.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, p.UserID, role, "2026-01-01T00:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	// Naming another project's comment, suggestion or document through this project is not found.
	x.expect(x.reviewer, "comment.get", review.GetIn{ProjectID: pid, CommentID: c.ID}, ops.CodeNotFound)
	x.expect(x.reviewer, "comment.resolve", review.GetIn{ProjectID: pid, CommentID: c.ID}, ops.CodeNotFound)
	x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: pid, DocumentID: doc, Body: "x"}, ops.CodeNotFound)
	x.expect(x.reviewer, "comment.create", review.CreateIn{ProjectID: pid, ParentID: c.ID, Body: "x"}, ops.CodeNotFound)
	x.expect(x.editor, "suggestion.accept", review.SuggestionAcceptIn{ProjectID: pid, SuggestionID: s.ID}, ops.CodeNotFound)
	x.expect(x.editor, "suggestion.reject", review.SuggestionRef{ProjectID: pid, SuggestionID: s.ID}, ops.CodeNotFound)
	x.expect(x.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: pid, DocumentID: doc, BaseRevision: 1, Changes: []review.ChangeIn{rep("quiet", "x")}}, ops.CodeNotFound)
	x.expect(x.editor, "review.queue", review.QueueIn{ProjectID: pid, After: c.ID}, ops.CodeNotFound)
	if n := x.call(x.editor, "review.queue", review.QueueIn{ProjectID: pid}).(review.QueueOut).Total; n != 0 {
		t.Errorf("the other project's queue holds %d", n)
	}
	// A mention of someone who is not on the project is refused.
	x.expect(x.owner, "comment.create", review.CreateIn{ProjectID: pid, Body: "hi", Mentions: []string{x.reviewer2.UserID}}, ops.CodeInvalid)
}

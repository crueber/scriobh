package mirror_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/share"
)

// Review data is not part of the manuscript, so it is not in its projection: comments,
// suggestions and share links wake the mirror for nothing, and nothing of them is in a file
// (and so nothing of them can reach a Git push, which is built from the mirror). Accepting a
// suggestion is an edit of the document through its save seam, so that does wake it, and is
// mirrored like any save.
func TestReviewDataIsNeverMirrored(t *testing.T) {
	e := newEnv(t)
	e.start()
	pid := e.project("Reviewed")
	d := e.doc(pid, "Chapter", "chapter", "The quiet house stood alone on the hill.\n")
	e.settleAll()
	call := func(op string, in any) any { return e.call(e.owner, op, in) }
	th := call("comment.create", review.CreateIn{ProjectID: pid, DocumentID: d.ID, Quote: "quiet house", Body: "REVIEWSECRET comment text"}).(review.ThreadItem).Item
	call("comment.create", review.CreateIn{ProjectID: pid, DocumentID: d.ID, ParentID: th.ID, Body: "REVIEWSECRET reply"})
	call("comment.resolve", review.GetIn{ProjectID: pid, CommentID: th.ID})
	call("comment.reopen", review.GetIn{ProjectID: pid, CommentID: th.ID})
	call("review.resolve_all", review.ResolveAllIn{ProjectID: pid})
	call("share.create", share.CreateIn{ProjectID: pid, Label: "REVIEWSECRET link", Scope: "comment"})
	sg := call("suggestion.create", review.SuggestionCreateIn{ProjectID: pid, DocumentID: d.ID, BaseRevision: e.get(d.ID).Revision,
		Changes: []review.ChangeIn{{Quote: "stood alone", Text: "stood REVIEWSECRET alone"}}, Note: "REVIEWSECRET why"}).(review.SuggestionItem).Item
	time.Sleep(50 * time.Millisecond)
	e.caughtUp()
	if e.m.Status(pid).Pending {
		t.Error("a comment, a suggestion or a share link woke the mirror")
	}
	walk := func(what string) {
		t.Helper()
		err := filepath.WalkDir(filepath.Join(e.dir, "mirror"), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, rerr := os.ReadFile(p)
			if rerr == nil && (strings.Contains(string(b), "REVIEWSECRET") || strings.Contains(string(b), "shl_") || strings.Contains(string(b), "cmt_")) {
				t.Errorf("%s: %s holds review data", what, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	walk("after the review ops")
	// accepting a suggestion is a save of the document: it reaches the mirror as text, and only as text
	call("suggestion.reject", review.SuggestionRef{ProjectID: pid, SuggestionID: sg.ID})
	sg2 := call("suggestion.create", review.SuggestionCreateIn{ProjectID: pid, DocumentID: d.ID, BaseRevision: e.get(d.ID).Revision,
		Changes: []review.ChangeIn{{Quote: "stood alone", Text: "stood silent"}}}).(review.SuggestionItem).Item
	call("suggestion.accept", review.SuggestionAcceptIn{ProjectID: pid, SuggestionID: sg2.ID})
	e.settle(pid)
	found := false
	_ = filepath.WalkDir(filepath.Join(e.dir, "mirror"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "stood silent") {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Error("an accepted suggestion did not reach the mirror")
	}
	walk("after accepting")
}

func (e *env) get(id string) docs.Doc {
	e.t.Helper()
	return e.call(e.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
}

// Every op of the review layer that changes data is classified: either it keeps something the
// projection does not show (and its events are ignored by the mirror), or it is an edit of the
// document through the save seam (and the doc.updated event is what wakes the mirror). A new one
// fails here until someone has said which.
func TestEveryReviewOpIsClassifiedForTheMirror(t *testing.T) {
	e := newEnv(t)
	exempt := map[string]string{
		"comment.create": "review data", "comment.update": "review data", "comment.delete": "review data", "comment.resolve": "review data",
		"comment.reopen": "review data", "comment.reanchor": "review data", "review.resolve_all": "review data",
		"suggestion.create": "review data", "suggestion.reject": "review data", "suggestion.withdraw": "review data",
		"share.create": "access, not content", "share.update": "access, not content", "share.revoke": "access, not content",
		"notification.mark_read": "per person", "notification.read_all": "per person",
		// edits of the document through its save seam: doc.updated wakes the mirror
		"suggestion.accept": "saves the document", "suggestion.accept_all": "saves the document",
	}
	n := 0
	for _, en := range e.reg.Entries() {
		name := en.Op.Name
		if !(strings.HasPrefix(name, "comment.") || strings.HasPrefix(name, "suggestion.") || strings.HasPrefix(name, "share.") ||
			strings.HasPrefix(name, "review.") || strings.HasPrefix(name, "notification.")) || en.Op.Method == "GET" {
			continue
		}
		n++
		if exempt[name] == "" {
			t.Errorf("%s changes data but is not classified for the mirror", name)
		}
	}
	if n != len(exempt) {
		t.Errorf("%d review ops change data, the table has %d", n, len(exempt))
	}
}

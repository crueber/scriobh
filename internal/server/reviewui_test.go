package server

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/web"
)

// postForm sends a UI form as a signed-in caller, with the session's own CSRF token unless the values name another.
func (s *Server) postForm(t *testing.T, c caller, path string, vals url.Values) *httptest.ResponseRecorder {
	t.Helper()
	own := url.Values{}
	for k, v := range vals {
		own[k] = v
	}
	vals = own
	if c.cookie != nil && vals.Get("_csrf") == "" {
		vals.Set("_csrf", s.Sess.CSRFToken(sessionOf(t, s, c)))
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(vals.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// callAs is an op as a person, for setting a scene up.
func (w *shareWorld) callAs(t *testing.T, c caller, op string, in any) any {
	t.Helper()
	role := ops.RoleWriter
	if c.name == "reviewer" {
		role = ops.RoleReader
	}
	out, err := w.s.Reg.Call(context.Background(), ops.Principal{UserID: c.userID, Role: role}, op, in)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func TestReviewPageIsForEditorsAndTheOwnerAndNobodyElseSeesIt(t *testing.T) {
	w := newShareWorld(t, nil)
	reader := w.s.newCaller(t, "reader", ops.RoleReader)
	w.s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	page := "/p/shared-book/review"
	for _, c := range []struct {
		who  caller
		want int
	}{{w.owner, 200}, {w.editor, 200}, {w.reviewer, 303}, {reader, 403}, {w.stranger, 404}, {caller{name: "anonymous"}, 303}} {
		rec := w.s.do(c.who, "GET", page, "")
		if rec.Code != c.want {
			t.Errorf("%s opens the review page: %d, want %d", c.who.name, rec.Code, c.want)
		}
	}
	if loc := w.s.do(w.reviewer, "GET", page, "").Header().Get("Location"); loc != "/p/shared-book/read" {
		t.Errorf("a reviewer is sent to %q", loc)
	}
	// the page of a stranger is the page of a project that does not exist
	a, b := w.s.do(w.stranger, "GET", page, ""), w.s.do(w.stranger, "GET", "/p/no-such-book/review", "")
	if a.Code != 404 || a.Body.String() != b.Body.String() {
		t.Errorf("a stranger learns something: %d %q", a.Code, a.Body)
	}
	if rec := w.s.do(w.owner, "GET", page, ""); strings.Contains(rec.Body.String(), "olive@t.local") || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("review page headers or content: %v", rec.Header())
	}
	// the project in the trash has no review page
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/trash", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := w.s.do(w.owner, "GET", page, ""); rec.Code != 404 {
		t.Errorf("a trashed project's review page: %d", rec.Code)
	}
}

func TestReviewFormsDoWhatTheOpsAllow(t *testing.T) {
	w := newShareWorld(t, nil)
	thread := func(quote string) string {
		return w.callAs(t, w.reviewer, "comment.create", review.CreateIn{ProjectID: w.pid, DocumentID: w.doc1, Quote: quote, Plain: true, Body: "Look at " + quote}).(review.ThreadItem).Item.ThreadID
	}
	t1, t2 := thread("quiet house"), thread("last paragraph")
	base := "/ui/projects/" + w.pid + "/review/"
	status := func(id string) string {
		return w.callAs(t, w.owner, "comment.get", review.GetIn{ProjectID: w.pid, CommentID: id}).(review.ThreadItem).Item.Status
	}
	// no token: refused; a stranger: the project does not exist; a reviewer: refused by the op, nothing changes
	if rec := w.s.postForm(t, w.owner, base+"resolve", url.Values{"thread_id": {t1}, "_csrf": {"nope"}}); rec.Code != 403 {
		t.Errorf("a bad token: %d", rec.Code)
	}
	if rec := w.s.postForm(t, w.stranger, base+"resolve", url.Values{"thread_id": {t1}}); rec.Code != 404 {
		t.Errorf("a stranger resolves: %d", rec.Code)
	}
	if rec := w.s.postForm(t, caller{name: "anonymous"}, base+"resolve", url.Values{"thread_id": {t1}}); rec.Code != 303 && rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
	w.s.postForm(t, w.reviewer, base+"resolve_all", url.Values{"step": {"apply"}, "expect_count": {"2"}})
	if status(t1) != "open" || status(t2) != "open" {
		t.Fatal("a reviewer resolved through the writer's form")
	}
	// the editor resolves one
	if rec := w.s.postForm(t, w.editor, base+"resolve", url.Values{"thread_id": {t1}}); rec.Code != 303 || status(t1) != "resolved" {
		t.Errorf("an editor resolves: %d %s", rec.Code, status(t1))
	}
	w.s.postForm(t, w.editor, base+"reopen", url.Values{"thread_id": {t1}})
	if status(t1) != "open" {
		t.Error("reopen")
	}
	// resolve all: a preview says how many and changes nothing; the wrong count changes nothing; the right one does it
	rec := w.s.postForm(t, w.owner, base+"resolve_all", url.Values{"step": {"preview"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "resolves 2 comments") || status(t1) != "open" {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `name="expect_count" value="2"`) {
		t.Error("the confirmation does not carry the count")
	}
	w.s.postForm(t, w.owner, base+"resolve_all", url.Values{"step": {"apply"}, "expect_count": {"5"}})
	if status(t1) != "open" {
		t.Error("a count that does not match resolved comments")
	}
	if rec := w.s.postForm(t, w.owner, base+"resolve_all", url.Values{"step": {"apply"}, "expect_count": {"2"}}); rec.Code != 303 || status(t1) != "resolved" || status(t2) != "resolved" {
		t.Errorf("apply: %d", rec.Code)
	}
	if rec := w.s.postForm(t, w.owner, base+"resolve_all", url.Values{"step": {"apply"}, "expect_count": {"no"}}); rec.Code != 422 {
		t.Errorf("a count that is not a number: %d", rec.Code)
	}

	// suggestions: a reviewer proposes, only an editor decides, the text moves only on accept
	d := w.callAs(t, w.owner, "doc.get", docs.DocIn{ID: w.doc2}).(docs.Item).Item
	mk := func(quote, text string) string {
		return w.callAs(t, w.reviewer, "suggestion.create", review.SuggestionCreateIn{ProjectID: w.pid, DocumentID: w.doc2, BaseRevision: d.Revision, Plain: true, Changes: []review.ChangeIn{{Quote: quote, Text: text}}}).(review.SuggestionItem).Item.ID
	}
	s1 := mk("plain and open", "plain and calm")
	page := w.s.do(w.owner, "GET", "/p/shared-book/review", "")
	if !strings.Contains(page.Body.String(), "plain and calm") || !strings.Contains(page.Body.String(), s1) {
		t.Error("the suggestion is not listed")
	}
	w.s.postForm(t, w.reviewer, base+"accept", url.Values{"suggestion_id": {s1}})
	if got := w.callAs(t, w.owner, "doc.get", docs.DocIn{ID: w.doc2}).(docs.Item).Item.Content; strings.Contains(got, "calm") {
		t.Fatal("a reviewer accepted a suggestion")
	}
	plan := w.s.postForm(t, w.owner, base+"accept_all", url.Values{"step": {"preview"}, "document_id": {w.doc2}, "suggestion_id": {s1}})
	if plan.Code != 200 || !strings.Contains(plan.Body.String(), "1 of 1 would be applied") {
		t.Fatalf("the plan: %d %s", plan.Code, plan.Body)
	}
	if got := w.callAs(t, w.owner, "doc.get", docs.DocIn{ID: w.doc2}).(docs.Item).Item.Content; strings.Contains(got, "calm") {
		t.Fatal("a plan changed the text")
	}
	if rec := w.s.postForm(t, w.editor, base+"accept", url.Values{"suggestion_id": {s1}}); rec.Code != 303 {
		t.Errorf("accept: %d", rec.Code)
	}
	if got := w.callAs(t, w.owner, "doc.get", docs.DocIn{ID: w.doc2}).(docs.Item).Item.Content; !strings.Contains(got, "plain and calm") {
		t.Errorf("the accepted text: %q", got)
	}
}

func TestTheReadingViewOffersReviewToReviewersAndUpOnly(t *testing.T) {
	w := newShareWorld(t, nil)
	reader := w.s.newCaller(t, "reader", ops.RoleReader)
	w.s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	url := "/p/shared-book/read/" + w.doc1
	for _, c := range []struct {
		who    caller
		margin bool
		decide bool
	}{{w.owner, true, true}, {w.editor, true, true}, {w.reviewer, true, false}, {reader, false, false}} {
		rec := w.s.do(c.who, "GET", url, "")
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", c.who.name, rec.Code)
		}
		if got := strings.Contains(body, `id="rv-margin"`); got != c.margin {
			t.Errorf("%s: margin %v, want %v", c.who.name, got, c.margin)
		}
		if got := strings.Contains(body, `data-decide="1"`); got != c.decide {
			t.Errorf("%s: decide %v, want %v", c.who.name, got, c.decide)
		}
		if strings.Contains(body, "GHOSTWORDS") || strings.Contains(body, "SECRETNOTE") || strings.Contains(body, "scriobh-ghost") {
			t.Errorf("%s sees what the book hides", c.who.name)
		}
		if !strings.Contains(body, `data-doc="`+w.doc1+`" data-rev="`) {
			t.Errorf("%s: the page does not carry the document and its revision", c.who.name)
		}
		if c.margin && !strings.Contains(body, "/static/js/review.js") && !strings.Contains(body, "review.") {
			t.Errorf("%s: no review script", c.who.name)
		}
		if !c.margin && strings.Contains(body, "review.js") {
			t.Errorf("%s: a reader is sent the review script", c.who.name)
		}
	}
}

// A Word author is shown with its origin, whatever its name: a name that is a
// member's, "Owner" or "Guest: Bob" never renders as a bare account name.
func TestAWordAuthorIsNeverShownAsABareName(t *testing.T) {
	for _, name := range []string{"Ann Editor", "Owner", "Guest: Bob", "admin"} {
		got := authorShown("en", review.AuthorOut{Kind: "label", Name: name})
		if got == name || !strings.Contains(got, name) || !strings.Contains(got, "Word") {
			t.Errorf("%q is shown as %q", name, got)
		}
	}
	if got := authorShown("en", review.AuthorOut{Kind: "user", UserID: "usr_1", Name: "Ann"}); got != "Ann" {
		t.Errorf("a member is shown as %q", got)
	}
	// the two scripts that draw the lists go through the same helper
	for f, want := range map[string]string{"js/review.js": `a.kind === "label"`, "js/suggest.js": "R.who(s.author)"} {
		b, err := fs.ReadFile(web.FS, "static/"+f)
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s does not mark a Word author (%v)", f, err)
		}
	}
}

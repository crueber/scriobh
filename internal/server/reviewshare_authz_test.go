package server

import (
	"net/url"
	"regexp"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// An instance administrator who is not on a project is a stranger to its review and share pages and to the forms
// behind them (the same rule as every other page: no manuscript text, no link, no count), and a project in the trash
// has neither page for anyone, owner included.
func TestReviewAndSharePagesKeepOutANonMemberAdministratorAndATrashedProject(t *testing.T) {
	w := newShareWorld(t, nil)
	admin := w.s.newCaller(t, "admin", ops.RoleAdmin)
	pages := []string{"/p/shared-book/review", "/p/shared-book/share", "/p/shared-book/read", "/p/shared-book/read/" + w.doc1}
	forms := map[string]url.Values{
		"/ui/projects/" + w.pid + "/review/resolve":     {"thread_id": {"x"}},
		"/ui/projects/" + w.pid + "/review/resolve_all": {"step": {"preview"}},
		"/ui/projects/" + w.pid + "/review/accept_all":  {"step": {"preview"}, "document_id": {w.doc1}},
		"/ui/projects/" + w.pid + "/share/create":       {"scope": {"read"}},
		"/ui/projects/" + w.pid + "/share/revoke":       {"share_id": {"x"}},
	}
	for _, p := range pages {
		if rec := w.s.do(admin, "GET", p, ""); rec.Code != 404 {
			t.Errorf("a non-member administrator opened %s: %d", p, rec.Code)
		}
	}
	for p, v := range forms {
		if rec := w.s.postForm(t, admin, p, v); rec.Code != 404 {
			t.Errorf("a non-member administrator posted to %s: %d", p, rec.Code)
		}
	}
	// the project is trashed: nobody has these pages, and a form answers like a project that does not exist
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/trash", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	for _, p := range pages {
		if rec := w.s.do(w.owner, "GET", p, ""); rec.Code != 404 {
			t.Errorf("the owner opened %s of a trashed project: %d", p, rec.Code)
		}
	}
	for p, v := range forms {
		if rec := w.s.postForm(t, w.owner, p, v); rec.Code != 404 {
			t.Errorf("the owner posted to %s of a trashed project: %d %s", p, rec.Code, regexp.MustCompile("(?s)<main.*</main>").FindString(rec.Body.String()))
		}
	}
}

package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// An instance administrator acts as the owner of every project through the ops (the API and the CLI). On the pages
// they are a stranger to what was not shared with them: the slug pages resolve only the projects they are a member of,
// and the id routes (/ui/projects/{id}/..., fragments, forms and files) answer the same 404, so no manuscript or
// bible text is shown to someone it was not shared with. Pinned here (docs/OPEN-QUESTIONS.md, "Phase 3 screens").
func TestAdminWithoutMembershipIsAStrangerOnThePagesAndAnOwnerInTheOps(t *testing.T) {
	e := newWriteEnv(t)
	admin := e.s.newCaller(t, "admin", ops.RoleAdmin)
	for _, p := range []string{"/p/" + e.slug, "/p/" + e.slug + "/outline", "/p/" + e.slug + "/goals", "/p/" + e.slug + "/git", "/p/" + e.slug + "/ai"} {
		if rec := e.s.do(admin, "GET", p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("a non-member administrator opened %s by slug: %d", p, rec.Code)
		}
	}
	for _, p := range []string{"/ui/projects/" + e.pid + "/tree", "/ui/projects/" + e.pid + "/outline", "/ui/docs/" + e.chap + "/move"} {
		if rec := e.s.do(admin, "GET", p, ""); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), `role="tree"`) {
			t.Errorf("a non-member administrator read %s by id: %d", p, rec.Code)
		}
	}
	// The op layer is unchanged: an administrator acts as the owner through the API.
	if rec := e.s.do(admin, "GET", "/api/v1/projects/"+e.pid+"/docs", ""); rec.Code != http.StatusOK {
		t.Errorf("an administrator through the API: %d", rec.Code)
	}
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	if rec := e.s.do(stranger, "GET", "/ui/projects/"+e.pid+"/tree", ""); rec.Code != http.StatusNotFound {
		t.Errorf("an ordinary stranger read the tree fragment: %d", rec.Code)
	}
}

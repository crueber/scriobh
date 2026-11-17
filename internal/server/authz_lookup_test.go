package server

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Adding a person by email must not tell anyone who is on the instance. A
// preview of an add used to answer with the found person's name, email and role
// while a missing address was a 404; both previews are now refused the same way.
// And lookups are limited per person, so adds cannot be run down a list of
// addresses.
func TestAddByEmailIsNotAnOracle(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	testutil.User(t, s.DB, ops.RoleWriter, "someone@test.local")
	pid := idOf(t, s.do(w, "POST", "/api/v1/projects", `{"title":"Book"}`).Body.Bytes())
	cid := idOf(t, s.do(w, "POST", "/api/v1/compendiums", `{"name":"World"}`).Body.Bytes())
	adds := map[string]string{
		"project":    "/api/v1/projects/" + pid + "/members",
		"compendium": "/api/v1/compendiums/" + cid + "/members",
	}
	for what, path := range adds {
		known := s.doDry(w, "POST", path, `{"email":"someone@test.local","role":"reader"}`)
		unknown := s.doDry(w, "POST", path, `{"email":"nobody@test.local","role":"reader"}`)
		if known.Code != 422 || unknown.Code != 422 || known.Body.String() != unknown.Body.String() || !strings.Contains(known.Body.String(), "dry_run_unsupported") {
			t.Errorf("%s add: a preview for a known address %d %s, for an unknown one %d %s; both must be the same refusal",
				what, known.Code, known.Body, unknown.Code, unknown.Body)
		}
		if strings.Contains(known.Body.String(), "someone@") {
			t.Errorf("%s add preview echoes the address: %s", what, known.Body)
		}
	}
	// The real thing: twenty lookups, then the limit. The lookups count whether or not the address exists.
	for what, path := range adds {
		who := s.newCaller(t, "limited-"+what, ops.RoleWriter)
		var own string
		if what == "project" {
			own = idOf(t, s.do(who, "POST", "/api/v1/projects", `{"title":"Mine"}`).Body.Bytes())
			path = "/api/v1/projects/" + own + "/members"
		} else {
			own = idOf(t, s.do(who, "POST", "/api/v1/compendiums", `{"name":"Mine"}`).Body.Bytes())
			path = "/api/v1/compendiums/" + own + "/members"
		}
		for i := 0; i < 20; i++ {
			rec := s.do(who, "POST", path, fmt.Sprintf(`{"email":"guess%d@test.local","role":"reader"}`, i))
			if rec.Code != 404 {
				t.Fatalf("%s lookup %d: %d, want 404: %s", what, i, rec.Code, rec.Body)
			}
		}
		rec := s.do(who, "POST", path, `{"email":"guess21@test.local","role":"reader"}`)
		if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
			t.Errorf("%s: the 21st lookup in a minute: %d (Retry-After %q), want 429", what, rec.Code, rec.Header().Get("Retry-After"))
		}
		// A different person is not held up by it.
		if rec := s.do(w, "POST", adds[what], `{"email":"someone@test.local","role":"reader"}`); rec.Code != 201 {
			t.Errorf("%s: another person's add: %d %s", what, rec.Code, rec.Body)
		}
	}
}

// whoami names a pinned key's project (and its Compendium) only while the key's
// owner can still open it: a person removed from a project keeps a key pinned to
// it, and must not learn the project's current title and slug from it.
func TestWhoamiDoesNotNameAProjectTheOwnerLeft(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	e := s.newCaller(t, "editor", ops.RoleWriter)
	pid := idOf(t, s.do(w, "POST", "/api/v1/projects", `{"title":"Original Title"}`).Body.Bytes())
	s.addMember(t, pid, e.userID, ops.ProjectEditor)
	key, _ := s.mintKey(t, e, `{"name":"k","project_id":"`+pid+`"}`)
	body := func() string { return s.doKey(key, "GET", "/api/v1/_whoami", "").Body.String() }
	if b := body(); !strings.Contains(b, "Original Title") {
		t.Fatalf("while a member: %s", b)
	}
	if rec := s.do(w, "PATCH", "/api/v1/projects/"+pid, `{"title":"Secret New Title"}`); rec.Code != 200 {
		t.Fatalf("rename: %d", rec.Code)
	}
	if _, err := s.DB.Writer().Exec(`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, pid, e.userID); err != nil {
		t.Fatal(err)
	}
	b := body()
	for _, leak := range []string{"Secret New Title", "Original Title", "secret-new-title", "original-title"} {
		if strings.Contains(b, leak) {
			t.Errorf("whoami for a key whose owner left the project names %q: %s", leak, b)
		}
	}
}

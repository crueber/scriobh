package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// TestProjectLifecycleOverREST drives update, trash, the purge confirmation and
// per-person pins through the generated REST routes, including DELETE with a body.
func TestProjectLifecycleOverREST(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Lifecycle"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	s.addMember(t, pid, editor.userID, ops.ProjectEditor)
	base := "/api/v1/projects/" + pid

	if rec := s.do(owner, "PATCH", base, `{"title":"Renamed","deadline":"2027-01-31"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"slug":"lifecycle"`) {
		t.Fatalf("patch: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(editor, "PATCH", base, `{"title":"Nope"}`); rec.Code != 403 {
		t.Errorf("editor patch: %d", rec.Code)
	}
	if rec := s.do(owner, "PATCH", base, `{"deadline":"tomorrow"}`); rec.Code != 422 {
		t.Errorf("bad deadline: %d", rec.Code)
	}
	if rec := s.do(editor, "POST", base+"/pin", `{"pinned":true}`); rec.Code != 200 {
		t.Fatalf("editor pin: %d %s", rec.Code, rec.Body)
	}
	var got struct{ Items []projects.Project }
	rec := s.do(owner, "GET", "/api/v1/projects", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got.Items) != 1 || got.Items[0].Pinned || got.Items[0].Title != "Renamed" {
		t.Errorf("owner list: %s", rec.Body)
	}
	if rec := s.do(owner, "DELETE", base, `{"confirm_title":"Renamed"}`); rec.Code != 409 {
		t.Errorf("purge of a live project: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "POST", base+"/trash", ""); rec.Code != 200 {
		t.Fatalf("trash: %d", rec.Code)
	}
	if rec := s.do(owner, "GET", "/api/v1/projects?state=bogus", ""); rec.Code != 422 {
		t.Errorf("bad state: %d", rec.Code)
	}
	if rec := s.do(owner, "GET", "/api/v1/projects?state=trashed", ""); !strings.Contains(rec.Body.String(), `"trashed":true`) {
		t.Errorf("trash list: %s", rec.Body)
	}
	if rec := s.do(owner, "DELETE", base, `{"confirm_title":"wrong"}`); rec.Code != 422 {
		t.Errorf("purge with the wrong title: %d", rec.Code)
	}
	if rec := s.do(owner, "DELETE", base, `{"confirm_title":"Renamed"}`); rec.Code != 200 {
		t.Errorf("purge: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "GET", base, ""); rec.Code != 404 {
		t.Errorf("get after purge: %d", rec.Code)
	}
}

// TestSessionOpsOverREST checks the session list against real cookie sessions:
// the current one is marked, another person's id is a 404, and revoking the
// current session ends it.
func TestSessionOpsOverREST(t *testing.T) {
	s := newAuthzServer(t)
	ada := s.newCaller(t, "ada", ops.RoleWriter)
	bob := s.newCaller(t, "bob", ops.RoleWriter)
	tok2, _, err := s.Sess.Create(context.Background(), ada.userID, "Second Browser", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	var list struct {
		Items []struct {
			ID      string `json:"id"`
			Current bool   `json:"current"`
		} `json:"items"`
	}
	rec := s.do(ada, "GET", "/api/v1/me/sessions", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 2 {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	var current, other string
	for _, it := range list.Items {
		if it.Current {
			current = it.ID
		} else {
			other = it.ID
		}
	}
	if current == "" || other == "" {
		t.Fatalf("current %q other %q", current, other)
	}
	if strings.Contains(rec.Body.String(), "token") || strings.Contains(rec.Body.String(), tok2) {
		t.Error("the list leaks session secrets")
	}
	// Bob cannot revoke Ada's session, and it is a plain 404.
	if rec := s.do(bob, "POST", "/api/v1/me/sessions/"+other+"/revoke", ""); rec.Code != 404 {
		t.Errorf("bob revoking ada's session: %d", rec.Code)
	}
	if rec := s.do(ada, "POST", "/api/v1/me/sessions/"+other+"/revoke", ""); rec.Code != 200 {
		t.Errorf("revoke other: %d %s", rec.Code, rec.Body)
	}
	// The revoked cookie no longer signs in.
	if rec := s.do(caller{name: "second", cookie: &http.Cookie{Name: auth.SessionCookie, Value: tok2}}, "GET", "/api/v1/me", ""); rec.Code != 401 {
		t.Errorf("revoked session still works: %d", rec.Code)
	}
	if rec := s.do(ada, "POST", "/api/v1/me/sessions/revoke-others", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"revoked":0`) {
		t.Errorf("revoke-others: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(ada, "POST", "/api/v1/me/sessions/"+current+"/revoke", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"current":true`) {
		t.Errorf("revoke current: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(ada, "GET", "/api/v1/me", ""); rec.Code != 401 {
		t.Errorf("the current session survived its own revocation: %d", rec.Code)
	}
	// Preferences over PUT.
	if rec := s.do(bob, "PUT", "/api/v1/me/prefs", `{"items":{"theme":"dark","editor_size":"21"}}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"editor_size":"21"`) {
		t.Errorf("prefs put: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(bob, "PUT", "/api/v1/me/prefs", `{"items":{"theme":"neon"}}`); rec.Code != 422 || !strings.Contains(rec.Body.String(), `"theme"`) {
		t.Errorf("prefs bad value: %d %s", rec.Code, rec.Body)
	}
}

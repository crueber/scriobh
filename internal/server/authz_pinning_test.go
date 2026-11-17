package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func ptr[T any](v T) *T { return &v }

func idOf(t *testing.T, body []byte) string {
	t.Helper()
	var out struct{ Item struct{ ID string } }
	if err := json.Unmarshal(body, &out); err != nil || out.Item.ID == "" {
		t.Fatalf("no item id in %s", body)
	}
	return out.Item.ID
}

// TestAPIKeyPinsThroughRealKeys: keys made with key.create and sent as Bearer
// tokens over REST.
//
//   - A key pinned to one Compendium reaches that Compendium's ops and nothing
//     else: no other Compendium, no project of its owner, nothing instance-wide.
//   - A key pinned to one project reaches that project and its primary
//     Compendium (and only that one), and cannot change which Compendiums the
//     project uses.
//   - A key never does more than its owner, and its scope narrows it further.
func TestAPIKeyPinsThroughRealKeys(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	create := func(path, body string) []byte {
		t.Helper()
		rec := s.do(w, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return rec.Body.Bytes()
	}
	c1 := idOf(t, create("/api/v1/compendiums", `{"name":"One"}`))
	c2 := idOf(t, create("/api/v1/compendiums", `{"name":"Two"}`))
	e1 := idOf(t, create("/api/v1/compendiums/"+c1+"/entities", `{"kind":"character","name":"Ada"}`))
	e2 := idOf(t, create("/api/v1/compendiums/"+c2+"/entities", `{"kind":"character","name":"Bea"}`))
	pid := idOf(t, create("/api/v1/projects", `{"title":"P"}`))
	qid := idOf(t, create("/api/v1/projects", `{"title":"Q"}`))
	if rec := s.do(w, "PUT", "/api/v1/projects/"+pid+"/compendium", `{"compendium_id":"`+c1+`"}`); rec.Code != 200 {
		t.Fatalf("set the primary: %d %s", rec.Code, rec.Body)
	}

	// Someone cannot pin a key to a Compendium they cannot read, and the answer is the same as for a missing one.
	if rec := s.do(stranger, "POST", "/api/v1/keys", `{"name":"x","compendium_id":"`+c1+`"}`); rec.Code != 404 {
		t.Errorf("pinning to someone else's Compendium: %d, want 404", rec.Code)
	}
	if rec := s.do(stranger, "POST", "/api/v1/keys", `{"name":"x","compendium_id":"cmp_00000000000000000000000000"}`); rec.Code != 404 {
		t.Errorf("pinning to a missing Compendium: %d, want 404", rec.Code)
	}
	// A key cannot carry both pins.
	if rec := s.do(w, "POST", "/api/v1/keys", `{"name":"x","compendium_id":"`+c1+`","project_id":"`+pid+`"}`); rec.Code != 422 {
		t.Errorf("both pins: %d, want 422", rec.Code)
	}

	pinC, _ := s.mintKey(t, w, `{"name":"compendium key","compendium_id":"`+c1+`"}`)
	readC, _ := s.mintKey(t, w, `{"name":"read compendium key","scope":"read","compendium_id":"`+c1+`"}`)
	pinP, _ := s.mintKey(t, w, `{"name":"project key","project_id":"`+pid+`"}`)
	status := func(secret, method, path, body string) int { return s.doKey(secret, method, path, body).Code }
	check := func(who string, secret string, want int, cases map[string][3]string) {
		t.Helper()
		for name, c := range cases {
			if got := status(secret, c[0], c[1], c[2]); got != want {
				t.Errorf("%s key, %s: %s %s = %d, want %d", who, name, c[0], c[1], got, want)
			}
		}
	}

	// The Compendium-pinned key: its own Compendium in full.
	check("compendium", pinC, 200, map[string][3]string{
		"get":    {"GET", "/api/v1/compendiums/" + c1, ""},
		"list":   {"GET", "/api/v1/compendiums/" + c1 + "/entities", ""},
		"entity": {"GET", "/api/v1/entities/" + e1, ""},
		"search": {"GET", "/api/v1/compendiums/" + c1 + "/search?q=ada", ""},
		"fields": {"GET", "/api/v1/compendiums/" + c1 + "/entity-fields", ""},
		"export": {"POST", "/api/v1/compendiums/" + c1 + "/export", ""},
	})
	if got := status(pinC, "POST", "/api/v1/compendiums/"+c1+"/entities", `{"kind":"place","name":"Quay"}`); got != 201 {
		t.Errorf("a Compendium key creating an entity: %d", got)
	}
	// ...and nothing else, including every project of its owner.
	check("compendium", pinC, 404, map[string][3]string{
		"another Compendium":         {"GET", "/api/v1/compendiums/" + c2, ""},
		"another Compendium's list":  {"GET", "/api/v1/compendiums/" + c2 + "/entities", ""},
		"another Compendium's thing": {"GET", "/api/v1/entities/" + e2, ""},
		"writing another's thing":    {"PATCH", "/api/v1/entities/" + e2, `{"summary":"x"}`},
		"another search":             {"GET", "/api/v1/compendiums/" + c2 + "/search?q=bea", ""},
		"the project that uses it":   {"GET", "/api/v1/projects/" + pid, ""},
		"that project's entities":    {"GET", "/api/v1/projects/" + pid + "/entities", ""},
		"that project's documents":   {"GET", "/api/v1/projects/" + pid + "/docs", ""},
		"that project's search":      {"GET", "/api/v1/projects/" + pid + "/search?q=x", ""},
		"another project":            {"GET", "/api/v1/projects/" + qid, ""},
		"its project's members":      {"GET", "/api/v1/projects/" + pid + "/members", ""},
	})
	check("compendium", pinC, 403, map[string][3]string{
		"project.list":      {"GET", "/api/v1/projects", ""},
		"compendium.list":   {"GET", "/api/v1/compendiums", ""},
		"compendium.create": {"POST", "/api/v1/compendiums", `{"name":"New"}`},
		"project.create":    {"POST", "/api/v1/projects", `{"title":"New"}`},
		"sessions":          {"GET", "/api/v1/me/sessions", ""},
		"a reference":       {"POST", "/api/v1/compendiums/" + c1 + "/refs", `{"target_id":"` + c2 + `"}`},
	})
	// The scope of a key still applies to its own Compendium.
	if got := status(readC, "GET", "/api/v1/compendiums/"+c1+"/entities", ""); got != 200 {
		t.Errorf("a read key listing: %d", got)
	}
	if got := status(readC, "POST", "/api/v1/compendiums/"+c1+"/entities", `{"kind":"item","name":"No"}`); got != 403 {
		t.Errorf("a read key creating an entity: %d, want 403", got)
	}

	// The project-pinned key: its project and that project's primary Compendium.
	check("project", pinP, 200, map[string][3]string{
		"the project":           {"GET", "/api/v1/projects/" + pid, ""},
		"its documents":         {"GET", "/api/v1/projects/" + pid + "/docs", ""},
		"its entities":          {"GET", "/api/v1/projects/" + pid + "/entities", ""},
		"its Compendium":        {"GET", "/api/v1/compendiums/" + c1, ""},
		"its Compendium's list": {"GET", "/api/v1/compendiums/" + c1 + "/entities", ""},
		"an entity of it":       {"GET", "/api/v1/entities/" + e1, ""},
		"search it":             {"GET", "/api/v1/compendiums/" + c1 + "/search?q=ada", ""},
		"the project's wiring":  {"GET", "/api/v1/projects/" + pid + "/compendium", ""},
	})
	if got := status(pinP, "POST", "/api/v1/compendiums/"+c1+"/entities", `{"kind":"place","name":"Harbour"}`); got != 201 {
		t.Errorf("a project key adding an entity to its project's Compendium: %d", got)
	}
	if got := status(pinP, "PUT", "/api/v1/projects/"+pid+"/entity-overrides", `{"entity_id":"`+e1+`","name":"Ada Q"}`); got != 200 {
		t.Errorf("a project key overriding an entity: %d", got)
	}
	check("project", pinP, 404, map[string][3]string{
		"another Compendium":        {"GET", "/api/v1/compendiums/" + c2, ""},
		"another Compendium's list": {"GET", "/api/v1/compendiums/" + c2 + "/entities", ""},
		"another's entity":          {"GET", "/api/v1/entities/" + e2, ""},
		"writing another's":         {"PATCH", "/api/v1/entities/" + e2, `{"summary":"x"}`},
		"another project":           {"GET", "/api/v1/projects/" + qid, ""},
		"another project's list":    {"GET", "/api/v1/projects/" + qid + "/entities", ""},
	})
	// It cannot change which Compendiums the project uses, nor grant anyone the bible.
	check("project", pinP, 403, map[string][3]string{
		"set the primary":   {"PUT", "/api/v1/projects/" + pid + "/compendium", `{"compendium_id":"` + c2 + `"}`},
		"add a reference":   {"POST", "/api/v1/projects/" + pid + "/compendium/refs", `{"compendium_id":"` + c2 + `"}`},
		"link an entity":    {"POST", "/api/v1/projects/" + pid + "/entity-links", `{"entity_id":"` + e2 + `"}`},
		"compendium.create": {"POST", "/api/v1/compendiums", `{"name":"New","project_id":"` + qid + `"}`},
		"project.list":      {"GET", "/api/v1/projects", ""},
	})
	// A project key fills its Compendium; it does not own it, and it cannot read others through it.
	check("project", pinP, 403, map[string][3]string{
		"rename":             {"PATCH", "/api/v1/compendiums/" + c1, `{"name":"Mine"}`},
		"archive":            {"POST", "/api/v1/compendiums/" + c1 + "/archive", ""},
		"trash":              {"POST", "/api/v1/compendiums/" + c1 + "/trash", ""},
		"restore":            {"POST", "/api/v1/compendiums/" + c1 + "/restore", ""},
		"purge":              {"DELETE", "/api/v1/compendiums/" + c1, `{"confirm_name":"One"}`},
		"list members":       {"GET", "/api/v1/compendiums/" + c1 + "/members", ""},
		"add a member":       {"POST", "/api/v1/compendiums/" + c1 + "/members", `{"email":"stranger@test.local","role":"reader"}`},
		"read via reference": {"GET", "/api/v1/compendiums/" + c1 + "/referenced-entities", ""},
	})
	if got := status(pinC, "GET", "/api/v1/compendiums/"+c1+"/referenced-entities", ""); got != 403 {
		t.Errorf("a Compendium key reading through a reference: %d, want 403", got)
	}
	if got := status(pinC, "PATCH", "/api/v1/compendiums/"+c1, `{"description":"Edited by its own key."}`); got != 200 {
		t.Errorf("a key pinned to the Compendium editing it: %d", got)
	}
	// whoami names the Compendium only to someone who may see it: a project member who is
	// not on it mints a project key and learns nothing.
	off := s.newCaller(t, "off", ops.RoleWriter)
	s.addMember(t, pid, off.userID, ops.ProjectEditor)
	offKey, _ := s.mintKey(t, off, `{"name":"off key","project_id":"`+pid+`"}`)
	var offWho struct {
		Compendium *struct{ Name string }
		Project    *struct{ Title string }
	}
	_ = json.Unmarshal(s.doKey(offKey, "GET", "/api/v1/_whoami", "").Body.Bytes(), &offWho)
	if offWho.Compendium != nil || offWho.Project == nil {
		t.Errorf("whoami for a project key whose owner is off the Compendium: %+v", offWho)
	}
	if got := status(offKey, "GET", "/api/v1/compendiums/"+c1, ""); got != 404 {
		t.Errorf("that key reading the Compendium: %d, want 404", got)
	}
	// A project key whose project has no Compendium yet makes one with project.compendium.create.
	r := idOf(t, create("/api/v1/projects", `{"title":"Fresh"}`))
	fresh, _ := s.mintKey(t, w, `{"name":"fresh key","project_id":"`+r+`"}`)
	if got := status(fresh, "GET", "/api/v1/compendiums/"+c1, ""); got != 404 {
		t.Errorf("a key reaching a Compendium its project does not use: %d, want 404", got)
	}
	rec := s.doKey(fresh, "POST", "/api/v1/projects/"+r+"/compendium", `{"name":"Fresh bible"}`)
	if rec.Code != 201 {
		t.Fatalf("project.compendium.create with a project key: %d %s", rec.Code, rec.Body)
	}
	nc := idOf(t, rec.Body.Bytes())
	if got := status(fresh, "POST", "/api/v1/compendiums/"+nc+"/entities", `{"kind":"character","name":"First"}`); got != 201 {
		t.Errorf("the project key adding to the bible it just made: %d", got)
	}
	if got := status(fresh, "POST", "/api/v1/projects/"+r+"/compendium", `{"name":"Second bible"}`); got != 409 {
		t.Errorf("a second primary: %d, want 409", got)
	}
	// whoami says which Compendium the key works in.
	for secret, want := range map[string]string{pinC: c1, pinP: c1, fresh: nc} {
		var who struct{ Compendium struct{ ID, Name string } }
		_ = json.Unmarshal(s.doKey(secret, "GET", "/api/v1/_whoami", "").Body.Bytes(), &who)
		if who.Compendium.ID != want || who.Compendium.Name == "" {
			t.Errorf("whoami: %+v, want %s", who, want)
		}
	}
	// Revoking the key ends it.
	keys := s.do(w, "GET", "/api/v1/keys", "")
	var kl struct{ Items []struct{ ID, Name string } }
	_ = json.Unmarshal(keys.Body.Bytes(), &kl)
	for _, k := range kl.Items {
		if k.Name == "compendium key" {
			if rec := s.do(w, "POST", "/api/v1/keys/"+k.ID+"/revoke", ""); rec.Code != 200 {
				t.Fatalf("revoke: %d", rec.Code)
			}
		}
	}
	if got := status(pinC, "GET", "/api/v1/compendiums/"+c1, ""); got != http.StatusUnauthorized {
		t.Errorf("a revoked key: %d, want 401", got)
	}
}

// A project-pinned principal never grants Compendium access through the
// collaborator prompt (a key cannot reach admin scope as a writer, so this
// pins the rule at the principal).
func TestProjectPinnedPrincipalDoesNotGrantTheBible(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	uid := testutil.User(t, s.DB, ops.RoleWriter, "keyowner@test.local")
	person := ops.Principal{UserID: uid, Role: ops.RoleWriter}
	pid := func() string {
		out, err := s.Reg.Call(ctx, person, "project.create", projects.CreateIn{Title: "P"})
		if err != nil {
			t.Fatal(err)
		}
		return out.(projects.Item).Item.ID
	}()
	cid := func() string {
		out, err := s.Reg.Call(ctx, person, "compendium.create", compendiums.CreateIn{Name: "C", ProjectID: pid})
		if err != nil {
			t.Fatal(err)
		}
		return out.(compendiums.Item).Item.ID
	}()
	_ = cid
	testutil.User(t, s.DB, ops.RoleWriter, "collab@test.local")
	testutil.User(t, s.DB, ops.RoleWriter, "collab2@test.local")
	pin := ops.Principal{UserID: uid, Role: ops.RoleWriter, Via: "apikey", Scope: ops.ScopeAdmin, ProjectID: pid}
	if _, err := s.Reg.Call(ctx, pin, "project.member.add", projects.MemberAddIn{ID: pid, Email: "collab2@test.local", Role: "reader", AddToCompendium: ptr(true)}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("an explicit add_to_compendium from a project-pinned principal: %v", err)
	}
	out, err := s.Reg.Call(ctx, pin, "project.member.add", projects.MemberAddIn{ID: pid, Email: "collab@test.local", Role: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	if g := out.(projects.MemberAddOut).Compendium; g.Granted || g.Reason != "restricted_key" {
		t.Errorf("%+v", g)
	}
}

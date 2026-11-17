package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func (s *Server) doKeyed(c caller, method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Idempotency-Key", key)
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// The writing studio's writes over REST: an Idempotency-Key replays the same answer and writes once, and a dry run says
// what would happen and changes nothing.
func TestStudioWritesAreIdempotentAndPreviewable(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(t.Context(), p, "project.create", projects.CreateIn{Title: "Idem"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	count := func(table string) int {
		var n int
		_ = s.DB.Read().QueryRow(`SELECT count(*) FROM ` + table).Scan(&n)
		return n
	}
	body := `{"period":"week","measure":"words","target":1000}`
	first := s.doKeyed(owner, "POST", "/api/v1/me/studio/goals", body, "goal-key-1")
	again := s.doKeyed(owner, "POST", "/api/v1/me/studio/goals", body, "goal-key-1")
	if first.Code != 201 || again.Code != 201 || first.Body.String() != again.Body.String() || count("studio_goals") != 1 {
		t.Errorf("replay: %d %d goals %d\n%s\n%s", first.Code, again.Code, count("studio_goals"), first.Body, again.Body)
	}
	if other := s.doKeyed(owner, "POST", "/api/v1/me/studio/goals", `{"period":"week","measure":"words","target":2000}`, "goal-key-1"); other.Code < 400 {
		t.Errorf("the same key for another request: %d", other.Code)
	}
	var made struct{ Item struct{ ID string } }
	_ = json.Unmarshal(first.Body.Bytes(), &made)

	// A dry run of a change shows the result and writes nothing: no goal, no version.
	dry := s.doDry(owner, "POST", "/api/v1/me/studio/goals", `{"period":"month","measure":"words","target":9000}`)
	if dry.Code >= 400 || count("studio_goals") != 1 {
		t.Errorf("dry goal: %d, goals %d: %s", dry.Code, count("studio_goals"), dry.Body)
	}
	dry = s.doDry(owner, "PATCH", "/api/v1/me/studio/goals/"+made.Item.ID, `{"target":3000}`)
	if dry.Code >= 400 || count("studio_goal_versions") != 1 {
		t.Errorf("dry update: %d, versions %d: %s", dry.Code, count("studio_goal_versions"), dry.Body)
	}
	dry = s.doDry(owner, "POST", "/api/v1/projects/"+pid+"/studio/sprints", `{"minutes":25}`)
	if dry.Code >= 400 || count("studio_sprints") != 0 {
		t.Errorf("dry sprint: %d, sprints %d: %s", dry.Code, count("studio_sprints"), dry.Body)
	}
	// A sprint started with a key once is one sprint.
	a := s.doKeyed(owner, "POST", "/api/v1/projects/"+pid+"/studio/sprints", `{"minutes":25}`, "sprint-key-1")
	b := s.doKeyed(owner, "POST", "/api/v1/projects/"+pid+"/studio/sprints", `{"minutes":25}`, "sprint-key-1")
	if a.Code != 201 || b.Code != 201 || a.Body.String() != b.Body.String() || count("studio_sprints") != 1 {
		t.Errorf("sprint replay: %d %d sprints %d", a.Code, b.Code, count("studio_sprints"))
	}
	// Without a key a second start is a conflict, not a second sprint.
	if c := s.do(owner, "POST", "/api/v1/projects/"+pid+"/studio/sprints", `{"minutes":25}`); c.Code != 409 || count("studio_sprints") != 1 {
		t.Errorf("a second sprint: %d", c.Code)
	}
	// A card's answer holds the secret and is never kept for replay (a repeat with the same key is refused, and makes no
	// second card); a dry run of it is refused.
	if d := s.doDry(owner, "POST", "/api/v1/me/studio/cards", `{}`); d.Code != 422 && d.Code != 400 && d.Code != 409 {
		t.Errorf("a dry run of a card: %d", d.Code)
	}
	if count("studio_cards") != 0 {
		t.Error("a dry run made a card")
	}
	k1 := s.doKeyed(owner, "POST", "/api/v1/me/studio/cards", `{}`, "card-key")
	k2 := s.doKeyed(owner, "POST", "/api/v1/me/studio/cards", `{}`, "card-key")
	if k1.Code != 201 || k2.Code == 201 || k1.Body.String() == k2.Body.String() || count("studio_cards") != 1 {
		t.Errorf("a secret was replayed or made twice: %d %d cards %d", k1.Code, k2.Code, count("studio_cards"))
	}
}

// A key reads its owner's own numbers with the read scope, and nothing more than a session that has not unlocked: a project
// with a passphrase is left out for a key that was not approved for it. Writes need the write scope, a key pinned to one
// project reaches no person-wide number, and the card and the progress email are for a browser.
func TestStudioOverAPIKeys(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	call := func(op string, in any) any {
		out, err := s.Reg.Call(t.Context(), p, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	open := call("project.create", projects.CreateIn{Title: "Open"}).(projects.Item).Item.ID
	locked := call("project.create", projects.CreateIn{Title: "KEYCANARY locked"}).(projects.Item).Item.ID
	write := func(pid, words string) {
		did := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "T"}).(docs.Item).Item.ID
		doc := call("doc.get", docs.DocIn{ID: did}).(docs.Item).Item
		call("doc.update", docs.UpdateIn{ID: did, BaseRevision: doc.Revision, Content: &words})
	}
	write(open, strings.Repeat("alpha ", 30))
	write(locked, strings.Repeat("beta ", 70))
	readKey, _ := s.mintKey(t, owner, `{"name":"r","scope":"read"}`)
	writeKey, _ := s.mintKey(t, owner, `{"name":"w","scope":"write"}`)
	pinned, _ := s.mintKey(t, owner, `{"name":"p","scope":"read","project_id":"`+open+`"}`)

	words := func(rec *httptest.ResponseRecorder) int {
		var d struct{ Today struct{ Words int } }
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &d) != nil {
			t.Fatalf("dashboard: %d %.200s", rec.Code, rec.Body)
		}
		return d.Today.Words
	}
	if got := words(s.doKey(readKey, "GET", "/api/v1/me/studio", "")); got != 100 {
		t.Errorf("a read key before the passphrase: %d, want 100", got)
	}
	if rec := s.do(owner, "PUT", "/api/v1/projects/"+locked+"/passphrase", `{"passphrase":"a long enough one"}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d %s", rec.Code, rec.Body)
	}
	if got := words(s.doKey(readKey, "GET", "/api/v1/me/studio", "")); got != 30 {
		t.Errorf("a key not approved for a protected project counted its words: %d, want 30", got)
	}
	if rec := s.doKey(readKey, "GET", "/api/v1/me/studio", ""); strings.Contains(rec.Body.String(), "KEYCANARY") {
		t.Error("a protected project's title is in a key's dashboard")
	}
	if got := words(s.do(owner, "GET", "/api/v1/me/studio", "")); got != 100 {
		t.Errorf("the session that set the passphrase: %d, want 100", got)
	}
	// Scope: a read key reads and cannot write; a write key writes; a pinned key reaches nothing person-wide.
	for _, path := range []string{"/api/v1/me/studio", "/api/v1/me/studio/series", "/api/v1/me/studio/calendar", "/api/v1/me/studio/goals", "/api/v1/me/studio/sprints",
		"/api/v1/me/studio/export?table=daily"} {
		if rec := s.doKey(readKey, "GET", path, ""); rec.Code != 200 {
			t.Errorf("a read key at %s: %d", path, rec.Code)
		}
		if rec := s.doKey(pinned, "GET", path, ""); rec.Code != 403 {
			t.Errorf("a key pinned to a project at %s: %d, want 403", path, rec.Code)
		}
	}
	goal := `{"period":"day","measure":"words","target":50}`
	if rec := s.doKey(readKey, "POST", "/api/v1/me/studio/goals", goal); rec.Code != 403 {
		t.Errorf("a read key made a goal: %d", rec.Code)
	}
	if rec := s.doKey(writeKey, "POST", "/api/v1/me/studio/goals", goal); rec.Code != 201 {
		t.Errorf("a write key: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/me/studio/cards", `{}`}, {"GET", "/api/v1/me/studio/cards", ""},
		{"GET", "/api/v1/me/mail/progress", ""}, {"PUT", "/api/v1/me/mail/progress", `{"enabled":false}`},
	} {
		for _, k := range []string{readKey, writeKey} {
			if rec := s.doKey(k, c.method, c.path, c.body); rec.Code != 403 {
				t.Errorf("a key at %s %s: %d, want 403", c.method, c.path, rec.Code)
			}
		}
	}
	// A pinned key may use the project ops of its own project: its own totals (a project op), but not the locked one's.
	if rec := s.doKey(pinned, "GET", "/api/v1/projects/"+open+"/studio", ""); rec.Code != 200 {
		t.Errorf("a pinned key's own project totals: %d", rec.Code)
	}
	if rec := s.doKey(pinned, "GET", "/api/v1/projects/"+locked+"/studio", ""); rec.Code != 404 {
		t.Errorf("a pinned key at another project: %d, want 404", rec.Code)
	}
}

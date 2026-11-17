package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// pinWorld is a project with a primary Compendium, a referenced one and a
// linked entity of a third, owned by one person, plus a second project that
// shares the primary Compendium.
type pinWorld struct {
	s                     *Server
	w                     caller
	oneID, twoID, threeID string // Compendiums One (primary), Two (referenced), Three (one entity linked)
	ada, bea, cal         string // an entity in each
	proj, other           string // the pinned project, and another that shares One
	readP, writeP         string // keys pinned to proj
	readC, writeC         string // keys pinned to One
}

func newPinWorld(t *testing.T) *pinWorld {
	t.Helper()
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	pw := &pinWorld{s: s, w: w}
	create := func(path, body string) string {
		t.Helper()
		rec := s.do(w, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return idOf(t, rec.Body.Bytes())
	}
	put := func(path, body string) {
		t.Helper()
		if rec := s.do(w, "PUT", path, body); rec.Code >= 300 {
			t.Fatalf("PUT %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	pw.oneID = create("/api/v1/compendiums", `{"name":"One"}`)
	pw.twoID = create("/api/v1/compendiums", `{"name":"Two"}`)
	pw.threeID = create("/api/v1/compendiums", `{"name":"Three"}`)
	pw.ada = create("/api/v1/compendiums/"+pw.oneID+"/entities", `{"kind":"character","name":"Ada"}`)
	pw.bea = create("/api/v1/compendiums/"+pw.twoID+"/entities", `{"kind":"character","name":"Bea"}`)
	pw.cal = create("/api/v1/compendiums/"+pw.threeID+"/entities", `{"kind":"character","name":"Cal"}`)
	pw.proj = create("/api/v1/projects", `{"title":"Pinned Book"}`)
	pw.other = create("/api/v1/projects", `{"title":"Sibling Book"}`)
	put("/api/v1/projects/"+pw.proj+"/compendium", `{"compendium_id":"`+pw.oneID+`"}`)
	put("/api/v1/projects/"+pw.other+"/compendium", `{"compendium_id":"`+pw.oneID+`"}`)
	for path, body := range map[string]string{
		"/api/v1/projects/" + pw.proj + "/compendium/refs": `{"compendium_id":"` + pw.twoID + `"}`,
		"/api/v1/projects/" + pw.proj + "/entity-links":    `{"entity_id":"` + pw.cal + `"}`,
	} {
		if rec := s.do(w, "POST", path, body); rec.Code >= 300 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	pw.readP, _ = s.mintKey(t, w, `{"name":"read project key","scope":"read","project_id":"`+pw.proj+`"}`)
	pw.writeP, _ = s.mintKey(t, w, `{"name":"write project key","scope":"write","project_id":"`+pw.proj+`"}`)
	pw.readC, _ = s.mintKey(t, w, `{"name":"read compendium key","scope":"read","compendium_id":"`+pw.oneID+`"}`)
	pw.writeC, _ = s.mintKey(t, w, `{"name":"write compendium key","scope":"write","compendium_id":"`+pw.oneID+`"}`)
	return pw
}

// A key pinned to a project reaches that project and its primary Compendium
// only: not the Compendiums the project references, and not the entities it
// links from others, through any of the project's entity or wiring ops.
func TestProjectKeyDoesNotSeeReferencesOrLinks(t *testing.T) {
	pw := newPinWorld(t)
	s := pw.s
	leaks := []string{"Bea", "Cal", "Two", "Three", pw.bea, pw.cal, pw.twoID, pw.threeID}
	for scope, key := range map[string]string{"read": pw.readP, "write": pw.writeP} {
		get := func(path string) (int, string) {
			rec := s.doKey(key, "GET", path, "")
			return rec.Code, rec.Body.String()
		}
		base := "/api/v1/projects/" + pw.proj
		for name, path := range map[string]string{
			"entity list":       base + "/entities",
			"entity list q":     base + "/entities?q=bea",
			"entity by name":    base + "/entity?entity=Bea",
			"entity by id":      base + "/entity?entity=" + pw.bea,
			"linked by name":    base + "/entity?entity=Cal",
			"link list":         base + "/entity-links",
			"compendium wiring": base + "/compendium",
		} {
			code, body := get(path)
			if strings.Contains(name, "by") && code != 404 {
				t.Errorf("%s key, %s: %d, want 404: %s", scope, name, code, body)
			}
			for _, l := range leaks {
				if strings.Contains(body, l) {
					t.Errorf("%s key, %s names %q: %s", scope, name, l, body)
				}
			}
		}
		// What it should see: its primary's entity.
		if code, body := get(base + "/entity?entity=Ada"); code != 200 || !strings.Contains(body, `"via":"primary"`) {
			t.Errorf("%s key reading its primary's entity: %d %s", scope, code, body)
		}
		if _, body := get(base + "/entities"); !strings.Contains(body, `"total":1`) {
			t.Errorf("%s key's entity list: %s", scope, body)
		}
		var wiring struct {
			Item struct{ Primary *struct{ Name string } }
		}
		_, wb := get(base + "/compendium")
		_ = json.Unmarshal([]byte(wb), &wiring)
		if wiring.Item.Primary == nil || wiring.Item.Primary.Name != "One" {
			t.Errorf("%s key's wiring: %s", scope, wb)
		}
	}
	// The write key cannot override, clear or unlink what it should not know.
	base := "/api/v1/projects/" + pw.proj
	for name, c := range map[string][3]string{
		"override a referenced entity": {"PUT", base + "/entity-overrides", `{"entity_id":"` + pw.bea + `","name":"Beatrice"}`},
		"override a linked entity":     {"PUT", base + "/entity-overrides", `{"entity_id":"` + pw.cal + `","name":"Calvin"}`},
		"clear a referenced override":  {"DELETE", base + "/entity-overrides?entity_id=" + pw.bea, ""},
		"clear a linked override":      {"DELETE", base + "/entity-overrides?entity_id=" + pw.cal, ""},
		"unlink":                       {"DELETE", base + "/entity-links?entity_id=" + pw.cal, ""},
	} {
		if rec := s.doKey(pw.writeP, c[0], c[1], c[2]); rec.Code != 404 {
			t.Errorf("%s: %d, want 404: %s", name, rec.Code, rec.Body)
		}
	}
	if n := s.dbCount(t, `SELECT count(*) FROM project_entity_overrides`); n != 0 {
		t.Errorf("a project key wrote %d overrides it should not reach", n)
	}
	if n := s.dbCount(t, `SELECT count(*) FROM project_entity_links`); n != 1 {
		t.Errorf("a project key removed a link: %d left", n)
	}
	if rec := s.doKey(pw.writeP, "PUT", base+"/entity-overrides", `{"entity_id":"`+pw.ada+`","name":"Ada Q"}`); rec.Code != 200 {
		t.Errorf("a project key overriding its own primary's entity: %d %s", rec.Code, rec.Body)
	}
	// The same person, with a session, still sees everything.
	rec := s.do(pw.w, "GET", base+"/entities", "")
	if !strings.Contains(rec.Body.String(), `"total":3`) {
		t.Errorf("the session view: %s", rec.Body)
	}
}

func (s *Server) dbCount(t *testing.T, q string) int {
	t.Helper()
	var n int
	if err := s.DB.Read().QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

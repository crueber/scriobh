package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// The Compendium ops over REST: path, query and body binding, the status
// codes, the download of an export, and the error shape for a stranger.
func TestCompendiumOverREST(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	post := func(c caller, path, body string, want int) map[string]any {
		t.Helper()
		return send(t, s, c, "POST", path, body, want)
	}

	c := post(owner, "/api/v1/compendiums", `{"name":"The Salt Road","kind":"fiction"}`, 201)["item"].(map[string]any)
	cid := c["id"].(string)
	if c["role"] != "owner" || c["slug"] != "the-salt-road" {
		t.Errorf("created: %v", c)
	}
	e := post(owner, "/api/v1/compendiums/"+cid+"/entities", `{"kind":"character","name":"Máire Nolan","aliases":["Maire"],"fields":{"age":34}}`, 201)["item"].(map[string]any)
	eid := e["id"].(string)
	if fields := e["fields"].(map[string]any); fields["age"] != "34" {
		t.Errorf("fields: %v", e["fields"])
	}
	// Unknown body fields and a wrong type are refused in the error shape.
	er := post(owner, "/api/v1/compendiums/"+cid+"/entities", `{"kind":"character","name":"X","nope":1}`, 422)["error"].(map[string]any)
	if er["code"] != "invalid" {
		t.Errorf("error: %v", er)
	}
	// Query parameters: filters, and a DELETE that takes its target in the query.
	list := send(t, s, owner, "GET", "/api/v1/compendiums/"+cid+"/entities?q=maire&limit=5", "", 200)
	if list["total"].(float64) != 1 {
		t.Errorf("list: %v", list)
	}
	found := send(t, s, owner, "GET", "/api/v1/compendiums/"+cid+"/search?q=maire", "", 200)
	if found["total"].(float64) != 1 {
		t.Errorf("search: %v", found)
	}
	alias := e["aliases"].([]any)[0].(map[string]any)["id"].(string)
	if rm := send(t, s, owner, "DELETE", "/api/v1/entities/"+eid+"/aliases?alias_id="+alias, "", 200); rm["removed"] != true {
		t.Errorf("alias remove: %v", rm)
	}
	if rm := send(t, s, owner, "DELETE", "/api/v1/compendiums/"+cid+"/entity-fields?kind=character&key=age&drop_values=true", "", 200); rm["removed"] != true || rm["values_dropped"].(float64) != 1 {
		t.Errorf("field remove: %v", rm)
	}
	// A dry-run merge is a POST that writes nothing.
	other := post(owner, "/api/v1/compendiums/"+cid+"/entities", `{"kind":"character","name":"Mary"}`, 201)["item"].(map[string]any)["id"].(string)
	plan := post(owner, "/api/v1/entities/"+other+"/merge", `{"into_id":"`+eid+`","dry_run":true}`, 200)
	if plan["dry_run"] != true || plan["merged"] != false {
		t.Errorf("merge preview: %v", plan)
	}

	// The export is the file itself, as an attachment.
	req := newRequest(owner, "POST", "/api/v1/compendiums/"+cid+"/export", "")
	rec := serve(s, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "the-salt-road-compendium.zip") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || len(zr.File) != 5 {
		t.Fatalf("zip: %v %v", err, zr)
	}

	// A stranger gets the same 404 for a Compendium that exists as for one that does not, on every verb.
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/compendiums/" + cid, ""},
		{"GET", "/api/v1/compendiums/" + cid + "/entities", ""},
		{"GET", "/api/v1/entities/" + eid, ""},
		{"POST", "/api/v1/entities/" + eid + "/merge", `{"into_id":"` + other + `"}`},
		{"DELETE", "/api/v1/compendiums/" + cid, `{"confirm_name":"x"}`},
		{"PATCH", "/api/v1/compendiums/" + cid + "/members", `{"user_id":"u","role":"reader"}`},
	} {
		real := s.do(stranger, tc.method, tc.path, tc.body)
		fake := s.do(stranger, tc.method, strings.NewReplacer(cid, "cmp_00000000000000000000000000", eid, "ent_00000000000000000000000000", other, "ent_00000000000000000000000001").Replace(tc.path), tc.body)
		if real.Code != 404 || fake.Code != 404 || real.Body.String() != fake.Body.String() {
			t.Errorf("%s %s: %d vs %d, bodies %q / %q", tc.method, tc.path, real.Code, fake.Code, real.Body, fake.Body)
		}
	}
	// The schema lists the new auth kind and every op.
	sch := send(t, s, owner, "GET", "/api/v1/_ops", "", 200)
	n := 0
	for _, o := range sch["ops"].([]any) {
		if o.(map[string]any)["auth"].(map[string]any)["kind"] == "compendium" {
			n++
		}
	}
	if n < 20 {
		t.Errorf("%d ops declare compendium auth in the schema", n)
	}
}

func newRequest(c caller, method, path, body string) *http.Request {
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	return req
}

func serve(s *Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func send(t *testing.T, s *Server, c caller, method, path, body string, want int) map[string]any {
	t.Helper()
	rec := s.do(c, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, rec.Code, want, rec.Body)
	}
	var out map[string]any
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, rec.Body)
		}
	}
	return out
}

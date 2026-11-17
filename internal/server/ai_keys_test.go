package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// Provider management handles keys and decides where text may go, so it is for a
// person in a browser: an API key is refused at every scope, whoever owns it. The key
// sweep has no universe of provider ids, so the ops with an {id} are checked here.
func TestProviderManagementIsForBrowsersOnly(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	rec := s.do(admin, "POST", "/api/v1/ai/providers", `{"name":"Main","kind":"openai_compatible","base_url":"https://api.example.com/v1","default_model":"m"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var made struct{ Item struct{ ID string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	pid := made.Item.ID
	p := "/api/v1/ai/providers"
	calls := []struct{ method, path, body string }{
		{"POST", p, `{"name":"X","kind":"ollama","base_url":"https://x.example.com"}`},
		{"PATCH", p + "/" + pid, `{"name":"Renamed"}`},
		{"DELETE", p + "/" + pid, ""},
		{"POST", p + "/" + pid + "/test", ""},
		{"POST", p + "/" + pid + "/models/refresh", ""},
		{"PUT", p + "/" + pid + "/models", `{"model":"m2"}`},
		{"DELETE", p + "/" + pid + "/models?model=m", ""},
	}
	for _, scope := range []string{"read", "write", "admin"} {
		secret, _ := s.mintKey(t, admin, `{"name":"agent `+scope+`","scope":"`+scope+`"}`)
		for _, c := range calls {
			if got := s.doKey(secret, c.method, c.path, c.body); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "browser") {
				t.Errorf("%s key: %s %s = %d %s", scope, c.method, c.path, got.Code, got.Body)
			}
		}
	}
	// The same calls from the browser session are not refused for being a key.
	if got := s.do(admin, "PATCH", p+"/"+pid, `{"name":"Renamed"}`); got.Code != http.StatusOK {
		t.Errorf("session: %d %s", got.Code, got.Body)
	}
	// Nothing changed behind the refusals, and the keys can still read (no secret in it).
	secret, _ := s.mintKey(t, admin, `{"name":"reader","scope":"read"}`)
	list := s.doKey(secret, "GET", p, "")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), "api_key") && strings.Contains(list.Body.String(), "cipher") {
		t.Errorf("list: %d %s", list.Code, list.Body)
	}
}

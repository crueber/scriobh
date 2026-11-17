package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// TestSettingsKeysSection: the settings page lists keys, creating one shows the
// secret once on the answering page only, and revoking removes it.
func TestSettingsKeysSection(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)

	rec := s.do(w, "GET", "/settings", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="keys"`) || !strings.Contains(rec.Body.String(), "You have no keys") {
		t.Fatalf("settings: %d", rec.Code)
	}
	if rec := s.do(reader, "GET", "/settings", ""); strings.Contains(rec.Body.String(), `id="keys"`) {
		t.Error("an instance reader should not see the API keys section")
	}
	rec = s.post(t, w, "/ui/keys", url.Values{"name": {"Claude <salt road>"}, "scope": {"write"}, "expires_in_days": {"30"}}, "", false)
	body := rec.Body.String()
	m := regexp.MustCompile(`scr_[A-Za-z0-9_-]{43}`).FindString(body)
	if rec.Code != 200 || m == "" || !strings.Contains(body, "shown once") {
		t.Fatalf("create: %d %.300s", rec.Code, body)
	}
	if strings.Contains(body, "Claude <salt road>") || !strings.Contains(body, "Claude &lt;salt road&gt;") {
		t.Error("the key name must be escaped")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the page with a secret must not be cached")
	}
	if rec := s.doKey(m, "GET", "/api/v1/me", ""); rec.Code != 200 {
		t.Errorf("the key shown does not work: %d", rec.Code)
	}
	// The next visit shows the key, but never its secret.
	again := s.do(w, "GET", "/settings", "").Body.String()
	if strings.Contains(again, m) || !strings.Contains(again, "Claude &lt;salt road&gt;") {
		t.Errorf("settings after create: the secret leaked or the key is missing")
	}
	// A bad form is a 422 on the page, not a stack trace.
	if rec := s.post(t, w, "/ui/keys", url.Values{"name": {""}}, "", false); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("empty name: %d", rec.Code)
	}
	id := regexp.MustCompile(`/ui/keys/(key_[0-9A-Za-z]+)/revoke`).FindStringSubmatch(again)
	if id == nil {
		t.Fatal("no revoke form")
	}
	if rec := s.post(t, w, "/ui/keys/"+id[1]+"/revoke", url.Values{}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("revoke: %d", rec.Code)
	}
	if rec := s.doKey(m, "GET", "/api/v1/me", ""); rec.Code != 401 {
		t.Errorf("a revoked key: %d", rec.Code)
	}
	// Without the CSRF token the form is refused.
	if rec := s.post(t, w, "/ui/keys", url.Values{"name": {"x"}}, "-", false); rec.Code != 403 {
		t.Errorf("no token: %d", rec.Code)
	}
}

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// label is a display name that says which header produced it.
func label(k, v string) string {
	return "Changed by " + strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, k+" "+v)
}

// TestDryRunHeaderSpellings: every way of asking for a preview is a preview,
// every way of declining is a real run, and a value that cannot be read is a
// 400, never a silent commit.
func TestDryRunHeaderSpellings(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	name := func() string {
		rec := s.do(w, "GET", "/api/v1/me", "")
		return decodeObj(t, rec)["item"].(map[string]any)["display_name"].(string)
	}
	send := func(k, v string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PATCH", "/api/v1/me", strings.NewReader(`{"display_name":"`+label(k, v)+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(k, v)
		req.AddCookie(w.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	preview := [][2]string{
		{"Prefer", "dry-run"}, {"Prefer", "DRY-RUN"}, {"Prefer", "dry-run=true"}, {"Prefer", "dry-run=1"}, {"Prefer", `dry-run="yes"`},
		{"Prefer", "dry-run=on"}, {"Prefer", "respond-async, dry-run"}, {"Prefer", "return=minimal,dry-run;x=1"}, {"Prefer", "dry-run=anything"},
		{"X-Scriobh-Dry-Run", "1"}, {"X-Scriobh-Dry-Run", "true"}, {"X-Scriobh-Dry-Run", "TRUE"}, {"X-Scriobh-Dry-Run", "yes"}, {"X-Scriobh-Dry-Run", "on"}, {"X-Scriobh-Dry-Run", " On "},
	}
	before := name()
	for _, h := range preview {
		rec := send(h[0], h[1])
		m := decodeObj(t, rec)
		if rec.Code != 200 || m["dry_run"] != true || rec.Header().Get("Preference-Applied") != "dry-run" {
			t.Errorf("%s: %s should be a preview: %d %s (Preference-Applied %q)", h[0], h[1], rec.Code, rec.Body, rec.Header().Get("Preference-Applied"))
		}
		if got := name(); got != before {
			t.Fatalf("%s: %s changed the profile to %q", h[0], h[1], got)
		}
	}
	real := [][2]string{{"Prefer", "dry-run=false"}, {"Prefer", "dry-run=0"}, {"Prefer", "Dry-Run=No"}, {"Prefer", "return=minimal"},
		{"X-Scriobh-Dry-Run", "0"}, {"X-Scriobh-Dry-Run", "false"}, {"X-Scriobh-Dry-Run", "no"}, {"X-Scriobh-Dry-Run", "off"}}
	for _, h := range real {
		rec := send(h[0], h[1])
		if rec.Code != 200 || decodeObj(t, rec)["dry_run"] != nil || rec.Header().Get("Preference-Applied") != "" {
			t.Errorf("%s: %s should be a real run: %d %s", h[0], h[1], rec.Code, rec.Body)
		}
		if got := name(); got != label(h[0], h[1]) {
			t.Errorf("%s: %s did not run for real (profile %q)", h[0], h[1], got)
		}
	}
	// An empty value, and one asking and one declining, resolved the safe way.
	if rec := send("X-Scriobh-Dry-Run", ""); rec.Code != 200 || decodeObj(t, rec)["dry_run"] != nil {
		t.Errorf("an empty X-Scriobh-Dry-Run is no request: %d %s", rec.Code, rec.Body)
	}
	both := httptest.NewRequest("PATCH", "/api/v1/me", strings.NewReader(`{"display_name":"both"}`))
	both.Header.Set("Content-Type", "application/json")
	both.Header.Set("Prefer", "dry-run=false")
	both.Header.Set("X-Scriobh-Dry-Run", "1")
	both.AddCookie(w.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, both)
	if decodeObj(t, rec)["dry_run"] != true || name() == "both" {
		t.Errorf("Prefer dry-run=false with X-Scriobh-Dry-Run 1 must be a preview: %s", rec.Body)
	}
	two := httptest.NewRequest("PATCH", "/api/v1/me", strings.NewReader(`{"display_name":"two"}`))
	two.Header.Set("Content-Type", "application/json")
	two.Header.Add("Prefer", "return=minimal")
	two.Header.Add("Prefer", "dry-run")
	two.AddCookie(w.cookie)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, two)
	if decodeObj(t, rec)["dry_run"] != true || name() == "two" {
		t.Errorf("a dry-run in a second Prefer line must be a preview: %s", rec.Body)
	}
	// Unreadable values never run, for real or otherwise.
	mark := name()
	for _, v := range []string{"maybe", "2", "dry", "enable"} {
		rec := send("X-Scriobh-Dry-Run", v)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"invalid"`) {
			t.Errorf("X-Scriobh-Dry-Run: %s: %d %s, want 400 invalid", v, rec.Code, rec.Body)
		}
		if got := name(); got != mark {
			t.Fatalf("an unreadable value ran the request for real: %q", got)
		}
	}
}

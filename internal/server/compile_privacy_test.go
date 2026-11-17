package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The contact block is personal data. A response that holds it is not kept for
// idempotent replay: not the response of compile.contact.set, not the preview's.
func TestContactBlockIsNotKeptForReplay(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	base := "/api/v1/projects/" + f.project
	send := func(method, path, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.AddCookie(f.owner.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	const secret = "tide-reader@example.invalid"
	if rec := send("PATCH", base+"/compile/contact", "contact-key-1", `{"name":"A Writer","email":"`+secret+`","phone":"+353 1 555 0199"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("contact.set: %d %s", rec.Code, rec.Body)
	}
	if rec := send("POST", base+"/compile/preview", "preview-key-1", `{"preset":"shunn-us"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	rows, err := s.DB.Read().Query(`SELECT op, response_json FROM idempotency_keys`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var op, body string
		if err := rows.Scan(&op, &body); err != nil {
			t.Fatal(err)
		}
		if op == "compile.contact.set" || op == "compile.preview" {
			seen++
		}
		if strings.Contains(body, secret) || strings.Contains(body, "555 0199") {
			t.Errorf("%s: the contact block is in the stored response", op)
		}
	}
	if seen != 2 {
		t.Errorf("expected a record (a tombstone) of both calls, found %d", seen)
	}
}

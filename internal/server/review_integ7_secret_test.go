package server

import (
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/idempotency"
	"github.com/crueber/scriobh/internal/ops"
)

// An op whose request carries a typed secret must not be kept for idempotent replay: the stored request hash of
// a body that holds a low-entropy password is a guessing oracle for anyone with the database or a backup (the
// reason SecretInput exists for the project passphrase). mail.config.set carries the relay's password and
// does not declare SecretInput.
func TestReviewInteg7MailPasswordIsNotAnOracleInTheIdempotencyTable(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	body := `{"host":"smtp.example.org","port":587,"security":"starttls","username":"scriobh","password":"hunter2","from_address":"scriobh@example.org"}`
	req := httptest.NewRequest("PUT", "/api/v1/admin/mail", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "rv-mail-1")
	req.AddCookie(admin.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("mail.config.set: %d %s", rec.Code, rec.Body)
	}
	var op, hash, resp string
	if err := s.DB.Read().QueryRow(`SELECT op, request_hash, response_json FROM idempotency_keys WHERE key = 'rv-mail-1'`).Scan(&op, &hash, &resp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return // nothing is kept for replay: what the fix (SecretInput) does
		}
		t.Fatal(err)
	}
	for _, guess := range []string{"letmein", "password", "hunter2"} {
		g := strings.Replace(body, "hunter2", guess, 1)
		if idempotency.Fingerprint(op, "PUT", "/api/v1/admin/mail", nil, []byte(g)) == hash {
			t.Errorf("the stored request_hash of %s is sha256 of the body: guessing %q reproduces it (an offline oracle for the relay's password)", op, guess)
		}
	}
}

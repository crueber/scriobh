package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// newServerWith builds a test server with extra configuration.
func newServerWith(t *testing.T, env map[string]string) *Server {
	t.Helper()
	s, err := New(Deps{Cfg: testutil.Config(t, env), DB: testutil.DB(t)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// clock makes the limiter's time ours.
func clock(s *Server) func(time.Duration) {
	now := time.Now()
	s.limiter.now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

// TestKeysShareTheirOwnersAllowance: a key spends its own bucket and its
// owner's, so holding many keys does not multiply the rate; another person's
// keys are unaffected; time refills the bucket.
func TestKeysShareTheirOwnersAllowance(t *testing.T) {
	s := newServerWith(t, map[string]string{"SCRIOBH_RATE_LIMIT": "40"}) // burst 10
	tick := clock(s)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)
	a, _ := s.mintKey(t, w, `{"name":"a"}`)
	b, _ := s.mintKey(t, w, `{"name":"b"}`)
	o, _ := s.mintKey(t, other, `{"name":"o"}`)
	// The browser session spent a token minting the keys; start from a full bucket.
	tick(10 * time.Minute)
	n := 0
	for ; n < 40; n++ {
		if s.doKey(a, "GET", "/api/v1/me", "").Code == http.StatusTooManyRequests {
			break
		}
	}
	if n < 5 || n >= 40 {
		t.Fatalf("key a was limited after %d requests", n)
	}
	// Key b has spent nothing of its own and is still refused: the owner's bucket is empty.
	if rec := s.doKey(b, "GET", "/api/v1/me", ""); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("a second key of the same person: %d, want 429 with Retry-After", rec.Code)
	}
	if rec := s.doKey(o, "GET", "/api/v1/me", ""); rec.Code != 200 {
		t.Errorf("another person's key: %d, want 200", rec.Code)
	}
	// The owner's browser session shares the same allowance.
	if rec := s.do(w, "GET", "/api/v1/me", ""); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the owner's session while a key has spent the allowance: %d", rec.Code)
	}
	tick(time.Minute)
	if rec := s.doKey(b, "GET", "/api/v1/me", ""); rec.Code != 200 {
		t.Errorf("after a minute: %d, want 200", rec.Code)
	}
}

func (s *Server) badKey(secret string) *httptest.ResponseRecorder {
	return s.doKey(secret, "GET", "/api/v1/me", "")
}

// TestBadKeysAreRateLimitedByAddress: guessing keys is held to an allowance per
// client address, and a good key from the same address is not affected.
func TestBadKeysAreRateLimitedByAddress(t *testing.T) {
	s := newServerWith(t, nil)
	tick := clock(s)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	good, _ := s.mintKey(t, w, `{"name":"good"}`)
	tick(time.Hour)
	var unauth, limited int
	for i := 0; i < 60; i++ {
		switch s.badKey("scr_" + string(rune('A'+i%26)) + "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx").Code {
		case http.StatusUnauthorized:
			unauth++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if unauth < 10 || unauth > 15 || limited < 40 {
		t.Fatalf("60 bad keys at once: %d answered 401 and %d 429; want about a burst of 10 then 429", unauth, limited)
	}
	if rec := s.badKey("scr_" + "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("a bad key after the burst: %d", rec.Code)
	}
	// A valid key is unaffected by the bad-key bucket.
	if rec := s.doKey(good, "GET", "/api/v1/me", ""); rec.Code != 200 {
		t.Errorf("a good key from the same address: %d", rec.Code)
	}
	// A minute later the address may try again (30 a minute: 30 tokens back, burst 10).
	tick(time.Minute)
	if rec := s.badKey("scr_" + "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"); rec.Code != http.StatusUnauthorized {
		t.Errorf("after a minute: %d, want 401", rec.Code)
	}
	// Garbage that is not even shaped like a key counts too.
	for i := 0; i < 30; i++ {
		s.badKey("hello")
	}
	if rec := s.badKey("hello"); rec.Code != http.StatusTooManyRequests {
		t.Errorf("garbage keys are not exempt: %d", rec.Code)
	}
}

// TestKeyCapPerUser: the number of live keys a person may hold is configurable
// (default 25); revoked and expired keys do not count.
func TestKeyCapPerUser(t *testing.T) {
	s := newServerWith(t, map[string]string{"SCRIOBH_MAX_KEYS_PER_USER": "3"})
	w := s.newCaller(t, "writer", ops.RoleWriter)
	var ids []string
	for i := 0; i < 3; i++ {
		_, id := s.mintKey(t, w, `{"name":"k"}`)
		ids = append(ids, id)
	}
	if rec := s.do(w, "POST", "/api/v1/keys", `{"name":"one too many"}`); rec.Code != 422 {
		t.Fatalf("the fourth key: %d %s", rec.Code, rec.Body)
	}
	s.do(w, "POST", "/api/v1/keys/"+ids[0]+"/revoke", "")
	s.mintKey(t, w, `{"name":"after a revoke"}`)
	if rec := s.do(w, "POST", "/api/v1/keys", `{"name":"over again"}`); rec.Code != 422 {
		t.Errorf("over again: %d", rec.Code)
	}
	if _, err := s.DB.Writer().Exec(`UPDATE api_keys SET expires_at = ? WHERE id = ?`, db.Format(db.NowTime().Add(-time.Hour)), ids[1]); err != nil {
		t.Fatal(err)
	}
	s.mintKey(t, w, `{"name":"after an expiry"}`)
	// Another person has their own count.
	other := s.newCaller(t, "other", ops.RoleWriter)
	s.mintKey(t, other, `{"name":"mine"}`)
	// The default is 25.
	d := newAuthzServer(t)
	dw := d.newCaller(t, "writer", ops.RoleWriter)
	for i := 0; i < 25; i++ {
		d.mintKey(t, dw, `{"name":"k"}`)
	}
	if rec := d.do(dw, "POST", "/api/v1/keys", `{"name":"26th"}`); rec.Code != 422 {
		t.Errorf("the 26th key: %d, want 422", rec.Code)
	}
}

// TestIdempotencyCapsComeFromTheConfiguration.
func TestIdempotencyCapsComeFromTheConfiguration(t *testing.T) {
	s := newServerWith(t, map[string]string{"SCRIOBH_IDEMPOTENCY_MAX_ENTRIES": "2", "SCRIOBH_IDEMPOTENCY_MAX_BYTES": "100000"})
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"retrier"}`)
	for _, k := range []string{"k1", "k2", "k3", "k4"} {
		if rec := s.doKey(secret, "POST", "/api/v1/projects", `{"title":"Cap `+k+`"}`, "Idempotency-Key", k); rec.Code != 201 {
			t.Fatal(rec.Body)
		}
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM idempotency_keys`).Scan(&n)
	if n != 2 {
		t.Errorf("%d records kept, want the configured 2", n)
	}
	// The newest are the ones kept.
	if rec := s.doKey(secret, "POST", "/api/v1/projects", `{"title":"Cap k4"}`, "Idempotency-Key", "k4"); rec.Header().Get("Idempotent-Replay") != "true" {
		t.Error("the newest record should replay")
	}
}

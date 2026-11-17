package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/idempotency"
	"github.com/crueber/scriobh/internal/ops"
)

// doKey sends a request authenticated by an API key.
func (s *Server) doKey(secret, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// mintKey creates a key as c through the REST route and returns its secret and id.
func (s *Server) mintKey(t *testing.T, c caller, body string) (secret, id string) {
	t.Helper()
	rec := s.do(c, "POST", "/api/v1/keys", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("key.create %s: %d %s", body, rec.Code, rec.Body)
	}
	var out struct {
		Item   struct{ ID string }
		Secret string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !strings.HasPrefix(out.Secret, "scr_") {
		t.Fatalf("key.create body: %s", rec.Body)
	}
	return out.Secret, out.Item.ID
}

func TestAPIKeyLifecycle(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, id := s.mintKey(t, w, `{"name":"agent one","scope":"write"}`)

	// The key signs in as its owner.
	rec := s.doKey(secret, "GET", "/api/v1/me", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "writer@test.local") {
		t.Fatalf("profile.get by key: %d %s", rec.Code, rec.Body)
	}
	if rec := s.doKey(secret, "POST", "/api/v1/projects", `{"title":"By key"}`); rec.Code != 201 {
		t.Errorf("a write key creates: %d %s", rec.Code, rec.Body)
	}
	// X-API-Key is an equal alternative to Authorization: Bearer.
	rec = s.doKey("", "GET", "/api/v1/me", "", "X-API-Key", secret)
	if rec.Code != 200 {
		t.Errorf("X-API-Key: %d", rec.Code)
	}
	// whoami says what the key is.
	rec = s.doKey(secret, "GET", "/api/v1/_whoami", "")
	var who struct {
		Via, Scope, SchemaHash string `json:"-"`
		User                   struct{ Email, Role string }
		Key                    struct{ ID, Prefix string }
		Agent                  bool
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if rec.Code != 200 || who.User.Role != "writer" || who.Key.ID != id || !who.Agent || !strings.Contains(rec.Body.String(), `"scope":"write"`) || !strings.Contains(rec.Body.String(), `"via":"apikey"`) {
		t.Errorf("whoami: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Error("whoami leaks the secret")
	}
	// The list never carries a secret, and the audit line names the key only.
	rec = s.do(w, "GET", "/api/v1/keys", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), secret) || !strings.Contains(rec.Body.String(), id) {
		t.Errorf("key.list: %d %s", rec.Code, rec.Body)
	}
	var detail string
	if err := s.DB.Read().QueryRow(`SELECT group_concat(detail_json || target_id) FROM audit_log WHERE action = 'apikey.create'`).Scan(&detail); err != nil || strings.Contains(detail, secret) || !strings.Contains(detail, id) {
		t.Errorf("audit: %v %q", err, detail)
	}
	// last_used_at is recorded.
	var used *string
	_ = s.DB.Read().QueryRow(`SELECT last_used_at FROM api_keys WHERE id = ?`, id).Scan(&used)
	if used == nil {
		t.Error("last_used_at was not recorded")
	}

	// Unknown, malformed and revoked keys are 401 and never fall back to a cookie.
	for name, bad := range map[string]string{"unknown": "scr_" + strings.Repeat("A", 43), "garbage": "hello"} {
		req := httptest.NewRequest("GET", "/api/v1/me", nil)
		req.Header.Set("Authorization", "Bearer "+bad)
		req.AddCookie(w.cookie)
		r2 := httptest.NewRecorder()
		s.Handler().ServeHTTP(r2, req)
		if r2.Code != 401 || !strings.Contains(r2.Body.String(), `"unauthenticated"`) {
			t.Errorf("%s key with a good cookie: %d %s", name, r2.Code, r2.Body)
		}
	}
	if rec := s.do(w, "POST", "/api/v1/keys/"+id+"/revoke", ""); rec.Code != 200 {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := s.doKey(secret, "GET", "/api/v1/me", ""); rec.Code != 401 {
		t.Errorf("a revoked key: %d, want 401", rec.Code)
	}
	if rec := s.do(w, "POST", "/api/v1/keys/"+id+"/revoke", ""); rec.Code != 200 {
		t.Errorf("revoking twice: %d", rec.Code)
	}

	// An expired key stops working.
	secret2, id2 := s.mintKey(t, w, `{"name":"short","expires_in_days":1}`)
	if rec := s.doKey(secret2, "GET", "/api/v1/me", ""); rec.Code != 200 {
		t.Fatalf("fresh key: %d", rec.Code)
	}
	past := db.Format(db.NowTime().Add(-time.Hour))
	if _, err := s.DB.Writer().Exec(`UPDATE api_keys SET expires_at = ? WHERE id = ?`, past, id2); err != nil {
		t.Fatal(err)
	}
	if rec := s.doKey(secret2, "GET", "/api/v1/me", ""); rec.Code != 401 {
		t.Errorf("an expired key: %d, want 401", rec.Code)
	}

	// A key is for the API: sent to a page it is ignored, not honoured.
	secret3, _ := s.mintKey(t, w, `{"name":"third"}`)
	if rec := s.doKey(secret3, "GET", "/settings", ""); rec.Code == 200 {
		t.Errorf("a key opened a page: %d", rec.Code)
	}
}

func TestAPIKeyScopeAndPinReachTheAPI(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	other := s.newCaller(t, "other", ops.RoleWriter)

	readKey, _ := s.mintKey(t, w, `{"name":"reader","scope":"read"}`)
	if rec := s.doKey(readKey, "POST", "/api/v1/projects", `{"title":"x"}`); rec.Code != 403 {
		t.Errorf("a read key creating: %d, want 403", rec.Code)
	}
	if rec := s.doKey(readKey, "GET", "/api/v1/projects", ""); rec.Code != 200 {
		t.Errorf("a read key listing: %d", rec.Code)
	}
	// A key cannot mint a stronger key, nor manage keys at all below admin.
	writeKey, _ := s.mintKey(t, w, `{"name":"writer","scope":"write"}`)
	if rec := s.doKey(writeKey, "POST", "/api/v1/keys", `{"name":"escalate","scope":"admin"}`); rec.Code != 403 {
		t.Errorf("a write key minting a key: %d, want 403", rec.Code)
	}
	if rec := s.doKey(writeKey, "GET", "/api/v1/keys", ""); rec.Code != 403 {
		t.Errorf("a write key listing keys: %d, want 403", rec.Code)
	}
	// A writer cannot create an admin-scope key, and cannot pin to a project they cannot see.
	if rec := s.do(w, "POST", "/api/v1/keys", `{"name":"x","scope":"admin"}`); rec.Code != 403 {
		t.Errorf("a writer's admin key: %d, want 403", rec.Code)
	}
	pr := s.do(w, "POST", "/api/v1/projects", `{"title":"Mine"}`)
	var proj struct{ Item struct{ ID string } }
	_ = json.Unmarshal(pr.Body.Bytes(), &proj)
	if rec := s.do(other, "POST", "/api/v1/keys", `{"name":"x","project_id":"`+proj.Item.ID+`"}`); rec.Code != 404 {
		t.Errorf("pinning to someone else's project: %d, want 404", rec.Code)
	}
	if rec := s.do(w, "POST", "/api/v1/keys", `{"name":"x","compendium_id":"cmp_x"}`); rec.Code != 404 {
		t.Errorf("a compendium pin to a compendium that does not exist: %d, want 404", rec.Code)
	}

	// A project-pinned key sees its project and nothing instance-wide, and
	// whoami still answers for it.
	pinned, _ := s.mintKey(t, w, `{"name":"pinned","project_id":"`+proj.Item.ID+`"}`)
	if rec := s.doKey(pinned, "GET", "/api/v1/projects/"+proj.Item.ID, ""); rec.Code != 200 {
		t.Errorf("pinned key on its project: %d", rec.Code)
	}
	if rec := s.doKey(pinned, "GET", "/api/v1/projects", ""); rec.Code != 403 {
		t.Errorf("pinned key listing: %d, want 403", rec.Code)
	}
	rec := s.doKey(pinned, "GET", "/api/v1/_whoami", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"title":"Mine"`) {
		t.Errorf("whoami for a pinned key: %d %s", rec.Code, rec.Body)
	}
	// Another person cannot revoke my key; an admin can.
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	_, kid := s.mintKey(t, w, `{"name":"to revoke"}`)
	if rec := s.do(other, "POST", "/api/v1/keys/"+kid+"/revoke", ""); rec.Code != 404 {
		t.Errorf("revoking another person's key: %d, want 404", rec.Code)
	}
	if rec := s.do(admin, "GET", "/api/v1/keys?all=true", ""); !strings.Contains(rec.Body.String(), kid) {
		t.Errorf("admin list all: %s", rec.Body)
	}
	if rec := s.do(admin, "POST", "/api/v1/keys/"+kid+"/revoke", ""); rec.Code != 200 {
		t.Errorf("admin revoke: %d", rec.Code)
	}
}

func TestIdempotency(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"retrier"}`)
	count := func() int {
		var n int
		_ = s.DB.Read().QueryRow(`SELECT count(*) FROM projects`).Scan(&n)
		return n
	}
	create := func(sec, key, title string) *httptest.ResponseRecorder {
		return s.doKey(sec, "POST", "/api/v1/projects", `{"title":"`+title+`"}`, "Idempotency-Key", key)
	}

	first := create(secret, "k-1", "Once")
	if first.Code != 201 || first.Header().Get("Idempotent-Replay") != "" {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	again := create(secret, "k-1", "Once")
	if again.Code != 201 || again.Header().Get("Idempotent-Replay") != "true" || again.Body.String() != first.Body.String() {
		t.Errorf("replay: %d %q %s", again.Code, again.Header().Get("Idempotent-Replay"), again.Body)
	}
	if count() != 1 {
		t.Errorf("the replay ran the op again: %d projects", count())
	}
	// The same key with a different request is refused and runs nothing.
	diff := create(secret, "k-1", "Twice")
	if diff.Code != 422 || !strings.Contains(diff.Body.String(), `"idempotency_key_reuse"`) {
		t.Errorf("reuse: %d %s", diff.Code, diff.Body)
	}
	if count() != 1 {
		t.Errorf("a refused reuse still ran: %d", count())
	}
	// Keys are per principal: another key's "k-1" is its own.
	secret2, _ := s.mintKey(t, w, `{"name":"another"}`)
	if rec := create(secret2, "k-1", "Twice"); rec.Code != 201 || rec.Header().Get("Idempotent-Replay") != "" {
		t.Errorf("another principal with the same key: %d %s", rec.Code, rec.Body)
	}
	// A failure is not stored: the same key can be used for a corrected request.
	if rec := create(secret, "k-2", ""); rec.Code != 422 {
		t.Fatalf("invalid first try: %d", rec.Code)
	}
	if rec := create(secret, "k-2", "Fixed"); rec.Code != 201 {
		t.Errorf("retry after a failure: %d %s", rec.Code, rec.Body)
	}
	// A bad key is a 422 and runs nothing.
	n := count()
	if rec := create(secret, "has space", "Bad"); rec.Code != 422 || count() != n {
		t.Errorf("a malformed key: %d", rec.Code)
	}
	// Reads ignore the header.
	if rec := s.doKey(secret, "GET", "/api/v1/projects", "", "Idempotency-Key", "k-3"); rec.Code != 200 || rec.Header().Get("Idempotent-Replay") != "" {
		t.Errorf("GET with a key: %d", rec.Code)
	}
}

func TestIdempotencyConcurrentDuplicatesRunOnce(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"racer"}`)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	replays := make([]string, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := s.doKey(secret, "POST", "/api/v1/projects", `{"title":"Race"}`, "Idempotency-Key", "race-1")
			codes[i], replays[i] = rec.Code, rec.Header().Get("Idempotent-Replay")
		}()
	}
	wg.Wait()
	var n, replayed int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'Race'`).Scan(&n)
	for i := range codes {
		if codes[i] != 201 {
			t.Errorf("request %d: %d", i, codes[i])
		}
		if replays[i] == "true" {
			replayed++
		}
	}
	if n != 1 || replayed != 7 {
		t.Errorf("%d projects created, %d replays; want 1 and 7", n, replayed)
	}
}

func TestIdempotencyExpiryAndSecrets(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"clock"}`)
	now := time.Now()
	s.Idem.Now = func() time.Time { return now }
	post := func() *httptest.ResponseRecorder {
		return s.doKey(secret, "POST", "/api/v1/projects", `{"title":"Timed"}`, "Idempotency-Key", "t-1")
	}
	if rec := post(); rec.Code != 201 {
		t.Fatal(rec.Body)
	}
	now = now.Add(idempotency.TTL - time.Minute)
	if rec := post(); rec.Header().Get("Idempotent-Replay") != "true" {
		t.Errorf("inside the window: %d replay=%q", rec.Code, rec.Header().Get("Idempotent-Replay"))
	}
	now = now.Add(2 * time.Minute) // past 24 h from the first request
	rec := post()
	if rec.Code != 201 || rec.Header().Get("Idempotent-Replay") != "" {
		t.Errorf("after expiry the key is free again: %d replay=%q", rec.Code, rec.Header().Get("Idempotent-Replay"))
	}
	var projects int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM projects WHERE title = 'Timed'`).Scan(&projects)
	if projects != 2 {
		t.Errorf("projects = %d, want 2 (the key expired between them)", projects)
	}
	// The sweep removes what has expired and keeps what has not.
	now = now.Add(48 * time.Hour)
	if n, err := s.Idem.Sweep(t.Context()); err != nil || n < 1 {
		t.Errorf("sweep: %d %v", n, err)
	}
	var left int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM idempotency_keys`).Scan(&left)
	if left != 0 {
		t.Errorf("%d rows left after the sweep", left)
	}

	// A response with a secret is never stored: a repeat is refused, not run twice.
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	req := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/keys", strings.NewReader(`{"name":"once"}`))
		r.Header.Set("Idempotency-Key", "secret-1")
		r.AddCookie(admin.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, r)
		return rec
	}
	first := req()
	if first.Code != 201 {
		t.Fatal(first.Body)
	}
	second := req()
	if second.Code != 409 || strings.Contains(second.Body.String(), "scr_") {
		t.Errorf("a repeat of a secret-bearing request: %d %s", second.Code, second.Body)
	}
	var stored string
	_ = s.DB.Read().QueryRow(`SELECT response_json FROM idempotency_keys WHERE key = 'secret-1'`).Scan(&stored)
	if stored != "" {
		t.Errorf("a secret was stored: %q", stored)
	}
	var keys int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM api_keys WHERE name = 'once'`).Scan(&keys)
	if keys != 1 {
		t.Errorf("%d keys named 'once', want 1", keys)
	}
}

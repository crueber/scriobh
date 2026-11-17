package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

type keyOut struct {
	Item struct {
		ID          string `json:"id"`
		ExpiresAt   string `json:"expires_at"`
		ParentKeyID string `json:"parent_key_id"`
		Scope       string `json:"scope"`
	}
	Secret string
}

func (s *Server) keyVia(t *testing.T, secret, body string) (keyOut, int) {
	t.Helper()
	rec := s.doKey(secret, "POST", "/api/v1/keys", body)
	var out keyOut
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out, rec.Code
}

// TestChildKeysNeverOutliveTheirParent: a key's children expire no later than
// it does, the lineage is listed, and revoking a key revokes what it made, at
// any depth.
func TestChildKeysNeverOutliveTheirParent(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	parentSecret, parentID := s.mintKey(t, admin, `{"name":"parent","scope":"admin","expires_in_days":1}`)
	var parentExpiry string
	if err := s.DB.Read().QueryRow(`SELECT expires_at FROM api_keys WHERE id = ?`, parentID).Scan(&parentExpiry); err != nil {
		t.Fatal(err)
	}

	// "Never expires" and "a year" are both cut to the parent's expiry.
	never, code := s.keyVia(t, parentSecret, `{"name":"never","scope":"write","expires_in_days":0}`)
	if code != 201 || never.Item.ExpiresAt != parentExpiry || never.Item.ParentKeyID != parentID {
		t.Fatalf("expires_in_days 0 from a 1-day key: %d %+v (parent expires %s)", code, never.Item, parentExpiry)
	}
	year, _ := s.keyVia(t, parentSecret, `{"name":"year","scope":"write","expires_in_days":365}`)
	if year.Item.ExpiresAt != parentExpiry {
		t.Errorf("a year from a 1-day key: expires %s, want %s", year.Item.ExpiresAt, parentExpiry)
	}
	// A shorter life is respected.
	short, _ := s.keyVia(t, parentSecret, `{"name":"short","scope":"read","expires_in_days":0}`)
	_ = short
	var shortExp string
	_ = s.DB.Read().QueryRow(`SELECT expires_at FROM api_keys WHERE name = 'short'`).Scan(&shortExp)
	if shortExp == "" || shortExp > parentExpiry {
		t.Errorf("short expires %q", shortExp)
	}
	// A session-made key with no expiry still can have none.
	_, noExpiryID := s.mintKey(t, admin, `{"name":"forever","scope":"write","expires_in_days":0}`)
	var e *string
	_ = s.DB.Read().QueryRow(`SELECT expires_at FROM api_keys WHERE id = ?`, noExpiryID).Scan(&e)
	if e != nil {
		t.Errorf("a key made in the browser with 0 days should not expire: %v", *e)
	}

	// A grandchild, made by an admin-scope child.
	mid, _ := s.keyVia(t, parentSecret, `{"name":"mid","scope":"admin"}`)
	grand, code := s.keyVia(t, mid.Secret, `{"name":"grand","scope":"write","expires_in_days":0}`)
	if code != 201 || grand.Item.ParentKeyID != mid.Item.ID || grand.Item.ExpiresAt != mid.Item.ExpiresAt {
		t.Fatalf("grandchild: %d %+v (mid expires %s)", code, grand.Item, mid.Item.ExpiresAt)
	}
	// The lineage is listed.
	rec := s.do(admin, "GET", "/api/v1/keys", "")
	if !strings.Contains(rec.Body.String(), `"parent_key_id":"`+parentID+`"`) {
		t.Errorf("list does not show the lineage: %s", rec.Body)
	}

	// Revoking the parent revokes the whole tree (5 below it), and says so.
	rec = s.do(admin, "POST", "/api/v1/keys/"+parentID+"/revoke", "")
	var rv struct{ Descendants int }
	_ = json.Unmarshal(rec.Body.Bytes(), &rv)
	if rec.Code != 200 || rv.Descendants != 5 {
		t.Errorf("revoke: %d %s", rec.Code, rec.Body)
	}
	for name, secret := range map[string]string{"parent": parentSecret, "never": never.Secret, "year": year.Secret, "mid": mid.Secret, "grand": grand.Secret} {
		if rec := s.doKey(secret, "GET", "/api/v1/me", ""); rec.Code != 401 {
			t.Errorf("%s key still works after its parent was revoked: %d", name, rec.Code)
		}
	}
	// The key made in the browser is untouched.
	var live int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM api_keys WHERE id = ? AND revoked_at IS NULL`, noExpiryID).Scan(&live)
	if live != 1 {
		t.Error("an unrelated key was revoked")
	}
	// Revoking a child alone leaves its parent.
	p2, p2id := s.mintKey(t, admin, `{"name":"p2","scope":"admin"}`)
	c2, _ := s.keyVia(t, p2, `{"name":"c2","scope":"write"}`)
	if rec := s.do(admin, "POST", "/api/v1/keys/"+c2.Item.ID+"/revoke", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := s.doKey(p2, "GET", "/api/v1/me", ""); rec.Code != 200 {
		t.Errorf("revoking a child revoked its parent: %d (%s)", rec.Code, p2id)
	}
}

// A key restricted to one project cannot reach key management at all, so it
// can never mint a key with a wider pin than its own. The owner is an
// administrator and the key has the admin scope, so the only thing standing in
// the way is the restriction.
func TestPinnedKeyCannotMakeKeys(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	var proj struct{ Item struct{ ID string } }
	_ = json.Unmarshal(s.do(admin, "POST", "/api/v1/projects", `{"title":"Pinned"}`).Body.Bytes(), &proj)
	secret, _ := s.mintKey(t, admin, `{"name":"pinned","scope":"admin","project_id":"`+proj.Item.ID+`"}`)
	free, _ := s.mintKey(t, admin, `{"name":"free","scope":"admin"}`)
	if _, code := s.keyVia(t, free, `{"name":"control","scope":"read"}`); code != 201 {
		t.Fatalf("the unpinned admin key should be able to make keys: %d", code)
	}
	for _, body := range []string{`{"name":"wider","scope":"read"}`, `{"name":"same","scope":"read","project_id":"` + proj.Item.ID + `"}`} {
		rec := s.doKey(secret, "POST", "/api/v1/keys", body)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "restricted") {
			t.Errorf("a pinned admin key making a key (%s): %d %s, want 403 naming the restriction", body, rec.Code, rec.Body)
		}
	}
	if rec := s.doKey(secret, "GET", "/api/v1/keys", ""); rec.Code != 403 {
		t.Errorf("a pinned key listing keys: %d", rec.Code)
	}
}

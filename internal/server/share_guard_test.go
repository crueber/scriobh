package server

import (
	"context"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestPassphraseUnlockLockoutAndProof(t *testing.T) {
	w := newShareWorld(t, nil)
	id, tok := w.link(t, `{"passphrase":"correct horse battery"}`)
	manifest := "/s/" + tok + "/api/manifest"
	// without a proof the link says it needs one, and shows nothing
	rec := w.pub("GET", manifest, "")
	if rec.Code != 401 || strings.Contains(rec.Body.String(), "Shared Book") {
		t.Fatalf("no proof: %d %s", rec.Code, rec.Body)
	}
	if r := w.pub("GET", manifest, "", "X-Share-Unlock", "forged"); r.Code != 401 {
		t.Errorf("a forged proof: %d", r.Code)
	}
	// a wrong passphrase: 401, not a hint of how wrong
	for i := 0; i < 3; i++ { // the first three are free
		if r := w.pub("POST", "/s/"+tok+"/api/unlock", `{"passphrase":"wrong guess `+string(rune('a'+i))+`"}`); r.Code != 401 {
			t.Fatalf("wrong passphrase %d: %d %s", i, r.Code, r.Body)
		}
	}
	// the right one still works (and clears the count)
	var u struct{ Proof string }
	rec = w.pub("POST", "/s/"+tok+"/api/unlock", `{"passphrase":"correct horse battery"}`)
	if rec.Code != 200 || rec.Header().Get("Set-Cookie") != "" {
		t.Fatalf("unlock: %d %v", rec.Code, rec.Header())
	}
	decode(t, rec, &u)
	if u.Proof == "" {
		t.Fatal("no proof")
	}
	if r := w.pub("GET", manifest, "", "X-Share-Unlock", u.Proof); r.Code != 200 {
		t.Errorf("with the proof: %d %s", r.Code, r.Body)
	}
	// the proof of one link opens no other
	_, tok2 := w.link(t, `{"passphrase":"another passphrase!"}`)
	if r := w.pub("GET", "/s/"+tok2+"/api/manifest", "", "X-Share-Unlock", u.Proof); r.Code != 401 {
		t.Errorf("a proof for another link: %d", r.Code)
	}
	// changing the passphrase voids every proof; clearing it opens the link
	if r := w.s.do(w.owner, "PATCH", "/api/v1/projects/"+w.pid+"/shares/"+id, `{"passphrase":"a brand new phrase"}`); r.Code != 200 {
		t.Fatalf("update: %d %s", r.Code, r.Body)
	}
	if r := w.pub("GET", manifest, "", "X-Share-Unlock", u.Proof); r.Code != 401 {
		t.Errorf("an old proof after a new passphrase: %d", r.Code)
	}
	if r := w.s.do(w.owner, "PATCH", "/api/v1/projects/"+w.pid+"/shares/"+id, `{"passphrase":""}`); r.Code != 200 {
		t.Fatal(r.Body)
	}
	if r := w.pub("GET", manifest, ""); r.Code != 200 {
		t.Errorf("a link whose passphrase was removed: %d", r.Code)
	}
	// a proof expires
	_, tok3 := w.link(t, `{"passphrase":"expiring passphrase"}`)
	var p3 struct{ Proof string }
	decode(t, w.pub("POST", "/s/"+tok3+"/api/unlock", `{"passphrase":"expiring passphrase"}`), &p3)
	restore := db.SetClock(func() time.Time { return time.Now().Add(9 * time.Hour) })
	r := w.pub("GET", "/s/"+tok3+"/api/manifest", "", "X-Share-Unlock", p3.Proof)
	restore()
	if r.Code != 401 {
		t.Errorf("an old proof: %d", r.Code)
	}
	// the passphrase is stored hashed, never as given
	var ph string
	_ = w.s.DB.Read().QueryRow(`SELECT passphrase_hash FROM share_links WHERE id = ?`, id).Scan(&ph)
	var any string
	_ = w.s.DB.Read().QueryRow(`SELECT group_concat(COALESCE(passphrase_hash,''), ' ') FROM share_links`).Scan(&any)
	if strings.Contains(any, "correct horse") || strings.Contains(any, "expiring passphrase") || !strings.Contains(any, "$argon2id$") {
		t.Errorf("stored passphrases: %q", any)
	}
	// short passphrases are refused
	if r := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/shares", `{"passphrase":"short"}`); r.Code != 422 {
		t.Errorf("a short passphrase: %d", r.Code)
	}
}

func TestShareRateLimitsUseTheAddressBeforeTheTokenAndTheLinkAfter(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_SHARE_RATE_LIMIT": "40", "SCRIOBH_SHARE_LINK_RATE": "40"})
	id, tok := w.link(t, `{}`)
	now := time.Now()
	w.s.limiter.now = func() time.Time { return now }
	from := func(ip, path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = ip + ":1"
		rec := httptest.NewRecorder()
		w.s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	// Garbage tokens spend only the address's allowance: after the burst, 429, the same for every token.
	got := map[int]int{}
	for i := 0; i < 20; i++ {
		got[from("192.0.2.50", "/s/garbage"+string(rune('a'+i))+"/api/manifest")]++
	}
	if got[404] != 10 || got[429] != 10 {
		t.Errorf("garbage tokens from one address: %v (burst 10)", got)
	}
	// none of that touched the link's bucket
	w.s.limiter.mu.Lock()
	for k := range w.s.limiter.m {
		if strings.Contains(k, id) {
			t.Errorf("a garbage token created a bucket for a link: %s", k)
		}
	}
	w.s.limiter.mu.Unlock()
	// another address is not limited by it
	if c := from("192.0.2.51", "/s/"+tok+"/api/manifest"); c != 200 {
		t.Errorf("another address: %d", c)
	}
	// time refills the bucket
	now = now.Add(time.Minute)
	if c := from("192.0.2.50", "/s/garbage/api/manifest"); c != 404 {
		t.Errorf("after a minute: %d", c)
	}
	// the link's own bucket is shared by every address that has its token
	counts := map[int]int{}
	for i := 0; i < 30; i++ {
		counts[from("192.0.2."+string(rune('1'+i%9))+"0", "/s/"+tok+"/api/manifest")]++
	}
	if counts[429] == 0 || counts[200] == 0 {
		t.Errorf("a link's bucket across addresses: %v", counts)
	}
	// 429 carries Retry-After in the one error shape
	req := httptest.NewRequest("GET", "/s/"+tok+"/api/manifest", nil)
	req.RemoteAddr = "192.0.2.99:1"
	rec := httptest.NewRecorder()
	for i := 0; i < 60 && rec.Code != 429; i++ {
		rec = httptest.NewRecorder()
		w.s.Handler().ServeHTTP(rec, req)
	}
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"rate_limited"`) {
		t.Errorf("the limit answer: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
}

func TestGuestCommentsAreLimitedPerHourAndInTotal(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_SHARE_COMMENTS_PER_HOUR": "3"})
	_, tok := w.link(t, `{"scope":"comment"}`)
	now := time.Now()
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	n := 0
	say := func() *httptest.ResponseRecorder {
		w.s.limiter.now = func() time.Time { return now }
		n++
		return w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+w.doc2+`","body":"a thought `+strconv.Itoa(n)+`","name":"Sam"}`)
	}
	for i := 0; i < 3; i++ {
		if r := say(); r.Code != 201 {
			t.Fatalf("comment %d: %d %s", i, r.Code, r.Body)
		}
		now = now.Add(time.Second)
	}
	r := say()
	if r.Code != 429 || r.Header().Get("Retry-After") == "" {
		t.Fatalf("the fourth: %d %v", r.Code, r.Header())
	}
	now = now.Add(time.Hour + time.Minute)
	if r := say(); r.Code != 201 {
		t.Errorf("an hour later: %d %s", r.Code, r.Body)
	}
}

func TestOwnerOpsAreSessionOnlyAndNeverShowTheToken(t *testing.T) {
	w := newShareWorld(t, nil)
	id, tok := w.link(t, `{"label":"for Sam","scope":"comment","document_ids":["`+w.doc2+`"],"allow_download":true}`)
	secret := tok[strings.Index(tok, ".")+1:]
	base := "/api/v1/projects/" + w.pid + "/shares"
	// an API key of the owner, at any scope, cannot make, list or change a link
	key, _ := w.s.mintKey(t, w.owner, `{"name":"agent","scope":"write"}`)
	for _, c := range []struct{ method, path, body string }{{"POST", base, `{}`}, {"GET", base, ""}, {"PATCH", base + "/" + id, `{"label":"x"}`}, {"POST", base + "/" + id + "/revoke", ""}, {"GET", base + "/" + id + "/usage", ""}} {
		if rec := w.s.doKey(key, c.method, c.path, c.body); rec.Code != 403 {
			t.Errorf("%s %s with a key: %d", c.method, c.path, rec.Code)
		}
	}
	// a project key cannot reach them either; the editor and reviewer cannot
	for _, who := range []caller{w.editor, w.reviewer, w.stranger} {
		want := 403
		if who.name == "stranger" {
			want = 404
		}
		if rec := w.s.do(who, "GET", base, ""); rec.Code != want {
			t.Errorf("%s lists links: %d", who.name, rec.Code)
		}
	}
	// the list and the usage never carry the token, its hash, or the passphrase hash
	list := w.s.do(w.owner, "GET", base, "").Body.String()
	usage := w.s.do(w.owner, "GET", base+"/"+id+"/usage", "").Body.String()
	var hash string
	_ = w.s.DB.Read().QueryRow(`SELECT token_hash FROM share_links WHERE id = ?`, id).Scan(&hash)
	for what, body := range map[string]string{"list": list, "usage": usage} {
		if strings.Contains(body, secret) || strings.Contains(body, hash) || strings.Contains(body, "token") || strings.Contains(body, "passphrase_hash") {
			t.Errorf("%s shows a secret: %s", what, body)
		}
	}
	// only the SHA-256 of the secret is stored; the token is nowhere in the database, the events, the audit log or the logs
	dump := testDump(t, w)
	if strings.Contains(dump, secret) {
		t.Error("the secret half of a token is stored")
	}
	if len(hash) != 64 {
		t.Errorf("token_hash %q", hash)
	}
	if strings.Contains(w.logs.String(), secret) || strings.Contains(w.logs.String(), tok) {
		t.Error("a token reached the log")
	}
	// the answer of share.create is not kept for replay (a repeat of the key is a conflict, never the token again)
	req := httptest.NewRequest("POST", base, strings.NewReader(`{"label":"idem"}`))
	req.Header.Set("Idempotency-Key", "k1")
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(w.owner.cookie)
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Fatalf("with a key: %d %s", rec.Code, rec.Body)
	}
	req2 := httptest.NewRequest("POST", base, strings.NewReader(`{"label":"idem"}`))
	req2.Header.Set("Idempotency-Key", "k1")
	req2.Header.Set("Content-Type", "application/json")
	req2.AddCookie(w.owner.cookie)
	rec2 := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec2, req2)
	var first struct{ Token string }
	decode(t, rec, &first)
	if rec2.Code == 201 || strings.Contains(rec2.Body.String(), first.Token) {
		t.Errorf("a replayed share.create: %d %s", rec2.Code, rec2.Body)
	}
	// validation
	for name, body := range map[string]string{
		"a bad scope":       `{"scope":"edit"}`,
		"a past expiry":     `{"expires_at":"2001-01-01T00:00:00Z"}`,
		"a bad expiry":      `{"expires_at":"tomorrow"}`,
		"a note":            `{"document_ids":["` + w.noteDoc + `"]}`,
		"another project's": `{"document_ids":["` + w.otherDoc + `"]}`,
		"a long label":      `{"label":"` + strings.Repeat("x", 101) + `"}`,
		"bad ghosts":        `{"ghosts":"maybe"}`,
		"unknown field":     `{"role":"owner"}`,
		"a view limit":      `{"max_views":5}`,
	} {
		if r := w.s.do(w.owner, "POST", base, body); r.Code != 422 {
			t.Errorf("%s: %d %s", name, r.Code, r.Body)
		}
	}
	// update and revoke
	var up struct {
		Item struct{ Label, Scope, Status string }
	}
	decode(t, w.s.do(w.owner, "PATCH", base+"/"+id, `{"label":"renamed","scope":"read","expires_at":"`+db.NowTime().Add(48*time.Hour).Format(time.RFC3339)+`"}`), &up)
	if up.Item.Label != "renamed" || up.Item.Scope != "read" || up.Item.Status != "active" {
		t.Errorf("update: %+v", up)
	}
	if r := w.pub("POST", "/s/"+tok+"/api/comments", `{"document_id":"`+w.doc2+`","body":"x","name":"Sam"}`); r.Code != 403 {
		t.Errorf("a link changed to read still takes comments: %d", r.Code)
	}
	var rv struct{ Item struct{ Status string } }
	decode(t, w.s.do(w.owner, "POST", base+"/"+id+"/revoke", ""), &rv)
	decode(t, w.s.do(w.owner, "POST", base+"/"+id+"/revoke", ""), &rv)
	if rv.Item.Status != "revoked" {
		t.Errorf("revoke: %+v", rv)
	}
	if r := w.s.do(w.owner, "PATCH", base+"/"+id, `{"label":"x"}`); r.Code != 409 {
		t.Errorf("updating a revoked link: %d", r.Code)
	}
	if r := w.pub("GET", "/s/"+tok+"/api/manifest", ""); r.Code != 404 {
		t.Errorf("a revoked link: %d", r.Code)
	}
	// the number of live links is bounded
	for i := 0; i < share.MaxLinksPerProject; i++ {
		if r := w.s.do(w.owner, "POST", base, `{}`); r.Code != 201 {
			if r.Code != 409 {
				t.Fatalf("link %d: %d %s", i, r.Code, r.Body)
			}
			return
		}
	}
	t.Error("the number of live links is not bounded")
}

func testDump(t *testing.T, w *shareWorld) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{`SELECT group_concat(token_hash || COALESCE(label,''), ' ') FROM share_links`, `SELECT group_concat(payload_json, ' ') FROM events`, `SELECT group_concat(detail_json, ' ') FROM audit_log`,
		`SELECT group_concat(COALESCE(token_hash,'') , ' ') FROM share_links`, `SELECT group_concat(COALESCE(response_json, ''), ' ') FROM idempotency_keys`} {
		var s *string
		if err := w.s.DB.Read().QueryRow(q).Scan(&s); err == nil && s != nil {
			b.WriteString(*s)
		}
	}
	return b.String()
}

// The log never holds a token, even for a request that fails in the middleware (a cross-site POST).
func TestATokenNeverReachesALogLine(t *testing.T) {
	w := newShareWorld(t, nil)
	_, tok := w.link(t, `{"scope":"comment"}`)
	req := httptest.NewRequest("POST", "/s/"+tok+"/api/comments", strings.NewReader(`{"document_id":"x","body":"x","name":"x"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("cross-site: %d", rec.Code)
	}
	w.pub("GET", "/s/"+tok+"/api/manifest", "")
	w.pub("GET", "/s/"+tok+"x/api/manifest", "")
	if !strings.Contains(w.logs.String(), "cross-origin request refused") {
		t.Error("the refusal was not logged at all")
	}
	if strings.Contains(w.logs.String(), tok) || strings.Contains(w.logs.String(), tok[strings.Index(tok, ".")+1:]) || strings.Contains(w.logs.String(), "shl_") {
		t.Errorf("a token reached the log:\n%s", w.logs.String())
	}
}

// Looking up an id that does not exist does the same work as a wrong secret for one that does, and the
// secret is compared in constant time. The first is a property of the code (one comparison either way);
// the second is a bound on the difference in time between the two.
func TestWrongSecretAndUnknownIdTakeAboutTheSameTime(t *testing.T) {
	if shareRace {
		t.Skip("timing is not meaningful under the race detector")
	}
	w := newShareWorld(t, nil)
	id, tok := w.link(t, `{}`)
	secret := tok[strings.Index(tok, ".")+1:]
	known := id + "." + strings.Repeat("B", len(secret))
	unknown := "shl_00000000000000000000000000." + strings.Repeat("B", len(secret))
	measure := func(token string) time.Duration {
		var ds []time.Duration
		for i := 0; i < 400; i++ {
			start := time.Now()
			_, err := share.Authenticate(context.Background(), w.s.DB, nil, token)
			ds = append(ds, time.Since(start))
			if err != share.ErrNotFound {
				t.Fatalf("%v", err)
			}
		}
		// timing: not a budget: sorts the samples for their median
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2]
	}
	a, b := measure(known), measure(unknown)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		hi, lo := a, b
		// timing: not a budget: orders the two medians
		if lo > hi {
			hi, lo = lo, hi
		}
		if hi > lo*4 && hi-lo > 200*time.Microsecond {
			t.Errorf("wrong secret %v, unknown id %v: a difference worth noticing", a, b)
		}
	})
}

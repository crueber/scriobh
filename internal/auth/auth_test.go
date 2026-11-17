package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func setup(t *testing.T, env map[string]string) (*Users, *Sessions, *db.DB, *config.Config) {
	t.Helper()
	d := testutil.DB(t)
	cfg := testutil.Config(t, env)
	return &Users{DB: d, Cfg: cfg}, &Sessions{DB: d, Cfg: cfg, Key: []byte("0123456789abcdef0123456789abcdef")}, d, cfg
}

func claims(sub, email string, groups ...string) Claims {
	return Claims{Subject: sub, Email: email, EmailVerified: true, Name: "Test " + sub, Groups: groups}
}

func denied(err error) bool { var d *DeniedError; return errors.As(err, &d) }

func TestRoleFromGroups(t *testing.T) {
	cfg := testutil.Config(t, nil)
	cases := []struct {
		groups []string
		want   string
	}{
		{[]string{"scriobh-admins"}, ops.RoleAdmin},
		{[]string{"x", "scriobh-writers"}, ops.RoleWriter},
		{[]string{"scriobh-readers"}, ops.RoleReader},
		{[]string{"scriobh-readers", "scriobh-admins"}, ops.RoleAdmin}, // highest wins
		{[]string{"scriobh-team-a"}, ""},
		{nil, ""},
	}
	for _, c := range cases {
		if got := RoleFromGroups(cfg, c.groups); got != c.want {
			t.Errorf("%v -> %q, want %q", c.groups, got, c.want)
		}
	}
}

func TestSignInRules(t *testing.T) {
	u, _, _, _ := setup(t, nil)
	ctx := context.Background()

	// Unverified email, missing email and missing subject are refused.
	bad := claims("s1", "a@x.test", "scriobh-writers")
	bad.EmailVerified = false
	if _, err := u.SignIn(ctx, "iss", bad); !denied(err) {
		t.Errorf("unverified email: %v", err)
	}
	if _, err := u.SignIn(ctx, "iss", Claims{Subject: "s", EmailVerified: true, Groups: []string{"scriobh-writers"}}); !denied(err) {
		t.Errorf("no email: %v", err)
	}
	if _, err := u.SignIn(ctx, "iss", Claims{Email: "a@x.test", EmailVerified: true}); !denied(err) {
		t.Errorf("no subject: %v", err)
	}
	// No recognised group: refused with a helpful message, nothing created.
	if _, err := u.SignIn(ctx, "iss", claims("s2", "b@x.test", "other")); !denied(err) || !strings.Contains(err.Error(), "scriobh-writers") {
		t.Errorf("no group: %v", err)
	}

	// A first sign-in creates the user; a second finds the same one and refreshes the role.
	r1, err := u.SignIn(ctx, "iss", claims("s3", "C@X.test", "scriobh-readers"))
	if err != nil || r1.User.Role != ops.RoleReader || r1.User.Email != "c@x.test" {
		t.Fatalf("first: %+v %v", r1, err)
	}
	r2, err := u.SignIn(ctx, "iss", claims("s3", "c@x.test", "scriobh-writers"))
	if err != nil || r2.User.ID != r1.User.ID || r2.User.Role != ops.RoleWriter {
		t.Fatalf("second: %+v %v", r2, err)
	}
	// Losing the group on the IdP removes the role at the next sign-in.
	if _, err := u.SignIn(ctx, "iss", claims("s3", "c@x.test", "nothing")); !denied(err) {
		t.Errorf("demoted to nothing must be refused: %v", err)
	}

	// The same email under a different subject is a hard failure.
	if _, err := u.SignIn(ctx, "iss", claims("someone-else", "c@x.test", "scriobh-writers")); !denied(err) {
		t.Errorf("email bound to another subject must be refused: %v", err)
	}
	// The same subject under another issuer is a different identity.
	if _, err := u.SignIn(ctx, "other-issuer", claims("s3", "d@x.test", "scriobh-writers")); err != nil {
		t.Errorf("a different issuer is a different identity: %v", err)
	}
}

func TestLinkByEmailOnlyToUnboundAccount(t *testing.T) {
	u, _, d, _ := setup(t, nil)
	ctx := context.Background()
	// A pre-provisioned account with no subject.
	_, err := d.Writer().Exec(`INSERT INTO users(id, email, display_name, role, created_at, updated_at) VALUES ('usr_pre','pre@x.test','Pre','writer',?,?)`, db.Now(), db.Now())
	if err != nil {
		t.Fatal(err)
	}
	r, err := u.SignIn(ctx, "iss", claims("sub-pre", "pre@x.test", "scriobh-writers"))
	if err != nil || r.User.ID != "usr_pre" {
		t.Fatalf("link: %+v %v", r, err)
	}
	var sub string
	_ = d.Read().QueryRow(`SELECT oidc_sub FROM users WHERE id='usr_pre'`).Scan(&sub)
	if sub != "sub-pre" {
		t.Errorf("subject not bound: %q", sub)
	}
	if _, err := u.SignIn(ctx, "iss", claims("sub-thief", "pre@x.test", "scriobh-writers")); !denied(err) {
		t.Errorf("a second subject must not take over a bound account: %v", err)
	}
}

func TestAdminClaimClosesAfterFirstUse(t *testing.T) {
	u, _, _, _ := setup(t, map[string]string{"SCRIOBH_ADMIN_GROUP": ""})
	ctx := context.Background()
	first, err := u.SignIn(ctx, "iss", claims("f", "first@x.test")) // no groups at all
	if err != nil || !first.OfferAdminClaim {
		t.Fatalf("the first user must be offered the claim: %+v %v", first, err)
	}
	// A second user cannot claim, and without a group is refused.
	if _, err := u.SignIn(ctx, "iss", claims("g", "second@x.test")); !denied(err) {
		t.Errorf("second user without a group: %v", err)
	}
	second, err := u.SignIn(ctx, "iss", claims("g", "second@x.test", "scriobh-writers"))
	if err != nil || second.OfferAdminClaim || u.AdminClaimOpen(ctx, second.User.ID) {
		t.Errorf("only the first user may claim: %+v %v", second, err)
	}
	if err := u.ClaimAdmin(ctx, second.User.ID); !denied(err) {
		t.Errorf("second user claimed: %v", err)
	}
	if err := u.ClaimAdmin(ctx, first.User.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := u.Get(ctx, first.User.ID)
	if got.Role != ops.RoleAdmin {
		t.Errorf("role = %s", got.Role)
	}
	if err := u.ClaimAdmin(ctx, first.User.ID); !denied(err) {
		t.Errorf("claim must close after first use: %v", err)
	}
	// The claimed admin persists across sign-ins while no admin group exists.
	again, err := u.SignIn(ctx, "iss", claims("f", "first@x.test"))
	if err != nil || again.User.Role != ops.RoleAdmin {
		t.Errorf("claimed admin lost on next sign-in: %+v %v", again, err)
	}
}

func TestNoClaimWhenAdminGroupConfigured(t *testing.T) {
	u, _, _, _ := setup(t, nil)
	r, err := u.SignIn(context.Background(), "iss", claims("a", "a@x.test", "scriobh-writers"))
	if err != nil || r.OfferAdminClaim {
		t.Fatalf("%+v %v", r, err)
	}
	if err := u.ClaimAdmin(context.Background(), r.User.ID); !denied(err) {
		t.Error("claims are only possible with no admin group configured")
	}
}

func TestSessionLifecycle(t *testing.T) {
	u, s, d, cfg := setup(t, map[string]string{"SCRIOBH_SESSION_IDLE_MINUTES": "10"})
	ctx := context.Background()
	r, _ := u.SignIn(ctx, "iss", claims("x", "x@x.test", "scriobh-writers"))

	tok, _, err := s.Create(ctx, r.User.ID, "ua", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// Only a hash is stored.
	var n int
	_ = d.Read().QueryRow(`SELECT count(*) FROM sessions WHERE token_hash = ?`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("the raw token must never be stored")
	}
	p, info, err := s.Lookup(ctx, tok)
	if err != nil || p.UserID != r.User.ID || p.Role != ops.RoleWriter || info.ID == "" {
		t.Fatalf("lookup: %+v %v", p, err)
	}
	if _, _, err := s.Lookup(ctx, tok+"x"); err != ErrNoSession {
		t.Errorf("wrong token: %v", err)
	}
	if !s.CheckCSRF(info.ID, s.CSRFToken(info.ID)) || s.CheckCSRF(info.ID, "nope") || s.CheckCSRF("other", s.CSRFToken(info.ID)) {
		t.Error("CSRF tokens must bind to the session")
	}

	// Idle expiry.
	restore := db.SetClock(func() time.Time { return time.Now().Add(11 * time.Minute) })
	if _, _, err := s.Lookup(ctx, tok); err != ErrNoSession {
		t.Errorf("idle session must expire: %v", err)
	}
	restore()
	// Absolute expiry.
	restore = db.SetClock(func() time.Time { return time.Now().Add(time.Duration(cfg.SessionHours)*time.Hour + time.Minute) })
	if _, _, err := s.Lookup(ctx, tok); err != ErrNoSession {
		t.Errorf("session must expire at the absolute limit: %v", err)
	}
	restore()

	// Revocation, single and all.
	if _, _, err := s.Lookup(ctx, tok); err != nil {
		t.Fatalf("still valid: %v", err)
	}
	if err := s.Revoke(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Lookup(ctx, tok); err != ErrNoSession {
		t.Errorf("revoked: %v", err)
	}
	t1, _, _ := s.Create(ctx, r.User.ID, "", "")
	t2, _, _ := s.Create(ctx, r.User.ID, "", "")
	_ = s.RevokeAll(ctx, r.User.ID)
	for _, tk := range []string{t1, t2} {
		if _, _, err := s.Lookup(ctx, tk); err != ErrNoSession {
			t.Error("sign out everywhere must end every session")
		}
	}
	// A deleted user's session stops working.
	t3, _, _ := s.Create(ctx, r.User.ID, "", "")
	_, _ = d.Writer().Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), r.User.ID)
	if _, _, err := s.Lookup(ctx, t3); err != ErrNoSession {
		t.Error("deleted users must lose their sessions")
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	_, s, _, _ := setup(t, nil)
	rec := httptest.NewRecorder()
	s.SetCookie(rec, "tok", time.Now().Add(time.Hour))
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.Path != "/" {
		t.Errorf("http base URL cookie wrong: %+v", c)
	}
	_, s2, _, _ := setup(t, map[string]string{"SCRIOBH_BASE_URL": "https://write.example.com", "SCRIOBH_OIDC_STUB": "0",
		"SCRIOBH_OIDC_ISSUER": "https://i", "SCRIOBH_OIDC_CLIENT_ID": "a", "SCRIOBH_OIDC_CLIENT_SECRET": "b"})
	rec = httptest.NewRecorder()
	s2.SetCookie(rec, "tok", time.Now().Add(time.Hour))
	if c := rec.Result().Cookies()[0]; !c.Secure {
		t.Error("an https base URL must set Secure")
	}
}

func TestSafeNext(t *testing.T) {
	ok := map[string]string{"/": "/", "/p/atlas": "/p/atlas", "/p/a?x=1": "/p/a?x=1"}
	for in, want := range ok {
		if got := SafeNext(in); got != want {
			t.Errorf("SafeNext(%q) = %q", in, got)
		}
	}
	for _, in := range []string{"", "https://evil.example", "//evil.example", "/\\evil.example", "javascript:alert(1)", "evil", "///x"} {
		if got := SafeNext(in); got != "/" {
			t.Errorf("SafeNext(%q) = %q, want /", in, got)
		}
	}
}

func TestFlowCookieIsSignedAndStateChecked(t *testing.T) {
	o := NewOIDC(testutil.Config(t, nil), []byte("0123456789abcdef0123456789abcdef"))
	good := o.sign([]byte(`{"s":"state","n":"nonce","v":"ver","x":"/","e":99999999999}`))
	if _, ok := o.verify(good); !ok {
		t.Fatal("a freshly signed value must verify")
	}
	tampered := strings.Replace(good, good[:6], "AAAAAA", 1)
	if _, ok := o.verify(tampered); ok {
		t.Error("a tampered cookie must not verify")
	}
	other := NewOIDC(testutil.Config(t, nil), []byte("another-key-another-key-another!!"))
	if _, ok := other.verify(good); ok {
		t.Error("a cookie signed with another key must not verify")
	}
	// A callback with no cookie, a bad cookie, or the wrong state is refused with DeniedError.
	for name, mutate := range map[string]func(*http.Request){
		"no cookie":  func(r *http.Request) {},
		"bad cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: FlowCookie, Value: "garbage"}) },
		"wrong state": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: FlowCookie, Value: good})
		},
	} {
		req := httptest.NewRequest("GET", "/auth/oidc/callback?code=c&state=different", nil)
		mutate(req)
		_, _, _, err := o.Complete(context.Background(), httptest.NewRecorder(), req)
		if !denied(err) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An error from the IdP is surfaced safely.
	req := httptest.NewRequest("GET", "/auth/oidc/callback?error=access_denied%0Ainjected", nil)
	_, _, _, err := o.Complete(context.Background(), httptest.NewRecorder(), req)
	if !denied(err) || strings.ContainsAny(err.Error(), "\n\r") {
		t.Errorf("idp error: %v", err)
	}
}

func TestParseBoolAcceptsStringTrue(t *testing.T) {
	for in, want := range map[string]bool{`true`: true, `"true"`: true, `false`: false, `"false"`: false, ``: false, `"yes"`: false, `1`: false} {
		if got := parseBool([]byte(in)); got != want {
			t.Errorf("parseBool(%s) = %v", in, got)
		}
	}
}

func TestInitialsAndAvatar(t *testing.T) {
	if Initials("Máire Nolan") != "MN" || Initials("") != "?" || Initials("cher") != "C" || Initials("a b c d") != "AB" {
		t.Error("initials wrong")
	}
	if AvatarColor("usr_1") != AvatarColor("usr_1") {
		t.Error("avatar colour must be deterministic")
	}
}

func TestClaimModeFirstUserIsNotLockedOut(t *testing.T) {
	u, _, _, _ := setup(t, map[string]string{"SCRIOBH_ADMIN_GROUP": ""})
	ctx := context.Background()
	first, err := u.SignIn(ctx, "iss", claims("f", "first@x.test"))
	if err != nil || !first.OfferAdminClaim {
		t.Fatal(first, err)
	}
	// They skip the claim screen and sign in again later.
	again, err := u.SignIn(ctx, "iss", claims("f", "first@x.test"))
	if err != nil || again.User.ID != first.User.ID || !again.OfferAdminClaim {
		t.Fatalf("the first user must be able to come back and still claim: %+v %v", again, err)
	}
}

func TestRemovedFromGroupsEndsSessions(t *testing.T) {
	u, s, _, _ := setup(t, nil)
	ctx := context.Background()
	r, _ := u.SignIn(ctx, "iss", claims("w", "w@x.test", "scriobh-writers"))
	tok, _, _ := s.Create(ctx, r.User.ID, "", "")
	if _, _, err := s.Lookup(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := u.SignIn(ctx, "iss", claims("w", "w@x.test", "other")); !denied(err) {
		t.Fatalf("expected a refusal: %v", err)
	}
	if _, _, err := s.Lookup(ctx, tok); err != ErrNoSession {
		t.Error("losing every group must end the user's sessions at once, not at cookie expiry")
	}
}

func TestRemovedAccountsAreRefusedNotRecreated(t *testing.T) {
	u, _, d, _ := setup(t, nil)
	ctx := context.Background()
	r, _ := u.SignIn(ctx, "iss", claims("gone", "gone@x.test", "scriobh-writers"))
	_, _ = d.Writer().Exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), r.User.ID)
	if _, err := u.SignIn(ctx, "iss", claims("gone", "gone@x.test", "scriobh-writers")); !denied(err) {
		t.Errorf("a removed account must be refused: %v", err)
	}
}

func TestParseGroupsToleratesOddShapes(t *testing.T) {
	for in, want := range map[string]int{`["a","b"]`: 2, `"a"`: 1, `null`: 0, `5`: 0, `{"a":1}`: 0, ``: 0} {
		if got := len(parseGroups([]byte(in))); got != want {
			t.Errorf("parseGroups(%s) = %d groups, want %d", in, got, want)
		}
	}
}

func TestClientIPUsesRightmostForwardedEntry(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.1:1234"
	r.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := ClientIP(r, true); got != "203.0.113.9" {
		t.Errorf("trusted proxy: %s", got)
	}
	if got := ClientIP(r, false); got != "10.0.0.1" {
		t.Errorf("untrusted proxy must ignore X-Forwarded-For: %s", got)
	}
}

func TestClaimModeStillRemovesAccessWhenGroupsAreRemoved(t *testing.T) {
	u, s, _, _ := setup(t, map[string]string{"SCRIOBH_ADMIN_GROUP": ""})
	ctx := context.Background()
	first, _ := u.SignIn(ctx, "iss", claims("f", "first@x.test"))
	if err := u.ClaimAdmin(ctx, first.User.ID); err != nil {
		t.Fatal(err)
	}
	w, err := u.SignIn(ctx, "iss", claims("w", "w@x.test", "scriobh-writers"))
	if err != nil {
		t.Fatal(err)
	}
	tok, _, _ := s.Create(ctx, w.User.ID, "", "")
	// The writer is removed from the IdP group: access ends, sessions with it.
	if _, err := u.SignIn(ctx, "iss", claims("w", "w@x.test")); !denied(err) {
		t.Fatalf("removing a writer from their group must remove access even in claim mode: %v", err)
	}
	if _, _, err := s.Lookup(ctx, tok); err != ErrNoSession {
		t.Error("their session must end")
	}
	// The claimed admin is unaffected.
	if r, err := u.SignIn(ctx, "iss", claims("f", "first@x.test")); err != nil || r.User.Role != ops.RoleAdmin {
		t.Errorf("the claimed admin must persist: %+v %v", r, err)
	}
}

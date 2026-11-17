package auth

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/desktopidp"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
)

// TestTheLocalProfileMayHaveNoAddress is the one exception to "a verified email address is required": the identity
// the embedded issuer of a desktop instance produces, and nobody else's.
func TestTheLocalProfileMayHaveNoAddress(t *testing.T) {
	u, _, d, _ := setup(t, map[string]string{"SCRIOBH_ADMIN_GROUP": ""})
	ctx := context.Background()
	local := Claims{Subject: "local-1", Name: "Ada"}

	// Every other issuer: refused as before, with and without the address.
	for _, iss := range []string{"https://idp.example.org", StubIdentityIssuer, "urn:scriobh:desktop-profile ", "URN:SCRIOBH:DESKTOP-PROFILE", ""} {
		if _, err := u.SignIn(ctx, iss, local); !denied(err) {
			t.Errorf("issuer %q with no address: %v, want a refusal", iss, err)
		}
	}
	res, err := u.SignIn(ctx, DesktopIdentityIssuer, local)
	if err != nil {
		t.Fatalf("the local profile with no address: %v", err)
	}
	if !res.OfferAdminClaim || res.User.Role != ops.RoleReader {
		t.Errorf("the first user: %+v, want the provisional role and the claim offered (the existing first-user rule)", res)
	}
	var email sql.NullString
	_ = d.Read().QueryRow(`SELECT email FROM users WHERE id = ?`, res.User.ID).Scan(&email)
	if email.Valid {
		t.Errorf("the address is stored as %q, want NULL (an empty string would take the unique index's one empty slot)", email.String)
	}
	if err := u.ClaimAdmin(ctx, res.User.ID); err != nil {
		t.Fatal(err)
	}
	// Signing in again keeps the account, the role and the absence of an address.
	res2, err := u.SignIn(ctx, DesktopIdentityIssuer, local)
	if err != nil || res2.User.ID != res.User.ID || res2.User.Role != ops.RoleAdmin || res2.OfferAdminClaim {
		t.Errorf("the second sign-in: %+v %v", res2, err)
	}
	// A second account with no address does not collide with the first (two NULLs).
	if _, err := u.SignIn(ctx, DesktopIdentityIssuer, Claims{Subject: "local-2", Name: "Other"}); !denied(err) {
		// Not the first user and in no group: refused by the group rule, not by a database error.
		t.Errorf("a second local subject: %v, want the ordinary group refusal", err)
	}
	// With an address the local profile obeys the ordinary rule: it must be marked verified.
	if _, err := u.SignIn(ctx, DesktopIdentityIssuer, Claims{Subject: "local-1", Name: "Ada", Email: "ada@example.org"}); !denied(err) {
		t.Errorf("the local profile with an unverified address: %v", err)
	}
	res3, err := u.SignIn(ctx, DesktopIdentityIssuer, Claims{Subject: "local-1", Name: "Ada", Email: "Ada@Example.org", EmailVerified: true})
	if err != nil || res3.User.Email != "ada@example.org" || res3.User.ID != res.User.ID {
		t.Errorf("the local profile with an address: %+v %v", res3, err)
	}
}

// TestDesktopRelyingPartyPointsAtTheEmbeddedIssuer: how the issuer, the client and the stored identity resolve.
func TestDesktopRelyingPartyPointsAtTheEmbeddedIssuer(t *testing.T) {
	_, _, _, cfg := setup(t, map[string]string{"SCRIOBH_DESKTOP": "1", "SCRIOBH_LAUNCH_TOKEN": strings.Repeat("ab", 32), "SCRIOBH_OIDC_STUB": "", "SCRIOBH_BASE_URL": ""})
	cfg.SetBoundAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5123})
	o := NewOIDC(cfg, []byte("0123456789abcdef0123456789abcdef"))
	if got := o.Issuer(); got != "http://127.0.0.1:5123"+desktopidp.MountPath {
		t.Errorf("issuer %q", got)
	}
	if id, secret := o.clientCreds(); id != desktopidp.ClientID || secret != "" {
		t.Errorf("client %q %q", id, secret)
	}
	// Switched to an external provider, the same Config resolves to it and to nothing of the embedded issuer.
	if err := cfg.ApplyDesktopExternal(struct {
		Issuer, ClientID, ClientSecret       string
		AdminGroup, WriterGroup, ReaderGroup string
	}{Issuer: "https://idp.example.org", ClientID: "c", ClientSecret: "s"}); err != nil {
		t.Fatal(err)
	}
	if id, secret := o.clientCreds(); o.Issuer() != "https://idp.example.org" || id != "c" || secret != "s" {
		t.Errorf("after the switch: %q %q", o.Issuer(), id)
	}
	// A relying party for a named provider ignores the configuration, and has its own flow cookie.
	f := NewOIDCFor(cfg, []byte("0123456789abcdef0123456789abcdef"), Provider{Issuer: "https://other.example", ClientID: "x"})
	if id, _ := f.clientCreds(); f.Issuer() != "https://other.example" || id != "x" || f.cookie == o.cookie {
		t.Errorf("the named provider's relying party: %q %q %q", f.Issuer(), id, f.cookie)
	}
}

// TestOwnsTellsTheFlowsApart: the shared callback belongs to the flow whose cookie names the state it carries.
func TestOwnsTellsTheFlowsApart(t *testing.T) {
	_, _, _, cfg := setup(t, nil)
	key := []byte("0123456789abcdef0123456789abcdef")
	link := NewOIDCFor(cfg, key, Provider{Issuer: "https://other.example", ClientID: "x"})
	cookie := link.sign([]byte(`{"s":"the-state","n":"n","v":"v","x":"/","e":9999999999}`))
	req := func(state, name, value string) *http.Request {
		r := httptest.NewRequest("GET", "/auth/oidc/callback?state="+state+"&code=c", nil)
		if name != "" {
			r.AddCookie(&http.Cookie{Name: name, Value: value})
		}
		return r
	}
	if !link.Owns(req("the-state", LinkFlowCookie, cookie)) {
		t.Error("the flow's own callback was not recognised")
	}
	for name, r := range map[string]*http.Request{
		"another state":          req("other", LinkFlowCookie, cookie),
		"no cookie":              req("the-state", "", ""),
		"the sign-in's cookie":   req("the-state", FlowCookie, cookie),
		"a cookie nobody signed": req("the-state", LinkFlowCookie, strings.Replace(cookie, ".", "x.", 1)),
		"no state":               req("", LinkFlowCookie, cookie),
	} {
		if link.Owns(r) {
			t.Errorf("%s: claimed", name)
		}
	}
	if NewOIDCFor(cfg, []byte("another-key-another-key-another-k"), Provider{}).Owns(req("the-state", LinkFlowCookie, cookie)) {
		t.Error("a cookie signed with another key was accepted")
	}
}

// TestThePinnedClientReachesOnlyItsOwnAddress: whatever host a URL names, the connection goes to the one address.
func TestThePinnedClientReachesOnlyItsOwnAddress(t *testing.T) {
	var hosts []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts = append(hosts, r.Host)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://elsewhere.example/", http.StatusFound)
		}
	}))
	defer ts.Close()
	addr := strings.TrimPrefix(ts.URL, "http://")
	var dials []string
	stop := netguard.Observe(func(_, a string) { dials = append(dials, a) })
	defer stop()
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128") // a proxy from the environment is never used
	c := PinnedClient(func() string { return addr })
	for _, u := range []string{ts.URL + "/a", "http://elsewhere.example/b", "http://169.254.169.254/latest/meta-data"} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatalf("%s: %v", u, err)
		}
		resp.Body.Close()
	}
	if len(hosts) != 3 {
		t.Fatalf("%d requests reached the one address, want all 3", len(hosts))
	}
	resp, err := c.Get(ts.URL + "/redirect")
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Errorf("a redirect was followed: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	if len(dials) == 0 {
		t.Error("the pinned client's connections were not observed")
	}
}

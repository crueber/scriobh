package desktopidp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// world is an issuer on a real loopback listener, with a clock the test moves and a launch that was redeemed.
type world struct {
	t       *testing.T
	ts      *httptest.Server
	srv     *Server
	launch  *Launch
	cookie  string
	mu      sync.Mutex
	now     time.Time
	profile *Profile
	refused []string
}

func (w *world) clock() time.Time { w.mu.Lock(); defer w.mu.Unlock(); return w.now }
func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
}
func (w *world) callback() string { return w.ts.URL + "/auth/oidc/callback" }

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, now: time.Now(), profile: &Profile{Subject: "local-abc", Name: "Nuala Ní Dhomhnaill", Email: "nuala@example.com"}}
	key, err := LoadKey(t.TempDir(), "test", w.now)
	if err != nil {
		t.Fatal(err)
	}
	w.launch = NewLaunch(testToken)
	c, ok := w.launch.Redeem(testToken)
	if !ok {
		t.Fatal("the launch token was not accepted")
	}
	w.cookie = c
	w.srv, err = New(Config{
		Base: func() string { return w.ts.URL }, RedirectURI: w.callback, Launch: w.launch, Key: key, Now: w.clock, ProfilePath: "/desktop/profile",
		Profile: func(context.Context) (Profile, bool, error) {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.profile == nil {
				return Profile{}, false, nil
			}
			return *w.profile, true, nil
		},
		Refused: func(endpoint, reason string) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.refused = append(w.refused, endpoint+" "+reason)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.ts = httptest.NewServer(http.StripPrefix(MountPath, w.srv.Handler()))
	t.Cleanup(w.ts.Close)
	return w
}

func challengeOf(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

const verifier = "a-verifier-of-forty-three-characters-or-more-0123456789"

// authQuery is a good authorization request; change changes it.
func (w *world) authQuery(change func(url.Values)) url.Values {
	q := url.Values{"client_id": {ClientID}, "redirect_uri": {w.callback()}, "response_type": {"code"}, "scope": {"openid profile email"},
		"state": {"state-1"}, "nonce": {"nonce-1"}, "code_challenge": {challengeOf(verifier)}, "code_challenge_method": {"S256"}}
	if change != nil {
		change(q)
	}
	return q
}

// get asks the authorization endpoint without following the redirect. edit changes the request (headers, cookie, host).
func (w *world) get(path string, edit func(*http.Request)) *http.Response {
	w.t.Helper()
	req, err := http.NewRequest("GET", w.ts.URL+MountPath+path, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: LaunchCookie, Value: w.cookie})
	if edit != nil {
		edit(req)
	}
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	w.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func (w *world) authorize(change func(url.Values), edit func(*http.Request)) *http.Response {
	return w.get("/authorize?"+w.authQuery(change).Encode(), edit)
}

// code runs a good authorization request and returns its code.
func (w *world) code() string {
	w.t.Helper()
	resp := w.authorize(nil, nil)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || loc.Query().Get("code") == "" || loc.Query().Get("state") != "state-1" {
		w.t.Fatalf("authorize: %d, Location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != w.callback() {
		w.t.Fatalf("redirected to %q, want the callback", got)
	}
	return loc.Query().Get("code")
}

// exchange posts to the token endpoint; change changes the form.
func (w *world) exchange(code string, change func(url.Values), edit func(*http.Request)) (int, map[string]any) {
	w.t.Helper()
	f := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {w.callback()}, "code_verifier": {verifier}, "client_id": {ClientID}}
	if change != nil {
		change(f)
	}
	req, _ := http.NewRequest("POST", w.ts.URL+MountPath+"/token", strings.NewReader(f.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if edit != nil {
		edit(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestARealClientSignsIn runs the flow with the libraries the application uses: discovery, the authorization
// request oauth2 builds, the exchange with the PKCE verifier, and go-oidc's verification of the ID token against the
// published key set.
func TestARealClientSignsIn(t *testing.T) {
	for _, style := range []oauth2.AuthStyle{oauth2.AuthStyleAutoDetect, oauth2.AuthStyleInParams, oauth2.AuthStyleInHeader} {
		w := newWorld(t)
		ctx := context.Background()
		p, err := oidc.NewProvider(ctx, w.srv.Issuer())
		if err != nil {
			t.Fatalf("discovery: %v", err)
		}
		ep := p.Endpoint()
		ep.AuthStyle = style
		cfg := &oauth2.Config{ClientID: ClientID, Endpoint: ep, RedirectURL: w.callback(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"}}
		v := oauth2.GenerateVerifier()
		dest := cfg.AuthCodeURL("the-state", oauth2.S256ChallengeOption(v), oidc.Nonce("the-nonce"))
		u, _ := url.Parse(dest)
		resp := w.get("/authorize?"+u.RawQuery, nil)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || loc.Query().Get("state") != "the-state" {
			t.Fatalf("style %v: authorize: %d %q", style, resp.StatusCode, resp.Header.Get("Location"))
		}
		tok, err := cfg.Exchange(ctx, loc.Query().Get("code"), oauth2.VerifierOption(v))
		if err != nil {
			t.Fatalf("style %v: exchange: %v", style, err)
		}
		raw, _ := tok.Extra("id_token").(string)
		id, err := p.Verifier(&oidc.Config{ClientID: ClientID}).Verify(ctx, raw)
		if err != nil {
			t.Fatalf("style %v: the ID token does not verify: %v", style, err)
		}
		var cl struct {
			Email         string   `json:"email"`
			EmailVerified bool     `json:"email_verified"`
			Name          string   `json:"name"`
			Groups        []string `json:"groups"`
		}
		if err := id.Claims(&cl); err != nil {
			t.Fatal(err)
		}
		if id.Subject != "local-abc" || id.Nonce != "the-nonce" || cl.Email != "nuala@example.com" || !cl.EmailVerified || cl.Name != "Nuala Ní Dhomhnaill" || cl.Groups == nil || len(cl.Groups) != 0 {
			t.Errorf("style %v: claims: sub %q nonce %q %+v", style, id.Subject, id.Nonce, cl)
		}
		if life := id.Expiry.Sub(id.IssuedAt); life != TokenTTL {
			t.Errorf("the ID token lives %v, want %v", life, TokenTTL)
		}
	}
}

func TestAProfileWithNoAddressHasNoVerifiedEmail(t *testing.T) {
	w := newWorld(t)
	w.profile.Email = ""
	status, out := w.exchange(w.code(), nil, nil)
	if status != 200 {
		t.Fatalf("exchange: %d %v", status, out)
	}
	parts := strings.Split(out["id_token"].(string), ".")
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var cl map[string]any
	_ = json.Unmarshal(payload, &cl)
	if _, has := cl["email"]; has || cl["email_verified"] != false {
		t.Errorf("claims with no address: %s", payload)
	}
	if g, ok := cl["groups"].([]any); !ok || len(g) != 0 {
		t.Errorf("groups must be an empty list, got %v", cl["groups"])
	}
}

// TestAuthorizeNeedsTheLaunchCookie is the rule that keeps everyone but the shell's own window out.
func TestAuthorizeNeedsTheLaunchCookie(t *testing.T) {
	w := newWorld(t)
	other := NewLaunch(testToken) // the same token in another process: another cookie
	otherCookie, _ := other.Redeem(testToken)
	for name, edit := range map[string]func(*http.Request){
		"no cookie":                 func(r *http.Request) { r.Header.Del("Cookie") },
		"an empty cookie":           func(r *http.Request) { r.Header.Set("Cookie", LaunchCookie+"=") },
		"the token itself":          func(r *http.Request) { r.Header.Set("Cookie", LaunchCookie+"="+testToken) },
		"another launch's cookie":   func(r *http.Request) { r.Header.Set("Cookie", LaunchCookie+"="+otherCookie) },
		"a cookie of another name":  func(r *http.Request) { r.Header.Set("Cookie", "scriobh_session="+w.cookie) },
		"the cookie with a suffix":  func(r *http.Request) { r.Header.Set("Cookie", LaunchCookie+"="+w.cookie+"0") },
		"the cookie as a parameter": func(r *http.Request) { r.Header.Del("Cookie"); r.URL.RawQuery += "&" + LaunchCookie + "=" + w.cookie },
	} {
		resp := w.authorize(nil, edit)
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d Location %q, want 403 and no redirect", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if otherCookie == w.cookie {
		t.Error("two launches with one token share a cookie value")
	}
	if strings.Contains(w.cookie, testToken) {
		t.Error("the cookie holds the token")
	}
}

// TestAuthorizeIsOnlyForThisInstancesOwnNavigation: a page of another site that fetches, frames or links to the
// authorization address is refused before anything is looked at, even if the browser sent the cookie.
func TestAuthorizeIsOnlyForThisInstancesOwnNavigation(t *testing.T) {
	w := newWorld(t)
	h := func(k, v string) func(*http.Request) { return func(r *http.Request) { r.Header.Set(k, v) } }
	refused := map[string]func(*http.Request){
		"cross-site":         h("Sec-Fetch-Site", "cross-site"),
		"same-site":          h("Sec-Fetch-Site", "same-site"), // another port of 127.0.0.1 is the same site
		"unknown site value": h("Sec-Fetch-Site", "weird"),
		"a cross-site fetch": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Sec-Fetch-Mode", "cors")
		},
		"a same-site frame": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
			r.Header.Set("Sec-Fetch-Dest", "iframe")
		},
		"a cross-site image": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Sec-Fetch-Dest", "image")
		},
		"a foreign origin":    h("Origin", "https://evil.example"),
		"the null origin":     h("Origin", "null"),
		"another port":        h("Origin", "http://127.0.0.1:1"),
		"localhost as origin": h("Origin", strings.Replace(w.ts.URL, "127.0.0.1", "localhost", 1)),
		"two origins": func(r *http.Request) {
			r.Header.Add("Origin", w.ts.URL)
			r.Header.Add("Origin", "https://evil.example")
		},
		"same-origin, but with a foreign Origin": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Origin", "https://evil.example")
		},
	}
	for name, edit := range refused {
		resp := w.authorize(nil, edit)
		if resp.StatusCode != http.StatusForbidden || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d Location %q, want 403 and no redirect", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	allowed := map[string]func(*http.Request){
		"no headers (not a browser)": nil,
		"typed or opened by the shell": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "none")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "document")
		},
		"from the app's own page": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "document")
		},
		"its own origin": h("Origin", w.ts.URL),
		// WebKit, when the application's service worker re-sends a navigation: seen in WKWebView, where it was refused.
		"a navigation the service worker re-sent (WebKit)": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "navigate")
			r.Header.Set("Sec-Fetch-Dest", "empty")
		},
		"the application's own fetch": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Sec-Fetch-Dest", "empty")
		},
	}
	for name, edit := range allowed {
		if resp := w.authorize(nil, edit); resp.StatusCode != http.StatusFound {
			t.Errorf("%s: %d, want the redirect with a code", name, resp.StatusCode)
		}
	}
}

// TestAForeignHostIsRefusedEverywhere is DNS rebinding: the page's own name in Host, on every endpoint.
func TestAForeignHostIsRefusedEverywhere(t *testing.T) {
	w := newWorld(t)
	_, port, _ := strings.Cut(strings.TrimPrefix(w.ts.URL, "http://"), ":")
	for _, host := range []string{"evil.example:" + port, "evil.example", "127.0.0.1", "127.0.0.1:1", "127.0.0.1." + "evil.example:" + port, "localhost.evil.example:" + port, "[::1]:" + port, "0.0.0.0:" + port} {
		for _, path := range []string{"/.well-known/openid-configuration", "/jwks", "/authorize?" + w.authQuery(nil).Encode()} {
			resp := w.get(path, func(r *http.Request) { r.Host = host })
			if resp.StatusCode != http.StatusMisdirectedRequest {
				t.Errorf("Host %q on %s: %d, want 421", host, path, resp.StatusCode)
			}
		}
		code := w.code()
		if status, _ := w.exchange(code, nil, func(r *http.Request) { r.Host = host }); status != http.StatusMisdirectedRequest {
			t.Errorf("Host %q on /token: %d, want 421", host, status)
		}
		if status, _ := w.exchange(code, nil, nil); status != 200 {
			t.Errorf("a request refused for its Host used the code up")
		}
	}
	if resp := w.get("/jwks", func(r *http.Request) { r.Host = "localhost:" + port }); resp.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("localhost with the bound port: %d, want 421 (it can resolve to ::1, where nothing listens)", resp.StatusCode)
	}
	if resp := w.get("/jwks", nil); resp.StatusCode != 200 {
		t.Errorf("the bound address itself: %d", resp.StatusCode)
	}
}

func TestTheRedirectAddressAndClientAreExact(t *testing.T) {
	w := newWorld(t)
	cb := w.callback()
	for name, change := range map[string]func(url.Values){
		"another client":       func(q url.Values) { q.Set("client_id", "scriobh-dev") },
		"no client":            func(q url.Values) { q.Del("client_id") },
		"no redirect":          func(q url.Values) { q.Del("redirect_uri") },
		"another path":         func(q url.Values) { q.Set("redirect_uri", cb+"/x") },
		"a query":              func(q url.Values) { q.Set("redirect_uri", cb+"?x=1") },
		"a fragment":           func(q url.Values) { q.Set("redirect_uri", cb+"#x") },
		"a trailing slash":     func(q url.Values) { q.Set("redirect_uri", cb+"/") },
		"localhost for the IP": func(q url.Values) { q.Set("redirect_uri", strings.Replace(cb, "127.0.0.1", "localhost", 1)) },
		"another host":         func(q url.Values) { q.Set("redirect_uri", "https://evil.example/auth/oidc/callback") },
		"userinfo":             func(q url.Values) { q.Set("redirect_uri", strings.Replace(cb, "http://", "http://evil.example@", 1)) },
		"upper case":           func(q url.Values) { q.Set("redirect_uri", strings.ToUpper(cb)) },
		"two redirects":        func(q url.Values) { q.Add("redirect_uri", "https://evil.example/") },
		"two clients":          func(q url.Values) { q.Add("client_id", ClientID) },
	} {
		resp := w.authorize(change, nil)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d Location %q, want 400 and no redirect", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

// TestABadRequestGoesBackAsAnError: with the client and redirect proved, the protocol's errors go to the application.
func TestABadRequestGoesBackAsAnError(t *testing.T) {
	w := newWorld(t)
	for name, c := range map[string]struct {
		change func(url.Values)
		want   string
	}{
		"no PKCE":            {func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
		"plain PKCE":         {func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		"no method":          {func(q url.Values) { q.Del("code_challenge_method") }, "invalid_request"},
		"a short challenge":  {func(q url.Values) { q.Set("code_challenge", "abc") }, "invalid_request"},
		"a padded challenge": {func(q url.Values) { q.Set("code_challenge", challengeOf(verifier)+"=") }, "invalid_request"},
		"no state":           {func(q url.Values) { q.Del("state") }, "invalid_request"},
		"no nonce":           {func(q url.Values) { q.Del("nonce") }, "invalid_request"},
		"two nonces":         {func(q url.Values) { q.Add("nonce", "x") }, "invalid_request"},
		"a huge state":       {func(q url.Values) { q.Set("state", strings.Repeat("s", maxParam+1)) }, "invalid_request"},
		"the implicit flow":  {func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
		"id_token":           {func(q url.Values) { q.Set("response_type", "id_token") }, "unsupported_response_type"},
		"no openid scope":    {func(q url.Values) { q.Set("scope", "profile") }, "invalid_scope"},
		"openid as a prefix": {func(q url.Values) { q.Set("scope", "openidx") }, "invalid_scope"},
	} {
		resp := w.authorize(c.change, nil)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || loc.Query().Get("error") != c.want || loc.Query().Get("code") != "" {
			t.Errorf("%s: %d %q, want a redirect with error=%s and no code", name, resp.StatusCode, resp.Header.Get("Location"), c.want)
		}
		if !strings.HasPrefix(resp.Header.Get("Location"), w.callback()+"?") {
			t.Errorf("%s: redirected to %q, not the callback", name, resp.Header.Get("Location"))
		}
	}
}

func TestTheFirstRunGoesToTheProfilePage(t *testing.T) {
	w := newWorld(t)
	w.mu.Lock()
	w.profile = nil
	w.mu.Unlock()
	resp := w.authorize(nil, nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/desktop/profile" {
		t.Fatalf("no profile: %d %q, want 303 to the profile page", resp.StatusCode, resp.Header.Get("Location"))
	}
	// Still behind the cookie: a stranger does not learn whether a profile exists.
	if resp := w.authorize(nil, func(r *http.Request) { r.Header.Del("Cookie") }); resp.StatusCode != http.StatusForbidden {
		t.Errorf("no profile and no cookie: %d, want 403", resp.StatusCode)
	}
}

func TestACodeIsShortLivedAndGoodOnce(t *testing.T) {
	w := newWorld(t)
	code := w.code()
	if status, _ := w.exchange(code, nil, nil); status != 200 {
		t.Fatalf("first exchange: %d", status)
	}
	if status, out := w.exchange(code, nil, nil); status != 400 || out["error"] != "invalid_grant" {
		t.Errorf("a code was accepted twice: %d %v", status, out)
	}
	code = w.code()
	w.advance(CodeTTL - time.Second)
	if status, _ := w.exchange(code, nil, nil); status != 200 {
		t.Errorf("a code one second inside its life was refused: %d", status)
	}
	code = w.code()
	w.advance(CodeTTL)
	if status, out := w.exchange(code, nil, nil); status != 400 || out["error"] != "invalid_grant" {
		t.Errorf("a code at the end of its life was accepted: %d %v", status, out)
	}
}

func TestTheExchangeIsBoundToTheRequest(t *testing.T) {
	w := newWorld(t)
	long := strings.Repeat("v", 129)
	for name, c := range map[string]struct {
		change func(url.Values)
		want   string
	}{
		"a wrong verifier":   {func(f url.Values) { f.Set("code_verifier", "another-verifier-of-forty-three-characters-0123456789") }, "invalid_grant"},
		"no verifier":        {func(f url.Values) { f.Del("code_verifier") }, "invalid_grant"},
		"the challenge":      {func(f url.Values) { f.Set("code_verifier", challengeOf(verifier)) }, "invalid_grant"},
		"a long verifier":    {func(f url.Values) { f.Set("code_verifier", long) }, "invalid_grant"},
		"another redirect":   {func(f url.Values) { f.Set("redirect_uri", w.callback()+"/x") }, "invalid_grant"},
		"no redirect":        {func(f url.Values) { f.Del("redirect_uri") }, "invalid_grant"},
		"an unknown code":    {func(f url.Values) { f.Set("code", "nope") }, "invalid_grant"},
		"no code":            {func(f url.Values) { f.Del("code") }, "invalid_request"},
		"two codes":          {func(f url.Values) { f.Add("code", "x") }, "invalid_request"},
		"another grant":      {func(f url.Values) { f.Set("grant_type", "refresh_token") }, "unsupported_grant_type"},
		"client credentials": {func(f url.Values) { f.Set("grant_type", "client_credentials") }, "unsupported_grant_type"},
	} {
		code := w.code()
		status, out := w.exchange(code, c.change, nil)
		if status != 400 || out["error"] != c.want || out["id_token"] != nil {
			t.Errorf("%s: %d %v, want 400 %s", name, status, out, c.want)
		}
		if c.want == "invalid_grant" && name != "an unknown code" {
			if status, _ := w.exchange(code, nil, nil); status == 200 {
				t.Errorf("%s: the code survived a failed exchange", name)
			}
		}
	}
}

// TestAWrongClientDoesNotUseACodeUp: the client is checked first, so the probe an OAuth library may make with
// another way of naming the client cannot burn the code.
func TestAWrongClientDoesNotUseACodeUp(t *testing.T) {
	w := newWorld(t)
	code := w.code()
	for name, c := range map[string]struct {
		change func(url.Values)
		edit   func(*http.Request)
	}{
		"another client":       {func(f url.Values) { f.Set("client_id", "other") }, nil},
		"no client":            {func(f url.Values) { f.Del("client_id") }, nil},
		"a secret in the form": {func(f url.Values) { f.Set("client_secret", "s") }, nil},
		"a secret in the header": {func(f url.Values) { f.Del("client_id") }, func(r *http.Request) {
			r.SetBasicAuth(ClientID, "s")
		}},
		"another client in the header": {func(f url.Values) { f.Del("client_id") }, func(r *http.Request) { r.SetBasicAuth("other", "") }},
	} {
		if status, out := w.exchange(code, c.change, c.edit); status != http.StatusUnauthorized || out["error"] != "invalid_client" {
			t.Errorf("%s: %d %v, want 401 invalid_client", name, status, out)
		}
	}
	if status, out := w.exchange(code, func(f url.Values) { f.Set("client_id", "other") }, func(r *http.Request) { r.SetBasicAuth(ClientID, "") }); status != 400 || out["error"] != "invalid_request" {
		t.Errorf("two different clients in one request: %d %v", status, out)
	}
	if status, _ := w.exchange(code, nil, nil); status != 200 {
		t.Errorf("the code was used up by requests from the wrong client: %d", status)
	}
}

func TestTheTokenEndpointRefusesAnotherSitesPage(t *testing.T) {
	w := newWorld(t)
	code := w.code()
	for name, edit := range map[string]func(*http.Request){
		"cross-site":       func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"same-site":        func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-site") },
		"a foreign origin": func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		if status, _ := w.exchange(code, nil, edit); status != http.StatusForbidden {
			t.Errorf("%s: %d, want 403", name, status)
		}
	}
	if status, _ := w.exchange(code, nil, nil); status != 200 {
		t.Error("a refused request used the code up")
	}
	// A body past the limit is refused, not read.
	if status, _ := w.exchange("x", func(f url.Values) { f.Set("pad", strings.Repeat("p", tokenBodyMax)) }, nil); status != 400 {
		t.Errorf("an oversized token request: %d, want 400", status)
	}
}

// TestNothingIsSharedWithAnotherOrigin: no endpoint answers with a CORS header, whatever is asked, and a preflight
// is not answered at all.
func TestNothingIsSharedWithAnotherOrigin(t *testing.T) {
	w := newWorld(t)
	check := func(label string, resp *http.Response) {
		for k := range resp.Header {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: header %s", label, k)
			}
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q", label, cc)
		}
	}
	for _, path := range []string{"/.well-known/openid-configuration", "/jwks", "/authorize?" + w.authQuery(nil).Encode(), "/nothing"} {
		check(path, w.get(path, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }))
		req, _ := http.NewRequest("OPTIONS", w.ts.URL+MountPath+path, nil)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Access-Control-Request-Method", "GET")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		check("OPTIONS "+path, resp)
		if resp.StatusCode < 400 {
			t.Errorf("OPTIONS %s: %d, want a refusal", path, resp.StatusCode)
		}
	}
	for _, m := range []string{"POST", "PUT", "DELETE"} {
		req, _ := http.NewRequest(m, w.ts.URL+MountPath+"/authorize?"+w.authQuery(nil).Encode(), nil)
		req.AddCookie(&http.Cookie{Name: LaunchCookie, Value: w.cookie})
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s /authorize: %d, want 405", m, resp.StatusCode)
		}
	}
	resp, _ := http.Get(w.ts.URL + MountPath + "/token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /token: %d, want 405", resp.StatusCode)
	}
}

// TestTheCodeStoreIsBounded: a window that reloads the authorization address cannot grow the store; the newest
// codes are the ones kept.
func TestTheCodeStoreIsBounded(t *testing.T) {
	w := newWorld(t)
	var codes []string
	for i := 0; i < maxCodes+8; i++ {
		codes = append(codes, w.code())
		w.advance(time.Millisecond)
	}
	w.srv.mu.Lock()
	n := len(w.srv.codes)
	w.srv.mu.Unlock()
	if n != maxCodes {
		t.Fatalf("%d codes are kept, want %d", n, maxCodes)
	}
	if status, _ := w.exchange(codes[0], nil, nil); status != 400 {
		t.Error("the oldest code outlived the bound")
	}
	if status, _ := w.exchange(codes[len(codes)-1], nil, nil); status != 200 {
		t.Error("the newest code was dropped")
	}
	w.advance(CodeTTL)
	w.code()
	w.srv.mu.Lock()
	n = len(w.srv.codes)
	w.srv.mu.Unlock()
	if n != 1 {
		t.Errorf("%d codes remain after all but one expired", n)
	}
}

func TestRefusalsAreReportedWithoutTheRequest(t *testing.T) {
	w := newWorld(t)
	w.authorize(func(q url.Values) { q.Set("state", "SECRET-STATE") }, func(r *http.Request) { r.Header.Del("Cookie") })
	w.exchange("SECRET-CODE", nil, nil)
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.refused) != 2 {
		t.Fatalf("refusals reported: %v", w.refused)
	}
	for _, r := range w.refused {
		if strings.Contains(r, "SECRET") || strings.Contains(r, "?") {
			t.Errorf("a refusal report carries part of the request: %q", r)
		}
	}
}

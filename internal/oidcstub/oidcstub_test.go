package oidcstub

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const cb = "http://127.0.0.1:1/auth/oidc/callback"

func newStub(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	var ts *httptest.Server
	s, err := New(Config{IssuerFunc: func() string { return ts.URL }, ClientID: "c", ClientSecret: "s", RedirectAllowed: func(u string) bool { return u == cb }})
	if err != nil {
		t.Fatal(err)
	}
	ts = httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func challenge(v string) string {
	h := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func authorize(t *testing.T, ts *httptest.Server, verifier string) string {
	t.Helper()
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.PostForm(ts.URL+"/authorize", url.Values{
		"client_id": {"c"}, "redirect_uri": {cb}, "state": {"st"}, "nonce": {"n"}, "response_type": {"code"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}, "sub": {"dev-writer"}})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != 302 || loc.Query().Get("state") != "st" {
		t.Fatalf("authorize: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	return loc.Query().Get("code")
}

func token(ts *httptest.Server, code, verifier, secret string) int {
	resp, _ := http.PostForm(ts.URL+"/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {cb},
		"code_verifier": {verifier}, "client_id": {"c"}, "client_secret": {secret}})
	resp.Body.Close()
	return resp.StatusCode
}

func TestPKCEIsEnforced(t *testing.T) {
	_, ts := newStub(t)
	if c := authorize(t, ts, "verifier-1"); token(ts, c, "wrong-verifier", "s") != 400 {
		t.Error("a wrong PKCE verifier must be refused")
	}
	if c := authorize(t, ts, "verifier-2"); token(ts, c, "verifier-2", "s") != 200 {
		t.Error("the right verifier must be accepted")
	}
}

func TestCodesAreSingleUseAndClientAuthenticated(t *testing.T) {
	_, ts := newStub(t)
	c := authorize(t, ts, "v")
	if token(ts, c, "v", "wrong-secret") != 401 {
		t.Error("a wrong client secret must be refused")
	}
	// The failed attempt did not consume the code, but a successful one does.
	c = authorize(t, ts, "v")
	if token(ts, c, "v", "s") != 200 || token(ts, c, "v", "s") != 400 {
		t.Error("an authorization code must be single use")
	}
}

func TestAuthorizeValidatesTheRequest(t *testing.T) {
	_, ts := newStub(t)
	q := func(m map[string]string) int {
		v := url.Values{"client_id": {"c"}, "redirect_uri": {cb}, "state": {"s"}, "response_type": {"code"},
			"code_challenge": {"x"}, "code_challenge_method": {"S256"}}
		for k, val := range m {
			v.Set(k, val)
		}
		resp, _ := http.Get(ts.URL + "/authorize?" + v.Encode())
		resp.Body.Close()
		return resp.StatusCode
	}
	if q(nil) != 200 {
		t.Error("a valid request shows the picker")
	}
	for name, m := range map[string]map[string]string{
		"unknown client": {"client_id": "evil"},
		"open redirect":  {"redirect_uri": "https://evil.example/cb"},
		"no PKCE":        {"code_challenge": ""},
		"plain PKCE":     {"code_challenge_method": "plain"},
		"implicit":       {"response_type": "token"},
		"no state":       {"state": ""},
	} {
		if q(m) != 400 {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestDiscoveryAndKeys(t *testing.T) {
	_, ts := newStub(t)
	resp, _ := http.Get(ts.URL + "/.well-known/openid-configuration")
	var b strings.Builder
	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	b.Write(buf[:n])
	for _, want := range []string{`"issuer":"` + ts.URL + `"`, `"code_challenge_methods_supported":["S256"]`, `/jwks`} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("discovery lacks %s:\n%s", want, b.String())
		}
	}
}

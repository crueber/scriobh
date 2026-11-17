package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/testutil"
)

// fakeIdP is a provider whose discovery document names the issuer it is told to, and whose token endpoint
// answers as configured.
type fakeIdP struct {
	srv         *httptest.Server
	reported    string // the issuer the discovery document names ("" = its own URL)
	tokenStatus int
	tokenBody   string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	f := &fakeIdP{tokenStatus: http.StatusOK, tokenBody: `{}`}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		iss := f.reported
		if iss == "" {
			iss = f.srv.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": iss, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "jwks_uri": f.srv.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.tokenStatus)
		_, _ = w.Write([]byte(f.tokenBody))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// serverFor builds a server that signs in through idp (never the stub) and returns it with its log.
func serverFor(t *testing.T, issuer string) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_OIDC_STUB": "0", "SCRIOBH_OIDC_ISSUER": issuer,
		"SCRIOBH_OIDC_CLIENT_ID": "scriobh-client", "SCRIOBH_OIDC_CLIENT_SECRET": "very-secret-value"})
	s, err := New(Deps{Cfg: cfg, DB: testutil.DB(t), Log: log})
	if err != nil {
		t.Fatal(err)
	}
	return s, &logs
}

func signInGet(s *Server, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A provider whose discovery document names another issuer than SCRIOBH_OIDC_ISSUER is the operator's
// mistake. It used to land in the generic "Something went wrong" page with only the log saying why.
func TestSignInIssuerMismatchShowsAnOperatorPage(t *testing.T) {
	idp := newFakeIdP(t)
	// A flow cookie from before the provider changed its mind, for the callback: a server that found the right
	// issuer started the sign-in.
	early, _ := serverFor(t, idp.srv.URL)
	started := signInGet(early, "/auth/login")
	if started.Code != http.StatusFound {
		t.Fatalf("login against a matching provider: %d %s", started.Code, started.Body)
	}
	idp.reported = "https://elsewhere.internal.example/realms/secret-realm"
	s, logs := serverFor(t, idp.srv.URL)
	for name, path := range map[string]string{"login": "/auth/login", "callback": "/auth/oidc/callback?code=c"} {
		t.Run(name, func(t *testing.T) {
			var cookies []*http.Cookie
			if name == "callback" {
				loc, _ := url.Parse(started.Header().Get("Location"))
				path += "&state=" + url.QueryEscape(loc.Query().Get("state"))
				cookies = started.Result().Cookies()
			}
			rec := signInGet(s, path, cookies...)
			body := rec.Body.String()
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status %d, want 500: %s", rec.Code, body)
			}
			if strings.Contains(body, "Something went wrong") {
				t.Error("still the generic error page")
			}
			for _, want := range []string{"Sign-in is not set up correctly", "SCRIOBH_OIDC_ISSUER", idp.srv.URL} {
				if !strings.Contains(body, want) {
					t.Errorf("page lacks %q: %s", want, body)
				}
			}
			for _, leak := range []string{"elsewhere.internal.example", "secret-realm", "very-secret-value", "oidc: issuer URL"} {
				if strings.Contains(body, leak) {
					t.Errorf("page leaks %q", leak)
				}
			}
		})
	}
	log := logs.String()
	for _, want := range []string{"configured_issuer=" + idp.srv.URL, "reported_issuer=https://elsewhere.internal.example/realms/secret-realm"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "very-secret-value") {
		t.Error("the client secret reached the log")
	}
}

// A provider that cannot be reached already had its own page; it is pinned here so it stays that way.
func TestSignInUnreachableIssuerShowsAnOperatorPage(t *testing.T) {
	idp := newFakeIdP(t)
	dead := idp.srv.URL
	idp.srv.Close()
	s, logs := serverFor(t, dead)
	rec := signInGet(s, "/auth/login")
	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway || !strings.Contains(body, "Cannot reach the identity provider") || !strings.Contains(body, dead) {
		t.Fatalf("status %d: %s", rec.Code, body)
	}
	if !strings.Contains(logs.String(), "identity provider unreachable") || !strings.Contains(logs.String(), "issuer="+dead) {
		t.Errorf("log: %s", logs)
	}
}

// A wrong client secret is refused by the token endpoint. It used to be the generic 403 "rejected the sign-in code"
// for the person and nothing helpful for the operator.
func TestSignInBadClientSecretShowsAnOperatorPage(t *testing.T) {
	idp := newFakeIdP(t)
	idp.tokenStatus = http.StatusUnauthorized
	idp.tokenBody = `{"error":"invalid_client","error_description":"Invalid client secret very-secret-value"}`
	s, logs := serverFor(t, idp.srv.URL)
	login := signInGet(s, "/auth/login")
	if login.Code != http.StatusFound {
		t.Fatalf("login: %d %s", login.Code, login.Body)
	}
	loc, err := url.Parse(login.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	rec := signInGet(s, "/auth/oidc/callback?code=abc&state="+url.QueryEscape(loc.Query().Get("state")), login.Result().Cookies()...)
	body := rec.Body.String()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500: %s", rec.Code, body)
	}
	for _, want := range []string{"Sign-in is not set up correctly", "SCRIOBH_OIDC_CLIENT_SECRET", idp.srv.URL} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q: %s", want, body)
		}
	}
	for _, leak := range []string{"very-secret-value", "Invalid client secret"} {
		if strings.Contains(body, leak) || strings.Contains(logs.String(), leak) {
			t.Errorf("%q must reach neither the page nor the log", leak)
		}
	}
	if !strings.Contains(logs.String(), "client_id=scriobh-client") || !strings.Contains(logs.String(), "provider_error=invalid_client") {
		t.Errorf("log lacks the client id and the provider's error code:\n%s", logs)
	}
}

// Another token-endpoint failure (not about the credentials) stays the person's "sign-in refused".
func TestSignInOtherTokenFailureStaysRefused(t *testing.T) {
	idp := newFakeIdP(t)
	idp.tokenStatus = http.StatusBadRequest
	idp.tokenBody = `{"error":"invalid_grant"}`
	s, _ := serverFor(t, idp.srv.URL)
	login := signInGet(s, "/auth/login")
	loc, _ := url.Parse(login.Header().Get("Location"))
	rec := signInGet(s, "/auth/oidc/callback?code=abc&state="+url.QueryEscape(loc.Query().Get("state")), login.Result().Cookies()...)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Sign-in refused") {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

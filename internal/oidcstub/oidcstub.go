// Package oidcstub is a minimal but real OIDC issuer for development and
// tests: discovery, authorization (with PKCE S256), token and JWKS endpoints,
// and three fixed users. It is not an auth bypass: the client runs the same
// discovery, state, nonce and PKCE code path as it does against production.
//
// It is mounted in-process by files without the "prod" build tag, and wrapped
// by cmd/scriobh-dev-idp. Production builds never include it.
package oidcstub

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// User is a fixed test identity.
type User struct {
	Sub    string
	Email  string
	Name   string
	Groups []string
}

// Users are the three fixed test users.
var Users = []User{
	{Sub: "dev-admin", Email: "admin@dev.scriobh", Name: "Ada Admin", Groups: []string{"scriobh-admins"}},
	{Sub: "dev-writer", Email: "writer@dev.scriobh", Name: "Wren Writer", Groups: []string{"scriobh-writers"}},
	{Sub: "dev-reader", Email: "reader@dev.scriobh", Name: "Rory Reader", Groups: []string{"scriobh-readers"}},
}

// Config configures an issuer.
type Config struct {
	// Issuer is the full issuer URL as the browser and the app both see it,
	// including any mount path, without a trailing slash.
	Issuer string
	// IssuerFunc, when set, wins over Issuer. The in-process mount uses it
	// because the issuer URL depends on the bound port.
	IssuerFunc   func() string
	ClientID     string
	ClientSecret string
	// RedirectAllowed reports whether a redirect URI is registered.
	RedirectAllowed func(uri string) bool
}

type authCode struct {
	user      User
	redirect  string
	challenge string
	nonce     string
	clientID  string
	expires   time.Time
}

// Server is the issuer.
type Server struct {
	cfg Config
	key *rsa.PrivateKey
	kid string

	mu    sync.Mutex
	codes map[string]authCode
}

// New creates an issuer with a fresh signing key.
func New(cfg Config) (*Server, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key.PublicKey.N.Bytes())
	return &Server{cfg: cfg, key: key, kid: hex.EncodeToString(sum[:8]), codes: map[string]authCode{}}, nil
}

func (s *Server) issuer() string {
	if s.cfg.IssuerFunc != nil {
		return strings.TrimRight(s.cfg.IssuerFunc(), "/")
	}
	return s.cfg.Issuer
}

// Handler serves the issuer's endpoints relative to its mount point; strip the
// mount prefix before calling it.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /authorize", s.authorizePage)
	mux.HandleFunc("POST /authorize", s.authorizeSubmit)
	mux.HandleFunc("POST /token", s.token)
	mux.HandleFunc("GET /jwks", s.jwks)
	return mux
}

func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                s.issuer(),
		"authorization_endpoint":                s.issuer() + "/authorize",
		"token_endpoint":                        s.issuer() + "/token",
		"jwks_uri":                              s.issuer() + "/jwks",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"claims_supported":                      []string{"sub", "email", "email_verified", "name", "groups"},
	})
}

func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	jwk := jose.JSONWebKey{Key: &s.key.PublicKey, KeyID: s.kid, Algorithm: "RS256", Use: "sig"}
	writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
}

type authReq struct {
	ClientID, RedirectURI, State, Nonce, Challenge, Method, ResponseType string
}

func readAuthReq(q url.Values) authReq {
	return authReq{
		ClientID: q.Get("client_id"), RedirectURI: q.Get("redirect_uri"), State: q.Get("state"), Nonce: q.Get("nonce"),
		Challenge: q.Get("code_challenge"), Method: q.Get("code_challenge_method"), ResponseType: q.Get("response_type"),
	}
}

// check validates the request. Problems with the client or redirect are shown
// to the developer, never redirected.
func (s *Server) check(a authReq) string {
	switch {
	case a.ClientID != s.cfg.ClientID:
		return "unknown client_id"
	case a.RedirectURI == "" || (s.cfg.RedirectAllowed != nil && !s.cfg.RedirectAllowed(a.RedirectURI)):
		return "redirect_uri is not registered"
	case a.ResponseType != "code":
		return "only response_type=code is supported"
	case a.Challenge == "" || a.Method != "S256":
		return "PKCE with code_challenge_method=S256 is required"
	case a.State == "":
		return "state is required"
	}
	return ""
}

func (s *Server) authorizePage(w http.ResponseWriter, r *http.Request) {
	a := readAuthReq(r.URL.Query())
	if msg := s.check(a); msg != "" {
		http.Error(w, "stub issuer: "+msg, http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Development sign-in</title></head><body><main><h1>Development sign-in</h1><p>This is the development identity provider. Pick a test user.</p>`)
	for _, u := range Users {
		b.WriteString(`<form method="post" action="` + html.EscapeString(s.issuer()+"/authorize") + `">`)
		for k, v := range map[string]string{
			"client_id": a.ClientID, "redirect_uri": a.RedirectURI, "state": a.State, "nonce": a.Nonce,
			"code_challenge": a.Challenge, "code_challenge_method": a.Method, "response_type": a.ResponseType, "sub": u.Sub,
		} {
			b.WriteString(`<input type="hidden" name="` + k + `" value="` + html.EscapeString(v) + `">`)
		}
		b.WriteString(`<p><button type="submit">Sign in as ` + html.EscapeString(u.Email) + `</button> <small>` + html.EscapeString(strings.Join(u.Groups, ", ")) + `</small></p></form>`)
	}
	b.WriteString(`</main></body></html>`)
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) authorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	a := readAuthReq(r.PostForm)
	if msg := s.check(a); msg != "" {
		http.Error(w, "stub issuer: "+msg, http.StatusBadRequest)
		return
	}
	var user *User
	for i := range Users {
		if Users[i].Sub == r.PostForm.Get("sub") {
			user = &Users[i]
		}
	}
	if user == nil {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	code := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	s.codes[code] = authCode{user: *user, redirect: a.RedirectURI, challenge: a.Challenge, nonce: a.Nonce, clientID: a.ClientID, expires: time.Now().Add(time.Minute)}
	for k, v := range s.codes { // sweep
		if time.Now().After(v.expires) {
			delete(s.codes, k)
		}
	}
	s.mu.Unlock()
	u, err := url.Parse(a.RedirectURI)
	if err != nil {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	q := u.Query()
	q.Set("code", code)
	q.Set("state", a.State)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := r.ParseForm(); err != nil {
		tokenErr(w, "invalid_request", http.StatusBadRequest)
		return
	}
	id, secret, ok := r.BasicAuth()
	if !ok {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != s.cfg.ClientID || subtle.ConstantTimeCompare([]byte(secret), []byte(s.cfg.ClientSecret)) != 1 {
		tokenErr(w, "invalid_client", http.StatusUnauthorized)
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		tokenErr(w, "unsupported_grant_type", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	ac, found := s.codes[r.PostForm.Get("code")]
	delete(s.codes, r.PostForm.Get("code")) // single use
	s.mu.Unlock()
	if !found || time.Now().After(ac.expires) || ac.clientID != id || ac.redirect != r.PostForm.Get("redirect_uri") {
		tokenErr(w, "invalid_grant", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(ac.challenge)) != 1 {
		tokenErr(w, "invalid_grant", http.StatusBadRequest)
		return
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: s.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", s.kid))
	if err != nil {
		tokenErr(w, "server_error", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	claims := struct {
		jwt.Claims
		Nonce         string   `json:"nonce,omitempty"`
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Name          string   `json:"name"`
		Groups        []string `json:"groups"`
	}{
		Claims: jwt.Claims{Issuer: s.issuer(), Subject: ac.user.Sub, Audience: jwt.Audience{id},
			IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(10 * time.Minute))},
		Nonce: ac.nonce, Email: ac.user.Email, EmailVerified: true, Name: ac.user.Name, Groups: ac.user.Groups,
	}
	idToken, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		tokenErr(w, "server_error", http.StatusInternalServerError)
		return
	}
	at := make([]byte, 24)
	_, _ = rand.Read(at)
	writeJSON(w, map[string]any{
		"access_token": base64.RawURLEncoding.EncodeToString(at), "token_type": "Bearer", "expires_in": 600, "id_token": idToken,
	})
}

func tokenErr(w http.ResponseWriter, code string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

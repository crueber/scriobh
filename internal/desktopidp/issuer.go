package desktopidp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// MountPath is where the issuer is mounted on the application's own listener. The issuer URL is the application's
// address followed by it.
const MountPath = "/desktop-idp"

// ClientID is the one client: the application itself, a public client (no secret; PKCE is the proof).
const ClientID = "scriobh-desktop"

// Lifetimes and limits.
const (
	// CodeTTL is how long an authorization code may wait to be exchanged.
	CodeTTL = 60 * time.Second
	// TokenTTL is the life of an ID token: it is verified at once and never kept.
	TokenTTL = 2 * time.Minute
	// maxCodes bounds the codes waiting to be exchanged. One person signs in at a time; the bound is for a page
	// that reloads the authorization address in a loop.
	maxCodes = 32
	// maxParam bounds state, nonce and the other values an authorization request carries.
	maxParam = 512
	// tokenBodyMax bounds a token request.
	tokenBodyMax = 16 << 10
)

// Config is what the issuer needs from the application.
type Config struct {
	// Base is the application's own address ("http://127.0.0.1:53124"), which depends on the bound port and so is
	// asked for at every request.
	Base func() string
	// RedirectURI is the application's callback, the only redirect address registered.
	RedirectURI func() string
	// Launch is the launch gate; the authorization endpoint answers only its cookie.
	Launch *Launch
	// Profile returns the local profile, and false when none has been made yet.
	Profile func(ctx context.Context) (Profile, bool, error)
	// ProfilePath is where the browser is sent to make the profile at the first run ("/desktop/profile").
	ProfilePath string
	// Key signs the ID tokens.
	Key *Key
	// Now is the clock; nil is real time.
	Now func() time.Time
	// Refused, when set, is told why a request was refused (a short fixed word, never anything the request carried).
	Refused func(endpoint, reason string)
}

type authCode struct {
	profile   Profile
	challenge string
	nonce     string
	expires   time.Time
}

// Server is the issuer.
type Server struct {
	cfg    Config
	signer jose.Signer

	mu    sync.Mutex
	codes map[string]authCode
}

// New makes an issuer.
func New(cfg Config) (*Server, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: cfg.Key.Private},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", cfg.Key.ID))
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, signer: signer, codes: map[string]authCode{}}, nil
}

// Issuer is the issuer URL.
func (s *Server) Issuer() string { return s.cfg.Base() + MountPath }

// Handler serves the issuer relative to its mount point; strip MountPath before calling it. Every answer is
// uncacheable, and none carries a CORS header: no page of another origin may read anything here.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /jwks", s.jwks)
	mux.HandleFunc("GET /authorize", s.authorize)
	mux.HandleFunc("POST /token", s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !HostAllowed(r.Host, s.cfg.Base()) {
			s.refuse(w, r, "host", http.StatusMisdirectedRequest)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) refuse(w http.ResponseWriter, r *http.Request, reason string, status int) {
	if s.cfg.Refused != nil {
		s.cfg.Refused(r.URL.Path, reason)
	}
	http.Error(w, http.StatusText(status), status)
}

func (s *Server) discovery(w http.ResponseWriter, r *http.Request) {
	iss := s.Issuer()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                iss,
		"authorization_endpoint":                iss + "/authorize",
		"token_endpoint":                        iss + "/token",
		"jwks_uri":                              iss + "/jwks",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"ES256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"code_challenge_methods_supported":      []string{"S256"},
		"scopes_supported":                      []string{"openid", "profile", "email"},
		"claims_supported":                      []string{"sub", "name", "email", "email_verified", "groups"},
	})
}

func (s *Server) jwks(w http.ResponseWriter, r *http.Request) {
	jwk := jose.JSONWebKey{Key: &s.cfg.Key.Private.PublicKey, KeyID: s.cfg.Key.ID, Algorithm: "ES256", Use: "sig"}
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk}})
}

// one returns the single value of a parameter. A parameter given twice is a request someone built to confuse a
// parser, and is refused.
func one(q url.Values, k string) (string, bool) {
	v := q[k]
	switch len(v) {
	case 0:
		return "", true
	case 1:
		return v[0], len(v[0]) <= maxParam
	}
	return "", false
}

// authorize is the authorization endpoint. In order: it is this instance's own window navigating (not another
// site's page, frame or fetch); the window holds the launch cookie; the client and its redirect address are the
// registered ones, exactly; then the request itself is checked, and its problems go back to the application the
// way the protocol says. There is no consent page and no password: the one local profile is approved for the one
// client. With no profile yet, the browser is sent to make one.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	base := s.cfg.Base()
	if !OwnNavigation(r, base) {
		s.refuse(w, r, "cross-site", http.StatusForbidden)
		return
	}
	if s.cfg.Launch == nil || !s.cfg.Launch.CookieOK(r) {
		s.refuse(w, r, "no-launch-cookie", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	get := func(k string) (string, bool) { return one(q, k) }
	clientID, ok1 := get("client_id")
	redirect, ok2 := get("redirect_uri")
	if !ok1 || !ok2 || clientID != ClientID || redirect == "" || redirect != s.cfg.RedirectURI() {
		// Never redirected: the address to redirect to is the thing that is wrong.
		s.refuse(w, r, "client-or-redirect", http.StatusBadRequest)
		return
	}
	state, ok3 := get("state")
	back := func(code string) {
		u, _ := url.Parse(redirect)
		v := u.Query()
		v.Set("error", code)
		if ok3 && state != "" {
			v.Set("state", state)
		}
		u.RawQuery = v.Encode()
		if s.cfg.Refused != nil {
			s.cfg.Refused(r.URL.Path, code)
		}
		http.Redirect(w, r, u.String(), http.StatusFound)
	}
	responseType, ok4 := get("response_type")
	challenge, ok5 := get("code_challenge")
	method, ok6 := get("code_challenge_method")
	nonce, ok7 := get("nonce")
	scope, ok8 := get("scope")
	switch {
	case !ok3 || !ok4 || !ok5 || !ok6 || !ok7 || !ok8:
		back("invalid_request")
		return
	case responseType != "code":
		back("unsupported_response_type")
		return
	case state == "" || nonce == "" || method != "S256" || !validChallenge(challenge):
		back("invalid_request")
		return
	case !hasScope(scope, "openid"):
		back("invalid_scope")
		return
	}
	p, ok, err := s.cfg.Profile(r.Context())
	if err != nil {
		back("server_error")
		return
	}
	if !ok {
		// The first run. The profile page sends the browser back to the application's sign-in, which begins a
		// new request: nothing of this one is carried through the form.
		http.Redirect(w, r, s.cfg.ProfilePath, http.StatusSeeOther)
		return
	}
	code, ok := s.newCode(authCode{profile: p, challenge: challenge, nonce: nonce})
	if !ok {
		back("temporarily_unavailable")
		return
	}
	u, _ := url.Parse(redirect)
	v := u.Query()
	v.Set("code", code)
	v.Set("state", state)
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// validChallenge reports whether c has the shape of an S256 challenge: 43 characters of unpadded base64url.
func validChallenge(c string) bool {
	if len(c) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil
}

func hasScope(scope, want string) bool {
	start := 0
	for i := 0; i <= len(scope); i++ {
		if i == len(scope) || scope[i] == ' ' {
			if scope[start:i] == want {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// newCode stores a code for CodeTTL. Expired codes are dropped first; when the store is still full the oldest goes.
func (s *Server) newCode(c authCode) (string, bool) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", false
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	now := s.cfg.Now()
	c.expires = now.Add(CodeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.codes {
		if !now.Before(v.expires) {
			delete(s.codes, k)
		}
	}
	if len(s.codes) >= maxCodes {
		keys := make([]string, 0, len(s.codes))
		for k := range s.codes {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return s.codes[keys[i]].expires.Before(s.codes[keys[j]].expires) })
		for _, k := range keys[:len(s.codes)-maxCodes+1] {
			delete(s.codes, k)
		}
	}
	s.codes[code] = c
	return code, true
}

// takeCode removes and returns a code: a code is good for one attempt, successful or not.
func (s *Server) takeCode(code string) (authCode, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[code]
	delete(s.codes, code)
	if !ok || !s.cfg.Now().Before(c.expires) {
		return authCode{}, false
	}
	return c, true
}

// token is the token endpoint: the authorization-code grant for the public client. The client is checked before the
// code is looked at, so a request this endpoint would never honour does not use a code up.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Pragma", "no-cache")
	if !OwnRequest(r, s.cfg.Base()) {
		s.refuse(w, r, "cross-site", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, tokenBodyMax)
	if err := r.ParseForm(); err != nil {
		s.tokenErr(w, r, "invalid_request", http.StatusBadRequest)
		return
	}
	f := r.PostForm
	clientID, secret := "", ""
	if id, pw, ok := r.BasicAuth(); ok {
		// oauth2 clients escape both halves; an id with nothing to escape is unchanged.
		clientID, secret = id, pw
		if v, okc := one(f, "client_id"); !okc || (v != "" && v != id) {
			s.tokenErr(w, r, "invalid_request", http.StatusBadRequest)
			return
		}
	} else {
		var okc bool
		if clientID, okc = one(f, "client_id"); !okc {
			s.tokenErr(w, r, "invalid_request", http.StatusBadRequest)
			return
		}
	}
	if v, ok := one(f, "client_secret"); !ok {
		s.tokenErr(w, r, "invalid_request", http.StatusBadRequest)
		return
	} else if v != "" {
		secret = v
	}
	if clientID != ClientID || secret != "" {
		// A public client has no secret; one that sends a secret is not this application.
		s.tokenErr(w, r, "invalid_client", http.StatusUnauthorized)
		return
	}
	grant, ok1 := one(f, "grant_type")
	code, ok2 := one(f, "code")
	redirect, ok3 := one(f, "redirect_uri")
	verifier, ok4 := one(f, "code_verifier")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		s.tokenErr(w, r, "invalid_request", http.StatusBadRequest)
		return
	}
	if grant != "authorization_code" {
		s.tokenErr(w, r, "unsupported_grant_type", http.StatusBadRequest)
		return
	}
	if code == "" {
		s.tokenErr(w, r, "invalid_request", http.StatusBadRequest)
		return
	}
	ac, found := s.takeCode(code)
	if !found || redirect != s.cfg.RedirectURI() || len(verifier) < 43 || len(verifier) > 128 {
		s.tokenErr(w, r, "invalid_grant", http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256([]byte(verifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(ac.challenge)) != 1 {
		s.tokenErr(w, r, "invalid_grant", http.StatusBadRequest)
		return
	}
	now := s.cfg.Now()
	claims := struct {
		jwt.Claims
		Nonce         string   `json:"nonce"`
		Name          string   `json:"name"`
		Email         string   `json:"email,omitempty"`
		EmailVerified bool     `json:"email_verified"`
		Groups        []string `json:"groups"`
	}{
		Claims: jwt.Claims{Issuer: s.Issuer(), Subject: ac.profile.Subject, Audience: jwt.Audience{ClientID},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now.Add(-5 * time.Second)), Expiry: jwt.NewNumericDate(now.Add(TokenTTL))},
		Nonce: ac.nonce, Name: ac.profile.Name, Email: ac.profile.Email, EmailVerified: ac.profile.Email != "", Groups: []string{},
	}
	idToken, err := jwt.Signed(s.signer).Claims(claims).Serialize()
	if err != nil {
		s.tokenErr(w, r, "server_error", http.StatusInternalServerError)
		return
	}
	at := make([]byte, 24)
	if _, err := rand.Read(at); err != nil {
		s.tokenErr(w, r, "server_error", http.StatusInternalServerError)
		return
	}
	// The access token opens nothing: there is no user-info endpoint. It is there because the protocol says so.
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": base64.RawURLEncoding.EncodeToString(at), "token_type": "Bearer", "expires_in": int(TokenTTL.Seconds()), "id_token": idToken,
	})
}

func (s *Server) tokenErr(w http.ResponseWriter, r *http.Request, code string, status int) {
	if s.cfg.Refused != nil {
		s.cfg.Refused(r.URL.Path, code)
	}
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/desktopidp"
	"github.com/crueber/scriobh/internal/netguard"
)

// FlowCookie carries state, nonce and the PKCE verifier across the redirect to
// the identity provider. It is signed, short-lived and scoped to /auth.
const FlowCookie = "scriobh_oidc"

const flowTTL = 10 * time.Minute

// StubIssuerPath is where the development issuer is mounted on the app's origin.
const StubIssuerPath = "/dev-idp"

// StubIdentityIssuer is the issuer string stored with development users. The
// stub's URL contains the bound port, which changes on every ephemeral-port
// start; identity must not.
const StubIdentityIssuer = "urn:scriobh:dev-idp"

// Stub client credentials, used only when the stub is enabled and no issuer is configured.
const (
	StubClientID     = "scriobh-dev"
	StubClientSecret = "scriobh-dev-secret"
)

// DesktopIdentityIssuer is the issuer string stored with the local profile of a desktop instance. The embedded
// issuer's URL contains the bound port, which changes at every launch; identity must not.
const DesktopIdentityIssuer = "urn:scriobh:desktop-profile"

// Provider names an identity provider outright, for a relying party that does not take it from the configuration:
// the provider a desktop instance's administrator is about to switch to (see NewOIDCFor).
type Provider struct {
	Issuer, ClientID, ClientSecret string
}

// OIDC is the relying party. Discovery is lazy: the first sign-in contacts the
// issuer and a failure is retried on the next attempt, so startup never
// depends on the network and an app can use an issuer mounted on itself.
type OIDC struct {
	Cfg  *config.Config
	Key  []byte
	HTTP *http.Client
	Log  *slog.Logger

	// fixed, when set, is the provider this relying party talks to whatever the configuration says, and cookie is
	// the name of its flow cookie (so its flow cannot be confused with the sign-in's).
	fixed  *Provider
	cookie string

	mu       sync.Mutex
	provider *oidc.Provider
	issuer   string
}

// NewOIDC returns a relying party.
func NewOIDC(cfg *config.Config, key []byte) *OIDC {
	return &OIDC{Cfg: cfg, Key: key, HTTP: observedClient(), cookie: FlowCookie}
}

// LinkFlowCookie carries the flow of NewOIDCFor's relying party.
const LinkFlowCookie = "scriobh_oidc_link"

// NewOIDCFor returns a relying party for one named provider. It runs the same discovery, state, nonce, PKCE and
// verification as the sign-in; its flow has its own cookie. Complete returns the provider's own issuer.
func NewOIDCFor(cfg *config.Config, key []byte, p Provider) *OIDC {
	return &OIDC{Cfg: cfg, Key: key, HTTP: observedClient(), fixed: &p, cookie: LinkFlowCookie}
}

// observedClient is the client that talks to an identity provider: the standard transport's settings, with every
// connection noted (see netguard.Note).
func observedClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = netguard.Observed((&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext)
	return &http.Client{Timeout: 10 * time.Second, Transport: tr}
}

// PinnedClient is the client a desktop instance uses to reach its own embedded issuer: whatever address a request
// names, the connection goes to addr() (the listener this process bound), with no proxy. It is the whole of the
// allowance for an issuer on plain http: one address, this process's own.
func PinnedClient(addr func() string) *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: netguard.Observed(func(ctx context.Context, network, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp", addr())
		}),
		MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 10 * time.Second, MaxResponseHeaderBytes: 16 << 10,
	}
	return &http.Client{Timeout: 10 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Issuer is the effective issuer URL.
func (o *OIDC) Issuer() string {
	if o.fixed != nil {
		return o.fixed.Issuer
	}
	if o.Cfg.DesktopEmbedded() {
		return o.Cfg.BaseURLString() + desktopidp.MountPath
	}
	if o.Cfg.OIDCIssuer != "" {
		return o.Cfg.OIDCIssuer
	}
	if o.Cfg.OIDCStub {
		return o.Cfg.BaseURLString() + StubIssuerPath
	}
	return ""
}

func (o *OIDC) clientCreds() (id, secret string) {
	if o.fixed != nil {
		return o.fixed.ClientID, o.fixed.ClientSecret
	}
	if o.Cfg.DesktopEmbedded() {
		return desktopidp.ClientID, "" // a public client: PKCE is the proof
	}
	if o.Cfg.OIDCClientID != "" {
		return o.Cfg.OIDCClientID, o.Cfg.OIDCClientSecret
	}
	if o.Cfg.OIDCStub {
		return StubClientID, StubClientSecret
	}
	return "", ""
}

// ErrIdPUnavailable means the identity provider could not be reached.
type ErrIdPUnavailable struct {
	Issuer string
	Err    error
}

func (e *ErrIdPUnavailable) Error() string {
	return fmt.Sprintf("identity provider unreachable at %s: %v", e.Issuer, e.Err)
}

// ErrIssuerMismatch means the provider answered but names a different issuer
// than SCRIOBH_OIDC_ISSUER: a configuration error, often a trailing slash.
type ErrIssuerMismatch struct {
	Configured string
	// Reported is the issuer the provider's discovery document names. It is for the log: it came from the
	// network, so a page never shows it.
	Reported string
	Err      error
}

func (e *ErrIssuerMismatch) Error() string {
	return fmt.Sprintf("SCRIOBH_OIDC_ISSUER %q does not match the issuer the provider reports: %v", e.Configured, e.Err)
}

// ErrClientRejected means the provider answered the token request but refused this application's client
// credentials (an unknown client id or a wrong secret): a configuration error for the operator.
type ErrClientRejected struct {
	Issuer   string
	ClientID string
	// Code is the OAuth error code the provider sent ("invalid_client"), for the log.
	Code string
	// Err is the exchange error. Its text carries the provider's response body, so it is kept out of logs and pages.
	Err error
}

func (e *ErrClientRejected) Error() string {
	return fmt.Sprintf("the identity provider at %s rejected the client credentials for %q (%s)", e.Issuer, e.ClientID, e.Code)
}

func (o *OIDC) discover(ctx context.Context) (*oidc.Provider, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	iss := o.Issuer()
	if iss == "" {
		return nil, errors.New("no OIDC issuer configured")
	}
	if o.provider != nil && o.issuer == iss {
		return o.provider, nil
	}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, o.HTTP), iss)
	if err != nil {
		var mism *oidc.IssuerMismatchError
		if errors.As(err, &mism) {
			return nil, &ErrIssuerMismatch{Configured: iss, Reported: mism.Discovered, Err: err}
		}
		return nil, &ErrIdPUnavailable{Issuer: iss, Err: err}
	}
	o.provider, o.issuer = p, iss
	return p, nil
}

func (o *OIDC) oauthConfig(p *oidc.Provider) *oauth2.Config {
	id, secret := o.clientCreds()
	ep := p.Endpoint()
	if o.fixed == nil && o.Cfg.DesktopEmbedded() {
		ep.AuthStyle = oauth2.AuthStyleInParams // the embedded issuer's client has no secret to put in a header
	}
	return &oauth2.Config{
		ClientID: id, ClientSecret: secret, Endpoint: ep,
		RedirectURL: o.Cfg.OIDCCallbackURL(), Scopes: []string{oidc.ScopeOpenID, "profile", "email"},
	}
}

type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"x"`
	Expires  int64  `json:"e"`
}

func (o *OIDC) sign(payload []byte) string {
	m := hmac.New(sha256.New, o.Key)
	m.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (o *OIDC) verify(v string) ([]byte, bool) {
	a, b, ok := strings.Cut(v, ".")
	if !ok {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(a)
	if err != nil {
		return nil, false
	}
	sig, err := base64.RawURLEncoding.DecodeString(b)
	if err != nil {
		return nil, false
	}
	m := hmac.New(sha256.New, o.Key)
	m.Write(payload)
	return payload, hmac.Equal(sig, m.Sum(nil))
}

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("auth: entropy source failed")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Owns reports whether a callback request belongs to a flow this relying party began: its flow cookie is there, is
// signed by this instance, and names the state the request carries. The callback address is shared by the sign-in and
// by NewOIDCFor's flow; this tells them apart without consuming anything.
func (o *OIDC) Owns(r *http.Request) bool {
	c, err := r.Cookie(o.cookie)
	if err != nil {
		return false
	}
	payload, ok := o.verify(c.Value)
	if !ok {
		return false
	}
	var st flowState
	if json.Unmarshal(payload, &st) != nil || st.State == "" {
		return false
	}
	return hmac.Equal([]byte(st.State), []byte(r.URL.Query().Get("state")))
}

// Begin starts the authorization-code + PKCE (S256) flow: it sets the flow
// cookie and returns the URL to redirect the browser to. next is a local path
// to return to afterwards.
func (o *OIDC) Begin(ctx context.Context, w http.ResponseWriter, next string) (string, error) {
	p, err := o.discover(ctx)
	if err != nil {
		return "", err
	}
	st := flowState{State: randToken(24), Nonce: randToken(24), Verifier: oauth2.GenerateVerifier(), Next: SafeNext(next), Expires: time.Now().Add(flowTTL).Unix()}
	raw, _ := json.Marshal(st)
	http.SetCookie(w, &http.Cookie{
		Name: o.cookie, Value: o.sign(raw), Path: "/auth", MaxAge: int(flowTTL.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: o.Cfg.Secure(),
	})
	return o.oauthConfig(p).AuthCodeURL(st.State,
		oauth2.S256ChallengeOption(st.Verifier),
		oidc.Nonce(st.Nonce)), nil
}

// Complete finishes the flow on the callback: it checks state, exchanges the
// code with the PKCE verifier, verifies the ID token and its nonce, and returns
// the claims. The flow cookie is cleared either way.
func (o *OIDC) Complete(ctx context.Context, w http.ResponseWriter, r *http.Request) (Claims, string, string, error) {
	http.SetCookie(w, &http.Cookie{Name: o.cookie, Value: "", Path: "/auth", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: o.Cfg.Secure()})

	if e := r.URL.Query().Get("error"); e != "" {
		return Claims{}, "", "", &DeniedError{"the identity provider refused the sign-in (" + sanitize(e) + ")"}
	}
	c, err := r.Cookie(o.cookie)
	if err != nil {
		return Claims{}, "", "", &DeniedError{"the sign-in took too long or cookies are blocked; try again"}
	}
	payload, ok := o.verify(c.Value)
	if !ok {
		return Claims{}, "", "", &DeniedError{"the sign-in could not be verified; try again"}
	}
	var st flowState
	if json.Unmarshal(payload, &st) != nil || time.Now().Unix() > st.Expires {
		return Claims{}, "", "", &DeniedError{"the sign-in took too long; try again"}
	}
	if !hmac.Equal([]byte(st.State), []byte(r.URL.Query().Get("state"))) {
		return Claims{}, "", "", &DeniedError{"the sign-in state did not match; try again"}
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		return Claims{}, "", "", &DeniedError{"the identity provider returned no code"}
	}
	p, err := o.discover(ctx)
	if err != nil {
		return Claims{}, "", "", err
	}
	hctx := oidc.ClientContext(ctx, o.HTTP)
	cfg := o.oauthConfig(p)
	tok, err := cfg.Exchange(hctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && (re.ErrorCode == "invalid_client" || (re.Response != nil && re.Response.StatusCode == http.StatusUnauthorized)) {
			code := re.ErrorCode
			if code == "" {
				code = "http 401"
			}
			return Claims{}, "", "", &ErrClientRejected{Issuer: o.Issuer(), ClientID: cfg.ClientID, Code: sanitize(code), Err: err}
		}
		o.warn("token exchange failed", err)
		return Claims{}, "", "", &DeniedError{"the identity provider rejected the sign-in code"}
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return Claims{}, "", "", &DeniedError{"the identity provider returned no ID token"}
	}
	id, err := p.Verifier(&oidc.Config{ClientID: cfg.ClientID}).Verify(hctx, rawID)
	if err != nil {
		o.warn("ID token verification failed", err)
		return Claims{}, "", "", &DeniedError{"the ID token could not be verified"}
	}
	if !hmac.Equal([]byte(id.Nonce), []byte(st.Nonce)) {
		return Claims{}, "", "", &DeniedError{"the ID token nonce did not match; try again"}
	}
	var raw struct {
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
		Name          string          `json:"name"`
		Groups        json.RawMessage `json:"groups"`
	}
	if err := id.Claims(&raw); err != nil {
		o.warn("ID token claims unreadable", err)
		return Claims{}, "", "", &DeniedError{"the ID token claims could not be read"}
	}
	cl := Claims{Subject: id.Subject, Email: raw.Email, Name: raw.Name, Groups: parseGroups(raw.Groups), EmailVerified: parseBool(raw.EmailVerified)}
	issuer := id.Issuer
	switch {
	case o.fixed != nil:
	case o.Cfg.DesktopEmbedded():
		issuer = DesktopIdentityIssuer
	case o.Cfg.OIDCStub && o.Cfg.OIDCIssuer == "":
		issuer = StubIdentityIssuer
	}
	return cl, issuer, st.Next, nil
}

// parseBool accepts true and "true", which some providers send as a string.
func parseBool(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return s == "true"
}

// parseGroups accepts the usual array and a single string, and ignores anything else.
func parseGroups(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return many
	}
	var one string
	if json.Unmarshal(raw, &one) == nil && one != "" {
		return []string{one}
	}
	return nil
}

func (o *OIDC) warn(msg string, err error) {
	if o.Log != nil {
		o.Log.Warn(msg, "issuer", o.Issuer(), "err", err)
	}
}

func sanitize(s string) string {
	if len(s) > 40 {
		s = s[:40]
	}
	return strings.Map(func(r rune) rune {
		if r < 32 || r > 126 {
			return -1
		}
		return r
	}, s)
}

// SafeNext keeps a post-login redirect on this site: a single-slash local path.
func SafeNext(next string) string {
	if next == "" || next[0] != '/' || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

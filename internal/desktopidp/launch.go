package desktopidp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
)

// LaunchCookie is the cookie the start address sets. It is HttpOnly, SameSite=Strict, for the whole origin and for
// the life of the window; never Secure, because the origin is http on loopback.
const LaunchCookie = "scriobh_desktop_launch"

// Launch is the launch gate of one process: the shell's token, good once, and the cookie it is exchanged for.
type Launch struct {
	tokenSum [sha256.Size]byte
	cookie   string
	formKey  []byte
	// proofKey answers the shell's challenge on /healthz (see Proof). It is derived from the token and is not the token.
	proofKey []byte
	// shellKey is what the shell presents with a request of its own, such as "save now" (see ShellOK). Derived like
	// proofKey, with its own label, so one is no use as the other.
	shellKey []byte

	mu   sync.Mutex
	used bool
}

// NewLaunch makes the gate for token. The cookie's value is an HMAC of a random salt under the token: it is different
// at every launch, cannot be worked out without the token, and does not reveal it.
func NewLaunch(token string) *Launch {
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		panic("desktopidp: entropy source failed")
	}
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("scriobh desktop launch cookie v1\x00"))
	m.Write(salt)
	cookie := m.Sum(nil)
	f := hmac.New(sha256.New, cookie)
	f.Write([]byte("scriobh desktop form key v1"))
	p := hmac.New(sha256.New, []byte(token))
	p.Write([]byte("scriobh desktop healthz key v1"))
	k := hmac.New(sha256.New, []byte(token))
	k.Write([]byte(ShellKeyLabel))
	return &Launch{tokenSum: sha256.Sum256([]byte(token)), cookie: hex.EncodeToString(cookie), formKey: f.Sum(nil), proofKey: p.Sum(nil), shellKey: k.Sum(nil)}
}

// ChallengeHeader and ProofHeader are how the shell asks `/healthz` whether the program that answers is the server it
// started, before it sends that program the launch token. The shell sends ChallengeHeader: a fresh nonce, 64
// lower-case hexadecimal characters. The answer carries ProofHeader: HMAC-SHA256 under k, of the nonce's 64 ASCII
// characters, as 64 lower-case hexadecimal characters, where k = HMAC-SHA256(key = the token's ASCII characters,
// message = "scriobh desktop healthz key v1"). Only a process that holds the token can answer, a nonce is never
// answered twice by anything but the same computation, and a proof recorded earlier answers no later challenge.
const (
	ChallengeHeader = "X-Scriobh-Challenge"
	ProofHeader     = "X-Scriobh-Proof"
)

// ShellHeader carries the shell's own key on a request the shell makes of the server (not the window): the lower-case
// hexadecimal of HMAC-SHA256(key = the token's ASCII characters, message = ShellKeyLabel). A page cannot send it (it never
// sees the token or the key, and a custom header on a cross-site request is refused without a preflight the server
// never answers); another program of the same user can read the token itself, which is outside what this defends.
const (
	ShellHeader   = "X-Scriobh-Shell"
	ShellKeyLabel = "scriobh desktop shell key v1"
)

// ShellOK reports whether r carries the shell's key, exactly once, in constant time.
func (l *Launch) ShellOK(r *http.Request) bool {
	v := r.Header.Values(ShellHeader)
	if len(v) != 1 {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(v[0]), []byte(hex.EncodeToString(l.shellKey))) == 1
}

// Proof answers a challenge. ok is false when the nonce is not exactly 64 lower-case hexadecimal characters: nothing
// is computed for anything else.
func (l *Launch) Proof(nonce string) (proof string, ok bool) {
	if len(nonce) != 64 {
		return "", false
	}
	for i := 0; i < len(nonce); i++ {
		c := nonce[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	m := hmac.New(sha256.New, l.proofKey)
	m.Write([]byte(nonce))
	return hex.EncodeToString(m.Sum(nil)), true
}

// Redeem exchanges the launch token for the cookie's value. It succeeds once: a second use of the right token is
// refused like a wrong one. There is no lockout: the token is at least 128 bits, so counting wrong guesses protects
// nothing, and a limit that any local program could use up would only keep the shell's own window out. The comparison
// takes the same time whatever is offered.
func (l *Launch) Redeem(candidate string) (cookie string, ok bool) {
	sum := sha256.Sum256([]byte(candidate))
	match := subtle.ConstantTimeCompare(sum[:], l.tokenSum[:]) == 1
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used || !match {
		return "", false
	}
	l.used = true
	return l.cookie, true
}

// SetCookie sets the launch cookie on a response.
func (l *Launch) SetCookie(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{Name: LaunchCookie, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// CookieOK reports whether r carries this launch's cookie.
func (l *Launch) CookieOK(r *http.Request) bool {
	c, err := r.Cookie(LaunchCookie)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(l.cookie)) == 1
}

// FormToken is the anti-forgery token of a form shown before anyone is signed in (the local profile). It is tied to
// this launch and to the form's purpose; the cookie it is derived from is never readable by a page.
func (l *Launch) FormToken(purpose string) string {
	m := hmac.New(sha256.New, l.formKey)
	m.Write([]byte(purpose))
	return hex.EncodeToString(m.Sum(nil))
}

// FormTokenOK checks a token FormToken made.
func (l *Launch) FormTokenOK(purpose, token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(l.FormToken(purpose))) == 1
}

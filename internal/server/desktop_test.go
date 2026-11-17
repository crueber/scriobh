package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/desktopidp"
	"github.com/crueber/scriobh/internal/domain/desktop"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// Desktop mode, in process: a server built as `scriobh serve` builds it with SCRIOBH_DESKTOP=1, on a real loopback
// listener (the application reaches its own issuer over HTTP), driven by a client that keeps cookies as a window does.

const deskToken = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
const deskKey = "desktop-test-secret-key-0123456789abcdef"

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type deskWorld struct {
	t    *testing.T
	dir  string
	d    *db.DB
	env  map[string]string
	logs *logBuf

	// cookieValue is the launch cookie firstRun's window was given.
	cookieValue string

	s    *Server
	cfg  *config.Config
	base string
	hs   *http.Server
}

// newDesk opens a data directory and starts a desktop server on it. env adds to or replaces the environment.
func newDesk(t *testing.T, env map[string]string) *deskWorld {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	w := &deskWorld{t: t, dir: dir, d: d, logs: &logBuf{}, env: map[string]string{
		"SCRIOBH_DESKTOP": "1", "SCRIOBH_LAUNCH_TOKEN": deskToken, "SCRIOBH_DATA_DIR": dir, "SCRIOBH_SECRET_KEY": deskKey, "SCRIOBH_RATE_LIMIT": "0"}}
	for k, v := range env {
		if v == "" {
			delete(w.env, k)
		} else {
			w.env[k] = v
		}
	}
	w.start()
	return w
}

// start builds and serves a server the way `scriobh serve` does in desktop mode: validate, make a pending switch,
// take up an external provider, bind loopback, build.
func (w *deskWorld) start() {
	w.t.Helper()
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := w.env[k]; return v, ok })
	if err != nil {
		w.t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		w.t.Fatal(err)
	}
	log := slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	key, err := cfg.EnsureSecretKey()
	if err != nil {
		w.t.Fatal(err)
	}
	st, err := desktop.ApplyPending(context.Background(), w.d, secrets.New(key), log)
	if err != nil {
		w.t.Fatal(err)
	}
	if st.Mode == desktop.ModeExternal {
		x, err := desktop.ExternalConfig(st, secrets.New(key))
		if err != nil {
			w.t.Fatal(err)
		}
		if err := cfg.ApplyDesktopExternal(x); err != nil {
			w.t.Fatal(err)
		}
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		w.t.Fatal(err)
	}
	cfg.SetBoundAddr(ln.Addr())
	s, err := New(Deps{Cfg: cfg, DB: w.d, Log: log})
	if err != nil {
		w.t.Fatal(err)
	}
	s.Webhooks.SetSender(offlineSender())
	w.s, w.cfg, w.base = s, cfg, "http://"+ln.Addr().String()
	w.hs = &http.Server{Handler: s.Handler()}
	go w.hs.Serve(ln)
	w.t.Cleanup(func() { w.hs.Close() })
}

// restart stops the server and starts another on the same data, with a new launch token as a new launch has.
func (w *deskWorld) restart(token string) {
	w.t.Helper()
	w.hs.Close()
	w.env["SCRIOBH_LAUNCH_TOKEN"] = token
	w.start()
}

// window is a client that behaves as the shell's window: cookies kept, redirects followed, no proxy.
type window struct {
	w  *deskWorld
	c  *http.Client
	ua string
}

func (w *deskWorld) window() *window {
	jar, _ := cookiejar.New(nil)
	return &window{w: w, c: &http.Client{Jar: jar, Transport: &http.Transport{Proxy: nil}, Timeout: 20 * time.Second}}
}

type page struct {
	status int
	url    string
	body   string
	header http.Header
}

func (win *window) do(method, path string, form url.Values, edit func(*http.Request)) page {
	win.w.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	u := path
	if strings.HasPrefix(path, "/") {
		u = win.w.base + path
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		win.w.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if edit != nil {
		edit(req)
	}
	resp, err := win.c.Do(req)
	if err != nil {
		win.w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return page{status: resp.StatusCode, url: resp.Request.URL.String(), body: string(b), header: resp.Header}
}

func (win *window) get(path string) page { return win.do("GET", path, nil, nil) }

// hop makes one request without following its redirect.
func (win *window) hop(method, path string, form url.Values, edit func(*http.Request)) page {
	old := win.c.CheckRedirect
	win.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { win.c.CheckRedirect = old }()
	return win.do(method, path, form, edit)
}

func (win *window) json(method, path, body string) (int, map[string]any) {
	win.w.t.Helper()
	req, _ := http.NewRequest(method, win.w.base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := win.c.Do(req)
	if err != nil {
		win.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

var hiddenRE = regexp.MustCompile(`name="_csrf" value="([^"]+)"`)

func csrfOf(t *testing.T, body string) string {
	t.Helper()
	m := hiddenRE.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the page has no form token:\n%.400s", body)
	}
	return m[1]
}

// firstRun opens the window at the start address and makes the profile; it ends signed in, on the library.
func (w *deskWorld) firstRun(name, email string) *window {
	w.t.Helper()
	win := w.window()
	p := win.get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"])
	if p.status != 200 || !strings.HasSuffix(p.url, "/desktop/profile") {
		w.t.Fatalf("the first run ended at %s (%d), want the profile page", p.url, p.status)
	}
	baseURL, _ := url.Parse(w.base)
	for _, c := range win.c.Jar.Cookies(baseURL) {
		if c.Name == desktopidp.LaunchCookie {
			w.cookieValue = c.Value
		}
	}
	p = win.do("POST", "/desktop/profile", url.Values{"_csrf": {csrfOf(w.t, p.body)}, "name": {name}, "email": {email}}, nil)
	if p.status != 200 || p.url != w.base+"/" || !strings.Contains(p.body, `class="signed-in`) {
		w.t.Fatalf("after the profile the window is at %s (%d), want the library, signed in", p.url, p.status)
	}
	return win
}

func (w *deskWorld) users() []map[string]string {
	w.t.Helper()
	rows, err := w.d.Read().Query(`SELECT id, COALESCE(oidc_issuer,''), COALESCE(oidc_sub,''), COALESCE(email,'<null>'), display_name, role FROM users WHERE is_agent = 0 AND deleted_at IS NULL ORDER BY created_at, id`)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]string
	for rows.Next() {
		var id, iss, sub, email, name, role string
		if err := rows.Scan(&id, &iss, &sub, &email, &name, &role); err != nil {
			w.t.Fatal(err)
		}
		out = append(out, map[string]string{"id": id, "issuer": iss, "sub": sub, "email": email, "name": name, "role": role})
	}
	return out
}

// TestDesktopFirstRunSignsInThroughTheEmbeddedIssuer is the whole first run: the start address, the launch cookie,
// the application's own sign-in redirecting to the issuer on the same listener, the profile form, the code exchange
// and go-oidc's verification over loopback, the session, and the first-user rule making the profile the administrator.
func TestDesktopFirstRunSignsInThroughTheEmbeddedIssuer(t *testing.T) {
	w := newDesk(t, nil)
	win := w.window()

	start := win.hop("GET", "/desktop/start?t="+deskToken, nil, nil)
	if start.status != http.StatusSeeOther || start.header.Get("Location") != "/" {
		t.Fatalf("start: %d Location %q, want 303 to /", start.status, start.header.Get("Location"))
	}
	var launch string
	for _, sc := range start.header.Values("Set-Cookie") {
		if strings.HasPrefix(sc, desktopidp.LaunchCookie+"=") {
			launch = sc
		}
	}
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(launch, want) {
			t.Errorf("the launch cookie %q lacks %s", launch, want)
		}
	}
	if strings.Contains(launch, "Secure") || strings.Contains(launch, "Max-Age") || strings.Contains(launch, deskToken) {
		t.Errorf("the launch cookie must be a session cookie, not Secure, and not the token: %q", launch)
	}
	if rp := start.header.Get("Referrer-Policy"); rp != "no-referrer" {
		t.Errorf("start: Referrer-Policy %q", rp)
	}

	// Each hop of the chain, so that the path is the real one and not a shortcut.
	hops := []struct{ from, to string }{
		{"/", "/auth/login"},
		{"/auth/login", w.base + "/desktop-idp/authorize?"},
	}
	for _, h := range hops {
		p := win.hop("GET", h.from, nil, nil)
		if p.status < 300 || p.status > 399 || !strings.HasPrefix(p.header.Get("Location"), h.to) {
			t.Fatalf("GET %s: %d Location %q, want a redirect to %s", h.from, p.status, p.header.Get("Location"), h.to)
		}
	}
	p := win.get("/")
	if !strings.HasSuffix(p.url, "/desktop/profile") {
		t.Fatalf("with no profile the sign-in ended at %s", p.url)
	}
	for _, want := range []string{"local profile", "no password", `name="name"`, `name="email"`, `autocomplete="name"`, `<label for="dp-name">`} {
		if !strings.Contains(p.body, want) {
			t.Errorf("the profile page lacks %q", want)
		}
	}
	lint(t, "desktop-profile", p.body)
	structure(t, "desktop-profile", p.body)

	p = win.do("POST", "/desktop/profile", url.Values{"_csrf": {csrfOf(t, p.body)}, "name": {"  Máire  Mhac an tSaoi "}, "email": {""}}, nil)
	if p.url != w.base+"/" || p.status != 200 || !strings.Contains(p.body, "Máire Mhac an tSaoi") {
		t.Fatalf("after the profile: %s %d", p.url, p.status)
	}

	us := w.users()
	if len(us) != 1 {
		t.Fatalf("users: %v", us)
	}
	u := us[0]
	if u["issuer"] != auth.DesktopIdentityIssuer || !strings.HasPrefix(u["sub"], "local-") || u["email"] != "<null>" || u["name"] != "Máire Mhac an tSaoi" {
		t.Errorf("the local profile's account: %v", u)
	}
	if u["role"] != ops.RoleAdmin {
		t.Errorf("the first user is %q, want the administrator by the first-user rule", u["role"])
	}
	var claimed string
	_ = w.d.Read().QueryRow(`SELECT value FROM settings WHERE key = 'admin_claimed'`).Scan(&claimed)
	if claimed != u["id"] {
		t.Errorf("admin_claimed is %q, want the profile's account: the claim was not the existing rule's", claimed)
	}
	for _, action := range []string{"desktop.profile.created", "auth.login", "auth.admin_claimed"} {
		var n int
		_ = w.d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = ?`, action).Scan(&n)
		if n != 1 {
			t.Errorf("audit rows for %s: %d, want 1", action, n)
		}
	}
	// The account works as any other: the administrator's pages open, and the menu offers this computer's pages.
	if p := win.get("/admin/users"); p.status != 200 {
		t.Errorf("/admin/users as the profile: %d", p.status)
	}
	if !strings.Contains(p.body, `href="/desktop/identity"`) {
		t.Error("the account menu does not offer the identity page")
	}

	// The next launch: a new token, a new window, the same person, no form.
	w.restart("ffeeddccbbaa99887766554433221100ffeeddccbbaa99887766554433221100")
	win2 := w.window()
	p = win2.get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"])
	if p.url != w.base+"/" || !strings.Contains(p.body, `class="signed-in`) {
		t.Fatalf("the second launch ended at %s, not signed in on the library", p.url)
	}
	if us2 := w.users(); len(us2) != 1 || us2[0]["id"] != u["id"] || us2[0]["role"] != ops.RoleAdmin {
		t.Errorf("the second launch changed the accounts: %v", us2)
	}
	// The first launch's cookie is a stranger's now: it is another launch's. (The first window itself is still signed
	// in: its session is its own and outlives a restart, as on any instance.)
	old := w.window()
	baseURL, _ := url.Parse(w.base)
	old.c.Jar.SetCookies(baseURL, []*http.Cookie{{Name: desktopidp.LaunchCookie, Value: strings.TrimPrefix(strings.SplitN(launch, ";", 2)[0], desktopidp.LaunchCookie+"=")}})
	if p := old.get("/auth/login"); p.status != http.StatusForbidden || !strings.Contains(p.body, "its own window") {
		t.Errorf("a sign-in with the last launch's cookie: %d", p.status)
	}

	logs := w.logs.String()
	for name, secret := range map[string]string{"the launch token": deskToken, "the second token": w.env["SCRIOBH_LAUNCH_TOKEN"], "the secret key": deskKey} {
		if strings.Contains(logs, secret) {
			t.Errorf("%s is in the log", name)
		}
	}
	if strings.Contains(logs, "desktop/start?") || strings.Contains(logs, "code=") {
		t.Error("a query string is in the log")
	}
}

// TestDesktopSignInIsNotHeldToTheSignInBucket: one person at one address signs out and in as often as they like (the
// per-address sign-in limit is for a provider that can be guessed at), while the general limit still holds.
func TestDesktopSignInIsNotHeldToTheSignInBucket(t *testing.T) {
	w := newDesk(t, map[string]string{"SCRIOBH_RATE_LIMIT": "600"})
	win := w.firstRun("Owner", "")
	for i := 0; i < 25; i++ {
		win.c.Jar, _ = cookiejar.New(nil) // a window whose session is gone but whose launch cookie is not
		baseURL, _ := url.Parse(w.base)
		win.c.Jar.SetCookies(baseURL, []*http.Cookie{{Name: desktopidp.LaunchCookie, Value: w.launchCookie()}})
		if p := win.get("/"); p.status != 200 || !strings.Contains(p.body, `class="signed-in`) {
			t.Fatalf("sign-in %d: %d at %s", i+1, p.status, p.url)
		}
	}
	// Outside desktop mode the sign-in bucket is as it was: the eleventh request in a burst is refused.
	s := newWebServer(t)
	refused := false
	for i := 0; i < 12; i++ {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/auth/claim-admin", nil))
		refused = refused || rec.Code == http.StatusTooManyRequests
	}
	if !refused {
		t.Error("outside desktop mode the sign-in bucket no longer refuses a burst")
	}
}

// launchCookie is the value of this launch's cookie, for a test that needs a second window of the same launch.
func (w *deskWorld) launchCookie() string { return w.cookieValue }

// TestDesktopProfileWithAnAddress: an address, when given, is the account's, and marked verified by the issuer.
func TestDesktopProfileWithAnAddress(t *testing.T) {
	w := newDesk(t, nil)
	w.firstRun("Seán Ó Ríordáin", "Sean@Example.org")
	if us := w.users(); len(us) != 1 || us[0]["email"] != "sean@example.org" || us[0]["role"] != ops.RoleAdmin {
		t.Fatalf("users: %v", us)
	}
}

func TestDesktopStartTokenIsExactAndOnce(t *testing.T) {
	w := newDesk(t, nil)
	stranger := w.window()
	unknown := stranger.get("/no/such/page")
	if unknown.status != 404 {
		t.Fatalf("an unknown path: %d", unknown.status)
	}
	same := func(label string, p page) {
		t.Helper()
		strip := func(s string) string { return regexp.MustCompile(`/[a-z/?=0-9A-Za-z&;%-]*`).ReplaceAllString(s, "") }
		if p.status != 404 || len(p.header.Values("Set-Cookie")) != 0 {
			t.Errorf("%s: %d, cookies %v; want the 404 of an unknown path and no cookie", label, p.status, p.header.Values("Set-Cookie"))
		}
		if strip(p.body) != strip(unknown.body) {
			t.Errorf("%s: the page differs from an unknown path's", label)
		}
	}
	same("no token", stranger.get("/desktop/start"))
	same("a wrong token", stranger.get("/desktop/start?t="+strings.Repeat("0", 64)))
	same("the token twice in the address", stranger.get("/desktop/start?t="+deskToken+"&t="+deskToken))
	// Three failures so far. The right token still works, once.
	win := w.window()
	if p := win.hop("GET", "/desktop/start?t="+deskToken, nil, nil); p.status != http.StatusSeeOther {
		t.Fatalf("the right token after three wrong ones: %d", p.status)
	}
	same("the right token a second time", w.window().get("/desktop/start?t="+deskToken))

	// Any number of wrong tokens, from anywhere, and the right one still works: there is no lockout to use up.
	w2 := newDesk(t, nil)
	for i := 0; i < 20; i++ {
		w2.window().get("/desktop/start?t=" + strings.Repeat("a", 64))
	}
	// A cross-site request, even with the right token, is refused and does not spend it.
	if p := w2.window().do("GET", "/desktop/start?t="+deskToken, nil, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); p.status != 404 || len(p.header.Values("Set-Cookie")) != 0 {
		t.Errorf("a cross-site request with the right token: %d, cookies %v; want 404 and none", p.status, p.header.Values("Set-Cookie"))
	}
	if p := w2.window().hop("GET", "/desktop/start?t="+deskToken, nil, nil); p.status != http.StatusSeeOther {
		t.Errorf("the right token after wrong guesses and a refused cross-site request: %d, want 303", p.status)
	}
	// POST and the other methods are not the start address.
	if p := w2.window().do("POST", "/desktop/start?t="+deskToken, url.Values{}, nil); p.status == http.StatusSeeOther || p.status == 200 {
		t.Errorf("POST /desktop/start: %d", p.status)
	}
}

// TestDesktopOnlyTheShellsWindowCanSignIn: everything that can reach the port but does not hold the launch cookie.
func TestDesktopOnlyTheShellsWindowCanSignIn(t *testing.T) {
	w := newDesk(t, nil)
	owner := w.firstRun("Owner", "")
	stranger := w.window()

	if p := stranger.get("/"); p.status != 200 || strings.Contains(p.body, "signed-in") || !strings.Contains(p.body, `href="/auth/login"`) {
		t.Errorf("a stranger at /: %d", p.status)
	}
	if p := stranger.get("/auth/login"); p.status != http.StatusForbidden || !strings.Contains(p.body, "its own window") {
		t.Errorf("a stranger's sign-in: %d, want 403 and the words about the window", p.status)
	}
	// Straight at the issuer, with a request as good as the application's own.
	q := url.Values{"client_id": {desktopidp.ClientID}, "redirect_uri": {w.base + "/auth/oidc/callback"}, "response_type": {"code"}, "scope": {"openid"},
		"state": {"s"}, "nonce": {"n"}, "code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}}
	authz := "/desktop-idp/authorize?" + q.Encode()
	if p := stranger.hop("GET", authz, nil, nil); p.status != http.StatusForbidden || p.header.Get("Location") != "" {
		t.Errorf("a stranger at the issuer: %d %q", p.status, p.header.Get("Location"))
	}
	// The owner's window, but asked for by another site's page: refused although the cookie is there.
	for name, edit := range map[string]func(*http.Request){
		"a cross-site navigation": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"a fetch from another port of this computer": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "same-site")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set("Origin", "http://127.0.0.1:1")
		},
		"a frame on another site's page": func(r *http.Request) {
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			r.Header.Set("Sec-Fetch-Dest", "iframe")
		},
	} {
		if p := owner.hop("GET", authz, nil, edit); p.status != http.StatusForbidden {
			t.Errorf("%s reached the issuer: %d", name, p.status)
		}
	}
	if p := owner.hop("GET", authz, nil, nil); p.status != http.StatusFound || !strings.Contains(p.header.Get("Location"), "code=") {
		t.Errorf("the control (the owner's own navigation): %d", p.status)
	}
	// What WKWebView sends when the application's service worker re-sends the navigation: it is the window's own.
	webkit := func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		r.Header.Set("Sec-Fetch-Dest", "empty")
	}
	if p := owner.hop("GET", authz, nil, webkit); p.status != http.StatusFound || !strings.Contains(p.header.Get("Location"), "code=") {
		t.Errorf("a navigation re-sent by the service worker, as WebKit marks it: %d, want a code", p.status)
	}
	if p := stranger.hop("GET", authz, nil, webkit); p.status != http.StatusForbidden {
		t.Errorf("the same headers without the launch cookie: %d", p.status)
	}
	// And the worker leaves the sign-in's navigations alone in the first place.
	sw := owner.get("/sw.js")
	if sw.status != 200 || !strings.Contains(sw.body, `/^\/(auth|desktop|desktop-idp)\//.test(url.pathname)) return;`) ||
		strings.Index(sw.body, "desktop-idp") > strings.Index(sw.body, `req.mode === "navigate"`) {
		t.Error("the service worker does not step aside for /auth/, /desktop/ and /desktop-idp/ before it answers navigations")
	}
	// The profile form is not there for a stranger, and not there at all once a profile exists.
	if p := stranger.get("/desktop/profile"); p.status != 404 {
		t.Errorf("a stranger at the profile form: %d", p.status)
	}
	if p := stranger.do("POST", "/desktop/profile", url.Values{"name": {"Mallory"}}, nil); p.status != 404 {
		t.Errorf("a stranger posting a profile: %d", p.status)
	}
	if p := owner.hop("POST", "/desktop/profile", url.Values{"name": {"Mallory"}, "_csrf": {"x"}}, nil); p.status != http.StatusSeeOther {
		t.Errorf("a second profile: %d, want a redirect away", p.status)
	}
	if prof, _, _ := desktop.LoadProfile(context.Background(), w.d); prof.Name != "Owner" {
		t.Errorf("the profile was replaced: %+v", prof)
	}
	// No answer of the issuer or the start address is shared with another origin.
	for _, path := range []string{"/desktop-idp/.well-known/openid-configuration", "/desktop-idp/jwks", authz, "/desktop/start?t=x", "/desktop/profile"} {
		p := stranger.hop("GET", path, nil, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") })
		for k := range p.header {
			if strings.HasPrefix(k, "Access-Control-") {
				t.Errorf("%s answers with %s", path, k)
			}
		}
	}
}

// TestDesktopProfileFormIsProtected: the form's token, the cross-origin check, and what a mistake shows.
func TestDesktopProfileFormIsProtected(t *testing.T) {
	w := newDesk(t, nil)
	win := w.window()
	p := win.get("/desktop/start?t=" + deskToken)
	tok := csrfOf(t, p.body)
	for name, c := range map[string]struct {
		form url.Values
		edit func(*http.Request)
		want int
	}{
		"no token":            {url.Values{"name": {"A"}}, nil, 403},
		"a wrong token":       {url.Values{"name": {"A"}, "_csrf": {strings.Repeat("0", 64)}}, nil, 403},
		"from another site":   {url.Values{"name": {"A"}, "_csrf": {tok}}, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
		"from another origin": {url.Values{"name": {"A"}, "_csrf": {tok}}, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		"no name":             {url.Values{"name": {"  "}, "_csrf": {tok}}, nil, 422},
		"a bad address":       {url.Values{"name": {"A"}, "email": {"nope"}, "_csrf": {tok}}, nil, 422},
		"a long name":         {url.Values{"name": {strings.Repeat("n", 81)}, "_csrf": {tok}}, nil, 422},
	} {
		got := win.hop("POST", "/desktop/profile", c.form, c.edit)
		if got.status != c.want {
			t.Errorf("%s: %d, want %d", name, got.status, c.want)
		}
		if c.want == 422 {
			if !strings.Contains(got.body, `role="alert"`) || !strings.Contains(got.body, `aria-invalid="true"`) {
				t.Errorf("%s: the form does not say what is wrong", name)
			}
			lint(t, "desktop-profile "+name, got.body)
		}
	}
	if _, ok, _ := desktop.LoadProfile(context.Background(), w.d); ok {
		t.Fatal("a refused form made a profile")
	}
	// What was typed comes back escaped.
	got := win.hop("POST", "/desktop/profile", url.Values{"name": {`<script>alert(1)</script>`}, "email": {"x"}, "_csrf": {tok}}, nil)
	if strings.Contains(got.body, "<script>alert") {
		t.Error("the name came back unescaped")
	}
	// A body past the form's limit is refused.
	if got := win.hop("POST", "/desktop/profile", url.Values{"name": {strings.Repeat("n", maxBody+1)}, "_csrf": {tok}}, nil); got.status == http.StatusSeeOther {
		t.Error("an oversized form made a profile")
	}
}

// TestDesktopRefusesAForeignHost is DNS rebinding, on every kind of route.
func TestDesktopRefusesAForeignHost(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(w.base, "http://"))
	for _, path := range []string{"/", "/healthz", "/readyz", "/static/css/app.css", "/desktop/start?t=" + deskToken, "/desktop/profile", "/desktop-idp/jwks",
		"/desktop-idp/.well-known/openid-configuration", "/api/v1/desktop/info", "/api/v1/projects", "/auth/login", "/mcp"} {
		for _, host := range []string{"evil.example:" + port, "evil.example", "127.0.0.1." + "nip.io:" + port, "127.0.0.1:1"} {
			p := win.hop("GET", path, nil, func(r *http.Request) { r.Host = host })
			if p.status != http.StatusMisdirectedRequest {
				t.Errorf("GET %s with Host %s: %d, want 421", path, host, p.status)
			}
			if strings.Contains(p.body, "Owner") || len(p.header.Values("Set-Cookie")) != 0 {
				t.Errorf("GET %s with Host %s carried something", path, host)
			}
		}
	}
	if p := win.hop("POST", "/api/v1/projects", url.Values{}, func(r *http.Request) { r.Host = "evil.example:" + port }); p.status != http.StatusMisdirectedRequest {
		t.Errorf("POST with a foreign Host: %d", p.status)
	}
	if p := win.hop("GET", "/healthz", nil, func(r *http.Request) { r.Host = "localhost:" + port }); p.status != http.StatusMisdirectedRequest {
		t.Errorf("localhost with the bound port: %d, want 421", p.status)
	}
	if p := win.hop("GET", "/healthz", nil, nil); p.status != 200 {
		t.Errorf("the bound address: %d", p.status)
	}
}

// TestDesktopInfo: the one thing the shell asks the application for, and who may ask.
func TestDesktopInfo(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	status, out := win.json("GET", "/api/v1/desktop/info", "")
	abs, _ := filepath.Abs(w.dir)
	if status != 200 || out["data_dir"] != abs || out["mode"] != "embedded" || out["version"] != w.cfg.Version {
		t.Fatalf("desktop.info: %d %v", status, out)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{deskToken, deskKey} {
		if strings.Contains(string(raw), secret) {
			t.Error("desktop.info carries a secret")
		}
	}
	if status, _ := w.window().json("GET", "/api/v1/desktop/info", ""); status != http.StatusUnauthorized {
		t.Errorf("desktop.info signed out: %d, want 401", status)
	}
	// A writer, and an administrator's API key, are told there is nothing there.
	wr := w.s.newCaller(t, "writer", ops.RoleWriter)
	if rec := w.s.doHost(wr, "GET", "/api/v1/desktop/info", "", w.base); rec.Code != 404 {
		t.Errorf("desktop.info as a writer: %d, want 404", rec.Code)
	}
	st, key := win.json("POST", "/api/v1/keys", `{"name":"k","scope":"admin"}`)
	secret := findString(key, "secret")
	if st >= 300 || secret == "" {
		t.Fatalf("making a key: %d %v", st, key)
	}
	req, _ := http.NewRequest("GET", w.base+"/api/v1/desktop/info", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Error("desktop.info answered an API key")
	}
}

// findString finds the first string value under a key anywhere in decoded JSON.
func findString(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if s, ok := x[key].(string); ok {
			return s
		}
		for _, c := range x {
			if s := findString(c, key); s != "" {
				return s
			}
		}
	case []any:
		for _, c := range x {
			if s := findString(c, key); s != "" {
				return s
			}
		}
	}
	return ""
}

// doHost is do with the Host a desktop server expects.
func (s *Server) doHost(c caller, method, path, body, base string) *responseRecorder {
	req, _ := http.NewRequest(method, path, strings.NewReader(body))
	req.Host = strings.TrimPrefix(base, "http://")
	req.RemoteAddr = "127.0.0.1:1"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := &responseRecorder{header: http.Header{}, Code: 200}
	s.Handler().ServeHTTP(rec, req)
	return rec
}

type responseRecorder struct {
	header http.Header
	Code   int
	Body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header         { return r.header }
func (r *responseRecorder) WriteHeader(c int)           { r.Code = c }
func (r *responseRecorder) Write(b []byte) (int, error) { return r.Body.Write(b) }

// TestDesktopOffChangesNothing: with SCRIOBH_DESKTOP unset there is no desktop route, no issuer, no Host check, no
// menu entry, and the desktop ops answer "not found" to an administrator.
func TestDesktopOffChangesNothing(t *testing.T) {
	s := newAuthzServer(t)
	if s.desk != nil {
		t.Fatal("a server outside desktop mode has desktop parts")
	}
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	for _, path := range []string{"/desktop/start", "/desktop/start?t=" + deskToken, "/desktop/profile", "/desktop/identity", "/desktop/identity/verify", "/desktop/local-model",
		"/desktop-idp/.well-known/openid-configuration", "/desktop-idp/jwks", "/desktop-idp/authorize", "/desktop-idp/", "/desktop-idp"} {
		for _, c := range []caller{{}, admin} {
			if rec := s.do(c, "GET", path, ""); rec.Code != 404 {
				t.Errorf("GET %s outside desktop mode: %d, want 404", path, rec.Code)
			}
		}
	}
	for _, c := range []struct{ method, path string }{{"POST", "/desktop/profile"}, {"POST", "/desktop-idp/token"}, {"POST", "/ui/desktop/identity/save"}, {"POST", "/ui/desktop/local-model/detect"}} {
		if rec := s.do(admin, c.method, c.path, ""); rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s %s outside desktop mode: %d, want 404", c.method, c.path, rec.Code)
		}
	}
	for _, e := range s.Reg.Entries() {
		if !strings.HasPrefix(e.Op.Name, "desktop.") {
			continue
		}
		// A good body and an empty one: the answer does not depend on what was sent.
		for _, body := range []string{desktopBodies[e.Op.Name], bodyFor(e)} {
			if rec := s.do(admin, e.Op.Method, e.Op.Path, body); rec.Code != 404 {
				t.Errorf("%s as an administrator outside desktop mode: %d, want 404", e.Op.Name, rec.Code)
			}
		}
	}
	// A request with any Host is served: the check belongs to desktop mode. A challenge is not answered.
	req, _ := http.NewRequest("GET", "/healthz", nil)
	req.Host = "writing.example.org"
	req.Header.Set("X-Scriobh-Challenge", strings.Repeat("0123456789abcdef", 4))
	rec := &responseRecorder{header: http.Header{}, Code: 200}
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("/healthz with another Host outside desktop mode: %d", rec.Code)
	}
	if rec.header.Get("X-Scriobh-Proof") != "" {
		t.Error("/healthz answered a challenge outside desktop mode")
	}
	if rec := s.do(admin, "GET", "/settings", ""); strings.Contains(rec.Body.String(), "/desktop/") {
		t.Error("a page outside desktop mode links to a desktop page")
	}
	if s.testLocales() != s.Cfg.OIDCStub {
		t.Error("the pseudo-locales' switch changed outside desktop mode")
	}
}

// desktopBodies is a well-formed body for each desktop op that takes one.
var desktopBodies = map[string]string{
	"desktop.idp.set":    `{"issuer":"https://idp.example.org","client_id":"c"}`,
	"desktop.idp.verify": `{"proof":"p"}`,
	"desktop.idp.switch": `{"to":"external","confirm":"x"}`,
}

// TestDesktopOpsAreForTheAdministratorsSession is the authorization sweep of the desktop ops, in the mode they exist
// in (the general sweep's server is not in it): signed out 401, anyone but an administrator "not found", an API key
// refused even with the admin scope, the administrator's browser session let through. None is offered over MCP.
func TestDesktopOpsAreForTheAdministratorsSession(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	writer := w.s.newCaller(t, "writer", ops.RoleWriter)
	reader := w.s.newCaller(t, "reader", ops.RoleReader)
	st, key := win.json("POST", "/api/v1/keys", `{"name":"k","scope":"admin"}`)
	secret := findString(key, "secret")
	if st >= 300 || secret == "" {
		t.Fatalf("making a key: %d %v", st, key)
	}
	n := 0
	for _, e := range w.s.Reg.Entries() {
		if !strings.HasPrefix(e.Op.Name, "desktop.") {
			continue
		}
		n++
		if !e.Op.Auth.SessionOnly || !e.Op.Auth.Hidden || e.Op.Auth.Kind != ops.AuthAdmin {
			t.Errorf("%s is declared %+v, want an administrator's session only, hidden from others", e.Op.Name, e.Op.Auth)
		}
		body := desktopBodies[e.Op.Name]
		if rec := w.s.doHost(caller{}, e.Op.Method, e.Op.Path, body, w.base); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s signed out: %d, want 401", e.Op.Name, rec.Code)
		}
		for _, c := range []caller{writer, reader} {
			if rec := w.s.doHost(c, e.Op.Method, e.Op.Path, body, w.base); rec.Code != http.StatusNotFound {
				t.Errorf("%s as %s: %d, want 404", e.Op.Name, c.name, rec.Code)
			}
		}
		req, _ := http.NewRequest(e.Op.Method, w.base+e.Op.Path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s with an administrator's API key: %d, want a refusal", e.Op.Name, resp.StatusCode)
		}
		if e.Op.Name == "desktop.localmodel.detect" {
			continue // the administrator's call is made in TestDesktopLocalModelPage, with the look replaced
		}
		if st, _ := win.json(e.Op.Method, e.Op.Path, body); st == 401 || st == 403 || st == 404 || st >= 500 {
			t.Errorf("%s as the administrator's session: %d, want the call let through", e.Op.Name, st)
		}
	}
	if n < 6 {
		t.Errorf("only %d desktop ops were swept", n)
	}
	// Not one of them is an MCP tool.
	if w.s.mcp == nil || len(w.s.mcp.catalog.Tools) == 0 {
		t.Error("the MCP catalogue is empty: the check below would pass for the wrong reason")
	} else {
		for _, tool := range w.s.mcp.catalog.Tools {
			if strings.Contains(tool.Name, "desktop") || strings.HasPrefix(tool.Op, "desktop.") {
				t.Errorf("the MCP catalogue offers %s", tool.Name)
			}
		}
	}
}

// TestDesktopLocalModelPage: the page asks nothing by itself; the button's one look lists the models; using one adds
// the ordinary Ollama provider, marked local.
func TestDesktopLocalModelPage(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	looks := 0
	w.s.desk.svc.Detect = func(context.Context) ([]desktop.Model, error) {
		looks++
		return []desktop.Model{{Name: "llama3.2:latest", Parameters: "3.2B"}, {Name: `<b>odd</b>`}}, nil
	}
	p := win.get("/desktop/local-model")
	lint(t, "desktop-local-model", p.body)
	structure(t, "desktop-local-model", p.body)
	if looks != 0 {
		t.Fatal("opening the page looked for the model server")
	}
	if !strings.Contains(p.body, "never downloads") || !strings.Contains(p.body, "127.0.0.1") {
		t.Error("the page does not say what the button does and that nothing is downloaded")
	}
	p = win.do("POST", "/ui/desktop/local-model/detect", url.Values{"_csrf": {csrfOf(t, p.body)}}, nil)
	if looks != 1 || !strings.Contains(p.body, "llama3.2:latest") || strings.Contains(p.body, "<b>odd</b>") {
		t.Fatalf("after the look (%d looks): %s", looks, p.url)
	}
	lint(t, "desktop-local-model found", p.body)
	// Reloading the page does not look again.
	win.get("/desktop/local-model")
	if looks != 1 {
		t.Error("reloading the page looked again")
	}
	p = win.do("POST", "/ui/desktop/local-model/use", url.Values{"_csrf": {csrfOf(t, p.body)}, "model": {"llama3.2:latest"}}, nil)
	if !strings.Contains(p.url, "added=1") {
		t.Fatalf("using a model: %s %d\n%.300s", p.url, p.status, p.body)
	}
	var kind, baseURL, model string
	var local int
	var owner *string
	if err := w.d.Read().QueryRow(`SELECT kind, base_url, default_model, local, user_id FROM ai_providers`).Scan(&kind, &baseURL, &model, &local, &owner); err != nil {
		t.Fatal(err)
	}
	if kind != "ollama" || baseURL != "http://127.0.0.1:11434" || model != "llama3.2:latest" || local != 1 || owner != nil {
		t.Errorf("the provider added: %s %s %s local=%d owner=%v", kind, baseURL, model, local, owner)
	}
	// No server: the page says so, and nothing is added.
	w.s.desk.svc.Detect = func(context.Context) ([]desktop.Model, error) { return nil, context.DeadlineExceeded }
	p = win.do("POST", "/ui/desktop/local-model/detect", url.Values{"_csrf": {csrfOf(t, win.get("/desktop/local-model").body)}}, nil)
	if !strings.Contains(p.body, `id="lm-none"`) {
		t.Error("the page does not say that no model server answered")
	}
	// The forms need the session's token, as every form does.
	if p := win.hop("POST", "/ui/desktop/local-model/detect", url.Values{}, nil); p.status != http.StatusForbidden {
		t.Errorf("the look without a form token: %d", p.status)
	}
	if p := w.window().hop("POST", "/ui/desktop/local-model/detect", url.Values{}, nil); p.status != http.StatusSeeOther && p.status != http.StatusUnauthorized {
		t.Errorf("the look signed out: %d", p.status)
	}
}

// TestDesktopSecretsAreSealedWithTheShellsKey: with SCRIOBH_SECRET_KEY in the environment (the shell reads it from
// the keychain) an AI key and a Git token are sealed with it and open again, no key file is written, and the key is
// in no log line and no file of the data directory. Without the variable, the key file is made and used.
func TestDesktopSecretsAreSealedWithTheShellsKey(t *testing.T) {
	for _, fromEnv := range []bool{true, false} {
		env := map[string]string{}
		if !fromEnv {
			env["SCRIOBH_SECRET_KEY"] = ""
		}
		w := newDesk(t, env)
		win := w.firstRun("Owner", "")
		const aiKey, gitToken = "sk-desktop-test-ai-key-0123456789", "git-desktop-test-token-0123456789"
		// A stand-in provider on this computer records the key the application sends it: sealed, then opened.
		var mu sync.Mutex
		var sent []string
		fake := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			mu.Lock()
			sent = append(sent, r.Header.Get("Authorization"))
			mu.Unlock()
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"data":[{"id":"m1"}]}`))
		}))
		st, prov := win.json("POST", "/api/v1/ai/providers", `{"name":"P","kind":"openai_compatible","scope":"instance","local":true,"base_url":"`+fake.URL+`/v1","api_key":"`+aiKey+`"}`)
		provID := findString(prov, "id")
		if st != 201 || provID == "" {
			t.Fatalf("ai.provider.create: %d %v", st, prov)
		}
		if st, out := win.json("POST", "/api/v1/ai/providers/"+provID+"/test", ""); st != 200 {
			t.Fatalf("ai.provider.test: %d %v", st, out)
		}
		fake.Close()
		mu.Lock()
		if len(sent) == 0 || sent[0] != "Bearer "+aiKey {
			t.Errorf("the provider was sent %q, want the key that was sealed, opened again", sent)
		}
		mu.Unlock()
		st, proj := win.json("POST", "/api/v1/projects", `{"title":"Book","kind":"blank"}`)
		pid := findString(proj, "id")
		if st != 201 || pid == "" {
			t.Fatalf("project.create: %d %v", st, proj)
		}
		st, rem := win.json("POST", "/api/v1/projects/"+pid+"/git/remotes", `{"name":"Backup","url":"https://git.example.org/me/book.git","auth_kind":"https_token","token":"`+gitToken+`","push_mode":"manual"}`)
		if st != 201 {
			t.Fatalf("git.remote.create: %d %v", st, rem)
		}
		key, err := w.cfg.EnsureSecretKey()
		if err != nil {
			t.Fatal(err)
		}
		keyFile := filepath.Join(w.dir, "secret.key")
		_, statErr := os.Stat(keyFile)
		if fromEnv {
			if string(key) != deskKey {
				t.Error("the key in use is not the environment's")
			}
			if statErr == nil {
				t.Error("a key file was written although the shell gave the key")
			}
		} else {
			raw, rerr := os.ReadFile(keyFile)
			if rerr != nil || strings.TrimSpace(string(raw)) != string(key) {
				t.Errorf("without the variable the key file must hold the key in use: %v", rerr)
			}
			if st, _ := os.Stat(keyFile); st != nil && st.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
				t.Errorf("the key file's mode is %v", st.Mode().Perm())
			}
		}
		// The Git token is sealed with the same key: it opens with it (for the remote it was sealed for) and with no other.
		var remoteID, cipher, aiCipher string
		if err := w.d.Read().QueryRow(`SELECT id, secret_cipher FROM git_remotes`).Scan(&remoteID, &cipher); err != nil || cipher == "" {
			t.Fatalf("the remote's sealed token: %v", err)
		}
		_ = w.d.Read().QueryRow(`SELECT api_key_cipher FROM ai_providers`).Scan(&aiCipher)
		if aiCipher == "" || strings.Contains(aiCipher, aiKey) || strings.Contains(cipher, gitToken) {
			t.Error("a credential is stored unsealed")
		}
		gitContext := "git_remotes/" + remoteID + "/secret/https|git.example.org|443|me/book"
		if plain, err := secrets.New(key).Open(cipher, gitContext); err != nil || string(plain) != gitToken {
			t.Errorf("the Git token does not open with the key in use: %v", err)
		}
		if _, err := secrets.New([]byte("another-key-another-key-another-key-0000")).Open(cipher, gitContext); err == nil {
			t.Error("the Git token opens with another key")
		}
		w.hs.Close()
		w.d.Close()
		secretsIn := []string{aiKey, gitToken, deskToken}
		if fromEnv {
			secretsIn = append(secretsIn, deskKey)
		}
		_ = filepath.WalkDir(w.dir, func(path string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || (!fromEnv && path == keyFile) {
				return nil
			}
			b, _ := os.ReadFile(path)
			for _, s := range secretsIn {
				if bytes.Contains(b, []byte(s)) {
					t.Errorf("%s holds a secret in the clear (fromEnv=%v)", filepath.Base(path), fromEnv)
				}
			}
			return nil
		})
		logs := w.logs.String()
		for _, s := range append(secretsIn, string(key)) {
			if strings.Contains(logs, s) {
				t.Errorf("a secret is in the log (fromEnv=%v)", fromEnv)
			}
		}
	}
}

// TestDesktopWithAIOffNothingLeavesTheMachine: a whole session (first run, a project, writing, search, export,
// import, the pages) with every outbound connection of the process observed. The only connections are the
// application's own, to its own listener, for the sign-in.
func TestDesktopWithAIOffNothingLeavesTheMachine(t *testing.T) {
	var mu sync.Mutex
	var dials []string
	stop := netguard.Observe(func(network, address string) {
		mu.Lock()
		dials = append(dials, address)
		mu.Unlock()
	})
	defer stop()

	w := newDesk(t, nil)
	win := w.firstRun("Owner", "owner@example.org")
	must := func(label string, status int, out map[string]any) map[string]any {
		t.Helper()
		if status >= 300 {
			t.Fatalf("%s: %d %v", label, status, out)
		}
		return out
	}
	st, out := win.json("POST", "/api/v1/projects", `{"title":"The Quiet Room","kind":"novel"}`)
	pid := findString(must("project.create", st, out), "id")
	st, out = win.json("POST", "/api/v1/projects/"+pid+"/docs", `{"title":"One","kind":"chapter","content":"The harbour was empty at dawn.\n"}`)
	did := findString(must("doc.create", st, out), "id")
	st, out = win.json("GET", "/api/v1/docs/"+did, "")
	must("doc.get", st, out)
	rev := findNumber(out, "revision")
	st, out = win.json("PATCH", "/api/v1/docs/"+did, `{"base_revision":`+rev+`,"content":"The harbour was empty at dawn, and the gulls knew it.\n"}`)
	must("doc.update", st, out)
	st, out = win.json("GET", "/api/v1/projects/"+pid+"/search?q=gulls", "")
	must("search.query", st, out)
	st, out = win.json("POST", "/api/v1/projects/"+pid+"/export", `{"mode":"single"}`)
	must("export.markdown", st, out)
	st, out = win.json("POST", "/api/v1/projects/"+pid+"/export/html", `{}`)
	must("export.html", st, out)
	files := `[{"path":"ch1.md","content":"# Arrival\n\nShe came by the low road.\n"}]`
	st, out = win.json("POST", "/api/v1/imports/preview", `{"files":`+files+`}`)
	tok := findString(must("import.preview", st, out), "token")
	st, out = win.json("POST", "/api/v1/imports", `{"files":`+files+`,"token":"`+tok+`","title":"Imported"}`)
	must("import.apply", st, out)
	for _, path := range []string{"/", "/settings", "/settings/ai", "/admin/users", "/admin/diagnostics", "/desktop/identity", "/desktop/local-model", "/search?q=harbour", "/import"} {
		if p := win.get(path); p.status != 200 {
			t.Errorf("GET %s: %d", path, p.status)
		}
	}
	w.hs.Close()

	mu.Lock()
	defer mu.Unlock()
	own := strings.TrimPrefix(w.base, "http://")
	if len(dials) == 0 {
		t.Fatal("no connection was observed at all: the sign-in's own requests to the issuer should have been (is the observer wired?)")
	}
	for _, a := range dials {
		if a != own {
			t.Errorf("an outbound connection to %s: only the application's own listener (%s) may be dialled with AI off", a, own)
		}
	}
	// The page that looks for a model was opened and asked nothing: the look is the button's.
	for _, a := range dials {
		if strings.HasSuffix(a, ":11434") {
			t.Error("the model server was asked without the button")
		}
	}
}

func findNumber(v any, key string) string {
	switch x := v.(type) {
	case map[string]any:
		if n, ok := x[key].(float64); ok {
			b, _ := json.Marshal(n)
			return string(b)
		}
		for _, c := range x {
			if s := findNumber(c, key); s != "" {
				return s
			}
		}
	}
	return ""
}

// TestDesktopHealthzProvesTheToken: the shell sends a fresh challenge with every check of /healthz and sends the
// window the launch token only when the answer is the one that needs the token. Without a challenge, or with one
// that is not a nonce, /healthz is as before and carries no proof.
func TestDesktopHealthzProvesTheToken(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	prove := func(token, nonce string) string {
		key := hmac.New(sha256.New, []byte(token))
		key.Write([]byte("scriobh desktop healthz key v1"))
		m := hmac.New(sha256.New, key.Sum(nil))
		m.Write([]byte(nonce))
		return hex.EncodeToString(m.Sum(nil))
	}
	nonce := strings.Repeat("0123456789abcdef", 4)
	ask := func(n string) page {
		return win.hop("GET", "/healthz", nil, func(r *http.Request) { r.Header.Set("X-Scriobh-Challenge", n) })
	}
	got := ask(nonce)
	if got.status != 200 || got.header.Get("X-Scriobh-Proof") != prove(deskToken, nonce) {
		t.Fatalf("/healthz with a challenge: %d, proof %q, want %q", got.status, got.header.Get("X-Scriobh-Proof"), prove(deskToken, nonce))
	}
	if !strings.Contains(got.body, `"status":"ok"`) {
		t.Errorf("the body changed: %s", got.body)
	}
	if got.header.Get("X-Scriobh-Proof") == prove("not the token"+deskToken[13:], nonce) {
		t.Error("the proof does not depend on the token")
	}
	for name, n := range map[string]string{"short": nonce[:63], "long": nonce + "0", "upper case": strings.ToUpper(nonce), "not hex": strings.Repeat("z", 64), "empty": ""} {
		if p := ask(n); p.status != 200 || p.header.Get("X-Scriobh-Proof") != "" {
			t.Errorf("%s challenge: %d, proof %q; want 200 and no proof", name, p.status, p.header.Get("X-Scriobh-Proof"))
		}
	}
	if p := win.hop("GET", "/healthz", nil, nil); p.status != 200 || p.header.Get("X-Scriobh-Proof") != "" {
		t.Errorf("no challenge: %d, proof %q", p.status, p.header.Get("X-Scriobh-Proof"))
	}
	// Two challenges, two answers; asking is free of side effects (the token is still good once for the window).
	if ask(strings.Repeat("ab", 32)).header.Get("X-Scriobh-Proof") == got.header.Get("X-Scriobh-Proof") {
		t.Error("two challenges got one answer")
	}
	// Two challenge headers are one malformed request, not a choice between them.
	two := win.hop("GET", "/healthz", nil, func(r *http.Request) {
		r.Header.Add("X-Scriobh-Challenge", nonce)
		r.Header.Add("X-Scriobh-Challenge", nonce)
	})
	if two.header.Get("X-Scriobh-Proof") != "" {
		t.Error("a doubled challenge was answered")
	}
	// A foreign Host is refused before the proof is made.
	if p := win.hop("GET", "/healthz", nil, func(r *http.Request) {
		r.Host = "evil.example"
		r.Header.Set("X-Scriobh-Challenge", nonce)
	}); p.status != http.StatusMisdirectedRequest || p.header.Get("X-Scriobh-Proof") != "" {
		t.Errorf("a foreign Host: %d, proof %q", p.status, p.header.Get("X-Scriobh-Proof"))
	}
}

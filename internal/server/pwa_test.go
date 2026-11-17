package server

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/web"
)

type workerConfig struct {
	Version string   `json:"version"`
	Static  []string `json:"static"`
	Offline string   `json:"offline"`
}

func get(s *Server, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func workerOf(t *testing.T, s *Server) (workerConfig, string) {
	t.Helper()
	rec := get(s, "/sw.js", nil)
	if rec.Code != 200 {
		t.Fatalf("/sw.js: %d", rec.Code)
	}
	body := rec.Body.String()
	m := regexp.MustCompile(`^var CONFIG = (\{.*\});\n`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the worker does not start with its configuration:\n%.200s", body)
	}
	var cfg workerConfig
	if err := json.Unmarshal([]byte(m[1]), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, body[len(m[0]):]
}

func TestServiceWorkerIsServedAtTheRoot(t *testing.T) {
	s := newAuthzServer(t)
	rec := get(s, "/sw.js", nil)
	h := rec.Header()
	if h.Get("Content-Type") != "text/javascript; charset=utf-8" {
		t.Errorf("type %q", h.Get("Content-Type"))
	}
	if cc := h.Get("Cache-Control"); !strings.Contains(cc, "no-cache") || !strings.Contains(cc, "no-store") {
		t.Errorf("the worker must not be cached by the browser: %q", cc)
	}
	if h.Get("Service-Worker-Allowed") != "/" {
		t.Errorf("scope header %q", h.Get("Service-Worker-Allowed"))
	}
	if h.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff is missing")
	}
	// It is not behind sign-in and sets no cookie.
	if rec.Code != 200 || h.Get("Set-Cookie") != "" {
		t.Errorf("%d, cookie %q", rec.Code, h.Get("Set-Cookie"))
	}
	cfg, src := workerOf(t, s)
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(cfg.Version) || cfg.Offline != "/offline" {
		t.Errorf("config %+v", cfg)
	}
	// What it keeps: the shell. What it does not: any page.
	for _, want := range []string{"css/app.", "js/htmx.min.", "js/shell.", "js/pwa.", "icons/favicon.", "icons/sprite.", "/static/fonts/newsreader.woff2", "/static/fonts/newsreader.woff2"} {
		found := false
		for _, u := range cfg.Static {
			if strings.Contains(u, want) {
				found = true
			}
		}
		if !found {
			t.Errorf("the worker does not keep %s", want)
		}
	}
	for _, u := range cfg.Static {
		if !strings.HasPrefix(u, "/static/") || strings.Contains(u, "sw/") || strings.Contains(u, "?") {
			t.Errorf("the worker keeps %q", u)
		}
	}
	// The source: scope, credentials, the update protocol and the cache policy.
	for _, want := range []string{`credentials: "omit"`, `type === "SKIP_WAITING"`, "self.skipWaiting()", `req.mode === "navigate"`, `url.pathname.indexOf("/static/") === 0`, "CONFIG.offline"} {
		if !strings.Contains(src, want) {
			t.Errorf("the worker lacks %s", want)
		}
	}
	if strings.Count(src, "skipWaiting()") != 1 {
		t.Error("the worker may skip waiting only on the page's message")
	}
	if strings.Contains(src, "importScripts") || strings.Contains(src, "eval(") || strings.Contains(src, "http://") || strings.Contains(src, "https://") {
		t.Error("the worker loads or names something outside itself")
	}
	// Nothing but a /static file is ever written to the cache while serving:
	// cache.put appears once, inside the /static branch.
	if strings.Count(src, "cache.put") != 1 || strings.Index(src, "cache.put") > strings.Index(src, `req.mode === "navigate"`) {
		t.Error("something other than a static file can be put into the cache")
	}
	if strings.Contains(src, "cache.add(") || strings.Count(src, "cache.addAll(") != 1 {
		t.Error("the install step must be one atomic addAll")
	}
}

// Every file the worker keeps exists, as a 200, with the right bytes: addAll
// fails the whole install on one missing file.
func TestEveryPrecachedFileIsServed(t *testing.T) {
	s := newAuthzServer(t)
	cfg, _ := workerOf(t, s)
	if len(cfg.Static) < 15 {
		t.Fatalf("only %d files kept", len(cfg.Static))
	}
	seen := map[string]bool{}
	for _, u := range cfg.Static {
		if seen[u] {
			t.Errorf("%s is listed twice", u)
		}
		seen[u] = true
		rec := get(s, u, nil)
		if rec.Code != 200 {
			t.Errorf("%s: %d", u, rec.Code)
			continue
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", u)
		}
		if rec.Header().Get("Set-Cookie") != "" {
			t.Errorf("%s sets a cookie", u)
		}
	}
	off := get(s, cfg.Offline, nil)
	if off.Code != 200 {
		t.Errorf("the offline page: %d", off.Code)
	}
}

func TestWorkerVersionFollowsTheFiles(t *testing.T) {
	a := newAuthzServer(t)
	cfgA, _ := workerOf(t, a)
	again := newAuthzServer(t)
	cfgB, _ := workerOf(t, again)
	if cfgA.Version != cfgB.Version {
		t.Errorf("the same build has two versions: %s %s", cfgA.Version, cfgB.Version)
	}
	// One changed stylesheet is a new worker and a new cache name.
	real, err := fs.Sub(web.FS, "static")
	if err != nil {
		t.Fatal(err)
	}
	m := fstest.MapFS{}
	err = fs.WalkDir(real, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(real, p)
		if p == "css/app.css" {
			b = append(b, []byte("\n/* changed */\n")...)
		}
		m[p] = &fstest.MapFile{Data: b}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := New(Deps{Cfg: testutil.Config(t, nil), DB: testutil.DB(t), Static: m})
	if err != nil {
		t.Fatal(err)
	}
	cfgC, _ := workerOf(t, changed)
	if cfgC.Version == cfgA.Version {
		t.Error("a changed stylesheet did not change the worker version")
	}
	if strings.Join(cfgC.Static, ",") == strings.Join(cfgA.Static, ",") {
		t.Error("the changed stylesheet has the same URL")
	}
}

// The offline page is one page for everybody: it is built without a session, so
// the cookie a request carries cannot reach it.
func TestOfflinePageIsTheSameForEveryone(t *testing.T) {
	s := newAuthzServer(t)
	alice := s.newCaller(t, "alice", ops.RoleWriter)
	anon := get(s, "/offline", nil)
	withCookie := get(s, "/offline", alice.cookie)
	if anon.Code != 200 || withCookie.Code != 200 {
		t.Fatalf("%d %d (it must be a 200, not a redirect)", anon.Code, withCookie.Code)
	}
	if anon.Header().Get("Location") != "" || withCookie.Header().Get("Set-Cookie") != "" {
		t.Error("the offline page redirects or sets a cookie")
	}
	if !bytes.Equal(anon.Body.Bytes(), withCookie.Body.Bytes()) {
		t.Error("the offline page differs for a signed-in request")
	}
	body := withCookie.Body.String()
	for _, secret := range []string{"alice@test.local", "alice", `name="csrf-token"`, `id="palette"`, "Sign out", "signed-in"} {
		if strings.Contains(body, secret) {
			t.Errorf("the offline page holds %q", secret)
		}
	}
	for _, want := range []string{"You are offline", "Try again", `href="/manifest.webmanifest"`} {
		if want == "You are offline" {
			want = "cannot reach the server"
		}
		if !strings.Contains(body, want) {
			t.Errorf("the offline page lacks %q", want)
		}
	}
	// The pages that do carry a person are not cacheable by any shared cache.
	if rec := get(s, "/", alice.cookie); !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("the library page is cacheable: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestManifest(t *testing.T) {
	s := newAuthzServer(t)
	rec := get(s, "/manifest.webmanifest", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var m struct {
		ID, Name, ShortName, StartURL, Scope, Display, BackgroundColor, ThemeColor string
		Icons                                                                      []struct{ Src, Sizes, Type, Purpose string }
	}
	raw := rec.Body.Bytes()
	if err := json.Unmarshal(raw, &struct {
		ID, Name string
	}{}); err != nil {
		t.Fatal(err)
	}
	dec := map[string]any{}
	_ = json.Unmarshal(raw, &dec)
	m.ID, m.Name, m.ShortName = dec["id"].(string), dec["name"].(string), dec["short_name"].(string)
	m.StartURL, m.Scope, m.Display = dec["start_url"].(string), dec["scope"].(string), dec["display"].(string)
	m.BackgroundColor, m.ThemeColor = dec["background_color"].(string), dec["theme_color"].(string)
	if err := json.Unmarshal([]byte(`{"icons":`+mustJSON(dec["icons"])+`}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Scríobh" || m.ShortName != "Scríobh" || m.StartURL != "/" || m.Scope != "/" || m.Display != "standalone" || m.ID != "/" {
		t.Errorf("manifest %+v", m)
	}
	// The colours are the Paper tokens.
	if m.BackgroundColor != "#FBFAF7" || m.ThemeColor != "#FBFAF7" {
		t.Errorf("colours %s %s", m.BackgroundColor, m.ThemeColor)
	}
	if strings.Contains(string(raw), "http://") || strings.Contains(string(raw), "https://") {
		t.Error("the manifest names an absolute address")
	}
	purposes := map[string]bool{}
	for _, ic := range m.Icons {
		purposes[ic.Purpose] = true
		r := get(s, ic.Src, nil)
		if r.Code != 200 || r.Header().Get("Content-Type") != ic.Type {
			t.Errorf("icon %s: %d %q", ic.Src, r.Code, r.Header().Get("Content-Type"))
			continue
		}
		if ic.Type == "image/png" {
			img, err := png.Decode(bytes.NewReader(r.Body.Bytes()))
			if err != nil {
				t.Errorf("icon %s: %v", ic.Src, err)
				continue
			}
			if want := strings.Split(ic.Sizes, "x")[0]; want != strconv.Itoa(img.Bounds().Dx()) {
				t.Errorf("icon %s is %d wide, the manifest says %s", ic.Src, img.Bounds().Dx(), ic.Sizes)
			}
		}
	}
	if !purposes["any"] || !purposes["maskable"] {
		t.Errorf("icon purposes %v", purposes)
	}
	// The apple touch icon the layout links exists.
	if r := get(s, "/pwa/icon-180.png", nil); r.Code != 200 {
		t.Errorf("apple touch icon: %d", r.Code)
	}
	if r := get(s, "/pwa/nope.png", nil); r.Code != 404 {
		t.Errorf("an unknown icon: %d", r.Code)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestPagesLinkTheManifestAndRegisterTheWorker(t *testing.T) {
	s := newAuthzServer(t)
	for _, path := range []string{"/", "/offline"} {
		rec := get(s, path, nil)
		body := rec.Body.String()
		if !strings.Contains(body, `<link rel="manifest" href="/manifest.webmanifest">`) || !regexp.MustCompile(`<script src="/static/js/pwa\.[0-9a-f]{8}\.js" defer></script>`).MatchString(body) {
			t.Errorf("%s: manifest or pwa.js missing", path)
		}
	}
	// The policy lets the worker and the manifest load and nothing inline.
	csp := get(s, "/", nil).Header().Get("Content-Security-Policy")
	for _, want := range []string{"worker-src 'self'", "manifest-src 'self'", "script-src 'self'", "default-src 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %s: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "blob:") {
		t.Errorf("CSP is loosened: %s", csp)
	}
}

// pwa.js is on every page, so it has its own small budget (and the worker, which
// is fetched on its own, another).
func TestPWABudgets(t *testing.T) {
	static := staticFS(t)
	gz := func(p string) int {
		b, err := fs.ReadFile(static, p)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		w.Write(b)
		w.Close()
		return buf.Len()
	}
	if n := gz("js/pwa.js"); n > 1024 {
		t.Errorf("pwa.js is %d bytes gzip, budget is 1024", n)
	} else {
		t.Logf("pwa.js: %d bytes gzip", n)
	}
	if n := gz("sw/worker.js"); n > 1536 {
		t.Errorf("worker.js is %d bytes gzip, budget is 1536", n)
	} else {
		t.Logf("worker.js: %d bytes gzip", n)
	}
}

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

// Independent review of p8-desktop-go. Each test states what it expects of a correct build.

var signsOutForm = regexp.MustCompile(`<form[^>]*\bdata-signs-out\b[^>]*>`)

// R1: shell.js signOut() reads its question from the form's data-m-* attributes. Every form that carries data-signs-out
// must carry them, or the in-page confirmation that replaced confirm() asks an empty question ("" with OK and Cancel)
// before discarding the browser's unsaved text. settings.html's "Sign out here" form (the current session) has none.
func TestRevP8EverySignOutFormCarriesItsQuestion(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	p := win.get("/settings")
	if p.status != 200 {
		t.Fatalf("/settings: %d", p.status)
	}
	forms := signsOutForm.FindAllString(p.body, -1)
	if len(forms) < 2 {
		t.Fatalf("expected the account menu's form and the current session's form on /settings, found %d: %v", len(forms), forms)
	}
	for _, f := range forms {
		for _, attr := range []string{"data-m-unsaved", "data-m-locked", "data-m-ok", "data-m-cancel"} {
			if !strings.Contains(f, attr) {
				t.Errorf("a sign-out form lacks %s, so the question it asks is empty: %.160s", attr, f)
			}
		}
	}
}

// R3: a database that already has accounts (a restored server database) and no local profile. The profile form works,
// the issuer approves, and the sign-in is refused. The refusal must say something the person can act on.
func TestRevP8RestoredDatabaseRefusalIsReadable(t *testing.T) {
	w := newDesk(t, nil)
	now := db.Now()
	if _, err := w.d.Writer().Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, avatar_color, role, created_at, updated_at, last_seen_at)
		VALUES ('usr_old','https://idp.example.org','sub-1','old@example.org','Old','#000000','admin',?,?,?)`, now, now, now); err != nil {
		t.Fatal(err)
	}
	win := w.window()
	p := win.get("/desktop/start?t=" + deskToken)
	if !strings.HasSuffix(p.url, "/desktop/profile") {
		t.Fatalf("ended at %s", p.url)
	}
	p = win.do("POST", "/desktop/profile", url.Values{"_csrf": {csrfOf(t, p.body)}, "name": {"Me"}}, nil)
	m := regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>.*?<p[^>]*>(.*?)</p>`).FindStringSubmatch(p.body)
	t.Logf("after the profile: %d, page says %q", p.status, m)
	if m == nil || strings.Contains(m[2], "scriobh-writers") || strings.Contains(m[2], "add you to") {
		t.Errorf("a desktop person is told to ask the administrator to be added to provider groups that do not exist here: %q", m)
	}
	if q := win.hop("GET", "/", nil, nil); q.status != http.StatusSeeOther {
		t.Logf("GET / afterwards: %d", q.status)
	}
}

// R4: SCRIOBH_ADMIN_GROUP set explicitly in desktop mode would turn the first-user rule off, and the local profile, which
// is in no group, would be refused at its very first sign-in: the instance could never be signed in to. Desktop mode
// refuses to start with the variable set, with a message that names it.
func TestRevP8AnExplicitAdminGroupDoesNotLockTheLocalProfileOut(t *testing.T) {
	env := map[string]string{"SCRIOBH_DESKTOP": "1", "SCRIOBH_LAUNCH_TOKEN": deskToken, "SCRIOBH_DATA_DIR": t.TempDir(), "SCRIOBH_SECRET_KEY": deskKey, "SCRIOBH_ADMIN_GROUP": "scriobh-admins"}
	cfg, err := config.Load(func(k string) (string, bool) { v, ok := env[k]; return v, ok })
	if err == nil {
		err = cfg.Validate()
	}
	if err == nil || !strings.Contains(err.Error(), "SCRIOBH_ADMIN_GROUP") {
		t.Fatalf("desktop mode started with SCRIOBH_ADMIN_GROUP set (error %v)", err)
	}
}

// R5: five wrong tokens lock the launch gate for a minute, for the whole process, and a right token offered meanwhile is
// refused (contract change 7). /desktop/start does not look at Sec-Fetch-Site, so a web page in the person's normal
// browser (an <img> or a fetch to http://127.0.0.1:<port>/desktop/start?t=x, five times a minute) keeps the shell's
// window from ever signing in. The token is 128 bits or more: the lockout protects nothing, and costs the window its way in.
func TestRevP8ACrossSitePageCannotLockTheShellOut(t *testing.T) {
	w := newDesk(t, nil)
	for i := 0; i < 5; i++ {
		req, _ := http.NewRequest("GET", w.base+"/desktop/start?t=deadbeef", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "no-cors")
		req.Header.Set("Sec-Fetch-Dest", "image")
		resp, err := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	win := w.window()
	p := win.get("/desktop/start?t=" + deskToken)
	if !strings.HasSuffix(p.url, "/desktop/profile") {
		t.Errorf("five cross-site image requests locked the shell's right token out: the window ended at %s (%d)", p.url, p.status)
	}
}

// R6 (a proof, expected to pass): a server deployment behind a reverse proxy, on a public name, with desktop mode off,
// answers every Host and carries nothing of desktop mode: no 421, no desktop route, no desktop menu entry.
func TestRevP8AProxiedServerDeploymentIsUntouched(t *testing.T) {
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_RATE_LIMIT": "0", "SCRIOBH_OIDC_STUB": "0", "SCRIOBH_BASE_URL": "https://writing.example.org",
		"SCRIOBH_TRUST_PROXY": "1", "SCRIOBH_OIDC_ISSUER": "https://idp.example.org", "SCRIOBH_OIDC_CLIENT_ID": "scriobh", "SCRIOBH_OIDC_CLIENT_SECRET": "x"})
	s, err := New(Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	if s.desk != nil || s.Cfg.Desktop {
		t.Fatal("desktop parts exist with SCRIOBH_DESKTOP unset")
	}
	for _, host := range []string{"writing.example.org", "10.0.0.7:8080", "localhost", "127.0.0.1.nip.io"} {
		for _, path := range []string{"/healthz", "/readyz", "/", "/manifest.webmanifest", "/static/css/app.css"} {
			// A request context that ends with the request, as a real server's does (a handler that leaves a row open holds a
			// connection until it does).
			ctx, cancel := context.WithCancel(context.Background())
			req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
			req.Host = host
			req.Header.Set("X-Forwarded-Host", "writing.example.org")
			req.Header.Set("X-Forwarded-Proto", "https")
			req.Header.Set("X-Forwarded-For", "203.0.113.9")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			cancel()
			if rec.Code == http.StatusMisdirectedRequest || rec.Code >= 500 {
				t.Errorf("GET %s with Host %s: %d", path, host, rec.Code)
			}
		}
	}
	for _, path := range []string{"/desktop/start?t=" + deskToken, "/desktop/profile", "/desktop-idp/authorize", "/desktop-idp/jwks"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s with desktop mode off: %d, want 404", path, rec.Code)
		}
	}
}

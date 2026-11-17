package server

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func adminWorld(t *testing.T) (*Server, caller, caller, caller) {
	t.Helper()
	s := newAuthzServer(t)
	return s, s.newCaller(t, "ada", ops.RoleAdmin), s.newCaller(t, "wren", ops.RoleWriter), s.newCaller(t, "rory", ops.RoleReader)
}

var adminPages = []string{"/admin", "/admin/users", "/admin/quotas", "/admin/diagnostics", "/admin/diagnostics/download"}

func TestTheConsoleIsAnAdministratorsAndNobodyElseFindsIt(t *testing.T) {
	s, ada, wren, rory := adminWorld(t)
	for _, p := range adminPages {
		if rec := s.do(ada, "GET", p, ""); rec.Code != 200 && rec.Code != 303 {
			t.Errorf("an administrator opens %s: %d", p, rec.Code)
		}
		for _, c := range []caller{wren, rory} {
			if rec := s.do(c, "GET", p, ""); rec.Code != 404 {
				t.Errorf("%s opens %s: %d", c.name, p, rec.Code)
			}
		}
		if rec := s.do(caller{name: "anonymous"}, "GET", p, ""); rec.Code != 303 {
			t.Errorf("anonymous at %s: %d", p, rec.Code)
		}
	}
	// the answer to a person who is not an administrator is that of a page that is not there
	a, b := s.do(wren, "GET", "/admin/users", ""), s.do(wren, "GET", "/no/such/page", "")
	if a.Body.String() != b.Body.String() {
		t.Error("the console's 404 differs from any other 404")
	}
	// the forms
	form := url.Values{"id": {wren.userID}, "confirm_name": {"wren@test.local"}}
	for _, p := range []string{"/ui/admin/users/disable", "/ui/admin/users/enable", "/ui/admin/users/role"} {
		for _, c := range []caller{wren, rory} {
			if rec := s.postForm(t, c, p, form); rec.Code != 404 {
				t.Errorf("%s posts %s: %d", c.name, p, rec.Code)
			}
		}
	}
	if rec := s.do(wren, "GET", "/admin/users/"+rory.userID+"/disable", ""); rec.Code != 404 {
		t.Errorf("the confirmation page: %d", rec.Code)
	}
	// the ops behind them refuse the same people by the API
	for _, p := range []string{"/api/v1/admin/users", "/api/v1/admin/quotas", "/api/v1/admin/diagnostics", "/api/v1/admin/diagnostics/download"} {
		if rec := s.do(wren, "GET", p, ""); rec.Code != 404 {
			t.Errorf("a writer calls %s: %d, want the 404 of a thing that is not there", p, rec.Code)
		}
	}
	// the menu offers it to an administrator only
	if body := s.do(ada, "GET", "/", "").Body.String(); !strings.Contains(body, `href="/admin/users"`) {
		t.Error("an administrator's menu has no way to the console")
	}
	if body := s.do(wren, "GET", "/", "").Body.String(); strings.Contains(body, "/admin/") {
		t.Error("a writer's menu names the console")
	}
}

// Every way in refuses a person who is switched off: the page, the API with a session and with a key, the live stream
// with either, a new sign-in; switching them on again opens each. A stream that is already open looks the session and
// the key up again at every heartbeat (streamPrincipal), so it ends with them.
func TestEveryWayInRefusesAPersonWhoIsSwitchedOff(t *testing.T) {
	s, ada, wren, _ := adminWorld(t)
	pw := ops.Principal{UserID: wren.userID, Role: ops.RoleWriter, Via: "session"}
	pr, err := s.Reg.Call(t.Context(), pw, "project.create", projects.CreateIn{Title: "Wren's Book"})
	if err != nil {
		t.Fatal(err)
	}
	pid, slug := pr.(projects.Item).Item.ID, pr.(projects.Item).Item.Slug
	ko, err := s.Reg.Call(t.Context(), pw, "key.create", keys.CreateIn{Name: "k"})
	if err != nil {
		t.Fatal(err)
	}
	secret := ko.(keys.CreateOut).Secret
	withKey := func(path string) int {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec.Code
	}
	paths := []string{"/api/v1/projects", "/api/v1/projects/" + pid + "/docs", "/api/v1/events?project=" + pid}
	check := func(open bool, when string) {
		t.Helper()
		for _, p := range paths {
			if open && strings.Contains(p, "/events") {
				continue // an open stream does not end: it is only asked for here to see it refused
			}
			code := s.do(wren, "GET", p, "").Code
			kc := withKey(p)
			if open && (code == 401 || kc == 401) {
				t.Errorf("%s: %s with a session %d, with a key %d", when, p, code, kc)
			}
			if !open && (code != 401 || kc != 401) {
				t.Errorf("%s: %s with a session %d, with a key %d, want 401 for both", when, p, code, kc)
			}
		}
		page := s.do(wren, "GET", "/p/"+slug, "").Code
		if open && page != 200 || !open && page != 303 {
			t.Errorf("%s: the project page %d", when, page)
		}
	}
	check(true, "before")
	if rec := s.postForm(t, ada, "/ui/admin/users/disable", url.Values{"id": {wren.userID}, "confirm_name": {"nope"}}); rec.Code != 422 {
		t.Errorf("a wrong name: %d", rec.Code)
	}
	check(true, "after a refused confirmation")
	if rec := s.postForm(t, ada, "/ui/admin/users/disable", url.Values{"id": {wren.userID}, "confirm_name": {"wren@test.local"}}); rec.Code != 303 {
		t.Fatalf("switching off: %d", rec.Code)
	}
	check(false, "switched off")
	if rec := s.postForm(t, ada, "/ui/admin/users/disable", url.Values{"id": {ada.userID}, "confirm_name": {"ada@test.local"}}); rec.Code != 303 || !strings.Contains(s.do(ada, "GET", "/admin/users", "").Body.String(), "cannot switch yourself off") {
		t.Errorf("switching yourself off: %d", rec.Code)
	}
	if rec := s.postForm(t, ada, "/ui/admin/users/enable", url.Values{"id": {wren.userID}}); rec.Code != 303 {
		t.Fatalf("switching on: %d", rec.Code)
	}
	// sessions ended for good when it was switched off: a cookie from before is not revived, a key is
	if code := s.do(wren, "GET", "/api/v1/projects", "").Code; code != 401 {
		t.Errorf("a session that was ended came back: %d", code)
	}
	if code := withKey("/api/v1/projects"); code != 200 {
		t.Errorf("a key after switching on: %d", code)
	}
}

func TestTheConsolePagesShowWhatTheyShouldAndNothingSecret(t *testing.T) {
	s, ada, wren, _ := adminWorld(t)
	users := s.do(ada, "GET", "/admin/users", "").Body.String()
	for _, want := range []string{"wren@test.local", "Switch off", "Administrator"} {
		if !strings.Contains(users, want) {
			t.Errorf("the people page lacks %q", want)
		}
	}
	if q := s.do(ada, "GET", "/admin/users?q=wren&status=active", "").Body.String(); strings.Contains(q, `id="u-`+ada.userID+`"`) || !strings.Contains(q, `id="u-`+wren.userID+`"`) {
		t.Error("the filter does not filter")
	}
	if page := s.do(ada, "GET", "/admin/users/"+wren.userID+"/disable", "").Body.String(); !strings.Contains(page, "Type the name") || !strings.Contains(page, `name="confirm_name"`) {
		t.Error("the confirmation does not ask for the name")
	}
	if q := s.do(ada, "GET", "/admin/quotas", "").Body.String(); !strings.Contains(q, "configuration") || !strings.Contains(q, "wren@test.local") {
		t.Error("the limits page")
	}
	d := s.do(ada, "GET", "/admin/diagnostics", "")
	if !strings.Contains(d.Body.String(), "/admin/diagnostics/download") || strings.Contains(d.Body.String(), s.Cfg.DataDir) {
		t.Error("the diagnostics page")
	}
	dl := s.do(ada, "GET", "/admin/diagnostics/download", "")
	if dl.Header().Get("Content-Type") != "application/zip" || !strings.Contains(dl.Header().Get("Content-Disposition"), "scriobh-diagnostics.zip") {
		t.Errorf("the download: %v", dl.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(dl.Body.Bytes()), int64(dl.Body.Len()))
	if err != nil || len(zr.File) != 2 {
		t.Fatalf("the zip: %v", err)
	}
	r, _ := zr.File[1].Open()
	js, _ := io.ReadAll(r)
	for _, secret := range []string{s.Cfg.DataDir, "test-secret-key", "test.local", "127.0.0.1", string(s.Cfg.SecretKey)} {
		if strings.Contains(string(js), secret) {
			t.Errorf("the bundle holds %q", secret)
		}
	}
	for _, want := range []string{`"secret_key_set": true`, `"oidc_stub": true`, `"listen_loopback": true`, `"users"`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("the bundle lacks %s", want)
		}
	}
}

// A new setting reaches the diagnostics only by being named in the allow-list: every field of the configuration is
// decided on (shown, or hidden on purpose), and the shape never holds a string that is not a short word.
func TestEveryConfigFieldIsDecided(t *testing.T) {
	typ := reflect.TypeOf(config.Config{})
	for i := 0; i < typ.NumField(); i++ {
		if _, ok := configCovered[typ.Field(i).Name]; !ok {
			t.Errorf("config.Config.%s is not in configCovered: decide whether the diagnostics show it", typ.Field(i).Name)
		}
	}
	for name := range configCovered {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("configCovered names %s, which is not a field", name)
		}
	}
	cfg := newAuthzServer(t).Cfg
	cfg.OIDCClientSecret, cfg.OIDCIssuer, cfg.AdminGroup = "CANARYCLIENTSECRET", "https://CANARY-idp.example.org", "CANARYGROUP"
	cfg.BaseURL, cfg.Listen = "https://CANARYHOST.example.org", "10.1.2.3:8080"
	shape := configShape(cfg)
	for k, v := range shape {
		switch x := v.(type) {
		case bool, int, int64:
		case string:
			if len(x) > 16 {
				t.Errorf("%s holds a string: %q", k, x)
			}
		default:
			t.Errorf("%s has type %T", k, v)
		}
	}
	for _, v := range shape {
		if str, ok := v.(string); ok && strings.Contains(strings.ToLower(str), "canary") {
			t.Error("a hidden value is in the shape")
		}
	}
	if shape["listen_loopback"] != false || shape["oidc_client_secret_set"] != true || shape["admin_group_set"] != true || shape["base_url_scheme"] != "https" {
		t.Errorf("shape: %v", shape)
	}
}

// The ring keeps warnings and errors with the route pattern and nothing a person typed: a token in a path, a bearer, a
// query, an email or a long secret is gone before it is kept.
func TestTheLogRingKeepsScrubbedWarningsOnly(t *testing.T) {
	ring := &logRing{}
	lg := slog.New(ringHandler{inner: slog.NewTextHandler(io.Discard, nil), ring: ring})
	lg.Info("request", "route", "GET /p/{slug}", "status", 200)
	lg.Warn("slow request for wren@example.org", "route", "GET /s/{token}", "path", "/s/shl_CANARYTOKEN.xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "err", "Bearer CANARYBEARER at ?q=CANARYQUERY", "principal", "usr_01CANARYUSER")
	lg.Error("boom", "status", 500, "body", "CANARYMANUSCRIPT words")
	got := ring.snapshot()
	if len(got) != 2 {
		t.Fatalf("kept %d lines", len(got))
	}
	all := ""
	for _, l := range got {
		all += l.Msg
		for k, v := range l.Attrs {
			all += k + "=" + v + " "
		}
	}
	for _, bad := range []string{"CANARY", "wren@example.org", "example.org"} {
		if strings.Contains(all, bad) {
			t.Errorf("the ring kept %q: %s", bad, all)
		}
	}
	if got[0].Attrs["route"] != "GET /s/{token}" || got[1].Attrs["status"] != "500" || got[0].Level != "WARN" {
		t.Errorf("lines: %+v", got)
	}
	for i := 0; i < ringSize+50; i++ {
		lg.Warn("again")
	}
	if n := len(ring.snapshot()); n != ringSize {
		t.Errorf("the ring is not bounded: %d", n)
	}
}

var _ = http.StatusOK

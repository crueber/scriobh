package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// postHX is post() as htmx sends it.
func (s *Server) postHX(t *testing.T, c caller, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	_, info, err := s.Sess.Lookup(context.Background(), c.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("X-CSRF-Token", s.Sess.CSRFToken(info.ID))
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestLibraryViewsAndActions(t *testing.T) {
	e := newWriteEnv(t) // "The Salt Road" and "Other Book", both owned by e.owner
	s := e.s
	get := func(path string) string {
		rec := s.do(e.owner, "GET", path, "")
		if rec.Code != 200 {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		lint(t, path, rec.Body.String())
		structure(t, path, rec.Body.String())
		return rec.Body.String()
	}

	// Nothing opened yet: no Continue card. Open a document: the card appears and resumes there.
	body := get("/")
	if strings.Contains(body, "Continue where you left off") {
		t.Error("a Continue card without anything opened")
	}
	e.call(t, "project.open", projects.OpenIn{ID: e.pid, DocumentID: e.chap})
	body = get("/")
	if !strings.Contains(body, "Continue where you left off") || !strings.Contains(body, `href="/p/`+e.slug+`/d/`+e.chap+`"`) {
		t.Error("Continue where I left off does not resume at the last document")
	}
	// The project page itself resumes at the last document, too.
	second := e.create(t, "Second", "chapter", "", "")
	e.call(t, "project.open", projects.OpenIn{ID: e.pid, DocumentID: second})
	if rec := s.do(e.owner, "GET", "/p/"+e.slug, ""); !strings.Contains(rec.Body.String(), `data-doc="`+second+`"`) {
		t.Error("/p/slug did not resume at the last document")
	}

	// Pin: the tile's button flips, through htmx (a fragment) and without (a redirect).
	rec := s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/pin", url.Values{"pinned": {"true"}, "view": {"active"}})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="shelf-wrap"`) || !strings.Contains(rec.Body.String(), `aria-pressed="true"`) {
		t.Fatalf("pin: %d %s", rec.Code, rec.Body)
	}
	if rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/pin", url.Values{"pinned": {"false"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("pin without htmx: %d", rec.Code)
	}

	// Archive: gone from the shelf, present in Archived, with an Undo in the toast header.
	rec = s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/archive", url.Values{"view": {"active"}})
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `/p/`+e.slug) || !strings.Contains(rec.Header().Get("HX-Trigger"), "scriobh:toast") || !strings.Contains(rec.Header().Get("HX-Trigger"), "unarchive") {
		t.Fatalf("archive: %d trigger=%q", rec.Code, rec.Header().Get("HX-Trigger"))
	}
	for _, c := range rec.Header().Get("HX-Trigger") {
		if c > 0x7e {
			t.Fatalf("the HX-Trigger header carries a non-ASCII character %q", c)
		}
	}
	// Archived means hidden from the shelf, still fully readable.
	if rec := s.do(e.owner, "GET", "/p/"+e.slug, ""); rec.Code != 200 {
		t.Errorf("an archived project must still open: %d", rec.Code)
	}
	if !strings.Contains(get("/?state=archived"), "The Salt Road") || strings.Contains(get("/"), "The Salt Road") {
		t.Error("archived project is in the wrong view")
	}
	rec = s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/unarchive", url.Values{"view": {"archived"}})
	if rec.Code != 200 || !strings.Contains(get("/"), "The Salt Road") {
		t.Fatalf("unarchive: %d", rec.Code)
	}

	// Trash, then the typed-title purge.
	if rec := s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/trash", url.Values{"view": {"active"}}); rec.Code != 200 {
		t.Fatalf("trash: %d", rec.Code)
	}
	// In the trash the project is out of reach by URL as well (restore it from the library first).
	for _, u := range []string{"/p/" + e.slug, "/p/" + e.slug + "/d/" + e.chap, "/p/" + e.slug + "/search?q=x"} {
		if rec := s.do(e.owner, "GET", u, ""); rec.Code != 404 {
			t.Errorf("GET %s for a trashed project: %d, want 404", u, rec.Code)
		}
	}
	trash := get("/?state=trashed")
	if !strings.Contains(trash, "The Salt Road") || !strings.Contains(trash, "Delete forever") || strings.Contains(trash, `href="/p/`+e.slug) {
		t.Error("the trash view is wrong, or links into a trashed project")
	}
	form := s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/purge", "")
	lint(t, "purge page", form.Body.String())
	if form.Code != 200 || !strings.Contains(form.Body.String(), "Type the project title to confirm") {
		t.Fatalf("purge form: %d", form.Code)
	}
	rec = s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/purge", url.Values{"confirm_title": {"the salt road"}})
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), "exact title") || !strings.Contains(rec.Body.String(), `value="the salt road"`) {
		t.Fatalf("purge with a wrong title: %d %s", rec.Code, rec.Body)
	}
	rec = s.postHX(t, e.owner, "/ui/projects/"+e.pid+"/purge", url.Values{"confirm_title": {"The Salt Road"}})
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") == "" {
		t.Fatalf("purge: %d %v", rec.Code, rec.Header())
	}
	if rec := s.do(e.owner, "GET", "/p/"+e.slug, ""); rec.Code != 404 {
		t.Errorf("a purged project still opens: %d", rec.Code)
	}
}

func TestLibraryActionsAreGuarded(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	member := s.newCaller(t, "member", ops.RoleWriter)
	s.addMember(t, e.pid, member.userID, ops.ProjectEditor)
	for _, verb := range []string{"pin", "archive", "unarchive", "trash", "restore", "purge"} {
		path := "/ui/projects/" + e.pid + "/" + verb
		if rec := s.post(t, caller{name: "anon"}, path, nil, "-", false); rec.Code != http.StatusSeeOther {
			t.Errorf("%s anonymous: %d", verb, rec.Code)
		}
		if rec := s.post(t, e.owner, path, nil, "-", false); rec.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token: %d", verb, rec.Code)
		}
		if rec := s.post(t, stranger, path, nil, "", false); rec.Code != http.StatusNotFound {
			t.Errorf("%s as a stranger: %d, want 404", verb, rec.Code)
		}
		if verb == "pin" {
			continue // any member may pin for themselves
		}
		if rec := s.post(t, member, path, url.Values{"confirm_title": {"The Salt Road"}}, "", false); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("%s as an editor (owner only): %d", verb, rec.Code)
		}
	}
	// An editor cannot delete for good, so the confirmation form is not offered to them.
	if rec := s.do(member, "GET", "/ui/projects/"+e.pid+"/purge", ""); rec.Code != http.StatusForbidden {
		t.Errorf("the purge form for an editor: %d, want 403", rec.Code)
	}
	for _, p := range []string{"/ui/projects/" + e.pid + "/menu", "/ui/projects/" + e.pid + "/purge"} {
		if rec := s.do(stranger, "GET", p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s as a stranger: %d", p, rec.Code)
		}
	}
	// The owner's project is still there after all of that.
	if rec := s.do(e.owner, "GET", "/p/"+e.slug, ""); rec.Code != 200 {
		t.Errorf("project lost: %d", rec.Code)
	}
}

func TestSettingsPreferencesProfileAndSessions(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	page := s.do(e.owner, "GET", "/settings", "")
	if page.Code != 200 {
		t.Fatalf("settings: %d", page.Code)
	}
	lint(t, "settings", page.Body.String())
	structure(t, "settings", page.Body.String())
	if strings.Contains(page.Body.String(), "data-theme=") {
		t.Error("by default the theme follows the system (no data-theme)")
	}
	if rec := s.do(caller{name: "anon"}, "GET", "/settings", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("settings signed out: %d", rec.Code)
	}

	// Preferences: saved, applied to <html> before first paint, and to the writing view.
	form := url.Values{"theme": {"dark"}, "density": {"spacious"}, "reduced_motion": {"on"}, "editor_font": {"sans"}, "editor_size": {"22"},
		"editor_measure": {"60"}, "editor_line_height": {"1.45"}, "autosave_ms": {"1000"}, "show_word_count": {"false", "false"}, "typewriter": {"false", "true"}, "focus_dim": {"false"}}
	rec := s.post(t, e.owner, "/ui/settings/prefs", form, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/settings?saved=prefs") {
		t.Fatalf("save prefs: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	body := s.do(e.owner, "GET", "/p/"+e.slug, "").Body.String()
	for _, want := range []string{`<html lang="en" data-theme="dark" data-density="roomy" data-motion="reduce">`, `data-size="22"`, `data-measure="60"`, `data-lh="145"`, `data-font="sans"`, `data-autosave="1000"`, `data-counts="false"`, `data-typewriter="true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the writing view lacks %s", want)
		}
	}
	lib := s.do(e.owner, "GET", "/", "").Body.String()
	if !strings.Contains(lib, `data-theme="dark"`) {
		t.Error("the library does not apply the theme")
	}
	saved := s.do(e.owner, "GET", "/settings?saved=prefs", "").Body.String()
	if !strings.Contains(saved, "Settings saved.") || !strings.Contains(saved, `<option value="dark" selected>`) {
		t.Error("the settings page does not show what was saved")
	}
	// One bad value is refused and the page says which.
	bad := s.post(t, e.owner, "/ui/settings/prefs", url.Values{"editor_size": {"99"}}, "", false)
	if bad.Code != 422 || !strings.Contains(bad.Body.String(), "editor_size") && !strings.Contains(bad.Body.String(), "role=\"alert\"") {
		t.Errorf("a bad preference: %d", bad.Code)
	}

	// Profile.
	rec = s.post(t, e.owner, "/ui/settings/profile", url.Values{"display_name": {"Máire Nolan"}, "timezone": {"Europe/Dublin"}, "locale": {"ga"}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save profile: %d", rec.Code)
	}
	if b := s.do(e.owner, "GET", "/settings", "").Body.String(); !strings.Contains(b, `value="Máire Nolan"`) {
		t.Error("the display name was not saved")
	}
	if rec := s.post(t, e.owner, "/ui/settings/profile", url.Values{"display_name": {"  "}}, "", false); rec.Code != 422 || !strings.Contains(rec.Body.String(), "Give yourself a name.") {
		t.Errorf("an empty name: %d", rec.Code)
	}
	if rec := s.post(t, e.owner, "/ui/settings/profile", url.Values{"display_name": {"X"}, "timezone": {"Mars/Olympus"}}, "", false); rec.Code != 422 {
		t.Errorf("a bad time zone: %d", rec.Code)
	}

	// Sessions: sign the others out; revoking one's own session signs one out.
	other := s.newCaller(t, "second-device", ops.RoleWriter)
	_ = other
	tok2, _, err := s.Sess.Create(context.Background(), e.owner.userID, "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Version/17.0 Mobile/15E148 Safari/604.1", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	sess := s.do(e.owner, "GET", "/settings", "").Body.String()
	if !strings.Contains(sess, "Safari on iOS") || !strings.Contains(sess, "This device") || !strings.Contains(sess, "Sign out the other 1 device") {
		t.Errorf("the sessions list is wrong: %s", sess[strings.Index(sess, "Signed-in devices"):])
	}
	if rec := s.post(t, e.owner, "/ui/sessions/revoke-others", nil, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke others: %d", rec.Code)
	}
	if _, _, err := s.Sess.Lookup(context.Background(), tok2); err == nil {
		t.Error("the other session still works")
	}
	if rec := s.do(e.owner, "GET", "/settings", ""); rec.Code != 200 {
		t.Errorf("this session was signed out too: %d", rec.Code)
	}
	_, info, _ := s.Sess.Lookup(context.Background(), e.owner.cookie.Value)
	rec = s.post(t, e.owner, "/ui/sessions/"+info.ID+"/revoke", nil, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Set-Cookie"), "Max-Age=0") && !strings.Contains(rec.Header().Get("Set-Cookie"), "expires") {
		t.Errorf("signing out here: %d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	if rec := s.do(e.owner, "GET", "/settings", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("a revoked session still opens settings: %d", rec.Code)
	}
	// Someone else's session is not found.
	if rec := s.post(t, other, "/ui/sessions/"+info.ID+"/revoke", nil, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("revoking another person's session: %d", rec.Code)
	}
}

func TestSettingsUsesTheProfileTimeZoneAndOffersZones(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	if rec := s.post(t, e.owner, "/ui/settings/profile", url.Values{"display_name": {"Wren"}, "timezone": {"Pacific/Auckland"}, "locale": {"en"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("profile: %d", rec.Code)
	}
	body := s.do(e.owner, "GET", "/settings", "").Body.String()
	auck, _ := time.LoadLocation("Pacific/Auckland")
	now := time.Now().In(auck)
	if !strings.Contains(body, now.Format("2 Jan 2006, 15:04")) && !strings.Contains(body, now.Add(-time.Minute).Format("2 Jan 2006, 15:04")) {
		t.Errorf("a session's last use is not shown in the profile's zone (%s)", now.Format("2 Jan 2006, 15:04"))
	}
	if !strings.Contains(body, `<datalist id="tz-list">`) || !strings.Contains(body, `<option value="Europe/Dublin">`) || !strings.Contains(body, `list="tz-list"`) {
		t.Error("no list of time zones to pick from")
	}
	// A forced theme is also the browser chrome's colour; following the system keeps both.
	if strings.Count(body, `name="theme-color"`) != 2 {
		t.Error("following the system keeps both theme-color metas")
	}
	s.post(t, e.owner, "/ui/settings/prefs", url.Values{"theme": {"dark"}}, "", false)
	forced := s.do(e.owner, "GET", "/settings", "").Body.String()
	if strings.Count(forced, `name="theme-color"`) != 1 || !strings.Contains(forced, `name="theme-color" content="#131311">`) {
		t.Error("a forced dark theme must give a single dark theme-color")
	}
}

func TestHTMLAttrs(t *testing.T) {
	th, de, mo := htmlAttrs(map[string]string{"theme": "system", "density": "comfortable", "reduced_motion": "system"})
	if th != "" || de != "" || mo != "" {
		t.Errorf("system defaults must render nothing: %q %q %q", th, de, mo)
	}
	th, de, mo = htmlAttrs(map[string]string{"theme": "light", "density": "compact", "reduced_motion": "off"})
	if th != "light" || de != "compact" || mo != "full" {
		t.Errorf("%q %q %q", th, de, mo)
	}
	e := editorPrefsFrom(map[string]string{"editor_line_height": "1.4", "editor_size": "16", "editor_measure": "full"})
	if e.LH != 140 || e.Size != 16 || e.Measure != "full" {
		t.Errorf("%+v", e)
	}
}

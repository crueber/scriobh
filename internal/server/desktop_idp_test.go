//go:build !prod

package server

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/desktop"
	"github.com/crueber/scriobh/internal/oidcstub"
	"github.com/crueber/scriobh/internal/ops"
)

// The switch between the embedded issuer and an external provider (F-390), with the development issuer standing in
// for the external one: it is a separate HTTP server here, as an organisation's provider would be. (This file is not
// in a production build's tests because the stand-in is not in a production build.)

type extIdP struct {
	ts     *httptest.Server
	issuer string
}

// externalIdP starts the stand-in. It accepts whatever callback address the test's servers have (the port changes
// with every restart, as a provider that allows any loopback port does).
func externalIdP(t *testing.T) *extIdP {
	t.Helper()
	x := &extIdP{}
	stub, err := oidcstub.New(oidcstub.Config{IssuerFunc: func() string { return x.issuer }, ClientID: "scriobh-at-corp", ClientSecret: "corp-client-secret-0123456789",
		RedirectAllowed: func(u string) bool {
			return strings.HasPrefix(u, "http://127.0.0.1:") && strings.HasSuffix(u, "/auth/oidc/callback")
		}})
	if err != nil {
		t.Fatal(err)
	}
	x.ts = httptest.NewServer(stub.Handler())
	t.Cleanup(x.ts.Close)
	x.issuer = x.ts.URL
	return x
}

// signInAt drives the provider's page: the window is at the provider's authorization page (a form per test user);
// it submits the form for sub and follows the redirect back to the application.
func (win *window) signInAt(x *extIdP, p page, sub string) page {
	win.w.t.Helper()
	if !strings.HasPrefix(p.url, x.issuer+"/authorize") {
		win.w.t.Fatalf("the window is at %s (%d), not at the provider:\n%.300s", p.url, p.status, p.body)
	}
	u, _ := url.Parse(p.url)
	form := u.Query()
	form.Set("sub", sub)
	return win.do("POST", x.issuer+"/authorize", form, nil)
}

func (w *deskWorld) idpState() desktop.State {
	st, err := desktop.LoadState(context.Background(), w.d.Read())
	if err != nil {
		w.t.Fatal(err)
	}
	return st
}

func (win *window) identityForm(action string, form url.Values) page {
	win.w.t.Helper()
	p := win.get("/desktop/identity")
	if p.status != 200 {
		win.w.t.Fatalf("/desktop/identity: %d", p.status)
	}
	form.Set("_csrf", csrfOf(win.w.t, p.body))
	if m := identityFieldRE.FindStringSubmatch(p.body); m != nil && action == "switch" && form.Get("identity") == "" {
		form.Set("identity", m[1]) // what the page showed, as the browser would send it
	}
	return win.do("POST", "/ui/desktop/identity/"+action, form, nil)
}

var identityFieldRE = regexp.MustCompile(`name="identity" value="([0-9a-f]+)"`)

// TestDesktopTheSwitchBindsTheIdentityThePageShowed: a second sign-in at the provider recorded after the page was drawn
// replaces who was proved. The confirmation carries the fingerprint of the identity the page showed, and a switch
// whose fingerprint is not the recorded one is refused, so the account is never handed to an identity nobody looked at.
func TestDesktopTheSwitchBindsTheIdentityThePageShowed(t *testing.T) {
	x := externalIdP(t)
	w := newDesk(t, nil)
	win := w.firstRun("Ada Local", "")
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"`+x.issuer+`","client_id":"scriobh-at-corp","client_secret":"corp-client-secret-0123456789"}`); st != 200 {
		t.Fatal("set")
	}
	win.signInAt(x, win.get("/desktop/identity/verify"), "dev-writer")
	shown := win.get("/desktop/identity")
	m := identityFieldRE.FindStringSubmatch(shown.body)
	if m == nil || !strings.Contains(shown.body, "writer@dev.scriobh") {
		t.Fatalf("the page does not carry the identity it shows:\n%.400s", shown.body)
	}
	if strings.Contains(shown.body, "dev-writer") {
		t.Error("the page shows the subject")
	}
	// Another identity is proved after the page was drawn.
	win.signInAt(x, win.get("/desktop/identity/verify"), "dev-admin")
	if st := w.idpState(); st.Verified == nil || st.Verified.Subject != "dev-admin" {
		t.Fatalf("the second proof was not recorded: %+v", st.Verified)
	}
	stale := url.Values{"_csrf": {csrfOf(t, shown.body)}, "confirm": {x.issuer}, "identity": {m[1]}}
	p := win.do("POST", "/ui/desktop/identity/switch", stale, nil)
	if w.idpState().Pending != nil || !strings.Contains(p.body, `role="alert"`) {
		t.Fatalf("a switch confirmed against the identity that was shown first was made (pending %+v)", w.idpState().Pending)
	}
	// Over the API: missing, and wrong.
	for _, body := range []string{`{"to":"external","confirm":"` + x.issuer + `"}`, `{"to":"external","confirm":"` + x.issuer + `","identity":"` + m[1] + `"}`} {
		if st, out := win.json("POST", "/api/v1/desktop/idp/switch", body); st != http.StatusConflict {
			t.Errorf("%s: %d %v", body, st, out)
		}
	}
	if w.idpState().Pending != nil {
		t.Fatal("a switch was recorded without the identity being confirmed")
	}
	// The page as it is now carries the identity now recorded, and its confirmation is accepted.
	win.identityForm("switch", url.Values{"confirm": {x.issuer}})
	if w.idpState().Pending == nil {
		t.Error("the switch with the current fingerprint was refused")
	}
}

func TestDesktopSwitchToAnExternalProviderAndBack(t *testing.T) {
	x := externalIdP(t)
	w := newDesk(t, nil)
	win := w.firstRun("Ada Local", "admin@dev.scriobh") // the same address the provider's dev-admin has: it must not matter
	local := w.users()[0]
	st, proj := win.json("POST", "/api/v1/projects", `{"title":"Before the switch","kind":"blank"}`)
	pid := findString(proj, "id")
	if st != 201 {
		t.Fatalf("project.create: %d", st)
	}

	// The page, before anything is entered.
	p := win.get("/desktop/identity")
	lint(t, "desktop-identity", p.body)
	structure(t, "desktop-identity", p.body)
	if !strings.Contains(p.body, `data-mode="embedded"`) || strings.Contains(p.body, `id="di-switch"`) {
		t.Error("the identity page before any settings")
	}

	// A switch with nothing proved is refused, whatever is typed.
	if st, out := win.json("POST", "/api/v1/desktop/idp/switch", `{"to":"external","confirm":"`+x.issuer+`"}`); st != http.StatusConflict {
		t.Errorf("a switch with no provider: %d %v", st, out)
	}

	// Save the provider. The secret is sealed and never comes back.
	p = win.identityForm("save", url.Values{"issuer": {x.issuer}, "client_id": {"scriobh-at-corp"}, "client_secret": {"corp-client-secret-0123456789"}})
	if !strings.Contains(p.url, "saved=1") {
		t.Fatalf("save: %s %d", p.url, p.status)
	}
	if strings.Contains(p.body, "corp-client-secret") {
		t.Error("the page shows the client secret")
	}
	if raw := w.idpState(); raw.External == nil || raw.External.Secret == "" || strings.Contains(raw.External.Secret, "corp-client-secret") {
		t.Errorf("the stored provider: %+v", raw.External)
	}
	_, got := win.json("GET", "/api/v1/desktop/idp", "")
	if ext, _ := got["external"].(map[string]any); ext == nil || ext["secret_set"] != true || ext["client_secret"] != nil || ext["secret"] != nil {
		t.Errorf("desktop.idp.get: %v", got)
	}
	if st, _ := win.json("POST", "/api/v1/desktop/idp/switch", `{"to":"external","confirm":"`+x.issuer+`"}`); st != http.StatusConflict {
		t.Errorf("a switch before signing in at the provider: %d", st)
	}
	// A proof nobody made.
	for _, proof := range []string{"x", "v1.AAAA.BBBB", strings.Repeat("A", 200)} {
		if st, _ := win.json("POST", "/api/v1/desktop/idp/verify", `{"proof":"`+proof+`"}`); st != http.StatusUnprocessableEntity && st != http.StatusBadRequest {
			t.Errorf("a forged proof: %d", st)
		}
	}
	if w.idpState().Verified != nil {
		t.Fatal("a forged proof was recorded")
	}

	// Prove it: sign in at the provider as dev-writer (another address than the local profile's).
	p = win.signInAt(x, win.get("/desktop/identity/verify"), "dev-writer")
	if !strings.Contains(p.url, "/desktop/identity?verified=1") || !strings.Contains(p.body, "writer@dev.scriobh") {
		t.Fatalf("after the sign-in at the provider: %s %d\n%.300s", p.url, p.status, p.body)
	}
	if us := w.users(); len(us) != 1 || us[0]["issuer"] != local["issuer"] {
		t.Fatalf("proving the provider changed the accounts: %v", us)
	}
	// The typed confirmation is exact.
	p = win.identityForm("switch", url.Values{"confirm": {x.issuer + "/"}})
	if !strings.Contains(p.body, `role="alert"`) || w.idpState().Pending != nil {
		t.Error("a wrong confirmation was accepted")
	}
	p = win.identityForm("switch", url.Values{"confirm": {x.issuer}})
	if w.idpState().Pending == nil || !strings.Contains(p.body, `id="di-pending"`) {
		t.Fatalf("the switch was not recorded: %s", p.url)
	}
	// Until the restart nothing has changed: the issuer is still there and the account is still the local one.
	if p := win.get("/desktop-idp/jwks"); p.status != 200 {
		t.Errorf("the embedded issuer before the restart: %d", p.status)
	}
	if us := w.users(); us[0]["issuer"] != local["issuer"] || us[0]["sub"] != local["sub"] {
		t.Errorf("the account moved before the restart: %v", us)
	}
	// Changing the settings while a switch waits is refused.
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"https://other.example","client_id":"c"}`); st != http.StatusConflict {
		t.Errorf("desktop.idp.set while a switch waits: %d", st)
	}

	// ---- restart: the external provider ------------------------------------
	w.restart("aa" + deskToken[2:])
	if !w.cfg.DesktopExternal || w.s.desk.issuer != nil {
		t.Fatal("after the restart the instance is not in external mode")
	}
	us := w.users()
	if len(us) != 1 || us[0]["id"] != local["id"] || us[0]["issuer"] != x.issuer || us[0]["sub"] != "dev-writer" || us[0]["role"] != ops.RoleAdmin {
		t.Fatalf("after the switch: %v", us)
	}
	for _, path := range []string{"/desktop-idp/jwks", "/desktop-idp/.well-known/openid-configuration", "/desktop-idp/authorize", "/desktop/profile"} {
		if p := win.get(path); p.status != 404 {
			t.Errorf("%s in external mode: %d, want 404 (the embedded issuer is not mounted)", path, p.status)
		}
	}
	// The session survived the restart; a new window signs in at the provider and is the same account, with its book.
	if st, _ := win.json("GET", "/api/v1/projects/"+pid, ""); st != 200 {
		t.Errorf("the old session after the restart: %d", st)
	}
	win2 := w.window()
	// The shell's window cannot show the provider's page: the start address says so, with the address to open in a browser.
	p = win2.get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"])
	if p.status != 200 || !strings.HasPrefix(p.url, w.base+"/desktop/start") || !strings.Contains(p.body, `id="dx-address"`) || !strings.Contains(p.body, w.base+"<") || strings.Contains(p.body, "signed-in") {
		t.Fatalf("in external mode the start address must show where to sign in: %s %d", p.url, p.status)
	}
	lint(t, "desktop-external", p.body)
	structure(t, "desktop-external", p.body)
	if p := w.window().get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"]); p.status != 404 {
		t.Errorf("the page is shown for a used token: %d", p.status)
	}
	if p := w.window().get("/desktop/start?t=" + strings.Repeat("9", 64)); p.status != 404 || strings.Contains(p.body, "dx-address") {
		t.Errorf("the page is shown without the launch token: %d", p.status)
	}
	p = win2.signInAt(x, win2.get("/auth/login"), "dev-writer")
	if p.url != w.base+"/" || !strings.Contains(p.body, "Before the switch") {
		t.Fatalf("signing in at the provider: %s %d", p.url, p.status)
	}
	if us := w.users(); len(us) != 1 || us[0]["id"] != local["id"] || us[0]["role"] != ops.RoleAdmin || us[0]["email"] != "writer@dev.scriobh" {
		t.Errorf("after signing in at the provider: %v", us)
	}
	// Another identity at the provider is another account, and never the profile's, whatever its address.
	win3 := w.window()
	win3.get("/desktop/start?t=x")
	p = win3.signInAt(x, win3.get("/auth/login"), "dev-reader")
	if strings.Contains(p.body, "Before the switch") {
		t.Error("another identity sees the profile's project")
	}
	us = w.users()
	if len(us) != 2 || us[1]["id"] == local["id"] || us[1]["role"] != ops.RoleReader {
		t.Errorf("another identity at the provider: %v", us)
	}
	// The way back needs an administrator's session: the reader is told there is nothing there.
	if p := win3.get("/desktop/identity"); p.status != 404 {
		t.Errorf("the identity page as a reader: %d", p.status)
	}
	if st, _ := win3.json("POST", "/api/v1/desktop/idp/switch", `{"to":"embedded","confirm":"Ada Local"}`); st != 404 {
		t.Errorf("desktop.idp.switch as a reader: %d", st)
	}
	p = win2.get("/desktop/identity")
	lint(t, "desktop-identity external", p.body)
	if !strings.Contains(p.body, `data-mode="external"`) || !strings.Contains(p.body, `id="di-back"`) || strings.Contains(p.body, `action="/ui/desktop/identity/save"`) {
		t.Error("the identity page in external mode")
	}
	if st, _ := win2.json("PUT", "/api/v1/desktop/idp", `{"issuer":"https://other.example","client_id":"c"}`); st != http.StatusConflict {
		t.Errorf("desktop.idp.set in external mode: %d", st)
	}
	p = win2.identityForm("back", url.Values{"confirm": {"ada local"}})
	if w.idpState().Pending != nil {
		t.Error("a wrong name was accepted as the confirmation")
	}
	win2.identityForm("back", url.Values{"confirm": {"Ada Local"}})
	if pd := w.idpState().Pending; pd == nil || pd.To != desktop.ModeEmbedded {
		t.Fatal("the way back was not recorded")
	}
	// Cancel, and ask again.
	win2.identityForm("cancel", url.Values{})
	if w.idpState().Pending != nil {
		t.Fatal("the cancel did not cancel")
	}
	win2.identityForm("back", url.Values{"confirm": {"Ada Local"}})

	// ---- restart: the local profile again -----------------------------------
	w.restart("bb" + deskToken[2:])
	if w.cfg.DesktopExternal || w.s.desk.issuer == nil {
		t.Fatal("after going back the instance is not embedded")
	}
	win4 := w.window()
	p = win4.get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"])
	if p.url != w.base+"/" || !strings.Contains(p.body, "Before the switch") {
		t.Fatalf("the local profile after going back: %s", p.url)
	}
	us = w.users()
	if us[0]["id"] != local["id"] || us[0]["issuer"] != local["issuer"] || us[0]["sub"] != local["sub"] || us[0]["role"] != ops.RoleAdmin {
		t.Errorf("after going back: %v", us)
	}
	var applied int
	_ = w.d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'desktop.idp.applied'`).Scan(&applied)
	if applied != 2 {
		t.Errorf("%d switches are in the audit log, want 2", applied)
	}
	if logs := w.logs.String(); strings.Contains(logs, "corp-client-secret") {
		t.Error("the client secret is in the log")
	}
}

// TestDesktopSwitchCannotLockTheAdministratorOut: the checks made when the provider is proved, before any switch.
func TestDesktopSwitchCannotLockTheAdministratorOut(t *testing.T) {
	x := externalIdP(t)
	w := newDesk(t, nil)
	win := w.firstRun("Ada Local", "")
	save := func(extra string) {
		t.Helper()
		if st, out := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"`+x.issuer+`","client_id":"scriobh-at-corp","client_secret":"corp-client-secret-0123456789"`+extra+`}`); st != 200 {
			t.Fatalf("desktop.idp.set: %d %v", st, out)
		}
	}
	// An administrators' group the identity is not in: the proof is refused, with the reason.
	save(`,"admin_group":"scriobh-admins"`)
	p := win.signInAt(x, win.get("/desktop/identity/verify"), "dev-writer")
	if w.idpState().Verified != nil || !strings.Contains(p.body, "administrators") {
		t.Errorf("an identity outside the administrators' group was accepted: %s", p.url)
	}
	// In it: accepted.
	p = win.signInAt(x, win.get("/desktop/identity/verify"), "dev-admin")
	if w.idpState().Verified == nil {
		t.Fatalf("an identity in the administrators' group was refused: %s\n%.300s", p.url, p.body)
	}
	// Changing the settings discards the proof.
	save(`,"admin_group":"scriobh-admins","port":45999`)
	if w.idpState().Verified != nil {
		t.Error("the proof outlived a change of settings")
	}
	if st, _ := win.json("POST", "/api/v1/desktop/idp/switch", `{"to":"external","confirm":"`+x.issuer+`"}`); st != http.StatusConflict {
		t.Errorf("a switch after the settings changed: %d", st)
	}
	// A wrong client secret: the provider refuses the exchange, nothing is recorded, and the page says so.
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"`+x.issuer+`","client_id":"scriobh-at-corp","client_secret":"wrong"}`); st != 200 {
		t.Fatal("set")
	}
	p = win.signInAt(x, win.get("/desktop/identity/verify"), "dev-admin")
	if w.idpState().Verified != nil || !strings.Contains(p.body, `role="alert"`) {
		t.Errorf("a provider that refused the client was accepted: %s", p.url)
	}
	// A provider that is not there.
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"http://127.0.0.1:1","client_id":"c"}`); st != 200 {
		t.Fatal("set")
	}
	if p := win.get("/desktop/identity/verify"); !strings.Contains(p.url, "/desktop/identity") || !strings.Contains(p.body, `role="alert"`) {
		t.Errorf("an unreachable provider: %s", p.url)
	}
	// The sign-in at the provider is begun only by this instance's own page.
	if p := win.hop("GET", "/desktop/identity/verify", nil, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); p.status != http.StatusForbidden {
		t.Errorf("another site began the sign-in at the provider: %d", p.status)
	}
	// A proof is for the administrator who made it, for five minutes.
	save("")
	win.signInAt(x, win.get("/desktop/identity/verify"), "dev-writer")
	if w.idpState().Verified == nil {
		t.Fatal("control: the proof was not recorded")
	}
	svc := w.s.desk.svc
	proof, err := svc.SealProof(context.Background(), w.d, "usr_someone_else", desktop.Identity{Issuer: x.issuer, Subject: "dev-admin", Email: "a@b.c"})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := win.json("POST", "/api/v1/desktop/idp/verify", `{"proof":"`+proof+`"}`); st == 200 {
		t.Error("a proof made for another administrator was accepted")
	}
	me := w.users()[0]["id"]
	proof, _ = svc.SealProof(context.Background(), w.d, me, desktop.Identity{Issuer: x.issuer, Subject: "dev-admin", Email: "a@b.c"})
	svc.Now = func() time.Time { return time.Now().Add(desktop.ProofTTL + time.Second) }
	if st, _ := win.json("POST", "/api/v1/desktop/idp/verify", `{"proof":"`+proof+`"}`); st == 200 {
		t.Error("an old proof was accepted")
	}
	svc.Now = nil
	if st, out := win.json("POST", "/api/v1/desktop/idp/verify", `{"proof":"`+proof+`"}`); st != 200 {
		t.Errorf("control: a fresh proof for this administrator: %d %v", st, out)
	}
	// Bad settings are refused with the field.
	for _, body := range []string{`{"issuer":"http://idp.example.org","client_id":"c"}`, `{"issuer":"https://idp.example.org","client_id":""}`,
		`{"issuer":"https://idp.example.org","client_id":"c","port":80}`, `{"issuer":"https://u:p@idp.example.org","client_id":"c"}`} {
		if st, _ := win.json("PUT", "/api/v1/desktop/idp", body); st != http.StatusUnprocessableEntity && st != http.StatusBadRequest {
			t.Errorf("%s: %d", body, st)
		}
	}
}

// TestDesktopRecoveryFromTheCommandLine: the provider is gone and nobody is signed in; the recovery puts the
// instance back on its local profile with the same account.
func TestDesktopRecoveryFromTheCommandLine(t *testing.T) {
	x := externalIdP(t)
	w := newDesk(t, nil)
	win := w.firstRun("Ada Local", "")
	local := w.users()[0]
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"`+x.issuer+`","client_id":"scriobh-at-corp","client_secret":"corp-client-secret-0123456789"}`); st != 200 {
		t.Fatal("set")
	}
	win.signInAt(x, win.get("/desktop/identity/verify"), "dev-writer")
	win.identityForm("switch", url.Values{"confirm": {x.issuer}})
	w.restart("cc" + deskToken[2:])
	x.ts.Close() // the provider is gone
	stranded := w.window()
	stranded.get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"])
	if p := stranded.get("/auth/login"); p.status != http.StatusBadGateway {
		t.Fatalf("signing in with the provider gone: %d, want the 502 page", p.status)
	}
	w.hs.Close()
	if changed, err := desktop.RequestEmbedded(context.Background(), w.d); err != nil || !changed {
		t.Fatalf("recovery: %v %v", changed, err)
	}
	w.restart("dd" + deskToken[2:])
	back := w.window()
	p := back.get("/desktop/start?t=" + w.env["SCRIOBH_LAUNCH_TOKEN"])
	if p.url != w.base+"/" || !strings.Contains(p.body, "signed-in") {
		t.Fatalf("after the recovery: %s", p.url)
	}
	if us := w.users(); len(us) != 1 || us[0]["id"] != local["id"] || us[0]["issuer"] != local["issuer"] || us[0]["role"] != ops.RoleAdmin {
		t.Errorf("after the recovery: %v", us)
	}
}

var ticketRE = regexp.MustCompile(`<code dir="ltr">(http://[^<]*/desktop/identity/verify\?ticket=[^<]+)</code>`)

// TestDesktopProveTheProviderFromAWebBrowser: the application's window does not open another site's page, so the
// sign-in that proves the provider is done in an ordinary browser, which has no session and no launch cookie, with an
// address made in the window. It records who the administrator is at the provider and signs nobody in.
func TestDesktopProveTheProviderFromAWebBrowser(t *testing.T) {
	x := externalIdP(t)
	w := newDesk(t, nil)
	win := w.firstRun("Ada Local", "")
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"`+x.issuer+`","client_id":"scriobh-at-corp","client_secret":"corp-client-secret-0123456789"}`); st != 200 {
		t.Fatal("set")
	}
	address := func() string {
		t.Helper()
		p := win.identityForm("ticket", url.Values{})
		m := ticketRE.FindStringSubmatch(p.body)
		if m == nil {
			t.Fatalf("the identity page shows no address for a web browser:\n%.300s", p.body)
		}
		lint(t, "desktop-identity with an address", p.body)
		return html.UnescapeString(m[1])
	}
	addr := address()
	// The address is shown once: a reload of the page does not repeat it.
	if p := win.get("/desktop/identity"); ticketRE.MatchString(p.body) {
		t.Error("the address is shown again on a reload")
	}

	browser := w.window() // no launch cookie, no session
	p := browser.signInAt(x, browser.get(addr), "dev-writer")
	if p.status != 200 || !strings.Contains(p.body, "Go back to Scríobh") || strings.Contains(p.body, "signed-in") {
		t.Fatalf("after the sign-in in the browser: %s %d\n%.300s", p.url, p.status, p.body)
	}
	baseURL, _ := url.Parse(w.base)
	for _, c := range browser.c.Jar.Cookies(baseURL) {
		if c.Name == "scriobh_session" {
			t.Error("the browser was signed in by the ticket")
		}
	}
	if p := browser.get("/desktop/identity"); strings.Contains(p.body, "External identity provider") {
		t.Error("the browser can read the identity page")
	}
	v := w.idpState().Verified
	if v == nil || v.Subject != "dev-writer" || v.UserID != w.users()[0]["id"] {
		t.Fatalf("the proof recorded: %+v", v)
	}
	// The window sees it and can confirm the switch.
	if p := win.get("/desktop/identity"); !strings.Contains(p.body, "writer@dev.scriobh") {
		t.Error("the window does not show who was proved")
	}
	win.identityForm("switch", url.Values{"confirm": {x.issuer}})
	if w.idpState().Pending == nil {
		t.Error("the switch could not be confirmed after a proof made in a browser")
	}
	win.identityForm("cancel", url.Values{})

	// A ticket is good once.
	if p := w.window().get(addr); p.status != http.StatusForbidden || !strings.Contains(p.body, "cannot be used") {
		t.Errorf("a used ticket: %d", p.status)
	}
	// Not after five minutes, not when forged, not when begun by another site's page.
	old := address()
	w.s.desk.svc.Now = func() time.Time { return time.Now().Add(desktop.ProofTTL + time.Second) }
	if p := w.window().get(old); p.status != http.StatusForbidden {
		t.Errorf("an old ticket: %d", p.status)
	}
	w.s.desk.svc.Now = nil
	for _, forged := range []string{"x", "v1.AAAA.BBBB", strings.Repeat("A", 5000)} {
		if p := w.window().get("/desktop/identity/verify?ticket=" + url.QueryEscape(forged)); p.status != http.StatusForbidden {
			t.Errorf("a forged ticket: %d", p.status)
		}
	}
	fresh := address()
	if p := w.window().hop("GET", strings.TrimPrefix(fresh, w.base), nil, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }); p.status != http.StatusForbidden {
		t.Errorf("a ticket's address followed from another site: %d", p.status)
	}
	// A ticket begun and then abandoned for another identity: whoever finishes the sign-in is who is recorded, and
	// the window shows the name before anything is confirmed. An identity the checks refuse is not recorded.
	if st, _ := win.json("PUT", "/api/v1/desktop/idp", `{"issuer":"`+x.issuer+`","client_id":"scriobh-at-corp","client_secret":"corp-client-secret-0123456789","admin_group":"scriobh-admins"}`); st != 200 {
		t.Fatal("set")
	}
	b2 := w.window()
	p = b2.signInAt(x, b2.get(address()), "dev-reader")
	if w.idpState().Verified != nil || p.status != http.StatusForbidden || !strings.Contains(p.body, "administrators") {
		t.Errorf("an identity outside the administrators' group was recorded through a ticket: %d", p.status)
	}
	// Without a ticket and without a session there is nothing: the sign-in page.
	if p := w.window().hop("GET", "/desktop/identity/verify", nil, nil); p.status != http.StatusSeeOther {
		t.Errorf("the proving address with neither a session nor a ticket: %d", p.status)
	}
}

// TestDesktopRestoredServerDatabaseHasAWayIn: a data directory that came from a server instance has accounts of another
// provider and none for the local profile. The window is refused, and says in words that fit desktop mode what happened
// and which command settles it; the command binds the profile to the administrator the person names, and then the same
// window signs in as that account, with its role and its address untouched.
func TestDesktopRestoredServerDatabaseHasAWayIn(t *testing.T) {
	w := newDesk(t, nil)
	now := db.Now()
	for _, u := range [][]string{{"usr_old", "admin", "old@example.org", "sub-1"}, {"usr_w", "writer", "w@example.org", "sub-2"}} {
		if _, err := w.d.Writer().Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, avatar_color, role, created_at, updated_at, last_seen_at)
			VALUES (?,?,?,?,?,'#000000',?,?,?,?)`, u[0], "https://idp.example.org", u[3], u[2], "Old "+u[0], u[1], now, now, now); err != nil {
			t.Fatal(err)
		}
	}
	win := w.window()
	p := win.get("/desktop/start?t=" + deskToken)
	p = win.do("POST", "/desktop/profile", url.Values{"_csrf": {csrfOf(t, p.body)}, "name": {"Me"}, "email": {"me@example.org"}}, nil)
	if p.status != http.StatusForbidden {
		t.Fatalf("the refusal: %d", p.status)
	}
	for _, want := range []string{"came from a Scríobh server", "scriobh desktop use-local-profile --adopt-admin"} {
		if !strings.Contains(p.body, want) {
			t.Errorf("the refusal does not say %q:\n%.500s", want, p.body)
		}
	}
	if strings.Contains(p.body, "scriobh-writers") || strings.Contains(p.body, "add you to") {
		t.Error("the refusal still talks about provider groups")
	}
	if n := len(w.users()); n != 2 {
		t.Fatalf("the refused sign-in made an account: %d users", n)
	}
	// The person runs the command (the application is stopped in real life; the effect is on the database alone).
	if _, err := desktop.AdoptAdmin(context.Background(), w.d, "usr_w"); err == nil {
		t.Fatal("a writer was adopted")
	}
	if _, err := desktop.AdoptAdmin(context.Background(), w.d, "old@example.org"); err != nil {
		t.Fatal(err)
	}
	p = win.get("/")
	if p.status != 200 || !strings.Contains(p.body, `class="signed-in`) {
		t.Fatalf("after the command the window ends at %s (%d):\n%.300s", p.url, p.status, p.body)
	}
	var role, email, name string
	if err := w.d.Read().QueryRow(`SELECT role, email, display_name FROM users WHERE id = 'usr_old'`).Scan(&role, &email, &name); err != nil {
		t.Fatal(err)
	}
	if role != "admin" || name != "Old usr_old" {
		t.Errorf("the account after the first sign-in: %s %q", role, name)
	}
	if email != "old@example.org" {
		t.Errorf("the account's address changed to %q: the profile must take the account's, not the other way", email)
	}
	if n := len(w.users()); n != 2 {
		t.Errorf("a sign-in made an account: %d users", n)
	}
	// The other account is as it was, and the profile cannot be taken by a second.
	for _, u := range w.users() {
		if u["id"] == "usr_w" && (u["issuer"] != "https://idp.example.org" || u["role"] != "writer") {
			t.Errorf("the other account changed: %v", u)
		}
	}
}

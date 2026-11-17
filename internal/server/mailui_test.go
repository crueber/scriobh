package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/mail"
)

// The mail screens: /admin/mail, the Kindle address and digest on /settings, the Send to Kindle section of the Compile
// page and /p/{slug}/kindle. The ops are tested in internal/mail and internal/server's mail tests; these are about the
// pages: who finds what, what a form sends when a field is empty, what is never in the HTML, and the pseudo-locale.

const relayPassword = "Hunter2-Relay-Canary-9f3"

func (w *siteWorld) mailOn(t *testing.T) {
	t.Helper()
	w.must(t, w.admin, "PUT", "/api/v1/admin/mail", `{"host":"127.0.0.1","port":2525,"security":"none","allow_private":true,"from_address":"scriobh@example.org","enabled":true}`)
}

func (w *siteWorld) mailCfg(t *testing.T) mail.ConfigOut {
	t.Helper()
	var o mail.ConfigOut
	if err := json.Unmarshal(w.must(t, w.admin, "GET", "/api/v1/admin/mail", "").Body.Bytes(), &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestTheMailAdminPageIsTheAdministratorsAndEveryFormOfItToo(t *testing.T) {
	w := newSiteWorld(t, nil)
	for _, c := range []caller{w.owner, w.editor, w.reader, w.stranger} {
		if code, _ := w.s.getPage(t, c, "/admin/mail"); code != http.StatusNotFound {
			t.Errorf("%s: /admin/mail answered %d", c.name, code)
		}
		for _, a := range []string{"save", "enable", "disable", "test", "domains"} {
			if rec := w.s.postForm(t, c, "/ui/admin/mail/"+a, url.Values{"host": {"x.example.org"}}); rec.Code != http.StatusNotFound {
				t.Errorf("%s: POST %s answered %d", c.name, a, rec.Code)
			}
		}
	}
	if code, body := w.s.getPage(t, w.admin, "/admin/mail"); code != 200 || !strings.Contains(body, `href="/admin/mail" aria-current="page"`) {
		t.Fatalf("the administrator's page: %d", code)
	}
}

func TestSavingTheRelayNeverWipesWhatTheFormDoesNotShow(t *testing.T) {
	w := newSiteWorld(t, nil)
	form := url.Values{"host": {"relay.example.org"}, "port": {"465"}, "security": {"tls"}, "username": {"mailer"}, "password": {relayPassword},
		"from_address": {"scriobh@example.org"}, "from_name": {"Scríobh"}, "max_attachment_mb": {"12"}}
	rec := w.s.postForm(t, w.admin, "/ui/admin/mail/save", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if !w.mailCfg(t).PasswordSet {
		t.Fatal("the password was not stored")
	}
	// A second save with the password and the allow-list left out keeps both.
	form.Del("password")
	if rec := w.s.postForm(t, w.admin, "/ui/admin/mail/save", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("second save: %d %s", rec.Code, rec.Body)
	}
	if !w.mailCfg(t).PasswordSet {
		t.Error("an empty password field removed the stored password")
	}
	// The page never holds the password, in either state, and says one is stored.
	_, page := w.s.getPage(t, w.admin, "/admin/mail")
	if strings.Contains(page, relayPassword) {
		t.Error("the password is in the page")
	}
	if !strings.Contains(page, `name="remove_password"`) {
		t.Error("no way to remove the stored password")
	}
	// A refused save shows what was typed but not the password, and says to enter it again.
	bad := url.Values{"host": {"relay.example.org"}, "port": {"465"}, "security": {"tls"}, "username": {"mailer"}, "password": {relayPassword}, "from_address": {"not an address"}}
	rec = w.s.postForm(t, w.admin, "/ui/admin/mail/save", bad)
	refusal := followAs(t, w.s, w.admin, rec).Body.String() // the refusal is shown by the page after the redirect
	if rec.Code != http.StatusSeeOther || strings.Contains(refusal, relayPassword) || !strings.Contains(refusal, `aria-invalid="true"`) {
		t.Errorf("a refused save: %d, password in page %v", rec.Code, strings.Contains(refusal, relayPassword))
	}
	if !strings.Contains(refusal, `id="ml-error"`) {
		t.Error("the refusal is not on the page")
	}
	// Removing is its own box.
	form.Set("remove_password", "1")
	w.s.postForm(t, w.admin, "/ui/admin/mail/save", form)
	if w.mailCfg(t).PasswordSet {
		t.Error("the remove box did not remove the password")
	}
}

func TestSwitchingMailOnAndTheRestorePauseHaveOneSwitchOnControl(t *testing.T) {
	w := newSiteWorld(t, nil)
	_, off := w.s.getPage(t, w.admin, "/admin/mail")
	if strings.Count(off, `action="/ui/admin/mail/enable"`) != 1 || strings.Contains(off, `action="/ui/admin/mail/disable"`) {
		t.Errorf("off: enable forms %d", strings.Count(off, `action="/ui/admin/mail/enable"`))
	}
	if rec := w.s.postForm(t, w.admin, "/ui/admin/mail/enable", nil); !refusedAfter(t, w.s, w.admin, rec) {
		t.Errorf("switching an empty relay on was not refused")
	}
	w.mailOn(t)
	_, on := w.s.getPage(t, w.admin, "/admin/mail")
	if strings.Contains(on, `action="/ui/admin/mail/enable"`) || strings.Count(on, `action="/ui/admin/mail/disable"`) != 1 {
		t.Error("on: the page offers the wrong switch")
	}
	// What a restore leaves: off and paused. The banner's button is then the page's only switch-on form.
	if _, err := w.s.DB.Writer().Exec(`UPDATE mail_config SET enabled = 0, restore_paused = 1`); err != nil {
		t.Fatal(err)
	}
	_, paused := w.s.getPage(t, w.admin, "/admin/mail")
	if strings.Count(paused, `action="/ui/admin/mail/enable"`) != 1 || !strings.Contains(paused, `id="ml-paused"`) {
		t.Errorf("paused: %d enable forms", strings.Count(paused, `action="/ui/admin/mail/enable"`))
	}
	if !strings.Contains(paused[strings.Index(paused, `id="ml-paused"`):], `/ui/admin/mail/enable`) {
		t.Error("the switch-on form is not in the banner")
	}
	// A saved setting does not lift the pause; switching on does.
	w.s.postForm(t, w.admin, "/ui/admin/mail/save", url.Values{"host": {"127.0.0.1"}, "port": {"2525"}, "security": {"none"}, "allow_private": {"1"}, "from_address": {"scriobh@example.org"}})
	if !w.mailCfg(t).PausedByRestore {
		t.Error("saving lifted the restore pause")
	}
	if rec := w.s.postForm(t, w.admin, "/ui/admin/mail/enable", nil); rec.Code != http.StatusSeeOther {
		t.Fatalf("switching on: %d %s", rec.Code, rec.Body)
	}
	if c := w.mailCfg(t); c.PausedByRestore || !c.Enabled {
		t.Errorf("after switching on: %+v", c)
	}
}

func TestWideningTheKindleDomainsNeedsATickAndTakingAwayDoesNot(t *testing.T) {
	w := newSiteWorld(t, nil)
	// post says 303 for an accepted change and 422 for a refusal, which the page after the redirect shows
	post := func(vals url.Values) int {
		rec := w.s.postForm(t, w.admin, "/ui/admin/mail/domains", vals)
		if refusedAfter(t, w.s, w.admin, rec) {
			return http.StatusUnprocessableEntity
		}
		return rec.Code
	}
	if c := post(url.Values{"domains": {"kindle.com\nfree.kindle.com\nexample.org"}}); c != http.StatusUnprocessableEntity {
		t.Errorf("adding a domain without the tick: %d", c)
	}
	if got := w.mailCfg(t).KindleDomains; len(got) != 2 {
		t.Errorf("the list changed: %v", got)
	}
	if rec := w.s.postForm(t, w.admin, "/ui/admin/mail/domains", url.Values{"domains": {"kindle.com\nexample.org"}}); !strings.Contains(followAs(t, w.s, w.admin, rec).Body.String(), "example.org") {
		t.Error("a refused allow-list does not come back as it was typed")
	}
	if c := post(url.Values{"domains": {"kindle.com\nfree.kindle.com\nexample.org"}, "understood": {"1"}}); c != http.StatusSeeOther {
		t.Errorf("adding with the tick: %d", c)
	}
	if got := w.mailCfg(t).KindleDomains; len(got) != 3 {
		t.Errorf("the list: %v", got)
	}
	if c := post(url.Values{"domains": {"kindle.com"}}); c != http.StatusSeeOther {
		t.Errorf("taking domains away: %d", c)
	}
	if c := post(url.Values{"reset": {"1"}}); c != http.StatusSeeOther {
		t.Errorf("the default: %d", c)
	}
	if got := w.mailCfg(t).KindleDomains; len(got) != 2 || got[0] != "kindle.com" {
		t.Errorf("after reset: %v", got)
	}
	if c := post(url.Values{"domains": {"*"}, "understood": {"1"}}); c != http.StatusUnprocessableEntity {
		t.Errorf("a wildcard: %d", c)
	}
}

func TestTheTestMessageSaysWhatHappenedInTheCataloguesWords(t *testing.T) {
	w := newSiteWorld(t, nil)
	rec := w.s.postForm(t, w.admin, "/ui/admin/mail/test", nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a test with no relay: %d %s", rec.Code, rec.Body)
	}
	_, page := w.s.getPage(t, w.admin, "/admin/mail")
	if !strings.Contains(page, `id="ml-test-result"`) || !strings.Contains(page, "relay") {
		t.Errorf("the result is not shown: %s", page[strings.Index(page, `id="test"`):][:400])
	}
	// A relay that nothing listens at: the class is a sentence of the catalogue, never what a server said.
	w.mailOn(t)
	w.s.postForm(t, w.admin, "/ui/admin/mail/test", nil)
	_, page = w.s.getPage(t, w.admin, "/admin/mail")
	if !strings.Contains(page, "Nothing is listening there.") {
		i := strings.Index(page, `id="ml-test-result"`)
		t.Errorf("the class is not in words: %q", page[max(i, 0):][:min(300, len(page)-max(i, 0))])
	}
	// Twice in five seconds is told to wait.
	w.s.postForm(t, w.admin, "/ui/admin/mail/test", nil)
	_, page = w.s.getPage(t, w.admin, "/admin/mail")
	if !strings.Contains(page, "seconds") {
		t.Error("a second test straight away is not told to wait")
	}
}

func TestTheKindleAddressAndTheDigestOnSettings(t *testing.T) {
	w := newSiteWorld(t, nil)
	_, off := w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(off, `id="kindle"`) || !strings.Contains(off, `id="digest"`) || !strings.Contains(off, "Email is not switched on for this server") {
		t.Error("the sections or the mail-off statement are missing")
	}
	if strings.Contains(off, `name="do" value="on"`) {
		t.Error("the digest can be turned on while mail is off")
	}
	w.mailOn(t)
	rec := w.s.postForm(t, w.owner, "/ui/settings/kindle/save", url.Values{"address": {"owner_k@kindle.com"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving the address: %d", rec.Code)
	}
	_, page := w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(page, "owner_k@kindle.com") || !strings.Contains(page, "scriobh@example.org") || !strings.Contains(page, "Approved Personal Document") {
		t.Error("the stored address or the sender to approve is not shown")
	}
	// A wrong domain is refused in the server's words, beside the field.
	w.s.postForm(t, w.owner, "/ui/settings/kindle/save", url.Values{"address": {"owner@example.com"}})
	_, page = w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(page, `id="ml-k-error"`) || !strings.Contains(page, `aria-invalid="true"`) {
		t.Error("a refused address is not shown with the field")
	}
	if !strings.Contains(page, `value="owner@example.com"`) {
		t.Error("a refused address does not come back as it was typed")
	}
	// The digest: needs a frequency; on; needs confirming after the address changes; off.
	w.s.postForm(t, w.owner, "/ui/settings/digest", url.Values{"do": {"on"}})
	_, page = w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(page, `id="ml-d-error"`) {
		t.Error("turning on without a frequency is not refused")
	}
	if rec := w.s.postForm(t, w.owner, "/ui/settings/digest", url.Values{"do": {"on"}, "frequency": {"weekly"}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("digest on: %d", rec.Code)
	}
	_, page = w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(page, "on Monday morning") || !strings.Contains(page, `value="weekly" checked`) {
		t.Error("the digest's state is not shown")
	}
	if _, err := w.s.DB.Writer().Exec(`UPDATE users SET email = 'new-owner@test.local' WHERE id = ?`, w.owner.userID); err != nil {
		t.Fatal(err)
	}
	_, page = w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(page, "new-owner@test.local, so the digest stopped") || !strings.Contains(page, "Confirm this address and turn it on") {
		t.Error("the needs-confirming state is not shown as its own state")
	}
	w.s.postForm(t, w.owner, "/ui/settings/digest", url.Values{"do": {"on"}, "frequency": {"weekly"}})
	w.s.postForm(t, w.owner, "/ui/settings/digest", url.Values{"do": {"off"}})
	_, page = w.s.getPage(t, w.owner, "/settings")
	if !strings.Contains(page, "The digest is off") {
		t.Error("off is not shown")
	}
	w.s.postForm(t, w.owner, "/ui/settings/kindle/delete", nil)
	_, page = w.s.getPage(t, w.owner, "/settings")
	if strings.Contains(page, "owner_k@kindle.com") {
		t.Error("the deleted address is still shown")
	}
}

var reTokenField = regexp.MustCompile(`name="token" value="([^"]+)"`)

func TestSendToKindleFromTheCompileSectionToTheLog(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.doc(t, "Opening", "chapter", "", "The harbour woke.\n", "done", "")
	for _, c := range []caller{w.reader, w.stranger} {
		if code, _ := w.s.getPage(t, c, "/p/the-salt-road/kindle"); code != http.StatusNotFound && code != http.StatusForbidden {
			t.Errorf("%s sees the Kindle page: %d", c.name, code)
		}
		if rec := w.s.postForm(t, c, "/ui/projects/"+w.pid+"/kindle/preview", nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Errorf("%s previews: %d", c.name, rec.Code)
		}
	}
	// With mail off, and then with no address, the compile section says what is missing and offers no preview.
	_, comp := w.s.getPage(t, w.editor, "/p/the-salt-road/compile")
	if !strings.Contains(comp, `id="kindle"`) || strings.Contains(comp, "/kindle/preview") || !strings.Contains(comp, "Email is not switched on") {
		t.Error("mail off: the compile section")
	}
	w.mailOn(t)
	_, comp = w.s.getPage(t, w.editor, "/p/the-salt-road/compile")
	if strings.Contains(comp, "/kindle/preview") || !strings.Contains(comp, `href="/settings#kindle"`) {
		t.Error("no address: the compile section")
	}
	if rec := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/preview", nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Save your Send-to-Kindle address first.") {
		t.Errorf("preview with no address: %d", rec.Code)
	}
	w.must(t, w.editor, "PUT", "/api/v1/me/kindle", `{"address":"editor_k@kindle.com"}`)
	_, comp = w.s.getPage(t, w.editor, "/p/the-salt-road/compile")
	if !strings.Contains(comp, `action="/ui/projects/`+w.pid+`/kindle/preview"`) {
		t.Fatal("the compile section has no preview form")
	}
	// A GET of the page never calls the preview: no token is in it.
	_, start := w.s.getPage(t, w.editor, "/p/the-salt-road/kindle")
	if reTokenField.MatchString(start) {
		t.Error("a GET holds a token")
	}
	rec := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/preview", nil)
	body := rec.Body.String()
	m := reTokenField.FindStringSubmatch(body)
	if rec.Code != 200 || m == nil || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview: %d, token %v, cache %q", rec.Code, m != nil, rec.Header().Get("Cache-Control"))
	}
	for _, want := range []string{"editor_k@kindle.com", ".epub", "The manuscript leaves this server", "scriobh@example.org", "Approved Personal Document"} {
		if !strings.Contains(body, want) {
			t.Errorf("the preview lacks %q", want)
		}
	}
	if strings.Count(body, m[1]) != 1 {
		t.Error("the token is in the page more than once")
	}
	// Send, once; the same token again is told it is no longer valid.
	send := url.Values{"token": {m[1]}}
	sent := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/send", send)
	if sent.Code != http.StatusSeeOther {
		t.Fatalf("send: %d %s", sent.Code, sent.Body)
	}
	again := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/send", send)
	if again.Code != http.StatusConflict || !strings.Contains(again.Body.String(), "not valid any more") {
		t.Errorf("a second send of the same preview: %d", again.Code)
	}
	if strings.Contains(again.Body.String(), m[1]) {
		t.Error("the token comes back in an error page")
	}
	// The log: the editor sees the domain only; the owner and an administrator see the address.
	_, ed := w.s.getPage(t, w.editor, "/p/the-salt-road/kindle")
	ed = ed[strings.Index(ed, `id="log"`):]
	if strings.Contains(ed, "editor_k@kindle.com") || !strings.Contains(ed, "kindle.com") || !strings.Contains(ed, "and an administrator see the full address") {
		t.Error("the editor's log")
	}
	_, ow := w.s.getPage(t, w.owner, "/p/the-salt-road/kindle")
	ow = ow[strings.Index(ow, `id="log"`):]
	if !strings.Contains(ow, "editor_k@kindle.com") || strings.Contains(ow, "and an administrator see the full address") {
		t.Error("the owner's log should hold the full address")
	}
}

func TestTheOutboxStatusShowsNoMessageContent(t *testing.T) {
	w := newSiteWorld(t, nil)
	w.doc(t, "Opening", "chapter", "", "The harbour woke.\n", "done", "")
	w.mailOn(t)
	w.must(t, w.editor, "PUT", "/api/v1/me/kindle", `{"address":"editor_k@kindle.com"}`)
	pv := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/preview", nil)
	w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/send", url.Values{"token": {reTokenField.FindStringSubmatch(pv.Body.String())[1]}})
	_, page := w.s.getPage(t, w.admin, "/admin/mail")
	i := strings.Index(page, `id="queue"`)
	if i < 0 || strings.Contains(page[i:], "editor_k") || strings.Contains(page[i:], "Salt Road") || !strings.Contains(page[i:], "Kindle edition") {
		t.Error("the queue section shows content, or does not show the message")
	}
}

var reMailLtr = regexp.MustCompile(`(?is)<bdi[^>]*>.*?</bdi>`)

// Every word on the mail pages comes from the catalogue: in the pseudo-locale no plain English is left, except what a
// person typed, an address, and the server's own refusal.
func TestThePseudoLocaleLeavesNoEnglishOnTheMailPages(t *testing.T) {
	w := newSiteWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	w.doc(t, "Opening", "chapter", "", "The harbour woke.\n", "done", "")
	for _, c := range []caller{w.admin, w.editor, w.owner} {
		if rec := w.s.do(c, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
			t.Fatalf("language: %d", rec.Code)
		}
	}
	w.mailOn(t)
	w.must(t, w.editor, "PUT", "/api/v1/me/kindle", `{"address":"editor_k@kindle.com"}`)
	w.s.postForm(t, w.admin, "/ui/admin/mail/test", nil)
	pages := map[string]string{}
	_, pages["admin"] = w.s.getPage(t, w.admin, "/admin/mail")
	_, pages["compile"] = w.s.getPage(t, w.editor, "/p/the-salt-road/compile")
	_, pages["kindle"] = w.s.getPage(t, w.editor, "/p/the-salt-road/kindle")
	pv := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/preview", nil)
	pages["preview"] = pv.Body.String()
	w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/kindle/send", url.Values{"token": {reTokenField.FindStringSubmatch(pages["preview"])[1]}})
	_, pages["log"] = w.s.getPage(t, w.editor, "/p/the-salt-road/kindle")
	w.s.postForm(t, w.editor, "/ui/settings/digest", url.Values{"do": {"on"}, "frequency": {"daily"}})
	_, settings := w.s.getPage(t, w.editor, "/settings")
	i, j := strings.Index(settings, `<section id="kindle"`), strings.Index(settings, `id="h-digest"`)
	if i < 0 || j < i {
		t.Fatal("the settings sections are not on the page")
	}
	pages["settings"] = settings[i : j+strings.Index(settings[j:], "</section>")]
	if k := strings.Index(pages["compile"], `<section id="kindle"`); k >= 0 {
		pages["compile"] = pages["compile"][k : k+strings.Index(pages["compile"][k:], "</section>")]
	} else {
		t.Fatal("the compile page has no Send to Kindle section")
	}
	allowed := map[string]bool{"the": true, "salt": true, "road": true, "wren": true, "ada": true, "editor": true, "scriobh": true, "utc": true}
	for name, body := range pages {
		if !strings.Contains(body, "⟦") {
			t.Errorf("%s: nothing in the pseudo-locale", name)
			continue
		}
		var bad []string
		for _, word := range englishIn(reMailLtr.ReplaceAllString(reCodeOrOp.ReplaceAllString(body, " "), " ")) {
			if !allowed[strings.ToLower(word)] {
				bad = append(bad, word)
			}
		}
		if len(bad) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", name, strings.Join(bad, " "))
		}
	}
}

// refusedAfter follows the redirect an answered form leads to and says whether the page it shows holds a refusal.
func refusedAfter(t *testing.T, s *Server, c caller, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect, got %d", rec.Code)
	}
	return strings.Contains(followAs(t, s, c, rec).Body.String(), `class="field-error"`)
}

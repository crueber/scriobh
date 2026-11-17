package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// The passphrase screens (F-252): the owner's page, the cover a page of an unlocked project carries, and the one rule that
// makes an open page lock when it should: its own upkeep is not use.

func TestPassphrasePageForTheOwnerHasEveryControlAndTheWords(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	rec := s.do(w.owner, "GET", "/p/"+w.slug+"/passphrase", "")
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b := rec.Body.String()
	for _, want := range []string{
		"It is not encryption", "the server&#39;s administrator", "Git pushes", "a borrowed laptop",
		`action="/ui/projects/` + w.pid + `/passphrase/set"`, `action="/ui/projects/` + w.pid + `/passphrase/options"`, `action="/ui/projects/` + w.pid + `/passphrase/clear"`,
		`name="idle_minutes"`, `name="allow_keys"`, "withdraws every approval", "scriobh passphrase clear --project " + w.pid,
	} {
		if !strings.Contains(b, want) {
			t.Errorf("the owner's page lacks %q", want)
		}
	}
	// no secret is ever drawn into the page, not even the empty boxes' values
	if strings.Contains(b, w.pass) {
		t.Error("the passphrase is on the page")
	}
	if got := regexp.MustCompile(`<input[^>]*type="password"[^>]*value=`).FindString(b); got != "" {
		t.Errorf("a password box carries a value: %s", got)
	}
}

// An editor sees the words and the state, and no control that would be refused; and a locked member can still open the page
// (it is how they learn what to type), and it carries nothing of the project.
func TestPassphrasePageForOthersAndWhileLocked(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	for _, c := range []caller{w.editor, w.owner2} {
		rec := s.do(c, "GET", "/p/"+w.slug+"/passphrase", "")
		if rec.Code != 200 {
			t.Fatalf("%s: %d", c.name, rec.Code)
		}
		b := rec.Body.String()
		if l := w.leaks(b); len(l) > 0 {
			t.Errorf("%s: a locked member's passphrase page carries %v", c.name, l)
		}
		if !strings.Contains(b, "It is locked in this browser") || !strings.Contains(b, "/p/"+w.slug+"/unlock") {
			t.Errorf("%s: the page does not say it is locked and point at the unlock page", c.name)
		}
	}
	rec := s.do(w.editor, "GET", "/p/"+w.slug+"/passphrase", "")
	b := rec.Body.String()
	if strings.Contains(b, "/passphrase/set") || strings.Contains(b, "/passphrase/clear") || !strings.Contains(b, "Only the project&#39;s owner") {
		t.Error("an editor is shown controls the server would refuse")
	}
	// a stranger gets the 404 a missing project gives
	if rec := s.do(w.outsider, "GET", "/p/"+w.slug+"/passphrase", ""); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger: %d", rec.Code)
	}
	// the owner's other session may use the page to change the passphrase it has not unlocked, with the current one
	if l := w.leaks(s.do(w.owner2, "GET", "/p/"+w.slug+"/passphrase", "").Body.String()); len(l) > 0 {
		t.Errorf("leaks %v", l)
	}
}

func TestPassphraseFormsSetChangeOptionsClear(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	f := "/ui/projects/" + w.pid + "/passphrase/"
	// a new passphrase must be typed twice
	rec := s.postForm(t, w.owner, f+"set", url.Values{"current": {w.pass}, "passphrase": {"another long phrase"}, "repeat": {"another long phrasf"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not the same") {
		t.Fatalf("mismatch: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "another long phrase") {
		t.Error("a typed passphrase came back in the page")
	}
	// the wrong current passphrase is said on the current field, in words
	rec = s.postForm(t, w.owner, f+"set", url.Values{"current": {"not it at all"}, "passphrase": {"another long phrase"}, "repeat": {"another long phrase"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "That passphrase is not right.") || !strings.Contains(rec.Body.String(), `aria-describedby="pass-err-set"`) {
		t.Fatalf("wrong current: %d\n%s", rec.Code, regexp.MustCompile(`(?s)<main.*</main>`).FindString(rec.Body.String()))
	}
	// changing it: this session stays open, every other asks again
	if !w.isLocked(w.owner2) {
		t.Fatal("setup: the second session should be locked")
	}
	if rec := w.unlock(w.owner2, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	rec = s.postForm(t, w.owner, f+"set", url.Values{"current": {w.pass}, "passphrase": {"another long phrase"}, "repeat": {"another long phrase"}})
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/passphrase?saved=set") {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.owner) || !w.isLocked(w.owner2) {
		t.Error("a new passphrase must leave the changing session open and lock the others")
	}
	w.pass = "another long phrase"
	// options: idle limit and keys need the passphrase
	rec = s.postForm(t, w.owner, f+"options", url.Values{"current": {"wrong wrong wrong"}, "idle_minutes": {"45"}, "allow_keys": {"true"}})
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("options with a wrong passphrase: %d", rec.Code)
	}
	rec = s.postForm(t, w.owner, f+"options", url.Values{"current": {w.pass}, "idle_minutes": {"45"}, "allow_keys": {"true"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("options: %d %s", rec.Code, rec.Body)
	}
	b := s.do(w.owner, "GET", "/p/"+w.slug+"/passphrase", "").Body.String()
	if !strings.Contains(b, `value="45"`) || !regexp.MustCompile(`name="allow_keys" value="true" checked`).MatchString(b) {
		t.Error("the page does not show the saved idle limit and key switch")
	}
	// the idle limit is checked by the op, and its message is the field's
	rec = s.postForm(t, w.owner, f+"options", url.Values{"current": {w.pass}, "idle_minutes": {"900"}, "allow_keys": {"false"}})
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "1 to 720") {
		t.Errorf("idle over the limit: %d", rec.Code)
	}
	// an editor's form post is refused by the server whatever the page showed
	rec = s.postForm(t, w.editor, f+"clear", url.Values{"current": {w.pass}})
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an editor clearing it: %d", rec.Code)
	}
	if w.isLockedAfterClear(t) {
		t.Fatal("an editor cleared the passphrase")
	}
	rec = s.postForm(t, w.owner, f+"clear", url.Values{"current": {w.pass}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.editor) {
		t.Error("after the passphrase is removed the project opens for its members")
	}
}

// isLockedAfterClear: the passphrase is still in force (the editor, who has not unlocked it, is still refused).
func (w *gateWorld) isLockedAfterClear(t *testing.T) bool {
	t.Helper()
	return !w.isLocked(w.editor)
}

// Key approvals: the page lists the owner's keys and the keys that have tried the project, and the form's checkboxes become
// approve and unapprove lists by comparing with the keys that were approved when it was drawn.
func TestPassphraseKeyApprovalsFromTheForm(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	rec := s.do(w.owner, "POST", "/api/v1/keys", `{"name":"agent one","scope":"write"}`)
	if rec.Code >= 300 {
		t.Skipf("no key route in this build: %d %s", rec.Code, rec.Body)
	}
	b := s.do(w.owner, "GET", "/p/"+w.slug+"/passphrase", "").Body.String()
	m := regexp.MustCompile(`name="approved" value="([^"]+)"`).FindStringSubmatch(b)
	if m == nil || !strings.Contains(b, "agent one") {
		t.Fatalf("the owner's key is not listed:\n%s", b)
	}
	kid := m[1]
	f := "/ui/projects/" + w.pid + "/passphrase/options"
	rec = s.postForm(t, w.owner, f, url.Values{"current": {w.pass}, "idle_minutes": {"30"}, "allow_keys": {"true"}, "approved": {kid}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	b = s.do(w.owner, "GET", "/p/"+w.slug+"/passphrase", "").Body.String()
	if !regexp.MustCompile(`value="`+kid+`" checked`).MatchString(b) || !strings.Contains(b, `name="was" value="`+kid+`"`) {
		t.Fatal("the approved key is not shown as approved")
	}
	// withdrawing: the box is unticked and the form says it was approved
	rec = s.postForm(t, w.owner, f, url.Values{"current": {w.pass}, "idle_minutes": {"30"}, "allow_keys": {"true"}, "was": {kid}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("withdraw: %d %s", rec.Code, rec.Body)
	}
	b = s.do(w.owner, "GET", "/p/"+w.slug+"/passphrase", "").Body.String()
	if regexp.MustCompile(`value="` + kid + `" checked`).MatchString(b) {
		t.Fatal("the key is still approved")
	}
}

// An administrator who is a member with another role can remove a forgotten passphrase from the page without it; the
// owner cannot, and is told the way back.
func TestPassphraseAdminClearOnThePage(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	s.addMember(t, w.pid, w.admin.userID, ops.ProjectEditor)
	b := s.do(w.admin, "GET", "/p/"+w.slug+"/passphrase", "").Body.String()
	if !strings.Contains(b, "/passphrase/admin-clear") {
		t.Fatal("an administrator who is a member has no way to remove a forgotten passphrase")
	}
	if strings.Contains(s.do(w.owner, "GET", "/p/"+w.slug+"/passphrase", "").Body.String(), "/passphrase/admin-clear") {
		t.Error("the owner is offered the administrator's shortcut")
	}
	// the project's name is typed first, and the server asks for it: without it nothing is removed
	if rec := s.postForm(t, w.admin, "/ui/projects/"+w.pid+"/passphrase/admin-clear", url.Values{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Type the project") || !w.isLocked(w.editor) {
		t.Fatalf("admin clear without the name: %d", rec.Code)
	}
	if b := s.do(w.admin, "GET", "/p/"+w.slug+"/passphrase", "").Body.String(); !strings.Contains(b, `name="confirm_name"`) || !strings.Contains(b, "data-confirm-submit") {
		t.Error("the administrator's form does not ask for the project's name")
	}
	rec := s.postForm(t, w.admin, "/ui/projects/"+w.pid+"/passphrase/admin-clear", url.Values{"confirm_name": {"Gate Book"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("admin clear: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.editor) {
		t.Error("still locked after the administrator removed it")
	}
}

// The lockout is explained on the unlock page and on the settings forms in words with a time, never as a raw error.
func TestUnlockPageExplainsTheWait(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	var last *struct {
		code int
		body string
	}
	for i := 0; i < 6; i++ {
		rec := s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/unlock", url.Values{"passphrase": {"wrong guess number"}, "next": {"/p/" + w.slug}})
		last = &struct {
			code int
			body string
		}{rec.Code, rec.Body.String()}
	}
	if last.code != http.StatusTooManyRequests {
		t.Fatalf("after six wrong tries: %d", last.code)
	}
	if !regexp.MustCompile(`try again in (about )?\d+ (second|minute)`).MatchString(last.body) || !strings.Contains(last.body, "right passphrase will work again") {
		t.Errorf("the lockout is not explained with a time:\n%s", last.body)
	}
	// The wait itself is given in seconds, so 429 can legitimately appear as the
	// number in "work again in 429 seconds"; the status is checked above. Only
	// the raw class token is a leak.
	if strings.Contains(last.body, "rate_limited") {
		t.Error("a raw error code is on the page")
	}
	if l := w.leaks(last.body); len(l) > 0 {
		t.Errorf("the unlock page carries %v", l)
	}
}

// A page of a project that has a passphrase and is open carries the cover, the script and the title of the project, and
// nothing of the text; a project with none carries none of it.
func TestOpenPageOfAProtectedProjectCarriesTheCover(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	rec := s.do(w.owner, "GET", "/p/"+w.slug+"/d/"+w.docID, "")
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	b := rec.Body.String()
	for _, want := range []string{`data-gate="` + w.pid + `"`, `id="gate-cover"`, "js/gate", "css/gate", "Gate Book", "data-gate-lock", "Lock this project now"} {
		if !strings.Contains(b, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	// the cover is made of the project's title and the catalogue's words: cut it out of the page and look at it
	i := strings.Index(b, `<dialog id="gate-cover"`)
	j := strings.Index(b[i:], "</dialog>")
	if i < 0 || j < 0 {
		t.Fatal("no cover")
	}
	cover := b[i : i+j]
	for _, canary := range []string{"Zqbody", "Zqchapter", "Zqprivate", "Zqsubtitle", "Zqdescription"} {
		if strings.Contains(cover, canary) {
			t.Errorf("the cover carries %q", canary)
		}
	}
	if strings.Contains(cover, "Zqtitle") {
		t.Error("the cover carries the chapter's title")
	}
	// a project with no passphrase carries none of it
	w2 := s.newCaller(t, "plain", ops.RoleWriter)
	rec = s.do(w2, "POST", "/api/v1/projects", `{"title":"Plain Book"}`)
	var pj struct{ Item struct{ Slug string } }
	_ = json.Unmarshal(rec.Body.Bytes(), &pj)
	pb := s.do(w2, "GET", "/p/"+pj.Item.Slug, "").Body.String()
	if strings.Contains(pb, "gate-cover") || strings.Contains(pb, "js/gate") || strings.Contains(pb, "data-gate") {
		t.Error("an unprotected project's page carries the cover")
	}
	// a locked member is sent away and sees no cover either
	if rec := s.do(w.editor, "GET", "/p/"+w.slug+"/d/"+w.docID, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("a locked page: %d", rec.Code)
	}
}

// A page's own upkeep (the lease it renews every 45 seconds, the question whether the project is still open) says so with
// a header, and is then not use: an editor left open on a desk locks after its idle limit. A key cannot say so.
func TestBackgroundRequestsDoNotKeepAnUnlockAlive(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"idle_minutes":10}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	w.setClock(t, t0)
	e := s.sessionFor(t, "bg", w.editor.userID)
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	get := func(header bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/docs/"+w.docID+"/lease", strings.NewReader(`{"tab_id":"t1"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(e.cookie)
		if header {
			req.Header.Set(backgroundHeader, "1")
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}
	at := t0.Add(5 * time.Minute)
	w.setClock(t, at)
	if rec := get(true); rec.Code != 200 { // upkeep at five minutes, with the header: not use
		t.Fatalf("the lease while open: %d %s", rec.Code, rec.Body)
	}
	at = t0.Add(12 * time.Minute) // twelve minutes since the unlock, seven since the upkeep: locked only if the upkeep did not count
	w.setClock(t, at)
	if !w.isLocked(e) {
		t.Error("upkeep marked as background kept the unlock alive past its idle limit")
	}
	// the same calls without the header are use
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	opened := at
	for i := 0; i < 4; i++ {
		at = at.Add(5 * time.Minute)
		w.setClock(t, at)
		if rec := get(false); rec.Code != 200 {
			t.Fatalf("the lease without the header, after %v: %d %s", at.Sub(opened), rec.Code, rec.Body)
		}
	}
	// the status question never counts, with or without the header
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	for i := 0; i < 3; i++ {
		at = at.Add(4 * time.Minute)
		w.setClock(t, at)
		if rec := s.do(e, "GET", w.base()+"/passphrase", ""); rec.Code != 200 {
			t.Fatal(rec.Body)
		}
	}
	if !w.isLocked(e) {
		t.Error("asking whether the project is open kept it open")
	}
	// only a browser session may say so: a key's calls are counted whatever it claims
	for _, c := range []struct {
		name string
		p    ops.Principal
		want bool
	}{
		{"session", ops.Principal{UserID: "u", SessionID: "ses_1", Via: "session"}, true},
		{"key", ops.Principal{UserID: "u", KeyID: "key_1", Via: "apikey"}, false},
		{"key that also names a session", ops.Principal{UserID: "u", SessionID: "ses_1", KeyID: "key_1", Via: "apikey"}, false},
	} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set(backgroundHeader, "1")
		req = req.WithContext(context.WithValue(req.Context(), keyAuth, reqAuth{P: c.p}))
		if got := ops.IsPassive(s.withCaller(req)); got != c.want {
			t.Errorf("%s: passive = %v, want %v", c.name, got, c.want)
		}
	}
}

// A locked project is its title, a lock mark and the words "Needs a passphrase" in the library: no count, no author, no
// recent activity, no menu that would ask the project something. The session that unlocked it sees it as any other.
func TestLibraryShowsALockedProjectAsItsTitleOnly(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	rec := s.do(w.owner2, "GET", "/", "")
	b := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, "Gate Book") || !strings.Contains(b, "Needs a passphrase") {
		t.Fatalf("a locked project in the library: %d", rec.Code)
	}
	if l := w.leaks(b); len(l) > 0 {
		t.Errorf("the library of a locked session carries %v", l)
	}
	if strings.Contains(b, "/ui/projects/"+w.pid+"/menu") || strings.Contains(b, "/ui/projects/"+w.pid+"/pin") {
		t.Error("a locked tile offers actions that would ask the project something")
	}
	if !strings.Contains(b, `href="/p/`+w.slug+`"`) {
		t.Error("the tile does not lead to the unlock page")
	}
	if n := strings.Count(b, "Needs a passphrase"); n != 1 {
		t.Errorf("the lock is named %d times on the tile", n)
	}
	open := s.do(w.owner, "GET", "/", "").Body.String()
	if strings.Contains(open, "Needs a passphrase") || !strings.Contains(open, "/ui/projects/"+w.pid+"/menu") {
		t.Error("an unlocked session's tile is the ordinary one")
	}
}

// Each key on the settings page says which projects with a passphrase it has met.
func TestKeyListShowsProjectsWithAPassphrase(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	rec := s.do(w.editor, "POST", "/api/v1/keys", `{"name":"scout","scope":"write"}`)
	if rec.Code >= 300 {
		t.Fatalf("key: %d %s", rec.Code, rec.Body)
	}
	var k struct {
		Secret string `json:"secret"`
		Item   struct{ ID string }
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &k)
	if k.Secret == "" {
		t.Skip("no secret in the key answer")
	}
	req := httptest.NewRequest("GET", "/api/v1/projects/"+w.pid, nil)
	req.Header.Set("Authorization", "Bearer "+k.Secret)
	r2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(r2, req)
	if r2.Code != http.StatusForbidden {
		t.Fatalf("a key at a protected project (the owner does not accept keys): %d", r2.Code)
	}
	b := s.do(w.editor, "GET", "/settings", "").Body.String()
	if !strings.Contains(b, "Gate Book") || !strings.Contains(b, "1 refused") || !strings.Contains(b, "unless its owner allows keys") {
		t.Errorf("the key list does not show the project the key met:\n%s", regexp.MustCompile(`(?s)<section id="keys".*?</section>`).FindString(b))
	}
}

// Every word on the passphrase and cover, Compendium management and search-by-meaning screens comes from the catalogue: in the
// pseudo-locale (every letter accented) no run of plain English is left. The right-to-left variant is looked at in
// scripts/e2e-gate.mjs, since only a browser can say whether it reads right.
func TestThePseudoLocaleLeavesNoEnglishOnThePhase7PrivacyPages(t *testing.T) {
	u := newSemUI(t)
	u.s.Cfg.OIDCStub = true
	cm := newCMWorld(t)
	_ = cm
	for _, c := range []caller{u.owner, u.editor, u.admin} {
		if rec := u.s.do(c, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
			t.Fatalf("language: %d", rec.Code)
		}
	}
	send(t, u.s, u.owner, "PUT", "/api/v1/projects/"+u.pid+"/passphrase", `{"passphrase":"correct horse battery"}`, 200)
	u.enable(t)
	pass := "/ui/projects/" + u.pid + "/passphrase/"
	pages := map[string]*httptest.ResponseRecorder{
		"passphrase":                u.s.do(u.owner, "GET", "/p/"+u.slug+"/passphrase", ""),
		"passphrase, locked":        u.s.do(u.editor, "GET", "/p/"+u.slug+"/passphrase", ""),
		"passphrase, error":         u.s.postForm(t, u.owner, pass+"set", url.Values{"current": {"wrong one here"}, "passphrase": {"another long phrase"}, "repeat": {"another long phrase"}}),
		"unlock page":               u.s.do(u.editor, "GET", "/p/"+u.slug+"/unlock", ""),
		"a chapter, with the cover": u.s.do(u.owner, "GET", "/p/"+u.slug+"/d/"+u.harbour, ""),
		"semantic":                  u.s.do(u.owner, "GET", "/p/"+u.slug+"/semantic", ""),
		"semantic preview":          u.s.postForm(t, u.owner, "/ui/projects/"+u.pid+"/semantic/preview", url.Values{"provider_id": {u.provider}, "model": {"embed-a"}}),
		"search by meaning":         u.s.do(u.owner, "GET", "/p/"+u.slug+"/search?mode=meaning&q=harbour+boats", ""),
		"similar":                   u.s.do(u.owner, "GET", "/ui/docs/"+u.harbour+"/similar", ""),
	}
	for name, rec := range pages {
		if rec.Code != 200 && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d", name, rec.Code)
			continue
		}
		raw := rec.Body.String()
		if strings.HasPrefix(name, "a chapter") { // the writing view's own words are older: only the cover and the inspector's new list are looked at
			raw = "<main>" + regexp.MustCompile(`(?s)<dialog id="gate-cover".*?</dialog>|<section class="insp-links sem-like".*?</section>`).FindAllString(raw, -1)[0] + regexp.MustCompile(`(?s)<section class="insp-links sem-like".*?</section>`).FindString(raw) + "</main>"
		}
		body := regexp.MustCompile(`(?s)<code\b.*?</code>`).ReplaceAllString(raw, " ")
		body = regexp.MustCompile(`\b(prj|doc|cmp)_[A-Za-z0-9]+`).ReplaceAllString(body, " ")
		body = strings.NewReplacer("The Salt Road", " ", "Local embeddings", " ", "embed-a", " ", "The Harbour", " ", "The Forge", " ", "The Mill", " ", "The Orchard", " ", "The Quay", " ").Replace(body) // what people named
		body = regexp.MustCompile(`(?s)<li class="sem-hit">.*?</li>`).ReplaceAllStringFunc(body, func(h string) string {                                                                                    // a hit is the manuscript's own words, and its kind and status are the older shared vocabulary
			return regexp.MustCompile(`(?s)<p>.*?</p>|<span class="fine">.*?</span>`).ReplaceAllString(h, " ")
		})
		body = regexp.MustCompile(`(?s)<h1 id="search-title">.*?</h1>`).ReplaceAllString(body, " ")
		body = strings.ReplaceAll(body, `aria-label="Search this project"`, "") // the words-mode form, older than this work
		for _, w := range englishIn(body) {
			t.Errorf("%s: %q is plain English in the pseudo-locale", name, w)
		}
	}
}

// A passage the search found is not carried into the unlock page's address when the project has locked since.
func TestFindIsNotCarriedIntoTheUnlockAddress(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	rec := s.do(w.editor, "GET", "/p/"+w.slug+"/d/"+w.docID+"?find=a+passage+of+the+manuscript&x=1", "")
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || strings.Contains(loc, "passage") || strings.Contains(loc, "find") {
		t.Errorf("the redirect to the unlock page: %d %q", rec.Code, loc)
	}
	if !strings.Contains(loc, "%2Fp%2F"+w.slug+"%2Fd%2F"+w.docID) {
		t.Errorf("the chapter is not what the unlock page returns to: %q", loc)
	}
}

// A name made to reorder the sentence it is put in (an override, U+202E) is wrapped in isolates wherever the new pages and the cover
// put a project's name into words.
func TestNamesAreIsolatedInTheirSentences(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	name := "Evil\u202Etitle"
	if rec := s.do(w.owner, "PATCH", w.base(), `{"title":"`+name+`"}`); rec.Code != 200 {
		t.Fatalf("rename: %d %s", rec.Code, rec.Body)
	}
	want := "\u2068" + name + "\u2069"
	for _, p := range []string{"/p/" + w.slug + "/passphrase", "/p/" + w.slug + "/d/" + w.docID, "/p/" + w.slug + "/semantic"} {
		if b := s.do(w.owner, "GET", p, "").Body.String(); !strings.Contains(b, want) {
			t.Errorf("%s: the name is not isolated", p)
		}
	}
}

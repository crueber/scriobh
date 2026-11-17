package server

import (
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

var reRemoteParam = regexp.MustCompile(`remote=([^&#]+)`)

// makeRemote adds a remote through the page's own form and returns its id.
func (e writeEnv) makeRemote(t *testing.T, name, addr, audience string, extra url.Values) string {
	t.Helper()
	form := url.Values{"name": {name}, "url": {addr}, "audience": {audience}}
	for k, v := range extra {
		form[k] = v
	}
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/create", form, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body)
	}
	m := reRemoteParam.FindStringSubmatch(rec.Header().Get("Location"))
	if m == nil {
		t.Fatalf("create %s: no remote in %q", name, rec.Header().Get("Location"))
	}
	return m[1]
}

// What a remote carries is chosen by the person, with nothing chosen for them: a form that does not say is refused, and the
// choice made is what is stored (the old form silently gave a backup the public filter).
func TestAddingARemoteNeedsAnExplicitAudience(t *testing.T) {
	e := newWriteEnv(t)
	pid := "/ui/projects/" + e.pid + "/git/"
	page := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git", "").Body.String())
	if !strings.Contains(page, `name="audience" value="private"`) || strings.Contains(page, `value="private" checked`) || strings.Contains(page, `value="public" checked`) {
		t.Error("the add form does not offer both audiences with neither chosen")
	}
	if !strings.Contains(page, "Everything in the project except secrets") || !strings.Contains(page, "Only what a reader sees") {
		t.Error("the page does not say in words what each audience sends")
	}
	rec := e.s.post(t, e.owner, pid+"create", url.Values{"name": {"Backup"}, "url": {"ssh://git@forge.example.com/a/b.git"}, "include_notes": {"1"}}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(stdhtml.UnescapeString(followAs(t, e.s, e.owner, rec).Body.String()), "Nothing is chosen for you") {
		t.Fatalf("a form without an audience: %d", rec.Code)
	}
	priv := e.makeRemote(t, "Backup", "ssh://git@forge.example.com/a/b.git", "private", url.Values{"include_notes": {"1"}})
	pub := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", url.Values{"only_done": {"1"}})
	_ = priv
	body := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+pub, "").Body.String())
	for _, need := range []string{"Backup", "Mirror", "Only what a reader sees", "only documents that are done", "Everything except secrets"} {
		if !strings.Contains(body, need) {
			t.Errorf("the page lacks %q", need)
		}
	}
}

// Changing what a remote carries starts its history over, and for a remote that is or may be public the name is typed first.
func TestChangingWhatARemoteCarriesAsksFirst(t *testing.T) {
	e := newWriteEnv(t)
	pid := "/ui/projects/" + e.pid + "/git/"
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	post := func(v url.Values) (int, string) {
		v.Set("remote_id", id)
		rec := e.s.post(t, e.owner, pid+"audience", v, "", false)
		if strings.Contains(rec.Header().Get("Location"), "saved=") {
			return http.StatusSeeOther, ""
		}
		return 422, stdhtml.UnescapeString(followAs(t, e.s, e.owner, rec).Body.String()) // a refusal is shown by the page after the redirect
	}
	if code, body := post(url.Values{"audience": {"private"}}); code != 422 || !strings.Contains(body, "history will start over") {
		t.Errorf("without the box: %d", code)
	}
	if code, body := post(url.Values{"audience": {"private"}, "understood": {"1"}}); code != 422 || !strings.Contains(body, "Type the remote's name exactly") {
		t.Errorf("widening a public remote without its name: %d", code)
	}
	if code, body := post(url.Values{"audience": {"private"}, "understood": {"1"}, "confirm": {"mirror"}}); code != 422 {
		t.Errorf("a wrongly typed name: %d %s", code, body[:0])
	}
	if code, _ := post(url.Values{}); code != 422 {
		t.Errorf("no audience at all: %d", code)
	}
	if code, _ := post(url.Values{"audience": {"public"}}); code != 422 {
		t.Errorf("no change: %d", code)
	}
	if code, _ := post(url.Values{"audience": {"private"}, "understood": {"1"}, "confirm": {"Mirror"}}); code != http.StatusSeeOther {
		t.Fatalf("widening with the name: %d", code)
	}
	if body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String(); !strings.Contains(body, "Everything in the project except secrets") {
		t.Error("the page does not show the new audience")
	}
	// a private remote that was never public is narrowed with the box alone
	if code, _ := post(url.Values{"audience": {"public"}, "understood": {"1"}}); code != http.StatusSeeOther {
		t.Errorf("narrowing a private remote: %d", code)
	}
}

// A project has up to five remotes; the page lists them, selects one by address, and stops offering a sixth.
func TestSeveralRemotesAreListedAndTheFifthIsTheLast(t *testing.T) {
	e := newWriteEnv(t)
	var ids []string
	for i, n := range []string{"One", "Two", "Three", "Four", "Five"} {
		aud := []string{"private", "public"}[i%2]
		ids = append(ids, e.makeRemote(t, n, "ssh://git@forge.example.com/a/"+strings.ToLower(n)+".git", aud, nil))
	}
	page := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+ids[3], "").Body.String()
	for _, n := range []string{"One", "Two", "Three", "Four", "Five"} {
		if !strings.Contains(page, "remote-"+ids[indexOf([]string{"One", "Two", "Three", "Four", "Five"}, n)]) {
			t.Errorf("remote %s is not in the list", n)
		}
	}
	if !strings.Contains(page, `id="gs-name" type="text" name="name" value="Four"`) {
		t.Error("the remote named in the address is not the one shown")
	}
	if strings.Contains(page, `class="form git-form" method="post" action="/ui/projects/`+e.pid+`/git/create"`) {
		t.Error("a sixth remote is offered")
	}
	if !strings.Contains(stdhtml.UnescapeString(page), "up to 5 remotes") {
		t.Error("the limit is not said")
	}
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/create", url.Values{"name": {"Six"}, "url": {"ssh://git@forge.example.com/a/six.git"}, "audience": {"private"}}, "", false)
	if strings.Contains(rec.Header().Get("Location"), "saved=created") {
		t.Error("a sixth remote was made")
	}
}

func indexOf(a []string, s string) int {
	for i, x := range a {
		if x == s {
			return i
		}
	}
	return -1
}

// The signing key and the trigger: the owner makes them here; a trigger's secret is in the answer that made it and never again.
func TestSigningAndTriggerPartsOfThePage(t *testing.T) {
	e := newWriteEnv(t)
	pid := "/ui/projects/" + e.pid + "/git/"
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	body := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String())
	for _, need := range []string{"Make the project's signing key", "Address to call", "Make the trigger"} {
		if !strings.Contains(body, need) {
			t.Errorf("the page lacks %q", need)
		}
	}
	rec := e.s.post(t, e.owner, pid+"signing-generate", url.Values{"remote_id": {id}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("signing: %d %s", rec.Code, rec.Body)
	}
	after := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String())
	if !strings.Contains(after, "ssh-ed25519 ") || !strings.Contains(after, "Allowed-signers line") || strings.Contains(after, "PRIVATE KEY") {
		t.Error("the public signing key is not shown, or a private key is")
	}
	if rec := e.s.post(t, e.owner, pid+"signing-remote", url.Values{"remote_id": {id}, "sign_commits": {"1"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("sign on: %d", rec.Code)
	}
	// the trigger
	set := e.s.post(t, e.owner, pid+"trigger-set", url.Values{"remote_id": {id}, "trigger_url": {"https://hooks.example.org/build/SECRET-PATH-123"}}, "", false)
	if set.Code != http.StatusSeeOther || strings.Contains(set.Header().Get("Location"), "whsec") {
		t.Fatalf("trigger: %d %s", set.Code, set.Header().Get("Location"))
	}
	once := set.Header().Get("Location")
	shown := followAs(t, e.s, e.owner, set)
	secret := regexp.MustCompile(`<code id="git-secret" class="wrap">([^<]+)</code>`).FindStringSubmatch(shown.Body.String())
	if secret == nil || len(secret[1]) < 16 {
		t.Fatal("the secret is not shown by the page the redirect leads to")
	}
	if strings.Contains(shown.Body.String(), "SECRET-PATH-123") || strings.Contains(once, secret[1]) {
		t.Error("the sealed path is in the page, or the secret is in the address")
	}
	// reloading that address shows that the secret was shown, and makes no new one
	loc, _, _ := strings.Cut(once, "#")
	reload := e.s.do(e.owner, "GET", loc, "").Body.String()
	if strings.Contains(reload, secret[1]) || !strings.Contains(stdhtml.UnescapeString(reload), "already shown") {
		t.Error("a reload shows the secret again, or does not say it was shown")
	}
	for _, p := range []string{"/p/" + e.slug + "/git?remote=" + id, "/p/" + e.slug + "/git"} {
		again := e.s.do(e.owner, "GET", p, "").Body.String()
		if strings.Contains(again, secret[1]) || strings.Contains(again, "SECRET-PATH-123") {
			t.Errorf("the secret or the path came back on %s", p)
		}
	}
	if b := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String(); !strings.Contains(b, "hooks.example.org") || !strings.Contains(b, "…") {
		t.Error("the page shows neither the host nor the secret's start")
	}
	// a test delivery is a result on the page, whether it reached the receiver or not (here nothing can be reached)
	test := e.s.post(t, e.owner, pid+"trigger-test", url.Values{"remote_id": {id}}, "", false)
	if test.Code != http.StatusSeeOther || !strings.Contains(followAs(t, e.s, e.owner, test).Body.String(), `id="git-trigger-result"`) {
		t.Errorf("a test delivery: %d", test.Code)
	}
	if rec := e.s.post(t, e.owner, pid+"trigger-delete", url.Values{"remote_id": {id}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("delete the trigger: %d", rec.Code)
	}
}

// Every new owner action is the owner's: an editor is refused and a stranger finds nothing.
func TestTheNewGitActionsAreTheOwners(t *testing.T) {
	e := newWriteEnv(t)
	editor := e.s.newCaller(t, "editor", ops.RoleWriter)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	e.s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	pid := "/ui/projects/" + e.pid + "/git/"
	for _, a := range []string{"audience", "signing-generate", "signing-remote", "trigger-set", "trigger-rotate", "trigger-switch", "trigger-test", "trigger-delete"} {
		v := url.Values{"remote_id": {id}, "audience": {"private"}, "understood": {"1"}, "confirm": {"Mirror"}, "trigger_url": {"https://hooks.example.org/x"}}
		if rec := e.s.post(t, editor, pid+a, v, "", false); rec.Code != 403 {
			t.Errorf("an editor ran %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, stranger, pid+a, v, "", false); rec.Code != 404 {
			t.Errorf("a stranger ran %s: %d", a, rec.Code)
		}
	}
	// and the editor's page offers none of them
	body := e.s.do(editor, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String()
	for _, gone := range []string{"git/audience", "git/signing-generate", "git/trigger-set", "git/trigger-test", "Make the trigger"} {
		if strings.Contains(body, gone) {
			t.Errorf("the editor is offered %q", gone)
		}
	}
}

// A remote paused by a restore says so and has one way back: the switch-on button, and nothing that pushes.
func TestARestorePausedRemoteHasOneSwitchOn(t *testing.T) {
	e := newWriteEnv(t)
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	if _, err := e.s.DB.Writer().Exec(`UPDATE git_remotes SET enabled = 0, state = 'blocked', last_error_code = 'paused_by_restore', restore_paused = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	body := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String())
	if !strings.Contains(body, "Paused by a restore") || !strings.Contains(body, "Switch on") {
		t.Error("the pause is not said, or has no switch-on")
	}
	if strings.Contains(body, ">Push now</button>") || strings.Contains(body, "Switch pushing on") {
		t.Error("a paused remote offers another way to push or to switch on")
	}
	if rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/enable", url.Values{"remote_id": {id}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("switch on: %d", rec.Code)
	}
	if body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String(); strings.Contains(body, "Paused by a restore") {
		t.Error("the pause is still said after it was lifted")
	}
}

// The palette's command pushes every remote that is on and not paused, with several remotes (the op alone would refuse to guess).
func TestThePalettePushNamesNoRemoteAndNeedsOneThatIsOn(t *testing.T) {
	e := newWriteEnv(t)
	a := e.makeRemote(t, "One", "ssh://git@forge.example.com/a/one.git", "private", nil)
	e.makeRemote(t, "Two", "ssh://git@forge.example.com/a/two.git", "public", nil)
	_ = a
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/push", url.Values{}, "", true)
	if rec.Code >= 500 || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("the palette push with two remotes that are not ready: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// A refused form gives its values back to itself and to no other: with a remote shown, a refused "add another remote" must not
// put its name, branch or folder into the form that saves the selected remote.
func TestARefusedFormFillsOnlyItself(t *testing.T) {
	e := newWriteEnv(t)
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/create", url.Values{"name": {"TYPED-NEW-NAME"}, "url": {"http://bad.example.com/x.git"}, "audience": {"private"}, "branch": {"typed-branch"}, "path_prefix": {"typed/folder"}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("a bad address: %d", rec.Code)
	}
	body := followAs(t, e.s, e.owner, rec).Body.String()
	settings := body[strings.Index(body, `id="settings"`):strings.Index(body, `id="carries"`)]
	for _, bad := range []string{"TYPED-NEW-NAME", "typed-branch", "typed/folder"} {
		if strings.Contains(settings, bad) {
			t.Errorf("the settings form of the selected remote took %q from the refused add form", bad)
		}
	}
	add := body[strings.Index(body, `id="add"`):]
	if !strings.Contains(add, "TYPED-NEW-NAME") || !strings.Contains(add, "typed-branch") {
		t.Error("the add form did not get its own values back")
	}
	// and the other way round: a refused save fills the settings form only
	rec = e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/settings", url.Values{"remote_id": {id}, "name": {"TYPED-RENAME"}, "branch": {"bad branch name"}, "push_mode": {"manual"}}, "", false)
	if rec.Code == http.StatusSeeOther && !strings.Contains(rec.Header().Get("Location"), "saved=") {
		b := followAs(t, e.s, e.owner, rec).Body.String()
		add := b[strings.Index(b, `id="add"`):]
		if strings.Contains(add, "TYPED-RENAME") {
			t.Error("the add form took the refused save's name")
		}
	}
}

// followAs follows the redirect an answered form leads to and returns the page it shows. A refusal, a scan, a test and a
// secret are all shown by the page after a redirect, so the address in the browser can be reloaded.
func followAs(t *testing.T, s *Server, c caller, rec *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect, got %d %s", rec.Code, rec.Body)
	}
	loc, _, _ := strings.Cut(rec.Header().Get("Location"), "#") // a fragment is the browser's, not the request's
	return s.do(c, "GET", loc, "")
}

package server

import (
	stdhtml "html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

func (e writeEnv) coverOn(t *testing.T, id string) bool {
	t.Helper()
	var n int
	if err := e.s.DB.Read().QueryRow(`SELECT include_cover FROM git_remotes WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// The Git page's cover control: a public remote has the cover off; turning it on is the owner's, needs the remote's name
// typed and is audited; turning it off needs nothing; a backup has it on and may turn it off.
func TestTheGitPageControlsWhetherARemoteCarriesTheCover(t *testing.T) {
	e := newWriteEnv(t)
	pub := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	priv := e.makeRemote(t, "Backup", "ssh://git@forge.example.com/a/b.git", "private", nil)
	if e.coverOn(t, pub) || !e.coverOn(t, priv) {
		t.Fatalf("defaults: public %v, private %v", e.coverOn(t, pub), e.coverOn(t, priv))
	}
	page := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+pub, "").Body.String())
	for _, need := range []string{`id="carries-cover"`, `action="/ui/projects/` + e.pid + `/git/cover"`, `name="include_cover"`, "No: the cover stays out of the repository"} {
		if !strings.Contains(page, need) {
			t.Errorf("the page of a public remote lacks %q", need)
		}
	}
	if page := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+priv, "").Body.String()); !strings.Contains(page, "Yes: the picture and its layout are pushed") {
		t.Error("the page of a backup does not say it carries the cover")
	}
	cover := "/ui/projects/" + e.pid + "/git/cover"
	// without the name: refused in words, nothing changed
	rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {pub}, "include_cover": {"1"}}, "", false)
	if rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no name: %d", rec.Code)
	}
	if e.coverOn(t, pub) {
		t.Fatal("the cover was turned on without the name")
	}
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {pub}, "include_cover": {"1"}, "confirm": {"mirror"}}, "", false); e.coverOn(t, pub) {
		t.Errorf("the cover was turned on with the wrong name (%d)", rec.Code)
	}
	// the owner with the name
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {pub}, "include_cover": {"1"}, "confirm": {"Mirror"}}, "", false); rec.Code != http.StatusSeeOther || !e.coverOn(t, pub) {
		t.Fatalf("with the name: %d, on %v", rec.Code, e.coverOn(t, pub))
	}
	var audited int
	_ = e.s.DB.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'git.remote.update' AND detail_json LIKE '%"include_cover":true%'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("%d audit entries for the cover turned on", audited)
	}
	if page := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+pub, "").Body.String()); !strings.Contains(page, "Yes: the picture and its layout are pushed") {
		t.Error("the page does not say the cover is carried now")
	}
	// the same again is refused in words; turning it off needs no name
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {pub}, "include_cover": {"1"}, "confirm": {"Mirror"}}, "", false); !strings.Contains(stdhtml.UnescapeString(followAs(t, e.s, e.owner, rec).Body.String()), "That is what the remote does with the cover now") {
		t.Error("saying what is already so is not answered")
	}
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {pub}}, "", false); rec.Code != http.StatusSeeOther || e.coverOn(t, pub) {
		t.Fatalf("turning it off: %d, on %v", rec.Code, e.coverOn(t, pub))
	}
	// a backup turns it off and on again with no name
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {priv}}, "", false); rec.Code != http.StatusSeeOther || e.coverOn(t, priv) {
		t.Fatalf("a backup turning it off: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {priv}, "include_cover": {"1"}}, "", false); rec.Code != http.StatusSeeOther || !e.coverOn(t, priv) {
		t.Fatalf("a backup turning it on: %d", rec.Code)
	}
	// a public remote added with the cover ticked needs the name too
	rec = e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/create", url.Values{"name": {"Third"}, "url": {"ssh://git@forge.example.com/a/d.git"}, "audience": {"public"}, "include_cover": {"1"}}, "", false)
	var n int
	_ = e.s.DB.Read().QueryRow(`SELECT count(*) FROM git_remotes WHERE name = 'Third'`).Scan(&n)
	if n != 0 {
		t.Errorf("a public remote was added with the cover on and no name (%d)", rec.Code)
	}
	if rec := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/git/create", url.Values{"name": {"Third"}, "url": {"ssh://git@forge.example.com/a/d.git"}, "audience": {"public"}, "include_cover": {"1"}, "confirm": {"Third"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("with the name: %d", rec.Code)
	}
	// the audience form does not carry the cover box: it is not that form's business
	body := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+pub, "").Body.String()
	a := strings.Index(body, `/git/audience"`)
	if a < 0 {
		t.Fatal("no audience form")
	}
	if form := body[a : a+strings.Index(body[a:], "</form>")]; strings.Contains(form, "include_cover") {
		t.Error("the cover box is on the audience form too")
	}
}

func TestTheCoverActionIsTheOwners(t *testing.T) {
	e := newWriteEnv(t)
	editor := e.s.newCaller(t, "editor", ops.RoleWriter)
	stranger := e.s.newCaller(t, "stranger", ops.RoleWriter)
	e.s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	id := e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/c.git", "public", nil)
	v := url.Values{"remote_id": {id}, "include_cover": {"1"}, "confirm": {"Mirror"}}
	cover := "/ui/projects/" + e.pid + "/git/cover"
	if rec := e.s.post(t, editor, cover, v, "", false); rec.Code != 403 {
		t.Errorf("an editor: %d", rec.Code)
	}
	if rec := e.s.post(t, stranger, cover, v, "", false); rec.Code != 404 {
		t.Errorf("a stranger: %d", rec.Code)
	}
	if e.coverOn(t, id) {
		t.Error("a refused person changed the setting")
	}
	if body := e.s.do(editor, "GET", "/p/"+e.slug+"/git?remote="+id, "").Body.String(); strings.Contains(body, "git/cover") {
		t.Error("the editor is offered the cover form")
	}
}

// A private remote at an address that was ever public is exposed: the op wants the name typed to widen it, so the page's forms
// (the cover's and the audience's) show the name field, and the page refuses without it before the op is asked.
func TestThePagesAskForTheNameWhereverTheServerWould(t *testing.T) {
	e := newWriteEnv(t)
	e.makeRemote(t, "Mirror", "ssh://git@forge.example.com/a/p.git", "public", nil) // the address is marked public for good
	priv := e.makeRemote(t, "Backup", "ssh://git@forge.example.com/a/p.git", "private", url.Values{"confirm": {"Backup"}})
	page := stdhtml.UnescapeString(e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+priv, "").Body.String())
	for _, id := range []string{`id="gc-confirm"`, `id="ga-confirm"`} {
		if !strings.Contains(page, id) {
			t.Errorf("the page of a private remote at an address that was public has no name field %s", id)
		}
	}
	var exposed int
	_ = e.s.DB.Read().QueryRow(`SELECT count(*) FROM git_remotes WHERE id = ? AND include_cover = 1`, priv).Scan(&exposed)
	if exposed != 1 {
		t.Fatal("a backup has the cover on")
	}
	// turn it off (no name), then on again: the name is wanted
	cover := "/ui/projects/" + e.pid + "/git/cover"
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {priv}}, "", false); rec.Code != http.StatusSeeOther || e.coverOn(t, priv) {
		t.Fatalf("off: %d", rec.Code)
	}
	if e.s.post(t, e.owner, cover, url.Values{"remote_id": {priv}, "include_cover": {"1"}}, "", false); e.coverOn(t, priv) {
		t.Error("the cover was turned on for an exposed remote with no name")
	}
	if rec := e.s.post(t, e.owner, cover, url.Values{"remote_id": {priv}, "include_cover": {"1"}, "confirm": {"Backup"}}, "", false); rec.Code != http.StatusSeeOther || !e.coverOn(t, priv) {
		t.Errorf("with the name: %d", rec.Code)
	}
	// an ordinary private remote has no such field
	plain := e.makeRemote(t, "Plain", "ssh://git@forge.example.com/a/q.git", "private", nil)
	if page := e.s.do(e.owner, "GET", "/p/"+e.slug+"/git?remote="+plain, "").Body.String(); strings.Contains(page, `id="gc-confirm"`) {
		t.Error("an ordinary backup is asked for a name")
	}
}

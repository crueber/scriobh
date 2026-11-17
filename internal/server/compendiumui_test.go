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

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

type compEnv struct {
	s                                                      *Server
	owner, editor, reader, stranger, projOwner, admin, ins caller
	cid, slug                                              string
}

// newCompEnv: a Compendium owned by owner, with an editor and a reader on it, a project owner who is not on it, a
// stranger, an administrator and an instance reader.
func newCompEnv(t *testing.T) compEnv {
	t.Helper()
	s := newAuthzServer(t)
	e := compEnv{s: s, owner: s.newCaller(t, "owner", ops.RoleWriter), editor: s.newCaller(t, "ceditor", ops.RoleWriter), reader: s.newCaller(t, "creader", ops.RoleWriter),
		stranger: s.newCaller(t, "stranger", ops.RoleWriter), projOwner: s.newCaller(t, "projowner", ops.RoleWriter), admin: s.newCaller(t, "admin", ops.RoleAdmin), ins: s.newCaller(t, "inst", ops.RoleReader)}
	c := send(t, s, e.owner, "POST", "/api/v1/compendiums", `{"name":"The Salt Road Bible"}`, 201)["item"].(map[string]any)
	e.cid, e.slug = c["id"].(string), c["slug"].(string)
	send(t, s, e.owner, "POST", "/api/v1/compendiums/"+e.cid+"/members", `{"email":"ceditor@test.local","role":"editor"}`, 201)
	send(t, s, e.owner, "POST", "/api/v1/compendiums/"+e.cid+"/members", `{"email":"creader@test.local","role":"reader"}`, 201)
	return e
}

func (e compEnv) project(t *testing.T, c caller, title string) projects.Project {
	t.Helper()
	out, err := e.s.Reg.Call(context.Background(), ops.Principal{UserID: c.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: title, Kind: "novel"})
	if err != nil {
		t.Fatal(err)
	}
	return out.(projects.Item).Item
}

func alertText(body string) string {
	m := regexp.MustCompile(`(?s)role="alert"[^>]*>(.*?)</p>`).FindStringSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func TestCompendiumPagesAreGuarded(t *testing.T) {
	e := newCompEnv(t)
	page := "/c/" + e.slug
	if rec := e.s.do(caller{name: "anon"}, "GET", page, ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := e.s.do(caller{name: "anon"}, "GET", "/compendiums", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous list: %d", rec.Code)
	}
	// A Compendium the person is not on is exactly as missing as one that is not there.
	missing := e.s.do(e.stranger, "GET", "/c/no-such-bible", "")
	stranger := e.s.do(e.stranger, "GET", page, "")
	if stranger.Code != 404 || missing.Code != 404 || stranger.Body.String() != missing.Body.String() {
		t.Errorf("a stranger gets %d and a missing one %d, and the bodies differ: %v", stranger.Code, missing.Code, stranger.Body.String() != missing.Body.String())
	}
	if rec := e.s.do(e.projOwner, "GET", page, ""); rec.Code != 404 {
		t.Errorf("a project owner who is not on the Compendium: %d", rec.Code)
	}
	send(t, e.s, e.owner, "POST", "/api/v1/compendiums", `{"name":"Tides"}`, 201)
	owner := e.s.do(e.owner, "GET", page, "").Body.String()
	for _, need := range []string{"You own this Compendium", "give-someone", "add-member", "ref-add", "/update", "Move to the trash", "/c/" + e.slug + "/entities", "/c/" + e.slug + "/export", "The Salt Road Bible"} {
		if need == "give-someone" {
			need = "Give someone access"
		}
		if !strings.Contains(owner, need) {
			t.Errorf("the owner's page lacks %q", need)
		}
	}
	if !strings.Contains(owner, "ceditor@test.local") || !strings.Contains(owner, "creader@test.local") {
		t.Error("the owner does not see the members")
	}
	editor := e.s.do(e.editor, "GET", page, "").Body.String()
	for _, need := range []string{"read everything in this Compendium and add or change", "Only the owner sees who has access", "/export"} {
		if !strings.Contains(editor, need) {
			t.Errorf("the editor's page lacks %q", need)
		}
	}
	reader := e.s.do(e.reader, "GET", page, "").Body.String()
	for _, gone := range []string{"/member-add", "/update", "/trash", "/archive", "/ref-add", "/export", "ceditor@test.local"} {
		if strings.Contains(editor, "/member-add") || strings.Contains(editor, "/trash\"") {
			t.Errorf("the editor is offered an owner's control")
			break
		}
		if strings.Contains(reader, gone) {
			t.Errorf("the reader's page has %q", gone)
		}
	}
	if !strings.Contains(reader, "You cannot change anything in it") {
		t.Error("the reader is not told what they can do")
	}
	// The library list shows it to its members and to no one else.
	if list := e.s.do(e.owner, "GET", "/compendiums", "").Body.String(); !strings.Contains(list, "The Salt Road Bible") || !strings.Contains(list, "You own it") {
		t.Error("the owner's list lacks the Compendium")
	}
	if list := e.s.do(e.reader, "GET", "/compendiums", "").Body.String(); !strings.Contains(list, "You can read") || strings.Contains(list, "Trash\" ") {
		t.Error("the reader's list")
	}
	if list := e.s.do(e.stranger, "GET", "/compendiums", "").Body.String(); strings.Contains(list, "The Salt Road Bible") {
		t.Error("a stranger's list shows it")
	}
}

func TestCompendiumActionsAreGuarded(t *testing.T) {
	e := newCompEnv(t)
	base := "/ui/compendiums/" + e.cid + "/"
	for _, a := range []string{"archive", "unarchive", "trash", "restore", "update", "member-add", "member-role", "member-remove", "ref-add", "ref-remove", "purge"} {
		f := url.Values{"name": {"X"}, "kind": {"fiction"}, "email": {"x@test.local"}, "role": {"reader"}, "user_id": {"usr_x"}, "target_id": {"cmp_x"}, "confirm_name": {"X"}}
		if rec := e.s.post(t, caller{name: "anon"}, base+a, f, "", false); rec.Code != http.StatusSeeOther && rec.Code != 401 {
			t.Errorf("anonymous %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, e.owner, base+a, f, "-", false); rec.Code != 403 {
			t.Errorf("owner without a token, %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, e.stranger, base+a, f, "", false); rec.Code != 404 {
			t.Errorf("stranger %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, e.projOwner, base+a, f, "", false); rec.Code != 404 {
			t.Errorf("a project owner who is not on it, %s: %d", a, rec.Code)
		}
		for who, c := range map[string]caller{"editor": e.editor, "reader": e.reader} {
			if rec := e.s.post(t, c, base+a, f, "", false); rec.Code != 403 {
				t.Errorf("%s %s: %d", who, a, rec.Code)
			}
		}
	}
	if rec := e.s.do(e.reader, "GET", "/ui/compendiums/"+e.cid+"/purge", ""); rec.Code != 403 {
		t.Errorf("a reader opened the delete form: %d", rec.Code)
	}
	if rec := e.s.do(e.stranger, "GET", "/ui/compendiums/"+e.cid+"/purge", ""); rec.Code != 404 {
		t.Errorf("a stranger opened the delete form: %d", rec.Code)
	}
}

func TestCompendiumStatesAndTypedDelete(t *testing.T) {
	e := newCompEnv(t)
	base := "/ui/compendiums/" + e.cid + "/"
	post := func(a string, f url.Values) *httptest.ResponseRecorder {
		return e.s.post(t, e.owner, base+a, f, "", false)
	}
	if rec := post("archive", nil); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=archived") {
		t.Fatalf("archive: %d %v", rec.Code, rec.Header())
	}
	if l := e.s.do(e.owner, "GET", "/compendiums", "").Body.String(); strings.Contains(l, "The Salt Road Bible") {
		t.Error("an archived Compendium is in the active list")
	}
	if l := e.s.do(e.owner, "GET", "/compendiums?state=archived", "").Body.String(); !strings.Contains(l, "Bring back") {
		t.Error("the archived list has no way back")
	}
	post("unarchive", nil)
	// A project uses it as its primary, and a second only references it: the delete form names both.
	proj := e.project(t, e.owner, "The Salt Road")
	other := e.project(t, e.owner, "Hedgerow")
	send(t, e.s, e.owner, "PUT", "/api/v1/projects/"+proj.ID+"/compendium", `{"compendium_id":"`+e.cid+`"}`, 200)
	send(t, e.s, e.owner, "POST", "/api/v1/projects/"+other.ID+"/compendium/refs", `{"compendium_id":"`+e.cid+`"}`, 200)
	// Not yet in the trash: the op refuses a delete, and the page says so beside the box.
	form := e.s.do(e.owner, "GET", "/ui/compendiums/"+e.cid+"/purge", "").Body.String()
	for _, need := range []string{"The Salt Road", "Hedgerow", "primary Compendium", "referenced", "data-confirm=\"The Salt Road Bible\""} {
		if !strings.Contains(form, need) {
			t.Errorf("the delete form lacks %q", need)
		}
	}
	if rec := post("purge", url.Values{"confirm_name": {"The Salt Road Bible"}}); rec.Code != 422 || alertText(rec.Body.String()) == "" {
		t.Errorf("a delete of a Compendium that is not in the trash: %d", rec.Code)
	}
	post("trash", nil)
	if rec := post("purge", url.Values{"confirm_name": {"the salt road bible"}}); rec.Code != 422 {
		t.Errorf("a wrongly typed name: %d", rec.Code)
	}
	if rec := post("purge", url.Values{}); rec.Code != 422 {
		t.Errorf("no name: %d", rec.Code)
	}
	if rec := post("purge", url.Values{"confirm_name": {"The Salt Road Bible"}}); rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=purged") {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := e.s.do(e.owner, "GET", "/c/"+e.slug, ""); rec.Code != 404 {
		t.Errorf("the page of a deleted Compendium: %d", rec.Code)
	}
}

func TestMemberAddGivesOneAnswerForEveryRefusal(t *testing.T) {
	e := newCompEnv(t)
	base := "/ui/compendiums/" + e.cid + "/member-add"
	// Unknown, an instance reader asked to be an editor, and a person who cannot be found at all: one sentence.
	answer := func(email, role string) (int, string) {
		rec := e.s.post(t, e.owner, base, url.Values{"email": {email}, "role": {role}}, "", false)
		return rec.Code, alertText(rec.Body.String())
	}
	c1, a1 := answer("nobody@test.local", "reader")
	c2, a2 := answer("inst@test.local", "editor")
	c3, a3 := answer("NOBODY@TEST.LOCAL", "editor")
	if c1 != 422 || c2 != 422 || c3 != 422 || a1 == "" || a1 != a2 || a2 != a3 {
		t.Errorf("answers differ: %d %q / %d %q / %d %q", c1, a1, c2, a2, c3, a3)
	}
	if strings.Contains(a1, "nobody@") || strings.Contains(a1, "instance") || strings.Contains(a1, "reader") {
		t.Errorf("the answer says why: %q", a1)
	}
	// A real person is added, case-insensitively; adding them again is refused in the op's own words.
	rec := e.s.post(t, e.owner, base, url.Values{"email": {"STRANGER@test.local"}, "role": {"reader"}}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=member-add") {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	if body := e.s.do(e.stranger, "GET", "/c/"+e.slug, "").Body.String(); !strings.Contains(body, "You can read everything") {
		t.Error("the new reader cannot open it")
	}
	again := e.s.post(t, e.owner, base, url.Values{"email": {"stranger@test.local"}, "role": {"reader"}}, "", false)
	if again.Code != 422 || alertText(again.Body.String()) == "" {
		t.Errorf("adding twice: %d", again.Code)
	}
	// Role change and removal.
	uid := e.stranger.userID
	if rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/member-role", url.Values{"user_id": {uid}, "role": {"editor"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("role: %d %s", rec.Code, rec.Body)
	}
	if rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/member-remove", url.Values{"user_id": {uid}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("remove: %d", rec.Code)
	}
	if rec := e.s.do(e.stranger, "GET", "/c/"+e.slug, ""); rec.Code != 404 {
		t.Errorf("a removed member still opens it: %d", rec.Code)
	}
}

func TestCompendiumReferencesAndTheOneLevelRule(t *testing.T) {
	e := newCompEnv(t)
	b := send(t, e.s, e.owner, "POST", "/api/v1/compendiums", `{"name":"Tides"}`, 201)["item"].(map[string]any)["id"].(string)
	rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/ref-add", url.Values{"target_id": {b}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("ref-add: %d %s", rec.Code, rec.Body)
	}
	page := e.s.do(e.owner, "GET", "/c/"+e.slug, "").Body.String()
	if !strings.Contains(page, "Tides") || !strings.Contains(page, "one level only") || !strings.Contains(page, "Stop referencing Tides") {
		t.Error("the reference or the one-level sentence is missing")
	}
	// A Compendium cannot reference itself, and the op's words are shown.
	if rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/ref-add", url.Values{"target_id": {e.cid}}, "", false); rec.Code != 422 {
		t.Errorf("a self reference: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/ref-remove", url.Values{"target_id": {b}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("ref-remove: %d", rec.Code)
	}
}

func TestCreateACompendiumFromTheLibrary(t *testing.T) {
	e := newCompEnv(t)
	if rec := e.s.post(t, e.owner, "/ui/compendiums", url.Values{"name": {" "}, "kind": {"fiction"}}, "", false); rec.Code != 422 || alertText(rec.Body.String()) == "" {
		t.Errorf("a blank name: %d", rec.Code)
	}
	rec := e.s.post(t, e.owner, "/ui/compendiums", url.Values{"name": {"Standalone World"}, "kind": {"nonfiction"}, "description": {"A world."}}, "", false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/standalone-world?saved=created" {
		t.Fatalf("create: %d %v", rec.Code, rec.Header())
	}
	// The dialog form lists only projects the person owns that have no primary yet.
	proj := e.project(t, e.owner, "Needs a bible")
	e.project(t, e.stranger, "Not mine")
	form := e.s.do(e.owner, "GET", "/ui/compendiums/new", "").Body.String()
	if !strings.Contains(form, "Needs a bible") || strings.Contains(form, "Not mine") {
		t.Error("the project choices are wrong")
	}
	made := e.s.post(t, e.owner, "/ui/compendiums", url.Values{"name": {"Bible Of Mine"}, "project_id": {proj.ID}, "starter": {"1"}}, "", false)
	if made.Code != http.StatusSeeOther {
		t.Fatalf("create for a project: %d %s", made.Code, made.Body)
	}
	if after := e.s.do(e.owner, "GET", "/ui/compendiums/new", "").Body.String(); strings.Contains(after, "Needs a bible") {
		t.Error("a project that now has a primary is still offered")
	}
}

func TestProjectCompendiumPageAndPeople(t *testing.T) {
	e := newCompEnv(t)
	proj := e.project(t, e.projOwner, "The Salt Road")
	pe := e.s.newCaller(t, "pedit", ops.RoleWriter)
	pr := e.s.newCaller(t, "pread", ops.RoleWriter)
	e.s.addMember(t, proj.ID, pe.userID, ops.ProjectEditor)
	e.s.addMember(t, proj.ID, pr.userID, ops.ProjectReader)
	page, people := "/p/"+proj.Slug+"/compendium", "/p/"+proj.Slug+"/people"
	pid := "/ui/projects/" + proj.ID + "/"
	for _, p := range []string{page, people} {
		if rec := e.s.do(caller{name: "anon"}, "GET", p, ""); rec.Code != http.StatusSeeOther {
			t.Errorf("anonymous %s: %d", p, rec.Code)
		}
		if rec := e.s.do(e.stranger, "GET", p, ""); rec.Code != 404 {
			t.Errorf("stranger %s: %d", p, rec.Code)
		}
	}
	// With no primary: the owner is offered the form to make one, and nobody else.
	own := e.s.do(e.projOwner, "GET", page, "").Body.String()
	if !strings.Contains(own, "no primary Compendium yet") || !strings.Contains(own, "Make a new Compendium for this project") {
		t.Error("the project owner is not offered a Compendium")
	}
	if !strings.Contains(own, "not the same") && !strings.Contains(own, "does not put anyone on the Compendium") {
		t.Error("the page does not say that project access is not Compendium access")
	}
	if ed := e.s.do(pe, "GET", page, "").Body.String(); strings.Contains(ed, "/compendium/preview") || strings.Contains(ed, "compendium/create") || !strings.Contains(ed, "Only the project's owner can choose") {
		t.Error("an editor is offered the owner's form")
	}
	// A preview of a Compendium the project owner is not an editor of is not found; one they can edit works.
	if rec := e.s.post(t, e.projOwner, pid+"compendium/preview", url.Values{"compendium_id": {e.cid}}, "", false); rec.Code != 404 && rec.Code != 403 {
		t.Errorf("a preview of a Compendium they are not on: %d", rec.Code)
	}
	mine := send(t, e.s, e.projOwner, "POST", "/api/v1/compendiums", `{"name":"Mine"}`, 201)["item"].(map[string]any)["id"].(string)
	pv := e.s.post(t, e.projOwner, pid+"compendium/preview", url.Values{"compendium_id": {mine}}, "", false)
	if pv.Code != 200 || !strings.Contains(pv.Body.String(), "Make &ldquo;Mine&rdquo; the primary Compendium?") || !strings.Contains(pv.Body.String(), "go dormant") {
		t.Fatalf("preview: %d %s", pv.Code, alertText(pv.Body.String()))
	}
	if got := e.s.do(e.projOwner, "GET", page, "").Body.String(); strings.Contains(got, "Take the primary away") {
		t.Error("a preview changed something")
	}
	// the change carries the preview's fingerprint: without it nothing is changed and a fresh preview is drawn
	if rec := e.s.post(t, e.projOwner, pid+"compendium/set", url.Values{"compendium_id": {mine}}, "", false); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Something changed since the preview") {
		t.Fatalf("set without a fingerprint: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	fp := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(pv.Body.String())
	if fp == nil {
		t.Fatal("the preview carries no fingerprint")
	}
	if rec := e.s.post(t, e.projOwner, pid+"compendium/set", url.Values{"compendium_id": {mine}, "fingerprint": {fp[1]}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("set: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	if got := e.s.do(e.projOwner, "GET", page, "").Body.String(); !strings.Contains(got, "Mine") || !strings.Contains(got, "Take the primary away") || !strings.Contains(got, `href="/c/mine"`) {
		t.Error("the primary is not shown or does not link to its page")
	}
	// A project reader sees the page with a count of what they cannot see, and no name of it.
	rd := e.s.do(pr, "GET", page, "").Body.String()
	if strings.Contains(rd, "Mine") || !strings.Contains(rd, "you are not a member") && !strings.Contains(rd, "that you are not a member of") {
		t.Errorf("the reader's page: names it or does not say it is hidden")
	}
	for _, a := range []string{"set", "clear", "create", "ref-add", "ref-remove", "preview"} {
		if rec := e.s.post(t, pe, pid+"compendium/"+a, url.Values{"compendium_id": {mine}}, "", false); rec.Code != 403 {
			t.Errorf("an editor ran %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, e.stranger, pid+"compendium/"+a, url.Values{"compendium_id": {mine}}, "", false); rec.Code != 404 {
			t.Errorf("a stranger ran %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, e.projOwner, pid+"compendium/"+a, url.Values{"compendium_id": {mine}}, "-", false); rec.Code != 403 {
			t.Errorf("no token, %s: %d", a, rec.Code)
		}
	}

	// People: the owner sees everyone and the Compendium role; an editor sees nothing of the list.
	if body := e.s.do(e.projOwner, "GET", people, "").Body.String(); !strings.Contains(body, "pedit@test.local") || !strings.Contains(body, "Ask by role") {
		t.Error("the owner's people page")
	}
	if body := e.s.do(pe, "GET", people, "").Body.String(); strings.Contains(body, "pread@test.local") || strings.Contains(body, "/people/add") {
		t.Error("an editor sees the member list or the form")
	}
	add := func(email, role, comp string) *httptest.ResponseRecorder {
		return e.s.post(t, e.projOwner, pid+"people/add", url.Values{"email": {email}, "role": {role}, "compendium": {comp}}, "", false)
	}
	// Unknown and not-allowed addresses give one answer.
	_, a1 := 0, alertText(add("nobody@test.local", "reader", "default").Body.String())
	a2 := alertText(add("inst@test.local", "editor", "default").Body.String())
	if a1 == "" || a1 != a2 {
		t.Errorf("answers differ: %q %q", a1, a2)
	}
	// The project owner owns "Mine", so an editor is added to it by default; the page says what was granted.
	rec := add("stranger@test.local", "editor", "default")
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.Contains(loc, "saved=added") || strings.ContainsAny(loc, "&") {
		t.Fatalf("add editor: %d %s %s (the address must carry no words of the result)", rec.Code, loc, rec.Body)
	}
	if body := e.s.do(e.projOwner, "GET", loc, "").Body.String(); !strings.Contains(body, "also added to the Compendium as an editor") {
		t.Error("the result does not say what was granted")
	}
	// It is said once: the server's flash is spent, and the address cannot be made to print words.
	if body := e.s.do(e.projOwner, "GET", loc, "").Body.String(); strings.Contains(body, "also added to the Compendium") {
		t.Error("the line came back on a reload")
	}
	if body := e.s.do(e.projOwner, "GET", people+"?saved=added&n=%3Cb%3EInjected+words&g=granted&cr=editor", "").Body.String(); strings.Contains(body, "Injected") || strings.Contains(body, "also added to the Compendium") {
		t.Error("the people page prints text taken from its address")
	}
	// A reader with the default is not added to the Compendium, and the page says why.
	other := e.s.newCaller(t, "other", ops.RoleWriter)
	rec = add("other@test.local", "reader", "default")
	if loc = rec.Header().Get("Location"); !strings.Contains(loc, "saved=added") {
		t.Fatalf("add reader: %d %s", rec.Code, loc)
	}
	if body := e.s.do(e.projOwner, "GET", loc, "").Body.String(); !strings.Contains(body, "not added to the Compendium, because that was not asked for") {
		t.Error("the reason is missing")
	}
	if rec := e.s.do(other, "GET", "/c/mine", ""); rec.Code != 404 {
		t.Errorf("a reader who was not asked to join the Compendium opens it: %d", rec.Code)
	}
	// "yes" for a reader gives reader access; an explicit request that the project owner cannot grant is refused whole.
	third := e.s.newCaller(t, "third", ops.RoleWriter)
	rec = add("third@test.local", "reader", "yes")
	if loc = rec.Header().Get("Location"); !strings.Contains(loc, "saved=added") {
		t.Fatalf("add reader with yes: %d %s", rec.Code, loc)
	}
	if body := e.s.do(e.projOwner, "GET", loc, "").Body.String(); !strings.Contains(body, "also added to the Compendium as a reader") {
		t.Error("the result does not say a reader was granted")
	}
	_ = third
	// Role change and removal; removal says which Compendium role remains.
	if rec := e.s.post(t, e.projOwner, pid+"people/role", url.Values{"user_id": {pe.userID}, "role": {"reviewer"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("role: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	rm := e.s.post(t, e.projOwner, pid+"people/remove", url.Values{"user_id": {e.stranger.userID}}, "", false)
	if loc = rm.Header().Get("Location"); rm.Code != http.StatusSeeOther || !strings.Contains(loc, "saved=out") {
		t.Errorf("remove: %d %s", rm.Code, loc)
	}
	if body := e.s.do(e.projOwner, "GET", loc, "").Body.String(); !strings.Contains(body, "never takes them off the Compendium") {
		t.Error("the removal does not say that the Compendium role stays")
	}
	for _, a := range []string{"add", "role", "remove"} {
		if rec := e.s.post(t, pe, pid+"people/"+a, url.Values{"email": {"x@test.local"}, "role": {"reader"}, "user_id": {pr.userID}}, "", false); rec.Code != 403 {
			t.Errorf("an editor ran %s: %d", a, rec.Code)
		}
		if rec := e.s.post(t, e.stranger, pid+"people/"+a, url.Values{}, "", false); rec.Code != 404 {
			t.Errorf("a stranger ran %s: %d", a, rec.Code)
		}
	}
}

// The people page asks for a Compendium grant the project owner cannot give: nothing is written and the op's own
// refusal is shown beside the form.
func TestExplicitCompendiumGrantByANonOwnerIsRefusedWhole(t *testing.T) {
	e := newCompEnv(t)
	proj := e.project(t, e.editor, "Borrowed Bible")
	// The project's owner is only an editor of the Compendium; they can make it primary but not share it.
	send(t, e.s, e.editor, "PUT", "/api/v1/projects/"+proj.ID+"/compendium", `{"compendium_id":"`+e.cid+`"}`, 200)
	pid := "/ui/projects/" + proj.ID + "/people/add"
	e.s.newCaller(t, "joiner", ops.RoleWriter)
	rec := e.s.post(t, e.editor, pid, url.Values{"email": {"joiner@test.local"}, "role": {"editor"}, "compendium": {"yes"}}, "", false)
	if rec.Code != 422 || alertText(rec.Body.String()) == "" {
		t.Fatalf("an explicit grant by an editor of the Compendium: %d %s", rec.Code, rec.Body)
	}
	if body := e.s.do(e.editor, "GET", "/p/"+proj.Slug+"/people", "").Body.String(); strings.Contains(body, "joiner@test.local") {
		t.Error("the person was added although the grant was refused")
	}
	// The default: added to the project alone, and told so.
	rec = e.s.post(t, e.editor, pid, url.Values{"email": {"joiner@test.local"}, "role": {"editor"}, "compendium": {"default"}}, "", false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "saved=added") {
		t.Errorf("the default: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if body := e.s.do(e.editor, "GET", rec.Header().Get("Location"), "").Body.String(); !strings.Contains(html.UnescapeString(body), "Only the Compendium's owner can give access") {
		t.Error("the default does not say the person was left off the Compendium")
	}
}

// Instance administrators act as owners in the registry (the API), but on the pages an administrator who is not a member
// is a stranger: the slug pages and the /ui id routes answer the same 404. Pinned here so a change is deliberate.
func TestAdministratorsAndTheCompendiumRoutes(t *testing.T) {
	e := newCompEnv(t)
	proj := e.project(t, e.projOwner, "Someone Else's Book")
	if rec := e.s.do(e.admin, "GET", "/p/"+proj.Slug+"/compendium", ""); rec.Code != 404 {
		t.Errorf("a non-member administrator opens the slug page: %d", rec.Code)
	}
	// The Compendium list an administrator gets holds only the Compendiums they are on, so its slug page is a 404 too;
	// the /ui action routes go through the op, which treats an administrator as an owner.
	if rec := e.s.do(e.admin, "GET", "/c/"+e.slug, ""); rec.Code != 404 {
		t.Errorf("a non-member administrator and a Compendium slug page: %d", rec.Code)
	}
	if rec := e.s.post(t, e.admin, "/ui/compendiums/"+e.cid+"/archive", nil, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("an administrator who is not a member archiving through the /ui route: %d, want 404", rec.Code)
	}
	if rec := e.s.post(t, e.admin, "/ui/projects/"+proj.ID+"/compendium/preview", url.Values{"compendium_id": {e.cid}}, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("an administrator who is not a member of the project, through the /ui route: %d, want 404", rec.Code)
	}
	// The ops keep their rule: through the API an administrator acts as the owner.
	if rec := e.s.do(e.admin, "GET", "/api/v1/compendiums/"+e.cid, ""); rec.Code != http.StatusOK {
		t.Errorf("an administrator reading the Compendium through the API: %d", rec.Code)
	}
}

// The dialogs as htmx fetches them: a fragment to swap in, errors answered inside it, and success as an HX-Redirect.
func TestCompendiumDialogsAsHtmxSendsThem(t *testing.T) {
	e := newCompEnv(t)
	get := func(c caller, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("HX-Request", "true")
		req.AddCookie(c.cookie)
		rec := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(rec, req)
		return rec
	}
	rec := get(e.owner, "/ui/compendiums/new")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="compendium-new-form"`) || strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("new dialog: %d %.200s", rec.Code, rec.Body)
	}
	bad := e.s.postHX(t, e.owner, "/ui/compendiums", url.Values{"name": {" "}, "kind": {"fiction"}})
	if bad.Code != 422 || !strings.Contains(bad.Body.String(), `id="compendium-new-form"`) || !strings.Contains(bad.Body.String(), "role=\"alert\"") {
		t.Errorf("a blank name in the dialog: %d %.300s", bad.Code, bad.Body)
	}
	good := e.s.postHX(t, e.owner, "/ui/compendiums", url.Values{"name": {"Dialog Made"}, "kind": {"fiction"}})
	if good.Header().Get("HX-Redirect") != "/c/dialog-made?saved=created" {
		t.Errorf("create from the dialog: %d %v", good.Code, good.Header())
	}
	made := send(t, e.s, e.owner, "GET", "/api/v1/compendiums", "", 200)["items"].([]any)
	var id string
	for _, it := range made {
		if m := it.(map[string]any); m["name"] == "Dialog Made" {
			id = m["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("the Compendium was not made")
	}
	send(t, e.s, e.owner, "POST", "/api/v1/compendiums/"+id+"/trash", "", 200)
	pd := get(e.owner, "/ui/compendiums/"+id+"/purge")
	if pd.Code != 200 || !strings.Contains(pd.Body.String(), `data-confirm="Dialog Made"`) || !strings.Contains(pd.Body.String(), `data-confirm-submit`) {
		t.Fatalf("purge dialog: %d %.300s", pd.Code, pd.Body)
	}
	wrong := e.s.postHX(t, e.owner, "/ui/compendiums/"+id+"/purge", url.Values{"confirm_name": {"dialog made"}})
	if wrong.Code != 422 || !strings.Contains(wrong.Body.String(), `id="compendium-purge-form"`) || !strings.Contains(wrong.Body.String(), `value="dialog made"`) {
		t.Errorf("a wrong name in the purge dialog: %d %.300s", wrong.Code, wrong.Body)
	}
	right := e.s.postHX(t, e.owner, "/ui/compendiums/"+id+"/purge", url.Values{"confirm_name": {"Dialog Made"}})
	if !strings.Contains(right.Header().Get("HX-Redirect"), "saved=purged") {
		t.Errorf("the purge: %d %v", right.Code, right.Header())
	}
}

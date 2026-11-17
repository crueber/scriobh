package server

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The entity bible's pages and the routes behind their buttons, called as every kind of person.

type entEnv struct {
	s                                                   *Server
	owner, editor, reader, projectOnly, stranger, admin caller
	cid, slug, pid, pslug                               string
	maire, quay                                         string
}

func newEntEnv(t *testing.T) entEnv {
	t.Helper()
	s := newAuthzServer(t)
	e := entEnv{s: s}
	e.admin = s.newCaller(t, "admin", ops.RoleAdmin)
	e.owner = s.newCaller(t, "owner", ops.RoleWriter)
	e.editor = s.newCaller(t, "editor", ops.RoleWriter)
	e.reader = s.newCaller(t, "reader", ops.RoleReader)
	e.projectOnly = s.newCaller(t, "project-only", ops.RoleWriter)
	e.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	ctx := context.Background()
	op := ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(ctx, op, "compendium.create", compendiums.CreateIn{Name: "Salt Bible"})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(compendiums.Item).Item
	e.cid, e.slug = c.ID, c.Slug
	s.addCompendiumMember(t, e.cid, e.editor.userID, ops.ProjectEditor)
	s.addCompendiumMember(t, e.cid, e.reader.userID, ops.ProjectReader)
	pout, err := s.Reg.Call(ctx, op, "project.create", projects.CreateIn{Title: "The Salt Road"})
	if err != nil {
		t.Fatal(err)
	}
	e.pid, e.pslug = pout.(projects.Item).Item.ID, pout.(projects.Item).Item.Slug
	if _, err := s.Reg.Call(ctx, op, "project.compendium.set", compendiums.SetIn{ID: e.pid, CompendiumID: e.cid}); err != nil {
		t.Fatal(err)
	}
	s.addMember(t, e.pid, e.projectOnly.userID, ops.ProjectEditor)
	e.maire = e.entity(t, "character", "Máire Nolan", "A ferrywoman.")
	e.quay = e.entity(t, "place", "Salt Quay", "Where the road meets the water.")
	return e
}

func (e entEnv) call(t *testing.T, op string, in any) any {
	t.Helper()
	out, err := e.s.Reg.Call(context.Background(), ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}, op, in)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e entEnv) entity(t *testing.T, kind, name, summary string) string {
	t.Helper()
	return e.call(t, "entity.create", entities.CreateIn{ID: e.cid, Kind: kind, Name: name, Summary: summary}).(entities.Item).Item.ID
}

func (e entEnv) get(c caller, path string) (int, string) {
	rec := e.s.do(c, "GET", path, "")
	return rec.Code, rec.Body.String()
}

func TestEntityPagesAreForMembers(t *testing.T) {
	e := newEntEnv(t)
	pages := []string{"/c/" + e.slug + "/entities", "/c/" + e.slug + "/e/" + e.maire, "/c/" + e.slug + "/e/" + e.maire + "/merge", "/c/" + e.slug + "/fields"}
	for _, p := range pages {
		if code, _ := e.get(caller{name: "anon"}, p); code != http.StatusSeeOther && code != http.StatusUnauthorized {
			t.Errorf("anonymous %s: %d", p, code)
		}
		for _, c := range []caller{e.stranger, e.projectOnly} {
			if code, body := e.get(c, p); code != http.StatusNotFound || strings.Contains(body, "Máire") || strings.Contains(body, "Salt Bible") {
				t.Errorf("%s at %s: %d (a Compendium is a 404 to a person not on it)", c.name, p, code)
			}
		}
	}
	for _, c := range []caller{e.owner, e.editor, e.reader} {
		for _, p := range pages[:2] {
			code, body := e.get(c, p)
			if code != http.StatusOK {
				t.Errorf("%s %s: %d", c.name, p, code)
				continue
			}
			editable := c.name != "reader"
			if has := strings.Contains(body, `action="/ui/entities/`+e.maire+`/save"`); p == pages[1] && has != editable {
				t.Errorf("%s: save form present = %v, want %v", c.name, has, editable)
			}
			if has := strings.Contains(body, `id="add"`); p == pages[0] && has != editable {
				t.Errorf("%s: add form present = %v, want %v", c.name, has, editable)
			}
		}
	}
	// A reader can read the merge page's address but not use it.
	if code, _ := e.get(e.reader, pages[2]); code != http.StatusForbidden {
		t.Errorf("reader at the merge page: %d", code)
	}
	if code, _ := e.get(e.editor, pages[2]); code != http.StatusOK {
		t.Errorf("editor at the merge page: %d", code)
	}
	if code, body := e.get(e.reader, pages[3]); code != http.StatusOK || strings.Contains(body, "/fields/set") {
		t.Errorf("reader at fields: %d", code)
	}
	// An entity of another Compendium, or an id that is not one, is as missing here.
	if code, _ := e.get(e.owner, "/c/"+e.slug+"/e/ent_nothere00000"); code != http.StatusNotFound {
		t.Errorf("a missing entity: %d", code)
	}
	if code, _ := e.get(e.owner, "/c/"+e.slug+"/e/not-an-id"); code != http.StatusNotFound {
		t.Errorf("a malformed id: %d", code)
	}
}

func TestEntityWriteRoutesCheckWhoAndWhy(t *testing.T) {
	e := newEntEnv(t)
	save := url.Values{"name": {"Máire Nolan"}, "summary": {"changed"}}
	cases := []struct {
		name, path string
		form       url.Values
	}{
		{"create", "/ui/compendiums/" + e.cid + "/entities", url.Values{"name": {"New One"}, "kind": {"item"}}},
		{"save", "/ui/entities/" + e.maire + "/save", save},
		{"alias", "/ui/entities/" + e.maire + "/alias", url.Values{"alias": {"Mam"}}},
		{"trash", "/ui/entities/" + e.quay + "/trash", nil},
		{"field", "/ui/compendiums/" + e.cid + "/fields/set", url.Values{"kind": {"character"}, "key": {"age"}, "label": {"Age"}, "type": {"number"}}},
		{"merge", "/ui/entities/" + e.quay + "/merge", url.Values{"into": {e.maire}}},
	}
	for _, c := range cases {
		if rec := e.s.post(t, caller{name: "anon"}, c.path, c.form, "-", false); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther {
			t.Errorf("%s anonymous: %d", c.name, rec.Code)
		}
		if rec := e.s.post(t, e.owner, c.path, c.form, "-", false); rec.Code != http.StatusForbidden {
			t.Errorf("%s with no CSRF token: %d", c.name, rec.Code)
		}
		if rec := e.s.post(t, e.owner, c.path, c.form, "wrong", false); rec.Code != http.StatusForbidden {
			t.Errorf("%s with the wrong CSRF token: %d", c.name, rec.Code)
		}
		for _, p := range []caller{e.stranger, e.projectOnly} {
			if rec := e.s.post(t, p, c.path, c.form, "", false); rec.Code != http.StatusNotFound {
				t.Errorf("%s as %s: %d", c.name, p.name, rec.Code)
			}
		}
		if rec := e.s.post(t, e.reader, c.path, c.form, "", false); rec.Code != http.StatusForbidden {
			t.Errorf("%s as a reader: %d", c.name, rec.Code)
		}
	}
	// Nothing above changed anything.
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Summary != "A ferrywoman." || len(got.Aliases) != 0 {
		t.Errorf("a refused request changed the entity: %+v", got)
	}
	// An editor can; an instance admin who is not a member is a stranger on the pages.
	if rec := e.s.post(t, e.editor, "/ui/entities/"+e.maire+"/save", save, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("an editor saving: %d", rec.Code)
	}
	if rec := e.s.post(t, e.admin, "/ui/entities/"+e.maire+"/alias", url.Values{"alias": {"Mam"}}, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("an admin who is not a member adding an alias: %d, want 404", rec.Code)
	}
	// Posting an entity of another Compendium into this one's route leaks nothing and writes nothing.
	other := e.call(t, "compendium.create", compendiums.CreateIn{Name: "Other"}).(compendiums.Item).Item
	oe := e.call(t, "entity.create", entities.CreateIn{ID: other.ID, Kind: "character", Name: "Elsewhere"}).(entities.Item).Item.ID
	e.s.addCompendiumMember(t, other.ID, e.editor.userID, ops.ProjectReader)
	if rec := e.s.post(t, e.editor, "/ui/entities/"+oe+"/save", save, "", false); rec.Code != http.StatusForbidden {
		t.Errorf("an editor of one Compendium saving in another where they only read: %d", rec.Code)
	}
	if code, _ := e.get(e.editor, "/c/"+e.slug+"/e/"+oe); code != http.StatusNotFound {
		t.Errorf("another Compendium's entity under this one's address: %d", code)
	}
}

func TestEntityRefusalsAreExplained(t *testing.T) {
	e := newEntEnv(t)
	base := "/ui/entities/" + e.maire
	// An alias another entity answers to names that entity, and offers the merge.
	e.call(t, "entity.alias.add", entities.AliasAddIn{ID: e.quay, Alias: "The Quay"})
	rec := e.s.post(t, e.owner, base+"/alias", url.Values{"alias": {"the quay"}, "notes": {"kept"}}, "", false)
	body := rec.Body.String()
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(body, "Salt Quay") || !strings.Contains(body, "/c/"+e.slug+"/e/"+e.quay) ||
		!strings.Contains(body, "/merge?into="+e.quay) || !strings.Contains(body, `value="the quay"`) {
		t.Errorf("alias collision: %d\n%s", rec.Code, between(body, `id="ent-error"`, "</div>"))
	}
	// A name in use, when adding an entity, stays in the box.
	rec = e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/entities", url.Values{"name": {"  salt  quay "}, "kind": {"place"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Salt Quay") && !strings.Contains(rec.Body.String(), "already") {
		t.Errorf("name collision: %d", rec.Code)
	}
	// A stale revision keeps the person's text and offers both ways out.
	cur := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Summary: ptr("Changed by someone else.")})
	rec = e.s.post(t, e.owner, base+"/save", url.Values{"name": {"Máire Nolan"}, "summary": {"My careful summary"}, "body": {"My notes"}, "base_revision": {fmt.Sprint(cur.Revision)}}, "", false)
	body = rec.Body.String()
	for _, want := range []string{"My careful summary", "My notes", "Keep mine", "Use theirs", "Changed by someone else."} {
		if !strings.Contains(body, want) {
			t.Errorf("stale save: missing %q (%d)", want, rec.Code)
		}
	}
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("stale save status: %d", rec.Code)
	}
	if now := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; now.Summary != "Changed by someone else." {
		t.Errorf("a stale save went through: %q", now.Summary)
	}
	// Keep mine is a plain resubmit with the new base revision.
	i := strings.Index(body, `name="base_revision" value="`)
	if i < 0 {
		t.Fatal("no base_revision in the conflict form")
	}
	rev := strings.SplitN(body[i+len(`name="base_revision" value="`):], `"`, 2)[0]
	rec = e.s.post(t, e.owner, base+"/save", url.Values{"name": {"Máire Nolan"}, "summary": {"My careful summary"}, "base_revision": {rev}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("keep mine: %d", rec.Code)
	}
	// A bad custom field value is explained on the sheet.
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "fare", Label: "Fare", Type: "number"})
	rec = e.s.post(t, e.owner, base+"/save", url.Values{"name": {"Máire Nolan"}, "f.fare": {"old"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `value="old"`) {
		t.Errorf("bad field: %d", rec.Code)
	}
	// A relationship to a name that is not here says so.
	rec = e.s.post(t, e.owner, base+"/relation", url.Values{"other": {"Nobody"}, "kind": {"ally"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "No entity in this Compendium has that name") {
		t.Errorf("relation to nobody: %d", rec.Code)
	}
	// Trashing frees the name, and the sheet says so; restore is refused if the name was reused.
	if rec := e.s.post(t, e.owner, "/ui/entities/"+e.quay+"/trash", nil, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("trash: %d", rec.Code)
	}
	_, sheet := e.get(e.owner, "/c/"+e.slug+"/e/"+e.quay+"?saved=trashed")
	if !strings.Contains(sheet, "in the trash") || !strings.Contains(sheet, "free for other entities") {
		t.Error("the trashed sheet does not explain the freed name")
	}
	e.entity(t, "place", "Salt Quay", "A new one")
	if rec := e.s.post(t, e.owner, "/ui/entities/"+e.quay+"/restore", nil, "", false); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("restore over a reused name: %d", rec.Code)
	}
}

func TestEntityRelationsAndAliasesRoundTrip(t *testing.T) {
	e := newEntEnv(t)
	base := "/ui/entities/" + e.maire
	if rec := e.s.post(t, e.owner, base+"/alias", url.Values{"alias": {"Mam"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("alias: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, base+"/relation", url.Values{"other": {"salt quay"}, "kind": {"custom"}, "label": {"keeps the ferry at"}, "inverse_label": {"ferry kept by"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("relation: %d", rec.Code)
	}
	_, body := e.get(e.owner, "/c/"+e.slug+"/e/"+e.maire)
	for _, want := range []string{"Mam", "keeps the ferry at", "Salt Quay", "from this entity"} {
		if !strings.Contains(body, want) {
			t.Errorf("sheet lacks %q", want)
		}
	}
	// The other side reads it from its own side.
	_, body = e.get(e.owner, "/c/"+e.slug+"/e/"+e.quay)
	if !strings.Contains(body, "ferry kept by") || !strings.Contains(body, "from the other") {
		t.Error("the inverse reading is missing on the other sheet")
	}
	// Search finds the entity by its alias, and says where it matched.
	_, list := e.get(e.owner, "/c/"+e.slug+"/entities?q=mam")
	if !strings.Contains(list, "Máire Nolan") || !strings.Contains(list, "alias") {
		t.Errorf("an alias search did not find the entity:\n%s", between(list, `id="ent-list"`, "</div>"))
	}
	// Removing both.
	d := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if rec := e.s.post(t, e.owner, base+"/unalias", url.Values{"alias_id": {d.Aliases[0].ID}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("unalias: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, base+"/unrelate", url.Values{"relation_id": {d.Relations[0].ID}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("unrelate: %d", rec.Code)
	}
}

func TestEntityListIsPaged(t *testing.T) {
	e := newEntEnv(t)
	for i := 0; i < 60; i++ {
		e.entity(t, "item", fmt.Sprintf("Item %03d", i), "")
	}
	code, body := e.get(e.owner, "/c/"+e.slug+"/entities")
	if code != http.StatusOK {
		t.Fatal(code)
	}
	if n := strings.Count(body, `class="ent-row"`); n != entityPageSize {
		t.Errorf("%d rows on the first page, want %d", n, entityPageSize)
	}
	i := strings.Index(body, `rel="next" href="`)
	if i < 0 || !strings.Contains(body, "62 entities") {
		t.Fatal("no next page, or the total is not the whole count")
	}
	next := strings.SplitN(body[i+len(`rel="next" href="`):], `"`, 2)[0]
	next = strings.ReplaceAll(next, "&amp;", "&")
	_, body2 := e.get(e.owner, next)
	if n := strings.Count(body2, `class="ent-row"`); n != 12 || strings.Contains(body2, `rel="next"`) {
		t.Errorf("second page has %d rows", n)
	}
	// The list answers on its own for a live refresh, without the page around it.
	rec := e.s.do(e.owner, "GET", "/c/"+e.slug+"/entities?frag=1&kind=place", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), "Salt Quay") || strings.Contains(rec.Body.String(), "Item 001") || rec.Header().Get("Cache-Control") != "private, no-cache" || rec.Header().Get("ETag") == "" {
		t.Errorf("the list fragment: %d %q %q", rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("ETag"))
	}
	// A list nobody changed costs a 304 with no body.
	req := httptest.NewRequest("GET", "/c/"+e.slug+"/entities?frag=1&kind=place", nil)
	req.AddCookie(e.owner.cookie)
	req.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	again := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(again, req)
	if again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("an unchanged list: %d with %d bytes, want a 304 with none", again.Code, again.Body.Len())
	}
	// The trash is for people who can edit.
	e.call(t, "entity.trash", entities.StateIn{ID: e.quay})
	_, tr := e.get(e.editor, "/c/"+e.slug+"/entities?state=trashed")
	_, rd := e.get(e.reader, "/c/"+e.slug+"/entities?state=trashed")
	if !strings.Contains(tr, "Salt Quay") || strings.Contains(rd, "Salt Quay") {
		t.Error("the trash view is not limited to editors")
	}
}

func TestEntityMergeDryRunIsWhatMergeDoes(t *testing.T) {
	e := newEntEnv(t)
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "eyes", Label: "Eyes", Type: "text"})
	src := e.entity(t, "character", "Maire", "second")
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Fields: map[string]any{"eyes": "green"}})
	e.call(t, "entity.update", entities.UpdateIn{ID: src, Fields: map[string]any{"eyes": "grey"}})
	e.call(t, "entity.alias.add", entities.AliasAddIn{ID: src, Alias: "Mairín"})
	e.call(t, "entity.relation.add", entities.RelationAddIn{ID: src, ToEntityID: e.quay, Kind: "ally"})
	page := "/c/" + e.slug + "/e/" + src + "/merge"

	// The picker lists same-kind entities only.
	_, pick := e.get(e.owner, page+"?q=a")
	if !strings.Contains(pick, "Máire Nolan") || strings.Contains(pick, "Salt Quay") {
		t.Error("the picker is not limited to the same kind")
	}
	// The preview with a conflict unanswered shows it, and offers no merge button.
	_, prev := e.get(e.owner, page+"?into="+e.maire)
	if !strings.Contains(prev, `name="fc.eyes"`) || strings.Contains(prev, `name="do" value="merge"`) || !strings.Contains(prev, "Mairín") {
		t.Errorf("preview with a conflict:\n%s", between(prev, `id="merge"`, "</form>"))
	}
	// The same request answered: the preview now shows it can merge.
	form := url.Values{"into": {e.maire}, "fc.eyes": {"source"}, "summary": {"source"}, "body": {"append"}}
	rec := e.s.post(t, e.owner, "/ui/entities/"+src+"/merge", form, "", false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `name="do" value="merge"`) {
		t.Fatalf("answered preview: %d", rec.Code)
	}
	// The page's wording is built from the op's own plan: compare it with the op's dry run, then the real merge.
	dry := e.call(t, "entity.merge", entities.MergeIn{ID: src, IntoID: e.maire, DryRun: true, FieldChoices: map[string]string{"eyes": "source"}, Summary: "source", Body: "append"}).(entities.MergeOut)
	if dry.Merged || len(dry.Plan.Unresolved) != 0 {
		t.Fatalf("dry run: %+v", dry)
	}
	for _, want := range append([]string{dry.Plan.TargetName}, dry.Plan.AliasesMoved...) {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the preview does not mention %q", want)
		}
	}
	if !strings.Contains(rec.Body.String(), fmt.Sprintf("%d relationship", dry.Plan.RelationsRewired)) {
		t.Errorf("the preview's relationship count is not the plan's (%d)", dry.Plan.RelationsRewired)
	}
	// Writes nothing: the source is still there.
	if code, _ := e.get(e.owner, "/c/"+e.slug+"/e/"+src); code != 200 {
		t.Error("the preview deleted the source")
	}
	m := regexp.MustCompile(`name="plan" value="([0-9a-f]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatal("the preview does not carry its plan's fingerprint")
	}
	form.Set("do", "merge")
	// Merge without the fingerprint of a preview, or with another's, is refused and merges nothing.
	if r := e.s.post(t, e.owner, "/ui/entities/"+src+"/merge", form, "", false); r.Code != http.StatusConflict || !strings.Contains(r.Body.String(), "Something changed since you looked") {
		t.Errorf("a merge with no preview behind it: %d", r.Code)
	}
	// An entity changes between the preview and the button: refused, with the current preview.
	e.call(t, "entity.alias.add", entities.AliasAddIn{ID: src, Alias: "Added meanwhile"})
	form.Set("plan", m[1])
	r2 := e.s.post(t, e.owner, "/ui/entities/"+src+"/merge", form, "", false)
	if r2.Code != http.StatusConflict || !strings.Contains(r2.Body.String(), "Added meanwhile") || !strings.Contains(r2.Body.String(), "Something changed since you looked") {
		t.Fatalf("a change between preview and merge: %d", r2.Code)
	}
	if code, _ := e.get(e.owner, "/c/"+e.slug+"/e/"+src); code != 200 {
		t.Fatal("the refused merge went ahead")
	}
	m2 := regexp.MustCompile(`name="plan" value="([0-9a-f]+)"`).FindStringSubmatch(r2.Body.String())
	if m2 == nil || m2[1] == m[1] {
		t.Fatal("the fresh preview does not carry a new fingerprint")
	}
	form.Set("plan", m2[1])
	rec = e.s.post(t, e.owner, "/ui/entities/"+src+"/merge", form, "", false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/"+e.slug+"/e/"+e.maire+"?merged="+src {
		t.Fatalf("merge: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Fields["eyes"] != "grey" {
		t.Errorf("the chosen field did not win: %q", got.Fields["eyes"])
	}
	aliases := map[string]bool{}
	for _, a := range got.Aliases {
		aliases[a.Alias] = true
	}
	for _, a := range dry.Plan.AliasesMoved {
		if !aliases[a] {
			t.Errorf("alias %q promised by the preview is missing after the merge", a)
		}
	}
	// The old id leads to the survivor, says so, and the survivor says the merge is done.
	rec = e.s.do(e.owner, "GET", "/c/"+e.slug+"/e/"+src, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/"+e.slug+"/e/"+e.maire+"?followed=1" {
		t.Errorf("old id: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, b := e.get(e.owner, "/c/"+e.slug+"/e/"+e.maire+"?followed=1"); !strings.Contains(b, "was merged into this one") {
		t.Error("no notice that the address was followed")
	}
	if _, b := e.get(e.owner, "/c/"+e.slug+"/e/"+e.maire+"?merged="+src); !strings.Contains(b, "The merge is done") {
		t.Error("no notice that the merge happened")
	}
	// A merge that is not fully answered is refused with its reason, and nothing changes.
	src2 := e.entity(t, "character", "Third", "")
	e.call(t, "entity.update", entities.UpdateIn{ID: src2, Fields: map[string]any{"eyes": "brown"}})
	rec = e.s.post(t, e.owner, "/ui/entities/"+src2+"/merge", url.Values{"into": {e.maire}, "do": {"merge"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("an unanswered conflict was merged: %d", rec.Code)
	}
}

func TestEntityProjectViewAndOverrides(t *testing.T) {
	e := newEntEnv(t)
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "fare", Label: "Fare", Type: "number"})
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Fields: map[string]any{"fare": "34"}})
	list := "/p/" + e.pslug + "/entities"
	// A member of the project who is not on the Compendium sees the bible is not shared, and no name from it.
	code, body := e.get(e.projectOnly, list)
	if code != 200 || strings.Contains(body, "Máire") || strings.Contains(body, "Salt Quay") || strings.Contains(body, "Salt Bible") || !strings.Contains(body, "not a member of") {
		t.Errorf("project-only member's list: %d\n%s", code, between(body, `id="ent-list"`, "</div>"))
	}
	if code, _ := e.get(e.projectOnly, "/p/"+e.pslug+"/e/"+e.maire); code != http.StatusNotFound {
		t.Errorf("project-only member at an entity sheet: %d", code)
	}
	if code, _ := e.get(e.stranger, list); code != http.StatusNotFound {
		t.Errorf("stranger at the project list: %d", code)
	}
	// The owner sees both, and can override.
	_, body = e.get(e.owner, list)
	if !strings.Contains(body, "Máire Nolan") || !strings.Contains(body, "Primary Compendium") {
		t.Errorf("the owner's project list:\n%s", between(body, `id="ent-list"`, "</div>"))
	}
	post := func(c caller, action string, form url.Values) *httptest.ResponseRecorder {
		return e.s.post(t, c, "/ui/projects/"+e.pid+"/entity-override/"+action, form, "", false)
	}
	if r := post(e.owner, "set", url.Values{"entity_id": {e.maire}, "name": {"Mairín"}, "f.fare": {"36"}, "notes": {"Older in this book."}}); r.Code != http.StatusSeeOther {
		t.Fatalf("override: %d", r.Code)
	}
	_, sheet := e.get(e.owner, "/p/"+e.pslug+"/e/"+e.maire+"?saved=override")
	for _, want := range []string{"Mairín", "36", "Older in this book.", "The Compendium calls it Máire Nolan", "The Compendium says: 34", "override is saved"} {
		if !strings.Contains(strings.ReplaceAll(sheet, "’", "&rsquo;"), strings.ReplaceAll(want, "’", "&rsquo;")) {
			t.Errorf("the override sheet lacks %q", want)
		}
	}
	// The Compendium's own record is untouched.
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Name != "Máire Nolan" || got.Fields["fare"] != "34" {
		t.Errorf("an override changed the Compendium: %+v", got)
	}
	// An editor of the project who is a reader of the Compendium may override; a project reader may not.
	pr := e.s.newCaller(t, "project-reader", ops.RoleWriter)
	e.s.addMember(t, e.pid, pr.userID, ops.ProjectReader)
	e.s.addCompendiumMember(t, e.cid, pr.userID, ops.ProjectReader)
	if r := post(pr, "set", url.Values{"entity_id": {e.maire}, "name": {"X"}}); r.Code != http.StatusForbidden {
		t.Errorf("a project reader overriding: %d", r.Code)
	}
	if _, b := e.get(pr, "/p/"+e.pslug+"/e/"+e.maire); strings.Contains(b, "entity-override/set") {
		t.Error("a project reader is offered the override form")
	}
	if r := post(e.projectOnly, "set", url.Values{"entity_id": {e.maire}, "name": {"X"}}); r.Code != http.StatusNotFound && r.Code != http.StatusForbidden {
		t.Errorf("a person who cannot read the Compendium overriding: %d", r.Code)
	}
	// A bad value is explained and stays in its box.
	r := post(e.owner, "set", url.Values{"entity_id": {e.maire}, "f.fare": {"old"}})
	if r.Code != http.StatusUnprocessableEntity || !strings.Contains(r.Body.String(), `value="old"`) {
		t.Errorf("a bad override value: %d", r.Code)
	}
	// Clearing goes back to the Compendium's own.
	if r := post(e.owner, "clear", url.Values{"entity_id": {e.maire}}); r.Code != http.StatusSeeOther {
		t.Fatalf("clear: %d", r.Code)
	}
	_, sheet = e.get(e.owner, "/p/"+e.pslug+"/e/"+e.maire)
	if strings.Contains(sheet, "Older in this book") || strings.Contains(sheet, "Clear this book") {
		t.Error("the override survived clearing")
	}
	// A list fragment, for a live refresh.
	rec := e.s.do(e.owner, "GET", list+"?frag=1", "")
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), "Máire Nolan") {
		t.Errorf("project list fragment: %d", rec.Code)
	}
}

func TestEntityFieldsPage(t *testing.T) {
	e := newEntEnv(t)
	base := "/ui/compendiums/" + e.cid + "/fields/"
	if rec := e.s.post(t, e.owner, base+"set", url.Values{"kind": {"character"}, "key": {"side"}, "label": {"Allegiance"}, "type": {"choice"}, "choices": {"Crown\r\nQuay\r\n"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: %d", rec.Code)
	}
	_, body := e.get(e.owner, "/c/"+e.slug+"/fields")
	if !strings.Contains(body, "Allegiance") || !strings.Contains(body, "Crown") {
		t.Error("the new field is not listed")
	}
	if _, sheet := e.get(e.owner, "/c/"+e.slug+"/e/"+e.maire); !strings.Contains(sheet, `name="f.side"`) || !strings.Contains(sheet, "<option value=\"Quay\"") {
		t.Error("the field is not on the sheet")
	}
	// A bad key is explained next to the form.
	rec := e.s.post(t, e.owner, base+"set", url.Values{"kind": {"character"}, "key": {"Bad Key"}, "type": {"text"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `value="Bad Key"`) {
		t.Errorf("bad key: %d", rec.Code)
	}
	// Removing a field that has values is refused until the person says to drop them.
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Fields: map[string]any{"side": "Quay"}})
	rec = e.s.post(t, e.owner, base+"remove", url.Values{"kind": {"character"}, "key": {"side"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("removing a field with values: %d", rec.Code)
	}
	rec = e.s.post(t, e.owner, base+"remove", url.Values{"kind": {"character"}, "key": {"side"}, "drop_values": {"1"}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("removing with drop_values: %d", rec.Code)
	}
}

func TestEntityExportAndPreview(t *testing.T) {
	e := newEntEnv(t)
	rec := e.s.post(t, e.owner, "/c/"+e.slug+"/export", nil, "", false)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") ||
		rec.Header().Get("X-Export-Entities") != "2" || len(rec.Header().Get("X-Export-Sha256")) != 64 || rec.Header().Get("X-Export-Id") == "" {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil || len(zr.File) == 0 {
		t.Fatalf("the export is not a zip: %v", err)
	}
	if rec := e.s.post(t, e.reader, "/c/"+e.slug+"/export", nil, "", false); rec.Code != http.StatusForbidden {
		t.Errorf("a reader exporting: %d", rec.Code)
	}
	if rec := e.s.post(t, e.stranger, "/c/"+e.slug+"/export", nil, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("a stranger exporting: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, "/c/"+e.slug+"/export", nil, "-", false); rec.Code != http.StatusForbidden {
		t.Errorf("an export with no CSRF token: %d", rec.Code)
	}
	// The preview renders with the export's whitelist: raw HTML is text, and the answer is a fragment.
	rec = e.s.post(t, e.owner, "/ui/md-preview", url.Values{"body": {"**bold** <script>alert(1)</script> <img src=x onerror=alert(2)> [x](javascript:alert(3))"}}, "", true)
	b := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(b, "<strong>bold</strong>") || strings.Contains(b, "<script") || strings.Contains(b, "<img") || strings.Contains(b, `href="javascript`) || strings.Contains(b, "<html") {
		t.Errorf("preview: %d %s", rec.Code, b)
	}
	if rec := e.s.post(t, caller{name: "anon"}, "/ui/md-preview", url.Values{"body": {"x"}}, "-", false); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous preview: %d", rec.Code)
	}
}

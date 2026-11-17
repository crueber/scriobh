package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// A Compendium in the trash can be read and restored and nothing else: every page of it says so, hides the forms that
// would change it, and the ops behind them answer 409. An archived one is only on a shelf: it says so and stays
// writable.
func TestTrashedCompendiumPagesAreReadOnlyAndSayWhy(t *testing.T) {
	e := newEntEnv(t)
	e.call(t, "compendium.trash", compendiums.StateIn{ID: e.cid})
	for _, tc := range []struct{ path, form string }{
		{"/c/" + e.slug + "/entities", `id="add"`},
		{"/c/" + e.slug + "/research", `/research/create`},
		{"/c/" + e.slug + "/files", `id="att-drop"`},
		{"/c/" + e.slug + "/timeline", `/timeline/events/new`},
		{"/c/" + e.slug + "/e/" + e.maire, `/ui/entities/` + e.maire + `/relation`},
	} {
		code, body := e.get(e.owner, tc.path)
		if code != 200 {
			t.Errorf("%s as the owner: %d", tc.path, code)
			continue
		}
		if !strings.Contains(body, "This Compendium is in the trash.") || !strings.Contains(body, "Restore it") {
			t.Errorf("%s has no trash banner with its Restore button", tc.path)
		}
		if strings.Contains(body, tc.form) {
			t.Errorf("%s still draws a form that changes the Compendium (%s)", tc.path, tc.form)
		}
		// Only the owner finds a trashed Compendium at all; to everyone else it is gone until it is restored.
		if code, _ := e.get(e.editor, tc.path); code != http.StatusNotFound {
			t.Errorf("%s as an editor: %d, want 404", tc.path, code)
		}
	}
	// The button behind a hidden form, pressed anyway, is refused with the reason.
	rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/entities", url.Values{"name": {"Late arrival"}, "kind": {"character"}}, "", false)
	if rec.Code < 400 || !strings.Contains(rec.Body.String(), "in the trash") {
		t.Errorf("creating an entity in a trashed Compendium by the form: %d %.200s", rec.Code, rec.Body.String())
	}
	// Restore from the banner's own form, and the page is whole again.
	if r := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/restore", url.Values{}, "", false); r.Code != http.StatusSeeOther {
		t.Fatalf("restore: %d", r.Code)
	}
	if _, body := e.get(e.owner, "/c/"+e.slug+"/entities"); strings.Contains(body, "in the trash") || !strings.Contains(body, `id="add"`) {
		t.Error("a restored Compendium still reads as trashed")
	}
}

func TestArchivedCompendiumStaysWritableAndSaysItIsOnTheShelf(t *testing.T) {
	e := newEntEnv(t)
	e.call(t, "compendium.archive", compendiums.StateIn{ID: e.cid})
	code, body := e.get(e.owner, "/c/"+e.slug+"/entities")
	if code != 200 || !strings.Contains(body, "Archived.") || !strings.Contains(body, "Unarchive") || !strings.Contains(body, `id="add"`) {
		t.Errorf("owner at an archived Compendium: %d (banner, Unarchive and the add form are expected)", code)
	}
	if _, body = e.get(e.editor, "/c/"+e.slug+"/entities"); !strings.Contains(body, "Archived.") || strings.Contains(body, "Unarchive") {
		t.Error("an editor sees the banner without the owner's button")
	}
	name := "Edited on the shelf"
	if _, err := e.s.Reg.Call(context.Background(), ops.Principal{UserID: e.editor.userID, Role: ops.RoleWriter}, "entity.update", entities.UpdateIn{ID: e.maire, Name: &name}); err != nil {
		t.Errorf("entity.update on an archived Compendium: %v", err)
	}
	if r := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/unarchive", url.Values{}, "", false); r.Code != http.StatusSeeOther {
		t.Errorf("unarchive from the banner: %d", r.Code)
	}
	if _, body = e.get(e.owner, "/c/"+e.slug+"/entities"); strings.Contains(body, "Archived.") {
		t.Error("an unarchived Compendium still says it is archived")
	}
}

// Slugs are unique per owner. A person who owns "Cosmos" and edits someone else's "Cosmos" gets the first at
// /c/cosmos and the second at /c/cosmos~suffix, and every link says so.
func TestCompendiumSlugCollisionAcrossOwners(t *testing.T) {
	e := newEntEnv(t)
	ctx := context.Background()
	mine := ops.Principal{UserID: e.owner.userID, Role: ops.RoleWriter}
	theirs := ops.Principal{UserID: e.editor.userID, Role: ops.RoleWriter}
	mk := func(p ops.Principal, desc string) compendiums.Compendium {
		out, err := e.s.Reg.Call(ctx, p, "compendium.create", compendiums.CreateIn{Name: "Cosmos", Description: desc})
		if err != nil {
			t.Fatal(err)
		}
		return out.(compendiums.Item).Item
	}
	own, shared := mk(mine, "mine alone"), mk(theirs, "shared with me")
	if own.Slug != shared.Slug {
		t.Fatalf("the scenario needs one slug: %q and %q", own.Slug, shared.Slug)
	}
	e.s.addCompendiumMember(t, shared.ID, e.owner.userID, ops.ProjectEditor)
	suffix := shared.ID[len(shared.ID)-6:]
	addr := "/c/" + shared.Slug + "~" + suffix

	if code, body := e.get(e.owner, "/c/"+own.Slug); code != 200 || !strings.Contains(body, "mine alone") {
		t.Errorf("the plain address is the person's own: %d", code)
	}
	if code, body := e.get(e.owner, addr); code != 200 || !strings.Contains(body, "shared with me") || strings.Contains(body, "mine alone") {
		t.Errorf("the suffixed address opens the shared one: %d", code)
	}
	if code, _ := e.get(e.owner, "/c/"+shared.Slug+"~zzzzzz"); code != http.StatusNotFound {
		t.Errorf("a wrong suffix: %d, want 404", code)
	}
	// The pages of the shared one link to themselves with the suffix, and so does the library.
	_, body := e.get(e.owner, addr+"/entities")
	if !strings.Contains(body, addr+"/research") || strings.Contains(body, `href="/c/`+shared.Slug+`/research"`) {
		t.Error("the tabs of the shared Compendium do not carry its address")
	}
	_, lib := e.get(e.owner, "/compendiums")
	if !strings.Contains(lib, `href="`+addr+`"`) || !strings.Contains(lib, `href="/c/`+own.Slug+`"`) {
		t.Errorf("the library does not link each Compendium to its own address:\n%s", between(lib, "Cosmos", "Cosmos"))
	}
	// The person who does not read the other one is not touched: the plain slug is theirs alone.
	if _, body := e.get(e.editor, "/c/"+shared.Slug+"/entities"); strings.Contains(body, "~") && strings.Contains(body, "/c/"+shared.Slug+"~") {
		t.Error("an owner with no collision was given a suffix")
	}
}

// The same collision between two projects: the bare slug is the person's own, slug~suffix names the other.
func TestProjectSlugCollisionResolvesBySuffix(t *testing.T) {
	e := newEntEnv(t)
	ctx := context.Background()
	theirs := ops.Principal{UserID: e.editor.userID, Role: ops.RoleWriter}
	out, err := e.s.Reg.Call(ctx, theirs, "project.create", projects.CreateIn{Title: "The Salt Road"})
	if err != nil {
		t.Fatal(err)
	}
	other := out.(projects.Item).Item
	if other.Slug != e.pslug {
		t.Fatalf("the scenario needs one slug: %q and %q", other.Slug, e.pslug)
	}
	if _, err := e.s.Reg.Call(ctx, theirs, "doc.create", docs.CreateIn{ProjectID: other.ID, Title: "Only in theirs", Kind: "chapter"}); err != nil {
		t.Fatal(err)
	}
	e.s.addMember(t, other.ID, e.owner.userID, ops.ProjectEditor)
	if _, body := e.get(e.owner, "/p/"+e.pslug+"/outline"); strings.Contains(body, "Only in theirs") {
		t.Error("the bare slug opened the other person's project")
	}
	addr := e.pslug + "~" + other.ID[len(other.ID)-6:]
	if code, body := e.get(e.owner, "/p/"+addr+"/outline"); code != 200 || !strings.Contains(body, "Only in theirs") {
		t.Errorf("the suffixed slug did not open the shared project: %d", code)
	} else if !strings.Contains(body, `href="/p/`+addr+`/goals"`) || strings.Contains(body, `href="/p/`+e.pslug+`/goals"`) {
		t.Error("the shared project's pages link to the plain slug, which is the person's own project")
	}
	// The ops give each project its address; the library and the project panel use it.
	if _, lib := e.get(e.owner, "/"); !strings.Contains(lib, `href="/p/`+addr+`"`) || !strings.Contains(lib, `href="/p/`+e.pslug+`"`) {
		t.Errorf("the library does not link each project to its own address")
	}
	var list struct {
		Items []struct{ ID, Slug, Address string } `json:"items"`
	}
	rec := e.s.do(e.owner, "GET", "/api/v1/projects", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, it := range list.Items {
		got[it.ID] = it.Address
	}
	if got[other.ID] != addr || got[e.pid] != e.pslug {
		t.Errorf("project.list addresses: %v, want %s for the shared one and %s for the person's own", got, addr, e.pslug)
	}
}

// The plan's counts do not move when only a summary changes, but the merge must still refuse text nobody previewed.
func TestMergeRefusesAfterASummaryOnlyEdit(t *testing.T) {
	e := newEntEnv(t)
	src := e.entity(t, "character", "Mairín", "second")
	page := "/c/" + e.slug + "/e/" + src + "/merge"
	_, prev := e.get(e.owner, page+"?into="+e.maire+"&summary=target&body=target")
	m := regexp.MustCompile(`name="plan" value="([0-9a-f]+)"`).FindStringSubmatch(prev)
	if m == nil {
		t.Fatalf("no fingerprint in the preview:\n%s", between(prev, `id="merge-form"`, "</form>"))
	}
	summary := "A summary typed after the preview was drawn."
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Summary: &summary})
	form := url.Values{"into": {e.maire}, "summary": {"target"}, "body": {"target"}, "do": {"merge"}, "plan": {m[1]}}
	rec := e.s.post(t, e.owner, "/ui/entities/"+src+"/merge", form, "", false)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Something changed since you looked") {
		t.Fatalf("a merge after a summary-only edit: %d", rec.Code)
	}
	if code, _ := e.get(e.owner, "/c/"+e.slug+"/e/"+src); code != 200 {
		t.Error("the refused merge went ahead")
	}
}

// Every page of a Compendium in the trash draws no form that would change it: the only forms are the ways out
// (restore, purge), the export, membership, and signing out.
func TestTrashedCompendiumPagesDrawOnlyTheWaysOut(t *testing.T) {
	e := newEntEnv(t)
	note := e.call(t, "compendium.research.create", research.CreateIn{ID: e.cid, Title: "A note", Kind: "note", Body: "text"}).(research.Item).Item.ID
	ev := e.call(t, "timeline.event.create", timeline.CreateIn{ID: e.cid, Title: "An event"}).(timeline.EventItem).Item.ID
	// Two more Compendiums, so the overview has references to offer and to show.
	e.call(t, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: e.cid, TargetID: e.call(t, "compendium.create", compendiums.CreateIn{Name: "Referenced"}).(compendiums.Item).Item.ID})
	e.call(t, "compendium.create", compendiums.CreateIn{Name: "Offered"})
	e.call(t, "compendium.trash", compendiums.StateIn{ID: e.cid})
	c := "/c/" + e.slug
	allowed := regexp.MustCompile(`/(restore|purge|export|member-add|member-role|member-remove)$|^/auth/logout$|^/c/[^/]+/export$|^/ui/compendiums/[^/]+/purge$`)
	forms := regexp.MustCompile(`(?s)<form[^>]*method="post"[^>]*action="([^"]*)"`)
	for _, path := range []string{c, c + "/entities", c + "/fields", c + "/e/" + e.maire, c + "/research", c + "/research/board",
		c + "/research/tags", c + "/research/" + note, c + "/scratch", c + "/files", c + "/timeline", c + "/timeline/events/" + ev} {
		code, body := e.get(e.owner, path)
		if code != 200 {
			t.Errorf("%s as the owner of a trashed Compendium: %d", path, code)
			continue
		}
		if !strings.Contains(body, "This Compendium is in the trash.") {
			t.Errorf("%s has no trash banner", path)
		}
		for _, m := range forms.FindAllStringSubmatch(body, -1) {
			if !allowed.MatchString(m[1]) {
				t.Errorf("%s draws a form that would change a trashed Compendium: %s", path, m[1])
			}
		}
	}
}

// The limits the server hands the alternatives ops are the defaults with the configured per-document figure: a zero
// per-alternative limit would refuse every wording after the first.
func TestAlternativesLimitsAreNotZero(t *testing.T) {
	e := newWriteEnv(t)
	cur := e.call(t, "doc.get", docs.DocIn{ID: e.chap}).(docs.Item).Item
	body := "The harbour was quiet.\n"
	e.call(t, "doc.update", docs.UpdateIn{ID: e.chap, BaseRevision: cur.Revision, Content: &body})
	rev := e.call(t, "doc.get", docs.DocIn{ID: e.chap}).(docs.Item).Item.Revision
	rec := e.s.do(e.owner, "POST", "/api/v1/docs/"+e.chap+"/alternatives", fmt.Sprintf(`{"base_revision":%d,"scope":"word","quote":"quiet","variants":["still"]}`, rev))
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var one struct {
		Revision    int `json:"revision"`
		Alternative struct {
			ID string `json:"id"`
		} `json:"alternative"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	rec = e.s.do(e.owner, "POST", "/api/v1/docs/"+e.chap+"/alternatives/variants", fmt.Sprintf(`{"base_revision":%d,"alternative_id":%q,"text":"calm"}`, one.Revision, one.Alternative.ID))
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("a third wording: %d %s", rec.Code, rec.Body)
	}
}

// The Stash tab and panel are for people who can edit: a reader's page has the Alternatives tab (to read) and no Stash.
func TestStashTabIsForEditorsOnly(t *testing.T) {
	e := newWriteEnv(t)
	reader := e.s.newCaller(t, "reader", ops.RoleReader)
	e.s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	page := func(c caller) string {
		rec := e.s.do(c, "GET", "/p/"+e.slug+"/d/"+e.chap, "")
		if rec.Code != 200 {
			t.Fatalf("page: %d", rec.Code)
		}
		return rec.Body.String()
	}
	if b := page(e.owner); !strings.Contains(b, `id="itab-stash"`) || !strings.Contains(b, `id="ipanel-stash"`) {
		t.Error("an editor's page has no Stash tab")
	}
	b := page(reader)
	if strings.Contains(b, `id="itab-stash"`) || strings.Contains(b, `id="ipanel-stash"`) {
		t.Error("a reader's page has a Stash tab")
	}
	if !strings.Contains(b, `id="itab-alts"`) {
		t.Error("a reader cannot read the alternatives")
	}
}

// The export dialog offers ghosts and alternatives as keep or omit, says what each does, and the choice reaches the op.
func TestExportDialogOffersGhostAndAlternativeChoices(t *testing.T) {
	e := newWriteEnv(t)
	body := "She waited by the old window.\n\nThe <scriobh-ghost>cut sentence stays hidden.</scriobh-ghost> Evening came.\n"
	cur := e.call(t, "doc.get", docs.DocIn{ID: e.chap}).(docs.Item).Item
	e.call(t, "doc.update", docs.UpdateIn{ID: e.chap, BaseRevision: cur.Revision, Content: &body})
	rev := e.call(t, "doc.get", docs.DocIn{ID: e.chap}).(docs.Item).Item.Revision
	rec := e.s.do(e.owner, "POST", "/api/v1/docs/"+e.chap+"/alternatives", fmt.Sprintf(`{"base_revision":%d,"scope":"word","quote":"waited","variants":["lingered"]}`, rev))
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("alternative: %d %s", rec.Code, rec.Body)
	}

	form := e.s.do(e.owner, "GET", "/ui/projects/"+e.pid+"/export", "").Body.String()
	for _, want := range []string{`name="ghosts"`, `name="alternatives"`, "The stash is never part of an export", "not counted"} {
		if !strings.Contains(form, want) {
			t.Errorf("the export dialog lacks %q", want)
		}
	}
	lint(t, "export dialog", form)

	get := func(v url.Values) string {
		v.Set("mode", "single")
		r := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", v, "", true)
		if r.Code != 200 {
			t.Fatalf("export %v: %d %s", v, r.Code, r.Body)
		}
		return r.Body.String()
	}
	kept := get(url.Values{})
	if !strings.Contains(kept, "<scriobh-ghost>") || !strings.Contains(kept, "scriobh:alts") {
		t.Error("by default the ghosts and the alternatives comment are kept")
	}
	left := get(url.Values{"ghosts": {"omit"}, "alternatives": {"omit"}})
	if strings.Contains(left, "scriobh-ghost") || strings.Contains(left, "cut sentence") || strings.Contains(left, "scriobh:alts") {
		t.Errorf("omit left something behind:\n%s", left)
	}
	if !strings.Contains(left, "waited") || !strings.Contains(left, "Evening came.") {
		t.Error("omit removed the chosen text")
	}

	// The HTML export reads the same choice through the dialog: kept by default (without the tags), left out on request.
	html := func(v url.Values) string {
		v.Set("mode", "single")
		v.Set("format", "html")
		r := e.s.post(t, e.owner, "/ui/projects/"+e.pid+"/export", v, "", true)
		if r.Code != 200 {
			t.Fatalf("html export %v: %d %s", v, r.Code, r.Body)
		}
		return r.Body.String()
	}
	if h := html(url.Values{}); !strings.Contains(h, "cut sentence stays hidden.") || strings.Contains(h, "scriobh-ghost") {
		t.Error("the HTML export keeps ghosted text by default, without its tags")
	}
	if h := html(url.Values{"ghosts": {"omit"}}); strings.Contains(h, "cut sentence") || !strings.Contains(h, "Evening came.") {
		t.Errorf("the HTML export ignored the choice to leave ghosted text out")
	}
}

package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The writing view's Compendium tab: its fragments, what each change does, and what each kind of person is shown (F-1253 to F-1260).

type lkEnv struct {
	entEnv
	base string
	// peReader is an editor of the book and a reader of its Compendium; ceProjReader is an editor of the Compendium and a reader of the book.
	peReader, ceProjReader, reviewer caller
}

func newLkEnv(t *testing.T) lkEnv {
	t.Helper()
	e := lkEnv{entEnv: newEntEnv(t)}
	e.base = "/ui/projects/" + e.pid + "/lookup"
	e.peReader = e.s.newCaller(t, "pe-reader", ops.RoleWriter)
	e.s.addMember(t, e.pid, e.peReader.userID, ops.ProjectEditor)
	e.s.addCompendiumMember(t, e.cid, e.peReader.userID, ops.ProjectReader)
	e.ceProjReader = e.s.newCaller(t, "ce-proj-reader", ops.RoleWriter)
	e.s.addMember(t, e.pid, e.ceProjReader.userID, ops.ProjectReader)
	e.s.addCompendiumMember(t, e.cid, e.ceProjReader.userID, ops.ProjectEditor)
	e.reviewer = e.s.newCaller(t, "lk-reviewer", ops.RoleReader)
	e.s.addMember(t, e.pid, e.reviewer.userID, ops.ProjectReviewer)
	e.s.addCompendiumMember(t, e.cid, e.reviewer.userID, ops.ProjectReader)
	return e
}

// frag reads a fragment as the panel's script does (the fetch header: no sign-in page, JSON for a refusal).
func (e lkEnv) frag(c caller, query string) (int, string) {
	rec := e.s.do(c, "GET", e.base+query, "")
	return rec.Code, rec.Body.String()
}

func (e lkEnv) postFrag(t *testing.T, c caller, action string, form url.Values) (int, string, http.Header) {
	t.Helper()
	rec := e.s.post(t, c, e.base+"/"+action, form, "", true)
	return rec.Code, rec.Body.String(), rec.Header()
}

func (e lkEnv) revision(t *testing.T, id string) int {
	return e.call(t, "entity.get", entities.GetIn{ID: id}).(entities.DetailItem).Item.Revision
}

func TestLookupListFindsByNameAndByOtherName(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "entity.alias.add", entities.AliasAddIn{ID: e.maire, Alias: "Mam"})
	code, body := e.frag(e.owner, "")
	if code != 200 || !strings.Contains(body, "Máire Nolan") || !strings.Contains(body, "Salt Quay") || !strings.Contains(body, "2 entries") {
		t.Fatalf("the list: %d %.600s", code, body)
	}
	if _, body = e.frag(e.owner, "?q=mam&part=rows"); !strings.Contains(body, "Máire Nolan") || strings.Contains(body, "Salt Quay") || !strings.Contains(body, "1 match") {
		t.Errorf("a search by another name: %.500s", body)
	}
	if _, body = e.frag(e.owner, "?kind=place&part=rows"); !strings.Contains(body, "Salt Quay") || strings.Contains(body, "Máire Nolan") {
		t.Errorf("the kind filter: %.500s", body)
	}
	if _, body = e.frag(e.owner, "?q=zzzz&part=rows"); !strings.Contains(body, "Nothing matches") {
		t.Errorf("an empty search: %.500s", body)
	}
	// The primary Compendium's entries carry no tag; a referenced Compendium's entry says where it lives, and an entry the book renames
	// carries the book's version.
	ref := e.call(t, "compendium.create", compendiums.CreateIn{Name: "Shared lore"}).(compendiums.Item).Item
	e.call(t, "entity.create", entities.CreateIn{ID: ref.ID, Kind: "item", Name: "The brass lantern"})
	e.call(t, "project.compendium.ref.add", compendiums.RefAddIn{ID: e.pid, CompendiumID: ref.ID})
	e.call(t, "project.entity.override.set", entities.OverrideSetIn{ID: e.pid, EntityID: e.quay, Name: strp("The Quay")})
	_, body = e.frag(e.owner, "?part=rows")
	if !strings.Contains(body, "Referenced: Shared lore") || !strings.Contains(body, "This book&#39;s version") || strings.Contains(body, "Primary Compendium") {
		t.Errorf("tags: %s", between(body, "<ul", "</ul>"))
	}
	if !strings.Contains(body, "The Quay") {
		t.Errorf("the book's own name for an entry is shown: %.400s", body)
	}
	// Paging: the next page is a button with the offset.
	for i := 0; i < lkPage+2; i++ {
		e.entity(t, "item", "Bulk "+strconv.Itoa(i), "")
	}
	if _, body = e.frag(e.owner, "?part=rows"); !strings.Contains(body, "offset="+strconv.Itoa(lkPage)) {
		t.Errorf("no More button: %.300s", between(body, `id="lk-more"`, "</div>"))
	}
	if _, body = e.frag(e.owner, "?part=more&offset="+strconv.Itoa(lkPage)); !strings.Contains(body, `id="lk-more"`) || !strings.Contains(body, "lk-row") {
		t.Errorf("the next page: %.300s", body)
	}
}

func strp(s string) *string { return &s }

func TestLookupEntryHasTheRevisionAndRendersTheBodySafely(t *testing.T) {
	e := newLkEnv(t)
	hostile := "She knows **every** tide.\n\n<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>"
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Body: &hostile})
	rev := e.revision(t, e.maire)
	code, body := e.frag(e.owner, "?entity="+e.maire)
	if code != 200 || !strings.Contains(body, `name="base_revision" value="`+strconv.Itoa(rev)+`"`) {
		t.Fatalf("the entry carries the revision %d: %d %.400s", rev, code, body)
	}
	if !strings.Contains(body, `id="es-summary"`) || !strings.Contains(body, `id="es-body"`) || !strings.Contains(body, "Fields (0 of 0 filled)") && !strings.Contains(body, "Fields (") {
		t.Errorf("summary, description and fields: %.500s", body)
	}
	if strings.Contains(body, "<script>alert") || strings.Contains(body, "<img src=x") {
		t.Errorf("raw HTML reached the panel as markup")
	}
	// Read only, the description is rendered through the whitelist renderer (raw HTML is text, **bold** is bold).
	_, ro := e.frag(e.reviewer, "?entity="+e.maire)
	if !strings.Contains(ro, "<strong>every</strong>") || strings.Contains(ro, "<script>") || strings.Contains(ro, "<img") {
		t.Errorf("the rendered description: %s", between(ro, `class="md en-md"`, "</div>"))
	}
	// the scope switch, for a person who may do both
	_, both := e.frag(e.owner, "?entity="+e.maire+"&scope=book")
	if !strings.Contains(both, `data-scope="book"`) || !strings.Contains(both, `id="ov-name"`) || strings.Contains(both, `id="es-body"`) {
		t.Errorf("this book's version: %.600s", both)
	}
}

func TestLookupSaveChangesOnlyWhatTheFormSentAndRefusesAStaleRevision(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, SortName: strp("Nolan, Máire"), Color: strp("#2F5D50"), Body: strp("The original description.")})
	rev := e.revision(t, e.maire)
	code, body, _ := e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "base_revision": {strconv.Itoa(rev)}, "summary": {"A new summary."}})
	if code != 200 || !strings.Contains(body, `id="lk-rev"`) || !strings.Contains(body, `value="`+strconv.Itoa(rev+1)+`"`) || !strings.Contains(body, "Saved.") {
		t.Fatalf("a save: %d %s", code, body)
	}
	if strings.Contains(body, "A new summary.") {
		t.Errorf("the answer carries the words back, and would replace what is typed: %s", body)
	}
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Summary != "A new summary." || got.SortName != "Nolan, Máire" || got.Color != "#2F5D50" || got.Name != "Máire Nolan" || got.Body != "The original description." {
		t.Errorf("a save of the summary changed more than the summary: %+v", got)
	}
	// A stale revision: 422, both texts in the box, and nothing changed.
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Summary: strp("Someone else's summary."), Body: strp("Someone else's description.")})
	code, body, hdr := e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "base_revision": {strconv.Itoa(rev + 1)}, "summary": {"Mine."}, "body": {"My description."}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "lk-conflict") || !strings.Contains(body, "Someone else&#39;s summary.") || !strings.Contains(body, "Someone else&#39;s description.") ||
		!strings.Contains(body, `data-lk-keep="`+strconv.Itoa(rev+2)+`"`) || !strings.Contains(body, "Use theirs") {
		t.Fatalf("a stale save: %d %s", code, body)
	}
	if hdr.Get("HX-Retarget") != "#lk-status" {
		t.Errorf("the conflict must go to the status line, not over the form: %v", hdr)
	}
	if strings.Contains(body, "My description.") {
		t.Errorf("the typed words must stay in the form, not in the answer")
	}
	got = e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Summary != "Someone else's summary." || got.Body != "Someone else's description." {
		t.Errorf("a refused save changed the entity: %+v", got)
	}
	// Keep mine is the same save with the current revision.
	code, _, _ = e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "base_revision": {strconv.Itoa(rev + 2)}, "summary": {"Mine."}, "body": {"My description."}})
	got = e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if code != 200 || got.Summary != "Mine." || got.Body != "My description." {
		t.Errorf("Keep mine: %d %+v", code, got)
	}
	// A field the form does not carry is left alone, and a refused value is explained.
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "fare", Label: "Fare", Type: "number"})
	code, body, _ = e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "f.fare": {"a lot"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "lk-bad") {
		t.Errorf("a value of the wrong type: %d %s", code, body)
	}
	code, _, _ = e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "f.fare": {"3"}})
	got = e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if code != 200 || got.Fields["fare"] != "3" || got.Summary != "Mine." {
		t.Errorf("a field: %d %+v", code, got)
	}
}

// A save of a field draws the Fields heading again with the count the save left, so the sidebar's "(n of m filled)" is not
// the count from before the save.
func TestLookupSaveRedrawsTheFieldsCount(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "fare", Label: "Fare", Type: "text"})
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "age", Label: "Age", Type: "text"})
	// the text of the Fields heading that opens with open ("" when the body has none)
	heading := func(body, open string) string {
		i := strings.Index(body, open)
		if i < 0 {
			return ""
		}
		rest := body[i+len(open):]
		if j := strings.Index(rest, "</h2>"); j >= 0 {
			return rest[:j]
		}
		return ""
	}
	plain := `<h2 id="h-fields" class="en-h">`
	_, body := e.frag(e.owner, "?entity="+e.maire+"&scope=record")
	before := heading(body, plain)
	if before == "" {
		t.Fatalf("no Fields heading: %.300s", body)
	}
	code, saved, _ := e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "base_revision": {strconv.Itoa(e.revision(t, e.maire))}, "f.fare": {"3"}})
	if code != 200 {
		t.Fatalf("save: %d %s", code, saved)
	}
	_, body = e.frag(e.owner, "?entity="+e.maire+"&scope=record")
	after := heading(body, plain)
	if got := heading(saved, `<h2 id="h-fields" class="en-h" hx-swap-oob="true">`); got != after || after == before {
		t.Errorf("the save's Fields heading %q, a fresh one %q (before the save %q)", got, after, before)
	}
}

func TestLookupOverrideSetsThisBooksVersionAndLeavesTheRecordAlone(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "age", Label: "Age", Type: "text"})
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Fields: map[string]any{"age": "34"}})
	code, body, _ := e.postFrag(t, e.owner, "override", url.Values{"entity_id": {e.maire}, "name": {"Mairín"}, "summary": {"Older here."}, "notes": {"Book two."}, "f.age": {"41"}})
	if code != 200 || !strings.Contains(body, "last save wins") {
		t.Fatalf("override: %d %s", code, body)
	}
	rec := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if rec.Name != "Máire Nolan" || rec.Fields["age"] != "34" || rec.Summary != "A ferrywoman." {
		t.Errorf("the Compendium's record changed: %+v", rec)
	}
	pe := e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: e.maire}).(entities.ProjectItem).Item
	if pe.Name != "Mairín" || pe.Fields["age"] != "41" || !pe.Overridden || pe.Override == nil || pe.Override.Notes != "Book two." {
		t.Errorf("the book's version: %+v", pe)
	}
	// An empty box stops overriding that field; "blank" overrides it with nothing; the notes box alone leaves the name alone.
	e.postFrag(t, e.owner, "override", url.Values{"entity_id": {e.maire}, "f.age": {""}, "notes": {"Only notes."}})
	pe = e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: e.maire}).(entities.ProjectItem).Item
	if pe.Fields["age"] != "34" || pe.Name != "Mairín" || pe.Override.Notes != "Only notes." {
		t.Errorf("an emptied box: %+v", pe)
	}
	e.postFrag(t, e.owner, "override", url.Values{"entity_id": {e.maire}, "f.age": {""}, "blank.age": {"1"}})
	pe = e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: e.maire}).(entities.ProjectItem).Item
	if pe.Fields["age"] != "" {
		t.Errorf("a blanked field: %+v", pe.Fields)
	}
	code, body, _ = e.postFrag(t, e.owner, "override-clear", url.Values{"entity_id": {e.maire}})
	pe = e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: e.maire}).(entities.ProjectItem).Item
	if code != 200 || pe.Overridden || pe.Name != "Máire Nolan" || !strings.Contains(body, `id="lk-entry"`) {
		t.Errorf("clear: %d overridden=%v", code, pe.Overridden)
	}
}

func TestLookupAnotherNameAddedRemovedAndRefused(t *testing.T) {
	e := newLkEnv(t)
	code, body, hdr := e.postFrag(t, e.owner, "alias", url.Values{"entity_id": {e.maire}, "alias": {"Mrs Riviera"}})
	if code != 200 || !strings.Contains(body, "Mrs Riviera") || !strings.Contains(body, `id="lk-aka"`) || !strings.Contains(hdr.Get("HX-Trigger"), "lk:names") {
		t.Fatalf("add: %d %s (%v)", code, body, hdr)
	}
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if len(got.Aliases) != 1 {
		t.Fatalf("aliases: %+v", got.Aliases)
	}
	// The word another entry holds: 422, the answer names that entry and has a button that opens it, and nothing is added.
	code, body, hdr = e.postFrag(t, e.owner, "alias", url.Values{"entity_id": {e.maire}, "alias": {"salt  quay"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Salt Quay") || !strings.Contains(body, "entity="+e.quay) || hdr.Get("HX-Retarget") != "#lk-aka-status" {
		t.Errorf("collision: %d %s", code, body)
	}
	for name, word := range map[string]string{"too long": strings.Repeat("x", lkNameMax+1), "a line break": "two\nlines"} {
		if code, body, _ = e.postFrag(t, e.owner, "alias", url.Values{"entity_id": {e.maire}, "alias": {word}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "too long or runs over a line") {
			t.Errorf("%s: %d %s", name, code, body)
		}
	}
	if got = e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; len(got.Aliases) != 1 {
		t.Errorf("a refused name was added: %+v", got.Aliases)
	}
	// From a selection, the target button asks for the whole entry back.
	code, body, _ = e.postFrag(t, e.owner, "alias", url.Values{"entity_id": {e.quay}, "alias": {"the Slip"}, "then": {"entry"}})
	if code != 200 || !strings.Contains(body, `id="lk-entry"`) || !strings.Contains(body, "the Slip") {
		t.Errorf("add as another name for: %d %.300s", code, body)
	}
	code, _, _ = e.postFrag(t, e.owner, "unalias", url.Values{"entity_id": {e.maire}, "alias_id": {got.Aliases[0].ID}})
	if got = e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; code != 200 || len(got.Aliases) != 0 {
		t.Errorf("remove: %d %+v", code, got.Aliases)
	}
}

func TestLookupSelectionRanksLikelyEntriesByWord(t *testing.T) {
	e := newLkEnv(t)
	riviera := e.entity(t, "character", "Alexa Riviera", "A captain.")
	e.entity(t, "character", "Mrs Dalloway", "")
	both := e.entity(t, "character", "Riviera, Mrs of Quay", "")
	_, body := e.frag(e.owner, "?sel="+url.QueryEscape("Mrs Riviera"))
	if !strings.Contains(body, "The selected words") || !strings.Contains(body, "Mrs Riviera") {
		t.Fatalf("the selection: %.500s", body)
	}
	// "Riviera" finds Alexa Riviera and the entry that has both words, "Mrs" finds Mrs Dalloway and that one: it matched two words, so it is first.
	a, b, c := strings.Index(body, "Riviera, Mrs of Quay"), strings.Index(body, "Alexa Riviera"), strings.Index(body, "Mrs Dalloway")
	if a < 0 || b < 0 || c < 0 || a > b || a > c {
		t.Errorf("likely entries: %d %d %d\n%s", a, b, c, between(body, `id="lk-rows"`, "</ul>"))
	}
	_ = both
	if strings.Contains(body, "lk-already") {
		t.Errorf("an exact-match banner for words that are nobody's name")
	}
	e.call(t, "entity.alias.add", entities.AliasAddIn{ID: riviera, Alias: "Mrs Riviera"})
	if _, body = e.frag(e.owner, "?sel="+url.QueryEscape("mrs  riviera")); !strings.Contains(body, "Already a name of Alexa Riviera") {
		t.Errorf("the exact match: %s", between(body, "lk-sel", "</section>"))
	}
	// As targets: only the entries of the primary Compendium the caller edits are offered, the rest say why.
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "age", Label: "Age", Type: "text"})
	_, body = e.frag(e.owner, "?alias="+url.QueryEscape("Mrs Riviera"))
	if !strings.Contains(body, `class="lk-target"`) || !strings.Contains(body, "Add as another name") {
		t.Errorf("targets: %.600s", body)
	}
	_, ro := e.frag(e.peReader, "?alias="+url.QueryEscape("Mrs Riviera"))
	if strings.Contains(ro, `class="lk-target"`) || !strings.Contains(ro, "Only editors of its Compendium can give it another name") {
		t.Errorf("a reader is offered targets: %.600s", ro)
	}
}

func TestLookupNewEntry(t *testing.T) {
	e := newLkEnv(t)
	code, body := e.frag(e.owner, "?new="+url.QueryEscape("Captain Vell"))
	if code != 200 || !strings.Contains(body, `value="Captain Vell"`) || strings.Contains(body, "checked") {
		t.Errorf("the form: %d (a kind must not be chosen for the writer) %.500s", code, body)
	}
	code, body, _ = e.postFrag(t, e.owner, "new", url.Values{"name": {"Captain Vell"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Choose a kind first") || !strings.Contains(body, `value="Captain Vell"`) {
		t.Errorf("no kind: %d %.400s", code, body)
	}
	code, body, hdr := e.postFrag(t, e.owner, "new", url.Values{"name": {"  Captain   Vell "}, "kind": {"character"}, "summary": {"Keeps the lamp."}})
	if code != 200 || !strings.Contains(body, `id="lk-entry"`) || !strings.Contains(body, "Added to Salt Bible.") || !strings.Contains(hdr.Get("HX-Trigger"), "lk:names") {
		t.Fatalf("create: %d %.500s", code, body)
	}
	list := e.call(t, "entity.list", entities.ListIn{ID: e.cid, Name: "Captain Vell"}).(entities.ListOut)
	if len(list.Items) != 1 || list.Items[0].Summary != "Keeps the lamp." {
		t.Errorf("created in the primary Compendium: %+v", list.Items)
	}
	// A name that exists shows that entry, with a button, and keeps the typed words.
	code, body, _ = e.postFrag(t, e.owner, "new", url.Values{"name": {"salt quay"}, "kind": {"place"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "Open Salt Quay") || !strings.Contains(body, `value="salt quay"`) {
		t.Errorf("collision: %d %.500s", code, body)
	}
	// Not offered to a reader of the Compendium, and a refused request creates nothing.
	if code, body = e.frag(e.peReader, "?new=Nobody"); code != 200 || !strings.Contains(body, "lk-new-why") && strings.Contains(body, "lkn-name") {
		t.Errorf("the form for a reader of the Compendium: %d %.400s", code, body)
	}
	code, body, _ = e.postFrag(t, e.peReader, "new", url.Values{"name": {"Nobody"}, "kind": {"character"}})
	if code == 200 {
		t.Errorf("a reader of the Compendium created an entry: %d %s", code, body)
	}
	if l := e.call(t, "entity.list", entities.ListIn{ID: e.cid, Name: "Nobody"}).(entities.ListOut); len(l.Items) != 0 {
		t.Errorf("an entry was created for a reader: %+v", l.Items)
	}
	// An editor of the Compendium who only reads the book may create (the same person could on the Compendium's own page).
	if code, _, _ = e.postFrag(t, e.ceProjReader, "new", url.Values{"name": {"By the editor"}, "kind": {"item"}}); code != 200 {
		t.Errorf("an editor of the Compendium creating from a book they read: %d", code)
	}
	// A book with no primary Compendium does not offer it, and says to make one.
	bare := e.call(t, "project.create", projects.CreateIn{Title: "Bare"}).(projects.Item).Item
	rec := e.s.do(e.owner, "GET", "/ui/projects/"+bare.ID+"/lookup?new=Vell", "")
	if !strings.Contains(rec.Body.String(), "no Compendium to add to") {
		t.Errorf("no Compendium: %.400s", rec.Body)
	}
}

func TestLookupNoCompendiumAndMake(t *testing.T) {
	e := newLkEnv(t)
	bare := e.call(t, "project.create", projects.CreateIn{Title: "Bare book"}).(projects.Item).Item
	e.s.addMember(t, bare.ID, e.projectOnly.userID, ops.ProjectEditor)
	get := func(c caller) string { return e.s.do(c, "GET", "/ui/projects/"+bare.ID+"/lookup", "").Body.String() }
	if body := get(e.owner); !strings.Contains(body, "no Compendium yet") || !strings.Contains(body, "Make one for this book") {
		t.Errorf("the owner: %.500s", body)
	}
	if body := get(e.projectOnly); !strings.Contains(body, "no Compendium yet") || strings.Contains(body, "Make one for this book") || !strings.Contains(body, "Ask the owner") {
		t.Errorf("an editor of the book: %.500s", body)
	}
	if rec := e.s.post(t, e.projectOnly, "/ui/projects/"+bare.ID+"/lookup/make", nil, "", true); rec.Code == 200 {
		t.Errorf("an editor of the book made a Compendium: %d", rec.Code)
	}
	if w := e.call(t, "project.compendium.get", compendiums.ProjectIn{ID: bare.ID}).(compendiums.WiringItem).Item; w.HasPrimary {
		t.Fatalf("a refused make made one")
	}
	rec := e.s.post(t, e.owner, "/ui/projects/"+bare.ID+"/lookup/make", nil, "", true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="lk-q"`) {
		t.Errorf("make: %d %.300s", rec.Code, rec.Body)
	}
	if w := e.call(t, "project.compendium.get", compendiums.ProjectIn{ID: bare.ID}).(compendiums.WiringItem).Item; !w.HasPrimary || w.Primary == nil || w.Primary.Name != "Bare book: Compendium" {
		t.Errorf("the book's Compendium: %+v", w)
	}
	// An owner's second make is refused: the book has one now.
	if rec := e.s.post(t, e.owner, "/ui/projects/"+bare.ID+"/lookup/make", nil, "", true); rec.Code == 200 {
		t.Errorf("a second Compendium was made for the book: %d", rec.Code)
	}
}

// The state matrix of the plan, one row for each kind of person: what the entry shows and what it offers.
func TestLookupStateMatrix(t *testing.T) {
	e := newLkEnv(t)
	type row struct {
		who           caller
		name          string
		form, summary bool // the record's form, and a summary box in the header
		book          bool // the scope switch needs both; a book form alone is the "book" scope
		reasons       []string
		absent        []string
	}
	rows := []row{
		{who: e.owner, name: "owner", form: true, summary: true, book: true},
		{who: e.peReader, name: "editor of the book, reader of the Compendium", book: true, reasons: []string{"only its editors can change it"}},
		{who: e.reviewer, name: "reviewer of the book, reader of the Compendium", reasons: []string{"only its editors can change it", "Only editors of this book can set its version"}},
		{who: e.ceProjReader, name: "editor of the Compendium, reader of the book", form: true, summary: true},
	}
	for _, r := range rows {
		code, body := e.frag(r.who, "?entity="+e.maire)
		if code != 200 {
			t.Errorf("%s: %d", r.name, code)
			continue
		}
		hasRecord := strings.Contains(body, `hx-post="`+e.base+`/save"`)
		hasBook := strings.Contains(body, `hx-post="`+e.base+`/override"`)
		switch {
		case r.form && !hasRecord:
			t.Errorf("%s: no record form", r.name)
		case !r.form && hasRecord:
			t.Errorf("%s: a record form for a person who may not edit the record", r.name)
		}
		if r.book && !r.form && !hasBook {
			t.Errorf("%s: no form for this book's version", r.name)
		}
		if !r.book && hasBook {
			t.Errorf("%s: a form for this book's version for a person who may not set it", r.name)
		}
		for _, s := range r.reasons {
			if !strings.Contains(body, s) {
				t.Errorf("%s: the reason %q is not said: %.400s", r.name, s, between(body, `id="lk-why"`, "</p>"))
			}
		}
		if !r.form && !r.book && (strings.Contains(body, `id="es-body"`) || strings.Contains(body, `id="es-summary"`) || strings.Contains(body, "lk-addname")) {
			t.Errorf("%s: an input for a person who can change nothing", r.name)
		}
		if r.form && r.name == "owner" && !strings.Contains(body, "lk-scope") {
			t.Errorf("%s: no switch between the Compendium and this book", r.name)
		}
	}
	// An entry reached by reference lives elsewhere: the record is text, with the Compendium named, and the book's version is open.
	ref := e.call(t, "compendium.create", compendiums.CreateIn{Name: "Shared lore"}).(compendiums.Item).Item
	le := e.call(t, "entity.create", entities.CreateIn{ID: ref.ID, Kind: "item", Name: "The brass lantern"}).(entities.Item).Item.ID
	e.call(t, "project.compendium.ref.add", compendiums.RefAddIn{ID: e.pid, CompendiumID: ref.ID})
	_, body := e.frag(e.owner, "?entity="+le)
	if !strings.Contains(body, "lives in Shared lore") || strings.Contains(body, `hx-post="`+e.base+`/save"`) || !strings.Contains(body, `hx-post="`+e.base+`/override"`) {
		t.Errorf("a referenced entry: %.500s", between(body, `id="lk-entry"`, "</header>"))
	}
	// Writing to it through the panel is refused, and says where it lives.
	if code, body, _ := e.postFrag(t, e.owner, "save", url.Values{"entity_id": {le}, "summary": {"changed"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "lives in Shared lore") {
		t.Errorf("a save of a referenced entry: %d %s", code, body)
	}
	if code, body, _ := e.postFrag(t, e.owner, "alias", url.Values{"entity_id": {le}, "alias": {"The lamp"}}); code != http.StatusUnprocessableEntity || !strings.Contains(body, "lives in Shared lore") {
		t.Errorf("another name for a referenced entry: %d %s", code, body)
	}
	if got := e.call(t, "entity.get", entities.GetIn{ID: le}).(entities.DetailItem).Item; got.Summary != "" || len(got.Aliases) != 0 {
		t.Errorf("a referenced entry was written through the book: %+v", got)
	}
	// A Compendium in the trash is not read through the book at all: the list says why it is empty, and every write is refused.
	e.call(t, "compendium.trash", compendiums.StateIn{ID: e.cid})
	if _, lb := e.frag(e.owner, ""); !strings.Contains(lb, "in the trash") || strings.Contains(lb, "Máire Nolan") {
		t.Errorf("a trashed Compendium: %.600s", between(lb, `class="lk-list"`, "</form>"))
	}
	if code, _ := e.frag(e.owner, "?entity="+e.maire); code == 200 {
		t.Errorf("an entry of a trashed Compendium was shown: %d", code)
	}
	for _, act := range []string{"save", "override", "alias"} {
		if code, _, _ := e.postFrag(t, e.owner, act, url.Values{"entity_id": {e.maire}, "summary": {"into the trash"}, "alias": {"Trash"}}); code == 200 {
			t.Errorf("%s into a Compendium in the trash: %d", act, code)
		}
	}
	if got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; got.Summary == "into the trash" {
		t.Errorf("a trashed Compendium was written: %+v", got)
	}
}

func TestLookupFragmentsNeedASignedInPerson(t *testing.T) {
	e := newLkEnv(t)
	for _, q := range []string{"", "?entity=" + e.maire, "?new=x", "?sel=x"} {
		rec := e.s.do(caller{name: "anon"}, "GET", e.base+q, "")
		if rec.Code != http.StatusSeeOther && rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %q: %d", q, rec.Code)
		}
	}
	// A request from the panel with a lost session is answered with JSON and the real status, never the sign-in page.
	req := e.s.post(t, caller{name: "anon"}, e.base+"/save", url.Values{"entity_id": {e.maire}, "summary": {"x"}}, "-", true)
	if req.Code != http.StatusUnauthorized || !strings.Contains(req.Header().Get("Content-Type"), "json") || strings.Contains(strings.ToLower(req.Body.String()), "<html") {
		t.Errorf("a lost session: %d %s %.200s", req.Code, req.Header().Get("Content-Type"), req.Body)
	}
	// A stale token: 403 as JSON, and nothing changed.
	rec := e.s.post(t, e.owner, e.base+"/save", url.Values{"entity_id": {e.maire}, "summary": {"x"}}, "wrong", true)
	if !strings.Contains(rec.Body.String(), `"reason":"csrf"`) {
		t.Errorf("a stale token says why, so the panel can ask for a new one: %s", rec.Body)
	}
	// A person who may not is refused in the server's words, with no such reason.
	if r2 := e.s.post(t, e.reviewer, e.base+"/alias", url.Values{"entity_id": {e.maire}, "alias": {"Nope"}}, "", true); r2.Code == 200 || strings.Contains(r2.Body.String(), `"reason":"csrf"`) {
		t.Errorf("a refusal for a role is not a stale token: %d %s", r2.Code, r2.Body)
	}
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Header().Get("Content-Type"), "json") || strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
		t.Errorf("a stale token: %d %s %.200s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; got.Summary != "A ferrywoman." {
		t.Errorf("a refused request changed the entity: %+v", got)
	}
}

// A save that names the words it started from (o.<key>) goes through over a newer revision when none of the fields it changes has been
// changed by anyone else, and is a conflict, naming those fields, when one has. Keep mine (force) saves over them.
func TestLookupSaveMergesWhatNobodyElseChanged(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "entity.field.set", entities.FieldSetIn{ID: e.cid, Kind: "character", Key: "appearance", Label: "Appearance", Type: "text"})
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Body: strp("The description.")})
	base := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	// Someone else changes the appearance and the description.
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Body: strp("Their description."), Fields: map[string]any{"appearance": "Their appearance"}})
	form := func(extra url.Values) url.Values {
		v := url.Values{"entity_id": {e.maire}, "base_revision": {strconv.Itoa(base.Revision)}, "o.summary": {lkHash(base.Summary)}}
		for k, x := range extra {
			v[k] = x
		}
		return v
	}
	code, body, _ := e.postFrag(t, e.owner, "save", form(url.Values{"summary": {"My summary."}}))
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if code != 200 || got.Summary != "My summary." || got.Body != "Their description." || got.Fields["appearance"] != "Their appearance" {
		t.Fatalf("a save of the summary over a newer revision: %d %s | %+v", code, body, got)
	}
	if !strings.Contains(body, `name="o.summary" value="`+lkHash("My summary.")+`"`) {
		t.Errorf("the answer gives the new starting words of the summary: %s", body)
	}
	// The same field changed by someone else: a conflict that names it, and nothing is saved.
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Summary: strp("Their summary.")})
	code, body, _ = e.postFrag(t, e.owner, "save", form(url.Values{"summary": {"Mine again."}, "o.summary": {lkHash("My summary.")}}))
	if code != http.StatusUnprocessableEntity || !strings.Contains(body, "lk-conflict") || !strings.Contains(body, "Their summary.") || strings.Contains(body, "Their description.") {
		t.Errorf("a conflict names the field that differs, and only that: %d %s", code, body)
	}
	if g := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; g.Summary != "Their summary." {
		t.Errorf("a refused save changed the entry: %+v", g)
	}
	// A save still on its way may have landed first: its words are acceptable starting words too, so the last words after it are not a conflict.
	code, _, _ = e.postFrag(t, e.owner, "save", form(url.Values{"summary": {"Mine again, and more."}, "o.summary": {lkHash("A very old summary") + "," + lkHash("Their summary.")}}))
	if g := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; code != 200 || g.Summary != "Mine again, and more." {
		t.Errorf("starting words of a save in flight: %d %+v", code, g)
	}
	// Keep mine: over a field someone else changed, on purpose; the other fields stay theirs.
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Summary: strp("Theirs once more.")})
	code, _, _ = e.postFrag(t, e.owner, "save", form(url.Values{"summary": {"Mine to keep."}, "o.summary": {lkHash("nothing like it")}, "force": {"1"}}))
	g := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if code != 200 || g.Summary != "Mine to keep." || g.Body != "Their description." || g.Fields["appearance"] != "Their appearance" {
		t.Errorf("Keep mine: %d %+v", code, g)
	}
}

// On the full page, a field the writer did not touch is never written back: Keep mine after a refusal leaves what others changed.
func TestEntityPageSaveKeepsWhatWasNotTouched(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Body: strp("The original description.")})
	cur := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	e.call(t, "entity.update", entities.UpdateIn{ID: e.maire, Body: strp("Changed by someone else.")})
	orig := func() url.Values {
		o := url.Values{}
		for _, k := range []string{"name", "sort_name", "summary", "body", "color"} {
			o.Set("o."+k, lkHash(entityValue(cur.Entity, k)))
		}
		return o
	}
	form := func(rev int) url.Values {
		v := orig()
		v.Set("name", cur.Name)
		v.Set("sort_name", "")
		v.Set("summary", "My summary.")
		v.Set("body", "The original description.") // untouched
		v.Set("color", "")
		v.Set("base_revision", strconv.Itoa(rev))
		return v
	}
	path := "/ui/entities/" + e.maire + "/save"
	if rec := e.s.post(t, e.owner, path, form(cur.Revision), "", false); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a stale save: %d", rec.Code)
	} else if !strings.Contains(rec.Body.String(), `name="o.body" value="`+lkHash("The original description.")+`"`) {
		t.Errorf("the page that comes back keeps the words the form began with")
	}
	now := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if rec := e.s.post(t, e.owner, path, form(now.Revision), "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("Keep mine: %d", rec.Code)
	}
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Summary != "My summary." || got.Body != "Changed by someone else." {
		t.Errorf("Keep mine wrote back a field that was not touched: %+v", got)
	}
}

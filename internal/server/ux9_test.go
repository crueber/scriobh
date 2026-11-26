package server

import (
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/entities"
)

// tagOf is the start tag of the element with this id, as the page wrote it ("" when it is not there).
func tagOf(body, id string) string {
	i := strings.Index(body, `id="`+id+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<")
	end := strings.Index(body[i:], ">")
	if start < 0 || end < 0 {
		return ""
	}
	return body[start : i+end+1]
}

// The first name of a person is offered as another name, one click away, and is never matched by itself (U9.2).
func TestFirstNameIsOfferedAsAnAliasNeverMatched(t *testing.T) {
	e := newLkEnv(t)
	ines := e.entity(t, "character", "Ines Ferreira", "")
	_, body := e.frag(e.owner, "?entity="+ines)
	if !strings.Contains(body, "en-offer") || !strings.Contains(body, `value="Ines"`) {
		t.Fatalf("no offer for a person with a two-word name: %.500s", body)
	}
	if n := len(e.call(t, "entity.get", entities.GetIn{ID: ines}).(entities.DetailItem).Item.Aliases); n != 0 {
		t.Errorf("the offer added a name by itself: %d aliases", n)
	}
	// Places, organisations, one-word names and names whose first word is already a name are not offered.
	salt := e.entity(t, "place", "Salt Road Inn", "")
	guild := e.entity(t, "org", "The Guild of Lamplighters", "")
	one := e.entity(t, "character", "Elara", "")
	// A title or an article is not a first name: nothing is offered for it.
	mrs := e.entity(t, "character", "Mrs Nolan Byrne", "")
	dr := e.entity(t, "character", "Dr Ines Ferreira", "")
	dara := e.call(t, "entity.create", entities.CreateIn{ID: e.cid, Kind: "character", Name: "Dara Quill", Aliases: []string{"dara"}}).(entities.Item).Item.ID
	for _, c := range []struct{ id, why string }{{salt, "a place"}, {guild, "an organisation"}, {one, "a one-word name"}, {dara, "a first word already an alias"}, {mrs, "a title"}, {dr, "a title"}} {
		if _, b := e.frag(e.owner, "?entity="+c.id); strings.Contains(b, "en-offer") {
			t.Errorf("an offer for %s", c.why)
		}
	}
	// Adding it is one request, through the alias route the names already use.
	code, body, _ := e.postFrag(t, e.owner, "alias", url.Values{"entity_id": {ines}, "alias": {"Ines"}, "scope": {"record"}})
	if code != 200 {
		t.Fatalf("adding the offered name: %d %.300s", code, body)
	}
	if got := e.call(t, "entity.get", entities.GetIn{ID: ines}).(entities.DetailItem).Item.Aliases; len(got) != 1 || got[0].Alias != "Ines" {
		t.Errorf("the offered name was not added: %+v", got)
	}
	if _, b := e.frag(e.owner, "?entity="+ines); strings.Contains(b, "en-offer") {
		t.Errorf("the offer stays after the name is added")
	}
}

// An entry made from the sidebar leaves focus in the sidebar, on its first empty field (U9.4).
func TestAnEntryMadeFromTheSidebarFocusesItsFirstEmptyField(t *testing.T) {
	e := newLkEnv(t)
	code, body, _ := e.postFrag(t, e.owner, "new", url.Values{"name": {"Lark Alder"}, "kind": {"character"}})
	if code != 200 || !strings.Contains(body, "Lark Alder") {
		t.Fatalf("create: %d %.300s", code, body)
	}
	if !strings.Contains(tagOf(body, "es-summary"), "autofocus") {
		t.Errorf("the empty summary is not the focus of the new entry: %s", tagOf(body, "es-summary"))
	}
	id := e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: "Lark Alder"}).(entities.ProjectItem).Item.ID
	if _, b := e.frag(e.owner, "?entity="+id); strings.Contains(b, "autofocus") {
		t.Errorf("an entry opened later takes the focus: it must only be given to the entry just made")
	}
}

// "Save and add another" on the Entities page keeps the form open with its name box ready (U9.7).
func TestSaveAndAddAnotherKeepsTheEntityFormReady(t *testing.T) {
	e := newEntEnv(t)
	form := url.Values{"name": {"Tamsin Vale"}, "kind": {"character"}, "again": {"1"}}
	rec := e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/entities", form, "", false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/"+e.slug+"/entities?saved=again" {
		t.Fatalf("save and add another: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	delete(form, "again")
	form.Set("name", "Tamsin Rowe")
	rec = e.s.post(t, e.owner, "/ui/compendiums/"+e.cid+"/entities", form, "", false)
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/c/"+e.slug+"/e/") {
		t.Errorf("the plain add still opens the new entity: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	code, body := e.get(e.owner, "/c/"+e.slug+"/entities?saved=again")
	// The saved line is in the add form, beside the name box it is about, and announced; the name box takes the focus.
	addAt := strings.Index(body, `id="add"`)
	if savedAt := strings.Index(body, `class="saved"`); savedAt < addAt || savedAt < 0 || strings.Count(body, "The entity is added") != 1 || !strings.Contains(body[strings.Index(body, `class="saved"`)-40:strings.Index(body, `class="saved"`)+200], `role="status"`) {
		t.Errorf("the saved line is not in the add form, announced: add at %d, saved at %d", addAt, strings.Index(body, `class="saved"`))
	}
	if code != 200 || !strings.Contains(body, "Save and add another") || !strings.Contains(tagOf(body, "ea-name"), "autofocus") {
		t.Errorf("the add form after save and add another: %d, name box %s", code, tagOf(body, "ea-name"))
	}
	if _, b := e.get(e.owner, "/c/"+e.slug+"/entities"); strings.Contains(tagOf(b, "ea-name"), "autofocus") {
		t.Errorf("the name box takes the focus on an ordinary visit")
	}
}

// A date the writer typed in the timeline is explained beside its field, and years under a thousand may be written without padding (U9.5).
func TestTimelineDateErrorsSitBesideTheirField(t *testing.T) {
	e := newTLEnv(t)
	rec := e.s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", url.Values{"title": {"Storm"}, "story_date": {"1998-02-30"}}, "", false)
	body := rec.Body.String()
	at := func(id string) int { return strings.Index(body, `id="`+id+`"`) }
	if rec.Code != http.StatusUnprocessableEntity || at("ev-start-h") < at("ev-start") || at("ev-start") < 0 || at("ev-title") > at("ev-start") {
		t.Errorf("the event's date error is not beside its field: %d, error at %d, field at %d", rec.Code, at("ev-start-h"), at("ev-start"))
	}
	if strings.Contains(body, `id="tl-err"`) {
		t.Errorf("a date error is drawn at the top of the form")
	}
	// One message per bad date: the hint under the field is the message, and no second one is drawn.
	if strings.Count(body, "Use a date such as") != 1 || strings.Contains(body, `id="ev-start-err"`) || !strings.Contains(tagOf(body, "ev-start-h"), "is-bad") {
		t.Errorf("the date says itself twice, or its hint is not the message")
	}
	if !strings.Contains(tagOf(body, "ev-start"), `aria-describedby="ev-start-h"`) {
		t.Errorf("the date field does not point at its error: %s", tagOf(body, "ev-start"))
	}
	code, bad := e.get(t, e.owner, "/c/"+e.cslug+"/timeline?from=1998-4")
	if code != http.StatusUnprocessableEntity || strings.Index(bad, `id="f-from-err"`) < strings.Index(bad, `id="f-from"`) || strings.Contains(bad, `id="tl-err"`) {
		t.Errorf("the filter's date error is not beside its field: %d", code)
	}
	// 687 is a year: written without padding, it is kept as 0687.
	rec = e.s.post(t, e.editor, "/ui/compendiums/"+e.cid+"/timeline/events", url.Values{"title": {"Old storm"}, "story_date": {"687-09-14"}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("a year under a thousand without padding: %d %s", rec.Code, visibleText(rec.Body.String()))
	}
}

// The sidebar's view of the Timeline is called Timeline, never "This scene" (U9.3).
func TestSidebarTimelineViewIsCalledTimeline(t *testing.T) {
	e := newLkEnv(t)
	_, body := e.scene(e.owner, e.doc(t, "Arrival"), "")
	if !strings.Contains(body, ">Timeline<") || strings.Contains(body, "This scene") {
		t.Errorf("the sidebar's Timeline view is not named Timeline: %.300s", body)
	}
}

// The project's tab for the Compendium is called Compendium, not Bible (U9.3).
func TestProjectTabIsCalledCompendium(t *testing.T) {
	e := newLkEnv(t)
	code, body := e.get(e.owner, "/p/"+e.pslug+"/outline")
	if code != 200 || !strings.Contains(body, `href="/p/`+e.pslug+`/compendium/open"`) || strings.Contains(body, ">Bible<") || !strings.Contains(body, ">Compendium<") {
		t.Errorf("the project's Compendium tab: %d", code)
	}
}

// The Compendium tab opens the entries: the first thing a writer sees of a Compendium is what is in it (U9.8).
func TestCompendiumTabOpensItsEntries(t *testing.T) {
	e := newEntEnv(t)
	rec := e.s.do(e.owner, "GET", "/p/"+e.pslug+"/compendium/open", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/c/"+e.slug+"/entities" {
		t.Errorf("the Compendium tab: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// The entry's squares that are not coloured are not boxes: they read as a mark, never as a checkbox (U9.6).
func TestUncolouredEntrySwatchIsNotABox(t *testing.T) {
	css, err := fs.ReadFile(staticFS(t), "css/entry.css")
	if err != nil {
		t.Fatal(err)
	}
	rule := regexp.MustCompile(`\.ent-sw-none\s*\{([^}]*)\}`).FindStringSubmatch(string(css))
	if rule == nil {
		t.Fatal("no rule for .ent-sw-none")
	}
	if strings.Contains(rule[1], "border:") {
		t.Errorf(".ent-sw-none is drawn as a box: %s", rule[1])
	}
}

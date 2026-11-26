package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/ops"
)

// The Compendium tab names nothing to a person who is not on the Compendium, answers a real id they cannot read exactly as it answers
// one that does not exist, and writes only what the ops allow. Run with: go test ./internal/server -run Authz

const missingEntity = "ent_00000000000000000000000000"

// fetchGet is a GET as the panel's script sends it.
func (e lkEnv) fetchGet(c caller, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set(fetchHeader, "1")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

// everyWrite is every POST action of the panel for an entity id.
func everyWrite(id string) map[string]url.Values {
	return map[string]url.Values{
		"save":           {"entity_id": {id}, "summary": {"x"}, "base_revision": {"1"}},
		"override":       {"entity_id": {id}, "summary": {"x"}},
		"override-clear": {"entity_id": {id}},
		"alias":          {"entity_id": {id}, "alias": {"Sneaky"}},
		"unalias":        {"entity_id": {id}, "alias_id": {"ali_000"}},
	}
}

func TestAuthzSidebarNamesNothingToAPersonNotOnTheCompendium(t *testing.T) {
	e := newLkEnv(t)
	who := e.projectOnly // an editor of the book, and not on its Compendium
	secrets := []string{"Máire", "Maire", "Salt Quay", "Salt Bible", "Nolan", "ferrywoman", "road meets the water", "2 entries", "entries"}
	for _, q := range []string{"", "?q=Maire", "?kind=place", "?sel=Maire+Nolan", "?alias=Salt+Quay", "?new=Maire", "?part=rows&q=Salt", "?part=more&offset=30"} {
		rec := e.fetchGet(who, e.base+q)
		body := rec.Body.String()
		if rec.Code != 200 && rec.Code != 404 {
			t.Errorf("%q: %d", q, rec.Code)
		}
		for _, s := range secrets {
			if strings.Contains(body, s) && !(strings.Contains(q, "sel=") && s == "Maire") && !strings.Contains(q, "q="+s) {
				t.Errorf("%q names %q to a person who is not on the Compendium: %.300s", q, s, body)
			}
		}
	}
	if body := e.fetchGet(who, e.base).Body.String(); !strings.Contains(body, "not shared with you") {
		t.Errorf("the closed state: %.300s", body)
	}
	// A real id they cannot read answers exactly as an id that does not exist, in every way the panel can ask.
	for _, q := range []string{"?entity=%s", "?entity=%s&scope=book", "?entity=%s&scope=record", "?entity=%s&fresh=1"} {
		real, fake := e.fetchGet(who, e.base+strings.Replace(q, "%s", e.maire, 1)), e.fetchGet(who, e.base+strings.Replace(q, "%s", missingEntity, 1))
		if real.Code != fake.Code || real.Body.String() != fake.Body.String() {
			t.Errorf("%s: a real entry answers %d %q, a missing one %d %q", q, real.Code, real.Body.String(), fake.Code, fake.Body.String())
		}
		if real.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", q, real.Code)
		}
	}
	for action, form := range everyWrite(e.maire) {
		fake := everyWrite(missingEntity)[action]
		r1 := e.s.post(t, who, e.base+"/"+action, form, "", true)
		r2 := e.s.post(t, who, e.base+"/"+action, fake, "", true)
		if r1.Code != r2.Code || r1.Body.String() != r2.Body.String() {
			t.Errorf("%s: a real entry answers %d %q, a missing one %d %q", action, r1.Code, r1.Body.String(), r2.Code, r2.Body.String())
		}
		if r1.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", action, r1.Code)
		}
	}
	// New and make: refused, and nothing is created.
	if rec := e.s.post(t, who, e.base+"/new", url.Values{"name": {"Intruder"}, "kind": {"character"}}, "", true); rec.Code == 200 {
		t.Errorf("new by a person not on the Compendium: %d", rec.Code)
	}
	if rec := e.s.post(t, who, e.base+"/make", nil, "", true); rec.Code == 200 {
		t.Errorf("make by an editor of the book: %d", rec.Code)
	}
	if l := e.call(t, "entity.list", entities.ListIn{ID: e.cid, Name: "Intruder"}).(entities.ListOut); len(l.Items) != 0 {
		t.Errorf("an entry was created: %+v", l.Items)
	}
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	if got.Summary != "A ferrywoman." || len(got.Aliases) != 0 {
		t.Errorf("a refused request changed the entity: %+v", got)
	}
	if pe := e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: e.maire}).(entities.ProjectItem).Item; pe.Overridden {
		t.Errorf("a refused request set this book's version")
	}
	// An instance administrator who is not on the Compendium is a stranger to it, here as on its own pages.
	if body := e.fetchGet(e.admin, e.base+"?entity="+e.maire).Body.String(); strings.Contains(body, "Máire") {
		t.Errorf("an administrator who is not a member is shown the entry: %.200s", body)
	}
}

func TestAuthzSidebarAnEntryOfAnotherBooksCompendiumIsAsMissingAsAnyOther(t *testing.T) {
	e := newLkEnv(t)
	// The editor is on another Compendium (which this book does not use) and on this book: the entity is theirs to read elsewhere, and
	// not through this book.
	other := e.call(t, "compendium.create", compendiums.CreateIn{Name: "Elsewhere bible"}).(compendiums.Item).Item
	oe := e.call(t, "entity.create", entities.CreateIn{ID: other.ID, Kind: "character", Name: "Zelda Elsewhere"}).(entities.Item).Item.ID
	e.s.addCompendiumMember(t, other.ID, e.peReader.userID, ops.ProjectEditor)
	for _, c := range []caller{e.owner, e.peReader} {
		real, fake := e.fetchGet(c, e.base+"?entity="+oe), e.fetchGet(c, e.base+"?entity="+missingEntity)
		if real.Code != http.StatusNotFound || real.Code != fake.Code || real.Body.String() != fake.Body.String() {
			t.Errorf("%s: %d %q against %d %q", c.name, real.Code, real.Body.String(), fake.Code, fake.Body.String())
		}
		for action, form := range everyWrite(oe) {
			r1 := e.s.post(t, c, e.base+"/"+action, form, "", true)
			r2 := e.s.post(t, c, e.base+"/"+action, everyWrite(missingEntity)[action], "", true)
			if r1.Code != http.StatusNotFound || r1.Code != r2.Code || r1.Body.String() != r2.Body.String() {
				t.Errorf("%s %s: %d %q against %d %q", c.name, action, r1.Code, r1.Body.String(), r2.Code, r2.Body.String())
			}
		}
	}
	// Even an editor of both Compendiums cannot write to the other one's entry through this book.
	if rec := e.s.post(t, e.owner, e.base+"/save", url.Values{"entity_id": {oe}, "summary": {"through the wrong book"}}, "", true); rec.Code == 200 {
		t.Errorf("a write to another Compendium's entry through this book: %d", rec.Code)
	}
	if got := e.call(t, "entity.get", entities.GetIn{ID: oe}).(entities.DetailItem).Item; got.Summary != "" {
		t.Errorf("another Compendium's entry was written through this book: %+v", got)
	}
	// The panel names no other Compendium and no other book.
	e.call(t, "project.create", projects.CreateIn{Title: "Elsewhere Secret Book"})
	for _, q := range []string{"", "?entity=" + e.maire, "?new=x", "?sel=Quay+person"} {
		if body := e.fetchGet(e.owner, e.base+q).Body.String(); strings.Contains(body, "Elsewhere") || strings.Contains(body, "Zelda") {
			t.Errorf("%q names something of another Compendium or book: %.200s", q, body)
		}
	}
}

func TestAuthzSidebarRolesDecideWhatMayBeWritten(t *testing.T) {
	e := newLkEnv(t)
	// A reviewer of the book (and a reader of the Compendium): nothing at all.
	for action, form := range everyWrite(e.maire) {
		if rec := e.s.post(t, e.reviewer, e.base+"/"+action, form, "", true); rec.Code == 200 {
			t.Errorf("%s by a reviewer: %d", action, rec.Code)
		}
	}
	if rec := e.s.post(t, e.reviewer, e.base+"/new", url.Values{"name": {"Rev"}, "kind": {"item"}}, "", true); rec.Code == 200 {
		t.Errorf("new by a reviewer: %d", rec.Code)
	}
	// An editor of the book who reads the Compendium: this book's version, and nothing of the Compendium's own.
	for _, action := range []string{"save", "alias", "unalias"} {
		if rec := e.s.post(t, e.peReader, e.base+"/"+action, everyWrite(e.maire)[action], "", true); rec.Code == 200 {
			t.Errorf("%s by a reader of the Compendium: %d", action, rec.Code)
		}
	}
	if rec := e.s.post(t, e.peReader, e.base+"/override", url.Values{"entity_id": {e.maire}, "summary": {"for this book"}}, "", true); rec.Code != 200 {
		t.Errorf("this book's version by an editor of the book: %d", rec.Code)
	}
	// An editor of the Compendium who only reads the book: the record, and not this book's version.
	if rec := e.s.post(t, e.ceProjReader, e.base+"/override", url.Values{"entity_id": {e.maire}, "summary": {"nope"}}, "", true); rec.Code == 200 {
		t.Errorf("this book's version by a reader of the book: %d", rec.Code)
	}
	if rec := e.s.post(t, e.ceProjReader, e.base+"/override-clear", url.Values{"entity_id": {e.maire}}, "", true); rec.Code == 200 {
		t.Errorf("clearing this book's version by a reader of the book: %d", rec.Code)
	}
	if rec := e.s.post(t, e.ceProjReader, e.base+"/save", url.Values{"entity_id": {e.maire}, "summary": {"by the Compendium's editor"}}, "", true); rec.Code != 200 {
		t.Errorf("the record by an editor of the Compendium: %d", rec.Code)
	}
	got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item
	pe := e.call(t, "project.entity.get", entities.ProjectGetIn{ID: e.pid, Entity: e.maire}).(entities.ProjectItem).Item
	if got.Summary != "by the Compendium's editor" || len(got.Aliases) != 0 || pe.Override == nil || pe.Override.Summary == nil || *pe.Override.Summary != "for this book" {
		t.Errorf("what the roles allowed: %+v | %+v", got, pe.Override)
	}
}

func TestAuthzSidebarATrashedBookAnswers404(t *testing.T) {
	e := newLkEnv(t)
	e.call(t, "project.trash", projects.StateIn{ID: e.pid})
	for _, c := range []caller{e.owner, e.peReader} {
		for _, q := range []string{"", "?entity=" + e.maire, "?new=x"} {
			if rec := e.fetchGet(c, e.base+q); rec.Code != http.StatusNotFound {
				t.Errorf("%s %q: %d", c.name, q, rec.Code)
			}
		}
		for action, form := range everyWrite(e.maire) {
			if rec := e.s.post(t, c, e.base+"/"+action, form, "", true); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s: %d", c.name, action, rec.Code)
			}
		}
	}
	if got := e.call(t, "entity.get", entities.GetIn{ID: e.maire}).(entities.DetailItem).Item; got.Summary != "A ferrywoman." {
		t.Errorf("a trashed book's panel wrote: %+v", got)
	}
}

// A book with a passphrase: until it is unlocked nobody is shown a word of it through the panel, and once it is, the same person is.
func TestAuthzSidebarAProtectedBookShowsNothingUntilItIsUnlocked(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	base := "/ui/projects/" + w.pid + "/lookup"
	notes, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"}, "project.research.list", research.ListIn{ID: w.pid})
	if err != nil || len(notes.(research.ListOut).Items) == 0 {
		t.Fatalf("the gate world's note: %v", err)
	}
	nid := notes.(research.ListOut).Items[0].ID
	paths := []string{base, base + "?entity=" + w.entID, base + "?entity=" + w.entID + "&scope=book", base + "?sel=Hero", base + "?new=Hero", base + "?part=rows&q=Hero",
		// the events this scene tells and the notes (F-1262, F-1263)
		base + "?view=events&doc=" + w.docID, base + "?view=events&doc=" + w.docID + "&part=hits&eq=crossing", base + "?view=notes", base + "?view=notes&note=" + nid,
		base + "?view=notes&new=1", base + "?view=notes&part=nrows&nq=Zq"}
	for _, c := range []caller{w.editor, w.owner2} {
		for _, p := range paths {
			rec := s.do(c, "GET", p, "")
			if !(rec.Code == http.StatusSeeOther && strings.HasPrefix(rec.Header().Get("Location"), "/p/"+w.slug+"/unlock")) && !(rec.Code == http.StatusForbidden && lockedBody(rec.Body.String())) {
				t.Errorf("%s %s while locked: %d %.200s", c.name, p, rec.Code, rec.Body)
			}
			if l := w.leaks(rec.Body.String()); len(l) > 0 || strings.Contains(rec.Body.String(), "Heroine") {
				t.Errorf("%s %s while locked leaks %v", c.name, p, l)
			}
		}
		writes := everyWrite(w.entID)
		for _, a := range []string{"event-link", "event-unlink", "event-new", "note-new", "note-save"} {
			writes[a] = url.Values{"doc": {w.docID}, "note_id": {nid}, "base_revision": {"1"}, "event_id": {"tle_00000000000000000000000001"}, "title": {"Zqmine"}, "body": {"x"}}
		}
		for action, form := range writes {
			rec := s.post(t, c, base+"/"+action, form, "", true)
			if rec.Code < 300 || strings.Contains(rec.Body.String(), "Heroine") || len(w.leaks(rec.Body.String())) > 0 {
				t.Errorf("%s %s while locked: %d %.200s", c.name, action, rec.Code, rec.Body)
			}
		}
	}
	// nothing was written by the posts above: the book has the one note it began with, and its chapter tells the one event
	if after, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"}, "project.research.list", research.ListIn{ID: w.pid}); err != nil || len(after.(research.ListOut).Items) != len(notes.(research.ListOut).Items) {
		t.Errorf("a locked book's panel wrote a note: %v", err)
	} else if after.(research.ListOut).Items[0].Title == "Zqmine" {
		t.Errorf("a locked book's note was saved through the panel")
	}
	if ev := s.do(w.owner, "GET", "/api/v1/compendiums/"+w.compID+"/timeline/events", ""); !strings.Contains(ev.Body.String(), "The crossing") || strings.Contains(ev.Body.String(), "Zqmine") {
		t.Errorf("a locked book's panel added an event: %.300s", ev.Body)
	}
	// the control: the events and the notes are there for the session that has unlocked it
	if body := s.do(w.owner, "GET", base+"?view=events&doc="+w.docID, "").Body.String(); !strings.Contains(body, "The crossing") {
		t.Errorf("the control, the events: %.300s", body)
	}
	if body := s.do(w.owner, "GET", base+"?view=notes", "").Body.String(); !strings.Contains(body, "Zqnote") {
		t.Errorf("the control, the notes: %.300s", body)
	}
	// the control: the session that has unlocked it reads the entry, and this book's own version is in it
	if body := s.do(w.owner, "GET", base, "").Body.String(); !strings.Contains(body, "Heroine") {
		t.Errorf("the control, the list: %.300s", body)
	}
	if body := s.do(w.owner, "GET", base+"?entity="+w.entID+"&scope=book", "").Body.String(); !strings.Contains(body, "Zqoverride") {
		t.Errorf("the control, this book's version: %.300s", body)
	}
}

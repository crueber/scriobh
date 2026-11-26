package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/timeline"
)

// What the events and notes views of the Compendium tab show and do for a person the ops would refuse (F-1262, F-1263).

// A project editor who is not on the book's Compendium is shown "not shared with you" and nothing of it: no event, no Compendium name,
// and every change answers as for an event that does not exist.
func TestAuthzSidebarEventsNameNothingToAStrangerToTheCompendium(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	ev := e.event(t, "Zqwedding", "1998")
	e.link(t, d, ev)
	for _, q := range []string{"", "&part=hits&eq=Zqwedding", "&eq=Zqwedding"} {
		code, body := e.scene(e.projectOnly, d, q)
		if code != 200 || !strings.Contains(body, "not shared with you") {
			t.Errorf("events%s: %d %.300s", q, code, body)
		}
		if strings.Contains(body, "Zqwedding") || strings.Contains(body, "Salt Bible") || strings.Contains(body, "/c/"+e.slug) {
			t.Errorf("events%s names the Compendium or its event: %.500s", q, body)
		}
	}
	for _, a := range []struct {
		act string
		f   url.Values
	}{
		{"event-link", url.Values{"doc": {d}, "event_id": {ev}}},
		{"event-unlink", url.Values{"doc": {d}, "event_id": {ev}}},
		{"event-new", url.Values{"doc": {d}, "title": {"Zqmine"}}},
	} {
		unknown := url.Values{"doc": {d}, "event_id": {"tle_00000000000000000000000001"}}
		c1, b1, _ := e.postFrag(t, e.projectOnly, a.act, a.f)
		c2, b2, _ := e.postFrag(t, e.projectOnly, a.act, unknown)
		if c1 != 404 || c1 != c2 || b1 != b2 {
			t.Errorf("%s: a real event answers %d %.120s, an unknown one %d %.120s", a.act, c1, b1, c2, b2)
		}
	}
	if got := e.call(t, "timeline.event.list", timeline.ListIn{ID: e.cid}).(timeline.ListOut); len(got.Items) != 1 || len(got.Items[0].Scenes) != 1 {
		t.Errorf("the stranger changed the Compendium's events: %+v", got.Items)
	}
}

// A Compendium the book references and the caller is not on: its events are never named, and linking one of them is refused.
func TestAuthzSidebarEventsOfAReferencedCompendiumTheCallerIsNotOn(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	ref := e.call(t, "compendium.create", compendiums.CreateIn{Name: "Zqsecretbible"}).(compendiums.Item).Item
	e.call(t, "project.compendium.ref.add", compendiums.RefAddIn{ID: e.pid, CompendiumID: ref.ID})
	secret := e.call(t, "timeline.event.create", timeline.CreateIn{ID: ref.ID, Title: "Zqsecretevent", StoryDate: "1990"}).(timeline.EventItem).Item.ID
	e.call(t, "timeline.scene.link", timeline.LinkIn{ID: d, EventID: secret})
	_, mine := e.scene(e.owner, d, "")
	if !strings.Contains(mine, "Zqsecretevent") {
		t.Fatalf("the control: the owner is on both Compendiums and reads the event: %.500s", mine)
	}
	for _, q := range []string{"", "&part=hits&eq=Zqsecret"} {
		_, body := e.scene(e.peReader, d, q)
		if strings.Contains(body, "Zqsecret") {
			t.Errorf("events%s of a Compendium the caller is not on: %.500s", q, body)
		}
	}
	// Linking and unlinking its event from the panel is refused, and the owner's link is still there.
	for _, act := range []string{"event-link", "event-unlink"} {
		if code, body, _ := e.postFrag(t, e.peReader, act, url.Values{"doc": {d}, "event_id": {secret}}); code == 200 || strings.Contains(body, "Zqsecret") {
			t.Errorf("%s of an event of a Compendium the caller is not on: %d %.200s", act, code, body)
		}
	}
	if got := e.call(t, "timeline.event.get", timeline.GetIn{ID: secret}).(timeline.EventItem).Item; len(got.Scenes) != 1 {
		t.Errorf("the owner's link was changed by someone who cannot read the event: %+v", got.Scenes)
	}
}

// The notes of a book and its scenes' events are the book's: a person who cannot open it is shown nothing, and a title of one book is
// never offered through another book's panel.
func TestAuthzSidebarNotesAndEventsBelongToTheBook(t *testing.T) {
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	n := e.note(t, "Zqprivatenote", "Zqprivatebody")
	for _, p := range []string{"?view=notes", "?view=notes&note=" + n, "?view=notes&new=1", "?view=notes&part=nrows&nq=Zq", "?view=events&doc=" + d, "?view=events&doc=" + d + "&part=hits&eq=a"} {
		code, body := e.frag(e.stranger, p)
		if code != 404 || strings.Contains(body, "Zqprivate") || strings.Contains(body, "The arrival") {
			t.Errorf("a stranger to the book: %s: %d %.200s", p, code, body)
		}
	}
	for _, a := range []string{"note-new", "note-save", "event-link", "event-unlink", "event-new"} {
		if code, body, _ := e.postFrag(t, e.stranger, a, url.Values{"note_id": {n}, "base_revision": {"1"}, "doc": {d}, "title": {"x"}, "body": {"x"}, "event_id": {"x"}}); code != 404 || strings.Contains(body, "Zq") {
			t.Errorf("a stranger to the book: %s: %d %.200s", a, code, body)
		}
	}
	if got := e.call(t, "project.research.list", research.ListIn{ID: e.pid}).(research.ListOut).Items; len(got) != 1 || got[0].Title != "Zqprivatenote" {
		t.Errorf("the stranger changed the book's notes: %+v", got)
	}
	// A reader of the book reads its notes (text, with the reason) and its list, and no more.
	if _, body := e.frag(e.reviewer, "?view=notes"); !strings.Contains(body, "Zqprivatenote") {
		t.Errorf("a reviewer reads the list: %.300s", body)
	}
}

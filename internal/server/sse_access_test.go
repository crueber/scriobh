package server

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
)

// A person added today cannot replay what happened before they were added:
// Last-Event-ID 0 is a reset, not the last day's events.
func TestReplayNeverReachesBeforeTheCallersAccess(t *testing.T) {
	f := newSSEFixture(t, nil)
	f.save(t, "before", 1) // events written while the newcomer was not on the project
	f.save(t, "before again", 2)
	time.Sleep(10 * time.Millisecond)
	late := f.s.newCaller(t, "late", ops.RoleReader)
	f.s.addMember(t, f.pid, late.userID, ops.ProjectReader)
	time.Sleep(10 * time.Millisecond)

	cl, code := openStream(t, f.ts.URL, late, "", "project="+f.pid, "0")
	if code != 200 {
		t.Fatal(code)
	}
	e := cl.nextEvent()
	if e.Event != "reset" {
		t.Fatalf("a newcomer's Last-Event-ID 0 got %q: %s", e.Event, e.Data)
	}
	// After the reset it hears what happens from now on, and can resume from there.
	f.save(t, "after", 3)
	if e := cl.nextEvent(); e.Event != "doc.updated" {
		t.Fatalf("live after the reset: %+v", e)
	}
	// Someone who was on the project all along still replays.
	old, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "0")
	if e := old.nextEvent(); e.Event == "reset" {
		t.Errorf("a member since before the events was reset: %s", e.Data)
	}
}

// The same for a Compendium stream, and for a key made after the events.
func TestReplayClampsCompendiumMembersAndKeys(t *testing.T) {
	f := newSSEFixture(t, nil)
	f.s.do(f.owner, "POST", "/api/v1/compendiums/"+f.cid+"/entities", `{"kind":"character","name":"Early"}`)
	time.Sleep(10 * time.Millisecond)
	late := f.s.newCaller(t, "late-member", ops.RoleWriter)
	f.s.addCompendiumMember(t, f.cid, late.userID, ops.ProjectReader)
	time.Sleep(10 * time.Millisecond)
	cl, _ := openStream(t, f.ts.URL, late, "", "compendium="+f.cid, "0")
	if e := cl.nextEvent(); e.Event != "reset" {
		t.Fatalf("a Compendium newcomer replayed: %+v", e)
	}
	// The owner's key, made after those events, may not replay them either.
	key, _ := f.s.mintKey(t, f.owner, `{"name":"late key","compendium_id":"`+f.cid+`"}`)
	kc, _ := openStream(t, f.ts.URL, caller{}, key, "compendium="+f.cid, "0")
	if e := kc.nextEvent(); e.Event != "reset" {
		t.Fatalf("a key made after the events replayed them: %+v", e)
	}
	// The owner's own session, since before them, does.
	oc, _ := openStream(t, f.ts.URL, f.owner, "", "compendium="+f.cid, "0")
	e := oc.nextEvent()
	if e.Event == "reset" || e.Event == "hello" && !strings.Contains(e.Data, "resumed") {
		t.Errorf("the owner's replay: %+v", e)
	}
}

// A project stream tells a member who is on the Compendium which bible
// operation it was, and everyone else only that the bible changed; it never
// carries a timeline event id.
func TestProjectStreamHidesBibleOperationsFromNonMembers(t *testing.T) {
	f := newSSEFixture(t, nil)
	editor := f.s.newCaller(t, "bible-editor", ops.RoleWriter)
	f.s.addMember(t, f.pid, editor.userID, ops.ProjectEditor)
	f.s.addCompendiumMember(t, f.cid, editor.userID, ops.ProjectEditor)
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/compendium", `{"compendium_id":"`+f.cid+`"}`); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	ro, _ := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, "")
	mem, _ := openStream(t, f.ts.URL, editor, "", "project="+f.pid, "")
	ro.nextEvent()
	mem.nextEvent()
	ent := idOf(t, f.s.do(f.owner, "POST", "/api/v1/compendiums/"+f.cid+"/entities", `{"kind":"character","name":"Ada"}`).Body.Bytes())
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/entity-overrides", `{"entity_id":"`+ent+`","name":"Ada Q"}`); rec.Code >= 300 {
		t.Fatalf("override: %d", rec.Code)
	}
	f.save(t, "text", 1)
	if e := ro.nextEvent(); e.Event != "compendium.changed" || strings.Contains(e.Data, "override") || strings.Contains(e.Data, "entity") {
		t.Errorf("a non-member heard %s %s", e.Event, e.Data)
	}
	if e := ro.nextEvent(); e.Event != "doc.updated" { // the book's own events are untouched
		t.Errorf("book event for the non-member: %s", e.Event)
	}
	if e := mem.nextEvent(); e.Event != "project.compendium.set" && e.Event != "entity.override.set" {
		t.Errorf("a Compendium member heard %s", e.Event)
	} else if strings.Contains(e.Data, "event_id") {
		t.Errorf("a project stream carries a timeline event id: %s", e.Data)
	}
}

// A timeline event id is a Compendium's: it never rides a project stream.
func TestShapeDropsTheTimelineEventIDFromAProjectStream(t *testing.T) {
	e := events.Event{Seq: 1, Type: "timeline.scene.linked", ProjectID: "prj_x", DocumentID: "doc_x", Payload: map[string]any{"event_id": "tle_01HZZZZZZZZZZZZZZZZZZZZZZZ", "linked": true}}
	got := shape(e, events.Scope{Kind: events.ScopeProject, ID: "prj_x"}, "usr_y", false)
	if _, ok := got.Payload["event_id"]; ok || got.Payload["linked"] != true {
		t.Errorf("payload %v", got.Payload)
	}
}

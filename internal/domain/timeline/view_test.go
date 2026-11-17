package timeline_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

func TestProjectViewMergesEventsAndDatedScenes(t *testing.T) {
	x := newH(t)
	wedding := x.ev("Wedding", "1998-04-12", "", x.dublin, "Máire")
	x.ev("The war", "1990", "1995", "")
	x.ev("Someday", "", "", "")
	s1 := x.scene("Opening", "1998-04-12") // dated like the wedding, but it tells it: not a separate item
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s1.ID, EventID: wedding.ID})
	x.scene("Rain", "1992-06")   // a dated scene no event covers
	x.scene("Quiet day", "")     // not on the timeline yet
	x.scene("Fuzzy", "someday?") // a date the timeline cannot read
	v := x.view(x.owner)
	eq(t, "story order", keys(v, v.Story), []string{"The war", "Rain", "Wedding", "Someday"})
	kindOf := map[string]string{}
	for _, it := range v.Items {
		kindOf[it.Title] = it.Kind
	}
	if kindOf["Rain"] != "scene" || kindOf["Wedding"] != "event" || kindOf["Opening"] != "" {
		t.Errorf("kinds %v (the scene that tells an event is shown under it, not beside it)", kindOf)
	}
	for _, it := range v.Items {
		if it.Title == "Wedding" {
			if len(it.Told) != 1 || it.Told[0].ID != s1.ID || it.Location == nil || it.Location.Name != "Dublin" || len(it.Participants) != 1 {
				t.Errorf("the wedding: %+v", it)
			}
		}
		if it.Title == "The war" && (it.StoryDateEnd != "1995" || !it.Dated) {
			t.Errorf("the war: %+v", it)
		}
	}
	// The chapter the project starts with, "Quiet day" and "Fuzzy" are on no timeline.
	if v.Unplaced < 2 {
		t.Errorf("unplaced scenes %d", v.Unplaced)
	}
	// Narrative order: scenes at their reading place, the wedding where its scene is, events no scene tells last.
	eq(t, "narrative order", keys(v, v.Narrative), []string{"Wedding", "Rain", "The war", "Someday"})
	for _, it := range v.Items {
		if v.Narrative[it.NarrativeIndex] != it.Key || v.Story[it.StoryIndex] != it.Key {
			t.Errorf("%s: indexes do not point back at it", it.Title)
		}
	}
	// A dated scene that gains an event is replaced by it.
	e := x.ev("Rain falls", "1992-06-03", "", "")
	rain := ""
	for _, it := range v.Items {
		if it.Title == "Rain" {
			rain = it.ID
		}
	}
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: rain, EventID: e.ID})
	for _, it := range x.view(x.owner).Items {
		if it.Title == "Rain" {
			t.Errorf("a scene with an event is shown twice")
		}
	}
}

func TestNarrativeOrderIsPerBook(t *testing.T) {
	x := newH(t)
	a := x.ev("Event A", "2001", "", "")
	b := x.ev("Event B", "2002", "", "")
	c := x.ev("Event C", "2003", "", "")
	sa, sb, sc := x.scene("Tells A", ""), x.scene("Tells B", ""), x.scene("Tells C", "")
	for _, p := range []struct{ doc, ev string }{{sa.ID, a.ID}, {sb.ID, b.ID}, {sc.ID, c.ID}} {
		x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: p.doc, EventID: p.ev})
	}
	// Derived: the scenes' reading order.
	get := func(pid string) timeline.OrderOut {
		return x.call(x.owner, "timeline.order.get", timeline.OrderIn{ID: pid}).(timeline.OrderOut)
	}
	if o := get(x.pid); len(o.Explicit) != 0 || len(o.Events) != 3 || o.Events[0] != a.ID || o.Events[2] != c.ID {
		t.Fatalf("derived order: %+v", o)
	}
	// The book tells C, then A, then B.
	o := x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{c.ID, a.ID, b.ID}}).(timeline.OrderOut)
	if len(o.Explicit) != 3 || o.Events[0] != c.ID || o.Events[1] != a.ID || o.Events[2] != b.ID {
		t.Fatalf("explicit order: %+v", o)
	}
	// Scenes never move: the narrative still reads scene order, with the events permuted among their places.
	v := x.view(x.owner)
	if !v.Explicit {
		t.Errorf("explicit flag")
	}
	var seq []string
	for _, k := range v.Narrative {
		for _, it := range v.Items {
			if it.Key == k {
				seq = append(seq, it.Title)
			}
		}
	}
	eq(t, "narrative", seq, []string{"Event C", "Event A", "Event B"})
	// Events not named follow in their derived order.
	x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{b.ID}})
	if o := get(x.pid); o.Events[0] != b.ID || o.Events[1] != a.ID || o.Events[2] != c.ID {
		t.Errorf("partial order: %+v", o.Events)
	}
	// Another book on the same Compendium has its own order.
	other := x.call(x.owner, "project.create", projects.CreateIn{Title: "Sequel"}).(projects.Item).Item.ID
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: other, CompendiumID: x.cid})
	if o := get(other); len(o.Explicit) != 0 {
		t.Errorf("the sequel inherited the first book's order: %+v", o)
	}
	x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: other, EventIDs: []string{a.ID, b.ID, c.ID}})
	if o := get(x.pid); o.Events[0] != b.ID {
		t.Errorf("setting the sequel's order moved the first book's")
	}
	if o := get(other); o.Events[0] != a.ID {
		t.Errorf("the sequel's own order: %+v", o.Events)
	}
	// Reset goes back to the scenes.
	x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, Reset: true})
	if o := get(x.pid); len(o.Explicit) != 0 || o.Events[0] != a.ID {
		t.Errorf("reset: %+v", o)
	}
	// Validation: both or neither, duplicates, events that are not readable or not there look the same.
	x.expect(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid}, ops.CodeInvalid)
	x.expect(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, Reset: true, EventIDs: []string{a.ID}}, ops.CodeInvalid)
	x.expect(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{a.ID, a.ID}}, ops.CodeInvalid)
	oc := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Elsewhere"}).(compendiums.Item).Item.ID
	oe := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: oc, Title: "Far away"}).(timeline.EventItem).Item
	real := x.expect(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{oe.ID}}, ops.CodeNotFound)
	fake := x.expect(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{"tle_00000000000000000000000000"}}, ops.CodeNotFound)
	if real.Message != fake.Message {
		t.Errorf("%q vs %q", real.Message, fake.Message)
	}
	// An editor not on the Compendium cannot set an order (the events are not theirs to see).
	x.projectMember(x.editor, ops.ProjectEditor)
	x.expect(x.editor, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{a.ID}}, ops.CodeNotFound)
	// Trashing an event drops it from the order, and restoring brings its place back.
	x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{c.ID, b.ID, a.ID}})
	x.call(x.owner, "timeline.event.delete", timeline.StateIn{ID: b.ID})
	if o := get(x.pid); len(o.Events) != 2 || o.Events[0] != c.ID || o.Events[1] != a.ID {
		t.Errorf("after trashing: %+v", o.Events)
	}
	x.call(x.owner, "timeline.event.restore", timeline.StateIn{ID: b.ID})
	if o := get(x.pid); len(o.Events) != 3 || o.Events[1] != b.ID {
		t.Errorf("after restoring: %+v", o.Events)
	}
}

func TestViewWithAReferencedCompendium(t *testing.T) {
	x := newH(t)
	x.ev("Mine", "2000", "", "")
	world := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Shared world"}).(compendiums.Item).Item.ID
	x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: world, Title: "Long ago", StoryDate: "-0100"})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: world})
	v := x.view(x.owner)
	eq(t, "story order across Compendiums", keys(v, v.Story), []string{"Long ago", "Mine"})
	// A reader of the project and of the primary only, not of the referenced one, does not see its events.
	x.projectMember(x.reader, ops.ProjectReader)
	x.compendiumMember(x.reader, "reader")
	v = x.view(x.reader)
	eq(t, "what a partial reader sees", keys(v, v.Story), []string{"Mine"})
	if !v.Hidden {
		t.Errorf("hidden should say something was left out")
	}
}

// Setting a book's order must not touch the positions of events the caller
// cannot read: someone who sees only the primary Compendium's events keeps the
// owner's positions for the rest, on set and on reset.
func TestSettingTheOrderLeavesUnreadableEventsWhereTheyAre(t *testing.T) {
	x := newH(t)
	mine := x.ev("Mine", "2000", "", "")
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Secret world"}).(compendiums.Item).Item.ID
	hidden := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: other, Title: "Hidden", StoryDate: "1000"}).(timeline.EventItem).Item
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: other})
	x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{hidden.ID, mine.ID}})
	pos := func(id string) float64 {
		var p float64
		if err := x.d.Read().QueryRow(`SELECT position FROM timeline_narrative WHERE project_id = ? AND event_id = ?`, x.pid, id).Scan(&p); err != nil {
			x.t.Fatalf("no position for %s: %v", id, err)
		}
		return p
	}
	before := pos(hidden.ID)
	x.projectMember(x.editor, "editor")
	x.compendiumMember(x.editor, "editor")
	x.call(x.editor, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: []string{mine.ID}})
	if got := pos(hidden.ID); got != before {
		t.Errorf("an event the editor cannot read moved from %v to %v", before, got)
	}
	x.call(x.editor, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, Reset: true})
	if got := pos(hidden.ID); got != before {
		t.Errorf("a reset by someone who cannot read it moved the hidden event: %v", got)
	}
	if n := x.count(`SELECT count(*) FROM timeline_narrative WHERE project_id = ? AND event_id = ?`, x.pid, mine.ID); n != 0 {
		t.Errorf("the editor's reset left %d positions of events they can read", n)
	}
}

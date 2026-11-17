package timeline_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// An update that changes nothing writes nothing: the revision stays, no event
// is emitted, so an editor holding the revision is not refused on its next save.
func TestUpdateThatChangesNothingIsANoOp(t *testing.T) {
	x := newH(t)
	e := x.ev("The wedding", "1998-04-12", "", "Dublin", "Máire", "Elara")
	events := func() int { return x.count(`SELECT count(*) FROM events WHERE type = 'timeline.event.updated'`) }
	before := events()
	same := []timeline.Participant{{Entity: "Elara"}, {Entity: x.maire}} // the same people, in another order
	out := x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: e.ID, Title: str("The wedding"), StoryDate: str("1998-04-12"), Location: str(x.dublin),
		Participants: &same}).(timeline.EventItem).Item
	if out.Revision != e.Revision {
		t.Errorf("the revision moved from %d to %d for no change", e.Revision, out.Revision)
	}
	if events() != before {
		t.Errorf("a no-op update emitted an event")
	}
	// A real change still bumps and emits.
	out = x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: e.ID, Title: str("The wedding, later")}).(timeline.EventItem).Item
	if out.Revision != e.Revision+1 || events() != before+1 {
		t.Errorf("a real change: revision %d, events %d", out.Revision, events()-before)
	}
	// A role change is a change.
	roles := []timeline.Participant{{Entity: "Elara", Role: "witness"}, {Entity: x.maire}}
	if out = x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: e.ID, Participants: &roles}).(timeline.EventItem).Item; out.Revision != e.Revision+2 {
		t.Errorf("a changed role should count: %d", out.Revision)
	}
}

// A name that two live entities of one Compendium both answer to cannot be
// created through the ops (aliases and names are unique), but if the data ever
// says so, an event refuses it as ambiguous instead of picking one.
func TestAnAmbiguousNameIsRefusedNotPicked(t *testing.T) {
	x := newH(t)
	// Bypass the entity ops: give Cork an alias equal to Dublin's name.
	x.exec(`INSERT INTO entity_aliases(id, entity_id, alias, alias_key, notes, created_at) VALUES (?,?,?,?,?,?)`, db.NewID("ali"), x.cork, "Dublin", "dublin", "", db.Now())
	e := x.expect(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "X", Location: "Dublin"}, ops.CodeConflict)
	if e.Data["reason"] != "ambiguous" {
		t.Errorf("%+v", e.Data)
	}
	// By id it is exact.
	x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: x.cid, Title: "X", Location: x.dublin})
}

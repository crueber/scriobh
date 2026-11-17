package scenes_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/ops"
)

// A point of view must be a character: a place with the same name in the
// primary Compendium must not make "Rose" fail when only one character is
// called Rose; and two characters called Rose are an ambiguity the caller
// resolves by id, never a silent pick.
func TestSetPovFiltersByKindThenRefusesAnAmbiguity(t *testing.T) {
	x := newH(t)
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Shared"}).(compendiums.Item).Item.ID
	x.call(x.owner, "entity.create", entities.CreateIn{ID: x.cid, Kind: "place", Name: "Rose"})
	rose := x.call(x.owner, "entity.create", entities.CreateIn{ID: other, Kind: "character", Name: "Rose"}).(entities.Item).Item.ID
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: other})
	d := x.scene("One", "words")
	out := x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: "Rose"}).(scenes.SetOut)
	if out.PovEntity == nil || out.PovEntity.ID != rose {
		t.Fatalf("the one character called Rose should be chosen: %+v", out)
	}
	// As a location the same word is ambiguous: a place and a character both answer.
	e := x.expect(x.owner, "scene.set_location", scenes.SetIn{ID: d.ID, Entity: "Rose"}, ops.CodeConflict)
	if e.Data["reason"] != "ambiguous" || len(e.Data["candidates"].([]map[string]any)) != 2 {
		t.Errorf("the error should name both candidates: %+v", e.Data)
	}
	// A second character of that name makes the point of view ambiguous too.
	more := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "More"}).(compendiums.Item).Item.ID
	third := x.call(x.owner, "entity.create", entities.CreateIn{ID: more, Kind: "character", Name: "Dame Rose", Aliases: []string{"Rose"}}).(entities.Item).Item.ID
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: more})
	e = x.expect(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: "Rose", BaseRevision: 0}, ops.CodeConflict)
	if e.Data["reason"] != "ambiguous" {
		t.Errorf("%+v", e.Data)
	}
	// By id it is exact.
	if out := x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: third}).(scenes.SetOut); out.PovEntity == nil || out.PovEntity.ID != third {
		t.Errorf("by id: %+v", out)
	}
	// A name that only a place answers is "must be a character", not "not found".
	x.expect(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: "Port Salt"}, ops.CodeInvalid)
	// Candidates are only entities the caller can read: an editor on the primary
	// Compendium alone sees one Rose (the place), so there is nothing ambiguous to
	// report and nothing to say about the hidden character.
	x.projectMember(x.editor, ops.ProjectEditor)
	x.compendiumMember(x.editor, "reader")
	out = x.call(x.editor, "scene.set_location", scenes.SetIn{ID: d.ID, Entity: "Rose"}).(scenes.SetOut)
	if out.LocationEntity == nil || out.LocationEntity.Kind != "place" {
		t.Errorf("the editor's Rose: %+v", out.LocationEntity)
	}
}

// Setting what is already set is a true no-op: no revision bump, no event.
func TestSetPovTwiceIsANoOp(t *testing.T) {
	x := newH(t)
	d := x.scene("One", "words")
	first := x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.elara}).(scenes.SetOut)
	var events int
	if err := x.d.Read().QueryRow(`SELECT count(*) FROM events WHERE document_id = ?`, d.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	again := x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: "Elara"}).(scenes.SetOut)
	if again.Revision != first.Revision {
		t.Errorf("the revision moved from %d to %d for no change", first.Revision, again.Revision)
	}
	var after int
	if err := x.d.Read().QueryRow(`SELECT count(*) FROM events WHERE document_id = ?`, d.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != events {
		t.Errorf("a no-op wrote an event")
	}
	// Clearing what is not set is a no-op as well.
	x.call(x.owner, "scene.set_location", scenes.SetIn{ID: d.ID, Entity: ""})
	if got := x.item(x.owner, d.ID).Revision; got != first.Revision {
		t.Errorf("clearing an empty location changed the revision to %d", got)
	}
}

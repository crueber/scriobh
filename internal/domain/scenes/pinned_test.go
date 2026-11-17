package scenes_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/ops"
)

// A key pinned to a project works with the primary Compendium's entities only:
// the book's references are not its to read or to name.
func TestPinnedKeyAndSceneEntities(t *testing.T) {
	x := newH(t)
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Shared"}).(compendiums.Item).Item.ID
	bran := x.call(x.owner, "entity.create", entities.CreateIn{ID: other, Kind: "character", Name: "Bran"}).(entities.Item).Item.ID
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: x.pid, CompendiumID: other})
	d := x.scene("One", "Elara and Bran.")
	key := ops.Principal{UserID: x.owner.UserID, Role: x.owner.Role, ProjectID: x.pid, Scope: ops.ScopeWrite, Via: "apikey"}
	// The person can make Bran the point of view; the pinned key cannot name him.
	x.expect(key, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: bran}, ops.CodeNotFound)
	x.call(key, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: x.elara})
	x.call(x.owner, "scene.set_pov", scenes.SetIn{ID: d.ID, Entity: bran})
	se := x.call(key, "scene.entities", scenes.EntitiesIn{ID: d.ID}).(scenes.EntitiesOut)
	if se.Pov != nil || !se.Hidden {
		t.Errorf("a pinned key reads a referenced Compendium's point of view: %+v", se)
	}
	if it := x.item(key, d.ID); it.PovEntity != nil {
		t.Errorf("outline.get through the pin: %+v", it.PovEntity)
	}
	// The repair run through the pin links only what the primary offers.
	x.exec(`UPDATE documents SET pov_entity_id = NULL`)
	e := x.scene("Two", "")
	pov := "Bran"
	x.call(x.owner, "doc.update", docs.UpdateIn{ID: e.ID, BaseRevision: e.Revision, Pov: &pov})
	out := x.call(key, "scene.link_entities", scenes.LinkIn{ID: x.pid}).(scenes.LinkOut)
	for _, l := range out.Linked {
		if l.Entity.ID == bran {
			t.Errorf("the repair linked an entity of a referenced Compendium through a pin: %+v", out)
		}
	}
}

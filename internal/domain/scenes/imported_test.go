package scenes_test

import (
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
)

func TestLinkImportedTrustsNothingUnchecked(t *testing.T) {
	x := newH(t)
	other := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Elsewhere"}).(compendiums.Item).Item.ID
	alien := x.call(x.owner, "entity.create", entities.CreateIn{ID: other, Kind: "character", Name: "Alien"}).(entities.Item).Item.ID
	a, b, c, d := x.scene("A", "a"), x.scene("B", "b"), x.scene("C", "c"), x.scene("D", "d")
	run := func(user ops.Principal, refs ...scenes.ImportedRef) {
		t.Helper()
		err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
			return scenes.LinkImported(x.ctx, tx, vocab.Reader{UserID: user.UserID}, x.pid, refs)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	run(x.owner,
		scenes.ImportedRef{DocumentID: a.ID, Field: "pov", EntityID: x.maire},                        // in scope, readable: believed
		scenes.ImportedRef{DocumentID: b.ID, Field: "pov", EntityID: alien, Text: "Elara"},           // out of scope: the text is matched by name instead
		scenes.ImportedRef{DocumentID: c.ID, Field: "pov", EntityID: alien, Text: "Someone unknown"}, // out of scope, no match: stays text
		scenes.ImportedRef{DocumentID: d.ID, Field: "pov", EntityID: x.harbour},                      // a point of view must be a character
		scenes.ImportedRef{DocumentID: d.ID, Field: "location", EntityID: "ent_00000000000000000000000000", Text: "Salt Road"})
	got := map[string]docs.OutlineItem{}
	for _, it := range x.outline(x.owner, "").Items {
		got[it.Title] = it
	}
	if got["A"].PovEntity == nil || got["A"].PovEntity.ID != x.maire {
		t.Errorf("A: %+v", got["A"].PovEntity)
	}
	if got["B"].PovEntity == nil || got["B"].PovEntity.ID != x.elara {
		t.Errorf("B: %+v", got["B"].PovEntity)
	}
	if got["C"].PovEntity != nil || got["D"].PovEntity != nil {
		t.Errorf("C %+v D %+v", got["C"].PovEntity, got["D"].PovEntity)
	}
	if got["D"].LocationEntity == nil || got["D"].LocationEntity.ID != x.saltRoad {
		t.Errorf("a missing id falls back to the text: %+v", got["D"].LocationEntity)
	}
	// An importer who may not read the bible links nothing: not by id, not by name.
	x.exec(`UPDATE documents SET pov_entity_id = NULL, location_entity_id = NULL`)
	x.projectMember(x.editor, ops.ProjectEditor)
	run(x.editor, scenes.ImportedRef{DocumentID: a.ID, Field: "pov", EntityID: x.maire, Text: "Máire Ní Bhriain"})
	if it := x.item(x.owner, a.ID); it.PovEntity != nil {
		t.Errorf("an importer outside the Compendium linked %+v", it.PovEntity)
	}
}

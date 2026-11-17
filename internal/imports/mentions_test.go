package imports_test

import (
	"context"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/mentions"
)

// An import hands the mention index to the worker: the project owes a scan
// when the import commits, and the scan finds the entities in the new text.
func TestImportQueuesAMentionScan(t *testing.T) {
	e := newEnv(t)
	cid := e.must(e.w, "compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	e.must(e.w, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Elara"})
	out := e.apply(files(map[string]string{
		"01-one.md": "# One\n\nElara walked the road.\n",
		"02-two.md": "# Two\n\nNobody here, only [[One]].\n",
	}), "", nil)
	var owed int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM mention_state WHERE project_id = ? AND stamp <> indexed_stamp`, out.ProjectID).Scan(&owed); err != nil || owed != 1 {
		t.Fatalf("the project should owe a scan after an import: %d %v", owed, err)
	}
	e.must(e.w, "project.compendium.set", compendiums.SetIn{ID: out.ProjectID, CompendiumID: cid})
	if _, err := mentions.Settle(context.Background(), e.d); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, out.ProjectID).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("the scan found %d mention rows, want 1 (%v)", rows, err)
	}
}

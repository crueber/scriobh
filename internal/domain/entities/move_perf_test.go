package entities_test

import (
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
)

// Budget for F-478: previewing and applying a move of a large bible (2,000 entities, relationships between neighbours,
// an alias on every fifth, custom fields) each stay under three seconds on a laptop, and the whole move is one
// transaction, so the writer is held for the apply only.
func TestMovePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("loads 2,000 entities")
	}
	x := newH(t)
	src, _ := bigBible(t, x, 2000)
	dst := x.compendium(x.owner, "Target for the big move")
	in := compendiums.MergeIn{ID: src, IntoID: dst}
	t0 := time.Now()
	plan := x.call(x.owner, "compendium.merge.preview", in).(compendiums.PlanOut).Plan
	bound(t, "preview of a 2,000-entity move", time.Since(t0), 3*time.Second)
	if plan.Moving != 2000 || plan.RelationsMoved != 1999 {
		t.Fatalf("plan: moving %d, relationships %d", plan.Moving, plan.RelationsMoved)
	}
	in.Fingerprint = plan.Fingerprint
	t0 = time.Now()
	out := x.call(x.owner, "compendium.merge", in).(compendiums.ChangeOut)
	bound(t, "apply of a 2,000-entity move", time.Since(t0), 3*time.Second)
	if len(out.BeforeState.Moved) != 2000 {
		t.Errorf("moved %d", len(out.BeforeState.Moved))
	}
	x.invariants()
	// more than the cap is refused with a plain message, before any work
	big := compendiums.MergeIn{ID: dst, IntoID: src}
	ids := make([]string, entities.MaxMoveEntities+1)
	for i := range ids {
		ids[i] = "ent_x"
	}
	big.EntityIDs = ids
	if e := x.fail(x.owner, "compendium.merge.preview", big); e.Field != "entity_ids" {
		t.Errorf("over the cap: %+v", e)
	}
}

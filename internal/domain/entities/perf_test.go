package entities_test

import (
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/testutil"
)

// bigBible loads a Compendium with n entities through entities.Insert in one
// transaction (the ops are one transaction each, which is not what is being
// measured): aliases on every fifth, custom field values, and relationships
// between neighbours.
func bigBible(t *testing.T, x *h, n int) (cid string, ids []string) {
	t.Helper()
	cid = x.compendium(x.owner, "Big bible")
	kinds := []string{"character", "place", "item", "org", "concept"}
	now := db.Now()
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		for i := 0; i < n; i++ {
			in := entities.CreateIn{ID: cid, Kind: kinds[i%5], Name: fmt.Sprintf("Entity %05d of the %s", i, kinds[i%5]),
				Summary: fmt.Sprintf("Summary %d about the harbour lantern and the quiet shore.", i),
				Body:    fmt.Sprintf("Body of %d. She walked the **shore** past the lantern, thinking of home.\n\nA second paragraph for %d.\n", i, i)}
			if i%5 == 0 {
				in.Aliases = []string{fmt.Sprintf("Nickname %05d", i)}
			}
			if kinds[i%5] == "character" {
				in.Fields = map[string]any{"age": i % 80, "role": "extra"}
			}
			id, err := entities.Insert(x.ctx, tx, in, now)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		for i := 1; i < n; i++ {
			if _, err := tx.Exec(`INSERT INTO entity_relations(id, compendium_id, from_entity_id, to_entity_id, kind, label, created_at) VALUES (?,?,?,?,?,?,?)`,
				db.NewID("rel"), cid, ids[i-1], ids[i], "custom", "next to", now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return cid, ids
}

func bound(t *testing.T, name string, got, limit time.Duration) {
	t.Helper()
	t.Logf("%s: %v (limit %v)", name, got.Round(time.Millisecond), limit)
	// The measurement is made by the caller; only the comparison is a timing test, so the behaviour checks around
	// the call keep running when SCRIOBH_TIMING=off.
	t.Run(name, func(t *testing.T) {
		testutil.Timing(t)
		if got > limit*timingFactor {
			t.Errorf("%s took %v, over %v", name, got, limit*timingFactor)
		}
	})
}

// 5,000 entities: list, search, project view, merge and export stay quick.
// The bounds are generous (several times what a laptop measures); they exist to
// catch a change that makes any of these scale badly.
func TestFiveThousandEntities(t *testing.T) {
	if testing.Short() || timingFactor > 1 {
		t.Skip("loads 5,000 entities; skipped under -short and under the race detector, where it takes ten minutes")
	}
	x := newH(t)
	start := time.Now()
	cid, ids := bigBible(t, x, 5000)
	t.Logf("loaded 5,000 entities in %v", time.Since(start).Round(time.Millisecond))

	// List the whole Compendium, 500 at a time, and one filtered page.
	start = time.Now()
	seen, cur, pages := 0, "", 0
	for {
		out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Limit: 500, Cursor: cur}).(entities.ListOut)
		seen += len(out.Items)
		pages++
		if out.NextCursor == "" {
			break
		}
		cur = out.NextCursor
	}
	if seen != 5000 || pages != 10 {
		t.Fatalf("listed %d entities in %d pages", seen, pages)
	}
	bound(t, "list all 5,000 in 10 pages", time.Since(start), 3*time.Second)
	start = time.Now()
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Q: "entity 0420", Limit: 100}).(entities.ListOut); out.Total != 10 {
		t.Errorf("filtered: %d", out.Total)
	}
	bound(t, "filtered list", time.Since(start), 500*time.Millisecond)
	start = time.Now()
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Name: "nickname 00420"}).(entities.ListOut); len(out.Items) != 1 {
		t.Errorf("by alias: %d", len(out.Items))
	}
	bound(t, "lookup by name", time.Since(start), 300*time.Millisecond)

	// Search: median of several runs.
	times := []time.Duration{}
	for i := 0; i < 15; i++ {
		s := time.Now()
		out := x.call(x.owner, "search.compendium", search.CompendiumQueryIn{ID: cid, Q: "shore lantern"}).(search.CompendiumQueryOut)
		times = append(times, time.Since(s))
		if out.Total != 5000 {
			t.Fatalf("search total %d", out.Total)
		}
	}
	// timing: not a budget: sorts the samples; the limit is asserted by bound
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	bound(t, "search (median of 15, all 5,000 match)", times[len(times)/2], 400*time.Millisecond)
	s := time.Now()
	if out := x.call(x.owner, "search.compendium", search.CompendiumQueryIn{ID: cid, Q: "nickname 004"}).(search.CompendiumQueryOut); out.Total != 20 {
		t.Errorf("rare search: %d", out.Total)
	}
	bound(t, "search for a rare word", time.Since(s), 200*time.Millisecond)

	// The project's view of all of it, with overrides.
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	for i := 0; i < 50; i++ {
		x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ids[i*7], Summary: str("Book summary")})
	}
	s = time.Now()
	pl := x.call(x.owner, "project.entity.list", entities.ProjectListIn{ID: pid, Limit: 500}).(entities.ProjectListOut)
	if pl.Total != 5000 {
		t.Fatalf("project list: %d", pl.Total)
	}
	bound(t, "project.entity.list over 5,000", time.Since(s), 2*time.Second)
	s = time.Now()
	files, err := entities.Projection(x.ctx, x.d, pid)
	if err != nil || len(files) != 5003 {
		t.Fatalf("projection: %d files, %v", len(files), err)
	}
	bound(t, "render the projection of 5,000 entities", time.Since(s), 3*time.Second)
	s = time.Now()
	if fps, err := entities.ProjectFingerprints(x.ctx, x.d.Read(), ""); err != nil || fps[pid] == "" {
		t.Fatalf("fingerprints: %v", err)
	}
	bound(t, "fingerprint 5,000 entities", time.Since(s), 500*time.Millisecond)

	// Merge: a dry run and the real thing, between two entities with many relationships.
	hub, other := ids[100], ids[4000]
	for i := 200; i < 400; i++ {
		x.exec(`INSERT OR IGNORE INTO entity_relations(id, compendium_id, from_entity_id, to_entity_id, kind, label, created_at) VALUES (?,?,?,?,?,?,?)`,
			db.NewID("rel"), cid, other, ids[i], "custom", "knows", db.Now())
	}
	s = time.Now()
	dry := x.call(x.owner, "entity.merge", entities.MergeIn{ID: other, IntoID: hub, DryRun: true, FieldChoices: map[string]string{"age": "target"}}).(entities.MergeOut)
	bound(t, "merge dry run", time.Since(s), 500*time.Millisecond)
	if dry.Plan.RelationsRewired < 200 {
		t.Errorf("plan: %+v", dry.Plan.RelationsRewired)
	}
	s = time.Now()
	out := x.call(x.owner, "entity.merge", entities.MergeIn{ID: other, IntoID: hub, FieldChoices: map[string]string{"age": "target"}}).(entities.MergeOut)
	bound(t, "merge", time.Since(s), 1*time.Second)
	if !out.Merged || out.Plan.RelationsRewired != dry.Plan.RelationsRewired {
		t.Errorf("merge: %+v", out.Plan)
	}

	// Export.
	s = time.Now()
	ex := x.call(x.owner, "compendium.export", entities.ExportIn{ID: cid}).(entities.ExportFile)
	bound(t, "export 5,000 entities", time.Since(s), 5*time.Second)
	t.Logf("export: %d entities, %d KB", ex.Entities, ex.Bytes>>10)

	// An edit stays cheap with a big bible.
	s = time.Now()
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: ids[10], Summary: str("Edited.")})
	bound(t, "edit one entity", time.Since(s), 200*time.Millisecond)
	s = time.Now()
	x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "item", Name: "One more", Aliases: []string{"Another name"}})
	bound(t, "create one entity", time.Since(s), 200*time.Millisecond)

	// Deleting it all: entities, aliases, relationships, search rows, overrides.
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: ""})
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: cid})
	s = time.Now()
	pg := x.call(x.owner, "compendium.purge", compendiums.PurgeIn{ID: cid, ConfirmName: "Big bible"}).(compendiums.PurgeOut)
	bound(t, "purge a Compendium of 5,000 entities", time.Since(s), 30*time.Second)
	if !pg.Purged || pg.Entities != 5000 {
		t.Errorf("purge: %+v", pg)
	}
	for _, table := range []string{"entities", "entity_aliases", "entity_relations", "entity_text", "project_entity_overrides"} {
		if n := x.count(`SELECT count(*) FROM ` + table); n != 0 {
			t.Errorf("%s has %d rows after the purge", table, n)
		}
	}
}

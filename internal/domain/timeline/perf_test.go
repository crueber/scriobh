package timeline_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/testutil"
)

// 500 entities, 400 events with people and places, a 60-scene book telling
// them: the reads a screen makes stay well inside their budgets. The bounds are
// generous (several times what a laptop measures), scaled under the race
// detector; they exist to catch a change that makes any of these scale badly.
func TestFourHundredEventsFiveHundredEntities(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 400-event timeline")
	}
	x := newH(t)
	now := db.Now()
	var people, places []string
	err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		for i := 0; i < 500; i++ {
			kind := "character"
			if i%5 == 0 {
				kind = "place"
			}
			id, err := entities.Insert(x.ctx, tx, entities.CreateIn{ID: x.cid, Kind: kind, Name: fmt.Sprintf("Eòghan%03d", i)}, now)
			if err != nil {
				return err
			}
			if kind == "place" {
				places = append(places, id)
			} else {
				people = append(people, id)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var evs []string
	err = x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		for i := 0; i < 400; i++ {
			id := db.NewID("tle")
			date := fmt.Sprintf("%04d-%02d-%02d", 1900+i/4, 1+i%12, 1+i%28)
			if _, err := tx.Exec(`INSERT INTO timeline_events(id, compendium_id, title, description, story_date, sort_start, sort_end, location_entity_id, revision, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,1,?,?)`, id, x.cid, fmt.Sprintf("Event %03d", i), "Something happens.", date, "", "", places[i%len(places)], now, now); err != nil {
				return err
			}
			for k := 0; k < 3; k++ {
				if _, err := tx.Exec(`INSERT INTO timeline_event_people(event_id, entity_id, role, created_at) VALUES (?,?,?,?)`, id, people[(i*7+k*13)%len(people)], "", now); err != nil {
					return err
				}
			}
			evs = append(evs, id)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The keys are derived in Go; compute them through the op layer's own rules by touching each event once.
	for _, id := range evs {
		x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: id, Description: str("Something happens, again.")})
	}
	for i := 0; i < 60; i++ {
		s := x.scene(fmt.Sprintf("Scene %02d", i), fmt.Sprintf("%04d-%02d", 1900+i, 1+i%12))
		x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: s.ID, EventID: evs[(i*6)%len(evs)]})
	}
	budget := func(name string, d, limit time.Duration) {
		t.Helper()
		t.Logf("%s: %v (limit %v)", name, d.Round(100*time.Microsecond), limit*timingFactor)
		t.Run(name, func(t *testing.T) {
			testutil.Timing(t)
			if d > limit*timingFactor {
				t.Errorf("%s took %v, over %v", name, d, limit*timingFactor)
			}
		})
	}
	timed := func(name string, limit time.Duration, f func()) {
		t.Helper()
		var best time.Duration
		for i := 0; i < 3; i++ {
			s := time.Now()
			f()
			// timing: not a budget: keeps the fastest run; the limit is asserted by budget
			if d := time.Since(s); best == 0 || d < best {
				best = d
			}
		}
		budget(name, best, limit)
	}
	timed("timeline.project.view (400 events, 60 scenes)", 150*time.Millisecond, func() { x.view(x.owner) })
	timed("timeline.check", 250*time.Millisecond, func() { x.check(x.owner) })
	timed("timeline.event.list, 100 a page", 100*time.Millisecond, func() {
		x.call(x.owner, "timeline.event.list", timeline.ListIn{ID: x.cid, Limit: 100})
	})
	timed("timeline.order.get", 150*time.Millisecond, func() {
		x.call(x.owner, "timeline.order.get", timeline.OrderIn{ID: x.pid})
	})
	timed("timeline.order.set, 400 events", 250*time.Millisecond, func() {
		x.call(x.owner, "timeline.order.set", timeline.OrderSetIn{ID: x.pid, EventIDs: evs})
	})
	timed("timeline.md for the project", 150*time.Millisecond, func() {
		if _, err := entities.Projection(x.ctx, x.d, x.pid); err != nil {
			t.Fatal(err)
		}
	})
	if v := x.view(x.owner); len(v.Items) < 400 {
		t.Errorf("view has %d items", len(v.Items))
	}
}

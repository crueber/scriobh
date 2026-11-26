package server

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/testutil"
)

// The scene view's server budget (docs/ARCHITECTURE.md): with 5,000 events in the Compendium and a scene that tells forty of them, the view
// answers in at most 100 ms (p95), and a search for an event to link that has to read all 5,000 in at most 150 ms.
func TestSceneEventsBudgetsAt5000Events(t *testing.T) {
	testutil.Timing(t)
	e := newLkEnv(t)
	d := e.doc(t, "The arrival")
	now := db.Now()
	var ids []string
	err := e.s.DB.WithTx(t.Context(), func(tx *sql.Tx) error {
		for i := 0; i < 5000; i++ {
			id := db.NewID("tle")
			ids = append(ids, id)
			if _, err := tx.Exec(`INSERT INTO timeline_events(id, compendium_id, title, description, story_date, sort_start, sort_end, revision, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,1,?,?)`, id, e.cid, fmt.Sprintf("Event %04d at the quay", i), "Something happens.", fmt.Sprintf("%04d-%02d-%02d", 1000+i/12, 1+i%12, 1+i%28), "", "", now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		e.call(t, "timeline.scene.link", timeline.LinkIn{ID: d, EventID: ids[i*120]})
	}
	p95 := func(f func() int) time.Duration {
		var ds []time.Duration
		for i := 0; i < 30; i++ {
			t0 := time.Now()
			if code := f(); code != 200 {
				t.Fatalf("status %d", code)
			}
			ds = append(ds, time.Since(t0))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[28]
	}
	view := p95(func() int { c, _ := e.scene(e.owner, d, ""); return c })
	// The words match one event, the last of the 5,000 in story order, so the whole of the Compendium is read and nothing ends the search early.
	found := false
	search := p95(func() int {
		c, body := e.scene(e.owner, d, "&part=hits&eq=event+4999")
		found = strings.Contains(body, "Event 4999 at the quay")
		return c
	})
	if !found {
		t.Errorf("an event past the 2,000th was not found by the search to link")
	}
	t.Logf("p95 at 5,000 events: the scene's events %v, a search to link %v", view, search)
	if view > 100*time.Millisecond {
		t.Errorf("scene view p95 %v, want at most 100 ms", view)
	}
	// The words are matched by the database (timeline.event.list's q), so the search reads the matching events and their page, not the
	// Compendium's events one by one; it is sent 300 ms after the writer stops typing, so it never stands in the way of a key. Its budget
	// is the list's, 150 ms.
	if search > 150*time.Millisecond {
		t.Errorf("search p95 %v, want at most 150 ms", search)
	}
}

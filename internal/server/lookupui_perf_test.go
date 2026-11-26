package server

import (
	"database/sql"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/testutil"
)

// The Compendium tab's server budgets (docs/ARCHITECTURE.md): at 5,000 entries the list and a search answer in at most 150 ms (p95), an
// entry in 50 ms, and a save in 100 ms.
func TestLookupBudgetsAt5000Entries(t *testing.T) {
	testutil.Timing(t)
	e := newLkEnv(t)
	err := e.s.DB.WithTx(t.Context(), func(tx *sql.Tx) error {
		for i := 0; i < 5000; i++ {
			if _, err := entities.Insert(t.Context(), tx, entities.CreateIn{ID: e.cid, Kind: "character", Name: "Entry " + strconv.Itoa(i), Summary: "One of five thousand."}, db.Now()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p95 := func(f func() int) time.Duration {
		var d []time.Duration
		for i := 0; i < 30; i++ {
			t0 := time.Now()
			if code := f(); code != 200 {
				t.Fatalf("status %d", code)
			}
			d = append(d, time.Since(t0))
		}
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		return d[28]
	}
	list := p95(func() int { c, _ := e.frag(e.owner, "?part=rows"); return c })
	search := p95(func() int { c, _ := e.frag(e.owner, "?part=rows&q=Entry+49"); return c })
	entry := p95(func() int { c, _ := e.frag(e.owner, "?entity="+e.maire); return c })
	rev := e.revision(t, e.maire)
	save := p95(func() int {
		c, _, _ := e.postFrag(t, e.owner, "save", url.Values{"entity_id": {e.maire}, "base_revision": {strconv.Itoa(rev)}, "summary": {fmt.Sprint("s", rev)}})
		rev++
		return c
	})
	t.Logf("p95 at 5,000 entries: list %v, search %v, entry %v, save %v", list, search, entry, save)
	for what, got := range map[string]time.Duration{"list": list, "search": search} {
		if got > 150*time.Millisecond {
			t.Errorf("%s p95 %v, want at most 150 ms", what, got)
		}
	}
	if entry > 50*time.Millisecond {
		t.Errorf("entry p95 %v, want at most 50 ms", entry)
	}
	if save > 100*time.Millisecond {
		t.Errorf("save p95 %v, want at most 100 ms", save)
	}
}

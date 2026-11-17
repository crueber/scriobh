package presence_test

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/presence"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// The load budget (docs/p7-notes/presence.md): 1,000 projects with 10 people each, every one beating every 20 seconds, is
// 500 beats a second. Each beat is a heartbeat and the list that answers it, done as the registry does them (the
// authorization is the registry's own and is measured with the other ops, not here). The budget is 600 microseconds
// on average (30 percent of one core at 500 a second) and 8 ms at the 99th percentile; the table of 10,000 entries
// stays under 8 MB of heap. The test fails at five times the average budget, so a loaded machine does not flake it
// and a real regression does.
func TestLoadThousandProjectsTenViewersEach(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	const projects, perProject = loadProjects, 10
	d := testutil.DB(t)
	reg := registry.Build(d, nil, t.TempDir())
	svc, _ := ops.Service[*presence.Service](reg.Env(), presence.ServiceName)
	type viewer struct {
		p   ops.Principal
		pid string
		doc string
	}
	var viewers []viewer
	now := db.Now()
	exp := db.Format(time.Now().Add(24 * time.Hour))
	if err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
		for i := 0; i < projects; i++ {
			pid := db.NewID("prj")
			did := db.NewID("doc")
			var owner string
			for j := 0; j < perProject; j++ {
				uid, sid := db.NewID("usr"), db.NewID("ses")
				if j == 0 {
					owner = uid
				}
				if _, err := tx.Exec(`INSERT INTO users(id, email, display_name, role, created_at, updated_at) VALUES (?,?,?,?,?,?)`, uid, uid+"@t.local", fmt.Sprintf("Person %d-%d", i, j), "writer", now, now); err != nil {
					return err
				}
				if j == 0 {
					if _, err := tx.Exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES (?,?,?,?,?,?)`, pid, owner, "P", "p", now, now); err != nil {
						return err
					}
					if _, err := tx.Exec(`INSERT INTO documents(id, project_id, title, created_at, updated_at) VALUES (?,?,?,?,?)`, did, pid, "Chapter", now, now); err != nil {
						return err
					}
				}
				role := "editor"
				switch {
				case j == 0:
					role = "owner"
				case j >= 8:
					role = "reviewer"
				case j == 7:
					role = "reader"
				}
				if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, uid, role, now); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO user_prefs(user_id, key, value) VALUES (?,?,'true')`, uid, presence.PrefKey); err != nil {
					return err
				}
				if _, err := tx.Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES (?,?,?,?,?,?)`, sid, uid, "h"+sid, now, now, exp); err != nil {
					return err
				}
				viewers = append(viewers, viewer{p: ops.Principal{UserID: uid, Via: "session", SessionID: sid}, pid: pid, doc: did})
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	runtime.GC()
	var m0 runtime.MemStats
	runtime.ReadMemStats(&m0)
	// round one fills the table, round two is the steady state of refreshes, round three is people moving between chapters
	for _, v := range viewers {
		if _, err := svc.Beat(ctx, v.p, v.pid, v.doc); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)
	if svc.Len() != projects*perProject {
		t.Fatalf("%d entries, want %d", svc.Len(), projects*perProject)
	}
	lat := make([]time.Duration, 0, len(viewers))
	start := time.Now()
	for _, v := range viewers {
		t0 := time.Now()
		view, err := svc.Beat(ctx, v.p, v.pid, v.doc)
		lat = append(lat, time.Since(t0))
		if err != nil {
			t.Fatal(err)
		}
		if !view.Enabled {
			t.Fatal("not enabled")
		}
	}
	wall := time.Since(start)
	// timing: not a budget: sorts the samples; the limits are asserted in the "time" subtest
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	avg := wall / time.Duration(len(viewers))
	p99 := lat[len(lat)*99/100]
	cpu := float64(avg) * 500 / float64(time.Second) * 100
	heap := int64(m1.HeapAlloc) - int64(m0.HeapAlloc)
	t.Logf("%d projects x %d viewers: %d beats in %v: average %v, median %v, p99 %v, max %v; at 500 a second that is %.1f%% of one core; heap for %d entries %.2f MB; %d CPUs, GOMAXPROCS %d",
		projects, perProject, len(viewers), wall, avg, lat[len(lat)/2], p99, lat[len(lat)-1], cpu, svc.Len(), float64(heap)/1e6, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	// The same beats as a client makes them: through the registry (the caller's role, the gate and the trash are read
	// for every op), one at a time and then from several goroutines at once (the server is concurrent).
	regLat := func(workers int) (time.Duration, time.Duration) {
		per := len(viewers) / workers
		var wg sync.WaitGroup
		lats := make([][]time.Duration, workers)
		t0 := time.Now()
		for w := 0; w < workers; w++ {
			w := w
			wg.Add(1)
			go func() {
				defer wg.Done()
				for _, v := range viewers[w*per : (w+1)*per] {
					b := time.Now()
					if _, err := reg.Call(ctx, v.p, "presence.beat", presence.BeatIn{ID: v.pid, DocumentID: v.doc}); err != nil {
						t.Error(err)
						return
					}
					lats[w] = append(lats[w], time.Since(b))
				}
			}()
		}
		wg.Wait()
		wall := time.Since(t0)
		var all []time.Duration
		for _, l := range lats {
			all = append(all, l...)
		}
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		return wall / time.Duration(len(all)), all[len(all)*99/100]
	}
	seqAvg, seqP99 := regLat(1)
	parAvg, parP99 := regLat(8)
	t.Logf("through the registry: one at a time average %v (p99 %v), %.1f%% of one core at 500 a second; eight at a time average %v of wall time per beat (p99 %v of a single beat's latency)",
		seqAvg, seqP99, float64(seqAvg)*500/float64(time.Second)*100, parAvg, parP99)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if seqAvg > 5*time.Millisecond*timingFactor {
			t.Errorf("through the registry a beat averages %v; the budget is 1 ms (failing at five times that)", seqAvg)
		}
		if avg > 3*time.Millisecond*timingFactor {
			t.Errorf("average %v per beat; the budget is 600 microseconds (failing at five times that)", avg)
		}
	})
	if heap > 8_000_000 {
		t.Errorf("the table of %d entries holds %.1f MB of heap, the budget is 8 MB", svc.Len(), float64(heap)/1e6)
	}
}

// A protected project asks the gate for every person shown. With the answer kept for GateTTL (5 s, dropped at once when the
// project relocks) a project of 64 people beating every 20 seconds costs what an unprotected one does, give or take the
// gate's own read. Measured through the registry, one beat at a time, the first beat of each person cold and the rest warm.
// Budget: under 1.5 ms average a beat with 64 entries (1.4 ms with no cache here, 2.4 ms in the review).
func TestGatedProjectBeatCost(t *testing.T) {
	if testing.Short() {
		t.Skip("load test")
	}
	w := newWorld(t)
	const people = 64
	ctx := context.Background()
	type v struct {
		p ops.Principal
	}
	var viewers []v
	for i := 0; i < people; i++ {
		pe := w.newPerson(fmt.Sprintf("gated%02d", i), ops.RoleWriter, ops.ProjectEditor)
		if _, err := w.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, w.pid, pe.p.UserID, ops.ProjectEditor, db.Now()); err != nil {
			t.Fatal(err)
		}
		w.share(pe, "true")
		viewers = append(viewers, v{pe.p})
	}
	w.share(w.owner, "true")
	if _, err := w.reg.Call(ctx, w.owner.p, "project.passphrase.set", projectgate.SetIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	for _, x := range viewers {
		if _, err := w.reg.Call(ctx, x.p, "project.unlock", projectgate.UnlockIn{ID: w.pid, Passphrase: "a long enough one"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range viewers { // everyone is in the table
		if _, err := w.reg.Call(ctx, x.p, "presence.beat", presence.BeatIn{ID: w.pid}); err != nil {
			t.Fatal(err)
		}
	}
	round := func() time.Duration {
		t0 := time.Now()
		for _, x := range viewers {
			if _, err := w.reg.Call(ctx, x.p, "presence.beat", presence.BeatIn{ID: w.pid}); err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(t0) / time.Duration(len(viewers))
	}
	warm := round()
	for i := 0; i < 3; i++ {
		// timing: not a budget: keeps the fastest round; the limit is asserted in the "time" subtest below
		if d := round(); d < warm {
			warm = d
		}
	}
	t.Logf("a protected project with %d people shown: %v a beat through the registry (warm gate answers)", people, warm)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if warm > 1500*time.Microsecond*5*timingFactor {
			t.Errorf("%v a beat; the budget is 1.5 ms (failing at five times that)", warm)
		}
	})
	// and the first lock is seen at once, whatever the cache holds
	if _, err := w.reg.Call(ctx, viewers[0].p, "project.lock", projectgate.ProjectIn{ID: w.pid}); err != nil {
		t.Fatal(err)
	}
	out, err := w.reg.Call(ctx, w.owner.p, "presence.list", presence.ListIn{ID: w.pid})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out.(presence.View).Items {
		if it.UserID == viewers[0].p.UserID {
			t.Fatal("a person who just locked the project is still shown: the gate's cached answer was not dropped")
		}
	}
	if n := len(out.(presence.View).Items); n != people-1 {
		t.Fatalf("%d shown, want %d", n, people-1)
	}
}

package studio_test

import (
	"database/sql"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/testutil"
)

// The dashboard's budget is p95 under 150 ms for five years of daily writing over 20 projects (invariant 13). The bench
// metric studio.dashboard is the gate; this is the same measurement as a test, with twice the budget as its bound (scaled when
// the race detector is on) so a loaded machine does not fail it, and the numbers in the log.
func TestTheDashboardIsFastOverFiveYearsOfDataOnTwentyProjects(t *testing.T) {
	if testing.Short() {
		t.Skip("builds 36,520 daily rows")
	}
	w := newWorld(t)
	u := w.person("u", "UTC")
	w.now = time.Now().UTC()
	var ids []string
	for i := 0; i < 20; i++ {
		ids = append(ids, w.project(u, fmt.Sprintf("Perf %02d", i)))
	}
	err := w.d.WithTx(t.Context(), func(tx *sql.Tx) error {
		ds, _ := tx.Prepare(`INSERT INTO daily_stats(user_id, project_id, day, words_added, words_net, minutes, sessions, active_seconds, goal_target, goal_hit, updated_at) VALUES (?,?,?,?,?,?,1,?,0,0,?)`)
		ss, _ := tx.Prepare(`INSERT INTO writing_sessions(id, user_id, project_id, document_id, day, tz, started_at, ended_at, words_added, words_net, active_seconds, saves) VALUES (?,?,?,NULL,?,'UTC',?,?,?,?,?,5)`)
		n := 0
		for d := 0; d < 1826; d++ {
			tm := w.now.AddDate(0, 0, -d)
			for p, pid := range ids {
				n++
				words, secs := 80+(n*37)%900, 600+(n*91)%3000
				if _, err := ds.Exec(u.id, pid, tm.Format("2006-01-02"), words, words-(n*13)%120, (secs+59)/60, secs, db.Now()); err != nil {
					return err
				}
				st := time.Date(tm.Year(), tm.Month(), tm.Day(), (n*7+p)%24, (n*11)%60, 0, 0, time.UTC)
				if _, err := ss.Exec(db.NewID("ses"), u.id, pid, tm.Format("2006-01-02"), db.Format(st), db.Format(st.Add(time.Duration(secs)*time.Second)), words, words, secs); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	mkGoal(t, w, u, "day", "words", 500)
	mkGoal(t, w, u, "week", "words", 5000)
	mkGoal(t, w, u, "total", "words", 2_000_000)
	run := func(in studio.DashboardIn) (p50, p95 time.Duration) {
		var ds []time.Duration
		for i := 0; i < 30; i++ {
			t0 := time.Now()
			w.call(u, "studio.dashboard", in)
			ds = append(ds, time.Since(t0))
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		return ds[len(ds)/2], ds[(len(ds)*95+99)/100-1]
	}
	for name, in := range map[string]studio.DashboardIn{"all time": {Range: "all"}, "30 days": {}} {
		p50, p95 := run(in)
		t.Logf("dashboard, %s, 36,520 daily rows: p50 %v, p95 %v (budget 150 ms)", name, p50, p95)
		t.Run("time/"+name, func(t *testing.T) {
			testutil.Timing(t)
			if assertTiming && p95 > 300*time.Millisecond*timingFactor {
				t.Errorf("%s: p95 %v is over twice the 150 ms budget", name, p95)
			}
		})
	}
	d := dash(t, w, u)
	if d.Cards[3].Words == 0 || len(d.Calendar.Weeks) != 24 {
		t.Errorf("the fixture was not read: %+v", d.Cards[3])
	}
}

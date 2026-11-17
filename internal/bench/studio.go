package bench

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
)

// The writing dashboard's budget (F-501 to F-512, docs/p7-notes/studio.md): p95 under 150 ms for a person with five years
// of daily writing over 20 projects, answered in one read from the per-project daily rows the save loop keeps. The fixture
// is the worst case that is still a person: every one of the 20 projects written in on every one of 1,826 days (36,520 daily
// rows, as many sessions), with goals of every period, 300 sprints and the chapters finished along the way. It is made by
// the rows the save loop would have written, so the dashboard reads what it reads in life; the generator of the project
// text and its seed are not touched (the baseline of the other metrics stays comparable).
const (
	studioProjects = 20
	studioDays     = 1826
)

// fillStudio writes the dashboard's fixture for the bench user.
func (e *env) fillStudio() error {
	// A bench directory that is reused already holds the fixture: it is not added to (the numbers would drift with every run).
	var have int
	if err := e.d.Read().QueryRowContext(e.ctx, `SELECT count(*) FROM projects WHERE title = 'Studio bench 00'`).Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return nil
	}
	ids := make([]string, 0, studioProjects)
	for i := 0; i < studioProjects; i++ {
		out, err := e.call("project.create", projects.CreateIn{Title: fmt.Sprintf("Studio bench %02d", i)})
		if err != nil {
			return err
		}
		ids = append(ids, out.(projects.Item).Item.ID)
	}
	today := time.Now().UTC()
	user := e.who.UserID
	return e.d.WithTx(e.ctx, func(tx *sql.Tx) error {
		ds, err := tx.Prepare(`INSERT INTO daily_stats(user_id, project_id, day, words_added, words_net, minutes, sessions, active_seconds, goal_target, goal_hit, updated_at)
			VALUES (?,?,?,?,?,?,1,?,0,0,?)`)
		if err != nil {
			return err
		}
		defer ds.Close()
		ss, err := tx.Prepare(`INSERT INTO writing_sessions(id, user_id, project_id, document_id, day, tz, started_at, ended_at, words_added, words_net, active_seconds, saves)
			VALUES (?,?,?,NULL,?,'UTC',?,?,?,?,?,5)`)
		if err != nil {
			return err
		}
		defer ss.Close()
		now := db.Now()
		n := 0
		for d := 0; d < studioDays; d++ {
			t := today.AddDate(0, 0, -d)
			day := t.Format("2006-01-02")
			for p, pid := range ids {
				n++
				words := 80 + (n*37)%900 // 80 to 979 words a project a day
				net := words - (n*13)%120
				secs := 600 + (n*91)%3000
				if _, err := ds.Exec(user, pid, day, words, net, (secs+59)/60, secs, now); err != nil {
					return err
				}
				start := time.Date(t.Year(), t.Month(), t.Day(), (n*7+p)%24, (n*11)%60, 0, 0, time.UTC)
				if _, err := ss.Exec(db.NewID("ses"), user, pid, day, db.Format(start), db.Format(start.Add(time.Duration(secs)*time.Second)), words, net, secs); err != nil {
					return err
				}
			}
		}
		// Goals of every period and measure, with a change or two each, and the records they read.
		for i, g := range []struct {
			period, measure string
			target          int
		}{
			{"day", "words", 500}, {"week", "words", 5000}, {"month", "words", 20000}, {"total", "words", 2_000_000},
			{"day", "sessions", 2}, {"week", "minutes", 600}, {"week", "chapters", 2}, {"total", "chapters", 400},
		} {
			id := db.NewID("sgl")
			start := today.AddDate(0, 0, -1500).Format("2006-01-02")
			if _, err := tx.Exec(`INSERT INTO studio_goals(id, user_id, project_id, period, measure, start_date, created_at) VALUES (?,?,'',?,?,?,?)`, id, user, g.period, g.measure, start, now); err != nil {
				return err
			}
			for v := 1; v <= 3; v++ {
				if _, err := tx.Exec(`INSERT INTO studio_goal_versions(goal_id, version, target, deadline, from_date, progress_at, created_at) VALUES (?,?,?,?,?,?,?)`,
					id, v, g.target+v*i, "", today.AddDate(0, 0, -1500+v*400).Format("2006-01-02"), 0, now); err != nil {
					return err
				}
			}
		}
		for i := 0; i < 300; i++ {
			t := today.AddDate(0, 0, -(i*6)%studioDays)
			if _, err := tx.Exec(`INSERT INTO studio_sprints(id, user_id, project_id, day, tz, planned_minutes, target_words, started_at, closes_at, state, ended_at, words_added, words_net, saves)
				VALUES (?,?,?,?,'UTC',25,0,?,?,'completed',?,?,?,8)`, db.NewID("spr"), user, ids[i%studioProjects], t.Format("2006-01-02"),
				db.Format(t), db.Format(t.Add(27*time.Minute)), db.Format(t.Add(25*time.Minute)), 200+i%300, 150+i%300); err != nil {
				return err
			}
		}
		return nil
	})
}

// measureStudio times the dashboard over the fixture (the all-time range, which is the heaviest) and the activity calendar.
func (e *env) measureStudio(n int, repeatable func(string, int, bool, func(int) error) error) error {
	if _, ok := e.srv.Reg.Lookup("studio.dashboard"); !ok {
		return nil
	}
	if err := e.fillStudio(); err != nil {
		return err
	}
	logf(e.log, "  studio fixture: %d projects, %d days, %d daily rows\n", studioProjects, studioDays, studioProjects*studioDays)
	if err := repeatable("studio.dashboard", n, true, func(int) error {
		_, err := e.call("studio.dashboard", studio.DashboardIn{Range: "all"})
		return err
	}); err != nil {
		return err
	}
	return repeatable("studio.calendar.all", n, true, func(int) error {
		_, err := e.call("studio.calendar", studio.CalendarIn{All: true})
		return err
	})
}

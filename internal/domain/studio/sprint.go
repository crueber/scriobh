package studio

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/ops"
)

// Sprints and the session timer (F-152, F-507).
//
// A sprint is a window of time on one project: 15, 25 or 45 minutes or any length from 1 to 240, or an open session
// timer (0 minutes) that runs until it is stopped or four hours have passed. The words in it are the additions the
// save loop records, added to the sprint in the same transaction as the day (goals.addToSprint): the words a sprint holds
// are the words recorded while it ran, counted once in the day each was written on and once in the sprint. A sprint that
// runs past local midnight (or a change of zone) holds the words of two days and so is not a part of either: it
// reconciles with the sum of the days it touches, over the time it ran, and no day holds a word twice. It counts on the
// day it began for the best sprint and the time-of-day statistics.
//
// A sprint left running is closed by a rule of its stored times, not by a timer and not by a write in a read: a save
// is added until closes_at (the planned end and two minutes of grace to finish a sentence), a read treats the row as
// closed from then on, and the next write that touches the person's sprints settles it. Its words stay. It is called
// completed, never failed.

const (
	// SprintGrace is how long after its planned end a sprint still takes saves.
	SprintGrace = 2 * time.Minute
	// OpenSession is how long an open session timer runs when nobody stops it.
	OpenSession = 4 * time.Hour

	maxSprintMinutes = 240
	maxSprintTarget  = 100_000
)

// SprintOut is a sprint or an open session.
type SprintOut struct {
	ID             string `json:"id"`
	ProjectID      string `json:"project_id"`
	PlannedMinutes int    `json:"planned_minutes" desc:"0 for an open session timer."`
	TargetWords    int    `json:"target_words" desc:"0 for none."`
	State          string `json:"state" enum:"running,completed,stopped,abandoned" desc:"A sprint left running past its closing time is completed."`
	Day            string `json:"day" desc:"The person's local date when it began."`
	Timezone       string `json:"timezone"`
	StartedAt      string `json:"started_at"`
	EndsAt         string `json:"ends_at,omitempty" desc:"The planned end; empty for an open session."`
	ClosesAt       string `json:"closes_at" desc:"Saves after this are not added."`
	EndedAt        string `json:"ended_at,omitempty"`
	WordsAdded     int    `json:"words_added" desc:"The words added while it ran: the saves recorded in its window, each also counted once in the day it was written on. A sprint across local midnight holds words of two days."`
	WordsNet       int    `json:"words_net" desc:"Net words in it, shown beside the words added."`
	Saves          int    `json:"saves"`
	RemainingSecs  int    `json:"remaining_seconds,omitempty" desc:"While running: seconds to the planned end (0 once past it)."`
	ElapsedSecs    int    `json:"elapsed_seconds"`
	Over           bool   `json:"over,omitempty" desc:"Running, past its planned end, still inside the grace."`
}

type sprintRow struct {
	id, user, project, day, tz, started, closes, state, ended string
	planned, target, added, net, saves                        int
}

const sprintCols = `id, user_id, project_id, day, tz, planned_minutes, target_words, started_at, closes_at, state, ended_at, words_added, words_net, saves`

func scanSprint(sc interface{ Scan(...any) error }) (sprintRow, error) {
	var r sprintRow
	err := sc.Scan(&r.id, &r.user, &r.project, &r.day, &r.tz, &r.planned, &r.target, &r.started, &r.closes, &r.state, &r.ended, &r.added, &r.net, &r.saves)
	return r, err
}

// plannedEnd is when a sprint was to end: its start and its minutes, or the closing time of an open session.
func (r sprintRow) userID() string { return r.user }

func (r sprintRow) plannedEnd() time.Time {
	st, _ := db.Parse(r.started)
	if r.planned == 0 {
		c, _ := db.Parse(r.closes)
		return c
	}
	return st.Add(time.Duration(r.planned) * time.Minute)
}

// out derives the state a reader sees from the stored row and the time: a running row past its closing time is
// completed, ended at its planned end.
func (r sprintRow) out(now time.Time) SprintOut {
	o := SprintOut{ID: r.id, ProjectID: r.project, PlannedMinutes: r.planned, TargetWords: r.target, State: r.state, Day: r.day, Timezone: r.tz,
		StartedAt: r.started, ClosesAt: r.closes, EndedAt: r.ended, WordsAdded: r.added, WordsNet: r.net, Saves: r.saves}
	st, _ := db.Parse(r.started)
	end := r.plannedEnd()
	if r.planned > 0 {
		o.EndsAt = db.Format(end)
	}
	closes, _ := db.Parse(r.closes)
	if r.state == "running" {
		if now.After(closes) {
			o.State, o.EndedAt = "completed", db.Format(end)
			o.ElapsedSecs = int(end.Sub(st) / time.Second)
			return o
		}
		o.ElapsedSecs = max(0, int(now.Sub(st)/time.Second))
		if r.planned > 0 {
			o.RemainingSecs = max(0, int(end.Sub(now)/time.Second))
			o.Over = now.After(end)
		}
		return o
	}
	if e, err := db.Parse(r.ended); err == nil {
		o.ElapsedSecs = max(0, int(e.Sub(st)/time.Second))
	}
	return o
}

// currentSprint is the running sprint the person can count, if there is one and it has not run past its closing time.
func currentSprint(v *Viewer) (*SprintOut, error) {
	r, err := scanSprint(v.Q.QueryRowContext(v.Ctx, `SELECT `+sprintCols+` FROM studio_sprints WHERE user_id = ? AND state = 'running'`, v.UserID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o := r.out(db.NowTime())
	if o.State != "running" || !v.sees(r.project) {
		return nil, nil
	}
	return &o, nil
}

// settleStale closes the person's running sprint if it is past its closing time. It runs in the write that starts,
// stops or abandons one.
func settleStale(tx *sql.Tx, userID string, now time.Time) error {
	r, err := scanSprint(tx.QueryRow(`SELECT `+sprintCols+` FROM studio_sprints WHERE user_id = ? AND state = 'running'`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	closes, _ := db.Parse(r.closes)
	if !now.After(closes) {
		return nil
	}
	_, err = tx.Exec(`UPDATE studio_sprints SET state = 'completed', ended_at = ? WHERE id = ?`, db.Format(r.plannedEnd()), r.id)
	return err
}

// SprintStartIn starts a sprint.
type SprintStartIn struct {
	ProjectID   string `path:"id" json:"-" desc:"Project id."`
	Minutes     int    `json:"minutes" desc:"15, 25 or 45 are the usual; any length from 1 to 240. 0 starts an open session timer that runs until stopped (at most four hours)."`
	TargetWords int    `json:"target_words,omitempty" desc:"Optional: words to add during it, 1 to 100,000."`
}

func (in SprintStartIn) ProjectRef() string { return in.ProjectID }

// Validate checks the sprint.
func (in *SprintStartIn) Validate() error {
	if in.Minutes < 0 || in.Minutes > maxSprintMinutes {
		return ops.Invalid("minutes", fmt.Sprintf("A sprint is between 1 and %d minutes, or 0 for an open timer.", maxSprintMinutes))
	}
	if in.TargetWords < 0 || in.TargetWords > maxSprintTarget {
		return ops.Invalid("target_words", fmt.Sprintf("The target is up to %d words.", maxSprintTarget))
	}
	return nil
}

// SprintItem wraps a sprint.
type SprintItem struct {
	Item SprintOut `json:"item"`
}

func sprintStart(c *ops.Ctx, in SprintStartIn) (SprintItem, error) {
	var out SprintOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		now := db.NowTime()
		if err := settleStale(tx, c.P.UserID, now); err != nil {
			return err
		}
		var running int
		if err := tx.QueryRow(`SELECT count(*) FROM studio_sprints WHERE user_id = ? AND state = 'running'`, c.P.UserID).Scan(&running); err != nil {
			return err
		}
		if running > 0 {
			return ops.ConflictWith("A sprint is already running. Stop it first.", map[string]any{"reason": "sprint_running"})
		}
		var zone string
		if err := tx.QueryRow(`SELECT timezone FROM users WHERE id = ?`, c.P.UserID).Scan(&zone); err != nil {
			return err
		}
		loc, zname := goals.Location(zone)
		closes := now.Add(OpenSession)
		if in.Minutes > 0 {
			closes = now.Add(time.Duration(in.Minutes)*time.Minute + SprintGrace)
		}
		id := db.NewID("spr")
		if _, err := tx.Exec(`INSERT INTO studio_sprints(id, user_id, project_id, day, tz, planned_minutes, target_words, started_at, closes_at, state)
			VALUES (?,?,?,?,?,?,?,?,?,'running')`, id, c.P.UserID, in.ProjectID, now.In(loc).Format(dayLayout), zname, in.Minutes, in.TargetWords, db.Format(now), db.Format(closes)); err != nil {
			return err
		}
		r, err := scanSprint(tx.QueryRow(`SELECT `+sprintCols+` FROM studio_sprints WHERE id = ?`, id))
		out = r.out(now)
		return err
	})
	return SprintItem{Item: out}, err
}

// SprintEndIn stops or abandons the running sprint.
type SprintEndIn struct{}

func closeSprint(c *ops.Ctx, state string) (SprintItem, error) {
	var out SprintOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		now := db.NowTime()
		r, err := scanSprint(tx.QueryRow(`SELECT `+sprintCols+` FROM studio_sprints WHERE user_id = ? AND state = 'running'`, c.P.UserID))
		if errors.Is(err, sql.ErrNoRows) {
			return ops.Conflict("No sprint is running.")
		}
		if err != nil {
			return err
		}
		closes, _ := db.Parse(r.closes)
		end := now
		if now.After(closes) { // it had already run its course: it is completed, at its planned end
			state, end = "completed", r.plannedEnd()
		}
		if _, err := tx.Exec(`UPDATE studio_sprints SET state = ?, ended_at = ? WHERE id = ?`, state, db.Format(end), r.id); err != nil {
			return err
		}
		r, err = scanSprint(tx.QueryRow(`SELECT `+sprintCols+` FROM studio_sprints WHERE id = ?`, r.id))
		out = r.out(now)
		return err
	})
	if err != nil {
		return SprintItem{}, err
	}
	// Stopping a sprint is always allowed, but what it shows of a project the caller cannot count (locked by a passphrase,
	// in the trash, left) is nothing: not the project, not a word.
	if v, verr := newViewer(c); verr != nil {
		return SprintItem{}, verr
	} else if !v.sees(out.ProjectID) {
		out = SprintOut{ID: out.ID, State: out.State, Day: out.Day, Timezone: out.Timezone, StartedAt: out.StartedAt, EndedAt: out.EndedAt, ClosesAt: out.ClosesAt}
	}
	return SprintItem{Item: out}, nil
}

func sprintStop(c *ops.Ctx, _ SprintEndIn) (SprintItem, error)    { return closeSprint(c, "stopped") }
func sprintAbandon(c *ops.Ctx, _ SprintEndIn) (SprintItem, error) { return closeSprint(c, "abandoned") }

// SprintCurrentOut is the running sprint, if any.
type SprintCurrentOut struct {
	Sprint *SprintOut `json:"sprint" desc:"Null when none is running (or the project it is on is locked or gone)."`
	Now    string     `json:"now"`
}

func sprintCurrent(c *ops.Ctx, _ SprintEndIn) (SprintCurrentOut, error) {
	v, err := newViewer(c)
	if err != nil {
		return SprintCurrentOut{}, err
	}
	sp, err := currentSprint(v)
	return SprintCurrentOut{Sprint: sp, Now: db.Now()}, err
}

// SprintStatsIn asks for the person's sprint statistics.
type SprintStatsIn struct {
	Range string `query:"range" enum:"30d,90d,365d,all" desc:"Default 90d."`
}

// SprintBest is the sprint with the most words.
type SprintBest struct {
	ID         string `json:"id"`
	Day        string `json:"day"`
	WordsAdded int    `json:"words_added"`
	Minutes    int    `json:"minutes"`
}

// SprintPart is the sprints that began in a quarter of the day.
type SprintPart struct {
	Part         string `json:"part" enum:"night,morning,afternoon,evening"`
	Sprints      int    `json:"sprints"`
	Words        int    `json:"words"`
	AvgPerSprint int    `json:"avg_per_sprint"`
}

// SprintStatsOut is the sprint statistics.
type SprintStatsOut struct {
	From         string       `json:"from"`
	To           string       `json:"to"`
	Sprints      int          `json:"sprints" desc:"Completed and stopped sprints. An abandoned one is not counted, and nothing marks it as anything."`
	Words        int          `json:"words" desc:"Words added while they ran: each word is also in the day it was written on, and is not added to that day a second time."`
	AvgPerSprint int          `json:"avg_per_sprint"`
	Minutes      int          `json:"minutes"`
	Best         *SprintBest  `json:"best,omitempty" desc:"The sprint with the most words; the earlier one wins a tie."`
	Parts        []SprintPart `json:"parts" desc:"By the local time of day the sprint began."`
	Recent       []SprintOut  `json:"recent" desc:"The last 20, newest first."`
}

func sprintStats(c *ops.Ctx, in SprintStatsIn) (SprintStatsOut, error) {
	v, err := newViewer(c)
	if err != nil {
		return SprintStatsOut{}, err
	}
	rng := in.Range
	if rng == "" {
		rng = "90d"
	}
	from, to := "", v.Today
	switch rng {
	case "30d":
		from = addDays(v.Today, -29)
	case "90d":
		from = addDays(v.Today, -89)
	case "365d":
		from = addDays(v.Today, -364)
	case "all":
		from = "0000-01-01"
	default:
		return SprintStatsOut{}, ops.Invalid("range", "The range is 30d, 90d, 365d or all.")
	}
	return sprintStatsFor(v, from, to)
}

// loadSprints is the sprints a person can count that began between two local dates, oldest first: the completed and
// the stopped, and a running one past its closing time (which a reader sees as completed). An abandoned one is not here.
func loadSprints(v *Viewer, from, to string) ([]SprintOut, error) {
	if len(v.IDs) == 0 {
		return nil, nil
	}
	list, args := inList(v.IDs)
	now := db.NowTime()
	rows, err := v.Q.QueryContext(v.Ctx, `SELECT `+sprintCols+` FROM studio_sprints WHERE user_id = ? AND project_id IN `+list+` AND day >= ? AND day <= ?
		AND (state IN ('completed','stopped') OR (state = 'running' AND closes_at < ?)) ORDER BY started_at, id`,
		append(append([]any{v.UserID}, args...), from, to, db.Format(now))...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var all []SprintOut
	for rows.Next() {
		r, err := scanSprint(rows)
		if err != nil {
			return nil, err
		}
		all = append(all, r.out(now))
	}
	return all, rows.Err()
}

func sprintStatsFor(v *Viewer, from, to string) (SprintStatsOut, error) {
	out := SprintStatsOut{From: from, To: to, Parts: []SprintPart{}, Recent: []SprintOut{}}
	all, err := loadSprints(v, from, to)
	if err != nil {
		return out, err
	}
	parts := emptyParts()
	for i := range all {
		o := all[i]
		out.Sprints++
		out.Words += o.WordsAdded
		out.Minutes += minutesOf(o.ElapsedSecs)
		if out.Best == nil || o.WordsAdded > out.Best.WordsAdded {
			out.Best = &SprintBest{ID: o.ID, Day: o.Day, WordsAdded: o.WordsAdded, Minutes: minutesOf(o.ElapsedSecs)}
		}
		if st, err := db.Parse(o.StartedAt); err == nil {
			p := st.In(zoneLoc(o.Timezone)).Hour() / 6
			parts[p].Sprints++
			parts[p].Words += o.WordsAdded
		}
	}
	for i := range parts {
		if parts[i].Sprints > 0 {
			parts[i].AvgPerSprint = roundHalfUp(float64(parts[i].Words) / float64(parts[i].Sprints))
		}
	}
	out.Parts = parts
	if out.Sprints > 0 {
		out.AvgPerSprint = roundHalfUp(float64(out.Words) / float64(out.Sprints))
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].StartedAt > all[j].StartedAt })
	out.Recent = all[:min(20, len(all))]
	return out, nil
}

func emptyParts() []SprintPart {
	return []SprintPart{{Part: "night"}, {Part: "morning"}, {Part: "afternoon"}, {Part: "evening"}}
}

// CloseOpenSprints closes every sprint left running, for a restore: a backup brings back sprints that were running when it was
// taken, and they ended with that moment. Each is ended by the rule of settleStale (its planned end, or the four-hour cap of
// an open timer) but never later than the last save that was credited to the person on that project since it began (the
// backup was made no later than that), and never later than now, so a 25-minute sprint restored ten days later is still 25
// minutes or less, and one with no save at all lasted no time. The words they hold stay.
func CloseOpenSprints(ctx context.Context, d *db.DB) (int, error) {
	n := 0
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+sprintCols+` FROM studio_sprints WHERE state = 'running'`)
		if err != nil {
			return err
		}
		var open []sprintRow
		for rows.Next() {
			r, err := scanSprint(rows)
			if err != nil {
				rows.Close()
				return err
			}
			open = append(open, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := db.NowTime()
		for _, r := range open {
			started, _ := db.Parse(r.started)
			end := r.plannedEnd()
			var last sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT max(ended_at) FROM writing_sessions WHERE user_id = ? AND project_id = ? AND ended_at >= ?`, r.userID(), r.project, r.started).Scan(&last); err != nil {
				return err
			}
			save := started
			if t, err := db.Parse(last.String); err == nil {
				save = t
			}
			if save.Before(end) {
				end = save
			}
			if now.Before(end) {
				end = now
			}
			if end.Before(started) {
				end = started
			}
			if _, err := tx.ExecContext(ctx, `UPDATE studio_sprints SET state = 'completed', ended_at = ? WHERE id = ?`, db.Format(end), r.id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

package studio

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Goals with a history (F-500, F-506, F-515, F-505).
//
// A goal is for a person, over every project they may read (project_id empty) or over one project; its period is a
// day, a week (Monday to Sunday), a month or the total (with an optional deadline); its measure is words added,
// writing sessions, chapters finished or minutes written. It is never edited in place: a change of target (or of a
// total goal's deadline) adds a version, and a period is judged against the versions that were in force in it. A goal
// is ended by archiving it; the record stays.

const maxActiveGoals = 20

// targetLimit is the largest target a goal of a period and measure may have.
func targetLimit(period, measure string) int {
	per := map[string]int{"day": 1, "week": 7, "month": 31, "total": 0}[period]
	switch measure {
	case "words":
		if period == "total" {
			return 10_000_000
		}
		return 100_000 * per
	case "sessions":
		if period == "total" {
			return 100_000
		}
		return 100 * per
	case "minutes":
		if period == "total" {
			return 10_000_000
		}
		return 1440 * per
	case "chapters":
		if period == "total" {
			return 5000
		}
		return 50 * per
	}
	return 0
}

func oneOf(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

var (
	periods  = []string{"day", "week", "month", "total"}
	measures = []string{"words", "sessions", "chapters", "minutes"}
)

// GoalCreateIn makes a goal.
type GoalCreateIn struct {
	Period    string `json:"period" enum:"day,week,month,total" desc:"day, week (Monday to Sunday), month, or total: a number to reach, with an optional deadline."`
	Measure   string `json:"measure" enum:"words,sessions,chapters,minutes" desc:"words: words added (never net). sessions: writing sessions. chapters: chapters set to done for the first time. minutes: active writing time. A total goal in words is measured against the words in the manuscript."`
	Target    int    `json:"target" desc:"At least 1. Words: up to 100,000 a day, seven times that a week, 31 times a month, 10,000,000 in total. Sessions, minutes and chapters have their own bounds, shown in the error."`
	ProjectID string `json:"project_id,omitempty" desc:"Only this project's. Empty: every project the person can read, except any locked by a passphrase in the session."`
	Deadline  string `json:"deadline,omitempty" desc:"A total goal's deadline, YYYY-MM-DD, today or later."`
}

// Validate checks the shape; the person's own state is checked in the op.
func (in *GoalCreateIn) Validate() error {
	if !oneOf(periods, in.Period) {
		return ops.Invalid("period", "A goal is for a day, a week, a month or in total.")
	}
	if !oneOf(measures, in.Measure) {
		return ops.Invalid("measure", "A goal counts words, sessions, chapters or minutes.")
	}
	if lim := targetLimit(in.Period, in.Measure); in.Target < 1 || in.Target > lim {
		return ops.Invalid("target", fmt.Sprintf("The target must be between 1 and %d.", lim))
	}
	if in.ProjectID != "" && !db.ValidID(in.ProjectID) {
		return ops.Invalid("project_id", "That is not a project id.")
	}
	return checkDeadline(in.Period, in.Deadline)
}

func checkDeadline(period, deadline string) error {
	if deadline == "" {
		return nil
	}
	if period != "total" {
		return ops.Invalid("deadline", "Only a total goal has a deadline.")
	}
	if _, ok := parseDay(deadline); !ok {
		return ops.Invalid("deadline", "Use a date as YYYY-MM-DD, or leave it empty.")
	}
	return nil
}

// checkGoalID refuses what cannot be one of the caller's studio goals. A goal set on a project's Goals page is read here and
// changed there.
func checkGoalID(id string) error {
	if strings.HasPrefix(id, "project:") {
		return ops.Invalid("id", "A goal set on a project's Goals page is changed there.")
	}
	if !db.ValidID(id) || !strings.HasPrefix(id, "sgl_") {
		return ops.Invalid("id", "That is not a goal id.")
	}
	return nil
}

// GoalItem wraps a goal.
type GoalItem struct {
	Item GoalOut `json:"item"`
}

// progressAt is the person's progress in the current period of a goal, at this moment: what a new version records.
func (s *snapshot) progressAt(g Goal) (int, error) {
	if g.Period == "total" {
		return 0, nil
	}
	days, err := s.scope(g)
	if err != nil {
		return 0, err
	}
	from, to := periodOf(g.Period, s.v.Today)
	return measureOver(days, g.Measure, from, min(to, s.v.Today)), nil
}

func goalCreate(c *ops.Ctx, in GoalCreateIn) (GoalItem, error) {
	var out GoalOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		v, err := newViewerOn(c, tx)
		if err != nil {
			return err
		}
		if in.Deadline != "" && in.Deadline < v.Today {
			return ops.Invalid("deadline", "The deadline has already passed.")
		}
		if in.ProjectID != "" && !v.sees(in.ProjectID) {
			return ops.NotFound("project") // not yours, locked, or in the trash: one answer
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM studio_goals WHERE user_id = ? AND archived_at = ''`, v.UserID).Scan(&n); err != nil {
			return err
		}
		if n >= maxActiveGoals {
			return ops.ConflictWith(fmt.Sprintf("You have %d goals already. Archive one to make room.", maxActiveGoals), map[string]any{"reason": "too_many_goals"})
		}
		var dup string
		err = tx.QueryRow(`SELECT id FROM studio_goals WHERE user_id = ? AND project_id = ? AND period = ? AND measure = ? AND archived_at = ''`,
			v.UserID, in.ProjectID, in.Period, in.Measure).Scan(&dup)
		if err == nil {
			return ops.ConflictWith("You already have that goal. Change its target instead.", map[string]any{"reason": "goal_exists", "goal_id": dup})
		}
		if !isNoRows(err) {
			return err
		}
		s, err := v.snapshot()
		if err != nil {
			return err
		}
		g := Goal{ID: db.NewID("sgl"), ProjectID: in.ProjectID, Period: in.Period, Measure: in.Measure, Start: v.Today}
		at, err := s.progressAt(g)
		if err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.Exec(`INSERT INTO studio_goals(id, user_id, project_id, period, measure, start_date, created_at) VALUES (?,?,?,?,?,?,?)`,
			g.ID, v.UserID, g.ProjectID, g.Period, g.Measure, g.Start, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO studio_goal_versions(goal_id, version, target, deadline, from_date, progress_at, created_at) VALUES (?,?,?,?,?,?,?)`,
			g.ID, 1, in.Target, in.Deadline, v.Today, at, now); err != nil {
			return err
		}
		g.Versions = []Version{{N: 1, Target: in.Target, Deadline: in.Deadline, From: v.Today, ProgressAt: at}}
		s.goals = append(s.goals, g)
		out, err = s.evalGoal(g)
		return err
	})
	return GoalItem{Item: out}, err
}

// GoalListIn lists goals.
type GoalListIn struct {
	Archived bool `query:"archived" desc:"Include archived goals, last."`
}

func goalList(c *ops.Ctx, in GoalListIn) (GoalsOut, error) {
	v, err := newViewer(c)
	if err != nil {
		return GoalsOut{}, err
	}
	s, err := v.snapshot()
	if err != nil {
		return GoalsOut{}, err
	}
	out, err := s.goalsOut(in.Archived)
	s.persistCompletions()
	return out, err
}

// GoalUpdateIn changes a goal's target or a total goal's deadline.
type GoalUpdateIn struct {
	ID       string  `path:"id" json:"-" desc:"Goal id."`
	Target   *int    `json:"target,omitempty" desc:"The new target. The goal gets a new version: periods already over are judged against the target they had, and a period that had already been met stays met."`
	Deadline *string `json:"deadline,omitempty" desc:"A total goal's new deadline, YYYY-MM-DD; an empty string removes it."`
}

// Validate checks what was sent.
func (in *GoalUpdateIn) Validate() error {
	if err := checkGoalID(in.ID); err != nil {
		return err
	}
	if in.Target == nil && in.Deadline == nil {
		return ops.Invalid("target", "Send a new target or a new deadline.")
	}
	if in.Target != nil && *in.Target < 1 {
		return ops.Invalid("target", "The target must be at least 1.")
	}
	if in.Deadline != nil && *in.Deadline != "" {
		if _, ok := parseDay(*in.Deadline); !ok {
			return ops.Invalid("deadline", "Use a date as YYYY-MM-DD, or an empty string for none.")
		}
	}
	return nil
}

// mine loads one goal of the caller's, or says there is none.
func mine(tx querier, c *ops.Ctx, id string) (Goal, error) {
	all, err := loadGoals(c, tx, c.P.UserID)
	if err != nil {
		return Goal{}, err
	}
	for _, g := range all {
		if g.ID == id {
			return g, nil
		}
	}
	return Goal{}, ops.NotFound("goal")
}

func goalUpdate(c *ops.Ctx, in GoalUpdateIn) (GoalItem, error) {
	var out GoalOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		g, err := mine(tx, c, in.ID)
		if err != nil {
			return err
		}
		if g.EndedOn != "" {
			return ops.Conflict("This goal is archived. Make a new one.")
		}
		cur := g.current()
		target, deadline := cur.Target, cur.Deadline
		if in.Target != nil {
			if lim := targetLimit(g.Period, g.Measure); *in.Target > lim {
				return ops.Invalid("target", fmt.Sprintf("The target must be between 1 and %d.", lim))
			}
			target = *in.Target
		}
		v, err := newViewerOn(c, tx)
		if err != nil {
			return err
		}
		if in.Deadline != nil {
			if err := checkDeadline(g.Period, *in.Deadline); err != nil {
				return err
			}
			if *in.Deadline != "" && *in.Deadline < v.Today {
				return ops.Invalid("deadline", "The deadline has already passed.")
			}
			deadline = *in.Deadline
		}
		if g.ProjectID != "" && !v.sees(g.ProjectID) {
			return ops.NotFound("goal") // its project is locked, in the trash or no longer the person's
		}
		s, err := v.snapshot()
		if err != nil {
			return err
		}
		if target != cur.Target || deadline != cur.Deadline {
			at, err := s.progressAt(g)
			if err != nil {
				return err
			}
			nv := Version{N: cur.N + 1, Target: target, Deadline: deadline, From: v.Today, ProgressAt: at}
			if _, err := tx.Exec(`INSERT INTO studio_goal_versions(goal_id, version, target, deadline, from_date, progress_at, created_at) VALUES (?,?,?,?,?,?,?)`,
				g.ID, nv.N, nv.Target, nv.Deadline, nv.From, nv.ProgressAt, db.Now()); err != nil {
				return err
			}
			g.Versions = append(g.Versions, nv)
			if nv.Target > cur.Target && g.CompletedOn != "" {
				// Raising the target of a goal that was reached is a new aim, so it is active again; only the person raising it
				// does that. A cut never does.
				if _, err := tx.Exec(`UPDATE studio_goals SET completed_on = '' WHERE id = ? AND user_id = ?`, g.ID, v.UserID); err != nil {
					return err
				}
				g.CompletedOn = ""
			}
			for i := range s.goals {
				if s.goals[i].ID == g.ID {
					s.goals[i] = g
				}
			}
		}
		out, err = s.evalGoal(g)
		return err
	})
	return GoalItem{Item: out}, err
}

// GoalArchiveIn ends a goal.
type GoalArchiveIn struct {
	ID string `path:"id" json:"-" desc:"Goal id."`
}

// Validate checks the id.
func (in *GoalArchiveIn) Validate() error { return checkGoalID(in.ID) }

func goalArchive(c *ops.Ctx, in GoalArchiveIn) (GoalItem, error) {
	var out GoalOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		g, err := mine(tx, c, in.ID)
		if err != nil {
			return err
		}
		v, err := newViewerOn(c, tx)
		if err != nil {
			return err
		}
		if g.ProjectID != "" && !v.sees(g.ProjectID) {
			return ops.NotFound("goal") // its project is locked, in the trash or no longer the person's
		}
		if g.EndedOn == "" {
			// The date is all that is stored. The last day the goal applied is worked out when it is read, for the reader: the
			// date if its period was met by what they may count, else the day before (settleEnd).
			if _, err := tx.Exec(`UPDATE studio_goals SET ended_on = ?, archived_at = ? WHERE id = ? AND user_id = ?`, v.Today, db.Now(), g.ID, v.UserID); err != nil {
				return err
			}
		}
		s, err := v.snapshot()
		if err != nil {
			return err
		}
		for _, x := range s.goals {
			if x.ID == g.ID {
				out, err = s.evalGoal(x)
				return err
			}
		}
		return ops.NotFound("goal")
	})
	return GoalItem{Item: out}, err
}

// GoalHistoryIn asks for a goal's record.
type GoalHistoryIn struct {
	ID      string `path:"id" json:"-" desc:"Goal id."`
	Periods int    `query:"periods" desc:"How many periods back, 1 to 200. Default 12."`
}

// VersionOut is one target a goal has had.
type VersionOut struct {
	Version  int    `json:"version"`
	Target   int    `json:"target"`
	Deadline string `json:"deadline,omitempty"`
	From     string `json:"from" desc:"The local date it took effect."`
}

// PeriodRow is one past period of a goal, judged against the target in force then.
type PeriodRow struct {
	Judged
	Net *int `json:"net,omitempty" desc:"Words goals: the net words in the period, beside the words added and never against them."`
}

// GoalHistoryOut is a goal's versions and its past periods.
type GoalHistoryOut struct {
	Goal     GoalOut      `json:"goal"`
	Versions []VersionOut `json:"versions" desc:"Oldest first."`
	Periods  []PeriodRow  `json:"periods" desc:"Oldest first, ending with the current period. Each is judged against the version in force in it; a period met under a target that was later raised is still met. A period that was not met is not marked as anything but not met."`
}

// Validate checks the id and the count.
func (in *GoalHistoryIn) Validate() error { return checkGoalID(in.ID) }

func goalHistory(c *ops.Ctx, in GoalHistoryIn) (GoalHistoryOut, error) {
	n := in.Periods
	if n == 0 {
		n = 12
	}
	if n < 1 || n > 200 {
		return GoalHistoryOut{}, ops.Invalid("periods", "Ask for between 1 and 200 periods.")
	}
	v, err := newViewer(c)
	if err != nil {
		return GoalHistoryOut{}, err
	}
	s, err := v.snapshot()
	if err != nil {
		return GoalHistoryOut{}, err
	}
	for _, g := range s.goals {
		if g.ID != in.ID {
			continue
		}
		out := GoalHistoryOut{Versions: []VersionOut{}, Periods: []PeriodRow{}}
		if out.Goal, err = s.evalGoal(g); err != nil {
			return out, err
		}
		for _, x := range g.Versions {
			out.Versions = append(out.Versions, VersionOut{Version: x.N, Target: x.Target, Deadline: x.Deadline, From: x.From})
		}
		if g.Period == "total" {
			return out, nil
		}
		days, err := s.scope(g)
		if err != nil {
			return out, err
		}
		last := v.Today
		if g.EndedOn != "" && g.EndedOn < last {
			last = g.EndedOn
		}
		var rows []PeriodRow
		for cursor := last; len(rows) < n; {
			from, to := periodOf(g.Period, cursor)
			if to < g.Start {
				break
			}
			upto := min(to, last)
			value := measureOver(days, g.Measure, from, upto)
			row := PeriodRow{Judged: g.judge(from, to, value)}
			if g.Measure == "words" {
				net := days.sum(from, upto).Net
				row.Net = &net
			}
			rows = append(rows, row)
			cursor = addDays(from, -1)
		}
		for i := len(rows) - 1; i >= 0; i-- {
			out.Periods = append(out.Periods, rows[i])
		}
		return out, nil
	}
	return GoalHistoryOut{}, ops.NotFound("goal")
}

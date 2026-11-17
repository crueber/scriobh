package goals

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Kinds a goal can have in v1. The table also admits weekly, monthly and
// document for later; nothing writes them yet.
var Kinds = []string{"daily", "project"}

const (
	maxDaily   = 100_000
	maxProject = 10_000_000

	// PaceWindow is how many days of history the pace and the projected finish
	// are read from.
	PaceWindow = 28
	// MinWritingDays is how many days with words in the pace window a projection
	// needs. With fewer there is no honest forecast, so none is given.
	MinWritingDays = 5

	maxHistory = 366
)

// Goal is one goal of the caller's.
type Goal struct {
	ID          string `json:"id"`
	Kind        string `json:"kind" enum:"daily,project"`
	TargetWords int    `json:"target_words"`
	StartDate   string `json:"start_date" desc:"The local date the goal was set."`
	EndDate     string `json:"end_date" desc:"Last local date the goal applies, or empty."`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// ---- inputs and outputs --------------------------------------------------------

// ListIn lists the caller's goals on a project.
type ListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

func (in ListIn) ProjectRef() string { return in.ProjectID }

// ListOut is the caller's goals plus the project's shared target.
type ListOut struct {
	Items []Goal `json:"items" desc:"The caller's own goals on this project: at most one daily and one project goal."`
	// WordGoal and Deadline are the project's shared target, set with project.update.
	WordGoal int    `json:"project_word_goal" desc:"The project's shared word goal (project.update), 0 for none."`
	Deadline string `json:"project_deadline" desc:"The project's shared deadline, YYYY-MM-DD, or empty."`
}

// SetIn creates or replaces one of the caller's goals.
type SetIn struct {
	ProjectID   string `path:"id" json:"-" desc:"Project id."`
	Kind        string `json:"kind" enum:"daily,project" desc:"daily: words added per day. project: the caller's own total-word target for the whole project (the shared target is set with project.update)."`
	TargetWords int    `json:"target_words" desc:"Words. Daily up to 100,000; project up to 10,000,000."`
	EndDate     string `json:"end_date,omitempty" desc:"Optional YYYY-MM-DD. For a project goal, the deadline; for a daily goal, the last day it applies."`
}

func (in SetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the goal.
func (in *SetIn) Validate() error {
	if !oneOf(Kinds, in.Kind) {
		return ops.Invalid("kind", "A goal is daily or project.")
	}
	limit := maxDaily
	if in.Kind == "project" {
		limit = maxProject
	}
	if in.TargetWords < 1 || in.TargetWords > limit {
		return ops.Invalid("target_words", fmt.Sprintf("The target must be between 1 and %d words.", limit))
	}
	if in.EndDate != "" {
		t, err := time.Parse(dayLayout, in.EndDate)
		if err != nil || t.Format(dayLayout) != in.EndDate {
			return ops.Invalid("end_date", "Use a date as YYYY-MM-DD, or leave it empty.")
		}
	}
	return nil
}

// Item wraps one goal.
type Item struct {
	Item Goal `json:"item"`
}

// DeleteIn removes one of the caller's goals.
type DeleteIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Kind      string `query:"kind" desc:"daily or project." enum:"daily,project"`
}

func (in DeleteIn) ProjectRef() string { return in.ProjectID }

// Validate checks the kind.
func (in *DeleteIn) Validate() error {
	if !oneOf(Kinds, in.Kind) {
		return ops.Invalid("kind", "A goal is daily or project.")
	}
	return nil
}

// DeleteOut says whether there was a goal to remove.
type DeleteOut struct {
	Deleted bool `json:"deleted"`
}

// StatsIn names a project for the caller's own statistics.
type StatsIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

func (in StatsIn) ProjectRef() string { return in.ProjectID }

// TodayOut is the caller's day so far.
type TodayOut struct {
	Day           string `json:"day" desc:"The caller's local date, YYYY-MM-DD."`
	Timezone      string `json:"timezone"`
	WordsAdded    int    `json:"words_added" desc:"Words added today. This is what the goal counts."`
	WordsNet      int    `json:"words_net" desc:"New words minus old words today; negative on a day of cutting. Shown beside the goal, never against it."`
	Minutes       int    `json:"minutes" desc:"Active writing time, from the gaps between saves."`
	Sessions      int    `json:"sessions"`
	HasGoal       bool   `json:"has_goal"`
	Target        int    `json:"target" desc:"Today's daily target, 0 when there is no goal."`
	Remaining     int    `json:"remaining" desc:"Words still to add to meet the target, 0 once met."`
	Hit           bool   `json:"hit"`
	Streak        int    `json:"streak" desc:"Consecutive days the goal was met, counting today once it is met. Today does not break it until the day ends."`
	LongestStreak int    `json:"longest_streak"`
}

// HistoryIn asks for a run of days.
type HistoryIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Days      int    `query:"days" desc:"How many days ending today, 1 to 366. Default 30."`
}

func (in HistoryIn) ProjectRef() string { return in.ProjectID }

// DayStat is one day of the caller's writing.
type DayStat struct {
	Day        string `json:"day"`
	WordsAdded int    `json:"words_added"`
	WordsNet   int    `json:"words_net"`
	Minutes    int    `json:"minutes"`
	Target     int    `json:"target" desc:"The daily target in force that day, 0 for none."`
	Hit        bool   `json:"hit"`
}

// HistoryOut is a dense run of days, oldest first, for a calendar or heatmap.
// Days with no writing are present with zeros; nothing marks them as missed.
type HistoryOut struct {
	From         string    `json:"from"`
	To           string    `json:"to"`
	Days         []DayStat `json:"days"`
	WritingDays  int       `json:"writing_days" desc:"Days in the range on which words were added."`
	TotalAdded   int       `json:"total_added"`
	TotalNet     int       `json:"total_net"`
	TotalMinutes int       `json:"total_minutes"`
}

// ProjectOut is progress toward the project's total, with pace.
type ProjectOut struct {
	Words        int    `json:"words" desc:"Words in the project's live documents."`
	Target       int    `json:"target" desc:"The target words are measured against: the caller's own project goal if set, else the project's shared word goal; 0 for none."`
	TargetSource string `json:"target_source" enum:",goal,project" desc:"goal: the caller's own; project: the shared word goal; empty: none."`
	Remaining    int    `json:"remaining"`
	Percent      int    `json:"percent" desc:"Words as a percentage of the target; may exceed 100."`
	Reached      bool   `json:"reached"`

	Deadline       string `json:"deadline" desc:"The caller's own deadline if set, else the project's; empty for none."`
	DaysLeft       int    `json:"days_left" desc:"Days from today to the deadline, counting today; 0 when there is no deadline or it has passed."`
	RequiredPerDay int    `json:"required_per_day" desc:"Net words a day to reach the target by the deadline; 0 when there is none to reach."`

	PaceWindowDays int    `json:"pace_window_days"`
	WritingDays    int    `json:"writing_days" desc:"Days with words added in the pace window."`
	AvgAddedPerDay int    `json:"avg_added_per_day"`
	AvgNetPerDay   int    `json:"avg_net_per_day"`
	ProjectedDate  string `json:"projected_date,omitempty" desc:"When the target would be reached at the recent net pace. Present only when the pace window holds enough writing days to say; absent is the honest answer otherwise."`
}

// RecomputeResult says what a recompute rewrote.
type RecomputeResult struct {
	Days int `json:"days"`
}

// ---- registration -----------------------------------------------------------------

// Register adds the goal and statistics ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "goal.list", Method: "GET", Path: "/api/v1/projects/{id}/goals",
		Desc: "List the caller's own goals on a project, with the project's shared word goal and deadline.",
		Auth: ops.RequireProject(ops.ProjectReader)}, list)
	ops.Register(r, ops.Op{Name: "goal.set", Method: "PUT", Path: "/api/v1/projects/{id}/goals",
		Desc: "Set one of the caller's own goals on a project: a daily words-added target, or a total-word target with an optional deadline. Replaces the caller's goal of that kind. Goals count words added; cutting never counts against them.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, set)
	ops.Register(r, ops.Op{Name: "goal.delete", Permanent: true, Method: "DELETE", Path: "/api/v1/projects/{id}/goals",
		Desc: "Remove one of the caller's own goals (kind=daily or kind=project). Days already met stay met.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, remove)
	ops.Register(r, ops.Op{Name: "stats.today", Method: "GET", Path: "/api/v1/projects/{id}/stats/today",
		Desc: "The caller's day so far on a project: words added and net, minutes, the daily target, whether it is met, and the streak. Per person: other members' numbers are never visible.",
		Auth: ops.RequireProject(ops.ProjectReader)}, todayOp)
	ops.Register(r, ops.Op{Name: "stats.history", Method: "GET", Path: "/api/v1/projects/{id}/stats/history",
		Desc: "The caller's last N days on a project, oldest first, one entry per day including quiet days, for a calendar or heatmap.",
		Auth: ops.RequireProject(ops.ProjectReader)}, history)
	ops.Register(r, ops.Op{Name: "stats.project", Method: "GET", Path: "/api/v1/projects/{id}/stats/project",
		Desc: "Progress toward the project's total words: the target, the words per day pace, the words per day needed for a deadline, and a projected finish date only when there is enough recent writing to project from.",
		Auth: ops.RequireProject(ops.ProjectReader)}, projectStats)
}

func oneOf(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ---- goals ----------------------------------------------------------------------------

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func scanGoals(rows *sql.Rows) ([]Goal, error) {
	defer rows.Close()
	out := []Goal{}
	for rows.Next() {
		var g Goal
		if err := rows.Scan(&g.ID, &g.Kind, &g.TargetWords, &g.StartDate, &g.EndDate, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

const goalCols = `id, kind, target_words, start_date, end_date, created_at, updated_at`

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT `+goalCols+` FROM goals WHERE user_id = ? AND project_id = ? ORDER BY kind`, c.P.UserID, in.ProjectID)
	if err != nil {
		return ListOut{}, err
	}
	items, err := scanGoals(rows)
	if err != nil {
		return ListOut{}, err
	}
	out := ListOut{Items: items}
	err = c.DB().Read().QueryRowContext(c, `SELECT word_goal, COALESCE(deadline,'') FROM projects WHERE id = ?`, in.ProjectID).Scan(&out.WordGoal, &out.Deadline)
	return out, err
}

// userDay is the caller's local date now and the zone it was read in.
func userDay(ctx context.Context, q querier, userID string) (day, zone string, err error) {
	var name string
	if err := q.QueryRowContext(ctx, `SELECT timezone FROM users WHERE id = ?`, userID).Scan(&name); err != nil {
		return "", "", err
	}
	loc, zone := location(name)
	return db.NowTime().In(loc).Format(dayLayout), zone, nil
}

func set(c *ops.Ctx, in SetIn) (Item, error) {
	var out Goal
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		today, _, err := userDay(c, tx, c.P.UserID)
		if err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.Exec(`INSERT INTO goals(id, user_id, project_id, kind, target_words, basis, start_date, end_date, created_at, updated_at)
			VALUES (?,?,?,?,?,'added',?,?,?,?)
			ON CONFLICT(user_id, project_id, kind) WHERE kind IN ('daily','project') DO UPDATE SET
			  target_words = excluded.target_words, end_date = excluded.end_date, updated_at = excluded.updated_at`,
			db.NewID("gol"), c.P.UserID, in.ProjectID, in.Kind, in.TargetWords, today, in.EndDate, now, now); err != nil {
			return err
		}
		if in.Kind == "daily" {
			if err := retargetToday(tx, c.P.UserID, in.ProjectID, today, in.TargetWords, in.EndDate); err != nil {
				return err
			}
		}
		g, err := scanGoals2(tx.QueryContext(c, `SELECT `+goalCols+` FROM goals WHERE user_id = ? AND project_id = ? AND kind = ?`, c.P.UserID, in.ProjectID, in.Kind))
		if err != nil {
			return err
		}
		if len(g) != 1 {
			return errors.New("goals: the goal just written was not found")
		}
		out = g[0]
		return nil
	})
	return Item{Item: out}, err
}

func scanGoals2(rows *sql.Rows, err error) ([]Goal, error) {
	if err != nil {
		return nil, err
	}
	return scanGoals(rows)
}

// retargetToday points today's totals at the daily target in force now. A day
// already hit is left alone: raising the goal never takes a finished day away.
func retargetToday(tx *sql.Tx, userID, projectID, today string, target int, endDate string) error {
	if endDate != "" && endDate < today {
		target = 0
	}
	_, err := tx.Exec(`UPDATE daily_stats SET goal_target = ?1,
		goal_hit = CASE WHEN ?1 > 0 AND words_added >= ?1 THEN 1 ELSE 0 END, updated_at = ?2
		WHERE user_id = ?3 AND project_id = ?4 AND day = ?5 AND goal_hit = 0`,
		target, db.Now(), userID, projectID, today)
	return err
}

func remove(c *ops.Ctx, in DeleteIn) (DeleteOut, error) {
	var deleted bool
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM goals WHERE user_id = ? AND project_id = ? AND kind = ?`, c.P.UserID, in.ProjectID, in.Kind)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		deleted = n > 0
		if deleted && in.Kind == "daily" {
			today, _, err := userDay(c, tx, c.P.UserID)
			if err != nil {
				return err
			}
			return retargetToday(tx, c.P.UserID, in.ProjectID, today, 0, "")
		}
		return nil
	})
	return DeleteOut{Deleted: deleted}, err
}

// ---- statistics -------------------------------------------------------------------------

func hitDays(ctx context.Context, q querier, userID, projectID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT day FROM daily_stats WHERE user_id = ? AND project_id = ? AND goal_hit = 1 ORDER BY day`, userID, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		days = append(days, d)
	}
	return days, rows.Err()
}

func todayOp(c *ops.Ctx, in StatsIn) (TodayOut, error) {
	rd := c.DB().Read()
	day, zone, err := userDay(c, rd, c.P.UserID)
	if err != nil {
		return TodayOut{}, err
	}
	out := TodayOut{Day: day, Timezone: zone}
	var hit int
	err = rd.QueryRowContext(c, `SELECT words_added, words_net, minutes, sessions, goal_target, goal_hit FROM daily_stats
		WHERE user_id = ? AND project_id = ? AND day = ?`, c.P.UserID, in.ProjectID, day).
		Scan(&out.WordsAdded, &out.WordsNet, &out.Minutes, &out.Sessions, &out.Target, &hit)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TodayOut{}, err
	}
	out.Hit = hit == 1
	if out.Target == 0 || errors.Is(err, sql.ErrNoRows) {
		// No row yet, or none with a target: read the goal itself.
		var t int
		e := rd.QueryRowContext(c, `SELECT target_words FROM goals WHERE user_id = ? AND project_id = ? AND kind = 'daily'
			AND (end_date = '' OR end_date >= ?)`, c.P.UserID, in.ProjectID, day).Scan(&t)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return TodayOut{}, e
		}
		if out.Target == 0 {
			out.Target = t
		}
	}
	out.HasGoal = out.Target > 0
	out.Remaining = max(0, out.Target-out.WordsAdded)
	days, err := hitDays(c, rd, c.P.UserID, in.ProjectID)
	if err != nil {
		return TodayOut{}, err
	}
	out.Streak, out.LongestStreak = Streaks(days, day)
	return out, nil
}

func history(c *ops.Ctx, in HistoryIn) (HistoryOut, error) {
	n := in.Days
	switch {
	case n == 0:
		n = 30
	case n < 1 || n > maxHistory:
		return HistoryOut{}, ops.Invalid("days", fmt.Sprintf("Ask for between 1 and %d days.", maxHistory))
	}
	rd := c.DB().Read()
	day, _, err := userDay(c, rd, c.P.UserID)
	if err != nil {
		return HistoryOut{}, err
	}
	return historyFor(c, rd, c.P.UserID, in.ProjectID, day, n)
}

func parseDay(s string) time.Time {
	t, _ := time.Parse(dayLayout, s)
	return t
}

func historyFor(ctx context.Context, q querier, userID, projectID, today string, n int) (HistoryOut, error) {
	end := parseDay(today)
	start := end.AddDate(0, 0, -(n - 1))
	out := HistoryOut{From: start.Format(dayLayout), To: today, Days: make([]DayStat, 0, n)}
	rows, err := q.QueryContext(ctx, `SELECT day, words_added, words_net, minutes, goal_target, goal_hit FROM daily_stats
		WHERE user_id = ? AND project_id = ? AND day >= ? AND day <= ?`, userID, projectID, out.From, out.To)
	if err != nil {
		return HistoryOut{}, err
	}
	defer rows.Close()
	have := map[string]DayStat{}
	for rows.Next() {
		var d DayStat
		var hit int
		if err := rows.Scan(&d.Day, &d.WordsAdded, &d.WordsNet, &d.Minutes, &d.Target, &hit); err != nil {
			return HistoryOut{}, err
		}
		d.Hit = hit == 1
		have[d.Day] = d
	}
	if err := rows.Err(); err != nil {
		return HistoryOut{}, err
	}
	for t := start; !t.After(end); t = t.AddDate(0, 0, 1) {
		k := t.Format(dayLayout)
		d, ok := have[k]
		if !ok {
			d = DayStat{Day: k}
		}
		out.Days = append(out.Days, d)
		if d.WordsAdded > 0 {
			out.WritingDays++
		}
		out.TotalAdded += d.WordsAdded
		out.TotalNet += d.WordsNet
		out.TotalMinutes += d.Minutes
	}
	return out, nil
}

func projectStats(c *ops.Ctx, in StatsIn) (ProjectOut, error) {
	rd := c.DB().Read()
	var out ProjectOut
	var shared int
	var sharedDeadline string
	if err := rd.QueryRowContext(c, `SELECT word_goal, COALESCE(deadline,'') FROM projects WHERE id = ?`, in.ProjectID).Scan(&shared, &sharedDeadline); err != nil {
		return ProjectOut{}, err
	}
	// The same total project.get reports: live documents of the project.
	if err := rd.QueryRowContext(c, `SELECT COALESCE(SUM(word_count),0) FROM documents WHERE project_id = ? AND deleted_at IS NULL`, in.ProjectID).Scan(&out.Words); err != nil {
		return ProjectOut{}, err
	}
	var own int
	var ownEnd string
	err := rd.QueryRowContext(c, `SELECT target_words, end_date FROM goals WHERE user_id = ? AND project_id = ? AND kind = 'project'`, c.P.UserID, in.ProjectID).Scan(&own, &ownEnd)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ProjectOut{}, err
	}
	switch {
	case own > 0:
		out.Target, out.TargetSource = own, "goal"
	case shared > 0:
		out.Target, out.TargetSource = shared, "project"
	}
	out.Deadline = sharedDeadline
	if ownEnd != "" {
		out.Deadline = ownEnd
	}
	day, _, err := userDay(c, rd, c.P.UserID)
	if err != nil {
		return ProjectOut{}, err
	}
	h, err := historyFor(c, rd, c.P.UserID, in.ProjectID, day, PaceWindow)
	if err != nil {
		return ProjectOut{}, err
	}
	out.PaceWindowDays = PaceWindow
	out.WritingDays = h.WritingDays
	out.AvgAddedPerDay = int(math.Round(float64(h.TotalAdded) / PaceWindow))
	out.AvgNetPerDay = int(math.Round(float64(h.TotalNet) / PaceWindow))
	if out.Target > 0 {
		out.Remaining = max(0, out.Target-out.Words)
		out.Percent = int(int64(out.Words) * 100 / int64(out.Target))
		out.Reached = out.Words >= out.Target
	}
	today := parseDay(day)
	if out.Deadline != "" && out.Remaining > 0 {
		if dl, err := time.Parse(dayLayout, out.Deadline); err == nil && !dl.Before(today) {
			out.DaysLeft = int(dl.Sub(today)/(24*time.Hour)) + 1
			out.RequiredPerDay = (out.Remaining + out.DaysLeft - 1) / out.DaysLeft
		}
	}
	// A projection needs a recent pace to read from: enough writing days, and
	// words that are moving the total up (net), not only churning it.
	if out.Remaining > 0 && h.WritingDays >= MinWritingDays && h.TotalNet > 0 {
		perDay := float64(h.TotalNet) / PaceWindow
		daysToGo := int(math.Ceil(float64(out.Remaining) / perDay))
		if daysToGo <= 3650 {
			out.ProjectedDate = today.AddDate(0, 0, daysToGo).Format(dayLayout)
		}
	}
	return out, nil
}

package studio

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/ops"
)

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Viewer is whose numbers are being read, which projects they may count, and what day it is for them.
//
// The one rule that matters: IDs is the list of projects whose numbers may appear. Every figure of the studio is
// summed from per-project rows over exactly this list, never from a stored person-level total, so a project that is
// locked, in the trash, or no longer the person's contributes nothing to any aggregate, not a word, a day or a goal.
type Viewer struct {
	Ctx    context.Context
	Q      querier
	UserID string
	IDs    []string
	Today  string
	Zone   string
	Loc    *time.Location

	// Hidden counts the projects left out because a passphrase locks them for this caller.
	Hidden int
	// Partial is true when any project the person has anything in is left out of IDs, for any reason: locked, in the trash,
	// not approved for a key, left, or protected where there is no session to unlock it. A number shown to a viewer is judged
	// only from the projects that viewer's figures include, so for a partial viewer nothing stored from a cross-project sum
	// (a goal version's progress_at, a total goal's completion) is used for a goal over every project.
	Partial bool
	// db is the database a read op may use for the one write it makes (a total goal's completion, once). Nil for the card
	// and the email, which write nothing.
	db *db.DB

	scoped map[string]Days
}

// newViewer reads the caller's own numbers. A project the caller has not unlocked (or that a key may not reach) is
// left out through the passphrase gate, exactly as the lists that span projects leave it out.
func newViewer(c *ops.Ctx) (*Viewer, error) { return newViewerOn(c, c.DB().Read()) }

// newViewerOn is newViewer reading through q (a transaction, for a write that needs the same view).
func newViewerOn(c *ops.Ctx, q querier) (*Viewer, error) {
	v := &Viewer{Ctx: c, Q: q, UserID: c.P.UserID, db: c.DB()}
	if err := v.zone(); err != nil {
		return nil, err
	}
	ids, err := memberIDs(c, q, v.UserID)
	if err != nil {
		return nil, err
	}
	hidden, err := projectgate.LockedProjects(c)
	if err != nil {
		return nil, err
	}
	if len(hidden) > 0 {
		skip := make(map[string]bool, len(hidden))
		for _, id := range hidden {
			skip[id] = true
		}
		keep := ids[:0]
		for _, id := range ids {
			if !skip[id] {
				keep = append(keep, id)
			}
		}
		ids = keep
	}
	v.IDs = ids
	v.Hidden = len(hidden)
	if err := v.settle(); err != nil {
		return nil, err
	}
	return v, nil
}

// NoSessionViewer reads a person's numbers where there is no session to unlock anything: the private card and the
// progress email. A project that has a passphrase at all is left out, whoever has unlocked it, because nothing here
// can know that anyone has.
func NoSessionViewer(ctx context.Context, q querier, userID string) (*Viewer, error) {
	v := &Viewer{Ctx: ctx, Q: q, UserID: userID}
	if err := v.zone(); err != nil {
		return nil, err
	}
	ids, err := memberIDs(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	gated := map[string]bool{}
	rows, err := q.QueryContext(ctx, `SELECT project_id FROM project_gates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		gated[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keep := ids[:0]
	for _, id := range ids {
		if !gated[id] {
			keep = append(keep, id)
		}
	}
	v.Hidden = len(ids) - len(keep)
	v.IDs = keep
	if err := v.settle(); err != nil {
		return nil, err
	}
	return v, nil
}

// settle works out Partial: whether the person has anything the figures leave out. Two things are asked, because a project
// can be left out while the person is still a member (locked, trashed) or after they have stopped being one (left, removed):
// the number of their memberships against the projects counted, and whether any project holding their daily rows is outside
// the counted list. The second is a skip scan, one index seek for each project they have written in. (A project that has
// been purged takes its rows with it, so it cannot be seen here: its words can only remain in a stored progress_at, and the
// project is gone.)
func (v *Viewer) settle() error {
	var members int
	if err := v.Q.QueryRowContext(v.Ctx, `SELECT count(*) FROM project_members WHERE user_id = ?`, v.UserID).Scan(&members); err != nil {
		return err
	}
	if members > len(v.IDs) {
		v.Partial = true
		return nil
	}
	rows, err := v.Q.QueryContext(v.Ctx, `WITH RECURSIVE p(id) AS (
		SELECT min(project_id) FROM daily_stats WHERE user_id = ?1
		UNION ALL SELECT (SELECT min(project_id) FROM daily_stats WHERE user_id = ?1 AND project_id > p.id) FROM p WHERE id IS NOT NULL)
		SELECT id FROM p WHERE id IS NOT NULL`, v.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		if !v.sees(id) {
			v.Partial = true
			return nil
		}
	}
	return rows.Err()
}

func (v *Viewer) zone() error {
	var name string
	if err := v.Q.QueryRowContext(v.Ctx, `SELECT timezone FROM users WHERE id = ? AND deleted_at IS NULL`, v.UserID).Scan(&name); err != nil {
		return err
	}
	v.Loc, v.Zone = goals.Location(name)
	v.Today = db.NowTime().In(v.Loc).Format(dayLayout)
	return nil
}

// memberIDs are the live projects the person is a member of: not trashed, not purged. Rows of daily_stats outlive a
// membership, so membership is asked here and not assumed from the rows.
func memberIDs(ctx context.Context, q querier, userID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT p.id FROM project_members m JOIN projects p ON p.id = m.project_id
		WHERE m.user_id = ? AND p.deleted_at IS NULL AND p.trashed_at IS NULL ORDER BY p.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (v *Viewer) sees(projectID string) bool {
	i := sort.SearchStrings(v.IDs, projectID)
	return i < len(v.IDs) && v.IDs[i] == projectID
}

// inList is an SQL list of placeholders for ids, and the arguments.
func inList(ids []string) (string, []any) {
	args := make([]any, len(ids))
	ph := make([]string, len(ids))
	for i, id := range ids {
		args[i], ph[i] = id, "?"
	}
	return "(" + strings.Join(ph, ",") + ")", args
}

// DayData is a person's days and the days a daily goal of the older kind (goal.set) was met.
type DayData struct {
	Days      Days
	LegacyHit map[string]bool
}

// loadDays sums the person's daily rows over the given projects, one entry per local day, from a date on ("" for all
// time). It also reads the chapters finished each day.
func (v *Viewer) loadDays(ids []string, from string) (DayData, error) {
	out := DayData{Days: Days{}, LegacyHit: map[string]bool{}}
	if len(ids) == 0 {
		return out, nil
	}
	list, args := inList(ids)
	a := append([]any{v.UserID}, args...)
	day := ""
	if from != "" {
		day = ` AND day >= ?`
		a = append(a, from)
	}
	rows, err := v.Q.QueryContext(v.Ctx, `SELECT day, SUM(words_added), SUM(words_net), SUM(active_seconds), SUM(sessions), MAX(goal_hit)
		FROM daily_stats WHERE user_id = ? AND project_id IN `+list+day+` GROUP BY day`, a...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var dd string
		var d Day
		var hit int
		if err := rows.Scan(&dd, &d.Words, &d.Net, &d.Seconds, &d.Sessions, &hit); err != nil {
			rows.Close()
			return out, err
		}
		out.Days[dd] = d
		if hit == 1 {
			out.LegacyHit[dd] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	cday := ""
	if from != "" {
		cday = ` AND c.day >= ?`
	}
	crows, err := v.Q.QueryContext(v.Ctx, `SELECT c.day, COUNT(*) FROM studio_chapters_done c JOIN documents d ON d.id = c.document_id AND d.deleted_at IS NULL
		WHERE c.user_id = ? AND c.project_id IN `+list+cday+` GROUP BY c.day`, a...)
	if err != nil {
		return out, err
	}
	defer crows.Close()
	for crows.Next() {
		var dd string
		var n int
		if err := crows.Scan(&dd, &n); err != nil {
			return out, err
		}
		d := out.Days[dd]
		d.Chapters = n
		out.Days[dd] = d
	}
	return out, crows.Err()
}

// projectDays is the person's days on one project they can count, cached for the request.
func (v *Viewer) projectDays(projectID string) (Days, error) {
	if d, ok := v.scoped[projectID]; ok {
		return d, nil
	}
	if !v.sees(projectID) {
		return Days{}, nil
	}
	dd, err := v.loadDays([]string{projectID}, "")
	if err != nil {
		return nil, err
	}
	if v.scoped == nil {
		v.scoped = map[string]Days{}
	}
	v.scoped[projectID] = dd.Days
	return dd.Days, nil
}

// sessions are the person's writing sessions that added words, between two local dates, on the projects they can count.
func (v *Viewer) sessions(from, to string) ([]SessionRow, error) {
	if len(v.IDs) == 0 {
		return nil, nil
	}
	list, args := inList(v.IDs)
	a := append([]any{v.UserID}, args...)
	a = append(a, from, to)
	rows, err := v.Q.QueryContext(v.Ctx, `SELECT started_at, tz, words_added FROM writing_sessions
		WHERE user_id = ? AND project_id IN `+list+` AND day >= ? AND day <= ? AND words_added > 0`, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var at, tz string
		var w int
		if err := rows.Scan(&at, &tz, &w); err != nil {
			return nil, err
		}
		t, err := db.Parse(at)
		if err != nil {
			continue
		}
		out = append(out, SessionRow{Start: t, Zone: tz, Words: w})
	}
	return out, rows.Err()
}

func zoneLoc(name string) *time.Location {
	l, _ := goals.Location(name)
	return l
}

// manuscriptWords is the words in the live documents of the given projects: the total a word-count target is
// measured against, the same figure project.get reports.
func (v *Viewer) manuscriptWords(ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	list, args := inList(ids)
	var n int
	err := v.Q.QueryRowContext(v.Ctx, `SELECT COALESCE(SUM(word_count),0) FROM documents WHERE project_id IN `+list+` AND deleted_at IS NULL`, args...).Scan(&n)
	return n, err
}

// ---- goals -------------------------------------------------------------------------------------

// loadGoals reads the person's studio goals with every version, oldest goal first.
func loadGoals(ctx context.Context, q querier, userID string) ([]Goal, error) {
	rows, err := q.QueryContext(ctx, `SELECT g.id, g.project_id, g.period, g.measure, g.start_date, g.ended_on, g.completed_on, v.version, v.target, v.deadline, v.from_date, v.progress_at
		FROM studio_goals g JOIN studio_goal_versions v ON v.goal_id = g.id WHERE g.user_id = ? ORDER BY g.created_at, g.id, v.version`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		var g Goal
		var v Version
		if err := rows.Scan(&g.ID, &g.ProjectID, &g.Period, &g.Measure, &g.Start, &g.EndedOn, &g.CompletedOn, &v.N, &v.Target, &v.Deadline, &v.From, &v.ProgressAt); err != nil {
			return nil, err
		}
		if n := len(out); n > 0 && out[n-1].ID == g.ID {
			out[n-1].Versions = append(out[n-1].Versions, v)
			continue
		}
		g.Versions = []Version{v}
		out = append(out, g)
	}
	return out, rows.Err()
}

// legacyGoal is a goal of the older kind: the person's daily or total goal on one project, set on the Goals page.
type legacyGoal struct {
	ProjectID string
	Kind      string // daily or project
	Target    int
	Start     string
	End       string
}

func loadLegacy(ctx context.Context, q querier, userID string, ids []string) ([]legacyGoal, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	list, args := inList(ids)
	rows, err := q.QueryContext(ctx, `SELECT project_id, kind, target_words, start_date, end_date FROM goals
		WHERE user_id = ? AND kind IN ('daily','project') AND project_id IN `+list+` ORDER BY project_id, kind`, append([]any{userID}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []legacyGoal
	for rows.Next() {
		var g legacyGoal
		if err := rows.Scan(&g.ProjectID, &g.Kind, &g.Target, &g.Start, &g.End); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// projectInfo is what a screen needs to name a project the person can count.
type projectInfo struct {
	Title, Slug string
}

func (v *Viewer) projectInfos(ids []string) (map[string]projectInfo, error) {
	out := map[string]projectInfo{}
	if len(ids) == 0 {
		return out, nil
	}
	list, args := inList(ids)
	rows, err := v.Q.QueryContext(v.Ctx, `SELECT id, title, slug FROM projects WHERE id IN `+list, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var p projectInfo
		if err := rows.Scan(&id, &p.Title, &p.Slug); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}

// userPref reads one of the person's preferences, the default when unset.
func userPref(ctx context.Context, q querier, userID, key, def string) string {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM user_prefs WHERE user_id = ? AND key = ?`, userID, key).Scan(&v); err != nil {
		return def
	}
	return v
}

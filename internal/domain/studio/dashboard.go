package studio

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/ops"
)

const (
	maxRangeDays    = 3660 // ten years
	maxCalendarWeek = 530
	// MaxNudgesPerDay is the most nudges a person is shown in a day. A nudge shown is recorded, and is not shown again.
	MaxNudgesPerDay = 3
	maxProjectRows  = 20
)

func streaks(days []string, today string) (int, int) { return goals.Streaks(days, today) }

// ---- the range a chart or a pattern reads -------------------------------------------------------------

func (s *snapshot) resolveRange(rng, from, to string) (string, string, string, error) {
	today := s.v.Today
	if from != "" || to != "" {
		if from == "" || to == "" {
			return "", "", "", ops.Invalid("from", "Give both from and to, or neither.")
		}
		if _, ok := parseDay(from); !ok {
			return "", "", "", ops.Invalid("from", "Use a date as YYYY-MM-DD.")
		}
		if _, ok := parseDay(to); !ok {
			return "", "", "", ops.Invalid("to", "Use a date as YYYY-MM-DD.")
		}
		if from > to {
			return "", "", "", ops.Invalid("from", "The start comes after the end.")
		}
		if to > today {
			to = today
		}
		if from > to {
			from = to
		}
		if daysBetween(from, to)+1 > maxRangeDays {
			return "", "", "", ops.Invalid("from", fmt.Sprintf("Ask for at most %d days at a time.", maxRangeDays))
		}
		return "custom", from, to, nil
	}
	switch rng {
	case "", "30d":
		return "30d", addDays(today, -29), today, nil
	case "90d":
		return "90d", addDays(today, -89), today, nil
	case "365d":
		return "365d", addDays(today, -364), today, nil
	case "all":
		f := s.all.Days.first()
		if f == "" || f > today {
			f = addDays(today, -29)
		}
		if daysBetween(f, today)+1 > maxRangeDays {
			f = addDays(today, -(maxRangeDays - 1))
		}
		return "all", f, today, nil
	}
	return "", "", "", ops.Invalid("range", "The range is 30d, 90d, 365d or all.")
}

// SeriesOut is the words-written chart and the patterns read over the same range.
type SeriesOut struct {
	Range            string        `json:"range" enum:"30d,90d,365d,all,custom"`
	From             string        `json:"from"`
	To               string        `json:"to"`
	Bucket           string        `json:"bucket" enum:"day,week,month" desc:"How the range is cut: days up to 92 days, weeks up to 800, months beyond."`
	Points           []Point       `json:"points" desc:"Every bucket of the range, oldest first, with zeros for the empty ones."`
	Figures          Figures       `json:"figures"`
	AvgPerDay        int           `json:"avg_per_day" desc:"Words added over the days of the range, writing days or not: the pace."`
	AvgPerWritingDay int           `json:"avg_per_writing_day"`
	Weekdays         []WeekdayStat `json:"weekdays" desc:"Monday first. Most productive day: best_weekday."`
	BestWeekday      *int          `json:"best_weekday" desc:"0 for Monday to 6 for Sunday, null when nothing was written; the earlier day wins a tie."`
	Hours            []HourStat    `json:"hours" desc:"Words by the local hour a writing session began in. A session is not split across hours."`
	Parts            []PartStat    `json:"parts"`
	BestHour         *int          `json:"best_hour" desc:"Null when nothing was written."`
}

func (s *snapshot) seriesFor(rng, from, to string) (SeriesOut, error) {
	r, f, t, err := s.resolveRange(rng, from, to)
	if err != nil {
		return SeriesOut{}, err
	}
	out := SeriesOut{Range: r, From: f, To: t}
	out.Bucket, out.Points = series(s.all.Days, f, t)
	out.Figures = figuresOf(s.all.Days, f, t)
	span := daysBetween(f, t) + 1
	out.AvgPerDay = roundHalfUp(float64(out.Figures.Words) / float64(span))
	if out.Figures.WritingDays > 0 {
		out.AvgPerWritingDay = roundHalfUp(float64(out.Figures.Words) / float64(out.Figures.WritingDays))
	}
	wd, best := weekdays(s.all.Days, f, t)
	out.Weekdays = wd[:]
	if best >= 0 {
		out.BestWeekday = &best
	}
	rows, err := s.v.sessions(f, t)
	if err != nil {
		return out, err
	}
	hs, parts, bh := hours(rows, zoneLoc)
	out.Hours, out.Parts = hs[:], parts[:]
	if bh >= 0 {
		out.BestHour = &bh
	}
	return out, nil
}

// calendarFor makes the activity calendar of the last n weeks, or of all the person's writing (never fewer than 24 weeks).
func (s *snapshot) calendarFor(n int, all bool) Calendar {
	if all {
		n = 24
		if f := s.all.Days.first(); f != "" {
			n = max(n, daysBetween(weekStart(f), weekStart(s.v.Today))/7+1)
		}
		n = min(n, maxCalendarWeek)
	}
	return calendar(s.all.Days, s.hits, s.v.Today, n, thresholdsOf(s.all.Days))
}

// ---- projects ----------------------------------------------------------------------------------------

// ProjectRow is one project's recent numbers for the person.
type ProjectRow struct {
	ProjectID    string `json:"project_id"`
	Title        string `json:"title"`
	Slug         string `json:"slug"`
	Today        int    `json:"today" desc:"The person's own words added to this project today."`
	Week         int    `json:"week" desc:"This week (Monday to today)."`
	PreviousWeek int    `json:"previous_week"`
	WeekNet      int    `json:"week_net"`
}

// projectRows are the person's own numbers per project for this week and the one before, the projects with any
// words in them first. Only the person's own words: other members' are never visible.
func (s *snapshot) projectRows() ([]ProjectRow, error) {
	v := s.v
	if len(v.IDs) == 0 {
		return []ProjectRow{}, nil
	}
	ws := weekStart(v.Today)
	from := addDays(ws, -7)
	list, args := inList(v.IDs)
	rows, err := v.Q.QueryContext(v.Ctx, `SELECT project_id, day, words_added, words_net FROM daily_stats
		WHERE user_id = ? AND project_id IN `+list+` AND day >= ? AND day <= ?`, append(append([]any{v.UserID}, args...), from, v.Today)...)
	if err != nil {
		return nil, err
	}
	by := map[string]*ProjectRow{}
	for rows.Next() {
		var id, day string
		var w, n int
		if err := rows.Scan(&id, &day, &w, &n); err != nil {
			rows.Close()
			return nil, err
		}
		r := by[id]
		if r == nil {
			r = &ProjectRow{ProjectID: id}
			by[id] = r
		}
		switch {
		case day == v.Today:
			r.Today += w
			r.Week += w
			r.WeekNet += n
		case day >= ws:
			r.Week += w
			r.WeekNet += n
		default:
			r.PreviousWeek += w
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(by))
	for id, r := range by {
		if r.Today+r.Week+r.PreviousWeek > 0 {
			ids = append(ids, id)
		}
	}
	infos, err := v.projectInfos(ids)
	if err != nil {
		return nil, err
	}
	out := []ProjectRow{}
	for _, id := range ids {
		r := by[id]
		r.Title, r.Slug = infos[id].Title, infos[id].Slug
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Week != out[j].Week {
			return out[i].Week > out[j].Week
		}
		if out[i].Title != out[j].Title {
			return out[i].Title < out[j].Title
		}
		return out[i].ProjectID < out[j].ProjectID
	})
	if len(out) > maxProjectRows {
		out = out[:maxProjectRows]
	}
	return out, nil
}

// ---- nudges ------------------------------------------------------------------------------------------

// Nudge is a gentle message the page may show, as a key with its parameters.
type Nudge struct {
	ID     string         `json:"id" desc:"Stable for the day. Send it to studio.nudge.seen once it has been shown, and it is not offered again."`
	Key    string         `json:"key"`
	Params map[string]any `json:"params,omitempty"`
	Kind   string         `json:"kind" enum:"progress,milestone,streak"`
}

var streakMilestones = map[int]bool{3: true, 7: true, 14: true, 21: true, 30: true, 50: true, 100: true, 200: true, 365: true, 500: true, 1000: true}

// nudgeCandidates are the nudges that fit the person's day now, best first. They are a pure function of the
// numbers: nothing here remembers anything, and studio.nudge.seen is what keeps one from coming back.
func nudgeCandidates(day string, t TodayOut, st StreakOut) []Nudge {
	var out []Nudge
	// The words goal the page calls today's target decides: a nudge never contradicts "N words to go".
	if t.HasGoal && t.Remaining == 0 {
		out = append(out, Nudge{ID: "met:" + day, Key: "studio.nudge.met", Kind: "milestone"})
	}
	if st.TodayHit && streakMilestones[st.Current] {
		out = append(out, Nudge{ID: fmt.Sprintf("streak:%d", st.Current), Key: "studio.nudge.streak", Params: map[string]any{"days": st.Current}, Kind: "streak"})
	}
	half := t.HasGoal && t.Remaining > 0 && t.Percent >= 50
	if half {
		out = append(out, Nudge{ID: "half:" + day, Key: "studio.nudge.half", Params: map[string]any{"percent": 50, "n": t.Remaining}, Kind: "milestone"})
	}
	if t.HasGoal && t.Remaining > 0 {
		if st.Current > 0 {
			out = append(out, Nudge{ID: "keep:" + day, Key: "studio.nudge.keep_streak", Params: map[string]any{"days": st.Current, "n": t.Remaining}, Kind: "streak"})
		}
		if !half {
			out = append(out, Nudge{ID: "to_go:" + day, Key: "studio.nudge.to_go", Params: map[string]any{"n": t.Remaining}, Kind: "progress"})
		}
	}
	return out
}

func (s *snapshot) nudges(t TodayOut, st StreakOut) ([]Nudge, error) {
	v := s.v
	if userPref(v.Ctx, v.Q, v.UserID, "studio.nudges", "true") != "true" {
		return []Nudge{}, nil
	}
	rows, err := v.Q.QueryContext(v.Ctx, `SELECT nudge FROM studio_nudges_seen WHERE user_id = ? AND day = ?`, v.UserID, v.Today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		seen[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	room := MaxNudgesPerDay - len(seen)
	out := []Nudge{}
	for _, n := range nudgeCandidates(v.Today, t, st) {
		if room <= 0 {
			break
		}
		if seen[n.ID] {
			continue
		}
		out = append(out, n)
		room--
	}
	return out, nil
}

var nudgeID = regexp.MustCompile(`^[a-z_]{2,16}:[0-9-]{1,10}$`)

// NudgeSeenIn names nudges that were shown.
type NudgeSeenIn struct {
	IDs []string `json:"ids" desc:"Nudge ids from the dashboard, at most 10."`
}

// Validate checks the ids.
func (in *NudgeSeenIn) Validate() error {
	if len(in.IDs) == 0 || len(in.IDs) > 10 {
		return ops.Invalid("ids", "Send between 1 and 10 nudge ids.")
	}
	for _, id := range in.IDs {
		if !nudgeID.MatchString(id) {
			return ops.Invalid("ids", "That is not a nudge id.")
		}
	}
	return nil
}

// NudgeSeenOut says how many nudges have been shown today.
type NudgeSeenOut struct {
	SeenToday int `json:"seen_today"`
	Left      int `json:"left" desc:"How many more may be shown today."`
}

func nudgeSeen(c *ops.Ctx, in NudgeSeenIn) (NudgeSeenOut, error) {
	var out NudgeSeenOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		v := &Viewer{Ctx: c, Q: tx, UserID: c.P.UserID}
		if err := v.zone(); err != nil {
			return err
		}
		for _, id := range in.IDs {
			if _, err := tx.Exec(`INSERT OR IGNORE INTO studio_nudges_seen(user_id, day, nudge) VALUES (?,?,?)`, v.UserID, v.Today, id); err != nil {
				return err
			}
		}
		// A person is shown at most MaxNudgesPerDay a day, so more rows than that are never read.
		if _, err := tx.Exec(`DELETE FROM studio_nudges_seen WHERE user_id = ? AND day < ?`, v.UserID, addDays(v.Today, -60)); err != nil {
			return err
		}
		return tx.QueryRow(`SELECT count(*) FROM studio_nudges_seen WHERE user_id = ? AND day = ?`, v.UserID, v.Today).Scan(&out.SeenToday)
	})
	out.Left = max(0, MaxNudgesPerDay-out.SeenToday)
	return out, err
}

// ---- the dashboard ------------------------------------------------------------------------------------

// ResumeOut is where "write N words today" goes: the project the person last wrote in.
type ResumeOut struct {
	ProjectID string `json:"project_id"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
}

func (s *snapshot) resume() (*ResumeOut, error) {
	v := s.v
	if len(v.IDs) == 0 {
		return nil, nil
	}
	list, args := inList(v.IDs)
	var id string
	err := v.Q.QueryRowContext(v.Ctx, `SELECT project_id FROM writing_sessions WHERE user_id = ? AND project_id IN `+list+` ORDER BY ended_at DESC LIMIT 1`,
		append([]any{v.UserID}, args...)...).Scan(&id)
	if isNoRows(err) {
		err = v.Q.QueryRowContext(v.Ctx, `SELECT id FROM projects WHERE id IN `+list+` ORDER BY updated_at DESC, id LIMIT 1`, args...).Scan(&id)
	}
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	infos, err := v.projectInfos([]string{id})
	if err != nil {
		return nil, err
	}
	return &ResumeOut{ProjectID: id, Slug: infos[id].Slug, Title: infos[id].Title}, nil
}

// DashboardIn asks for the dashboard.
type DashboardIn struct {
	Range string `query:"range" enum:"30d,90d,365d,all" desc:"The range of the words-written chart and of the weekday and time-of-day patterns. Default 30d. The period cards and the 24-week calendar do not depend on it."`
	From  string `query:"from" desc:"A custom range: YYYY-MM-DD, with to."`
	To    string `query:"to"`
}

// DashboardOut is everything the writing dashboard shows, in one read.
type DashboardOut struct {
	Day           string       `json:"day" desc:"The person's local date."`
	Timezone      string       `json:"timezone" desc:"The zone the days are cut in; change it in the profile."`
	Today         TodayOut     `json:"today"`
	Streak        StreakOut    `json:"streak"`
	Goals         GoalsOut     `json:"goals"`
	Cards         []Card       `json:"cards" desc:"Today, 7, 30 and 365 days, each against the period before, with a sparkline."`
	Series        SeriesOut    `json:"series"`
	Calendar      Calendar     `json:"calendar" desc:"The last 24 weeks; studio.calendar has the other views."`
	Projects      []ProjectRow `json:"projects" desc:"The person's own words per project this week and last."`
	Trend         Trend        `json:"trend"`
	Nudges        []Nudge      `json:"nudges" desc:"At most a few a day, none twice. Empty when the person switched nudges off (the preference studio.nudges)."`
	NudgesEnabled bool         `json:"nudges_enabled"`
	Sprint        *SprintOut   `json:"sprint,omitempty" desc:"The sprint or session timer running now."`
	Resume        *ResumeOut   `json:"resume,omitempty" desc:"Where to start writing: the project last written in."`
	Hidden        int          `json:"hidden_projects" desc:"Projects the person is on that are left out of every figure because a passphrase locks them in this session. A count and nothing else."`
}

func dashboard(c *ops.Ctx, in DashboardIn) (DashboardOut, error) {
	v, err := newViewer(c)
	if err != nil {
		return DashboardOut{}, err
	}
	s, err := v.snapshot()
	if err != nil {
		return DashboardOut{}, err
	}
	return s.dashboard(in)
}

func (s *snapshot) dashboard(in DashboardIn) (DashboardOut, error) {
	v := s.v
	out := DashboardOut{Day: v.Today, Timezone: v.Zone}
	var err error
	if out.Goals, err = s.goalsOut(false); err != nil {
		return out, err
	}
	s.persistCompletions()
	out.Today, out.Streak = s.todayOut(out.Goals.Items)
	out.Cards = cards(s.all.Days, v.Today)
	if out.Series, err = s.seriesFor(in.Range, in.From, in.To); err != nil {
		return out, err
	}
	out.Calendar = s.calendarFor(24, false)
	if out.Projects, err = s.projectRows(); err != nil {
		return out, err
	}
	out.Trend = trendOf(s.all.Days, v.Today)
	out.NudgesEnabled = userPref(v.Ctx, v.Q, v.UserID, "studio.nudges", "true") == "true"
	if out.Nudges, err = s.nudges(out.Today, out.Streak); err != nil {
		return out, err
	}
	if out.Sprint, err = currentSprint(v); err != nil {
		return out, err
	}
	if out.Resume, err = s.resume(); err != nil {
		return out, err
	}
	out.Hidden = v.Hidden
	return out, nil
}

// ---- series and calendar ops ------------------------------------------------------------------------

// SeriesIn asks for the words-written series.
type SeriesIn struct {
	Range string `query:"range" enum:"30d,90d,365d,all" desc:"Default 30d."`
	From  string `query:"from" desc:"A custom range: YYYY-MM-DD, with to. At most ten years."`
	To    string `query:"to"`
}

func seriesOp(c *ops.Ctx, in SeriesIn) (SeriesOut, error) {
	v, err := newViewer(c)
	if err != nil {
		return SeriesOut{}, err
	}
	s := &snapshot{v: v}
	if s.all, err = v.loadDays(v.IDs, ""); err != nil {
		return SeriesOut{}, err
	}
	return s.seriesFor(in.Range, in.From, in.To)
}

// CalendarIn asks for the activity calendar.
type CalendarIn struct {
	Weeks int  `query:"weeks" desc:"How many weeks, 1 to 530. Default 24."`
	All   bool `query:"all" desc:"All the person's writing, never fewer than 24 weeks."`
}

func calendarOp(c *ops.Ctx, in CalendarIn) (Calendar, error) {
	n := in.Weeks
	if n == 0 {
		n = 24
	}
	if n < 1 || n > maxCalendarWeek {
		return Calendar{}, ops.Invalid("weeks", fmt.Sprintf("Ask for between 1 and %d weeks.", maxCalendarWeek))
	}
	v, err := newViewer(c)
	if err != nil {
		return Calendar{}, err
	}
	s, err := v.snapshot()
	if err != nil {
		return Calendar{}, err
	}
	return s.calendarFor(n, in.All), nil
}

// ---- compare -------------------------------------------------------------------------------------------

// CompareIn compares two periods, or two projects over a period (draft against draft).
type CompareIn struct {
	Preset   string `query:"preset" enum:"this_week,this_month,last_30" desc:"A against B, over finished days of the same number: the days of this week before today against the same days of last week, the days of this month before today against the same days of last month (the first days, when last month was shorter), the 30 days before today against the 30 before them. Today is not in the comparison. Instead of the dates."`
	AFrom    string `query:"a_from" desc:"YYYY-MM-DD."`
	ATo      string `query:"a_to"`
	AProject string `query:"a_project" desc:"Only this project's words in A. Empty: every project."`
	BFrom    string `query:"b_from"`
	BTo      string `query:"b_to"`
	BProject string `query:"b_project" desc:"Only this project's words in B. Two projects over the same dates are two drafts."`
}

// Side is one side of a comparison.
type Side struct {
	From      string `json:"from"`
	To        string `json:"to"`
	ProjectID string `json:"project_id,omitempty"`
	Days      int    `json:"days" desc:"How many days the side spans, from and to included. Differences between two sides of different lengths say little; a page leaves them out."`
	Figures
}

// CompareOut is two sides and what changed from B to A.
type CompareOut struct {
	Compared bool  `json:"compared" desc:"False when A is a period with no finished day yet (a Monday, the 1st of a month): A is shown so far, B is empty, the differences are 0 and mean nothing, and a note says the comparison starts tomorrow."`
	A        Side  `json:"a"`
	B        Side  `json:"b"`
	Words    Delta `json:"words" desc:"A against B."`
	Net      Delta `json:"net"`
	Minutes  Delta `json:"minutes"`
	Days     int   `json:"writing_days_change"`
	Notes    []Msg `json:"notes,omitempty" desc:"studio.compare.so_far (the finished days both sides span), .finished (30 days), .starts_tomorrow (no finished day yet) and .unequal (the two sides are not the same length)."`
}

func compare(c *ops.Ctx, in CompareIn) (CompareOut, error) {
	v, err := newViewer(c)
	if err != nil {
		return CompareOut{}, err
	}
	a, b := Side{From: in.AFrom, To: in.ATo, ProjectID: in.AProject}, Side{From: in.BFrom, To: in.BTo, ProjectID: in.BProject}
	t := v.Today
	var soFar Msg
	noPrior := false // a period with no finished day yet: it is shown so far, with nothing to compare it with
	switch in.Preset {
	case "this_week":
		// the finished days of the week against the same days of last week: today is not over and is on the Today card, so it is
		// not in the comparison; a Monday has no finished day yet
		a.From = weekStart(t)
		n := daysBetween(a.From, t)
		b.From = addDays(a.From, -7)
		if n == 0 {
			noPrior = true
			a.To = t
		} else {
			a.To, b.To = addDays(a.From, n-1), addDays(b.From, n-1)
			soFar = Msg{Key: "studio.compare.so_far", Params: map[string]any{"days": n}}
		}
	case "this_month":
		// the same days of the month; when last month was shorter both sides stop at its last day; the 1st has no finished day yet
		pm := addDays(monthStart(t), -1)
		a.From, b.From = monthStart(t), monthStart(pm)
		n := min(daysBetween(a.From, t), daysBetween(b.From, pm)+1)
		if n == 0 {
			noPrior = true
			a.To = t
		} else {
			a.To, b.To = addDays(a.From, n-1), addDays(b.From, n-1)
			soFar = Msg{Key: "studio.compare.so_far", Params: map[string]any{"days": n}}
		}
	case "last_30":
		// the 30 finished days before today against the 30 before them
		a.From, a.To = addDays(t, -30), addDays(t, -1)
		b.From, b.To = addDays(t, -60), addDays(t, -31)
		soFar = Msg{Key: "studio.compare.finished"}
	case "":
	default:
		return CompareOut{}, ops.Invalid("preset", "The preset is this_week, this_month or last_30.")
	}
	sides := []*Side{&a, &b}
	if noPrior {
		sides = sides[:1]
	}
	for _, side := range sides {
		for _, d := range []*string{&side.From, &side.To} {
			if _, ok := parseDay(*d); !ok {
				return CompareOut{}, ops.Invalid("from", "Give both dates of each side as YYYY-MM-DD, or a preset.")
			}
		}
		if side.From > side.To {
			return CompareOut{}, ops.Invalid("from", "A start comes after its end.")
		}
		if daysBetween(side.From, side.To)+1 > maxRangeDays {
			return CompareOut{}, ops.Invalid("from", fmt.Sprintf("Ask for at most %d days on a side.", maxRangeDays))
		}
	}
	all, err := v.loadDays(v.IDs, "")
	if err != nil {
		return CompareOut{}, err
	}
	for _, side := range sides {
		days := all.Days
		if side.ProjectID != "" {
			// A project that is locked, in the trash or not the caller's is not there: the same answer for all three.
			if !v.sees(side.ProjectID) {
				return CompareOut{}, ops.NotFound("project")
			}
			if days, err = v.projectDays(side.ProjectID); err != nil {
				return CompareOut{}, err
			}
		}
		side.Figures = figuresOf(days, side.From, side.To)
		side.Days = daysBetween(side.From, side.To) + 1
	}
	if noPrior {
		b = Side{}
		return CompareOut{A: a, B: b, Notes: []Msg{{Key: "studio.compare.starts_tomorrow"}}}, nil
	}
	out := CompareOut{A: a, B: b, Compared: true, Words: deltaOf(a.Words, b.Words), Net: deltaOf(a.Net, b.Net), Minutes: deltaOf(a.Minutes, b.Minutes),
		Days: a.WritingDays - b.WritingDays}
	if soFar.Key != "" {
		out.Notes = append(out.Notes, soFar)
	}
	if a.Days != b.Days {
		out.Notes = append(out.Notes, Msg{Key: "studio.compare.unequal", Params: map[string]any{"a": a.Days, "b": b.Days}})
	}
	return out, nil
}

// ---- one project's totals (F-153) ---------------------------------------------------------------------------

// ProjectTotalsIn asks for a project's daily and weekly totals.
type ProjectTotalsIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Days      int    `query:"days" desc:"How many days ending today, 1 to 366. Default 84."`
}

func (in ProjectTotalsIn) ProjectRef() string { return in.ProjectID }

// DayRow is one day of a project.
type DayRow struct {
	Day     string `json:"day"`
	Words   int    `json:"words" desc:"The person's own words added."`
	Net     int    `json:"net"`
	Minutes int    `json:"minutes"`
}

// WeekRow is one Monday-first week of a project.
type WeekRow struct {
	Start       string `json:"start" desc:"The Monday."`
	Words       int    `json:"words"`
	Net         int    `json:"net"`
	Minutes     int    `json:"minutes"`
	WritingDays int    `json:"writing_days"`
}

// ProjectTotalsOut is the person's own daily and weekly totals on one project.
type ProjectTotalsOut struct {
	From        string    `json:"from"`
	To          string    `json:"to"`
	Days        []DayRow  `json:"days" desc:"Every day, oldest first; a day with no writing is zeros."`
	Weeks       []WeekRow `json:"weeks" desc:"Monday-first weeks; the first and last may be partial."`
	TotalWords  int       `json:"total_words"`
	TotalNet    int       `json:"total_net"`
	WritingDays int       `json:"writing_days"`
}

func projectTotals(c *ops.Ctx, in ProjectTotalsIn) (ProjectTotalsOut, error) {
	n := in.Days
	if n == 0 {
		n = 84
	}
	if n < 1 || n > 366 {
		return ProjectTotalsOut{}, ops.Invalid("days", "Ask for between 1 and 366 days.")
	}
	// The registry has already checked membership, the passphrase gate and the trash for this project.
	// A viewer of one project the registry has already admitted. It judges no goal (only days are summed), so which other projects
	// count (Viewer.Partial) is not asked.
	v := &Viewer{Ctx: c, Q: c.DB().Read(), UserID: c.P.UserID, IDs: []string{in.ProjectID}}
	if err := v.zone(); err != nil {
		return ProjectTotalsOut{}, err
	}
	dd, err := v.loadDays(v.IDs, addDays(v.Today, -(n-1)))
	if err != nil {
		return ProjectTotalsOut{}, err
	}
	from := addDays(v.Today, -(n - 1))
	out := ProjectTotalsOut{From: from, To: v.Today, Days: make([]DayRow, 0, n)}
	for d := from; d <= v.Today; d = addDays(d, 1) {
		x := dd.Days[d]
		out.Days = append(out.Days, DayRow{Day: d, Words: x.Words, Net: x.Net, Minutes: minutesOf(x.Seconds)})
		out.TotalWords += x.Words
		out.TotalNet += x.Net
		if x.Words > 0 {
			out.WritingDays++
		}
	}
	for ws := weekStart(from); ws <= v.Today; ws = addDays(ws, 7) {
		f := figuresOf(dd.Days, max(ws, from), min(addDays(ws, 6), v.Today))
		out.Weeks = append(out.Weeks, WeekRow{Start: ws, Words: f.Words, Net: f.Net, Minutes: f.Minutes, WritingDays: f.WritingDays})
	}
	return out, nil
}

var _ = math.Round
var _ = strings.TrimSpace
var _ = db.Now

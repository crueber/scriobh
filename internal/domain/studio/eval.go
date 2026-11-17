package studio

import (
	"database/sql"
	"errors"
	"math"
	"sort"
)

// Msg is a message the page translates: a key of the catalogue (internal/i18n, prefix studio.) and its parameters. The
// server never returns an English sentence for a screen to show.
type Msg struct {
	Key    string         `json:"key"`
	Params map[string]any `json:"params,omitempty"`
}

// GoalOut is a goal with its progress in the current period.
type GoalOut struct {
	ID           string    `json:"id" desc:"A studio goal's id; a goal set on a project's Goals page has an id that begins project:."`
	Source       string    `json:"source" enum:"studio,project" desc:"studio: made with studio.goal.create. project: the daily or total goal set on a project's Goals page (goal.set), shown here and changed there."`
	ProjectID    string    `json:"project_id,omitempty" desc:"Empty for a goal over every project the person may read."`
	ProjectTitle string    `json:"project_title,omitempty"`
	ProjectSlug  string    `json:"project_slug,omitempty"`
	Period       string    `json:"period" enum:"day,week,month,total"`
	Measure      string    `json:"measure" enum:"words,sessions,chapters,minutes"`
	Target       int       `json:"target"`
	Value        int       `json:"value" desc:"The measure in the current period (day, week, month) or so far (total). For a total goal in words: the words in the manuscript."`
	Remaining    int       `json:"remaining"`
	Percent      int       `json:"percent" desc:"May exceed 100."`
	Met          bool      `json:"met" desc:"The current period is met (day, week, month), or the total is reached."`
	CompletedOn  string    `json:"completed_on,omitempty" desc:"A total goal that has been reached stays completed, with the date it was first seen reached: cutting words afterwards never un-completes it (raising the target does)."`
	Status       string    `json:"status" enum:"active,completed,archived" desc:"A day, week or month goal is active until it is archived; a total goal is completed once reached."`
	From         string    `json:"period_from,omitempty"`
	To           string    `json:"period_to,omitempty"`
	Start        string    `json:"start_date"`
	Deadline     string    `json:"deadline,omitempty"`
	EndedOn      string    `json:"ended_on,omitempty"`
	Version      int       `json:"version" desc:"The version of the target now in force."`
	Changes      int       `json:"changes" desc:"How many times the target has been changed."`
	Net          *int      `json:"net,omitempty" desc:"Words goals only: the net words in the same period, shown beside the goal and never against it."`
	Forecast     *Forecast `json:"forecast,omitempty" desc:"Total goals: what a deadline asks and when the target would be reached, as an estimate with its basis."`
	Message      Msg       `json:"message"`
}

// snapshot is everything the numbers are made from, read once.
type snapshot struct {
	v        *Viewer
	all      DayData
	goals    []Goal // studio goals the person can count, archived included
	legacy   []legacyGoal
	infos    map[string]projectInfo
	hits     map[string]bool // the days a daily goal in force that day was met
	hasDaily bool            // a daily goal exists (studio or project)
	streak   int
	longest  int
	reached  []string // total goals seen reached for the first time on this read, to be remembered (persistCompletions)
}

func (v *Viewer) snapshot() (*snapshot, error) {
	s := &snapshot{v: v}
	var err error
	if s.all, err = v.loadDays(v.IDs, ""); err != nil {
		return nil, err
	}
	all, err := loadGoals(v.Ctx, v.Q, v.UserID)
	if err != nil {
		return nil, err
	}
	for _, g := range all {
		// A goal on a project the person cannot count now (locked, in the trash, left) shows nothing at all.
		if g.ProjectID != "" && !v.sees(g.ProjectID) {
			continue
		}
		g.FinalOnly = g.ProjectID == "" && v.Partial
		s.goals = append(s.goals, g)
	}
	for i := range s.goals {
		if s.goals[i], err = s.settleEnd(s.goals[i]); err != nil {
			return nil, err
		}
	}
	if s.legacy, err = loadLegacy(v.Ctx, v.Q, v.UserID, v.IDs); err != nil {
		return nil, err
	}
	var named []string
	for _, g := range s.goals {
		if g.ProjectID != "" {
			named = append(named, g.ProjectID)
		}
	}
	for _, g := range s.legacy {
		named = append(named, g.ProjectID)
	}
	if s.infos, err = v.projectInfos(named); err != nil {
		return nil, err
	}
	if err := s.goalDays(); err != nil {
		return nil, err
	}
	return s, nil
}

// settleEnd turns the stored end of an archived goal into the last date it applied. What is stored is the date it was
// archived, so it never holds a figure from a project the reader cannot count: here, for this reader, that date counts if its
// period was met by what they may count ("what is met stays met"), else the day before.
func (s *snapshot) settleEnd(g Goal) (Goal, error) {
	if g.EndedOn == "" {
		return g, nil
	}
	a := g.EndedOn
	if g.Period != "total" {
		days, err := s.scope(g)
		if err != nil {
			return g, err
		}
		from, to := periodOf(g.Period, a)
		open := g
		open.EndedOn = ""
		if !open.judge(from, to, measureOver(days, g.Measure, from, a)).Met {
			a = addDays(a, -1)
		}
	}
	if a < g.Start {
		a = g.Start
	}
	g.EndedOn = a
	return g, nil
}

func dayValue(d Day, measure string) int {
	switch measure {
	case "sessions":
		return d.Sessions
	case "chapters":
		return d.Chapters
	case "minutes":
		return minutesOf(d.Seconds)
	}
	return d.Words
}

// measureOver is a goal's measure summed over a range.
func measureOver(days Days, measure, from, to string) int {
	s := days.sum(from, to)
	switch measure {
	case "sessions":
		return s.Sessions
	case "chapters":
		return s.Chapters
	case "minutes":
		return minutesOf(s.Seconds)
	}
	return s.Words
}

func (s *snapshot) scope(g Goal) (Days, error) {
	if g.ProjectID == "" {
		return s.all.Days, nil
	}
	return s.v.projectDays(g.ProjectID)
}

// goalDays finds the days a daily goal in force that day was met, and the streaks they make. A day is a goal day when
// any daily goal of the person's that applied that day was met by the versions then in force; a day on which nothing
// was written is simply not one.
func (s *snapshot) goalDays() error {
	s.hits = map[string]bool{}
	for d := range s.all.LegacyHit {
		s.hits[d] = true
	}
	for _, g := range s.legacy {
		if g.Kind == "daily" {
			s.hasDaily = true
		}
	}
	today := s.v.Today
	for _, g := range s.goals {
		if g.Period != "day" {
			continue
		}
		if g.EndedOn == "" {
			s.hasDaily = true
		}
		days, err := s.scope(g)
		if err != nil {
			return err
		}
		end := today
		if g.EndedOn != "" && g.EndedOn < end {
			end = g.EndedOn
		}
		for d := g.Start; d <= end; d = addDays(d, 1) {
			if j := g.judge(d, d, dayValue(days[d], g.Measure)); j.Met {
				s.hits[d] = true
			}
		}
	}
	keys := make([]string, 0, len(s.hits))
	for d := range s.hits {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	s.streak, s.longest = streaks(keys, today)
	return nil
}

func pct(value, target int) int {
	if target <= 0 {
		return 0
	}
	return int(int64(value) * 100 / int64(target))
}

// evalGoal reads a studio goal's progress now.
func (s *snapshot) evalGoal(g Goal) (GoalOut, error) {
	cur := g.current()
	out := GoalOut{ID: g.ID, Source: "studio", ProjectID: g.ProjectID, Period: g.Period, Measure: g.Measure,
		Target: cur.Target, Start: g.Start, Deadline: cur.Deadline, EndedOn: g.EndedOn, Version: cur.N, Changes: len(g.Versions) - 1}
	if p, ok := s.infos[g.ProjectID]; ok {
		out.ProjectTitle, out.ProjectSlug = p.Title, p.Slug
	}
	days, err := s.scope(g)
	if err != nil {
		return out, err
	}
	today := s.v.Today
	if g.Period != "total" {
		from, to := periodOf(g.Period, today)
		out.From, out.To = from, to
		upto := min(to, today)
		if g.EndedOn != "" && g.EndedOn < upto {
			upto = g.EndedOn
		}
		out.Value = measureOver(days, g.Measure, from, upto)
		j := g.judge(from, to, out.Value)
		out.Met = j.Met
		if j.Target > 0 {
			out.Target = j.Target
		}
		out.Status = "active"
		if g.Measure == "words" {
			n := days.sum(from, upto).Net
			out.Net = &n
		}
	} else {
		ids := s.v.IDs
		if g.ProjectID != "" {
			ids = []string{g.ProjectID}
		}
		if g.Measure == "words" {
			w, err := s.v.manuscriptWords(ids)
			if err != nil {
				return out, err
			}
			out.Value = w
		} else {
			out.Value = measureOver(days, g.Measure, g.Start, s.v.Today)
		}
		// Completion is sticky: a goal that has been reached stays completed, whatever is cut afterwards. A goal over every project
		// has its completion stored from a sum that may hold words the reader cannot count, so a reader who cannot count them
		// all (Partial) has it judged live only.
		live := out.Value >= out.Target
		sticky := g.CompletedOn != "" && (g.ProjectID != "" || !s.v.Partial)
		out.Met = live || sticky
		out.Status = "active"
		if out.Met {
			out.Status = "completed"
			out.CompletedOn = s.v.Today
			if sticky {
				out.CompletedOn = g.CompletedOn
			}
		}
		if live && g.CompletedOn == "" && (g.ProjectID != "" || !s.v.Partial) {
			s.reached = append(s.reached, g.ID)
		}
		out.Remaining = max(0, out.Target-out.Value)
		f := s.forecastFor(g.Measure, days, out.Remaining, cur.Deadline)
		out.Forecast = &f
	}
	if g.EndedOn != "" {
		out.Status = "archived"
	}
	out.Remaining = max(0, out.Target-out.Value)
	out.Percent = pct(out.Value, out.Target)
	out.Message = goalMessage(out)
	return out, nil
}

// forecastFor reads the recent pace of a measure over the pace window ending today. A word-count target is reached by the
// manuscript growing, so its pace is the net words (a cut slows the date and fails nothing; a pace that is nothing or less is
// answered with no date, not with a shortfall); every other measure is read as it is counted.
func (s *snapshot) forecastFor(measure string, days Days, remaining int, deadline string) Forecast {
	from := addDays(s.v.Today, -(PaceWindow - 1))
	total, active := 0, 0
	basis := "added"
	for d := from; d <= s.v.Today; d = addDays(d, 1) {
		x := days[d]
		if measure == "words" {
			total += x.Net
			basis = "net"
			if x.Words > 0 {
				active++
			}
			continue
		}
		if n := dayValue(x, measure); n > 0 {
			active++
			total += n
		}
	}
	if measure == "minutes" { // minutes of the window, not a sum of rounded days
		total = minutesOf(days.sum(from, s.v.Today).Seconds)
	}
	return forecast(remaining, deadline, s.v.Today, total, active, measure, basis)
}

func goalMessage(g GoalOut) Msg {
	p := map[string]any{"period": g.Period, "measure": g.Measure}
	if g.Met {
		return Msg{Key: "studio.goal.met", Params: p}
	}
	p["n"] = g.Remaining
	return Msg{Key: "studio.goal.remaining", Params: p}
}

// legacyOut is a goal of the older kind as a studio goal, read-only.
func (s *snapshot) legacyOut(g legacyGoal) (GoalOut, error) {
	out := GoalOut{ID: "project:" + g.Kind + ":" + g.ProjectID, Source: "project", ProjectID: g.ProjectID, Measure: "words",
		Target: g.Target, Start: g.Start, Status: "active"}
	if p, ok := s.infos[g.ProjectID]; ok {
		out.ProjectTitle, out.ProjectSlug = p.Title, p.Slug
	}
	days, err := s.v.projectDays(g.ProjectID)
	if err != nil {
		return out, err
	}
	if g.Kind == "daily" {
		out.Period = "day"
		out.From, out.To = s.v.Today, s.v.Today
		out.Value = days[s.v.Today].Words
		n := days[s.v.Today].Net
		out.Net = &n
		out.Met = out.Value >= g.Target
		if g.End != "" && g.End < s.v.Today {
			out.Status, out.EndedOn = "archived", g.End
		}
	} else {
		out.Period, out.Deadline = "total", g.End
		w, err := s.v.manuscriptWords([]string{g.ProjectID})
		if err != nil {
			return out, err
		}
		out.Value = w
		out.Met = w >= g.Target
		if out.Met {
			out.Status = "completed"
		}
		f := s.forecastFor("words", days, max(0, g.Target-w), g.End)
		out.Forecast = &f
	}
	out.Remaining = max(0, out.Target-out.Value)
	out.Percent = pct(out.Value, out.Target)
	out.Message = goalMessage(out)
	return out, nil
}

// GoalsOut is the goals of a person with the counts the dashboard shows ("2 active, 1 completed").
type GoalsOut struct {
	Active    int       `json:"active" desc:"Goals that are not archived and not completed."`
	Completed int       `json:"completed" desc:"Total goals that have been reached."`
	Archived  int       `json:"archived"`
	Items     []GoalOut `json:"items" desc:"Studio goals first, then goals set on a project's Goals page; archived ones last."`
}

func (s *snapshot) goalsOut(includeArchived bool) (GoalsOut, error) {
	out := GoalsOut{Items: []GoalOut{}}
	var archived []GoalOut
	add := func(o GoalOut) {
		switch o.Status {
		case "archived":
			out.Archived++
			if includeArchived {
				archived = append(archived, o)
			}
		case "completed":
			out.Completed++
			out.Items = append(out.Items, o)
		default:
			out.Active++
			out.Items = append(out.Items, o)
		}
	}
	for _, g := range s.goals {
		o, err := s.evalGoal(g)
		if err != nil {
			return out, err
		}
		add(o)
	}
	for _, g := range s.legacy {
		o, err := s.legacyOut(g)
		if err != nil {
			return out, err
		}
		add(o)
	}
	out.Items = append(out.Items, archived...)
	return out, nil
}

// persistCompletions remembers the total goals this read saw reached for the first time, once each. It is the only write a read
// makes, and only the owner's own reads (a session or a key of the person) make it: the card and the email, which have no
// database to write to, never do. A goal over every project is remembered only by a read that counts every project the person
// has written in.
func (s *snapshot) persistCompletions() {
	if s.v.db == nil || len(s.reached) == 0 {
		return
	}
	ids := s.reached
	s.reached = nil
	_ = s.v.db.WithTx(s.v.Ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(s.v.Ctx, `UPDATE studio_goals SET completed_on = ? WHERE id = ? AND user_id = ? AND completed_on = ''`, s.v.Today, id, s.v.UserID); err != nil {
				return err
			}
		}
		return nil
	})
}

// headline picks the daily words goal "today's target" is read from: the goal over every project if there is one,
// else the one nearest to being met.
func headlineOf(items []GoalOut) (GoalOut, bool) {
	var best GoalOut
	found := false
	for _, g := range items {
		if g.Period != "day" || g.Measure != "words" || g.Status != "active" {
			continue
		}
		better := !found
		if found {
			switch {
			case g.ProjectID == "" && best.ProjectID != "":
				better = true
			case g.ProjectID != "" && best.ProjectID == "":
			default:
				better = g.Remaining < best.Remaining || (g.Remaining == best.Remaining && g.ID < best.ID)
			}
		}
		if better {
			best, found = g, true
		}
	}
	return best, found
}

// TodayOut is the person's day so far.
type TodayOut struct {
	Words     int    `json:"words" desc:"Words added today. Over every project, unless the daily goal the target is read from belongs to one project: then the words added in that project, so that words, target and remaining always speak of the same goal."`
	Net       int    `json:"net" desc:"Net words today; shown beside the words added, never against them."`
	Minutes   int    `json:"minutes"`
	Sessions  int    `json:"sessions"`
	HasGoal   bool   `json:"has_goal" desc:"A daily words goal is in force."`
	GoalID    string `json:"goal_id,omitempty" desc:"The goal the target is read from."`
	ProjectID string `json:"project_id,omitempty" desc:"The project that goal belongs to; empty for a goal over every project. Say it beside the words when it is set."`
	Target    int    `json:"target"`
	Remaining int    `json:"remaining" desc:"Words still to add to meet the target, 0 once it is met."`
	Percent   int    `json:"percent"`
	Hit       bool   `json:"hit" desc:"A daily goal in force today has been met."`
	Message   *Msg   `json:"message,omitempty" desc:"studio.today.to_go with n, or studio.today.met."`
}

// StreakOut is the habit-streak card.
type StreakOut struct {
	HasGoal     bool   `json:"has_goal"`
	Current     int    `json:"current" desc:"Consecutive goal days. Today counts once its goal is met and does not break the run until the day is over."`
	Longest     int    `json:"longest"`
	TodayWords  int    `json:"today_words" desc:"The words today that today_target is measured against (see today.words)."`
	TodayTarget int    `json:"today_target" desc:"The target of the daily goal the card reads, in its own measure."`
	TodayHit    bool   `json:"today_hit"`
	Measure     string `json:"measure,omitempty" enum:"words,sessions,minutes,chapters" desc:"What the daily goal the card reads counts. Describe the card in this measure: a sessions goal is never worded as words."`
	TodayValue  int    `json:"today_value" desc:"Today's count in that measure, toward today_target."`
	Message     Msg    `json:"message" desc:"studio.streak.hit, .keep, .begin, .keep_goal, .begin_goal or .set_goal. There is no message for a day that went by: a day without writing is empty, not missed."`
	Note        *Msg   `json:"note,omitempty" desc:"studio.streak.note, when some project the person has written in is not counted (locked, in the trash, left): days written in projects they cannot open now are not counted here, and come back when they can. Show it beside the streak."`
}

func (s *snapshot) todayOut(items []GoalOut) (TodayOut, StreakOut) {
	d := s.all.Days[s.v.Today]
	t := TodayOut{Words: d.Words, Net: d.Net, Minutes: minutesOf(d.Seconds), Sessions: d.Sessions, Hit: s.hits[s.v.Today]}
	st := StreakOut{HasGoal: s.hasDaily, Current: s.streak, Longest: s.longest, TodayWords: t.Words, TodayHit: t.Hit}
	goalMeasure := ""
	if h, ok := headlineOf(items); ok {
		// the words are the goal's own: a goal that belongs to one project is not met, nor short, by words written in another
		t.HasGoal, t.GoalID, t.ProjectID, t.Words, t.Target, t.Remaining, t.Percent = true, h.ID, h.ProjectID, h.Value, h.Target, h.Remaining, h.Percent
		if h.Met {
			t.Message = &Msg{Key: "studio.today.met"}
		} else {
			t.Message = &Msg{Key: "studio.today.to_go", Params: map[string]any{"n": h.Remaining}}
		}
		st.TodayWords, st.TodayTarget, st.TodayValue, st.Measure = t.Words, t.Target, t.Words, "words"
		goalMeasure = "words"
	} else if h, ok := otherDailyGoalOf(items); ok {
		// a daily goal that counts something else is described in what it counts, never in words
		st.TodayTarget, st.TodayValue, st.Measure = h.Target, h.Value, h.Measure
		goalMeasure = h.Measure
	}
	switch {
	case !s.hasDaily:
		st.Message = Msg{Key: "studio.streak.set_goal"}
	case t.Hit:
		st.Message = Msg{Key: "studio.streak.hit", Params: map[string]any{"days": s.streak}}
	case goalMeasure == "words" && s.streak > 0:
		st.Message = Msg{Key: "studio.streak.keep", Params: map[string]any{"days": s.streak, "n": t.Remaining}}
	case goalMeasure == "words":
		st.Message = Msg{Key: "studio.streak.begin", Params: map[string]any{"n": t.Remaining}}
	case s.streak > 0:
		st.Message = Msg{Key: "studio.streak.keep_goal", Params: map[string]any{"days": s.streak}}
	default:
		st.Message = Msg{Key: "studio.streak.begin_goal"}
	}
	if s.v.Partial {
		st.Note = &Msg{Key: "studio.streak.note"}
	}
	return t, st
}

// otherDailyGoalOf is the daily goal of a measure other than words that is nearest to being met, for a person who has no
// daily words goal.
func otherDailyGoalOf(items []GoalOut) (GoalOut, bool) {
	var best GoalOut
	found := false
	for _, g := range items {
		if g.Period != "day" || g.Measure == "words" || g.Status != "active" {
			continue
		}
		if !found || g.Remaining < best.Remaining || (g.Remaining == best.Remaining && g.ID < best.ID) {
			best, found = g, true
		}
	}
	return best, found
}

var errMissing = errors.New("studio: missing")

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func roundHalfUp(f float64) int { return int(math.Floor(f + 0.5)) }

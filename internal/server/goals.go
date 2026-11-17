package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/ops"
)

// The Goals page: a person's own numbers on one project. Everything on it is read
// through the goal and stats ops, which only ever answer for the caller, and every
// change goes through the goal and project ops. Goals count words added; a cut is
// never shown against a day, and a quiet day is never marked as missed.

// Days the history can show.
var goalHistoryDays = []int{30, 90}

// goalDay is one day of the calendar. Blank cells pad the first week.
type goalDay struct {
	Blank bool
	Num   int    // day of the month
	Text  string // what the cell shows: the day, with the month on the first of it and on the first cell
	Label string // the whole sentence a screen reader gets for the cell
	Level int    // 0 to 4: how much was written, drawn as the height of a bar
	Hit   bool   // the daily goal was met
	Today bool
}

// goalPace is the words-per-day text, with nothing shown that the ops did not offer.
type goalPace struct {
	Needed     int // words a day to reach the target by the deadline
	DaysLeft   int
	Projected  string // the projected finish, only when the op gave one
	Average    int    // net words a day over the pace window
	HasAverage bool
}

// goalWeek is one Monday-first week of the person's own words on the project.
type goalWeek struct{ Start, Words, Days string }

type goalsData struct {
	Project projects.Project
	Slug    string
	CSRF    string
	CanEdit bool
	IsOwner bool

	Today    goals.TodayOut
	TodayCap int         // words added, held to the target so the meter stays valid
	Daily    *goals.Goal // the person's own daily goal
	Own      *goals.Goal // the person's own project goal
	// The project's shared target, which the owner sets.
	SharedWords    int
	SharedDeadline string

	Proj goals.ProjectOut
	Pace goalPace
	// Dates written for people, not as ISO strings.
	TodayLabel, SharedDeadlineLabel, OwnEndLabel, DeadlineLabel string
	Percent                                                     int

	Days     int
	Others   []int // the other history lengths, as links
	Weeks    [][]goalDay
	Hist     goals.HistoryOut
	Summary  string
	Timezone string

	// The person's own words on this project week by week, from the Writing Studio (studio.project.totals).
	OwnWeeks []goalWeek

	Saved  string
	Error  string
	Field  string
	Values map[string]string // what the person typed, shown again beside an error
}

func parseGoalNumber(field, s string) (int, error) {
	s = strings.NewReplacer(",", "", " ", "", "_", "").Replace(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, ops.Invalid(field, "Enter a whole number of words, such as 80000.")
	}
	return n, nil
}

// shortDay writes "2026-11-12" as "Thu 12 Nov".
func goalShortDay(s string) string {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("Mon 2 Jan")
}

// longDay writes "2026-11-12" as "12 November 2026".
func goalLongDay(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return s
	}
	return t.Format("2 January 2006")
}

func goalWords(n int) string {
	if n == 1 {
		return "1 word"
	}
	return commas(n) + " words"
}

// levelFor says how full a day's bar is: against the daily target when there is
// one, otherwise against the busiest day in the range.
func goalLevel(words, target, peak int) int {
	if words <= 0 {
		return 0
	}
	if target > 0 {
		switch p := words * 100 / target; {
		case p >= 100:
			return 4
		case p >= 50:
			return 3
		case p >= 25:
			return 2
		}
		return 1
	}
	if peak <= 0 {
		return 1
	}
	l := (words*4 + peak - 1) / peak
	if l < 1 {
		l = 1
	}
	if l > 4 {
		l = 4
	}
	return l
}

// goalCalendar lays the days out in weeks, Monday first. The table it feeds is also the
// text alternative: every cell says its date and words in plain sentences.
func goalCalendar(h goals.HistoryOut, today string) [][]goalDay {
	if len(h.Days) == 0 {
		return nil
	}
	peak := 0
	for _, d := range h.Days {
		if d.WordsAdded > peak {
			peak = d.WordsAdded
		}
	}
	first, _ := time.Parse("2006-01-02", h.Days[0].Day)
	offset := (int(first.Weekday()) + 6) % 7
	var cells []goalDay
	for i := 0; i < offset; i++ {
		cells = append(cells, goalDay{Blank: true})
	}
	for i, d := range h.Days {
		t, _ := time.Parse("2006-01-02", d.Day)
		text := strconv.Itoa(t.Day())
		if i == 0 || t.Day() == 1 {
			text += " " + t.Format("Jan")
		}
		label := goalShortDay(d.Day) + ": "
		switch {
		case d.WordsAdded == 0:
			label += "no words added"
		default:
			label += goalWords(d.WordsAdded) + " added"
		}
		if d.Hit {
			label += ", goal met"
		}
		if d.Day == today {
			label += ", today"
		}
		cells = append(cells, goalDay{Num: t.Day(), Text: text, Label: label, Level: goalLevel(d.WordsAdded, d.Target, peak), Hit: d.Hit, Today: d.Day == today})
	}
	for len(cells)%7 != 0 {
		cells = append(cells, goalDay{Blank: true})
	}
	var weeks [][]goalDay
	for i := 0; i < len(cells); i += 7 {
		weeks = append(weeks, cells[i:i+7])
	}
	return weeks
}

func (s *Server) goalsProject(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return projects.Project{}, false
	}
	pid, ok := projects.IDBySlug(r.Context(), s.DB, p.UserID, r.PathValue("slug"))
	if !ok {
		s.fail(w, r, ops.NotFound("project"))
		return projects.Project{}, false
	}
	return s.goalsProjectByID(w, r, pid)
}

func (s *Server) goalsProjectByID(w http.ResponseWriter, r *http.Request, pid string) (projects.Project, bool) {
	po, err := s.Reg.Call(r.Context(), s.principal(r), "project.get", projects.GetIn{ID: pid})
	if err != nil {
		s.fail(w, r, err)
		return projects.Project{}, false
	}
	proj := po.(projects.Item).Item.Addressed()
	if proj.Trashed {
		s.fail(w, r, ops.NotFound("project"))
		return projects.Project{}, false
	}
	return proj, true
}

// goalsView reads everything the page shows, each piece through its op.
func (s *Server) goalsView(r *http.Request, proj projects.Project, days int) (goalsData, error) {
	p := s.principal(r)
	ctx := r.Context()
	d := goalsData{Project: proj, Slug: proj.Slug, CSRF: authFrom(ctx).CSRF, Days: days,
		CanEdit: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor, IsOwner: proj.Role == ops.ProjectOwner}
	for _, n := range goalHistoryDays {
		if n != days {
			d.Others = append(d.Others, n)
		}
	}
	lo, err := s.Reg.Call(ctx, p, "goal.list", goals.ListIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	list := lo.(goals.ListOut)
	d.SharedWords, d.SharedDeadline = list.WordGoal, list.Deadline
	for i := range list.Items {
		switch list.Items[i].Kind {
		case "daily":
			d.Daily = &list.Items[i]
		case "project":
			d.Own = &list.Items[i]
		}
	}
	to, err := s.Reg.Call(ctx, p, "stats.today", goals.StatsIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	d.Today = to.(goals.TodayOut)
	d.Timezone = d.Today.Timezone
	d.TodayCap = min(d.Today.WordsAdded, d.Today.Target)
	ho, err := s.Reg.Call(ctx, p, "stats.history", goals.HistoryIn{ProjectID: proj.ID, Days: days})
	if err != nil {
		return d, err
	}
	d.Hist = ho.(goals.HistoryOut)
	d.Weeks = goalCalendar(d.Hist, d.Today.Day)
	d.TodayLabel = goalLongDay(d.Today.Day)
	d.SharedDeadlineLabel = goalLongDay(d.SharedDeadline)
	if d.Own != nil {
		d.OwnEndLabel = goalLongDay(d.Own.EndDate)
	}
	d.Summary = "In the last " + strconv.Itoa(days) + " days you added " + goalWords(d.Hist.TotalAdded) + " on " + strconv.Itoa(d.Hist.WritingDays) + " " + goalPlural(d.Hist.WritingDays, "day", "days") + "."
	po, err := s.Reg.Call(ctx, p, "stats.project", goals.StatsIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	d.Proj = po.(goals.ProjectOut)
	if so, err := s.Reg.Call(ctx, p, "studio.project.totals", studio.ProjectTotalsIn{ProjectID: proj.ID, Days: 84}); err == nil {
		t := stT{s.page(r, "", nil).Lang}
		weeks := so.(studio.ProjectTotalsOut).Weeks
		for i := len(weeks) - 1; i >= 0; i-- {
			if w := weeks[i]; w.Words > 0 {
				d.OwnWeeks = append(d.OwnWeeks, goalWeek{Start: t.Date(w.Start), Words: commas(w.Words), Days: commas(w.WritingDays)})
			}
		}
	}
	d.DeadlineLabel = goalLongDay(d.Proj.Deadline)
	if d.Proj.Target > 0 {
		d.Percent = d.Proj.Percent
		if d.Percent > 100 {
			d.Percent = 100
		}
		d.Pace.Needed, d.Pace.DaysLeft, d.Pace.Projected = d.Proj.RequiredPerDay, d.Proj.DaysLeft, goalLongDay(d.Proj.ProjectedDate)
		if d.Proj.WritingDays > 0 {
			d.Pace.Average, d.Pace.HasAverage = d.Proj.AvgNetPerDay, true
		}
	}
	return d, nil
}

func goalPlural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func (s *Server) goalsPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	days := 30
	if r.URL.Query().Get("days") == "90" {
		days = 90
	}
	d, err := s.goalsView(r, proj, days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved")
	s.render(w, r, http.StatusOK, "goals", s.page(r, "Goals · "+proj.Title, d))
}

// goalsFail shows a refused value beside the form it came from, in plain words.
func (s *Server) goalsFail(w http.ResponseWriter, r *http.Request, proj projects.Project, err error) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid {
		s.fail(w, r, err)
		return
	}
	d, verr := s.goalsView(r, proj, 30)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	d.Error, d.Field = e.Message, e.Field
	d.Values = map[string]string{}
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			d.Values[k] = v[0]
		}
	}
	s.render(w, r, http.StatusUnprocessableEntity, "goals", s.page(r, "Goals · "+proj.Title, d))
}

// uiGoals applies one of the page's forms. The `action` field names which.
func (s *Server) uiGoals(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	p := s.principal(r)
	var err error
	action := r.PostFormValue("action")
	switch action {
	case "daily", "own":
		var n int
		field := "daily_words"
		kind := "daily"
		end := ""
		if action == "own" {
			field, kind, end = "own_words", "project", strings.TrimSpace(r.PostFormValue("own_end"))
		}
		if n, err = parseGoalNumber(field, r.PostFormValue(field)); err == nil && n == 0 {
			err = ops.Invalid(field, "Enter a number of words, or use Remove to take the target away.")
		}
		if err == nil {
			_, err = s.Reg.Call(r.Context(), p, "goal.set", goals.SetIn{ProjectID: proj.ID, Kind: kind, TargetWords: n, EndDate: end})
		}
		if err != nil {
			err = goalRelabel(err, "target_words", field)
			err = goalRelabel(err, "end_date", "own_end")
		}
	case "daily_remove", "own_remove":
		kind := "daily"
		if action == "own_remove" {
			kind = "project"
		}
		_, err = s.Reg.Call(r.Context(), p, "goal.delete", goals.DeleteIn{ProjectID: proj.ID, Kind: kind})
	case "shared":
		var n int
		if n, err = parseGoalNumber("shared_words", r.PostFormValue("shared_words")); err == nil {
			dl := strings.TrimSpace(r.PostFormValue("shared_end"))
			_, err = s.Reg.Call(r.Context(), p, "project.update", projects.UpdateIn{ID: proj.ID, WordGoal: &n, Deadline: &dl})
			err = goalRelabel(err, "word_goal", "shared_words")
			err = goalRelabel(err, "deadline", "shared_end")
		}
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	if err != nil {
		s.goalsFail(w, r, proj, err)
		return
	}
	anchor := map[string]string{"daily": "daily", "daily_remove": "daily", "own": "target", "own_remove": "target", "shared": "target"}[action]
	http.Redirect(w, r, "/p/"+proj.Slug+"/goals?saved="+action+"#"+anchor, http.StatusSeeOther)
}

// relabel points an invalid-field error at the form field the page uses.
func goalRelabel(err error, from, to string) error {
	if err == nil {
		return nil
	}
	if e := ops.AsError(err); e.Code == ops.CodeInvalid && e.Field == from {
		return ops.Invalid(to, e.Message)
	}
	return err
}

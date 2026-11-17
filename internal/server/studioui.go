package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/mail"
	"github.com/crueber/scriobh/internal/ops"
)

// The Writing Studio's pages (F-1100 and up): the dashboard, the goals, the sprints, compare and export, and the private
// progress card. Every number on them is read through the studio.* ops, which answer for the caller only, and every
// change goes through an op and answers with a redirect to a GET page, so a reload never repeats it. The charts are
// drawn here as SVG geometry and as plain tables and meters: no script is needed to see a number. No page marks a day
// that was not written, and nothing here is coloured as a warning.

func (s *Server) registerStudioUI(m *http.ServeMux) {
	m.HandleFunc("GET /studio", s.studioPage)
	m.HandleFunc("GET /studio/goals", s.studioGoalsPage)
	m.HandleFunc("GET /studio/goals/{id}", s.studioGoalPage)
	m.HandleFunc("GET /studio/sprints", s.studioSprintsPage)
	m.HandleFunc("GET /studio/data", s.studioDataPage)
	m.HandleFunc("GET /studio/card", s.studioCardPage)
	s.uiRoute("POST /ui/studio/goals", s.uiStudioGoalCreate)
	s.uiRoute("POST /ui/studio/goals/{id}/{action}", s.uiStudioGoal)
	s.uiRoute("POST /ui/studio/sprint/{action}", s.uiStudioSprint)
	s.uiRoute("POST /ui/studio/nudges/seen", s.uiStudioNudges)
	s.uiRoute("POST /ui/studio/cards", s.uiStudioCardCreate)
	s.uiRoute("POST /ui/studio/cards/{id}/revoke", s.uiStudioCardRevoke)
	s.uiRoute("POST /ui/settings/progress", s.uiSettingsProgress)
	s.uiRoute("POST /ui/settings/nudges", s.uiSettingsNudges)
}

// ---- words ---------------------------------------------------------------------------------------------------------

// stT is the page's translator with numbers written for people: a count is grouped in thousands, and a date written as
// YYYY-MM-DD is written out with the catalogue's month.
type stT struct{ lang string }

func (t stT) T(key string, args ...any) string {
	for i := 1; i < len(args); i += 2 {
		args[i] = t.show(args[i])
	}
	return i18n.Default.T(t.lang, key, args...)
}

// N is a message with a count: key.one or key.other, {n} the count grouped in thousands.
func (t stT) N(key string, n int, args ...any) string {
	form := ".other"
	if n == 1 {
		form = ".one"
	}
	return t.T(key+form, append([]any{"n", n}, args...)...)
}

// stNum is a whole number as the studio's screens write it: grouped in thousands, and a negative with the real minus sign
// (U+2212), which keeps its place beside the digits in a right-to-left line, where a hyphen would not.
func stNum(n int) string { return strings.Replace(commas(n), "-", "\u2212", 1) }

func (t stT) show(v any) any {
	switch x := v.(type) {
	case int:
		return stNum(x)
	case float64:
		return stNum(int(x))
	case string:
		if len(x) == 10 && x[4] == '-' && x[7] == '-' {
			if _, err := time.Parse("2006-01-02", x); err == nil {
				return t.Date(x)
			}
		}
	}
	return v
}

// Date writes 2026-11-03 as "3 Nov 2026" in the page's language.
func (t stT) Date(iso string) string {
	d, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return i18n.Default.T(t.lang, "studio.ui.date", "d", d.Day(), "month", i18n.Default.T(t.lang, "studio.month_short."+strconv.Itoa(int(d.Month()))), "y", d.Year())
}

// Day writes 2026-11-03 as "Tuesday 3 Nov".
func (t stT) Day(iso string) string {
	d, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	wd := (int(d.Weekday()) + 6) % 7
	return i18n.Default.T(t.lang, "studio.ui.day", "weekday", i18n.Default.T(t.lang, "studio.weekday."+strconv.Itoa(wd)), "d", d.Day(),
		"month", i18n.Default.T(t.lang, "studio.month_short."+strconv.Itoa(int(d.Month()))))
}

// When writes a moment the server stored as a date in the person's zone.
func (t stT) When(stamp string, loc *time.Location) string {
	if stamp == "" {
		return ""
	}
	d, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return stamp
	}
	return t.Date(d.In(loc).Format("2006-01-02"))
}

// Msg is a message the studio ops answered with: a key and its parameters. A key with count forms takes its count from
// days, else from n.
func (t stT) Msg(m studio.Msg) string {
	if m.Key == "" {
		return ""
	}
	var args []any
	for k, v := range m.Params {
		if s, ok := v.(string); ok && (k == "period" || k == "measure") {
			v = i18n.Default.T(t.lang, "studio."+k+"."+s)
		}
		args = append(args, k, v)
	}
	if i18n.Default.Has(m.Key) {
		return t.T(m.Key, args...)
	}
	count := 0
	for _, k := range []string{"n", "days"} {
		switch x := m.Params[k].(type) {
		case int:
			count = x
		case float64:
			count = int(x)
		}
	}
	form := ".other"
	if count == 1 {
		form = ".one"
	}
	return t.T(m.Key+form, args...)
}

// ---- the flash -------------------------------------------------------------------------------------------------------

// stFlash is what an action hands to the page it redirects to, once. Nothing a person typed travels in an address.
type stFlash struct {
	Page   string            `json:"p"`
	Msg    string            `json:"m,omitempty"`
	Err    string            `json:"e,omitempty"`
	Field  string            `json:"f,omitempty"`
	Form   string            `json:"fm,omitempty"` // which form of the page the message belongs to
	Values map[string]string `json:"v,omitempty"`
	Link   string            `json:"l,omitempty"` // an address the message offers
	Once   string            `json:"o,omitempty"` // a card's address, shown once
	Label  string            `json:"ol,omitempty"`
}

func (s *Server) stStash(r *http.Request, f stFlash) {
	if b, err := json.Marshal(f); err == nil {
		s.flashSet(r, map[string]string{"_scope": "studio", "page": f.Page, "st": string(b)})
	}
}

func (s *Server) stTake(r *http.Request, page string) stFlash {
	var f stFlash
	if fl := s.flashTakeFor(r, "studio", func(m map[string]string) bool { return m["page"] == page }); fl != nil {
		_ = json.Unmarshal([]byte(fl["st"]), &f)
	}
	return f
}

// stBase is what every studio page carries.
type stBase struct {
	Tab    string
	CSRF   string
	Flash  string
	Error  string
	Field  string
	Form   string
	Link   string
	Values map[string]string
	W      stT
	// a card's address, shown once on the page that follows the form that made it
	Once, OnceLabel string
}

func (s *Server) stBaseFor(r *http.Request, tab string) stBase {
	b := stBase{Tab: tab, CSRF: authFrom(r.Context()).CSRF, W: stT{s.page(r, "", nil).Lang}, Values: map[string]string{}}
	f := s.stTake(r, tab)
	b.Flash, b.Error, b.Field, b.Form, b.Link = f.Msg, f.Err, f.Field, f.Form, f.Link
	b.Once, b.OnceLabel = f.Once, f.Label
	if f.Values != nil {
		b.Values = f.Values
	}
	return b
}

// V is a value the person typed into a form that was refused, shown again.
func (b stBase) V(k string) string { return b.Values[k] }

func (s *Server) stAuthed(w http.ResponseWriter, r *http.Request) bool {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

// stErr turns an op's refusal into the catalogue's words, by its reason or its field. What the catalogue has no word for is
// shown as the op said it.
func (s *Server) stErr(t stT, err error) (msg, field, link string) {
	e := ops.AsError(err)
	reason, _ := e.Data["reason"].(string)
	switch {
	case reason == "goal_exists":
		id, _ := e.Data["goal_id"].(string)
		return t.T("studio.ui.err.goal_exists"), "period", "/studio/goals/" + id
	case reason == "too_many_goals":
		return t.T("studio.ui.err.too_many_goals"), "", ""
	case reason == "sprint_running":
		return t.T("studio.ui.err.sprint_running"), "", ""
	case reason == "too_many_cards":
		return t.T("studio.ui.err.too_many_cards"), "", ""
	case reason == "mail_off":
		return t.T("studio.ui.err.mail_off"), "", ""
	case e.Code == ops.CodeNotFound:
		return t.T("studio.ui.err.gone"), "", ""
	case e.Code == ops.CodeForbidden:
		return t.T("studio.ui.err.role"), "", ""
	case e.Code == ops.CodeInvalid && i18n.Default.Has("studio.ui.err.f_"+e.Field):
		return t.T("studio.ui.err.f_" + e.Field), e.Field, ""
	}
	return e.Message, e.Field, ""
}

// stRefused reports whether an error is one a form shows beside itself (a value, a conflict, a project that cannot be
// written in); anything else is the server's own page.
func stRefused(err error) bool {
	switch ops.AsError(err).Code {
	case ops.CodeInvalid, ops.CodeConflict, ops.CodeNotFound, ops.CodeForbidden:
		return true
	}
	return false
}

// ---- the dashboard ---------------------------------------------------------------------------------------------------

type stNudge struct{ ID, Text string }

type stCard struct {
	Key, Label, Value, Delta, Sub string
	Pct                           string // the change as a signed percentage, in the same ink; empty when there was nothing before
	Spark                         string // the polyline's points, in a 100 by 28 box
	SparkText                     string
}

type stMeter struct {
	Label, Text string
	Value, Max  int
	Best        bool
}

type stCalCell struct {
	Blank, Hit, Today bool
	Level             int
	Name              string
}

type stCalRow struct {
	Short, Long string
	Cells       []stCalCell
}

type stCalMonth struct {
	Span  int
	Label string
}

type stCal struct {
	Months  []stCalMonth
	Rows    []stCalRow
	Weeks   int
	All     bool
	Summary string
	Legend  string
	Empty   bool
}

type stLink struct{ Text, URL string }

type stGoal struct {
	G        studio.GoalOut
	Title    string
	Value    string
	Message  string
	Net      string
	Notes    []string
	MeterVal int
	Done     string // "Reached on 3 Nov 2026"
	Ended    string
	Editable bool
}

type stChartCol struct {
	X, W  string
	Title string
}

type stChartRow struct{ Label, Words, Net, Minutes string }

type stChart struct {
	Empty      bool
	Line, Area string
	Cols       []stChartCol
	YTop, YMid string
	XLabels    []string
	Desc       string
	Rows       []stChartRow
	Range      string
	From, To   string
	Period     string
	Pace       []string
	BucketHead string
}

type stSprint struct {
	Line, Words   string
	Title, Slug   string
	Open, Running bool
}

type stProject struct {
	Title, Slug       string
	Today, Week, Prev string
}

type stProjectOpt struct {
	ID, Title string
	CanWrite  bool
}

type studioData struct {
	stBase
	Zone      string
	Nudges    []stNudge
	NudgeIDs  string
	Cards     []stCard
	Streak    studio.StreakOut
	StreakMsg string
	StreakFig string
	Longest   string
	StreakDay string
	Days30    string // said in place of a run of 0 days: what the past month holds
	Note      string
	TodayMsg  string
	TodayMax  int
	TodayVal  int
	Quick     *studio.ResumeOut // where "write N words today" goes; nil when there is nowhere the person may write
	QuickN    int
	QuickText string
	Resume    *studio.ResumeOut
	Cal       stCal
	CalRanges []stLink
	Goals     []stGoal
	GoalsLine string
	NoGoals   bool
	Chart     stChart
	Weekdays  []stMeter
	Parts     []stMeter
	BestDay   string
	BestHour  string
	Trend     string
	TrendFig  string
	Resources []stLink
	Projects  []stProject
	Hidden    string
	Sprint    *stSprint
	Writable  []stProjectOpt
	New       bool // nothing has been written yet: the page says how it will fill
}

var stRanges = []string{"30d", "90d", "365d", "all"}

func (s *Server) stProjects(r *http.Request) []stProjectOpt {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.list", projects.ListIn{})
	if err != nil {
		return nil
	}
	var opts []stProjectOpt
	for _, p := range out.(projects.ListOut).Items {
		if p.Locked { // a project this session has not unlocked is not offered: a goal or a sprint on it would be refused
			continue
		}
		opts = append(opts, stProjectOpt{ID: p.ID, Title: displayTitle(p.Title), CanWrite: p.Role == ops.ProjectOwner || p.Role == ops.ProjectEditor})
	}
	return opts
}

func stWritable(all []stProjectOpt) []stProjectOpt {
	var out []stProjectOpt
	for _, p := range all {
		if p.CanWrite {
			out = append(out, p)
		}
	}
	return out
}

func (s *Server) studioPage(w http.ResponseWriter, r *http.Request) {
	if !s.stAuthed(w, r) {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	d := studioData{stBase: s.stBaseFor(r, "studio")}
	t := d.W
	q := r.URL.Query()
	in := studio.DashboardIn{Range: q.Get("range"), From: strings.TrimSpace(q.Get("from")), To: strings.TrimSpace(q.Get("to"))}
	out, err := s.Reg.Call(ctx, p, "studio.dashboard", in)
	if err != nil {
		if ops.AsError(err).Code != ops.CodeInvalid {
			s.fail(w, r, err)
			return
		}
		// a range the op refuses: the page as it is by default, with the refusal beside the dates as they were typed
		d.Error, d.Field, d.Form = t.T("studio.ui.err.range"), "from", "range"
		d.Values = map[string]string{"from": in.From, "to": in.To}
		if out, err = s.Reg.Call(ctx, p, "studio.dashboard", studio.DashboardIn{}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	o := out.(studio.DashboardOut)
	var calAll *studio.Calendar
	if q.Get("cal") == "all" {
		if co, err := s.Reg.Call(ctx, p, "studio.calendar", studio.CalendarIn{All: true}); err == nil {
			c := co.(studio.Calendar)
			calAll = &c
		}
	}
	d.fill(o, s.stProjects(r), calAll)
	s.render(w, r, http.StatusOK, "studio", s.page(r, t.T("studio.ui.title"), d))
}

// fill reads what the dashboard op answered into what the page draws.
func (d *studioData) fill(o studio.DashboardOut, all []stProjectOpt, calAll *studio.Calendar) {
	t := d.W
	d.Zone = o.Timezone
	for _, n := range o.Nudges {
		d.Nudges = append(d.Nudges, stNudge{ID: n.ID, Text: t.Msg(studio.Msg{Key: n.Key, Params: n.Params})})
	}
	for _, c := range o.Cards {
		d.Cards = append(d.Cards, stCardOf(t, c))
	}
	d.New = o.Calendar.AllTimeDays == 0
	d.Streak = o.Streak
	d.StreakMsg = t.Msg(o.Streak.Message)
	d.StreakDay = t.N("studio.card.streak", o.Streak.Current)
	d.Longest = t.N("studio.card.longest", o.Streak.Longest)
	if o.Streak.Current == 0 {
		// a run that has lapsed is not drawn as a large 0: the past month is what is said (nothing, if it holds nothing)
		for _, c := range o.Cards {
			if c.Key == "30d" && c.WritingDays > 0 {
				d.Days30 = t.N("studio.card.days30", c.WritingDays)
			}
		}
	}
	if o.Streak.Note != nil {
		d.Note = t.Msg(*o.Streak.Note)
	}
	switch {
	case o.Streak.HasGoal && o.Streak.Measure == "words":
		// the words of the goal the card reads, which may belong to one project
		d.StreakFig = t.T("studio.ui.streak.today", "words", o.Streak.TodayValue, "target", o.Streak.TodayTarget)
		for _, pr := range all {
			if o.Today.ProjectID != "" && pr.ID == o.Today.ProjectID {
				d.StreakFig = t.T("studio.ui.streak.today_in", "words", o.Streak.TodayValue, "target", o.Streak.TodayTarget, "title", pr.Title)
			}
		}
		d.TodayMax, d.TodayVal = o.Streak.TodayTarget, min(o.Streak.TodayValue, o.Streak.TodayTarget)
		if o.Today.Message != nil {
			d.TodayMsg = t.Msg(*o.Today.Message)
		}
	case o.Streak.HasGoal && o.Streak.Measure != "":
		// a goal that counts something else is said in what it counts
		d.StreakFig = t.T("studio.ui.streak.today_of", "measure", t.T("studio.measure."+o.Streak.Measure), "value", o.Streak.TodayValue, "target", o.Streak.TodayTarget)
		d.TodayMax, d.TodayVal = o.Streak.TodayTarget, min(o.Streak.TodayValue, o.Streak.TodayTarget)
	default:
		d.StreakFig = t.N("studio.ui.today.words", o.Today.Words)
	}
	d.Writable = stWritable(all)
	d.Resume = o.Resume
	if o.Resume != nil {
		for _, pr := range d.Writable {
			if pr.ID == o.Resume.ProjectID {
				d.Quick = o.Resume
			}
		}
	}
	// "write N words today" goes to the project the person was last in; a goal that belongs to another project would not count it
	if d.Quick != nil && o.Today.HasGoal && o.Today.Remaining > 0 && (o.Today.ProjectID == "" || o.Today.ProjectID == d.Quick.ProjectID) {
		d.QuickN = o.Today.Remaining
		d.QuickText = t.N("studio.ui.quick", o.Today.Remaining)
	}
	d.Cal = stCalOf(t, o.Calendar, o.Day, false)
	if calAll != nil {
		d.Cal = stCalOf(t, *calAll, o.Day, true)
	}
	d.GoalsLine = t.T("studio.ui.goals.counts", "active", o.Goals.Active, "done", o.Goals.Completed)
	d.NoGoals = len(o.Goals.Items) == 0
	for _, g := range o.Goals.Items {
		if len(d.Goals) == 4 { // the panel is a summary: the goals page has them all
			break
		}
		d.Goals = append(d.Goals, stGoalOf(t, g))
	}
	d.Chart = stChartOf(t, o.Series)
	peak := 0
	for _, wd := range o.Series.Weekdays {
		peak = max(peak, wd.Words)
	}
	for i, wd := range o.Series.Weekdays {
		d.Weekdays = append(d.Weekdays, stMeter{Label: t.T("studio.weekday." + strconv.Itoa(i)), Text: t.N("studio.ui.words", wd.Words), Value: wd.Words, Max: max(peak, 1),
			Best: o.Series.BestWeekday != nil && *o.Series.BestWeekday == i})
	}
	peak = 0
	for _, pt := range o.Series.Parts {
		peak = max(peak, pt.Words)
	}
	for _, pt := range o.Series.Parts {
		d.Parts = append(d.Parts, stMeter{Label: t.T("studio.part." + pt.Part), Text: t.N("studio.ui.words", pt.Words), Value: pt.Words, Max: max(peak, 1)})
	}
	if o.Series.BestWeekday != nil {
		d.BestDay = t.T("studio.ui.best_day", "weekday", t.T("studio.weekday."+strconv.Itoa(*o.Series.BestWeekday)))
	}
	if o.Series.BestHour != nil {
		d.BestHour = t.T("studio.ui.best_hour", "from", fmt.Sprintf("%02d:00", *o.Series.BestHour), "to", fmt.Sprintf("%02d:00", (*o.Series.BestHour+1)%24))
	}
	d.Trend = t.T(o.Trend.MessageKey)
	if o.Trend.RecentWords+o.Trend.PreviousWords > 0 {
		d.TrendFig = t.T("studio.ui.trend.fig", "recent", o.Trend.RecentWords, "before", o.Trend.PreviousWords)
	}
	for _, a := range o.Trend.Resources {
		u := map[string]string{"sprint": "/studio/sprints", "goal": "/studio/goals", "calendar": "/studio#activity"}[a.Action]
		if u != "" {
			d.Resources = append(d.Resources, stLink{Text: t.T(a.Key), URL: u})
		}
	}
	for _, pr := range o.Projects {
		d.Projects = append(d.Projects, stProject{Title: displayTitle(pr.Title), Slug: pr.Slug, Today: t.N("studio.ui.words", pr.Today), Week: t.N("studio.ui.words", pr.Week), Prev: t.N("studio.ui.words", pr.PreviousWeek)})
	}
	if o.Hidden > 0 {
		d.Hidden = t.N("studio.ui.hidden", o.Hidden)
	}
	d.Sprint = stSprintOf(t, o.Sprint, all)
	// the links that change what the page shows are plain addresses: the chart's range and the calendar's length
	cal := ""
	if d.Cal.All {
		cal = "&cal=all"
	}
	rng := o.Series.Range
	if rng == "custom" {
		rng = "30d"
	}
	d.CalRanges = []stLink{{Text: t.T("studio.ui.cal.r24"), URL: "/studio?range=" + rng + "#activity"}, {Text: t.T("studio.ui.cal.rall"), URL: "/studio?range=" + rng + "&cal=all#activity"}}
	d.Chart.Period = cal
}

func stCardOf(t stT, c studio.Card) stCard {
	out := stCard{Key: c.Key, Label: t.T("studio.ui.card." + c.Key), Value: stNum(c.Words)}
	ch := c.Delta.Change
	pre := "studio.ui.delta." + c.Key
	// What was added is said; a smaller figure than the period before is not. Nothing is said when there is nothing to
	// compare with (a quiet period before is not news), and today, which is not over, is never set against a whole day:
	// it says "more than yesterday" once it is, and otherwise nothing.
	switch {
	case c.Previous.Words == 0:
	case ch == 0:
		out.Delta = t.T(pre + ".same")
	case ch > 0:
		out.Delta = t.N(pre+".more", ch)
	case c.Key != "today":
		out.Delta = t.N(pre+".fewer", -ch)
	}
	// a percentage is only said of a gain: -15% is the loudest part of a decline, and today's day is not over
	if p := c.Delta.Percent; p != nil && out.Delta != "" && *p > 0 && c.Key != "today" {
		out.Pct = fmt.Sprintf("+%d%%", *p)
	}
	if c.Key == "today" {
		if c.Minutes > 0 {
			out.Sub = t.N("studio.ui.minutes", c.Minutes)
		}
	} else if c.WritingDays > 0 {
		out.Sub = t.N("studio.ui.writing_days", c.WritingDays)
	}
	out.Spark = stPolyline(c.Spark, 100, 28, 2)
	nums := make([]string, len(c.Spark))
	for i, v := range c.Spark {
		nums[i] = stNum(v)
	}
	unit := "studio.ui.spark.days"
	if c.Key == "365d" {
		unit = "studio.ui.spark.weeks"
	}
	out.SparkText = t.T(unit, "list", strings.Join(nums, ", "))
	return out
}

// stPolyline places values across a box, oldest first, the largest at the top less a margin. Coordinates have one decimal.
func stPolyline(vals []int, w, h, pad float64) string {
	if len(vals) == 0 {
		return ""
	}
	peak := 0
	for _, v := range vals {
		peak = max(peak, v)
	}
	var b strings.Builder
	for i, v := range vals {
		x := w / 2
		if len(vals) > 1 {
			x = float64(i) * w / float64(len(vals)-1)
		}
		y := h - pad
		if peak > 0 {
			y = h - pad - float64(v)*(h-2*pad)/float64(peak)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	return b.String()
}

func stCalOf(t stT, c studio.Calendar, today string, all bool) stCal {
	out := stCal{Weeks: len(c.Weeks), All: all, Empty: c.AllTimeDays == 0}
	label := map[int]bool{}
	for _, i := range c.LabelRows {
		label[i] = true
	}
	for row := 0; row < 7; row++ {
		r := stCalRow{Long: t.T("studio.weekday." + strconv.Itoa(row))}
		if label[row] {
			r.Short = t.T("studio.weekday_short." + strconv.Itoa(row))
		}
		for _, wk := range c.Weeks {
			if row >= len(wk.Cells) || wk.Cells[row].Future {
				r.Cells = append(r.Cells, stCalCell{Blank: true})
				continue
			}
			cell := wk.Cells[row]
			key := "studio.ui.cal.cell"
			if cell.Hit {
				key = "studio.ui.cal.cell_hit"
			}
			name := t.N(key, cell.Words, "date", t.Date(cell.Day))
			if cell.Words == 0 {
				name = t.T("studio.ui.cal.cell_none", "date", t.Date(cell.Day))
			}
			r.Cells = append(r.Cells, stCalCell{Level: cell.Level, Hit: cell.Hit, Today: cell.Day == today, Name: name})
		}
		out.Rows = append(out.Rows, r)
	}
	// the months as a row of headings over the weeks they begin in; a month with too little room to be read says nothing
	span, name := 0, ""
	flush := func() {
		if span == 0 {
			return
		}
		if span < 3 {
			name = ""
		}
		out.Months = append(out.Months, stCalMonth{Span: span, Label: name})
	}
	for i, wk := range c.Weeks {
		if wk.Month > 0 && i > 0 {
			flush()
			span, name = 0, ""
		}
		if wk.Month > 0 {
			name = t.T("studio.month_short." + strconv.Itoa(wk.Month))
			if all && (wk.Month == 1 || i == 0) && len(wk.Start) >= 4 {
				y := wk.Start[:4]
				if wk.Month == 1 && len(wk.Cells) > 0 {
					y = wk.Cells[len(wk.Cells)-1].Day[:4] // the week that holds 1 January may begin in December
				}
				name = t.T("studio.ui.cal.month_year", "month", name, "y", y)
			}
		}
		span++
	}
	flush()
	if all {
		out.Summary = t.N("studio.ui.cal.sum_all", c.WritingDays)
	} else {
		out.Summary = t.N("studio.ui.cal.sum", c.WritingDays, "weeks", len(c.Weeks))
	}
	th := c.Thresholds
	switch {
	case th.Days == 0:
	case th.Uniform:
		out.Legend = t.T("studio.ui.cal.legend_uniform")
	default:
		out.Legend = t.T("studio.ui.cal.legend", "a", th.Cuts[0], "b", th.Cuts[1], "c", th.Cuts[2])
	}
	return out
}

func stGoalOf(t stT, g studio.GoalOut) stGoal {
	out := stGoal{G: g, Title: t.T("studio.ui.goal.t." + g.Period + "." + g.Measure), Editable: g.Source == "studio" && g.Status != "archived"}
	out.Value = t.T("studio.ui.goal.value."+g.Measure, "value", g.Value, "target", g.Target)
	out.MeterVal = min(g.Value, g.Target)
	switch {
	case g.CompletedOn != "":
		out.Message = t.T("studio.ui.goal.done_on", "date", g.CompletedOn)
	case g.Met:
		out.Message = t.T("studio.ui.goal.met." + g.Period)
	default:
		out.Message = t.N("studio.ui.goal.left."+g.Measure, g.Remaining)
	}
	if g.Net != nil && *g.Net != g.Value && g.Measure == "words" && g.Period != "total" {
		out.Net = t.T("studio.ui.goal.net", "n", *g.Net)
	}
	if f := g.Forecast; f != nil && !g.Met {
		for _, n := range f.Notes {
			if n.Key == "studio.forecast.behind" { // an answer from before: a projection after a deadline is not counted out in days
				continue
			}
			out.Notes = append(out.Notes, t.Msg(n))
		}
	}
	if g.EndedOn != "" || g.Status == "archived" {
		// an ended goal says when it ended, and nothing about what was still to go
		out.Notes = nil
		if g.EndedOn != "" {
			out.Ended = t.T("studio.ui.goal.ended", "date", g.EndedOn)
		}
		if !g.Met && g.CompletedOn == "" {
			out.Message = ""
		}
	}
	return out
}

// stNice is the round number at or above n that the chart's top line is drawn at.
func stNice(n int) int {
	if n <= 10 {
		return 10
	}
	p := 1
	for p*10 < n {
		p *= 10
	}
	for _, m := range []int{1, 2, 5, 10} {
		if m*p >= n {
			return m * p
		}
	}
	return 10 * p
}

func stChartOf(t stT, so studio.SeriesOut) stChart {
	c := stChart{Range: so.Range, From: so.From, To: so.To, Empty: so.Figures.Words == 0}
	c.BucketHead = t.T("studio.ui.chart.h_" + so.Bucket)
	label := func(p studio.Point) string {
		switch so.Bucket {
		case "week":
			return t.T("studio.ui.chart.week_of", "date", p.Start)
		case "month":
			if d, err := time.Parse("2006-01-02", p.Start); err == nil {
				return t.T("studio.ui.cal.month_year", "month", t.T("studio.month_short."+strconv.Itoa(int(d.Month()))), "y", strconv.Itoa(d.Year()))
			}
		}
		return t.Date(p.Start)
	}
	vals := make([]int, len(so.Points))
	peak := 0
	for i, p := range so.Points {
		vals[i] = p.Words
		peak = max(peak, p.Words)
		c.Rows = append(c.Rows, stChartRow{Label: label(p), Words: stNum(p.Words), Net: stNum(p.Net), Minutes: stNum(p.Minutes)})
	}
	top := stNice(peak)
	const w, h, pad = 600.0, 160.0, 8.0
	n := len(vals)
	var line strings.Builder
	for i, v := range vals {
		x := w / 2
		if n > 1 {
			x = float64(i) * w / float64(n-1)
		}
		y := h - pad - float64(v)*(h-2*pad)/float64(top)
		if i == 0 {
			fmt.Fprintf(&line, "M%.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&line, " L%.1f %.1f", x, y)
		}
		step := w
		if n > 1 {
			step = w / float64(n-1)
		}
		c.Cols = append(c.Cols, stChartCol{X: fmt.Sprintf("%.1f", max(0, x-step/2)), W: fmt.Sprintf("%.1f", step), Title: t.N("studio.ui.chart.point", v, "when", label(so.Points[i]))})
	}
	c.Line = line.String()
	if n > 0 {
		c.Area = c.Line + fmt.Sprintf(" L%.1f %.1f L0 %.1f Z", w, h-pad, h-pad)
		if n == 1 {
			c.Area = ""
		}
		c.XLabels = []string{label(so.Points[0])}
		if n > 2 {
			c.XLabels = append(c.XLabels, label(so.Points[n/2]))
		}
		if n > 1 {
			c.XLabels = append(c.XLabels, label(so.Points[n-1]))
		}
	}
	c.YTop, c.YMid = stNum(top), stNum(top/2)
	c.Desc = t.N("studio.ui.chart.desc", so.Figures.Words, "from", so.From, "to", so.To)
	c.Pace = []string{t.N("studio.ui.pace.day", so.AvgPerDay)}
	if so.Figures.WritingDays > 0 {
		c.Pace = append(c.Pace, t.N("studio.ui.pace.writing_day", so.AvgPerWritingDay), t.N("studio.ui.pace.days", so.Figures.WritingDays))
	}
	if so.Figures.Net != so.Figures.Words {
		c.Pace = append(c.Pace, t.T("studio.ui.pace.net", "n", so.Figures.Net))
	}
	return c
}

func stSprintOf(t stT, sp *studio.SprintOut, all []stProjectOpt) *stSprint {
	if sp == nil || sp.State != "running" {
		return nil
	}
	out := &stSprint{Running: true, Open: sp.PlannedMinutes == 0}
	switch {
	case out.Open:
		out.Line = t.N("studio.ui.sprint.open_for", sp.ElapsedSecs/60)
	case sp.Over || sp.RemainingSecs <= 0:
		out.Line = t.T("studio.sprint.over")
	default:
		out.Line = t.N("studio.ui.sprint.left", (sp.RemainingSecs+59)/60)
	}
	if sp.TargetWords > 0 {
		out.Words = t.T("studio.ui.sprint.words_of", "n", sp.WordsAdded, "target", sp.TargetWords)
	} else {
		out.Words = t.N("studio.ui.sprint.words", sp.WordsAdded)
	}
	for _, p := range all {
		if p.ID == sp.ProjectID {
			out.Title = p.Title
		}
	}
	return out
}

func (s *Server) uiStudioNudges(w http.ResponseWriter, r *http.Request) {
	ids := strings.Fields(r.PostFormValue("ids"))
	if len(ids) > 0 {
		if _, err := s.Reg.Call(r.Context(), s.principal(r), "studio.nudge.seen", studio.NudgeSeenIn{IDs: ids}); err != nil && !stRefused(err) {
			s.fail(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/studio", http.StatusSeeOther)
}

// ---- goals -----------------------------------------------------------------------------------------------------------

type studioGoalsData struct {
	stBase
	Active, Done, Archived []stGoal
	Counts                 string
	Projects               []stProjectOpt
	Periods, Measures      []opt
}

func (s *Server) stGoalWords(t stT) (periods, measures []opt) {
	for _, p := range []string{"day", "week", "month", "total"} {
		periods = append(periods, opt{p, t.T("studio.ui.goal.p." + p)})
	}
	for _, m := range []string{"words", "sessions", "chapters", "minutes"} {
		measures = append(measures, opt{m, t.T("studio.ui.goal.m." + m)})
	}
	return
}

func (s *Server) studioGoalsPage(w http.ResponseWriter, r *http.Request) {
	if !s.stAuthed(w, r) {
		return
	}
	d := studioGoalsData{stBase: s.stBaseFor(r, "goals")}
	t := d.W
	out, err := s.Reg.Call(r.Context(), s.principal(r), "studio.goal.list", studio.GoalListIn{Archived: true})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	o := out.(studio.GoalsOut)
	for _, g := range o.Items {
		v := stGoalOf(t, g)
		switch g.Status {
		case "archived":
			d.Archived = append(d.Archived, v)
		case "completed":
			d.Done = append(d.Done, v)
		default:
			d.Active = append(d.Active, v)
		}
	}
	d.Counts = t.T("studio.ui.goals.counts", "active", o.Active, "done", o.Completed)
	d.Projects = s.stProjects(r)
	d.Periods, d.Measures = s.stGoalWords(t)
	if d.Values["period"] == "" {
		d.Values["period"] = "day"
	}
	if d.Values["measure"] == "" {
		d.Values["measure"] = "words"
	}
	if pid := r.URL.Query().Get("project"); pid != "" && d.Values["project_id"] == "" {
		for _, p := range d.Projects { // a project's Goals page links here with its own project chosen; only one the person can see
			if p.ID == pid {
				d.Values["project_id"] = pid
			}
		}
	}
	s.render(w, r, http.StatusOK, "studio-goals", s.page(r, t.T("studio.ui.goals.title"), d))
}

type stPeriodRow struct {
	Range, Target, Value, Net string
	Met                       bool
}

type stVersionRow struct{ N, Target, Deadline, From string }

type studioGoalData struct {
	stBase
	Goal     stGoal
	Versions []stVersionRow
	Periods  []stPeriodRow
	Total    bool
}

func (s *Server) studioGoalPage(w http.ResponseWriter, r *http.Request) {
	if !s.stAuthed(w, r) {
		return
	}
	d := studioGoalData{stBase: s.stBaseFor(r, "goal")}
	t := d.W
	out, err := s.Reg.Call(r.Context(), s.principal(r), "studio.goal.history", studio.GoalHistoryIn{ID: r.PathValue("id"), Periods: 12})
	if err != nil {
		if ops.AsError(err).Code == ops.CodeInvalid {
			err = ops.NotFound("goal")
		}
		s.fail(w, r, err)
		return
	}
	o := out.(studio.GoalHistoryOut)
	d.Goal = stGoalOf(t, o.Goal)
	d.Total = o.Goal.Period == "total"
	for i := len(o.Versions) - 1; i >= 0; i-- {
		v := o.Versions[i]
		row := stVersionRow{N: strconv.Itoa(v.Version), Target: stNum(v.Target), From: t.Date(v.From)}
		if v.Deadline != "" {
			row.Deadline = t.Date(v.Deadline)
		}
		d.Versions = append(d.Versions, row)
	}
	for i := len(o.Periods) - 1; i >= 0; i-- {
		p := o.Periods[i]
		row := stPeriodRow{Range: t.Date(p.From), Target: stNum(p.Target), Value: stNum(p.Value), Met: p.Met}
		if p.From != p.To {
			row.Range = t.T("studio.ui.range", "from", p.From, "to", p.To)
		}
		if p.Net != nil {
			row.Net = stNum(*p.Net)
		}
		d.Periods = append(d.Periods, row)
	}
	d.Tab = "goals"
	s.render(w, r, http.StatusOK, "studio-goal", s.page(r, d.Goal.Title, d))
}

func stInt(v string) (int, bool) {
	v = strings.NewReplacer(",", "", " ", "", "_", "").Replace(strings.TrimSpace(v))
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= 0
}

func stPosted(r *http.Request, keys ...string) map[string]string {
	v := map[string]string{}
	for _, k := range keys {
		if x := strings.TrimSpace(r.PostFormValue(k)); x != "" {
			v[k] = x
		}
	}
	return scrubValues(v)
}

func (s *Server) uiStudioGoalCreate(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	vals := stPosted(r, "period", "measure", "target", "project_id", "deadline")
	back := func(f stFlash) {
		f.Page = "goals"
		s.stStash(r, f)
		// no fragment: the page focuses what it has to say (a browser does not autofocus a page opened at a fragment)
		http.Redirect(w, r, "/studio/goals", http.StatusSeeOther)
	}
	n, ok := stInt(r.PostFormValue("target"))
	if !ok || n == 0 {
		back(stFlash{Err: t.T("studio.ui.err.f_target"), Field: "target", Form: "new", Values: vals})
		return
	}
	in := studio.GoalCreateIn{Period: r.PostFormValue("period"), Measure: r.PostFormValue("measure"), Target: n, ProjectID: r.PostFormValue("project_id"), Deadline: strings.TrimSpace(r.PostFormValue("deadline"))}
	if in.Period != "total" {
		in.Deadline = "" // the date field is on the form for every period; it belongs to a total goal only, and the page says so
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "studio.goal.create", in); err != nil {
		if !stRefused(err) {
			s.fail(w, r, err)
			return
		}
		msg, field, link := s.stErr(t, err)
		if ops.AsError(err).Code == ops.CodeNotFound {
			msg, field = t.T("studio.ui.err.f_project_id"), "project_id"
		}
		back(stFlash{Err: msg, Field: field, Link: link, Form: "new", Values: vals})
		return
	}
	back(stFlash{Msg: t.T("studio.ui.goals.created")})
}

func (s *Server) uiStudioGoal(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	id := r.PathValue("id")
	ctx, p := r.Context(), s.principal(r)
	page := func(f stFlash) {
		f.Page = "goal"
		s.stStash(r, f)
		http.Redirect(w, r, "/studio/goals/"+id, http.StatusSeeOther)
	}
	var err error
	switch r.PathValue("action") {
	case "update":
		in := studio.GoalUpdateIn{ID: id}
		n, ok := stInt(r.PostFormValue("target"))
		if !ok || n == 0 {
			page(stFlash{Err: t.T("studio.ui.err.f_target"), Field: "target", Values: stPosted(r, "target", "deadline")})
			return
		}
		in.Target = &n
		if _, has := r.PostForm["deadline"]; has {
			dl := strings.TrimSpace(r.PostFormValue("deadline"))
			in.Deadline = &dl
		}
		if _, err = s.Reg.Call(ctx, p, "studio.goal.update", in); err == nil {
			page(stFlash{Msg: t.T("studio.ui.goals.updated")})
			return
		}
	case "archive":
		if _, err = s.Reg.Call(ctx, p, "studio.goal.archive", studio.GoalArchiveIn{ID: id}); err == nil {
			s.stStash(r, stFlash{Page: "goals", Msg: t.T("studio.ui.goals.archived")})
			http.Redirect(w, r, "/studio/goals", http.StatusSeeOther)
			return
		}
	default:
		err = ops.NotFound("page")
	}
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	msg, field, _ := s.stErr(t, err)
	if e.Code == ops.CodeConflict {
		msg = t.T("studio.ui.err.archived")
	}
	page(stFlash{Err: msg, Field: field, Values: stPosted(r, "target", "deadline")})
}

// ---- sprints ---------------------------------------------------------------------------------------------------------

type stSprintRow struct{ Day, Length, Words, State string }

type studioSprintsData struct {
	stBase
	Sprint   *stSprint
	Writable []stProjectOpt
	Resume   string
	Range    string
	Ranges   []string
	Facts    []stLink // a label and its figure
	Best     string
	Parts    []stMeter
	Recent   []stSprintRow
	None     bool
}

func (s *Server) studioSprintsPage(w http.ResponseWriter, r *http.Request) {
	if !s.stAuthed(w, r) {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	d := studioSprintsData{stBase: s.stBaseFor(r, "sprints"), Ranges: stRanges}
	t := d.W
	d.Range = r.URL.Query().Get("range")
	if d.Range == "" {
		d.Range = "90d"
	}
	out, err := s.Reg.Call(ctx, p, "studio.sprint.stats", studio.SprintStatsIn{Range: d.Range})
	if err != nil {
		if ops.AsError(err).Code != ops.CodeInvalid {
			s.fail(w, r, err)
			return
		}
		d.Range = "90d"
		if out, err = s.Reg.Call(ctx, p, "studio.sprint.stats", studio.SprintStatsIn{Range: d.Range}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	o := out.(studio.SprintStatsOut)
	all := s.stProjects(r)
	d.Writable = stWritable(all)
	if co, err := s.Reg.Call(ctx, p, "studio.sprint.current", studio.SprintEndIn{}); err == nil {
		d.Sprint = stSprintOf(t, co.(studio.SprintCurrentOut).Sprint, all)
	}
	if do, err := s.Reg.Call(ctx, p, "studio.dashboard", studio.DashboardIn{}); err == nil {
		if res := do.(studio.DashboardOut).Resume; res != nil {
			d.Resume = res.ProjectID
		}
	}
	d.None = o.Sprints == 0
	d.Facts = []stLink{
		{Text: t.T("studio.ui.sprints.count"), URL: stNum(o.Sprints)},
		{Text: t.T("studio.ui.sprints.words"), URL: stNum(o.Words)},
		{Text: t.T("studio.ui.sprints.avg"), URL: stNum(o.AvgPerSprint)},
		{Text: t.T("studio.ui.sprints.minutes"), URL: stNum(o.Minutes)},
	}
	if o.Best != nil {
		d.Best = t.N("studio.ui.sprints.best", o.Best.WordsAdded, "date", o.Best.Day, "minutes", o.Best.Minutes)
	}
	peak := 0
	for _, pt := range o.Parts {
		peak = max(peak, pt.Words)
	}
	for _, pt := range o.Parts {
		d.Parts = append(d.Parts, stMeter{Label: t.T("studio.part." + pt.Part), Text: t.N("studio.ui.sprints.part", pt.Sprints, "words", pt.Words), Value: pt.Words, Max: max(peak, 1)})
	}
	for _, sp := range o.Recent {
		row := stSprintRow{Day: t.Date(sp.Day), Words: stNum(sp.WordsAdded), State: t.T("studio.ui.sprints.s_" + sp.State)}
		if sp.PlannedMinutes == 0 {
			row.Length = t.T("studio.sprint.open")
		} else {
			row.Length = t.N("studio.ui.minutes", sp.PlannedMinutes)
		}
		d.Recent = append(d.Recent, row)
	}
	s.render(w, r, http.StatusOK, "studio-sprints", s.page(r, t.T("studio.ui.sprints.title"), d))
}

// uiStudioSprint starts, stops or sets aside a sprint from a plain form. A start goes to the project's writing view, where
// the words are written; the others come back to the page that asked.
func (s *Server) uiStudioSprint(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	ctx, p := r.Context(), s.principal(r)
	from := "sprints"
	if r.PostFormValue("back") == "studio" {
		from = "studio"
	}
	back := func(f stFlash) {
		f.Page = from
		s.stStash(r, f)
		u := "/studio/sprints"
		if from == "studio" {
			u = "/studio"
		}
		http.Redirect(w, r, u, http.StatusSeeOther)
	}
	refuse := func(err error, vals map[string]string) {
		if !stRefused(err) {
			s.fail(w, r, err)
			return
		}
		msg, field, _ := s.stErr(t, err)
		if ops.AsError(err).Code == ops.CodeConflict && ops.AsError(err).Data["reason"] == nil {
			msg = t.T("studio.ui.err.no_sprint")
		}
		back(stFlash{Err: msg, Field: field, Form: "sprint", Values: vals})
	}
	switch r.PathValue("action") {
	case "start":
		vals := stPosted(r, "project_id", "minutes", "custom")
		in := studio.SprintStartIn{ProjectID: r.PostFormValue("project_id")}
		switch m := r.PostFormValue("minutes"); m {
		case "custom":
			n, ok := stInt(r.PostFormValue("custom"))
			if !ok || n < 1 {
				back(stFlash{Err: t.T("studio.ui.err.f_minutes"), Field: "minutes", Form: "sprint", Values: vals})
				return
			}
			in.Minutes = n
		default:
			n, ok := stInt(m)
			if !ok {
				back(stFlash{Err: t.T("studio.ui.err.f_minutes"), Field: "minutes", Form: "sprint", Values: vals})
				return
			}
			in.Minutes = n
		}
		if n, ok := stInt(r.PostFormValue("target_words")); ok {
			in.TargetWords = n
		}
		if in.ProjectID == "" {
			back(stFlash{Err: t.T("studio.ui.err.f_project_id"), Field: "project_id", Form: "sprint", Values: vals})
			return
		}
		if _, err := s.Reg.Call(ctx, p, "studio.sprint.start", in); err != nil {
			refuse(err, vals)
			return
		}
		po, err := s.Reg.Call(ctx, p, "project.get", projects.GetIn{ID: in.ProjectID})
		if err != nil {
			back(stFlash{Msg: t.T("studio.ui.sprint.started")})
			return
		}
		http.Redirect(w, r, "/p/"+po.(projects.Item).Item.Addressed().Slug, http.StatusSeeOther)
	case "stop", "abandon":
		out, err := s.Reg.Call(ctx, p, "studio.sprint."+r.PathValue("action"), studio.SprintEndIn{})
		if err != nil {
			refuse(err, nil)
			return
		}
		sp := out.(studio.SprintItem).Item
		switch sp.State {
		case "abandoned":
			back(stFlash{Msg: t.T("studio.sprint.abandoned")})
		case "completed":
			back(stFlash{Msg: t.T("studio.sprint.done", "n", sp.WordsAdded)})
		default:
			back(stFlash{Msg: t.T("studio.sprint.stopped", "n", sp.WordsAdded)})
		}
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

// ---- compare and export ----------------------------------------------------------------------------------------------

type stCompareRow struct{ Label, A, B, Change string }

type studioDataData struct {
	stBase
	Projects []stProjectOpt
	Preset   string
	Presets  []opt
	Has      bool
	Compared bool // false: the period has no finished day yet, so there is one column and no difference
	HeadA    string
	HeadB    string
	Notes    []string
	Rows     []stCompareRow
	Ranges   []opt
	Tables   []opt
}

func (s *Server) studioDataPage(w http.ResponseWriter, r *http.Request) {
	if !s.stAuthed(w, r) {
		return
	}
	d := studioDataData{stBase: s.stBaseFor(r, "data")}
	t := d.W
	q := r.URL.Query()
	d.Projects = s.stProjects(r)
	for _, k := range []string{"this_week", "this_month", "last_30"} {
		d.Presets = append(d.Presets, opt{k, t.T("studio.ui.compare.p_" + k)})
	}
	for _, k := range stRanges {
		d.Ranges = append(d.Ranges, opt{k, t.T("studio.ui.range." + k)})
	}
	for _, k := range []string{"daily", "weekly", "goals", "sprints", "projects"} {
		d.Tables = append(d.Tables, opt{k, t.T("studio.ui.export.t_" + k)})
	}
	in := studio.CompareIn{Preset: q.Get("preset"), AFrom: q.Get("a_from"), ATo: q.Get("a_to"), BFrom: q.Get("b_from"), BTo: q.Get("b_to"), AProject: q.Get("a_project"), BProject: q.Get("b_project")}
	custom := in.AFrom != "" || in.ATo != "" || in.BFrom != "" || in.BTo != ""
	if in.Preset == "" && !custom {
		in.Preset = "this_week"
	}
	if custom {
		in.Preset = ""
		d.Values = map[string]string{"a_from": in.AFrom, "a_to": in.ATo, "b_from": in.BFrom, "b_to": in.BTo, "a_project": in.AProject, "b_project": in.BProject}
	}
	d.Preset = in.Preset
	out, err := s.Reg.Call(r.Context(), s.principal(r), "studio.compare", in)
	switch {
	case err == nil:
		o := out.(studio.CompareOut)
		d.Has = true
		head := func(from, to string) string { // a single day is written as that day, not as "13 Mar to 13 Mar"
			if from == to {
				return t.Date(from)
			}
			return t.T("studio.ui.range", "from", from, "to", to)
		}
		d.HeadA, d.HeadB = head(o.A.From, o.A.To), head(o.B.From, o.B.To)
		d.Compared = o.Compared
		equal := o.A.Days == o.B.Days
		for _, n := range o.Notes {
			d.Notes = append(d.Notes, t.Msg(n))
			if n.Key == "studio.compare.so_far" {
				// the heads say what the sides are: the period so far, and the same days of the one before
				d.HeadA, d.HeadB = t.T("studio.ui.compare.head_so_far", "range", d.HeadA), t.T("studio.ui.compare.head_same", "range", d.HeadB)
			}
		}
		if !o.Compared {
			d.HeadA = t.T("studio.ui.compare.head_so_far", "range", d.HeadA)
		}
		// a difference is worked out between two periods of the same length, and only then: one day against seven says nothing
		signed := func(n int) string {
			switch {
			case !equal:
				return "\u2014"
			case n > 0:
				return "+" + stNum(n)
			}
			return stNum(n)
		}
		d.Rows = []stCompareRow{
			{t.T("studio.ui.compare.words"), stNum(o.A.Words), stNum(o.B.Words), signed(o.Words.Change)},
			{t.T("studio.ui.compare.net"), stNum(o.A.Net), stNum(o.B.Net), signed(o.Net.Change)},
			{t.T("studio.ui.compare.minutes"), stNum(o.A.Minutes), stNum(o.B.Minutes), signed(o.Minutes.Change)},
			{t.T("studio.ui.compare.sessions"), stNum(o.A.Sessions), stNum(o.B.Sessions), signed(o.A.Sessions - o.B.Sessions)},
			{t.T("studio.ui.compare.days"), stNum(o.A.WritingDays), stNum(o.B.WritingDays), signed(o.Days)},
		}
	case stRefused(err):
		d.Error, d.Field, d.Form = t.T("studio.ui.err.compare"), "a_from", "compare"
	default:
		s.fail(w, r, err)
		return
	}
	s.render(w, r, http.StatusOK, "studio-data", s.page(r, t.T("studio.ui.data.title"), d))
}

// ---- the private progress card ---------------------------------------------------------------------------------------

type stCardRow struct {
	ID, Label, Made, Expires, Views, Last, State string
	Live, Projects                               bool
}

type studioCardData struct {
	stBase
	Cards []stCardRow
	Live  int
}

func (s *Server) studioCardPage(w http.ResponseWriter, r *http.Request) {
	if !s.stAuthed(w, r) {
		return
	}
	d := studioCardData{stBase: s.stBaseFor(r, "card")}
	t := d.W
	out, err := s.Reg.Call(r.Context(), s.principal(r), "studio.card.list", studio.CardListIn{})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc := s.userZone(r)
	for _, c := range out.(studio.CardListOut).Items {
		row := stCardRow{ID: c.ID, Label: c.Label, Made: t.When(c.CreatedAt, loc), Expires: t.When(c.ExpiresAt, loc), Views: t.N("studio.ui.card.views", c.Views), Projects: c.ShowProjects}
		if c.LastViewedAt != "" {
			row.Last = t.T("studio.ui.card.last", "date", t.When(c.LastViewedAt, loc))
		}
		switch {
		case c.Revoked:
			row.State = t.T("studio.ui.card.s_revoked")
		case c.Expired:
			row.State = t.T("studio.ui.card.s_expired")
		default:
			row.State, row.Live = t.T("studio.ui.card.s_live"), true
			d.Live++
		}
		d.Cards = append(d.Cards, row)
	}
	s.render(w, r, http.StatusOK, "studio-card", s.page(r, t.T("studio.ui.card.title"), d))
}

func (s *Server) uiStudioCardCreate(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	vals := stPosted(r, "label", "expires_days", "show_projects")
	back := func(f stFlash) {
		f.Page = "card"
		s.stStash(r, f)
		http.Redirect(w, r, "/studio/card", http.StatusSeeOther)
	}
	in := studio.CardIn{Label: strings.TrimSpace(r.PostFormValue("label")), ShowProjects: r.PostFormValue("show_projects") == "on"}
	if v := strings.TrimSpace(r.PostFormValue("expires_days")); v != "" {
		n, ok := stInt(v)
		if !ok || n == 0 {
			back(stFlash{Err: t.T("studio.ui.err.f_expires_days"), Field: "expires_days", Values: vals})
			return
		}
		in.ExpiresDays = n
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "studio.card.create", in)
	if err != nil {
		if !stRefused(err) {
			s.fail(w, r, err)
			return
		}
		msg, field, _ := s.stErr(t, err)
		back(stFlash{Err: msg, Field: field, Values: vals})
		return
	}
	c := out.(studio.CardItem).Item
	// Post/Redirect/Get: the address is kept on the server for the next page of this session and handed over once, so a
	// reload of that page asks for nothing and makes no second card.
	back(stFlash{Once: c.Path, Label: c.Label})
}

func (s *Server) uiStudioCardRevoke(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	f := stFlash{Page: "card", Msg: t.T("studio.ui.card.revoked")}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "studio.card.revoke", studio.CardRevokeIn{ID: r.PathValue("id")}); err != nil {
		if !stRefused(err) {
			s.fail(w, r, err)
			return
		}
		f.Msg, f.Err = "", t.T("studio.ui.err.gone")
	}
	s.stStash(r, f)
	http.Redirect(w, r, "/studio/card", http.StatusSeeOther)
}

// ---- settings: the progress email and the nudges ---------------------------------------------------------------------

// stSettings is the Writing Studio's part of the Settings page.
type stSettings struct {
	Progress     mail.ProgressOut
	NudgesOn     bool
	Flash, Error string
	Where        string // "progress" or "nudges"
	Zone         string
}

func (s *Server) stSettingsView(r *http.Request) *stSettings {
	ctx, p := r.Context(), s.principal(r)
	v := &stSettings{NudgesOn: true, Zone: s.userZone(r).String()}
	if o, err := s.Reg.Call(ctx, p, "mail.progress.get", mail.ProgressGetIn{}); err == nil {
		v.Progress = o.(mail.ProgressOut)
	}
	if o, err := s.Reg.Call(ctx, p, "prefs.get", account.EmptyIn{}); err == nil {
		v.NudgesOn = o.(account.PrefsOut).Items["studio.nudges"] != "false"
	}
	f := s.stTake(r, "settings")
	v.Flash, v.Error, v.Where = f.Msg, f.Err, f.Form
	return v
}

func (s *Server) uiSettingsProgress(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	back := func(f stFlash) {
		f.Page, f.Form = "settings", "progress"
		s.stStash(r, f)
		http.Redirect(w, r, "/settings", http.StatusSeeOther) // no fragment: the section focuses its own answer
	}
	on := r.PostFormValue("do") == "on"
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "mail.progress.set", mail.ProgressSetIn{Enabled: on, Frequency: r.PostFormValue("frequency")}); err != nil {
		e := ops.AsError(err)
		switch {
		case e.Code == ops.CodeInvalid && e.Field == "frequency":
			back(stFlash{Err: t.T("studio.ui.mail.needs_freq")})
		case e.Code == ops.CodeConflict:
			back(stFlash{Err: t.T("studio.ui.err.mail_off")})
		case e.Code == ops.CodeInvalid:
			back(stFlash{Err: e.Message})
		default:
			s.fail(w, r, err)
		}
		return
	}
	back(stFlash{Msg: t.T(map[bool]string{true: "studio.ui.mail.saved_on", false: "studio.ui.mail.saved_off"}[on])})
}

func (s *Server) uiSettingsNudges(w http.ResponseWriter, r *http.Request) {
	t := stT{s.page(r, "", nil).Lang}
	on := r.PostFormValue("nudges") == "true"
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "prefs.set", account.SetPrefsIn{Items: map[string]string{"studio.nudges": strconv.FormatBool(on)}}); err != nil {
		s.fail(w, r, err)
		return
	}
	s.stStash(r, stFlash{Page: "settings", Form: "nudges", Msg: t.T(map[bool]string{true: "studio.ui.nudges.saved_on", false: "studio.ui.nudges.saved_off"}[on])})
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ---- the writing view's sprint line -----------------------------------------------------------------------------------

// writeSprint is the sprint line of the writing view.
type writeSprint struct {
	Running bool
	Text    string // "Sprint" when idle, else the minutes left
	JSON    string // the running sprint as studio.sprint.current gives it
	// Sizers are the longest words the line can say, laid unseen under it: the line's place is as wide as the widest, so a
	// change of its words moves nothing beside it.
	Sizers []string
}

// stSprintSizers are the longest words the sprint line can say, one of each kind.
func stSprintSizers(t stT) []string {
	return []string{t.T("studio.sprint.over"), t.N("studio.ui.sprint.left", 240), t.N("studio.ui.sprint.open_for", 240)}
}

func (s *Server) writeSprintOf(r *http.Request, pid string) *writeSprint {
	t := stT{s.page(r, "", nil).Lang}
	out := &writeSprint{Text: t.T("studio.ui.sprint.idle"), Sizers: stSprintSizers(t)}
	co, err := s.Reg.Call(r.Context(), s.principal(r), "studio.sprint.current", studio.SprintEndIn{})
	if err != nil {
		return out
	}
	sp := co.(studio.SprintCurrentOut).Sprint
	if sp == nil || sp.State != "running" || sp.ProjectID != pid {
		return out
	}
	if b, err := json.Marshal(sp); err == nil {
		out.Running, out.JSON = true, string(b)
		out.Text = stSprintOf(t, sp, nil).Line
	}
	return out
}

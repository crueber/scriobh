// Package studio is the writing studio: goals with a history, the dashboard's
// numbers, writing sessions and sprints, gentle nudges, the analytics export,
// the private progress card and the figures of the progress email.
//
// Everything here is computed locally from the statistics the save loop already
// keeps (daily_stats and writing_sessions, internal/domain/goals) and from a
// few small tables of its own. Nothing is sent anywhere, and nothing is stored
// that cannot be rebuilt from those two tables except the goals, the sprints
// and the record of what was shown.
//
// Goals count words added. Net words are carried beside them and are never used
// to fail a day. A day is a goal day when a daily goal that was in force that
// day was met; a day with no writing is simply empty, and nothing in this
// package calls a day missed.
//
// This file is the arithmetic: pure functions of days, goals and dates, with
// no database and no clock (the callers pass "today"), so that every number can
// be checked against a hand count (calc_test.go).
package studio

import (
	"math"
	"sort"
	"time"
)

const dayLayout = "2006-01-02"

// Day is one local day of a person's writing, summed over the projects that are
// visible to them.
type Day struct {
	Words    int // words added
	Net      int // new words minus old words; negative on a day of cutting
	Seconds  int // active writing time
	Sessions int
	Chapters int // chapters finished for the first time
}

// Days maps a local date (YYYY-MM-DD) to its Day. A date with no entry is an
// empty day.
type Days map[string]Day

// ---- dates ----------------------------------------------------------------------------------

func parseDay(s string) (time.Time, bool) {
	t, err := time.Parse(dayLayout, s)
	if err != nil || t.Format(dayLayout) != s {
		return time.Time{}, false
	}
	return t, true
}

// addDays steps a calendar date. Dates are stepped as calendar dates (midnight UTC
// plus a number of days) and never as 24-hour intervals, so a 23- or 25-hour day on
// a daylight-saving change cannot skip or repeat a date.
func addDays(day string, n int) string {
	t, ok := parseDay(day)
	if !ok {
		return day
	}
	return t.AddDate(0, 0, n).Format(dayLayout)
}

// daysBetween is b minus a in whole calendar days.
func daysBetween(a, b string) int {
	ta, ok1 := parseDay(a)
	tb, ok2 := parseDay(b)
	if !ok1 || !ok2 {
		return 0
	}
	return int(math.Round(tb.Sub(ta).Hours() / 24))
}

// weekday is 0 for Monday to 6 for Sunday.
func weekday(day string) int {
	t, ok := parseDay(day)
	if !ok {
		return 0
	}
	return (int(t.Weekday()) + 6) % 7
}

// weekStart is the Monday on or before day.
func weekStart(day string) string { return addDays(day, -weekday(day)) }

func monthStart(day string) string {
	t, ok := parseDay(day)
	if !ok {
		return day
	}
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Format(dayLayout)
}

func monthEnd(day string) string {
	t, ok := parseDay(day)
	if !ok {
		return day
	}
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, time.UTC).Format(dayLayout)
}

// periodOf is the local dates a day, week (Monday to Sunday) or month containing day covers.
func periodOf(period, day string) (from, to string) {
	switch period {
	case "week":
		return weekStart(day), addDays(weekStart(day), 6)
	case "month":
		return monthStart(day), monthEnd(day)
	}
	return day, day
}

func minutesOf(seconds int) int { return (seconds + 59) / 60 }

// sum adds the days from..to inclusive. A reversed range is empty.
func (d Days) sum(from, to string) Day {
	var out Day
	if from > to {
		return out
	}
	for t := from; t <= to; t = addDays(t, 1) {
		x, ok := d[t]
		if !ok {
			continue
		}
		out.Words += x.Words
		out.Net += x.Net
		out.Seconds += x.Seconds
		out.Sessions += x.Sessions
		out.Chapters += x.Chapters
	}
	return out
}

// first is the earliest date that has an entry, "" for none.
func (d Days) first() string {
	f := ""
	for k := range d {
		if f == "" || k < f {
			f = k
		}
	}
	return f
}

// ---- goals ------------------------------------------------------------------------------------

// Version is one target a goal has had. From is the local date it took effect;
// ProgressAt is the person's progress in that period at that moment.
type Version struct {
	N          int
	Target     int
	Deadline   string // a total goal's deadline, "" for none
	From       string
	ProgressAt int
}

// Goal is a studio goal with every version it has had, oldest first.
type Goal struct {
	ID        string
	ProjectID string
	Period    string // day, week, month, total
	Measure   string // words, sessions, chapters, minutes
	Start     string
	// CompletedOn is the date a total goal was first seen reached; once set it stays (see snapshot.evalGoal).
	CompletedOn string
	EndedOn     string // the last date it applied, once archived (see settleEnd: the stored value is the date it was archived)
	Versions    []Version
	// FinalOnly judges a period by the version in force at its end alone. A reader whose figures leave out any project the
	// person has written in (locked, in the trash, not approved for a key, left, or protected where there is no session) gets it
	// for a goal over every project: a version's ProgressAt was written from a sum that may have held
	// the words of a project this reader may not count, so it is not used to judge for them.
	FinalOnly bool
}

// current is the version now in force.
func (g Goal) current() Version {
	if len(g.Versions) == 0 {
		return Version{}
	}
	return g.Versions[len(g.Versions)-1]
}

// activeOn says whether the goal applied on a local date.
func (g Goal) activeOn(day string) bool {
	return day >= g.Start && (g.EndedOn == "" || day <= g.EndedOn)
}

// Judged is a period of a goal and how it came out.
type Judged struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Target int    `json:"target" desc:"The target the period is judged against: the version in force at its end."`
	Value  int    `json:"value" desc:"What was measured in the period."`
	Met    bool   `json:"met"`
}

// judge judges the period from..to, whose full measure is value, against the
// versions that were in force in it.
//
// A period is met if, at some moment in it, the total reached the target then in
// force. That is the rule a day already follows ("raising the goal later never
// takes a finished day away"), applied to every period: a version that was
// replaced inside the period is judged on the total at the moment it was
// replaced (the next version's ProgressAt), the version in force at the end on the
// period's total. So a goal changed in the middle of a week is judged fairly
// whichever way it was changed.
func (g Goal) judge(from, to string, value int) Judged {
	j := Judged{From: from, To: to, Value: value}
	vs := g.Versions
	for i, v := range vs {
		if v.From > to {
			continue
		}
		if i+1 < len(vs) && vs[i+1].From < from {
			continue // replaced before the period began
		}
		replaced := i+1 < len(vs) && vs[i+1].From <= to
		if replaced && g.FinalOnly {
			continue
		}
		j.Target = v.Target
		cum := value
		if replaced {
			cum = vs[i+1].ProgressAt
		}
		if cum >= v.Target {
			j.Met = true
		}
	}
	return j
}

// ---- intensity, calendar ------------------------------------------------------------------------

// Levels is the number of intensity steps of the activity calendar: 0 is an empty
// day, 1 to 4 are the writing days from lightest to heaviest.
const Levels = 5

// Thresholds are the intensity cut points of a person's writing days.
type Thresholds struct {
	Cuts    [3]int `json:"cuts" desc:"The words of the 25th, 50th and 75th percentile of the person's own writing days (nearest rank): a day above the third cut is step 4, above the second step 3, above the first step 2, otherwise step 1."`
	Uniform bool   `json:"uniform" desc:"Every writing day has the same words, so every writing day is step 4."`
	Days    int    `json:"days" desc:"How many writing days the cuts were read from."`
}

// thresholdsOf reads the cut points from every non-zero day of the person's
// own record (all time, not the window shown), so a day keeps its colour as the
// window moves and changes only when the person's own pattern does.
func thresholdsOf(days Days) Thresholds {
	var v []int
	for _, d := range days {
		if d.Words > 0 {
			v = append(v, d.Words)
		}
	}
	t := Thresholds{Days: len(v)}
	if len(v) == 0 {
		return t
	}
	sort.Ints(v)
	for k := 1; k <= 3; k++ {
		rank := (k*len(v) + 3) / 4 // ceil(k*n/4), nearest rank
		t.Cuts[k-1] = v[rank-1]
	}
	t.Uniform = v[0] == v[len(v)-1]
	return t
}

// level is the step of a day's words.
func (t Thresholds) level(words int) int {
	switch {
	case words <= 0:
		return 0
	case t.Uniform || words > t.Cuts[2]:
		return 4
	case words > t.Cuts[1]:
		return 3
	case words > t.Cuts[0]:
		return 2
	}
	return 1
}

// Cell is one day of the calendar.
type Cell struct {
	Day    string `json:"day"`
	Words  int    `json:"words"`
	Level  int    `json:"level" desc:"0 for an empty day, 1 to 4 by the thresholds."`
	Hit    bool   `json:"hit,omitempty" desc:"A daily goal in force that day was met."`
	Future bool   `json:"future,omitempty" desc:"After today: not a day yet."`
}

// Week is one column of the calendar.
type Week struct {
	Start string `json:"start" desc:"The Monday."`
	Month int    `json:"month,omitempty" desc:"1 to 12 on the first column of a month (the column holding its first day), else absent: the month label."`
	Cells []Cell `json:"cells" desc:"Seven days, Monday first."`
}

// Calendar is the activity calendar.
type Calendar struct {
	From        string     `json:"from"`
	To          string     `json:"to"`
	Weeks       []Week     `json:"weeks" desc:"Columns of seven days, Monday first, oldest first; the last column holds today."`
	LabelRows   []int      `json:"label_rows" desc:"The rows that carry a weekday label: Monday, Wednesday and Friday (0, 2, 4). Weekday names are the keys studio.weekday.0 to studio.weekday.6."`
	Levels      int        `json:"levels"`
	Thresholds  Thresholds `json:"thresholds"`
	WritingDays int        `json:"writing_days" desc:"Days in the window with words added."`
	Words       int        `json:"words" desc:"Words added in the window."`
	AllTimeDays int        `json:"all_time_writing_days"`
}

// calendar makes the columns of the last n weeks ending with the week of today.
func calendar(days Days, hits map[string]bool, today string, n int, th Thresholds) Calendar {
	end := addDays(weekStart(today), 6)
	start := addDays(weekStart(today), -7*(n-1))
	c := Calendar{From: start, To: end, LabelRows: []int{0, 2, 4}, Levels: Levels, Thresholds: th, AllTimeDays: th.Days}
	prevMonth := 0
	for w := 0; w < n; w++ {
		ws := addDays(start, 7*w)
		col := Week{Start: ws, Cells: make([]Cell, 7)}
		for i := 0; i < 7; i++ {
			d := addDays(ws, i)
			cell := Cell{Day: d}
			if d > today {
				cell.Future = true
			} else {
				cell.Words = days[d].Words
				cell.Level = th.level(cell.Words)
				cell.Hit = hits[d]
				if cell.Words > 0 {
					c.WritingDays++
					c.Words += cell.Words
				}
			}
			col.Cells[i] = cell
		}
		// The month label goes on the column that holds the first of the month.
		for i := 0; i < 7; i++ {
			t, _ := parseDay(col.Cells[i].Day)
			if t.Day() == 1 && int(t.Month()) != prevMonth {
				col.Month = int(t.Month())
				prevMonth = col.Month
				break
			}
		}
		if w == 0 && col.Month == 0 {
			t, _ := parseDay(ws)
			col.Month = int(t.Month())
			prevMonth = col.Month
		}
		c.Weeks = append(c.Weeks, col)
	}
	return c
}

// ---- series -------------------------------------------------------------------------------------

// Point is one bucket of the words-written series.
type Point struct {
	Start   string `json:"start" desc:"The first date of the bucket."`
	Words   int    `json:"words"`
	Net     int    `json:"net"`
	Minutes int    `json:"minutes"`
}

// bucketFor says how a range is cut for a chart: days up to 92 days, weeks (Monday
// first) up to 800 days, months beyond.
func bucketFor(from, to string) string {
	span := daysBetween(from, to) + 1
	switch {
	case span <= 92:
		return "day"
	case span <= 800:
		return "week"
	}
	return "month"
}

// series sums from..to into buckets. Every bucket of the range is present, with zeros
// for the empty ones, so a chart has an even axis; nothing marks an empty bucket as anything.
func series(days Days, from, to string) (string, []Point) {
	bucket := bucketFor(from, to)
	var pts []Point
	for t := from; t <= to; {
		var lo, hi string
		switch bucket {
		case "week":
			lo, hi = periodOf("week", t)
		case "month":
			lo, hi = periodOf("month", t)
		default:
			lo, hi = t, t
		}
		s := days.sum(max(lo, from), min(hi, to))
		pts = append(pts, Point{Start: max(lo, from), Words: s.Words, Net: s.Net, Minutes: minutesOf(s.Seconds)})
		t = addDays(hi, 1)
	}
	return bucket, pts
}

// ---- patterns ----------------------------------------------------------------------------------

// WeekdayStat is one weekday over a range.
type WeekdayStat struct {
	Weekday          int `json:"weekday" desc:"0 for Monday to 6 for Sunday."`
	Words            int `json:"words"`
	WritingDays      int `json:"writing_days" desc:"Days of this weekday in the range with words added."`
	AvgPerWritingDay int `json:"avg_per_writing_day"`
}

// weekdays tallies the range by weekday and names the busiest. best is -1 when
// nothing was written.
func weekdays(days Days, from, to string) (out [7]WeekdayStat, best int) {
	for i := range out {
		out[i].Weekday = i
	}
	for t := from; t <= to; t = addDays(t, 1) {
		d := days[t]
		if d.Words <= 0 {
			continue
		}
		w := weekday(t)
		out[w].Words += d.Words
		out[w].WritingDays++
	}
	best = -1
	for i := range out {
		if out[i].WritingDays > 0 {
			out[i].AvgPerWritingDay = int(math.Round(float64(out[i].Words) / float64(out[i].WritingDays)))
		}
		if out[i].Words > 0 && (best < 0 || out[i].Words > out[best].Words) {
			best = i
		}
	}
	return out, best
}

// HourStat is the words written in one local hour of the day.
type HourStat struct {
	Hour  int `json:"hour" desc:"0 to 23, in the zone the session was written in."`
	Words int `json:"words"`
}

// PartStat is a quarter of the day.
type PartStat struct {
	Part  string `json:"part" enum:"night,morning,afternoon,evening" desc:"night 00 to 05, morning 06 to 11, afternoon 12 to 17, evening 18 to 23."`
	Words int    `json:"words"`
}

// SessionRow is one writing session as the patterns read it: when it began, where, and what it added.
type SessionRow struct {
	Start time.Time
	Zone  string
	Words int
}

// hours tallies sessions by the local hour they began in. A session's words are
// attributed to the hour it began: a long session is not split, and this is
// stated wherever the figure is shown. best is -1 when nothing was written.
func hours(rows []SessionRow, loc func(string) *time.Location) (out [24]HourStat, parts [4]PartStat, best int) {
	for i := range out {
		out[i].Hour = i
	}
	for _, r := range rows {
		if r.Words <= 0 {
			continue
		}
		out[r.Start.In(loc(r.Zone)).Hour()].Words += r.Words
	}
	names := [4]string{"night", "morning", "afternoon", "evening"}
	for i := range parts {
		parts[i].Part = names[i]
	}
	best = -1
	for h := range out {
		parts[h/6].Words += out[h].Words
		if out[h].Words > 0 && (best < 0 || out[h].Words > out[best].Words) {
			best = h
		}
	}
	return out, parts, best
}

// ---- deltas, periods -------------------------------------------------------------------------

// Delta compares a figure with the same figure of the period before.
type Delta struct {
	Change  int  `json:"change" desc:"This period minus the previous."`
	Percent *int `json:"percent" desc:"The change as a percentage of the previous, rounded; null when the previous period had none."`
}

func deltaOf(cur, prev int) Delta {
	d := Delta{Change: cur - prev}
	if prev > 0 {
		p := int(math.Round(float64(cur-prev) * 100 / float64(prev)))
		d.Percent = &p
	}
	return d
}

// Figures are the totals of a range.
type Figures struct {
	Words       int `json:"words" desc:"Words added: what goals count."`
	Net         int `json:"net" desc:"New words minus old words, shown beside the words added and never against them."`
	Minutes     int `json:"minutes"`
	Sessions    int `json:"sessions"`
	WritingDays int `json:"writing_days"`
}

func figuresOf(days Days, from, to string) Figures {
	var f Figures
	var secs int
	if from > to {
		return f
	}
	for t := from; t <= to; t = addDays(t, 1) {
		d, ok := days[t]
		if !ok {
			continue
		}
		f.Words += d.Words
		f.Net += d.Net
		secs += d.Seconds
		f.Sessions += d.Sessions
		if d.Words > 0 {
			f.WritingDays++
		}
	}
	f.Minutes = minutesOf(secs)
	return f
}

// Card is a period card: the last N days, the N days before them, and a sparkline.
type Card struct {
	Key       string  `json:"key" enum:"today,7d,30d,365d"`
	Days      int     `json:"days"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	Figures           // the period
	Previous  Figures `json:"previous" desc:"The same figures for the N days before."`
	Delta     Delta   `json:"delta" desc:"The change in words added against the previous period."`
	DeltaMins Delta   `json:"delta_minutes"`
	Spark     []int   `json:"sparkline" desc:"Words added: the last 7 days for the today card, each day for 7d and 30d, each of 52 weeks for 365d."`
}

// cards makes the four period cards.
func cards(days Days, today string) []Card {
	specs := []struct {
		key string
		n   int
	}{{"today", 1}, {"7d", 7}, {"30d", 30}, {"365d", 365}}
	out := make([]Card, 0, 4)
	for _, s := range specs {
		from := addDays(today, -(s.n - 1))
		pFrom, pTo := addDays(today, -(2*s.n-1)), addDays(today, -s.n)
		c := Card{Key: s.key, Days: s.n, From: from, To: today,
			Figures: figuresOf(days, from, today), Previous: figuresOf(days, pFrom, pTo)}
		c.Delta = deltaOf(c.Words, c.Previous.Words)
		c.DeltaMins = deltaOf(c.Minutes, c.Previous.Minutes)
		switch s.key {
		case "today":
			for i := 6; i >= 0; i-- {
				c.Spark = append(c.Spark, days[addDays(today, -i)].Words)
			}
		case "365d":
			// 52 weeks ending today: the last bucket is the 7 days up to today.
			for w := 51; w >= 0; w-- {
				hi := addDays(today, -7*w)
				c.Spark = append(c.Spark, days.sum(addDays(hi, -6), hi).Words)
			}
		default:
			for i := s.n - 1; i >= 0; i-- {
				c.Spark = append(c.Spark, days[addDays(today, -i)].Words)
			}
		}
		out = append(out, c)
	}
	return out
}

// ---- forecast ---------------------------------------------------------------------------------

// PaceWindow and MinWritingDays are how much recent writing a forecast reads, the same as the
// project's own forecast (goals.PaceWindow, goals.MinWritingDays).
const (
	PaceWindow     = 28
	MinWritingDays = 5
	maxProjectDays = 3650
)

// Basis says what a forecast was made from.
type Basis struct {
	WindowDays  int    `json:"window_days"`
	WritingDays int    `json:"writing_days" desc:"Days in the window with words added."`
	PerDay      int    `json:"per_day" desc:"The average over the whole window, writing days or not, rounded."`
	Measure     string `json:"measure" enum:"net,added" desc:"net: for a total goal in words, the manuscript's growth, which is what a word-count target is reached by (a cut slows the date; it fails nothing). added: any other measure, as it is counted."`
}

// Forecast is an estimate of when a total goal is reached, and what a deadline asks.
type Forecast struct {
	Estimate       bool   `json:"estimate" desc:"Always true: this is a projection from recent writing, not a promise."`
	Unit           string `json:"unit" enum:"words,sessions,minutes,chapters" desc:"What the goal counts. For words the pace is in net words (see basis.measure)."`
	Remaining      int    `json:"remaining"`
	Deadline       string `json:"deadline,omitempty"`
	DaysLeft       int    `json:"days_left" desc:"From today to the deadline, counting today; 0 with no deadline or once it has passed."`
	RequiredPerDay int    `json:"required_per_day" desc:"What each remaining day must add to reach the target by the deadline, rounded up."`
	Basis          Basis  `json:"basis"`
	ProjectedDate  string `json:"projected_date,omitempty" desc:"When the target would be reached at the recent pace. Absent when the window holds too few writing days or no growth: absent is the honest answer."`
	MarginDays     *int   `json:"margin_days,omitempty" desc:"With a deadline and a projected date: days between the projected date and the deadline, positive when the projection is earlier, negative when later. Information, not a verdict, and not said in the notes when it is negative: the pace a deadline asks and the date the recent pace reaches are both there to read."`
	Notes          []Msg  `json:"notes" desc:"What a page may say, as message keys with parameters: studio.forecast.required (and .required_words), .projected, .none, .ahead, .basis_words (net words a day) and .basis. Always stated as an estimate with its basis."`
}

// forecast projects when remaining units are reached. total is the sum over the
// window of the measure the pace is read in (days of the window ending today; net words for
// a total in words), writingDays how many of those days had words added. unit is what the
// goal counts, basis "net" or "added".
func forecast(remaining int, deadline, today string, total, writingDays int, unit, basis string) (f Forecast) {
	f = Forecast{Estimate: true, Unit: unit, Remaining: remaining, Deadline: deadline,
		Basis: Basis{WindowDays: PaceWindow, WritingDays: writingDays, Measure: basis}}
	f.Basis.PerDay = int(math.Round(float64(total) / PaceWindow))
	f.Notes = []Msg{}
	if remaining <= 0 {
		return f
	}
	defer func() { f.Notes = f.notes() }()
	if deadline != "" && deadline >= today {
		f.DaysLeft = daysBetween(today, deadline) + 1
		f.RequiredPerDay = (remaining + f.DaysLeft - 1) / f.DaysLeft
	}
	if writingDays >= MinWritingDays && total > 0 {
		// days = ceil(remaining / (total / window)), in integers so that an exact answer (300 to go at 300 in 28 days is
		// exactly 28 days) is not tipped over by a float.
		toGo := (remaining*PaceWindow + total - 1) / total
		if toGo <= maxProjectDays {
			f.ProjectedDate = addDays(today, toGo)
			if f.DaysLeft > 0 {
				m := daysBetween(f.ProjectedDate, deadline)
				f.MarginDays = &m
			}
		}
	}
	return f
}

// notes says the forecast in message keys.
func (f Forecast) notes() []Msg {
	out := []Msg{}
	if f.RequiredPerDay > 0 {
		key := "studio.forecast.required"
		if f.Unit == "words" && f.Basis.Measure == "net" {
			key = "studio.forecast.required_words"
		}
		out = append(out, Msg{Key: key, Params: map[string]any{"n": f.RequiredPerDay, "measure": f.Unit, "date": f.Deadline}})
	}
	if f.ProjectedDate != "" {
		out = append(out, Msg{Key: "studio.forecast.projected", Params: map[string]any{"date": f.ProjectedDate}})
		// a projection later than the deadline is not counted out in days: the pace a deadline asks and the date the pace reaches
		// are both said, side by side, and the reader sets one against the other
		if f.MarginDays != nil && *f.MarginDays > 0 {
			out = append(out, Msg{Key: "studio.forecast.ahead", Params: map[string]any{"n": *f.MarginDays}})
		}
	} else {
		out = append(out, Msg{Key: "studio.forecast.none"})
	}
	if f.Basis.WritingDays == 0 && f.Basis.PerDay <= 0 {
		return out // nothing written in the window: no sentence of noughts under "no date can be estimated yet"
	}
	basis := "studio.forecast.basis"
	if f.Unit == "words" && f.Basis.Measure == "net" {
		basis = "studio.forecast.basis_words"
	}
	out = append(out, Msg{Key: basis, Params: map[string]any{"days": f.Basis.WindowDays, "writing": f.Basis.WritingDays, "n": f.Basis.PerDay, "measure": f.Unit}})
	return out
}

// ---- trend ------------------------------------------------------------------------------------

// Trend compares the last three weeks with the three before them.
type Trend struct {
	RecentWords   int      `json:"recent_words" desc:"Words added in the last 21 days."`
	PreviousWords int      `json:"previous_words" desc:"Words added in the 21 days before."`
	ChangePercent *int     `json:"change_percent" desc:"Null when the previous three weeks had none."`
	Direction     string   `json:"direction" enum:"none,new,up,steady,down" desc:"none: nothing in either period; new: nothing before, something now; up and down: a change of 10% or more; steady otherwise."`
	MessageKey    string   `json:"message_key" desc:"studio.trend.<direction>. A note, never a warning: no direction is an error."`
	Resources     []Action `json:"resources,omitempty" desc:"Only when the direction is down: small things that help, offered, never required."`
}

// Action is something the page may offer: a message key and what pressing it does.
type Action struct {
	Key    string         `json:"key"`
	Action string         `json:"action" enum:"sprint,goal,calendar" desc:"sprint: start a short sprint; goal: adjust a goal; calendar: see the activity calendar."`
	Params map[string]any `json:"params,omitempty"`
}

func trendOf(days Days, today string) Trend {
	cur := days.sum(addDays(today, -20), today).Words
	prev := days.sum(addDays(today, -41), addDays(today, -21)).Words
	t := Trend{RecentWords: cur, PreviousWords: prev}
	switch {
	case cur == 0 && prev == 0:
		t.Direction = "none"
	case prev == 0:
		t.Direction = "new"
	default:
		p := int(math.Round(float64(cur-prev) * 100 / float64(prev)))
		t.ChangePercent = &p
		switch {
		case p >= 10:
			t.Direction = "up"
		case p <= -10:
			t.Direction = "down"
		default:
			t.Direction = "steady"
		}
	}
	t.MessageKey = "studio.trend." + t.Direction
	if t.Direction == "down" {
		t.Resources = []Action{
			{Key: "studio.resource.sprint", Action: "sprint", Params: map[string]any{"minutes": 15}},
			{Key: "studio.resource.goal", Action: "goal"},
			{Key: "studio.resource.calendar", Action: "calendar"},
		}
	}
	return t
}

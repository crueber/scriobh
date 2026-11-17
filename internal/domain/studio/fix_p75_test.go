package studio_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/studio"
)

// Fix round for the Writing Studio screens: a card says what a person did and sets nothing that is not over, or not the same
// length, against anything.

// Compare sets the finished days of this week against the same days of last week: today is not in it (it is on the Today card),
// and a Monday, with no finished day, is shown so far with nothing to compare, never as a row of losses.
func TestCompareThisWeekSetsFinishedDaysAgainstTheSameDays(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-10T09:00:00Z") // Tuesday of the week before
	w.save(u, d, lorem(400, "a"))
	// Monday: nothing is finished yet
	w.at("2026-03-16T09:00:00Z")
	o := w.call(u, "studio.compare", studio.CompareIn{Preset: "this_week"}).(studio.CompareOut)
	if o.Compared || o.A.From != "2026-03-16" || o.A.To != "2026-03-16" || o.B.From != "" || o.B.Days != 0 || o.B.Words != 0 || o.Words.Change != 0 {
		t.Errorf("a Monday: %+v", o)
	}
	if len(o.Notes) != 1 || o.Notes[0].Key != "studio.compare.starts_tomorrow" {
		t.Errorf("a Monday's note: %+v", o.Notes)
	}
	for _, c := range []struct {
		now        string
		from, to   string
		bFrom, bTo string
		days       int
		bWords     int
	}{
		{"2026-03-17T09:00:00Z", "2026-03-16", "2026-03-16", "2026-03-09", "2026-03-09", 1, 0},
		{"2026-03-18T09:00:00Z", "2026-03-16", "2026-03-17", "2026-03-09", "2026-03-10", 2, 400},
		{"2026-03-22T09:00:00Z", "2026-03-16", "2026-03-21", "2026-03-09", "2026-03-14", 6, 400},
	} {
		w.at(c.now)
		o := w.call(u, "studio.compare", studio.CompareIn{Preset: "this_week"}).(studio.CompareOut)
		if !o.Compared || o.A.From != c.from || o.A.To != c.to || o.B.From != c.bFrom || o.B.To != c.bTo || o.A.Days != c.days || o.B.Days != c.days || o.B.Words != c.bWords {
			t.Errorf("%s: A %s..%s (%d days), B %s..%s (%d days, %d words)", c.now, o.A.From, o.A.To, o.A.Days, o.B.From, o.B.To, o.B.Days, o.B.Words)
		}
		if len(o.Notes) != 1 || o.Notes[0].Key != "studio.compare.so_far" || o.Notes[0].Params["days"] != c.days {
			t.Errorf("%s: the page is not told the sides are the first %d finished days: %+v", c.now, c.days, o.Notes)
		}
	}
}

// The same for the month: finished days against the same days of last month (both stop at the shorter month's last day), and the
// 1st, with no finished day, has nothing to compare.
func TestCompareThisMonthSetsFinishedDaysAgainstTheSameDays(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	w.at("2026-03-01T09:00:00Z")
	o := w.call(u, "studio.compare", studio.CompareIn{Preset: "this_month"}).(studio.CompareOut)
	if o.Compared || o.A.From != "2026-03-01" || o.A.To != "2026-03-01" || o.B.From != "" || len(o.Notes) != 1 || o.Notes[0].Key != "studio.compare.starts_tomorrow" {
		t.Errorf("the 1st: %+v", o)
	}
	w.at("2026-03-31T09:00:00Z")
	o = w.call(u, "studio.compare", studio.CompareIn{Preset: "this_month"}).(studio.CompareOut)
	if !o.Compared || o.A.From != "2026-03-01" || o.A.To != "2026-03-28" || o.B.From != "2026-02-01" || o.B.To != "2026-02-28" || o.A.Days != 28 || o.B.Days != 28 {
		t.Errorf("31 March: A %s..%s, B %s..%s", o.A.From, o.A.To, o.B.From, o.B.To)
	}
	w.at("2026-03-04T09:00:00Z")
	o = w.call(u, "studio.compare", studio.CompareIn{Preset: "this_month"}).(studio.CompareOut)
	if !o.Compared || o.A.From != "2026-03-01" || o.A.To != "2026-03-03" || o.B.From != "2026-02-01" || o.B.To != "2026-02-03" {
		t.Errorf("4 March: A %s..%s, B %s..%s", o.A.From, o.A.To, o.B.From, o.B.To)
	}
	// the 30 days are the 30 finished days before today and the 30 before them
	o = w.call(u, "studio.compare", studio.CompareIn{Preset: "last_30"}).(studio.CompareOut)
	if !o.Compared || o.A.From != "2026-02-02" || o.A.To != "2026-03-03" || o.B.From != "2026-01-03" || o.B.To != "2026-02-01" || o.A.Days != 30 || o.B.Days != 30 {
		t.Errorf("last_30: A %s..%s, B %s..%s", o.A.From, o.A.To, o.B.From, o.B.To)
	}
	// two dates the person chose, of different lengths, are said to be: no page sets one against the other without it
	o = w.call(u, "studio.compare", studio.CompareIn{AFrom: "2026-03-01", ATo: "2026-03-07", BFrom: "2026-02-01", BTo: "2026-02-03"}).(studio.CompareOut)
	if !o.Compared || len(o.Notes) != 1 || o.Notes[0].Key != "studio.compare.unequal" {
		t.Errorf("sides of 7 and 3 days: %+v", o.Notes)
	}
}

// A daily goal that belongs to one project is the card's own: its words are that project's, not the person's total, so the
// words, the target and the words to go speak of one goal.
func TestTodayWordsAreTheHeadlineGoalsOwn(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	a := w.project(u, "Alpha")
	b := w.project(u, "Beta")
	docB := w.doc(u, b)
	w.at("2026-03-02T09:00:00Z")
	w.call(u, "studio.goal.create", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 200, ProjectID: a})
	w.save(u, docB, lorem(500, "b"))
	m := dash(t, w, u)
	if m.Today.Words != 0 || m.Today.Target != 200 || m.Today.Remaining != 200 || m.Today.Hit || m.Today.ProjectID != a {
		t.Errorf("today: %+v: 500 words in another project meet nothing", m.Today)
	}
	if m.Streak.TodayValue != 0 || m.Streak.TodayTarget != 200 || m.Streak.TodayWords != 0 || m.Streak.Measure != "words" || m.Streak.TodayHit {
		t.Errorf("streak: %+v", m.Streak)
	}
	docA := w.doc(u, a)
	w.save(u, docA, lorem(120, "a"))
	m = dash(t, w, u)
	if m.Today.Words != 120 || m.Today.Remaining != 80 || m.Streak.TodayValue != 120 {
		t.Errorf("after 120 words in the goal's own project: today %+v, streak %+v", m.Today, m.Streak)
	}
	// the Today card, which is every project, still holds all of it
	for _, c := range m.Cards {
		if c.Key == "today" && c.Words != 620 {
			t.Errorf("the today card holds %d words, not 620", c.Words)
		}
	}
}

// A daily goal that counts sessions is said in sessions: the streak's own figure and message, never a words target of 0.
func TestASessionsOnlyDailyGoalIsNotWords(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	w.call(u, "studio.goal.create", studio.GoalCreateIn{Period: "day", Measure: "sessions", Target: 1})
	m := dash(t, w, u)
	if m.Today.HasGoal || m.Today.Target != 0 {
		t.Errorf("a sessions goal is not the words target: %+v", m.Today)
	}
	if m.Streak.Measure != "sessions" || m.Streak.TodayTarget != 1 || m.Streak.TodayValue != 0 || !m.Streak.HasGoal {
		t.Errorf("streak: %+v", m.Streak)
	}
	if m.Streak.Message.Key != "studio.streak.begin_goal" {
		t.Errorf("the message is %s", m.Streak.Message.Key)
	}
	w.save(u, d, lorem(10, "a"))
	m = dash(t, w, u)
	if m.Streak.TodayValue != 1 || !m.Streak.TodayHit || m.Streak.Current != 1 || m.Streak.Message.Key != "studio.streak.hit" {
		t.Errorf("after a session: %+v", m.Streak)
	}
	w.at("2026-03-03T09:00:00Z")
	m = dash(t, w, u)
	if m.Streak.Message.Key != "studio.streak.keep_goal" || m.Streak.Current != 1 {
		t.Errorf("the next morning: %+v", m.Streak)
	}
}

// The forecast of a total goal in words is read from the net words (the manuscript's growth, which a word-count target is reached
// by), says so, and never counts days after a deadline. A cut slows the date and fails nothing: with no growth there is no date.
func TestForecastIsReadFromNetWordsAndNeverCountsDaysAfter(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-01T09:00:00Z")
	g := w.call(u, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 6000, Deadline: "2026-03-20"}).(studio.GoalItem).Item
	for i := 0; i < 6; i++ {
		w.at("2026-03-0" + string(rune('1'+i)) + "T09:00:00Z")
		w.save(u, d, w.text(u, d)+lorem(100, string(rune('a'+i))))
	}
	w.at("2026-03-08T09:00:00Z")
	f := goalByID(t, w, u, g.ID).Forecast
	if f == nil || f.Unit != "words" || f.Basis.Measure != "net" || f.Basis.PerDay != 21 || f.ProjectedDate == "" {
		t.Fatalf("forecast %+v: 600 net words in 28 days is 21 a day", f)
	}
	if f.MarginDays == nil || *f.MarginDays >= 0 {
		t.Fatalf("the control: this projection is after the deadline: %+v", f.MarginDays)
	}
	keysOf := func(f *studio.Forecast) string {
		var keys []string
		for _, n := range f.Notes {
			keys = append(keys, n.Key)
		}
		return strings.Join(keys, " ")
	}
	k := keysOf(f)
	if strings.Contains(k, "behind") || !strings.Contains(k, "studio.forecast.required") || !strings.Contains(k, "studio.forecast.basis_words") {
		t.Errorf("notes %s", k)
	}
	// the manuscript is cut to nothing: no growth, so no date, said without a word of shortfall
	w.save(u, d, "")
	f = goalByID(t, w, u, g.ID).Forecast
	k = keysOf(f)
	if f.ProjectedDate != "" || f.MarginDays != nil || !strings.Contains(k, "studio.forecast.none") || strings.Contains(k, "behind") {
		t.Errorf("a manuscript that has not grown: %+v (%s)", f, k)
	}
}

// The card of a run that has lapsed leads with the past month; the Markdown export gives the Today row no loss.
func TestTheMarkdownExportSaysNoLossForToday(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	w.save(u, d, lorem(300, "a"))
	w.at("2026-03-03T09:00:00Z") // a new day, nothing yet: today against yesterday would be -300 (-100%)
	out := w.call(u, "studio.export", studio.ExportIn{Range: "30d", Format: "md"}).(studio.ExportOut)
	body := string(out.Body)
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "| Today ") && (strings.Contains(line, "-") || strings.Contains(line, "%")) {
			t.Errorf("the Today row: %s", line)
		}
	}
	if !strings.Contains(body, "| Today |") {
		t.Errorf("no Today row in %s", body)
	}
}

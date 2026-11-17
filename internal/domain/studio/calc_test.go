package studio

import (
	"reflect"
	"testing"
	"time"
)

// Every expected number in this file was worked out by hand from the inputs written beside it.

func TestCalendarDates(t *testing.T) {
	for _, c := range []struct{ got, want, why string }{
		{addDays("2028-02-28", 1), "2028-02-29", "2028 is a leap year"},
		{addDays("2028-02-28", 2), "2028-03-01", "the day after the 29th"},
		{addDays("2027-02-28", 1), "2027-03-01", "2027 is not"},
		{addDays("2100-02-28", 1), "2100-03-01", "a century year that is not a leap year"},
		{addDays("2026-03-08", 1), "2026-03-09", "the day after a daylight-saving change is the next date, not a skipped one"},
		{addDays("2026-03-31", 1), "2026-04-01", "a month end"},
		{addDays("2026-01-01", -1), "2025-12-31", "a year start"},
		{weekStart("2026-03-02"), "2026-03-02", "2 March 2026 is a Monday"},
		{weekStart("2026-03-01"), "2026-02-23", "a Sunday belongs to the week that began on the Monday before"},
		{weekStart("2026-03-08"), "2026-03-02", "Sunday 8 March"},
		{monthEnd("2028-02-10"), "2028-02-29", "February of a leap year"},
		{monthEnd("2026-02-10"), "2026-02-28", "February"},
		{monthStart("2026-03-31"), "2026-03-01", "month start"},
	} {
		if c.got != c.want {
			t.Errorf("%s: got %s, want %s", c.why, c.got, c.want)
		}
	}
	if w := weekday("2026-03-02"); w != 0 {
		t.Errorf("Monday is %d", w)
	}
	if w := weekday("2026-03-08"); w != 6 {
		t.Errorf("Sunday is %d", w)
	}
	if n := daysBetween("2026-03-07", "2026-03-09"); n != 2 {
		t.Errorf("days between = %d", n)
	}
	if n := daysBetween("2028-02-28", "2028-03-01"); n != 2 {
		t.Errorf("days across a leap day = %d, want 2", n)
	}
	// The week that straddles a month end: Mon 30 March to Sun 5 April.
	f, to := periodOf("week", "2026-04-02")
	if f != "2026-03-30" || to != "2026-04-05" {
		t.Errorf("week = %s..%s", f, to)
	}
	f, to = periodOf("month", "2026-04-02")
	if f != "2026-04-01" || to != "2026-04-30" {
		t.Errorf("month = %s..%s", f, to)
	}
}

func v(n, target int, from string, at int) Version {
	return Version{N: n, Target: target, From: from, ProgressAt: at}
}

func TestAGoalChangedMidWeekIsJudgedByWhatWasInForceThen(t *testing.T) {
	// The week is Monday 2 March to Sunday 8 March 2026.
	const from, to = "2026-03-02", "2026-03-08"
	for _, c := range []struct {
		name       string
		versions   []Version
		value      int
		wantMet    bool
		wantTarget int
	}{
		{"one target, reached exactly", []Version{v(1, 5000, "2026-03-02", 0)}, 5000, true, 5000},
		{"one target, one word short", []Version{v(1, 5000, "2026-03-02", 0)}, 4999, false, 5000},
		{"lowered on Wednesday after 2000 words, 3500 in the week: the new target is met", []Version{v(1, 5000, "2026-03-02", 0), v(2, 3000, "2026-03-04", 2000)}, 3500, true, 3000},
		{"lowered on Wednesday, 2900 in the week: neither target is met", []Version{v(1, 5000, "2026-03-02", 0), v(2, 3000, "2026-03-04", 2000)}, 2900, false, 3000},
		{"raised on Wednesday after the old target was reached (3200 of 3000): the week stays met", []Version{v(1, 3000, "2026-03-02", 0), v(2, 5000, "2026-03-04", 3200)}, 3400, true, 5000},
		{"raised on Wednesday at 2800, before reaching 3000, and 3400 in the week: not met", []Version{v(1, 3000, "2026-03-02", 0), v(2, 5000, "2026-03-04", 2800)}, 3400, false, 5000},
		{"raised and then reached the new one", []Version{v(1, 3000, "2026-03-02", 0), v(2, 5000, "2026-03-04", 2800)}, 5200, true, 5000},
		{"changed on the first day of the week, after 100 words", []Version{v(1, 3000, "2026-02-20", 0), v(2, 500, "2026-03-02", 100)}, 600, true, 500},
		{"changed on the last day", []Version{v(1, 3000, "2026-02-20", 0), v(2, 9000, "2026-03-08", 3100)}, 3100, true, 9000},
		{"a version replaced before the week is not asked about it", []Version{v(1, 100, "2026-02-01", 0), v(2, 4000, "2026-02-20", 50)}, 3999, false, 4000},
		{"made after the week: the week is not this goal's", []Version{v(1, 100, "2026-03-09", 0)}, 5000, false, 0},
	} {
		g := Goal{Period: "week", Measure: "words", Start: c.versions[0].From, Versions: c.versions}
		got := g.judge(from, to, c.value)
		if got.Met != c.wantMet || got.Target != c.wantTarget {
			t.Errorf("%s: got met=%v target=%d, want met=%v target=%d", c.name, got.Met, got.Target, c.wantMet, c.wantTarget)
		}
	}
}

func TestIntensityStepsFollowThePersonsOwnWritingDays(t *testing.T) {
	mk := func(vals ...int) Days {
		d := Days{}
		day := "2026-01-01"
		for _, w := range vals {
			d[day] = Day{Words: w}
			day = addDays(day, 1)
		}
		return d
	}
	// Eight writing days, 10 to 80: nearest ranks 2, 4 and 6 give cuts 20, 40 and 60.
	th := thresholdsOf(mk(80, 10, 70, 20, 60, 30, 50, 40, 0, 0))
	if th.Cuts != [3]int{20, 40, 60} || th.Days != 8 || th.Uniform {
		t.Fatalf("thresholds = %+v", th)
	}
	for w, want := range map[int]int{0: 0, 10: 1, 20: 1, 21: 2, 30: 2, 40: 2, 50: 3, 60: 3, 61: 4, 70: 4, 80: 4} {
		if got := th.level(w); got != want {
			t.Errorf("level(%d) = %d, want %d", w, got, want)
		}
	}
	// Five writing days, 5 to 25: ranks ceil(5/4)=2, ceil(10/4)=3, ceil(15/4)=4: cuts 10, 15, 20.
	th = thresholdsOf(mk(5, 10, 15, 20, 25))
	if th.Cuts != [3]int{10, 15, 20} {
		t.Fatalf("thresholds = %+v", th)
	}
	for w, want := range map[int]int{5: 1, 10: 1, 15: 2, 20: 3, 25: 4} {
		if got := th.level(w); got != want {
			t.Errorf("level(%d) = %d, want %d", w, got, want)
		}
	}
	// One writing day, or the same every day: the heaviest step, not the faintest.
	for _, d := range []Days{mk(300), mk(500, 500, 500)} {
		th = thresholdsOf(d)
		if !th.Uniform || th.level(500) != 4 && th.level(300) != 4 {
			t.Errorf("uniform: %+v", th)
		}
	}
	// No writing at all: every cell is empty.
	if th = thresholdsOf(Days{}); th.level(0) != 0 || th.Days != 0 {
		t.Errorf("empty: %+v", th)
	}
}

func TestTheCalendarOfTwentyFourWeeks(t *testing.T) {
	const today = "2026-03-11" // a Wednesday
	days := Days{"2026-03-11": {Words: 500}, "2026-03-09": {Words: 100}, "2025-09-30": {Words: 40}, "2025-09-28": {Words: 999}}
	th := thresholdsOf(days)
	hits := map[string]bool{"2026-03-11": true}
	c := calendar(days, hits, today, 24, th)

	if len(c.Weeks) != 24 {
		t.Fatalf("%d columns", len(c.Weeks))
	}
	// 2026-03-09 is the Monday of today's week; 23 weeks (161 days) before it is 2025-09-29.
	if c.From != "2025-09-29" || c.To != "2026-03-15" || c.Weeks[0].Start != "2025-09-29" || c.Weeks[23].Start != "2026-03-09" {
		t.Errorf("range %s..%s, first %s, last %s", c.From, c.To, c.Weeks[0].Start, c.Weeks[23].Start)
	}
	// The month labels sit on the columns that hold the first of a month: 1 Oct (Wed, column of 29 Sep), 1 Nov (Sat),
	// 1 Dec (Mon), 1 Jan (Thu), 1 Feb (Sun), 1 Mar (Sun).
	var months []int
	for _, w := range c.Weeks {
		if w.Month != 0 {
			months = append(months, w.Month)
		}
	}
	if !reflect.DeepEqual(months, []int{10, 11, 12, 1, 2, 3}) {
		t.Errorf("month labels %v", months)
	}
	if !reflect.DeepEqual(c.LabelRows, []int{0, 2, 4}) || c.Levels != 5 {
		t.Errorf("labels %v levels %d", c.LabelRows, c.Levels)
	}
	last := c.Weeks[23]
	// Monday 9 (100 words), Tuesday empty, Wednesday 11 (500 words, a goal day); Thursday to Sunday are not days yet.
	if last.Cells[0].Words != 100 || last.Cells[1].Words != 0 || last.Cells[2].Words != 500 || !last.Cells[2].Hit || last.Cells[0].Hit {
		t.Errorf("this week: %+v", last.Cells[:3])
	}
	for i := 3; i < 7; i++ {
		if !last.Cells[i].Future || last.Cells[i].Level != 0 {
			t.Errorf("cell %d should be future: %+v", i, last.Cells[i])
		}
	}
	if last.Cells[2].Future || last.Cells[1].Future {
		t.Error("today and the days before it are not the future")
	}
	// 30 September 2025 (a Tuesday) is in the first column; 28 September (a Sunday) is the week before it, outside.
	if c.Weeks[0].Cells[1].Day != "2025-09-30" || c.Weeks[0].Cells[1].Words != 40 {
		t.Errorf("first column: %+v", c.Weeks[0].Cells[1])
	}
	// Writing days in the window: 40, 100, 500; the 999 of 28 September is outside.
	if c.WritingDays != 3 || c.Words != 640 {
		t.Errorf("writing days %d, words %d", c.WritingDays, c.Words)
	}
	if c.AllTimeDays != 4 {
		t.Errorf("all-time writing days %d", c.AllTimeDays)
	}
	// No cell anywhere carries a level for an empty day, and none is marked as anything but empty.
	for _, w := range c.Weeks {
		for _, cell := range w.Cells {
			if cell.Words == 0 && (cell.Level != 0 || cell.Hit) {
				t.Errorf("an empty day carries a mark: %+v", cell)
			}
		}
	}
}

func TestSeriesBucketsAndLeapDay(t *testing.T) {
	for _, c := range []struct {
		from, to, want string
	}{
		{"2026-02-10", "2026-03-11", "day"},  // 30 days
		{"2026-01-01", "2026-04-02", "day"},  // 92 days
		{"2026-01-01", "2026-04-03", "week"}, // 93 days
		{"2024-01-01", "2026-03-11", "month"},
	} {
		if got := bucketFor(c.from, c.to); got != c.want {
			t.Errorf("%s..%s is %s, want %s", c.from, c.to, got, c.want)
		}
	}
	if got := bucketFor("2024-01-01", "2026-03-11"); got != "month" {
		t.Error(got)
	}
	// 100 days of weeks: Thursday 1 January to Friday 10 April 2026. The first week is the four days to Sunday 4 January.
	days := Days{"2026-01-01": {Words: 10}, "2026-01-04": {Words: 5}, "2026-01-05": {Words: 7}, "2026-04-10": {Words: 3, Net: -2}}
	b, pts := series(days, "2026-01-01", "2026-04-10")
	if b != "week" {
		t.Fatalf("bucket %s", b)
	}
	if pts[0].Start != "2026-01-01" || pts[0].Words != 15 || pts[1].Start != "2026-01-05" || pts[1].Words != 7 {
		t.Errorf("first buckets %+v %+v", pts[0], pts[1])
	}
	last := pts[len(pts)-1]
	if last.Start != "2026-04-06" || last.Words != 3 || last.Net != -2 {
		t.Errorf("last bucket %+v", last)
	}
	// 15 weeks and a bit: Jan 1-4, then Mondays Jan 5 .. Apr 6 (14 of them) = 15 buckets.
	if len(pts) != 15 {
		t.Errorf("%d buckets", len(pts))
	}
	// Months across a leap day: the 29th of February 2028 is in February, and a day of March is in March.
	days = Days{"2028-02-29": {Words: 100}, "2028-03-01": {Words: 7}, "2028-02-01": {Words: 1}}
	b, pts = series(days, "2027-12-15", "2028-04-15")
	if b != "week" { // 123 days
		t.Fatalf("bucket %s", b)
	}
	b, pts = series(days, "2026-01-01", "2028-04-15") // over 800 days: months
	if b != "month" {
		t.Fatalf("bucket %s", b)
	}
	got := map[string]int{}
	for _, p := range pts {
		got[p.Start] = p.Words
	}
	if got["2028-02-01"] != 101 || got["2028-03-01"] != 7 {
		t.Errorf("months: feb %d mar %d", got["2028-02-01"], got["2028-03-01"])
	}
}

func TestPeriodCardsAndTheirDeltas(t *testing.T) {
	const today = "2026-03-11"
	days := Days{
		"2026-03-11": {Words: 500, Net: 450, Seconds: 600}, // today
		"2026-03-10": {Words: 400, Seconds: 300},
		"2026-03-08": {Words: 600},
		"2026-03-07": {Words: 300},
		"2026-03-06": {Words: 200},
		"2026-03-05": {Words: 100},
		"2026-03-04": {Words: 700}, // the 7 days before: 26 Feb to 4 Mar
		"2026-02-28": {Words: 250},
		"2026-02-20": {Words: 1000}, // inside the 30 days (10 Feb to 11 Mar)
		"2026-01-15": {Words: 90},   // the 30 days before (11 Jan to 9 Feb)
		"2025-03-12": {Words: 77},   // the first day of the 365-day window
		"2025-03-11": {Words: 33},   // the day before it: the previous period
	}
	cs := cards(days, today)
	if len(cs) != 4 {
		t.Fatal(len(cs))
	}
	by := map[string]Card{}
	for _, c := range cs {
		by[c.Key] = c
	}
	today1 := by["today"]
	if today1.Words != 500 || today1.Net != 450 || today1.Minutes != 10 || today1.Previous.Words != 400 || today1.Previous.Minutes != 5 {
		t.Errorf("today: %+v", today1)
	}
	if today1.Delta.Change != 100 || today1.Delta.Percent == nil || *today1.Delta.Percent != 25 {
		t.Errorf("today delta %+v", today1.Delta)
	}
	// the last seven days are 5 to 11 March
	if !reflect.DeepEqual(today1.Spark, []int{100, 200, 300, 600, 0, 400, 500}) {
		t.Errorf("today sparkline %v", today1.Spark)
	}
	d7 := by["7d"]
	// 5 to 11 March: 100+200+300+600+0+400+500 = 2100 over 6 writing days; the 7 before: 700+250 = 950.
	if d7.Words != 2100 || d7.WritingDays != 6 || d7.Previous.Words != 950 || d7.Previous.WritingDays != 2 {
		t.Errorf("7d: %+v", d7)
	}
	if d7.Delta.Change != 1150 || d7.Delta.Percent == nil || *d7.Delta.Percent != 121 { // 1150*100/950 = 121.05
		t.Errorf("7d delta %+v", d7.Delta)
	}
	if !reflect.DeepEqual(d7.Spark, today1.Spark) {
		t.Errorf("7d sparkline %v", d7.Spark)
	}
	d30 := by["30d"]
	// 2100 + 700 + 250 + 1000 = 4050; the 30 days before: 90.
	if d30.Words != 4050 || d30.Previous.Words != 90 || d30.Delta.Change != 3960 || d30.Delta.Percent == nil || *d30.Delta.Percent != 4400 {
		t.Errorf("30d: %+v", d30)
	}
	// index 0 is 10 February, so 20 February is index 10 and today is index 29
	if len(d30.Spark) != 30 || d30.Spark[10] != 1000 || d30.Spark[29] != 500 || d30.Spark[22] != 700 || d30.Spark[18] != 250 {
		t.Errorf("30d sparkline %v", d30.Spark)
	}
	d365 := by["365d"]
	// 12 March 2025 to 11 March 2026 holds everything but the 33 and the 90 (15 January 2026 is inside!): the 90 is inside.
	if d365.Words != 4050+90+77 || d365.Previous.Words != 33 {
		t.Errorf("365d: %+v", d365)
	}
	if len(d365.Spark) != 52 || d365.Spark[51] != 2100 || d365.Spark[50] != 950 {
		t.Errorf("365d sparkline: last %d, before %d, len %d", d365.Spark[51], d365.Spark[50], len(d365.Spark))
	}
	// Nothing before: no percentage, and no division.
	e := cards(Days{"2026-03-11": {Words: 5}}, today)
	if e[0].Delta.Percent != nil || e[0].Delta.Change != 5 {
		t.Errorf("no previous: %+v", e[0].Delta)
	}
}

func TestForecastArithmetic(t *testing.T) {
	const today = "2026-03-11"
	// 6,000 words to go; 2,800 added over the 28 days (100 a day) on 10 writing days; a deadline on 30 April.
	f := forecast(6000, "2026-04-30", today, 2800, 10, "words", "net")
	// 11 March to 30 April is 20 days of March after today, then 30 of April: 50 days, so 51 counting today.
	if f.DaysLeft != 51 || f.RequiredPerDay != 118 { // ceil(6000/51): 51*117 = 5967
		t.Errorf("deadline: %+v", f)
	}
	// 6000 / 100 a day = 60 days: today + 60 = 10 May. The deadline is 10 days before it.
	if f.ProjectedDate != "2026-05-10" || f.MarginDays == nil || *f.MarginDays != -10 || f.Basis.PerDay != 100 || !f.Estimate {
		t.Errorf("projection: %+v", f)
	}
	// One word under: 6000/99.96 = 60.02, so a 61st day; the per-day figure rounds to 100.
	f = forecast(6000, "", today, 2799, 10, "words", "net")
	if f.ProjectedDate != "2026-05-11" || f.Basis.PerDay != 100 || f.Basis.Measure != "net" || f.DaysLeft != 0 || f.MarginDays != nil {
		t.Errorf("one under: %+v", f)
	}
	// Too few writing days, or no growth: no date. Absent is the answer.
	if f = forecast(6000, "", today, 2800, 4, "words", "net"); f.ProjectedDate != "" {
		t.Errorf("4 writing days gave %s", f.ProjectedDate)
	}
	if f = forecast(6000, "", today, 0, 20, "words", "net"); f.ProjectedDate != "" {
		t.Errorf("no growth gave %s", f.ProjectedDate)
	}
	if f = forecast(6000, "", today, -50, 20, "words", "net"); f.ProjectedDate != "" {
		t.Errorf("shrinking gave %s", f.ProjectedDate)
	}
	// A pace so slow it passes ten years is not a forecast.
	if f = forecast(6_000_000, "", today, 280, 6, "words", "net"); f.ProjectedDate != "" {
		t.Errorf("a ten-year projection was given: %s", f.ProjectedDate)
	}
	// Reached: nothing to ask of any day.
	if f = forecast(0, "2026-04-30", today, 2800, 10, "words", "net"); f.RequiredPerDay != 0 || f.ProjectedDate != "" || f.DaysLeft != 0 {
		t.Errorf("reached: %+v", f)
	}
	// A deadline that has passed asks for nothing.
	if f = forecast(100, "2026-03-01", today, 0, 0, "words", "net"); f.RequiredPerDay != 0 || f.DaysLeft != 0 {
		t.Errorf("past deadline: %+v", f)
	}
	// The deadline is today: one day left, all of it required today.
	if f = forecast(100, today, today, 0, 0, "words", "net"); f.DaysLeft != 1 || f.RequiredPerDay != 100 {
		t.Errorf("deadline today: %+v", f)
	}
}

func TestTrendOffersResourcesAndNeverAWarning(t *testing.T) {
	const today = "2026-03-11"
	// The last three weeks are 19 February to 11 March, the three before 29 January to 18 February.
	at := func(recent, previous int) Days {
		return Days{"2026-03-01": {Words: recent}, "2026-02-10": {Words: previous}, "2026-01-28": {Words: 9999}, "2026-02-18": {}}
	}
	for _, c := range []struct {
		recent, previous int
		dir              string
		pct              *int
	}{
		{3000, 2000, "up", ptr(50)},
		{2200, 2000, "up", ptr(10)}, // exactly +10
		{2100, 2000, "steady", ptr(5)},
		{1900, 2000, "steady", ptr(-5)},
		{1800, 2000, "down", ptr(-10)}, // exactly -10
		{0, 2000, "down", ptr(-100)},
		{5, 0, "new", nil},
		{0, 0, "none", nil},
	} {
		tr := trendOf(at(c.recent, c.previous), today)
		if tr.Direction != c.dir || (c.pct == nil) != (tr.ChangePercent == nil) || (c.pct != nil && *c.pct != *tr.ChangePercent) {
			t.Errorf("%d vs %d: %+v, want %s %v", c.recent, c.previous, tr, c.dir, c.pct)
		}
		if tr.MessageKey != "studio.trend."+c.dir {
			t.Errorf("key %s", tr.MessageKey)
		}
		if (c.dir == "down") != (len(tr.Resources) > 0) {
			t.Errorf("%s: resources %v", c.dir, tr.Resources)
		}
	}
	// 28 January is before both windows, and 29 January is the first day of the earlier one.
	tr := trendOf(Days{"2026-01-29": {Words: 100}, "2026-01-28": {Words: 100}}, today)
	if tr.PreviousWords != 100 {
		t.Errorf("the window edge: %+v", tr)
	}
}

func ptr(n int) *int { return &n }

func TestWeekdaysAndHours(t *testing.T) {
	// Monday 2 March, Wednesday 4 March and Monday 9 March 2026.
	days := Days{"2026-03-02": {Words: 100}, "2026-03-04": {Words: 300}, "2026-03-09": {Words: 200}, "2026-03-03": {Words: 0}}
	w, best := weekdays(days, "2026-03-01", "2026-03-15")
	if w[0].Words != 300 || w[0].WritingDays != 2 || w[0].AvgPerWritingDay != 150 || w[2].Words != 300 || w[1].WritingDays != 0 {
		t.Errorf("weekdays %+v", w)
	}
	if best != 0 { // Monday and Wednesday tie at 300: the earlier weekday
		t.Errorf("best %d", best)
	}
	if _, b := weekdays(Days{}, "2026-03-01", "2026-03-15"); b != -1 {
		t.Errorf("no writing: best %d", b)
	}
	zone := func(name string) *time.Location {
		l, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	rows := []SessionRow{
		{time.Date(2026, 3, 8, 4, 59, 0, 0, time.UTC), "America/New_York", 120}, // still EST: 23:59 on 7 March
		{time.Date(2026, 3, 8, 5, 1, 0, 0, time.UTC), "America/New_York", 30},   // 00:01 on 8 March
		{time.Date(2026, 3, 9, 3, 59, 0, 0, time.UTC), "America/New_York", 80},  // EDT now: 23:59 on 8 March
		{time.Date(2026, 4, 4, 13, 30, 0, 0, time.UTC), "Pacific/Auckland", 40}, // NZDT, UTC+13: 02:30 on 5 April
		{time.Date(2026, 4, 4, 15, 30, 0, 0, time.UTC), "Pacific/Auckland", 60}, // NZST after the clocks go back at 03:00: 03:30
		{time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC), "Europe/Dublin", 25},    // 01:30 UTC is 02:30 BST (clocks went forward at 01:00 UTC)
		{time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), "Europe/Dublin", 15},    // 00:30 GMT
		{time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC), "Europe/Dublin", 0},     // nothing
	}
	h, parts, hb := hours(rows, zone)
	want := map[int]int{23: 200, 0: 30 + 15, 2: 40 + 25, 3: 60}
	for hr, wds := range want {
		if h[hr].Words != wds {
			t.Errorf("hour %d: %d, want %d", hr, h[hr].Words, wds)
		}
	}
	if hb != 23 {
		t.Errorf("best hour %d", hb)
	}
	// night 0 to 5: 45 + 65 + 60 = 170; evening 18 to 23: 200.
	if parts[0].Part != "night" || parts[0].Words != 170 || parts[3].Part != "evening" || parts[3].Words != 200 || parts[1].Words != 0 {
		t.Errorf("parts %+v", parts)
	}
}

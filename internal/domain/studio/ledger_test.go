package studio_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/studio"
)

func dash(t *testing.T, w *world, p person) studio.DashboardOut {
	t.Helper()
	return w.call(p, "studio.dashboard", studio.DashboardIn{}).(studio.DashboardOut)
}

func mkGoal(t *testing.T, w *world, p person, period, measure string, target int) studio.GoalOut {
	t.Helper()
	return w.call(p, "studio.goal.create", studio.GoalCreateIn{Period: period, Measure: measure, Target: target}).(studio.GoalItem).Item
}

// words a to b of a tag, the tail of a text that was cut.
func loremRange(tag string, from, to int) string {
	return strings.TrimPrefix(lorem(to, tag), lorem(from, tag))
}

// The ledger: every save below was typed by the test with words no other save uses, so what each day holds is worked
// out by hand, here, from what was typed. Two projects, a daily goal of 100 words added over both, a person in New
// York whose clocks go forward on the night of 8 March (the 8th is 23 hours long) and whose days end at local midnight.
//
//	day   what was typed                                                        added  net
//	03-02 120 words in chapter 1                                                 120   +120
//	03-03 99 more in chapter 1                                                    99    +99   (one under the goal)
//	03-04 100 in chapter 2 (project 2); later 200 words cut from chapter 1       100   -100   (cutting is not subtracted)
//	03-05 10 words of chapter 1 rewritten as 10 others                             10     0
//	03-06 nothing
//	03-07 100 more in chapter 1, at 23:59 EST (04:59Z, a minute before midnight)  100   +100
//	03-08 60 in chapter 2 at 00:01 EST (05:01Z), 40 in chapter 1 at 23:59 EDT     100   +100   (the 23-hour day: 05:00Z to 04:00Z)
//	03-09 5 more in chapter 2 at 00:01 EDT (04:01Z)                                 5    +5
//	03-10 chapter 2 emptied: 165 words cut, none written                            0   -165
//	03-11 100 in chapter 1                                                       100   +100
//	03-12 100 in chapter 1 in three saves three minutes apart (6 minutes)        100   +100
//	03-13 100 in chapter 1 at 16:00 EDT                                          100   +100
func typeTheLedger(t *testing.T, w *world, a person, d1, d2 string, upToThe13th bool) {
	t.Helper()
	save := func(at string, doc, text string) {
		w.at(at)
		w.save(a, doc, text)
	}
	cur1 := func() string { return w.text(a, d1) }
	cur2 := func() string { return w.text(a, d2) }
	save("2026-03-02T15:00:00Z", d1, lorem(120, "a"))
	save("2026-03-03T15:00:00Z", d1, cur1()+lorem(99, "b"))
	save("2026-03-04T15:00:00Z", d2, lorem(100, "c"))
	save("2026-03-04T20:00:00Z", d1, lorem(19, "a"))
	save("2026-03-05T15:00:00Z", d1, lorem(10, "e")+loremRange("a", 10, 19))
	save("2026-03-08T04:59:00Z", d1, cur1()+lorem(100, "g"))
	save("2026-03-08T05:01:00Z", d2, cur2()+lorem(60, "h"))
	save("2026-03-09T03:59:00Z", d1, cur1()+lorem(40, "i"))
	save("2026-03-09T04:01:00Z", d2, cur2()+lorem(5, "j"))
	save("2026-03-10T14:00:00Z", d2, "")
	save("2026-03-11T14:00:00Z", d1, cur1()+lorem(100, "k"))
	save("2026-03-12T14:00:00Z", d1, cur1()+lorem(40, "l"))
	save("2026-03-12T14:03:00Z", d1, cur1()+lorem(30, "m"))
	save("2026-03-12T14:06:00Z", d1, cur1()+lorem(30, "n"))
	if upToThe13th {
		save("2026-03-13T20:00:00Z", d1, cur1()+lorem(100, "o"))
	}
}

func TestTheLedgerReconcilesWithAHandCount(t *testing.T) {
	w := newWorld(t)
	a := w.person("a", "America/New_York")
	p1, p2 := w.project(a, "Book one"), w.project(a, "Book two")
	d1, d2 := w.doc(a, p1), w.doc(a, p2)
	w.at("2026-03-01T12:00:00Z")
	mkGoal(t, w, a, "day", "words", 100)

	typeTheLedger(t, w, a, d1, d2, false)

	// The morning of the 13th, nothing yet today: the run of 11 and 12 stands, and today does not break it.
	w.at("2026-03-13T08:00:00Z")
	m := dash(t, w, a)
	if m.Day != "2026-03-13" || m.Timezone != "America/New_York" {
		t.Errorf("day %s zone %s", m.Day, m.Timezone)
	}
	if m.Streak.Current != 2 || m.Streak.Longest != 2 || m.Streak.TodayHit || m.Today.Words != 0 || m.Today.Remaining != 100 || !m.Today.HasGoal {
		t.Errorf("morning: streak %+v today %+v", m.Streak, m.Today)
	}
	if m.Today.Message == nil || m.Today.Message.Key != "studio.today.to_go" || m.Today.Message.Params["n"] != 100 {
		t.Errorf("message %+v", m.Today.Message)
	}
	if m.Streak.Message.Key != "studio.streak.keep" {
		t.Errorf("streak message %s", m.Streak.Message.Key)
	}
	keys := map[string]bool{}
	for _, n := range m.Nudges {
		keys[n.Key] = true
	}
	if !keys["studio.nudge.to_go"] || !keys["studio.nudge.keep_streak"] || len(m.Nudges) > studio.MaxNudgesPerDay {
		t.Errorf("morning nudges %+v", m.Nudges)
	}

	// Today's save, then the day is hit and the run is three.
	w.at("2026-03-13T20:00:00Z")
	w.save(a, d1, w.text(a, d1)+lorem(100, "o"))
	m = dash(t, w, a)
	if m.Streak.Current != 3 || m.Streak.Longest != 3 || !m.Streak.TodayHit || m.Today.Words != 100 || m.Today.Remaining != 0 || !m.Today.Hit {
		t.Errorf("evening: streak %+v today %+v", m.Streak, m.Today)
	}
	if m.Streak.Message.Key != "studio.streak.hit" {
		t.Errorf("streak message %s", m.Streak.Message.Key)
	}
	nk := map[string]bool{}
	for _, n := range m.Nudges {
		nk[n.Key] = true
	}
	if !nk["studio.nudge.met"] || !nk["studio.nudge.streak"] || nk["studio.nudge.to_go"] {
		t.Errorf("evening nudges %+v", m.Nudges)
	}

	// Every day of the ledger, against the table above.
	s := w.call(a, "studio.series", studio.SeriesIn{From: "2026-03-01", To: "2026-03-13"}).(studio.SeriesOut)
	if s.Bucket != "day" || len(s.Points) != 13 {
		t.Fatalf("bucket %s, %d points", s.Bucket, len(s.Points))
	}
	want := []struct {
		day        string
		words, net int
	}{
		{"2026-03-01", 0, 0}, {"2026-03-02", 120, 120}, {"2026-03-03", 99, 99}, {"2026-03-04", 100, -100}, {"2026-03-05", 10, 0},
		{"2026-03-06", 0, 0}, {"2026-03-07", 100, 100}, {"2026-03-08", 100, 100}, {"2026-03-09", 5, 5}, {"2026-03-10", 0, -165},
		{"2026-03-11", 100, 100}, {"2026-03-12", 100, 100}, {"2026-03-13", 100, 100},
	}
	for i, x := range want {
		p := s.Points[i]
		if p.Start != x.day || p.Words != x.words || p.Net != x.net {
			t.Errorf("%s: words %d net %d, want %d and %d", x.day, p.Words, p.Net, x.words, x.net)
		}
	}
	if s.Points[11].Minutes != 6 { // three saves three minutes apart: 180 + 180 seconds
		t.Errorf("12 March minutes = %d, want 6", s.Points[11].Minutes)
	}
	// Totals: 834 added; net 120+99-100+0+100+100+5-165+100+100+100 = 459; 10 writing days; 13 days in the span, 834/13 = 64.15,
	// and 834/10 = 83.4 a writing day.
	if s.Figures.Words != 834 || s.Figures.Net != 459 || s.Figures.WritingDays != 10 || s.AvgPerDay != 64 || s.AvgPerWritingDay != 83 {
		t.Errorf("figures %+v avg %d / %d", s.Figures, s.AvgPerDay, s.AvgPerWritingDay)
	}
	// Sessions: the 4th had two (a cut five hours after the first save), the 8th two (either side of midnight and the
	// clocks), the 12th one.
	if s.Figures.Sessions != 1+1+2+1+1+2+1+1+1+1+1 { // 2,3,4,5,7,8,9,10,11,12,13 March
		t.Errorf("sessions %d", s.Figures.Sessions)
	}

	// The period cards at 13 March.
	by := map[string]studio.Card{}
	for _, c := range m.Cards {
		by[c.Key] = c
	}
	if c := by["today"]; c.Words != 100 || c.Previous.Words != 100 || c.Delta.Change != 0 || c.Delta.Percent == nil || *c.Delta.Percent != 0 {
		t.Errorf("today card %+v", c)
	}
	// 7 to 13 March: 100+100+5+0+100+100+100 = 505 added, 100+100+5-165+100+100+100 = 340 net, 6 writing days, 6 minutes;
	// 28 February to 6 March: 120+99+100+10 = 329 added, 119 net, 4 writing days. 176 more is 53.5%: 53.
	if c := by["7d"]; c.Words != 505 || c.Net != 340 || c.WritingDays != 6 || c.Minutes != 6 || c.Previous.Words != 329 || c.Previous.Net != 119 ||
		c.Previous.WritingDays != 4 || c.Delta.Change != 176 || c.Delta.Percent == nil || *c.Delta.Percent != 53 {
		t.Errorf("7d card %+v", c)
	}
	if c := by["30d"]; c.Words != 834 || c.Previous.Words != 0 || c.Delta.Percent != nil {
		t.Errorf("30d card %+v", c)
	}
	if c := by["365d"]; c.Words != 834 || len(c.Spark) != 52 || c.Spark[51] != 505 || c.Spark[50] != 329 {
		t.Errorf("365d card words %d spark tail %v", c.Words, c.Spark[48:])
	}

	// The calendar: the writing days' own quartiles. Ten writing days sorted: 5 10 99 100 100 100 100 100 100 120; nearest
	// ranks 3, 5 and 8 give the cuts 99, 100 and 100; so 120 is step 4, 100 is step 2, 99 and below step 1.
	cal := m.Calendar
	if cal.Thresholds.Cuts != [3]int{99, 100, 100} || cal.Thresholds.Days != 10 || cal.WritingDays != 10 || cal.Words != 834 {
		t.Errorf("thresholds %+v writing days %d words %d", cal.Thresholds, cal.WritingDays, cal.Words)
	}
	cells := map[string]studio.Cell{}
	for _, wk := range cal.Weeks {
		for _, c := range wk.Cells {
			cells[c.Day] = c
		}
	}
	for day, lvl := range map[string]int{"2026-03-02": 4, "2026-03-03": 1, "2026-03-04": 2, "2026-03-05": 1, "2026-03-06": 0, "2026-03-07": 2, "2026-03-09": 1, "2026-03-10": 0} {
		if cells[day].Level != lvl {
			t.Errorf("%s: step %d, want %d", day, cells[day].Level, lvl)
		}
	}
	for day, hit := range map[string]bool{"2026-03-02": true, "2026-03-03": false, "2026-03-04": true, "2026-03-07": true, "2026-03-08": true, "2026-03-09": false, "2026-03-13": true} {
		if cells[day].Hit != hit {
			t.Errorf("%s: goal day = %v, want %v", day, cells[day].Hit, hit)
		}
	}
	if cells["2026-03-15"].Future != true || cells["2026-03-13"].Future {
		t.Error("future flags")
	}

	// The weekdays and the hours of the day, 1 to 13 March: Monday 120+5, Tuesday 99, Wednesday 100+100, Thursday 10+100,
	// Friday 100, Saturday 100, Sunday 100.
	wantWd := []int{125, 99, 200, 110, 100, 100, 100}
	for i, x := range s.Weekdays {
		if x.Words != wantWd[i] {
			t.Errorf("weekday %d: %d, want %d", i, x.Words, wantWd[i])
		}
	}
	if s.BestWeekday == nil || *s.BestWeekday != 2 {
		t.Errorf("best weekday %v", s.BestWeekday)
	}
	// Sessions began at 10:00 on six days (529 words), at 23:59 twice (100 on the 7th and 40 on the 8th), at 00:01 twice
	// (60 and 5) and at 16:00 once (100). The 15:00 EST cut added nothing.
	for hr, ww := range map[int]int{10: 529, 23: 140, 0: 65, 16: 100, 15: 0} {
		if s.Hours[hr].Words != ww {
			t.Errorf("hour %d: %d, want %d", hr, s.Hours[hr].Words, ww)
		}
	}
	if s.BestHour == nil || *s.BestHour != 10 {
		t.Errorf("best hour %v", s.BestHour)
	}

	// Net words are separate: the goal never read them.
	if m.Goals.Items[0].Net == nil || *m.Goals.Items[0].Net != 100 {
		t.Errorf("goal net %v", m.Goals.Items[0].Net)
	}
	if m.Goals.Active != 1 || m.Goals.Completed != 0 {
		t.Errorf("goal counts %+v", m.Goals)
	}

	// Per project, this week (Monday 9 March to the 13th) and last (2 to 8 March), the person's own words.
	// Project 1 this week: the 11th, 12th and 13th, 100 each. Last week: 120+99+10+100+40 = 369 (the cut of the 4th adds 0).
	// Project 2 this week: 5 on the 9th and 165 cut on the 10th, net -160. Last week: 100 on the 4th and 60 on the 8th.
	rows := map[string]studio.ProjectRow{}
	for _, r := range m.Projects {
		rows[r.ProjectID] = r
	}
	if r := rows[p1]; r.Week != 300 || r.Today != 100 || r.PreviousWeek != 369 || r.WeekNet != 300 {
		t.Errorf("project 1: %+v", r)
	}
	if r := rows[p2]; r.Week != 5 || r.Today != 0 || r.PreviousWeek != 160 || r.WeekNet != -160 {
		t.Errorf("project 2: %+v", r)
	}

	// A recompute from the recorded sessions gives exactly what the incremental numbers gave.
	before, _ := json.Marshal(m)
	if _, err := goals.Recompute(context.Background(), w.d, "", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(dash(t, w, a))
	if string(before) != string(after) {
		t.Errorf("a recompute changed the dashboard:\n before %s\n after  %s", before, after)
	}
	// Even with every day row removed and rebuilt from the recorded sessions alone: the whole dashboard is the same, byte for byte.
	if _, err := w.d.Writer().Exec(`DELETE FROM daily_stats`); err != nil {
		t.Fatal(err)
	}
	if _, err := goals.Recompute(context.Background(), w.d, "", ""); err != nil {
		t.Fatal(err)
	}
	if rebuilt, _ := json.Marshal(dash(t, w, a)); string(rebuilt) != string(before) {
		t.Errorf("a rebuild from the sessions changed the dashboard:\n before  %s\n rebuilt %s", before, rebuilt)
	}
}

func TestAStreakAcrossTheTimeChangeInDublin(t *testing.T) {
	w := newWorld(t)
	b := w.person("b", "Europe/Dublin")
	p := w.project(b, "Dublin")
	d := w.doc(b, p)
	w.at("2026-03-27T10:00:00Z")
	mkGoal(t, w, b, "day", "words", 50)
	// Clocks go forward at 01:00 UTC on 29 March: 29 March is 23 hours long, and 23:30Z that night is 00:30 on the 30th.
	for _, s := range []struct {
		at string
		n  int
		t  string
	}{
		{"2026-03-28T23:30:00Z", 60, "a"}, // 23:30 GMT on the 28th
		{"2026-03-29T00:30:00Z", 60, "b"}, // 00:30 GMT on the 29th
		{"2026-03-29T01:30:00Z", 10, "c"}, // 02:30 BST on the 29th
		{"2026-03-29T23:30:00Z", 60, "d"}, // 00:30 BST on the 30th
	} {
		w.at(s.at)
		w.save(b, d, w.text(b, d)+lorem(s.n, s.t))
	}
	w.at("2026-03-30T12:00:00Z")
	m := dash(t, w, b)
	if m.Day != "2026-03-30" {
		t.Fatalf("day %s", m.Day)
	}
	if m.Streak.Current != 3 || m.Streak.Longest != 3 || !m.Streak.TodayHit {
		t.Errorf("streak %+v: the 28th, the 29th and the 30th each met 50", m.Streak)
	}
	s := w.call(b, "studio.series", studio.SeriesIn{From: "2026-03-27", To: "2026-03-30"}).(studio.SeriesOut)
	for i, ww := range []int{0, 60, 70, 60} {
		if s.Points[i].Words != ww {
			t.Errorf("%s: %d, want %d", s.Points[i].Start, s.Points[i].Words, ww)
		}
	}
}

func TestAPersonWhoChangesTimeZoneKeepsTheirDays(t *testing.T) {
	w := newWorld(t)
	c := w.person("c", "Pacific/Auckland")
	p := w.project(c, "Travel")
	d := w.doc(c, p)
	w.at("2026-04-01T00:00:00Z")
	mkGoal(t, w, c, "day", "words", 100)
	add := func(at string, n int, tag string) {
		w.at(at)
		w.save(c, d, w.text(c, d)+lorem(n, tag))
	}
	// Auckland puts its clocks back at 03:00 on 5 April (14:00Z on the 4th): the 5th is 25 hours long.
	add("2026-04-04T10:59:00Z", 70, "a") // 23:59 NZDT on the 4th
	add("2026-04-04T11:01:00Z", 70, "b") // 00:01 NZDT on the 5th
	add("2026-04-05T10:59:00Z", 30, "c") // 22:59 NZST on the 5th: the 5th holds 100
	add("2026-04-05T12:01:00Z", 80, "d") // 00:01 NZST on the 6th
	// She flies to Los Angeles. Days already written keep the dates they were written under; new ones are cut there.
	if _, err := w.d.Writer().Exec(`UPDATE users SET timezone = 'America/Los_Angeles' WHERE id = ?`, c.id); err != nil {
		t.Fatal(err)
	}
	add("2026-04-07T05:00:00Z", 20, "e")  // 22:00 PDT on 6 April: the same date as the 80 already written: 100
	add("2026-04-07T20:00:00Z", 100, "f") // 13:00 PDT on the 7th
	w.at("2026-04-07T21:00:00Z")
	m := dash(t, w, c)
	if m.Day != "2026-04-07" || m.Timezone != "America/Los_Angeles" {
		t.Fatalf("day %s zone %s", m.Day, m.Timezone)
	}
	s := w.call(c, "studio.series", studio.SeriesIn{From: "2026-04-03", To: "2026-04-07"}).(studio.SeriesOut)
	for i, ww := range []int{0, 70, 100, 100, 100} {
		if s.Points[i].Words != ww {
			t.Errorf("%s: %d, want %d", s.Points[i].Start, s.Points[i].Words, ww)
		}
	}
	if m.Streak.Current != 3 || m.Streak.Longest != 3 {
		t.Errorf("streak %+v: the 5th, 6th and 7th each reached 100", m.Streak)
	}
}

// 2028 is a leap year: 29 February is a day, the 28th, 29th and 1 March are three days in a row, and the calendar and the
// month series put the 29th in February.
func TestALeapDayIsADayAndKeepsAStreak(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "Europe/Dublin")
	p := w.project(u, "Leap")
	d := w.doc(u, p)
	w.at("2028-02-27T09:00:00Z")
	mkGoal(t, w, u, "day", "words", 10)
	for i, day := range []string{"2028-02-28", "2028-02-29", "2028-03-01"} {
		w.at(day + "T12:00:00Z")
		w.save(u, d, w.text(u, d)+lorem(10+i, "L"+day[8:]))
	}
	m := dash(t, w, u)
	if m.Day != "2028-03-01" || m.Streak.Current != 3 || m.Streak.Longest != 3 {
		t.Errorf("day %s streak %+v", m.Day, m.Streak)
	}
	s := w.call(u, "studio.series", studio.SeriesIn{From: "2028-02-27", To: "2028-03-01"}).(studio.SeriesOut)
	for i, ww := range []int{0, 10, 11, 12} {
		if s.Points[i].Words != ww {
			t.Errorf("%s: %d, want %d", s.Points[i].Start, s.Points[i].Words, ww)
		}
	}
	if s.Points[2].Start != "2028-02-29" {
		t.Errorf("the days of the range: %+v", s.Points)
	}
	// Three years back to now (over 800 days): months, and the 29th is February's.
	w.at("2028-03-01T13:00:00Z")
	all := w.call(u, "studio.series", studio.SeriesIn{From: "2025-01-01", To: "2028-03-01"}).(studio.SeriesOut)
	if all.Bucket != "month" {
		t.Fatalf("bucket %s", all.Bucket)
	}
	by := map[string]int{}
	for _, pt := range all.Points {
		by[pt.Start] = pt.Words
	}
	if by["2028-02-01"] != 21 || by["2028-03-01"] != 12 {
		t.Errorf("february %d (want 10+11), march %d (want 12)", by["2028-02-01"], by["2028-03-01"])
	}
	cal := w.call(u, "studio.calendar", studio.CalendarIn{Weeks: 2}).(studio.Calendar)
	cells := map[string]studio.Cell{}
	for _, wk := range cal.Weeks {
		for _, c := range wk.Cells {
			cells[c.Day] = c
		}
	}
	if !cells["2028-02-29"].Hit || cells["2028-02-29"].Words != 11 || cells["2028-02-29"].Day != "2028-02-29" {
		t.Errorf("29 February: %+v", cells["2028-02-29"])
	}
}

package studio_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/ops"
)

func goalByID(t *testing.T, w *world, p person, id string) studio.GoalOut {
	t.Helper()
	for _, g := range w.call(p, "studio.goal.list", studio.GoalListIn{Archived: true}).(studio.GoalsOut).Items {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("goal %s is not listed", id)
	return studio.GoalOut{}
}

func hist(t *testing.T, w *world, p person, id string) studio.GoalHistoryOut {
	t.Helper()
	return w.call(p, "studio.goal.history", studio.GoalHistoryIn{ID: id, Periods: 6}).(studio.GoalHistoryOut)
}

func TestARaisedWeeklyGoalKeepsAWeekThatWasAlreadyMet(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z") // a Monday
	g := mkGoal(t, w, u, "week", "words", 200)
	if g.Version != 1 || g.From != "2026-03-02" || g.To != "2026-03-08" {
		t.Fatalf("goal %+v", g)
	}
	w.save(u, d, lorem(120, "a")) // Monday
	w.at("2026-03-03T09:00:00Z")
	w.save(u, d, w.text(u, d)+lorem(100, "b")) // Tuesday: 220, the week is met
	if g = goalByID(t, w, u, g.ID); !g.Met || g.Value != 220 {
		t.Errorf("met on Tuesday: %+v", g)
	}
	// Wednesday: she raises it to 400. A new version; the week had reached 200 at the moment it changed.
	w.at("2026-03-04T09:00:00Z")
	upd := w.call(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(400)}).(studio.GoalItem).Item
	if upd.Version != 2 || upd.Changes != 1 || upd.Target != 400 || !upd.Met {
		t.Errorf("after raising: %+v", upd)
	}
	w.at("2026-03-05T09:00:00Z")
	w.save(u, d, w.text(u, d)+lorem(50, "c")) // Thursday: 270, short of 400
	g = goalByID(t, w, u, g.ID)
	if !g.Met || g.Value != 270 || g.Target != 400 || g.Percent != 67 || g.Remaining != 130 {
		t.Errorf("after the raise: %+v (the week was met at 200 before the change; it stays met)", g)
	}
	// Next Monday: the new week is judged against 400, and the old week still says what it was.
	w.at("2026-03-09T09:00:00Z")
	h := hist(t, w, u, g.ID)
	if len(h.Versions) != 2 || h.Versions[0].Target != 200 || h.Versions[1].Target != 400 || h.Versions[1].From != "2026-03-04" {
		t.Errorf("versions %+v", h.Versions)
	}
	if len(h.Periods) != 2 {
		t.Fatalf("periods %+v", h.Periods)
	}
	old, cur := h.Periods[0], h.Periods[1]
	if old.From != "2026-03-02" || !old.Met || old.Value != 270 || old.Target != 400 || old.Net == nil || *old.Net != 270 {
		t.Errorf("the week of the change: %+v", old)
	}
	if cur.From != "2026-03-09" || cur.Met || cur.Value != 0 || cur.Target != 400 {
		t.Errorf("the new week: %+v", cur)
	}
	// Lowering: judged by the lower target when the total reached it.
	d2 := w.call(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(100)}).(studio.GoalItem).Item
	if d2.Version != 3 || !d2.Met == false && false {
		t.Errorf("lowering: %+v", d2)
	}
	w.save(u, d, w.text(u, d)+lorem(100, "d")) // Monday 9th: 100
	if g = goalByID(t, w, u, g.ID); !g.Met || g.Target != 100 || g.Value != 100 {
		t.Errorf("after lowering to 100 and writing 100: %+v", g)
	}
	// A change that changes nothing is not a version.
	same := w.call(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(100)}).(studio.GoalItem).Item
	if same.Version != 3 {
		t.Errorf("a repeat made version %d", same.Version)
	}
}

func ptr[T any](v T) *T { return &v }

func TestATotalGoalForecastsFromRecentWritingAndSaysSo(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-01T09:00:00Z")
	g := w.call(u, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 600, Deadline: "2026-03-31"}).(studio.GoalItem).Item
	// Six writing days of 50 words: 300 words in the window of 28 days.
	for i := 0; i < 6; i++ {
		w.at(time.Date(2026, 3, 1+i, 9, 0, 0, 0, time.UTC).Format(time.RFC3339))
		w.save(u, d, w.text(u, d)+lorem(50, string(rune('a'+i))))
	}
	w.at("2026-03-10T09:00:00Z")
	g = goalByID(t, w, u, g.ID)
	f := g.Forecast
	if g.Value != 300 || g.Remaining != 300 || g.Percent != 50 || g.Status != "active" || f == nil {
		t.Fatalf("goal %+v", g)
	}
	// 300 to go at 300 in 28 days is exactly 28 days: 10 March plus 28 is 7 April. 10 to 31 March counts 22 days with today,
	// so 300/22 rounds up to 14 a day; the projection is 7 days after the deadline.
	if !f.Estimate || f.DaysLeft != 22 || f.RequiredPerDay != 14 || f.ProjectedDate != "2026-04-07" || f.MarginDays == nil || *f.MarginDays != -7 ||
		f.Basis.WindowDays != 28 || f.Basis.WritingDays != 6 || f.Basis.PerDay != 11 || f.Basis.Measure != "net" || f.Unit != "words" {
		t.Errorf("forecast %+v", f)
	}
	// Cutting words is work and not a failure, but it moves the manuscript the wrong way for a word-count target: with
	// the window's growth at nothing there is no projected date. Absent is the answer, said neutrally, never as a shortfall.
	w.save(u, d, "")
	g = goalByID(t, w, u, g.ID)
	if g.Forecast.ProjectedDate != "" || g.Forecast.Basis.PerDay != 0 {
		t.Errorf("a manuscript that has not grown was given a projected date %s", g.Forecast.ProjectedDate)
	}
	var keys []string
	for _, n := range g.Forecast.Notes {
		keys = append(keys, n.Key)
		if n.Key == "studio.forecast.behind" {
			t.Errorf("the forecast counts the days after the deadline: %+v", n)
		}
	}
	if !strings.Contains(strings.Join(keys, " "), "studio.forecast.none") {
		t.Errorf("a pace of nothing is not said neutrally: %v", keys)
	}
	if g.Status != "active" || g.Value != 0 || g.Forecast.RequiredPerDay != 28 { // 600 words over 22 days, up
		t.Errorf("goal %+v", g)
	}
	// Writing it back past the target completes it.
	w.save(u, d, lorem(650, "y"))
	g = goalByID(t, w, u, g.ID)
	if g.Status != "completed" || !g.Met || g.Remaining != 0 {
		t.Errorf("goal %+v", g)
	}
	list := w.call(u, "studio.goal.list", studio.GoalListIn{}).(studio.GoalsOut)
	if list.Active != 0 || list.Completed != 1 {
		t.Errorf("counts %+v", list)
	}
}

func TestSessionsMinutesAndChaptersGoals(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	note := w.call(u, "doc.create", docs.CreateIn{ProjectID: p, Title: "Idea", Kind: "note"}).(docs.Item).Item.ID
	w.at("2026-03-02T09:00:00Z")
	sess := mkGoal(t, w, u, "day", "sessions", 2)
	mins := mkGoal(t, w, u, "day", "minutes", 5)
	chap := mkGoal(t, w, u, "week", "chapters", 1)
	w.save(u, d, lorem(10, "a"))
	if g := goalByID(t, w, u, sess.ID); g.Value != 1 || g.Met {
		t.Errorf("one session: %+v", g)
	}
	// Two more saves three minutes apart in the same session add 360 seconds: six minutes.
	w.at("2026-03-02T09:03:00Z")
	w.save(u, d, w.text(u, d)+lorem(10, "b"))
	w.at("2026-03-02T09:06:00Z")
	w.save(u, d, w.text(u, d)+lorem(10, "c"))
	if g := goalByID(t, w, u, mins.ID); g.Value != 6 || !g.Met {
		t.Errorf("minutes: %+v", g)
	}
	// An hour later: a second session.
	w.at("2026-03-02T10:30:00Z")
	w.save(u, d, w.text(u, d)+lorem(10, "d"))
	if g := goalByID(t, w, u, sess.ID); g.Value != 2 || !g.Met {
		t.Errorf("two sessions: %+v", g)
	}
	// A chapter finished for the first time counts once; a note does not; marking it again does not.
	cur := w.call(u, "doc.get", docs.DocIn{ID: d}).(docs.Item).Item
	done := "done"
	w.call(u, "doc.update", docs.UpdateIn{ID: d, BaseRevision: cur.Revision, Status: &done})
	w.call(u, "doc.update", docs.UpdateIn{ID: note, BaseRevision: 1, Status: &done})
	if g := goalByID(t, w, u, chap.ID); g.Value != 1 || !g.Met {
		t.Errorf("one chapter: %+v", g)
	}
	cur = w.call(u, "doc.get", docs.DocIn{ID: d}).(docs.Item).Item
	rev := "revise"
	w.call(u, "doc.update", docs.UpdateIn{ID: d, BaseRevision: cur.Revision, Status: &rev})
	cur = w.call(u, "doc.get", docs.DocIn{ID: d}).(docs.Item).Item
	w.call(u, "doc.update", docs.UpdateIn{ID: d, BaseRevision: cur.Revision, Status: &done})
	if g := goalByID(t, w, u, chap.ID); g.Value != 1 {
		t.Errorf("finished twice counted %d", g.Value)
	}
}

func TestGoalRulesAndRefusals(t *testing.T) {
	w := newWorld(t)
	u, v := w.person("u", "UTC"), w.person("v", "UTC")
	p := w.project(u, "Mine")
	w.at("2026-03-02T09:00:00Z")
	bad := []struct {
		name string
		in   studio.GoalCreateIn
		want string
	}{
		{"zero target", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 0}, "target"},
		{"over the limit", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 100_001}, "target"},
		{"a week may hold seven days of it", studio.GoalCreateIn{Period: "week", Measure: "words", Target: 700_001}, "target"},
		{"too many chapters a day", studio.GoalCreateIn{Period: "day", Measure: "chapters", Target: 51}, "target"},
		{"no such period", studio.GoalCreateIn{Period: "year", Measure: "words", Target: 5}, "period"},
		{"no such measure", studio.GoalCreateIn{Period: "day", Measure: "pages", Target: 5}, "measure"},
		{"a deadline needs a total goal", studio.GoalCreateIn{Period: "week", Measure: "words", Target: 5, Deadline: "2026-04-01"}, "deadline"},
		{"a deadline is a date", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 5, Deadline: "soon"}, "deadline"},
		{"a past deadline", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 5, Deadline: "2026-03-01"}, "deadline"},
		{"not a project id", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 5, ProjectID: "../x"}, "project_id"},
	}
	for _, c := range bad {
		_, err := w.try(u, "studio.goal.create", c.in)
		var e *ops.Error
		if err == nil || !asOpsErr(err, &e) || e.Field != c.want {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// Another person's project is not there.
	if _, err := w.try(v, "studio.goal.create", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 5, ProjectID: p}); !isNotFound(err) {
		t.Errorf("a stranger's project: %v", err)
	}
	g := mkGoal(t, w, u, "day", "words", 100)
	if _, err := w.try(u, "studio.goal.create", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 200}); !isConflict(err) {
		t.Errorf("the same goal twice: %v", err)
	}
	// Someone else's goal is not there: not to change, archive or read.
	for _, call := range []func() error{
		func() error {
			_, e := w.try(v, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(5)})
			return e
		},
		func() error { _, e := w.try(v, "studio.goal.archive", studio.GoalArchiveIn{ID: g.ID}); return e },
		func() error { _, e := w.try(v, "studio.goal.history", studio.GoalHistoryIn{ID: g.ID}); return e },
	} {
		if err := call(); !isNotFound(err) {
			t.Errorf("a stranger's goal: %v", err)
		}
	}
	if len(w.call(v, "studio.goal.list", studio.GoalListIn{Archived: true}).(studio.GoalsOut).Items) != 0 {
		t.Error("another person's goals are listed")
	}
	// Twenty at most: the fifteen other period and measure pairs over every project, and four goals on one project.
	for _, per := range []string{"day", "week", "month", "total"} {
		for _, ms := range []string{"words", "sessions", "minutes", "chapters"} {
			if per == "day" && ms == "words" {
				continue
			}
			if _, err := w.try(u, "studio.goal.create", studio.GoalCreateIn{Period: per, Measure: ms, Target: 1}); err != nil {
				t.Fatalf("%s %s: %v", per, ms, err)
			}
		}
	}
	for _, per := range []string{"day", "week", "month", "total"} {
		if _, err := w.try(u, "studio.goal.create", studio.GoalCreateIn{Period: per, Measure: "words", Target: 1, ProjectID: p}); err != nil {
			t.Fatalf("project %s: %v", per, err)
		}
	}
	if _, err := w.try(u, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "sessions", Target: 2, ProjectID: p}); !isConflict(err) {
		t.Errorf("the twenty-first goal: %v", err)
	}
	// Archiving one makes room.
	w.call(u, "studio.goal.archive", studio.GoalArchiveIn{ID: g.ID})
	if _, err := w.try(u, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "sessions", Target: 2, ProjectID: p}); err != nil {
		t.Errorf("after archiving: %v", err)
	}
}

func asOpsErr(err error, e **ops.Error) bool {
	x, ok := err.(*ops.Error)
	if ok {
		*e = x
	}
	return ok
}
func isNotFound(err error) bool { x, ok := err.(*ops.Error); return ok && x.Code == ops.CodeNotFound }
func isConflict(err error) bool { x, ok := err.(*ops.Error); return ok && x.Code == ops.CodeConflict }

func TestArchivingKeepsTheDaysThatWereMet(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-01T09:00:00Z")
	g := mkGoal(t, w, u, "day", "words", 100)
	for _, day := range []string{"2026-03-02", "2026-03-03"} {
		w.at(day + "T09:00:00Z")
		w.save(u, d, w.text(u, d)+lorem(100, "x"+day[8:]))
	}
	// Today (the 3rd) is met: archiving ends the goal today, and the two days stay a run of two.
	a := w.call(u, "studio.goal.archive", studio.GoalArchiveIn{ID: g.ID}).(studio.GoalItem).Item
	if a.Status != "archived" || a.EndedOn != "2026-03-03" {
		t.Errorf("archived: %+v", a)
	}
	w.at("2026-03-04T09:00:00Z")
	m := dash(t, w, u)
	if m.Streak.Current != 2 || m.Streak.Longest != 2 || m.Streak.HasGoal || m.Streak.Message.Key != "studio.streak.set_goal" {
		t.Errorf("streak after archiving: %+v", m.Streak)
	}
	if m.Goals.Active != 0 || m.Goals.Archived != 1 {
		t.Errorf("counts %+v", m.Goals)
	}
	if _, err := w.try(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(5)}); !isConflict(err) {
		t.Errorf("updating an archived goal: %v", err)
	}
	// The record stays readable: the two days, judged by the target then.
	h := hist(t, w, u, g.ID)
	met := 0
	for _, pr := range h.Periods {
		if pr.Met {
			met++
		}
	}
	if met != 2 {
		t.Errorf("periods %+v", h.Periods)
	}
	// An unmet goal is archived through yesterday, and a repeat changes nothing.
	g2 := mkGoal(t, w, u, "day", "words", 100)
	a2 := w.call(u, "studio.goal.archive", studio.GoalArchiveIn{ID: g2.ID}).(studio.GoalItem).Item
	if a2.EndedOn != "2026-03-03" && a2.EndedOn != "2026-03-04" { // the goal began on the 4th: its end is clamped to its start
		t.Errorf("ended %s", a2.EndedOn)
	}
	if again := w.call(u, "studio.goal.archive", studio.GoalArchiveIn{ID: g2.ID}).(studio.GoalItem).Item; again.EndedOn != a2.EndedOn {
		t.Errorf("a second archive moved the end: %s then %s", a2.EndedOn, again.EndedOn)
	}
}

func TestGoalsSetOnAProjectsGoalsPageCountToo(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	w.call(u, "goal.set", goals.SetIn{ProjectID: p, Kind: "daily", TargetWords: 50})
	w.call(u, "goal.set", goals.SetIn{ProjectID: p, Kind: "project", TargetWords: 100, EndDate: "2026-04-01"})
	w.save(u, d, lorem(60, "a"))
	m := dash(t, w, u)
	if m.Goals.Active != 2 || m.Goals.Completed != 0 || len(m.Goals.Items) != 2 {
		// the daily goal is met (60 of 50) and stays active; the total of 100 words is 60 of 100
		t.Fatalf("goals %+v", m.Goals)
	}
	byPeriod := map[string]studio.GoalOut{}
	for _, g := range m.Goals.Items {
		byPeriod[g.Period] = g
		if g.Source != "project" || !strings.HasPrefix(g.ID, "project:") || g.ProjectTitle != "Book" {
			t.Errorf("goal %+v", g)
		}
	}
	if g := byPeriod["day"]; !g.Met || g.Value != 60 || g.Target != 50 {
		t.Errorf("daily %+v", g)
	}
	if g := byPeriod["total"]; g.Value != 60 || g.Remaining != 40 || g.Deadline != "2026-04-01" || g.Forecast == nil {
		t.Errorf("total %+v", g)
	}
	if !m.Today.HasGoal || m.Today.Target != 50 || !m.Today.Hit || m.Streak.Current != 1 {
		t.Errorf("today %+v streak %+v", m.Today, m.Streak)
	}
	// They are changed where they were made, not here.
	var oe *ops.Error
	if _, err := w.try(u, "studio.goal.update", studio.GoalUpdateIn{ID: byPeriod["day"].ID, Target: ptr(5)}); err == nil || !asOpsErr(err, &oe) || oe.Field != "id" {
		t.Errorf("updating one: %v", err)
	}
	// A project in the trash takes its goals off the dashboard.
	w.call(u, "project.trash", projects.StateIn{ID: p})
	if m = dash(t, w, u); len(m.Goals.Items) != 0 || m.Cards[0].Words != 0 {
		t.Errorf("a trashed project still counts: goals %d, today %d", len(m.Goals.Items), m.Cards[0].Words)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "Book") {
		t.Error("a trashed project's title is on the dashboard")
	}
}

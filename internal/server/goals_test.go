package server

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/ops"
)

// A fixture for the template lint and the render tests: some history, a streak, a target.
func goalsFixture(editor, owner bool) goalsData {
	pr := ownerFixture(fixtureProjects()[0])
	if !owner {
		pr.Role = ops.ProjectEditor
	}
	if !editor {
		pr.Role = ops.ProjectReader
	}
	days := make([]goals.DayStat, 0, 30)
	for i := 0; i < 30; i++ {
		d := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		ds := goals.DayStat{Day: d.Format("2006-01-02")}
		if i%3 != 1 {
			ds.WordsAdded, ds.WordsNet, ds.Target = 200+i*40, 150+i*30, 500
			ds.Hit = ds.WordsAdded >= 500
		}
		days = append(days, ds)
	}
	h := goals.HistoryOut{From: days[0].Day, To: days[29].Day, Days: days, WritingDays: 20, TotalAdded: 9000}
	today := goals.TodayOut{Day: "2026-11-12", Timezone: "Europe/Dublin", WordsAdded: 640, WordsNet: 410, Minutes: 38, HasGoal: true, Target: 500, Hit: true, Streak: 3, LongestStreak: 9}
	d := goalsData{Project: pr, Slug: pr.Slug, CSRF: "tok_writer", CanEdit: editor, IsOwner: owner && editor, Today: today, TodayCap: 500,
		Daily: &goals.Goal{Kind: "daily", TargetWords: 500}, Own: &goals.Goal{Kind: "project", TargetWords: 80000, EndDate: "2027-06-30"},
		SharedWords: 90000, SharedDeadline: "2027-08-01", Days: 30, Others: []int{90}, Hist: h, Summary: "In the last 30 days you added 9,000 words on 20 days.",
		Weeks: goalCalendar(h, today.Day), TodayLabel: "12 November 2026", Timezone: "Europe/Dublin", OwnEndLabel: "30 June 2027", DeadlineLabel: "30 June 2027", Percent: 68}
	d.Proj = goals.ProjectOut{Words: 54300, Target: 80000, TargetSource: "goal", Remaining: 25700, Percent: 68, Deadline: "2027-06-30", DaysLeft: 230, RequiredPerDay: 112,
		PaceWindowDays: 28, WritingDays: 17, AvgNetPerDay: 320, ProjectedDate: "2027-02-01"}
	d.Pace = goalPace{Needed: 112, DaysLeft: 230, Projected: "1 February 2027", Average: 320, HasAverage: true}
	return d
}

func TestGoalLevels(t *testing.T) {
	cases := []struct{ words, target, peak, want int }{
		{0, 500, 900, 0}, {10, 500, 900, 1}, {130, 500, 900, 2}, {260, 500, 900, 3}, {500, 500, 900, 4}, {2000, 500, 900, 4},
		{10, 0, 900, 1}, {900, 0, 900, 4}, {450, 0, 900, 2}, {300, 0, 0, 1},
	}
	for _, c := range cases {
		if got := goalLevel(c.words, c.target, c.peak); got != c.want {
			t.Errorf("goalLevel(%d, %d, %d) = %d, want %d", c.words, c.target, c.peak, got, c.want)
		}
	}
}

func TestGoalCalendarStartsOnTheRightWeekday(t *testing.T) {
	// 2026-11-12 is a Thursday: three blank cells before it, a row per week, padded at the end.
	var days []goals.DayStat
	for i := 0; i < 10; i++ {
		days = append(days, goals.DayStat{Day: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format("2006-01-02")})
	}
	days[0].WordsAdded, days[0].Hit = 600, true
	weeks := goalCalendar(goals.HistoryOut{Days: days}, "2026-11-21")
	if len(weeks) != 2 {
		t.Fatalf("%d weeks, want 2", len(weeks))
	}
	for i := 0; i < 3; i++ {
		if !weeks[0][i].Blank {
			t.Errorf("cell %d of the first week should be blank", i)
		}
	}
	if c := weeks[0][3]; c.Blank || c.Num != 12 || !c.Hit || !strings.Contains(c.Label, "600 words added, goal met") || !strings.Contains(c.Label, "Thu 12 Nov") {
		t.Errorf("the first day: %+v", c)
	}
	if c := weeks[1][5]; !c.Today || !strings.HasSuffix(c.Label, "today") {
		t.Errorf("today: %+v", c)
	}
	if !weeks[1][6].Blank {
		t.Error("the last week is padded")
	}
}

func (e writeEnv) goalsPageBody(t *testing.T, c caller, query string) (int, string) {
	t.Helper()
	rec := e.s.do(c, "GET", "/p/"+e.slug+"/goals"+query, "")
	return rec.Code, rec.Body.String()
}

func TestGoalsPage(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	// With nothing set the page invites a first target and shows no streak.
	code, body := e.goalsPageBody(t, e.owner, "")
	if code != http.StatusOK {
		t.Fatalf("goals page: %d", code)
	}
	for _, want := range []string{"No daily target yet", "Needs a daily target", "No target yet", `name="daily_words"`, `name="own_words"`, `name="shared_words"`, "never counts against you", `aria-current="page">Goals`} {
		if !strings.Contains(body, want) {
			t.Errorf("empty goals page lacks %q", want)
		}
	}
	// Setting the daily target, a project target and the shared target goes through the ops.
	for _, f := range []url.Values{
		{"action": {"daily"}, "daily_words": {"500"}},
		{"action": {"own"}, "own_words": {"80,000"}, "own_end": {"2030-06-30"}},
		{"action": {"shared"}, "shared_words": {"90000"}, "shared_end": {"2030-08-01"}},
	} {
		if rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", f, "", false); rec.Code != http.StatusSeeOther {
			t.Fatalf("%v: %d %s", f, rec.Code, rec.Body)
		}
	}
	_, body = e.goalsPageBody(t, e.owner, "?saved=daily")
	for _, want := range []string{"Daily target saved.", `value="500"`, `value="80000"`, "30 June 2030", "target you set for yourself", "Remove"} {
		if !strings.Contains(body, want) {
			t.Errorf("goals page after saving lacks %q", want)
		}
	}
	// Words typed today appear as words added, and meet the target.
	e.call(t, "doc.update", docs.UpdateIn{ID: e.chap, BaseRevision: 1, Content: goalPtr(strings.Repeat("word ", 520))})
	_, body = e.goalsPageBody(t, e.owner, "")
	if !strings.Contains(body, "520</strong> words added") || !strings.Contains(body, "Daily target of 500 met.") {
		t.Errorf("today's words are not shown as met: %s", between(body, `id="today"`, `id="daily"`))
	}
	if !strings.Contains(body, "1 day") {
		t.Error("the streak after one met day")
	}
	// The history is a table with one text cell per day, and 90 days is a link away.
	if n := strings.Count(body, `class="day `); n != 30 {
		t.Errorf("%d day cells for 30 days", n)
	}
	if !strings.Contains(body, "goal met, today") {
		t.Error("today's cell does not say it met the goal")
	}
	_, body = e.goalsPageBody(t, e.owner, "?days=90")
	if n := strings.Count(body, `class="day `); n != 90 {
		t.Errorf("%d day cells for 90 days", n)
	}
	// Removing a goal.
	if rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily_remove"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("remove: %d", rec.Code)
	}
	_, body = e.goalsPageBody(t, e.owner, "")
	// Today stays met (a finished day stays finished), but the target itself is gone from the form.
	if strings.Contains(body, `value="daily_remove"`) || strings.Contains(body, `name="daily_words" type="number" inputmode="numeric" min="1" max="100000" step="1" value="500"`) {
		t.Error("the daily target is still offered after Remove")
	}
	// Plain-words errors keep what was typed and the right field marked.
	rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily"}, "daily_words": {"lots"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Enter a whole number of words") || !strings.Contains(rec.Body.String(), `value="lots"`) || !strings.Contains(rec.Body.String(), `aria-invalid="true"`) {
		t.Errorf("a bad number: %d %s", rec.Code, between(rec.Body.String(), `id="goals-error"`, "</p>"))
	}
	rec = s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"own"}, "own_words": {"1000"}, "own_end": {"next week"}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "YYYY-MM-DD") {
		t.Errorf("a bad date: %d", rec.Code)
	}
	rec = s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily"}, "daily_words": {""}}, "", false)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "Enter a number of words") {
		t.Errorf("an empty target: %d", rec.Code)
	}
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return "(" + a + " not found)"
	}
	s = s[i:]
	if j := strings.Index(s[len(a):], b); j >= 0 {
		return s[:len(a)+j+len(b)]
	}
	return s
}

func goalPtr[T any](v T) *T { return &v }

func TestGoalsAreAPersonsOwn(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	ctx := context.Background()
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	s.addMember(t, e.pid, editor.userID, ops.ProjectEditor)
	reader := s.newCaller(t, "reader", ops.RoleWriter)
	s.addMember(t, e.pid, reader.userID, ops.ProjectReader)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	anon := caller{name: "anon"}

	if rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily"}, "daily_words": {"1234"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	// The editor does not see the owner's daily target, streak or words.
	rec := s.do(editor, "GET", "/p/"+e.slug+"/goals", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "1234") || !strings.Contains(rec.Body.String(), "No daily target yet") {
		t.Errorf("another member's daily target leaked: %d", rec.Code)
	}
	// An editor sets their own project target but is offered no shared one, and cannot post it.
	if strings.Contains(rec.Body.String(), `name="shared_words"`) {
		t.Error("an editor is offered the shared target")
	}
	if rec := s.post(t, editor, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"shared"}, "shared_words": {"5"}}, "", false); rec.Code != http.StatusForbidden {
		t.Errorf("an editor setting the shared target: %d", rec.Code)
	}
	if rec := s.post(t, editor, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"own"}, "own_words": {"4000"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Errorf("an editor setting their own target: %d", rec.Code)
	}
	// A reader sees the numbers but no forms, and cannot post.
	rec = s.do(reader, "GET", "/p/"+e.slug+"/goals", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<form") && strings.Contains(rec.Body.String(), `name="daily_words"`) || !strings.Contains(rec.Body.String(), "Only people who can edit") {
		t.Errorf("reader page: %d", rec.Code)
	}
	if rec := s.post(t, reader, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily"}, "daily_words": {"10"}}, "", false); rec.Code != http.StatusForbidden {
		t.Errorf("a reader setting a goal: %d", rec.Code)
	}
	// A stranger gets the same 404 as a project that is not there; anonymous is sent to sign in.
	if rec := s.do(stranger, "GET", "/p/"+e.slug+"/goals", ""); rec.Code != http.StatusNotFound {
		t.Errorf("stranger: %d", rec.Code)
	}
	if rec := s.post(t, stranger, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily"}, "daily_words": {"10"}}, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("stranger post: %d", rec.Code)
	}
	if rec := s.do(anon, "GET", "/p/"+e.slug+"/goals", ""); rec.Code != http.StatusSeeOther {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := s.post(t, e.owner, "/ui/projects/"+e.pid+"/goals", url.Values{"action": {"daily"}, "daily_words": {"10"}}, "-", false); rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF token: %d", rec.Code)
	}
	// Another project's id in the path is another project's: no leak through the goals route.
	if rec := s.post(t, editor, "/ui/projects/"+e.pidOth+"/goals", url.Values{"action": {"daily"}, "daily_words": {"10"}}, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("a project the person is not in: %d", rec.Code)
	}
	_ = ctx
}

// The time zone of a day is the person's: a goal saved just before local midnight is dated by the profile's zone.
func TestGoalsPageDatesFollowTheProfileZone(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	if rec := s.post(t, e.owner, "/ui/settings/profile", url.Values{"display_name": {"Wren"}, "timezone": {"Pacific/Auckland"}, "locale": {"en"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatal(rec.Code)
	}
	_, body := e.goalsPageBody(t, e.owner, "")
	auck, _ := time.LoadLocation("Pacific/Auckland")
	if want := time.Now().In(auck).Format("2 January 2006"); !strings.Contains(body, want) {
		t.Errorf("today's date is not the profile's (%s)", want)
	}
	if !strings.Contains(body, "Pacific/Auckland") {
		t.Error("the time zone is not named")
	}
}

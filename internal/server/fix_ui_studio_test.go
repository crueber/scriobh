package server

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/studio"
)

// The fix round for the Writing Studio screens, tested on the markup the templates make from a dashboard in each state.

func stDashPage(t *testing.T, s *Server, o studio.DashboardOut) string {
	t.Helper()
	d := studioData{stBase: stFixtureBase("studio")}
	d.fill(o, stFixtureProjects(), nil)
	return renderPage(t, s, fixture{"studio", p5With("Writing studio", "/studio", d)})
}

// A run that has lapsed is not drawn as a large 0: the dashboard says what the past month holds and what starts a run, and the
// longest run only when there was one.
func TestADashboardOfALapsedRunDrawsNoZero(t *testing.T) {
	s := newWebServer(t)
	o := stFixtureDashboard(false)
	o.Streak = studio.StreakOut{HasGoal: true, Current: 0, Longest: 4, TodayWords: 0, TodayTarget: 100, Measure: "words", TodayValue: 0, Message: studio.Msg{Key: "studio.streak.begin", Params: map[string]any{"n": 100}}}
	o.Today = studio.TodayOut{HasGoal: true, Target: 100, Remaining: 100, Message: &studio.Msg{Key: "studio.today.to_go", Params: map[string]any{"n": 100}}}
	body := stDashPage(t, s, o)
	if strings.Contains(body, "data-streak") || strings.Contains(body, "days in a row</span>") {
		t.Error("the dashboard draws the run, which is 0")
	}
	if !strings.Contains(body, "data-days30") || !strings.Contains(body, "A new run starts with today&#39;s 100 words") {
		t.Errorf("the lapsed run is not said in the past month and what starts a new one")
	}
	o.Streak.Longest = 0
	if strings.Contains(stDashPage(t, s, o), "data-longest") {
		t.Error("a longest run of 0 is said")
	}
}

// A person whose only daily goal counts sessions is told of sessions, with a meter of its own measure.
func TestADashboardOfASessionsGoalSpeaksOfSessions(t *testing.T) {
	s := newWebServer(t)
	o := stFixtureDashboard(false)
	o.Today = studio.TodayOut{Words: 60}
	o.Streak = studio.StreakOut{HasGoal: true, TodayTarget: 2, TodayValue: 1, Measure: "sessions", Message: studio.Msg{Key: "studio.streak.begin_goal"}}
	body := stDashPage(t, s, o)
	if !strings.Contains(body, "Today, in sessions: 1 / 2") || !strings.Contains(body, `<progress class="st-meter" value="1" max="2"`) || !strings.Contains(body, "A new run starts when today&#39;s goal is met") {
		t.Errorf("the streak card of a sessions goal: %s", body[strings.Index(body, `id="streak"`):][:900])
	}
	if strings.Contains(body, "words today") && strings.Contains(body[strings.Index(body, `id="streak"`):][:900], "words today") {
		t.Error("a sessions goal is worded as words")
	}
}

// A goal that belongs to one project names the project beside its words, so words, target and the goal under it agree.
func TestTheStreakFigureNamesTheProjectOfAScopedGoal(t *testing.T) {
	s := newWebServer(t)
	o := stFixtureDashboard(false)
	o.Today.ProjectID = "prj_a"
	o.Streak.TodayValue = 60
	if body := stDashPage(t, s, o); !strings.Contains(body, "60 / 100 words today in The Salt Road") {
		t.Error("the figure does not name the goal's project")
	}
}

// The status bar's sprint line keeps its place: the unseen sizers hold the width of the longest thing it can say.
func TestTheSprintLineHoldsItsWidthWithSizers(t *testing.T) {
	sz := strings.Join(stSprintSizers(stT{"en"}), "|")
	if !strings.Contains(sz, "Time is up. Finish your sentence") || !strings.Contains(sz, "240 minutes left") || !strings.Contains(sz, "Session timer: 240 minutes") {
		t.Errorf("sizers %v", sz)
	}
}

// One minus sign, the real one, in every figure the screens write; a hyphen is not one.
func TestTheStudioWritesNegativesWithTheRealMinus(t *testing.T) {
	if got := stNum(-1234); got != "−1,234" {
		t.Errorf("stNum(-1234) = %q", got)
	}
	if got := (stT{"en"}).T("studio.ui.goal.net", "n", -20); strings.Contains(got, "-") || !strings.Contains(got, "−20") {
		t.Errorf("the goal's net line: %q", got)
	}
}

// Compare, through the page: periods of unequal length have no difference worked out, and the page says why.
func TestCompareOfSidesOfUnequalLengthWorksOutNoDifference(t *testing.T) {
	w := newShareWorld(t, nil)
	body := w.s.do(w.owner, "GET", "/studio/data?a_from=2026-03-01&a_to=2026-03-07&b_from=2026-02-01&b_to=2026-02-03", "").Body.String()
	i := strings.Index(body, `id="st-compare"`)
	if i < 0 {
		t.Fatal("no compare table")
	}
	table := body[i:]
	table = table[:strings.Index(table, "</table>")]
	if strings.Count(table, "\u2014") != 5 || strings.Contains(table, "+") {
		t.Errorf("a difference between 7 days and 3: %s", table)
	}
	if !strings.Contains(body, "The periods are 7 and 3 days long") {
		t.Error("the page does not say why there is no difference")
	}
	// the presets say what their sides are: finished days, the same number on each side
	restore := db.SetClock(func() time.Time { return time.Date(2026, 3, 18, 12, 0, 0, 0, time.UTC) }) // a Wednesday
	defer restore()
	body = w.s.do(w.owner, "GET", "/studio/data?preset=this_week", "").Body.String()
	if !strings.Contains(body, ", so far</th>") || !strings.Contains(body, ", the same days</th>") || !strings.Contains(body, "The first 2 finished days") {
		t.Error("the preset's heads and note do not say that the sides are the same finished days")
	}
	// a Monday has no finished day: the week so far in one column, no difference, and the comparison starts tomorrow
	restore = db.SetClock(func() time.Time { return time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC) })
	defer restore()
	body = w.s.do(w.owner, "GET", "/studio/data?preset=this_week", "").Body.String()
	i = strings.Index(body, `id="st-compare"`)
	table = body[i:]
	table = table[:strings.Index(table, "</table>")]
	if strings.Contains(table, "\u2212") || strings.Contains(table, "Difference") || strings.Count(table, "<th scope=\"col\">") != 1 || !strings.Contains(body, "The comparison starts tomorrow") {
		t.Errorf("a Monday's compare: %s", table)
	}
}

package server

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/i18n"
)

// Tests written by the independent review of the Writing Studio screens (ui-75-studio). Each one fails on the branch as it
// stands and says why; the review's findings give the same cases in prose.

// F1: a card must not set a day that is not over against the day before it. The branch drops the percentage for that reason and
// keeps the sentence "N fewer than yesterday", which says the same thing at breakfast.
func TestReviewTodayCardDoesNotSetAnUnfinishedDayAgainstYesterday(t *testing.T) {
	c := studio.Card{Key: "today", Figures: studio.Figures{Words: 50, Net: 50}, Previous: studio.Figures{Words: 350, Net: 350}, Delta: studio.Delta{Change: -300}, Spark: []int{0, 0, 0, 0, 0, 0, 350}}
	got := stCardOf(stT{"en"}, c)
	if strings.Contains(strings.ToLower(got.Delta), "fewer") || strings.Contains(got.Delta, "−") {
		t.Errorf("today's card says %q: a day that is not over is measured against a whole one", got.Delta)
	}
}

// F2: after a quiet day, the card must not name the quiet day. The comparison is left out when there was nothing before.
func TestReviewACardDoesNotNameAnEmptyPreviousPeriod(t *testing.T) {
	for _, key := range []string{"today", "7d", "30d"} {
		c := studio.Card{Key: key, Figures: studio.Figures{Words: 150, Net: 150, WritingDays: 1}, Previous: studio.Figures{}, Delta: studio.Delta{Change: 150}, Spark: []int{0, 0, 0, 0, 0, 0, 150}}
		got := stCardOf(stT{"en"}, c)
		if strings.Contains(got.Delta, "Nothing was written") {
			t.Errorf("%s card says %q to someone who has just written", key, got.Delta)
		}
	}
}

// F3: a streak that has lapsed is not drawn as the card's hero number (a large 0 to hand to a friend).
func TestReviewALapsedStreakIsNotTheHeroOfTheCard(t *testing.T) {
	tr := func(key string, args ...any) string { return i18n.Default.T("en", key, args...) }
	svg := string(studio.CardSVG(studio.CardView{Day: "2026-11-16", Streak: 0, Longest: 4, WritingDays30: 17, HasGoal: true, GoalsActive: 1}, tr))
	if regexp.MustCompile(`font-size="64"[^>]*>0</text>`).MatchString(svg) {
		t.Error("the card's hero numeral is 0 days in a row")
	}
}

// F4: the default Compare view must not set one day against a whole week. On a Monday "This week and last" is 1 day against 7
// and every row is a large negative. Written on a Tuesday, read on the next Monday.
func TestReviewCompareThisWeekDoesNotSetOneDayAgainstSeven(t *testing.T) {
	w := newShareWorld(t, nil)
	s := w.s
	defer db.SetClock(func() time.Time { return time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC) })()
	d := s.do(w.owner, "GET", "/api/v1/docs/"+w.doc2, "").Body.String()
	rev := regexp.MustCompile(`"revision":(\d+)`).FindStringSubmatch(d)
	if rec := s.do(w.owner, "PATCH", "/api/v1/docs/"+w.doc2, `{"base_revision":`+rev[1]+`,"content":"`+strings.Repeat("lantern ", 400)+`"}`); rec.Code != 200 {
		t.Fatalf("writing: %d %s", rec.Code, rec.Body)
	}
	restore := db.SetClock(func() time.Time { return time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC) }) // the Monday after
	defer restore()
	rec := s.do(w.owner, "GET", "/studio/data?preset=this_week", "")
	body := rec.Body.String()
	i := strings.Index(body, `id="st-compare"`)
	if rec.Code != 200 || i < 0 {
		t.Fatalf("compare page: %d", rec.Code)
	}
	table := body[i:]
	table = table[:strings.Index(table, "</table>")]
	if strings.Contains(table, "−") {
		t.Errorf("a Monday's compare table shows a loss for a period of one day against seven: %s", regexp.MustCompile(`<[^>]+>`).ReplaceAllString(table, " "))
	}
}

// F5: a forecast must not be a count of days "after the deadline" on the dashboard (the key is named for what the screens must not say).
func TestReviewForecastDoesNotCountDaysAfterTheDeadline(t *testing.T) {
	margin := -341
	g := studio.GoalOut{ID: "sgl_1", Source: "studio", Period: "total", Measure: "words", Target: 60000, Value: 300, Remaining: 59700, Status: "active",
		Forecast: &studio.Forecast{Estimate: true, MarginDays: &margin, Notes: []studio.Msg{{Key: "studio.forecast.behind", Params: map[string]any{"n": 341}}}}}
	got := stGoalOf(stT{"en"}, g)
	for _, n := range got.Notes {
		if strings.Contains(strings.ToLower(n), "after the deadline") {
			t.Errorf("the goal says %q", n)
		}
	}
}

// F6: the calendar's key says a larger square is "darker", which is false in the dark themes (the fullest step is the lightest).
func TestReviewCalendarKeyIsTrueInTheDarkThemes(t *testing.T) {
	if s := i18n.Default.Raw("en", "studio.ui.cal.key"); strings.Contains(s, "darker") {
		t.Errorf("studio.ui.cal.key says %q", s)
	}
}

// F7: a person whose only daily goal counts sessions is told nothing about words ("A new run starts with today's 0 words") and
// is not given a meter with a maximum of 0.
func TestReviewASessionsOnlyDailyGoalIsNotDescribedInWords(t *testing.T) {
	w := newShareWorld(t, nil)
	s := w.s
	stLocation(t, s, w.owner, "/ui/studio/goals", url.Values{"period": {"day"}, "measure": {"sessions"}, "target": {"1"}})
	body := s.do(w.owner, "GET", "/studio", "").Body.String()
	if strings.Contains(body, `max="0"`) || strings.Contains(body, "today&#39;s 0 words") || strings.Contains(body, "today's 0 words") {
		t.Error("the streak card shows a meter with max 0, or a run that starts with 0 words")
	}
}

// F8: the streak card's figure must agree with itself. With one daily goal, of 200 words, belonging to a project that has no
// words today, and 500 words written in another project, the card shows "500 / 200 words today" over a full meter and, in the
// same card, "200 words to go today" and "Write 200 words today"; the goals panel under it reads 0 / 200. Either the
// words are the goal's own (0 of 200) or the goal is met. (Backend: studio.dashboard's today.)
func TestReviewTheStreakCardAgreesWithItself(t *testing.T) {
	w := newShareWorld(t, nil)
	s := w.s
	other, err := s.Reg.Call(t.Context(), w.ownerP, "project.create", projects.CreateIn{Title: "Elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	pid := other.(projects.Item).Item.ID
	docs := s.do(w.owner, "GET", "/api/v1/projects/"+pid+"/docs", "").Body.String()
	doc := regexp.MustCompile(`"order":\["([^"]+)"`).FindStringSubmatch(docs)
	if doc == nil {
		t.Fatalf("no first document: %s", docs)
	}
	stLocation(t, s, w.owner, "/ui/studio/goals", url.Values{"period": {"day"}, "measure": {"words"}, "target": {"200"}, "project_id": {w.pid}})
	d := s.do(w.owner, "GET", "/api/v1/docs/"+doc[1], "").Body.String()
	rev := regexp.MustCompile(`"revision":(\d+)`).FindStringSubmatch(d)
	if rec := s.do(w.owner, "PATCH", "/api/v1/docs/"+doc[1], `{"base_revision":`+rev[1]+`,"content":"`+strings.Repeat("lantern ", 500)+`"}`); rec.Code != 200 {
		t.Fatalf("writing: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Today struct {
			Words     int  `json:"words"`
			Target    int  `json:"target"`
			Remaining int  `json:"remaining"`
			Hit       bool `json:"hit"`
		} `json:"today"`
		Goals struct {
			Items []struct{ Value int } `json:"items"`
		} `json:"goals"`
	}
	if err := json.Unmarshal(s.do(w.owner, "GET", "/api/v1/me/studio", "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Goals.Items) != 1 || out.Goals.Items[0].Value != 0 {
		t.Fatalf("the control: the goal's own project has no words today: %+v", out.Goals.Items)
	}
	if (out.Today.Words >= out.Today.Target) != (out.Today.Remaining == 0) || (out.Today.Words >= out.Today.Target) != out.Today.Hit {
		t.Errorf("today reads %d of %d words, %d to go, met=%v", out.Today.Words, out.Today.Target, out.Today.Remaining, out.Today.Hit)
	}
}

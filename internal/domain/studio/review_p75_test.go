package studio_test

import (
	"context"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/ops"
)

// Review finding R2, decided: a sprint's words are the words recorded while it ran, so a sprint that runs past local midnight
// holds the words of two days and is not a subset of one. It reconciles with the sum of the days it touches over the time it
// ran, no day holds a word twice, and it counts on the day it began.
func TestRvp75ASprintAcrossMidnightIsLargerThanItsDay(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T23:50:00Z")
	startSprint(t, w, u, p, 25, 0)
	w.at("2026-03-02T23:55:00Z")
	w.save(u, d, lorem(30, "a"))
	w.at("2026-03-03T00:05:00Z")
	w.save(u, d, w.text(u, d)+lorem(70, "b"))
	w.call(u, "studio.sprint.stop", studio.SprintEndIn{})
	st := w.call(u, "studio.sprint.stats", studio.SprintStatsIn{Range: "all"}).(studio.SprintStatsOut)
	if st.Best == nil || st.Sprints != 1 {
		t.Fatal("no sprint")
	}
	if st.Best.Day != "2026-03-02" {
		t.Errorf("the sprint counts on the day it began: %s", st.Best.Day)
	}
	pr := w.call(u, "studio.project.totals", studio.ProjectTotalsIn{ProjectID: p, Days: 3}).(studio.ProjectTotalsOut)
	days := map[string]int{}
	for _, r := range pr.Days {
		days[r.Day] = r.Words
	}
	// The days hold 30 and 70, each word once; the sprint holds the 100 recorded while it ran: the sum of the days it touched.
	if days["2026-03-02"] != 30 || days["2026-03-03"] != 70 {
		t.Errorf("the days: %v", days)
	}
	if st.Best.WordsAdded != days["2026-03-02"]+days["2026-03-03"] || st.Words != 100 {
		t.Errorf("the sprint holds %d words, the days it touched %d", st.Best.WordsAdded, days["2026-03-02"]+days["2026-03-03"])
	}
	if total := w.call(u, "studio.series", studio.SeriesIn{From: "2026-03-02", To: "2026-03-03"}).(studio.SeriesOut).Figures.Words; total != 100 {
		t.Errorf("the days together hold %d words: a word was counted twice", total)
	}
}

// Review finding R3: addToSprint runs inside the savepoint of goals.RecordSave, so a fault in studio_sprints rolls back the
// day's credit and the writing session of the save as well. The save succeeds; the day is lost.
func TestRvp75ASprintTableFaultLosesTheDaysCredit(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	if _, err := w.d.Writer().Exec(`ALTER TABLE studio_sprints RENAME TO studio_sprints_broken`); err != nil {
		t.Fatal(err)
	}
	w.save(u, d, lorem(80, "a"))
	var n int
	_ = w.d.Read().QueryRow(`SELECT COALESCE(SUM(words_added),0) FROM daily_stats WHERE user_id = ?`, u.id).Scan(&n)
	if n != 80 {
		t.Errorf("the save went through but the day holds %d of 80 words: a fault in the sprint table cost the day's statistics", n)
	}
}

// Review finding R4: a total goal in words is judged on the manuscript's size, so a cut can turn a completed goal back to
// active (net words deciding a met goal; the goal's card count falls with it).
func TestRvp75ATotalGoalUncompletesAfterACut(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	w.call(u, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 100, ProjectID: p})
	w.save(u, d, lorem(120, "a"))
	if g := w.call(u, "studio.goal.list", studio.GoalListIn{}).(studio.GoalsOut); g.Completed != 1 {
		t.Fatalf("not completed: %+v", g)
	}
	w.at("2026-03-03T09:00:00Z")
	w.save(u, d, lorem(60, "a")) // tightening: cut 60 words
	if g := w.call(u, "studio.goal.list", studio.GoalListIn{}).(studio.GoalsOut); g.Completed != 1 {
		t.Errorf("a cut un-completed the goal: %+v", g)
	}
}

var _ = ops.RoleWriter

// Review R1b: Hidden counts only passphrase-locked projects, so a trashed project's words still reach a day's judgement
// through progress_at for the card and the dashboard: trashing it changes the words but a raised goal still counts them.
func TestRvp75ATrashedProjectsWordsStillJudgeADay(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	pA := w.project(u, "Open")
	pB := w.project(u, "Trashed")
	dA, dB := w.doc(u, pA), w.doc(u, pB)
	w.at("2026-03-02T09:00:00Z")
	g := mkGoal(t, w, u, "day", "words", 100)
	w.save(u, dA, lorem(60, "a"))
	w.save(u, dB, lorem(60, "b"))
	w.call(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(200)})
	w.call(u, "project.trash", projects.StateIn{ID: pB})
	card, err := studio.NoSessionViewer(context.Background(), w.d.Read(), u.id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := card.Report("2026-03-02", "2026-03-02")
	if err != nil {
		t.Fatal(err)
	}
	// words visible: 60 against a target of 200 now, 100 before: only the trashed project's 60 words made it a goal day
	if r.Words != 60 || r.GoalDaysMet != 0 {
		t.Errorf("visible words %d, goal days met %d: the trashed project's words still count the day as met", r.Words, r.GoalDaysMet)
	}
}

// Review R7: a restore closes a running sprint with ended_at = the moment of the restore, so a sprint that was running when the
// backup was taken is reported as lasting until then (days, weeks), inflating the sprint minutes and the best sprint's minutes.
func TestRvp75ARestoreClosedSprintDoesNotLastUntilTheRestore(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	startSprint(t, w, u, p, 25, 0)
	w.at("2026-03-02T09:10:00Z")
	w.save(u, d, lorem(40, "a"))
	w.at("2026-03-12T09:00:00Z") // the backup is restored ten days later
	if n, err := studio.CloseOpenSprints(context.Background(), w.d); err != nil || n != 1 {
		t.Fatalf("closed %d: %v", n, err)
	}
	st := w.call(u, "studio.sprint.stats", studio.SprintStatsIn{Range: "all"}).(studio.SprintStatsOut)
	if st.Sprints != 1 || st.Minutes > 27 {
		t.Errorf("a 25 minute sprint is reported as %d minutes (best %+v)", st.Minutes, st.Best)
	}
}

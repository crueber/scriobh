package studio_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/studio"
)

// A total goal that was reached stays completed with the date it was first seen reached: a cut afterwards, even to nothing,
// never takes it back, and the card's count of completed goals does not fall. Only the person raising the target makes it
// active again.
func TestACompletedTotalGoalStaysCompletedAfterACut(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "Book")
	d := w.doc(u, p)
	w.at("2026-03-02T09:00:00Z")
	g := w.call(u, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 100, ProjectID: p}).(studio.GoalItem).Item
	card := w.call(u, "studio.card.create", studio.CardIn{}).(studio.CardItem).Item
	w.save(u, d, lorem(120, "a"))
	got := goalByID(t, w, u, g.ID)
	if got.Status != "completed" || got.CompletedOn != "2026-03-02" {
		t.Fatalf("reached: %+v", got)
	}
	// Three days on, the manuscript is cut to nothing.
	w.at("2026-03-05T09:00:00Z")
	w.save(u, d, "")
	got = goalByID(t, w, u, g.ID)
	if got.Status != "completed" || !got.Met || got.CompletedOn != "2026-03-02" || got.Value != 0 {
		t.Errorf("after the cut: %+v", got)
	}
	if list := w.call(u, "studio.goal.list", studio.GoalListIn{}).(studio.GoalsOut); list.Completed != 1 || list.Active != 0 {
		t.Errorf("counts %+v", list)
	}
	if v, _ := w.cards.Open(t.Context(), card.Token, false); v.GoalsDone != 1 {
		t.Errorf("the card's completed goals: %d", v.GoalsDone)
	}
	// Raising the target is a new aim: active again, and a later cut does not complete or un-complete anything it did not reach.
	w.save(u, d, lorem(120, "b"))
	up := w.call(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(500)}).(studio.GoalItem).Item
	if up.Status != "active" || up.CompletedOn != "" {
		t.Errorf("after raising: %+v", up)
	}
	// Lowering it below the words reaches it again, from the next read.
	low := w.call(u, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(50)}).(studio.GoalItem).Item
	if low.Status != "completed" {
		t.Errorf("after lowering: %+v", low)
	}
}

// For a goal over every project the stored completion comes from a sum that may hold a locked project's words, so a reader
// who cannot count every project is not told it: they see the live figure, and the person who can count them all sees it
// sticky.
func TestAStoredCompletionOfAGoalOverEveryProjectIsNotShownToAPartialReader(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	open, other := w.session(u, "ses_open"), w.session(u, "ses_other")
	pA, pB := w.project(open, "Open"), w.project(open, "Locked")
	dA, dB := w.doc(open, pA), w.doc(open, pB)
	w.at("2026-03-02T09:00:00Z")
	g := w.call(open, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 100}).(studio.GoalItem).Item
	w.save(open, dA, lorem(40, "a"))
	w.save(open, dB, lorem(80, "b"))
	if got := goalByID(t, w, open, g.ID); got.Status != "completed" { // 120 over both; remembered by this read
		t.Fatalf("%+v", got)
	}
	if _, err := w.try(open, "project.passphrase.set", projectgate.SetIn{ID: pB, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	got := goalByID(t, w, other, g.ID)
	if got.Status != "active" || got.CompletedOn != "" || got.Value != 40 {
		t.Errorf("a locked reader was told of the completion: %+v", got)
	}
	if list := w.call(other, "studio.goal.list", studio.GoalListIn{}).(studio.GoalsOut); list.Completed != 0 {
		t.Errorf("counts %+v", list)
	}
	if got := goalByID(t, w, open, g.ID); got.Status != "completed" || got.CompletedOn != "2026-03-02" {
		t.Errorf("the session that counts everything: %+v", got)
	}
}

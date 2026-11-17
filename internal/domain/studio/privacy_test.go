package studio_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/studio"
)

const canary = "CANARYTITLE-lockedbook"

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A project with a passphrase contributes nothing to any aggregate for a session that has not unlocked it: not a word, a
// day, a goal, a sprint, a streak day or its title. The unlocked session is the positive control: it must find every one.
func TestALockedProjectContributesNothingToAnyAggregate(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	open := w.session(u, "ses_open")   // the owner's session that sets the passphrase stays unlocked
	other := w.session(u, "ses_other") // another browser: locked
	pOpen := w.project(open, "Open book")
	pLock := w.project(open, canary)
	dOpen, dLock := w.doc(open, pOpen), w.doc(open, pLock)

	w.at("2026-03-02T09:00:00Z")
	mkGoal(t, w, open, "day", "words", 100)
	mkGoal(t, w, open, "week", "words", 500)
	lockedGoal := w.call(open, "studio.goal.create", studio.GoalCreateIn{Period: "week", Measure: "chapters", Target: 3, ProjectID: pLock}).(studio.GoalItem).Item
	w.call(open, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 90_000, ProjectID: pLock})
	w.save(open, dOpen, lorem(50, "a"))
	w.at("2026-03-02T09:10:00Z")
	startSprint(t, w, open, pLock, 25, 0)
	w.save(open, dLock, lorem(70, "b"))
	w.call(open, "studio.sprint.stop", studio.SprintEndIn{})
	w.at("2026-03-02T10:00:00Z")
	startSprint(t, w, open, pOpen, 25, 0)
	w.save(open, dOpen, w.text(open, dOpen)+lorem(5, "c"))
	w.call(open, "studio.sprint.stop", studio.SprintEndIn{})
	// A comment and a legacy goal on the locked project too.
	w.call(open, "comment.create", review.CreateIn{ProjectID: pLock, DocumentID: dLock, Body: "CANARYCOMMENT"})
	w.call(open, "goal.set", goals.SetIn{ProjectID: pLock, Kind: "daily", TargetWords: 60})

	if _, err := w.try(open, "project.passphrase.set", projectgate.SetIn{ID: pLock, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}

	reads := func(p person) map[string]string {
		out := map[string]string{
			"dashboard": marshal(t, w.call(p, "studio.dashboard", studio.DashboardIn{Range: "all"})),
			"series":    marshal(t, w.call(p, "studio.series", studio.SeriesIn{Range: "all"})),
			"calendar":  marshal(t, w.call(p, "studio.calendar", studio.CalendarIn{All: true})),
			"goals":     marshal(t, w.call(p, "studio.goal.list", studio.GoalListIn{Archived: true})),
			"sprints":   marshal(t, w.call(p, "studio.sprint.stats", studio.SprintStatsIn{Range: "all"})),
			"current":   marshal(t, w.call(p, "studio.sprint.current", studio.SprintEndIn{})),
			"compare":   marshal(t, w.call(p, "studio.compare", studio.CompareIn{Preset: "this_week"})),
		}
		for _, table := range []string{"daily", "weekly", "goals", "sprints", "projects"} {
			out["csv "+table] = string(w.call(p, "studio.export", studio.ExportIn{Table: table, Range: "all"}).(studio.ExportOut).Body)
		}
		out["markdown"] = string(w.call(p, "studio.export", studio.ExportIn{Format: "md", Range: "all"}).(studio.ExportOut).Body)
		return out
	}

	// The positive control: the session that has the project unlocked finds the canary and the locked words everywhere it should.
	unlocked := reads(open)
	d := w.call(open, "studio.dashboard", studio.DashboardIn{}).(studio.DashboardOut)
	if d.Today.Words != 125 || d.Hidden != 0 {
		t.Fatalf("unlocked: today %d, hidden %d", d.Today.Words, d.Hidden)
	}
	for _, name := range []string{"dashboard", "csv projects", "csv goals", "markdown"} {
		if !strings.Contains(unlocked[name], canary) {
			t.Errorf("the unlocked session's %s does not name the project: the control proves nothing", name)
		}
	}
	if st := w.call(open, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut); st.Sprints != 2 || st.Words != 75 {
		t.Fatalf("unlocked sprint stats %+v", st)
	}

	// The locked session: a count of hidden projects, and nothing else of them.
	m := w.call(other, "studio.dashboard", studio.DashboardIn{}).(studio.DashboardOut)
	if m.Today.Words != 55 || m.Cards[0].Words != 55 || m.Hidden != 1 {
		t.Errorf("locked: today %d (want 55), card %d, hidden %d (want 1)", m.Today.Words, m.Cards[0].Words, m.Hidden)
	}
	if len(m.Projects) != 1 || m.Projects[0].Title != "Open book" {
		t.Errorf("projects %+v", m.Projects)
	}
	for _, g := range m.Goals.Items {
		if g.ProjectID == pLock || g.Source == "project" {
			t.Errorf("a goal of the locked project: %+v", g)
		}
	}
	if m.Goals.Active != 2 { // the day goal and the week goal over every project
		t.Errorf("goal counts %+v", m.Goals)
	}
	// 70 words on the locked project would have met the 100-word day goal (50+5+70 = 125 > 100) and a legacy goal of 60: not
	// for this session (55 words), so today is not a goal day for it, and its streak does not have the locked day.
	if m.Streak.TodayHit || m.Streak.Current != 0 {
		t.Errorf("locked streak %+v", m.Streak)
	}
	if st := w.call(other, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut); st.Sprints != 1 || st.Words != 5 {
		t.Errorf("locked sprint stats %+v", st)
	}
	for name, body := range reads(other) {
		for _, bad := range []string{canary, "CANARYCOMMENT", pLock, lockedGoal.ID, dLock} {
			if strings.Contains(body, bad) {
				t.Errorf("%s, read by a session that has not unlocked the project, holds %q", name, bad)
			}
		}
	}
	csv := reads(other)["csv daily"]
	if !strings.Contains(csv, "2026-03-02,55,55,") {
		t.Errorf("daily csv for the locked session: %s", csv)
	}
	// Naming the locked project asks for what is not there.
	if _, err := w.try(other, "studio.goal.create", studio.GoalCreateIn{Period: "day", Measure: "words", Target: 5, ProjectID: pLock}); !isNotFound(err) {
		t.Errorf("a goal on a locked project: %v", err)
	}
	if _, err := w.try(other, "studio.compare", studio.CompareIn{Preset: "this_week", AProject: pLock}); !isNotFound(err) {
		t.Errorf("a comparison naming a locked project: %v", err)
	}
	for _, op := range []string{"studio.goal.update", "studio.goal.archive", "studio.goal.history"} {
		var err error
		switch op {
		case "studio.goal.update":
			_, err = w.try(other, op, studio.GoalUpdateIn{ID: lockedGoal.ID, Target: ptr(4)})
		case "studio.goal.archive":
			_, err = w.try(other, op, studio.GoalArchiveIn{ID: lockedGoal.ID})
		default:
			_, err = w.try(other, op, studio.GoalHistoryIn{ID: lockedGoal.ID})
		}
		if !isNotFound(err) {
			t.Errorf("%s on a goal of a locked project: %v", op, err)
		}
	}
	// A sprint left running on the project: the locked session may stop it, and sees nothing of the project in the answer.
	w.at("2026-03-02T10:10:00Z") // inside the unlock's 30 idle minutes
	startSprint(t, w, open, pLock, 25, 0)
	w.save(open, dLock, w.text(open, dLock)+lorem(33, "z"))
	stopped := w.call(other, "studio.sprint.stop", studio.SprintEndIn{}).(studio.SprintItem).Item
	if stopped.State != "stopped" || stopped.ProjectID != "" || stopped.WordsAdded != 0 || strings.Contains(marshal(t, stopped), pLock) {
		t.Errorf("a locked session was told of the sprint's project or words: %+v", stopped)
	}
	// The project's own totals are a project op: the gate answers it.
	if _, err := w.try(other, "studio.project.totals", studio.ProjectTotalsIn{ProjectID: pLock}); err == nil {
		t.Error("project totals answered a locked session")
	}
	if _, err := w.try(other, "studio.sprint.start", studio.SprintStartIn{ProjectID: pLock, Minutes: 5}); err == nil {
		t.Error("a sprint started on a locked project")
	}
	// The no-session reading (a card, the progress email) leaves out every project that has a passphrase at all.
	v, err := studio.NoSessionViewer(t.Context(), w.d.Read(), u.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.IDs) != 1 || v.IDs[0] != pOpen {
		t.Errorf("no-session projects: %v", v.IDs)
	}
}

// A project in the trash, or one the person has left, is not theirs to count, and its numbers go with it.
func TestTrashedAndAbandonedProjectsAreLeftOut(t *testing.T) {
	w := newWorld(t)
	u, v := w.person("u", "UTC"), w.person("v", "UTC")
	pMine := w.project(u, "Mine")
	pShared := w.project(v, "Shared")
	w.call(v, "project.member.add", projects.MemberAddIn{ID: pShared, Email: "u@test.local", Role: "editor"})
	d1, d2 := w.doc(u, pMine), w.doc(u, pShared)
	w.at("2026-03-02T09:00:00Z")
	w.save(u, d1, lorem(40, "a"))
	w.save(u, d2, lorem(60, "b"))
	if got := dash(t, w, u).Today.Words; got != 100 {
		t.Fatalf("both: %d", got)
	}
	w.call(v, "project.member.remove", projects.MemberRemoveIn{ID: pShared, UserID: u.id})
	if got := dash(t, w, u).Today.Words; got != 40 {
		t.Errorf("after leaving a project: %d, want 40", got)
	}
	// Her words in the shared project are not in his numbers, either.
	if got := dash(t, w, v).Today.Words; got != 0 {
		t.Errorf("another person's words in v's day: %d", got)
	}
	w.call(u, "project.trash", projects.StateIn{ID: pMine})
	if got := dash(t, w, u).Today.Words; got != 0 {
		t.Errorf("after trashing: %d", got)
	}
}

// A version of a goal over every project remembers the person's progress at the moment it was replaced, a sum that may
// hold the words of a project a reader may not count. A reader who cannot count every project is therefore told nothing
// that depends on it: the week below was met only by the locked project's words, before the goal was raised, and only the
// session that may count them is told so.
func TestAGoalChangedBeforeALockDoesNotTellALockedReaderWhatWasWritten(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	open := w.session(u, "ses_open")
	other := w.session(u, "ses_other")
	pOpen, pLock := w.project(open, "Open"), w.project(open, "Locked book")
	dOpen, dLock := w.doc(open, pOpen), w.doc(open, pLock)
	w.at("2026-03-02T09:00:00Z") // a Monday
	g := mkGoal(t, w, open, "week", "words", 100)
	w.save(open, dOpen, lorem(40, "a"))
	w.save(open, dLock, lorem(80, "b")) // 120 in the week: met, by the help of the locked project
	w.at("2026-03-03T09:00:00Z")
	w.call(open, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(300)})
	if _, err := w.try(open, "project.passphrase.set", projectgate.SetIn{ID: pLock, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	mine := goalByID(t, w, open, g.ID)
	theirs := goalByID(t, w, other, g.ID)
	if !mine.Met || mine.Value != 120 || mine.Target != 300 {
		t.Errorf("the session that may count everything: %+v", mine)
	}
	if theirs.Met || theirs.Value != 40 || theirs.Target != 300 {
		t.Errorf("the locked session was told the week was met, or its words: %+v", theirs)
	}
	for _, p := range hist(t, w, other, g.ID).Periods {
		if p.Value != 40 || p.Met {
			t.Errorf("the locked session's history: %+v", p)
		}
	}
	// The same for the date an archived goal ended on: stored as the date it was archived, worked out for the reader.
	w.call(open, "studio.goal.archive", studio.GoalArchiveIn{ID: g.ID})
	if end := goalByID(t, w, other, g.ID).EndedOn; end != "2026-03-02" {
		t.Errorf("a locked reader's end of the goal is %s: the week was not met by what they may count, so it ends the day before it was archived (2026-03-02)", end)
	}
	if end := goalByID(t, w, open, g.ID).EndedOn; end != "2026-03-03" {
		t.Errorf("the owner's end of the goal is %s: the week was met, so it ends the day it was archived", end)
	}
}

package studio_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/studio"
)

func startSprint(t *testing.T, w *world, p person, project string, minutes, target int) studio.SprintOut {
	t.Helper()
	return w.call(p, "studio.sprint.start", studio.SprintStartIn{ProjectID: project, Minutes: minutes, TargetWords: target}).(studio.SprintItem).Item
}

func current(t *testing.T, w *world, p person) *studio.SprintOut {
	t.Helper()
	return w.call(p, "studio.sprint.current", studio.SprintEndIn{}).(studio.SprintCurrentOut).Sprint
}

func TestASprintIsAPartOfItsDayAndNeverCountedTwice(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p1, p2 := w.project(u, "One"), w.project(u, "Two")
	d1, d2 := w.doc(u, p1), w.doc(u, p2)
	w.at("2026-03-02T09:00:00Z")
	w.save(u, d1, lorem(25, "pre")) // before the sprint: in the day, not in the sprint

	w.at("2026-03-02T10:00:00Z")
	sp := startSprint(t, w, u, p1, 25, 100)
	if sp.State != "running" || sp.PlannedMinutes != 25 || sp.RemainingSecs != 25*60 || sp.EndsAt != "2026-03-02T10:25:00.000Z" || sp.ClosesAt != "2026-03-02T10:27:00.000Z" {
		t.Fatalf("sprint %+v", sp)
	}
	w.at("2026-03-02T10:02:00Z")
	w.save(u, d1, w.text(u, d1)+lorem(40, "a")) // in
	w.at("2026-03-02T10:05:00Z")
	w.save(u, d2, lorem(500, "other")) // another project: the day, not this sprint
	w.at("2026-03-02T10:10:00Z")
	w.save(u, d1, w.text(u, d1)+lorem(30, "b")) // in
	if cur := current(t, w, u); cur == nil || cur.WordsAdded != 70 || cur.Saves != 2 || cur.RemainingSecs != 15*60 {
		t.Errorf("running: %+v", cur)
	}
	// Past the planned end but inside the two minutes of grace: still taking saves, and told so.
	w.at("2026-03-02T10:26:00Z")
	if cur := current(t, w, u); cur == nil || !cur.Over || cur.RemainingSecs != 0 {
		t.Errorf("in the grace: %+v", cur)
	}
	w.save(u, d1, w.text(u, d1)+lorem(5, "c")) // in: 75
	// After closes_at the sprint takes nothing more, and nobody had to stop it: no write, no timer.
	w.at("2026-03-02T10:30:00Z")
	w.save(u, d1, w.text(u, d1)+lorem(20, "d")) // the day, not the sprint
	if cur := current(t, w, u); cur != nil {
		t.Errorf("a sprint past its closing time is still current: %+v", cur)
	}
	st := w.call(u, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut)
	if st.Sprints != 1 || st.Words != 75 || st.AvgPerSprint != 75 || st.Best == nil || st.Best.WordsAdded != 75 || len(st.Recent) != 1 {
		t.Fatalf("stats %+v", st)
	}
	r := st.Recent[0]
	if r.State != "completed" || r.EndedAt != "2026-03-02T10:25:00.000Z" || r.ElapsedSecs != 25*60 || r.WordsAdded != 75 || r.Saves != 3 {
		t.Errorf("settled by the rule: %+v", r)
	}
	if st.Parts[1].Part != "morning" || st.Parts[1].Sprints != 1 || st.Parts[1].AvgPerSprint != 75 {
		t.Errorf("by time of day: %+v", st.Parts)
	}
	// The day holds 25 + 40 + 500 + 30 + 5 + 20 = 620; the sprint holds 75 of them.
	d := dash(t, w, u)
	if d.Today.Words != 620 {
		t.Errorf("the day: %d", d.Today.Words)
	}
	if st.Words > d.Today.Words {
		t.Error("a sprint holds more than its day")
	}
	// The record still says "running" until the next write settles it; that write does.
	var state string
	_ = w.d.Read().QueryRow(`SELECT state FROM studio_sprints WHERE id = ?`, sp.ID).Scan(&state)
	if state != "running" {
		t.Errorf("a read wrote: %s", state)
	}
	w.at("2026-03-02T11:00:00Z")
	sp2 := startSprint(t, w, u, p1, 15, 0)
	_ = w.d.Read().QueryRow(`SELECT state FROM studio_sprints WHERE id = ?`, sp.ID).Scan(&state)
	var ended string
	_ = w.d.Read().QueryRow(`SELECT ended_at FROM studio_sprints WHERE id = ?`, sp.ID).Scan(&ended)
	if state != "completed" || ended != "2026-03-02T10:25:00.000Z" || sp2.State != "running" {
		t.Errorf("settled on the next start: %s at %s", state, ended)
	}
}

func TestASprintThatIsTheWholeOfItsDayEqualsTheDay(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "One")
	d := w.doc(u, p)
	w.at("2026-03-02T10:00:00Z")
	startSprint(t, w, u, p, 45, 0)
	for i, n := range []int{33, 21, 8} {
		w.at([]string{"2026-03-02T10:05:00Z", "2026-03-02T10:20:00Z", "2026-03-02T10:40:00Z"}[i])
		w.save(u, d, w.text(u, d)+lorem(n, string(rune('a'+i))))
	}
	// A cut inside the sprint is net, never subtracted from what it added.
	w.at("2026-03-02T10:41:00Z")
	w.save(u, d, lorem(10, "a"))
	out := w.call(u, "studio.sprint.stop", studio.SprintEndIn{}).(studio.SprintItem).Item
	if out.State != "stopped" || out.WordsAdded != 62 || out.WordsNet != 10-0 {
		t.Errorf("stopped: %+v", out)
	}
	m := dash(t, w, u)
	if m.Today.Words != out.WordsAdded || m.Today.Net != out.WordsNet {
		t.Errorf("the day (%d added, %d net) and its only sprint (%d, %d) must agree", m.Today.Words, m.Today.Net, out.WordsAdded, out.WordsNet)
	}
	// A recompute of the days leaves the sprint where it was, and the sprint still fits inside its day.
	if _, err := goals.Recompute(context.Background(), w.d, "", ""); err != nil {
		t.Fatal(err)
	}
	st := w.call(u, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut)
	if st.Words != 62 || dash(t, w, u).Today.Words != 62 {
		t.Errorf("after a recompute: sprints %d words, day %d", st.Words, dash(t, w, u).Today.Words)
	}
}

func TestSprintRules(t *testing.T) {
	w := newWorld(t)
	u, v := w.person("u", "UTC"), w.person("v", "UTC")
	p := w.project(u, "One")
	d := w.doc(u, p)
	w.at("2026-03-02T10:00:00Z")
	for _, bad := range []studio.SprintStartIn{{ProjectID: p, Minutes: -1}, {ProjectID: p, Minutes: 241}, {ProjectID: p, Minutes: 5, TargetWords: 100_001}} {
		if _, err := w.try(u, "studio.sprint.start", bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	// A reader cannot start one; a stranger finds no project.
	if _, err := w.try(v, "studio.sprint.start", studio.SprintStartIn{ProjectID: p, Minutes: 15}); !isNotFound(err) {
		t.Errorf("a stranger: %v", err)
	}
	startSprint(t, w, u, p, 15, 0)
	if _, err := w.try(u, "studio.sprint.start", studio.SprintStartIn{ProjectID: p, Minutes: 15}); !isConflict(err) {
		t.Errorf("a second sprint: %v", err)
	}
	// Nobody else's sprint is touched by stop or abandon.
	if _, err := w.try(v, "studio.sprint.stop", studio.SprintEndIn{}); !isConflict(err) {
		t.Errorf("stopping with none running: %v", err)
	}
	// Abandoning keeps the words in the day and leaves the sprint out of the statistics, saying nothing about it.
	w.at("2026-03-02T10:03:00Z")
	w.save(u, d, lorem(50, "a"))
	ab := w.call(u, "studio.sprint.abandon", studio.SprintEndIn{}).(studio.SprintItem).Item
	if ab.State != "abandoned" || ab.WordsAdded != 50 {
		t.Errorf("abandoned: %+v", ab)
	}
	st := w.call(u, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut)
	if st.Sprints != 0 || st.Words != 0 || st.Best != nil || len(st.Recent) != 0 {
		t.Errorf("an abandoned sprint is in the statistics: %+v", st)
	}
	if dash(t, w, u).Today.Words != 50 {
		t.Error("abandoning a sprint took its words out of the day")
	}
	// The open session timer runs four hours: a save at three hours is in it, a save at five is not.
	w.at("2026-03-02T12:00:00Z")
	startSprint(t, w, u, p, 0, 0)
	w.at("2026-03-02T15:00:00Z")
	w.save(u, d, w.text(u, d)+lorem(10, "b"))
	w.at("2026-03-02T17:00:00Z")
	w.save(u, d, w.text(u, d)+lorem(10, "c"))
	if cur := current(t, w, u); cur != nil {
		t.Errorf("an open timer after five hours: %+v", cur)
	}
	st = w.call(u, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut)
	if st.Sprints != 1 || st.Words != 10 || st.Recent[0].PlannedMinutes != 0 || st.Recent[0].EndedAt != "2026-03-02T16:00:00.000Z" {
		t.Errorf("open session: %+v", st)
	}
}

func TestASprintLeftOpenIsClosedByARestore(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "One")
	d := w.doc(u, p)
	w.at("2026-03-02T10:00:00Z")
	startSprint(t, w, u, p, 45, 0)
	w.at("2026-03-02T10:05:00Z")
	w.save(u, d, lorem(30, "a"))
	n, err := studio.CloseOpenSprints(context.Background(), w.d)
	if err != nil || n != 1 {
		t.Fatalf("closed %d: %v", n, err)
	}
	if cur := current(t, w, u); cur != nil {
		t.Errorf("still running: %+v", cur)
	}
	st := w.call(u, "studio.sprint.stats", studio.SprintStatsIn{}).(studio.SprintStatsOut)
	if st.Sprints != 1 || st.Words != 30 {
		t.Errorf("its words stay: %+v", st)
	}
	// A second pass finds nothing, and a new sprint can start.
	if n, _ := studio.CloseOpenSprints(context.Background(), w.d); n != 0 {
		t.Errorf("second pass closed %d", n)
	}
	startSprint(t, w, u, p, 15, 0)
}

func TestTheRestoreStepIsRegisteredAndClosesSprints(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	p := w.project(u, "One")
	w.at("2026-03-02T10:00:00Z")
	startSprint(t, w, u, p, 45, 0)
	said := ""
	for _, step := range backup.PostRestore {
		msg, err := step(context.Background(), w.d, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(msg, "sprint") {
			said = msg
		}
	}
	if said == "" {
		t.Error("no restore step spoke of a sprint")
	}
	if cur := current(t, w, u); cur != nil {
		t.Errorf("a sprint survived the restore: %+v", cur)
	}
}

package mail

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
)

// Review R1 (through the real path): the progress email is built from a viewer that never sets Hidden, so a goal that was raised
// during a day is judged with a version's progress_at, a sum that held the words of a project that now has a passphrase. The
// email reports a daily goal as met on a day that only the protected project's words met. (Recipient: the account owner.)
func TestRvp75ProgressEmailCountsAProtectedProjectsWordsThroughAGoalVersion(t *testing.T) {
	w := newWorld(t)
	w.configure()
	open := w.project
	locked := w.addProject("Locked", w.alice, nil)
	day := db.Now()[:10]
	w.words(w.alice, open, day, 60, 60, 600)
	w.words(w.alice, locked, day, 60, 60, 600)
	ex := func(q string, a ...any) {
		if _, err := w.d.Writer().Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	ex(`INSERT INTO studio_goals(id, user_id, project_id, period, measure, start_date, created_at) VALUES ('sgl_rv1', ?, '', 'day', 'words', ?, ?)`, w.alice.id, day, db.Now())
	ex(`INSERT INTO studio_goal_versions(goal_id, version, target, from_date, progress_at, created_at) VALUES ('sgl_rv1', 1, 100, ?, 0, ?)`, day, db.Now())
	ex(`INSERT INTO studio_goal_versions(goal_id, version, target, from_date, progress_at, created_at) VALUES ('sgl_rv1', 2, 200, ?, 120, ?)`, day, db.Now())
	w.protect(locked)
	body, err := w.svc.BuildProgress(context.Background(), w.alice.id, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "You met your daily goal on 1 day") || strings.Contains(body, "Days in a row now: 1") {
		t.Errorf("the email says the goal was met although only the protected project's words reached it (60 of 100 visible):\n%s", body)
	}
}

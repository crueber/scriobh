package mail

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
)

// A person with a daily goal who did not meet it in the period is not told "met your goal on 0 days" or "0 days in a row":
// what was done is said, a nought is left out.
func TestTheProgressEmailLeavesOutAGoalNoughtAndAStreakNought(t *testing.T) {
	w := newWorld(t)
	w.configure()
	day := db.Now()[:10]
	w.words(w.alice, w.project, day, 60, 60, 600)
	if _, err := w.d.Writer().Exec(`INSERT INTO studio_goals(id, user_id, project_id, period, measure, start_date, created_at) VALUES ('sgl_fx1', ?, '', 'day', 'words', ?, ?)`, w.alice.id, day, db.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Writer().Exec(`INSERT INTO studio_goal_versions(goal_id, version, target, from_date, progress_at, created_at) VALUES ('sgl_fx1', 1, 500, ?, 0, ?)`, day, db.Now()); err != nil {
		t.Fatal(err)
	}
	body, err := w.svc.BuildProgress(context.Background(), w.alice.id, day, day)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "Words added: 60") {
		t.Fatalf("the control: no words in\n%s", body)
	}
	if strings.Contains(body, "on 0 days") || strings.Contains(body, "Days in a row now") {
		t.Errorf("the email names a nought:\n%s", body)
	}
}

package studio_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/ops"
)

// A number shown to a viewer is judged only from the projects that viewer's figures include. A daily goal over every project
// is raised during a day on which 60 words were written in each of two projects (a version remembers 120 words at that moment,
// so the day was met under the first target): once the second project is left out, for any of these reasons, the 60 words
// that remain are against the target now in force, and the day is not met, on the dashboard, on the public card and in the
// progress email.
func TestAProjectLeftOutForAnyReasonNeverJudgesADay(t *testing.T) {
	for _, reason := range []string{"locked", "trashed", "left", "unapproved key", "no session"} {
		t.Run(reason, func(t *testing.T) {
			w := newWorld(t)
			u, v := w.person("u", "UTC"), w.person("v", "UTC")
			us := w.session(u, "ses_u")
			vs := w.session(v, "ses_v")
			pA := w.project(us, "Open")
			pB := w.project(vs, "Other")
			w.call(vs, "project.member.add", projects.MemberAddIn{ID: pB, Email: "u@test.local", Role: "editor"})
			dA, dB := w.doc(us, pA), w.doc(us, pB)
			w.at("2026-03-02T09:00:00Z")
			g := mkGoal(t, w, us, "day", "words", 100)
			w.save(us, dA, lorem(60, "a"))
			w.save(us, dB, lorem(60, "b"))
			w.call(us, "studio.goal.update", studio.GoalUpdateIn{ID: g.ID, Target: ptr(200)})
			// A card, opened once while everything counts, so the cache is warm.
			card := w.call(us, "studio.card.create", studio.CardIn{}).(studio.CardItem).Item
			before, err := w.cards.Open(t.Context(), card.Token, false)
			if err != nil || before.Streak != 1 || !before.TodayHit {
				t.Fatalf("with everything counted the day is met under the first target: %+v %v", before, err)
			}
			dashPrincipal := us
			switch reason {
			case "locked", "no session":
				if _, err := w.try(vs, "project.passphrase.set", projectgate.SetIn{ID: pB, Passphrase: "a long enough one"}); err != nil {
					t.Fatal(err)
				}
			case "trashed":
				w.call(vs, "project.trash", projects.StateIn{ID: pB})
			case "left":
				w.call(vs, "project.member.remove", projects.MemberRemoveIn{ID: pB, UserID: u.id})
			case "unapproved key":
				if _, err := w.try(vs, "project.passphrase.set", projectgate.SetIn{ID: pB, Passphrase: "a long enough one"}); err != nil {
					t.Fatal(err)
				}
				err := w.d.WithTx(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO api_keys(id, user_id, name, label, prefix, secret_hash, scope, is_agent, created_at) VALUES ('key_p', ?, 'k', '', 'scr_p', 'h', 'read', 1, ?)`, u.id, db.Now())
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
				dashPrincipal = person{id: u.id, p: ops.Principal{UserID: u.id, Role: ops.RoleWriter, Via: "apikey", KeyID: "key_p", Scope: "read"}}
			}

			// The dashboard.
			d := w.call(dashPrincipal, "studio.dashboard", studio.DashboardIn{}).(studio.DashboardOut)
			if d.Today.Words != 60 || d.Today.Hit || d.Streak.TodayHit || d.Streak.Current != 0 {
				t.Errorf("dashboard: words %d, hit %v, streak %+v: only the 60 visible words, against 200", d.Today.Words, d.Today.Hit, d.Streak)
			}
			if d.Streak.Note == nil || d.Streak.Note.Key != "studio.streak.note" {
				t.Errorf("the streak says nothing of the days it cannot count: %+v", d.Streak.Note)
			}
			// The public card, which had the day met a moment ago.
			after, err := w.cards.Open(t.Context(), card.Token, false)
			if err != nil {
				t.Fatal(err)
			}
			if after.TodayHit || after.Streak != 0 || after.WritingDays30 != 1 {
				t.Errorf("card: %+v", after)
			}
			// The progress email's figures: the projects it may speak of are the live ones with no passphrase.
			rows, err := w.d.Read().Query(`SELECT m.project_id FROM project_members m WHERE m.user_id = ? AND NOT EXISTS (SELECT 1 FROM project_gates g WHERE g.project_id = m.project_id)`, u.id)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
			mv, err := studio.ViewerFor(t.Context(), w.d.Read(), u.id, ids)
			if err != nil {
				t.Fatal(err)
			}
			r, err := mv.Report("2026-03-02", "2026-03-02")
			if err != nil {
				t.Fatal(err)
			}
			if r.Words != 60 || r.GoalDaysMet != 0 || r.Streak != 0 {
				t.Errorf("email: %+v", r)
			}
			if strings.Contains(marshal(t, d)+marshal(t, after), "Other") {
				t.Errorf("the other project's title is in what was shown")
			}
		})
	}
}

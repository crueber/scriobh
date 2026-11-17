package mail

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// words plants one day of a person's writing, the way the save loop records it.
func (w *world) words(user person, project, day string, added, net, seconds int) {
	w.t.Helper()
	if _, err := w.d.Writer().Exec(`INSERT INTO daily_stats(user_id, project_id, day, words_added, words_net, minutes, sessions, active_seconds, updated_at) VALUES (?,?,?,?,?,?,1,?,?)
		ON CONFLICT(user_id, project_id, day) DO UPDATE SET words_added = words_added + excluded.words_added, words_net = words_net + excluded.words_net`,
		user.id, project, day, added, net, (seconds+59)/60, seconds, db.Now()); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) protect(project string) {
	w.t.Helper()
	if _, err := w.d.Writer().Exec(`INSERT INTO project_gates(project_id, pass_hash, created_at, updated_at) VALUES (?, 'x', ?, ?)`, project, db.Now(), db.Now()); err != nil {
		w.t.Fatal(err)
	}
}

func TestCoveredPeriodsAndSlots(t *testing.T) {
	for _, c := range []struct {
		freq, at, key, from, to string
	}{
		{ProgressWeekly, "2026-06-08", "2026-W23", "2026-06-01", "2026-06-07"},
		{ProgressWeekly, "2026-06-14", "2026-W23", "2026-06-01", "2026-06-07"}, // Sunday: the week before the one it is in
		{ProgressWeekly, "2027-01-04", "2026-W53", "2026-12-28", "2027-01-03"}, // across a year, an ISO year of 53 weeks
		{ProgressWeekly, "2026-01-05", "2026-W01", "2025-12-29", "2026-01-04"}, // the week that holds New Year's Day belongs to 2026
		{ProgressMonthly, "2026-07-01", "2026-06", "2026-06-01", "2026-06-30"},
		{ProgressMonthly, "2027-01-01", "2026-12", "2026-12-01", "2026-12-31"},
		{ProgressMonthly, "2028-03-01", "2028-02", "2028-02-01", "2028-02-29"}, // a leap February
		{ProgressMonthly, "2026-03-31", "2026-02", "2026-02-01", "2026-02-28"}, // any day of the month: the one before it
	} {
		tm, _ := time.Parse("2006-01-02", c.at)
		k, f, to := coveredPeriod(c.freq, tm)
		if k != c.key || f != c.from || to != c.to {
			t.Errorf("%s at %s: %s %s..%s, want %s %s..%s", c.freq, c.at, k, f, to, c.key, c.from, c.to)
		}
	}
	// Due in the person's morning, on the Monday or the first, never before 07:00.
	loc, _ := time.LoadLocation("America/New_York")
	for _, freq := range []string{ProgressWeekly, ProgressMonthly} {
		s := progressSlot(freq, "usr_x", time.Date(2026, 6, 11, 15, 0, 0, 0, loc))
		wantDay := map[string]int{ProgressWeekly: 8, ProgressMonthly: 1}[freq]
		if s.Day() != wantDay || s.Hour() != 7 || s.Minute() > 30 || s.Location() != loc {
			t.Errorf("%s slot %v", freq, s)
		}
	}
}

func TestAProgressEmailIsOffByDefaultAndNeedsMail(t *testing.T) {
	w := newWorld(t)
	out := w.must(w.alice, "mail.progress.get", ProgressGetIn{}).(ProgressOut)
	if out.Enabled || out.MailAvailable || out.Frequency != "" || out.Email != "alice@example.com" || out.Contents == "" {
		t.Errorf("%+v", out)
	}
	w.words(w.alice, w.project, "2026-05-27", 500, 500, 600)
	w.clk.Advance(8 * 24 * time.Hour)
	if n := w.svc.ProgressOnce(context.Background()); n != 0 || len(w.rows()) != 0 {
		t.Fatalf("a message nobody asked for: %d", n)
	}
	_, err := w.call(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
	if e := ops_asError(err); e == nil || e.Code != ops.CodeConflict {
		t.Errorf("with mail off: %v", err)
	}
	w.configure()
	if _, err := w.call(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true}); err == nil {
		t.Error("no frequency")
	}
	if _, err := w.call(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: "daily"}); err == nil {
		t.Error("a frequency it does not have")
	}
	// An address is not a field of the request, and a key cannot turn it on.
	if s := jsonOf(t, ProgressSetIn{}); strings.Contains(s, "address") || strings.Contains(s, "email") {
		t.Errorf("ProgressSetIn: %s", s)
	}
	key := w.alice
	key.p.KeyID, key.p.Via, key.p.Scope = "key_x", "apikey", "write"
	for _, op := range []string{"mail.progress.get", "mail.progress.set"} {
		var in any = ProgressGetIn{}
		if op == "mail.progress.set" {
			in = ProgressSetIn{Enabled: true, Frequency: ProgressWeekly}
		}
		if _, err := w.call(key, op, in); err == nil {
			t.Errorf("%s through an API key", op)
		}
	}
	on := w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly}).(ProgressOut)
	if !on.Enabled || on.Frequency != ProgressWeekly || !on.MailAvailable {
		t.Errorf("%+v", on)
	}
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: false})
	if o := w.must(w.alice, "mail.progress.get", ProgressGetIn{}).(ProgressOut); o.Enabled {
		t.Errorf("still on: %+v", o)
	}
	_ = auth.LostAccess
}

func TestTheProgressEmailSaysNumbersAndNothingElse(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setZone(w.alice, "America/New_York")
	w.plant(w.project)
	locked := w.addProject(cLocked, w.alice, nil)
	trashed := w.addProject("TRASHED-CANARY-7a5", w.alice, nil)
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
	// Before the person's first Monday morning after turning it on, and for the period they turned it on in: nothing.
	w.clk.Advance(2 * 24 * time.Hour)
	if w.svc.ProgressOnce(context.Background()) != 0 {
		t.Fatal("a message in the week it was turned on")
	}
	// The week of Monday 1 to Sunday 7 June: 400 + 150 + 300 words on the open project; a protected one and a trashed one
	// wrote 999 and 777 words that must not appear in anything; the week before held 200.
	w.words(w.alice, w.project, "2026-06-02", 400, 400, 1200)
	w.words(w.alice, w.project, "2026-06-03", 150, 100, 600)
	w.words(w.alice, w.project, "2026-06-05", 300, -20, 0)
	w.words(w.alice, w.project, "2026-05-26", 200, 200, 0)
	w.words(w.alice, locked, "2026-06-04", 999, 999, 6000)
	w.words(w.alice, trashed, "2026-06-04", 777, 777, 6000)
	w.protect(locked)
	if _, err := w.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, db.Now(), trashed); err != nil {
		t.Fatal(err)
	}
	// Monday 8 June, 12:00 UTC is 08:00 in New York: past her slot.
	w.clk.Advance(5 * 24 * time.Hour)
	if n := w.svc.ProgressOnce(context.Background()); n != 1 {
		t.Fatalf("queued %d", n)
	}
	r := w.onlyRow()
	if r.Kind != KindProgress || r.Recipient != "alice@example.com" || r.Subject != ProgressSubject || r.Person != w.alice.id {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{"Your writing, 2026-06-01 to 2026-06-07.", "Words added: 850 (the period before: 200)", "You wrote on 3 days.", "Time writing: 30 minutes"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the message lacks %q:\n%s", want, r.Body)
		}
	}
	for _, bad := range []string{cDoc, cComment, cManu, cLocked, "TRASHED-CANARY", "Salt Road", "Bob", "bob@", "999", "1849", "777", "850 words"} {
		if bad == "850 words" {
			continue
		}
		if strings.Contains(r.Body, bad) || strings.Contains(r.Subject, bad) {
			t.Errorf("the message carries %q:\n%s", bad, r.Body)
		}
	}
	for _, u := range regexp.MustCompile(`https?://\S+`).FindAllString(r.Body, -1) {
		if !strings.HasPrefix(u, "https://scriobh.example.org/") {
			t.Errorf("a foreign link: %s", u)
		}
	}
	// What was done, never a count of days against the days there were; no word of shame.
	if m := regexp.MustCompile(`(?i)\b\d+ of \d+\b|\bout of\b|missed|failed|behind|lost|broke`).FindString(r.Body); m != "" {
		t.Errorf("the message frames a deficit (%q):\n%s", m, r.Body)
	}
	w.pump()
	m := w.smtp.Mails()
	if len(m) != 1 || m[0].To != "alice@example.com" {
		t.Fatalf("%+v", m)
	}
	h, body := decode(t, m[0].Data)
	if h.Get("List-Unsubscribe") != "<https://scriobh.example.org/settings>" || h.Get("Auto-Submitted") != "auto-generated" {
		t.Errorf("headers: %v", h)
	}
	if strings.Contains(strings.ToLower(m[0].Data), "text/html") || strings.Contains(body, "<") || !strings.Contains(body, "Words added: 850") {
		t.Errorf("not plain text, or not built: %q", body)
	}
	// Made once for the period, however often the scheduler asks, and again the next week.
	for i := 0; i < 3; i++ {
		w.svc.ProgressOnce(context.Background())
	}
	if len(w.rows()) != 1 {
		t.Errorf("%d messages for one week", len(w.rows()))
	}
	w.words(w.alice, w.project, "2026-06-09", 50, 50, 60)
	w.clk.Advance(7 * 24 * time.Hour)
	if n := w.svc.ProgressOnce(context.Background()); n != 1 || len(w.rows()) != 2 {
		t.Errorf("the next week: %d queued, %d rows", n, len(w.rows()))
	}
}

func TestAPeriodWithNothingToReportSendsNothingAndIsDone(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
	// The only writing of the week was on a protected project: for this email it did not happen.
	locked := w.addProject(cLocked, w.alice, nil)
	w.words(w.alice, locked, "2026-06-02", 500, 500, 600)
	w.protect(locked)
	w.clk.Advance(7 * 24 * time.Hour)
	if n := w.svc.ProgressOnce(context.Background()); n != 0 || len(w.rows()) != 0 {
		t.Fatalf("queued %d", n)
	}
	var last string
	_ = w.d.Read().QueryRow(`SELECT last_period FROM mail_progress WHERE user_id = ?`, w.alice.id).Scan(&last)
	if last != "2026-W23" {
		t.Errorf("the period was not marked done: %q", last)
	}
}

func TestAProgressEmailIsMadeAgainWhenItIsSent(t *testing.T) {
	for name, change := range map[string]func(w *world, project string){
		"the project is protected after it was queued": func(w *world, p string) { w.protect(p) },
		"the project is trashed after it was queued": func(w *world, p string) {
			if _, err := w.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, db.Now(), p); err != nil {
				w.t.Fatal(err)
			}
		},
		"the person left the project": func(w *world, p string) {
			if _, err := w.d.Writer().Exec(`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, p, w.bob.id); err != nil {
				w.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.configure()
			w.must(w.bob, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
			w.words(w.bob, w.project, "2026-06-02", 500, 500, 600)
			w.clk.Advance(7 * 24 * time.Hour)
			if n := w.svc.ProgressOnce(context.Background()); n != 1 {
				t.Fatalf("queued %d", n)
			}
			change(w, w.project)
			w.pump()
			if len(w.smtp.Mails()) != 0 {
				t.Errorf("sent after it was no longer wanted:\n%s", w.smtp.Mails()[0].Data)
			}
			if r := w.rows(); len(r) != 1 || r[0].State != "dead" || r[0].Err != "cancelled" || r[0].Body != "" {
				t.Errorf("%+v", r)
			}
		})
	}
}

func TestAProgressEmailIsBoundToItsAddressAndStopsWithTheAccount(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
	w.words(w.alice, w.project, "2026-06-02", 500, 500, 600)
	w.clk.Advance(7 * 24 * time.Hour)
	if _, err := w.d.Writer().Exec(`UPDATE users SET email = 'moved@example.com' WHERE id = ?`, w.alice.id); err != nil {
		t.Fatal(err)
	}
	if n := w.svc.ProgressOnce(context.Background()); n != 0 {
		t.Errorf("queued %d for an address it was not turned on for", n)
	}
	if o := w.must(w.alice, "mail.progress.get", ProgressGetIn{}).(ProgressOut); o.Enabled || !o.NeedsConfirm {
		t.Errorf("%+v", o)
	}
	// Turning it on again confirms the new address, from that moment.
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
	if o := w.must(w.alice, "mail.progress.get", ProgressGetIn{}).(ProgressOut); !o.Enabled || o.NeedsConfirm {
		t.Errorf("%+v", o)
	}
	// A person the identity provider removed has no subscription and nothing waiting.
	w.words(w.alice, w.project, "2026-06-10", 500, 500, 600)
	w.clk.Advance(7 * 24 * time.Hour)
	if n := w.svc.ProgressOnce(context.Background()); n != 1 {
		t.Fatalf("queued %d", n)
	}
	for _, hook := range auth.LostAccess {
		tx, _ := w.d.Writer().Begin()
		if err := hook(tx, w.alice.id); err != nil {
			t.Fatal(err)
		}
		_ = tx.Commit()
	}
	var n int
	_ = w.d.Read().QueryRow(`SELECT count(*) FROM mail_progress`).Scan(&n)
	if n != 0 || w.rows()[0].State != "dead" {
		t.Errorf("subscriptions %d, row %+v", n, w.rows()[0])
	}
}

func TestARestorePausesTheProgressEmailLikeTheRest(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressMonthly})
	w.words(w.alice, w.project, "2026-06-05", 500, 500, 600)
	w.clk.Advance(30 * 24 * time.Hour) // 2026-07-01 12:00: the first of the month, covering June
	if n := w.svc.ProgressOnce(context.Background()); n != 1 {
		t.Fatalf("queued %d", n)
	}
	notes := w.runPostRestore()
	if !strings.Contains(strings.Join(notes, " "), "Mail:") {
		t.Fatalf("notes %v", notes)
	}
	if rows := w.rows(); len(rows) != 0 {
		t.Errorf("a message survived the restore: %+v", rows)
	}
	// With mail paused nothing is made, and an administrator switching it on again starts everyone after that moment.
	w.words(w.alice, w.project, "2026-07-02", 400, 400, 600)
	w.clk.Advance(32 * 24 * time.Hour)
	if n := w.svc.ProgressOnce(context.Background()); n != 0 {
		t.Errorf("made %d with mail paused", n)
	}
	w.configure(func(c *ConfigSetIn) { c.Enabled = boolp(true) })
	if n := w.svc.ProgressOnce(context.Background()); n != 0 {
		t.Errorf("a backlog of %d after switching on", n)
	}
	var last string
	_ = w.d.Read().QueryRow(`SELECT last_period FROM mail_progress`).Scan(&last)
	if last != "2026-07" { // switched on on 2 August: the month just over is July, which was written while mail was off, so it is not sent
		t.Errorf("last period %q", last)
	}
}

func TestTheProgressMessageHasNoPlaceForText(t *testing.T) {
	// Every line of the body is built from numbers and fixed words: the builder takes no title, name or text.
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.progress.set", ProgressSetIn{Enabled: true, Frequency: ProgressWeekly})
	w.words(w.alice, w.project, "2026-06-02", 10, 10, 60)
	w.clk.Advance(7 * 24 * time.Hour)
	body, err := w.svc.BuildProgress(context.Background(), w.alice.id, "2026-06-01", "2026-06-07")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "Open Scríobh: https://") || strings.HasPrefix(line, "Stop these emails") {
			continue
		}
		if !regexp.MustCompile(`^(Your writing, [0-9-]+ to [0-9-]+\.|Words added: \d+ \(the period before: \d+\)|You wrote on \d+ days?\.|Time writing: \d+ minutes?|These are only numbers\. Your writing is never in this email\.)$`).MatchString(line) {
			t.Errorf("an unexpected line: %q", line)
		}
	}
}

package goals_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// env is a database, a registry and a clock the test moves by hand.
type env struct {
	t   *testing.T
	d   *db.DB
	reg *ops.Registry
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, d: testutil.DB(t), now: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)}
	e.reg = registry.Build(e.d, nil, t.TempDir())
	restore := db.SetClock(func() time.Time { return e.now })
	t.Cleanup(restore)
	return e
}

func (e *env) at(s string) {
	e.t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		e.t.Fatal(err)
	}
	e.now = tm
}

func (e *env) advance(d time.Duration) { e.now = e.now.Add(d) }

type person struct {
	id string
	p  ops.Principal
}

func (e *env) person(name, zone string) person {
	e.t.Helper()
	id := testutil.User(e.t, e.d, "writer", name+"@test.local")
	if zone != "" {
		e.setZone(id, zone)
	}
	return person{id: id, p: ops.Principal{UserID: id, Role: ops.RoleWriter}}
}

func (e *env) setZone(userID, zone string) {
	e.t.Helper()
	if _, err := e.d.Writer().Exec(`UPDATE users SET timezone = ? WHERE id = ?`, zone, userID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) call(p person, op string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(context.Background(), p.p, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (e *env) project(owner person) string {
	e.t.Helper()
	return e.call(owner, "project.create", projects.CreateIn{Title: "Book"}).(projects.Item).Item.ID
}

func (e *env) join(projectID string, p person, role string) {
	e.t.Helper()
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, projectID, p.id, role, db.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) doc(p person, projectID string) string {
	e.t.Helper()
	return e.call(p, "doc.create", docs.CreateIn{ProjectID: projectID, Title: "Ch"}).(docs.Item).Item.ID
}

// save writes text into a document as p, at the current clock.
func (e *env) save(p person, docID, text string) {
	e.t.Helper()
	cur := e.call(p, "doc.get", docs.DocIn{ID: docID}).(docs.Item).Item
	e.call(p, "doc.update", docs.UpdateIn{ID: docID, BaseRevision: cur.Revision, Content: &text})
}

func (e *env) setDaily(p person, projectID string, target int) {
	e.t.Helper()
	e.call(p, "goal.set", goals.SetIn{ProjectID: projectID, Kind: "daily", TargetWords: target})
}

func (e *env) today(p person, projectID string) goals.TodayOut {
	e.t.Helper()
	return e.call(p, "stats.today", goals.StatsIn{ProjectID: projectID}).(goals.TodayOut)
}

func lorem(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s%d ", tag, i)
	}
	return b.String()
}

func TestTypingAcrossAutosavesCreditsEachWordOnce(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	for _, s := range []string{"the qu", "the quick", "the quick br", "the quick brown", "the quick brown fo", "the quick brown fox"} {
		e.advance(3 * time.Second)
		e.save(w, d, s)
	}
	got := e.today(w, pid)
	if got.WordsAdded != 4 || got.WordsNet != 4 {
		t.Errorf("added %d net %d, want 4 and 4", got.WordsAdded, got.WordsNet)
	}
	if got.Sessions != 1 {
		t.Errorf("sessions = %d, want 1", got.Sessions)
	}
}

func TestCutPasteDeleteAndRestoreNeverReduceCredit(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 300)

	draft := lorem(300, "w")
	e.save(w, d, draft)
	if got := e.today(w, pid); got.WordsAdded != 300 || !got.Hit {
		t.Fatalf("after writing 300: %+v", got)
	}

	// Cut everything: net falls, credit stands, the day stays hit.
	e.advance(time.Minute)
	e.save(w, d, "")
	got := e.today(w, pid)
	if got.WordsAdded != 300 || got.WordsNet != 0 || !got.Hit {
		t.Errorf("after cutting everything: %+v", got)
	}

	// Paste it back: the same text is not new writing.
	e.advance(time.Minute)
	e.save(w, d, draft)
	got = e.today(w, pid)
	if got.WordsAdded != 300 || got.WordsNet != 300 {
		t.Errorf("after pasting back: added %d net %d, want 300 and 300", got.WordsAdded, got.WordsNet)
	}

	// Replace the text with 500 different words in the same session: the
	// rewrite is new writing on top of the 300 already earned.
	e.advance(time.Minute)
	e.save(w, d, lorem(500, "n"))
	got = e.today(w, pid)
	if got.WordsAdded != 800 || got.WordsNet != 500 {
		t.Errorf("after replacing: added %d net %d, want 800 and 500", got.WordsAdded, got.WordsNet)
	}

	// A big cut: net falls, added does not.
	e.advance(time.Minute)
	e.save(w, d, lorem(10, "n"))
	got = e.today(w, pid)
	if got.WordsAdded != 800 || got.WordsNet != 10 {
		t.Errorf("after cutting to 10: added %d net %d", got.WordsAdded, got.WordsNet)
	}
}

// Draft, discard, redraft is a day of writing: a rewritten draft is not
// credited as only its larger half.
func TestDraftDiscardRedraftCountsBoth(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.save(w, d, lorem(300, "a"))
	e.advance(time.Minute)
	e.save(w, d, "") // discard
	e.advance(time.Minute)
	e.save(w, d, lorem(300, "b")) // redraft
	if got := e.today(w, pid); got.WordsAdded != 600 || got.WordsNet != 300 {
		t.Errorf("added %d net %d, want 600 and 300", got.WordsAdded, got.WordsNet)
	}
	// And again in another document, after cutting most of what was written.
	d2 := e.doc(w, pid)
	e.advance(time.Hour) // a new session
	e.save(w, d2, lorem(200, "c"))
	e.advance(time.Minute)
	e.save(w, d2, lorem(50, "c")) // cut most of it
	e.advance(time.Minute)
	e.save(w, d2, lorem(50, "c")+lorem(120, "d"))
	if got := e.today(w, pid); got.WordsAdded != 600+200+120 {
		t.Errorf("added %d, want %d", got.WordsAdded, 600+200+120)
	}
}

// Text typed earlier in the session, cut and pasted back, earns nothing more,
// and writing on from there still adds.
func TestCutAndPasteBackOfFreshlyTypedTextEarnsNothingMore(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	whole := lorem(300, "t")
	e.save(w, d, whole)
	e.advance(10 * time.Minute)
	e.save(w, d, lorem(200, "t")) // cut the last 100 words
	if got := e.today(w, pid); got.WordsAdded != 300 || got.WordsNet != 200 {
		t.Fatalf("after the cut: %+v", got)
	}
	e.advance(time.Minute)
	e.save(w, d, whole) // paste them back
	if got := e.today(w, pid); got.WordsAdded != 300 || got.WordsNet != 300 {
		t.Errorf("after pasting back: added %d net %d, want 300 and 300", got.WordsAdded, got.WordsNet)
	}
	e.advance(time.Minute)
	e.save(w, d, whole+lorem(50, "u"))
	if got := e.today(w, pid); got.WordsAdded != 350 {
		t.Errorf("after writing 50 more: added %d, want 350", got.WordsAdded)
	}
}

// The case the baseline exists for: yesterday's 500 words are cut and 500 new
// ones written today. The cut subtracts nothing and the new words count in full.
func TestReplacingExistingTextCreditsTheNewWords(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.at("2026-03-02T10:00:00Z")
	e.save(w, d, lorem(500, "old"))
	e.at("2026-03-03T10:00:00Z")
	e.setDaily(w, pid, 400)
	e.save(w, d, "") // cut it all
	if got := e.today(w, pid); got.WordsAdded != 0 || got.WordsNet != -500 || got.Hit {
		t.Fatalf("after the cut: %+v", got)
	}
	e.advance(10 * time.Minute)
	e.save(w, d, lorem(500, "new"))
	got := e.today(w, pid)
	if got.WordsAdded != 500 || got.WordsNet != 0 || !got.Hit {
		t.Errorf("after rewriting: %+v", got)
	}
	// Cutting a paragraph and pasting it back, in two saves, earns nothing more.
	e.advance(time.Minute)
	e.save(w, d, lorem(100, "new"))
	e.advance(time.Minute)
	e.save(w, d, lorem(500, "new"))
	if got := e.today(w, pid); got.WordsAdded != 500 {
		t.Errorf("cut and paste back: %+v", got)
	}
}

func TestADayOfOnlyCuttingIsStillADay(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 100)
	e.save(w, d, lorem(1000, "w"))
	e.at("2026-03-03T10:00:00Z")
	e.save(w, d, lorem(400, "w")) // cut 600
	got := e.today(w, pid)
	if got.WordsAdded != 0 || got.WordsNet != -600 {
		t.Errorf("a cutting day: added %d net %d, want 0 and -600", got.WordsAdded, got.WordsNet)
	}
	if got.Hit {
		t.Error("a day with no words added does not meet a words-added goal, and is not a failure either")
	}
	h := e.call(w, "stats.history", goals.HistoryIn{ProjectID: pid, Days: 3}).(goals.HistoryOut)
	if len(h.Days) != 3 || h.Days[2].WordsNet != -600 {
		t.Errorf("history: %+v", h.Days)
	}
}

func TestSessionsSplitOnGapAndMinutesAreActiveTime(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	text := ""
	add := func(gap time.Duration) {
		e.advance(gap)
		text += lorem(5, fmt.Sprintf("t%d", e.now.Unix()%1000))
		e.save(w, d, text)
	}
	add(0)
	add(2 * time.Minute)  // 2 active minutes
	add(2 * time.Minute)  // 2 more
	add(20 * time.Minute) // within the session gap, but only 5 minutes count as active
	got := e.today(w, pid)
	if got.Sessions != 1 || got.Minutes != 9 {
		t.Errorf("one session of 9 active minutes expected, got %d sessions %d minutes", got.Sessions, got.Minutes)
	}
	add(31 * time.Minute) // past the gap: a new session
	got = e.today(w, pid)
	if got.Sessions != 2 {
		t.Errorf("sessions = %d, want 2", got.Sessions)
	}
	if got.WordsAdded != 25 {
		t.Errorf("added = %d, want 25", got.WordsAdded)
	}
}

func TestOpeningADocumentNeverCountsAsWriting(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 1)
	e.call(w, "project.open", projects.OpenIn{ID: pid})
	e.call(w, "doc.get", docs.DocIn{ID: d})
	// A save that changes nothing about the text is not writing either.
	e.save(w, d, "")
	got := e.today(w, pid)
	if got.WordsAdded != 0 || got.Sessions != 0 || got.Streak != 0 || got.Hit {
		t.Errorf("nothing was written: %+v", got)
	}
}

func TestStreakEndsOnlyWhenADayEnds(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 10)
	text := ""
	write := func(n int) {
		text += lorem(n, fmt.Sprintf("d%d", e.now.Day()))
		e.save(w, d, text)
	}
	e.at("2026-03-02T09:00:00Z")
	write(10)
	e.at("2026-03-03T09:00:00Z")
	write(12)
	if got := e.today(w, pid); got.Streak != 2 || !got.Hit {
		t.Errorf("two days met: %+v", got)
	}
	// The next morning, nothing written yet: the streak is intact.
	e.at("2026-03-04T08:00:00Z")
	if got := e.today(w, pid); got.Streak != 2 || got.Hit || got.Remaining != 10 {
		t.Errorf("an unfinished day must not break the streak: %+v", got)
	}
	// Some words, short of the goal: still intact, still not met.
	write(4)
	if got := e.today(w, pid); got.Streak != 2 || got.Hit || got.Remaining != 6 {
		t.Errorf("a partial day: %+v", got)
	}
	// The goal is met: the streak grows.
	write(6)
	if got := e.today(w, pid); got.Streak != 3 || !got.Hit || got.LongestStreak != 3 {
		t.Errorf("a third day: %+v", got)
	}
	// A day passes with nothing, and the next one starts: the streak ended.
	e.at("2026-03-06T08:00:00Z")
	if got := e.today(w, pid); got.Streak != 0 || got.LongestStreak != 3 {
		t.Errorf("after a missed day: %+v", got)
	}
}

func TestGoalChangesNeverTakeAFinishedDayAway(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 50)
	e.save(w, d, lorem(60, "a"))
	if got := e.today(w, pid); !got.Hit || got.Target != 50 {
		t.Fatalf("%+v", got)
	}
	e.setDaily(w, pid, 500) // raise it after the day was met
	if got := e.today(w, pid); !got.Hit || got.Target != 50 || got.Streak != 1 {
		t.Errorf("a met day stays met at the target it was met against: %+v", got)
	}
	// Lowering the goal below the words already written meets the day.
	e.at("2026-03-03T10:00:00Z")
	e.save(w, d, lorem(60, "a")+lorem(30, "b"))
	if got := e.today(w, pid); got.Hit || got.Target != 500 {
		t.Fatalf("%+v", got)
	}
	e.setDaily(w, pid, 25)
	if got := e.today(w, pid); !got.Hit || got.Streak != 2 {
		t.Errorf("lowering the goal below today's words meets today: %+v", got)
	}
	// Removing the goal leaves met days met.
	out := e.call(w, "goal.delete", goals.DeleteIn{ProjectID: pid, Kind: "daily"}).(goals.DeleteOut)
	if !out.Deleted {
		t.Error("expected the goal to be deleted")
	}
	if got := e.today(w, pid); !got.Hit || got.HasGoal && got.Target != 25 {
		t.Errorf("after deleting the goal: %+v", got)
	}
}

func TestDaysAreTheWritersLocalDaysAcrossDST(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "America/New_York")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 5)
	text := ""
	write := func(at string) {
		e.at(at)
		text += lorem(6, "x"+at[5:13])
		e.save(w, d, text)
	}
	// US spring forward: 2026-03-08, 02:00 EST becomes 03:00 EDT (a 23-hour day).
	write("2026-03-08T04:30:00Z") // 23:30 EST on Mar 7
	write("2026-03-08T05:10:00Z") // 00:10 EST on Mar 8
	write("2026-03-08T06:30:00Z") // 01:30 EST on Mar 8, still before the change
	write("2026-03-08T07:10:00Z") // 03:10 EDT on Mar 8 (40 real minutes later)
	write("2026-03-09T03:30:00Z") // 23:30 EDT on Mar 8
	write("2026-03-09T04:30:00Z") // 00:30 EDT on Mar 9

	e.at("2026-03-09T12:00:00Z")
	h := e.call(w, "stats.history", goals.HistoryIn{ProjectID: pid, Days: 4}).(goals.HistoryOut)
	got := map[string]int{}
	for _, d := range h.Days {
		got[d.Day] = d.WordsAdded
	}
	want := map[string]int{"2026-03-06": 0, "2026-03-07": 6, "2026-03-08": 24, "2026-03-09": 6}
	for day, w := range want {
		if got[day] != w {
			t.Errorf("%s: added %d, want %d (all: %v)", day, got[day], w, got)
		}
	}
	// Mar 7, 8 and 9 are consecutive local dates; the 23-hour day is one day.
	if s := e.today(w, pid); s.Streak != 3 || s.Day != "2026-03-09" || s.Timezone != "America/New_York" {
		t.Errorf("streak across the spring-forward day: %+v", s)
	}

	// US fall back: 2026-11-01, 02:00 EDT becomes 01:00 EST (a 25-hour day).
	write("2026-11-01T05:30:00Z") // 01:30 EDT
	write("2026-11-01T06:20:00Z") // 01:20 EST: the wall clock went backwards
	write("2026-11-02T04:30:00Z") // 23:30 EST on Nov 1
	e.at("2026-11-02T12:00:00Z")
	h = e.call(w, "stats.history", goals.HistoryIn{ProjectID: pid, Days: 3}).(goals.HistoryOut)
	for _, d := range h.Days {
		switch d.Day {
		case "2026-11-01":
			if d.WordsAdded != 18 {
				t.Errorf("the 25-hour day holds all three saves: %+v", d)
			}
		case "2026-10-31", "2026-11-02":
			if d.WordsAdded != 0 {
				t.Errorf("%s should be quiet: %+v", d.Day, d)
			}
		}
	}
}

func TestChangingZoneNeverRebucketsHistory(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 5)
	e.at("2026-04-10T23:30:00Z")
	e.save(w, d, lorem(8, "a"))
	before := e.call(w, "stats.history", goals.HistoryIn{ProjectID: pid, Days: 5}).(goals.HistoryOut)

	e.setZone(w.id, "Pacific/Auckland") // 12:30 on the 11th there
	e.at("2026-04-10T23:45:00Z")
	e.save(w, d, lorem(8, "a")+lorem(8, "b"))
	e.at("2026-04-11T10:00:00Z")
	h := e.call(w, "stats.history", goals.HistoryIn{ProjectID: pid, Days: 4}).(goals.HistoryOut)
	byDay := map[string]goals.DayStat{}
	for _, d := range h.Days {
		byDay[d.Day] = d
	}
	if byDay["2026-04-10"].WordsAdded != 8 {
		t.Errorf("the day written in UTC keeps its 8 words: %+v", byDay["2026-04-10"])
	}
	if byDay["2026-04-11"].WordsAdded != 8 {
		t.Errorf("the save made in Auckland lands on Auckland's 11th: %+v", byDay["2026-04-11"])
	}
	_ = before

	// Recompute reproduces the stored rows exactly, in the current zone.
	snapshot := dailyRows(t, e.d)
	if _, err := goals.Recompute(context.Background(), e.d, "", ""); err != nil {
		t.Fatal(err)
	}
	if again := dailyRows(t, e.d); again != snapshot {
		t.Errorf("recompute changed the totals:\nbefore %s\nafter  %s", snapshot, again)
	}
}

func dailyRows(t *testing.T, d *db.DB) string {
	t.Helper()
	rows, err := d.Read().Query(`SELECT user_id, project_id, day, words_added, words_net, minutes, sessions, goal_target, goal_hit
		FROM daily_stats ORDER BY user_id, project_id, day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var u, p, day string
		var a, n, m, s, tg, h int
		if err := rows.Scan(&u, &p, &day, &a, &n, &m, &s, &tg, &h); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%d|%d|%d|%d|%d|%d\n", u[len(u)-4:], p[len(p)-4:], day, a, n, m, s, tg, h)
	}
	return b.String()
}

func TestRecomputeRebuildsDriftedDailyTotals(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.setDaily(w, pid, 10)
	text := ""
	for day := 2; day <= 4; day++ {
		e.at(fmt.Sprintf("2026-03-%02dT09:00:00Z", day))
		text += lorem(12, fmt.Sprintf("d%d", day))
		e.save(w, d, text)
		e.advance(3 * time.Minute)
		text += lorem(3, fmt.Sprintf("e%d", day))
		e.save(w, d, text)
	}
	want := dailyRows(t, e.d)
	// Damage the totals.
	if _, err := e.d.Writer().Exec(`UPDATE daily_stats SET words_added = 0, words_net = 7, minutes = 99, sessions = 0`); err != nil {
		t.Fatal(err)
	}
	res, err := goals.Recompute(context.Background(), e.d, w.id, pid)
	if err != nil {
		t.Fatal(err)
	}
	if res.Days != 3 {
		t.Errorf("days = %d, want 3", res.Days)
	}
	if got := dailyRows(t, e.d); got != want {
		t.Errorf("recompute did not restore the totals:\nwant %s\ngot  %s", want, got)
	}
	// Narrowed to another person, it touches nothing.
	if _, err := e.d.Writer().Exec(`UPDATE daily_stats SET minutes = 42`); err != nil {
		t.Fatal(err)
	}
	other := e.person("other", "")
	if res, err := goals.Recompute(context.Background(), e.d, other.id, ""); err != nil || res.Days != 0 {
		t.Errorf("another person's scope: %v %+v", err, res)
	}
}

func TestStatsArePerPerson(t *testing.T) {
	e := newEnv(t)
	owner := e.person("owner", "UTC")
	editor := e.person("editor", "UTC")
	reader := e.person("reader", "UTC")
	pid := e.project(owner)
	e.join(pid, editor, ops.ProjectEditor)
	e.join(pid, reader, ops.ProjectReader)
	d1, d2 := e.doc(owner, pid), e.doc(owner, pid)
	e.setDaily(owner, pid, 5)
	e.setDaily(editor, pid, 500)

	e.save(owner, d1, lorem(40, "o"))
	e.save(editor, d2, lorem(7, "e"))

	if got := e.today(owner, pid); got.WordsAdded != 40 || got.Target != 5 || !got.Hit {
		t.Errorf("owner: %+v", got)
	}
	if got := e.today(editor, pid); got.WordsAdded != 7 || got.Target != 500 || got.Hit {
		t.Errorf("editor: %+v", got)
	}
	// A reader writes nothing and sees nothing of anyone else's.
	if got := e.today(reader, pid); got.WordsAdded != 0 || got.Sessions != 0 || got.HasGoal {
		t.Errorf("reader: %+v", got)
	}
	if h := e.call(reader, "stats.history", goals.HistoryIn{ProjectID: pid, Days: 7}).(goals.HistoryOut); h.TotalAdded != 0 {
		t.Errorf("reader history: %+v", h)
	}
	text := "x"
	if _, err := e.reg.Call(context.Background(), reader.p, "doc.update", docs.UpdateIn{ID: d1, BaseRevision: 2, Content: &text}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader must not save: %v", err)
	}
	if _, err := e.reg.Call(context.Background(), reader.p, "goal.set", goals.SetIn{ProjectID: pid, Kind: "daily", TargetWords: 5}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a reader must not set a goal: %v", err)
	}
	var n int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM writing_sessions WHERE user_id = ?`, reader.id).Scan(&n); err != nil || n != 0 {
		t.Errorf("a reader has %d sessions (%v)", n, err)
	}
	// Goals are listed per person.
	l := e.call(editor, "goal.list", goals.ListIn{ProjectID: pid}).(goals.ListOut)
	if len(l.Items) != 1 || l.Items[0].TargetWords != 500 {
		t.Errorf("editor goals: %+v", l.Items)
	}
	// A stranger cannot reach the project's stats at all.
	stranger := e.person("stranger", "")
	if _, err := e.reg.Call(context.Background(), stranger.p, "stats.today", goals.StatsIn{ProjectID: pid}); ops.AsError(err).Code != ops.CodeNotFound {
		t.Errorf("a stranger must get not found: %v", err)
	}
}

func TestAStatisticsFaultNeverFailsASave(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	if _, err := e.d.Writer().Exec(`ALTER TABLE daily_stats RENAME TO daily_stats_gone`); err != nil {
		t.Fatal(err)
	}
	e.save(w, d, "the words still save")
	got := e.call(w, "doc.get", docs.DocIn{ID: d}).(docs.Item).Item
	if got.Content != "the words still save" || got.Revision != 2 {
		t.Errorf("the save was lost: %+v", got.Meta)
	}
	var n int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM writing_sessions WHERE user_id = ?`, w.id).Scan(&n); err != nil || n != 0 {
		t.Errorf("a failed recording must roll back whole (sessions=%d, err=%v)", n, err)
	}
}

func TestRestoreRecordsNetButEarnsNothing(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.save(w, d, lorem(10, "a"))
	earlier := lorem(10, "a")
	older := lorem(10, "a") + lorem(500, "old")

	// Restoring an older text is a non-typing write.
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		return goals.RecordSave(tx, goals.Save{UserID: w.id, ProjectID: pid, DocumentID: d,
			OldText: earlier, NewText: older, OldWords: 10, NewWords: 510, NoCredit: true})
	})
	if err != nil {
		t.Fatal(err)
	}
	got := e.today(w, pid)
	if got.WordsAdded != 10 || got.WordsNet != 510 {
		t.Errorf("restore: added %d net %d, want 10 and 510", got.WordsAdded, got.WordsNet)
	}
	// Typing on after the restore earns only the new words, not the restored ones.
	err = e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		return goals.RecordSave(tx, goals.Save{UserID: w.id, ProjectID: pid, DocumentID: d,
			OldText: older, NewText: older + "three new words", OldWords: 510, NewWords: 513})
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.today(w, pid); got.WordsAdded != 13 || got.WordsNet != 513 {
		t.Errorf("after typing on: added %d net %d, want 13 and 513", got.WordsAdded, got.WordsNet)
	}
}

func TestGoalValidationAndOneGoalPerKind(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	bad := []goals.SetIn{
		{ProjectID: pid, Kind: "weekly", TargetWords: 10},
		{ProjectID: pid, Kind: "daily", TargetWords: 0},
		{ProjectID: pid, Kind: "daily", TargetWords: 100_001},
		{ProjectID: pid, Kind: "project", TargetWords: 10_000_001},
		{ProjectID: pid, Kind: "project", TargetWords: 100, EndDate: "2026-02-30"},
		{ProjectID: pid, Kind: "project", TargetWords: 100, EndDate: "next week"},
	}
	for _, in := range bad {
		if _, err := e.reg.Call(context.Background(), w.p, "goal.set", in); ops.AsError(err).Code != ops.CodeInvalid {
			t.Errorf("%+v: %v", in, err)
		}
	}
	e.setDaily(w, pid, 200)
	e.setDaily(w, pid, 300)
	e.call(w, "goal.set", goals.SetIn{ProjectID: pid, Kind: "project", TargetWords: 80000, EndDate: "2026-12-01"})
	l := e.call(w, "goal.list", goals.ListIn{ProjectID: pid}).(goals.ListOut)
	if len(l.Items) != 2 {
		t.Fatalf("goals: %+v", l.Items)
	}
	for _, g := range l.Items {
		if g.Kind == "daily" && g.TargetWords != 300 {
			t.Errorf("setting again replaces: %+v", g)
		}
	}
	if _, err := e.reg.Call(context.Background(), w.p, "goal.delete", goals.DeleteIn{ProjectID: pid, Kind: "nonsense"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("delete with a bad kind: %v", err)
	}
	if out := e.call(w, "goal.delete", goals.DeleteIn{ProjectID: pid, Kind: "project"}).(goals.DeleteOut); !out.Deleted {
		t.Error("expected deleted")
	}
	if out := e.call(w, "goal.delete", goals.DeleteIn{ProjectID: pid, Kind: "project"}).(goals.DeleteOut); out.Deleted {
		t.Error("deleting twice reports nothing deleted")
	}
}

func TestProjectStatsProjectionNeedsEnoughData(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	e.call(w, "goal.set", goals.SetIn{ProjectID: pid, Kind: "project", TargetWords: 10000, EndDate: "2026-04-30"})

	e.at("2026-03-01T09:00:00Z")
	text := ""
	writeDay := func(day int, n int) {
		e.at(fmt.Sprintf("2026-03-%02dT09:00:00Z", day))
		text += lorem(n, fmt.Sprintf("p%d", day))
		e.save(w, d, text)
	}
	writeDay(1, 500)
	writeDay(2, 500)
	e.at("2026-03-03T08:00:00Z")
	s := e.call(w, "stats.project", goals.StatsIn{ProjectID: pid}).(goals.ProjectOut)
	if s.ProjectedDate != "" {
		t.Errorf("two writing days are not enough to project from: %+v", s)
	}
	if s.Words != 1000 || s.Target != 10000 || s.TargetSource != "goal" || s.Remaining != 9000 || s.Percent != 10 || s.Reached {
		t.Errorf("progress: %+v", s)
	}
	if s.Deadline != "2026-04-30" || s.DaysLeft != 59 || s.RequiredPerDay != 153 {
		t.Errorf("deadline maths: days left %d, required %d (%+v)", s.DaysLeft, s.RequiredPerDay, s)
	}
	for day := 3; day <= 6; day++ {
		writeDay(day, 500)
	}
	e.at("2026-03-07T08:00:00Z")
	s = e.call(w, "stats.project", goals.StatsIn{ProjectID: pid}).(goals.ProjectOut)
	if s.WritingDays != 6 || s.ProjectedDate == "" {
		t.Fatalf("six writing days are enough: %+v", s)
	}
	// 3000 net words over the 28-day window is about 107 a day; 7000 remain.
	if s.AvgNetPerDay != 107 || s.ProjectedDate != "2026-05-12" {
		t.Errorf("projection: avg %d, date %s", s.AvgNetPerDay, s.ProjectedDate)
	}
	// The shared word goal is the fallback when the caller has none of their own.
	e.call(w, "goal.delete", goals.DeleteIn{ProjectID: pid, Kind: "project"})
	e.call(w, "project.update", projects.UpdateIn{ID: pid, WordGoal: ptr(2000)})
	s = e.call(w, "stats.project", goals.StatsIn{ProjectID: pid}).(goals.ProjectOut)
	if s.Target != 2000 || s.TargetSource != "project" || !s.Reached || s.Remaining != 0 || s.ProjectedDate != "" || s.RequiredPerDay != 0 {
		t.Errorf("shared goal reached: %+v", s)
	}
}

func ptr[T any](v T) *T { return &v }

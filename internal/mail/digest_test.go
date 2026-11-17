package mail

import (
	"context"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

const (
	cDoc     = "DOCTITLE-CANARY-7a1"
	cComment = "COMMENTTEXT-CANARY-7a2"
	cManu    = "MANUSCRIPT-CANARY-7a3"
	cLocked  = "LOCKEDPROJECT-CANARY-7a4"
)

// notify plants an unread notification for a person, the way a comment or a mention does.
func (w *world) notify(user person, project, kind string, n int, at time.Time) {
	w.t.Helper()
	if _, err := w.d.Writer().Exec(`INSERT INTO notifications(id, user_id, project_id, kind, actor_id, count, created_at) VALUES (?,?,?,?,?,?,?)`,
		db.NewID("ntf"), user.id, project, kind, w.bob.id, n, db.Format(at)); err != nil {
		w.t.Fatal(err)
	}
}

// plant puts canaries where a digest must never reach: a document's title and text, a comment's words.
func (w *world) plant(project string) {
	w.t.Helper()
	doc := db.NewID("doc")
	now := db.Now()
	if _, err := w.d.Writer().Exec(`INSERT INTO documents(id, project_id, kind, title, content, created_at, updated_at) VALUES (?,?,'chapter',?,?,?,?)`, doc, project, cDoc, cManu, now, now); err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.d.Writer().Exec(`INSERT INTO comments(id, project_id, document_id, author_id, body, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`, db.NewID("cmt"), project, doc, w.bob.id, cComment, now, now); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) setZone(p person, tz string) {
	w.t.Helper()
	if _, err := w.d.Writer().Exec(`UPDATE users SET timezone = ? WHERE id = ?`, tz, p.id); err != nil {
		w.t.Fatal(err)
	}
}

func decode(t *testing.T, raw string) (mail.Header, string) {
	return bodyOf(t, raw)
}

func TestTheDigestSaysCountsAndProjectTitlesOnly(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setZone(w.alice, "America/New_York")
	w.plant(w.project)
	locked := w.addProject(cLocked, w.alice, map[person]string{w.bob: "editor"})
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	// What happened after she turned it on.
	at := w.clk.Now().Add(time.Hour)
	w.notify(w.alice, w.project, "comment", 1, at)
	w.notify(w.alice, w.project, "comment", 3, at) // a guest's grouped comments count as 3
	w.notify(w.alice, w.project, "reply", 1, at)
	w.notify(w.alice, w.project, "mention", 1, at)
	w.notify(w.alice, w.project, "suggestion", 2, at)
	w.notify(w.alice, locked, "comment", 4, at)
	w.clk.Advance(24 * time.Hour)
	// A project locked behind a passphrase is left out by the one function that decides visibility.
	w.svc.o.VisibleProjects = func(ctx context.Context, user string) (map[string]string, error) {
		m, err := w.svc.defaultVisible(ctx, user)
		delete(m, locked)
		return m, err
	}
	if n := w.svc.DigestOnce(context.Background()); n != 1 {
		t.Fatalf("queued %d", n)
	}
	r := w.onlyRow()
	if r.Recipient != "alice@example.com" || r.Subject != DigestSubject || r.Kind != KindDigest {
		t.Fatalf("%+v", r)
	}
	for _, bad := range []string{cDoc, cComment, cManu, cLocked, "Bob", "bob@", "Ada", "Carol"} {
		if strings.Contains(r.Body, bad) || strings.Contains(r.Subject, bad) {
			t.Errorf("the digest carries %q:\n%s", bad, r.Body)
		}
	}
	for _, want := range []string{"Salt Road", "1 mention", "5 comments", "2 suggestions", "8 new notifications"} {
		if !strings.Contains(r.Body, want) {
			t.Errorf("the digest lacks %q:\n%s", want, r.Body)
		}
	}
	// Every address in it is the app's own base address.
	for _, u := range regexp.MustCompile(`https?://\S+`).FindAllString(r.Body, -1) {
		if !strings.HasPrefix(u, "https://scriobh.example.org/") {
			t.Errorf("a foreign link: %s", u)
		}
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
	if strings.Contains(strings.ToLower(m[0].Data), "text/html") || strings.Contains(body, "<") {
		t.Error("the digest is not plain text")
	}
	if !strings.Contains(body, "Salt Road") {
		t.Errorf("body: %q", body)
	}
}

func TestADigestWithNothingNewIsNotSentAndThePeriodIsDone(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	// Before the person's first morning after turning it on: nothing is due.
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Fatal("a digest in the period she turned it on")
	}
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 0 || len(w.rows()) != 0 {
		t.Fatal("an empty digest was made")
	}
	var last string
	_ = w.d.Read().QueryRow(`SELECT last_period FROM mail_digest WHERE user_id = ?`, w.alice.id).Scan(&last)
	if last != "2026-06-02" {
		t.Errorf("the period was not marked done: %q", last)
	}
	// Already read, or older than the window, or on a trashed project: nothing to say either.
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(-3*time.Hour))
	if _, err := w.d.Writer().Exec(`UPDATE notifications SET read_at = ?`, db.Now()); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Error("a digest of what was already read")
	}
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(-time.Hour))
	if _, err := w.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, db.Now(), w.project); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Error("a digest about a trashed project")
	}
}

func TestADigestIsMadeOncePerPeriodEvenAcrossARestart(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	for i := 0; i < 3; i++ {
		w.svc.DigestOnce(context.Background())
	}
	if len(w.rows()) != 1 {
		t.Fatalf("%d digests in a period", len(w.rows()))
	}
	// A restart: a new service on the same database.
	svc2 := New(Options{DB: w.d, Box: w.box, DataDir: w.dir, Now: w.clk.Now, Rand: func() float64 { return 0.5 }})
	svc2.DigestOnce(context.Background())
	w.clk.Advance(2 * time.Hour)
	svc2.DigestOnce(context.Background())
	if len(w.rows()) != 1 {
		t.Fatalf("a restart made %d digests", len(w.rows()))
	}
	// The dedupe key alone also holds, if the schedule row were lost.
	if _, err := w.d.Writer().Exec(`UPDATE mail_digest SET last_period = '2026-05-01'`); err != nil {
		t.Fatal(err)
	}
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(-time.Minute))
	_, _ = w.d.Writer().Exec(`UPDATE mail_digest SET last_cursor = ?`, db.Format(w.clk.Now().Add(-time.Hour)))
	svc2.DigestOnce(context.Background())
	if len(w.rows()) != 1 {
		t.Errorf("the dedupe key did not hold: %d", len(w.rows()))
	}
	// Next day, new news: the next digest.
	w.notify(w.alice, w.project, "mention", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	svc2.DigestOnce(context.Background())
	if len(w.rows()) != 2 {
		t.Errorf("%d digests after the next day", len(w.rows()))
	}
}

func TestADigestIsDueInThePersonsMorningAndWeeklyOnMonday(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setZone(w.alice, "Pacific/Auckland") // UTC+12 in June
	w.setZone(w.bob, "America/Los_Angeles")
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.must(w.bob, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Weekly})
	for _, p := range []person{w.alice, w.bob} {
		w.notify(p, w.project, "comment", 1, w.clk.Now().Add(time.Minute))
	}
	// 2026-06-01 12:00 UTC is Monday. Alice, 12 hours ahead, turned it on at midnight Tuesday; her next morning is Wednesday 07:00, 2026-06-02 19:00 UTC.
	w.clk.Advance(29 * time.Hour) // 2026-06-02 17:00 UTC: 05:00 on Wednesday in Auckland, before her slot
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Fatal("before her morning")
	}
	w.clk.Advance(2*time.Hour + 31*time.Minute) // 19:31 UTC: after 07:00 + the most jitter
	if n := w.svc.DigestOnce(context.Background()); n != 1 {
		t.Fatalf("queued %d for her morning", n)
	}
	if r := w.rows(); len(r) != 1 || r[0].Person != w.alice.id {
		t.Fatalf("%+v", r)
	}
	// Bob's weekly digest is next Monday morning, Los Angeles: 2026-06-08 14:00 UTC plus jitter.
	w.clk.Advance(6 * 24 * time.Hour)
	w.notify(w.bob, w.project, "mention", 1, w.clk.Now().Add(-time.Hour))
	if n := w.svc.DigestOnce(context.Background()); n != 1 {
		t.Errorf("bob's weekly digest: %d", n)
	}
	if r := w.rows(); len(r) != 2 || r[1].Person != w.bob.id {
		t.Errorf("rows: %+v", r)
	}
}

func TestADigestGoesToTheAccountAddressOnlyAndOnlyToActivePeople(t *testing.T) {
	w := newWorld(t)
	w.configure()
	// No address: refused.
	if _, err := w.d.Writer().Exec(`UPDATE users SET email = NULL WHERE id = ?`, w.carol.id); err != nil {
		t.Fatal(err)
	}
	if _, err := w.call(w.carol, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily}); err == nil {
		t.Error("a digest with no address")
	}
	// There is no field that names an address.
	if s := jsonOf(t, DigestSetIn{}); strings.Contains(s, "address") || strings.Contains(s, "email") {
		t.Errorf("DigestSetIn: %s", s)
	}
	if _, err := w.reg.CallJSON(context.Background(), w.alice.p, "mail.digest.set", []byte(`{"enabled":true,"email":"x@y.z"}`)); err == nil {
		t.Error("an address in the request")
	}
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.must(w.bob, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.notify(w.bob, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	// Bob is disabled; Alice changed her address after the digest was queued.
	if _, err := w.d.Writer().Exec(`UPDATE users SET disabled_at = ? WHERE id = ?`, db.Now(), w.bob.id); err != nil {
		t.Fatal(err)
	}
	if n := w.svc.DigestOnce(context.Background()); n != 1 {
		t.Fatalf("queued %d", n)
	}
	if _, err := w.d.Writer().Exec(`UPDATE users SET email = 'new-alice@example.com' WHERE id = ?`, w.alice.id); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if len(w.smtp.Mails()) != 0 {
		t.Errorf("sent %+v", w.smtp.Mails()[0].To)
	}
	if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" {
		t.Errorf("%+v", r)
	}
}

func TestATitleCannotInjectHeadersOrLinksIntoADigest(t *testing.T) {
	w := newWorld(t)
	w.configure()
	evil := w.addProject("Hello\r\nBcc: attacker@example.net\r\n\r\n.\r\nQUIT\r\n see https://evil.example/x and www.evil.example", w.alice, map[person]string{w.bob: "editor"})
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, evil, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 1 {
		t.Fatal("not queued")
	}
	w.pump()
	m := w.smtp.Mails()
	if len(m) != 1 {
		t.Fatalf("%d messages", len(m))
	}
	msg, err := mail.ReadMessage(strings.NewReader(m[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.Header["Bcc"]) != 0 || msg.Header.Get("To") != "alice@example.com" {
		t.Errorf("headers: %v", msg.Header)
	}
	if strings.Count(strings.ToLower(m[0].Data), "\nto:") != 1 {
		t.Error("more than one To header")
	}
	b, _ := io.ReadAll(msg.Body)
	_ = b
	_, body := decode(t, m[0].Data)
	if strings.Contains(body, "\nBcc:") || strings.Contains(body, "\nQUIT") || strings.Contains(body, "https://evil") || strings.Contains(body, "www.evil") || strings.Contains(body, "://evil") {
		t.Errorf("a title came through whole:\n%s", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Errorf("the title is gone:\n%s", body)
	}
	// What the relay's DATA saw was a single message: nothing after the end of it.
	if len(w.smtp.Mails()) != 1 {
		t.Error("the title ended the message early")
	}
}

func TestNoLinksWhenTheBaseAddressIsNotOneToPutInMail(t *testing.T) {
	for _, base := range []string{"", "http://127.0.0.1:8080", "http://localhost:8080", "ftp://x.example", "https://user:pw@x.example", "https://x.example/?a=b"} {
		w := newWorld(t, func(o *wopt) { o.base = base })
		w.configure()
		w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
		w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
		w.clk.Advance(24 * time.Hour)
		w.svc.DigestOnce(context.Background())
		r := w.onlyRow()
		if strings.Contains(r.Body, "http") {
			t.Errorf("base %q: a link in the digest:\n%s", base, r.Body)
		}
		w.pump()
		h, _ := decode(t, w.smtp.Mails()[0].Data)
		if h.Get("List-Unsubscribe") != "" {
			t.Errorf("base %q: List-Unsubscribe %q", base, h.Get("List-Unsubscribe"))
		}
	}
	if got := cleanBase("https://scriobh.example.org/app/"); got != "https://scriobh.example.org/app" {
		t.Errorf("cleanBase = %q", got)
	}
	if got := cleanBase("http://192.168.1.20:8080"); got == "" {
		t.Error("a LAN address is a fine link for a person on the LAN")
	}
}

func TestSwitchingMailOnStartsDigestsFreshAndOptingOutStopsThem(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.must(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(false)})
	w.clk.Advance(3 * 24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Fatal("a digest with mail off")
	}
	w.must(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(true)})
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Fatal("a backlog digest after switching mail on")
	}
	// Opting out drops the subscription and what was queued for it.
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	w.svc.DigestOnce(context.Background())
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: false})
	if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" {
		t.Errorf("%+v", r)
	}
	if got := w.must(w.alice, "mail.digest.get", DigestGetIn{}).(DigestOut); got.Enabled {
		t.Errorf("%+v", got)
	}
	w.pump()
	if len(w.smtp.Mails()) != 0 {
		t.Error("sent after opting out")
	}
	// Turning it on again while it is on leaves the schedule alone.
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	var before string
	_ = w.d.Read().QueryRow(`SELECT last_cursor FROM mail_digest WHERE user_id = ?`, w.alice.id).Scan(&before)
	w.clk.Advance(time.Hour)
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	var after string
	_ = w.d.Read().QueryRow(`SELECT last_cursor FROM mail_digest WHERE user_id = ?`, w.alice.id).Scan(&after)
	if before != after {
		t.Error("turning the digest on again moved the window")
	}
}

// A digest is made again when it is sent: a project trashed, locked or read in the meantime is not named, and a
// digest with nothing left to say is not sent.
func TestADigestIsBuiltAgainAtSendTime(t *testing.T) {
	w := newWorld(t)
	w.configure()
	second := w.addProject("Second Book", w.alice, nil)
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	at := w.clk.Now().Add(time.Hour)
	w.notify(w.alice, w.project, "comment", 1, at)
	w.notify(w.alice, second, "comment", 1, at)
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 1 {
		t.Fatal("not queued")
	}
	if !strings.Contains(w.onlyRow().Body, "Second Book") {
		t.Fatal("the queued digest does not name both")
	}
	// While the relay was down, one project went to the trash.
	if _, err := w.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, db.Now(), second); err != nil {
		t.Fatal(err)
	}
	w.pump()
	m := w.smtp.Mails()
	if len(m) != 1 {
		t.Fatalf("%d messages", len(m))
	}
	if _, body := decode(t, m[0].Data); strings.Contains(body, "Second Book") || !strings.Contains(body, "Salt Road") {
		t.Errorf("a digest sent after a project was trashed:\n%s", body)
	}
	// And one whose news was all read before it was sent is dropped.
	w.clk.Advance(24 * time.Hour)
	w.notify(w.alice, w.project, "mention", 1, w.clk.Now().Add(-time.Hour))
	if w.svc.DigestOnce(context.Background()) != 1 {
		t.Fatal("not queued")
	}
	if _, err := w.d.Writer().Exec(`UPDATE notifications SET read_at = ?`, db.Now()); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if len(w.smtp.Mails()) != 1 {
		t.Error("a digest of what was read in the meantime was sent")
	}
	rows := w.rows()
	if last := rows[len(rows)-1]; last.State != "dead" || last.Err != "cancelled" {
		t.Errorf("%+v", last)
	}
}

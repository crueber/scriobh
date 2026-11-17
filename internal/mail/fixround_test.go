package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func strs(v ...string) *[]string { return &v }

// ---- H1: the Kindle domain allow-list -------------------------------------------------------------------------

func TestTheKindleDomainIsOnTheAdministratorsAllowList(t *testing.T) {
	w := newWorld(t)
	w.configure()
	// The default is exactly kindle.com and free.kindle.com.
	if got := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut).KindleDomains; strings.Join(got, ",") != "kindle.com,free.kindle.com" {
		t.Fatalf("default domains: %v", got)
	}
	for _, ok := range []string{"a@kindle.com", "a@free.kindle.com", "A@Kindle.COM"} {
		if _, err := w.call(w.bob, "kindle.address.set", KindleAddressSetIn{Address: ok}); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"a@evil.kindle.com", "a@kindle.com.evil.example", "a@xkindle.com", "a@gmail.com", "a@kindle.co", "a@kindle.com.", "a@example.com"} {
		if _, err := w.call(w.bob, "kindle.address.set", KindleAddressSetIn{Address: bad}); err == nil {
			t.Errorf("%s was stored", bad)
		}
	}
	// "Any" is never an option.
	for _, bad := range [][]string{{"*"}, {"*.com"}, {"any"}, {""}, {"a@b.com"}, {"kindle.com", "*"}, {"127.0.0.1"}, {"a b.com"}} {
		if _, err := w.call(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: &bad}); err == nil {
			t.Errorf("the list %v was accepted", bad)
		}
	}
	// Widened to an allow-list, a person's address on it is accepted, and on nothing else.
	w.must(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: strs("example.com", "Example.com", "kindle.com"), ConfirmDomains: []string{"example.com"}})
	if got := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut).KindleDomains; strings.Join(got, ",") != "example.com,kindle.com" {
		t.Errorf("%v", got)
	}
	w.setKindle(w.bob, "bob@example.com")
	if _, err := w.call(w.bob, "kindle.address.set", KindleAddressSetIn{Address: "bob@other.example"}); err == nil {
		t.Error("a domain off the list")
	}
	// An empty list is the default again.
	w.must(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: strs()})
	if got := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut).KindleDomains; strings.Join(got, ",") != "kindle.com,free.kindle.com" {
		t.Errorf("%v", got)
	}
	// An address stored under the wider list is refused at the preview, and a queued one is cancelled at send time.
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Data["reason"] != "kindle_domain" {
		t.Errorf("preview with an address off the list: %+v", e)
	}
	w.must(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: strs("example.com"), ConfirmDomains: []string{"example.com"}})
	pv := w.preview(w.bob)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	w.must(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: strs("kindle.com"), ConfirmDomains: []string{"kindle.com"}}) // the administrator tightens it
	w.pump()
	if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" {
		t.Errorf("%+v", r)
	}
	if len(w.smtp.Mails()) != 0 {
		t.Error("sent to a domain that left the list")
	}
	// And send itself refuses a token whose address is no longer allowed.
	w.must(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: strs("example.com"), ConfirmDomains: []string{"example.com"}})
	pv = w.preview(w.bob)
	w.must(w.admin, "mail.config.set", ConfigSetIn{KindleDomains: strs("kindle.com"), ConfirmDomains: []string{"kindle.com"}})
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}))); e.Data["reason"] != "kindle_domain" {
		t.Errorf("send with an address off the list: %+v", e)
	}
}

func TestTheSendLogKeepsTheFullAddressSealedForTheOwnerAndAdministrators(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	if strings.Contains(w.dump(), "bob_kindle") {
		t.Error("the mailbox is in the database in the clear")
	}
	show := func(p person) KindleLogEntry {
		lg := w.must(p, "kindle.log", KindleProjectIn{ProjectID: w.project}).(KindleLogOut)
		if len(lg.Items) != 1 {
			t.Fatalf("%+v", lg)
		}
		return lg.Items[0]
	}
	if got := show(w.alice).Recipient; got != "bob_kindle@kindle.com" {
		t.Errorf("the owner sees %q", got)
	}
	if got := show(w.admin).Recipient; got != "bob_kindle@kindle.com" {
		t.Errorf("an administrator sees %q", got)
	}
	if got := show(w.bob); got.Recipient != "" || got.RecipientDomain != "kindle.com" {
		t.Errorf("an editor sees %+v", got)
	}
	// Changing the address later does not change what the log says went where.
	w.setKindle(w.bob, "other@kindle.com")
	if got := show(w.alice).Recipient; got != "bob_kindle@kindle.com" {
		t.Errorf("after a change: %q", got)
	}
}

// ---- G1: the project passphrase gate ---------------------------------------------------------------------------

func (w *world) gate(project string, updated time.Time) {
	w.t.Helper()
	if _, err := w.d.Writer().Exec(`INSERT INTO project_gates(project_id, pass_hash, created_at, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(project_id) DO UPDATE SET updated_at = excluded.updated_at`, project, "hash", db.Format(updated), db.Format(updated)); err != nil {
		w.t.Fatal(err)
	}
}

func TestADigestNeverNamesAProjectWithAPassphrase(t *testing.T) {
	w := newWorld(t)
	w.configure()
	locked := w.addProject("LOCKED-TITLE-CANARY", w.alice, nil)
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	at := w.clk.Now().Add(time.Hour)
	w.notify(w.alice, w.project, "comment", 1, at)
	w.notify(w.alice, locked, "comment", 7, at)
	w.gate(locked, w.clk.Now())
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 1 {
		t.Fatal("not queued")
	}
	if r := w.onlyRow(); strings.Contains(r.Body, "LOCKED-TITLE-CANARY") || strings.Contains(r.Body, "7 ") || !strings.Contains(r.Body, "Salt Road") {
		t.Errorf("the digest:\n%s", r.Body)
	}
	// Only a protected project's news: nothing to say, nothing sent.
	w.clk.Advance(24 * time.Hour)
	w.notify(w.alice, locked, "mention", 1, w.clk.Now().Add(-time.Hour))
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Error("a digest of a protected project")
	}
	// A passphrase set while a digest waited: it is built again at send time and the project is gone from it.
	w.clk.Advance(24 * time.Hour)
	w.notify(w.alice, w.project, "mention", 1, w.clk.Now().Add(-time.Hour))
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(-time.Hour))
	if w.svc.DigestOnce(context.Background()) != 1 {
		t.Fatal("not queued")
	}
	w.gate(w.project, w.clk.Now().Add(time.Second))
	before := len(w.smtp.Mails())
	w.pump()
	if len(w.smtp.Mails()) != before {
		t.Error("a digest was sent about a project that got a passphrase while it waited")
	}
	rows := w.rows()
	if last := rows[len(rows)-1]; last.State != "dead" || last.Err != "cancelled" {
		t.Errorf("%+v", last)
	}
}

func TestAKindleEditionQueuedBeforeAPassphraseIsNotSentAfterIt(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	// A passphrase that was already there when the person sent (they had unlocked the project): it goes.
	w.gate(w.project, w.clk.Now().Add(-time.Hour))
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	if len(w.smtp.Mails()) != 1 {
		t.Fatalf("an edition sent under an older passphrase was held back: %d", len(w.smtp.Mails()))
	}
	// A passphrase set, or changed, after it was queued: it does not.
	w.clk.Advance(time.Minute)
	w.clk.Advance(0)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.clk.Advance(time.Second)
	w.gate(w.project, w.clk.Now()) // the owner changed the passphrase
	w.pump()
	if len(w.smtp.Mails()) != 1 {
		t.Errorf("an edition went out after the passphrase changed: %d", len(w.smtp.Mails()))
	}
	rows := w.rows()
	if last := rows[len(rows)-1]; last.State != "dead" || last.Err != "cancelled" {
		t.Errorf("%+v", last)
	}
	// And a gate that cannot be read fails closed.
	w2 := newWorld(t)
	w2.configure()
	w2.setKindle(w2.bob, "bob_kindle@kindle.com")
	w2.must(w2.bob, "kindle.send", KindleSendIn{ProjectID: w2.project, Token: w2.preview(w2.bob).Token})
	if _, err := w2.d.Writer().Exec(`ALTER TABLE project_gates RENAME TO project_gates_gone`); err != nil {
		t.Fatal(err)
	}
	w2.pump()
	if len(w2.smtp.Mails()) != 0 {
		t.Error("sent although the gate could not be read")
	}
}

// ---- M1: the settings are read for every message ---------------------------------------------------------------

func TestARelayChangedMidBatchIsUsedForTheRestOfTheBatch(t *testing.T) {
	var w *world
	n := 0
	w = newWorld(t, func(o *wopt) {
		o.afterSend = func(string) error {
			if n++; n == 1 { // after the first message, the administrator changes the relay's password
				_, err := w.call(w.admin, "mail.config.set", ConfigSetIn{Password: str("changed-" + relayPW)})
				if err != nil {
					t.Error(err)
				}
			}
			return nil
		}
	})
	w.configure()
	for _, p := range []person{w.bob, w.alice} {
		w.setKindle(p, p.email[:strings.Index(p.email, "@")]+"@kindle.com")
		w.must(p, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(p).Token})
	}
	w.pump()
	if got := len(w.smtp.Mails()); got != 1 {
		t.Errorf("%d messages: the second went to the relay with the old password", got)
	}
	for _, r := range w.rows() {
		if r.State == "sent" {
			continue
		}
		if r.State != "pending" || r.Attempts > 1 {
			t.Errorf("%+v", r)
		}
	}
}

// ---- M2: the digest is bound to the address it was turned on for ----------------------------------------------

func TestADigestStopsWhenTheAccountAddressChangesUntilTheyTurnItOnAgain(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	if _, err := w.d.Writer().Exec(`UPDATE users SET email = 'new-alice@example.net' WHERE id = ?`, w.alice.id); err != nil {
		t.Fatal(err)
	}
	got := w.must(w.alice, "mail.digest.get", DigestGetIn{}).(DigestOut)
	if got.Enabled || !got.NeedsConfirm || got.Email != "new-alice@example.net" {
		t.Fatalf("%+v", got)
	}
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 0 {
		t.Fatal("a digest to an address nobody opted in with")
	}
	w.clk.Advance(24 * time.Hour)
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily}) // they confirm the new address
	w.notify(w.alice, w.project, "mention", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(24 * time.Hour)
	if w.svc.DigestOnce(context.Background()) != 1 {
		t.Fatal("not queued after they turned it on again")
	}
	w.pump()
	if m := w.smtp.Mails(); len(m) != 1 || m[0].To != "new-alice@example.net" {
		t.Fatalf("%+v", m)
	}
}

func TestADigestStopsForAPersonTheIdentityProviderRemoved(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.alice, "alice@kindle.com")
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	users := &auth.Users{DB: w.d, Cfg: testutil.Config(t, nil)}
	if _, err := users.SignIn(context.Background(), "test", auth.Claims{Subject: "sub-" + w.alice.id, Email: w.alice.email, EmailVerified: true}); err == nil {
		t.Fatal("signed in with no group")
	}
	var n int
	_ = w.d.Read().QueryRow(`SELECT count(*) FROM mail_digest WHERE user_id = ?`, w.alice.id).Scan(&n)
	if n != 0 {
		t.Error("the subscription of a person who lost every group stayed")
	}
}

func TestADigestGoesToNoOneWhoIsDisabledDeletedAbsentOrAnAgent(t *testing.T) {
	for name, q := range map[string]string{
		"disabled": `UPDATE users SET disabled_at = '2026-06-02T00:00:00.000Z' WHERE id = ?`,
		"deleted":  `UPDATE users SET deleted_at = '2026-06-02T00:00:00.000Z' WHERE id = ?`,
		"agent":    `UPDATE users SET is_agent = 1 WHERE id = ?`,
		"absent":   `UPDATE users SET last_seen_at = '2026-01-01T00:00:00.000Z' WHERE id = ?`, // 90 days before the fake clock is 2026-03-03
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.configure()
			w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
			w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
			w.clk.Advance(24 * time.Hour)
			if name == "absent" {
				w.clk.Advance(100 * 24 * time.Hour)
				w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(-time.Hour))
			}
			if _, err := w.d.Writer().Exec(q, w.alice.id); err != nil {
				t.Fatal(err)
			}
			if w.svc.DigestOnce(context.Background()) != 0 {
				t.Error("queued")
			}
			w.pump()
			if len(w.smtp.Mails()) != 0 {
				t.Error("sent")
			}
		})
	}
}

func TestAnOmittedDigestFrequencyIsRefused(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Weekly})
	if _, err := w.call(w.alice, "mail.digest.set", DigestSetIn{Enabled: true}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("an omitted frequency: %v", err)
	}
	if got := w.must(w.alice, "mail.digest.get", DigestGetIn{}).(DigestOut); got.Frequency != Weekly {
		t.Errorf("a refused call changed the subscription: %+v", got)
	}
	if _, err := w.reg.CallJSON(context.Background(), w.alice.p, "mail.digest.set", []byte(`{"enabled":true}`)); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("over JSON: %v", err)
	}
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: false}) // turning it off needs no frequency
}

// ---- M3: the restore pause is fail-safe --------------------------------------------------------------------------

func TestARestoreThatCannotFinishLeavesAMarkerThatPausesMailAtStart(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	// What Restore does first: the hooks that write the markers, right after the files are in place.
	for _, h := range backup.PostInstall {
		if err := h(w.dir); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(w.dir, MarkerName)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	// The database cannot be reopened, so no step runs. The next start pauses mail all the same.
	svc := New(Options{DB: w.d, Box: w.box, DataDir: w.dir, Now: w.clk.Now, Client: w.svc.client})
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx)
	cancel()
	svc.Wait()
	cfg := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if cfg.Enabled || !cfg.PausedByRestore {
		t.Errorf("%+v", cfg)
	}
	if len(w.rows()) != 0 {
		t.Errorf("the outbox was not emptied: %+v", w.rows())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the marker stayed after mail was paused")
	}
	if len(w.smtp.Mails()) != 0 {
		t.Error("sent")
	}
}

func TestARestoreStepThatFailsRetriesOnceThenFailsLoudly(t *testing.T) {
	w := newWorld(t)
	w.configure()
	for _, h := range backup.PostInstall {
		if err := h(filepath.Dir(w.d.Path)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	old := pauseFn
	defer func() { pauseFn = old }()
	pauseFn = func(ctx context.Context, d *db.DB) (int, error) {
		calls++
		return 0, errors.New("disk is full")
	}
	_, err := stepOf(t)(context.Background(), w.d, nil)
	if err == nil || calls != 2 {
		t.Fatalf("err=%v after %d tries", err, calls)
	}
	for _, want := range []string{"mail is NOT paused", "UPDATE mail_config SET enabled = 0", MarkerName} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.d.Path), MarkerName)); err != nil {
		t.Error("the marker was removed although mail is not paused")
	}
	// One failure, then success: paused, no error, the marker gone.
	calls = 0
	pauseFn = func(ctx context.Context, d *db.DB) (int, error) {
		if calls++; calls == 1 {
			return 0, errors.New("database is locked")
		}
		return old(ctx, d)
	}
	if _, err := stepOf(t)(context.Background(), w.d, nil); err != nil {
		t.Fatal(err)
	}
	if cfg := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut); cfg.Enabled || !cfg.PausedByRestore {
		t.Errorf("%+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(w.d.Path), MarkerName)); !os.IsNotExist(err) {
		t.Error("the marker stayed after the pause")
	}
}

// stepOf finds the mail step among the post-restore steps: the one that answers with mail's own error text.
func stepOf(t *testing.T) func(ctx context.Context, d *db.DB, key []byte) (string, error) {
	t.Helper()
	return func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		var errs []error
		var note string
		for _, s := range backup.PostRestore {
			m, err := s(ctx, d, key)
			if err != nil {
				errs = append(errs, err)
			}
			note += m
		}
		return note, errors.Join(errs...)
	}
}

// ---- M4 and the lows ---------------------------------------------------------------------------------------------

func TestTheClaimClearsTheMarkOfAnEarlierAttempt(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	if _, err := w.d.Writer().Exec(`UPDATE mail_outbox SET data_started_at = '2026-06-01T11:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	w.svc.o.AfterSend = func(string) error { return errors.New("stop") } // leave it as the claim made it
	w.svc.client = &Client{Idle: time.Second, Overall: 3 * time.Second}
	w.smtp.mu.Lock()
	w.smtp.Replies["RCPT"] = "451 later"
	w.smtp.mu.Unlock()
	w.pump()
	if r := w.onlyRow(); r.DataStarted != "" {
		t.Errorf("the mark of an earlier attempt survived: %+v", r)
	}
}

func TestTheCallerKindForSessionOnlyOpsIsCheckedPositively(t *testing.T) {
	w := newWorld(t)
	for _, c := range []struct {
		name string
		mod  func(*ops.Principal)
		ok   bool
	}{
		{"a browser session", func(p *ops.Principal) { p.Via = "session" }, true},
		{"the operator's shell", func(p *ops.Principal) { p.Via = "cli" }, true},
		{"a key", func(p *ops.Principal) { p.Via = "apikey"; p.KeyID = "key_1" }, false},
		{"a key with no kind", func(p *ops.Principal) { p.Via = ""; p.KeyID = "key_1"; p.Scope = "admin" }, false},
		{"a kind added later", func(p *ops.Principal) { p.Via = "mcp" }, false},
	} {
		p := w.admin.p
		c.mod(&p)
		_, err := w.reg.Call(context.Background(), p, "mail.config.get", ConfigGetIn{})
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestALongSenderNameIsFoldedUnderTheLineLimit(t *testing.T) {
	m := testMsg()
	m.FromName = strings.Repeat("😀", 80)
	raw := build(t, m)
	for _, l := range strings.Split(raw, "\r\n") {
		if len(l) > 998 {
			t.Fatalf("a line of %d octets", len(l))
		}
	}
	if _, err := mailReadFrom(raw); err != nil {
		t.Errorf("the message does not parse: %v", err)
	}
}

// A restore's marker whose pause fails is a latch: nothing is sent or made until a pause succeeds, however long it
// takes and whatever an administrator does meanwhile (a timed hold would have lapsed, or been cleared by a setting).
func TestAMarkerWhosePauseFailsLatchesSendingUntilItSucceeds(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	if err := os.WriteFile(filepath.Join(w.dir, MarkerName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := pauseFn
	defer func() { pauseFn = old }()
	failing := true
	pauseFn = func(ctx context.Context, d *db.DB) (int, error) {
		if failing {
			return 0, errors.New("database is locked")
		}
		return old(ctx, d)
	}
	w.svc.honourMarker(context.Background())
	for day := 0; day < 3; day++ {
		w.clk.Advance(24 * time.Hour)
		w.must(w.admin, "mail.config.set", ConfigSetIn{FromName: str("Else")}) // clears any timed hold, changes nothing about the pause
		w.svc.ClearHold()
		if w.pump() != 0 || w.svc.DigestOnce(context.Background()) != 0 {
			t.Fatalf("work while the marker could not be honoured, day %d", day)
		}
	}
	if len(w.smtp.Mails()) != 0 || w.smtp.Conns() != 0 {
		t.Fatal("the relay was used")
	}
	failing = false
	if w.pump() != 0 {
		t.Error("work on the pass that paused")
	}
	if cfg := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut); cfg.Enabled || !cfg.PausedByRestore {
		t.Errorf("%+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(w.dir, MarkerName)); !os.IsNotExist(err) {
		t.Error("the marker stayed after the pause")
	}
	if len(w.smtp.Mails()) != 0 {
		t.Error("sent")
	}
}

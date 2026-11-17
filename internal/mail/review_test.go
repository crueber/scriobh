package mail

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/testutil"
)

// R-ADDR: a domain whose last label is all digits is not a domain a mail can go to; the relay would read
// "127.1" as an address on its own machine.
func TestReviewNumericTopLevelDomainIsRefused(t *testing.T) {
	for _, a := range []string{"x@127.1", "x@10.1", "x@192.168.1.1.2", "x@0.0"} {
		if _, err := ValidAddress(a); err == nil {
			t.Errorf("ValidAddress accepted %q", a)
		}
	}
}

// R-DIGEST: the digest is opted in with the address on the account then; an address that changes afterwards
// (an IdP claim, which sign-in copies into users.email on every login) must not silently re-point it.
func TestReviewDigestDoesNotFollowAChangedAccountAddress(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	// The same person (same subject), the identity provider now claims another verified address: sign-in copies it.
	users := &auth.Users{DB: w.d, Cfg: testutil.Config(t, nil)}
	if _, err := users.SignIn(context.Background(), "test", auth.Claims{Subject: "sub-" + w.alice.id, Email: "someone-else@example.net", EmailVerified: true, Groups: []string{"scriobh-writers"}}); err != nil {
		t.Fatal(err)
	}
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(26 * time.Hour)
	w.svc.DigestOnce(context.Background())
	w.pump()
	for _, m := range w.smtp.Mails() {
		if m.To == "someone-else@example.net" {
			t.Fatalf("Alice's digest (her project titles and counts) went to the address the IdP claimed later: %s", m.To)
		}
	}
}

// R-RETRY: data_started_at is written before DATA is even asked for. A relay that answers the DATA command with a
// 4xx (greylisting at DATA, a content filter) has certainly NOT taken the message, the row goes back to pending,
// and the stale mark makes a later restart give it up as delivery_unknown.
func TestReviewAStaleDataMarkDoesNotTurnARefusedMessageIntoUnknown(t *testing.T) {
	w := newWorld(t, func(o *wopt) {
		o.smtpMod = append(o.smtpMod, func(f *fakeSMTP) { f.Replies["DATA"] = "451 4.7.1 try later" })
	})
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	r := w.onlyRow()
	if r.State != "pending" {
		t.Fatalf("expected a retry, got %+v", r)
	}
	// The relay recovers. The next attempt is claimed and the process dies before it connects.
	if _, err := w.d.Writer().Exec(`UPDATE mail_outbox SET state = 'sending', claimed_at = ?, attempts = attempts + 1`, w.clk.Now().Format("2006-01-02T15:04:05.000Z")); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.onlyRow(); got.State != "pending" {
		t.Errorf("a message that was refused at DATA and never offered again was given up as %s/%s after a restart (data_started_at=%q is from the refused attempt)", got.State, got.Err, got.DataStarted)
	}
}

// R-HEADER: a header line may not be longer than 998 octets (RFC 5322 2.1.1). The sender's display name is up to
// 80 characters; 80 four-byte characters encode to a From line well past that.
func TestReviewFromLineStaysUnderTheLineLimit(t *testing.T) {
	m := &Message{From: "a@example.org", FromName: strings.Repeat("\U0001F600", 80), To: "b@example.org", Subject: "s", MessageID: "x@example.org",
		Date: time.Unix(1700000000, 0), Body: "hi"}
	var b bytes.Buffer
	if err := m.Write(&b); err != nil {
		t.Fatal(err)
	}
	for _, l := range strings.Split(b.String(), "\r\n") {
		if len(l) > 998 {
			t.Errorf("a header line is %d octets: %.60s...", len(l), l)
		}
	}
}

// R-SWITCH: DispatchOnce claims up to five rows and then delivers them one by one from the settings it read
// before the first. Switching mail off marks the rows dead, but a row already claimed is delivered anyway.
// (AfterSend runs after the first message has been sent; the administrator switches mail off there.)
func TestReviewSwitchingMailOffStopsAClaimedMessage(t *testing.T) {
	var w *world
	switched := false
	w = newWorld(t, func(o *wopt) {
		o.afterSend = func(string) error {
			if !switched {
				switched = true
				w.must(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(false)})
			}
			return nil
		}
	})
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.must(w.bob, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.notify(w.bob, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(26 * time.Hour)
	if n := w.svc.DigestOnce(context.Background()); n != 2 {
		t.Fatalf("queued %d", n)
	}
	w.svc.DispatchOnce(context.Background())
	if !switched {
		t.Fatal("the seam did not run")
	}
	if n := len(w.smtp.Mails()); n > 1 {
		t.Errorf("%d messages were sent; mail was switched off after the first, while the second waited, already claimed, in the same batch", n)
	}
}

// R-IDP: a person the identity provider no longer lists in any Scríobh group has their sessions revoked at the
// next sign-in attempt, but their row stays active, so the digest (which they can no longer sign in to turn off)
// keeps going to their address.
func TestReviewDigestStopsForAPersonRemovedFromEveryGroup(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	users := &auth.Users{DB: w.d, Cfg: testutil.Config(t, nil)}
	if _, err := users.SignIn(context.Background(), "test", auth.Claims{Subject: "sub-" + w.alice.id, Email: w.alice.email, EmailVerified: true}); err == nil {
		t.Fatal("a person with no group signed in")
	}
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	w.clk.Advance(26 * time.Hour)
	w.svc.DigestOnce(context.Background())
	w.pump()
	if len(w.smtp.Mails()) != 0 {
		t.Errorf("a digest (project titles, counts) went to %s after the identity provider removed them from every group", w.smtp.Mails()[0].To)
	}
}

// R-DOMAIN (was a demonstration, now the rule): a Kindle address is on the administrator's allow-list, which is
// kindle.com and free.kindle.com until it is widened, so the operator's relay cannot be aimed at any mailbox.
func TestReviewAKindleSendDoesNotGoToAnyDomain(t *testing.T) {
	w := newWorld(t)
	w.configure()
	if _, err := w.call(w.bob, "kindle.address.set", KindleAddressSetIn{Address: "ceo@victim-company.example"}); err == nil {
		t.Fatal("an address on any domain was stored")
	}
	if len(w.smtp.Mails()) != 0 {
		t.Fatal("sent")
	}
}

// R-EOD (a guard, passes today): body and attachment bytes that look like an end of data in any of the known
// smuggling spellings must reach the relay as base64 inside one message. No output line may be a lone dot, start with
// a dot, or end in a bare CR or bare LF.
func TestReviewEndOfDataSequencesNeverReachTheWire(t *testing.T) {
	nasty := "a\n.\nb\r\n.\r\nc\r.\rd\n.\r\ne\r.\n\x00f\n.\n"
	m := &Message{From: "a@example.org", To: "b@example.org", Subject: "s", MessageID: "x@example.org", Date: time.Unix(1700000000, 0), Body: strings.ReplaceAll(nasty, "\x00", ""),
		Attachment: &Attachment{Name: "book.epub", ContentType: "application/epub+zip", Data: []byte(strings.Repeat(nasty, 5000))}}
	var b bytes.Buffer
	if err := m.Write(&b); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") || strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\r") {
		t.Error("a bare CR or LF in the output")
	}
	for _, l := range strings.Split(out, "\r\n") {
		if l == "." || strings.HasPrefix(l, ".") {
			t.Errorf("a line starts with a dot: %q", l)
		}
	}
	// And through the real client to the fake relay: one message, and nothing the relay saw after it.
	f := newFakeSMTP(t)
	r := relayFor(f, SecStartTLS)
	if e := send(t, &Client{}, r, m); e != nil {
		t.Fatalf("send: %v", e)
	}
	if got := f.Mails(); len(got) != 1 {
		t.Fatalf("the relay saw %d messages", len(got))
	}
}

// R-STALL: a relay that answers 354 and then never reads must not hold the worker beyond the session's deadline.
func TestReviewARelayThatStopsReadingMidDataReleasesTheWorker(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		c.Write([]byte("220 stall ESMTP\r\n"))
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch up := strings.ToUpper(strings.TrimSpace(l)); {
			case strings.HasPrefix(up, "EHLO"):
				c.Write([]byte("250 stall\r\n"))
			case strings.HasPrefix(up, "DATA"):
				c.Write([]byte("354 go\r\n"))
				<-stop // never read another byte
				return
			default:
				c.Write([]byte("250 ok\r\n"))
			}
		}
	}()
	m := testMsg()
	m.Attachment = &Attachment{Name: "book.epub", ContentType: "application/epub+zip", Data: bytes.Repeat([]byte{7, 8, 9, 10, 11}, 3<<20)}
	r := Relay{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: SecNone, AllowPrivate: true, HelloName: "scriobh.example.org"}
	start := time.Now()
	e := send(t, &Client{Idle: 400 * time.Millisecond, Overall: 2 * time.Second}, r, m)
	if e == nil || e.Class != ClassTimeout || e.AfterData {
		t.Fatalf("got %+v", e)
	}
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 5*time.Second {
			t.Errorf("the worker was held %s", d)
		}
	})
}

// R-STUCK: when the result of an attempt cannot be written (a locked or full database), the row stays "sending"
// for ever: nothing reclaims it until a restart, and it counts against the person's queue the whole time.
func TestReviewASendingRowWhoseResultCouldNotBeWrittenIsReclaimed(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	if _, err := w.d.Writer().Exec(`CREATE TRIGGER review_fail BEFORE UPDATE ON mail_outbox WHEN NEW.state IN ('sent','dead') BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if _, err := w.d.Writer().Exec(`DROP TRIGGER review_fail`); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(3 * time.Hour)
	w.pump()
	_ = w.svc.Sweep(context.Background())
	if r := w.onlyRow(); r.State == "sending" {
		t.Errorf("the row is still %q three hours later (data_started_at=%q); only a restart settles it, and until then it holds a queue slot", r.State, r.DataStarted)
	}
}

// R-SWITCH-KINDLE: the same as the switch-off test, with Kindle editions: is the second claimed edition sent?
func TestReviewSwitchingMailOffStopsAClaimedKindleEdition(t *testing.T) {
	var w *world
	switched := false
	w = newWorld(t, func(o *wopt) {
		o.afterSend = func(string) error {
			if !switched {
				switched = true
				w.must(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(false)})
			}
			return nil
		}
	})
	w.configure()
	w.setKindle(w.alice, "alice_kindle@kindle.com")
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.alice, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.alice).Token})
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.svc.DispatchOnce(context.Background())
	if n := len(w.smtp.Mails()); n > 1 {
		t.Errorf("%d editions were sent after mail was switched off", n)
	}
}

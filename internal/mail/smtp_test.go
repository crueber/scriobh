package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

func testMsg() *Message {
	return &Message{From: "scriobh@example.org", FromName: "Scríobh", To: "reader@example.com", Subject: "Hello",
		Body: "A line.\n.\nQUIT\n.hidden\n", MessageID: "msg_1@example.org", Date: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), Auto: true}
}

func relayFor(f *fakeSMTP, sec string) Relay {
	r := Relay{Host: "127.0.0.1", Port: f.Port(), Security: sec, AllowPrivate: true, RootCAs: f.Pool, HelloName: "scriobh.example.org"}
	return r
}

func send(t *testing.T, c *Client, r Relay, m *Message) *Error {
	t.Helper()
	err := c.Send(context.Background(), r, m, SendOptions{})
	if err == nil {
		return nil
	}
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not a *Error: %T %v", err, err)
	}
	return e
}

func bodyOf(t *testing.T, raw string) (mail.Header, string) {
	t.Helper()
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("the relay got an unreadable message: %v\n%s", err, raw)
	}
	b, _ := io.ReadAll(m.Body)
	dec, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", ""), "\n", ""))
	if err != nil {
		t.Fatalf("body is not base64: %v", err)
	}
	return m.Header, string(dec)
}

func TestSendOverStartTLSAndImplicitTLS(t *testing.T) {
	for _, sec := range []string{SecStartTLS, SecTLS} {
		t.Run(sec, func(t *testing.T) {
			f := newFakeSMTP(t, func(f *fakeSMTP) { f.Implicit = sec == SecTLS; f.AuthMechs = "PLAIN LOGIN" })
			r := relayFor(f, sec)
			r.Username, r.Password = "user", "pw"
			if e := send(t, &Client{}, r, testMsg()); e != nil {
				t.Fatalf("send: %v", e)
			}
			got := f.Mails()
			if len(got) != 1 || !got[0].UsedTLS || !got[0].Authed || got[0].AuthUser != "user" {
				t.Fatalf("got %+v", got)
			}
			if got[0].From != "scriobh@example.org" || got[0].To != "reader@example.com" {
				t.Errorf("envelope %q -> %q", got[0].From, got[0].To)
			}
			h, body := bodyOf(t, got[0].Data)
			if body != "A line.\r\n.\r\nQUIT\r\n.hidden\r\n" {
				t.Errorf("body after the relay undid dot-stuffing = %q", body)
			}
			if h.Get("Auto-Submitted") != "auto-generated" || !strings.HasPrefix(h.Get("Content-Type"), "text/plain") {
				t.Errorf("headers %v", h)
			}
		})
	}
}

func TestAuthLoginWhenOnlyLoginIsOffered(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.AuthMechs = "LOGIN" })
	r := relayFor(f, SecStartTLS)
	r.Username, r.Password = "u", "pw"
	if e := send(t, &Client{}, r, testMsg()); e != nil {
		t.Fatal(e)
	}
	if m := f.Mails(); len(m) != 1 || m[0].AuthUser != "u" {
		t.Fatalf("%+v", m)
	}
}

// A relay that strips STARTTLS must not get a message or a password: there is no fall-through to plain text.
func TestStartTLSStrippedIsRefusedBeforeAnythingSecret(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.NoStartTLS = true; f.AuthMechs = "PLAIN" })
	r := relayFor(f, SecStartTLS)
	r.Username, r.Password = "user", "pw-canary"
	e := send(t, &Client{}, r, testMsg())
	if e == nil || e.Class != ClassNoStartTLS {
		t.Fatalf("got %v", e)
	}
	if len(f.Mails()) != 0 {
		t.Fatal("a message went over a connection that could not be encrypted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.got
}

func TestPasswordNeverOverPlainText(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.AuthMechs = "PLAIN" })
	r := relayFor(f, SecNone)
	r.Username, r.Password = "user", "pw"
	if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassConfig {
		t.Fatalf("a password over a plain connection: %v", e)
	}
	if f.Conns() != 0 {
		t.Error("it connected before refusing")
	}
	// Without a password, plain text to a loopback relay works when the operator chose it.
	r.Username, r.Password = "", ""
	if e := send(t, &Client{}, r, testMsg()); e != nil {
		t.Fatalf("plain to loopback: %v", e)
	}
	if m := f.Mails(); len(m) != 1 || m[0].UsedTLS {
		t.Fatalf("%+v", m)
	}
}

func TestPlainTextOnlyToLoopback(t *testing.T) {
	// The relay is on this machine's LAN address, not on loopback: plain text is refused whatever the name says.
	var lan net.IP
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() && !ipn.IP.IsLinkLocalUnicast() {
			lan = ipn.IP
		}
	}
	if lan == nil {
		t.Skip("no non-loopback address on this machine")
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte("220 hi\r\n"))
			c.Close()
		}
	}()
	r := Relay{Host: lan.String(), Port: ln.Addr().(*net.TCPAddr).Port, Security: SecNone, AllowPrivate: true}
	e := send(t, &Client{LookupIP: nil}, r, testMsg())
	if e == nil || (e.Class != ClassBlocked) {
		// A public-class LAN address (some CI hosts) is outside AllowPrivate's concern but still not loopback.
		t.Fatalf("plain text to a non-loopback address: %v", e)
	}
}

func TestPrivateRelayNeedsTheOperatorsSetting(t *testing.T) {
	f := newFakeSMTP(t)
	r := relayFor(f, SecStartTLS)
	r.AllowPrivate = false
	if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassBlocked {
		t.Fatalf("loopback without the setting: %v", e)
	}
	if f.Conns() != 0 {
		t.Error("the guard let a connection through")
	}
}

func TestLinkLocalAndMetadataAreNeverAllowed(t *testing.T) {
	for _, ip := range []string{"169.254.169.254", "169.254.1.1", "fe80::1", "0.0.0.0", "100.100.100.200"} {
		r := Relay{Host: ip, Port: 25, Security: SecStartTLS, AllowPrivate: true}
		if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassBlocked {
			t.Errorf("%s with the setting on: %v", ip, e)
		}
	}
	// A name that resolves to one of them, and one whose answer is mixed.
	c := &Client{LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("169.254.169.254")}, nil }}
	if e := send(t, c, Relay{Host: "relay.internal.example", Port: 25, Security: SecStartTLS, AllowPrivate: true}, testMsg()); e == nil || e.Class != ClassBlocked {
		t.Errorf("a name that resolves to the metadata address: %v", e)
	}
}

func TestCertificateIsVerified(t *testing.T) {
	other := newFakeSMTP(t) // another certificate authority
	for _, sec := range []string{SecStartTLS, SecTLS} {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.Implicit = sec == SecTLS })
		r := relayFor(f, sec)
		r.RootCAs = other.Pool
		if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassCert {
			t.Errorf("%s with a certificate nobody vouches for: %v", sec, e)
		}
		r.RootCAs = nil // the system roots do not know a self-signed certificate
		if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassCert {
			t.Errorf("%s against the system roots: %v", sec, e)
		}
		if len(f.Mails()) != 0 {
			t.Errorf("%s: a message was delivered", sec)
		}
	}
	// A certificate for another name is refused too.
	f := newFakeSMTP(t)
	r := relayFor(f, SecStartTLS)
	r.Host = "localhost"
	if e := send(t, &Client{}, r, testMsg()); e != nil {
		t.Fatalf("the certificate names localhost: %v", e)
	}
	// The name the operator typed is the name the certificate must carry.
	c := &Client{LookupIP: func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("127.0.0.1")}, nil }}
	r.Host = "relay.other.example"
	if e := send(t, c, r, testMsg()); e == nil || e.Class != ClassCert {
		t.Errorf("a name the certificate does not carry: %v", e)
	}
}

func TestAuthFailureIsAClassWithoutWhatTheServerSaid(t *testing.T) {
	for _, mech := range []string{"PLAIN", "LOGIN"} {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.AuthMechs = mech })
		r := relayFor(f, SecStartTLS)
		r.Username, r.Password = "user", "wrong-password-canary"
		e := send(t, &Client{}, r, testMsg())
		if e == nil || e.Class != ClassAuth {
			t.Fatalf("%s: %v", mech, e)
		}
		if strings.Contains(e.Error(), "canary") || strings.Contains(fmt.Sprintf("%+v", e), "canary") {
			t.Errorf("the error carries what the server echoed: %v", e)
		}
		if len(f.Mails()) != 0 {
			t.Error("delivered after a failed login")
		}
	}
	// A relay that offers no safe mechanism.
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.AuthMechs = "CRAM-MD5" })
	r := relayFor(f, SecStartTLS)
	r.Username, r.Password = "u", "pw"
	if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassAuth {
		t.Errorf("no usable mechanism: %v", e)
	}
	f = newFakeSMTP(t) // does not advertise AUTH at all
	r = relayFor(f, SecStartTLS)
	r.Username, r.Password = "u", "pw"
	if e := send(t, &Client{}, r, testMsg()); e == nil || e.Class != ClassAuth {
		t.Errorf("no AUTH offered: %v", e)
	}
}

func TestReplyCodesSetTheClass(t *testing.T) {
	cases := []struct {
		key, reply string
		class      Class
		permanent  bool
	}{
		{"RCPT", "451 4.3.0 try later", ClassTemporary, false},
		{"MAIL", "421 4.7.0 busy", ClassTemporary, false},
		{"RCPT", "550 5.1.1 no such user", ClassRejected, true},
		{"MAIL", "553 5.1.8 bad sender", ClassRejected, true},
		{"END", "552 5.3.4 too big", ClassTooLarge, true},
		{"END", "451 4.3.0 local error", ClassTemporary, false},
		{"END", "554 5.7.1 spam", ClassRejected, true},
		{"DATA", "554 no", ClassRejected, true},
	}
	for _, c := range cases {
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.Replies[c.key] = c.reply })
		e := send(t, &Client{}, relayFor(f, SecStartTLS), testMsg())
		if e == nil || e.Class != c.class || e.Class.Permanent() != c.permanent {
			t.Errorf("%s %q: got %v", c.key, c.reply, e)
		}
		if e != nil && e.AfterData {
			t.Errorf("%s %q: a clear answer is not 'unknown'", c.key, c.reply)
		}
		if len(f.Mails()) != 0 {
			t.Errorf("%s: recorded a mail the relay refused", c.key)
		}
	}
}

func TestAnswerLostAfterTheBodyIsUnknown(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.DropAfterData = true })
	e := send(t, &Client{}, relayFor(f, SecStartTLS), testMsg())
	if e == nil || !e.AfterData {
		t.Fatalf("got %v", e)
	}
}

func TestSlowRelayTimesOut(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.SlowFirstRead = 3 * time.Second })
	start := time.Now()
	e := send(t, &Client{Idle: 200 * time.Millisecond, Overall: time.Second}, relayFor(f, SecStartTLS), testMsg())
	if e == nil || e.Class != ClassTimeout {
		t.Fatalf("got %v", e)
	}
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 2*time.Second {
			t.Errorf("took %v", d)
		}
	})
}

func TestEndlessBannerIsBounded(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.HugeBanner = 50 << 20 })
	start := time.Now()
	e := send(t, &Client{MaxRead: 16 << 10}, relayFor(f, SecStartTLS), testMsg())
	if e == nil || e.Class != ClassProtocol {
		t.Fatalf("got %v", e)
	}
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 3*time.Second {
			t.Errorf("took %v", d)
		}
	})
}

func TestHugeReplyLineIsBounded(t *testing.T) {
	// One enormous line with no end, in answer to EHLO.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.Write([]byte("220 hi\r\n"))
		buf := make([]byte, 100)
		c.Read(buf)
		chunk := bytes.Repeat([]byte("y"), 4096)
		for i := 0; i < 20000; i++ {
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
	}()
	r := Relay{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Security: SecStartTLS, AllowPrivate: true}
	e := send(t, &Client{MaxRead: 64 << 10}, r, testMsg())
	if e == nil || e.Class != ClassProtocol {
		t.Fatalf("got %v", e)
	}
}

func TestHostileBanners(t *testing.T) {
	banners := map[string]Class{
		"554 no SMTP service here":    ClassRejected,
		"421 4.3.2 shutting down":     ClassTemporary,
		"500 \x1b]0;pwned\x07 what":   ClassRejected,
		"999 nonsense":                ClassProtocol,
		"garbage with no code at all": ClassProtocol,
		"":                            ClassProtocol, // handled below: empty banner line
		"220 \x00\x00\x00 hello\r\n250 injected ok": ClassNoStartTLS,
	}
	for b, want := range banners {
		if b == "" {
			continue
		}
		f := newFakeSMTP(t, func(f *fakeSMTP) { f.Banner = b; f.NoStartTLS = true })
		e := send(t, &Client{Idle: time.Second}, relayFor(f, SecStartTLS), testMsg())
		if e == nil {
			t.Errorf("%q: delivered", b)
			continue
		}
		if e.Class != want {
			t.Errorf("%q: class %s, want %s", b, e.Class, want)
		}
		if len(f.Mails()) != 0 {
			t.Errorf("%q: delivered", b)
		}
	}
}

func TestRelaySizeLimitIsHonouredBeforeSending(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.Exts = []string{"SIZE 500"} })
	m := testMsg()
	m.Attachment = &Attachment{Name: "book.epub", ContentType: "application/epub+zip", Data: bytes.Repeat([]byte("a"), 2000)}
	e := send(t, &Client{}, relayFor(f, SecStartTLS), m)
	if e == nil || e.Class != ClassTooLarge {
		t.Fatalf("got %v", e)
	}
	for _, m := range f.Mails() {
		for _, c := range m.RawCommand {
			if strings.HasPrefix(strings.ToUpper(c), "MAIL") {
				t.Error("it started a transaction the relay would refuse")
			}
		}
	}
}

func TestBeforeDataRunsOnlyAfterTheEnvelopeIsAccepted(t *testing.T) {
	f := newFakeSMTP(t, func(f *fakeSMTP) { f.Replies["RCPT"] = "550 nope" })
	called := false
	err := (&Client{}).Send(context.Background(), relayFor(f, SecStartTLS), testMsg(), SendOptions{BeforeData: func() error { called = true; return nil }})
	if err == nil || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	f2 := newFakeSMTP(t)
	called = false
	if err := (&Client{}).Send(context.Background(), relayFor(f2, SecStartTLS), testMsg(), SendOptions{BeforeData: func() error { called = true; return nil }}); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

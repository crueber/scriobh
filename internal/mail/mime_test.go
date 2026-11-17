package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

func build(t *testing.T, m *Message) string {
	t.Helper()
	var b bytes.Buffer
	if err := m.Write(&b); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return b.String()
}

func TestEveryHeaderFieldRefusesInjection(t *testing.T) {
	evil := []string{"x\r\nBcc: attacker@example.net", "x\nBcc: attacker@example.net", "x\rBcc: a@b.c", "x\x00y", "x\x0by", "a\u2028b", "a\u0085b"}
	fields := map[string]func(m *Message, v string){
		"subject":  func(m *Message, v string) { m.Subject = v },
		"to":       func(m *Message, v string) { m.To = v },
		"from":     func(m *Message, v string) { m.From = v },
		"fromname": func(m *Message, v string) { m.FromName = v },
		"filename": func(m *Message, v string) {
			m.Attachment = &Attachment{Name: v, ContentType: "application/epub+zip", Data: []byte("x")}
		},
		"messageid":   func(m *Message, v string) { m.MessageID = v },
		"unsubscribe": func(m *Message, v string) { m.ListUnsubscribe = "https://example.org/" + v },
		"contenttype": func(m *Message, v string) {
			m.Attachment = &Attachment{Name: "a.epub", ContentType: v, Data: []byte("x")}
		},
	}
	for name, set := range fields {
		for _, v := range evil {
			m := testMsg()
			set(m, v)
			var b bytes.Buffer
			if err := m.Write(&b); err == nil {
				t.Errorf("%s accepted %q:\n%s", name, v, b.String())
			}
		}
	}
	// Recipients that are lists, names or tricks.
	for _, to := range []string{"a@example.com, b@example.com", "a@example.com;b@example.com", "Name <a@example.com>", "<a@example.com>", `"a b"@example.com`,
		"a@example.com\r\nRCPT TO:<b@example.com>", "a@[127.0.0.1]", "a@b", "a b@example.com", "ä@example.com", "a@exämple.com", "@example.com", "a@", "a@@example.com", ""} {
		m := testMsg()
		m.To = to
		if err := m.Write(io.Discard); err == nil {
			t.Errorf("recipient %q accepted", to)
		}
		if _, err := ValidAddress(to); err == nil {
			t.Errorf("ValidAddress(%q) accepted", to)
		}
	}
}

func TestPlainMessageHasOnlyTheHeadersItShould(t *testing.T) {
	m := testMsg()
	m.ListUnsubscribe = "https://scriobh.example.org/settings"
	raw := build(t, m)
	if strings.Contains(raw, "\n") && strings.Contains(strings.ReplaceAll(raw, "\r\n", ""), "\n") {
		t.Error("a bare LF in the message")
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Date": "Mon, 01 Jun 2026 12:00:00 +0000", "To": "reader@example.com", "Message-Id": "<msg_1@example.org>", "Mime-Version": "1.0",
		"List-Unsubscribe": "<https://scriobh.example.org/settings>", "Auto-Submitted": "auto-generated", "Subject": "Hello", "Content-Transfer-Encoding": "base64"}
	for k, v := range want {
		if got := msg.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	from, _ := mail.ParseAddress(msg.Header.Get("From"))
	if from == nil || from.Address != "scriobh@example.org" || from.Name != "Scríobh" {
		t.Errorf("From = %q", msg.Header.Get("From"))
	}
	if n := len(msg.Header); n != 10 { // the ones above, From and Content-Type
		t.Errorf("%d headers: %v", n, msg.Header)
	}
	for _, line := range strings.Split(raw, "\r\n") {
		if len(line) > 998 {
			t.Errorf("line of %d bytes", len(line))
		}
	}
	// No HTML, no image, no link the app did not put there, no tracking.
	low := strings.ToLower(raw)
	for _, bad := range []string{"text/html", "<img", "<a ", "http://", "x-mailer", "x-tracking", "disposition-notification", "return-receipt"} {
		if strings.Contains(low, bad) {
			t.Errorf("message contains %q", bad)
		}
	}
}

func TestSubjectAndDisplayNameAreRFC2047(t *testing.T) {
	m := testMsg()
	m.Subject = "Ein Brief für Zoë: 日本語 " + strings.Repeat("lang ", 30)
	raw := build(t, m)
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// Folded encoded words: each header line is short, and decoding gives the subject back.
	for _, l := range strings.Split(strings.SplitN(raw, "\r\n\r\n", 2)[0], "\r\n") {
		if len(l) > 90 && !strings.HasPrefix(l, "Content-Type") {
			t.Errorf("an unfolded header line of %d: %q", len(l), l)
		}
	}
	dec := new(mime.WordDecoder)
	got, err := dec.DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || got != m.Subject {
		t.Errorf("decoded subject %q (%v)", got, err)
	}
	if strings.Contains(strings.SplitN(raw, "\r\n\r\n", 2)[0], "日本語") {
		t.Error("raw non-ASCII bytes in a header")
	}
}

func TestAttachmentIsMultipartBase64WithRFC2231Name(t *testing.T) {
	data := bytes.Repeat([]byte{0, 1, 2, 250, 251, 'a', '\r', '\n', '.'}, 5000)
	m := testMsg()
	m.Attachment = &Attachment{Name: "Ünïcode Title ✓.epub", ContentType: "application/epub+zip", Data: data}
	raw := build(t, m)
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("%q %v", mt, err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	text, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := io.ReadAll(text)
	if d, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(tb), "\r\n", "")); !strings.HasPrefix(string(d), "A line.") {
		t.Errorf("text part = %q", d)
	}
	att, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if att.Header.Get("Content-Transfer-Encoding") != "base64" {
		t.Error("attachment not base64")
	}
	_, dp, err := mime.ParseMediaType(att.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("disposition %q: %v", att.Header.Get("Content-Disposition"), err)
	}
	if dp["filename"] != "Ünïcode Title ✓.epub" { // Go prefers filename* (RFC 2231) when both are present
		t.Errorf("filename* decodes to %q", dp["filename"])
	}
	if !strings.Contains(att.Header.Get("Content-Disposition"), `filename="_n_code Title _.epub"`) &&
		!strings.Contains(att.Header.Get("Content-Disposition"), "filename=") {
		t.Errorf("no ASCII fallback: %q", att.Header.Get("Content-Disposition"))
	}
	ab, _ := io.ReadAll(att)
	for _, l := range strings.Split(strings.TrimRight(string(ab), "\r\n"), "\r\n") {
		if len(l) > 76 {
			t.Fatalf("a base64 line of %d", len(l))
		}
	}
	got, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(ab), "\r\n", ""))
	if err != nil || !bytes.Equal(got, data) {
		t.Errorf("attachment does not round-trip (%v)", err)
	}
	if _, err := mr.NextPart(); err != io.EOF {
		t.Errorf("more than two parts: %v", err)
	}
	// Size is exact, and the bytes are the same every time (a retry sends the same message).
	n, err := m.Size()
	if err != nil || n != int64(len(raw)) {
		t.Errorf("Size = %d, %v; message is %d", n, err, len(raw))
	}
	if again := build(t, m); again != raw {
		t.Error("the message is not deterministic")
	}
}

func TestFileNamesAreChecked(t *testing.T) {
	for _, n := range []string{"", "../x.epub", "a/b.epub", `a\b.epub`, `a"b.epub`, ".hidden", strings.Repeat("a", 200), "a\tb"} {
		m := testMsg()
		m.Attachment = &Attachment{Name: n, ContentType: "application/epub+zip", Data: []byte("x")}
		if m.Write(io.Discard) == nil {
			t.Errorf("file name %q accepted", n)
		}
	}
}

func TestBodyIsTextOnlyAndBounded(t *testing.T) {
	m := testMsg()
	m.Body = strings.Repeat("x", MaxBodyBytes+1)
	if m.Write(io.Discard) == nil {
		t.Error("an oversized body")
	}
	m.Body = "a\x00b"
	if m.Write(io.Discard) == nil {
		t.Error("a NUL in the body")
	}
	m.Body = "line\r\nBcc: x\r\n\r\n.\r\nQUIT\r\n"
	raw := build(t, m)
	if strings.Contains(raw, "QUIT") || strings.Contains(raw, "Bcc:") {
		t.Error("body text appears in the clear")
	}
	if time.Now().IsZero() {
		t.Fatal()
	}
}

func TestFlatTextDefusesATitle(t *testing.T) {
	got := flatText("Line one\r\nBcc: x@y.z\t\x00 see https://evil.example/x and www.evil.example "+strings.Repeat("long ", 40), 60)
	if strings.ContainsAny(got, "\r\n\t\x00") || strings.Contains(got, "://") || strings.Contains(got, "www.") {
		t.Errorf("flatText = %q", got)
	}
	if len([]rune(got)) > 80 {
		t.Errorf("not truncated: %d runes", len([]rune(got)))
	}
}

// The attachment is encoded straight into the writer: building a 12 MB message allocates a small fraction of it
// and takes a fraction of a second.
func TestALargeAttachmentIsStreamedNotBuffered(t *testing.T) {
	data := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, (12<<20)/7)
	m := testMsg()
	m.Attachment = &Attachment{Name: "book.epub", ContentType: "application/epub+zip", Data: data}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	if err := m.Write(io.Discard); err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("12 MB attachment: %d KB allocated, %v", alloc>>10, took)
	if alloc > 1<<20 {
		t.Errorf("building allocated %d KB: the attachment was copied", alloc>>10)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 2*time.Second {
			t.Errorf("took %v", took)
		}
	})
}

package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits of one message.
const (
	MaxSubjectRunes = 200
	MaxBodyBytes    = 64 << 10
	MaxFileName     = 150
)

// Attachment is the one file a message may carry.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

// Message is what Write turns into RFC 5322 text. Every string is checked; none is trusted.
type Message struct {
	// From is the envelope sender and the From header: the operator's configured address.
	From     string
	FromName string
	To       string
	Subject  string
	// Body is plain text (UTF-8). No HTML part is ever made, so there is nothing to load and nothing to track.
	Body string
	// MessageID is the id without angle brackets, fixed for the life of the outbox row.
	MessageID  string
	Date       time.Time
	Attachment *Attachment
	// ListUnsubscribe is an http(s) address of the app's own page; empty for a message that is no digest.
	ListUnsubscribe string
	// Auto marks the message as machine-made (Auto-Submitted: auto-generated).
	Auto bool
}

var (
	contentTypeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]{0,60}/[a-z0-9][a-z0-9.+-]{0,60}$`)
	messageIDRe   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,120}@[A-Za-z0-9.-]{1,253}$`)
)

// validate checks every field and returns the first problem. It does not change the message.
func (m *Message) validate() error {
	if _, err := ValidAddress(m.From); err != nil {
		return fmt.Errorf("sender: %w", err)
	}
	if _, err := ValidAddress(m.To); err != nil {
		return fmt.Errorf("recipient: %w", err)
	}
	if err := checkHeaderValue(m.FromName); err != nil {
		return fmt.Errorf("sender name: %w", err)
	}
	if utf8.RuneCountInString(m.FromName) > 80 {
		return errors.New("sender name is too long")
	}
	if err := checkHeaderValue(m.Subject); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if utf8.RuneCountInString(m.Subject) > MaxSubjectRunes {
		return errors.New("subject is too long")
	}
	if !messageIDRe.MatchString(m.MessageID) {
		return errors.New("message id is not valid")
	}
	if m.Date.IsZero() {
		return errors.New("a message needs a date")
	}
	if !utf8.ValidString(m.Body) || strings.ContainsRune(m.Body, 0) || len(m.Body) > MaxBodyBytes {
		return errors.New("the body is not valid text or is too long")
	}
	if m.ListUnsubscribe != "" {
		u, err := url.Parse(m.ListUnsubscribe)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || checkURLChars(m.ListUnsubscribe) != nil {
			return errors.New("the unsubscribe address is not a plain http(s) address")
		}
	}
	if a := m.Attachment; a != nil {
		if err := checkFileName(a.Name); err != nil {
			return err
		}
		if !contentTypeRe.MatchString(a.ContentType) {
			return errors.New("the attachment's content type is not valid")
		}
	}
	return nil
}

func checkURLChars(s string) error {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c <= 0x20 || c >= 0x7f || c == '<' || c == '>' || c == '"' {
			return ErrHeader
		}
	}
	return nil
}

func checkFileName(n string) error {
	if n == "" || len(n) > MaxFileName {
		return errors.New("the file name is empty or too long")
	}
	if err := checkHeaderValue(n); err != nil {
		return fmt.Errorf("file name: %w", err)
	}
	if strings.ContainsAny(n, `/\"`) || strings.HasPrefix(n, ".") || strings.Contains(n, "\t") {
		return errors.New("the file name has a path separator, a quote or a leading dot")
	}
	return nil
}

// asciiFallback is the plain name old readers use when they do not understand an RFC 2231 name.
func asciiFallback(n string) string {
	var b strings.Builder
	for _, r := range n {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// pctEncode is the RFC 2231 / 5987 percent-encoding of a parameter value (attr-char stays).
func pctEncode(s string) string {
	const attr = "!#$&+-.^_`|~"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(attr, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// dispositionParams writes filename (an ASCII fallback) and, when the name is not plain ASCII, filename* (RFC 2231).
func dispositionParams(name string) string {
	fb := asciiFallback(name)
	out := `filename="` + fb + `"`
	if fb != name {
		out += "; filename*=UTF-8''" + pctEncode(name)
	}
	return out
}

func encodeSubject(s string) string {
	if s == "" {
		return ""
	}
	// The encoder joins its words with a space; a header line should stay short, so they are folded.
	return strings.ReplaceAll(mime.QEncoding.Encode("utf-8", s), "?= =?", "?=\r\n =?")
}

var crlf = []byte("\r\n")

// wrapWriter writes base64 in 76-column lines ending CRLF.
type wrapWriter struct {
	w   io.Writer
	col int
	err error
}

func (b *wrapWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && b.err == nil {
		room := 76 - b.col
		if room > len(p) {
			room = len(p)
		}
		_, b.err = b.w.Write(p[:room])
		b.col += room
		p = p[room:]
		if b.col == 76 {
			_, b.err = b.w.Write(crlf)
			b.col = 0
		}
	}
	return n, b.err
}

func (b *wrapWriter) Close() error {
	if b.err == nil && b.col > 0 {
		_, b.err = b.w.Write(crlf)
		b.col = 0
	}
	return b.err
}

func writeBase64(w io.Writer, data []byte) error {
	ww := &wrapWriter{w: w}
	enc := base64.NewEncoder(base64.StdEncoding, ww)
	if _, err := enc.Write(data); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return ww.Close()
}

func canonicalBody(s string) []byte {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.TrimRight(s, "\n") + "\n"
	return []byte(strings.ReplaceAll(s, "\n", "\r\n"))
}

// Write validates m and writes the message to w, CRLF line ends throughout, base64 bodies. The attachment is
// encoded straight into w: it is never held a second time.
func (m *Message) Write(w io.Writer) error {
	if err := m.validate(); err != nil {
		return err
	}
	// The encoder joins its words with a space: fold them, so no line passes the 998 octets of RFC 5322.
	from := strings.ReplaceAll((&mail.Address{Name: m.FromName, Address: m.From}).String(), "?= =?", "?=\r\n =?")
	var h bytes.Buffer
	line := func(k, v string) { h.WriteString(k + ": " + v + "\r\n") }
	line("Date", m.Date.UTC().Format(time.RFC1123Z))
	line("From", from)
	line("To", m.To)
	line("Subject", encodeSubject(m.Subject))
	line("Message-ID", "<"+m.MessageID+">")
	line("MIME-Version", "1.0")
	if m.Auto {
		line("Auto-Submitted", "auto-generated")
	}
	if m.ListUnsubscribe != "" {
		line("List-Unsubscribe", "<"+m.ListUnsubscribe+">")
	}
	text := canonicalBody(m.Body)
	if m.Attachment == nil {
		line("Content-Type", "text/plain; charset=utf-8")
		line("Content-Transfer-Encoding", "base64")
		h.WriteString("\r\n")
		if _, err := w.Write(h.Bytes()); err != nil {
			return err
		}
		return writeBase64(w, text)
	}
	sum := sha256.Sum256([]byte(m.MessageID))
	boundary := "scriobh-" + hex.EncodeToString(sum[:16])
	line("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	h.WriteString("\r\n")
	if _, err := w.Write(h.Bytes()); err != nil {
		return err
	}
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(boundary); err != nil {
		return err
	}
	tp, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return err
	}
	if err := writeBase64(tp, text); err != nil {
		return err
	}
	a := m.Attachment
	ap, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {a.ContentType + `; name="` + asciiFallback(a.Name) + `"`},
		"Content-Disposition":       {"attachment; " + dispositionParams(a.Name)},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return err
	}
	if err := writeBase64(ap, a.Data); err != nil {
		return err
	}
	return mw.Close()
}

// countWriter counts and discards.
type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// Size is the exact length of what Write produces, or an error when the message is not valid.
func (m *Message) Size() (int64, error) {
	var c countWriter
	if err := m.Write(&c); err != nil {
		return 0, err
	}
	return c.n, nil
}

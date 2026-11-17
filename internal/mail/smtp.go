package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
)

// Security says how a connection to the relay is protected.
const (
	SecStartTLS = "starttls" // plain connection, upgraded with STARTTLS before anything secret; refused if the relay does not offer it
	SecTLS      = "tls"      // TLS from the first byte (port 465)
	SecNone     = "none"     // no encryption: only to a loopback relay, only when the operator chose it, never with a password
)

// Relay is where and how to submit mail. It holds the opened password; the value lives for one send.
type Relay struct {
	Host     string
	Port     int
	Security string
	// AllowPrivate lets the relay be on this machine or its own network (the operator's setting). Link-local,
	// metadata and reserved addresses are never allowed.
	AllowPrivate bool
	// RootCAs, when set, replaces the system roots (a relay with a private certificate authority).
	RootCAs  *x509.CertPool
	Username string
	Password string
	// HelloName is what EHLO says.
	HelloName string
}

// Client sends messages. The zero value is usable.
type Client struct {
	// Idle is how long one read or write may wait; Overall bounds the whole session (the size of the message
	// is added at one second per 100 KB).
	Idle, Overall time.Duration
	// MaxRead is the most bytes read from the relay in one session.
	MaxRead int64
	// LookupIP is a test seam for the network guard.
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
}

const (
	defaultIdle    = 20 * time.Second
	defaultOverall = 60 * time.Second
	maxOverall     = 10 * time.Minute
	defaultMaxRead = 256 << 10
)

func (c *Client) idle() time.Duration {
	if c != nil && c.Idle > 0 {
		return c.Idle
	}
	return defaultIdle
}

func (c *Client) overall(size int64) time.Duration {
	o := defaultOverall
	if c != nil && c.Overall > 0 {
		o = c.Overall
	}
	o += time.Duration(size/(100<<10)) * time.Second
	return min(o, maxOverall)
}

func (c *Client) maxRead() int64 {
	if c != nil && c.MaxRead > 0 {
		return c.MaxRead
	}
	return defaultMaxRead
}

var errTooMuch = errors.New("the relay sent too much")

// guardConn bounds what a relay can make us wait for or read.
type guardConn struct {
	net.Conn
	idle    time.Duration
	hard    time.Time
	maxRead int64
	read    int64
}

func (g *guardConn) deadline() time.Time {
	d := time.Now().Add(g.idle)
	if d.After(g.hard) {
		return g.hard
	}
	return d
}

func (g *guardConn) Read(p []byte) (int, error) {
	if g.read >= g.maxRead {
		return 0, errTooMuch
	}
	if rem := g.maxRead - g.read; int64(len(p)) > rem {
		p = p[:rem]
	}
	_ = g.Conn.SetReadDeadline(g.deadline())
	n, err := g.Conn.Read(p)
	g.read += int64(n)
	return n, err
}

func (g *guardConn) Write(p []byte) (int, error) {
	_ = g.Conn.SetWriteDeadline(g.deadline())
	return g.Conn.Write(p)
}

// authPlain and authLogin are written here because net/smtp's PlainAuth sends to a host merely named
// "localhost" in the clear and has no LOGIN. They are only ever started on an encrypted connection: Send
// refuses before it gets here.
type authPlain struct{ user, pass string }

func (a authPlain) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a authPlain) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected challenge")
	}
	return nil, nil
}

type authLogin struct{ user, pass string }

func (a authLogin) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }
func (a authLogin) Next(challenge []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch c := strings.ToLower(string(challenge)); {
	case strings.HasPrefix(c, "user"):
		return []byte(a.user), nil
	case strings.HasPrefix(c, "pass"):
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected challenge")
}

// SendOptions are the hooks of one send.
type SendOptions struct {
	// BeforeData runs just before the message body is sent, after the relay accepted the sender and the
	// recipient. A crash after it is "the relay may have the message". An error stops the send.
	BeforeData func() error
}

// Send submits msg to the relay. A nil return means the relay accepted it. Every failure is an *Error: a
// class, and whether the relay might have taken the message anyway.
func (c *Client) Send(ctx context.Context, r Relay, msg *Message, o SendOptions) error {
	if r.Host == "" || r.Port < 1 || r.Port > 65535 {
		return &Error{Class: ClassConfig}
	}
	size, err := msg.Size()
	if err != nil {
		return &Error{Class: ClassInternal}
	}
	if r.Username != "" && r.Security == SecNone {
		return &Error{Class: ClassConfig} // never a password in the clear
	}
	hard := time.Now().Add(c.overall(size))
	cctx, cancel := context.WithDeadline(ctx, hard)
	defer cancel()

	mode := netguard.PublicOnly
	if r.AllowPrivate {
		mode = netguard.AllowLocal
	}
	dial := netguard.Dialer(netguard.Options{Mode: mode, DialTimeout: c.idle(), LookupIP: c.lookup()})
	raw, err := dial(cctx, "tcp", net.JoinHostPort(r.Host, strconv.Itoa(r.Port)))
	if err != nil {
		return classify(err, false)
	}
	defer raw.Close()
	if r.Security == SecNone {
		// "Loopback" is judged on the address that answered, never on the name that was typed.
		ta, ok := raw.RemoteAddr().(*net.TCPAddr)
		if !ok || !ta.IP.IsLoopback() {
			return &Error{Class: ClassBlocked}
		}
	}
	stopAfter := context.AfterFunc(cctx, func() { _ = raw.Close() })
	defer stopAfter()
	gc := &guardConn{Conn: raw, idle: c.idle(), hard: hard, maxRead: c.maxRead()}
	var conn net.Conn = gc

	tlsCfg := &tls.Config{ServerName: r.Host, MinVersion: tls.VersionTLS12, RootCAs: r.RootCAs}
	if r.Security == SecTLS {
		tc := tls.Client(gc, tlsCfg)
		if err := tc.HandshakeContext(cctx); err != nil {
			return classify(err, false)
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, r.Host)
	if err != nil {
		return classify(err, false)
	}
	defer cl.Close()
	helo := r.HelloName
	if helo == "" {
		helo = "localhost"
	}
	if err := cl.Hello(helo); err != nil {
		return classify(err, false)
	}
	secure := r.Security != SecNone
	if r.Security == SecStartTLS {
		ok, _ := cl.Extension("STARTTLS")
		if !ok {
			return &Error{Class: ClassNoStartTLS}
		}
		if err := cl.StartTLS(tlsCfg); err != nil {
			return classify(err, false)
		}
		secure = true
	}
	if r.Username != "" {
		if !secure {
			return &Error{Class: ClassConfig}
		}
		_, params := cl.Extension("AUTH")
		var a smtp.Auth
		for _, m := range strings.Fields(strings.ToUpper(params)) {
			if m == "PLAIN" {
				a = authPlain{r.Username, r.Password}
				break
			}
			if m == "LOGIN" && a == nil {
				a = authLogin{r.Username, r.Password}
			}
		}
		if a == nil {
			return &Error{Class: ClassAuth}
		}
		if err := cl.Auth(a); err != nil {
			var te *textproto.Error
			if errors.As(err, &te) {
				if te.Code >= 400 && te.Code < 500 {
					return &Error{Class: ClassTemporary}
				}
				return &Error{Class: ClassAuth}
			}
			return classify(err, false)
		}
	}
	if ok, p := cl.Extension("SIZE"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil && n > 0 && size > n {
			return &Error{Class: ClassTooLarge}
		}
	}
	if err := cl.Mail(msg.From); err != nil {
		return classify(err, false)
	}
	if err := cl.Rcpt(msg.To); err != nil {
		return classify(err, false)
	}
	if o.BeforeData != nil {
		if err := o.BeforeData(); err != nil {
			return &Error{Class: ClassInternal}
		}
	}
	w, err := cl.Data()
	if err != nil {
		return classify(err, false)
	}
	if err := msg.Write(w); err != nil {
		// Not closed: closing would end the message with the final dot and make a half message a whole one.
		return classify(err, false)
	}
	if err := w.Close(); err != nil {
		return classify(err, true)
	}
	_ = cl.Quit()
	return nil
}

func (c *Client) lookup() func(context.Context, string) ([]net.IP, error) {
	if c == nil {
		return nil
	}
	return c.LookupIP
}

// classify turns an error into a class. It never keeps the error's text.
func classify(err error, afterData bool) *Error {
	e := &Error{Class: ClassNetwork}
	var te *textproto.Error
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		e.Class = ClassBlocked
	case errors.As(err, &te):
		// A reply: the relay answered, so whether it took the message is known.
		afterData = false
		switch {
		case te.Code == 552:
			e.Class = ClassTooLarge
		case te.Code >= 400 && te.Code < 500:
			e.Class = ClassTemporary
		case te.Code >= 500 && te.Code < 600:
			e.Class = ClassRejected
		default:
			e.Class = ClassProtocol
		}
	case isCertError(err):
		e.Class = ClassCert
	case errors.Is(err, errTooMuch):
		e.Class = ClassProtocol
	case errors.As(err, &dns):
		e.Class = ClassDNS
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		e.Class = ClassTimeout
	case errors.Is(err, syscall.ECONNREFUSED):
		e.Class = ClassRefused
	case isTLSError(err):
		e.Class = ClassTLS
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		e.Class = ClassNetwork
	default:
		var pe textproto.ProtocolError
		if errors.As(err, &pe) || strings.Contains(fmt.Sprint(err), "malformed") {
			e.Class = ClassProtocol
		}
	}
	e.AfterData = afterData
	return e
}

func isCertError(err error) bool {
	var cv *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var ci x509.CertificateInvalidError
	var he x509.HostnameError
	return errors.As(err, &cv) || errors.As(err, &ua) || errors.As(err, &ci) || errors.As(err, &he)
}

func isTLSError(err error) bool {
	var re tls.RecordHeaderError
	var ae *tls.AlertError
	var ce *tls.CertificateVerificationError
	return errors.As(err, &re) || errors.As(err, &ae) || errors.As(err, &ce) || strings.Contains(err.Error(), "tls:")
}

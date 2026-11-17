package mail

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// A minimal in-process SMTP server: EHLO, STARTTLS, AUTH PLAIN and LOGIN, MAIL, RCPT, DATA, RSET, QUIT. It records
// what it is sent and answers as the test scripts it.

type gotMail struct {
	From, To   string
	Data       string
	AuthUser   string
	AuthPass   string
	Authed     bool
	UsedTLS    bool
	RawCommand []string
}

type fakeSMTP struct {
	t        *testing.T
	ln       net.Listener
	cert     tls.Certificate
	CertPEM  []byte
	Pool     *x509.CertPool
	Implicit bool // TLS from the first byte

	mu    sync.Mutex
	got   []gotMail
	conns int

	// Behaviour, set before the first connection.
	Banner        string   // replaces the 220 greeting line
	NoStartTLS    bool     // do not advertise STARTTLS
	Exts          []string // extra EHLO lines (SIZE 1000, ...)
	AuthMechs     string   // advertised AUTH mechanisms; empty means none
	Password      string   // the password that is accepted; default "pw"
	Replies       map[string]string
	SlowFirstRead time.Duration // sleep before the banner
	HugeBanner    int           // send this many bytes of banner
	DropAfterData bool          // close without answering the final dot
}

func selfSigned(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	pemb := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	cert, err := tls.X509KeyPair(pemb, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: mustMarshalEC(t, k)}))
	if err != nil {
		t.Fatal(err)
	}
	return cert, pemb
}

func mustMarshalEC(t *testing.T, k *ecdsa.PrivateKey) []byte {
	b, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newFakeSMTP(t *testing.T, mod ...func(*fakeSMTP)) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{t: t, Password: "pw", Replies: map[string]string{}}
	f.cert, f.CertPEM = selfSigned(t)
	f.Pool = x509.NewCertPool()
	f.Pool.AppendCertsFromPEM(f.CertPEM)
	for _, m := range mod {
		m(f)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns++
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) Port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) Mails() []gotMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]gotMail(nil), f.got...)
}

func (f *fakeSMTP) Conns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

func (f *fakeSMTP) reply(key, def string) string {
	if r, ok := f.Replies[key]; ok {
		return r
	}
	return def
}

func (f *fakeSMTP) serve(raw net.Conn) {
	defer raw.Close()
	var c net.Conn = raw
	usedTLS := false
	if f.Implicit {
		tc := tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{f.cert}})
		if tc.Handshake() != nil {
			return
		}
		c, usedTLS = tc, true
	}
	if f.SlowFirstRead > 0 {
		time.Sleep(f.SlowFirstRead)
	}
	if f.HugeBanner > 0 {
		chunk := []byte("220-" + strings.Repeat("x", 1000) + "\r\n")
		for n := 0; n < f.HugeBanner; n += len(chunk) {
			if _, err := c.Write(chunk); err != nil {
				return
			}
		}
		return
	}
	banner := f.Banner
	if banner == "" {
		banner = "220 fake.test ESMTP ready"
	}
	fmt.Fprintf(c, "%s\r\n", banner)
	r := bufio.NewReader(c)
	var cur gotMail
	say := func(s string) { fmt.Fprintf(c, "%s\r\n", s) }
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		cur.RawCommand = append(cur.RawCommand, line)
		switch {
		case strings.HasPrefix(up, "EHLO"), strings.HasPrefix(up, "HELO"):
			lines := []string{"fake.test greets you"}
			if !f.NoStartTLS && !usedTLS {
				lines = append(lines, "STARTTLS")
			}
			if f.AuthMechs != "" {
				lines = append(lines, "AUTH "+f.AuthMechs)
			}
			lines = append(lines, f.Exts...)
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				say("250" + sep + l)
			}
		case up == "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{f.cert}})
			if tc.Handshake() != nil {
				return
			}
			c, usedTLS = tc, true
			r = bufio.NewReader(c)
			cur = gotMail{RawCommand: cur.RawCommand}
		case strings.HasPrefix(up, "AUTH PLAIN"):
			arg := strings.TrimSpace(line[len("AUTH PLAIN"):])
			if arg == "" {
				say("334 ")
				l, _ := r.ReadString('\n')
				arg = strings.TrimSpace(l)
			}
			b, _ := base64.StdEncoding.DecodeString(arg)
			parts := strings.Split(string(b), "\x00")
			if len(parts) == 3 && parts[2] == f.Password {
				cur.AuthUser, cur.AuthPass, cur.Authed = parts[1], parts[2], true
				say("235 ok")
			} else {
				say("535 5.7.8 bad credentials " + string(b)) // echoes what it was sent, as a hostile server may
			}
		case strings.HasPrefix(up, "AUTH LOGIN"):
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
			u, _ := r.ReadString('\n')
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			p, _ := r.ReadString('\n')
			ub, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(u))
			pb, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
			if string(pb) == f.Password {
				cur.AuthUser, cur.AuthPass, cur.Authed = string(ub), string(pb), true
				say("235 ok")
			} else {
				say("535 5.7.8 bad credentials " + string(pb))
			}
		case strings.HasPrefix(up, "MAIL FROM:"):
			rep := f.reply("MAIL", "250 ok")
			if strings.HasPrefix(rep, "250") {
				cur.From = strings.Trim(strings.SplitN(line[len("MAIL FROM:"):], " ", 2)[0], "<> ")
			}
			say(rep)
		case strings.HasPrefix(up, "RCPT TO:"):
			rep := f.reply("RCPT", "250 ok")
			if strings.HasPrefix(rep, "250") {
				cur.To = strings.Trim(strings.SplitN(line[len("RCPT TO:"):], " ", 2)[0], "<> ")
			}
			say(rep)
		case up == "DATA":
			rep := f.reply("DATA", "354 go")
			say(rep)
			if !strings.HasPrefix(rep, "354") {
				continue
			}
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				l = strings.TrimPrefix(l, ".") // undo dot stuffing
				sb.WriteString(l)
			}
			if f.DropAfterData {
				return
			}
			rep = f.reply("END", "250 queued")
			if strings.HasPrefix(rep, "250") {
				cur.Data, cur.UsedTLS = sb.String(), usedTLS
				f.mu.Lock()
				f.got = append(f.got, cur)
				f.mu.Unlock()
				cur = gotMail{}
			}
			say(rep)
		case up == "RSET", up == "NOOP":
			say("250 ok")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented")
		}
	}
}

var _ = io.EOF

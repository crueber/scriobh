package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/netguard"
)

// Error classes recorded on a delivery. They say what kind of failure it was and
// nothing about where: no address, no resolver answer, no response body.
const (
	ErrClassBlocked  = "blocked"          // the network guard refused the address
	ErrClassTimeout  = "timeout"          // no answer in time
	ErrClassDNS      = "dns"              // the name did not resolve
	ErrClassRefused  = "refused"          // nothing listening
	ErrClassTLS      = "tls"              // the certificate or handshake failed
	ErrClassNetwork  = "network"          // any other transport failure
	ErrClassRedirect = "redirect_refused" // a 3xx; redirects are never followed
	ErrClassRejected = "rejected"         // a 4xx that a retry will not fix
	ErrClassServer   = "server_error"     // a 5xx
	ErrClassBusy     = "rate_limited"     // a 429 or 408
	ErrClassGone     = "gone"             // a 410: the receiver asked not to be called again
)

const (
	excerptRead  = 512 // bytes of a response read for the excerpt; the rest is never read
	excerptChars = 160 // characters kept
)

// Result is what one attempt came to.
type Result struct {
	Status     int
	Class      string // empty on success
	Excerpt    string // a short, cleaned piece of a failing response
	RetryAfter time.Duration
	Took       time.Duration
}

// OK reports a 2xx.
func (r Result) OK() bool { return r.Class == "" && r.Status >= 200 && r.Status < 300 }

// Permanent reports a failure that a retry cannot fix.
func (r Result) Permanent() bool {
	switch r.Class {
	case ErrClassRejected, ErrClassGone, ErrClassRedirect:
		return true
	}
	return false
}

// SenderOptions configure a Sender.
type SenderOptions struct {
	AllowPrivate bool
	Timeout      time.Duration
	Version      string
	// LookupIP resolves a name (nil: the system resolver). Tests use it to make a
	// name resolve to an internal address.
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
	// Classify and TLS are test seams: a loopback test server has to count as a
	// public address, and a test server's certificate is not in the system's roots.
	Classify func(net.IP) netguard.Class
	TLS      *tls.Config
}

// Sender makes the requests. Every connection goes through the network guard:
// the address that is connected to is the one that is checked, after the name
// was resolved, so a name that resolves to an internal address, or one whose
// answer changes between lookups, is refused. No proxy from the environment is
// used (the proxy would be the address checked, not the target), redirects are
// not followed, connections are not reused, and what is read of a response is
// bounded.
type Sender struct {
	o       SenderOptions
	public  *http.Client // https
	local   *http.Client // plain http: only addresses on this machine or its network
	timeout time.Duration
}

// NewSender returns a Sender.
func NewSender(o SenderOptions) *Sender {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	s := &Sender{o: o, timeout: o.Timeout}
	mode := netguard.PublicOnly
	if o.AllowPrivate {
		mode = netguard.AllowLocal
	}
	s.public = s.client(mode)
	s.local = s.client(netguard.RequireLocal)
	return s
}

func (s *Sender) client(mode netguard.Mode) *http.Client {
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            netguard.Dialer(netguard.Options{Mode: mode, DialTimeout: s.timeout, LookupIP: s.o.LookupIP, Classify: s.o.Classify}),
		TLSClientConfig:        s.o.TLS,
		TLSHandshakeTimeout:    s.timeout,
		ResponseHeaderTimeout:  s.timeout,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 16 << 10,
		ForceAttemptHTTP2:      false,
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       s.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Send posts body to target with the headers and reports what came of it.
func (s *Sender) Send(ctx context.Context, target string, h http.Header, body []byte) Result {
	start := time.Now()
	u, err := url.Parse(target)
	if err != nil {
		return Result{Class: ErrClassNetwork}
	}
	c := s.public
	if u.Scheme == "http" {
		// Plain http is only for the local network, and only where the administrator
		// allowed local targets. A webhook that was made while they were allowed
		// stops being able to reach them the day they are not.
		if !s.o.AllowPrivate {
			return Result{Class: ErrClassBlocked, Took: time.Since(start)}
		}
		c = s.local
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Result{Class: ErrClassNetwork}
	}
	for k, v := range h {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Scriobh-Webhook/"+s.o.Version)
	req.Header.Set("Accept", "*/*")
	resp, err := c.Do(req)
	if err != nil {
		return Result{Class: classify(err), Took: time.Since(start)}
	}
	defer resp.Body.Close()
	res := Result{Status: resp.StatusCode, Took: time.Since(start)}
	// A bounded look at the answer, only to keep a few words of a failure; the
	// rest is not read, and the connection is closed rather than drained.
	var excerpt []byte
	if resp.StatusCode >= 300 {
		excerpt, _ = io.ReadAll(io.LimitReader(resp.Body, excerptRead))
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		res.Class = ErrClassRedirect
	case resp.StatusCode == http.StatusGone:
		res.Class = ErrClassGone
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusRequestTimeout:
		res.Class = ErrClassBusy
		if secs, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && secs > 0 {
			res.RetryAfter = time.Duration(secs) * time.Second
			if res.RetryAfter > MaxRetryAfter {
				res.RetryAfter = MaxRetryAfter
			}
		}
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		res.Class = ErrClassRejected
	default:
		res.Class = ErrClassServer
	}
	if res.Class != "" {
		res.Excerpt = Excerpt(excerpt)
	}
	return res
}

func classify(err error) string {
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		return ErrClassBlocked
	case errors.Is(err, context.DeadlineExceeded):
		return ErrClassTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ErrClassTimeout
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return ErrClassDNS
	}
	var cie *tls.CertificateVerificationError
	var rhe tls.RecordHeaderError
	if errors.As(err, &cie) || errors.As(err, &rhe) || strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "x509:") {
		return ErrClassTLS
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return ErrClassRefused
	}
	if strings.Contains(err.Error(), "no address for the host") {
		return ErrClassDNS
	}
	return ErrClassNetwork
}

// Excerpt turns the start of a response into a short line that is safe to
// store and show: valid text, no control characters, one line, at most 160
// characters, and nothing that looks like markup is kept as such (it is only
// ever displayed as text, and angle brackets are removed besides).
func Excerpt(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if !utf8.Valid(b) {
		b = []byte(strings.ToValidUTF8(string(b), " "))
	}
	var out strings.Builder
	n := 0
	space := true
	for _, r := range string(b) {
		switch {
		case r == '<' || r == '>' || r == '&' || r == '"' || r == '\'':
			r = ' '
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || r == utf8.RuneError:
			r = ' '
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		out.WriteRune(r)
		if n++; n >= excerptChars {
			break
		}
	}
	return strings.TrimSpace(out.String())
}

package gitmirror

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// The network guard. Every connection Git sync makes goes through it:
//
//   - HTTPS (clone, push, ls-remote, fetch): go-git is given Scríobh's own HTTP
//     client, whose dialer checks the address it is about to connect to (the
//     resolved one, at connect time, so a name that resolves to an internal
//     address, or a redirect, or a DNS answer that changes between lookups, is
//     caught). The client never uses a proxy from the environment, because a
//     proxy would be the address checked, not the target.
//   - SSH: go-git's SSH dial has no hook, so the name is resolved here, checked,
//     and go-git is handed the address as an IP literal (the host key pin does
//     not depend on the name used to connect).
//   - The host key scan uses the same dialer.
//
// Loopback, link-local (which holds the cloud metadata address), private
// (RFC 1918 and unique-local), carrier-grade NAT, "this network" and reserved
// ranges are refused unless the administrator opts in with
// SCRIOBH_GIT_ALLOW_PRIVATE=1 (a Forgejo on the LAN is the common reason).
// What a caller is told is generic: no resolved address, no port, no reason
// the connection failed. The detail goes to the server log.

var (
	allowPrivate atomic.Bool
	maxBodyBytes atomic.Int64
	detailLog    atomic.Pointer[slog.Logger]
)

// errBlockedAddr is returned by the dialer for an address that is not allowed.
var errBlockedAddr = errors.New("address not allowed")

// errBodyTooLarge is returned when a response is larger than a clone may be.
var errBodyTooLarge = errors.New("response too large")

const (
	idleTimeout      = 60 * time.Second // no bytes in either direction for this long ends a connection
	errBodyLimit     = 64 << 10         // go-git keeps a refused request's body in memory; this much of it
	maxResponseHdrs  = 64 << 10
	maxRedirectsHops = 3
)

// configureNet sets what the guard enforces. It is called by New.
func configureNet(allow bool, maxRepoBytes int64, log *slog.Logger) {
	allowPrivate.Store(allow)
	maxBodyBytes.Store(maxRepoBytes + 16<<20)
	detailLog.Store(log)
	// x/net/proxy, used by go-git's SSH dial, reads ALL_PROXY from the
	// environment; a proxy would be the address that is checked, not the target.
	for _, k := range []string{"ALL_PROXY", "all_proxy"} {
		if os.Getenv(k) != "" {
			_ = os.Unsetenv(k)
			log.Warn("git: " + k + " is ignored: Git sync connects directly and checks the address it connects to")
		}
	}
}

// blockedIP reports whether an address is one Git sync must not connect to. The
// address classes are the shared ones in internal/netguard (AI providers use
// them too); everything that is not public is refused unless the operator opts in.
func blockedIP(ip net.IP) bool { return netguard.BlockedIP(ip) }

// guardControl is the dialer's Control hook: it sees the address about to be
// connected to, after resolution, for every candidate address.
func guardControl(network, address string, _ syscall.RawConn) error {
	if allowPrivate.Load() {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errBlockedAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || blockedIP(ip) {
		return errBlockedAddr
	}
	return nil
}

// newDialer is the only dialer Git sync uses.
func newDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: guardControl}
}

// idleConn ends a connection that has been silent too long, in either direction.
type idleConn struct{ net.Conn }

func (c idleConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(idleTimeout))
	return c.Conn.Read(b)
}

func (c idleConn) Write(b []byte) (int, error) {
	_ = c.Conn.SetWriteDeadline(time.Now().Add(idleTimeout))
	return c.Conn.Write(b)
}

func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	netguard.Note(network, addr)
	c, err := newDialer(dialTimeout).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return idleConn{c}, nil
}

// limitedBody bounds a response body. A refused request's body is cut off
// quietly at errBodyLimit (go-git keeps it in memory for the error message); a
// successful one fails with errBodyTooLarge past the limit, which is the most a
// clone may be.
type limitedBody struct {
	rc    io.ReadCloser
	left  int64
	quiet bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	if b.left <= 0 {
		if b.quiet {
			return 0, io.EOF
		}
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.rc.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *limitedBody) Close() error { return b.rc.Close() }

// limitTransport wraps the transport so that every response body, whatever the
// server sends, is bounded. It wraps outside the transport, so what is counted
// is what go-git would read (after any transparent decompression).
type limitTransport struct{ base http.RoundTripper }

func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	if resp.StatusCode >= 300 {
		resp.Body = &limitedBody{rc: resp.Body, left: errBodyLimit, quiet: true}
	} else {
		resp.Body = &limitedBody{rc: resp.Body, left: maxBodyBytes.Load()}
	}
	return resp, nil
}

// httpTransport is the one transport go-git uses for HTTPS.
var httpTransport = &http.Transport{
	Proxy:                  nil, // never a proxy from the environment
	DialContext:            guardedDial,
	TLSHandshakeTimeout:    15 * time.Second,
	ResponseHeaderTimeout:  60 * time.Second,
	MaxResponseHeaderBytes: maxResponseHdrs,
	MaxIdleConns:           4,
	IdleConnTimeout:        30 * time.Second,
	TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
}

// checkRedirect follows at most a few redirects, and only to https. The dialer
// checks the address of every hop.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirectsHops {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return errors.New("redirect to a scheme other than https")
	}
	// The address was entered, and its credential bound to it, by the owner: a
	// server that sends us to another host or port is not that address. (A move
	// within the host, a renamed repository, is followed.)
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		return errors.New("redirect to another origin")
	}
	return nil
}

func init() {
	hc := &http.Client{Transport: limitTransport{httpTransport}, CheckRedirect: checkRedirect}
	client.InstallProtocol("https", githttp.NewClientWithOptions(hc, &githttp.ClientOptions{RedirectPolicy: githttp.FollowInitialRedirects}))
	// Only https and ssh (and the in-process file scheme the tests use) exist.
	client.InstallProtocol("http", nil)
	client.InstallProtocol("git", nil)
}

// ---- SSH: resolve, check, and connect to the literal ------------------------------

// resolveAllowed resolves host and returns the first address that is allowed.
func resolveAllowed(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !allowPrivate.Load() && blockedIP(ip) {
			return nil, errBlockedAddr
		}
		return ip, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	netguard.Note("lookup", net.JoinHostPort(host, "0"))
	addrs, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil {
		return nil, err
	}
	blocked := false
	for _, a := range addrs {
		if allowPrivate.Load() || !blockedIP(a.IP) {
			return a.IP, nil
		}
		blocked = true
	}
	if blocked {
		return nil, errBlockedAddr
	}
	return nil, fmt.Errorf("no address for %s", host)
}

// dialURL gives the address go-git is to connect to. For SSH it is the
// remote's address with the host replaced by the checked IP; anything else is
// returned as it is (HTTPS is guarded at the dialer, files have no network).
func dialURL(ctx context.Context, raw string, ep endpointInfo) (string, error) {
	if ep.Proto != "ssh" {
		return raw, nil
	}
	ip, err := resolveAllowed(ctx, ep.Host)
	if err != nil {
		return "", err
	}
	hostPort := net.JoinHostPort(ip.String(), strconv.Itoa(ep.Port))
	if !strings.Contains(raw, "://") && ip.To4() != nil && ep.Port == 22 {
		// scp-style: keep the path exactly as written (it is relative).
		user := ep.User
		_, rest, _ := strings.Cut(raw, ":")
		return user + "@" + ip.String() + ":" + rest, nil
	}
	path := ep.Path
	if !strings.HasPrefix(path, "/") {
		// An scp-style relative path cannot be written in the ssh:// form; the
		// forges accept it as absolute.
		path = "/" + path
	}
	u := url.URL{Scheme: "ssh", User: url.User(ep.User), Host: hostPort, Path: path}
	return u.String(), nil
}

// logDetail records what a caller is not told.
func logTransportDetail(code string, err error, scrub ...string) {
	if l := detailLog.Load(); l != nil && err != nil {
		l.Debug("git: transport error detail", "code", code, "detail", redactAll(err.Error(), scrub...))
	}
}

func redactAll(s string, scrub ...string) string {
	for _, v := range scrub {
		if len(v) >= 4 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

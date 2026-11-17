// Package netguard is the outbound network guard shared by everything that
// connects to an address a person typed: Git sync and AI providers.
//
// The rule is enforced at connect time, on the address that is about to be
// dialled, so a name that resolves to an internal address, a redirect, or a DNS
// answer that changes between lookups is caught. Three modes decide what may be
// reached:
//
//   - PublicOnly: only public addresses. Loopback, private (RFC 1918 and
//     unique-local), carrier-grade NAT, link-local (which holds the cloud
//     metadata address), "this network", multicast and reserved ranges are
//     refused.
//   - AllowLocal: public addresses and loopback, private and carrier-grade NAT
//     addresses. Link-local, the cloud metadata addresses and the reserved
//     ranges stay refused. An administrator opts one endpoint in (a model
//     server on the LAN is the common reason).
//   - RequireLocal: only loopback, private and carrier-grade NAT addresses. A
//     project that must never send text off the machine or the private network
//     uses it, and it holds whatever the endpoint's own flag says: a name that
//     now resolves to a public address is refused.
//
// A connection never goes through a proxy from the environment: the proxy would
// be the address checked, not the target.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"
)

// Mode says what a dialer may reach.
type Mode int

// Modes.
const (
	PublicOnly Mode = iota
	AllowLocal
	RequireLocal
)

// Class says what kind of place an address is.
type Class int

// Classes.
const (
	// Public is an address on the internet.
	Public Class = iota
	// Local is loopback, a private range or carrier-grade NAT: a place on this
	// machine or its own network.
	Local
	// Blocked is never connected to, in any mode.
	Blocked
)

func (c Class) String() string {
	switch c {
	case Public:
		return "public"
	case Local:
		return "local"
	}
	return "blocked"
}

// ErrBlocked is returned by a dial the guard refused. It carries no address:
// what a caller is told is generic, the detail is for the server log.
var ErrBlocked = errors.New("address not allowed")

// ErrTooLarge is returned when a bounded reader passes its limit.
var ErrTooLarge = errors.New("response too large")

// Classify says what kind of place ip is. An IPv4 address written as IPv6 is
// read as IPv4, and so is one embedded in an address that carries it (the
// IPv4-compatible ::a.b.c.d and 6to4, 2002::/16): what matters is where the
// packet ends up. Transition ranges that have no business carrying a request
// from this server (Teredo, NAT64) are blocked outright.
func Classify(ip net.IP) Class {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if ip == nil {
		return Blocked
	}
	if len(ip) == net.IPv6len {
		if embedded := embeddedIPv4(ip); embedded != nil {
			if c := Classify(embedded); c != Public {
				return Blocked // 6to4 or ::a.b.c.d aimed at an internal address
			}
			if ip[0] == 0 { // ::a.b.c.d to a public address is still not a normal destination
				return Blocked
			}
			return Public
		}
	}
	switch {
	case ip.IsUnspecified(), ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast(), ip.IsInterfaceLocalMulticast(), ip.IsMulticast():
		return Blocked
	case ip.IsLoopback():
		return Local
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10, ip4[0] == 172 && ip4[1]&0xf0 == 16, ip4[0] == 192 && ip4[1] == 168:
			return Local
		case ip4[0] == 100 && ip4[1] == 100 && ip4[2] == 100 && ip4[3] == 200: // a cloud metadata address inside CGNAT
			return Blocked
		case ip4[0] == 100 && ip4[1]&0xc0 == 64: // 100.64.0.0/10, carrier-grade NAT (Tailscale lives here)
			return Local
		case ip4[0] == 168 && ip4[1] == 63 && ip4[2] == 129 && ip4[3] == 16: // Azure's platform address (metadata, DNS)
			return Blocked
		case ip4[0] == 0: // 0.0.0.0/8, this network
			return Blocked
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24, IETF protocol assignments
			return Blocked
		case ip4[0] == 192 && ip4[1] == 88 && ip4[2] == 99: // 192.88.99.0/24, the retired 6to4 relay anycast
			return Blocked
		case ip4[0] == 198 && (ip4[1] == 18 || ip4[1] == 19): // 198.18.0.0/15, benchmarking
			return Blocked
		case ip4[0] >= 240: // 240.0.0.0/4, reserved, and broadcast
			return Blocked
		}
		return Public
	}
	// IPv6.
	if ip[0] == 0xfd && ip[1] == 0x00 && ip[2] == 0x0e && ip[3] == 0xc2 && ip[15] == 0x54 { // fd00:ec2::254, a cloud metadata address
		return Blocked
	}
	if ip.IsPrivate() { // fc00::/7
		return Local
	}
	switch {
	case ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8: // 2001:db8::/32, documentation
		return Blocked
	case ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0 && ip[3] == 0: // 2001::/32, Teredo
		return Blocked
	case ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b: // 64:ff9b::/32, NAT64 (and the local-use 64:ff9b:1::/48)
		return Blocked
	case ip[0] == 0xfe && ip[1]&0xc0 == 0xc0: // fec0::/10, the deprecated site-local range
		return Blocked
	case ip[0] == 0x01 && ip[1] == 0 && ip[2] == 0 && ip[3] == 0 && ip[4] == 0 && ip[5] == 0 && ip[6] == 0 && ip[7] == 0: // 100::/64, discard-only
		return Blocked
	}
	return Public
}

// embeddedIPv4 returns the IPv4 address an IPv6 address carries: the last four
// bytes of ::a.b.c.d (the IPv4-compatible form, except :: and ::1) or the four
// after the prefix of a 6to4 address (2002:aabb:ccdd::). Nil when it carries none.
func embeddedIPv4(ip net.IP) net.IP {
	if ip[0] == 0x20 && ip[1] == 0x02 {
		return net.IPv4(ip[2], ip[3], ip[4], ip[5]).To4()
	}
	zero := true
	for _, b := range ip[:12] {
		zero = zero && b == 0
	}
	if zero && !(ip[12] == 0 && ip[13] == 0 && ip[14] == 0 && (ip[15] == 0 || ip[15] == 1)) {
		return net.IPv4(ip[12], ip[13], ip[14], ip[15]).To4()
	}
	return nil
}

// BlockedIP reports whether an address is one that must not be connected to
// unless the administrator opted in: everything that is not public. It is the
// rule Git sync applies.
func BlockedIP(ip net.IP) bool { return Classify(ip) != Public }

// Allowed reports whether a dial in this mode may reach an address of class c.
func (m Mode) Allowed(c Class) bool {
	switch m {
	case AllowLocal:
		return c == Public || c == Local
	case RequireLocal:
		return c == Local
	}
	return c == Public
}

// Options configure a dialer.
type Options struct {
	Mode        Mode
	DialTimeout time.Duration
	// LookupIP resolves a name. Nil uses the system resolver. It is a seam for
	// tests (a name that changes its answer between lookups).
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
	// Classify overrides Classify. It is a seam for tests, which need a
	// loopback server to count as public (or as somewhere else).
	Classify func(net.IP) Class
}

func (o Options) class(ip net.IP) Class {
	if o.Classify != nil {
		return o.Classify(ip)
	}
	return Classify(ip)
}

// Dialer returns a DialContext function that resolves the name itself, drops
// every address the mode refuses, and connects to the first that answers. The
// address checked is the address connected to: the connection is made to the IP
// literal, and the socket's own Control hook checks it again.
func Dialer(o Options) func(ctx context.Context, network, addr string) (net.Conn, error) {
	timeout := o.DialTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		Note(network, addr) // before the name is looked up: a lookup is a request too
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, ErrBlocked
		}
		var ips []net.IP
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IP{ip}
		} else {
			lookup := o.LookupIP
			if lookup == nil {
				lookup = func(ctx context.Context, h string) ([]net.IP, error) {
					addrs, err := net.DefaultResolver.LookupIPAddr(ctx, h)
					if err != nil {
						return nil, err
					}
					out := make([]net.IP, 0, len(addrs))
					for _, a := range addrs {
						out = append(out, a.IP)
					}
					return out, nil
				}
			}
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if ips, err = lookup(rctx, host); err != nil {
				return nil, err
			}
		}
		var ok []net.IP
		for _, ip := range ips {
			if o.Mode.Allowed(o.class(ip)) {
				ok = append(ok, ip)
			}
		}
		if len(ok) == 0 {
			if len(ips) == 0 {
				return nil, fmt.Errorf("no address for the host")
			}
			return nil, ErrBlocked
		}
		d := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
			h, _, err := net.SplitHostPort(address)
			if err != nil {
				return ErrBlocked
			}
			ip := net.ParseIP(h)
			if ip == nil || !o.Mode.Allowed(o.class(ip)) {
				return ErrBlocked
			}
			return nil
		}}
		var last error
		for _, ip := range ok {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return c, nil
			}
			last = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, last
	}
}

// ReadLimited reads r to its end and fails with ErrTooLarge if it holds more
// than max bytes. Nothing past max is kept.
func ReadLimited(r io.Reader, max int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return b, err
	}
	if int64(len(b)) > max {
		return b[:max], ErrTooLarge
	}
	return b, nil
}

// LimitedReader fails with ErrTooLarge once more than Max bytes were read.
type LimitedReader struct {
	R    io.Reader
	Max  int64
	read int64
}

// Read implements io.Reader.
func (l *LimitedReader) Read(p []byte) (int, error) {
	if l.read > l.Max {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > l.Max-l.read+1 {
		p = p[:l.Max-l.read+1]
	}
	n, err := l.R.Read(p)
	l.read += int64(n)
	if l.read > l.Max {
		return n - int(l.read-l.Max), ErrTooLarge
	}
	return n, err
}

// Bytes is how many bytes were read.
func (l *LimitedReader) Bytes() int64 { return l.read }

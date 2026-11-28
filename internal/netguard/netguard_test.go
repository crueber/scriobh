package netguard_test

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
)

func TestClasses(t *testing.T) {
	local := []string{"127.0.0.1", "127.8.8.8", "::1", "10.0.0.5", "172.16.0.1", "172.31.255.255", "192.168.1.10", "fd00::1", "fc00::5", "fd00:ec2::1", "fd20:ce::1", "100.64.0.1", "100.127.255.255", "::ffff:10.0.0.1", "::ffff:127.0.0.1"}
	blocked := []string{"0.0.0.0", "0.1.2.3", "::", "169.254.169.254", "169.254.0.1", "fe80::1", "fd00:ec2::254", "fd20:ce::254", "::ffff:169.254.169.254", "::ffff:0:169.254.169.254", "100.100.100.200",
		"224.0.0.1", "ff02::1", "240.0.0.1", "255.255.255.255", "198.18.0.1", "192.0.0.8", "2001:db8::1", "64:ff9b::1",
		// addresses that carry or tunnel an IPv4 address, and other places that are not the internet
		"2002:7f00:1::", "2002:a9fe:a9fe::", "2002:0a00:0001::5", "2002:c0a8:0101::1", "2001:0::1", "2001:0:4136:e378:8000:63bf:3fff:fdd2",
		"::10.0.0.1", "::127.0.0.1", "::169.254.169.254", "::8.8.8.8", "fec0::1", "feff::1", "100::1", "64:ff9b:1::1", "168.63.129.16", "192.88.99.1"}
	public := []string{"8.8.8.8", "140.82.112.3", "100.63.255.255", "100.128.0.1", "172.15.0.1", "172.32.0.1", "192.169.0.1", "2606:4700:4700::1111", "2a00:1450:4001::200e", "2002:0808:0808::1", "168.63.129.15", "168.63.129.17"}
	for class, ips := range map[netguard.Class][]string{netguard.Local: local, netguard.Blocked: blocked, netguard.Public: public} {
		for _, ip := range ips {
			if got := netguard.Classify(net.ParseIP(ip)); got != class {
				t.Errorf("%s is %s, want %s", ip, got, class)
			}
		}
	}
	if netguard.Classify(nil) != netguard.Blocked {
		t.Error("no address is blocked")
	}
	// The modes.
	for _, c := range []struct {
		m    netguard.Mode
		cl   netguard.Class
		want bool
	}{
		{netguard.PublicOnly, netguard.Public, true}, {netguard.PublicOnly, netguard.Local, false}, {netguard.PublicOnly, netguard.Blocked, false},
		{netguard.AllowLocal, netguard.Public, true}, {netguard.AllowLocal, netguard.Local, true}, {netguard.AllowLocal, netguard.Blocked, false},
		{netguard.RequireLocal, netguard.Public, false}, {netguard.RequireLocal, netguard.Local, true}, {netguard.RequireLocal, netguard.Blocked, false},
	} {
		if c.m.Allowed(c.cl) != c.want {
			t.Errorf("mode %d class %s", c.m, c.cl)
		}
	}
}

func listener(t *testing.T) (addr string, accepted *atomic.Int32) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted = &atomic.Int32{}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	t.Cleanup(func() { l.Close() })
	return l.Addr().String(), accepted
}

func TestTheDialerEnforcesTheMode(t *testing.T) {
	addr, accepted := listener(t)
	dial := func(o netguard.Options) error {
		c, err := netguard.Dialer(o)(context.Background(), "tcp", addr)
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := dial(netguard.Options{Mode: netguard.PublicOnly}); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("public only reached loopback: %v", err)
	}
	if err := dial(netguard.Options{Mode: netguard.AllowLocal}); err != nil {
		t.Errorf("allow local: %v", err)
	}
	if err := dial(netguard.Options{Mode: netguard.RequireLocal}); err != nil {
		t.Errorf("require local: %v", err)
	}
	// With the classifier seam saying "public": RequireLocal refuses it, PublicOnly allows it.
	asPublic := func(net.IP) netguard.Class { return netguard.Public }
	if err := dial(netguard.Options{Mode: netguard.RequireLocal, Classify: asPublic}); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("require local reached a public address: %v", err)
	}
	if err := dial(netguard.Options{Mode: netguard.PublicOnly, Classify: asPublic}); err != nil {
		t.Errorf("public only: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // the connections above are counted when the listener's goroutine gets to them
	before := accepted.Load()
	_ = dial(netguard.Options{Mode: netguard.PublicOnly})
	time.Sleep(50 * time.Millisecond)
	if accepted.Load() != before {
		t.Error("a refused dial still connected")
	}
	// The metadata address is refused in every mode, before any connection.
	for _, m := range []netguard.Mode{netguard.PublicOnly, netguard.AllowLocal, netguard.RequireLocal} {
		_, err := netguard.Dialer(netguard.Options{Mode: m, DialTimeout: 300 * time.Millisecond})(context.Background(), "tcp", "169.254.169.254:80")
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("mode %d reached the metadata address: %v", m, err)
		}
	}
	if err := dial(netguard.Options{Mode: netguard.AllowLocal, Classify: func(net.IP) netguard.Class { return netguard.Blocked }}); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("the socket-level check did not hold: %v", err)
	}
}

func TestEveryResolvedAddressIsChecked(t *testing.T) {
	addr, _ := listener(t)
	_, port, _ := net.SplitHostPort(addr)
	// A name with a public and a private answer: only the allowed one may be used, in either order.
	for _, answers := range [][]string{{"10.1.1.1", "127.0.0.1"}, {"127.0.0.1", "10.1.1.1"}} {
		lookup := func(context.Context, string) ([]net.IP, error) {
			var out []net.IP
			for _, a := range answers {
				out = append(out, net.ParseIP(a))
			}
			return out, nil
		}
		o := netguard.Options{Mode: netguard.AllowLocal, LookupIP: lookup, DialTimeout: 300 * time.Millisecond}
		if c, err := netguard.Dialer(o)(context.Background(), "tcp", "name.example:"+port); err != nil {
			t.Errorf("%v: %v", answers, err)
		} else {
			c.Close()
		}
		o.Mode = netguard.PublicOnly
		o.Classify = func(ip net.IP) netguard.Class {
			if ip.IsLoopback() {
				return netguard.Local
			}
			return netguard.Classify(ip)
		}
		if _, err := netguard.Dialer(o)(context.Background(), "tcp", "name.example:"+port); !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%v: a name with only internal answers was dialled: %v", answers, err)
		}
	}
	// A name with no answer is not an address problem.
	_, err := netguard.Dialer(netguard.Options{LookupIP: func(context.Context, string) ([]net.IP, error) { return nil, nil }})(context.Background(), "tcp", "nothing.example:80")
	if err == nil || errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("%v", err)
	}
}

func TestBoundedReaders(t *testing.T) {
	if b, err := netguard.ReadLimited(strings.NewReader("12345"), 5); err != nil || string(b) != "12345" {
		t.Errorf("%q %v", b, err)
	}
	if b, err := netguard.ReadLimited(strings.NewReader("123456"), 5); !errors.Is(err, netguard.ErrTooLarge) || len(b) != 5 {
		t.Errorf("%q %v", b, err)
	}
	lr := &netguard.LimitedReader{R: strings.NewReader(strings.Repeat("x", 100)), Max: 10}
	b, err := io.ReadAll(lr)
	if !errors.Is(err, netguard.ErrTooLarge) || len(b) > 10 || lr.Bytes() <= 10 {
		t.Errorf("%d bytes, %v, counted %d", len(b), err, lr.Bytes())
	}
}

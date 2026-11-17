package webhook

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/netguard"
)

// The sender's own checks, below the address validation: the address that is
// dialled is the one that is checked, whatever name led to it.

func fixedLookup(m map[string][]string) func(context.Context, string) ([]net.IP, error) {
	return func(_ context.Context, host string) ([]net.IP, error) {
		v, ok := m[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		var out []net.IP
		for _, s := range v {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
}

// loopbackIsPublic lets a test server on 127.0.0.1 stand in for a public host.
func loopbackIsPublic(ip net.IP) netguard.Class {
	if ip.IsLoopback() {
		return netguard.Public
	}
	return netguard.Classify(ip)
}

func TestSenderRefusesNamesThatResolveInside(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	port := strings.Split(srv.Listener.Addr().String(), ":")[1]
	s := NewSender(SenderOptions{Timeout: 3 * time.Second, LookupIP: fixedLookup(map[string][]string{
		"loopback.example.com": {"127.0.0.1"}, "private.example.com": {"10.1.2.3"}, "linklocal.example.com": {"169.254.169.254"},
		"v6.example.com": {"::1"}, "mapped.example.com": {"::ffff:127.0.0.1"}, "cgnat.example.com": {"100.64.0.9"}, "mixed.example.com": {"10.0.0.1", "192.168.0.1"},
		"sixtofour.example.com": {"2002:7f00:1::"}, "nat64.example.com": {"64:ff9b::7f00:1"}, "zero.example.com": {"0.0.0.0"},
	})})
	for _, host := range []string{"loopback.example.com", "private.example.com", "linklocal.example.com", "v6.example.com", "mapped.example.com", "cgnat.example.com", "mixed.example.com", "sixtofour.example.com", "nat64.example.com", "zero.example.com"} {
		res := s.Send(context.Background(), "https://"+host+":"+port+"/hook", http.Header{}, []byte("{}"))
		if res.Class != ErrClassBlocked {
			t.Errorf("%s: %+v, want blocked", host, res)
		}
		// plain http to an internal name is the other client's rule (local only), and a name that resolves to a public address is refused there
		res = s.Send(context.Background(), "http://"+host+":"+port+"/hook", http.Header{}, []byte("{}"))
		if res.Class == "" {
			t.Errorf("%s over http: %+v", host, res)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("a refused address was contacted %d times", hits.Load())
	}
	// An unknown name is a DNS failure, not a block, and says nothing about the resolver.
	if res := s.Send(context.Background(), "https://nothing.example.com/", http.Header{}, nil); res.Class != ErrClassDNS {
		t.Errorf("unknown name: %+v", res)
	}
}

// With private targets allowed, loopback and the local network are reachable; link-local never is.
func TestSenderAllowPrivate(t *testing.T) {
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got.Add(1) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	closed := NewSender(SenderOptions{Timeout: 3 * time.Second})
	if res := closed.Send(context.Background(), srv.URL, http.Header{}, []byte("{}")); res.Class != ErrClassBlocked {
		t.Errorf("default sender reached loopback: %+v", res)
	}
	open := NewSender(SenderOptions{AllowPrivate: true, Timeout: 3 * time.Second})
	if res := open.Send(context.Background(), srv.URL, http.Header{}, []byte("{}")); !res.OK() {
		t.Errorf("private allowed, plain http to loopback: %+v", res)
	}
	if got.Load() != 1 {
		t.Errorf("hits %d", got.Load())
	}
	for _, target := range []string{"http://169.254.169.254/latest/meta-data/", "https://169.254.169.254/", "http://[fe80::1]/", "http://0.0.0.0:" + u.Port() + "/"} {
		if res := open.Send(context.Background(), target, http.Header{}, nil); res.Class != ErrClassBlocked {
			t.Errorf("%s: %+v", target, res)
		}
	}
	// Plain http is for the local network only: to a public address the dialer refuses it.
	pub := NewSender(SenderOptions{AllowPrivate: true, Timeout: 3 * time.Second, Classify: loopbackIsPublic})
	if res := pub.Send(context.Background(), srv.URL, http.Header{}, nil); res.Class != ErrClassBlocked {
		t.Errorf("plain http to a public address: %+v", res)
	}
}

// DNS rebinding: a name that is public the first time and internal the second is
// checked at each connection, and nothing is kept alive between deliveries.
func TestSenderRebindingIsCaughtAtConnectTime(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	port := strings.Split(srv.Listener.Addr().String(), ":")[1]
	var calls atomic.Int32
	lookup := func(_ context.Context, host string) ([]net.IP, error) {
		if calls.Add(1) == 1 {
			return []net.IP{net.ParseIP("203.0.113.9")}, nil // public: validation and the first dial would pass
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig
	s := NewSender(SenderOptions{Timeout: 2 * time.Second, LookupIP: lookup, TLS: pool})
	first := s.Send(context.Background(), "https://rebind.example.com:"+port+"/", http.Header{}, nil)
	second := s.Send(context.Background(), "https://rebind.example.com:"+port+"/", http.Header{}, nil)
	if first.Class == ErrClassBlocked {
		t.Errorf("the first lookup was public and should not have been blocked: %+v", first)
	}
	if second.Class != ErrClassBlocked {
		t.Errorf("the rebound lookup was not blocked: %+v", second)
	}
}

func TestSenderTLSAndHeaders(t *testing.T) {
	var seen http.Header
	var body []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
		w.WriteHeader(204)
	}))
	defer srv.Close()
	good := NewSender(SenderOptions{Timeout: 3 * time.Second, Classify: loopbackIsPublic, TLS: srv.Client().Transport.(*http.Transport).TLSClientConfig, Version: "9.9"})
	h := http.Header{}
	h.Set(HeaderID, "whd_1")
	h.Set("Authorization", "Bearer should-not-be-needed")
	res := good.Send(context.Background(), srv.URL+"/p?x=1", h, []byte(`{"a":1}`))
	if !res.OK() || res.Status != 204 {
		t.Fatalf("%+v", res)
	}
	if seen.Get("User-Agent") != "Scriobh-Webhook/9.9" || seen.Get("Content-Type") != "application/json" || seen.Get(HeaderID) != "whd_1" || string(body) != `{"a":1}` {
		t.Errorf("request: %v %s", seen, body)
	}
	// A certificate the system does not trust fails as tls, and a plain default sender does not skip verification.
	strict := NewSender(SenderOptions{Timeout: 3 * time.Second, Classify: loopbackIsPublic})
	if res := strict.Send(context.Background(), srv.URL, http.Header{}, nil); res.Class != ErrClassTLS {
		t.Errorf("untrusted certificate: %+v", res)
	}
}

func TestSenderStatusesAndLimits(t *testing.T) {
	var behave atomic.Value
	behave.Store("ok")
	var follow atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { follow.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch behave.Load().(string) {
		case "redirect":
			http.Redirect(w, r, target.URL+"/internal", http.StatusFound)
		case "500":
			w.WriteHeader(500)
			w.Write([]byte("<html>\nBoom &amp; <b>bad</b>\x00 gateway</html>"))
		case "502big":
			w.WriteHeader(502)
			w.Write([]byte(strings.Repeat("A", 5<<20)))
		case "429":
			w.Header().Set("Retry-After", "7200")
			w.WriteHeader(429)
		case "408":
			w.WriteHeader(408)
		case "410":
			w.WriteHeader(410)
		case "404":
			w.WriteHeader(404)
		case "slow":
			time.Sleep(2 * time.Second)
		case "hang-body":
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			time.Sleep(2 * time.Second)
		case "301":
			w.Header().Set("Location", "https://example.com/")
			w.WriteHeader(301)
		default:
			w.WriteHeader(200)
			w.Write([]byte("fine"))
		}
	}))
	defer srv.Close()
	s := NewSender(SenderOptions{AllowPrivate: true, Timeout: 700 * time.Millisecond})
	send := func(mode string) Result {
		behave.Store(mode)
		return s.Send(context.Background(), srv.URL, http.Header{}, []byte("{}"))
	}
	cases := []struct {
		mode      string
		class     string
		status    int
		permanent bool
	}{
		{"ok", "", 200, false}, {"500", ErrClassServer, 500, false}, {"502big", ErrClassServer, 502, false}, {"429", ErrClassBusy, 429, false}, {"408", ErrClassBusy, 408, false},
		{"410", ErrClassGone, 410, true}, {"404", ErrClassRejected, 404, true}, {"redirect", ErrClassRedirect, 302, true}, {"301", ErrClassRedirect, 301, true},
		{"slow", ErrClassTimeout, 0, false},
	}
	for _, c := range cases {
		res := send(c.mode)
		if res.Class != c.class || res.Status != c.status || res.Permanent() != c.permanent {
			t.Errorf("%s: %+v, want class %q status %d permanent %v", c.mode, res, c.class, c.status, c.permanent)
		}
		if len([]rune(res.Excerpt)) > excerptChars || strings.ContainsAny(res.Excerpt, "<>&\x00\n") {
			t.Errorf("%s: excerpt %q", c.mode, res.Excerpt)
		}
	}
	if follow.Load() != 0 {
		t.Errorf("a redirect was followed %d times", follow.Load())
	}
	if r := send("429"); r.RetryAfter != MaxRetryAfter {
		t.Errorf("Retry-After of two hours should be capped at %s: %s", MaxRetryAfter, r.RetryAfter)
	}
	if r := send("500"); !strings.Contains(r.Excerpt, "Boom") || strings.Contains(r.Excerpt, "<") {
		t.Errorf("excerpt: %q", r.Excerpt)
	}
	// A success keeps nothing of the response.
	if r := send("ok"); r.Excerpt != "" {
		t.Errorf("a success kept %q", r.Excerpt)
	}
	// A connection that goes nowhere is refused, not hung.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	if r := s.Send(context.Background(), "http://"+addr+"/", http.Header{}, nil); r.Class != ErrClassRefused {
		t.Errorf("closed port: %+v", r)
	}
	_ = tls.VersionTLS12
}

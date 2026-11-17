package webhook

import (
	"strings"
	"testing"
	"time"
)

// Signature vectors computed independently (Python's hmac and hashlib): the
// signature is lowercase hex HMAC-SHA256 under the secret of "<timestamp>.<body>".
func TestSignatureVectors(t *testing.T) {
	cases := []struct {
		secret string
		ts     int64
		body   string
		want   string
	}{
		{"whsec_test_secret_0123456789", 1700000000, `{"id":"whd_1","type":"ping"}`, "bd97a24d44eea386b45135275484a1f71d423a5da478f96606ea89a5c3302149"},
		{"k", 0, ``, "6b4a4b8b3c40f1e8f53a3d36682e5f99f7ad2ac1df1c93dfe336f329167641e7"},
		{"whsec_ü", 1700000001, `{"data":{"text":"é"}}`, "b79d03f6ee815833d97508e4b9e606ddc7df06ef8e81e5333d7c7f59c1f3ce69"},
	}
	for _, c := range cases {
		if got := Sign(c.secret, c.ts, []byte(c.body)); got != c.want {
			t.Errorf("Sign(%q, %d, %q) = %s, want %s", c.secret, c.ts, c.body, got, c.want)
		}
	}
}

func TestVerify(t *testing.T) {
	secret, body := "whsec_abc", []byte(`{"id":"whd_1"}`)
	now := time.Unix(1700000100, 0)
	hdr := SignatureHeader(1700000000, body, secret)
	ts := "1700000000"
	if !Verify(secret, hdr, ts, body, now, 5*time.Minute) {
		t.Fatal("a correct delivery was refused")
	}
	for name, ok := range map[string]bool{
		"altered body":      Verify(secret, hdr, ts, []byte(`{"id":"whd_2"}`), now, 5*time.Minute),
		"altered timestamp": Verify(secret, hdr, "1700000001", body, now, 5*time.Minute),
		"another secret":    Verify("whsec_other", hdr, ts, body, now, 5*time.Minute),
		"too old":           Verify(secret, hdr, ts, body, now.Add(time.Hour), 5*time.Minute),
		"from the future":   Verify(secret, hdr, ts, body, now.Add(-time.Hour), 5*time.Minute),
		"no header":         Verify(secret, "", ts, body, now, 5*time.Minute),
		"junk header":       Verify(secret, "v1=zz, v2=abc, nonsense", ts, body, now, 5*time.Minute),
		"wrong scheme":      Verify(secret, "v0="+Sign(secret, 1700000000, body), ts, body, now, 5*time.Minute),
		"bad timestamp":     Verify(secret, hdr, "soon", body, now, 5*time.Minute),
		"empty timestamp":   Verify(secret, hdr, "", body, now, 5*time.Minute),
		"truncated sig":     Verify(secret, hdr[:len(hdr)-4], ts, body, now, 5*time.Minute),
	} {
		if ok {
			t.Errorf("%s was accepted", name)
		}
	}
	// During a rotation a delivery carries two signatures; either secret verifies it.
	both := SignatureHeader(1700000000, body, "whsec_new", "whsec_old")
	if strings.Count(both, "v1=") != 2 || !Verify("whsec_new", both, ts, body, now, 5*time.Minute) || !Verify("whsec_old", both, ts, body, now, 5*time.Minute) {
		t.Errorf("rotation header: %s", both)
	}
	if SignatureHeader(1, body, "", "") != "" {
		t.Error("empty secrets must produce no signature")
	}
}

func TestSecretShape(t *testing.T) {
	a, b := NewSecret(), NewSecret()
	if a == b || !strings.HasPrefix(a, SecretPrefix) || len(a) < 40 {
		t.Errorf("secrets: %q %q", a, b)
	}
	if h := Hint(a); strings.Contains(h, a[len(SecretPrefix)+4:]) || !strings.HasPrefix(h, a[:len(SecretPrefix)+4]) {
		t.Errorf("hint %q gives away too much of %q", h, a)
	}
}

// The schedule, exactly, with the jitter pinned at its extremes and its middle.
func TestBackoffTable(t *testing.T) {
	bases := []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for i, base := range bases {
		failed := i + 1
		lo, mid, hi := Backoff(failed, 0), Backoff(failed, 0.5), Backoff(failed, 0.999999)
		if lo != time.Duration(float64(base)*0.8) || mid != base || hi < time.Duration(float64(base)*1.19) || hi > time.Duration(float64(base)*1.2) {
			t.Errorf("after %d failures: %s %s %s around %s", failed, lo, mid, hi, base)
		}
	}
	for _, f := range []int{0, -3} {
		if Backoff(f, 0.5) != 10*time.Second {
			t.Errorf("%d failures is read as the first", f)
		}
	}
	if Backoff(1000, 0.5) != 24*time.Hour {
		t.Error("the schedule must stop growing")
	}
	// Out-of-range randomness cannot push the wait outside the band.
	if Backoff(3, -5) != Backoff(3, 0) || Backoff(3, 7) > time.Duration(float64(5*time.Minute)*1.2) {
		t.Error("jitter is not clamped")
	}
	// Strictly increasing until the cap, for the middle of the band.
	prev := time.Duration(0)
	for f := 1; f <= 8; f++ {
		if d := Backoff(f, 0.5); d <= prev {
			t.Errorf("backoff does not grow at %d", f)
		} else {
			prev = d
		}
	}
}

func TestExcerptIsCleanAndShort(t *testing.T) {
	cases := map[string]string{
		"":                                  "",
		"plain text":                        "plain text",
		"<html><body>Bad\r\nGateway</body>": "html body Bad Gateway /body",
		"a\x00b\x1b[31mc":                   "a b [31mc",
		"tab\tand   spaces":                 "tab and spaces",
		"\xff\xfe bytes":                    "bytes",
		"it's \"quoted\" & done":            "it s quoted done",
	}
	for in, want := range cases {
		if got := Excerpt([]byte(in)); got != want {
			t.Errorf("Excerpt(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Excerpt([]byte(strings.Repeat("é", 1000))); len([]rune(got)) != excerptChars {
		t.Errorf("long excerpt has %d characters", len([]rune(got)))
	}
}

// The address checks. Everything here is refused by reading the address alone,
// before any resolver is asked; the dialer checks the connected address again
// (TestSenderRefuses...).
func TestValidateURL(t *testing.T) {
	good := []string{
		"https://example.com/hook", "https://hooks.example.com:8443/a/b?c=d", "HTTPS://Example.COM/x", "https://a.b.c.example.org",
		"https://203.0.113.7/hook", "https://[2001:4860:4860::8888]/hook", "https://8.8.8.8:8443/", "https://example.com./x", "https://under_score.example.com/",
	}
	for _, u := range good {
		if got, err := ValidateURL(u, false); err != nil {
			t.Errorf("%s: %v", u, err)
		} else if !strings.HasPrefix(got, "https://") || strings.Contains(got, "EXAMPLE") {
			t.Errorf("%s was not canonicalised: %s", u, got)
		}
	}
	bad := []string{
		"", "   ", "example.com", "//example.com/x", "https:example.com", "https://", "https:///path", "https://exa mple.com", "https://example.com/a b",
		"http://example.com/hook", "ftp://example.com/x", "file:///etc/passwd", "javascript:alert(1)", "gopher://example.com", "data:text/plain,hi", "ws://example.com",
		// credentials and tricks with the authority
		"https://user:pw@example.com/", "https://user@example.com/", "https://example.com@evil.example/", "https://evil.example\\@example.com/",
		"https://example.com#@evil.example/", "https://example.com/hook#frag", "https://example.com:443@evil.example/",
		// internal names
		"https://localhost/", "https://LOCALHOST/", "https://foo.localhost/", "https://printer.local/", "https://metadata.google.internal/", "https://intranet/",
		// internal literals, every spelling
		"https://127.0.0.1/", "https://127.1/", "https://127.0.0.2:8080/", "https://0.0.0.0/", "https://0/", "https://10.0.0.1/", "https://172.16.5.4/", "https://172.31.255.255/",
		"https://192.168.0.1/", "https://100.64.0.1/", "https://169.254.169.254/latest/meta-data/", "https://168.63.129.16/", "https://100.100.100.200/", "https://192.0.0.192/",
		"https://198.18.0.1/", "https://240.0.0.1/", "https://255.255.255.255/", "https://224.0.0.1/",
		"https://2130706433/", "https://0x7f000001/", "https://0x7f.0.0.1/", "https://017700000001/", "https://0177.0.0.1/", "https://0x7f.1/", "https://4294967295/", "https://1.2.3/", "https://08.8.8.8/x",
		"https://[::1]/", "https://[::]/", "https://[::ffff:127.0.0.1]/", "https://[::ffff:7f00:1]/", "https://[::ffff:10.0.0.1]/", "https://[::127.0.0.1]/", "https://[0:0:0:0:0:ffff:7f00:1]/",
		"https://[fe80::1]/", "https://[fe80::1%25eth0]/", "https://[fc00::1]/", "https://[fd00:ec2::254]/", "https://[2002:7f00:1::]/", "https://[2002:a9fe:a9fe::]/",
		"https://[64:ff9b::7f00:1]/", "https://[2001::1]/", "https://[2001:db8::1]/", "https://[ff02::1]/", "https://[::ffff:169.254.169.254]/",
		"https://[1.2.3.4]/", "https://[not-an-ip]/",
		// ports
		"https://example.com:0/", "https://example.com:65536/", "https://example.com:abc/", "https://example.com:/",
		// shapes
		"https://example..com/", "https://-bad.example.com/", "https://exam!ple.com/", "https://ex\x00ample.com/", "https://é.example.com/", "https://" + strings.Repeat("a", 300) + ".com/",
		"https://example.com/" + strings.Repeat("a", 2100),
	}
	for _, u := range bad {
		if got, err := ValidateURL(u, false); err == nil {
			t.Errorf("%q was accepted as %q", u, got)
		} else if _, ok := err.(*ErrURL); !ok {
			t.Errorf("%q: not an ErrURL: %v", u, err)
		}
	}
}

func TestValidateURLWithPrivateTargetsAllowed(t *testing.T) {
	ok := []string{
		"http://192.168.1.5:8080/hook", "https://192.168.1.5/hook", "http://10.1.2.3/x", "https://127.0.0.1:9000/", "http://127.0.0.1:9000/", "http://[::1]:9000/",
		"http://[fd12:3456::1]/hook", "https://localhost:8443/", "http://localhost:8443/", "http://forgejo.lan/hook", "http://100.64.1.1/hook", "https://example.com/hook",
	}
	for _, u := range ok {
		if _, err := ValidateURL(u, true); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	// Even then: link-local (the metadata address), reserved ranges, odd spellings, and plain http to a public address.
	bad := []string{
		"http://169.254.169.254/", "https://169.254.169.254/", "http://[fe80::1]/", "http://0.0.0.0/", "https://240.0.0.1/", "http://[64:ff9b::7f00:1]/", "https://[2002:a9fe:a9fe::]/",
		"http://8.8.8.8/hook", "http://[2001:4860:4860::8888]/", "http://2130706433/", "http://0x7f000001/", "https://user@192.168.1.5/", "ftp://192.168.1.5/",
		"https://100.100.100.200/",
	}
	for _, u := range bad {
		if got, err := ValidateURL(u, true); err == nil {
			t.Errorf("%q was accepted as %q with private targets allowed", u, got)
		}
	}
}

// Eight attempts are seven waits, so the default schedule gives a receiver about 20 hours, not two days; the
// comment on schedule and the documentation say so, and this pins the arithmetic they quote.
func TestDefaultScheduleIsAboutTwentyHours(t *testing.T) {
	const attempts = 8 // SCRIOBH_WEBHOOK_MAX_ATTEMPTS default
	var total time.Duration
	for failed := 1; failed < attempts; failed++ {
		total += Backoff(failed, 0.5)
	}
	if want := 20*time.Hour + 36*time.Minute + 10*time.Second; total != want {
		t.Errorf("seven waits add up to %s, want 20h36m10s", total)
	}
}

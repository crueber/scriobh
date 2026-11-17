package secrets

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

var key = []byte("0123456789abcdef0123456789abcdef-instance-key")

func TestSealOpenRoundTrip(t *testing.T) {
	b := New(key)
	if !b.Enabled() {
		t.Fatal("a Box with a key must be enabled")
	}
	plain := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n")
	s1, err := b.Seal(plain, "remote/grm_1/secret")
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := b.Seal(plain, "remote/grm_1/secret")
	if s1 == s2 {
		t.Error("two seals of the same value must differ (fresh nonce)")
	}
	if strings.Contains(s1, "PRIVATE") || strings.Contains(s1, "secret") {
		t.Errorf("sealed text leaks the value: %s", s1)
	}
	got, err := b.Open(s1, "remote/grm_1/secret")
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open = %q, %v", got, err)
	}
}

func TestOpenRefusesWrongContextKeyAndTampering(t *testing.T) {
	b := New(key)
	s, _ := b.Seal([]byte("token"), "ctx-a")
	if _, err := b.Open(s, "ctx-b"); !errors.Is(err, ErrCannotOpen) {
		t.Errorf("another context opened it: %v", err)
	}
	if _, err := New([]byte("a-different-instance-key-of-32-chars!!")).Open(s, "ctx-a"); !errors.Is(err, ErrCannotOpen) {
		t.Errorf("another key opened it: %v", err)
	}
	tampered := s[:len(s)-2] + "AA"
	if _, err := b.Open(tampered, "ctx-a"); !errors.Is(err, ErrCannotOpen) {
		t.Errorf("a tampered value opened: %v", err)
	}
	for _, bad := range []string{"", "plain text", "v1.", "v1.!!!"} {
		if _, err := b.Open(bad, "ctx-a"); !errors.Is(err, ErrCannotOpen) {
			t.Errorf("Open(%q) = %v, want ErrCannotOpen", bad, err)
		}
	}
	if !b.CanOpen(s, "ctx-a") || b.CanOpen(s, "ctx-b") {
		t.Error("CanOpen disagrees with Open")
	}
}

func TestDisabledBoxNeverStoresPlaintext(t *testing.T) {
	for _, b := range []*Box{New(nil), New([]byte{}), nil} {
		if b.Enabled() {
			t.Error("a Box without a key must be disabled")
		}
		if s, err := b.Seal([]byte("x"), "c"); !errors.Is(err, ErrNoKey) || s != "" {
			t.Errorf("Seal without a key = %q, %v", s, err)
		}
		if _, err := b.Open("v1.abc", "c"); !errors.Is(err, ErrNoKey) {
			t.Errorf("Open without a key = %v", err)
		}
	}
}

func TestRedact(t *testing.T) {
	got := Redact("push to https://u:ghp_abcdef123456@host failed: ghp_abcdef123456", "ghp_abcdef123456", "", "ab")
	if strings.Contains(got, "ghp_") {
		t.Errorf("not redacted: %s", got)
	}
	if !strings.Contains(Redact("keep ab", "ab"), "ab") {
		t.Error("values under four characters are not redacted (they would mangle text)")
	}
}

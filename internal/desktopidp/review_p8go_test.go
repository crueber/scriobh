package desktopidp

import "testing"

// A proof, expected to pass: the Host check is an exact comparison, so every other spelling of the same address is refused.
func TestRevP8HostSpellings(t *testing.T) {
	base := "http://127.0.0.1:53124"
	for _, h := range []string{"127.0.0.1.:53124", "127.1:53124", "0x7f000001:53124", "2130706433:53124", "[::ffff:127.0.0.1]:53124", "[::1]:53124", "localhost:53124",
		"LOCALHOST:53124", "127.0.0.1", "127.0.0.1:53125", "127.0.0.1:053124", " 127.0.0.1:53124", "127.0.0.1:53124 ", "127.0.0.1:53124,evil.example", "", "127.0.0.1:53124@evil.example"} {
		if HostAllowed(h, base) {
			t.Errorf("Host %q was accepted for %s", h, base)
		}
	}
	if !HostAllowed("127.0.0.1:53124", base) || !HostAllowed("[::1]:53124", "http://[::1]:53124") {
		t.Error("the bound address itself was refused")
	}
}

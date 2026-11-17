package desktopidp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTheLaunchTokenIsGoodOnce(t *testing.T) {
	l := NewLaunch(testToken)
	if _, ok := l.Redeem(testToken + "0"); ok {
		t.Fatal("a longer token was accepted")
	}
	if _, ok := l.Redeem(testToken[:len(testToken)-1]); ok {
		t.Fatal("a prefix was accepted")
	}
	if _, ok := l.Redeem(""); ok {
		t.Fatal("an empty token was accepted")
	}
	c, ok := l.Redeem(testToken)
	if !ok || len(c) != 64 {
		t.Fatalf("the right token: %q %v", c, ok)
	}
	if _, ok := l.Redeem(testToken); ok {
		t.Fatal("the token was accepted twice")
	}
}

// There is no lockout: any number of wrong tokens, and the right one still works, once.
func TestWrongTokensNeverLockTheGate(t *testing.T) {
	l := NewLaunch(testToken)
	for i := 0; i < 1000; i++ {
		l.Redeem("wrong")
	}
	if _, ok := l.Redeem(testToken); !ok {
		t.Fatal("wrong tokens kept the right one out")
	}
	for i := 0; i < 20; i++ {
		if _, ok := l.Redeem(testToken); ok {
			t.Fatal("the token was accepted twice")
		}
	}
}

func TestTheLaunchCookieAndFormToken(t *testing.T) {
	l := NewLaunch(testToken)
	c, _ := l.Redeem(testToken)
	rec := httptest.NewRecorder()
	l.SetCookie(rec, c)
	set := rec.Header().Get("Set-Cookie")
	for _, want := range []string{LaunchCookie + "=" + c, "Path=/", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(set, want) {
			t.Errorf("Set-Cookie %q lacks %q", set, want)
		}
	}
	for _, not := range []string{"Secure", "Max-Age", "Expires", "Domain"} {
		if strings.Contains(set, not) {
			t.Errorf("Set-Cookie %q has %q", set, not)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	if l.CookieOK(r) {
		t.Error("a request with no cookie passed")
	}
	r.AddCookie(&http.Cookie{Name: LaunchCookie, Value: c})
	if !l.CookieOK(r) {
		t.Error("the launch cookie was refused")
	}
	tok := l.FormToken("profile")
	if !l.FormTokenOK("profile", tok) || l.FormTokenOK("other", tok) || l.FormTokenOK("profile", "") || l.FormTokenOK("profile", c) {
		t.Error("the form token is not tied to its purpose")
	}
	if NewLaunch(testToken).FormTokenOK("profile", tok) {
		t.Error("a form token of one launch is good in another")
	}
}

func TestHostAllowed(t *testing.T) {
	base := "http://127.0.0.1:5123"
	for host, want := range map[string]bool{
		"127.0.0.1:5123": true, "localhost:5123": false,
		"127.0.0.1": false, "localhost": false, "127.0.0.1:5124": false, "localhost:80": false, "": false,
		"evil.example:5123": false, "127.0.0.1.evil.example:5123": false, "localhost.evil.example:5123": false,
		"LOCALHOST:5123": false, "[::1]:5123": false, "0.0.0.0:5123": false, "127.0.0.2:5123": false, "127.1:5123": false,
		"127.0.0.1:5123:5123": false, "127.0.0.1:05123": false, "user@127.0.0.1:5123": false,
	} {
		if got := HostAllowed(host, base); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
	if !HostAllowed("[::1]:5123", "http://[::1]:5123") {
		t.Error("the bound IPv6 loopback address was refused")
	}
	for _, base := range []string{"", "http://", "127.0.0.1:5123", "http://127.0.0.1", "http://example.com:5123"} {
		for _, host := range []string{"127.0.0.1:5123", "example.com:5123", "127.0.0.1", ""} {
			if HostAllowed(host, base) {
				t.Errorf("%q accepted for base %q", host, base)
			}
		}
	}
}

func TestCleanProfile(t *testing.T) {
	for _, c := range []struct{ name, email, wantName, wantEmail, field, problem string }{
		{"  Ada   Lovelace ", "", "Ada Lovelace", "", "", ""},
		{"Ada", " Ada@Example.COM ", "Ada", "ada@example.com", "", ""},
		{"", "", "", "", "name", "required"},
		{" \t ", "a@b.c", "", "", "name", "required"},
		{strings.Repeat("n", NameMax), "", strings.Repeat("n", NameMax), "", "", ""},
		{strings.Repeat("n", NameMax+1), "", "", "", "name", "long"},
		{"A\x00da", "", "", "", "name", "invalid"},
		{"A‮da", "", "A‮da", "", "", ""}, // a format character is the person's own business; it is escaped where shown
		{"Ada", "not-an-address", "", "", "email", "invalid"},
		{"Ada", "Ada <ada@example.com>", "", "", "email", "invalid"},
		{"Ada", "a@b.c, d@e.f", "", "", "email", "invalid"},
		{"Ada", "a@", "", "", "email", "invalid"},
		{"Ada", strings.Repeat("a", EmailMax) + "@example.com", "", "", "email", "long"},
	} {
		n, e, f, p := CleanProfile(c.name, c.email)
		if n != c.wantName || e != c.wantEmail || f != c.field || p != c.problem {
			t.Errorf("CleanProfile(%q, %q) = %q %q %q %q", c.name, c.email, n, e, f, p)
		}
	}
	a, b := NewSubject(), NewSubject()
	if a == b || len(a) != len("local-")+32 {
		t.Errorf("subjects %q %q", a, b)
	}
}

func TestTheKeyIsPrivateStableAndRotatedOnANewVersion(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	k1, err := LoadKey(dir, "2.0.0", now)
	if err != nil || !k1.Rotated {
		t.Fatalf("first load: %v rotated=%v", err, k1 != nil && k1.Rotated)
	}
	path := filepath.Join(dir, KeyDir, KeyFile)
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("the key file's mode is %v, want 0600", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Dir(path)); st.Mode().Perm() != 0o700 {
			t.Errorf("the key folder's mode is %v, want 0700", st.Mode().Perm())
		}
	}
	k2, err := LoadKey(dir, "2.0.0", now)
	if err != nil || k2.Rotated || k2.ID != k1.ID || !k2.Private.Equal(k1.Private) {
		t.Fatalf("second load changed the key: %v", err)
	}
	k3, _ := LoadKey(dir, "2.0.1", now)
	if !k3.Rotated || k3.ID == k1.ID {
		t.Error("a new version kept the old key")
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o644)
		if k, _ := LoadKey(dir, "2.0.1", now); k.ID != k3.ID {
			t.Error("a readable key file was replaced, not closed")
		}
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("a key file readable by others was left at %v", st.Mode().Perm())
		}
	}
	// A file that is not a key, and one far too large, are replaced, never fatal.
	for name, content := range map[string][]byte{"garbage": []byte("{not json"), "another kind of key": []byte(`{"format":1,"app_version":"2.0.1","pkcs8":"AAAA"}`),
		"huge": []byte(strings.Repeat("x", keyFileMax+1)), "empty": nil} {
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		k, err := LoadKey(dir, "2.0.1", now)
		if err != nil || !k.Rotated {
			t.Errorf("%s: %v rotated=%v", name, err, k != nil && k.Rotated)
		}
	}
	left, _ := os.ReadDir(filepath.Join(dir, KeyDir))
	if len(left) != 1 {
		t.Errorf("%d files in the key folder, want the key alone", len(left))
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"app_version":"2.0.1"`) {
		t.Errorf("the key file does not record the version: %s", raw[:40])
	}
}

// The shell checks this answer in Rust; both sides hold the same vector so that they cannot drift apart. Changing
// the construction means changing desktop/src-tauri/src/launch.rs, its test, and this one.
func TestTheHealthzProofMatchesTheShellsVector(t *testing.T) {
	const (
		token = "5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e"
		nonce = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
		want  = "68976f01ca10fc56efcfd44364fc8a604e14f46723d71f4d717517802c04c899"
	)
	l := NewLaunch(token)
	if got, ok := l.Proof(nonce); !ok || got != want {
		t.Fatalf("Proof = %q, %v; want %q", got, ok, want)
	}
	// The same challenge gets the same answer; another challenge another one; another token another one.
	if again, _ := l.Proof(nonce); again != want {
		t.Error("the same challenge got another answer")
	}
	if other, _ := l.Proof(strings.Repeat("ab", 32)); other == want {
		t.Error("two challenges got the same answer")
	}
	if other, _ := NewLaunch(testToken).Proof(nonce); other == want {
		t.Error("another token gave the same answer")
	}
	// What is not a challenge is not answered at all.
	for name, bad := range map[string]string{"empty": "", "short": nonce[:62], "long": nonce + "0", "upper case": strings.ToUpper(nonce),
		"not hex": strings.Repeat("g", 64), "space": nonce[:63] + " "} {
		if p, ok := l.Proof(bad); ok || p != "" {
			t.Errorf("%s: answered %q", name, p)
		}
	}
	// The answer is not the token, the cookie, or the form key: it reveals none of them.
	cookie, _ := l.Redeem(token)
	for name, secret := range map[string]string{"the token": token, "the cookie": cookie, "the form key": l.FormToken("x")} {
		if strings.Contains(want, secret) || strings.Contains(secret, want) {
			t.Errorf("the proof contains %s", name)
		}
	}
}

package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaultsAreLoopback(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("default listen = %s; the binary must default to loopback", c.Listen)
	}
	if c.BaseURLString() != "http://localhost:8080" || c.Secure() {
		t.Errorf("base = %s secure=%v", c.BaseURLString(), c.Secure())
	}
	if c.TrustProxy {
		t.Error("TRUST_PROXY must default to false")
	}
}

func TestEphemeralPortDerivesBaseURL(t *testing.T) {
	c, _ := Load(env(map[string]string{"SCRIOBH_LISTEN": "127.0.0.1:0"}))
	c.SetBoundAddr(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 54321})
	if got := c.BaseURLString(); got != "http://127.0.0.1:54321" {
		t.Errorf("base = %s", got)
	}
	if got := c.OIDCCallbackURL(); got != "http://127.0.0.1:54321/auth/oidc/callback" {
		t.Errorf("callback = %s", got)
	}
	// An explicit base URL is never overwritten.
	c, _ = Load(env(map[string]string{"SCRIOBH_BASE_URL": "https://write.example.com/"}))
	c.SetBoundAddr(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9})
	if c.BaseURLString() != "https://write.example.com" || !c.Secure() {
		t.Errorf("explicit base lost: %s", c.BaseURLString())
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string // substring of the error; "" = valid
	}{
		{"stub on loopback", map[string]string{"SCRIOBH_OIDC_STUB": "1"}, ""},
		{"stub on public url", map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_BASE_URL": "https://write.example.com"}, "loopback"},
		{"stub on public listen without base", map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_LISTEN": ":8080"}, "loopback"},
		{"stub with loopback base but public listen", map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_BASE_URL": "http://localhost:8080", "SCRIOBH_LISTEN": "0.0.0.0:8080"}, "reachable by anyone"},
		{"stub public listen, dev compose opt-in", map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_BASE_URL": "http://localhost:8080", "SCRIOBH_LISTEN": ":8080", "SCRIOBH_DEV_ALLOW_ANY_LISTEN": "1"}, ""},
		{"no idp", map[string]string{}, "SCRIOBH_OIDC_ISSUER"},
		{"real idp", map[string]string{"SCRIOBH_OIDC_ISSUER": "https://auth.example.com", "SCRIOBH_OIDC_CLIENT_ID": "a", "SCRIOBH_OIDC_CLIENT_SECRET": "b"}, ""},
		{"bad base", map[string]string{"SCRIOBH_OIDC_STUB": "1", "SCRIOBH_BASE_URL": "not a url"}, "SCRIOBH_BASE_URL"},
	}
	for _, tc := range cases {
		c, err := Load(env(tc.env))
		if err != nil {
			t.Fatal(err)
		}
		err = c.Validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: unexpected %v", tc.name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestEmptyAdminGroupMeansClaimMode(t *testing.T) {
	c, _ := Load(env(map[string]string{"SCRIOBH_ADMIN_GROUP": ""}))
	if c.AdminGroup != "" {
		t.Errorf("an explicitly empty admin group must stay empty, got %q", c.AdminGroup)
	}
	c, _ = Load(env(nil))
	if c.AdminGroup != "scriobh-admins" {
		t.Errorf("default admin group = %q", c.AdminGroup)
	}
}

func TestSecretKeyFile(t *testing.T) {
	dir := t.TempDir()
	c, _ := Load(env(map[string]string{"SCRIOBH_DATA_DIR": dir}))
	k1, err := c.EnsureSecretKey()
	if err != nil || len(k1) < 32 {
		t.Fatalf("key %q err %v", k1, err)
	}
	c2, _ := Load(env(map[string]string{"SCRIOBH_DATA_DIR": dir}))
	k2, _ := c2.EnsureSecretKey()
	if string(k1) != string(k2) {
		t.Error("the key must survive a restart so sessions stay valid")
	}
}

func TestMirrorAndBackupSettings(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.MirrorDisabled || c.MirrorDebounceMs != 2000 || c.BackupIntervalHours != 24 || c.BackupKeep != 7 {
		t.Errorf("defaults: mirror off=%v debounce=%d interval=%d keep=%d", c.MirrorDisabled, c.MirrorDebounceMs, c.BackupIntervalHours, c.BackupKeep)
	}
	c, err = Load(env(map[string]string{"SCRIOBH_MIRROR": "off", "SCRIOBH_MIRROR_DEBOUNCE_MS": "50", "SCRIOBH_BACKUP_INTERVAL_HOURS": "0", "SCRIOBH_BACKUP_KEEP": "30"}))
	if err != nil || !c.MirrorDisabled || c.MirrorDebounceMs != 50 || c.BackupIntervalHours != 0 || c.BackupKeep != 30 {
		t.Errorf("overrides: %+v %v", c, err)
	}
	for _, bad := range []map[string]string{{"SCRIOBH_MIRROR": "maybe"}, {"SCRIOBH_BACKUP_KEEP": "0"}, {"SCRIOBH_BACKUP_KEEP": "x"}, {"SCRIOBH_BACKUP_INTERVAL_HOURS": "-1"}} {
		if _, err := Load(env(bad)); err == nil {
			t.Errorf("%v should be refused", bad)
		}
	}
}

func TestIssuerIsNotTrimmed(t *testing.T) {
	c, _ := Load(env(map[string]string{"SCRIOBH_OIDC_ISSUER": "https://auth.example.com/application/o/scriobh/"}))
	if c.OIDCIssuer != "https://auth.example.com/application/o/scriobh/" {
		t.Errorf("issuer must be kept exactly as configured, got %q", c.OIDCIssuer)
	}
}

func TestSecretKeyRules(t *testing.T) {
	if _, err := Load(env(map[string]string{"SCRIOBH_SECRET_KEY": "short"})); err == nil {
		t.Error("a short secret key must be rejected")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(env(map[string]string{"SCRIOBH_DATA_DIR": dir}))
	if _, err := c.EnsureSecretKey(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Errorf("a corrupt key file must fail loudly, got %v", err)
	}
}

func TestBadNumbers(t *testing.T) {
	if _, err := Load(env(map[string]string{"SCRIOBH_SESSION_HOURS": "soon"})); err == nil {
		t.Error("non-numeric hours must be rejected")
	}
	if _, err := Load(env(map[string]string{"SCRIOBH_SESSION_HOURS": "0"})); err == nil {
		t.Error("zero session hours must be rejected")
	}
}

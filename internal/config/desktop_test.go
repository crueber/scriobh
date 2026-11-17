package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const goodToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func desktopEnv(change map[string]string) func(string) (string, bool) {
	m := map[string]string{"SCRIOBH_DESKTOP": "1", "SCRIOBH_LAUNCH_TOKEN": goodToken, "SCRIOBH_DATA_DIR": "/tmp/scriobh-test-data"}
	for k, v := range change {
		if v == "\x00" {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDesktopModeDefaultsAndValidation(t *testing.T) {
	c, err := Load(desktopEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a good desktop environment was refused: %v", err)
	}
	if !c.Desktop || !c.DesktopEmbedded() || c.Listen != DesktopListen || c.AdminGroup != "" || c.BaseURL != "" {
		t.Errorf("defaults: desktop=%v listen=%q admin group=%q", c.Desktop, c.Listen, c.AdminGroup)
	}
	// An explicit listen address is kept; an explicit administrators' group is refused (the local profile is in no
	// group), an explicitly empty one is not.
	c, _ = Load(desktopEnv(map[string]string{"SCRIOBH_LISTEN": "127.0.0.1:4455", "SCRIOBH_ADMIN_GROUP": ""}))
	if c.Listen != "127.0.0.1:4455" || c.AdminGroup != "" || c.Validate() != nil {
		t.Errorf("explicit settings: %q %q %v", c.Listen, c.AdminGroup, c.Validate())
	}
	c, _ = Load(desktopEnv(map[string]string{"SCRIOBH_ADMIN_GROUP": "ops"}))
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "SCRIOBH_ADMIN_GROUP") {
		t.Errorf("an explicit administrators' group in desktop mode: %v", err)
	}

	refused := map[string]struct {
		env  map[string]string
		want string
	}{
		"no token":                       {map[string]string{"SCRIOBH_LAUNCH_TOKEN": "\x00"}, "SCRIOBH_LAUNCH_TOKEN"},
		"an empty token":                 {map[string]string{"SCRIOBH_LAUNCH_TOKEN": ""}, "SCRIOBH_LAUNCH_TOKEN"},
		"a short token":                  {map[string]string{"SCRIOBH_LAUNCH_TOKEN": goodToken[:31]}, "SCRIOBH_LAUNCH_TOKEN"},
		"a token that is not hex":        {map[string]string{"SCRIOBH_LAUNCH_TOKEN": strings.Repeat("z", 64)}, "SCRIOBH_LAUNCH_TOKEN"},
		"a huge token":                   {map[string]string{"SCRIOBH_LAUNCH_TOKEN": strings.Repeat("a", 4096)}, "SCRIOBH_LAUNCH_TOKEN"},
		"every interface":                {map[string]string{"SCRIOBH_LISTEN": "0.0.0.0:0"}, "SCRIOBH_LISTEN"},
		"no host":                        {map[string]string{"SCRIOBH_LISTEN": ":8080"}, "SCRIOBH_LISTEN"},
		"a LAN address":                  {map[string]string{"SCRIOBH_LISTEN": "192.168.1.10:8080"}, "SCRIOBH_LISTEN"},
		"a name":                         {map[string]string{"SCRIOBH_LISTEN": "localhost:8080"}, "SCRIOBH_LISTEN"},
		"IPv6 any":                       {map[string]string{"SCRIOBH_LISTEN": "[::]:0"}, "SCRIOBH_LISTEN"},
		"a base URL":                     {map[string]string{"SCRIOBH_BASE_URL": "https://writing.example.org"}, "SCRIOBH_BASE_URL"},
		"a loopback base URL":            {map[string]string{"SCRIOBH_BASE_URL": "http://127.0.0.1:8080"}, "SCRIOBH_BASE_URL"},
		"the development issuer":         {map[string]string{"SCRIOBH_OIDC_STUB": "1"}, "SCRIOBH_OIDC_STUB"},
		"an issuer from the environment": {map[string]string{"SCRIOBH_OIDC_ISSUER": "https://idp.example.org", "SCRIOBH_OIDC_CLIENT_ID": "c", "SCRIOBH_OIDC_CLIENT_SECRET": "s"}, "SCRIOBH_OIDC_ISSUER"},
		"a trusted proxy":                {map[string]string{"SCRIOBH_TRUST_PROXY": "1"}, "SCRIOBH_TRUST_PROXY"},
	}
	for name, c := range refused {
		cfg, err := Load(desktopEnv(c.env))
		if err != nil {
			t.Errorf("%s: Load: %v", name, err)
			continue
		}
		verr := cfg.Validate()
		if verr == nil || !strings.Contains(verr.Error(), c.want) {
			t.Errorf("%s: Validate = %v, want a problem naming %s", name, verr, c.want)
			continue
		}
		if tok := c.env["SCRIOBH_LAUNCH_TOKEN"]; len(tok) >= 8 && strings.Contains(verr.Error(), tok) {
			t.Errorf("%s: the problem repeats the token", name)
		}
	}
	if ok, err := Load(desktopEnv(map[string]string{"SCRIOBH_LISTEN": "[::1]:0"})); err != nil || ok.Validate() != nil {
		t.Errorf("the IPv6 loopback address was refused: %v", err)
	}
	if _, err := Load(desktopEnv(map[string]string{"SCRIOBH_DESKTOP": "maybe"})); err == nil {
		t.Error("SCRIOBH_DESKTOP=maybe was read as a value")
	}
}

// TestWithoutDesktopModeNothingChanges: the same environment with and without the desktop variables that are
// ignored outside desktop mode validates the same, and the desktop defaults are not applied.
func TestWithoutDesktopModeNothingChanges(t *testing.T) {
	base := map[string]string{"SCRIOBH_OIDC_ISSUER": "https://idp.example.org", "SCRIOBH_OIDC_CLIENT_ID": "c", "SCRIOBH_OIDC_CLIENT_SECRET": "s"}
	lookup := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	plain, err := Load(lookup(base))
	if err != nil || plain.Validate() != nil {
		t.Fatalf("plain: %v %v", err, plain.Validate())
	}
	for _, off := range []string{"0", "false", "off", ""} {
		m := map[string]string{"SCRIOBH_DESKTOP": off, "SCRIOBH_LAUNCH_TOKEN": "not-even-a-token"}
		for k, v := range base {
			m[k] = v
		}
		c, err := Load(lookup(m))
		if err != nil || c.Validate() != nil {
			t.Fatalf("SCRIOBH_DESKTOP=%q: %v %v", off, err, c.Validate())
		}
		if c.Desktop || c.DesktopEmbedded() || c.Listen != plain.Listen || c.DataDir != plain.DataDir || c.AdminGroup != plain.AdminGroup || c.OIDCIssuer != plain.OIDCIssuer {
			t.Errorf("SCRIOBH_DESKTOP=%q changed the configuration", off)
		}
		if c.ApplyDesktopExternal(DesktopExternalIdP{Issuer: "https://x.example", ClientID: "c"}) == nil {
			t.Error("an external provider from the settings was applied outside desktop mode")
		}
	}
	// With no identity provider at all the problems are the ones they always were.
	none, _ := Load(lookup(map[string]string{}))
	if err := none.Validate(); err == nil || !strings.Contains(err.Error(), "SCRIOBH_OIDC_ISSUER is required") || strings.Contains(err.Error(), "DESKTOP") {
		t.Errorf("validation without desktop mode: %v", err)
	}
	if _, retired := RetiredEnv["SCRIOBH_DESKTOP"]; retired {
		t.Error("SCRIOBH_DESKTOP is still listed as not built")
	}
	if !plain.envRead["SCRIOBH_DESKTOP"] || !plain.envRead["SCRIOBH_LAUNCH_TOKEN"] {
		t.Error("Load does not read the desktop variables, so they would be warned about as unknown")
	}
}

func TestDesktopDataDir(t *testing.T) {
	env := func(m map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
	}
	for _, c := range []struct {
		goos string
		env  map[string]string
		want string
	}{
		{"darwin", map[string]string{"HOME": "/Users/ada"}, filepath.Join("/Users/ada", "Library", "Application Support", "Scriobh")},
		{"linux", map[string]string{"HOME": "/home/ada"}, filepath.Join("/home/ada", ".local", "share", "scriobh")},
		{"linux", map[string]string{"HOME": "/home/ada", "XDG_DATA_HOME": "/data/xdg"}, filepath.Join("/data/xdg", "scriobh")},
		{"linux", map[string]string{"HOME": "/home/ada", "XDG_DATA_HOME": "relative/dir"}, filepath.Join("/home/ada", ".local", "share", "scriobh")},
		{"freebsd", map[string]string{"HOME": "/home/ada"}, filepath.Join("/home/ada", ".local", "share", "scriobh")},
		{"windows", map[string]string{"APPDATA": `C:\Users\ada\AppData\Roaming`}, filepath.Join(`C:\Users\ada\AppData\Roaming`, "Scriobh")},
	} {
		got, err := DesktopDataDir(c.goos, env(c.env))
		if err != nil || got != c.want {
			t.Errorf("%s %v: %q %v, want %q", c.goos, c.env, got, err, c.want)
		}
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		if got, err := DesktopDataDir(goos, env(nil)); err == nil {
			t.Errorf("%s with no home: %q, want an error that names SCRIOBH_DATA_DIR", goos, got)
		} else if !strings.Contains(err.Error(), "SCRIOBH_DATA_DIR") {
			t.Errorf("%s: %v", goos, err)
		}
	}
	// Load uses it only when the directory is not given.
	c, err := Load(desktopEnv(map[string]string{"SCRIOBH_DATA_DIR": "\x00", "HOME": "/home/ada", "APPDATA": `C:\A`}))
	if err != nil || (!strings.Contains(c.DataDir, "criobh") || c.DataDir == "data") {
		t.Errorf("the default data directory in desktop mode: %q %v", c.DataDir, err)
	}
	if _, err := Load(desktopEnv(map[string]string{"SCRIOBH_DATA_DIR": "\x00"})); err == nil {
		t.Error("desktop mode with no data directory and no home started")
	}
}

func TestApplyDesktopExternal(t *testing.T) {
	c, _ := Load(desktopEnv(nil))
	if err := c.ApplyDesktopExternal(DesktopExternalIdP{Issuer: "", ClientID: "c"}); err == nil {
		t.Error("a provider with no issuer was applied")
	}
	if err := c.ApplyDesktopExternal(DesktopExternalIdP{Issuer: "https://idp.example.org", ClientID: "c", ClientSecret: "s", AdminGroup: "admins"}); err != nil {
		t.Fatal(err)
	}
	if c.DesktopEmbedded() || !c.DesktopExternal || c.OIDCIssuer != "https://idp.example.org" || c.OIDCClientSecret != "s" || c.AdminGroup != "admins" || c.WriterGroup != "scriobh-writers" {
		t.Errorf("after applying: %+v", c.OIDCIssuer)
	}
}

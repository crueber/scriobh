package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSiteSettings(t *testing.T) {
	data := t.TempDir()
	cases := []struct {
		name string
		env  map[string]string
		root string
		off  bool
		bad  string
	}{
		{"defaults: build on, publish off", map[string]string{"SCRIOBH_DATA_DIR": data}, "", false, ""},
		{"publish into the data directory", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_PUBLISH": "1"}, filepath.Join(data, "sites"), false, ""},
		{"a folder of the administrator's", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_DIR": filepath.Join(t.TempDir(), "www")}, "", false, ""},
		{"off turns publishing off too", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE": "off", "SCRIOBH_SITE_PUBLISH": "1"}, "", true, ""},
		{"a relative folder", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_DIR": "www"}, "", false, "not an absolute path"},
		{"the data directory itself", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_DIR": data}, "", false, "outside the data directory"},
		{"a folder inside the data directory", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_DIR": filepath.Join(data, "files", "www")}, "", false, "outside the data directory"},
		{"a folder that holds the data directory", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_DIR": filepath.Dir(data)}, "", false, "outside the data directory"},
		{"the filesystem root", map[string]string{"SCRIOBH_DATA_DIR": data, "SCRIOBH_SITE_DIR": "/"}, "", false, "root of the file system"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.env["SCRIOBH_OIDC_STUB"] = "1"
			cfg, err := Load(env(c.env))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.Validate()
			if c.bad != "" {
				if err == nil || !strings.Contains(err.Error(), c.bad) {
					t.Fatalf("want %q, got %v", c.bad, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := c.root
			if want == "" && c.env["SCRIOBH_SITE_DIR"] != "" {
				want = c.env["SCRIOBH_SITE_DIR"]
			}
			if cfg.SiteRoot() != want || cfg.SiteDisabled != c.off {
				t.Errorf("root %q (want %q), disabled %v", cfg.SiteRoot(), want, cfg.SiteDisabled)
			}
		})
	}
	if _, err := Load(env(map[string]string{"SCRIOBH_SITE": "maybe"})); err == nil {
		t.Error("SCRIOBH_SITE=maybe was accepted")
	}
}

package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A mistyped yes/no setting used to be silently off. Every spelling of every boolean setting is checked, the same
// way for the three families (the flags, the on/off switches, the AI and integration settings).
func TestMistypedBooleansFailTheLoad(t *testing.T) {
	bools := []string{
		"SCRIOBH_TRUST_PROXY", "SCRIOBH_OIDC_STUB", "SCRIOBH_DEV_ALLOW_ANY_LISTEN", "SCRIOBH_GIT_ALLOW_PRIVATE", "SCRIOBH_SITE_PUBLISH",
		"SCRIOBH_MIRROR", "SCRIOBH_SITE", "SCRIOBH_AI", "SCRIOBH_AI_USER_PROVIDERS",
		"SCRIOBH_MCP", "SCRIOBH_MCP_ADMIN_TOOLS", "SCRIOBH_MCP_PERMANENT_TOOLS", "SCRIOBH_WEBHOOKS", "SCRIOBH_WEBHOOK_ALLOW_PRIVATE",
	}
	for _, name := range bools {
		for _, bad := range []string{"ture", "enabled", "2", "of", "tru e"} {
			_, err := Load(env(map[string]string{name: bad}))
			if err == nil {
				t.Errorf("%s=%q loaded", name, bad)
				continue
			}
			if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), BoolValues) || !strings.Contains(err.Error(), `"`+bad+`"`) {
				t.Errorf("%s=%q: the message %q must name the variable, the value and the accepted spellings", name, bad, err)
			}
		}
		for _, ok := range []string{"", "1", "0", "true", "FALSE", "Yes", "no", "on", "Off", " on "} {
			if _, err := Load(env(map[string]string{name: ok})); err != nil {
				t.Errorf("%s=%q: %v", name, ok, err)
			}
		}
	}
	// Both problems at once, not one per attempt.
	_, err := Load(env(map[string]string{"SCRIOBH_TRUST_PROXY": "ture", "SCRIOBH_GIT_ALLOW_PRIVATE": "yep"}))
	if err == nil || !strings.Contains(err.Error(), "SCRIOBH_TRUST_PROXY") || !strings.Contains(err.Error(), "SCRIOBH_GIT_ALLOW_PRIVATE") {
		t.Errorf("two mistyped booleans: %v", err)
	}
	// What each means.
	c, err := Load(env(map[string]string{"SCRIOBH_TRUST_PROXY": "Yes", "SCRIOBH_MIRROR": "no", "SCRIOBH_SITE": "off", "SCRIOBH_AI": "0", "SCRIOBH_MCP": "off",
		"SCRIOBH_WEBHOOKS": "false", "SCRIOBH_MCP_ADMIN_TOOLS": "on", "SCRIOBH_AI_USER_PROVIDERS": "off"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.TrustProxy || !c.MirrorDisabled || !c.SiteDisabled || !c.AI.Off || !c.MCP.Off || !c.Webhook.Off || !c.MCP.AdminTools || c.AI.UserProviders {
		t.Errorf("parsed wrongly: %+v", c)
	}
	d, err := Load(env(nil))
	if err != nil || d.TrustProxy || d.MirrorDisabled || d.SiteDisabled || d.AI.Off || d.MCP.Off || d.Webhook.Off || !d.AI.UserProviders {
		t.Errorf("defaults changed: %v %+v", err, d)
	}
}

// The importer reads its own concurrency on first use and ignores what it cannot read; the load says so instead.
func TestMistypedImportConcurrencyFailsTheLoad(t *testing.T) {
	for _, bad := range []string{"two", "0", "-1", "1.5"} {
		if _, err := Load(env(map[string]string{"SCRIOBH_IMPORT_CONCURRENCY": bad})); err == nil || !strings.Contains(err.Error(), "SCRIOBH_IMPORT_CONCURRENCY") {
			t.Errorf("SCRIOBH_IMPORT_CONCURRENCY=%q: %v", bad, err)
		}
	}
	if _, err := Load(env(map[string]string{"SCRIOBH_IMPORT_CONCURRENCY": "3"})); err != nil {
		t.Error(err)
	}
	// Numbers elsewhere already named the variable; they keep doing so.
	if _, err := Load(env(map[string]string{"SCRIOBH_RATE_LIMIT": "fast"})); err == nil || !strings.Contains(err.Error(), "SCRIOBH_RATE_LIMIT") {
		t.Errorf("SCRIOBH_RATE_LIMIT=fast: %v", err)
	}
}

func TestUnknownEnvNamesTheNearestSetting(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	environ := []string{
		"PATH=/bin", "HOME=/root", "SCRIOBH_TRUST_PROXIES=1", "SCRIOBH_SESION_HOURS=2", "SCRIOBH_OIDC_ISUER=x",
		"SCRIOBH_TRUST_PROXY=1",                  // read: not a warning
		"SCRIOBH_URL=http://x", "SCRIOBH_KEEP=1", // the CLI's and the tests': not a warning
		"SCRIOBH_GIT_BINARY=/usr/bin/git", "SCRIOBH_AI_ALLOW_PRIVATE=1",
		"SCRIOBH_COMPLETELY_UNRELATED_THING_HERE=secret-value-do-not-print", "scriobh_lower=1",
	}
	got := map[string]string{}
	var order []string
	for _, w := range c.UnknownEnv(environ) {
		got[w.Name] = w.Hint
		order = append(order, w.Name)
		if strings.Contains(w.Message(), "secret-value-do-not-print") || strings.Contains(w.Message(), "=") {
			t.Errorf("a value reached the warning: %q", w.Message())
		}
	}
	if !sort.StringsAreSorted(order) {
		t.Errorf("not in name order: %v", order)
	}
	want := map[string]string{
		"SCRIOBH_TRUST_PROXIES": "did you mean SCRIOBH_TRUST_PROXY?",
		"SCRIOBH_SESION_HOURS":  "did you mean SCRIOBH_SESSION_HOURS?",
		"SCRIOBH_OIDC_ISUER":    "did you mean SCRIOBH_OIDC_ISSUER?",
	}
	for n, h := range want {
		if got[n] != h {
			t.Errorf("%s: hint %q, want %q", n, got[n], h)
		}
	}
	if !strings.Contains(got["SCRIOBH_GIT_BINARY"], "not built") || !strings.Contains(got["SCRIOBH_AI_ALLOW_PRIVATE"], "removed") {
		t.Errorf("retired names should say so: %q %q", got["SCRIOBH_GIT_BINARY"], got["SCRIOBH_AI_ALLOW_PRIVATE"])
	}
	if h, ok := got["SCRIOBH_COMPLETELY_UNRELATED_THING_HERE"]; !ok || h != "" {
		t.Errorf("an unrelated name is warned about without a guess: %q %v", h, ok)
	}
	for _, n := range []string{"SCRIOBH_TRUST_PROXY", "SCRIOBH_URL", "SCRIOBH_KEEP", "PATH", "scriobh_lower"} {
		if _, ok := got[n]; ok {
			t.Errorf("%s was warned about", n)
		}
	}
	if len(got) != 6 {
		t.Errorf("warnings = %v", got)
	}
}

// Every SCRIOBH_ name written anywhere in the repository (Go, the compose files, the Dockerfile, scripts, and the
// operations guide) is a setting Load reads, a tooling name, or a retired one with its reason. Otherwise a
// variable the code or a compose file passes would be reported as a mistake, or a new setting would be
// documented without being read.
func TestEverySCRIOBHNameInTheRepositoryIsAccountedFor(t *testing.T) {
	c, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, k := range c.KnownEnv() {
		known[k] = true
	}
	if len(known) < 80 {
		t.Fatalf("Load read only %d names; has it stopped reading some when the value is unset?", len(known))
	}
	root := filepath.Join("..", "..")
	name := regexp.MustCompile(`SCRIOBH_[A-Z0-9_]*[A-Z0-9]`)
	skipDir := map[string]bool{"node_modules": true, ".git": true, "data": true, "archive": true}
	exts := map[string]bool{".go": true, ".yaml": true, ".yml": true, ".md": true, ".mjs": true, ".js": true, ".sh": true, ".py": true, ".json": true, ".html": true}
	found := map[string][]string{}
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		base := d.Name()
		if base == "env_test.go" && filepath.Base(filepath.Dir(path)) == "config" {
			return nil // this file spells mistakes on purpose
		}
		if !exts[filepath.Ext(base)] && base != "Dockerfile" && base != "Makefile" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// A group of names written with a trailing underscore ("SCRIOBH_SSE_*") is a prefix, not a name: the pattern
		// above drops the underscore, so look at the character after each match.
		text := string(b)
		for _, loc := range name.FindAllStringIndex(text, -1) {
			n := text[loc[0]:loc[1]]
			if loc[1] < len(text) && text[loc[1]] == '_' {
				continue
			}
			found[n] = append(found[n], path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var strays []string
	for n, files := range found {
		if known[n] || ToolingEnv[n] {
			continue
		}
		if _, retired := RetiredEnv[n]; retired {
			continue
		}
		strays = append(strays, n+" ("+files[0]+")")
	}
	sort.Strings(strays)
	if len(strays) > 0 {
		t.Errorf("SCRIOBH_ names that Load does not read and that are not in ToolingEnv or RetiredEnv (a typo in the code or the docs, or a new setting to read in Load):\n  %s", strings.Join(strays, "\n  "))
	}
	// And the other way: nothing in the lists is spelled in no file at all, so they do not collect dead names.
	for n := range ToolingEnv {
		if _, ok := found[n]; !ok {
			t.Errorf("ToolingEnv names %s, which appears nowhere in the repository", n)
		}
	}
	for n := range RetiredEnv {
		if known[n] {
			t.Errorf("%s is read by Load and also listed as retired", n)
		}
	}
}

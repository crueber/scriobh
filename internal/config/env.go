package config

import (
	"fmt"
	"sort"
	"strings"
)

// BoolValues are the spellings a yes/no setting accepts, in the order the error message lists them.
const BoolValues = "1, true, yes, on, 0, false, no, off"

// ParseBool reads a yes/no setting. An empty value is the default; the spellings are case-insensitive. Anything
// else is an error that names the variable and what it accepts, so a mistyped SCRIOBH_TRUST_PROXY is not
// silently off.
func ParseBool(name, value string, def bool) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return def, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return def, fmt.Errorf("%s: %q is not a yes or no value; use one of %s", name, value, BoolValues)
}

// ToolingEnv names the SCRIOBH_ variables that exist in this project but are never read by the server: the CLI's
// own settings (it runs on the same machine and often in the same shell), the build and the compose file, and the
// switches of tests and scripts. They are not mistakes, so they are not warned about. A guard test checks that every
// SCRIOBH_ name written anywhere in the repository is in one of the lists here, is read by Load, or is in
// RetiredEnv.
var ToolingEnv = map[string]bool{
	// The CLI (internal/cli): where the server is, the key, and where its login and cache live.
	"SCRIOBH_URL": true, "SCRIOBH_API_KEY": true, "SCRIOBH_TOKEN": true, "SCRIOBH_CONFIG_DIR": true, "SCRIOBH_CACHE_DIR": true,
	// Compose and the image build: the label the binary reports.
	"SCRIOBH_VERSION": true,
	// A development build only (internal/server/dev_clock.go, not in a production build).
	"SCRIOBH_DEV_CLOCK_OFFSET_SECONDS": true,
	// A development build only (internal/server/dev_trust.go): trust a code host's self-signed certificate in the browser tests.
	"SCRIOBH_DEV_TRUST_PEM": true,
	// Tests, benchmarks and scripts.
	"SCRIOBH_KEEP": true, "SCRIOBH_TIMING": true, "SCRIOBH_MEASURE_PANDOC": true, "SCRIOBH_PANDOC_HELPER_TOKEN": true, "SCRIOBH_GOLDEN": true, "SCRIOBH_UPDATE_GOLDEN": true, "SCRIOBH_LIBREOFFICE": true, "SCRIOBH_PDFTOOLS": true,
	"SCRIOBH_SEED_DIR": true, "SCRIOBH_SEED_USER": true, "SCRIOBH_SEED_PROJECT": true, "SCRIOBH_SEED_DOC": true, "SCRIOBH_SEED_ROWS": true,
	"SCRIOBH_SITE_SAMPLE_DIR": true, "SCRIOBH_SITE_SAMPLE_LANG": true, "SCRIOBH_REHEARSAL_BIN": true,
	"SCRIOBH_UPGRADE_FIXTURE": true, "SCRIOBH_UPGRADE_OLD_BIN": true, "SCRIOBH_UPGRADE_TAG": true, "SCRIOBH_CHROMIUM": true,
	// The desktop shell's tests against the real server (desktop/scripts/test.sh sets it; the Rust tests read it).
	"SCRIOBH_REAL_SERVER": true,
	// The Windows cross-build script (desktop/scripts/build-windows-cross.sh): the directory that keeps the Windows SDK and
	// the Rust installation between runs.
	"SCRIOBH_WIN_CACHE": true,
}

// RetiredEnv names settings that earlier plans or versions mentioned and this version does not read, with the
// sentence that says why. Setting one is warned about with that sentence instead of a spelling hint.
var RetiredEnv = map[string]string{
	"SCRIOBH_AI_ALLOW_PRIVATE": "removed: an administrator declares a provider local, per provider",
	"SCRIOBH_AI_ALLOWED_HOSTS": "removed: an administrator declares a provider local, per provider",
	"SCRIOBH_ALLOW_PRIVATE_AI": "removed: an administrator declares a provider local, per provider",
	"SCRIOBH_GIT_BINARY":       "planned, not built: Git sync uses a pure-Go library and never runs a git program",
	"SCRIOBH_EMBEDDED_OIDC":    "never built: SCRIOBH_DESKTOP=1 always starts the embedded issuer, and an administrator switches to an external provider in the settings",
}

// EnvWarning is an environment variable that begins SCRIOBH_ and that this version does not read.
type EnvWarning struct {
	Name string
	// Hint is what to do about it: the nearest setting this version reads ("did you mean SCRIOBH_TRUST_PROXY?"),
	// or why a retired one is gone. Empty when nothing is close.
	Hint string
}

// Message is the warning as one line, with no value (a value may be a secret).
func (w EnvWarning) Message() string {
	m := w.Name + " is not a setting this version reads and is ignored"
	if w.Hint != "" {
		m += " (" + w.Hint + ")"
	}
	return m
}

// KnownEnv lists every SCRIOBH_ name Load read, sorted. It is empty for a Config that did not come from Load.
func (c *Config) KnownEnv() []string {
	out := make([]string, 0, len(c.envRead))
	for k := range c.envRead {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UnknownEnv finds the SCRIOBH_ variables in environ ("NAME=value" entries, as os.Environ gives them) that this
// version does not read, in name order, each with the nearest known name as a hint. Names only: a value is never
// looked at, kept or returned. A name that Load reads, a tooling name and a retired name (which gets its reason)
// are all accounted for.
func (c *Config) UnknownEnv(environ []string) []EnvWarning {
	seen := map[string]bool{}
	var names []string
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !strings.HasPrefix(name, "SCRIOBH_") || seen[name] || c.envRead[name] || ToolingEnv[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	known := c.KnownEnv()
	out := make([]EnvWarning, 0, len(names))
	for _, n := range names {
		w := EnvWarning{Name: n}
		if why, ok := RetiredEnv[n]; ok {
			w.Hint = why
		} else if near := nearest(n, known); near != "" {
			w.Hint = "did you mean " + near + "?"
		}
		out = append(out, w)
	}
	return out
}

// nearest is the known name closest to name by edit distance, or "" when none is near enough to be a typo.
func nearest(name string, known []string) string {
	best, bestD := "", 1<<30
	for _, k := range known {
		if d := editDistance(name, k); d < bestD {
			best, bestD = k, d
		}
	}
	limit := len(name) / 4
	if limit < 2 {
		limit = 2
	}
	if limit > 5 {
		limit = 5
	}
	if bestD > limit {
		return ""
	}
	return best
}

// editDistance is the Levenshtein distance between two ASCII names.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

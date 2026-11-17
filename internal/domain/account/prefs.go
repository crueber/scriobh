package account

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// A pref is one allowed preference: its default and a check that returns the
// canonical stored form. Values are TEXT in user_prefs and strings on the
// wire, so a number is sent as "19", not 19.
type pref struct {
	def   string
	check func(v string) (string, bool)
}

func oneOf(values ...string) func(string) (string, bool) {
	return func(v string) (string, bool) {
		for _, x := range values {
			if v == x {
				return v, true
			}
		}
		return "", false
	}
}

func intRange(lo, hi int, extra ...string) func(string) (string, bool) {
	return func(v string) (string, bool) {
		for _, x := range extra {
			if v == x {
				return v, true
			}
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return "", false
		}
		return strconv.Itoa(n), true // canonical: "019" and "+19" are stored as "19"
	}
}

// lineHeights lists 1.4 to 1.9 in steps of 0.05 as strings: "1.4", "1.45", "1.5".
// Built with integers so no float rounding can let a near miss through.
var lineHeights = func() []string {
	var out []string
	for h := 140; h <= 190; h += 5 {
		s := fmt.Sprintf("%d.%02d", h/100, h%100)
		if s[len(s)-1] == '0' {
			s = s[:len(s)-1]
		}
		out = append(out, s)
	}
	return out
}()

// The named themes. The first of each list is the default and is drawn by app.css; the others are in
// themes.css. internal/server holds the labels and browser colours and a test keeps both lists in step.
var (
	LightThemes = []string{"paper", "sepia"}
	DarkThemes  = []string{"ink", "slate", "moss"}
)

// prefSpecs is the allowlist (F-005, F-287). The pages read these values as
// data-* attributes; there is no free-form key.
var prefSpecs = map[string]pref{
	"theme":              {"system", oneOf("system", "light", "dark")},
	"theme_light":        {LightThemes[0], oneOf(LightThemes...)}, // used when the page is light
	"theme_dark":         {DarkThemes[0], oneOf(DarkThemes...)},   // used when the page is dark
	"editor_font":        {"serif", oneOf("serif", "sans", "mono")},
	"editor_size":        {"19", intRange(16, 24)},
	"editor_measure":     {"66", intRange(52, 82, "full")},
	"editor_line_height": {"1.65", oneOf(lineHeights...)},
	"reduced_motion":     {"system", oneOf("system", "on", "off")},
	"autosave_ms":        {"700", intRange(300, 5000)},
	"show_word_count":    {"true", oneOf("true", "false")},
	"density":            {"comfortable", oneOf("comfortable", "compact", "spacious")},
	"typewriter":         {"false", oneOf("true", "false")},
	"focus_dim":          {"false", oneOf("true", "false")},
	"language":           {"en", language}, // the language of the interface (internal/i18n): never the manuscript's
	// F-244: whether the person shares which projects they have open, and sees who else does. Off for a new account, and symmetric:
	// off means neither sent nor seen (internal/domain/presence).
	"presence.share": {"false", oneOf("true", "false")},
	// F-508: the gentle in-app nudges of the writing studio ("450 words to go today"). On by default and quiet: at most
	// a few a day, none twice (internal/domain/studio). Off means the dashboard offers none.
	"studio.nudges": {"true", oneOf("true", "false")},
}

// language accepts a tag the message catalogue can show and stores the catalogue's own spelling of it.
func language(v string) (string, bool) {
	l, ok := i18n.Default.Resolve(v)
	if !ok {
		return "", false
	}
	return l.Tag, true
}

// PrefKeys lists the allowed preference keys, sorted.
func PrefKeys() []string {
	keys := make([]string, 0, len(prefSpecs))
	for k := range prefSpecs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Defaults returns every preference at its default.
func Defaults() map[string]string {
	out := make(map[string]string, len(prefSpecs))
	for k, s := range prefSpecs {
		out[k] = s.def
	}
	return out
}

// Prefs returns userID's preferences with defaults filled in. It is for page
// handlers that render data-* attributes without going through the op
// registry. A stored value that is no longer valid (the allowlist can only
// narrow over time) falls back to the default, and a read failure yields the
// defaults, so a page always renders.
func Prefs(ctx context.Context, d *db.DB, userID string) map[string]string {
	out := Defaults()
	rows, err := d.Read().QueryContext(ctx, `SELECT key, value FROM user_prefs WHERE user_id = ?`, userID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) != nil {
			continue
		}
		if spec, ok := prefSpecs[k]; ok {
			if canon, ok := spec.check(v); ok {
				out[k] = canon
			}
		}
	}
	return out
}

// PrefsOut is the full preference map.
type PrefsOut struct {
	Items map[string]string `json:"items" desc:"Every preference with defaults filled in. Values are strings, numbers included."`
}

// SetPrefsIn sets one or more preferences.
type SetPrefsIn struct {
	Items map[string]string `json:"items" desc:"Preference key to value. Every value is a string, numbers included (\"19\"). Keys: theme, theme_light, theme_dark, editor_font, editor_size, editor_measure, editor_line_height, reduced_motion, autosave_ms, show_word_count, density, typewriter, focus_dim, language (the interface's language tag, such as en), presence.share (\"true\" to show others which projects you have open and see who else does; \"false\" by default). One bad key or value fails the whole call."`
}

// Validate checks every key and value and normalises them. It names the first
// offender in alphabetical order, so the message is stable.
func (in *SetPrefsIn) Validate() error {
	if len(in.Items) == 0 {
		return ops.Invalid("items", "Send at least one preference.")
	}
	keys := make([]string, 0, len(in.Items))
	for k := range in.Items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	clean := make(map[string]string, len(in.Items))
	for _, k := range keys {
		spec, ok := prefSpecs[k]
		if !ok {
			return ops.Invalid(k, "Unknown preference.")
		}
		v, ok := spec.check(in.Items[k])
		if !ok {
			return ops.Invalid(k, "That is not a valid value for this preference.")
		}
		clean[k] = v
	}
	in.Items = clean
	return nil
}

func prefsGet(c *ops.Ctx, _ EmptyIn) (PrefsOut, error) {
	return PrefsOut{Items: Prefs(c, c.DB(), c.P.UserID)}, nil
}

// TestLocalesService, when registered as a true bool, lets a person choose the pseudo-locales (development and the
// stub issuer only). In production they are not a language anyone can pick.
const TestLocalesService = "account.test_locales"

func prefsSet(c *ops.Ctx, in SetPrefsIn) (PrefsOut, error) {
	// Whether a person is shown to the others is theirs to decide at a browser: a key (an agent, a script) must not be able
	// to start broadcasting which projects they have open.
	if _, ok := in.Items["presence.share"]; ok && (c.P.KeyID != "" || c.P.Via == "apikey") {
		return PrefsOut{}, ops.Forbidden("presence.share can only be changed from a browser session, not with an API key")
	}
	if v, ok := in.Items["language"]; ok {
		if l, found := i18n.Default.Resolve(v); found && l.Test {
			if allowed, _ := ops.Service[bool](c.Env, TestLocalesService); !allowed {
				return PrefsOut{}, ops.Invalid("language", "That is not a valid value for this preference.")
			}
		}
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		for k, v := range in.Items {
			if _, err := tx.Exec(`INSERT INTO user_prefs(user_id, key, value) VALUES (?,?,?)
				ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, c.P.UserID, k, v); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return PrefsOut{}, err
	}
	if v, ok := in.Items["presence.share"]; ok && v != "true" && !c.DryRun() {
		// Switching sharing off takes the person off every list at once, not when their entries run out.
		if f, ok := ops.Service[interface{ Forget(user string) }](c.Env, "presence"); ok {
			f.Forget(c.P.UserID)
		}
	}
	return PrefsOut{Items: Prefs(c, c.DB(), c.P.UserID)}, nil
}

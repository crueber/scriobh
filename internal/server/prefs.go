package server

import (
	"strings"

	"github.com/crueber/scriobh/internal/domain/account"
)

// theme is one named colour theme as the server knows it: its label for Settings and the colour the
// browser paints its own chrome with (the theme's page background; a test keeps it equal to --bg in
// the stylesheet). The first theme of each scheme is drawn by app.css, the rest by themes.css.
type theme struct{ ID, Label, Scheme, Color string }

var themeTable = []theme{
	{"paper", "Paper", "light", "#FBFAF7"},
	{"sepia", "Sepia", "light", "#F1E7D0"},
	{"ink", "Ink", "dark", "#131311"},
	{"slate", "Slate", "dark", "#1A1F26"},
	{"moss", "Moss", "dark", "#161E19"},
}

func themeByID(id string) theme {
	for _, t := range themeTable {
		if t.ID == id {
			return t
		}
	}
	return themeTable[0]
}

// themesOf lists the themes of one scheme, default first.
func themesOf(scheme string) []theme {
	var out []theme
	for _, t := range themeTable {
		if t.Scheme == scheme {
			out = append(out, t)
		}
	}
	return out
}

// themeChoice names the light and dark theme in use ("" for the defaults, Paper and Ink, which the
// stylesheet draws without an attribute).
func themeChoice(p map[string]string) (light, dark string) {
	if v := p["theme_light"]; v != "" && v != account.LightThemes[0] {
		light = v
	}
	if v := p["theme_dark"]; v != "" && v != account.DarkThemes[0] {
		dark = v
	}
	return
}

// themeMeta is one <meta name="theme-color">: the colour, and the colour scheme it is for when the page
// follows the system (empty when the person has forced a scheme).
type themeMeta struct{ Color, Media string }

func themeMetas(p map[string]string) []themeMeta {
	l, d := themeByID(p["theme_light"]), themeByID(p["theme_dark"])
	switch p["theme"] {
	case "light":
		return []themeMeta{{Color: l.Color}}
	case "dark":
		return []themeMeta{{Color: d.Color}}
	}
	return []themeMeta{{l.Color, "light"}, {d.Color, "dark"}}
}

// htmlAttrs maps the stored preferences to the data attributes the stylesheet
// reads. "system" values render nothing, so the media queries decide.
func htmlAttrs(p map[string]string) (theme, density, motion string) {
	if t := p["theme"]; t == "light" || t == "dark" {
		theme = t
	}
	switch p["density"] {
	case "compact":
		density = "compact"
	case "spacious":
		density = "roomy"
	}
	switch p["reduced_motion"] {
	case "on":
		motion = "reduce"
	case "off":
		motion = "full"
	}
	return
}

// editorPrefsFrom turns the stored preferences into the writing view's data
// attributes (the stylesheet has a rule for every value the allowlist permits).
func editorPrefsFrom(p map[string]string) editorPrefs {
	e := defaultPrefs()
	if p == nil {
		return e
	}
	if n := atoiOr(p["editor_size"], 0); n >= 16 && n <= 24 {
		e.Size = n
	}
	e.Measure = p["editor_measure"]
	if e.Measure == "" {
		e.Measure = "66"
	}
	if lh := strings.ReplaceAll(p["editor_line_height"], ".", ""); lh != "" {
		// "1.65" -> 165, "1.4" -> 140, "1.9" -> 190
		for len(lh) < 3 {
			lh += "0"
		}
		e.LH = atoiOr(lh, 165)
	}
	if f := p["editor_font"]; f == "serif" || f == "sans" || f == "mono" {
		e.Font = f
	}
	e.AutosaveMS = atoiOr(p["autosave_ms"], 700)
	e.ShowCounts = p["show_word_count"] != "false"
	e.Typewriter = p["typewriter"] == "true"
	e.FocusDim = p["focus_dim"] == "true"
	return e
}

func atoiOr(s string, d int) int {
	n := 0
	if s == "" {
		return d
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	return n
}

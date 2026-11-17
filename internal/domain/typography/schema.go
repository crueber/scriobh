// Package typography is custom typography for a project's rich and reading views (F-085).
//
// A project's owner or an editor sets it; each person may override it for themselves. It is a fixed schema, validated
// on the server: no free CSS, no URL, no font upload, no external font. The values are enums and bounded numbers, and
// the page turns them into data attributes that stylesheet rules already in web/static/css/typography.css read. It is
// applied when a page loads, never live into an open editor, and never to exports, compile, print, a share link or
// the static site, which have their own typography options.
//
// Precedence, lowest to highest: what the application ships, the person's own editor settings (Settings > Editor), the
// project's typography, the person's typography override. The person's text size is different: it is always the base
// of the size, and the project's size is a percentage of it that can only keep or enlarge it, so a project can never
// make the text smaller than the person chose for themselves.
package typography

import (
	"encoding/json"
	"strconv"

	"github.com/crueber/scriobh/internal/ops"
)

// Settings is one layer of choices. A zero field is "not set": the layer below decides.
type Settings struct {
	Font       string `json:"font,omitempty" enum:"serif,sans,mono,system-serif,system-sans,system-mono" desc:"Typeface: Newsreader, Instrument Sans or IBM Plex Mono (already part of the application), or the system's serif, sans or monospace. Nothing is downloaded."`
	Size       int    `json:"size,omitempty" desc:"Body size as a percentage of the reader's own text size (Settings > Editor), 100 to 140 in steps of 5. Never below 100: a project cannot make the text smaller than the reader chose."`
	LineHeight string `json:"line_height,omitempty" desc:"Line height as a multiple of the size: 1.3 to 2 in steps of 0.05, written without a trailing zero (\"1.3\", \"1.35\", \"2\"). Never below 1.3."`
	Measure    int    `json:"measure,omitempty" desc:"Characters per line: 40, 45, 50, 55, 60, 66, 70, 75 or 80. Never wider than 80."`
	Paragraphs string `json:"paragraphs,omitempty" enum:"spacing,indent" desc:"spacing: a gap between paragraphs (the default). indent: a first-line indent and no gap, as in a printed book."`
	SceneBreak string `json:"scene_break,omitempty" enum:"ornament,rule" desc:"How a scene break is drawn: ornament (three asterisks, the default) or a short rule."`
}

// The bounds, in one place. The tests walk every value; the stylesheet has a rule for each (a test reads the file).
const (
	MinSize       = 100
	MaxSize       = 140
	SizeStep      = 5
	MinLineHeight = 130 // x100
	MaxLineHeight = 200
	LineStep      = 5
	MinMeasure    = 40
	MaxMeasure    = 80
)

// Shipped defaults: the values that need no attribute because the application already draws them.
const (
	DefaultSize       = 100
	DefaultParagraphs = "spacing"
	DefaultSceneBreak = "ornament"
)

// Fonts are the allowed typefaces, serif first.
var Fonts = []string{"serif", "sans", "mono", "system-serif", "system-sans", "system-mono"}

// Measures are the allowed measures.
var Measures = []int{40, 45, 50, 55, 60, 66, 70, 75, 80}

// Sizes lists the allowed size percentages.
func Sizes() []int {
	var out []int
	for n := MinSize; n <= MaxSize; n += SizeStep {
		out = append(out, n)
	}
	return out
}

// LineHeights lists the allowed line heights, canonical spelling.
func LineHeights() []string {
	var out []string
	for n := MinLineHeight; n <= MaxLineHeight; n += LineStep {
		out = append(out, FormatLH(n))
	}
	return out
}

// FormatLH writes hundredths as the canonical string: 130 is "1.3", 135 "1.35", 200 "2".
func FormatLH(n int) string { return strconv.FormatFloat(float64(n)/100, 'f', -1, 64) }

// ParseLH reads a canonical line height back to hundredths; ok is false for anything else.
func ParseLH(s string) (int, bool) {
	for n := MinLineHeight; n <= MaxLineHeight; n += LineStep {
		if FormatLH(n) == s {
			return n, true
		}
	}
	return 0, false
}

func inList(v string, list ...string) bool {
	for _, x := range list {
		if v == x {
			return true
		}
	}
	return false
}

// Validate checks every set field against the fixed lists. It names the first offender in a fixed order.
func (s Settings) Validate() error {
	if s.Font != "" && !inList(s.Font, Fonts...) {
		return ops.Invalid("font", "Choose one of the listed typefaces.")
	}
	if s.Size != 0 && (s.Size < MinSize || s.Size > MaxSize || s.Size%SizeStep != 0) {
		return ops.Invalid("size", "The size is 100 to 140 percent of the reader's own, in steps of 5.")
	}
	if s.LineHeight != "" {
		if _, ok := ParseLH(s.LineHeight); !ok {
			return ops.Invalid("line_height", "The line height is 1.3 to 2 in steps of 0.05, written like 1.35.")
		}
	}
	if s.Measure != 0 {
		ok := false
		for _, m := range Measures {
			ok = ok || m == s.Measure
		}
		if !ok {
			return ops.Invalid("measure", "The measure is 40, 45, 50, 55, 60, 66, 70, 75 or 80 characters.")
		}
	}
	if s.Paragraphs != "" && !inList(s.Paragraphs, "spacing", "indent") {
		return ops.Invalid("paragraphs", "Paragraphs are spacing or indent.")
	}
	if s.SceneBreak != "" && !inList(s.SceneBreak, "ornament", "rule") {
		return ops.Invalid("scene_break", "A scene break is an ornament or a rule.")
	}
	return nil
}

// IsZero reports whether nothing is set.
func (s Settings) IsZero() bool { return s == Settings{} }

// Clean keeps the valid fields of a stored value and drops the rest, so a list that narrows later never breaks a page.
func (s Settings) Clean() Settings {
	var out Settings
	if (Settings{Font: s.Font}).Validate() == nil {
		out.Font = s.Font
	}
	if (Settings{Size: s.Size}).Validate() == nil {
		out.Size = s.Size
	}
	if (Settings{LineHeight: s.LineHeight}).Validate() == nil {
		out.LineHeight = s.LineHeight
	}
	if (Settings{Measure: s.Measure}).Validate() == nil {
		out.Measure = s.Measure
	}
	if (Settings{Paragraphs: s.Paragraphs}).Validate() == nil {
		out.Paragraphs = s.Paragraphs
	}
	if (Settings{SceneBreak: s.SceneBreak}).Validate() == nil {
		out.SceneBreak = s.SceneBreak
	}
	return out
}

// Over returns s with every field that top sets replaced by top's.
func (s Settings) Over(top Settings) Settings {
	if top.Font != "" {
		s.Font = top.Font
	}
	if top.Size != 0 {
		s.Size = top.Size
	}
	if top.LineHeight != "" {
		s.LineHeight = top.LineHeight
	}
	if top.Measure != 0 {
		s.Measure = top.Measure
	}
	if top.Paragraphs != "" {
		s.Paragraphs = top.Paragraphs
	}
	if top.SceneBreak != "" {
		s.SceneBreak = top.SceneBreak
	}
	return s
}

// Decode reads a stored value, tolerating anything: an unreadable row is "nothing set".
func Decode(raw string) Settings {
	var s Settings
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil {
		return Settings{}
	}
	return s.Clean()
}

// Notice is what a screen says about where this applies.
const Notice = "Applies to the writing and reading views on screen, when a page loads. Exports, compile, print and the static site have their own typography options and never use these."

// Schema is what a screen needs to draw the form.
type Schema struct {
	Fonts       []string `json:"fonts"`
	Sizes       []int    `json:"sizes" desc:"Percentages of the reader's own text size."`
	LineHeights []string `json:"line_heights"`
	Measures    []int    `json:"measures"`
	Paragraphs  []string `json:"paragraphs"`
	SceneBreaks []string `json:"scene_breaks"`
	Notice      string   `json:"notice"`
}

// TheSchema is the fixed schema.
func TheSchema() Schema {
	return Schema{Fonts: Fonts, Sizes: Sizes(), LineHeights: LineHeights(), Measures: Measures,
		Paragraphs: []string{"spacing", "indent"}, SceneBreaks: []string{"ornament", "rule"}, Notice: Notice}
}

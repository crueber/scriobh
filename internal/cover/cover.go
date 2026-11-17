// Package cover draws a project's typographic cover: a flat colour, the title,
// the subtitle, the author and the kind, as an SVG.
//
// A cover is a function of its text. The same title, subtitle, author, kind and
// colour give the same bytes, so a cover can be cached by the hash of what it
// shows and used as an image anywhere (the library tile, the title page of an
// HTML export). There is no gradient and no image: one flat fill, a hairline
// rule and text in the system serif stack. Text is wrapped by measured
// character widths (an estimate of a serif face, since no font is embedded) and
// shrinks before it is cut.
//
// The SVG uses presentation attributes only (no style element, no style
// attributes, no script), so it stays inert under the application's strict
// Content-Security-Policy and when opened on its own.
package cover

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Version changes whenever the drawing changes, so a cached cover is never
// served for newer rules.
const Version = 1

// Palette is the flat colours a cover may use: the cover-0..7 swatches of the
// design tokens (projects.CoverPalette repeats it for validation).
var Palette = []string{"#2F5D50", "#7A4B2A", "#3B5B7A", "#6B3E4F", "#4B6B3A", "#5A4E7A", "#8A6420", "#4A4740"}

const (
	width, height = 600, 800
	margin        = 56
	ink           = "#FFFFFF"
	// The stack is the design's prose face without the bundled font: SVG used
	// as an image cannot load one, so it falls back to what the system has.
	serif = "Newsreader, 'Iowan Old Style', 'Palatino Linotype', Palatino, Georgia, 'Times New Roman', serif"
	sans  = "'Instrument Sans', system-ui, -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif"
)

// Spec is what a cover shows.
type Spec struct {
	Title, Subtitle, Author, Kind string
	// Color is one of Palette; anything else is replaced by a colour chosen from
	// the title, so even a project with no colour gets a stable one.
	Color string
}

// Key is the content hash of everything a cover depends on.
func (s Spec) Key() string {
	s = s.clean()
	h := sha256.New()
	fmt.Fprintf(h, "v%d\x00%s\x00%s\x00%s\x00%s\x00%s", Version, s.Title, s.Subtitle, s.Author, s.Kind, s.Color)
	return hex.EncodeToString(h.Sum(nil))
}

// Clean is the spec as the drawing reads it: text tidied and bounded, the
// colour one of the palette (chosen from the title when it is not).
func (s Spec) Clean() Spec { return s.clean() }

// clean normalises the text the way the drawing reads it: invalid XML
// characters removed, runs of white space folded, lengths bounded.
func (s Spec) clean() Spec {
	s.Title, s.Subtitle, s.Author, s.Kind = tidy(s.Title, 200), tidy(s.Subtitle, 200), tidy(s.Author, 200), tidy(s.Kind, 40)
	s.Color = strings.ToUpper(strings.TrimSpace(s.Color))
	ok := false
	for _, c := range Palette {
		if c == s.Color {
			ok = true
		}
	}
	if !ok {
		sum := sha256.Sum256([]byte(s.Title))
		s.Color = Palette[int(sum[0])%len(Palette)]
	}
	return s
}

func tidy(s string, max int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError, !validXML(r):
			continue
		case unicode.IsSpace(r):
			b.WriteByte(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r): // controls, invisible formatting and bidirectional overrides
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > max {
		out = string(r[:max])
	}
	return out
}

// validXML reports whether r is allowed in an XML 1.0 document. Escaping alone
// does not make a control character legal; an image holding one does not load.
func validXML(r rune) bool {
	switch {
	case r == 0x9 || r == 0xA || r == 0xD:
		return true
	case r >= 0x20 && r <= 0xD7FF, r >= 0xE000 && r <= 0xFFFD, r >= 0x10000 && r <= 0x10FFFF:
		return r != 0xFFFE && r != 0xFFFF
	}
	return false
}

// ---- measuring and wrapping ----------------------------------------------------------

// advance is the width of r in ems for a typical serif face, by class. It is an
// estimate; a cover must never overflow on it, so the margins have slack.
func advance(r rune) float64 {
	switch {
	case r == ' ':
		return 0.25
	case strings.ContainsRune("ijl.,;:'!|’", r):
		return 0.27
	case strings.ContainsRune("ftr()-[]\"‘“”", r):
		return 0.36
	case strings.ContainsRune("mwMWæœÆŒ", r):
		return 0.82
	case unicode.IsUpper(r):
		return 0.68
	case unicode.IsDigit(r):
		return 0.5
	case r >= 0x2E80: // CJK and kana, full width
		return 1.0
	case unicode.IsLetter(r):
		return 0.5
	}
	return 0.55
}

func measure(s string, size, track float64) float64 {
	w := 0.0
	for _, r := range s {
		w += advance(r)*size + track
	}
	return w
}

// wrap breaks s into lines no wider than max at the given size. A word wider
// than a line is split. ok is false when more than maxLines lines are needed.
func wrap(s string, size, track, max float64, maxLines int) (lines []string, ok bool) {
	var cur string
	push := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, word := range strings.Fields(s) {
		for measure(word, size, track) > max { // split an over-long word
			cut, w := 0, 0.0
			for i, r := range word {
				w += advance(r)*size + track
				if w > max {
					break
				}
				cut = i + utf8.RuneLen(r)
			}
			if cut == 0 {
				_, n := utf8.DecodeRuneInString(word)
				cut = n
			}
			push()
			lines = append(lines, word[:cut])
			word = word[cut:]
		}
		if word == "" {
			continue
		}
		next := word
		if cur != "" {
			next = cur + " " + word
		}
		if measure(next, size, track) > max {
			push()
			next = word
		}
		cur = next
	}
	push()
	return lines, len(lines) <= maxLines
}

// fit finds the largest size from hi down to lo at which s wraps in maxLines
// and no more than maxH tall; at lo it is cut with an ellipsis.
func fit(s string, hi, lo, track, max float64, maxLines int, maxH float64) (lines []string, size float64) {
	for size = hi; size >= lo; size -= 2 {
		if l, ok := wrap(s, size, track, max, maxLines); ok && float64(len(l))*size*1.3 <= maxH {
			return l, size
		}
	}
	size = lo
	l, _ := wrap(s, size, track, max, maxLines)
	if len(l) > maxLines {
		l = l[:maxLines]
		last := []rune(l[maxLines-1])
		for len(last) > 0 && measure(string(last)+"…", size, track) > max {
			last = last[:len(last)-1]
		}
		l[maxLines-1] = strings.TrimRight(string(last), " ") + "…"
	}
	return l, size
}

// ---- drawing -----------------------------------------------------------------------------

func esc(s string) string { return html.EscapeString(s) }

// SVG draws the cover.
func SVG(s Spec) []byte {
	s = s.clean()
	textW := float64(width - 2*margin)
	var b strings.Builder
	label := "Cover of " + s.Title
	if s.Author != "" {
		label += " by " + s.Author
	}
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="%s">`+"\n", width, height, width, height, esc(label))
	fmt.Fprintf(&b, `<title>%s</title>`+"\n", esc(label))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="%s"/>`+"\n", width, height, s.Color)

	y := float64(margin) + 20
	// The title: large, top left, up to five lines.
	title, size := fit(s.Title, 66, 30, 0, textW, 5, 320)
	if len(title) == 0 {
		title = []string{"Untitled"}
	}
	lead := size * 1.18
	y += size * 0.8
	for _, l := range title {
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-family="%s" font-size="%.0f" fill="%s">%s</text>`+"\n", margin, y, esc(serif), size, ink, esc(l))
		y += lead
	}
	y += 6
	fmt.Fprintf(&b, `<rect x="%d" y="%.1f" width="44" height="2" fill="%s" fill-opacity="0.6"/>`+"\n", margin, y, ink)
	y += 40
	if s.Subtitle != "" {
		sub, ssize := fit(s.Subtitle, 28, 18, 0, textW, 3, 120)
		for _, l := range sub {
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-family="%s" font-size="%.0f" font-style="italic" fill="%s" fill-opacity="0.9">%s</text>`+"\n", margin, y, esc(serif), ssize, ink, esc(l))
			y += ssize * 1.3
		}
	}
	// The author and the kind sit at the foot, the way the library tile has the kind.
	by := float64(height - margin)
	if kind := strings.ToUpper(s.Kind); kind != "" {
		fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-family="%s" font-size="18" font-weight="500" letter-spacing="3" fill="%s" fill-opacity="0.8">%s</text>`+"\n", margin, by, esc(sans), ink, esc(clip(kind, 18, 3, textW)))
		by -= 44
	}
	if s.Author != "" {
		au, asize := fit(strings.ToUpper(s.Author), 26, 16, 2, textW, 2, 80)
		by -= float64(len(au)-1) * asize * 1.3
		for _, l := range au {
			fmt.Fprintf(&b, `<text x="%d" y="%.1f" font-family="%s" font-size="%.0f" letter-spacing="2" fill="%s">%s</text>`+"\n", margin, by, esc(serif), asize, ink, esc(l))
			by += asize * 1.3
		}
	}
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

// clip cuts s with an ellipsis so it fits max at the size and tracking.
func clip(s string, size, track, max float64) string {
	r := []rune(s)
	if measure(s, size, track) <= max {
		return s
	}
	for len(r) > 0 && measure(string(r)+"…", size, track) > max {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// ---- cache ---------------------------------------------------------------------------------

var (
	mu    sync.Mutex
	cache = map[string][]byte{}
	order []string
)

const cacheMax = 512

// Cached returns the cover for a spec, drawn once per content hash. The cache
// is bounded; the oldest entry goes first.
func Cached(s Spec) (key string, svg []byte) {
	key = s.Key()
	mu.Lock()
	if b, ok := cache[key]; ok {
		mu.Unlock()
		return key, b
	}
	mu.Unlock()
	b := SVG(s)
	mu.Lock()
	defer mu.Unlock()
	if _, ok := cache[key]; !ok {
		cache[key] = b
		order = append(order, key)
		for len(order) > cacheMax {
			delete(cache, order[0])
			order = order[1:]
		}
	}
	return key, b
}

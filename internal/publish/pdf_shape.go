package publish

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/crueber/scriobh/internal/publish/font"
)

// Warn is something a writer had to leave out, change or could not fix, with
// a stable code the compile pipeline reports.
type Warn struct {
	Code    string
	Message string
}

// style is the look of a run of text.
type style uint8

const (
	stItalic style = 1 << iota
	stBold
	stMono
	stStrike
	stSup   // a footnote reference: smaller and raised
	stSmall // inline code: a little smaller than the text around it
)

// faceRT is an embedded face as the layout uses it.
type faceRT struct {
	idx  int
	face *font.Face
	f    *font.Font
	name string
}

// typeface is a document's fonts: the chosen family in its styles, the mono
// face for code, and the fallbacks for characters the family lacks.
type typeface struct {
	body    [4]*faceRT
	mono    *faceRT
	fb      []*faceRT
	all     []*faceRT
	natural int64 // the family's natural line height in font units over units per em, in thousandths

	missing     map[rune]int
	unsupported map[string]int
	quiet       bool // measuring only: do not count what is missing
	err         error
	note        string // a font substitution to report
}

func newTypeface(fontName string) (*typeface, error) {
	t := &typeface{missing: map[rune]int{}, unsupported: map[string]int{}}
	add := func(f *font.Face) *faceRT {
		if f == nil {
			return nil
		}
		for _, x := range t.all {
			if x.face == f {
				return x
			}
		}
		pf, err := f.Font()
		if err != nil {
			t.err = err
			return nil
		}
		rt := &faceRT{idx: len(t.all), face: f, f: pf, name: f.Name}
		t.all = append(t.all, rt)
		return rt
	}
	fam := font.Serif()
	switch fontName {
	case "", "Times New Roman", "Liberation Serif":
	case "Liberation Mono", "Courier New":
		fam = font.Mono()
	default:
		t.note = fmt.Sprintf("The font %s is not embedded in a PDF, so Liberation Serif was used instead.", fontName)
	}
	for s := font.Regular; s <= font.BoldItalic; s++ {
		f, _ := fam.Face(s)
		t.body[s] = add(f)
	}
	mf, _ := font.Mono().Face(font.Regular)
	t.mono = add(mf)
	for _, fb := range font.Fallbacks() {
		f, _ := fb.Face(font.Regular)
		if rt := add(f); rt != nil {
			t.fb = append(t.fb, rt)
		}
	}
	if t.err != nil {
		return nil, t.err
	}
	reg := t.body[font.Regular].f
	t.natural = int64(reg.Ascent+reg.Descent+reg.LineGap) * 1000 / int64(reg.UnitsPerEm)
	return t, nil
}

func (t *typeface) bodyFace(st style) *faceRT {
	if st&stMono != 0 {
		return t.mono
	}
	var s font.Style
	switch {
	case st&stBold != 0 && st&stItalic != 0:
		s = font.BoldItalic
	case st&stBold != 0:
		s = font.Bold
	case st&stItalic != 0:
		s = font.Italic
	}
	return t.body[s]
}

// unsupportedScript names the script of a character that needs shaping (its
// letters change shape with their neighbours, or it is written right to left,
// or it is ideographic), which this layout does not do.
func unsupportedScript(r rune) string {
	if r < 0x0590 {
		return ""
	}
	for _, s := range []struct {
		name string
		tab  *unicode.RangeTable
	}{{"Arabic", unicode.Arabic}, {"Hebrew", unicode.Hebrew}, {"Syriac", unicode.Syriac}, {"Thaana", unicode.Thaana}, {"Devanagari", unicode.Devanagari},
		{"Bengali", unicode.Bengali}, {"Gurmukhi", unicode.Gurmukhi}, {"Gujarati", unicode.Gujarati}, {"Tamil", unicode.Tamil}, {"Telugu", unicode.Telugu},
		{"Kannada", unicode.Kannada}, {"Malayalam", unicode.Malayalam}, {"Sinhala", unicode.Sinhala}, {"Thai", unicode.Thai}, {"Lao", unicode.Lao},
		{"Tibetan", unicode.Tibetan}, {"Myanmar", unicode.Myanmar}, {"Khmer", unicode.Khmer}, {"Mongolian", unicode.Mongolian},
		{"Han", unicode.Han}, {"Hiragana", unicode.Hiragana}, {"Katakana", unicode.Katakana}, {"Hangul", unicode.Hangul}} {
		if unicode.Is(s.tab, r) {
			return s.name
		}
	}
	return ""
}

// ignorable are characters that draw nothing and are dropped silently:
// controls, joiners, bidirectional marks and overrides, the soft hyphen (no
// hyphenation is done), the byte order mark, variation selectors and tags.
func ignorable(r rune) bool {
	switch {
	case r < 0x20:
		return r != '\t' && r != '\n'
	case r >= 0x7f && r <= 0x9f:
		return true
	case r == 0x00AD, r == 0x034F, r == 0x061C, r == 0x180E:
		return true
	case r >= 0x200C && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2060 && r <= 0x206F, r == 0xFEFF:
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0000 && r <= 0xE0FFF:
		return true
	case r >= 0xFFF9 && r <= 0xFFFB, r == 0xFFFE, r == 0xFFFF:
		return true
	}
	return false
}

// cleanText normalises to NFC and drops ignorable characters. A zero-width
// space is kept: the tokenizer reads it as a place a line may end.
func cleanText(s string) string {
	plain := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x80 || c < 0x20 && c != '\t' && c != '\n' || c == 0x7f {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == 0x200B {
			b.WriteRune(r)
			continue
		}
		if ignorable(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// pick chooses the face and glyph for a character: the style's face, then the
// fallbacks. ok is false when no face has it (the layout then draws a
// replacement and warns).
func (t *typeface) pick(r rune, st style) (*faceRT, uint16, bool) {
	if s := unsupportedScript(r); s != "" {
		if !t.quiet {
			t.unsupported[s]++
		}
		return nil, 0, false
	}
	prim := t.bodyFace(st)
	if g, ok := prim.f.Glyph(r); ok {
		return prim, g, true
	}
	if prim != t.body[font.Regular] && st&stMono == 0 {
		// a style the family has but lacks this character in: its regular face
		if g, ok := t.body[font.Regular].f.Glyph(r); ok {
			return t.body[font.Regular], g, true
		}
	}
	for _, fb := range t.fb {
		if g, ok := fb.f.Glyph(r); ok {
			return fb, g, true
		}
	}
	return nil, 0, false
}

// glyphRun is a run of glyphs in one face at one size.
type glyphRun struct {
	face   *faceRT
	size   mpt
	rise   mpt
	track  mpt // extra space after each glyph (letter-spacing of a label in capitals)
	gids   []uint16
	runes  []rune
	adv    []int32 // advance of each glyph
	kern   []int32 // adjustment after each glyph (zero or negative), same length
	width  mpt
	strike bool
}

// shape turns text into runs. Characters the faces lack become a question
// mark (or a space) and are counted for the warning; nothing draws glyph 0.
func (t *typeface) shape(text string, st style, size mpt, rise mpt) []glyphRun {
	if text == "" {
		return nil
	}
	// One backing array per call, carved into the runs: a word is shaped in a
	// handful of allocations, not one per glyph.
	n := len(text)
	gids := make([]uint16, 0, n)
	runes := make([]rune, 0, n)
	adv := make([]int32, 0, n)
	kern := make([]int32, 0, n)
	var runs []glyphRun
	start := 0
	var cur *glyphRun
	var lastG uint16
	closeRun := func() {
		if cur != nil {
			e := len(gids)
			cur.gids, cur.runes, cur.adv, cur.kern = gids[start:e:e], runes[start:e:e], adv[start:e:e], kern[start:e:e]
		}
	}
	for _, r := range text {
		if r == '\u00A0' || r == '\u202F' || r == '\u2007' {
			r = ' '
		}
		face, g, ok := t.pick(r, st)
		if !ok {
			if !t.quiet {
				t.missing[r]++
			}
			r = '?'
			face, g, ok = t.pick('?', st)
			if !ok {
				face, g = t.bodyFace(st), 0
				if sp, has := face.f.Glyph(' '); has {
					g = sp
				}
			}
		}
		if g == 0 {
			continue
		}
		if cur == nil || cur.face != face {
			closeRun()
			start = len(gids)
			runs = append(runs, glyphRun{face: face, size: size, rise: rise, strike: st&stStrike != 0})
			cur = &runs[len(runs)-1]
			lastG = 0
		}
		a := int32(face.f.Scale(face.f.Advance(g), size))
		if lastG != 0 {
			if k := face.f.Kern(lastG, g); k != 0 {
				kk := int32(face.f.Scale(k, size))
				kern[len(kern)-1] = kk
				cur.width += mpt(kk)
			}
		}
		gids = append(gids, g)
		runes = append(runes, r)
		adv = append(adv, a)
		kern = append(kern, 0)
		cur.width += mpt(a)
		lastG = g
	}
	closeRun()
	return runs
}

// width measures text set plain in a style.
func (t *typeface) width(text string, st style, size mpt) mpt {
	t.quiet = true
	defer func() { t.quiet = false }()
	var w mpt
	for _, r := range t.shape(cleanText(text), st, size, 0) {
		w += r.width
	}
	return w
}

func (t *typeface) spaceWidth(st style, size mpt) mpt {
	f := t.bodyFace(st)
	g, _ := f.f.Glyph(' ')
	return f.f.Scale(f.f.Advance(g), size)
}

// warnings reports what shaping could not do, aggregated.
func (t *typeface) warnings() []Warn {
	var out []Warn
	if t.note != "" {
		out = append(out, Warn{"font_substituted", t.note})
	}
	if len(t.missing) > 0 {
		var rs []rune
		total := 0
		for r, n := range t.missing {
			rs = append(rs, r)
			total += n
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
		var names []string
		for i, r := range rs {
			if i == 12 {
				names = append(names, fmt.Sprintf("and %d more", len(rs)-12))
				break
			}
			names = append(names, fmt.Sprintf("U+%04X", r))
		}
		out = append(out, Warn{"missing_glyph", fmt.Sprintf("%d character(s) of %d kind(s) are not in any embedded font and were drawn as a question mark: %s.", total, len(rs), strings.Join(names, ", "))})
	}
	if len(t.unsupported) > 0 {
		var names []string
		total := 0
		for n, c := range t.unsupported {
			names = append(names, n)
			total += c
		}
		sort.Strings(names)
		out = append(out, Warn{"script_unsupported", fmt.Sprintf("%d character(s) are in scripts this layout cannot shape (%s) and were drawn as a question mark. Use the EPUB or DOCX for them.", total, strings.Join(names, ", "))})
	}
	return out
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func runeCount(s string) int { return utf8.RuneCountInString(s) }

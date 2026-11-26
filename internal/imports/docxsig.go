package imports

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What the chapter detector reads of a paragraph, recorded while the body is read (nothing here is a new parse: the paragraph
// is already in hand). The signals are read from the view the text is imported from, so a boundary never exists only in a
// view that was not imported. lastRenderedPageBreak is ignored: it is a cache of where Word last put a page, and stale in
// many files.

// blockSig is the look of one paragraph or heading.
type blockSig struct {
	ord        int    // the paragraph's index among the body's w:p elements: the stable part of a chapter's key
	style      string // the paragraph style's name, lower case
	outline    int    // the outline level (1 to 9), from the paragraph, its style or a style named "heading N"; 0 for none
	pageBefore bool   // pageBreakBefore, on the paragraph or in its style
	brBefore   bool   // a page break just before it: an empty paragraph that holds one, or one at its own start
	sectBefore bool   // a next-page section break just before it
	center     bool
	bold       bool // every run of text is bold
	caps       bool // written in capitals, or set in them
	size       int  // the largest size of its text, in half-points; 0 when no size is stated
	before     int  // space above, in twentieths of a point
	toc        bool // a contents entry: a toc style, or a TOC field
	numLabel   string
	plain      string // its text, as the view imported reads it; empty for a long paragraph
	words      int
	full       bool // the text ends like a sentence
}

// look is what a paragraph's properties and runs say of how it looks and where the page changes.
type look struct {
	pageBefore, center, bold, caps bool
	size, before                   int
	brStart, brEnd, brAny, sect    bool
	toc                            bool
}

// lookOf reads a paragraph's layout. p is the paragraph, ppr its properties (nil when it has none).
func (c *docxConv) lookOf(p, ppr *dnode, styleID, lname string) look {
	sp := c.styles.props(styleID)
	var l look
	l.pageBefore = sp.pageBreak
	l.center = sp.jc == "center"
	l.before = sp.before
	if ppr != nil {
		if pb := ppr.child("pageBreakBefore"); pb != nil {
			l.pageBefore = isOn(pb)
		}
		if jc := ppr.child("jc"); jc != nil {
			l.center = jc.val() == "center"
		}
		if sp := ppr.child("spacing"); sp != nil {
			if v, err := strconv.Atoi(sp.attr("before")); err == nil && v >= 0 {
				l.before = v
			}
		}
		if sec := ppr.child("sectPr"); sec != nil {
			switch sec.child("type").val() {
			case "continuous", "nextColumn":
			default:
				l.sect = true
			}
		}
	}
	if strings.HasPrefix(lname, "toc ") || lname == "toc heading" || lname == "table of contents" || lname == "contents heading" {
		l.toc = true
	}
	seenText, texts, boldRuns, capRuns := false, 0, 0, 0
	var walk func(n *dnode)
	walk = func(n *dnode) {
		for _, k := range n.kids {
			switch k.name {
			case "r":
				var txt strings.Builder
				for _, rk := range k.kids {
					switch rk.name {
					case "br":
						if rk.attr("type") == "page" {
							l.brAny = true
							if seenText {
								l.brEnd = true
							} else {
								l.brStart = true
							}
						}
					case "t":
						txt.WriteString(rk.text)
						if strings.TrimSpace(rk.text) != "" {
							seenText = true
						}
					case "instrText":
						if f := strings.TrimSpace(rk.text); strings.HasPrefix(strings.ToUpper(f), "TOC") {
							l.toc = true
						}
					}
				}
				if strings.TrimSpace(txt.String()) == "" {
					continue
				}
				texts++
				bold, caps, size := sp.bold, sp.caps, sp.size
				if rp := k.child("rPr"); rp != nil {
					if b := rp.child("b"); b != nil {
						bold = isOn(b)
					}
					if cp := rp.child("caps"); cp != nil {
						caps = isOn(cp)
					}
					if sz := rp.child("sz"); sz != nil {
						if v, err := strconv.Atoi(sz.val()); err == nil && v > 0 && v < 1000 {
							size = v
						}
					}
				}
				if bold {
					boldRuns++
				}
				if caps {
					capRuns++
				}
				l.size = max(l.size, size)
			case "ins", "moveTo", "hyperlink", "smartTag", "fldSimple", "sdt", "sdtContent", "customXml", "bdo", "dir":
				walk(k)
			}
		}
	}
	walk(p)
	l.bold = texts > 0 && boldRuns == texts
	l.caps = texts > 0 && capRuns == texts
	return l
}

// sigOf makes a paragraph's signals. final is the text of the view imported when it differs from plain (a paragraph with
// tracked changes read as the original).
func (c *docxConv) sigOf(l look, plain, final string, outline int, lname string, ord int) *blockSig {
	text := plain
	if final != "" {
		text = final
	}
	sg := c.newSig()
	*sg = blockSig{ord: ord, style: lname, outline: outline, pageBefore: l.pageBefore, center: l.center, bold: l.bold,
		caps: l.caps || allCapsText(text), size: l.size, before: l.before, toc: l.toc, words: len(strings.Fields(text))}
	// A paragraph of prose is never a title, a label or a contents line: its words are counted, not kept. A line of 200 characters or fewer
	// is kept whatever its words or bytes (a line of Chinese is three bytes a character and one "word"), because that is what a pattern is
	// tried on; the byte guard keeps a hostile file from holding more than a few hundred bytes of each paragraph.
	if (len(text) <= 400 && sg.words <= 60) || (len(text) <= 3*maxPatternLine*2 && utf8.RuneCountInString(text) <= maxPatternLine) {
		sg.plain = text
	}
	sg.full = endsLikeSentence(text)
	return sg
}

// newSig takes a blockSig from a slab: a book of a million paragraphs does not make a million small allocations.
func (c *docxConv) newSig() *blockSig {
	if len(c.sigSlab) == 0 {
		c.sigSlab = make([]blockSig, 2048)
	}
	sg := &c.sigSlab[0]
	c.sigSlab = c.sigSlab[1:]
	return sg
}

// allCapsText says the text is written in capitals: at least two letters, none of them lower case.
func allCapsText(s string) bool {
	letters := 0
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			letters++
		}
	}
	return letters >= 2
}

// endsLikeSentence says the text ends with a full stop, a question mark, an exclamation mark or an ellipsis, maybe inside a quote.
func endsLikeSentence(s string) bool {
	s = strings.TrimRight(strings.TrimSpace(s), "\"'”’»)]")
	r, _ := utf8.DecodeLastRuneInString(s)
	return r == '.' || r == '?' || r == '!' || r == '…'
}

// numLabelOf is the number Word would print for a numbered heading ("Chapter 1"), and counts it. It is shown in the review and
// is never put in a title: Scríobh numbers chapters when it compiles.
func (c *docxConv) numLabelOf(numID string, lvl int) string {
	if numID == "" || strings.HasPrefix(numID, "-") || c.nums == nil {
		return ""
	}
	if c.numCount == nil {
		c.numCount = map[string][]int{}
	}
	lv := c.nums.level(numID, lvl)
	if lv.bullet || lv.text == "" {
		return ""
	}
	cnt := c.numCount[numID]
	for len(cnt) <= lvl {
		cnt = append(cnt, 0)
	}
	if cnt[lvl] == 0 {
		cnt[lvl] = lv.start
	} else {
		cnt[lvl]++
	}
	for i := lvl + 1; i < len(cnt); i++ {
		cnt[i] = 0
	}
	c.numCount[numID] = cnt
	var b strings.Builder
	t := lv.text
	for i := 0; i < len(t); i++ {
		if t[i] == '%' && i+1 < len(t) && t[i+1] >= '1' && t[i+1] <= '9' {
			l := int(t[i+1] - '1')
			n := 0
			if l < len(cnt) {
				n = cnt[l]
			}
			b.WriteString(numberIn(n, c.nums.level(numID, l).format))
			i++
			continue
		}
		b.WriteByte(t[i])
	}
	out, _ := cleanLine(b.String(), 60)
	return out
}

// numberIn writes n in one of Word's number formats.
func numberIn(n int, format string) string {
	switch format {
	case "upperRoman":
		return roman(n, true)
	case "lowerRoman":
		return roman(n, false)
	case "upperLetter":
		return letters(n, true)
	case "lowerLetter":
		return letters(n, false)
	case "decimalZero":
		if n < 10 {
			return "0" + strconv.Itoa(n)
		}
	}
	return strconv.Itoa(n)
}

func roman(n int, upper bool) string {
	if n <= 0 || n > 3999 {
		return strconv.Itoa(n)
	}
	var b strings.Builder
	for _, p := range []struct {
		v int
		s string
	}{{1000, "M"}, {900, "CM"}, {500, "D"}, {400, "CD"}, {100, "C"}, {90, "XC"}, {50, "L"}, {40, "XL"}, {10, "X"}, {9, "IX"}, {5, "V"}, {4, "IV"}, {1, "I"}} {
		for n >= p.v {
			b.WriteString(p.s)
			n -= p.v
		}
	}
	if upper {
		return b.String()
	}
	return strings.ToLower(b.String())
}

func letters(n int, upper bool) string {
	if n <= 0 {
		return strconv.Itoa(n)
	}
	base := 'a'
	if upper {
		base = 'A'
	}
	var out []rune
	for n > 0 {
		n--
		out = append([]rune{base + rune(n%26)}, out...)
		n /= 26
	}
	return string(out)
}

// headOf is the first n bytes of s, cut at a character.
func headOf(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

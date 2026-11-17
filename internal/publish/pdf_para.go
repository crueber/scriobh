package publish

import (
	"strings"
	"unicode"

	"github.com/crueber/scriobh/internal/text"
)

// An item is what a line is made of, as in Knuth and Plass's model: a box
// (a word, drawn), glue (a space that can stretch and shrink, and where a line
// may end), or a penalty (a place a line may end, at a cost).
type itemKind uint8

const (
	iBox itemKind = iota
	iGlue
	iPenalty
)

const (
	penInf   = 10000 // a break here is forbidden
	penChunk = 1000  // a break inside a word too long for a line
)

type item struct {
	kind itemKind
	flag bool  // a penalty that is a break after a hyphen already in the text
	pen  int32 // penalty
	w    mpt   // width (a penalty: the width added if the line ends here)
	y, z mpt   // glue: stretch and shrink
	r0   int32 // box: first run in para.runs; glue: the run holding the space glyph
	r1   int32 // box: after the last run
	note int32 // box: the number of the footnote this is the reference of, else 0
}

// para is a paragraph's items and the runs they draw.
type para struct {
	items []item
	runs  []glyphRun
	spc   map[spaceKey]int32
	// dropCap is the large initial, drawn with the first line.
	dropCap *dropCap
	// Lines, once broken.
	lines []lineRec
}

type spaceKey struct {
	face int
	size mpt
}

type dropCap struct {
	run   glyphRun // the initial's glyph at its large size
	width mpt      // advance plus the gap after it
	lines int
}

// lineRec is a broken line: items [from, to), the stretch ratio in thousandths
// (positive stretches the glue, negative shrinks it), and where the line sits.
type lineRec struct {
	from, to int32
	r        int64
	nat      mpt // natural width of the contents
	indent   mpt
	width    mpt // the measure
	hyphen   bool
	last     bool
	forced   bool // ended by a hard line break
}

// lineFn gives the indent and measure of line i (0-based).
type lineFn func(i int) (indent, width mpt)

type seg struct {
	text string
	st   style
	note int  // a footnote reference: its number
	size mpt  // 0: the paragraph's size
	brk  bool // a hard line break
}

// flatten turns inline nodes into styled segments.
func flatten(in []text.Inline, st style, out *[]seg, imgs *[]text.Inline, linkURLs bool) {
	for _, n := range in {
		switch n.Kind {
		case text.IText:
			*out = append(*out, seg{text: n.Text, st: st})
		case text.ICode:
			*out = append(*out, seg{text: n.Text, st: st | stMono | stSmall})
		case text.IBreak:
			*out = append(*out, seg{brk: true, st: st})
		case text.IEmph:
			flatten(n.Kids, st^stItalic, out, imgs, linkURLs)
		case text.IStrong:
			flatten(n.Kids, st|stBold, out, imgs, linkURLs)
		case text.IStrike:
			flatten(n.Kids, st|stStrike, out, imgs, linkURLs)
		case text.ILink:
			flatten(n.Kids, st, out, imgs, linkURLs)
			if linkURLs && (strings.HasPrefix(n.Text, "http://") || strings.HasPrefix(n.Text, "https://") || strings.HasPrefix(n.Text, "mailto:")) {
				*out = append(*out, seg{text: " (" + n.Text + ")", st: st})
			}
		case text.IImage:
			*imgs = append(*imgs, n)
		case text.INote:
			*out = append(*out, seg{note: n.Num, st: st | stSup})
		}
	}
}

// paraBuilder makes the items of one paragraph.
type paraBuilder struct {
	ts       *typeface
	p        *para
	size     mpt
	justify  bool
	maxChunk mpt // the widest a piece of a word may be before it is cut
	// the word being read
	word       []wordPiece
	pendBreak  bool // the word may be cut after its last piece
	pendHyphen bool // that cut is after a hyphen-minus and needs a letter to follow
}

type wordPiece struct {
	text string
	st   style
	size mpt
}

func newParaBuilder(ts *typeface, size mpt, justify bool, maxChunk mpt) *paraBuilder {
	return &paraBuilder{ts: ts, p: &para{spc: map[spaceKey]int32{}}, size: size, justify: justify, maxChunk: maxChunk}
}

// breakAfter reports characters after which a word may be cut: an em or en
// dash, or a hyphen between letters or digits.
func breakAfter(r rune) bool {
	return r == '—' || r == '–' || r == '‐'
}

func (b *paraBuilder) spaceRun(st style, size mpt) int32 {
	face := b.ts.bodyFace(st)
	key := spaceKey{face.idx, size}
	if i, ok := b.p.spc[key]; ok {
		return i
	}
	g, _ := face.f.Glyph(' ')
	w := mpt(face.f.Scale(face.f.Advance(g), size))
	b.p.runs = append(b.p.runs, glyphRun{face: face, size: size, gids: []uint16{g}, runes: []rune{' '}, adv: []int32{int32(w)}, kern: []int32{0}, width: w})
	i := int32(len(b.p.runs) - 1)
	b.p.spc[key] = i
	return i
}

func (b *paraBuilder) glue(st style, size mpt) {
	if n := len(b.p.items); n > 0 && b.p.items[n-1].kind == iGlue {
		return // one space between words, however many were written
	}
	if len(b.p.items) == 0 {
		return // no space at the start of a paragraph
	}
	run := b.spaceRun(st, size)
	w := b.p.runs[run].width
	it := item{kind: iGlue, w: w, r0: run}
	if b.justify {
		it.y, it.z = w/2, w/3
	} else {
		it.y, it.z = 0, w/3
	}
	b.p.items = append(b.p.items, it)
}

func (b *paraBuilder) box(runs []glyphRun, note int32) {
	if len(runs) == 0 {
		return
	}
	r0 := int32(len(b.p.runs))
	var w mpt
	for _, r := range runs {
		w += r.width
		b.p.runs = append(b.p.runs, r)
	}
	b.p.items = append(b.p.items, item{kind: iBox, w: w, r0: r0, r1: int32(len(b.p.runs)), note: note})
}

// flushWord shapes the pieces read so far into one or more boxes.
func (b *paraBuilder) flushWord() {
	if len(b.word) == 0 {
		return
	}
	var runs []glyphRun
	for _, pc := range b.word {
		sz := pc.size
		if sz == 0 {
			sz = b.size
		}
		runs = append(runs, b.ts.shape(pc.text, pc.st, sz, 0)...)
	}
	b.word = b.word[:0]
	var total mpt
	for _, r := range runs {
		total += r.width
	}
	if b.maxChunk > 0 && total > b.maxChunk {
		b.chunks(runs)
		return
	}
	b.box(runs, 0)
}

// chunks cuts runs wider than a line into pieces no wider than maxChunk, with
// a costly break between them, so a word with no break in it cannot make a line
// overfull.
func (b *paraBuilder) chunks(runs []glyphRun) {
	var cur []glyphRun
	var w mpt
	flush := func() {
		if len(cur) > 0 {
			b.box(cur, 0)
			b.p.items = append(b.p.items, item{kind: iPenalty, pen: penChunk})
			cur = nil
			w = 0
		}
	}
	for _, r := range runs {
		start := 0
		for i := range r.gids {
			step := mpt(r.adv[i] + r.kern[i])
			if w+step > b.maxChunk && (w > 0 || i > start) {
				if i > start {
					cur = append(cur, sliceRun(r, start, i))
				}
				flush()
				start = i
			}
			w += step
		}
		if start < len(r.gids) {
			cur = append(cur, sliceRun(r, start, len(r.gids)))
		}
	}
	if len(cur) > 0 {
		b.box(cur, 0)
	}
}

func sliceRun(r glyphRun, from, to int) glyphRun {
	out := r
	out.gids, out.runes, out.adv, out.kern = r.gids[from:to], r.runes[from:to], r.adv[from:to], r.kern[from:to]
	out.width = 0
	for i := range out.gids {
		out.width += mpt(out.adv[i] + out.kern[i])
	}
	return out
}

func (b *paraBuilder) cutHere() {
	b.flushWord()
	if n := len(b.p.items); n > 0 && b.p.items[n-1].kind == iBox {
		b.p.items = append(b.p.items, item{kind: iPenalty, pen: 50})
	}
}

// add reads one styled segment.
func (b *paraBuilder) add(s seg) {
	size := s.size
	if size == 0 {
		size = b.size
		if s.st&stSmall != 0 {
			size = b.size * 92 / 100
		}
	}
	if s.brk {
		b.flushWord()
		b.pendBreak = false
		if len(b.p.items) > 0 {
			b.p.items = append(b.p.items, item{kind: iPenalty, pen: penInf}, item{kind: iGlue, y: 1 << 40}, item{kind: iPenalty, pen: -penInf})
		}
		return
	}
	if s.note > 0 {
		b.flushWord()
		b.pendBreak = false
		sup := size * 62 / 100
		runs := b.ts.shape(itoa(s.note), s.st&^stSup, sup, size*33/100)
		if len(runs) > 0 {
			r0 := int32(len(b.p.runs))
			var w mpt
			for _, r := range runs {
				w += r.width
				b.p.runs = append(b.p.runs, r)
			}
			b.p.items = append(b.p.items, item{kind: iBox, w: w, r0: r0, r1: int32(len(b.p.runs)), note: int32(s.note)})
		}
		return
	}
	txt := cleanText(s.text)
	// A code block keeps its spaces: each is a glyph of the monospace face inside
	// one unbreakable piece (cut at the measure, not at a space), so leading
	// indentation and runs of spaces survive. Inline code is ordinary text.
	pre := s.st&stMono != 0 && s.st&stSmall == 0
	var piece strings.Builder
	var prev rune
	flushPiece := func() {
		if piece.Len() > 0 {
			b.word = append(b.word, wordPiece{text: piece.String(), st: s.st, size: size})
			piece.Reset()
		}
	}
	for _, r := range txt {
		switch {
		case pre && (r == ' ' || r == '\t'):
			if r == '\t' {
				piece.WriteString("    ")
			} else {
				piece.WriteRune(' ')
			}
		case r == ' ' || r == '\t' || r == '\n':
			flushPiece()
			b.flushWord()
			b.pendBreak = false
			b.glue(s.st, size)
		case r == '​':
			flushPiece()
			b.cutHere()
			b.pendBreak = false
		default:
			if b.pendBreak {
				ok := true
				if b.pendHyphen && !isWordRune(r) {
					ok = false
				}
				if ok {
					flushPiece()
					b.cutHere()
				}
				b.pendBreak = false
			}
			piece.WriteRune(r)
			if breakAfter(r) || r == '-' && isWordRune(prev) {
				b.pendBreak = true
				b.pendHyphen = r == '-'
			}
		}
		prev = r
	}
	flushPiece()
}

// finish closes the paragraph: the last line is as long as it is, so the
// paragraph ends in glue that stretches without limit and a forced break.
func (b *paraBuilder) finish() *para {
	b.flushWord()
	b.pendBreak = false
	it := b.p.items
	for len(it) > 0 && it[len(it)-1].kind != iBox {
		it = it[:len(it)-1]
	}
	b.p.items = it
	b.p.items = append(b.p.items, item{kind: iPenalty, pen: penInf}, item{kind: iGlue, y: 1 << 40}, item{kind: iPenalty, pen: -penInf})
	return b.p
}

// buildPara reads inline nodes into a paragraph. Images found among them are
// returned for the caller to place between paragraphs.
func buildPara(ts *typeface, in []text.Inline, base style, size mpt, justify bool, maxChunk mpt, linkURLs bool) (*para, []text.Inline) {
	var segs []seg
	var imgs []text.Inline
	flatten(in, base, &segs, &imgs, linkURLs)
	b := newParaBuilder(ts, size, justify, maxChunk)
	for _, s := range segs {
		b.add(s)
	}
	return b.finish(), imgs
}

// plainRunes reports whether a paragraph has no box (only space).
func (p *para) empty() bool {
	for _, it := range p.items {
		if it.kind == iBox {
			return false
		}
	}
	return true
}

// firstLetter returns the first rune of a paragraph's text and whether it is
// a letter.
func firstLetter(in []text.Inline) (rune, bool) {
	for _, n := range in {
		switch n.Kind {
		case text.IText:
			for _, r := range n.Text {
				if unicode.IsSpace(r) {
					continue
				}
				return r, unicode.IsLetter(r)
			}
		case text.IEmph, text.IStrong, text.IStrike:
			if r, ok := firstLetter(n.Kids); ok || r != 0 {
				return r, ok
			}
		default:
			return 0, false
		}
	}
	return 0, false
}

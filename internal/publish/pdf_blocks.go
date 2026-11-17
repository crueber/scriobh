package publish

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/crueber/scriobh/internal/publish/font"
	"github.com/crueber/scriobh/internal/text"
)

// typesetter turns the structure of a book into boxes for the flow.
type typesetter struct {
	o  Options
	pr params
	ts *typeface
	b  *Book

	lead  mpt // body leading: every vertical measure is a whole number of these
	size  mpt
	capH  mpt // cap height of the body face at the body size
	ascH  mpt // ascent and descent of the body face at the body size
	descH mpt

	// counters that become warnings
	loose, overfull int
	longTable       int
	tableCapped     bool
	images          map[string]*imgPlace
	lowRes          int
	minDPI          int
	missingImage    int
	deepNest        int
	tableImages     int

	// footnotes of the section being set
	noteBytes int // note text set so far, for the whole book
	noteCut   int // notes not set because of the limit
	notes     map[int]text.Footnote
	noteBuf   map[int]*note
	endNotes  []text.Footnote
	budget    *textBudget
}

// textBudget bounds what a hostile book can make the layout do.
type textBudget struct {
	cells int // table cells left
	lines int // lines left, for the whole book
}

const (
	maxBookLines    = 4_000_000
	maxTableCells   = 400_000
	maxPDFTableCols = 24
	maxNesting      = 12
	// maxNoteBytes bounds the footnote text a layout sets: the structure already
	// bounds the copies of a repeated note, and this bounds what they cost in
	// memory once each is broken into lines.
	maxNoteBytes = 4 << 20
)

func newTypesetter(b *Book, o Options, ts *typeface, pr params) *typesetter {
	t := &typesetter{o: o, pr: pr, ts: ts, b: b, images: map[string]*imgPlace{}, budget: &textBudget{cells: maxTableCells, lines: maxBookLines}}
	t.size = pr.size
	reg := ts.body[font.Regular].f
	ls := o.LineSpacing
	if ls <= 0 {
		ls = 1.1
	}
	nat := ts.natural // thousandths of the size
	t.lead = pr.size * nat * mpt(math.Round(ls*1000)) / 1_000_000
	if t.lead < pr.size { // never less than solid
		t.lead = pr.size
	}
	t.capH = reg.Scale(reg.CapHeight, t.size)
	t.ascH = reg.Scale(reg.Ascent, t.size)
	t.descH = reg.Scale(reg.Descent, t.size)
	return t
}

// baseFor is the baseline's distance from the top of a box of height h holding
// a line at size: the face's ascent and descent are centred in it.
func (t *typesetter) baseFor(size, h mpt) mpt {
	reg := t.ts.body[font.Regular].f
	asc, desc := reg.Scale(reg.Ascent, size), reg.Scale(reg.Descent, size)
	return (h-(asc+desc))/2 + asc
}

// lines is how many body leadings a height takes, rounded up.
func (t *typesetter) lines(h mpt) mpt {
	n := (h + t.lead - 1) / t.lead
	if n < 1 {
		n = 1
	}
	return n * t.lead
}

// noteLead is the leading of footnote text.
func (t *typesetter) noteSize() mpt { return t.size * 85 / 100 }
func (t *typesetter) noteLead() mpt { return t.lead * 85 / 100 }

// ---- paragraphs ------------------------------------------------------------------------------

type paraOpt struct {
	style       style
	size        mpt
	lead        mpt // line height; 0: the body leading
	x, w        mpt // left edge and measure
	indentFirst bool
	align       align
	space       mpt // space above the first line
	drop        bool
	label       *para // a list label for the first line
	labelX      mpt
	keepAll     bool // the paragraph does not break across pages (headings)
	keepNext    bool
	keepPrev    bool
	ragged      bool
	noNotes     bool
	// fitWords shrinks the type until the widest word fits the measure: a title
	// is never broken in the middle of a word.
	fitWords bool
}

// fitWordSize is the size at which the widest word of txt is no wider than w
// (width is proportional to size), a little under it; size when it already fits.
func (t *typesetter) fitWordSize(txt string, st style, size, w mpt) mpt {
	var widest mpt
	for _, word := range strings.Fields(txt) {
		if x := t.ts.width(word, st, size); x > widest {
			widest = x
		}
	}
	if widest <= w || widest == 0 || w <= 0 {
		return size
	}
	return max(size*w/widest*97/100, 2*pt)
}

// paragraph sets a paragraph and returns its lines and any images found
// inside it (to be placed after it).
func (t *typesetter) paragraph(in []text.Inline, o paraOpt) ([]*vitem, []text.Inline) {
	size := o.size
	if size == 0 {
		size = t.size
	}
	lead := o.lead
	if lead == 0 {
		lead = t.lead
	}
	if o.fitWords {
		if ns := t.fitWordSize(text.InlinePlain(in), o.style, size, o.w*88/100); ns < size {
			lead = lead * ns / size
			size = ns
		}
	}
	justify := o.align == alignJustify
	var dc *dropCap
	if o.drop {
		orig := in
		in, dc = t.takeDropCap(in)
		// an initial that would take more than a third of the measure is not worth setting
		if dc != nil && dc.width > o.w/3 {
			in, dc = orig, nil
		}
	}
	maxChunk := o.w * 9 / 10
	if dc != nil {
		maxChunk = (o.w - dc.width) * 9 / 10
	}
	p, imgs := buildPara(t.ts, in, o.style, size, justify, maxChunk, Set(t.o.LinkURLs))
	if p.empty() {
		return nil, imgs
	}
	build := func(dc *dropCap) []lineRec {
		indent := mpt(0)
		if o.indentFirst && dc == nil {
			indent = t.pr.indent
		}
		lf := func(i int) (mpt, mpt) {
			switch {
			case dc != nil && i < dc.lines:
				return dc.width, o.w - dc.width
			case i == 0:
				return indent, o.w - indent
			}
			return 0, o.w
		}
		dn := 0
		if dc != nil {
			dn = dc.lines
		}
		lines, loose, overfull := breakParagraph(p.items, lf, dn, justify)
		t.loose += loose
		t.overfull += overfull
		return lines
	}
	lines := build(dc)
	if dc != nil && len(lines) < dc.lines {
		// a paragraph shorter than the initial is not worth one
		dc = nil
		lines = build(nil)
	}
	if len(lines) == 0 {
		return nil, imgs
	}
	t.budget.lines -= len(lines)
	p.lines = lines
	p.dropCap = dc
	out := make([]*vitem, len(lines))
	base := t.baseFor(size, lead)
	for k := range lines {
		ln := lines[k]
		al := o.align
		if al == alignJustify && (ln.last || ln.forced) {
			al = alignLeft
		}
		it := &vitem{kind: vLine, h: lead, base: base, par: p, ln: ln, x: o.x, w: o.w, align: al, first: k == 0}
		if k == 0 {
			it.space = o.space
			it.lead, it.leadX = o.label, o.labelX
			it.keepPrev = o.keepPrev
		}
		if !o.noNotes {
			it.notes = t.notesIn(p, ln)
		}
		out[k] = it
	}
	n := len(out)
	switch {
	case o.keepAll:
		for k := 0; k < n; k++ {
			out[k].keepNext = true
		}
	default:
		if n >= 2 {
			out[0].keepNext = true // an orphan: one line alone at the foot of a page
		}
		if n >= 3 {
			out[n-2].keepNext = true // a widow: one line alone at the head of a page
		}
		if dc != nil {
			for k := 0; k < dc.lines-1 && k < n; k++ {
				out[k].keepNext = true
			}
		}
	}
	if o.keepNext {
		out[n-1].keepNext = true
	}
	return out, imgs
}

// takeDropCap cuts the first letter of a paragraph out of its text and returns
// the initial to draw in its place; it leaves the text alone when the
// paragraph does not begin with a letter.
func (t *typesetter) takeDropCap(in []text.Inline) ([]text.Inline, *dropCap) {
	r, ok := firstLetter(in)
	if !ok {
		return in, nil
	}
	face, gid, found := t.ts.pick(r, 0)
	if !found || face != t.ts.body[font.Regular] {
		return in, nil
	}
	cut, done := cutFirst(in, r)
	if !done {
		return in, nil
	}
	// the initial's cap height spans the first line's cap height and the two
	// lines below it
	want := 2*t.lead + t.capH
	f := face.f
	size := want * mpt(f.UnitsPerEm) / mpt(f.CapHeight)
	adv := f.Scale(f.Advance(gid), size)
	gap := t.size * 3 / 10
	run := glyphRun{face: face, size: size, gids: []uint16{gid}, runes: []rune{r}, adv: []int32{int32(adv)}, kern: []int32{0}, width: adv}
	return cut, &dropCap{run: run, width: adv + gap, lines: 3}
}

// cutFirst returns the inlines without the first rune r.
func cutFirst(in []text.Inline, r rune) ([]text.Inline, bool) {
	out := make([]text.Inline, len(in))
	copy(out, in)
	for i, n := range out {
		switch n.Kind {
		case text.IText:
			trimmed := strings.TrimLeftFunc(n.Text, unicode.IsSpace)
			if trimmed == "" {
				continue
			}
			first, size := []rune(trimmed)[0], len(string([]rune(trimmed)[0]))
			if first != r {
				return in, false
			}
			out[i].Text = trimmed[size:]
			return out, true
		case text.IEmph, text.IStrong, text.IStrike:
			kids, ok := cutFirst(n.Kids, r)
			if !ok {
				return in, false
			}
			out[i].Kids = kids
			return out, true
		default:
			return in, false
		}
	}
	return in, false
}

// notesIn finds the footnotes referenced on a line.
func (t *typesetter) notesIn(p *para, ln lineRec) []*note {
	if t.pr.notes != "page" || len(t.notes) == 0 {
		return nil
	}
	var out []*note
	for i := ln.from; i < ln.to && int(i) < len(p.items); i++ {
		if n := int(p.items[i].note); n > 0 {
			if nt := t.noteFor(n); nt != nil {
				out = append(out, nt)
			}
		}
	}
	return out
}

// noteFor sets the text of a footnote, once.
func (t *typesetter) noteFor(num int) *note {
	if n, ok := t.noteBuf[num]; ok {
		return n
	}
	fn, ok := t.notes[num]
	if !ok {
		return nil
	}
	size, lead := t.noteSize(), t.noteLead()
	n := &note{num: num}
	var all []*vitem
	cost := 16
	for _, node := range fn.Nodes {
		cost += len(nodePlain(node))
	}
	if t.noteBytes += cost; t.noteBytes > maxNoteBytes {
		t.noteCut++
		lines, _ := t.paragraph([]text.Inline{{Kind: text.INote, Num: num}, {Kind: text.IText, Text: " [note not set: the book's notes are over the layout's limit]"}}, paraOpt{size: size, lead: lead, w: t.pr.textW, noNotes: true})
		for _, l := range lines {
			n.h += l.h
		}
		n.lines = lines
		if t.noteBuf == nil {
			t.noteBuf = map[int]*note{}
		}
		t.noteBuf[num] = n
		return n
	}
	for k, node := range fn.Nodes {
		var in []text.Inline
		switch node.Kind {
		case text.NPara:
			in = node.Inl
		default:
			in = []text.Inline{{Kind: text.IText, Text: nodePlain(node)}}
		}
		if k == 0 {
			in = append([]text.Inline{{Kind: text.INote, Num: num}, {Kind: text.IText, Text: " "}}, in...)
		}
		lines, _ := t.paragraph(in, paraOpt{size: size, lead: lead, x: 0, w: t.pr.textW, align: alignJustify, noNotes: true, indentFirst: k > 0})
		for _, l := range lines {
			l.keepNext = false
			l.keepPrev = false
		}
		all = append(all, lines...)
	}
	if len(all) == 0 {
		// A note with no text still has its number.
		lines, _ := t.paragraph([]text.Inline{{Kind: text.INote, Num: num}}, paraOpt{size: size, lead: lead, w: t.pr.textW, noNotes: true})
		all = lines
	}
	for _, l := range all {
		n.h += l.h
	}
	n.lines = all
	if t.noteBuf == nil {
		t.noteBuf = map[int]*note{}
	}
	t.noteBuf[num] = n
	return n
}

// nodePlain is the text of a block that a footnote shows as a plain paragraph.
func nodePlain(n text.Node) string {
	switch n.Kind {
	case text.NCode, text.NSource:
		return n.Text
	case text.NHeading:
		return text.InlinePlain(n.Inl)
	case text.NQuote:
		var s []string
		for _, k := range n.Kids {
			s = append(s, nodePlain(k))
		}
		return strings.Join(s, " ")
	case text.NList:
		var s []string
		for _, it := range n.Items {
			for _, k := range it {
				s = append(s, nodePlain(k))
			}
		}
		return strings.Join(s, " ")
	case text.NTable:
		var s []string
		for _, r := range n.Rows {
			for _, c := range r.Cells {
				s = append(s, text.InlinePlain(c.Inl))
			}
		}
		return strings.Join(s, " ")
	case text.NPara:
		return text.InlinePlain(n.Inl)
	}
	return ""
}

// ---- blocks ----------------------------------------------------------------------------------

type flowCtx struct {
	x, w   mpt
	quote  int
	inList bool
	first  bool // the next paragraph is the first of its text: no indent, maybe a drop cap
	drop   bool // that paragraph gets a drop cap
	depth  int
}

// blocks sets a node list. It returns boxes for the flow.
func (t *typesetter) blocks(nodes []text.Node, c *flowCtx) []*vitem {
	var out []*vitem
	c.depth++
	defer func() { c.depth-- }()
	if c.depth > maxNesting {
		t.deepNest++
		// Deeper than any book needs: set it as the plain text it holds.
		for _, n := range nodes {
			if s := strings.TrimSpace(nodePlain(n)); s != "" {
				ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: s}}, paraOpt{x: c.x, w: c.w, align: alignLeft})
				out = append(out, ls...)
			}
		}
		return out
	}
	afterBlock := c.first // no indent after a heading, a break, a quote...
	for _, n := range nodes {
		if t.budget.lines <= 0 {
			break
		}
		switch n.Kind {
		case text.NPara:
			al := alignLeft
			if t.pr.justify {
				al = alignJustify
			}
			space := t.pr.spaceAfter
			ls, imgs := t.paragraph(n.Inl, paraOpt{style: 0, x: c.x, w: c.w, align: al, indentFirst: !afterBlock && c.quote == 0, space: space, drop: c.drop && afterBlock})
			if c.drop && len(ls) > 0 {
				c.drop = false
			}
			out = append(out, ls...)
			if len(ls) > 0 {
				afterBlock = false
			}
			for _, im := range imgs {
				out = append(out, t.image(im, c)...)
				afterBlock = true
			}
		case text.NHeading:
			out = append(out, t.heading(n, c)...)
			afterBlock = true
			c.drop = false
		case text.NRule:
			out = append(out, t.sceneBreak(c)...)
			afterBlock = true
			c.drop = false
		case text.NQuote:
			qc := &flowCtx{x: c.x + t.quoteIndent(), w: c.w - 2*t.quoteIndent(), quote: c.quote + 1, first: true, depth: c.depth}
			if qc.w < t.size*8 {
				qc.x, qc.w = c.x, c.w
			}
			ls := t.blocks(n.Kids, qc)
			if len(ls) > 0 {
				ls[0].space += t.lead / 2
				ls[len(ls)-1].space += 0
				// half a line above and below keeps the grid after the quote
				out = append(out, ls...)
				out = append(out, &vitem{kind: vLine, h: t.lead / 2, base: 0, keepPrev: true})
				out[len(out)-1].par = nil
			}
			afterBlock = true
			c.drop = false
		case text.NList:
			out = append(out, t.list(n, c)...)
			afterBlock = true
			c.drop = false
		case text.NCode, text.NSource:
			out = append(out, t.code(n, c)...)
			afterBlock = true
			c.drop = false
		case text.NTable:
			out = append(out, t.table(n, c)...)
			afterBlock = true
			c.drop = false
		}
	}
	return out
}

func (t *typesetter) quoteIndent() mpt { return t.size * 2 }

func (t *typesetter) heading(n text.Node, c *flowCtx) []*vitem {
	level := n.Level
	size := t.size
	st := stBold
	switch level {
	case 1:
		size = t.size * 140 / 100
	case 2:
		size = t.size * 120 / 100
	case 3:
		size = t.size * 108 / 100
	default:
		st = stBold | stItalic
		if level >= 5 {
			st = stItalic
		}
	}
	lead := t.lines(size * 118 / 100)
	ls, _ := t.paragraph(n.Inl, paraOpt{style: st, size: size, lead: lead, x: c.x, w: c.w, align: alignLeft, space: 2 * t.lead, keepAll: true, keepNext: true, ragged: true, fitWords: true})
	return ls
}

// ornamentLine sets text centred in a box of the given number of leadings.
func (t *typesetter) ornamentLine(txt string, size mpt, h mpt, c *flowCtx) *vitem {
	runs := t.ts.shape(cleanText(txt), 0, size, 0)
	p := &para{runs: runs}
	var w mpt
	for _, r := range runs {
		w += r.width
	}
	p.items = []item{{kind: iBox, w: w, r0: 0, r1: int32(len(runs))}}
	ln := lineRec{from: 0, to: 1, nat: w, width: c.w, last: true}
	p.lines = []lineRec{ln}
	return &vitem{kind: vLine, h: h, base: t.baseFor(size, t.lead) + (h-t.lead)/2, par: p, ln: ln, x: c.x, w: c.w, align: alignCenter}
}

func (t *typesetter) sceneBreak(c *flowCtx) []*vitem {
	it := t.ornamentLine(SceneBreakText(t.o), t.size, 2*t.lead, c)
	it.keepPrev, it.keepNext = true, true
	return []*vitem{it}
}

func (t *typesetter) list(n text.Node, c *flowCtx) []*vitem {
	var out []*vitem
	hang := t.size * 17 / 10
	labW := hang - t.size*3/10
	num := n.Start
	if num < 1 {
		num = 1
	}
	for _, it := range n.Items {
		label := "•"
		if n.Ordered {
			label = itoa(num) + "."
			num++
		}
		lp := t.labelPara(label)
		ic := &flowCtx{x: c.x + hang, w: c.w - hang, first: true, depth: c.depth, inList: true}
		if ic.w < t.size*8 {
			ic.x, ic.w = c.x, c.w
		}
		first := true
		for _, k := range it {
			var ls []*vitem
			switch k.Kind {
			case text.NPara:
				opt := paraOpt{x: ic.x, w: ic.w, align: alignLeft, space: t.pr.spaceAfter}
				if t.pr.justify {
					opt.align = alignJustify
				}
				if first {
					opt.label, opt.labelX = lp, c.x+labW
				}
				var imgs []text.Inline
				ls, imgs = t.paragraph(k.Inl, opt)
				for _, im := range imgs {
					ls = append(ls, t.image(im, ic)...)
				}
			default:
				ls = t.blocks([]text.Node{k}, ic)
			}
			if len(ls) > 0 {
				first = false
			}
			out = append(out, ls...)
		}
	}
	if len(out) > 0 && !c.inList {
		// half a line above and below, so a list stands off from the text
		out[0].space += t.lead / 2
		out = append(out, &vitem{kind: vLine, h: t.lead / 2, keepPrev: true})
	}
	return out
}

// labelPara is the bullet or number of a list item, as a one-box paragraph.
func (t *typesetter) labelPara(label string) *para {
	runs := t.ts.shape(cleanText(label), 0, t.size, 0)
	var w mpt
	for _, r := range runs {
		w += r.width
	}
	return &para{runs: runs, items: []item{{kind: iBox, w: w, r0: 0, r1: int32(len(runs))}}}
}

func (t *typesetter) code(n text.Node, c *flowCtx) []*vitem {
	size := t.size * 85 / 100
	x, w := c.x+t.size, c.w-t.size
	if w < t.size*8 {
		x, w = c.x, c.w
	}
	var out []*vitem
	for k, line := range strings.Split(strings.TrimRight(n.Text, "\n"), "\n") {
		if t.budget.lines <= 0 {
			break
		}
		line = strings.ReplaceAll(line, "\t", "    ")
		if strings.TrimSpace(line) == "" {
			out = append(out, &vitem{kind: vLine, h: t.lead, space: 0})
			continue
		}
		ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: line}}, paraOpt{style: stMono, size: size, x: x, w: w, align: alignLeft, ragged: true, noNotes: true, space: 0})
		_ = k
		out = append(out, ls...)
	}
	if len(out) > 0 {
		out[0].space += t.lead / 2
		out = append(out, &vitem{kind: vLine, h: t.lead / 2, keepPrev: true})
		// a code block may break across pages anywhere but after its first or before its last line
		if len(out) >= 3 {
			out[0].keepNext = true
			out[len(out)-3].keepNext = true
		}
	}
	return out
}

// ---- images ------------------------------------------------------------------------------------

type imgPlace struct {
	key  string
	img  Image
	x, w mpt
	h    mpt
	dpi  int
	ok   bool
}

// image places a picture in the flow as a figure on its own, centred, at the
// width of the text unless it is small (then at 96 pixels to the inch, as the
// DOCX does), and never taller than most of a page.
func (t *typesetter) image(n text.Inline, c *flowCtx) []*vitem {
	img, ok := t.b.Images[n.Text]
	if !ok || img.Width <= 0 || img.Height <= 0 || !embeddable(img.Ext) {
		label := "[image"
		if a := strings.TrimSpace(n.Alt); a != "" {
			label += ": " + a
		}
		label += " (not included)]"
		if !ok || img.Width <= 0 {
			t.missingImage++
		}
		ls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: label}}, paraOpt{style: stItalic, x: c.x, w: c.w, align: alignCenter, space: t.lead / 2})
		return ls
	}
	pw, ph := img.upright()
	natW := mpt(pw) * 750 // 96 pixels to the inch: a pixel is 0.75 point
	w := c.w
	if natW < w {
		w = natW
	}
	h := w * mpt(ph) / mpt(pw)
	maxH := t.pr.textH * 75 / 100
	if h > maxH {
		h = maxH
		w = h * mpt(pw) / mpt(ph)
	}
	dpi := int(int64(pw) * 72000 / int64(w))
	if dpi < 300 {
		t.lowRes++
		if t.minDPI == 0 || dpi < t.minDPI {
			t.minDPI = dpi
		}
	}
	place := &imgPlace{key: n.Text, img: img, x: c.x + (c.w-w)/2, w: w, h: h, dpi: dpi, ok: true}
	t.images[n.Text] = place
	// the box is a whole number of lines tall, with a line of air above and below
	box := t.lines(h)
	return []*vitem{{kind: vImage, h: box, space: t.lead, img: place, keepPrev: false}, {kind: vLine, h: t.lead, keepPrev: true}}
}

func embeddable(ext string) bool {
	switch ext {
	case "png", "jpg", "gif":
		return true
	}
	return false
}

// ---- tables -------------------------------------------------------------------------------------

type cellLines struct {
	par   *para
	lines []lineRec
	align align
}

type rowSlice struct {
	x      mpt
	cols   []mpt // column x offsets from the table's left edge, len = ncols+1
	cells  []cellLines
	from   int
	to     int // lines [from, to) of every cell
	head   bool
	last   bool // the table's last slice: a closing rule
	first  bool
	pad    mpt
	cellLd mpt
	size   mpt
	// headRef is the table's header row, repeated when the table continues on
	// another page.
	headRef *vitem
	isHead  bool
}

func (t *typesetter) table(n text.Node, c *flowCtx) []*vitem {
	ncols := 0
	for _, r := range n.Rows {
		ncols = max(ncols, len(r.Cells))
	}
	if ncols == 0 || len(n.Rows) == 0 {
		return nil
	}
	size := t.size * 90 / 100
	cellLd := t.lead * 90 / 100
	pad := t.size * 3 / 10
	if ncols > maxPDFTableCols {
		t.longTable++
	}
	colsUsed := min(ncols, maxPDFTableCols)
	// natural and minimum widths from the first rows
	nat := make([]mpt, colsUsed)
	minW := make([]mpt, colsUsed)
	for ri, r := range n.Rows {
		if ri >= 400 {
			break
		}
		for ci := 0; ci < len(r.Cells) && ci < colsUsed; ci++ {
			s := text.InlinePlain(r.Cells[ci].Inl)
			st := style(0)
			if r.Head {
				st = stBold
			}
			if w := t.ts.width(s, st, size); w > nat[ci] {
				nat[ci] = w
			}
			for _, wd := range strings.Fields(s) {
				if w := t.ts.width(wd, st, size); w > minW[ci] {
					minW[ci] = w
				}
			}
		}
	}
	total := c.w
	avail := total - mpt(colsUsed)*2*pad
	widths := make([]mpt, colsUsed)
	var sumNat, sumMin mpt
	for i := range widths {
		if nat[i] < size*2 {
			nat[i] = size * 2
		}
		if minW[i] > nat[i] {
			minW[i] = nat[i]
		}
		sumNat += nat[i]
		sumMin += minW[i]
	}
	switch {
	case sumNat <= avail:
		extra := avail - sumNat
		for i := range widths {
			widths[i] = nat[i] + extra*nat[i]/sumNat
		}
	case sumMin >= avail:
		for i := range widths {
			widths[i] = avail / mpt(colsUsed)
		}
	default:
		spare := avail - sumMin
		var room mpt
		for i := range widths {
			room += nat[i] - minW[i]
		}
		for i := range widths {
			widths[i] = minW[i] + spare*(nat[i]-minW[i])/room
		}
	}
	cols := make([]mpt, colsUsed+1)
	for i := range widths {
		cols[i+1] = cols[i] + widths[i] + 2*pad
	}
	maxLines := int(t.pr.textH/2/cellLd) - 1
	if maxLines < 3 {
		maxLines = 3
	}
	var out []*vitem
	var head *vitem
	for ri, r := range n.Rows {
		if t.budget.cells <= 0 {
			t.tableCapped = true
			break
		}
		cells := make([]cellLines, colsUsed)
		rowLines := 1
		for ci := 0; ci < colsUsed; ci++ {
			var in []text.Inline
			al := alignLeft
			if ci < len(r.Cells) {
				in = r.Cells[ci].Inl
				switch r.Cells[ci].Align {
				case "right":
					al = alignRight
				case "center":
					al = alignCenter
				}
			}
			t.budget.cells--
			st := style(0)
			if r.Head {
				st = stBold
			}
			cw := widths[ci]
			p, imgs := buildPara(t.ts, in, st, size, false, cw*9/10, false)
			t.tableImages += len(imgs)
			var lines []lineRec
			if !p.empty() {
				lines, _, _ = breakParagraph(p.items, func(int) (mpt, mpt) { return 0, cw }, 0, false)
			}
			t.budget.lines -= len(lines)
			cells[ci] = cellLines{par: p, lines: lines, align: al}
			rowLines = max(rowLines, len(lines))
		}
		// slices of at most maxLines lines
		for from := 0; from < rowLines || from == 0; from += maxLines {
			to := min(from+maxLines, rowLines)
			rs := &rowSlice{x: c.x, cols: cols, cells: cells, from: from, to: to, head: r.Head, pad: pad, cellLd: cellLd, size: size, first: ri == 0 && from == 0,
				last: ri == len(n.Rows)-1 && to == rowLines}
			it := &vitem{kind: vRow, h: mpt(to-from)*cellLd + 2*pad, row: rs, x: c.x, w: total}
			if ri == 0 && from == 0 {
				it.space = t.lead / 2
			}
			if r.Head {
				it.keepNext = true
				head = it
				rs.isHead = true
			}
			if head != nil && !r.Head {
				rs.headRef = head
			}
			for _, cl := range cells {
				for k := from; k < to && k < len(cl.lines); k++ {
					it.notes = append(it.notes, t.notesIn(cl.par, cl.lines[k])...)
				}
			}
			out = append(out, it)
			if to >= rowLines {
				break
			}
		}
	}
	if len(out) > 0 {
		out = append(out, &vitem{kind: vLine, h: t.lead / 2, keepPrev: true})
		if len(out) > 2 && !out[0].keepNext {
			out[0].keepNext = true // a table is not begun alone at the foot of a page
		}
	}
	return out
}

// widthWarning formats the warnings the typesetter collected.
func (t *typesetter) warnings() []Warn {
	var out []Warn
	if t.loose > 0 {
		out = append(out, Warn{"loose_lines", fmt.Sprintf("%d line(s) are loosely spaced because no better break was found; a shorter paragraph, a wider page or turning justification off would help.", t.loose)})
	}
	if t.overfull > 0 {
		out = append(out, Warn{"overfull_line", fmt.Sprintf("%d line(s) are wider than the text block (a word or a code line that cannot be broken).", t.overfull)})
	}
	if t.lowRes > 0 {
		out = append(out, Warn{"image_low_resolution", fmt.Sprintf("%d image(s) print below 300 dpi at the size they are set (the lowest is %d dpi). Printers ask for 300 dpi or more.", t.lowRes, t.minDPI)})
	}
	if t.longTable > 0 {
		out = append(out, Warn{"table_columns", fmt.Sprintf("%d table(s) have more than %d columns; the columns beyond are not set.", t.longTable, maxPDFTableCols)})
	}
	if t.tableCapped || t.budget.lines <= 0 {
		out = append(out, Warn{"layout_limit", "The book is larger than the layout will set, and the export will be refused: the text would be cut off at its limit. Split the book and compile a part at a time."})
	}
	if t.noteCut > 0 {
		out = append(out, Warn{"layout_limit", fmt.Sprintf("%d footnote(s) were not set because the book's footnote text is over the layout's limit of %d MB, and the export will be refused.", t.noteCut, maxNoteBytes>>20)})
	}
	if t.tableImages > 0 {
		out = append(out, Warn{"table_images", fmt.Sprintf("%d image(s) inside table cells are not set; put pictures between paragraphs.", t.tableImages)})
	}
	if t.deepNest > 0 {
		out = append(out, Warn{"nesting_flattened", fmt.Sprintf("%d block(s) were nested more than %d deep and are set as plain text.", t.deepNest, maxNesting)})
	}
	return out
}

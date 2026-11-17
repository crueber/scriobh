package publish

import "context"

// The vertical flow: a book's text, after its paragraphs are broken into
// lines, is a list of boxes (a line, an image, a slice of a table row) with
// space between them and rules about where a page may end. fill pours it into
// pages.

type vkind uint8

const (
	vLine  vkind = iota // a line of text
	vImage              // a picture
	vRow                // a slice of a table row
	vBreak              // end the page here (recto: and start on a right-hand page)
	vTOC                // a line of the contents
)

type align uint8

const (
	alignLeft align = iota
	alignJustify
	alignCenter
	alignRight
)

// vitem is one box of the flow.
type vitem struct {
	kind  vkind
	space mpt // space above, dropped at the top of a page unless keepTop
	h     mpt // height of the box
	base  mpt // text: the baseline's distance from the top of the box

	keepNext bool // a page may not end after this box
	keepPrev bool // a page may not end before this box
	keepTop  bool // keep the space above even at the top of a page
	recto    bool // vBreak: the next page is a right-hand page
	opener   bool // the first box of a chapter opener: the page has no running head

	// text
	par   *para
	ln    lineRec
	x, w  mpt // left edge and measure, from the left edge of the text block
	align align
	first bool // the first line of its paragraph (draws the drop cap)
	lead  *para
	leadX mpt // a list label, drawn right-aligned to this x

	// notes whose references are on this line (footnotes at the foot of the page)
	notes []*note

	img *imgPlace
	row *rowSlice
	toc *tocLine

	// sec is 1 + the index of the section this box opens, else 0.
	sec int
}

// note is a footnote as set at the foot of a page.
type note struct {
	num   int
	lines []*vitem
	h     mpt
}

// placed is a box on a page, at y from the top of the text block.
type placed struct {
	it  *vitem
	y   mpt
	idx int // position in the flow
	// nl are the lines of its footnotes set on this page (all of them, or the
	// first of a note that goes on to the next page).
	nl []*vitem
	// deferN is how many lines of its notes went to the carry (the rest of a
	// split note, or the notes of a box placed after one).
	deferN int
}

type pageKind uint8

const (
	pageBody pageKind = iota
	pageBlank
	pageTitle
	pageCopyright
	pageDedication
	pageEpigraph
)

// sheet is a laid-out page.
type sheet struct {
	kind  pageKind
	items []placed
	// noHead: no running head (a chapter opener, a blank page, a title page).
	noHead bool
	// noFolio: no page number.
	noFolio bool
	// carried lines of a footnote that began on an earlier page.
	carried []*vitem
	noteH   mpt
	// starts: sections that begin on this page.
	starts []pageStart
	// direct is how a title page and the like are drawn.
	direct func(r *renderer, p *sheet, idx int)
}

type pageStart struct {
	sec int
	y   mpt
}

// noteLines is what is set at the foot of a page, top to bottom.
func (pg *sheet) noteLines() []*vitem {
	out := append([]*vitem(nil), pg.carried...)
	for _, p := range pg.items {
		out = append(out, p.nl...)
	}
	return out
}

// filler pours boxes into pages.
type filler struct {
	pr      *params
	pages   *[]*sheet
	sepH    mpt // the footnote rule and the space around it
	leading mpt

	cur       *sheet
	y         mpt
	start     int
	pendRecto bool
	carry     []*vitem // lines of a footnote waiting for the next page
	// splitPage: the note area of the current page is full of a note that runs
	// on, so the notes of boxes placed after it wait for the next page too.
	splitPage bool
	unfixed   int // page ends that broke a keep rule
	overflow  bool
	secFirst  map[int]int // section -> index in *pages of the page it starts on
	ctx       context.Context
}

const maxBookPages = 20_000

func (f *filler) newPage(kind pageKind) *sheet {
	pg := &sheet{kind: kind}
	*f.pages = append(*f.pages, pg)
	return pg
}

func (f *filler) blank() {
	pg := f.newPage(pageBlank)
	pg.noHead, pg.noFolio = true, true
}

func (f *filler) begin() {
	// A recto request on a page that would be left-hand inserts a blank page.
	if f.pendRecto && len(*f.pages)%2 == 1 {
		f.blank()
	}
	f.pendRecto = false
	f.cur = f.newPage(pageBody)
	f.y = 0
	if len(f.carry) > 0 {
		f.takeCarry()
	}
	f.splitPage = len(f.carry) > 0
	if len(*f.pages) > maxBookPages {
		f.overflow = true
	}
}

// takeCarry puts what is left of a footnote at the top of the note area of the
// new page, leaving room for some text above it.
func (f *filler) takeCarry() {
	limit := f.pr.textH - 4*f.leading - f.sepH
	var h mpt
	n := 0
	for n < len(f.carry) && h+f.carry[n].h <= limit {
		h += f.carry[n].h
		n++
	}
	if n == 0 { // one line taller than the limit: it goes anyway
		n, h = 1, f.carry[0].h
	}
	f.cur.carried = append(f.cur.carried, f.carry[:n]...)
	f.cur.noteH = f.sepH + h
	f.carry = f.carry[n:]
}

func (f *filler) endPage() {
	f.cur = nil
	f.y = 0
}

// noteAdd is the height a box's notes add to the page's note area.
func (f *filler) noteAdd(it *vitem) mpt {
	if len(it.notes) == 0 || f.splitPage {
		return 0
	}
	var h mpt
	for _, n := range it.notes {
		h += n.h
	}
	if f.cur.noteH == 0 {
		h += f.sepH
	}
	return h
}

func (f *filler) markStart(it *vitem, y mpt) {
	if it.sec > 0 {
		f.cur.starts = append(f.cur.starts, pageStart{sec: it.sec - 1, y: y})
		if f.secFirst != nil {
			if _, ok := f.secFirst[it.sec-1]; !ok {
				f.secFirst[it.sec-1] = len(*f.pages) - 1
			}
		}
	}
	if it.opener {
		f.cur.noHead = true
	}
}

func (f *filler) place(i int, it *vitem, space mpt) {
	var nl []*vitem
	for _, n := range it.notes {
		nl = append(nl, n.lines...)
	}
	if f.splitPage {
		f.carry = append(f.carry, nl...)
		f.placeWith(i, it, space, nil, len(nl))
		return
	}
	f.placeWith(i, it, space, nl, 0)
}

// placeWith puts a box on the page with these lines of its footnotes.
func (f *filler) placeWith(i int, it *vitem, space mpt, nl []*vitem, deferN int) {
	var h mpt
	for _, l := range nl {
		h += l.h
	}
	if h > 0 && f.cur.noteH == 0 {
		h += f.sepH
	}
	f.cur.noteH += h
	f.cur.items = append(f.cur.items, placed{it: it, y: f.y + space, idx: i, nl: nl, deferN: deferN})
	f.markStart(it, f.y+space)
	f.y += space + it.h
}

// splitNotes divides the lines of a box's footnotes into those that fit in room
// and the rest. A note is not split unless two lines of it fit on this page and
// at least two go to the next (no lone first or last line of a note); ok is
// false when no such division exists.
func splitNotes(it *vitem, room mpt) (fit, rest []*vitem, ok bool) {
	type nline struct {
		l    *vitem
		note int
	}
	var all []nline
	for ni, n := range it.notes {
		for _, l := range n.lines {
			all = append(all, nline{l, ni})
		}
	}
	var h mpt
	k := 0
	for k < len(all) && h+all[k].l.h <= room {
		h += all[k].l.h
		k++
	}
	if k == 0 || k == len(all) {
		return nil, nil, false
	}
	// do not leave a single line of a note for the next page
	if k < len(all) && all[k].note == all[k-1].note && (k+1 == len(all) || all[k+1].note != all[k].note) {
		k--
	}
	// nor put a single first line of a note on this one
	if k >= 1 && (k == 1 || all[k-2].note != all[k-1].note) && k < len(all) && all[k].note == all[k-1].note {
		k--
	}
	if k == 0 {
		return nil, nil, false
	}
	for _, x := range all[:k] {
		fit = append(fit, x.l)
	}
	for _, x := range all[k:] {
		rest = append(rest, x.l)
	}
	return fit, rest, true
}

// recomputeNotes rebuilds the note area's height from what is on the page.
func (f *filler) recomputeNotes() {
	var h mpt
	for _, l := range f.cur.carried {
		h += l.h
	}
	for _, p := range f.cur.items {
		for _, l := range p.nl {
			h += l.h
		}
	}
	if h > 0 {
		h += f.sepH
	}
	f.cur.noteH = h
}

// chainHeight is the height of the boxes that may not be parted from box i (up
// to limit).
func chainHeight(items []*vitem, i int, limit mpt) mpt {
	var h mpt
	for j := i + 1; j < len(items) && items[j-1].keepNext && items[j].kind != vBreak; j++ {
		h += items[j].space + items[j].h
		if h >= limit {
			return limit
		}
	}
	return h
}

func legalBreak(items []*vitem, j int) bool {
	if j <= 0 || j >= len(items) {
		return true
	}
	a, b := items[j-1], items[j]
	if a.kind == vBreak || b.kind == vBreak {
		return true
	}
	return !a.keepNext && !b.keepPrev
}

// fill pours the flow into pages, ending the last page.
func (f *filler) fill(items []*vitem) {
	i, steps := 0, 0
	for i < len(items) && !f.overflow {
		if steps++; steps&255 == 0 && f.ctx != nil && f.ctx.Err() != nil {
			return
		}
		it := items[i]
		if it.kind == vBreak {
			f.endPage()
			if it.recto {
				f.pendRecto = true
			}
			i++
			continue
		}
		if f.cur == nil {
			f.begin()
			f.start = i
		}
		space := it.space
		if len(f.cur.items) == 0 && !it.keepTop {
			space = 0
		}
		// A table that goes on over a page repeats its header row, when both fit.
		if len(f.cur.items) == 0 && it.kind == vRow && it.row.headRef != nil && f.cur.noteH == 0 {
			if h := it.row.headRef; h.h+space+it.h <= f.pr.textH {
				hc := *h
				hc.notes = nil // a header's notes were set where it first stood
				f.place(i, &hc, 0)
			}
		}
		// A box with footnotes is placed with them only if what must stay with it
		// fits too: otherwise the page would end by taking the box back again.
		var chain mpt
		if len(it.notes) > 0 && !f.splitPage {
			chain = chainHeight(items, i, f.pr.textH/3)
		}
		if f.y+space+it.h+chain+f.cur.noteH+f.noteAdd(it) <= f.pr.textH && (chain == 0 || f.y+space+it.h+f.cur.noteH+f.noteAdd(it) <= f.pr.textH) {
			f.place(i, it, space)
			i++
			continue
		}
		// The box fits but not all its footnotes: a footnote may run on to the next
		// page, so the box stays and the rest of the note is carried over.
		if len(it.notes) > 0 && !f.splitPage && f.y+space+it.h+f.cur.noteH <= f.pr.textH {
			// what must stay with the box (the second line of a paragraph, a
			// heading's next line) keeps its room
			room := f.pr.textH - (f.y + space + it.h) - f.cur.noteH - chain
			if f.cur.noteH == 0 {
				room -= f.sepH
			}
			if fit, rest, ok := splitNotes(it, room); ok {
				f.placeWith(i, it, space, fit, len(rest))
				f.carry = append(f.carry, rest...)
				f.splitPage = true
				i++
				continue
			}
		}
		if len(f.cur.items) == 0 {
			// Alone on a page and still too big (a very tall box, or notes with no
			// room for even two lines): it goes on the page anyway, and its notes
			// are carried to the next page.
			f.placeForced(i, it)
			f.endPage()
			i++
			continue
		}
		// End the page at the latest place the rules allow.
		j := i
		for j > f.start && !legalBreak(items, j) {
			j--
		}
		if j > f.start {
			k := len(f.cur.items)
			for k > 0 && f.cur.items[k-1].idx >= j {
				k--
			}
			f.takeBack(k)
			f.endPage()
			i = j
			continue
		}
		// No legal place on this page: end it here and note that a rule gave way.
		if !legalBreak(items, i) {
			f.unfixed++
		}
		f.endPage()
	}
}

// takeBack removes the boxes from index k of the current page, and what they
// started: the sections that open on them, a chapter opener's missing head.
func (f *filler) takeBack(k int) {
	pg := f.cur
	pageIdx := len(*f.pages) - 1
	for _, p := range pg.items[k:] {
		if p.it.sec > 0 {
			for n := len(pg.starts) - 1; n >= 0; n-- {
				if pg.starts[n].sec == p.it.sec-1 {
					pg.starts = append(pg.starts[:n], pg.starts[n+1:]...)
					break
				}
			}
			if f.secFirst != nil && f.secFirst[p.it.sec-1] == pageIdx {
				delete(f.secFirst, p.it.sec-1)
			}
		}
	}
	for n := len(pg.items) - 1; n >= k; n-- { // the notes that went to the carry go back with their boxes
		if d := pg.items[n].deferN; d > 0 {
			f.carry = f.carry[:len(f.carry)-d]
		}
	}
	f.splitPage = len(f.carry) > 0
	pg.items = pg.items[:k]
	pg.noHead = false
	for _, p := range pg.items {
		if p.it.opener {
			pg.noHead = true
		}
	}
	f.recomputeNotes()
}

// placeForced puts a box that cannot fit anywhere on an empty page, with what
// fits of its notes; the rest go to the next page.
func (f *filler) placeForced(i int, it *vitem) {
	var all []*vitem
	for _, n := range it.notes {
		all = append(all, n.lines...)
	}
	room := f.pr.textH - f.y - it.h - f.cur.noteH
	if f.cur.noteH == 0 {
		room -= f.sepH
	}
	var fit []*vitem
	var h mpt
	for k, l := range all {
		if l.h > room-h || len(f.carry) > 0 {
			f.carry = append(f.carry, all[k:]...)
			break
		}
		fit = append(fit, l)
		h += l.h
	}
	f.placeWith(i, it, 0, fit, len(all)-len(fit))
}

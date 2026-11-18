package publish

import (
	"context"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/publish/font"
	"github.com/crueber/scriobh/internal/publish/pdfw"
)

// renderer draws a layout into a PDF document.
type renderer struct {
	l     *PDFLayout
	d     *pdfw.Doc
	fonts map[*faceRT]*pdfw.Font
	imgs  *imageEmbedder
	warns []Warn
	// the media box and where the trim box sits in it
	mediaW, mediaH mpt
	off            mpt
	failedImages   int
	failedWhy      string
	placeholders   int
}

// geom is where the text block of a page is, in media-box coordinates measured
// from the top left.
type geom struct {
	x0, yTop mpt
	recto    bool
}

func (r *renderer) font(f *faceRT) *pdfw.Font {
	if pf, ok := r.fonts[f]; ok {
		return pf
	}
	pf := r.d.NewFont(f.name, f.f)
	r.fonts[f] = pf
	return pf
}

// uncompressedForTests is set by tests that read content streams.
var uncompressedForTests bool

// WritePDF lays the book out and writes it. The warnings are the layout's and
// the writer's together.
func WritePDF(b *Book, o Options) ([]byte, []Warn, error) {
	return WritePDFContext(context.Background(), b, o)
}

// WritePDFContext is WritePDF that stops when the context is cancelled.
func WritePDFContext(ctx context.Context, b *Book, o Options) ([]byte, []Warn, error) {
	l, err := LayoutPDFContext(ctx, b, o)
	if err != nil {
		return nil, nil, err
	}
	out, w, err := l.Render()
	return out, append(append([]Warn{}, l.Warnings...), w...), err
}

// Render writes the PDF. The warnings it returns are the ones found while
// writing (an image that could not be embedded); the layout's are in Warnings.
func (l *PDFLayout) Render() ([]byte, []Warn, error) {
	if len(l.truncated) > 0 {
		return nil, nil, fmt.Errorf("%w: %s", ErrTruncated, strings.Join(l.truncated, "; "))
	}
	pr, b := l.pr, l.b
	r := &renderer{l: l, d: pdfw.New(), fonts: map[*faceRT]*pdfw.Font{}}
	r.imgs = newImageEmbedder(r.d)
	r.mediaW, r.mediaH = pr.pageW, pr.pageH
	if pr.cropMarks {
		r.off = pr.bleed + pr.slug
		r.mediaW += 2 * r.off
		r.mediaH += 2 * r.off
	}
	d := r.d
	d.Uncompressed = uncompressedForTests
	d.Title, d.Author = CleanLine(b.Title), CleanLine(b.Byline)
	d.Language = firstNonEmpty(l.o.Language, b.Language, "en")
	if !validLang(d.Language) {
		d.Language = "en"
	}
	if t := b.Modified.UTC(); !b.Modified.IsZero() {
		d.Created = [6]int{t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second()}
	}
	d.ID = pdfw.DocumentID("scriobh-pdf", b.InputHash, b.Title, b.Identifier, fmt.Sprint(len(l.sheets)))
	d.TwoPage = true
	base := 0 // the pages before the book's own first page: the cover and the blank page behind it
	if Set(l.o.CoverPage) && b.PrintCover != nil {
		if err := r.coverPages(*b.PrintCover); err != nil {
			return nil, nil, err
		}
		base = 2
	}
	for idx, sh := range l.sheets {
		if l.ctx != nil && l.ctx.Err() != nil {
			return nil, nil, l.ctx.Err()
		}
		r.sheet(idx, sh)
	}
	l.labelsAndOutline(r, base)
	out, err := d.Finish()
	if err != nil {
		return nil, nil, err
	}
	if r.imgs.profiled > 0 {
		r.warns = append(r.warns, Warn{"image_colour_profile", fmt.Sprintf("%d image(s) carry a colour profile or a gamma that the PDF does not apply: their colours are shown as the numbers in the file say and may differ from a screen's. Convert them to sRGB first if the colours matter.", r.imgs.profiled)})
	}
	if r.failedImages > 0 {
		r.warns = append(r.warns, Warn{"image_not_embedded", fmt.Sprintf("%d image(s) could not be put in the PDF and a grey box stands in their place (%s).", r.failedImages, r.failedWhy)})
	}
	return out, r.warns, nil
}

func (l *PDFLayout) labelsAndOutline(r *renderer, base int) {
	d := r.d
	var labels []pdfw.PageLabel
	if base > 0 { // the cover and the page behind it are labelled "Cover", outside the numbering
		labels = append(labels, pdfw.PageLabel{Page: 0, Prefix: "Cover"})
	}
	if l.bodyStart > 0 && l.bodyStart < len(l.sheets) {
		labels = append(labels, pdfw.PageLabel{Page: base, Style: "r"}, pdfw.PageLabel{Page: base + l.bodyStart, Style: "D", Start: 1})
	} else if l.bodyStart >= len(l.sheets) {
		labels = append(labels, pdfw.PageLabel{Page: base, Style: "r"})
	} else {
		labels = append(labels, pdfw.PageLabel{Page: base, Style: "D", Start: 1})
	}
	d.SetLabels(labels)
	var items []pdfw.OutlineItem
	hasPart := false
	for _, s := range l.b.Sections {
		if s.Kind == SecPart {
			hasPart = true
		}
	}
	for i, s := range l.b.Sections {
		if l.secPage[i] < 0 || l.secKind[i] == spCopyright {
			continue
		}
		label := l.sectionLabel(i, s)
		if label == "" {
			continue
		}
		lv := 0
		if s.Kind == SecChapter && hasPart && s.Number > 0 {
			lv = 1
		}
		y := r.off + l.pr.top + l.secY[i]
		items = append(items, pdfw.OutlineItem{Title: label, Page: base + l.secPage[i], Level: lv, Y: y})
	}
	d.SetOutline(items)
}

// sheet draws one page.
func (r *renderer) sheet(idx int, sh *sheet) {
	l, pr := r.l, r.l.pr
	pg := r.d.NewPage(r.mediaW, r.mediaH)
	c := pg.Content
	recto := idx%2 == 0
	g := geom{yTop: r.off + pr.top, recto: recto}
	if recto {
		g.x0 = r.off + pr.inside
	} else {
		g.x0 = r.off + pr.outside
	}
	if pr.cropMarks {
		pg.Trim = [4]int64{r.off, r.off, r.off + pr.pageW, r.off + pr.pageH}
		pg.Bleed = [4]int64{r.off - pr.bleed, r.off - pr.bleed, r.off + pr.pageW + pr.bleed, r.off + pr.pageH + pr.bleed}
		r.cropMarks(c)
	}
	// body
	for _, p := range sh.items {
		r.item(c, g, p.it, p.y)
	}
	// footnotes
	if nl := sh.noteLines(); len(nl) > 0 {
		y := pr.textH - sh.noteH
		// a note that goes on from the page before has no rule over its rest
		if len(sh.carried) == 0 {
			ruleY := y + l.t.lead*17/20
			c.Save()
			c.StrokeGray(0)
			c.LineWidth(400)
			c.Line(g.x0, r.mediaH-(g.yTop+ruleY), g.x0+pr.textW/4, r.mediaH-(g.yTop+ruleY))
			c.Restore()
		}
		y += l.t.lead * 2 // the rule's space
		for _, it := range nl {
			r.item(c, g, it, y)
			y += it.h
		}
	}
	if sh.kind == pageBody && !sh.noHead && pr.runningHeads {
		r.runningHead(c, g, idx)
	}
	if !sh.noFolio && pr.folio != "none" {
		r.folio(c, g, sh, idx)
	}
	_ = l
}

func (r *renderer) cropMarks(c *pdfw.Content) {
	pr := r.l.pr
	c.Save()
	c.StrokeGray(0)
	c.LineWidth(250)
	gap := pr.bleed + inch/32 // the marks start clear of the bleed
	ln := r.off - gap - pt    // and end before the edge of the sheet
	x0, x1 := r.off, r.off+pr.pageW
	y0, y1 := r.off, r.off+pr.pageH
	for _, x := range []mpt{x0, x1} {
		c.Line(x, y0-gap, x, y0-gap-ln)
		c.Line(x, y1+gap, x, y1+gap+ln)
	}
	for _, y := range []mpt{y0, y1} {
		c.Line(x0-gap, y, x0-gap-ln, y)
		c.Line(x1+gap, y, x1+gap+ln, y)
	}
	c.Restore()
}

func (r *renderer) runningHead(c *pdfw.Content, g geom, idx int) {
	l, pr := r.l, r.l.pr
	var txt string
	if g.recto {
		txt = RunningTitle(l.b, l.o)
	} else {
		txt = CleanLine(l.b.Byline)
	}
	if txt == "" {
		return
	}
	size := l.t.size * 80 / 100
	st := stItalic
	var track mpt
	if pr.upperHead {
		txt = strings.ToUpper(txt)
		st, track = 0, size/12
	}
	y := pr.top/2 + size*7/20
	r.centred(c, g, txt, st, size, track, y)
	if pr.folio == "header" && !l.sheets[idx].noFolio {
		r.folioAt(c, g, l.PageLabel(idx), y, size)
	}
}

func (r *renderer) folio(c *pdfw.Content, g geom, sh *sheet, idx int) {
	l, pr := r.l, r.l.pr
	label := l.PageLabel(idx)
	size := l.t.size * 90 / 100
	if pr.folio == "header" && sh.kind == pageBody && !sh.noHead && pr.runningHeads {
		return // drawn with the head
	}
	y := pr.top + pr.textH + pr.bottom/2 + size*7/20
	r.centred(c, g, label, 0, size, 0, y)
}

// folioAt draws a page number at the outer edge of the text block.
func (r *renderer) folioAt(c *pdfw.Content, g geom, label string, y, size mpt) {
	runs := r.l.ts.shape(label, 0, size, 0)
	var w mpt
	for _, run := range runs {
		w += run.width
	}
	x := g.x0
	if g.recto {
		x = g.x0 + r.l.pr.textW - w
	}
	r.rawRuns(c, runs, x, r.mediaH-(r.off+y))
}

// centred draws a line of text centred on the text block, its baseline y from
// the top of the page (the trim box).
func (r *renderer) centred(c *pdfw.Content, g geom, txt string, st style, size, track mpt, y mpt) {
	ts := r.l.ts
	set := func(txt string) ([]glyphRun, mpt) {
		runs := ts.shape(cleanText(txt), st, size, 0)
		var w mpt
		for i := range runs {
			runs[i].track = track
			runs[i].width += track * mpt(len(runs[i].gids))
			w += runs[i].width
		}
		return runs, w
	}
	runs, w := set(txt)
	maxW := r.l.pr.textW
	if w > maxW { // a long title is cut, not run off the page
		runs, w = set(r.l.t.fit(txt, st, size, maxW-track*mpt(runeCount(txt))))
	}
	r.rawRuns(c, runs, g.x0+(maxW-w)/2, r.mediaH-(r.off+y))
}

// rawRuns draws runs left to right from (x, y) in PDF coordinates.
func (r *renderer) rawRuns(c *pdfw.Content, runs []glyphRun, x, y mpt) {
	e := &emitter{r: r, c: c}
	c.BeginText()
	c.TextAt(x, y)
	for i := range runs {
		e.run(&runs[i])
	}
	e.flush()
	e.reset()
	c.EndText()
}

// ---- items ---------------------------------------------------------------------------------------

func (r *renderer) item(c *pdfw.Content, g geom, it *vitem, y mpt) {
	switch it.kind {
	case vLine:
		if it.par == nil {
			return
		}
		r.textLine(c, g, it, y)
	case vTOC:
		r.tocLine(c, g, it, y)
	case vImage:
		r.image(c, g, it, y)
	case vRow:
		r.row(c, g, it, y)
	}
}

// textLine draws a line of a paragraph.
func (r *renderer) textLine(c *pdfw.Content, g geom, it *vitem, y mpt) {
	p, ln := it.par, it.ln
	left := g.x0 + it.x
	base := r.mediaH - (g.yTop + y + it.base)
	if it.first && p.dropCap != nil {
		r.dropCap(c, p.dropCap, left, base-2*r.l.t.lead)
	}
	if it.lead != nil {
		w := it.lead.items[0].w
		r.rawRuns(c, it.lead.runs, g.x0+it.leadX-w, base)
	}
	r.drawLine(c, p, ln, left, it.w, it.align, base)
}

// drawLine draws the items of a broken line with its glue set, at the left
// edge of its measure, aligned as asked.
func (r *renderer) drawLine(c *pdfw.Content, p *para, ln lineRec, left, width mpt, al align, base mpt) {
	ratio := ln.r
	x := left + ln.indent
	switch al {
	case alignCenter:
		x = left + (width-ln.nat)/2
		ratio = 0
	case alignRight:
		x = left + width - ln.nat
		ratio = 0
	case alignLeft:
		if ratio > 0 {
			ratio = 0
		}
	}
	e := &emitter{r: r, c: c}
	c.BeginText()
	c.TextAt(x, base)
	exact := mpt(0)
	type strike struct{ x0, x1, size mpt }
	var strikes []strike
	for i := ln.from; i < ln.to; i++ {
		it := p.items[i]
		switch it.kind {
		case iBox:
			for k := it.r0; k < it.r1; k++ {
				run := &p.runs[k]
				if run.strike {
					strikes = append(strikes, strike{x + exact, x + exact + run.width, run.size})
				}
				e.run(run)
				exact += run.width
			}
		case iGlue:
			if it.r0 >= 0 && int(it.r0) < len(p.runs) && it.w > 0 {
				w := it.w
				if ratio >= 0 {
					w += it.y * mpt(ratio) / 1000
				} else {
					w += it.z * mpt(ratio) / 1000
				}
				run := &p.runs[it.r0]
				e.run(run)
				exact += w
				e.sync(exact)
			}
		}
	}
	e.flush()
	e.reset()
	c.EndText()
	if len(strikes) > 0 {
		c.Save()
		c.FillGray(0)
		for _, s := range strikes {
			c.FillRect(s.x0, base+s.size*3/10, s.x1-s.x0, max(s.size/16, 300))
		}
		c.Restore()
	}
}

func (r *renderer) dropCap(c *pdfw.Content, dc *dropCap, x, base mpt) {
	r.rawRuns(c, []glyphRun{dc.run}, x, base)
}

// emitter writes glyph runs into a text object, keeping the position the PDF
// viewer will have in step with the position the layout computed.
type emitter struct {
	r     *renderer
	c     *pdfw.Content
	font  *pdfw.Font
	face  *faceRT
	size  mpt
	rise  mpt
	track mpt
	cur   pdfw.TJItem
	items []pdfw.TJItem
	pdfX  mpt // where the viewer's pen is, measured from the line's start
}

func (e *emitter) flush() {
	if len(e.cur.Codes) > 0 || e.cur.Adj != 0 {
		e.items = append(e.items, e.cur)
		e.cur = pdfw.TJItem{}
	}
	if len(e.items) > 0 {
		e.c.Show(e.items)
		e.items = e.items[:0]
	}
}

func (e *emitter) reset() {
	if e.rise != 0 {
		e.c.Rise(0)
		e.rise = 0
	}
	if e.track != 0 {
		e.c.CharSpace(0)
		e.track = 0
	}
}

// adjust moves the pen right by delta (negative moves it left).
func (e *emitter) adjust(delta mpt) {
	if delta == 0 || e.size == 0 {
		return
	}
	e.cur.Adj = -delta * 1_000_000 / e.size
	e.items = append(e.items, e.cur)
	e.cur = pdfw.TJItem{}
	e.pdfX += delta
}

// pdfAdvance is a glyph's advance as the viewer computes it: the width in the
// font dictionary is a whole number of thousandths of an em.
func pdfAdvance(f *font.Font, gid uint16, size mpt) mpt {
	w := (int64(f.Advance(gid))*1000 + int64(f.UnitsPerEm)/2) / int64(f.UnitsPerEm)
	return (w*size + 500) / 1000
}

func (e *emitter) run(run *glyphRun) {
	if run.face != e.face || run.size != e.size {
		e.flush()
		e.face, e.size = run.face, run.size
		e.font = e.r.font(run.face)
		e.c.SetFont(e.font, run.size)
	}
	if run.rise != e.rise {
		e.flush()
		e.c.Rise(run.rise)
		e.rise = run.rise
	}
	if run.track != e.track {
		e.flush()
		e.c.CharSpace(run.track)
		e.track = run.track
	}
	for i, g := range run.gids {
		code := e.font.Code(g, run.runes[i])
		e.cur.Codes = append(e.cur.Codes, code)
		e.pdfX += pdfAdvance(run.face.f, g, run.size) + run.track
		if k := mpt(run.kern[i]); k != 0 {
			e.adjust(k)
		}
	}
}

// sync moves the viewer's pen to the exact position the layout computed, so
// the rounding of font widths cannot add up along a line.
func (e *emitter) sync(exact mpt) {
	e.adjust(exact - e.pdfX)
}

// ---- other items -----------------------------------------------------------------------------

func (r *renderer) tocLine(c *pdfw.Content, g geom, it *vitem, y mpt) {
	l := r.l
	tl := it.toc
	base := r.mediaH - (g.yTop + y + it.base)
	left := g.x0 + it.x
	r.rawRuns(c, tl.runs, left, base)
	pg := l.SectionPage(tl.sec)
	if pg == 0 {
		return
	}
	label := l.PageLabel(pg - 1)
	size := l.t.size
	st := style(0)
	if tl.head {
		st = stBold
	}
	runs := l.ts.shape(label, st, size, 0)
	var lw mpt
	for _, run := range runs {
		lw += run.width
	}
	right := g.x0 + l.pr.textW
	r.rawRuns(c, runs, right-lw, base)
	// dots from the end of the title to the number, on a fixed pitch so they line up down the page
	dot := l.ts.shape(".", 0, size, 0)
	if len(dot) == 0 {
		return
	}
	dw := dot[0].width
	pitch := dw + size*4/10
	start := left + tl.w + size/2
	end := right - lw - size/2
	if end-start < pitch {
		return
	}
	// Dots sit on a fixed pitch measured from the right edge, so they line up
	// down the page whatever the width of each number.
	anchor := right - l.ts.width("000", 0, size) - size/2
	var xs []mpt
	for x := anchor; x >= start; x -= pitch {
		if x+dw <= right-lw-size/3 {
			xs = append([]mpt{x}, xs...)
		}
	}
	if len(xs) == 0 {
		return
	}
	e := &emitter{r: r, c: c}
	c.BeginText()
	c.TextAt(xs[0], base)
	for k := range xs {
		e.run(&dot[0])
		if k+1 < len(xs) {
			e.sync(xs[k+1] - xs[0])
		}
	}
	e.flush()
	e.reset()
	c.EndText()
}

func (r *renderer) image(c *pdfw.Content, g geom, it *vitem, y mpt) {
	ip := it.img
	x := g.x0 + ip.x
	top := g.yTop + y
	pdfY := r.mediaH - (top + ip.h)
	if im, why := r.imgs.embed(ip.img); im != nil {
		c.ImageOriented(im, x, pdfY, ip.w, ip.h, ip.img.orientation())
		return
	} else {
		r.failedImages++
		if r.failedWhy == "" {
			r.failedWhy = why
		}
	}
	c.Save()
	c.FillGray(900)
	c.FillRect(x, pdfY, ip.w, ip.h)
	c.StrokeGray(500)
	c.LineWidth(500)
	c.StrokeRect(x, pdfY, ip.w, ip.h)
	c.Restore()
}

func (r *renderer) row(c *pdfw.Content, g geom, it *vitem, y mpt) {
	rs := it.row
	left := g.x0 + rs.x
	topY := g.yTop + y
	total := rs.cols[len(rs.cols)-1]
	c.Save()
	c.StrokeGray(0)
	rule := func(at mpt, wd int64, gray int64) {
		c.StrokeGray(gray)
		c.LineWidth(wd)
		py := r.mediaH - at
		c.Line(left, py, left+total, py)
	}
	if rs.first {
		rule(topY, 750, 0)
	}
	for ci, cell := range rs.cells {
		cw := rs.cols[ci+1] - rs.cols[ci] - 2*rs.pad
		for k := rs.from; k < rs.to && k < len(cell.lines); k++ {
			base := r.mediaH - (topY + rs.pad + mpt(k-rs.from)*rs.cellLd + r.l.t.baseFor(rs.size, rs.cellLd))
			r.drawLine(c, cell.par, cell.lines[k], left+rs.cols[ci]+rs.pad, cw, cell.align, base)
		}
	}
	bottom := topY + it.h
	switch {
	case rs.last:
		rule(bottom, 750, 0)
	case rs.head:
		rule(bottom, 500, 0)
	default:
		rule(bottom, 250, 700)
	}
	c.Restore()
}

// coverPages puts the cover's front first, edge to edge on a page of the book's own size (into the bleed, with the crop
// marks, when the book has them), and a blank page behind it. The blank page is the inside of the cover: with it the
// title page is still a right-hand page, the margins still mirror, and a file printed on both sides still has the
// cover's reverse blank. Neither is a page of the book: they are labelled "Cover", and the page count that sets a spine
// does not include them.
func (r *renderer) coverPages(cs CoverSpec) error {
	pr := r.l.pr
	cs.TrimW, cs.TrimH, cs.Bleed, cs.Spine, cs.FrontOnly = pr.pageW, pr.pageH, 0, 0, true
	if pr.cropMarks {
		cs.Bleed = pr.bleed
	}
	p, err := planCover(cs)
	if err != nil {
		return err
	}
	cd, err := newCoverDrawer(r.d, cs, p, r.imgs)
	if err != nil {
		return err
	}
	cd.r.mediaH = r.mediaH
	cd.placeholder = true
	for i := 0; i < 2; i++ {
		pg := r.d.NewPage(r.mediaW, r.mediaH)
		if pr.cropMarks {
			pg.Trim = [4]int64{r.off, r.off, r.off + pr.pageW, r.off + pr.pageH}
			pg.Bleed = [4]int64{r.off - pr.bleed, r.off - pr.bleed, r.off + pr.pageW + pr.bleed, r.off + pr.pageH + pr.bleed}
			r.cropMarks(pg.Content)
		}
		if i == 0 {
			b := cs.Bleed
			fr := frame{trim: rect{r.off, r.off, r.off + pr.pageW, r.off + pr.pageH}, bl: b, bb: b, br: b, bt: b}
			if p.wrap != nil {
				// cut from a wrap, the front has the wrap's bleed on its outer edges only: none on the spine side (what lies
				// there is the spine's art), and no more than the wrap was made with
				wb := min(b, p.bleed)
				fr.bl, fr.br, fr.bb, fr.bt = 0, wb, wb, wb
				if p.rtl {
					fr.bl, fr.br = wb, 0
				}
			}
			cd.frontAlone(pg.Content, fr, b)
		}
	}
	if cd.err != nil {
		return cd.err
	}
	r.warns = append(r.warns, plainWarns(p.warns)...)
	r.warns = append(r.warns, cd.ts.warnings()...)
	if cd.skipped > 0 {
		r.warns = append(r.warns, Warn{"image_not_embedded", "The cover page's picture was not read (a preview or a dry run), and a grey box stands in its place."})
	}
	return nil
}

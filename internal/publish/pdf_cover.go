package publish

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/publish/pdfw"
	"github.com/crueber/scriobh/internal/text"
)

// CoverSpec describes a print cover: the wrap for a paperback (back, spine, front, with bleed), the front alone, and,
// when the writer has a cover layout, a proof. With no Art the cover is the generated one: a flat colour with type.
type CoverSpec struct {
	Title, Subtitle, Author, Kind string
	Color                         string // "#RRGGBB", one flat fill
	TrimW, TrimH                  mpt
	Spine                         mpt
	Bleed                         mpt
	Guides                        bool // trim, spine and safe-area lines in the bleed and on the art
	Barcode                       bool // a white box for the barcode on the back
	Language, Identifier          string
	InputHash                     string
	Modified                      time.Time

	// Art is the writer's own cover (nil: the generated cover, as it always was).
	Art *CoverArt
	// Profile is the printer's figures. A generated cover (no Art) uses them for its spine type and, when ProfileGiven,
	// for its text margin and barcode box; a cover with Art always does. Pages is the interior's page count (0: unknown,
	// so the profile's page floor for spine type is not applied). RTL is the book's direction.
	Profile      CoverProfile
	ProfileGiven bool
	Pages        int
	RTL          bool
	// FrontOnly plans only the front (the interior PDF's cover page): no back, spine, barcode or spine check.
	FrontOnly bool
	// Part is which file this is: "cover" (or empty), the one page a printer takes (the wrap); "front", the front alone at
	// trim plus bleed; "proof", the proof page (a cover with a layout only). "all" writes the pages together, as a test
	// reads them.
	Part string
	// NoPictures says the pictures' bytes were not read (a dry run): a grey box stands in each, with a warning, and the
	// file is a look at the layout, not a cover to send.
	NoPictures bool
}

// SpineWidth is the width of a spine: the number of pages times the paper's
// thickness per page (the caliper of a leaf is two pages' worth), both in the
// units of the result. The thickness is the printer's figure, never a guess.
func SpineWidth(pages int, thicknessPerPage mpt) mpt { return mpt(pages) * thicknessPerPage }

// MinSpineText is the narrowest spine that can carry type (printers commonly
// ask for 0.125 in of space each side, so about a quarter of an inch).
const MinSpineText = inch / 4

func parseHex(c string) (r, g, b uint8, ok bool) {
	c = strings.TrimPrefix(c, "#")
	if len(c) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(c, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}

// coverDrawer draws the parts of a cover into a document.
type coverDrawer struct {
	p      *coverPlan
	d      *pdfw.Doc
	r      *renderer
	t      *typesetter
	ts     *typeface
	imgs   *imageEmbedder
	cr     [3]uint8
	err    error
	warnTS []Warn
	// placeholder asks for a grey box where a picture has no bytes (a preview or a dry run of the interior PDF reads none),
	// as the book's own pictures get; skipped counts them. A print cover is never made without its pictures.
	placeholder bool
	skipped     int
}

func newCoverDrawer(d *pdfw.Doc, cs CoverSpec, p *coverPlan, imgs *imageEmbedder) (*coverDrawer, error) {
	cr, cg, cb, ok := parseHex(cs.Color)
	if !ok {
		cr, cg, cb = 0x2F, 0x5D, 0x50
	}
	ts, err := newTypeface("")
	if err != nil {
		return nil, err
	}
	o := printDefaults(Options{FontSize: 12})
	pr := resolveParams(o)
	t := newTypesetter(&Book{}, o, ts, pr)
	r := &renderer{l: &PDFLayout{ts: ts, t: t, pr: pr}, d: d, fonts: map[*faceRT]*pdfw.Font{}}
	return &coverDrawer{p: p, d: d, r: r, t: t, ts: ts, imgs: imgs, cr: [3]uint8{cr, cg, cb}}, nil
}

func (cd *coverDrawer) embed(ci *CoverImage, behind [3]uint8) *pdfw.Image {
	if len(ci.Data) == 0 && cd.placeholder {
		cd.skipped++
		return nil
	}
	im, why := cd.imgs.embed(ci.image(behind))
	if im == nil && cd.err == nil {
		cd.err = fmt.Errorf("%w: %s", ErrCoverPicture, why)
	}
	return im
}

// WriteCoverPDF writes the cover: page 1 is the wrap (back, spine and front, with bleed), page 2 the front alone at trim
// size with bleed, and, for a cover with a layout, page 3 a proof (the wrap at half size with the trim, bleed, spine
// folds, text margin and barcode box drawn over it, and a legend). With no layout it is the generated cover: a flat colour
// with white type, no gradient and no image, two pages. The warnings are what the writer should know (a picture's
// resolution at its printed size, a stale wrap, a spine that cannot carry type).
func WriteCoverPDF(cs CoverSpec) ([]byte, []Warn, error) {
	p, err := planCover(cs)
	if err != nil {
		return nil, nil, err
	}
	d := pdfw.New()
	d.Uncompressed = uncompressedForTests
	d.Title = CleanLine(cs.Title)
	d.Author = CleanLine(cs.Author)
	d.Subject = "Cover"
	d.Language = firstNonEmpty(cs.Language, "en")
	if !validLang(d.Language) {
		d.Language = "en"
	}
	if !cs.Modified.IsZero() {
		m := cs.Modified.UTC()
		d.Created = [6]int{m.Year(), int(m.Month()), m.Day(), m.Hour(), m.Minute(), m.Second()}
	}
	d.ID = pdfw.DocumentID("scriobh-cover", cs.InputHash, cs.Title, cs.Identifier)
	cd, err := newCoverDrawer(d, cs, p, newImageEmbedder(d))
	if err != nil {
		return nil, nil, err
	}
	cd.placeholder = cs.NoPictures
	part := cs.Part
	if part == "" {
		part = PartCover
	}
	if part == PartProof && p.legacy {
		return nil, nil, ErrNoProof
	}
	if p.refuse != nil {
		return nil, nil, p.refuse
	}
	all := part == "all"
	// The wrap: the one page a printer takes.
	if all || part == PartCover {
		pg := d.NewPage(p.wrapW, p.wrapH)
		boxes(pg, p.wrapW, p.wrapH, p.bleed)
		cd.r.mediaH = p.wrapH
		cd.wrapContent(pg.Content, cs.Guides)
		if coverPageHook != nil {
			coverPageHook(1, pg.Content.Bytes())
		}
	}
	// The front on its own. Made of sides it has bleed on every side; cut from a wrap it has the wrap's bleed on its outer
	// edges only (the spine side has none to give: what lies there is the spine's art, which is not the front's bleed).
	if all || part == PartFront {
		fw, fh := p.trimW+2*p.bleed, p.trimH+2*p.bleed
		fr := frame{trim: rect{p.bleed, p.bleed, p.bleed + p.trimW, p.bleed + p.trimH}, bl: p.bleed, bb: p.bleed, br: p.bleed, bt: p.bleed}
		if p.wrap != nil {
			fw = p.trimW + p.bleed
			x0 := mpt(0)
			fr.bl, fr.br = 0, p.bleed
			if p.rtl {
				x0, fr.bl, fr.br = p.bleed, p.bleed, 0
			}
			fr.trim = rect{x0, p.bleed, x0 + p.trimW, p.bleed + p.trimH}
		}
		pg2 := d.NewPage(fw, fh)
		if p.bleed > 0 {
			pg2.Trim = [4]int64{fr.trim.x0, fr.trim.y0, fr.trim.x1, fr.trim.y1}
			pg2.Bleed = [4]int64{0, 0, fw, fh}
		}
		cd.r.mediaH = fh
		cd.frontAlone(pg2.Content, fr, p.bleed)
		if cs.Guides {
			c2 := pg2.Content
			c2.Save()
			c2.StrokeGray(1000)
			c2.LineWidth(400)
			c2.Dash(3*pt, 3*pt)
			c2.StrokeRect(fr.trim.x0, fr.trim.y0, fr.trim.w(), fr.trim.h())
			c2.Restore()
		}
		if coverPageHook != nil {
			coverPageHook(2, pg2.Content.Bytes())
		}
	}
	// The proof, for a cover that has a layout.
	if (all && !p.legacy) || part == PartProof {
		cd.proof()
	}
	if cd.err != nil {
		return nil, nil, cd.err
	}
	out, err := d.Finish()
	if err != nil {
		return nil, nil, err
	}
	warns := plainWarns(p.warns)
	if cd.skipped > 0 {
		warns = append(warns, Warn{"image_not_embedded", "The cover's pictures were not read (a dry run), and grey boxes stand in their places."})
	}
	warns = append(warns, cd.ts.warnings()...)
	warns = append(warns, cd.t.warnings()...)
	return out, warns, nil
}

var white = [3]uint8{255, 255, 255}

// coverPageHook, when a test sets it, is given each page's content stream as it is drawn.
var coverPageHook func(n int, content []byte)

// Parts of a cover file (CoverSpec.Part).
const (
	PartCover = "cover"
	PartFront = "front"
	PartProof = "proof"
)

// ErrNoProof is returned for the proof of a cover with no layout: the proof is of a layout.
var ErrNoProof = errors.New("a cover with no layout has no proof")

// boxes sets the trim and bleed boxes of a page whose art runs to its edge and whose trim is the bleed in from it.
func boxes(pg *pdfw.Page, w, h, bleed mpt) {
	if bleed > 0 {
		pg.Trim = [4]int64{bleed, bleed, w - bleed, h - bleed}
		pg.Bleed = [4]int64{0, 0, w, h}
	}
}

// ---- page 1 ----------------------------------------------------------------------------------

// wrapContent draws the wrap: with a wrap picture, that picture and nothing else; otherwise the flat colour, the panels,
// the spine type and the barcode box. Guides, when asked for, are drawn over it.
func (cd *coverDrawer) wrapContent(c *pdfw.Content, guides bool) {
	p := cd.p
	if p.wrap != nil {
		if im := cd.embed(&p.wrap.Image, white); im != nil {
			c.Image(im, 0, 0, p.wrapW, p.wrapH)
		} else if cd.err == nil {
			cd.grey(c, rect{0, 0, p.wrapW, p.wrapH})
		}
		cd.barcode(c)
		cd.guides(c, guides)
		return
	}
	c.FillRGB(cd.cr[0], cd.cr[1], cd.cr[2])
	c.FillRect(0, 0, p.wrapW, p.wrapH)
	cd.panel(c, p.bp, p.back, false)
	cd.panel(c, p.fp, p.front, true)
	cd.barcode(c)
	cd.spineType(c)
	cd.guides(c, guides)
}

// panel draws one panel of a cover made of sides.
func (cd *coverDrawer) panel(c *pdfw.Content, pp panelPlan, fr frame, front bool) {
	switch pp.mode {
	case PanelGenerated:
		cd.generated(c, fr, front)
	case PanelImage:
		im := cd.embed(pp.img, white)
		a := fr.painted()
		if im == nil {
			cd.grey(c, a)
			return
		}
		c.Save()
		c.FillGray(1000)
		c.FillRect(a.x0, a.y0, a.w(), a.h())
		c.Clip(a.x0, a.y0, a.w(), a.h())
		c.Image(im, pp.pic.x0, pp.pic.y0, pp.pic.w(), pp.pic.h())
		c.Restore()
	case PanelPage:
		im := cd.embed(pp.img, pp.bg)
		a := fr.painted()
		if im == nil {
			cd.grey(c, a)
			return
		}
		c.Save()
		c.FillRGB(pp.bg[0], pp.bg[1], pp.bg[2])
		c.FillRect(a.x0, a.y0, a.w(), a.h())
		c.Clip(pp.clip.x0, pp.clip.y0, pp.clip.w(), pp.clip.h())
		c.Image(im, pp.pic.x0, pp.pic.y0, pp.pic.w(), pp.pic.h())
		c.Restore()
	}
}

// grey is the box that stands in a picture whose bytes were not read.
func (cd *coverDrawer) grey(c *pdfw.Content, a rect) {
	c.Save()
	c.FillGray(900)
	c.FillRect(a.x0, a.y0, a.w(), a.h())
	c.StrokeGray(500)
	c.LineWidth(500)
	c.StrokeRect(a.x0, a.y0, a.w(), a.h())
	c.Restore()
}

// generated draws the typographic panel: the title, subtitle and author on the front; the kind of book on the back (and,
// for a cover with no layout, the barcode box, where it has always been). The colour is the page's, filled before.
func (cd *coverDrawer) generated(c *pdfw.Content, fr frame, front bool) {
	cs, t, r := cd.p.cs, cd.t, cd.r
	left, top := fr.trim.x0, r.mediaH-fr.trim.y1
	w, h := fr.trim.w(), fr.trim.h()
	margin := w * 12 / 100
	textW := w - 2*margin
	c.FillGray(1000)
	if front {
		size := mpt(math.Round(float64(t.size) * 3.2))
		lh := t.lines(size * 12 / 10)
		title := CleanLine(cs.Title)
		if title == "" {
			title = "Untitled"
		}
		// shrink the title until it fits in a third of the panel
		var ls []*vitem
		for {
			ls, _ = t.paragraph([]text.Inline{{Kind: text.IText, Text: title}}, paraOpt{size: size, style: stBold, lead: lh, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
			if heightOf(ls) <= h*38/100 || size <= t.size*14/10 {
				break
			}
			size = size * 9 / 10
			lh = t.lines(size * 12 / 10)
		}
		y := h * 16 / 100
		g := geom{x0: left + margin, yTop: top}
		for _, it := range ls {
			r.item(c, g, it, y)
			y += it.h
		}
		if sub := CleanLine(cs.Subtitle); sub != "" {
			ss := t.size * 15 / 10
			sl := t.lines(ss * 12 / 10)
			sls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: sub}}, paraOpt{size: ss, style: stItalic, lead: sl, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
			y += t.lead
			for _, it := range sls {
				r.item(c, g, it, y)
				y += it.h
			}
		}
		if a := CleanLine(cs.Author); a != "" {
			as := t.size * 18 / 10
			al := t.lines(as * 12 / 10)
			als, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: a}}, paraOpt{size: as, lead: al, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
			yy := h*88/100 - heightOf(als)
			for _, it := range als {
				r.item(c, g, it, yy)
				yy += it.h
			}
		}
		return
	}
	// back: the kind of book and the author, a place for the barcode
	g := geom{x0: left + margin, yTop: top}
	y := h * 18 / 100
	if k := CleanLine(cs.Kind); k != "" {
		ks, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: strings.ToUpper(k)}}, paraOpt{size: t.size * 9 / 10, style: stBold, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
		for _, it := range ks {
			r.item(c, g, it, y)
			y += it.h
		}
	}
	if cs.Barcode && cd.p.legacy && !cs.ProfileGiven {
		bw, bh := inch*2, inch*6/5
		c.Save()
		c.FillGray(1000)
		c.FillRect(left+w-margin/2-bw, r.mediaH-(top+h-margin/2), bw, bh)
		c.Restore()
	}
}

// barcode draws the white box the printer's barcode goes in, on the back, when it is asked for. (A generated cover with
// no profile named has drawn it inside its back panel since before profiles.)
func (cd *coverDrawer) barcode(c *pdfw.Content) {
	p := cd.p
	if !p.cs.Barcode || (p.legacy && !p.cs.ProfileGiven) {
		return
	}
	b := p.barcodeRect()
	c.Save()
	c.FillGray(1000)
	c.FillRect(b.x0, b.y0, b.w(), b.h())
	c.Restore()
}

// spineType sets the title and the author down the spine when the plan allows it.
func (cd *coverDrawer) spineType(c *pdfw.Content) {
	p := cd.p
	if !p.spineText {
		return
	}
	var b strings.Builder
	b.WriteString(CleanLine(p.cs.Title))
	if a := CleanLine(p.cs.Author); a != "" {
		b.WriteString("   ·   " + a)
	}
	t, ts, r := cd.t, cd.ts, cd.r
	size := t.size * 12 / 10
	// the type stays the profile's distance from the spine's edges: its height (ascent and descent, about 1.2 em) fits in
	// what is left
	edge := inchesMpt(p.prof.SpineEdge(float64(p.spine) / float64(inch)))
	if m := (p.spine - 2*edge) * 10 / 12; size > m && !p.legacySpine() {
		size = m
	}
	txt := t.fit(b.String(), stBold, size, p.trimH*80/100)
	runs := ts.shape(cleanText(txt), stBold, size, 0)
	c.FillGray(1000)
	e := &emitter{r: r, c: c}
	c.BeginText()
	// reads from the top down, centred on the spine and on the height
	var w mpt
	for _, rn := range runs {
		w += rn.width
	}
	c.TextAtRotated(p.spineX0+p.spine/2-size*7/20, p.wrapH-p.bleed-(p.trimH-w)/2, false)
	for i := range runs {
		e.run(&runs[i])
	}
	e.flush()
	e.reset()
	c.EndText()
}

// guides draws the dashed trim, spine and safe-area lines on page 1.
func (cd *coverDrawer) guides(c *pdfw.Content, on bool) {
	if !on {
		return
	}
	p := cd.p
	c.Save()
	c.StrokeGray(1000)
	c.LineWidth(400)
	c.Dash(3*pt, 3*pt)
	line := func(x0, y0, x1, y1 mpt) { c.Line(x0, y0, x1, y1) }
	trim := rect{p.bleed, p.bleed, p.wrapW - p.bleed, p.wrapH - p.bleed}
	// over a picture a white line can vanish: a dark hairline goes under it
	under := func(f func()) {
		if p.legacy {
			return
		}
		c.Save()
		c.StrokeGray(0)
		c.LineWidth(250)
		c.Dash(0, 0)
		f()
		c.Restore()
	}
	draw := func() {
		c.StrokeRect(trim.x0, trim.y0, trim.w(), trim.h())
		if p.spine > 0 {
			line(p.spineX0, trim.y0, p.spineX0, trim.y1)
			line(p.spineX0+p.spine, trim.y0, p.spineX0+p.spine, trim.y1)
		}
		if p.legacy && !p.cs.ProfileGiven {
			safe := inch / 8
			c.StrokeRect(trim.x0+safe, trim.y0+safe, trim.w()-2*safe, trim.h()-2*safe)
			return
		}
		safe := inchesMpt(p.prof.TextMarginIn)
		for _, fr := range []frame{p.back, p.front} {
			c.StrokeRect(fr.trim.x0+safe, fr.trim.y0+safe, fr.trim.w()-2*safe, fr.trim.h()-2*safe)
		}
	}
	under(draw)
	draw()
	c.Restore()
}

// ---- page 2 ----------------------------------------------------------------------------------

// frontAlone draws the front on a frame whose bleed is bleed on every side: the generated front, the picture of an image or
// page side, or the front cut from a wrap picture.
func (cd *coverDrawer) frontAlone(c *pdfw.Content, fr frame, bleed mpt) {
	p := cd.p
	a := fr.painted()
	if p.wrap != nil {
		w := p.wrap
		im := cd.embed(&w.Image, white)
		if im == nil {
			cd.grey(c, a)
			return
		}
		// the picture at the size page 1 has it, moved so the front's trim lands on the frame's trim, and clipped to the
		// frame (the bleed of its outer edges only)
		px := func(v int) mpt { return (int64(v)*p.wrapH + int64(w.Image.H)/2) / int64(w.Image.H) }
		x0 := fr.trim.x0 - px(w.Front.Min.X)
		top := fr.trim.y1 + px(w.Front.Min.Y)
		c.Save()
		c.Clip(a.x0, a.y0, a.w(), a.h())
		c.Image(im, x0, top-p.wrapH, p.wrapW, p.wrapH)
		c.Restore()
		return
	}
	c.FillRGB(cd.cr[0], cd.cr[1], cd.cr[2])
	c.FillRect(a.x0, a.y0, a.w(), a.h())
	cd.panel(c, p.place("front", p.panelOf("front"), fr, bleed), fr, true)
}

// ---- page 3: the proof ---------------------------------------------------------------------

// The proof is the wrap at half size on a page of its own, with the lines a printer cuts and folds along drawn over it,
// and a legend. It is for the writer's eyes: it is never what is sent to a printer.
func (cd *coverDrawer) proof() {
	p, d := cd.p, cd.d
	const m = 36 * pt
	wrapWH, wrapHH := p.wrapW/2, p.wrapH/2
	pageW := max(wrapWH+2*m, 7*inch)
	lines := cd.legendLines()
	var items [][]*vitem
	var legendH mpt
	textX := m + 44*pt
	for _, ln := range lines {
		o := paraOpt{size: 9 * pt, lead: 12 * pt, x: 0, w: pageW - textX - m, noNotes: true}
		if ln.bold {
			o.style = stBold
		}
		its, _ := cd.t.paragraph([]text.Inline{{Kind: text.IText, Text: ln.text}}, o)
		items = append(items, its)
		legendH += heightOf(its)
	}
	gap := 20 * pt
	pageH := m + wrapHH + gap + legendH + m
	pg := d.NewPage(pageW, pageH)
	c := pg.Content
	ox := (pageW - wrapWH) / 2
	oy := pageH - m - wrapHH // the wrap's bottom edge
	// the wrap, at half size
	cd.r.mediaH = p.wrapH
	c.Save()
	c.Cm(500, 0, 0, 500, ox, oy)
	cd.wrapContent(c, false)
	cd.overlay(c)
	c.Restore()
	// the legend
	cd.r.mediaH = pageH
	y := m + wrapHH + gap
	g := geom{x0: textX, yTop: 0}
	for i, its := range items {
		ln := lines[i]
		if ln.key != nil {
			c.Save()
			ln.key.stroke(c)
			c.LineWidth(1500)
			c.Dash(ln.key.dash, ln.key.dash)
			ly := pageH - (y + 6*pt)
			c.Line(m, ly, m+36*pt, ly)
			c.Restore()
		}
		c.Save()
		c.FillGray(0)
		for _, it := range its {
			r := cd.r
			r.item(c, g, it, y)
			y += it.h
		}
		c.Restore()
	}
}

type lineKey struct {
	r, g, b uint8
	dash    mpt
}

func (k *lineKey) stroke(c *pdfw.Content) { c.StrokeRGB(k.r, k.g, k.b) }

type legendLine struct {
	text string
	bold bool
	key  *lineKey
}

var (
	keyTrim    = &lineKey{0xD6, 0x27, 0x28, 0}
	keyBleed   = &lineKey{0xD6, 0x27, 0x28, 3 * pt}
	keySpine   = &lineKey{0x1F, 0x5F, 0xBF, 3 * pt}
	keySafe    = &lineKey{0x2E, 0x8B, 0x3D, 3 * pt}
	keyBarcode = &lineKey{0xE0, 0x7A, 0x00, 0}
	keyStale   = &lineKey{0x8E, 0x2D, 0xA6, 3 * pt}
)

func (cd *coverDrawer) legendLines() []legendLine {
	p := cd.p
	in := func(v mpt) float64 { return float64(v) / float64(inch) }
	ls := []legendLine{
		{text: "Proof: for you, not for the printer. Page 1 is the file to send.", bold: true},
		{text: fmt.Sprintf("Shown at half size. Trim %.4g by %.4g in, bleed %.4g in, spine %.3f in, whole wrap %.3f by %.3f in. Profile %s.", in(p.trimW), in(p.trimH), in(p.bleed), in(p.spine), in(p.wrapW), in(p.wrapH), p.prof.Name)},
		{text: "Trim: where the pages are cut.", key: keyTrim},
		{text: "Edge of the bleed: art runs to here, and is cut off.", key: keyBleed},
		{text: "Spine folds.", key: keySpine},
		{text: fmt.Sprintf("Text-safe margin, %.4g in inside the trim of the front and the back.", p.prof.TextMarginIn), key: keySafe},
		{text: fmt.Sprintf("Barcode area, %.4g by %.4g in, %.4g in from the trim of the back: leave it clear.", p.prof.BarcodeWIn, p.prof.BarcodeHIn, p.prof.BarcodeInsetIn), key: keyBarcode},
	}
	if cd.staleSpine() {
		ls = append(ls, legendLine{text: fmt.Sprintf("Where the spine folds if the book's own spine (%.3f in) is right: the wrap does not match it.", in(p.cs.Spine)), key: keyStale})
	}
	return ls
}

func (cd *coverDrawer) staleSpine() bool {
	p := cd.p
	return p.wrap != nil && p.cs.Spine > 0 && math.Abs(float64(p.spine-p.cs.Spine)) > float64(inch)/32
}

// overlay draws the printer's lines over the wrap, in the wrap's own coordinates (the caller has scaled them to half).
func (cd *coverDrawer) overlay(c *pdfw.Content) {
	p := cd.p
	const w = 2 * pt // one point on the half-size page
	stroke := func(k *lineKey, f func()) {
		c.Save()
		k.stroke(c)
		c.LineWidth(w)
		if k.dash > 0 {
			c.Dash(2*k.dash, 2*k.dash)
		}
		f()
		c.Restore()
	}
	trim := rect{p.bleed, p.bleed, p.wrapW - p.bleed, p.wrapH - p.bleed}
	stroke(keyBleed, func() { c.StrokeRect(w/2, w/2, p.wrapW-w, p.wrapH-w) })
	stroke(keyTrim, func() { c.StrokeRect(trim.x0, trim.y0, trim.w(), trim.h()) })
	stroke(keySpine, func() {
		c.Line(p.spineX0, 0, p.spineX0, p.wrapH)
		c.Line(p.spineX0+p.spine, 0, p.spineX0+p.spine, p.wrapH)
	})
	safe := inchesMpt(p.prof.TextMarginIn)
	stroke(keySafe, func() {
		for _, fr := range []frame{p.back, p.front} {
			c.StrokeRect(fr.trim.x0+safe, fr.trim.y0+safe, fr.trim.w()-2*safe, fr.trim.h()-2*safe)
		}
	})
	b := p.barcodeRect()
	stroke(keyBarcode, func() { c.StrokeRect(b.x0, b.y0, b.w(), b.h()) })
	if cd.staleSpine() {
		mid := p.spineX0 + p.spine/2
		stroke(keyStale, func() {
			c.Line(mid-p.cs.Spine/2, 0, mid-p.cs.Spine/2, p.wrapH)
			c.Line(mid+p.cs.Spine/2, 0, mid+p.cs.Spine/2, p.wrapH)
		})
	}
}

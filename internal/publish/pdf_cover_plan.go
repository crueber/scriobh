package publish

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sort"
)

// The arithmetic of a print cover, apart from drawing it: where each panel sits on the wrap, where each picture lands
// (a size and a place in milli-points), how sharp it comes out at that size, and what is worth telling the writer. The
// drawing (pdf_cover.go) follows this plan and adds nothing to it, and the cover check (CoverWarnings) runs the same
// plan without a picture's bytes, so a check and the file it checks cannot disagree.

// CoverProfile is a printer's figures for a paperback cover (docs/COVERS.md). They are the printer's, and the printer's
// own template stays authoritative.
type CoverProfile struct {
	Name string
	// TextMarginIn is how far text and important art stay inside the trim.
	TextMarginIn float64
	// BarcodeWIn and BarcodeHIn are the barcode box, and BarcodeInsetIn how far its lower right corner is from the trim of
	// the back (the figure the design leaves open: a quarter inch for every profile).
	BarcodeWIn, BarcodeHIn, BarcodeInsetIn float64
	// SpineTextFromPages is the fewest pages of a book that has type on its spine.
	SpineTextFromPages int
	// SpineEdgeIn is how far spine type stays from the spine's edges; SpineEdgeNarrowIn replaces it for a spine under
	// SpineNarrowUnderIn (0 for no such rule).
	SpineEdgeIn, SpineEdgeNarrowIn, SpineNarrowUnderIn float64
}

// SpineEdge is the clear space spine type keeps from each edge of a spine of the given width.
func (p CoverProfile) SpineEdge(spineIn float64) float64 {
	if p.SpineNarrowUnderIn > 0 && spineIn < p.SpineNarrowUnderIn {
		return p.SpineEdgeNarrowIn
	}
	return p.SpineEdgeIn
}

// CoverProfiles are the named profiles: the KDP and IngramSpark paperback figures of docs/COVERS.md, and a generic one.
var CoverProfiles = map[string]CoverProfile{
	"kdp":     {Name: "kdp", TextMarginIn: 0.125, BarcodeWIn: 2, BarcodeHIn: 1.2, BarcodeInsetIn: 0.25, SpineTextFromPages: 79, SpineEdgeIn: 0.0625},
	"ingram":  {Name: "ingram", TextMarginIn: 0.25, BarcodeWIn: 1.75, BarcodeHIn: 1, BarcodeInsetIn: 0.25, SpineTextFromPages: 48, SpineEdgeIn: 0.0625, SpineEdgeNarrowIn: 0.03125, SpineNarrowUnderIn: 0.35},
	"generic": {Name: "generic", TextMarginIn: 0.25, BarcodeWIn: 2, BarcodeHIn: 1.2, BarcodeInsetIn: 0.25, SpineTextFromPages: 48, SpineEdgeIn: 0.0625},
}

// LookupCoverProfile finds a profile by name; "" is generic.
func LookupCoverProfile(name string) (CoverProfile, bool) {
	if name == "" {
		name = "generic"
	}
	p, ok := CoverProfiles[name]
	return p, ok
}

// Modes of a cover panel.
const (
	PanelGenerated = "generated" // the typographic cover for the panel
	PanelImage     = "image"     // one picture filling the panel, bleed included
	PanelPage      = "page"      // a picture placed on a page of the trim
	PanelNone      = "none"      // a back that is left out: the cover colour
)

// Shapes of cover art.
const (
	ArtSides = "sides"
	ArtWrap  = "wrap"
)

// CoverImage is a cleaned cover picture. Data is empty for a check, which needs only the size.
type CoverImage struct {
	Data []byte
	Ext  string // jpg or png
	// Hash names the picture: the same picture is one object in the file however many pages show it.
	Hash string
	W, H int
	// ICC is the picture's own RGB profile, vetted by the caller (empty: DeviceRGB).
	ICC []byte
}

// image is the picture as the PDF embedder takes it, flattened (if it has transparency) on the colour behind it.
func (ci *CoverImage) image(behind [3]uint8) Image {
	mime := "image/jpeg"
	if ci.Ext == "png" {
		mime = "image/png"
	}
	return Image{Data: ci.Data, Mime: mime, Ext: ci.Ext, Width: ci.W, Height: ci.H, Hash: ci.Hash, Orient: 1, ICC: ci.ICC, Flat: true, Flatten: behind}
}

// CoverPlacement is where a picture lands on the page of a page panel, in the page's own pixels (the long side is
// 2,560 of them for every trim): coverlayout.PlacePage's answer, in plain numbers.
type CoverPlacement struct {
	CanvasW, CanvasH int
	Box, Dst         image.Rectangle
}

// CoverPanel is one side of the cover.
type CoverPanel struct {
	Mode  string
	Image *CoverImage
	// Place and Background are a page panel's: where the picture goes and the colour of the page (#RRGGBB).
	Place      *CoverPlacement
	Background string
}

// CoverWrap is a wrap picture as coverlayout measured it: the panels cut from it in the picture's pixels.
type CoverWrap struct {
	Image CoverImage
	// BleedIn is the bleed the picture was made with on its outer edges.
	BleedIn float64
	// DPI is the picture's resolution (its height over the trim height plus two bleeds); SpineIn the measured spine.
	DPI, SpineIn float64
	// Front, Back and Spine are in the picture's pixels; the Bleed rectangles include the outer bleed.
	Front, Back, FrontBleed, BackBleed, Spine image.Rectangle
	FrontOnRight                              bool
}

// CoverArt is the writer's own cover: two sides, or one picture for the whole wrap.
type CoverArt struct {
	Shape       string
	Front, Back CoverPanel
	Wrap        *CoverWrap
}

// ErrCoverRTL is returned when a right-to-left book's cover would have type set on it: this program sets Latin script
// left to right, so a generated side (or any text it draws) cannot be made for such a book; pictures can.
var ErrCoverRTL = errors.New("a generated cover sets type left to right, which a right-to-left book cannot have: use a picture for the front and the back, or the wrap")

// ErrCoverPicture is returned when a cover picture cannot be put in the PDF (it is damaged, or larger than a PDF here takes);
// the sentence after it says why.
var ErrCoverPicture = errors.New("a cover picture cannot be put in the PDF")

// ---- frames ----------------------------------------------------------------------------------

// rect is a rectangle in the PDF's space (y up) in milli-points.
type rect struct{ x0, y0, x1, y1 mpt }

func (r rect) w() mpt { return r.x1 - r.x0 }
func (r rect) h() mpt { return r.y1 - r.y0 }

// frame is a panel: its trim and the bleed beyond it on each side (0 on a side that has none, as the spine side of a
// panel on the wrap).
type frame struct {
	trim           rect
	bl, bb, br, bt mpt
}

func (f frame) painted() rect {
	return rect{f.trim.x0 - f.bl, f.trim.y0 - f.bb, f.trim.x1 + f.br, f.trim.y1 + f.bt}
}

// panelPlan is a panel as it will be drawn.
type panelPlan struct {
	part string
	mode string
	fr   frame
	// image: the picture's rectangle and the clip; the white or background fill is the painted area.
	img  *CoverImage
	pic  rect
	clip rect
	// dpi is the picture's resolution at its printed size (0 for no picture); the printed size in milli-points.
	dpi          float64
	lost         float64 // image side: the share of the picture that lies outside the region it covers (the bleed's included)
	printW       mpt
	printH       mpt
	bg           [3]uint8
	margin, edge []string // what a page panel's picture crosses: text margin, trim edge without bleed art
}

// ---- the plan --------------------------------------------------------------------------------

type coverPlan struct {
	cs     CoverSpec
	art    *CoverArt
	wrap   *CoverWrap
	legacy bool // no layout: the generated wrap exactly as it was
	rtl    bool
	prof   CoverProfile

	trimW, trimH, bleed, spine mpt
	wrapW, wrapH               mpt
	back, front                frame
	spineX0                    mpt
	spineText                  bool

	bp, fp panelPlan
	warns  []CoverWarn
	// refuse is why the file cannot be made (the plan itself is still worked out, for the measurements).
	refuse error
}

func inchesMpt(v float64) mpt { return mpt(math.Round(v * float64(inch))) }

func profileOrGeneric(cs CoverSpec) CoverProfile {
	if cs.Profile.Name != "" {
		return cs.Profile
	}
	return CoverProfiles["generic"]
}

// planCover works the cover out. It reads no picture's bytes.
func planCover(cs CoverSpec) (*coverPlan, error) {
	if cs.TrimW < inch || cs.TrimH < inch || cs.Bleed < 0 || cs.Spine < 0 {
		return nil, fmt.Errorf("publish: a cover needs a trim size and a non-negative spine and bleed")
	}
	p := &coverPlan{cs: cs, art: cs.Art, legacy: cs.Art == nil, rtl: cs.RTL && cs.Art != nil, prof: profileOrGeneric(cs),
		trimW: cs.TrimW, trimH: cs.TrimH, bleed: cs.Bleed, spine: cs.Spine}
	if p.art != nil && p.art.Shape == ArtWrap {
		w := p.art.Wrap
		if w == nil || w.Image.W <= 0 || w.Image.H <= 0 || w.Image.H > math.MaxInt32 || w.Image.W > math.MaxInt32 {
			return nil, fmt.Errorf("publish: a wrap cover needs its picture")
		}
		if w.FrontOnRight == p.rtl {
			return nil, fmt.Errorf("publish: the wrap was cut for a book read the other way")
		}
		p.wrap = w
		p.bleed = inchesMpt(w.BleedIn)
		p.wrapH = p.trimH + 2*p.bleed
		p.wrapW = (int64(w.Image.W)*p.wrapH + int64(w.Image.H)/2) / int64(w.Image.H)
		p.spine = p.wrapW - 2*p.bleed - 2*p.trimW
		if p.spine < 0 {
			return nil, fmt.Errorf("publish: the wrap picture is narrower than a back and a front")
		}
		if !cs.FrontOnly && math.Abs(float64(cs.Bleed-p.bleed)) > float64(inch)/1000 {
			p.warn("wrap", "cover_wrap_bleed", fmt.Sprintf("The wrap was made with a %.3f in bleed, not the %.3f in asked for: the wrap's own bleed is used.", w.BleedIn, float64(cs.Bleed)/float64(inch)))
		}
		if !cs.FrontOnly && cs.Spine > 0 && math.Abs(float64(p.spine-cs.Spine)) > float64(inch)/32 {
			pages := ""
			if cs.Pages > 0 {
				pages = fmt.Sprintf(" (%d pages of this paper)", cs.Pages)
			}
			p.warn("wrap", "cover_wrap_stale", fmt.Sprintf("The wrap's spine measures %.3f in, but the book's spine is %.3f in%s. The picture is placed as it is, never stretched: get a new wrap from your designer or the printer's template.",
				float64(p.spine)/float64(inch), float64(cs.Spine)/float64(inch), pages))
		}
	} else {
		p.wrapW = 2*p.bleed + 2*p.trimW + p.spine
		p.wrapH = p.trimH + 2*p.bleed
	}
	// the panels of page 1
	bl := p.bleed
	p.spineX0 = bl + p.trimW
	leftX0, rightX0 := bl, bl+p.trimW+p.spine
	tr := func(x0 mpt) rect { return rect{x0, bl, x0 + p.trimW, bl + p.trimH} }
	left := frame{trim: tr(leftX0), bl: p.bleed, bb: p.bleed, bt: p.bleed}
	right := frame{trim: tr(rightX0), br: p.bleed, bb: p.bleed, bt: p.bleed}
	if p.rtl {
		p.front, p.back = left, right
	} else {
		p.back, p.front = left, right
	}
	// A layout with a generated side cannot be made for a right-to-left book (type set left to right): the plan says so, and the
	// file is refused, but the measurements are still worked out. A cover with no layout is the generated cover as it always
	// was, whatever the language.
	if p.rtl && !cs.FrontOnly && p.art.Shape == ArtSides && (p.art.Front.Mode == PanelGenerated || p.art.Back.Mode == PanelGenerated) {
		p.refuse = ErrCoverRTL
		p.warn("", "cover_rtl_text", ErrCoverRTL.Error())
	}
	if p.wrap == nil {
		p.fp = p.place("front", p.panelOf("front"), p.front, p.bleed)
		if !cs.FrontOnly {
			p.bp = p.place("back", p.panelOf("back"), p.back, p.bleed)
		}
		p.checkPanels()
	} else {
		w := p.wrap
		p.fp = panelPlan{part: "wrap", mode: "wrap", img: &w.Image, dpi: w.DPI}
		p.checkWrap()
	}
	if !cs.FrontOnly {
		p.checkSpine()
		p.checkBarcode()
	}
	return p, nil
}

// CoverWarn is a Warn that says which part of the cover it is about: front, back, spine or wrap (empty for the cover as a
// whole).
type CoverWarn struct {
	Warn
	Part string
}

// legacySpine says the cover is the generated one with no profile named: its spine type is what it always was (a fixed size,
// whatever the pages), until a profile is asked for or a layout is in use.
func (p *coverPlan) legacySpine() bool { return p.legacy && !p.cs.ProfileGiven }

func (p *coverPlan) warn(part, code, msg string) {
	p.warns = append(p.warns, CoverWarn{Warn{code, msg}, part})
}

// plainWarns drops the parts.
func plainWarns(ws []CoverWarn) []Warn {
	out := make([]Warn, len(ws))
	for i, w := range ws {
		out[i] = w.Warn
	}
	return out
}

func (p *coverPlan) panelOf(part string) CoverPanel {
	if p.art == nil {
		if part == "back" {
			return CoverPanel{Mode: PanelGenerated}
		}
		return CoverPanel{Mode: PanelGenerated}
	}
	if part == "front" {
		return p.art.Front
	}
	b := p.art.Back
	if b.Mode == "" {
		b.Mode = PanelNone
	}
	return b
}

// place works out one panel on a frame: the picture's rectangle, its clip and its resolution. bleed is the bleed the
// picture is placed to cover (the whole of it, on every side of the trim: a panel on the wrap shows less of it, and the
// front alone shows all, so the front is the same picture on every page).
func (p *coverPlan) place(part string, panel CoverPanel, fr frame, bleed mpt) panelPlan {
	pp := panelPlan{part: part, mode: panel.Mode, fr: fr}
	painted := fr.painted()
	switch panel.Mode {
	case PanelImage:
		im := panel.Image
		if im == nil || im.W <= 0 || im.H <= 0 {
			pp.mode = PanelNone
			return pp
		}
		pp.img = im
		region := rect{fr.trim.x0 - bleed, fr.trim.y0 - bleed, fr.trim.x1 + bleed, fr.trim.y1 + bleed}
		rw, rh := region.w(), region.h()
		var w, h mpt
		if rw*int64(im.H) >= rh*int64(im.W) { // fills the width: the height overflows
			w = rw
			h = (rw*int64(im.H) + int64(im.W)/2) / int64(im.W)
		} else {
			h = rh
			w = (rh*int64(im.W) + int64(im.H)/2) / int64(im.H)
		}
		cx, cy := (region.x0+region.x1)/2, (region.y0+region.y1)/2
		x0, y0 := cx-w/2, cy-h/2
		pp.pic = rect{x0, y0, x0 + w, y0 + h}
		pp.clip = painted
		pp.printW, pp.printH = w, h
		pp.dpi = float64(im.W) / (float64(w) / float64(inch))
		pp.lost = 1 - float64(rw)*float64(rh)/(float64(w)*float64(h))
	case PanelPage:
		im, pl := panel.Image, panel.Place
		if im == nil || pl == nil || pl.CanvasW <= 0 || pl.CanvasH <= 0 || im.W <= 0 || im.H <= 0 {
			pp.mode = PanelNone
			return pp
		}
		pp.img = im
		pp.bg = bgOf(panel.Background, [3]uint8{0x4A, 0x47, 0x40})
		cw, ch := int64(pl.CanvasW), int64(pl.CanvasH)
		tw, th := fr.trim.w(), fr.trim.h()
		mx := func(v int) mpt { return fr.trim.x0 + (int64(v)*tw+cw/2)/cw }
		my := func(v int) mpt { return fr.trim.y1 - (int64(v)*th+ch/2)/ch }
		pp.pic = rect{mx(pl.Dst.Min.X), my(pl.Dst.Max.Y), mx(pl.Dst.Max.X), my(pl.Dst.Min.Y)}
		pp.clip = rect{mx(pl.Box.Min.X), my(pl.Box.Max.Y), mx(pl.Box.Max.X), my(pl.Box.Min.Y)}
		full := pl.Box.Min.X == 0 && pl.Box.Min.Y == 0 && pl.Box.Max.X == pl.CanvasW && pl.Box.Max.Y == pl.CanvasH
		if full { // no margin: the picture runs on into the bleed where it has pixels to
			pp.clip = painted
		}
		pp.printW, pp.printH = pp.pic.w(), pp.pic.h()
		pp.dpi = float64(im.W) / (float64(pp.printW) / float64(inch))
		// what the picture reaches
		vis := pl.Dst.Intersect(pl.Box)
		tm := float64(inchesMpt(p.prof.TextMarginIn))
		mxp, myp := tm*float64(cw)/float64(tw), tm*float64(ch)/float64(th)
		if float64(vis.Min.X) < mxp || float64(pl.CanvasW-vis.Max.X) < mxp || float64(vis.Min.Y) < myp || float64(pl.CanvasH-vis.Max.Y) < myp {
			pp.margin = append(pp.margin, "text")
		}
		if full && bleed > 0 {
			bx, by := float64(bleed)*float64(cw)/float64(tw), float64(bleed)*float64(ch)/float64(th)
			short := func(d, need float64) bool { return d <= 1 && d > -need+1 } // reaches the edge, with too little past it
			edge := func(name string, d, need float64) {
				if short(d, need) {
					pp.edge = append(pp.edge, name)
				}
			}
			outerLeft, outerRight := part == "back" && !p.rtl || part == "front" && p.rtl, part == "front" && !p.rtl || part == "back" && p.rtl
			edge("top", float64(pl.Dst.Min.Y), by)
			edge("bottom", float64(pl.CanvasH-pl.Dst.Max.Y), by)
			if outerLeft {
				edge("outer", float64(pl.Dst.Min.X), bx)
			}
			if outerRight {
				edge("outer", float64(pl.CanvasW-pl.Dst.Max.X), bx)
			}
		}
	}
	return pp
}

func bgOf(hex string, def [3]uint8) [3]uint8 {
	if r, g, b, ok := parseHex(hex); ok {
		return [3]uint8{r, g, b}
	}
	return def
}

// LowDPI is the resolution under which a picture is reported where it prints. Print asks for 300; a picture made at 300 dpi
// for the trim and then placed over the bleed lands at about 288, which is not worth a warning, so the line is drawn at 285.
const LowDPI = 285

func (p *coverPlan) lowRes(pp panelPlan) {
	if pp.img == nil || pp.dpi == 0 || pp.dpi >= LowDPI {
		return
	}
	wi, hi := float64(pp.printW)/float64(inch), float64(pp.printH)/float64(inch)
	tail := "It is never enlarged to fill the gap."
	if pp.mode == PanelImage {
		tail = "It is scaled up as little as will cover the panel and its bleed, so the printer gets it this soft."
	}
	p.warn(pp.part, "image_low_resolution", fmt.Sprintf("The %s picture is %.0f dpi at its printed size (%.2f by %.2f in); 300 is advised for print, and a picture of about %d by %d pixels gives it. %s",
		pp.part, pp.dpi, wi, hi, int(math.Ceil(wi*300)), int(math.Ceil(hi*300)), tail))
}

func (p *coverPlan) checkPanels() {
	panels := []panelPlan{p.fp, p.bp}
	if p.cs.FrontOnly {
		panels = panels[:1]
	}
	for _, pp := range panels {
		p.lowRes(pp)
		if pp.mode == PanelImage && pp.lost > 0.05 {
			p.warn(pp.part, "cover_print_cropped", fmt.Sprintf("To fill the panel and its bleed the %s picture is cut by about %.0f%% (the part past the bleed, from every side but the proportions' shorter one). Use a page side with fit inside to keep all of it, or a picture in the panel's proportions.", pp.part, pp.lost*100))
		}
		if len(pp.margin) > 0 {
			p.warn(pp.part, "cover_margin_crossed", fmt.Sprintf("The %s picture reaches into the %.3g in text-safe margin of the %s profile: keep anything that must not be trimmed (text, faces) inside it.", pp.part, p.prof.TextMarginIn, p.prof.Name))
		}
		if len(pp.edge) > 0 {
			p.warn(pp.part, "cover_bleed_missing", fmt.Sprintf("The %s picture reaches the trim edge (%s) with no art past it for the bleed: the bleed shows the page colour, and a cut a hair off would show it. Fill and crop the picture, or leave a margin.", pp.part, edgeList(pp.edge)))
		}
	}
}

func edgeList(e []string) string {
	seen := map[string]bool{}
	var out []string
	for _, x := range e {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	s := ""
	for i, x := range out {
		if i > 0 {
			s += ", "
		}
		s += x
	}
	return s
}

func (p *coverPlan) checkWrap() {
	w := p.wrap
	if w.DPI <= 0 || w.DPI >= LowDPI {
		return
	}
	if p.cs.FrontOnly { // the cover page is the front: what matters is what the front needs, not the whole wrap
		wi, hi := float64(p.trimW+p.bleed)/float64(inch), float64(p.trimH+2*p.bleed)/float64(inch)
		p.warn("wrap", "image_low_resolution", fmt.Sprintf("The front is cut from a wrap that is %.0f dpi; 300 is advised for print. The front (with its bleed) needs about %d by %d pixels at 300.", w.DPI, int(math.Ceil(wi*300)), int(math.Ceil(hi*300))))
		return
	}
	wi, hi := float64(p.wrapW)/float64(inch), float64(p.wrapH)/float64(inch)
	p.warn("wrap", "image_low_resolution", fmt.Sprintf("The wrap is %.0f dpi (%.2f by %.2f in); 300 is advised for print. A picture of about %d by %d pixels gives 300. It is never stretched.", w.DPI, wi, hi, int(math.Ceil(wi*300)), int(math.Ceil(hi*300))))
}

// spineTextOK decides whether the spine carries type, and says why not.
func (p *coverPlan) checkSpine() {
	cs := p.cs
	if p.wrap != nil { // the picture is the whole wrap: nothing is drawn on it
		return
	}
	switch {
	case p.rtl:
		p.warn("spine", "spine_rtl", "The spine is left without type: a right-to-left book's title cannot be set here. Put the title in the front and back pictures.")
	case p.spine < MinSpineText:
		p.warn("spine", "spine_narrow", fmt.Sprintf("The spine is %.3f in wide, too narrow for type (printers ask for about a quarter of an inch); the spine is left blank.", float64(p.spine)/float64(inch)))
	case cs.Pages > 0 && cs.Pages < p.prof.SpineTextFromPages && !p.legacySpine():
		p.warn("spine", "spine_few_pages", fmt.Sprintf("The book has %d pages, under the %d at which the %s profile puts type on the spine; the spine is left blank.", cs.Pages, p.prof.SpineTextFromPages, p.prof.Name))
	default:
		p.spineText = true
	}
}

// barcodePart is the part the barcode note is about: the back, or the wrap that holds it.
func barcodePart(p *coverPlan) string {
	if p.wrap != nil {
		return "wrap"
	}
	return "back"
}

func (p *coverPlan) checkBarcode() {
	if p.cs.Barcode || p.legacy {
		return
	}
	if p.wrap != nil || p.bp.mode == PanelImage || p.bp.mode == PanelPage {
		p.warn(barcodePart(p), "cover_barcode_area", fmt.Sprintf("Leave the lower right of the back clear: the printer prints its barcode in a %.3g by %.3g in box there (the %s profile), %.3g in from the trim. Nothing here can tell whether the art is clear; turn the barcode box on to see where it goes.",
			p.prof.BarcodeWIn, p.prof.BarcodeHIn, p.prof.Name, p.prof.BarcodeInsetIn))
	}
}

// barcodeRect is the barcode box on page 1, in the PDF's space.
func (p *coverPlan) barcodeRect() rect {
	if p.legacySpine() { // the generated cover with no profile named: where it has always drawn it (12 per cent of the width, halved)
		margin := p.trimW * 12 / 100
		x1 := p.back.trim.x1 - margin/2
		y0 := p.back.trim.y0 + margin/2
		return rect{x1 - 2*inch, y0, x1, y0 + inch*6/5}
	}
	in := inchesMpt(p.prof.BarcodeInsetIn)
	bw, bh := inchesMpt(p.prof.BarcodeWIn), inchesMpt(p.prof.BarcodeHIn)
	x1 := p.back.trim.x1 - in
	y0 := p.back.trim.y0 + in
	return rect{x1 - bw, y0, x1, y0 + bh}
}

// CoverPicture is one placed picture as the plan has it: where it prints and how sharp it comes out there.
type CoverPicture struct {
	Part string // front, back or wrap
	DPI  float64
	// PrintWIn and PrintHIn are the size the whole picture prints at, in inches (for an image side, the part that is
	// cropped away included).
	PrintWIn, PrintHIn float64
}

// CoverReport is what a cover comes to, worked out without drawing it.
type CoverReport struct {
	Warnings []CoverWarn
	Pictures []CoverPicture
	// Refuse is why the file cannot be made (ErrCoverRTL), nil when it can: the report is worked out either way.
	Refuse error
	// SpineType says the spine carries type; SpineIn is the spine the cover has (a wrap's, as measured).
	SpineType bool
	SpineIn   float64
	// WrapWIn and WrapHIn are page 1's size in inches.
	WrapWIn, WrapHIn float64
	// BarcodeBox is the barcode box on page 1 in points from the left and bottom edges (x0, y0, x1, y1).
	BarcodeBox [4]float64
	// Spine type: when it is left off, why (narrow, few_pages, rtl, wrap) and "" when it is set.
	SpineTypeWhy string
}

// PlanCover runs the plan of a cover without drawing it. The pictures' bytes are not needed, only their sizes, so the
// cover check and a dry run agree with the file.
func PlanCover(cs CoverSpec) (*CoverReport, error) {
	p, err := planCover(cs)
	if err != nil {
		return nil, err
	}
	bb := p.barcodeRect()
	r := &CoverReport{Refuse: p.refuse, BarcodeBox: [4]float64{float64(bb.x0) / 1000, float64(bb.y0) / 1000, float64(bb.x1) / 1000, float64(bb.y1) / 1000}, Warnings: p.warns, SpineType: p.spineText, SpineIn: float64(p.spine) / float64(inch), WrapWIn: float64(p.wrapW) / float64(inch), WrapHIn: float64(p.wrapH) / float64(inch)}
	pics := []panelPlan{p.fp}
	if p.wrap == nil && !cs.FrontOnly {
		pics = append(pics, p.bp)
	}
	for _, pp := range pics {
		if pp.img != nil {
			r.Pictures = append(r.Pictures, CoverPicture{Part: pp.part, DPI: pp.dpi, PrintWIn: float64(pp.printW) / float64(inch), PrintHIn: float64(pp.printH) / float64(inch)})
		}
	}
	switch {
	case p.spineText:
	case p.wrap != nil:
		r.SpineTypeWhy = "wrap"
	case p.rtl:
		r.SpineTypeWhy = "rtl"
	case p.spine < MinSpineText:
		r.SpineTypeWhy = "narrow"
	default:
		r.SpineTypeWhy = "few_pages"
	}
	if p.wrap != nil {
		r.Pictures = []CoverPicture{{Part: "wrap", DPI: p.wrap.DPI, PrintWIn: r.WrapWIn, PrintHIn: r.WrapHIn}}
	}
	return r, nil
}

// CoverWarnings is PlanCover's warnings, without their parts.
func CoverWarnings(cs CoverSpec) ([]Warn, error) {
	r, err := PlanCover(cs)
	if err != nil {
		return nil, err
	}
	return plainWarns(r.Warnings), nil
}

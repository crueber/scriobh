package coverlayout

import (
	"fmt"
	"image"
	"math"
	"strings"

	"github.com/crueber/scriobh/internal/publish"
)

// This file is the arithmetic of a cover: the trim's proportions, the measuring of a wrap picture and the cut of
// the front from it, and the placing of a picture on a page. It touches no database and decodes nothing; every
// function takes sizes and returns sizes, so each is tested with exact pixel expectations.

// Trims are the page sizes a cover may be made for, the print presets' sizes (the PDF writer's own names).
var Trims = []string{"6x9", "5.5x8.5", "5.25x8", "5x8", "a5", "letter", "a4"}

// DefaultTrim is the trim of a layout that names none.
const DefaultTrim = "6x9"

// Limits of a wrap.
const (
	// MinWrapDPI is the lowest resolution a wrap picture may measure at. Under it the picture is not a wrap for that
	// trim (a thumbnail, or a trim that is far too large for it); between this and 300 it is a warning.
	MinWrapDPI = 150
	// MaxSpineIn is the widest spine a book could need. A wider measured spine means the trim is wrong, because with the
	// height fixed any error in the trim's width moves into the measured spine.
	MaxSpineIn = 2.5
	// DefaultBleedIn is the bleed of a wrap that names none.
	DefaultBleedIn = 0.125
	// MaxBleedIn bounds the bleed a wrap may declare.
	MaxBleedIn = 0.5
	// PrintDPI is the resolution a print cover is asked to have.
	PrintDPI = 300
	// MaxDerivedSide is the longest side of the front and back derivatives (KDP's and Vellum's figure).
	MaxDerivedSide = 2560
	// ThumbWidth is the width of the thumbnail derivative.
	ThumbWidth = 480
)

// TrimIn is a trim's size in inches.
type TrimIn struct {
	Name string
	W, H float64
}

// Aspect is width over height.
func (t TrimIn) Aspect() float64 { return t.W / t.H }

// LookupTrim finds a trim by name.
func LookupTrim(name string) (TrimIn, bool) {
	w, h, ok := publish.PageSize(name)
	if !ok {
		return TrimIn{}, false
	}
	return TrimIn{Name: name, W: float64(w) / 72000, H: float64(h) / 72000}, true
}

// Refusal is a picture or a layout that is turned away, with the sentence that says why. Code is a stable token.
type Refusal struct {
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Message }

// roundPx rounds half up with a margin against the error of a division, so 1837.4999999 is 1837.5 is 1838.
func roundPx(v float64) int { return int(math.Floor(v + 0.5 + 1e-9)) }

// WrapGeometry is a wrap picture measured against a trim and a bleed.
type WrapGeometry struct {
	// Pixels of the picture.
	Width, Height int
	// DPI is the picture's resolution: its height over the trim height plus two bleeds.
	DPI float64
	// SpineIn is the measured spine: the width in inches less two trim widths and two bleeds.
	SpineIn float64
	// WidthIn and HeightIn are the picture's size in inches.
	WidthIn, HeightIn float64
	// BleedPx is the bleed in pixels, on the outer edges.
	BleedPx int
	// Front and Back are the panels cut at the trim, without bleed, in picture pixels; FrontBleed and BackBleed include
	// the outer bleed (the print cover places the picture at that size). Spine is the strip between the two panels.
	Front, Back           image.Rectangle
	FrontBleed, BackBleed image.Rectangle
	Spine                 image.Rectangle
	// FrontOnRight is true for a book read left to right.
	FrontOnRight bool
}

// MeasureWrap measures a wrap picture of w by h pixels made for a trim and a bleed, and cuts the panels. rtl puts the
// front on the left (a right-to-left book opens from the other side). It refuses a picture that cannot be a wrap for
// that trim: a resolution under MinWrapDPI, a spine under zero or over MaxSpineIn.
func MeasureWrap(w, h int, t TrimIn, bleedIn float64, rtl bool) (WrapGeometry, error) {
	if w <= 0 || h <= 0 {
		return WrapGeometry{}, &Refusal{"cover_wrap_empty", "The picture has no size."}
	}
	if bleedIn < 0 || bleedIn > MaxBleedIn || math.IsNaN(bleedIn) {
		return WrapGeometry{}, &Refusal{"cover_wrap_bleed", fmt.Sprintf("The bleed is 0 to %g inches.", MaxBleedIn)}
	}
	g := WrapGeometry{Width: w, Height: h, FrontOnRight: !rtl}
	g.HeightIn = t.H + 2*bleedIn
	g.DPI = float64(h) / g.HeightIn
	if g.DPI < MinWrapDPI {
		return g, &Refusal{"cover_wrap_low_dpi", fmt.Sprintf("At %s with a %g in bleed this picture is only %.0f dpi, under the %d the printers accept. It is not a wrap for this trim: check the trim, or send a larger picture.",
			trimLabel(t), bleedIn, g.DPI, MinWrapDPI)}
	}
	g.WidthIn = float64(w) / g.DPI
	g.SpineIn = math.Round((g.WidthIn-2*t.W-2*bleedIn)*1e4) / 1e4 // to a ten-thousandth of an inch: not the noise of a division
	// A hair under zero is the rounding of a picture whose size is a whole number of pixels, not a trim that is too wide.
	if g.SpineIn < -0.5/g.DPI {
		return g, &Refusal{"cover_wrap_spine_negative", fmt.Sprintf("This picture is %.2f in wide at its height, but a back, a front and two bleeds of %s need %.2f in: the spine would be %.2f in. It is not a wrap for this trim: check the trim and the bleed.",
			g.WidthIn, trimLabel(t), 2*t.W+2*bleedIn, g.SpineIn)}
	}
	if g.SpineIn > MaxSpineIn {
		return g, &Refusal{"cover_wrap_spine_wide", fmt.Sprintf("The spine would be %.2f in wide, wider than any book needs (%.1f in at most). The picture is probably made for a larger trim than %s: check the trim.",
			g.SpineIn, MaxSpineIn, trimLabel(t))}
	}
	if g.SpineIn < 0 {
		g.SpineIn = 0
	}
	bleedPx := bleedIn * g.DPI
	g.BleedPx = roundPx(bleedPx)
	panelW := t.W * g.DPI
	fw, fh := float64(w), float64(h)
	// Every edge is rounded from its own exact position, never as a size from another edge: the panels then have the
	// size the trim says to within the pixel, the bleeds do not each lose half a pixel to the same rounding, and the
	// panels and the spine tile the picture.
	top, bottom := roundPx(bleedPx), roundPx(fh-bleedPx)
	trimL, trimR := roundPx(bleedPx), roundPx(fw-bleedPx)
	backEdge := roundPx(bleedPx + panelW)       // the right edge of the left panel
	frontEdge := roundPx(fw - bleedPx - panelW) // the left edge of the right panel
	leftPanelTrim := image.Rect(trimL, top, backEdge, bottom)
	rightPanelTrim := image.Rect(frontEdge, top, trimR, bottom)
	leftBleed := image.Rect(0, 0, backEdge, h)
	rightBleed := image.Rect(frontEdge, 0, w, h)
	g.Spine = image.Rect(backEdge, 0, frontEdge, h)
	if g.FrontOnRight {
		g.Front, g.Back, g.FrontBleed, g.BackBleed = rightPanelTrim, leftPanelTrim, rightBleed, leftBleed
	} else {
		g.Front, g.Back, g.FrontBleed, g.BackBleed = leftPanelTrim, rightPanelTrim, leftBleed, rightBleed
	}
	return g, nil
}

func trimLabel(t TrimIn) string {
	switch t.Name {
	case "a5", "a4":
		return strings.ToUpper(t.Name)
	case "letter":
		return "US Letter"
	}
	return t.Name + " in"
}

// ---- placing a picture on a page ---------------------------------------------------------------

// Positions are the nine places a picture may sit on its page, row by row.
var Positions = []string{"top-left", "top", "top-right", "left", "center", "right", "bottom-left", "bottom", "bottom-right"}

// Fits of a page side.
const (
	FitContain = "contain" // fit inside
	FitCover   = "cover"   // fill and crop
	FitActual  = "actual"  // at 300 dpi on the page
)

// anchor is a position's place on the unit square: 0, 0.5 or 1 on each axis.
func anchor(pos string) (fx, fy float64) {
	fx, fy = 0.5, 0.5
	switch pos {
	case "top-left", "left", "bottom-left":
		fx = 0
	case "top-right", "right", "bottom-right":
		fx = 1
	}
	switch pos {
	case "top-left", "top", "top-right":
		fy = 0
	case "bottom-left", "bottom", "bottom-right":
		fy = 1
	}
	return fx, fy
}

// CanvasSize is the page of a side, in pixels: the trim's proportions with the long side at MaxDerivedSide.
func CanvasSize(t TrimIn) (w, h int) {
	if t.H >= t.W {
		return roundPx(MaxDerivedSide * t.W / t.H), MaxDerivedSide
	}
	return MaxDerivedSide, roundPx(MaxDerivedSide * t.H / t.W)
}

// PagePlacement is where a picture lands on a page side, in canvas pixels.
type PagePlacement struct {
	CanvasW, CanvasH int
	// Box is the part of the page inside the margin.
	Box image.Rectangle
	// Dst is where the whole picture would land (it may extend past Box for fills and actual size); Visible is the part
	// of it inside Box, and Src the matching part of the picture, in the picture's pixels.
	Dst, Visible, Src image.Rectangle
	// Clamped says the picture is smaller than the fit would make it, and sits at its own size: a cover never enlarges a
	// picture (it would only show the pixels bigger and softer).
	Clamped bool
}

// PlacePage works out where a picture of pw by ph pixels goes on a page of the trim, for a page side's fit, scale (10
// to 100 percent of the fit), position and margin (inches). The picture is scaled uniformly and clipped to the box
// inside the margin.
func PlacePage(pw, ph int, t TrimIn, s Side) (PagePlacement, error) {
	cw, ch := CanvasSize(t)
	pl := PagePlacement{CanvasW: cw, CanvasH: ch}
	if pw <= 0 || ph <= 0 {
		return pl, &Refusal{"cover_picture_empty", "The picture has no size."}
	}
	ppi := float64(cw) / t.W // canvas pixels per inch of the page
	m := roundPx(s.MarginIn * ppi)
	if 2*m >= cw || 2*m >= ch {
		return pl, &Refusal{"cover_margin_large", "The margin leaves no room for the picture."}
	}
	pl.Box = image.Rect(m, m, cw-m, ch-m)
	bw, bh := float64(pl.Box.Dx()), float64(pl.Box.Dy())
	var k float64 // canvas pixels per picture pixel
	switch s.Fit {
	case FitCover:
		k = math.Max(bw/float64(pw), bh/float64(ph))
	case FitActual:
		k = ppi / PrintDPI
	default: // contain
		k = math.Min(bw/float64(pw), bh/float64(ph))
	}
	if k > 1 && s.Fit != FitActual { // one picture pixel to one page pixel at most: never enlarged
		k, pl.Clamped = 1, true
	}
	k *= float64(s.Scale) / 100
	dw, dh := float64(pw)*k, float64(ph)*k
	if dw > 8*float64(cw) || dh > 8*float64(ch) {
		return pl, &Refusal{"cover_picture_extreme", "This picture's proportions are too far from the page to place it at this size."}
	}
	fx, fy := anchor(s.Position)
	x0 := float64(pl.Box.Min.X) + (bw-dw)*fx
	y0 := float64(pl.Box.Min.Y) + (bh-dh)*fy
	pl.Dst = image.Rect(roundPx(x0), roundPx(y0), roundPx(x0+dw), roundPx(y0+dh))
	if pl.Dst.Dx() < 1 || pl.Dst.Dy() < 1 {
		return pl, &Refusal{"cover_picture_extreme", "This picture would be less than a pixel at this size."}
	}
	pl.Visible = pl.Dst.Intersect(pl.Box)
	// The source rectangle is the visible part mapped back; the scale is exact for what fits the box and is rounded at
	// the edges of a clipped one.
	sx := float64(pw) / float64(pl.Dst.Dx())
	sy := float64(ph) / float64(pl.Dst.Dy())
	pl.Src = image.Rect(
		int(math.Round(float64(pl.Visible.Min.X-pl.Dst.Min.X)*sx)), int(math.Round(float64(pl.Visible.Min.Y-pl.Dst.Min.Y)*sy)),
		int(math.Round(float64(pl.Visible.Max.X-pl.Dst.Min.X)*sx)), int(math.Round(float64(pl.Visible.Max.Y-pl.Dst.Min.Y)*sy)))
	pl.Src = pl.Src.Intersect(image.Rect(0, 0, pw, ph))
	return pl, nil
}

// ---- an image side: fill and crop --------------------------------------------------------------

// CropToAspect is the part of a picture of pw by ph that has the trim's proportions, cut from its centre.
func CropToAspect(pw, ph int, t TrimIn) image.Rectangle {
	want := t.Aspect()
	have := float64(pw) / float64(ph)
	switch {
	case have > want: // too wide: lose a sliver left and right
		cw := roundPx(float64(ph) * want)
		if cw < 1 {
			cw = 1
		}
		x0 := (pw - cw) / 2
		return image.Rect(x0, 0, x0+cw, ph)
	case have < want: // too tall: lose a sliver top and bottom
		chh := roundPx(float64(pw) / want)
		if chh < 1 {
			chh = 1
		}
		y0 := (ph - chh) / 2
		return image.Rect(0, y0, pw, y0+chh)
	}
	return image.Rect(0, 0, pw, ph)
}

// CropLoss is the share of a picture's area the fill-and-crop cuts away, 0 to 1.
func CropLoss(pw, ph int, t TrimIn) float64 {
	r := CropToAspect(pw, ph, t)
	return 1 - float64(r.Dx()*r.Dy())/float64(pw*ph)
}

// FitWithin scales a w by h picture down so its long side is at most max, never up.
func FitWithin(w, h, max int) (int, int) {
	long := w
	if h > long {
		long = h
	}
	if long <= max {
		return w, h
	}
	f := float64(max) / float64(long)
	nw, nh := roundPx(float64(w)*f), roundPx(float64(h)*f)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	return nw, nh
}

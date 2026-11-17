package coverlayout

import (
	"errors"
	"image"
	"testing"
)

func trim(t *testing.T, name string) TrimIn {
	t.Helper()
	tr, ok := LookupTrim(name)
	if !ok {
		t.Fatalf("no trim %s", name)
	}
	return tr
}

// A 6x9 wrap with a 0.125 in bleed and a half-inch spine at 300 dpi is 3825 by 2775 pixels. Every edge is worked out
// by hand here: the front is the right-hand 1800 px at the trim, the back the left-hand 1800, the spine 150.
func TestWrapMeasuresAndCutsToTheExactPixel(t *testing.T) {
	g, err := MeasureWrap(3825, 2775, trim(t, "6x9"), 0.125, false)
	if err != nil {
		t.Fatal(err)
	}
	if g.DPI != 300 || g.SpineIn != 0.5 || g.WidthIn != 12.75 || g.HeightIn != 9.25 {
		t.Errorf("dpi %v spine %v width %v height %v", g.DPI, g.SpineIn, g.WidthIn, g.HeightIn)
	}
	want := map[string]image.Rectangle{
		"front": image.Rect(1988, 38, 3788, 2738), "back": image.Rect(38, 38, 1838, 2738), "spine": image.Rect(1838, 0, 1988, 2775),
		"front with bleed": image.Rect(1988, 0, 3825, 2775), "back with bleed": image.Rect(0, 0, 1838, 2775),
	}
	got := map[string]image.Rectangle{"front": g.Front, "back": g.Back, "spine": g.Spine, "front with bleed": g.FrontBleed, "back with bleed": g.BackBleed}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %v, want %v", k, got[k], w)
		}
	}
	if g.Front.Dx() != 1800 || g.Front.Dy() != 2700 {
		t.Errorf("the front is %dx%d, a 6x9 page at 300 dpi is 1800x2700", g.Front.Dx(), g.Front.Dy())
	}
}

// A right-to-left book opens from the other side: the front is on the left and the back on the right.
func TestWrapFrontFollowsTheTextDirection(t *testing.T) {
	g, err := MeasureWrap(3825, 2775, trim(t, "6x9"), 0.125, true)
	if err != nil {
		t.Fatal(err)
	}
	if g.FrontOnRight || g.Front != image.Rect(38, 38, 1838, 2738) || g.Back != image.Rect(1988, 38, 3788, 2738) {
		t.Errorf("front %v back %v onRight %v", g.Front, g.Back, g.FrontOnRight)
	}
	if g.FrontBleed != image.Rect(0, 0, 1838, 2775) {
		t.Errorf("the front's bleed is the outer one: %v", g.FrontBleed)
	}
}

func TestWrapWithNoBleedAndAnotherTrim(t *testing.T) {
	// 5.5x8.5 at 300 dpi, no bleed, 0.3 in spine: 11.3 in by 8.5 in
	g, err := MeasureWrap(3390, 2550, trim(t, "5.5x8.5"), 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if g.DPI != 300 || g.SpineIn != 0.3 || g.Back != image.Rect(0, 0, 1650, 2550) || g.Front != image.Rect(1740, 0, 3390, 2550) || g.Spine != image.Rect(1650, 0, 1740, 2550) {
		t.Errorf("%+v", g)
	}
}

func TestWrapRefusals(t *testing.T) {
	tr := trim(t, "6x9")
	for _, c := range []struct {
		name       string
		w, h       int
		code       string
		bleed      float64
		wantNoCode bool
	}{
		{"a spine under zero", 3500, 2775, "cover_wrap_spine_negative", 0.125, false},
		{"a spine over 2.5 in", 4455 + 30, 2775, "cover_wrap_spine_wide", 0.125, false},
		{"under 150 dpi", 1700, 1300, "cover_wrap_low_dpi", 0.125, false},
		{"a spine of exactly zero is a wrap with a flat fold", 3675, 2775, "", 0.125, true},
		{"2.5 in exactly is allowed", 4425, 2775, "", 0.125, true},
		{"150 dpi exactly is allowed", 1912, 1388, "", 0.125, true}, // 1387.5/9.25 = 150 dpi; 11.1: spine = 12.7467-12.25 = 0.4967
	} {
		_, err := MeasureWrap(c.w, c.h, tr, c.bleed, false)
		var r *Refusal
		switch {
		case c.wantNoCode && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case !c.wantNoCode && (!errors.As(err, &r) || r.Code != c.code || r.Message == ""):
			t.Errorf("%s: %v, want %s", c.name, err, c.code)
		}
	}
	if _, err := MeasureWrap(3825, 2775, tr, 0.6, false); err == nil {
		t.Error("a bleed over half an inch is refused")
	}
}

func TestCanvasSizeAndFitWithin(t *testing.T) {
	for _, c := range []struct {
		trim string
		w, h int
	}{{"6x9", 1707, 2560}, {"5x8", 1600, 2560}, {"5.5x8.5", 1656, 2560}, {"letter", 1978, 2560}, {"a4", 1810, 2560}} {
		if w, h := CanvasSize(trim(t, c.trim)); w != c.w || h != c.h {
			t.Errorf("%s: %dx%d, want %dx%d", c.trim, w, h, c.w, c.h)
		}
	}
	if w, h := FitWithin(3000, 4500, 2560); w != 1707 || h != 2560 {
		t.Errorf("%dx%d", w, h)
	}
	if w, h := FitWithin(1600, 2400, 2560); w != 1600 || h != 2400 {
		t.Errorf("a picture that is small enough is never enlarged: %dx%d", w, h)
	}
	if w, h := FitWithin(4000, 100, 2560); w != 2560 || h != 64 {
		t.Errorf("%dx%d", w, h)
	}
}

// An image side is filled and cropped from the centre: a picture taller than the page loses a sliver top and bottom,
// a wider one left and right.
func TestImageSideCropsFromTheCentre(t *testing.T) {
	tr := trim(t, "6x9") // 2:3
	for _, c := range []struct {
		w, h int
		want image.Rectangle
		loss float64
	}{
		{1600, 2560, image.Rect(0, 80, 1600, 2480), 0.063},  // an ebook cover, 1:1.6, is a little taller than 2:3
		{1600, 1000, image.Rect(466, 0, 1133, 1000), 0.583}, // a landscape picture
		{1200, 1800, image.Rect(0, 0, 1200, 1800), 0},       // exactly the page
	} {
		got := CropToAspect(c.w, c.h, tr)
		if got != c.want {
			t.Errorf("%dx%d: %v, want %v", c.w, c.h, got, c.want)
		}
		if loss := CropLoss(c.w, c.h, tr); loss < c.loss-0.001 || loss > c.loss+0.001 {
			t.Errorf("%dx%d: loss %.3f, want %.3f", c.w, c.h, loss, c.loss)
		}
	}
}

func page(fit, pos string, scale int, margin float64) Side {
	return Side{Mode: ModePage, Fit: fit, Position: pos, Scale: scale, MarginIn: margin, Background: "#2F5D50"}
}

// A page side places the picture on the trim's page: 6x9 is 1707x2560 px, 284.5 px to the inch.
func TestPagePlacement(t *testing.T) {
	tr := trim(t, "6x9")
	for _, c := range []struct {
		name         string
		pw, ph       int
		side         Side
		dst, visible image.Rectangle
		src          image.Rectangle
	}{
		// a picture is never enlarged: a 1000 px square is 1000 page pixels whatever the fit would make it
		{"a small square, fit inside, centred: not enlarged", 1000, 1000, page("contain", "center", 100, 0),
			image.Rect(354, 780, 1354, 1780), image.Rect(354, 780, 1354, 1780), image.Rect(0, 0, 1000, 1000)},
		{"a small square, fill and crop: not enlarged either", 1000, 1000, page("cover", "center", 100, 0),
			image.Rect(354, 780, 1354, 1780), image.Rect(354, 780, 1354, 1780), image.Rect(0, 0, 1000, 1000)},
		{"half size, top left", 1000, 1000, page("contain", "top-left", 50, 0),
			image.Rect(0, 0, 500, 500), image.Rect(0, 0, 500, 500), image.Rect(0, 0, 1000, 1000)},
		{"a small square inside a half-inch margin, bottom right", 1000, 1000, page("contain", "bottom-right", 100, 0.5),
			image.Rect(565, 1418, 1565, 2418), image.Rect(565, 1418, 1565, 2418), image.Rect(0, 0, 1000, 1000)},
		{"a large square, fit inside, is made smaller to fit", 3000, 3000, page("contain", "center", 100, 0),
			image.Rect(0, 427, 1707, 2134), image.Rect(0, 427, 1707, 2134), image.Rect(0, 0, 3000, 3000)},
		{"a large square, fill and crop: the sides are cut", 3000, 3000, page("cover", "center", 100, 0),
			image.Rect(-426, 0, 2134, 2560), image.Rect(0, 0, 1707, 2560), image.Rect(499, 0, 2500, 3000)},
		{"actual size is 300 dpi: a 1200 px picture is 4 in, 1138 px of the page", 1200, 1200, page("actual", "center", 100, 0),
			image.Rect(285, 711, 1423, 1849), image.Rect(285, 711, 1423, 1849), image.Rect(0, 0, 1200, 1200)},
	} {
		pl, err := PlacePage(c.pw, c.ph, tr, c.side)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if pl.CanvasW != 1707 || pl.CanvasH != 2560 {
			t.Errorf("%s: canvas %dx%d", c.name, pl.CanvasW, pl.CanvasH)
		}
		if pl.Dst != c.dst || pl.Visible != c.visible || pl.Src != c.src {
			t.Errorf("%s:\n  dst     %v want %v\n  visible %v want %v\n  src     %v want %v", c.name, pl.Dst, c.dst, pl.Visible, c.visible, pl.Src, c.src)
		}
	}
	if pl, _ := PlacePage(1000, 1000, tr, page("contain", "center", 100, 0)); !pl.Clamped {
		t.Error("a picture that was not enlarged does not say so")
	}
	if pl, _ := PlacePage(3000, 3000, tr, page("contain", "center", 100, 0)); pl.Clamped {
		t.Error("a picture that was made smaller says it was not enlarged")
	}
	if _, err := PlacePage(10, 10, tr, page("contain", "center", 100, 3)); err == nil {
		t.Error("a margin that leaves no room is refused")
	}
	if _, err := PlacePage(1, 100000, tr, page("cover", "center", 100, 0)); err == nil {
		t.Error("an extreme picture is refused rather than made huge")
	}
}

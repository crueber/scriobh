package publish

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// ---- pictures ----------------------------------------------------------------------------------

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{uint8(x * 255 / w), 90, uint8(y * 255 / h), 255})
		}
	}
	return img
}

func testJPEG(t testing.TB, w, h int) *CoverImage {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, gradient(w, h), &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return &CoverImage{Data: b.Bytes(), Ext: "jpg", Hash: fmt.Sprintf("jpg-%d-%d", w, h), W: w, H: h}
}

func testPNG(t testing.TB, w, h int) *CoverImage {
	var b bytes.Buffer
	if err := png.Encode(&b, gradient(w, h)); err != nil {
		t.Fatal(err)
	}
	return &CoverImage{Data: b.Bytes(), Ext: "png", Hash: fmt.Sprintf("png-%d-%d", w, h), W: w, H: h}
}

// ---- a cover to look at ------------------------------------------------------------------------

type trimCase struct {
	name string
	w, h mpt
}

var coverTrims = []trimCase{{"6x9", 6 * inch, 9 * inch}, {"5.5x8.5", 11 * inch / 2, 17 * inch / 2}, {"a4", 595276, 841890}, {"letter", 612000, 792000}}

func baseSpec(tc trimCase, prof string, spineIn float64, pages int) CoverSpec {
	p, _ := LookupCoverProfile(prof)
	return CoverSpec{Title: "The Salt Road", Subtitle: "A novel of the coast", Author: "Máire Ní Bhriain", Kind: "novel", Color: "#2F5D50",
		TrimW: tc.w, TrimH: tc.h, Spine: inchesMpt(spineIn), Bleed: inch / 8, Language: "en", Identifier: "id", InputHash: "h",
		Profile: p, ProfileGiven: prof != "", Pages: pages, Part: "all"}
}

// mkWrap makes the wrap picture's measures as coverlayout.MeasureWrap does (its rounding is tested there): a picture made
// at dpi for the trim, the bleed and a spine of spineIn.
func mkWrap(t testing.TB, tc trimCase, bleedIn, spineIn, dpi float64, jpg bool) *CoverWrap {
	tw, th := float64(tc.w)/float64(inch), float64(tc.h)/float64(inch)
	w := int(math.Round((2*tw + 2*bleedIn + spineIn) * dpi))
	h := int(math.Round((th + 2*bleedIn) * dpi))
	im := testPNG(t, w, h)
	if jpg {
		im = testJPEG(t, w, h)
	}
	g := &CoverWrap{Image: *im, BleedIn: bleedIn, DPI: float64(h) / (th + 2*bleedIn), SpineIn: spineIn, FrontOnRight: true}
	bp := bleedIn * g.DPI
	pw := tw * g.DPI
	r := func(v float64) int { return int(math.Floor(v + 0.5 + 1e-9)) }
	top, bottom := r(bp), r(float64(h)-bp)
	trimL, trimR := r(bp), r(float64(w)-bp)
	back, front := r(bp+pw), r(float64(w)-bp-pw)
	g.Back = image.Rect(trimL, top, back, bottom)
	g.Front = image.Rect(front, top, trimR, bottom)
	g.BackBleed = image.Rect(0, 0, back, h)
	g.FrontBleed = image.Rect(front, 0, w, h)
	g.Spine = image.Rect(back, 0, front, h)
	return g
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func pts(v mpt) float64 { return float64(v) / 1000 }

func writeCover(t *testing.T, cs CoverSpec) (*check.PDFDoc, []byte, []Warn) {
	t.Helper()
	b, ws, err := WriteCoverPDF(cs)
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(b)
	for _, p := range probs {
		t.Error("the checker:", p)
	}
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" { // for looking at: pdftoppm -r 50 file.pdf out
		_ = os.WriteFile(filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "_")+".pdf"), b, 0o644)
	}
	return doc, b, ws
}

func warnCodes(ws []Warn) string {
	var c []string
	for _, w := range ws {
		c = append(c, w.Code)
	}
	return strings.Join(c, ",")
}

func hasWarn(ws []Warn, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// A cover of sides: an image front (full bleed, cropped from the centre), a page back (the picture on its page, over a
// background), for every profile and trim. The page boxes, the picture's placement and the pictures' own bytes are
// asserted.
func TestSidesCoverPlacesPicturesExactly(t *testing.T) {
	for _, tc := range coverTrims {
		for _, prof := range []string{"kdp", "ingram", "generic"} {
			t.Run(tc.name+"/"+prof, func(t *testing.T) {
				cs := baseSpec(tc, prof, 0.75, 300)
				// the front picture is 3:4, far from the panel's proportions, so it is cropped top and bottom
				front := testJPEG(t, 1800, 2400)
				back := testPNG(t, 800, 1000)
				cs.Art = &CoverArt{Shape: ArtSides,
					Front: CoverPanel{Mode: PanelImage, Image: front},
					Back: CoverPanel{Mode: PanelPage, Image: back, Background: "#E8E1D5",
						Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: image.Rect(128, 128, 1579, 2432), Dst: image.Rect(300, 600, 1100, 1600)}}}
				doc, _, ws := writeCover(t, cs)
				if len(doc.Pages) != 3 {
					t.Fatalf("%d pages, want the wrap, the front and the proof", len(doc.Pages))
				}
				bl := pts(cs.Bleed)
				tw, th := pts(cs.TrimW), pts(cs.TrimH)
				sp := pts(cs.Spine)
				// page 1: the wrap, with the boxes of a printer's file
				p1 := doc.Pages[0]
				wantW, wantH := 2*bl+2*tw+sp, th+2*bl
				if !near(p1.W, wantW, 0.001) || !near(p1.H, wantH, 0.001) {
					t.Errorf("page 1 is %vx%v, want %vx%v", p1.W, p1.H, wantW, wantH)
				}
				if len(p1.Trim) != 4 || !near(p1.Trim[0], bl, 0.001) || !near(p1.Trim[1], bl, 0.001) || !near(p1.Trim[2], wantW-bl, 0.001) || !near(p1.Trim[3], wantH-bl, 0.001) {
					t.Errorf("trim box %v", p1.Trim)
				}
				if len(p1.Bleed) != 4 || p1.Bleed[0] != 0 || p1.Bleed[1] != 0 || !near(p1.Bleed[2], wantW, 0.001) || !near(p1.Bleed[3], wantH, 0.001) {
					t.Errorf("bleed box %v", p1.Bleed)
				}
				// the front: the picture covers the front's trim plus the bleed on every side, centred on the trim, and is
				// clipped to the front panel with the bleed on its outer edges only
				fx0 := bl + tw + sp // the front's trim left
				region := [4]float64{fx0 - bl, bl - bl, fx0 + tw + bl, bl + th + bl}
				var fd, bd *check.PDFDraw
				for i := range p1.Draws {
					switch p1.Draws[i].Image.ColorSpace {
					}
					if p1.Draws[i].Image.Filter == "DCTDecode" {
						fd = &p1.Draws[i]
					} else {
						bd = &p1.Draws[i]
					}
				}
				if fd == nil || bd == nil {
					t.Fatalf("draws: %+v", p1.Draws)
				}
				r := fd.Rect()
				wantPicW := region[2] - region[0]
				wantPicH := wantPicW * 2400 / 1800
				if wantPicH < region[3]-region[1] { // the panel is the taller: the picture fills the height
					wantPicH = region[3] - region[1]
					wantPicW = wantPicH * 1800 / 2400
				}
				cx, cy := (region[0]+region[2])/2, (region[1]+region[3])/2
				if !near(r[2]-r[0], wantPicW, 0.01) || !near(r[3]-r[1], wantPicH, 0.01) || !near((r[0]+r[2])/2, cx, 0.01) || !near((r[1]+r[3])/2, cy, 0.01) {
					t.Errorf("front picture at %v, want %.3f by %.3f centred on %.3f,%.3f", r, wantPicW, wantPicH, cx, cy)
				}
				wantClip := []float64{fx0, 0, wantW, wantH}
				for i := range wantClip {
					if fd.Clip == nil || !near(fd.Clip[i], wantClip[i], 0.01) {
						t.Errorf("front clip %v, want %v", fd.Clip, wantClip)
						break
					}
				}
				// at full resolution: the pixels of the file, and the file's own stream
				if fd.Image.W != 1800 || fd.Image.H != 2400 {
					t.Errorf("the front is embedded at %dx%d", fd.Image.W, fd.Image.H)
				}
				if want, _ := cleanJPEG(front.Data); !bytes.Equal(fd.Image.Data, want) {
					t.Error("the JPEG was not passed through")
				}
				if bd.Image.W != 800 || bd.Image.H != 1000 || bd.Image.Filter != "FlateDecode" {
					t.Errorf("the back is embedded as %+v", bd.Image.W)
				}
				// the back is a page: the picture at canvas pixels mapped to the trim, clipped to the box inside the margin
				k := tw / 1707
				kh := th / 2560
				bx0 := bl
				wantBack := [4]float64{bx0 + 300*k, bl + th - 1600*kh, bx0 + 1100*k, bl + th - 600*kh}
				br := bd.Rect()
				for i := range br {
					if !near(br[i], wantBack[i], 0.02) {
						t.Errorf("back picture at %v, want %v", br, wantBack)
						break
					}
				}
				wantBox := []float64{bx0 + 128*k, bl + th - 2432*kh, bx0 + 1579*k, bl + th - 128*kh}
				for i := range wantBox {
					if bd.Clip == nil || !near(bd.Clip[i], wantBox[i], 0.02) {
						t.Errorf("back clip %v, want %v", bd.Clip, wantBox)
						break
					}
				}
				// the background fills the whole back panel with its bleed
				foundBG := false
				for _, f := range p1.Fills {
					if near(f.Color[0], 0xE8/255.0, 0.002) && near(f.Rect[0], 0, 0.01) && near(f.Rect[2], bl+tw, 0.01) && near(f.Rect[3], wantH, 0.01) {
						foundBG = true
					}
				}
				if !foundBG {
					t.Errorf("no background over the back with its bleed: %+v", p1.Fills)
				}
				// page 2: the front alone with bleed on every side
				p2 := doc.Pages[1]
				if !near(p2.W, tw+2*bl, 0.001) || !near(p2.H, th+2*bl, 0.001) || len(p2.Trim) != 4 || !near(p2.Trim[0], bl, 0.001) {
					t.Errorf("page 2: %vx%v trim %v", p2.W, p2.H, p2.Trim)
				}
				if len(p2.Draws) != 1 || p2.Draws[0].Clip == nil || !near(p2.Draws[0].Clip[2]-p2.Draws[0].Clip[0], tw+2*bl, 0.01) {
					t.Errorf("page 2 draws: %+v", p2.Draws)
				} else if r2 := p2.Draws[0].Rect(); !near(r2[2]-r2[0], wantPicW, 0.01) || !near(r2[3]-r2[1], wantPicH, 0.01) {
					t.Errorf("the front is another size on page 2: %v", r2)
				}
				// page 3: the proof
				p3 := doc.Pages[2]
				if len(p3.Draws) != 2 || !strings.Contains(p3.Text(), "Proof") || p3.Strokes < 6 {
					t.Errorf("proof: %d draws, %d strokes, text %q", len(p3.Draws), p3.Strokes, p3.Text())
				}
				for _, d := range p3.Draws {
					if !near(d.CTM[0], d.Rect()[2]-d.Rect()[0], 0.001) || d.Clip == nil {
						t.Errorf("proof draw %+v", d)
					}
				}
				// the same two pictures are two objects in the file, not six
				if doc.Images != 2 {
					t.Errorf("%d image objects", doc.Images)
				}
				if hasWarn(ws, "spine_narrow") {
					t.Errorf("warnings %s", warnCodes(ws))
				}
			})
		}
	}
}

// A wrap is one picture, the whole first page, at its measured size, and nothing is drawn over it.
func TestWrapCoverIsTheWholeFirstPage(t *testing.T) {
	for _, tc := range coverTrims {
		for _, prof := range []string{"kdp", "ingram", "generic"} {
			for _, jpg := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/jpeg=%v", tc.name, prof, jpg), func(t *testing.T) {
					cs := baseSpec(tc, prof, 0.75, 300)
					w := mkWrap(t, tc, 0.125, 0.75, 100, jpg)
					cs.Art = &CoverArt{Shape: ArtWrap, Wrap: w}
					doc, _, ws := writeCover(t, cs)
					if len(doc.Pages) != 3 {
						t.Fatalf("%d pages", len(doc.Pages))
					}
					p1 := doc.Pages[0]
					tw, th := pts(cs.TrimW), pts(cs.TrimH)
					wantW := float64(w.Image.W) / w.DPI * 72
					wantH := th + 18
					if !near(p1.H, wantH, 0.001) || !near(p1.W, wantW, 0.5) {
						t.Errorf("page 1 is %vx%v, want %vx%v (the picture's measured size)", p1.W, p1.H, wantW, wantH)
					}
					if len(p1.Trim) != 4 || !near(p1.Trim[0], 9, 0.001) || !near(p1.Trim[2], p1.W-9, 0.001) || !near(p1.Trim[3], p1.H-9, 0.001) {
						t.Errorf("trim box %v", p1.Trim)
					}
					// nothing else: no fill, no stroke, no text, one picture at the whole page
					if len(p1.Draws) != 1 || len(p1.Fills) != 0 || p1.Strokes != 0 || len(p1.Lines) != 0 {
						t.Fatalf("page 1 holds %d draws, %d fills, %d strokes, %d lines", len(p1.Draws), len(p1.Fills), p1.Strokes, len(p1.Lines))
					}
					d := p1.Draws[0]
					if !near(d.CTM[0], p1.W, 0.001) || !near(d.CTM[3], p1.H, 0.001) || d.CTM[1] != 0 || d.CTM[2] != 0 || d.CTM[4] != 0 || d.CTM[5] != 0 || d.Clip != nil {
						t.Errorf("the wrap is placed by %v clip %v", d.CTM, d.Clip)
					}
					if d.Image.W != w.Image.W || d.Image.H != w.Image.H {
						t.Errorf("embedded at %dx%d, the picture is %dx%d", d.Image.W, d.Image.H, w.Image.W, w.Image.H)
					}
					if jpg {
						if want, _ := cleanJPEG(w.Image.Data); d.Image.Filter != "DCTDecode" || !bytes.Equal(d.Image.Data, want) {
							t.Error("the JPEG was not passed through")
						}
					} else if d.Image.Filter != "FlateDecode" {
						t.Errorf("the PNG's filter is %q", d.Image.Filter)
					}
					// the front alone: the front's trim at the frame's trim, bleed on its outer edges only
					p2 := doc.Pages[1]
					if !near(p2.W, tw+9, 0.001) || !near(p2.H, th+18, 0.001) || len(p2.Draws) != 1 || len(p2.Fills) != 0 {
						t.Errorf("page 2: %vx%v, %d draws", p2.W, p2.H, len(p2.Draws))
					}
					d2 := p2.Draws[0].Rect()
					k := p1.H / float64(w.Image.H)
					wantX := 0 - float64(w.Front.Min.X)*k
					if !near(d2[0], wantX, 0.6) || !near(d2[2]-d2[0], p1.W, 0.01) {
						t.Errorf("page 2 puts the picture at %v, want x %.2f", d2, wantX)
					}
					// the proof has the wrap at half size and the lines over it
					if p3 := doc.Pages[2]; len(p3.Draws) != 1 || !near(p3.Draws[0].CTM[0], p1.W/2, 0.01) || p3.Strokes < 6 {
						t.Errorf("proof: %+v strokes %d", p3.Draws, p3.Strokes)
					}
					if !hasWarn(ws, "cover_barcode_area") || hasWarn(ws, "cover_wrap_stale") {
						t.Errorf("warnings %s", warnCodes(ws))
					}
					if hasWarn(ws, "image_low_resolution") != true { // 100 dpi
						t.Errorf("a 100 dpi wrap was not reported: %s", warnCodes(ws))
					}
				})
			}
		}
	}
}

func TestWrapCoverStaleSpineAndGuides(t *testing.T) {
	tc := coverTrims[0]
	for _, c := range []struct {
		name     string
		spine    float64 // the book's
		measured float64
		stale    bool
	}{{"same", 0.75, 0.75, false}, {"a thirty-second under", 0.75, 0.75 - 1.0/32 + 0.002, false}, {"over", 0.75, 0.80, true}, {"thin", 0.40, 0.75, true}} {
		t.Run(c.name, func(t *testing.T) {
			cs := baseSpec(tc, "kdp", c.spine, 300)
			cs.Barcode, cs.Guides = true, true
			w := mkWrap(t, tc, 0.125, c.measured, 300, true)
			cs.Art = &CoverArt{Shape: ArtWrap, Wrap: w}
			doc, _, ws := writeCover(t, cs)
			if hasWarn(ws, "cover_wrap_stale") != c.stale {
				t.Errorf("stale = %v, warnings %s", !c.stale, warnCodes(ws))
			}
			if c.stale {
				for _, wn := range ws {
					if wn.Code == "cover_wrap_stale" && (!strings.Contains(wn.Message, fmt.Sprintf("%.3f", c.measured)) || !strings.Contains(wn.Message, fmt.Sprintf("%.3f", c.spine))) {
						t.Errorf("the warning does not give both figures: %s", wn.Message)
					}
				}
				// placed as it is: the page is the picture's size, not the book's
				want := (12.25 + c.measured) * 72
				if !near(doc.Pages[0].W, want, 0.5) {
					t.Errorf("page 1 is %v wide, want the picture's %v", doc.Pages[0].W, want)
				}
			}
			// the barcode box and the guides are drawn over the wrap only when asked for, at the profile's place
			p1 := doc.Pages[0]
			if p1.Strokes == 0 {
				t.Error("no guides")
			}
			bw, bh := 2*72.0, 1.2*72
			found := false
			for _, f := range p1.Fills {
				if near(f.Rect[2]-f.Rect[0], bw, 0.01) && near(f.Rect[3]-f.Rect[1], bh, 0.01) {
					found = near(f.Rect[2], 9+6*72-0.25*72, 0.01) && near(f.Rect[1], 9+0.25*72, 0.01)
				}
			}
			if !found {
				t.Errorf("no 2 x 1.2 in barcode box in the back's lower right: %+v", p1.Fills)
			}
			if hasWarn(ws, "cover_barcode_area") {
				t.Error("the area note is for a box that is not drawn")
			}
		})
	}
}

// A page side with a picture that reaches the text margin, or the trim with no art for the bleed, says so; a picture well
// inside does not. A low-resolution picture says how sharp it is where it prints.
func TestPageSideWarnings(t *testing.T) {
	tc := coverTrims[0]
	mk := func(prof string, dst, box image.Rectangle, w, h int) []Warn {
		cs := baseSpec(tc, prof, 0.75, 300)
		cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelPage, Image: &CoverImage{Ext: "jpg", Hash: "x", W: w, H: h}, Background: "#2F5D50",
			Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: box, Dst: dst}}}
		ws, err := CoverWarnings(cs)
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	full := image.Rect(0, 0, 1707, 2560)
	// 400 canvas px is 1.4 in: well inside both profiles' margins
	if ws := mk("generic", image.Rect(400, 600, 1300, 1900), full, 2700, 3900); hasWarn(ws, "cover_margin_crossed") || hasWarn(ws, "cover_bleed_missing") {
		t.Errorf("a picture inside: %s", warnCodes(ws))
	}
	// 40 canvas px = 0.14 in: inside the generic profile's 0.25 in margin, outside KDP's 0.125 in only when under 36 px
	edge := image.Rect(40, 100, 1667, 2460)
	if ws := mk("generic", edge, full, 1627, 2360); !hasWarn(ws, "cover_margin_crossed") {
		t.Errorf("generic: %s", warnCodes(ws))
	}
	if ws := mk("kdp", edge, full, 1627, 2360); hasWarn(ws, "cover_margin_crossed") {
		t.Errorf("kdp: %s", warnCodes(ws))
	}
	// fill and crop with the whole page: the width is exact, the height overflows (art for the bleed top and bottom, none at the sides)
	ws := mk("generic", image.Rect(0, -200, 1707, 2760), full, 1707, 2960)
	var bleed string
	for _, w := range ws {
		if w.Code == "cover_bleed_missing" {
			bleed = w.Message
		}
	}
	if bleed == "" || !strings.Contains(bleed, "outer") || strings.Contains(bleed, "top") {
		t.Errorf("bleed warning %q (%s)", bleed, warnCodes(ws))
	}
	// a small picture: its dpi at the printed size, never enlarged
	var low string
	for _, w := range mk("generic", image.Rect(400, 600, 1300, 1900), full, 700, 1011) {
		if w.Code == "image_low_resolution" {
			low = w.Message
		}
	}
	if !strings.Contains(low, "front picture is 233 dpi") && !strings.Contains(low, "dpi at its printed size") {
		t.Errorf("low resolution: %q", low)
	}
}

func TestSpineTypeFollowsTheProfileAndTheWidth(t *testing.T) {
	tc := coverTrims[0]
	for _, c := range []struct {
		prof  string
		pages int
		spine float64
		text  bool
		code  string
	}{
		{"generic", 60, 0.5, true, ""},
		{"kdp", 60, 0.5, false, "spine_few_pages"},
		{"kdp", 79, 0.5, true, ""},
		{"ingram", 47, 0.5, false, "spine_few_pages"},
		{"ingram", 48, 0.5, true, ""},
		{"ingram", 300, 0.2, false, "spine_narrow"},
		{"ingram", 300, 0.25, true, ""},
		{"generic", 300, 0.9, true, ""},
	} {
		t.Run(fmt.Sprintf("%s-%d-%v", c.prof, c.pages, c.spine), func(t *testing.T) {
			cs := baseSpec(tc, c.prof, c.spine, c.pages)
			cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelGenerated}}
			doc, _, ws := writeCover(t, cs)
			var rotated bool
			for _, ln := range doc.Pages[0].Lines {
				if strings.Contains(ln.Text, "Salt Road") && strings.Contains(ln.Text, "Bhriain") {
					rotated = true
				}
			}
			// the type fits what is left of the spine between the profile's edges (its height is about 1.2 em)
			p, _ := LookupCoverProfile(c.prof)
			for _, ln := range doc.Pages[0].Lines {
				if rotated && strings.Contains(ln.Text, "Salt Road") && strings.Contains(ln.Text, "Bhriain") {
					clear := (c.spine - 2*p.SpineEdge(c.spine)) * 72
					if ln.Size*1.2 > clear+0.01 {
						t.Errorf("the type is %.2f pt, which does not fit the %.2f pt between the edges", ln.Size, clear)
					}
				}
			}
			if rotated != c.text {
				t.Errorf("spine type = %v, want %v (%s)", rotated, c.text, warnCodes(ws))
			}
			if c.code != "" && !hasWarn(ws, c.code) || c.code == "" && (hasWarn(ws, "spine_narrow") || hasWarn(ws, "spine_few_pages")) {
				t.Errorf("warnings %s, want %q", warnCodes(ws), c.code)
			}
		})
	}
}

// A layout whose front is generated and whose back is left out: the front is today's drawing, the back is the cover
// colour with no type, and the barcode box (when asked for) is at the profile's place.
func TestGeneratedSidesInALayout(t *testing.T) {
	for _, tc := range coverTrims[:2] {
		for _, prof := range []string{"kdp", "ingram", "generic"} {
			t.Run(tc.name+"/"+prof, func(t *testing.T) { generatedSidesInALayout(t, tc, prof) })
		}
	}
}

func generatedSidesInALayout(t *testing.T, tc trimCase, prof string) {
	{
		cs := baseSpec(tc, prof, 0.6, 320)
		cs.Barcode = true
		cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelGenerated}, Back: CoverPanel{Mode: PanelNone}}
		doc, _, _ := writeCover(t, cs)
		p1 := doc.Pages[0]
		var onBack, onFront bool
		mid := p1.W / 2
		for _, ln := range p1.Lines {
			if ln.X < mid && !strings.Contains(ln.Text, "Bhriain") {
				onBack = true
			}
			if ln.X > mid && strings.Contains(ln.Text, "Salt") {
				onFront = true
			}
		}
		if onBack || !onFront {
			t.Errorf("%s: back type %v, front type %v: %+v", prof, onBack, onFront, p1.Lines)
		}
		p, _ := LookupCoverProfile(prof)
		bw, bh := p.BarcodeWIn*72, p.BarcodeHIn*72
		ok := false
		for _, f := range p1.Fills {
			if near(f.Rect[2]-f.Rect[0], bw, 0.01) && near(f.Rect[3]-f.Rect[1], bh, 0.01) && near(f.Rect[2], 9+pts(tc.w)-p.BarcodeInsetIn*72, 0.01) {
				ok = true
			}
		}
		if !ok {
			t.Errorf("%s: no %vx%v barcode box: %+v", prof, bw, bh, p1.Fills)
		}
	}
}

// A right-to-left book has its front on the left; the cover can be made when it has no type to set (pictures and a wrap),
// and is refused, with a sentence, when it has.
func TestRightToLeftCovers(t *testing.T) {
	tc := coverTrims[0]
	cs := baseSpec(tc, "generic", 0.75, 300)
	cs.RTL = true
	cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: testJPEG(t, 1900, 2800)}, Back: CoverPanel{Mode: PanelImage, Image: testJPEG(t, 1900, 2801)}}
	doc, _, ws := writeCover(t, cs)
	p1 := doc.Pages[0]
	var front, back *check.PDFDraw
	for i := range p1.Draws {
		if p1.Draws[i].Image.H == 2800 {
			front = &p1.Draws[i]
		} else {
			back = &p1.Draws[i]
		}
	}
	if front == nil || back == nil || front.Rect()[0] >= back.Rect()[0] {
		t.Fatalf("the front should be on the left: %+v", p1.Draws)
	}
	if front.Clip[0] != 0 || !near(front.Clip[2], 9+6*72, 0.01) { // bleed on its outer (left) edge only
		t.Errorf("front clip %v", front.Clip)
	}
	if !hasWarn(ws, "spine_rtl") || len(p1.Lines) != 0 {
		t.Errorf("an RTL spine has no type: %s %v", warnCodes(ws), p1.Lines)
	}
	// a wrap cut for the other direction is not placed
	cs.Art = &CoverArt{Shape: ArtWrap, Wrap: mkWrap(t, tc, 0.125, 0.75, 100, true)}
	if _, _, err := WriteCoverPDF(cs); err == nil {
		t.Error("a wrap whose front is on the right was placed for a right-to-left book")
	}
	w := mkWrap(t, tc, 0.125, 0.75, 100, true)
	w.FrontOnRight = false
	cs.Art = &CoverArt{Shape: ArtWrap, Wrap: w}
	if _, _, err := WriteCoverPDF(cs); err != nil {
		t.Error(err)
	}
	// generated sides, or no layout at all, would be type set the wrong way
	cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelGenerated}}
	if _, _, err := WriteCoverPDF(cs); err != ErrCoverRTL {
		t.Errorf("generated sides: %v", err)
	}
	// no layout: the generated cover as it always was, whatever the language
	cs.Art = nil
	if _, _, err := WriteCoverPDF(cs); err != nil {
		t.Errorf("no layout: %v", err)
	}
}

// The check and the file agree: CoverWarnings, which reads no bytes, says what WriteCoverPDF says.
func TestCoverWarningsAreTheFilesWarnings(t *testing.T) {
	tc := coverTrims[2]
	cs := baseSpec(tc, "kdp", 0.9, 60)
	cs.Art = &CoverArt{Shape: ArtSides,
		Front: CoverPanel{Mode: PanelImage, Image: testJPEG(t, 900, 1300)},
		Back:  CoverPanel{Mode: PanelImage, Image: testJPEG(t, 900, 1300)}}
	_, _, got := writeCover(t, cs)
	lite := cs
	lite.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: &CoverImage{W: 900, H: 1300}}, Back: CoverPanel{Mode: PanelImage, Image: &CoverImage{W: 900, H: 1300}}}
	want, err := CoverWarnings(lite)
	if err != nil {
		t.Fatal(err)
	}
	if warnCodes(got) != warnCodes(want) || len(want) < 3 {
		t.Errorf("file: %s, check: %s", warnCodes(got), warnCodes(want))
	}
}

// A cover with no layout that names a profile has the profile's barcode box; one that names none keeps its own (pinned
// elsewhere). Either way the file is the two pages it was, and the checker passes.
func TestGeneratedCoverWithAProfile(t *testing.T) {
	for _, tc := range coverTrims[:2] {
		for _, prof := range []string{"kdp", "ingram", "generic"} {
			cs := baseSpec(tc, prof, 0.6, 320)
			cs.Barcode, cs.Guides = true, true
			doc, _, _ := writeCover(t, cs)
			if len(doc.Pages) != 2 {
				t.Fatalf("%s/%s: %d pages", tc.name, prof, len(doc.Pages))
			}
			p, _ := LookupCoverProfile(prof)
			ok := false
			for _, f := range doc.Pages[0].Fills {
				if near(f.Rect[2]-f.Rect[0], p.BarcodeWIn*72, 0.01) && near(f.Rect[3]-f.Rect[1], p.BarcodeHIn*72, 0.01) {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s/%s: no barcode box of the profile's size", tc.name, prof)
			}
		}
	}
}

func countPages(t *testing.T, b []byte) int {
	t.Helper()
	d, probs := check.ParsePDF(b)
	for _, p := range probs {
		t.Error("the checker:", p)
	}
	return len(d.Pages)
}

// With a layout the default file is the one page a printer takes; the front and the proof are files of their own.
func TestCoverPartsAreFilesOfTheirOwn(t *testing.T) {
	tc := coverTrims[0]
	cs := baseSpec(tc, "kdp", 0.75, 300)
	cs.Part = ""
	cs.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: testJPEG(t, 1900, 2850)}, Back: CoverPanel{Mode: PanelNone}}
	var want = map[string]struct {
		w, h float64
		text string
	}{
		"":      {2*6*72 + 18 + 0.75*72, 9*72 + 18, ""},
		"cover": {2*6*72 + 18 + 0.75*72, 9*72 + 18, ""},
		"front": {6*72 + 18, 9*72 + 18, ""},
		"proof": {0, 0, "Proof"},
	}
	for part, w := range want {
		c := cs
		c.Part = part
		b, _, err := WriteCoverPDF(c)
		if err != nil {
			t.Fatal(err)
		}
		d, probs := check.ParsePDF(b)
		for _, p := range probs {
			t.Error(part, p)
		}
		if len(d.Pages) != 1 {
			t.Fatalf("part %q: %d pages", part, len(d.Pages))
		}
		if w.w > 0 && (!near(d.Pages[0].W, w.w, 0.01) || !near(d.Pages[0].H, w.h, 0.01)) {
			t.Errorf("part %q is %vx%v, want %vx%v", part, d.Pages[0].W, d.Pages[0].H, w.w, w.h)
		}
		if w.text != "" && !strings.Contains(d.Pages[0].Text(), w.text) {
			t.Errorf("part %q: %q", part, d.Pages[0].Text())
		}
		if w.text == "" && strings.Contains(d.Pages[0].Text(), "Proof") {
			t.Errorf("part %q holds the proof", part)
		}
	}
	// the printer's file, the front and the proof differ; the printer's file is the same as the first page of the three
	var a, bb []byte
	c1, c2 := cs, cs
	c1.Part, c2.Part = "cover", "all"
	a, _, _ = WriteCoverPDF(c1)
	bb, _, _ = WriteCoverPDF(c2)
	if countPages(t, a) != 1 || countPages(t, bb) != 3 {
		t.Error("page counts of cover and all")
	}
}

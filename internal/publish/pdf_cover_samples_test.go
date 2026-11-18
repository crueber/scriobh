package publish

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// artwork is a picture that shows what a cover does to it: a border near the edge, a cross through the centre, a circle and
// corner marks, so a crop, a bleed and a margin can be seen in a render.
func artwork(w, h int, base color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bw := w / 40
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := base
			t := float64(y) / float64(h)
			c.R, c.G, c.B = uint8(float64(base.R)*(1-0.4*t)), uint8(float64(base.G)*(1-0.4*t)), uint8(float64(base.B)*(1-0.4*t))
			d := math.Hypot(float64(x-w/2), float64(y-h/2))
			switch {
			case x < bw || y < bw || x >= w-bw || y >= h-bw:
				c = color.RGBA{250, 220, 80, 255}
			case math.Abs(d-float64(w)/3) < float64(bw)/2:
				c = color.RGBA{240, 240, 240, 255}
			case math.Abs(float64(x-w/2)) < float64(bw)/3 || math.Abs(float64(y-h/2)) < float64(bw)/3:
				c = color.RGBA{255, 255, 255, 255}
			case x < 3*bw && y < 3*bw, x >= w-3*bw && y >= h-3*bw:
				c = color.RGBA{20, 20, 20, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func artJPEG(t testing.TB, w, h int, base color.RGBA) *CoverImage {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, artwork(w, h, base), &jpeg.Options{Quality: 88}); err != nil {
		t.Fatal(err)
	}
	return &CoverImage{Data: b.Bytes(), Ext: "jpg", Hash: "art-" + string(rune('a'+w%20)) + string(rune('a'+h%20)), W: w, H: h}
}

// TestSampleCovers writes covers to look at: set SCRIOBH_KEEP to a folder, then pdftoppm -r 50 on the files.
func TestSampleCovers(t *testing.T) {
	dir := os.Getenv("SCRIOBH_KEEP")
	if dir == "" {
		t.Skip("set SCRIOBH_KEEP to a folder to write sample covers to")
	}
	tc := coverTrims[0]
	write := func(name string, cs CoverSpec) {
		b, ws, err := WriteCoverPDF(cs)
		if err != nil {
			t.Fatal(name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".pdf"), b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s", name, warnCodes(ws))
	}
	// no layout
	g := baseSpec(tc, "", 0.75, 300)
	write("generated", g)
	// a layout with generated sides
	gl := baseSpec(tc, "kdp", 0.75, 300)
	gl.Barcode = true
	gl.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelGenerated}, Back: CoverPanel{Mode: PanelNone}}
	write("generated-layout", gl)
	// sides: an image front, a page back
	s := baseSpec(tc, "kdp", 0.75, 300)
	s.Barcode, s.Guides = true, false
	back := artJPEG(t, 1200, 1600, color.RGBA{60, 90, 150, 255})
	s.Art = &CoverArt{Shape: ArtSides,
		Front: CoverPanel{Mode: PanelImage, Image: artJPEG(t, 1900, 2850, color.RGBA{170, 60, 60, 255})},
		Back: CoverPanel{Mode: PanelPage, Image: back, Background: "#E8E1D5",
			Place: &CoverPlacement{CanvasW: 1707, CanvasH: 2560, Box: image.Rect(128, 128, 1579, 2432), Dst: image.Rect(213, 480, 1493, 1760)}}}
	write("sides", s)
	// a wrap: a spine strip of its own colour
	w := mkWrap(t, tc, 0.125, 0.75, 150, true)
	art := artwork(w.Image.W, w.Image.H, color.RGBA{40, 120, 100, 255})
	for y := 0; y < w.Image.H; y++ { // the spine in another colour, so it can be found
		for x := w.Spine.Min.X; x < w.Spine.Max.X; x++ {
			art.SetRGBA(x, y, color.RGBA{30, 30, 60, 255})
		}
	}
	var jb bytes.Buffer
	_ = jpeg.Encode(&jb, art, &jpeg.Options{Quality: 88})
	w.Image.Data = jb.Bytes()
	ws := baseSpec(tc, "kdp", 0.75, 300)
	ws.Art = &CoverArt{Shape: ArtWrap, Wrap: w}
	write("wrap", ws)
	// a stale wrap, with the guides
	st := baseSpec(tc, "kdp", 0.9, 360)
	st.Guides, st.Barcode = true, true
	st.Art = &CoverArt{Shape: ArtWrap, Wrap: w}
	write("wrap-stale", st)
	// a right-to-left book: pictures, the front on the left
	rt := baseSpec(tc, "generic", 0.75, 300)
	rt.RTL = true
	rt.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: artJPEG(t, 1900, 2850, color.RGBA{170, 60, 60, 255})}, Back: CoverPanel{Mode: PanelImage, Image: artJPEG(t, 1900, 2851, color.RGBA{60, 90, 150, 255})}}
	write("rtl", rt)
	// narrow spines with a layout: where the type is sized to the profile's edge
	for _, c := range []struct {
		n     string
		prof  string
		spine float64
	}{{"spine025", "ingram", 0.25}, {"spine030", "kdp", 0.30}} {
		ns := baseSpec(tc, c.prof, c.spine, 100)
		ns.Art = &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelGenerated}}
		write(c.n, ns)
	}
	// the interior's cover page, with and without crop marks
	for _, crop := range []bool{false, true} {
		b := featureBook()
		o := print6x9()
		o.CoverPage = True()
		if crop {
			o.CropMarks = True()
		}
		b.PrintCover = &CoverSpec{Title: "Features", Author: "A. Writer", Color: "#2F5D50", Language: "en",
			Art: &CoverArt{Shape: ArtSides, Front: CoverPanel{Mode: PanelImage, Image: artJPEG(t, 1900, 2850, color.RGBA{170, 60, 60, 255})}}}
		out, _, err := WritePDF(b, o)
		if err != nil {
			t.Fatal(err)
		}
		n := "interior-cover"
		if crop {
			n += "-crop"
		}
		_ = os.WriteFile(filepath.Join(dir, n+".pdf"), out, 0o644)
	}
}

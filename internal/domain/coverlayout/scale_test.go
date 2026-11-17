package coverlayout

import (
	"image"
	"image/color"
	"math/rand"
	"testing"

	xdraw "golang.org/x/image/draw"
)

func noisy(w, h int, alpha bool) *image.NRGBA {
	r := rand.New(rand.NewSource(7))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// smooth shapes with some edges and a little noise: what a picture is
			v := uint8((x*255/w + (y/17%2)*90) % 256)
			a := uint8(255)
			if alpha && x < w/3 {
				a = uint8(255 * x / (w / 3))
			}
			img.SetNRGBA(x, y, color.NRGBA{v, uint8(y * 255 / h), uint8(r.Intn(40)) + v/2, a})
		}
	}
	return img
}

// The streaming Catmull-Rom is x/image's CatmullRom: on a picture small enough for the library's temporary, over white,
// every channel of every pixel is within one level, shrinking by a fraction and by a lot, a cut of the picture, and with
// transparency.
func TestTheScalerIsXImagesCatmullRom(t *testing.T) {
	for _, c := range []struct {
		name         string
		w, h, dw, dh int
		sr           image.Rectangle
		alpha        bool
	}{
		{"by a fraction", 300, 450, 200, 300, image.Rect(0, 0, 300, 450), false},
		{"by half", 400, 600, 200, 300, image.Rect(0, 0, 400, 600), false},
		{"by a lot", 900, 1350, 120, 180, image.Rect(0, 0, 900, 1350), false},
		{"a cut", 600, 600, 150, 200, image.Rect(100, 50, 400, 450), false},
		{"with transparency", 300, 450, 150, 225, image.Rect(0, 0, 300, 450), true},
		{"the same size", 120, 180, 120, 180, image.Rect(0, 0, 120, 180), false},
		{"larger", 100, 150, 130, 200, image.Rect(0, 0, 100, 150), false},
	} {
		src := noisy(c.w, c.h, c.alpha)
		want, got := newCanvas(c.dw, c.dh, white), newCanvas(c.dw, c.dh, white)
		xdraw.CatmullRom.Scale(want, want.Bounds(), src, c.sr, xdraw.Over, nil)
		catmullRom(got, got.Bounds(), src, c.sr)
		worst := 0
		for i := range want.Pix {
			d := int(want.Pix[i]) - int(got.Pix[i])
			if d < 0 {
				d = -d
			}
			worst = max(worst, d)
		}
		if worst > 1 {
			t.Errorf("%s: the scaler is %d levels from x/image's", c.name, worst)
		}
	}
}

// The box pass is the average of its blocks, with a smaller block at the edge, and a transparent picture keeps its alpha.
func TestTheBoxPassAveragesItsBlocks(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 5, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 5; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 10), uint8(y * 10), 100, 255})
		}
	}
	out := boxReduce(img, img.Bounds(), 2)
	if b := out.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("%v", b)
	}
	// block (0,0): x 0..1, y 0..1: red (0+10)/2=5, green (0+10)/2=5; the right-hand block is one pixel wide: x=4 only
	if p := out.RGBAAt(0, 0); p.R != 5 || p.G != 5 || p.B != 100 || p.A != 255 {
		t.Errorf("%v", p)
	}
	if p := out.RGBAAt(2, 1); p.R != 40 || p.G != 25 || p.B != 100 {
		t.Errorf("the edge block: %v", p)
	}
	half := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for i := 0; i < 4; i++ {
		half.SetNRGBA(i%2, i/2, color.NRGBA{200, 0, 0, 128})
	}
	if p := boxReduce(half, half.Bounds(), 2).RGBAAt(0, 0); p.A < 127 || p.A > 129 || p.R < 99 || p.R > 101 {
		t.Errorf("transparency is premultiplied: %v", p)
	}
}

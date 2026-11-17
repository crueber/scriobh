package coverlayout

import (
	"image"
	"image/color"
	"math"
)

// The scaler. golang.org/x/image/draw's CatmullRom is the filter the design names, but its implementation keeps a
// temporary of the destination's width by the source's height in four float64s a pixel: 140 MB to make a 1707 by 2560
// front from a 2700-pixel-high cut, 330 MB from a 6000-pixel one. This is the same filter (Catmull-Rom, its support
// stretched by the scale when a picture shrinks, the weights of the pixels that exist normalised, a premultiplied result
// drawn over what is there) done a row at a time: each source row is scaled across once, and only the few rows the
// vertical pass is reading are kept, so the memory is the size of the output and not of the source. scale_test.go holds it
// to x/image's own result on a picture small enough for that to be made.

// tap is the source pixels one destination pixel is made from: from first, with these weights.
type tap struct {
	first int
	w     []float32
}

func catmullRomK(t float64) float64 {
	t = math.Abs(t)
	switch {
	case t < 1:
		return 1.5*t*t*t - 2.5*t*t + 1
	case t < 2:
		return -0.5*t*t*t + 2.5*t*t - 4*t + 2
	}
	return 0
}

// crTaps works out, for each of dn destination pixels along a line, which of the sn source pixels contribute and how much.
func crTaps(sn, dn int) []tap {
	scale := float64(sn) / float64(dn)
	stretch := math.Max(1, scale)
	out := make([]tap, dn)
	for d := range out {
		center := (float64(d) + 0.5) * scale
		lo := int(math.Floor(center - 2*stretch))
		hi := int(math.Ceil(center + 2*stretch))
		lo, hi = max(lo, 0), min(hi, sn)
		var sum float64
		ws := make([]float64, 0, hi-lo)
		for i := lo; i < hi; i++ {
			w := catmullRomK((float64(i) + 0.5 - center) / stretch)
			ws = append(ws, w)
			sum += w
		}
		if sum == 0 { // no pixel under the kernel (a degenerate one-pixel line): the nearest
			lo, ws, sum = min(max(int(center), 0), sn-1), []float64{1}, 1
		}
		t := tap{first: lo, w: make([]float32, len(ws))}
		for i, w := range ws {
			t.w[i] = float32(w / sum)
		}
		out[d] = t
	}
	return out
}

// rowReader fills buf (4 floats a pixel: premultiplied red, green, blue, alpha, 0 to 255) with the pixels x0 to x1 of row y.
func rowReader(img image.Image) func(y, x0, x1 int, buf []float32) {
	switch m := img.(type) {
	case *image.YCbCr:
		return func(y, x0, x1 int, buf []float32) {
			for x := x0; x < x1; x++ {
				yi, ci := m.YOffset(x, y), m.COffset(x, y)
				r, g, b := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
				o := (x - x0) * 4
				buf[o], buf[o+1], buf[o+2], buf[o+3] = float32(r), float32(g), float32(b), 255
			}
		}
	case *image.RGBA:
		return func(y, x0, x1 int, buf []float32) {
			o := m.PixOffset(x0, y)
			for i := 0; i < (x1-x0)*4; i++ {
				buf[i] = float32(m.Pix[o+i])
			}
		}
	}
	return func(y, x0, x1 int, buf []float32) {
		for x := x0; x < x1; x++ {
			r, g, b, a := img.At(x, y).RGBA() // premultiplied, 16 bits
			o := (x - x0) * 4
			buf[o], buf[o+1], buf[o+2], buf[o+3] = float32(r)/257, float32(g)/257, float32(b)/257, float32(a)/257
		}
	}
}

// catmullRom draws the part sr of src into dr of dst, scaled, over what is there.
func catmullRom(dst *image.RGBA, dr image.Rectangle, src image.Image, sr image.Rectangle) {
	sw, sh, dw, dh := sr.Dx(), sr.Dy(), dr.Dx(), dr.Dy()
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return
	}
	xt, yt := crTaps(sw, dw), crTaps(sh, dh)
	read := rowReader(src)
	srcRow := make([]float32, sw*4)
	// the rows scaled across that the vertical pass still needs, by source row
	have := map[int][]float32{}
	var free [][]float32
	scaledRow := func(y int) []float32 {
		if r, ok := have[y]; ok {
			return r
		}
		read(sr.Min.Y+y, sr.Min.X, sr.Max.X, srcRow)
		var r []float32
		if n := len(free); n > 0 {
			r, free = free[n-1], free[:n-1]
		} else {
			r = make([]float32, dw*4)
		}
		for x, t := range xt {
			var a0, a1, a2, a3 float32
			for i, w := range t.w {
				o := (t.first + i) * 4
				a0 += w * srcRow[o]
				a1 += w * srcRow[o+1]
				a2 += w * srcRow[o+2]
				a3 += w * srcRow[o+3]
			}
			r[x*4], r[x*4+1], r[x*4+2], r[x*4+3] = a0, a1, a2, a3
		}
		have[y] = r
		return r
	}
	acc := make([]float32, dw*4)
	for d, t := range yt {
		for y, r := range have { // rows above this one's first are never read again
			if y < t.first {
				free = append(free, r)
				delete(have, y)
			}
		}
		clear(acc)
		for i, w := range t.w {
			r := scaledRow(t.first + i)
			for k := range acc {
				acc[k] += w * r[k]
			}
		}
		o := dst.PixOffset(dr.Min.X, dr.Min.Y+d)
		for x := 0; x < dw; x++ {
			a := clamp255(acc[x*4+3])
			p := dst.Pix[o+x*4 : o+x*4+4]
			if a == 255 {
				p[0], p[1], p[2], p[3] = clamp255(acc[x*4]), clamp255(acc[x*4+1]), clamp255(acc[x*4+2]), 255
				continue
			}
			inv := 255 - uint32(a) // draw.Over: source plus the destination left uncovered
			p[0] = uint8(min(255, uint32(clamp255(acc[x*4]))+(uint32(p[0])*inv+127)/255))
			p[1] = uint8(min(255, uint32(clamp255(acc[x*4+1]))+(uint32(p[1])*inv+127)/255))
			p[2] = uint8(min(255, uint32(clamp255(acc[x*4+2]))+(uint32(p[2])*inv+127)/255))
			p[3] = uint8(min(255, uint32(a)+(uint32(p[3])*inv+127)/255))
		}
	}
}

func clamp255(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

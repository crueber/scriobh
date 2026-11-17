// Package pwa draws the application's install icons.
//
// The icon is the favicon's mark (a moss square with three white lines) drawn
// with the standard library only, so no binary file is committed and no image
// library is linked. Drawing is deterministic: the same size gives the same
// bytes. Edges are anti-aliased by 4x4 supersampling.
package pwa

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
)

// Moss is the accent and the icon's flat fill, as in web/static/icons/favicon.svg.
var moss = color.NRGBA{0x2F, 0x5D, 0x50, 0xFF}

type seg struct{ x1, y1, x2, y2 float64 }

// The mark, in the favicon's 32-unit space: three strokes of width 2 with round caps.
var strokes = []seg{{9, 11, 23, 11}, {9, 16, 23, 16}, {9, 21, 17, 21}}

const (
	unit   = 32.0
	radius = 6.0 // corner radius of the rounded square
	half   = 1.0 // half the stroke width
	ss     = 4   // samples per pixel side
)

// distSeg is the distance from (px,py) to a segment.
func distSeg(px, py float64, s seg) float64 {
	dx, dy := s.x2-s.x1, s.y2-s.y1
	t := ((px-s.x1)*dx + (py-s.y1)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(px-(s.x1+t*dx), py-(s.y1+t*dy))
}

// inRounded reports whether (x,y) is inside the 32-unit square with rounded corners.
func inRounded(x, y float64) bool {
	if x < 0 || y < 0 || x > unit || y > unit {
		return false
	}
	cx := math.Min(math.Max(x, radius), unit-radius)
	cy := math.Min(math.Max(y, radius), unit-radius)
	return math.Hypot(x-cx, y-cy) <= radius
}

// Icon draws the icon as a PNG of size x size pixels. A maskable icon fills the
// whole square (the platform applies its own shape); the mark sits well inside
// the safe zone either way.
func Icon(size int, maskable bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	var r, g, b, a int
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, a = 0, 0, 0, 0
			for j := 0; j < ss; j++ {
				for i := 0; i < ss; i++ {
					px := (float64(x) + (float64(i)+0.5)/ss) / float64(size) * unit
					py := (float64(y) + (float64(j)+0.5)/ss) / float64(size) * unit
					if !maskable && !inRounded(px, py) {
						continue
					}
					c := moss
					for _, s := range strokes {
						if distSeg(px, py, s) <= half {
							c = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
							break
						}
					}
					r, g, b, a = r+int(c.R), g+int(c.G), b+int(c.B), a+255
				}
			}
			if a == 0 {
				continue
			}
			// Average the covered samples' colour; alpha is the covered share.
			n := a / 255
			img.SetNRGBA(x, y, color.NRGBA{uint8(r / n), uint8(g / n), uint8(b / n), uint8(a / (ss * ss))})
		}
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	_ = enc.Encode(&buf, img)
	return buf.Bytes()
}

var (
	mu    sync.Mutex
	cache = map[[2]int][]byte{}
)

// Cached is Icon, drawn once per size and kind.
func Cached(size int, maskable bool) []byte {
	k := [2]int{size, 0}
	if maskable {
		k[1] = 1
	}
	mu.Lock()
	defer mu.Unlock()
	if b, ok := cache[k]; ok {
		return b
	}
	b := Icon(size, maskable)
	cache[k] = b
	return b
}

package pdfw

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
)

// Content builds a page's content stream. Lengths are milli-points measured
// from the bottom left of the page (PDF's own space).
type Content struct {
	b      bytes.Buffer
	fonts  map[*Font]bool
	images map[*Image]bool
}

// Bytes is the stream so far.
func (c *Content) Bytes() []byte { return c.b.Bytes() }

func (c *Content) op(format string, a ...any) {
	fmt.Fprintf(&c.b, format, a...)
	c.b.WriteByte('\n')
}

// Save and Restore bracket a graphics state (q, Q).
func (c *Content) Save()    { c.b.WriteString("q\n") }
func (c *Content) Restore() { c.b.WriteString("Q\n") }

// Gray sets the fill and stroke colour to a grey in thousandths (0 black, 1000 white).
func (c *Content) Gray(v int64) { c.op("%s g %s G", U(v), U(v)) }

// FillGray and StrokeGray set one of them.
func (c *Content) FillGray(v int64)   { c.op("%s g", U(v)) }
func (c *Content) StrokeGray(v int64) { c.op("%s G", U(v)) }

// FillRGB sets the fill colour from bytes.
func (c *Content) FillRGB(r, g, b uint8) {
	c.op("%s %s %s rg", U(int64(r)*1000/255), U(int64(g)*1000/255), U(int64(b)*1000/255))
}

// StrokeRGB sets the stroke colour from bytes.
func (c *Content) StrokeRGB(r, g, b uint8) {
	c.op("%s %s %s RG", U(int64(r)*1000/255), U(int64(g)*1000/255), U(int64(b)*1000/255))
}

// LineWidth sets the stroke width.
func (c *Content) LineWidth(w int64) { c.op("%s w", U(w)) }

// Dash sets a dash pattern (0 or 0 clears it).
func (c *Content) Dash(on, off int64) {
	if on <= 0 || off <= 0 {
		c.op("[] 0 d")
		return
	}
	c.op("[%s %s] 0 d", U(on), U(off))
}

// Rect adds a rectangle path.
func (c *Content) Rect(x, y, w, h int64) { c.op("%s %s %s %s re", U(x), U(y), U(w), U(h)) }

// FillRect fills a rectangle.
func (c *Content) FillRect(x, y, w, h int64) {
	c.Rect(x, y, w, h)
	c.b.WriteString("f\n")
}

// StrokeRect strokes a rectangle.
func (c *Content) StrokeRect(x, y, w, h int64) {
	c.Rect(x, y, w, h)
	c.b.WriteString("S\n")
}

// Line strokes a line.
func (c *Content) Line(x0, y0, x1, y1 int64) {
	c.op("%s %s m %s %s l S", U(x0), U(y0), U(x1), U(y1))
}

// BeginText and EndText bracket text objects (BT, ET).
func (c *Content) BeginText() { c.b.WriteString("BT\n") }
func (c *Content) EndText()   { c.b.WriteString("ET\n") }

// SetFont selects a font and size for the text that follows.
func (c *Content) SetFont(f *Font, size int64) {
	c.fonts[f] = true
	c.op("/%s %s Tf", f.Res, U(size))
}

// TextAt places the text matrix at (x, y), unrotated.
func (c *Content) TextAt(x, y int64) { c.op("1 0 0 1 %s %s Tm", U(x), U(y)) }

// TextAtRotated places the text matrix rotated a quarter turn anticlockwise
// (reading upwards, as on a spine) when up is true, clockwise otherwise.
func (c *Content) TextAtRotated(x, y int64, up bool) {
	if up {
		c.op("0 1 -1 0 %s %s Tm", U(x), U(y))
	} else {
		c.op("0 -1 1 0 %s %s Tm", U(x), U(y))
	}
}

// CharSpace sets the extra space after each character (Tc).
func (c *Content) CharSpace(v int64) { c.op("%s Tc", U(v)) }

// Rise sets the text rise (Ts): positive raises the baseline.
func (c *Content) Rise(v int64) { c.op("%s Ts", U(v)) }

// Fill modes for text (Tr): 0 fill, 3 invisible.
func (c *Content) RenderMode(m int) { c.op("%d Tr", m) }

// TJItem is a run of codes, or a movement.
type TJItem struct {
	Codes []uint16
	// Adj moves the next glyph: positive to the left, negative to the right, in
	// thousandths of 1/1000 em (so 1000 is one thousandth of the text size).
	Adj int64
}

// Show writes a TJ array.
func (c *Content) Show(items []TJItem) {
	if len(items) == 0 {
		return
	}
	var s strings.Builder
	s.WriteByte('[')
	for _, it := range items {
		if len(it.Codes) > 0 {
			s.WriteByte('<')
			for _, code := range it.Codes {
				fmt.Fprintf(&s, "%04X", code)
			}
			s.WriteByte('>')
		}
		if it.Adj != 0 {
			s.WriteString(U(it.Adj))
		}
	}
	s.WriteString("] TJ\n")
	c.b.WriteString(s.String())
}

// Image draws an image into the rectangle (x, y, w, h).
func (c *Content) Image(im *Image, x, y, w, h int64) {
	c.images[im] = true
	c.op("q %s 0 0 %s %s %s cm /%s Do Q", U(w), U(h), U(x), U(y), im.Res)
}

// ImageOriented draws an image in the box (x, y, w, h) turned and flipped as
// an EXIF orientation (1 to 8) says, so that a picture a camera saved sideways
// stands upright: the data is not touched, only the matrix that places it.
func (c *Content) ImageOriented(im *Image, x, y, w, h int64, orient int) {
	c.images[im] = true
	var a, b, cc, d, e, f int64
	switch orient {
	case 2:
		a, d, e, f = -w, h, x+w, y
	case 3:
		a, d, e, f = -w, -h, x+w, y+h
	case 4:
		a, d, e, f = w, -h, x, y+h
	case 5:
		b, cc, e, f = -h, -w, x+w, y+h
	case 6:
		b, cc, e, f = -h, w, x, y+h
	case 7:
		b, cc, e, f = h, w, x, y
	case 8:
		b, cc, e, f = h, -w, x+w, y
	default:
		a, d, e, f = w, h, x, y
	}
	c.op("q %s %s %s %s %s %s cm /%s Do Q", U(a), U(b), U(cc), U(d), U(e), U(f), im.Res)
}

func (c *Content) sortedFonts() []*Font {
	return sortedKeys(c.fonts, func(a, b *Font) bool { return a.idx < b.idx })
}

func (c *Content) sortedImages() []*Image {
	out := make([]*Image, 0, len(c.images))
	for im := range c.images {
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].idx < out[j].idx })
	return out
}

package coverlayout

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // the decoder for a PNG cover
	"strconv"

	xdraw "golang.org/x/image/draw"

	"github.com/crueber/scriobh/internal/imagesafe"
)

// Quality of the JPEGs made for screens and ebooks (KDP's and Vellum's figure for a cover).
const (
	FrontQuality = 90
	ThumbQuality = 85
)

// Caps on a cover picture's area, in pixels. A cover's picture has a cap of its own, above the PDF writer's 16
// million: a 300-dpi wrap at US Letter is 18.5 million and at A4 19. A JPEG is passed through into a PDF without
// decoding, so the cap is about the memory the derivatives take here (docs/ARCHITECTURE.md has the measured figures); a
// PNG would have to be decoded for the PDF and so has to fit the PDF's cap to be accepted at all.
const (
	MaxCoverPixels = 24_000_000
	MaxPNGPixels   = 16_000_000
	// MaxDecodeBytes is what decoding one cover picture may take (imagesafe.DecodeBytes: the decoded planes and, for a
	// progressive JPEG, its coefficients). A progressive JPEG at 4:4:4 takes 15 bytes a pixel, so one of more than 20
	// megapixels is refused with the advice to save it as a baseline JPEG, which takes 3 (or 1.5 at 4:2:0).
	MaxDecodeBytes = 300 << 20
)

// Decoded is a picture read for rendering.
type Decoded struct {
	Img  image.Image
	W, H int
}

// Decode reads a stored cover picture. The caller has the file's bytes; the header is checked against the cap before
// anything is decoded, and a CMYK JPEG is refused (the PDF writer is RGB only).
func Decode(b []byte) (Decoded, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return Decoded{}, &Refusal{"cover_picture_damaged", "The picture is damaged or not a PNG or a JPEG."}
	}
	if int64(cfg.Width)*int64(cfg.Height) > MaxCoverPixels {
		return Decoded{}, &Refusal{"cover_picture_large", fmt.Sprintf("The picture has %d million pixels, over the %d million a cover may have.", int64(cfg.Width)*int64(cfg.Height)/1_000_000, MaxCoverPixels/1_000_000)}
	}
	if format == "jpeg" && cfg.ColorModel == color.CMYKModel {
		return Decoded{}, &Refusal{"cover_picture_cmyk", "This JPEG is CMYK. Save it as RGB: the cover is drawn in RGB."}
	}
	if format == "jpeg" {
		// what a decode takes is read from the frame header; a JPEG whose header cannot be read is not decoded
		est, ok := imagesafe.DecodeBytes(b)
		if !ok {
			return Decoded{}, &Refusal{"cover_picture_damaged", "The picture is damaged or not a PNG or a JPEG."}
		}
		if est > MaxDecodeBytes {
			return Decoded{}, &Refusal{"cover_picture_progressive", "This JPEG takes more memory to prepare than the server allows for a cover (it is a large progressive JPEG): save it as a baseline JPEG."}
		}
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return Decoded{}, &Refusal{"cover_picture_damaged", "The picture is damaged or not a PNG or a JPEG."}
	}
	r := img.Bounds()
	return Decoded{Img: img, W: r.Dx(), H: r.Dy()}, nil
}

// Header reads a picture's size and kind without decoding it: format is png or jpeg.
func Header(b []byte) (w, h int, format string, cmyk bool, err error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, "", false, err
	}
	return cfg.Width, cfg.Height, format, format == "jpeg" && cfg.ColorModel == color.CMYKModel, nil
}

// parseHex reads #RRGGBB.
func parseHex(s string) color.RGBA {
	if len(s) == 7 && s[0] == '#' {
		if v, err := strconv.ParseUint(s[1:], 16, 32); err == nil {
			return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
		}
	}
	return color.RGBA{0x4A, 0x47, 0x40, 255}
}

// newCanvas is an opaque RGBA picture of w by h filled with c.
func newCanvas(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(img, img.Bounds(), &image.Uniform{C: c}, image.Point{}, xdraw.Src)
	return img
}

var white = color.RGBA{255, 255, 255, 255}

// RenderCrop is an image side or a cut of a wrap: the part src of the picture, scaled down (never up) so its long
// side is at most max, over white, so a picture with transparency becomes an opaque one.
func RenderCrop(d Decoded, src image.Rectangle, max int) *image.RGBA {
	w, h := FitWithin(src.Dx(), src.Dy(), max)
	out := newCanvas(w, h, white)
	sr := src.Add(d.Img.Bounds().Min)
	if w == src.Dx() && h == src.Dy() {
		// No scaling: the pixels are copied as they are, so a cut of a wrap is exactly the cut.
		xdraw.Draw(out, out.Bounds(), d.Img, sr.Min, xdraw.Over)
		return out
	}
	scaleInto(out, out.Bounds(), d.Img, sr)
	return out
}

// RenderPage is a page side: the trim's page in the side's background colour with the picture placed on it.
func RenderPage(d Decoded, t TrimIn, s Side) (*image.RGBA, error) {
	pl, err := PlacePage(d.W, d.H, t, s)
	if err != nil {
		return nil, err
	}
	out := newCanvas(pl.CanvasW, pl.CanvasH, parseHex(s.Background))
	if pl.Visible.Empty() || pl.Src.Empty() {
		return out, nil
	}
	scaleInto(out, pl.Visible, d.Img, pl.Src.Add(d.Img.Bounds().Min))
	return out, nil
}

// scaleInto draws the part sr of src (the crop is made first: nothing outside it is read) into dr of dst, scaled with
// Catmull-Rom, over what is there. A source more than twice the size it is to be drawn at is first averaged down by a
// whole number (a box pass that reads the decoded picture a block at a time and keeps only the small result), and the
// Catmull-Rom pass does the rest, where its quality matters (scale.go keeps its memory to the size of the output).
func scaleInto(dst *image.RGBA, dr image.Rectangle, src image.Image, sr image.Rectangle) {
	if f := min(sr.Dx()/dr.Dx(), sr.Dy()/dr.Dy()); f >= 2 {
		small := boxReduce(src, sr, f)
		src, sr = small, small.Bounds()
	}
	catmullRom(dst, dr, src, sr)
}

// boxReduce is the part sr of img with every f by f block of pixels averaged into one (the blocks at the right and
// bottom edge may be smaller). An image with transparency comes out premultiplied, as the drawing wants it.
func boxReduce(img image.Image, sr image.Rectangle, f int) *image.RGBA {
	ow, oh := (sr.Dx()+f-1)/f, (sr.Dy()+f-1)/f
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	switch m := img.(type) {
	case *image.YCbCr:
		for oy := 0; oy < oh; oy++ {
			y0, y1 := sr.Min.Y+oy*f, min(sr.Min.Y+(oy+1)*f, sr.Max.Y)
			for ox := 0; ox < ow; ox++ {
				x0, x1 := sr.Min.X+ox*f, min(sr.Min.X+(ox+1)*f, sr.Max.X)
				var sy, scb, scr, n uint32
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						yi, ci := m.YOffset(x, y), m.COffset(x, y)
						sy += uint32(m.Y[yi])
						scb += uint32(m.Cb[ci])
						scr += uint32(m.Cr[ci])
						n++
					}
				}
				r, g, b := color.YCbCrToRGB(uint8((sy+n/2)/n), uint8((scb+n/2)/n), uint8((scr+n/2)/n))
				o := out.PixOffset(ox, oy)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = r, g, b, 255
			}
		}
	case *image.NRGBA:
		for oy := 0; oy < oh; oy++ {
			y0, y1 := sr.Min.Y+oy*f, min(sr.Min.Y+(oy+1)*f, sr.Max.Y)
			for ox := 0; ox < ow; ox++ {
				x0, x1 := sr.Min.X+ox*f, min(sr.Min.X+(ox+1)*f, sr.Max.X)
				var sr_, sg, sb, sa, n uint64
				for y := y0; y < y1; y++ {
					row := m.PixOffset(x0, y)
					for x := x0; x < x1; x++ {
						r, g, b, a := uint64(m.Pix[row]), uint64(m.Pix[row+1]), uint64(m.Pix[row+2]), uint64(m.Pix[row+3])
						row += 4
						sr_ += (r * 0x101 * a) / 0xff
						sg += (g * 0x101 * a) / 0xff
						sb += (b * 0x101 * a) / 0xff
						sa += a * 0x101
						n++
					}
				}
				o := out.PixOffset(ox, oy)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8((sr_/n)>>8), uint8((sg/n)>>8), uint8((sb/n)>>8), uint8((sa/n)>>8)
			}
		}
	case *image.RGBA:
		for oy := 0; oy < oh; oy++ {
			y0, y1 := sr.Min.Y+oy*f, min(sr.Min.Y+(oy+1)*f, sr.Max.Y)
			for ox := 0; ox < ow; ox++ {
				x0, x1 := sr.Min.X+ox*f, min(sr.Min.X+(ox+1)*f, sr.Max.X)
				var sr_, sg, sb, sa, n uint64
				for y := y0; y < y1; y++ {
					row := m.PixOffset(x0, y)
					for x := x0; x < x1; x++ {
						sr_ += uint64(m.Pix[row]) * 0x101
						sg += uint64(m.Pix[row+1]) * 0x101
						sb += uint64(m.Pix[row+2]) * 0x101
						sa += uint64(m.Pix[row+3]) * 0x101
						row += 4
						n++
					}
				}
				o := out.PixOffset(ox, oy)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8((sr_/n)>>8), uint8((sg/n)>>8), uint8((sb/n)>>8), uint8((sa/n)>>8)
			}
		}
	default:
		for oy := 0; oy < oh; oy++ {
			y0, y1 := sr.Min.Y+oy*f, min(sr.Min.Y+(oy+1)*f, sr.Max.Y)
			for ox := 0; ox < ow; ox++ {
				x0, x1 := sr.Min.X+ox*f, min(sr.Min.X+(ox+1)*f, sr.Max.X)
				var sr_, sg, sb, sa, n uint64
				for y := y0; y < y1; y++ {
					for x := x0; x < x1; x++ {
						r, g, b, a := img.At(x, y).RGBA() // premultiplied, 16 bits
						sr_, sg, sb, sa, n = sr_+uint64(r), sg+uint64(g), sb+uint64(b), sa+uint64(a), n+1
					}
				}
				o := out.PixOffset(ox, oy)
				out.Pix[o], out.Pix[o+1], out.Pix[o+2], out.Pix[o+3] = uint8((sr_/n)>>8), uint8((sg/n)>>8), uint8((sb/n)>>8), uint8((sa/n)>>8)
			}
		}
	}
	return out
}

// RenderThumb scales a picture to ThumbWidth wide, never up.
func RenderThumb(img image.Image) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > ThumbWidth {
		h = roundPx(float64(h) * ThumbWidth / float64(w))
		if h < 1 {
			h = 1
		}
		w = ThumbWidth
	}
	out := newCanvas(w, h, white)
	scaleInto(out, out.Bounds(), img, b)
	return out
}

// EncodeJPEG writes an opaque picture as a JPEG.
func EncodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var errNoPicture = errors.New("coverlayout: the layout has no picture for that part")

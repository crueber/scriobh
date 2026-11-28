package publish

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/crueber/scriobh/internal/publish/pdfw"
)

// MaxImagePixels is the most pixels one picture may have to be put in a PDF,
// whether it is decoded or passed through (a variable so that a server may
// change it before it starts).
var MaxImagePixels int64 = 16_000_000

// Images in a PDF. A JPEG goes in as it is (the file is the stream). A PNG
// without an alpha channel or interlacing goes in as it is too, its data
// stream taken as a Flate stream with a PNG predictor, after the stream is
// checked to inflate to the right size. Anything else is decoded and written
// as 8-bit samples with a soft mask for transparency, within a budget of
// pixels for the whole book. WebP and CMYK JPEG are left out, with a warning.

const (
	maxDecodeTotalPixels = 64_000_000  // all of them
	maxJPEGPixels        = 200_000_000 // a JPEG is passed through, not decoded: only its claim is bounded
)

// noteProfile counts a picture that says how its colours are to be read (a
// gamma or a colour profile): a PDF here shows its samples as they are.
func (e *imageEmbedder) noteProfile(chunks map[string][]byte) {
	for _, k := range []string{"iCCP", "gAMA", "cHRM", "sRGB"} {
		if _, ok := chunks[k]; ok && k != "sRGB" {
			e.profiled++
			return
		}
	}
}

type imageEmbedder struct {
	profiled int // pictures with a colour profile or gamma that is not applied
	d        *pdfw.Doc
	decoded  int64
	cache    map[string]*pdfw.Image
	failed   map[string]string
}

func newImageEmbedder(d *pdfw.Doc) *imageEmbedder {
	return &imageEmbedder{d: d, cache: map[string]*pdfw.Image{}, failed: map[string]string{}}
}

// embed adds an image to the document and returns it, or nil and the reason.
func (e *imageEmbedder) embed(img Image) (*pdfw.Image, string) {
	key := img.Hash
	if key == "" {
		key = fmt.Sprintf("%d:%s", len(img.Data), img.Ext)
	}
	if img.Flat { // the same picture flattened on another colour is another object
		key += fmt.Sprintf("/flat%02x%02x%02x", img.Flatten[0], img.Flatten[1], img.Flatten[2])
	}
	if im, ok := e.cache[key]; ok {
		return im, ""
	}
	if why, ok := e.failed[key]; ok {
		return nil, why
	}
	im, why := e.build(img)
	if im == nil {
		e.failed[key] = why
		return nil, why
	}
	e.cache[key] = im
	return im, ""
}

func (e *imageEmbedder) build(img Image) (*pdfw.Image, string) {
	if len(img.Data) == 0 {
		return nil, "its bytes were not read (a preview or a dry run)"
	}
	switch img.Ext {
	case "jpg":
		return e.jpeg(img)
	case "png":
		return e.png(img)
	case "gif":
		return e.decode(img)
	}
	return nil, "this type of image cannot be put in a PDF"
}

func (e *imageEmbedder) jpeg(img Image) (*pdfw.Image, string) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil {
		return nil, "the JPEG is damaged"
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxJPEGPixels {
		return nil, "the JPEG is larger than the size a PDF will take"
	}
	var cs string
	switch cfg.ColorModel {
	case color.YCbCrModel:
		cs = "/DeviceRGB"
	case color.GrayModel:
		cs = "/DeviceGray"
	default:
		return nil, "a CMYK JPEG cannot be embedded; save it as RGB"
	}
	d, ok := cleanJPEG(img.Data)
	if !ok {
		return nil, "the JPEG is cut short"
	}
	if cs == "/DeviceRGB" && len(img.ICC) > 0 {
		cs = e.d.NewICC(img.ICC)
	} else if bytes.Contains(img.Data, []byte("ICC_PROFILE\x00")) {
		e.profiled++
	}
	return e.d.NewImage(pdfw.ImageSpec{Width: cfg.Width, Height: cfg.Height, ColorSpace: cs, BPC: 8, Filter: "/DCTDecode"}, d), ""
}

// pngChunks splits a PNG file into its chunks and checks each checksum.
func pngChunks(b []byte) (map[string][]byte, []byte, bool) {
	if len(b) < 8 || string(b[:8]) != "\x89PNG\r\n\x1a\n" {
		return nil, nil, false
	}
	chunks := map[string][]byte{}
	var idat []byte
	off := 8
	for off+12 <= len(b) {
		n := int(binary.BigEndian.Uint32(b[off:]))
		if n < 0 || off+12+n > len(b) {
			return nil, nil, false
		}
		typ := string(b[off+4 : off+8])
		data := b[off+8 : off+8+n]
		if crc32.ChecksumIEEE(b[off+4:off+8+n]) != binary.BigEndian.Uint32(b[off+8+n:]) {
			return nil, nil, false
		}
		switch typ {
		case "IDAT":
			idat = append(idat, data...)
		default:
			if _, dup := chunks[typ]; !dup {
				chunks[typ] = data
			}
		}
		off += 12 + n
		if typ == "IEND" {
			return chunks, idat, true
		}
	}
	return nil, nil, false
}

func (e *imageEmbedder) png(img Image) (*pdfw.Image, string) {
	chunks, idat, ok := pngChunks(img.Data)
	ihdr := chunks["IHDR"]
	if !ok || len(ihdr) != 13 || len(idat) == 0 {
		return e.decode(img)
	}
	w, h := int(binary.BigEndian.Uint32(ihdr)), int(binary.BigEndian.Uint32(ihdr[4:]))
	depth, ctype, comp, filt, interlace := int(ihdr[8]), int(ihdr[9]), ihdr[10], ihdr[11], ihdr[12]
	if w <= 0 || h <= 0 || comp != 0 || filt != 0 || interlace != 0 || chunks["tRNS"] != nil {
		return e.decode(img)
	}
	var cs string
	colors := 1
	switch {
	case ctype == 0 && (depth == 1 || depth == 2 || depth == 4 || depth == 8 || depth == 16):
		cs = "/DeviceGray"
	case ctype == 2 && (depth == 8 || depth == 16):
		cs, colors = "/DeviceRGB", 3
	case ctype == 3 && depth <= 8:
		pl := chunks["PLTE"]
		if len(pl) == 0 || len(pl)%3 != 0 || len(pl)/3 > 1<<depth {
			return e.decode(img)
		}
		cs = fmt.Sprintf("[/Indexed /DeviceRGB %d <%X>]", len(pl)/3-1, pl)
	default:
		return e.decode(img) // an alpha channel: a soft mask is needed
	}
	if int64(w)*int64(h) > MaxImagePixels {
		return nil, fmt.Sprintf("the PNG has %d million pixels, more than the %d million a PDF here takes", int64(w)*int64(h)/1_000_000, MaxImagePixels/1_000_000)
	}
	if cs == "/DeviceRGB" && len(img.ICC) > 0 {
		cs = e.d.NewICC(img.ICC)
	} else {
		e.noteProfile(chunks)
	}
	// The stream must inflate to exactly the rows the header promises, each
	// starting with a filter type from 0 to 4; it is read a row at a time, so
	// a hostile stream costs one row of memory and no more.
	rowBytes := (w*colors*depth + 7) / 8
	if rowBytes > 64<<20 {
		return nil, "a row of the PNG is too wide"
	}
	zr, err := zlib.NewReader(bytes.NewReader(idat))
	if err != nil {
		return e.decode(img)
	}
	defer zr.Close()
	row := make([]byte, rowBytes+1)
	for y := 0; y < h; y++ {
		if _, err := io.ReadFull(zr, row); err != nil {
			return e.decode(img)
		}
		if row[0] > 4 {
			return nil, "the PNG has a row with an invalid filter type"
		}
	}
	if n, _ := zr.Read(row[:1]); n != 0 {
		return e.decode(img) // more data than the header's rows
	}
	parms := fmt.Sprintf("<< /Predictor 15 /Colors %d /BitsPerComponent %d /Columns %d >>", colors, depth, w)
	return e.d.NewImage(pdfw.ImageSpec{Width: w, Height: h, ColorSpace: cs, BPC: depth, Filter: "/FlateDecode", Parms: parms}, idat), ""
}

// decode reads an image into samples and writes them out with a soft mask
// when it is not opaque.
func (e *imageEmbedder) decode(img Image) (*pdfw.Image, string) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(img.Data))
	if err != nil {
		return nil, "the image is damaged"
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px <= 0 || px > MaxImagePixels {
		return nil, fmt.Sprintf("the image has %d million pixels, more than the %d million that can be prepared for a PDF", px/1_000_000, MaxImagePixels/1_000_000)
	}
	if e.decoded+px > maxDecodeTotalPixels {
		return nil, "the images in this book that need preparing are over the pixel budget"
	}
	if img.Ext == "png" && len(img.ICC) == 0 {
		if chunks, _, ok := pngChunks(img.Data); ok {
			e.noteProfile(chunks)
		}
	}
	e.decoded += px
	var m image.Image
	switch img.Ext {
	case "png":
		m, err = png.Decode(bytes.NewReader(img.Data))
	case "gif":
		// The first frame only: gif.Decode stops after it, where DecodeAll would
		// keep every frame of an animation in memory.
		m, err = gif.Decode(bytes.NewReader(img.Data))
	case "jpg":
		m, err = jpeg.Decode(bytes.NewReader(img.Data))
	}
	if err != nil || m == nil {
		return nil, "the image is damaged"
	}
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, "the image is empty"
	}
	if int64(w)*int64(h) > MaxImagePixels { // a frame larger than the size the header promised
		return nil, "the image's first frame is larger than the size it declares"
	}
	rgb := make([]byte, 0, w*h*3)
	var alpha []byte
	if !img.Flat {
		alpha = make([]byte, 0, w*h)
	}
	opaque := true
	emit := func(r, g, b, a uint8) {
		if img.Flat && a != 255 { // over what is behind it, and no mask
			au := uint32(a)
			r = uint8((uint32(r)*au + uint32(img.Flatten[0])*(255-au) + 127) / 255)
			g = uint8((uint32(g)*au + uint32(img.Flatten[1])*(255-au) + 127) / 255)
			b = uint8((uint32(b)*au + uint32(img.Flatten[2])*(255-au) + 127) / 255)
			a = 255
		}
		rgb = append(rgb, r, g, b)
		if !img.Flat {
			alpha = append(alpha, a)
		}
		if a != 255 {
			opaque = false
		}
	}
	switch src := m.(type) {
	case *image.NRGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			o := src.PixOffset(b.Min.X, y)
			for x := b.Min.X; x < b.Max.X; x++ {
				emit(src.Pix[o], src.Pix[o+1], src.Pix[o+2], src.Pix[o+3])
				o += 4
			}
		}
	case *image.RGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			o := src.PixOffset(b.Min.X, y)
			for x := b.Min.X; x < b.Max.X; x++ {
				r, g, bl, a := src.Pix[o], src.Pix[o+1], src.Pix[o+2], src.Pix[o+3]
				o += 4
				if a != 0 && a != 255 {
					au := uint32(a)
					r = uint8(min((uint32(r)*0xff)/au, 0xff))
					g = uint8(min((uint32(g)*0xff)/au, 0xff))
					bl = uint8(min((uint32(bl)*0xff)/au, 0xff))
				}
				emit(r, g, bl, a)
			}
		}
	case *image.Paletted:
		pal := make([]color.NRGBA, len(src.Palette))
		for i, c := range src.Palette {
			pal[i] = color.NRGBAModel.Convert(c).(color.NRGBA)
		}
		for y := b.Min.Y; y < b.Max.Y; y++ {
			o := src.PixOffset(b.Min.X, y)
			for x := b.Min.X; x < b.Max.X; x++ {
				c := pal[src.Pix[o]]
				o++
				emit(c.R, c.G, c.B, c.A)
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(m.At(x, y)).(color.NRGBA)
				emit(c.R, c.G, c.B, c.A)
			}
		}
	}
	var mask *pdfw.Image
	if !opaque {
		mask = e.d.NewImage(pdfw.ImageSpec{Width: w, Height: h, ColorSpace: "/DeviceGray", BPC: 8}, alpha)
	}
	cs := "/DeviceRGB"
	if len(img.ICC) > 0 {
		cs = e.d.NewICC(img.ICC)
	}
	return e.d.NewImage(pdfw.ImageSpec{Width: w, Height: h, ColorSpace: cs, BPC: 8, Mask: mask}, rgb), ""
}

// PNGNeedsDecode says whether a PNG would have to be decoded to go in a PDF (an alpha channel, a transparency chunk,
// interlacing, a damaged stream), which takes memory a caller may want to reserve first; a plain RGB, grey or indexed
// PNG is passed through as its own stream.
func PNGNeedsDecode(data []byte) bool {
	chunks, idat, ok := pngChunks(data)
	ihdr := chunks["IHDR"]
	if !ok || len(ihdr) != 13 || len(idat) == 0 {
		return true
	}
	depth, ctype, comp, filt, interlace := int(ihdr[8]), int(ihdr[9]), ihdr[10], ihdr[11], ihdr[12]
	if comp != 0 || filt != 0 || interlace != 0 || chunks["tRNS"] != nil {
		return true
	}
	switch {
	case ctype == 0 && (depth == 1 || depth == 2 || depth == 4 || depth == 8 || depth == 16):
	case ctype == 2 && (depth == 8 || depth == 16):
	case ctype == 3 && depth <= 8:
	default:
		return true
	}
	return false
}

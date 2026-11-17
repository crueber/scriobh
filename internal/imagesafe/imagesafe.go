// Package imagesafe makes a picture fit to publish: it keeps what draws the
// picture and nothing else. A photo carries where it was taken (EXIF GPS), what
// took it, who made it, a thumbnail of the unedited original, provenance
// manifests (C2PA) and free text; none of that belongs in a file that leaves the
// project. Every format is cleaned by an allow-list, so a chunk or segment this
// code does not know is dropped, not kept: new kinds of metadata are the ones a
// deny-list misses.
//
// What it keeps, by format:
//
//   - JPEG: the tables and frame (DQT, DHT, SOFn, SOS and the scan data, DRI,
//     restart markers), a JFIF header rebuilt with its thumbnail removed, Adobe's
//     colour-transform marker, a well-formed ICC profile. Everything after the
//     end of image goes.
//   - PNG: IHDR, PLTE, IDAT, IEND, tRNS, gAMA, cHRM, sRGB, iCCP, sBIT, bKGD,
//     pHYs and the animation chunks acTL, fcTL, fdAT.
//   - GIF: the image data, the graphic control and the loop count of an
//     animation. Comments, plain text and other application extensions go.
//   - WebP: VP8, VP8L, VP8X (its EXIF and XMP flags cleared), ALPH, ANIM, ANMF
//     (cleaned inside) and ICCP.
//
// A JPEG that says it is rotated (EXIF orientation 2 to 8) is rotated for real
// and written again at high quality, because removing the EXIF removes the
// instruction and a portrait photo would otherwise appear on its side.
//
// Every function bounds what it reads: the declared size is checked against
// MaxPixels before anything is decoded.
package imagesafe

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
)

// MaxPixels bounds a picture's area.
const MaxPixels = 100_000_000

// MaxRotatePixels bounds a JPEG that is decoded to be rotated (the decoded
// picture takes about six bytes a pixel while it is turned).
const MaxRotatePixels = 25_000_000

// RotateQuality is the JPEG quality of a picture that had to be rotated.
const RotateQuality = 92

// Info says what a cleaned picture is.
type Info struct {
	Width, Height int
	// Orientation is the EXIF orientation the file had (1 when none).
	Orientation int
	// Rotated is set when the pixels were rotated and the file written again.
	Rotated bool
}

// Errors.
var (
	ErrDamaged  = errors.New("the image is damaged")
	ErrTooLarge = errors.New("the image is too large")
	ErrType     = errors.New("unsupported image type")
)

// Clean cleans data, a picture of type ext (png, jpg, gif or webp).
func Clean(ext string, data []byte) ([]byte, Info, error) { return CleanBounded(ext, data, 0) }

// CleanBounded is Clean for a caller that has a memory budget for the one case that decodes: a JPEG that says it is
// rotated is decoded, turned and written again, which takes DecodeBytes of the picture and four bytes a pixel more for the
// turned copy. A rotated picture that would take more than maxRotateBytes is refused (ErrTooLarge); 0 means the pixel limit
// of MaxRotatePixels alone.
func CleanBounded(ext string, data []byte, maxRotateBytes int64) ([]byte, Info, error) {
	return CleanGuarded(ext, data, maxRotateBytes, nil)
}

// Guard is asked, before a JPEG is decoded to be turned, for the memory that will take (the bytes it estimates); it returns
// what to call when the decode is over, or an error that stops the clean (and is returned as it is).
type Guard func(bytes int64) (release func(), err error)

// CleanGuarded is CleanBounded for a caller that shares its memory between jobs: guard holds the decode's share.
func CleanGuarded(ext string, data []byte, maxRotateBytes int64, guard Guard) ([]byte, Info, error) {
	switch ext {
	case "jpg", "jpeg":
		return cleanJPEG(data, maxRotateBytes, guard)
	case "png":
		return cleanPNG(data)
	case "gif":
		return cleanGIF(data)
	case "webp":
		return cleanWebP(data)
	}
	return nil, Info{}, ErrType
}

func pixelsOK(w, h int) error {
	if w <= 0 || h <= 0 {
		return ErrDamaged
	}
	if int64(w)*int64(h) > MaxPixels {
		return ErrTooLarge
	}
	return nil
}

// ---- JPEG -------------------------------------------------------------------------------

func cleanJPEG(d []byte, maxRotateBytes int64, guard Guard) ([]byte, Info, error) {
	if len(d) < 4 || d[0] != 0xFF || d[1] != 0xD8 {
		return nil, Info{}, ErrDamaged
	}
	info := Info{Orientation: 1}
	out := make([]byte, 0, len(d))
	out = append(out, 0xFF, 0xD8)
	i := 2
	sawFrame, sawScan := false, false
	for i < len(d) {
		if d[i] != 0xFF {
			return nil, Info{}, ErrDamaged
		}
		for i < len(d) && d[i] == 0xFF {
			i++
		}
		if i >= len(d) {
			return nil, Info{}, ErrDamaged
		}
		m := d[i]
		i++
		switch {
		case m == 0xD9: // end of image: everything after it is dropped
			if !sawFrame || !sawScan {
				return nil, Info{}, ErrDamaged
			}
			out = append(out, 0xFF, 0xD9)
			return finishJPEG(out, info, maxRotateBytes, guard)
		case m == 0x01 || (m >= 0xD0 && m <= 0xD7):
			out = append(out, 0xFF, m)
			continue
		case m == 0x00 || m == 0xD8:
			return nil, Info{}, ErrDamaged
		}
		if i+2 > len(d) {
			return nil, Info{}, ErrDamaged
		}
		n := int(binary.BigEndian.Uint16(d[i:]))
		if n < 2 || i+n > len(d) {
			return nil, Info{}, ErrDamaged
		}
		seg := d[i+2 : i+n] // the payload
		i += n
		keep := false
		var rebuilt []byte
		switch {
		case m == 0xE0: // JFIF: rebuilt without its thumbnail
			if len(seg) >= 14 && bytes.HasPrefix(seg, []byte("JFIF\x00")) {
				rebuilt = append([]byte(nil), seg[:14]...)
				rebuilt[12], rebuilt[13] = 0, 0
			}
		case m == 0xE1: // EXIF or XMP: read the orientation, keep nothing
			if o := exifOrientation(seg); o > 0 && info.Orientation == 1 {
				info.Orientation = o
			}
		case m == 0xE2: // ICC profile, if it is one
			if len(seg) > 14 && bytes.HasPrefix(seg, []byte("ICC_PROFILE\x00")) && seg[13] >= 1 && seg[12] >= 1 && seg[12] <= seg[13] {
				keep = true
			}
		case m == 0xEE: // Adobe colour transform
			keep = len(seg) == 12 && bytes.HasPrefix(seg, []byte("Adobe"))
		case m >= 0xE0 && m <= 0xEF, m >= 0xF0 && m <= 0xFE: // any other APPn, JPGn, comments
		case m == 0xC8: // reserved
		case m >= 0xC0 && m <= 0xCF: // frame headers, DHT (C4), DAC (CC)
			keep = true
			if m != 0xC4 && m != 0xCC {
				if len(seg) < 6 {
					return nil, Info{}, ErrDamaged
				}
				info.Height = int(binary.BigEndian.Uint16(seg[1:]))
				info.Width = int(binary.BigEndian.Uint16(seg[3:]))
				if err := pixelsOK(info.Width, info.Height); err != nil {
					return nil, Info{}, err
				}
				sawFrame = true
			}
		case m == 0xDB || m == 0xDD || m == 0xDC || m == 0xDF || m == 0xDE || m == 0xDA:
			keep = true
		default: // not a marker this knows
		}
		if rebuilt != nil {
			out = append(out, 0xFF, m)
			out = binary.BigEndian.AppendUint16(out, uint16(len(rebuilt)+2))
			out = append(out, rebuilt...)
		} else if keep {
			out = append(out, 0xFF, m)
			out = binary.BigEndian.AppendUint16(out, uint16(n))
			out = append(out, seg...)
		}
		if m == 0xDA { // the scan's entropy-coded data runs to the next real marker
			sawScan = true
			start := i
			for i < len(d) {
				if d[i] == 0xFF && i+1 < len(d) && d[i+1] != 0x00 && !(d[i+1] >= 0xD0 && d[i+1] <= 0xD7) && d[i+1] != 0xFF {
					break
				}
				i++
			}
			out = append(out, d[start:i]...)
		}
	}
	return nil, Info{}, ErrDamaged // no end of image
}

func finishJPEG(out []byte, info Info, maxRotateBytes int64, guard Guard) ([]byte, Info, error) {
	if info.Orientation < 2 || info.Orientation > 8 {
		return out, info, nil
	}
	if maxRotateBytes > 0 {
		if est, ok := DecodeBytes(out); !ok || est+4*int64(info.Width)*int64(info.Height) > maxRotateBytes {
			return nil, Info{}, fmt.Errorf("%w to turn upright (it says it is rotated)", ErrTooLarge)
		}
	}
	if int64(info.Width)*int64(info.Height) > MaxRotatePixels {
		return nil, Info{}, fmt.Errorf("%w to turn upright (it says it is rotated)", ErrTooLarge)
	}
	if guard != nil {
		est, _ := DecodeBytes(out)
		release, err := guard(est + 4*int64(info.Width)*int64(info.Height))
		if err != nil {
			return nil, Info{}, err
		}
		defer release()
	}
	// jpeg.Encode writes none: an RGB profile goes across the turn, or the colours change with it. The turned picture is
	// always three-component, so a profile for another colour space (Gray, CMYK) is dropped, not attached to RGB samples.
	icc := JPEGProfile(out)
	if !rgbProfile(icc) {
		icc = nil
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		return nil, Info{}, ErrDamaged
	}
	rot := orient(img, info.Orientation)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rot, &jpeg.Options{Quality: RotateQuality}); err != nil {
		return nil, Info{}, err
	}
	b := rot.Bounds()
	return WithJPEGProfile(buf.Bytes(), icc), Info{Width: b.Dx(), Height: b.Dy(), Orientation: info.Orientation, Rotated: true}, nil
}

// exifOrientation reads the orientation (1 to 8) from an APP1 payload, or 0.
func exifOrientation(seg []byte) int {
	if !bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
		return 0
	}
	t := seg[6:]
	if len(t) < 8 {
		return 0
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0
	}
	if bo.Uint16(t[2:]) != 42 {
		return 0
	}
	off := int(bo.Uint32(t[4:]))
	if off < 8 || off+2 > len(t) {
		return 0
	}
	n := int(bo.Uint16(t[off:]))
	for k := 0; k < n && k < 200; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 && bo.Uint16(t[e+2:]) == 3 && bo.Uint32(t[e+4:]) == 1 {
			if v := int(bo.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 0
		}
	}
	return 0
}

// orient returns img turned the way EXIF orientation o (2 to 8) says to show it.
func orient(img image.Image, o int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	ycc, _ := img.(*image.YCbCr) // a JPEG's own form: read it without a colour value (and its allocation) for each pixel
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			default: // 8
				sx, sy = w-1-y, x
			}
			if ycc != nil {
				px, py := b.Min.X+sx, b.Min.Y+sy
				yi, ci := ycc.YOffset(px, py), ycc.COffset(px, py)
				r, g, bl := color.YCbCrToRGB(ycc.Y[yi], ycc.Cb[ci], ycc.Cr[ci])
				i := dst.PixOffset(x, y)
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = r, g, bl, 255
				continue
			}
			dst.Set(x, y, img.At(b.Min.X+sx, b.Min.Y+sy))
		}
	}
	return dst
}

// ---- PNG --------------------------------------------------------------------------------

var pngKeep = map[string]bool{"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true, "cHRM": true, "sRGB": true,
	"iCCP": true, "sBIT": true, "bKGD": true, "pHYs": true, "acTL": true, "fcTL": true, "fdAT": true}

func cleanPNG(d []byte) ([]byte, Info, error) {
	sig := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(d, sig) {
		return nil, Info{}, ErrDamaged
	}
	info := Info{Orientation: 1}
	out := make([]byte, 0, len(d))
	out = append(out, sig...)
	i := len(sig)
	first, sawEnd := true, false
	for i+12 <= len(d) {
		n := int(binary.BigEndian.Uint32(d[i:]))
		if n < 0 || n > len(d) || i+12+n > len(d) {
			return nil, Info{}, ErrDamaged
		}
		typ := string(d[i+4 : i+8])
		if first {
			if typ != "IHDR" || n != 13 {
				return nil, Info{}, ErrDamaged
			}
			info.Width = int(binary.BigEndian.Uint32(d[i+8:]))
			info.Height = int(binary.BigEndian.Uint32(d[i+12:]))
			if info.Width < 0 || info.Height < 0 || info.Width > 1<<30 || info.Height > 1<<30 {
				return nil, Info{}, ErrTooLarge
			}
			if err := pixelsOK(info.Width, info.Height); err != nil {
				return nil, Info{}, err
			}
			first = false
		}
		if pngKeep[typ] {
			out = append(out, d[i:i+12+n]...)
		}
		i += 12 + n
		if typ == "IEND" {
			sawEnd = true
			break
		}
	}
	if !sawEnd {
		return nil, Info{}, ErrDamaged
	}
	return out, info, nil
}

// ---- GIF --------------------------------------------------------------------------------

func cleanGIF(d []byte) ([]byte, Info, error) {
	if len(d) < 13 || !(bytes.HasPrefix(d, []byte("GIF87a")) || bytes.HasPrefix(d, []byte("GIF89a"))) {
		return nil, Info{}, ErrDamaged
	}
	info := Info{Orientation: 1, Width: int(binary.LittleEndian.Uint16(d[6:])), Height: int(binary.LittleEndian.Uint16(d[8:]))}
	if err := pixelsOK(info.Width, info.Height); err != nil {
		return nil, Info{}, err
	}
	flags := d[10]
	i := 13
	if flags&0x80 != 0 {
		i += 3 * (1 << (uint(flags&7) + 1))
	}
	if i > len(d) {
		return nil, Info{}, ErrDamaged
	}
	out := make([]byte, 0, len(d))
	out = append(out, d[:i]...)
	subBlocks := func(j int) (int, error) {
		for {
			if j >= len(d) {
				return 0, ErrDamaged
			}
			n := int(d[j])
			j++
			if n == 0 {
				return j, nil
			}
			j += n
		}
	}
	for i < len(d) {
		switch d[i] {
		case 0x3B:
			out = append(out, 0x3B)
			return out, info, nil
		case 0x21:
			if i+2 > len(d) {
				return nil, Info{}, ErrDamaged
			}
			label := d[i+1]
			end, err := subBlocks(i + 2)
			if err != nil {
				return nil, Info{}, err
			}
			if label == 0xF9 || (label == 0xFF && bytes.HasPrefix(d[i+2:end], []byte("\x0bNETSCAPE2.0"))) {
				out = append(out, d[i:end]...)
			}
			i = end
		case 0x2C:
			if i+10 > len(d) {
				return nil, Info{}, ErrDamaged
			}
			f := d[i+9]
			j := i + 10
			if f&0x80 != 0 {
				j += 3 * (1 << (uint(f&7) + 1))
			}
			j++
			end, err := subBlocks(j)
			if err != nil {
				return nil, Info{}, err
			}
			out = append(out, d[i:end]...)
			i = end
		default:
			return nil, Info{}, ErrDamaged
		}
	}
	return nil, Info{}, ErrDamaged
}

// ---- WebP -------------------------------------------------------------------------------

func cleanWebP(d []byte) ([]byte, Info, error) {
	if len(d) < 12 || string(d[:4]) != "RIFF" || string(d[8:12]) != "WEBP" {
		return nil, Info{}, ErrDamaged
	}
	total := int(binary.LittleEndian.Uint32(d[4:])) + 8
	if total > len(d) || total < 12 {
		return nil, Info{}, ErrDamaged
	}
	info := Info{Orientation: 1}
	body, err := webpChunks(d[12:total], &info, true)
	if err != nil {
		return nil, Info{}, err
	}
	if info.Width == 0 {
		return nil, Info{}, ErrDamaged
	}
	if err := pixelsOK(info.Width, info.Height); err != nil {
		return nil, Info{}, err
	}
	out := make([]byte, 0, len(body)+12)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(body)+4))
	out = append(out, "WEBP"...)
	out = append(out, body...)
	return out, info, nil
}

// webpChunks keeps the allowed chunks of a run of chunks. top is the file's own
// level (the canvas size is read there); inside an ANMF frame it is false.
func webpChunks(d []byte, info *Info, top bool) ([]byte, error) {
	var out []byte
	i := 0
	for i+8 <= len(d) {
		cc := string(d[i : i+4])
		n := int(binary.LittleEndian.Uint32(d[i+4:]))
		if n < 0 || i+8+n > len(d) {
			return nil, ErrDamaged
		}
		payload := d[i+8 : i+8+n]
		end := i + 8 + n + n&1
		var keep []byte
		switch cc {
		case "VP8X":
			if n < 10 {
				return nil, ErrDamaged
			}
			if top {
				info.Width = 1 + (int(payload[4]) | int(payload[5])<<8 | int(payload[6])<<16)
				info.Height = 1 + (int(payload[7]) | int(payload[8])<<8 | int(payload[9])<<16)
			}
			p := append([]byte(nil), payload...)
			p[0] &^= 0x08 | 0x04 // no EXIF, no XMP
			keep = p
		case "VP8L":
			if n < 5 || payload[0] != 0x2f {
				return nil, ErrDamaged
			}
			if top && info.Width == 0 {
				bits := binary.LittleEndian.Uint32(payload[1:])
				info.Width = int(bits&0x3FFF) + 1
				info.Height = int(bits>>14&0x3FFF) + 1
			}
			keep = payload
		case "VP8 ":
			if n < 10 || payload[3] != 0x9d || payload[4] != 0x01 || payload[5] != 0x2a {
				return nil, ErrDamaged
			}
			if top && info.Width == 0 {
				info.Width = int(binary.LittleEndian.Uint16(payload[6:]) & 0x3FFF)
				info.Height = int(binary.LittleEndian.Uint16(payload[8:]) & 0x3FFF)
			}
			keep = payload
		case "ALPH", "ANIM", "ICCP":
			keep = payload
		case "ANMF":
			if n < 16 {
				return nil, ErrDamaged
			}
			inner, err := webpChunks(payload[16:], info, false)
			if err != nil {
				return nil, err
			}
			keep = append(append([]byte(nil), payload[:16]...), inner...)
		}
		if keep != nil {
			out = append(out, cc...)
			out = binary.LittleEndian.AppendUint32(out, uint32(len(keep)))
			out = append(out, keep...)
			if len(keep)&1 == 1 {
				out = append(out, 0)
			}
		}
		i = end
	}
	return out, nil
}

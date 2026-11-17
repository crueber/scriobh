package coverlayout

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"io"
	"strings"

	"github.com/crueber/scriobh/internal/imagesafe"
)

// Colour. The decoders here read a picture's samples and ignore the profile it carries, and the derivatives are written
// from those samples unchanged, so the profile goes with them: a picture in Display P3 or Adobe RGB is shown in those
// colours by a reader that honours profiles, and its derivative says the same. (Converting to sRGB would need a colour
// management engine; carrying the profile keeps the pixels and what they mean together.) The cover upload says when a
// profile is not sRGB, since a reader that ignores profiles then shows the colours differently, and when it leaves a
// profile out.
//
// A profile is carried only if it is plausible and small: at most MaxProfileBytes, with the signature "acsp" at byte 36
// of its header and the colour space "RGB " at byte 16 (a derivative is an RGB picture; a grey or CMYK profile on it
// would be wrong). Anything else is left out, not cut down, so a front, a thumbnail and an EPUB never carry a huge one.

// MaxProfileBytes is the largest profile a derivative carries.
const MaxProfileBytes = 1 << 20

func validProfile(p []byte) bool {
	return len(p) >= 128 && len(p) <= MaxProfileBytes && string(p[36:40]) == "acsp" && string(p[16:20]) == "RGB "
}

// profileOf finds the ICC profile of a PNG or a JPEG that a derivative may carry (nil: none, or not one that may be
// carried), and says whether the picture had a profile at all, so that a profile left out can be reported.
func profileOf(b []byte) (use []byte, present bool) {
	var raw []byte
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		raw, present = iccFromPNG(b)
	case len(b) > 2 && b[0] == 0xFF && b[1] == 0xD8:
		raw = imagesafe.JPEGProfile(b)
		present = raw != nil
	}
	if validProfile(raw) {
		return raw, true
	}
	return nil, present
}

// iccFromPNG reads a PNG's iCCP chunk. present is true when there is one; the profile is nil when it inflates to more
// than MaxProfileBytes (it is not cut down).
func iccFromPNG(d []byte) (profile []byte, present bool) {
	for off := 8; off+12 <= len(d); {
		n := int(binary.BigEndian.Uint32(d[off:]))
		if n < 0 || off+12+n > len(d) {
			return nil, false
		}
		typ, data := string(d[off+4:off+8]), d[off+8:off+8+n]
		if typ == "iCCP" {
			z := bytes.IndexByte(data, 0)
			if z < 0 || z+2 > len(data) || data[z+1] != 0 {
				return nil, true
			}
			r, err := zlib.NewReader(bytes.NewReader(data[z+2:]))
			if err != nil {
				return nil, true
			}
			defer r.Close()
			out, err := io.ReadAll(io.LimitReader(r, MaxProfileBytes+1))
			if err != nil || len(out) > MaxProfileBytes {
				return nil, true
			}
			return out, true
		}
		if typ == "IDAT" || typ == "IEND" {
			return nil, false
		}
		off += 12 + n
	}
	return nil, false
}

// isSRGB reports whether a picture with this profile (nil: none) is in sRGB. With no profile it is taken to be (a
// reader shows it so); a PNG with an sRGB chunk is; a profile is sRGB when it says so in its description.
func isSRGB(b []byte, icc []byte) bool {
	if icc == nil {
		return true
	}
	if bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")) && bytes.Contains(b[:min(len(b), 1<<16)], []byte("sRGB")) {
		return true
	}
	return strings.Contains(strings.ToLower(string(icc[:min(len(icc), 4096)])), "srgb")
}

// withICC writes the profile into a JPEG.
func withICC(jpg, icc []byte) []byte { return imagesafe.WithJPEGProfile(jpg, icc) }

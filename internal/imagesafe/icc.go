package imagesafe

import "encoding/binary"

const iccHeader = "ICC_PROFILE\x00"

// maxRotatedProfile is the largest profile that goes across a turn. An RGB display profile is a few hundred bytes to a
// few hundred kilobytes; the cap keeps a re-embedded profile from being the biggest thing in the picture.
const maxRotatedProfile = 1 << 20

// rgbProfile reports whether a profile can describe the samples jpeg.Encode writes (three components, YCbCr, which a
// decoder turns into RGB): the ICC header's profile file signature "acsp" at byte 36, the data colour space "RGB " at byte
// 16, and a size under the cap. A Gray, CMYK or Lab profile on RGB samples would change the colours, so it is not carried.
func rgbProfile(p []byte) bool {
	return len(p) >= 128 && len(p) <= maxRotatedProfile && string(p[36:40]) == "acsp" && string(p[16:20]) == "RGB "
}

// JPEGProfile returns a JPEG's ICC profile, its APP2 segments joined in sequence, or nil when it has none or is missing a
// piece. It reads the segments before the scan and nothing else.
func JPEGProfile(d []byte) []byte {
	var parts [][]byte
	i := 2
	for i+4 <= len(d) {
		if d[i] != 0xFF {
			break
		}
		for i < len(d) && d[i] == 0xFF {
			i++
		}
		if i >= len(d) {
			break
		}
		m := d[i]
		i++
		if m == 0xD9 || m == 0xDA {
			break
		}
		if m == 0x01 || (m >= 0xD0 && m <= 0xD8) || m == 0 {
			continue
		}
		if i+2 > len(d) {
			break
		}
		l := int(binary.BigEndian.Uint16(d[i:]))
		if l < 2 || i+l > len(d) {
			break
		}
		seg := d[i+2 : i+l]
		i += l
		if m == 0xE2 && len(seg) > 14 && string(seg[:12]) == iccHeader {
			seq, n := int(seg[12]), int(seg[13])
			if seq >= 1 && seq <= n {
				for len(parts) < n {
					parts = append(parts, nil)
				}
				if n == len(parts) {
					parts[seq-1] = seg[14:]
				}
			}
		}
	}
	var out []byte
	for _, p := range parts {
		if p == nil {
			return nil
		}
		out = append(out, p...)
	}
	return out
}

// WithJPEGProfile writes a profile into a JPEG as APP2 segments right after the start of image (a long profile in pieces).
func WithJPEGProfile(jpg, icc []byte) []byte {
	if len(icc) == 0 || len(jpg) < 2 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		return jpg
	}
	const piece = 65533 - 2 - len(iccHeader) - 2
	n := (len(icc) + piece - 1) / piece
	if n > 255 {
		return jpg
	}
	out := make([]byte, 0, len(jpg)+len(icc)+n*20)
	out = append(out, jpg[:2]...)
	for i := 0; i < n; i++ {
		chunk := icc[i*piece : min((i+1)*piece, len(icc))]
		out = append(out, 0xFF, 0xE2)
		out = binary.BigEndian.AppendUint16(out, uint16(2+len(iccHeader)+2+len(chunk)))
		out = append(out, iccHeader...)
		out = append(out, byte(i+1), byte(n))
		out = append(out, chunk...)
	}
	return append(out, jpg[2:]...)
}

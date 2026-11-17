package imagesafe

import "encoding/binary"

// DecodeBytes estimates the memory Go's JPEG decoder takes to decode a JPEG, from its frame header alone: the planes of
// the decoded picture (a byte for each sample, so 3 bytes a pixel at 4:4:4, 1.5 at 4:2:0) and, for a progressive JPEG,
// which the decoder keeps whole as coefficients before it can draw it, four bytes for each sample more (15 a pixel at
// 4:4:4, measured: a 24-megapixel progressive 4:4:4 JPEG takes about 390 MB, the same picture as a baseline 4:2:0 JPEG
// 36 MB). ok is false when there is no frame header to read.
func DecodeBytes(d []byte) (n int64, ok bool) {
	if len(d) < 4 || d[0] != 0xFF || d[1] != 0xD8 {
		return 0, false
	}
	i := 2
	for i+4 <= len(d) {
		if d[i] != 0xFF {
			return 0, false
		}
		for i < len(d) && d[i] == 0xFF {
			i++
		}
		if i >= len(d) {
			return 0, false
		}
		m := d[i]
		i++
		if m == 0x01 || (m >= 0xD0 && m <= 0xD8) || m == 0x00 {
			continue
		}
		if i+2 > len(d) {
			return 0, false
		}
		l := int(binary.BigEndian.Uint16(d[i:]))
		if l < 2 || i+l > len(d) {
			return 0, false
		}
		seg := d[i+2 : i+l]
		i += l
		if m >= 0xC0 && m <= 0xCF && m != 0xC4 && m != 0xC8 && m != 0xCC {
			if len(seg) < 6 {
				return 0, false
			}
			h, w, nc := int64(binary.BigEndian.Uint16(seg[1:])), int64(binary.BigEndian.Uint16(seg[3:])), int(seg[5])
			if nc < 1 || nc > 4 || len(seg) < 6+3*nc {
				return 0, false
			}
			hmax, vmax := 1, 1
			for c := 0; c < nc; c++ {
				hv := seg[6+3*c+1]
				hmax, vmax = max(hmax, int(hv>>4)), max(vmax, int(hv&15))
			}
			var samples float64
			for c := 0; c < nc; c++ {
				hv := seg[6+3*c+1]
				samples += float64(int(hv>>4)*int(hv&15)) / float64(hmax*vmax)
			}
			per := samples                                        // bytes a pixel in the decoded planes
			if m == 0xC2 || m == 0xC6 || m == 0xCA || m == 0xCE { // progressive
				per += 4 * samples
			}
			return int64(per * float64(w*h)), true
		}
		if m == 0xDA {
			return 0, false
		}
	}
	return 0, false
}

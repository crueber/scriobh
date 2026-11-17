package publish

import (
	"bytes"
	"encoding/binary"
)

// JPEGOrientation reads the EXIF orientation (1 to 8) from the start of a JPEG
// file; 1 when there is none or it cannot be read. The camera's setting is a
// property of how the picture is to be shown, so a printed page honours it.
func JPEGOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			return 1
		}
		marker := data[pos+1]
		if marker == 0xFF {
			pos++
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		n := int(binary.BigEndian.Uint16(data[pos+2:]))
		if n < 2 || pos+2+n > len(data) {
			return 1
		}
		if marker == 0xE1 {
			seg := data[pos+4 : pos+2+n]
			if o := exifOrientation(seg); o != 0 {
				return o
			}
		}
		pos += 2 + n
	}
	return 1
}

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
	for i := 0; i < n; i++ {
		e := off + 2 + 12*i
		if e+12 > len(t) {
			return 0
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			v := int(bo.Uint16(t[e+8:]))
			if v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// cleanJPEG copies the parts of a JPEG a printed page needs, and nothing else: the quantisation and
// Huffman (or arithmetic conditioning) tables, the restart interval, the frame header, the scans, and
// the Adobe marker, which says whether three components are RGB or YCbCr. It is an allow-list, like
// internal/imagesafe: every other segment is left out, whatever its marker (EXIF and XMP, IPTC, JFIF and
// its thumbnail, comments, an ICC profile (the PDF names DeviceRGB and applies none), the application
// segments, the JPEGn extensions 0xF0 to 0xFD, the reserved 0xC8 and every marker nobody has assigned),
// so a camera's GPS position, a name in a comment or a block copied into one has no way into a book.
// Anything after the end-of-image marker is cut. ok is false when the data is damaged or ends before
// its end-of-image marker.
func cleanJPEG(data []byte) (out []byte, ok bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, false
	}
	out = append(make([]byte, 0, len(data)), 0xFF, 0xD8)
	pos := 2
	for pos < len(data) {
		if data[pos] != 0xFF {
			return nil, false
		}
		for pos+1 < len(data) && data[pos+1] == 0xFF { // fill bytes
			pos++
		}
		if pos+1 >= len(data) {
			return nil, false
		}
		marker := data[pos+1]
		switch {
		case marker == 0xD9:
			return append(out, 0xFF, 0xD9), true
		case marker == 0x00, marker == 0x01, marker >= 0xD0 && marker <= 0xD7, marker == 0xD8:
			// stuffing, TEM, a restart or a second start of image outside a scan: nothing a decoder needs
			pos += 2
			continue
		}
		if pos+4 > len(data) {
			return nil, false
		}
		n := int(binary.BigEndian.Uint16(data[pos+2:]))
		if n < 2 || pos+2+n > len(data) {
			return nil, false
		}
		seg := data[pos : pos+2+n]
		if jpegSegmentNeeded(marker, seg[4:]) {
			out = append(out, seg...)
		}
		pos += 2 + n
		if marker == 0xDA {
			// entropy-coded data up to the next real marker
			for pos+1 < len(data) {
				if data[pos] == 0xFF && data[pos+1] != 0x00 && !(data[pos+1] >= 0xD0 && data[pos+1] <= 0xD7) && data[pos+1] != 0xFF {
					break
				}
				out = append(out, data[pos])
				pos++
			}
			if pos+1 >= len(data) {
				return nil, false
			}
		}
	}
	return nil, false
}

// jpegSegmentNeeded is the allow-list: whether a segment with this marker and payload is one a decoder
// needs. The payload of the Adobe marker must be exactly the 12 bytes the marker has.
func jpegSegmentNeeded(marker byte, payload []byte) bool {
	switch {
	case marker == 0xC4, marker == 0xCC: // DHT, DAC
		return true
	case marker >= 0xC0 && marker <= 0xCF: // the frame headers (0xC8 is reserved, not one)
		return marker != 0xC8
	case marker == 0xDB, marker == 0xDD, marker == 0xDA: // DQT, DRI, SOS
		return true
	case marker == 0xEE: // Adobe
		return len(payload) == 12 && bytes.HasPrefix(payload, []byte("Adobe"))
	}
	return false
}

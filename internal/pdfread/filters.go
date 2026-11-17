package pdfread

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"math"
)

// Stream filters. Flate is read through limits (size and expansion ratio) and
// a damaged stream yields what it produced before it broke; the rest are small
// decoders written here. Image filters (DCT, JPX, CCITT, JBIG2) are never
// decoded: the importer does not read pictures.

var errBomb = errors.New("a stream inflates far beyond its size")

// flate inflates b. max bounds the output; ratio, asked once the output passes
// floor, bounds output over input. A damaged stream returns what was read with
// damaged set.
func flate(b []byte, max int64, ratio int, floor int64) (out []byte, damaged bool, err error) {
	zr, e := zlib.NewReader(bytes.NewReader(b))
	if e != nil {
		// Some producers write raw deflate or a bad header: nothing is read.
		return nil, true, nil
	}
	defer zr.Close()
	var buf bytes.Buffer
	chunk := make([]byte, 32<<10)
	for {
		n, e := zr.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if int64(buf.Len()) > max {
				return nil, false, errBomb
			}
			if int64(buf.Len()) > floor && int64(buf.Len()) > int64(ratio)*int64(len(b)+1) {
				return nil, false, errBomb
			}
		}
		if e == io.EOF {
			return buf.Bytes(), false, nil
		}
		if e != nil {
			return buf.Bytes(), true, nil
		}
	}
}

func asciiHex(b []byte) []byte {
	out := make([]byte, 0, len(b)/2+1)
	hi := -1
	for _, c := range b {
		if c == '>' {
			break
		}
		if !isHexDigit(c) {
			continue
		}
		if hi < 0 {
			hi = int(hexVal(c))
		} else {
			out = append(out, byte(hi<<4)|hexVal(c))
			hi = -1
		}
	}
	if hi >= 0 {
		out = append(out, byte(hi<<4))
	}
	return out
}

func ascii85(b []byte) []byte {
	var out []byte
	var grp [5]byte
	n := 0
	flush := func(k int) {
		for i := n; i < 5; i++ {
			grp[i] = 'u' - '!'
		}
		var v uint32
		for i := 0; i < 5; i++ {
			v = v*85 + uint32(grp[i])
		}
		for i := 0; i < k-1; i++ {
			out = append(out, byte(v>>(24-8*uint(i))))
		}
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case isWS(c):
		case c == '~':
			if n > 1 {
				flush(n)
			}
			return out
		case c == 'z' && n == 0:
			out = append(out, 0, 0, 0, 0)
		case c >= '!' && c <= 'u':
			grp[n] = c - '!'
			n++
			if n == 5 {
				flush(5)
				n = 0
			}
		}
	}
	if n > 1 {
		flush(n)
	}
	return out
}

func runLength(b []byte, max int64) ([]byte, error) {
	var out []byte
	for i := 0; i < len(b); {
		l := int(b[i])
		i++
		switch {
		case l == 128:
			return out, nil
		case l < 128:
			end := min(i+l+1, len(b))
			out = append(out, b[i:end]...)
			i = end
		default:
			if i >= len(b) {
				return out, nil
			}
			for k := 0; k < 257-l; k++ {
				out = append(out, b[i])
			}
			i++
		}
		if int64(len(out)) > max {
			return nil, errBomb
		}
	}
	return out, nil
}

// lzw decodes PDF's variant (MSB first, 9 to 12 bits, early change by default).
func lzw(b []byte, early int, max int64) ([]byte, error) {
	type entry struct {
		prev int
		ch   byte
		len  int
	}
	table := make([]entry, 258, 4096)
	for i := 0; i < 256; i++ {
		table[i] = entry{-1, byte(i), 1}
	}
	var out []byte
	bits, nbits := uint32(0), 0
	width := 9
	prev := -1
	pos := 0
	buf := make([]byte, 0, 4096)
	expand := func(code int) []byte {
		buf = buf[:0]
		for c := code; c >= 0; c = table[c].prev {
			buf = append(buf, table[c].ch)
		}
		for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
			buf[i], buf[j] = buf[j], buf[i]
		}
		return buf
	}
	for {
		for nbits < width {
			if pos >= len(b) {
				return out, nil
			}
			bits = bits<<8 | uint32(b[pos])
			pos++
			nbits += 8
		}
		code := int(bits>>(uint(nbits-width))) & (1<<uint(width) - 1)
		nbits -= width
		switch {
		case code == 256:
			table = table[:258]
			width, prev = 9, -1
			continue
		case code == 257:
			return out, nil
		}
		var s []byte
		switch {
		case code < len(table) && (code < 256 || code >= 258):
			s = expand(code)
		case code == len(table) && prev >= 0:
			s = expand(prev)
			s = append(s, s[0])
		default:
			return out, nil
		}
		out = append(out, s...)
		if int64(len(out)) > max {
			return nil, errBomb
		}
		if prev >= 0 && len(table) < 4096 {
			table = append(table, entry{prev, s[0], 0})
		}
		prev = code
		switch n := len(table) + early; {
		case n >= 4096:
			width = 12
		case n >= 2048:
			width = 12
		case n >= 1024:
			width = 11
		case n >= 512:
			width = 10
		default:
			width = 9
		}
	}
}

// predictor undoes a PNG or TIFF predictor (Flate and LZW parameters).
func predictor(b []byte, pred, colors, bpc, columns int) []byte {
	if pred < 2 {
		return b
	}
	if colors < 1 {
		colors = 1
	}
	if bpc < 1 {
		bpc = 8
	}
	if columns < 1 {
		columns = 1
	}
	rowBytes, pixelBytes, ok := predictorGeometry(int64(colors), int64(bpc), int64(columns), 1<<24)
	if !ok {
		return b
	}
	row, bpp := int(rowBytes), int(pixelBytes) // at most 1<<24: an int holds them

	if pred == 2 { // TIFF, byte-wise for 8 bits
		if bpc != 8 {
			return b
		}
		out := append([]byte(nil), b...)
		for r := 0; r+row <= len(out); r += row {
			for i := bpp; i < row; i++ {
				out[r+i] += out[r+i-bpp]
			}
		}
		return out
	}
	stride := row + 1
	rows := (len(b) + stride - 1) / stride
	out := make([]byte, 0, int(min(int64(rows)*int64(row), math.MaxInt))) // in int64: on a 32-bit int the product can wrap
	prev := make([]byte, row)
	cur := make([]byte, row)
	for r := 0; r < rows; r++ {
		line := b[r*stride:]
		if len(line) == 0 {
			break
		}
		ft := line[0]
		data := line[1:]
		for i := range cur {
			cur[i] = 0
		}
		copy(cur, data[:min(len(data), row)])
		for i := 0; i < row; i++ {
			var a, up, c byte
			if i >= bpp {
				a = cur[i-bpp]
				c = prev[i-bpp]
			}
			up = prev[i]
			switch ft {
			case 1:
				cur[i] += a
			case 2:
				cur[i] += up
			case 3:
				cur[i] += byte((int(a) + int(up)) / 2)
			case 4:
				p := int(a) + int(up) - int(c)
				pa, pb, pc := abs(p-int(a)), abs(p-int(up)), abs(p-int(c))
				switch {
				case pa <= pb && pa <= pc:
					cur[i] += a
				case pb <= pc:
					cur[i] += up
				default:
					cur[i] += c
				}
			}
		}
		out = append(out, cur...)
		prev, cur = cur, prev
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Package font reads the TrueType faces the PDF writer sets type in and cuts a
// face down to the glyphs a document uses.
//
// The faces are embedded in the binary (data/), unmodified; they are the only
// fonts a PDF can name, so the reader here is written for them and for what a
// subset needs: the character map, the advances, the legacy kerning table and
// the outlines. There is no shaping: a character maps to one glyph, and the
// pair of two glyphs may be kerned. That is right for Latin, Greek and
// Cyrillic text and wrong for scripts that need contextual forms; the layout
// says so rather than emit what it cannot draw.
package font

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
)

// Font is a parsed TrueType face with glyph outlines.
type Font struct {
	data []byte

	UnitsPerEm int
	Ascent     int // hhea, in font units
	Descent    int // positive: the depth below the baseline
	LineGap    int
	CapHeight  int // the height of H
	XHeight    int // the height of x
	NumGlyphs  int
	ItalicDeg  float64 // italic angle in degrees, negative when it leans right
	Bold       bool
	Italic     bool
	Mono       bool
	BBox       [4]int // head xMin, yMin, xMax, yMax
	PostScript string // name table id 6

	advance []uint16
	lsb     []int16
	loca    []uint32
	glyf    []byte
	cmap    map[rune]uint16
	ascii   [128]uint16
	kern    map[uint32]int16
	name    []byte
}

var errFont = errors.New("font: damaged font file")

func u16(b []byte, off int) int {
	if off < 0 || off+2 > len(b) {
		return 0
	}
	return int(binary.BigEndian.Uint16(b[off:]))
}

func i16(b []byte, off int) int {
	return int(int16(u16(b, off)))
}

func u32(b []byte, off int) int {
	if off < 0 || off+4 > len(b) {
		return 0
	}
	return int(binary.BigEndian.Uint32(b[off:]))
}

// Parse reads a TrueType face (glyf outlines; a .ttc collection or a CFF font
// is refused).
func Parse(data []byte) (*Font, error) {
	if len(data) < 12 || u32(data, 0) != 0x00010000 {
		return nil, fmt.Errorf("%w: not a TrueType file", errFont)
	}
	n := u16(data, 4)
	tables := map[string][]byte{}
	for i := 0; i < n; i++ {
		rec := 12 + 16*i
		if rec+16 > len(data) {
			return nil, errFont
		}
		tag := string(data[rec : rec+4])
		off, ln := u32(data, rec+8), u32(data, rec+12)
		if off < 0 || ln < 0 || off+ln > len(data) {
			return nil, fmt.Errorf("%w: table %q is out of range", errFont, tag)
		}
		tables[tag] = data[off : off+ln]
	}
	for _, need := range []string{"head", "hhea", "maxp", "hmtx", "cmap", "loca", "glyf"} {
		if tables[need] == nil {
			return nil, fmt.Errorf("%w: no %q table", errFont, need)
		}
	}
	f := &Font{data: data}
	head := tables["head"]
	f.UnitsPerEm = u16(head, 18)
	if f.UnitsPerEm < 16 || f.UnitsPerEm > 16384 {
		return nil, fmt.Errorf("%w: units per em %d", errFont, f.UnitsPerEm)
	}
	f.BBox = [4]int{i16(head, 36), i16(head, 38), i16(head, 40), i16(head, 42)}
	macStyle := u16(head, 44)
	f.Bold, f.Italic = macStyle&1 != 0, macStyle&2 != 0
	locaFmt := i16(head, 50)
	hhea := tables["hhea"]
	f.Ascent, f.Descent, f.LineGap = i16(hhea, 4), -i16(hhea, 6), i16(hhea, 8)
	nh := u16(hhea, 34)
	f.NumGlyphs = u16(tables["maxp"], 4)
	if f.NumGlyphs < 2 || nh < 1 || nh > f.NumGlyphs {
		return nil, fmt.Errorf("%w: glyph counts", errFont)
	}
	hmtx := tables["hmtx"]
	if len(hmtx) < nh*4+(f.NumGlyphs-nh)*2 {
		return nil, fmt.Errorf("%w: hmtx is short", errFont)
	}
	f.advance = make([]uint16, f.NumGlyphs)
	f.lsb = make([]int16, f.NumGlyphs)
	for g := 0; g < f.NumGlyphs; g++ {
		if g < nh {
			f.advance[g] = uint16(u16(hmtx, g*4))
			f.lsb[g] = int16(i16(hmtx, g*4+2))
		} else {
			f.advance[g] = f.advance[nh-1]
			f.lsb[g] = int16(i16(hmtx, nh*4+(g-nh)*2))
		}
	}
	loca := tables["loca"]
	f.loca = make([]uint32, f.NumGlyphs+1)
	for g := 0; g <= f.NumGlyphs; g++ {
		switch locaFmt {
		case 0:
			if (g+1)*2 > len(loca) {
				return nil, fmt.Errorf("%w: loca is short", errFont)
			}
			f.loca[g] = uint32(u16(loca, g*2)) * 2
		default:
			if (g+1)*4 > len(loca) {
				return nil, fmt.Errorf("%w: loca is short", errFont)
			}
			f.loca[g] = uint32(u32(loca, g*4))
		}
		if g > 0 && f.loca[g] < f.loca[g-1] {
			return nil, fmt.Errorf("%w: loca goes backwards", errFont)
		}
	}
	f.glyf = tables["glyf"]
	if int(f.loca[f.NumGlyphs]) > len(f.glyf) {
		return nil, fmt.Errorf("%w: glyf is short", errFont)
	}
	if err := f.readCmap(tables["cmap"]); err != nil {
		return nil, err
	}
	for r := rune(0); r < 128; r++ {
		f.ascii[r] = f.cmap[r]
	}
	f.readKern(tables["kern"])
	if post := tables["post"]; len(post) >= 16 {
		f.ItalicDeg = float64(i16(post, 4)) + float64(u16(post, 6))/65536
		f.Mono = u32(post, 12) != 0
	}
	f.name = tables["name"]
	f.PostScript = nameString(tables["name"], 6)
	f.CapHeight = f.glyphTop('H')
	f.XHeight = f.glyphTop('x')
	if f.CapHeight == 0 {
		f.CapHeight = f.Ascent * 7 / 10
	}
	if f.XHeight == 0 {
		f.XHeight = f.CapHeight * 7 / 10
	}
	return f, nil
}

func nameString(name []byte, id int) string {
	if len(name) < 6 {
		return ""
	}
	cnt, so := u16(name, 2), u16(name, 4)
	for i := 0; i < cnt; i++ {
		rec := 6 + 12*i
		if rec+12 > len(name) {
			break
		}
		pl, nid, ln, off := u16(name, rec), u16(name, rec+6), u16(name, rec+8), u16(name, rec+10)
		if nid != id || so+off+ln > len(name) {
			continue
		}
		raw := name[so+off : so+off+ln]
		switch pl {
		case 1:
			return string(raw)
		case 3:
			var out []byte
			for j := 0; j+1 < len(raw); j += 2 {
				if r := binary.BigEndian.Uint16(raw[j:]); r < 0x80 {
					out = append(out, byte(r))
				}
			}
			return string(out)
		}
	}
	return ""
}

func (f *Font) glyphTop(r rune) int {
	g, ok := f.cmap[r]
	if !ok {
		return 0
	}
	a, b := f.loca[g], f.loca[g+1]
	if b-a < 10 {
		return 0
	}
	return i16(f.glyf, int(a)+8) // yMax
}

func (f *Font) readCmap(cm []byte) error {
	n := u16(cm, 2)
	best, bestScore := -1, -1
	for i := 0; i < n; i++ {
		rec := 4 + 8*i
		plat, enc, off := u16(cm, rec), u16(cm, rec+2), u32(cm, rec+4)
		if off <= 0 || off+4 > len(cm) {
			continue
		}
		format := u16(cm, off)
		score := -1
		switch {
		case plat == 3 && enc == 10 && format == 12:
			score = 4
		case plat == 0 && (enc == 4 || enc == 6) && format == 12:
			score = 3
		case plat == 3 && enc == 1 && format == 4:
			score = 2
		case plat == 0 && format == 4:
			score = 1
		}
		if score > bestScore {
			best, bestScore = off, score
		}
	}
	if best < 0 {
		return fmt.Errorf("%w: no usable character map", errFont)
	}
	f.cmap = map[rune]uint16{}
	switch u16(cm, best) {
	case 4:
		segX2 := u16(cm, best+6)
		seg := segX2 / 2
		ends, starts, deltas, offs := best+14, best+16+segX2, best+16+2*segX2, best+16+3*segX2
		for s := 0; s < seg; s++ {
			end, start := u16(cm, ends+2*s), u16(cm, starts+2*s)
			delta, ro := u16(cm, deltas+2*s), u16(cm, offs+2*s)
			if start > end || end == 0xFFFF && start == 0xFFFF {
				continue
			}
			for c := start; c <= end; c++ {
				var g int
				if ro == 0 {
					g = (c + delta) & 0xFFFF
				} else {
					p := offs + 2*s + ro + 2*(c-start)
					if p+2 > len(cm) {
						continue
					}
					g = u16(cm, p)
					if g != 0 {
						g = (g + delta) & 0xFFFF
					}
				}
				if g != 0 && g < f.NumGlyphs {
					f.cmap[rune(c)] = uint16(g)
				}
			}
		}
	case 12:
		ng := u32(cm, best+12)
		for i := 0; i < ng && best+16+12*i+12 <= len(cm); i++ {
			start, end, g := u32(cm, best+16+12*i), u32(cm, best+20+12*i), u32(cm, best+24+12*i)
			if end < start || end-start > 0x20000 {
				continue
			}
			for c := start; c <= end; c++ {
				if gg := g + c - start; gg != 0 && gg < f.NumGlyphs {
					f.cmap[rune(c)] = uint16(gg)
				}
			}
		}
	}
	if len(f.cmap) == 0 {
		return fmt.Errorf("%w: empty character map", errFont)
	}
	return nil
}

// readKern reads the horizontal format 0 pairs of a version 0 'kern' table.
func (f *Font) readKern(k []byte) {
	if len(k) < 4 || u16(k, 0) != 0 {
		return
	}
	n := u16(k, 2)
	off := 4
	for i := 0; i < n && off+6 <= len(k); i++ {
		ln, cov := u16(k, off+2), u16(k, off+4)
		if ln < 6 {
			break
		}
		if cov&1 != 0 && cov>>8 == 0 && cov&4 == 0 { // horizontal, format 0, not cross-stream
			np := u16(k, off+6)
			if f.kern == nil {
				f.kern = make(map[uint32]int16, np)
			}
			for p := 0; p < np && off+14+6*p+6 <= len(k); p++ {
				l, r, v := u16(k, off+14+6*p), u16(k, off+16+6*p), i16(k, off+18+6*p)
				if cov&2 == 0 { // minimum values would replace, not add: leave them
					f.kern[uint32(l)<<16|uint32(r)] = int16(v)
				}
			}
		}
		off += ln
	}
}

// Glyph returns the glyph for a rune; ok is false when the face has none.
func (f *Font) Glyph(r rune) (uint16, bool) {
	if r >= 0 && r < 128 {
		g := f.ascii[r]
		return g, g != 0
	}
	g, ok := f.cmap[r]
	return g, ok
}

// Advance is a glyph's advance width in font units.
func (f *Font) Advance(g uint16) int {
	if int(g) >= len(f.advance) {
		return 0
	}
	return int(f.advance[g])
}

// Kern is the adjustment, in font units, between two glyphs.
func (f *Font) Kern(l, r uint16) int {
	if f.kern == nil {
		return 0
	}
	return int(f.kern[uint32(l)<<16|uint32(r)])
}

// HasKern reports whether the face has kerning pairs.
func (f *Font) HasKern() bool { return len(f.kern) > 0 }

// Runes returns every character the face maps, sorted.
func (f *Font) Runes() []rune {
	out := make([]rune, 0, len(f.cmap))
	for r := range f.cmap {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Scale converts font units to the unit of size: units * size / unitsPerEm,
// rounded half away from zero, in integers (so layout does not depend on
// floating point behaviour of the machine).
func (f *Font) Scale(units int, size int64) int64 {
	n := int64(units) * size
	d := int64(f.UnitsPerEm)
	if n >= 0 {
		return (n + d/2) / d
	}
	return -((-n + d/2) / d)
}

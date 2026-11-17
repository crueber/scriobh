package font

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// Subset is a face cut down to some glyphs, ready to be embedded in a PDF.
type Subset struct {
	// Data is the TrueType file: head, hhea, maxp, hmtx, loca, glyf only.
	// Hinting is dropped (a PDF viewer draws unhinted outlines, and a printer
	// at 1200 dpi does not need it).
	Data []byte
	// NewID maps an original glyph id to its id in the subset.
	NewID map[uint16]uint16
	// Orig maps a subset id back to the original id (index 0 is the .notdef).
	Orig []uint16
	// Advance is each subset glyph's advance in font units.
	Advance []int
}

const (
	cArgWords    = 0x0001
	cHaveScale   = 0x0008
	cMore        = 0x0020
	cHaveXYScale = 0x0040
	cHave2x2     = 0x0080
	cHaveInstr   = 0x0100
)

// components lists the glyph ids a composite glyph is made of (and reports
// whether the glyph is a composite).
func (f *Font) components(g uint16) (kids []uint16, composite bool, err error) {
	a, b := int(f.loca[g]), int(f.loca[int(g)+1])
	if b-a < 10 {
		return nil, false, nil
	}
	d := f.glyf[a:b]
	if i16(d, 0) >= 0 {
		return nil, false, nil
	}
	off := 10
	for {
		if off+4 > len(d) {
			return nil, true, fmt.Errorf("%w: composite glyph %d is cut short", errFont, g)
		}
		flags, kid := u16(d, off), u16(d, off+2)
		kids = append(kids, uint16(kid))
		off += 4
		if flags&cArgWords != 0 {
			off += 4
		} else {
			off += 2
		}
		switch {
		case flags&cHaveScale != 0:
			off += 2
		case flags&cHaveXYScale != 0:
			off += 4
		case flags&cHave2x2 != 0:
			off += 8
		}
		if flags&cMore == 0 {
			return kids, true, nil
		}
	}
}

// Subset cuts the face to gids (and the components of any composite among
// them). Glyph 0 stays the .notdef; the others are numbered 1.. in the order
// given, components after them in the order found, so the result depends only
// on the arguments.
func (f *Font) Subset(gids []uint16) (*Subset, error) {
	order := []uint16{0}
	seen := map[uint16]bool{0: true}
	add := func(g uint16) {
		if int(g) < f.NumGlyphs && !seen[g] {
			seen[g] = true
			order = append(order, g)
		}
	}
	for _, g := range gids {
		add(g)
	}
	for i := 0; i < len(order); i++ {
		kids, comp, err := f.components(order[i])
		if err != nil {
			return nil, err
		}
		if comp {
			for _, k := range kids {
				add(k)
			}
		}
	}
	if len(order) > 0xFFFF {
		return nil, fmt.Errorf("font: too many glyphs for one font")
	}
	s := &Subset{NewID: make(map[uint16]uint16, len(order)), Orig: order, Advance: make([]int, len(order))}
	for i, g := range order {
		s.NewID[g] = uint16(i)
		s.Advance[i] = f.Advance(g)
	}
	var glyf []byte
	loca := make([]byte, 4*(len(order)+1))
	hmtx := make([]byte, 4*len(order))
	for i, g := range order {
		binary.BigEndian.PutUint32(loca[4*i:], uint32(len(glyf)))
		binary.BigEndian.PutUint16(hmtx[4*i:], uint16(f.advance[g]))
		binary.BigEndian.PutUint16(hmtx[4*i+2:], uint16(f.lsb[g]))
		a, b := int(f.loca[g]), int(f.loca[int(g)+1])
		if b-a >= 10 {
			out, err := f.rewriteGlyph(f.glyf[a:b], s.NewID)
			if err != nil {
				return nil, err
			}
			glyf = append(glyf, out...)
			for len(glyf)%4 != 0 {
				glyf = append(glyf, 0)
			}
		}
	}
	binary.BigEndian.PutUint32(loca[4*len(order):], uint32(len(glyf)))

	tables := f.tablesForSubset()
	head := append([]byte(nil), tables["head"]...)
	binary.BigEndian.PutUint32(head[8:], 0)
	binary.BigEndian.PutUint16(head[50:], 1) // long loca
	hhea := append([]byte(nil), tables["hhea"]...)
	binary.BigEndian.PutUint16(hhea[34:], uint16(len(order)))
	maxp := append([]byte(nil), tables["maxp"]...)
	binary.BigEndian.PutUint16(maxp[4:], uint16(len(order)))
	s.Data = assemble(map[string][]byte{"head": head, "hhea": hhea, "maxp": maxp, "hmtx": hmtx, "loca": loca, "glyf": glyf, "name": f.noticeTable()})
	return s, nil
}

// noticeTable is a name table holding only the font's copyright notice, its
// licence description and its licence address (name ids 0, 13 and 14), copied
// from the original: the licences ask that a copy of the font carry its notice,
// and a subset embedded in a PDF does, readable by any tool that reads a name
// table. It costs the length of the notice.
func (f *Font) noticeTable() []byte {
	name := f.name
	var recs []struct {
		id  int
		str []byte
	}
	if len(name) >= 6 {
		cnt, so := u16(name, 2), u16(name, 4)
		seen := map[int]bool{}
		for i := 0; i < cnt; i++ {
			rec := 6 + 12*i
			if rec+12 > len(name) {
				break
			}
			pl, enc, id, ln, off := u16(name, rec), u16(name, rec+2), u16(name, rec+6), u16(name, rec+8), u16(name, rec+10)
			if pl != 3 || enc != 1 || (id != 0 && id != 13 && id != 14) || seen[id] || so+off+ln > len(name) {
				continue
			}
			seen[id] = true
			recs = append(recs, struct {
				id  int
				str []byte
			}{id, name[so+off : so+off+ln]})
		}
	}
	out := make([]byte, 6+12*len(recs))
	binary.BigEndian.PutUint16(out[2:], uint16(len(recs)))
	binary.BigEndian.PutUint16(out[4:], uint16(6+12*len(recs)))
	var storage []byte
	for i, r := range recs {
		rec := 6 + 12*i
		binary.BigEndian.PutUint16(out[rec:], 3)
		binary.BigEndian.PutUint16(out[rec+2:], 1)
		binary.BigEndian.PutUint16(out[rec+4:], 0x409)
		binary.BigEndian.PutUint16(out[rec+6:], uint16(r.id))
		binary.BigEndian.PutUint16(out[rec+8:], uint16(len(r.str)))
		binary.BigEndian.PutUint16(out[rec+10:], uint16(len(storage)))
		storage = append(storage, r.str...)
	}
	return append(out, storage...)
}

func (f *Font) tablesForSubset() map[string][]byte {
	t := map[string][]byte{}
	n := u16(f.data, 4)
	for i := 0; i < n; i++ {
		rec := 12 + 16*i
		tag := string(f.data[rec : rec+4])
		off, ln := u32(f.data, rec+8), u32(f.data, rec+12)
		switch tag {
		case "head", "hhea", "maxp":
			t[tag] = f.data[off : off+ln]
		}
	}
	return t
}

// rewriteGlyph copies one glyph with its instructions removed and its
// component ids renumbered.
func (f *Font) rewriteGlyph(d []byte, newID map[uint16]uint16) ([]byte, error) {
	nc := i16(d, 0)
	if nc >= 0 {
		end := 10 + 2*nc
		if end+2 > len(d) {
			return nil, fmt.Errorf("%w: glyph is cut short", errFont)
		}
		il := u16(d, end)
		if end+2+il > len(d) {
			return nil, fmt.Errorf("%w: glyph instructions are cut short", errFont)
		}
		out := make([]byte, 0, len(d)-il)
		out = append(out, d[:end]...)
		out = append(out, 0, 0)
		out = append(out, d[end+2+il:]...)
		return out, nil
	}
	out := append([]byte(nil), d[:10]...)
	off := 10
	for {
		if off+4 > len(d) {
			return nil, fmt.Errorf("%w: composite glyph is cut short", errFont)
		}
		flags, kid := u16(d, off), u16(d, off+2)
		nid, ok := newID[uint16(kid)]
		if !ok {
			return nil, fmt.Errorf("font: component glyph %d was not kept", kid)
		}
		size := 4
		if flags&cArgWords != 0 {
			size += 4
		} else {
			size += 2
		}
		switch {
		case flags&cHaveScale != 0:
			size += 2
		case flags&cHaveXYScale != 0:
			size += 4
		case flags&cHave2x2 != 0:
			size += 8
		}
		if off+size > len(d) {
			return nil, fmt.Errorf("%w: composite glyph is cut short", errFont)
		}
		comp := append([]byte(nil), d[off:off+size]...)
		binary.BigEndian.PutUint16(comp, uint16(flags&^cHaveInstr))
		binary.BigEndian.PutUint16(comp[2:], nid)
		out = append(out, comp...)
		off += size
		if flags&cMore == 0 {
			return out, nil
		}
	}
}

func checksum(b []byte) uint32 {
	var sum uint32
	for i := 0; i < len(b); i += 4 {
		var w [4]byte
		copy(w[:], b[i:min(i+4, len(b))])
		sum += binary.BigEndian.Uint32(w[:])
	}
	return sum
}

// assemble writes an sfnt with the tables in tag order, each padded to four
// bytes, and sets head's checkSumAdjustment.
func assemble(tables map[string][]byte) []byte {
	tags := make([]string, 0, len(tables))
	for t := range tables {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	n := len(tags)
	hdr := 12 + 16*n
	var maxPow, log2 int
	for maxPow = 1; maxPow*2 <= n; maxPow *= 2 {
		log2++
	}
	out := make([]byte, hdr)
	binary.BigEndian.PutUint32(out[0:], 0x00010000)
	binary.BigEndian.PutUint16(out[4:], uint16(n))
	binary.BigEndian.PutUint16(out[6:], uint16(maxPow*16))
	binary.BigEndian.PutUint16(out[8:], uint16(log2))
	binary.BigEndian.PutUint16(out[10:], uint16(n*16-maxPow*16))
	headOff := 0
	for i, t := range tags {
		data := tables[t]
		off := len(out)
		rec := 12 + 16*i
		copy(out[rec:], t)
		binary.BigEndian.PutUint32(out[rec+4:], checksum(data))
		binary.BigEndian.PutUint32(out[rec+8:], uint32(off))
		binary.BigEndian.PutUint32(out[rec+12:], uint32(len(data)))
		out = append(out, data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		if t == "head" {
			headOff = off
		}
	}
	if headOff > 0 {
		binary.BigEndian.PutUint32(out[headOff+8:], 0xB1B0AFBA-checksum(out))
	}
	return out
}

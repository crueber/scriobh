package pdfw

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/publish/font"
)

// Font is a face used in the document. Text is shown as two-byte codes; a
// code is assigned the first time a glyph is used, so the subset that is
// embedded at the end numbers its glyphs in that same order.
type Font struct {
	Res string // the name in page resources: F1, F2, ...
	idx int
	// Base is the font's name without the subset tag.
	Base string
	face *font.Font
	// codes maps an original glyph id to its code; order and runes are indexed
	// by code-1.
	codes map[uint16]uint16
	order []uint16
	runes []rune
	obj   int
	// Fixed and styles go into the descriptor.
	Serif bool
}

// NewFont registers a face. base is the PostScript-style name without spaces.
func (d *Doc) NewFont(base string, f *font.Font) *Font {
	pf := &Font{Res: fmt.Sprintf("F%d", len(d.fonts)+1), idx: len(d.fonts), Base: base, face: f, codes: map[uint16]uint16{}, Serif: true}
	d.fonts = append(d.fonts, pf)
	return pf
}

// Code returns the code that shows glyph gid, assigning one on first use. r is
// the character the glyph stands for (for the ToUnicode map; the first
// character seen for a glyph wins).
func (f *Font) Code(gid uint16, r rune) uint16 {
	if gid == 0 {
		return 0 // the .notdef is never shown: callers check for it
	}
	if c, ok := f.codes[gid]; ok {
		return c
	}
	f.order = append(f.order, gid)
	f.runes = append(f.runes, r)
	c := uint16(len(f.order))
	f.codes[gid] = c
	return c
}

// Used reports how many glyphs the font has been asked for.
func (f *Font) Used() int { return len(f.order) }

func (f *Font) tag() string {
	h := sha256.New()
	h.Write([]byte(f.Base))
	for _, g := range f.order {
		fmt.Fprintf(h, ",%d", g)
	}
	sum := h.Sum(nil)
	var b [6]byte
	for i := range b {
		b[i] = 'A' + sum[i]%26
	}
	return string(b[:])
}

func (f *Font) write(d *Doc) error {
	sub, err := f.face.Subset(f.order)
	if err != nil {
		return err
	}
	name := f.tag() + "+" + f.Base
	upem := int64(f.face.UnitsPerEm)
	scale := func(v int) int64 {
		n := int64(v) * 1000
		if n >= 0 {
			return (n + upem/2) / upem
		}
		return -((-n + upem/2) / upem)
	}
	ff := d.Alloc()
	d.PutStream(ff, fmt.Sprintf("/Length1 %d", len(sub.Data)), sub.Data, true)
	flags := 32 // nonsymbolic
	if f.face.Mono {
		flags |= 1
	} else if f.Serif {
		flags |= 2
	}
	if f.face.Italic {
		flags |= 64
	}
	if f.face.Bold {
		flags |= 1 << 18
	}
	stem := 80
	if f.face.Bold {
		stem = 140
	}
	desc := d.Obj(fmt.Sprintf("<< /Type /FontDescriptor /FontName /%s /Flags %d /FontBBox [%d %d %d %d] /ItalicAngle %d /Ascent %d /Descent %d /CapHeight %d /StemV %d /FontFile2 %d 0 R >>",
		name, flags, scale(f.face.BBox[0]), scale(f.face.BBox[1]), scale(f.face.BBox[2]), scale(f.face.BBox[3]),
		int(f.face.ItalicDeg), scale(f.face.Ascent), -scale(f.face.Descent), scale(f.face.CapHeight), stem, ff))
	// Widths of the glyphs used, codes 1..n (the .notdef is code 0).
	var w strings.Builder
	if len(f.order) > 0 {
		w.WriteString("1 [")
		for i := range f.order {
			if i > 0 {
				w.WriteByte(' ')
			}
			fmt.Fprintf(&w, "%d", scale(sub.Advance[i+1]))
		}
		w.WriteString("]")
	}
	cid := d.Obj(fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /CIDToGIDMap /Identity /DW 0 /W [%s] >>", name, desc, w.String()))
	tu := d.Alloc()
	d.PutStream(tu, "", f.toUnicode(), true)
	f.obj = d.Obj(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", name, cid, tu))
	return nil
}

// toUnicode is the CMap that maps each code to the character it stands for.
func (f *Font) toUnicode() []byte {
	var b strings.Builder
	b.WriteString("/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n")
	b.WriteString("/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n")
	b.WriteString("/CMapName /Adobe-Identity-UCS def\n/CMapType 2 def\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n")
	idx := make([]int, len(f.order))
	for i := range idx {
		idx[i] = i
	}
	sort.Ints(idx)
	for start := 0; start < len(idx); start += 100 {
		end := min(start+100, len(idx))
		fmt.Fprintf(&b, "%d beginbfchar\n", end-start)
		for _, i := range idx[start:end] {
			r := f.runes[i]
			if r < 0 || r > 0x10FFFF || r >= 0xD800 && r <= 0xDFFF {
				r = 0xFFFD
			}
			fmt.Fprintf(&b, "<%04X> <%s>\n", i+1, utf16Hex(r))
		}
		b.WriteString("endbfchar\n")
	}
	b.WriteString("endcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
	return []byte(b.String())
}

func utf16Hex(r rune) string {
	if r < 0x10000 {
		return fmt.Sprintf("%04X", r)
	}
	r -= 0x10000
	return fmt.Sprintf("%04X%04X", 0xD800+(r>>10), 0xDC00+(r&0x3FF))
}

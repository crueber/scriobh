package pdfread

import (
	"bytes"
	"sort"
	"strings"
)

// Font is what the text reader needs of a font dictionary: how to cut a
// string into codes, what each code means, how wide it is, and whether the
// face is bold, italic or monospaced.
type Font struct {
	Name     string // BaseFont, without the subset tag
	Bold     bool
	Italic   bool
	Mono     bool
	Vertical bool // vertical writing mode (not read)
	Symbolic bool

	composite bool
	codeLen   int
	enc       *[256]rune
	toUni     *cmap
	enc2      *cmap // composite font's encoding CMap
	ucs2      bool  // the encoding's codes are Unicode (UCS2 or UTF16 CMaps)
	hasToUni  bool
	hasEnc    bool

	// widths, in thousandths of an em
	first    int
	widths   []float64
	missing  float64
	cid      *cidWidths
	dw       float64
	hasW     bool
	wscale   float64 // Type 3: glyph space to thousandths
	truncCMs bool
}

type widthRun struct {
	lo, hi uint32
	w      float64
}

var subsetTag = func(s string) string {
	if len(s) > 7 && s[6] == '+' {
		ok := true
		for i := 0; i < 6; i++ {
			if s[i] < 'A' || s[i] > 'Z' {
				ok = false
			}
		}
		if ok {
			return s[7:]
		}
	}
	return s
}

func (f *File) loadFont(d Dict) *Font {
	ft := &Font{missing: 0, dw: 1000, wscale: 1}
	sub, _ := f.Resolve(d["Subtype"]).(Name)
	base, _ := f.Resolve(d["BaseFont"]).(Name)
	ft.Name = subsetTag(string(base))
	desc, _ := f.Resolve(d["FontDescriptor"]).(Dict)
	if sub == "Type0" {
		ft.composite = true
		if df, _ := f.Resolve(d["DescendantFonts"]).(Array); len(df) > 0 {
			if cf, ok := f.Resolve(df[0]).(Dict); ok {
				if desc == nil {
					desc, _ = f.Resolve(cf["FontDescriptor"]).(Dict)
				}
				ref, _ := df[0].(Ref)
				ft.cid = f.cidWidthsOf(ref.Num, cf)
				if ft.Name == "" {
					bn, _ := f.Resolve(cf["BaseFont"]).(Name)
					ft.Name = subsetTag(string(bn))
				}
			}
		}
		switch e := f.Resolve(d["Encoding"]).(type) {
		case Name:
			ne := string(e)
			ft.hasEnc = true
			ft.codeLen = 2
			if strings.HasSuffix(ne, "-V") {
				ft.Vertical = true
			}
			if strings.Contains(ne, "UCS2") || strings.Contains(ne, "UTF16") {
				ft.ucs2 = true
			}
		case *Stream:
			ft.enc2 = f.cmapOf(d["Encoding"], e)
			ft.codeLen = 2
			if ft.enc2.wmode == 1 {
				ft.Vertical = true
			}
			if w, _ := f.Resolve(e.Dict["WMode"]).(float64); w == 1 {
				ft.Vertical = true
			}
		}
		if ft.codeLen == 0 {
			ft.codeLen = 2
		}
	} else {
		ft.codeLen = 1
		f.loadSimpleEncoding(ft, d, sub, desc)
		if fc, ok := f.Resolve(d["FirstChar"]).(float64); ok {
			ft.first = fint(fc)
		}
		if w, ok := f.Resolve(d["Widths"]).(Array); ok {
			for i, x := range w {
				if i >= 65536 || f.widthLeft <= 0 {
					break
				}
				f.widthLeft--
				v, _ := f.Resolve(x).(float64)
				ft.widths = append(ft.widths, v)
			}
			ft.hasW = len(ft.widths) > 0
		}
		if sub == "Type3" {
			if fm, ok := f.Resolve(d["FontMatrix"]).(Array); ok && len(fm) >= 1 {
				if a, ok := f.Resolve(fm[0]).(float64); ok && a != 0 {
					ft.wscale = a * 1000
				}
			}
		}
		if desc != nil {
			if mw, ok := f.Resolve(desc["MissingWidth"]).(float64); ok {
				ft.missing = mw
			}
		}
	}
	if tu, ok := f.Resolve(d["ToUnicode"]).(*Stream); ok {
		ft.toUni = f.cmapOf(d["ToUnicode"], tu)
		ft.hasToUni = len(ft.toUni.uni) > 0
		ft.truncCMs = ft.toUni.truncated
		if ft.composite && ft.enc2 == nil && len(ft.toUni.spaces) > 0 {
			ft.enc2 = &cmap{spaces: ft.toUni.spaces}
		}
	}
	f.styleOf(ft, desc)
	return ft
}

func (f *File) styleOf(ft *Font, desc Dict) {
	n := strings.ToLower(ft.Name)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(n, s) {
				return true
			}
		}
		return false
	}
	ft.Bold = has("bold", "black", "heavy", "semibold", "demibold", "extrabold")
	ft.Italic = has("italic", "oblique", "-ital", "slanted")
	ft.Mono = has("courier", "mono", "consolas", "typewriter", "lucidaconsole", "menlo")
	if desc != nil {
		if fl, ok := f.Resolve(desc["Flags"]).(float64); ok {
			v := f64i(fl)
			if v&1 != 0 {
				ft.Mono = true
			}
			if v&4 != 0 {
				ft.Symbolic = true
			}
			if v&64 != 0 {
				ft.Italic = true
			}
			if v&(1<<18) != 0 {
				ft.Bold = true
			}
		}
		if w, ok := f.Resolve(desc["FontWeight"]).(float64); ok && w >= 600 {
			ft.Bold = true
		}
		if a, ok := f.Resolve(desc["ItalicAngle"]).(float64); ok && (a <= -3 || a >= 3) {
			ft.Italic = true
		}
	}
}

func (f *File) loadSimpleEncoding(ft *Font, d Dict, sub Name, desc Dict) {
	baseName := "StandardEncoding"
	if sub == "TrueType" {
		baseName = "WinAnsiEncoding"
	}
	var diffs Array
	explicit := false // the dictionary names the base encoding
	switch e := f.Resolve(d["Encoding"]).(type) {
	case Name:
		baseName = string(e)
		ft.hasEnc = true
		explicit = true
	case Dict:
		ft.hasEnc = true
		if b, ok := f.Resolve(e["BaseEncoding"]).(Name); ok {
			baseName = string(b)
			explicit = true
		}
		diffs, _ = f.Resolve(e["Differences"]).(Array)
	}
	ft.enc = baseEncoding(baseName)
	// With no base named, an embedded Type 1 font's own encoding is the base.
	if !explicit && desc != nil && sub != "TrueType" && sub != "Type3" {
		if ff, ok := f.Resolve(desc["FontFile"]).(*Stream); ok {
			for code, name := range f.type1Encoding(ff) {
				if r := runeOfGlyph(name); r != 0 && code >= 0 && code < 256 {
					ft.enc[code] = r
				}
			}
		}
	}
	code := 0
	for i, x := range diffs {
		if i > 4096 {
			break
		}
		switch v := f.Resolve(x).(type) {
		case float64:
			code = fnum(v)
		case Name:
			if code >= 0 && code < 256 {
				if r := runeOfGlyph(string(v)); r != 0 {
					ft.enc[code] = r
				} else {
					ft.enc[code] = 0
				}
			}
			code++
		}
	}
}

// cidWidths are the widths of a CIDFont, shared by every font that names it.
type cidWidths struct {
	w    map[uint32]float64
	runs []widthRun // sorted by first code
	dw   float64
	has  bool
}

// cidWidthsOf reads (once for each descendant font object) the default width and the /W array.
func (f *File) cidWidthsOf(num int, cf Dict) *cidWidths {
	if num != 0 {
		if c, ok := f.cidWidths[num]; ok {
			return c
		}
	}
	c := &cidWidths{dw: 1000, w: map[uint32]float64{}}
	if dw, ok := f.Resolve(cf["DW"]).(float64); ok {
		c.dw = dw
	}
	w, _ := f.Resolve(cf["W"]).(Array)
	for i := 0; i < len(w) && i < 200_000 && f.widthLeft > 0; {
		c1, ok := f.Resolve(w[i]).(float64)
		if !ok {
			i++
			continue
		}
		if i+1 >= len(w) {
			break
		}
		switch v := f.Resolve(w[i+1]).(type) {
		case Array:
			for k, x := range v {
				if f.widthLeft <= 0 || len(c.w) > f.lim.CMapEntries {
					break
				}
				f.widthLeft--
				wv, _ := f.Resolve(x).(float64)
				c.w[uint32(fint64(c1))+uint32(k)] = wv
			}
			i += 2
		case float64:
			if i+2 >= len(w) {
				i = len(w)
				break
			}
			wv, _ := f.Resolve(w[i+2]).(float64)
			if v >= c1 && len(c.runs) < f.lim.CMapEntries {
				f.widthLeft--
				c.runs = append(c.runs, widthRun{uint32(fint64(c1)), uint32(fint64(v)), wv})
			}
			i += 3
		default:
			i++
		}
	}
	sort.SliceStable(c.runs, func(i, j int) bool { return c.runs[i].lo < c.runs[j].lo })
	c.has = len(c.w) > 0 || len(c.runs) > 0
	if num != 0 {
		f.cidWidths[num] = c
	}
	return c
}

func (c *cidWidths) lookup(cid uint32) float64 {
	if w, ok := c.w[cid]; ok {
		return w
	}
	i := sort.Search(len(c.runs), func(i int) bool { return c.runs[i].lo > cid })
	for k := i - 1; k >= 0 && k >= i-8; k-- {
		if cid <= c.runs[k].hi {
			return c.runs[k].w
		}
	}
	return c.dw
}

// cmapOf reads a character map stream once for every object that names it, and
// every map shares one budget of entries for the file.
func (f *File) cmapOf(raw any, s *Stream) *cmap {
	ref, isRef := raw.(Ref)
	if isRef {
		if c, ok := f.cmaps[ref.Num]; ok {
			return c
		}
	}
	var c *cmap
	if f.cmapLeft <= 0 {
		c = &cmap{uni: map[uint32]string{}, truncated: true}
	} else if data, err := f.Decode(s); err == nil {
		c = parseCMap(data, f.lim.CMapEntries, f.lim.Depth, int64(f.cmapLeft), f.expired)
		f.cmapLeft -= int(min(c.cost, int64(f.cmapLeft))) + 1
	} else {
		c = &cmap{uni: map[uint32]string{}}
	}
	if isRef {
		f.cmaps[ref.Num] = c
	}
	return c
}

// glyph is one decoded character of a shown string.
type glyph struct {
	text   string  // what it means; empty when unmapped
	width  float64 // thousandths of an em
	single bool    // a one-byte code 32: word spacing applies
	n      int     // bytes it took
}

func (ft *Font) width(code uint32) float64 {
	if ft.composite {
		cid := code
		if ft.enc2 != nil && len(ft.enc2.cids) > 0 {
			cid = ft.enc2.cid(code)
		}
		if ft.cid == nil {
			return 1000
		}
		return ft.cid.lookup(cid)
	}
	i := int(code) - ft.first
	if i >= 0 && i < len(ft.widths) {
		return ft.widths[i] * ft.wscale
	}
	if ft.hasW {
		return ft.missing * ft.wscale
	}
	// A font with no widths (one of the standard fourteen, by name): an average
	// face; Courier is fixed.
	if ft.Mono {
		return 600
	}
	return 500
}

func (ft *Font) unicode(code uint32) string {
	if ft.toUni != nil {
		if s, ok := ft.toUni.uni[code]; ok {
			return s
		}
	}
	if ft.composite {
		if ft.ucs2 && code != 0 {
			return string(rune(code))
		}
		return ""
	}
	if ft.enc != nil && code < 256 {
		if r := ft.enc[code]; r != 0 {
			return runeString(r)
		}
	}
	if code == 32 {
		return " " // a space no encoding names is still a space
	}
	return ""
}

// decode cuts a shown string into glyphs.
func (ft *Font) decode(s []byte, out []glyph) []glyph {
	out, _ = ft.decodeN(s, out, 1<<30)
	return out
}

// decodeN cuts at most max glyphs off the front of s and returns the rest.
func (ft *Font) decodeN(s []byte, out []glyph, max int) ([]glyph, []byte) {
	for n := 0; len(s) > 0 && n < max; n++ {
		var code uint32
		n := 1
		if ft.composite {
			code, n = ft.enc2.next(s, ft.codeLen)
			if n == 0 {
				break
			}
		} else {
			code = uint32(s[0])
		}
		out = append(out, glyph{text: ft.unicode(code), width: ft.width(code), single: n == 1 && code == 32, n: n})
		s = s[n:]
	}
	return out, s
}

// type1Encoding reads the encoding of an embedded Type 1 font program from the
// part that is not encrypted: "dup 12 /fi put" lines after /Encoding. A font
// that says StandardEncoding has none to give.
func (f *File) type1Encoding(ff *Stream) map[int]string {
	data, err := f.Decode(ff)
	if err != nil {
		return nil
	}
	clear := data
	if i := bytes.Index(clear, []byte("eexec")); i >= 0 {
		clear = clear[:i]
	}
	if len(clear) > 128<<10 {
		clear = clear[:128<<10]
	}
	i := bytes.Index(clear, []byte("/Encoding"))
	if i < 0 {
		return nil
	}
	lx := newLexer(clear, f.lim.Depth)
	lx.pos = i + len("/Encoding")
	out := map[int]string{}
	var st []any
	for n := 0; n < 20000; n++ {
		v, err := lx.next(false)
		if err != nil {
			break
		}
		op, isOp := v.(Op)
		if !isOp {
			st = append(st, v)
			if len(st) > 8 {
				st = st[len(st)-4:]
			}
			continue
		}
		switch op {
		case "StandardEncoding":
			return nil
		case "put":
			if len(st) >= 2 {
				if c, ok := st[len(st)-2].(float64); ok {
					if name, ok := st[len(st)-1].(Name); ok && c >= 0 && c < 256 {
						out[int(c)] = string(name)
					}
				}
			}
		case "def", "readonly", "currentdict":
			if len(out) > 0 {
				return out
			}
		}
		st = st[:0]
	}
	return out
}

package pdfread

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"
)

// xentry says where an object is: at an offset in the file, or at an index in
// an object stream.
type xentry struct {
	off      int64 // type 1: offset; type 2: the object stream's number
	idx      int   // type 2: index inside it
	inStream bool
	free     bool
}

// File is an opened PDF: the bytes, the table of where objects are, and the
// objects read so far.
type File struct {
	data    []byte
	lim     Limits
	xref    map[int]xentry
	trailer Dict
	cache   map[int]any
	objstm  map[int]map[int]any // object stream number -> objects by number
	loading map[int]bool
	rebuilt bool
	version string

	budget   int64 // inflated bytes left for the whole file
	Damaged  int   // streams that broke part-way (read as far as they went)
	Skipped  int   // streams a filter could not read
	ctx      context.Context
	objCount int

	fontCache   map[int]*Font
	scanLeft    int64 // bytes the file may still be searched for the end of a stream
	parseLeft   int64 // bytes of objects that may still be parsed
	nodesLeft   int   // nodes that may still be visited in name trees, destinations and the outline
	cmapLeft    int   // entries that may still be read into character maps
	widthLeft   int   // glyph widths that may still be read
	piecesLeft  int   // pieces of text that may still be kept
	contentLeft int64 // bytes of content that may still be interpreted
	lenDepth    int   // /Length resolved through an object that is itself read: nesting
	sawEncrypt  bool  // a trailer or cross-reference dictionary named /Encrypt
	cmaps       map[int]*cmap
	cidWidths   map[int]*cidWidths
	fontsRead   int
	objstmTotal int
	parseOver   bool // an object was refused because the parse budget was spent
	objDepth    int
	workLeft    int // elements of arrays visited in trees
	rebuildOver bool
	deadline    time.Time // when reading must stop (zero: no limit)
	tick        int
}

// Version is the header's version ("1.7"), when there is one.
func (f *File) Version() string { return f.version }

// Rebuilt says the cross-reference table was rebuilt by scanning the file.
func (f *File) Rebuilt() bool { return f.rebuilt }

// expired asks the clock every time: for places where one step can cost a whole stream.
func (f *File) expired() bool {
	if f.ctx != nil && f.ctx.Err() != nil {
		return true
	}
	return !f.deadline.IsZero() && time.Now().After(f.deadline)
}

// spendNode pays for visiting one node of a name tree, a destination tree or the
// outline; false when the budget is gone or the read is over.
func (f *File) spendNode() bool {
	if f.nodesLeft--; f.nodesLeft < 0 {
		return false
	}
	return !f.cancelled()
}

// spendWork pays for visiting n elements of arrays in a tree.
func (f *File) spendWork(n int) bool {
	f.workLeft -= n
	return f.workLeft >= 0 && !f.cancelled()
}

// fintLimit is how far fint64 lets a number go from zero.
const fintLimit = 1 << 40

// fint64 turns a number from a file into an int64 within +-2^40, without overflow, NaN or Inf (which convert to
// different things on different processors). It is the same on every platform.
func fint64(v float64) int64 { return clampTo(f64i(v), -fintLimit, fintLimit) }

// fint is fint64 as an int: the same value wherever an int holds 2^40, and the ends of int where it does not (a
// 32-bit int), so that no conversion wraps. Use fint64 where the value is going to be cut to 32 bits anyway.
func fint(v float64) int { return int(clampTo(fint64(v), math.MinInt, math.MaxInt)) }

func (f *File) cancelled() bool {
	if f.ctx != nil && f.ctx.Err() != nil {
		return true
	}
	if !f.deadline.IsZero() {
		if f.tick++; f.tick&63 == 0 && time.Now().After(f.deadline) {
			return true
		}
	}
	return false
}

// openFile reads the structure of a PDF.
func openFile(ctx context.Context, data []byte, lim Limits) (*File, error) {
	if int64(len(data)) > int64(lim.FileBytes) {
		return nil, ErrTooLarge
	}
	head := data[:min(len(data), 1024)]
	i := bytes.Index(head, []byte("%PDF-"))
	if i < 0 {
		return nil, ErrNotPDF
	}
	f := &File{data: data, lim: lim, xref: map[int]xentry{}, cache: map[int]any{}, objstm: map[int]map[int]any{}, loading: map[int]bool{}, fontCache: map[int]*Font{}, budget: lim.InflateBytes, ctx: ctx, scanLeft: 8*int64(len(data)) + 1<<20, parseLeft: lim.ParseMem, workLeft: lim.WorkBudget, nodesLeft: lim.NodeBudget, cmapLeft: lim.CMapTotal, widthLeft: lim.WidthCells, piecesLeft: lim.Pieces, contentLeft: lim.ContentBytes, cmaps: map[int]*cmap{}, cidWidths: map[int]*cidWidths{}}
	if lim.Time > 0 {
		f.deadline = time.Now().Add(lim.Time)
	}
	if v := data[i+5:]; len(v) >= 3 && v[0] >= '1' && v[0] <= '2' && v[1] == '.' {
		f.version = string(v[:3])
	}
	ok := f.readXrefChain()
	if ok {
		ok = f.rootOK()
	}
	if !ok {
		f.rebuild()
		if f.rebuildOver {
			return nil, ErrDamaged
		}
		if !f.rootOK() {
			if f.encrypted() {
				return nil, ErrEncrypted
			}
			return nil, ErrDamaged
		}
	}
	if f.encrypted() {
		return nil, ErrEncrypted
	}
	return f, nil
}

func (f *File) encrypted() bool {
	if f.sawEncrypt {
		return true
	}
	if f.trailer == nil {
		return false
	}
	_, ok := f.trailer["Encrypt"]
	return ok
}

func (f *File) rootOK() bool {
	if f.trailer == nil {
		return false
	}
	root, _ := f.Resolve(f.trailer["Root"]).(Dict)
	if root == nil {
		return false
	}
	pages, _ := f.Resolve(root["Pages"]).(Dict)
	return pages != nil
}

// ---- cross-reference tables ---------------------------------------------------------------

func (f *File) readXrefChain() bool {
	tail := f.data[max(0, len(f.data)-2048):]
	i := bytes.LastIndex(tail, []byte("startxref"))
	if i < 0 {
		return false
	}
	lx := newLexer(tail[i+9:], f.lim.Depth)
	v, err := lx.next(false)
	n, ok := v.(float64)
	if err != nil || !ok || n < 0 || f64i(n) >= int64(len(f.data)) {
		return false
	}
	seen := map[int64]bool{}
	off := f64i(n)
	first := true
	for steps := 0; steps < 64; steps++ {
		if seen[off] || off < 0 || off >= int64(len(f.data)) {
			return !first && len(f.xref) > 0
		}
		seen[off] = true
		tr, ok := f.readXrefSection(off)
		if !ok {
			return false
		}
		if first {
			f.trailer = tr
			first = false
		} else {
			for k, v := range tr { // older trailers fill what newer ones lack
				if _, has := f.trailer[k]; !has {
					f.trailer[k] = v
				}
			}
		}
		if xs, ok := tr["XRefStm"].(float64); ok {
			// f64i, not int64(xs): a number past 2^63 must saturate (and then be refused as outside the file), where
			// amd64's own conversion gives MinInt64, which is not outside the file by a test of "< len".
			if o := f64i(xs); xs >= 0 && o < int64(len(f.data)) && !seen[o] {
				seen[o] = true
				if _, ok := f.readXrefSection(o); !ok {
					return false
				}
			}
		}
		prev, ok := tr["Prev"].(float64)
		if !ok {
			return len(f.xref) > 0
		}
		off = f64i(prev)
	}
	return len(f.xref) > 0
}

func (f *File) put(num int, e xentry) {
	if _, has := f.xref[num]; !has {
		if len(f.xref) >= f.lim.Objects {
			return
		}
		f.xref[num] = e
	}
}

func (f *File) readXrefSection(off int64) (Dict, bool) {
	if off < 0 || off >= int64(len(f.data)) {
		return nil, false
	}
	lx := newLexer(f.data, f.lim.Depth)
	lx.pos = int(off)
	lx.skip()
	if bytes.HasPrefix(f.data[lx.pos:], []byte("xref")) {
		lx.pos += 4
		for {
			lx.skip()
			if bytes.HasPrefix(f.data[lx.pos:], []byte("trailer")) {
				lx.pos += 7
				v, err := lx.next(true)
				d, _ := v.(Dict)
				if err != nil && d == nil {
					return nil, false
				}
				return d, d != nil
			}
			a, err1 := lx.next(false)
			b, err2 := lx.next(false)
			start, ok1 := a.(float64)
			count, ok2 := b.(float64)
			if err1 != nil || err2 != nil || !ok1 || !ok2 || count < 0 || count > float64(f.lim.Objects) || start < 0 {
				return nil, false
			}
			for k := 0; k < int(count); k++ {
				o, e1 := lx.next(false)
				g, e2 := lx.next(false)
				t, e3 := lx.next(false)
				of, ok1 := o.(float64)
				_, ok2 := g.(float64)
				tt, ok3 := t.(Op)
				if e1 != nil || e2 != nil || e3 != nil || !ok1 || !ok2 || !ok3 {
					return nil, false
				}
				num := fnum(start) + k
				switch tt {
				case "n":
					f.put(num, xentry{off: f64i(of)})
				case "f":
					f.put(num, xentry{free: true})
				default:
					return nil, false
				}
			}
		}
	}
	// A cross-reference stream: "n g obj << /Type /XRef ... >> stream".
	obj, st, _, ok := f.readIndirectAt(off)
	s, isStream := st.(*Stream)
	_ = obj
	if !ok || !isStream {
		return nil, false
	}
	if t, _ := s.Dict["Type"].(Name); t != "XRef" {
		return nil, false
	}
	data, err := f.Decode(s)
	if err != nil {
		return nil, false
	}
	w, _ := s.Dict["W"].(Array)
	if len(w) != 3 {
		return nil, false
	}
	var wd [3]int
	for i := range wd {
		v, _ := w[i].(float64)
		if v < 0 || v > 8 {
			return nil, false
		}
		wd[i] = int(v)
	}
	size, _ := s.Dict["Size"].(float64)
	index, _ := s.Dict["Index"].(Array)
	if len(index) == 0 {
		index = Array{0.0, size}
	}
	row := wd[0] + wd[1] + wd[2]
	if row == 0 {
		return nil, false
	}
	pos := 0
	field := func(n int, def int64) int64 {
		if n == 0 {
			return def
		}
		var v int64
		for i := 0; i < n; i++ {
			v = v<<8 | int64(data[pos])
			pos++
		}
		return v
	}
	for i := 0; i+1 < len(index); i += 2 {
		st, _ := index[i].(float64)
		ct, _ := index[i+1].(float64)
		for k := 0; k < fnum(ct); k++ {
			if pos+row > len(data) || len(f.xref) >= f.lim.Objects {
				break
			}
			t := field(wd[0], 1)
			a := field(wd[1], 0)
			b := field(wd[2], 0)
			num := fnum(st) + k
			switch t {
			case 0:
				f.put(num, xentry{free: true})
			case 1:
				f.put(num, xentry{off: a})
			case 2:
				f.put(num, xentry{off: a, idx: int(b), inStream: true})
			}
		}
	}
	tr := Dict{}
	for k, v := range s.Dict {
		tr[k] = v
	}
	return tr, true
}

// readIndirectAt reads "n g obj <object> [stream ... endstream] endobj" at off.
func (f *File) readIndirectAt(off int64) (num int, obj any, gen int, ok bool) {
	if off < 0 || off >= int64(len(f.data)) {
		return 0, nil, 0, false
	}
	if f.parseLeft <= 0 {
		return 0, nil, 0, false
	}
	lx := newLexer(f.data, f.lim.Depth)
	lx.pos = int(off)
	a, e1 := lx.next(false)
	b, e2 := lx.next(false)
	k, e3 := lx.next(false)
	an, ok1 := a.(float64)
	bn, ok2 := b.(float64)
	kw, ok3 := k.(Op)
	if e1 != nil || e2 != nil || e3 != nil || !ok1 || !ok2 || !ok3 || kw != "obj" {
		return 0, nil, 0, false
	}
	lx.maxMem = f.parseLeft // the value is charged as it is parsed, and the parse stops where the budget does
	v, err := lx.next(true)
	f.parseLeft -= lx.mem
	if err == errTooBig {
		f.parseOver = true
	}
	if err != nil {
		if d, isD := v.(Dict); !isD || d == nil {
			return 0, nil, 0, false
		}
	}
	if op, isOp := v.(Op); isOp {
		if op == "endobj" {
			return fnum(an), nil, fnum(bn), true
		}
		return 0, nil, 0, false
	}
	if d, isD := v.(Dict); isD {
		lx.skip()
		if bytes.HasPrefix(f.data[lx.pos:], []byte("stream")) {
			s := f.readStream(lx, d)
			return fnum(an), s, fnum(bn), s != nil
		}
	}
	return fnum(an), v, fnum(bn), true
}

// readStream reads the data after the "stream" keyword. /Length is believed
// only when "endstream" follows it; otherwise the data runs to the next
// "endstream" keyword.
func (f *File) readStream(lx *lexer, d Dict) *Stream {
	p := lx.pos + 6
	if p < len(f.data) && f.data[p] == '\r' {
		p++
	}
	if p < len(f.data) && f.data[p] == '\n' {
		p++
	}
	n := -1
	switch v := d["Length"].(type) {
	case float64:
		n = fint(v)
	case Ref:
		// A length held in another object is read through at most three levels, so a chain of streams
		// whose lengths are the next stream cannot recurse once per link.
		if !f.loading[v.Num] && f.lenDepth < 3 {
			f.lenDepth++
			lv, ok := f.Resolve(v).(float64)
			f.lenDepth--
			if ok {
				n = fint(lv)
			}
		}
	}
	if n >= 0 && n <= len(f.data)-p {
		rest := f.data[p+n:]
		q := 0
		for q < len(rest) && q < 32 && isWS(rest[q]) {
			q++
		}
		if bytes.HasPrefix(rest[q:], []byte("endstream")) {
			lx.pos = p + n
			return &Stream{Dict: d, raw: f.data[p : p+n]}
		}
	}
	// The end of a stream whose length cannot be trusted is searched for, within a budget for the whole file.
	if f.scanLeft <= 0 {
		return &Stream{Dict: d}
	}
	i := bytes.Index(f.data[p:], []byte("endstream"))
	if i < 0 {
		f.scanLeft -= int64(len(f.data) - p)
		return &Stream{Dict: d, raw: f.data[p:]}
	}
	f.scanLeft -= int64(i)
	end := p + i
	for end > p && (f.data[end-1] == '\n' || f.data[end-1] == '\r') {
		end--
		if f.data[end] == '\n' && end > p && f.data[end-1] == '\r' {
			end--
		}
		break
	}
	lx.pos = p + i
	return &Stream{Dict: d, raw: f.data[p:end]}
}

// ---- rebuilding ----------------------------------------------------------------------------

// rebuild scans the whole file for "n g obj" and for trailers, for a file whose
// table is missing or wrong. Later definitions of an object win, as in an
// incrementally updated file. Objects inside object streams are added after.
func (f *File) rebuild() {
	f.rebuilt = true
	f.xref = map[int]xentry{}
	f.cache = map[int]any{}
	f.objstm = map[int]map[int]any{}
	f.trailer = nil
	b := f.data
	var streams []int
	var lastTrailer Dict
	var lastXRef []byte
	nh := 0
	n := 0
	for i := 0; i < len(b); {
		if f.cancelled() {
			return
		}
		// find the next "obj" keyword preceded by "n g "
		j := bytes.Index(b[i:], []byte("obj"))
		if j < 0 {
			break
		}
		j += i
		i = j + 3
		if j > 0 && (b[j-1] == 'd') { // "endobj"
			continue
		}
		if i < len(b) && isReg(b[i]) {
			continue
		}
		// walk back over "g" and "n"
		p := j
		for p > 0 && isWS(b[p-1]) {
			p--
		}
		ge := p
		for p > 0 && b[p-1] >= '0' && b[p-1] <= '9' {
			p--
		}
		gs := p
		if gs == ge {
			continue
		}
		for p > 0 && isWS(b[p-1]) {
			p--
		}
		ne := p
		for p > 0 && b[p-1] >= '0' && b[p-1] <= '9' {
			p--
		}
		ns := p
		if ns == ne || ne-ns > 9 {
			continue
		}
		if ns > 0 && isReg(b[ns-1]) {
			continue
		}
		num, _ := strconv.Atoi(string(b[ns:ne]))
		if n++; n > f.lim.RebuildObjects {
			break
		}
		f.xref[num] = xentry{off: int64(ns)}
		// peek at the dictionary: an object stream or an xref stream, to read after the scan. It is read from a
		// window, so each object costs what its dictionary does; a name written with #xx escapes is decoded.
		k := i
		for k < len(b) && isWS(b[k]) {
			k++
		}
		head := b[k:min(len(b), k+8192)]
		if i := bytes.Index(head, []byte("stream")); i >= 0 {
			head = head[:i]
		}
		if !bytes.HasPrefix(head, []byte("<<")) {
			continue
		}
		hasXRef, hasObjStm := bytes.Contains(head, []byte("XRef")), bytes.Contains(head, []byte("ObjStm"))
		switch {
		case hasObjStm:
			streams = append(streams, num)
		case hasXRef && bytes.IndexByte(head, '#') < 0:
			// a cross-reference stream: it may name /Encrypt, and the last one that names /Root is the trailer
			if bytes.Contains(head, []byte("Encrypt")) {
				f.sawEncrypt = true
			}
			if bytes.Contains(head, []byte("Root")) {
				lastXRef = head
			}
		case bytes.IndexByte(head, '#') >= 0:
			// a name written with an escape could be /Type /XRef or /Encrypt: the dictionary is read
			if nh++; nh > 50_000 {
				f.rebuildOver = true
				return
			}
			if v, _ := newLexer(head, f.lim.Depth).next(true); v != nil {
				if d, ok := v.(Dict); ok {
					switch t, _ := d["Type"].(Name); t {
					case "ObjStm":
						streams = append(streams, num)
					case "XRef":
						if _, enc := d["Encrypt"]; enc {
							f.sawEncrypt = true
						}
						if d["Root"] != nil {
							lastTrailer = d
						}
					}
				}
			}
		}
	}
	if lastXRef != nil {
		if v, _ := newLexer(lastXRef, f.lim.Depth).next(true); v != nil {
			if d, ok := v.(Dict); ok && d["Root"] != nil {
				lastTrailer = d
			}
		}
	}
	// trailer dictionaries: the last one with a Root wins. One pass: each is read from a window and the scan
	// goes on after what was read; at most 4,096 are looked at, and a file with more is refused.
	for i, count := 0, 0; i < len(b); {
		if f.cancelled() {
			return
		}
		j := bytes.Index(b[i:], []byte("trailer"))
		if j < 0 {
			break
		}
		if count++; count > 4096 {
			f.rebuildOver = true
			break
		}
		i += j + 7
		lx := newLexer(b[i:min(len(b), i+64<<10)], f.lim.Depth)
		lx.maxMem = f.parseLeft
		v, _ := lx.next(true)
		f.parseLeft -= lx.mem
		if d, ok := v.(Dict); ok {
			if _, enc := d["Encrypt"]; enc {
				f.sawEncrypt = true
			}
			if d["Root"] != nil {
				lastTrailer = d
			}
		}
		i += lx.pos
	}
	f.trailer = lastTrailer
	for _, sn := range streams {
		f.loadObjStm(sn)
		for onum := range f.objstm[sn] {
			if _, direct := f.xref[onum]; !direct {
				f.xref[onum] = xentry{off: int64(sn), inStream: true}
			}
		}
	}
	if f.trailer == nil || f.trailer["Root"] == nil {
		// no trailer at all: find a catalog deterministically (highest object number first, matching incremental updates)
		nums := make([]int, 0, len(f.xref))
		for num := range f.xref {
			nums = append(nums, num)
		}
		slices.Sort(nums)
		for i := len(nums) - 1; i >= 0; i-- {
			num := nums[i]
			if d, ok := f.Resolve(Ref{Num: num}).(Dict); ok {
				if t, _ := d["Type"].(Name); t == "Catalog" {
					if f.trailer == nil {
						f.trailer = Dict{}
					}
					f.trailer["Root"] = Ref{Num: num}
					break
				}
			}
		}
	}
}

// ---- objects ---------------------------------------------------------------------------

// Resolve follows references until it reaches a value. A loop, or a depth past
// the limit, is nil.
func (f *File) Resolve(o any) any {
	for depth := 0; depth < f.lim.Depth; depth++ {
		r, ok := o.(Ref)
		if !ok {
			return o
		}
		o = f.Object(r.Num)
	}
	return nil
}

// Object returns object num, or nil.
func (f *File) Object(num int) any {
	if v, ok := f.cache[num]; ok {
		return v
	}
	if f.loading[num] || f.cancelled() || f.objDepth >= 12 {
		return nil // a cycle, the end of the read, or too deep (a chain of objects that each need the next to be read)
	}
	e, ok := f.xref[num]
	if !ok || e.free {
		return nil
	}
	f.loading[num] = true
	f.objDepth++
	defer func() { delete(f.loading, num); f.objDepth-- }()
	var v any
	if e.inStream {
		// The number of the object stream is an int64 from the table; one that is not an int here names no stream
		// (and must not wrap to one that is).
		if sn, ok := toInt(e.off); ok {
			f.loadObjStm(sn)
			v = f.objstm[sn][num]
		}
	} else {
		n, obj, _, ok := f.readIndirectAt(e.off)
		if ok && n == num {
			v = obj
		}
	}
	if f.objCount++; f.objCount > f.lim.Objects*4 {
		return nil
	}
	f.cache[num] = v
	return v
}

func (f *File) loadObjStm(sn int) {
	if _, done := f.objstm[sn]; done {
		return
	}
	f.objstm[sn] = nil
	e, ok := f.xref[sn]
	if !ok || e.inStream || e.free {
		return
	}
	_, st, _, ok := f.readIndirectAt(e.off)
	s, isS := st.(*Stream)
	if !ok || !isS {
		return
	}
	data, err := f.Decode(s)
	if err != nil {
		return
	}
	nf, _ := s.Dict["N"].(float64)
	firstF, _ := s.Dict["First"].(float64)
	first := fint(firstF)
	if nf < 0 || nf > float64(f.lim.Objects) || first < 0 || first > len(data) {
		return
	}
	lx := newLexer(data, f.lim.Depth)
	type pair struct{ num, off int }
	var ps []pair
	for i := 0; i < int(nf); i++ {
		a, e1 := lx.next(false)
		b, e2 := lx.next(false)
		an, ok1 := a.(float64)
		bn, ok2 := b.(float64)
		if e1 != nil || e2 != nil || !ok1 || !ok2 {
			break
		}
		ps = append(ps, pair{fint(an), fint(bn)})
	}
	// Objects are parsed once for each distinct offset and shared by the numbers that name it, within a budget
	// of bytes for the whole file: a table of a million pairs at one offset costs one object.
	out := make(map[int]any, min(len(ps), 4096))
	byOff := map[int]any{}
	for i, p := range ps {
		if i&255 == 0 && f.cancelled() {
			break
		}
		if f.objstmTotal++; f.objstmTotal > f.lim.Objects {
			break
		}
		at, inside := objStmAt(first, p.off, len(data))
		if !inside {
			continue
		}
		if v, seen := byOff[at]; seen {
			if v != nil {
				out[p.num] = v
			}
			continue
		}
		if f.parseLeft <= 0 {
			break
		}
		l2 := newLexer(data, f.lim.Depth)
		l2.pos = at
		l2.maxMem = f.parseLeft
		v, err := l2.next(true)
		f.parseLeft -= l2.mem
		if err == errTooBig {
			f.parseOver = true
		}
		if _, isOp := v.(Op); isOp || err != nil && !isDict(v) {
			byOff[at] = nil
			continue
		}
		byOff[at] = v
		out[p.num] = v
	}
	f.objstm[sn] = out
}

// Decode applies a stream's filters. The whole file shares one budget of
// inflated bytes.
func (f *File) Decode(s *Stream) ([]byte, error) {
	data := s.raw
	filters, parms := f.filterList(s.Dict)
	if len(filters) == 0 {
		// Stored data costs too: a stream drawn or read many times is not free.
		if f.budget -= int64(len(data)); f.budget < 0 {
			return nil, errors.New("the file holds more stream data than the import limit")
		}
	}
	if len(filters) > 6 {
		return nil, errors.New("too many filters")
	}
	for i, name := range filters {
		var dp Dict
		if i < len(parms) {
			dp, _ = f.Resolve(parms[i]).(Dict)
		}
		var err error
		switch name {
		case "FlateDecode", "Fl":
			var damaged bool
			data, damaged, err = flate(data, min(f.lim.StreamBytes, f.budget), f.lim.Ratio, f.lim.RatioFloor)
			if damaged {
				f.Damaged++
			}
			if err == nil {
				data = f.applyPredictor(data, dp)
			}
		case "LZWDecode", "LZW":
			early := 1
			if dp != nil {
				if v, ok := f.Resolve(dp["EarlyChange"]).(float64); ok {
					early = fnum(v)
				}
			}
			data, err = lzw(data, early, min(f.lim.StreamBytes, f.budget))
			if err == nil {
				data = f.applyPredictor(data, dp)
			}
		case "ASCIIHexDecode", "AHx":
			data = asciiHex(data)
		case "ASCII85Decode", "A85":
			data, err = ascii85(data, min(f.lim.StreamBytes, f.budget))
		case "RunLengthDecode", "RL":
			data, err = runLength(data, min(f.lim.StreamBytes, f.budget))
		case "Crypt":
			return nil, ErrEncrypted
		default: // DCT, JPX, CCITT, JBIG2: pictures, never decoded
			f.Skipped++
			return nil, fmt.Errorf("filter %s is not read", name)
		}
		if err != nil {
			return nil, err
		}
		if int64(len(data)) > f.lim.StreamBytes {
			return nil, errBomb
		}
		if f.budget -= int64(len(data)); f.budget < 0 {
			return nil, errors.New("the file inflates to more than the import limit")
		}
	}
	return data, nil
}

func (f *File) filterList(d Dict) ([]Name, Array) {
	var names []Name
	switch v := f.Resolve(d["Filter"]).(type) {
	case Name:
		names = []Name{v}
	case Array:
		for _, x := range v {
			if n, ok := f.Resolve(x).(Name); ok {
				names = append(names, n)
			}
		}
	}
	var parms Array
	switch v := f.Resolve(d["DecodeParms"]).(type) {
	case Dict:
		parms = Array{v}
	case Array:
		parms = v
	}
	if parms == nil {
		switch v := f.Resolve(d["DP"]).(type) {
		case Dict:
			parms = Array{v}
		case Array:
			parms = v
		}
	}
	return names, parms
}

func (f *File) applyPredictor(data []byte, dp Dict) []byte {
	if dp == nil {
		return data
	}
	num := func(k string, def int) int {
		if v, ok := f.Resolve(dp[k]).(float64); ok {
			return fnum(v)
		}
		return def
	}
	return predictor(data, num("Predictor", 1), num("Colors", 1), num("BitsPerComponent", 8), num("Columns", 1))
}

func isDict(v any) bool { _, ok := v.(Dict); return ok }

package pdfread

import (
	"bytes"
	"errors"
	"math"
	"strings"
)

// Frag is a run of text on one baseline: what it says and where it sits, in
// the page as it is displayed (origin at the lower left, y up, units of the
// page's own points).
type Frag struct {
	Text      string
	X0, X1, Y float64 // start, end (the pen after the last glyph) and baseline
	Size      float64 // the font size as drawn, in points
	Font      *Font
	Rise      float64 // text rise as drawn (superscripts)
	Invisible bool    // render mode 3: an OCR layer or hidden text
}

// Image is where a picture is drawn: never decoded.
type Image struct{ X, Y, W, H float64 }

// Page is what one page holds.
type Page struct {
	Index         int
	W, H          float64
	Frags         []Frag
	Images        []Image
	Glyphs        int // characters shown (rotated and vertical ones included)
	Unmapped      int // characters whose meaning is unknown (left out)
	Rotated       int // characters set at an angle or upside down (left out)
	Vertical      int // characters in vertical writing mode (left out)
	RTL           int // right-to-left characters
	Invisible     int // characters set invisible
	WidthlessFont bool
	Truncated     bool // an operator or text limit was reached on this page
	FontsDamaged  bool // a font's character map was cut at its limit
	obj           int
}

type gstate struct {
	ctm [6]float64
}

type tstate struct {
	font                 *Font
	size, tc, tw, th, tl float64
	rise                 float64
	mode                 int
	tm, tlm              [6]float64
}

func mul(a, b [6]float64) [6]float64 { // a then b (row vectors)
	return [6]float64{
		a[0]*b[0] + a[1]*b[2], a[0]*b[1] + a[1]*b[3],
		a[2]*b[0] + a[3]*b[2], a[2]*b[1] + a[3]*b[3],
		a[4]*b[0] + a[5]*b[2] + b[4], a[4]*b[1] + a[5]*b[3] + b[5],
	}
}

type pageReader struct {
	f      *File
	page   *Page
	ops    *int // per-document counter
	pageOp int
	gs     []gstate
	ts     tstate
	stack  []int // form objects being read
	cur    *Frag
	curB   strings.Builder // the text of cur
	curSp  bool            // cur ends in a space
	textB  *int
	fonts  map[string]*Font
	err    error
}

var errOps = errors.New("operator limit")

// readPage interprets a page's content streams.
func (f *File) readPage(p *Page, pd Dict, res Dict, base [6]float64, opsTotal *int, textBytes *int) {
	if f.piecesLeft <= 0 {
		p.Truncated = true // the file's text is already as much as is kept: later pages are not read
		return
	}
	pr := &pageReader{f: f, page: p, ops: opsTotal, textB: textBytes, fonts: map[string]*Font{}}
	pr.gs = []gstate{{ctm: base}}
	pr.ts = tstate{th: 1, tm: [6]float64{1, 0, 0, 1, 0, 0}, tlm: [6]float64{1, 0, 0, 1, 0, 0}}
	var streams []*Stream
	switch c := f.Resolve(pd["Contents"]).(type) {
	case *Stream:
		streams = []*Stream{c}
	case Array:
		for _, x := range c {
			if s, ok := f.Resolve(x).(*Stream); ok {
				streams = append(streams, s)
			}
			if len(streams) > 4096 {
				break
			}
		}
	}
	// The streams of one page are one program: an operator may be split across them.
	var all []byte
	for _, s := range streams {
		b, err := f.Decode(s)
		if err != nil {
			p.Truncated = true // a stream that could not be read: the page is not whole
			continue
		}
		all = append(all, b...)
		all = append(all, '\n')
		if int64(len(all)) > f.lim.StreamBytes {
			p.Truncated = true
			break
		}
	}
	if err := pr.run(all, res, 0); err != nil {
		p.Truncated = true
	}
	pr.flush()
}

func (pr *pageReader) flush() {
	if pr.cur != nil {
		pr.cur.Text = pr.curB.String()
		if strings.TrimSpace(pr.cur.Text) != "" {
			// Pieces are counted as they are read: for a page, and for the whole file.
			if len(pr.page.Frags) >= MaxFragsPerPage || pr.f.piecesLeft <= 0 {
				pr.page.Truncated = true
			} else {
				pr.f.piecesLeft--
				pr.page.Frags = append(pr.page.Frags, *pr.cur)
			}
		}
		pr.cur = nil
		pr.curB.Reset()
		pr.curSp = false
	}
}

// maxFragBytes bounds the text of one fragment: a line set as one very long
// string is cut into pieces that the layout joins again.
const maxFragBytes = 2048

func (pr *pageReader) font(res Dict, name string) *Font {
	if ft, ok := pr.fonts[name]; ok {
		return ft
	}
	fd, _ := pr.f.Resolve(res["Font"]).(Dict)
	var ft *Font
	if fd != nil {
		raw := fd[name]
		if ref, ok := raw.(Ref); ok {
			if cached, ok := pr.f.fontCache[ref.Num]; ok {
				ft = cached
			} else if d, ok := pr.f.Resolve(ref).(Dict); ok && pr.f.fontsRead < pr.f.lim.Fonts {
				pr.f.fontsRead++
				ft = pr.f.loadFont(d)
				pr.f.fontCache[ref.Num] = ft
			}
		} else if d, ok := raw.(Dict); ok && pr.f.fontsRead < pr.f.lim.Fonts {
			pr.f.fontsRead++
			ft = pr.f.loadFont(d)
		}
	}
	if ft == nil && fd != nil && fd[name] != nil {
		pr.page.Truncated = true // a font that was not read (over the limit, or not a font): its text is not kept
	}
	pr.fonts[name] = ft
	return ft
}

func (pr *pageReader) run(data []byte, res Dict, depth int) error {
	// What is interpreted is paid for by the byte, so a form drawn many times, or an operand-only stream that
	// has no operator to count, is not free; and the clock is asked every few thousand tokens.
	if pr.f.contentLeft -= int64(len(data)); pr.f.contentLeft < 0 {
		return errOps
	}
	lx := newLexer(data, pr.f.lim.Depth)
	var st []any
	tokens := 0
	for {
		if tokens++; tokens&2047 == 0 && pr.f.expired() {
			return errOps
		}
		v, err := lx.next(false)
		if err != nil {
			if lx.eof() {
				return nil
			}
			if strings.Contains(err.Error(), "end of data") {
				return nil
			}
			// A syntax error inside the content: skip the token and carry on.
			st = st[:0]
			if lx.pos >= len(lx.b) {
				return nil
			}
			continue
		}
		op, isOp := v.(Op)
		if !isOp {
			st = append(st, v)
			if len(st) > 64 {
				st = st[len(st)-32:]
			}
			continue
		}
		*pr.ops++
		if pr.pageOp++; pr.pageOp > pr.f.lim.OpsPerPage || *pr.ops > pr.f.lim.OpsTotal {
			return errOps
		}
		if pr.pageOp&0x3fff == 0 && pr.f.cancelled() {
			return errOps
		}
		if op == "BI" {
			pr.inlineImage(lx)
			st = st[:0]
			continue
		}
		if err := pr.exec(string(op), st, res, depth); err != nil {
			return err
		}
		st = st[:0]
	}
}

func nums(st []any, n int) ([]float64, bool) {
	if len(st) < n {
		return nil, false
	}
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		v, ok := st[len(st)-n+i].(float64)
		if !ok {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

func (pr *pageReader) exec(op string, st []any, res Dict, depth int) error {
	ts := &pr.ts
	g := &pr.gs[len(pr.gs)-1]
	switch op {
	case "q":
		if len(pr.gs) < 256 {
			pr.gs = append(pr.gs, *g)
		}
	case "Q":
		if len(pr.gs) > 1 {
			pr.gs = pr.gs[:len(pr.gs)-1]
		}
	case "cm":
		if v, ok := nums(st, 6); ok {
			g.ctm = mul([6]float64{v[0], v[1], v[2], v[3], v[4], v[5]}, g.ctm)
		}
	case "BT":
		pr.flush()
		ts.tm = [6]float64{1, 0, 0, 1, 0, 0}
		ts.tlm = ts.tm
	case "ET":
		pr.flush()
	case "Tf":
		if len(st) >= 2 {
			if n, ok := st[len(st)-2].(Name); ok {
				ts.font = pr.font(res, string(n))
			}
			if s, ok := st[len(st)-1].(float64); ok {
				ts.size = s
			}
		}
	case "Tc":
		if v, ok := nums(st, 1); ok {
			ts.tc = v[0]
		}
	case "Tw":
		if v, ok := nums(st, 1); ok {
			ts.tw = v[0]
		}
	case "Tz":
		if v, ok := nums(st, 1); ok {
			ts.th = v[0] / 100
		}
	case "TL":
		if v, ok := nums(st, 1); ok {
			ts.tl = v[0]
		}
	case "Ts":
		if v, ok := nums(st, 1); ok {
			ts.rise = v[0]
		}
	case "Tr":
		if v, ok := nums(st, 1); ok {
			ts.mode = fnum(v[0])
		}
	case "Td", "TD":
		if v, ok := nums(st, 2); ok {
			if op == "TD" {
				ts.tl = -v[1]
			}
			ts.tlm = mul([6]float64{1, 0, 0, 1, v[0], v[1]}, ts.tlm)
			ts.tm = ts.tlm
		}
	case "Tm":
		if v, ok := nums(st, 6); ok {
			ts.tm = [6]float64{v[0], v[1], v[2], v[3], v[4], v[5]}
			ts.tlm = ts.tm
		}
	case "T*":
		ts.tlm = mul([6]float64{1, 0, 0, 1, 0, -ts.tl}, ts.tlm)
		ts.tm = ts.tlm
	case "Tj":
		if len(st) >= 1 {
			if s, ok := st[len(st)-1].(String); ok {
				pr.show(s)
			}
		}
	case "'", "\"":
		ts.tlm = mul([6]float64{1, 0, 0, 1, 0, -ts.tl}, ts.tlm)
		ts.tm = ts.tlm
		if op == "\"" && len(st) >= 3 {
			if aw, ok := st[len(st)-3].(float64); ok {
				ts.tw = aw
			}
			if ac, ok := st[len(st)-2].(float64); ok {
				ts.tc = ac
			}
		}
		if len(st) >= 1 {
			if s, ok := st[len(st)-1].(String); ok {
				pr.show(s)
			}
		}
	case "TJ":
		if len(st) >= 1 {
			if arr, ok := st[len(st)-1].(Array); ok {
				for _, e := range arr {
					switch x := e.(type) {
					case String:
						pr.show(x)
					case float64:
						// thousandths of the text size; positive moves left
						ts.tm = mul([6]float64{1, 0, 0, 1, -x / 1000 * ts.size * ts.th, 0}, ts.tm)
					}
				}
			}
		}
	case "Do":
		if len(st) >= 1 {
			if n, ok := st[len(st)-1].(Name); ok {
				return pr.xobject(res, string(n), depth)
			}
		}
	}
	return nil
}

func (pr *pageReader) xobject(res Dict, name string, depth int) error {
	xd, _ := pr.f.Resolve(res["XObject"]).(Dict)
	if xd == nil {
		return nil
	}
	raw := xd[name]
	s, ok := pr.f.Resolve(raw).(*Stream)
	if !ok {
		return nil
	}
	g := &pr.gs[len(pr.gs)-1]
	sub, _ := pr.f.Resolve(s.Dict["Subtype"]).(Name)
	switch sub {
	case "Image":
		c := g.ctm
		pr.page.Images = append(pr.page.Images, Image{X: c[4], Y: c[5], W: math.Hypot(c[0], c[1]), H: math.Hypot(c[2], c[3])})
		if len(pr.page.Images) > 5000 {
			pr.page.Images = pr.page.Images[:5000]
		}
	case "Form":
		if depth >= pr.f.lim.FormDepth {
			return nil
		}
		num := -1
		if r, ok := raw.(Ref); ok {
			num = r.Num
			for _, o := range pr.stack {
				if o == num {
					return nil // a form that draws itself
				}
			}
		}
		data, err := pr.f.Decode(s)
		if err != nil {
			return nil
		}
		fres, _ := pr.f.Resolve(s.Dict["Resources"]).(Dict)
		if fres == nil {
			fres = res
		}
		saveG, saveT := pr.gs, pr.ts
		pr.gs = append([]gstate(nil), pr.gs[len(pr.gs)-1])
		if m, ok := pr.f.Resolve(s.Dict["Matrix"]).(Array); ok && len(m) == 6 {
			var mm [6]float64
			for i := range mm {
				mm[i], _ = pr.f.Resolve(m[i]).(float64)
			}
			pr.gs[0].ctm = mul(mm, pr.gs[0].ctm)
		}
		pr.stack = append(pr.stack, num)
		pr.flush()
		err = pr.run(data, fres, depth+1)
		pr.flush()
		pr.stack = pr.stack[:len(pr.stack)-1]
		pr.gs, pr.ts = saveG, saveT
		if err == nil && pr.f.expired() {
			err = errOps
		}
		return err
	}
	return nil
}

// inlineImage skips "BI <dict> ID <data> EI".
func (pr *pageReader) inlineImage(lx *lexer) {
	i := bytes.Index(lx.b[lx.pos:], []byte("ID"))
	if i < 0 {
		lx.pos = len(lx.b)
		return
	}
	p := lx.pos + i + 2
	for q := p; q+2 <= len(lx.b); q++ {
		if lx.b[q] == 'E' && lx.b[q+1] == 'I' && (q == 0 || isWS(lx.b[q-1])) && (q+2 == len(lx.b) || isWS(lx.b[q+2])) {
			lx.pos = q + 2
			c := pr.gs[len(pr.gs)-1].ctm
			if len(pr.page.Images) < 5000 {
				pr.page.Images = append(pr.page.Images, Image{X: c[4], Y: c[5], W: math.Hypot(c[0], c[1]), H: math.Hypot(c[2], c[3])})
			}
			return
		}
	}
	lx.pos = len(lx.b)
}

func isRTL(r rune) bool {
	return r >= 0x0590 && r <= 0x08FF || r >= 0xFB1D && r <= 0xFDFF || r >= 0xFE70 && r <= 0xFEFF
}

// show draws a string: it moves the text matrix and adds the characters to the
// current fragment.
func (pr *pageReader) show(s []byte) {
	ts := &pr.ts
	ft := ts.font
	if ft == nil {
		return
	}
	pg := pr.page
	if ft.truncCMs {
		pg.FontsDamaged = true
	}
	if !ft.hasW && !ft.composite {
		pg.WidthlessFont = true
	}
	var buf [512]glyph
	for len(s) > 0 {
		if pr.f.cancelled() {
			pg.Truncated = true
			return
		}
		var glyphs []glyph
		glyphs, s = ft.decodeN(s, buf[:0], len(buf))
		pr.showGlyphs(ft, glyphs)
	}
}

func (pr *pageReader) showGlyphs(ft *Font, glyphs []glyph) {
	ts := &pr.ts
	pg := pr.page
	if pr.f.piecesLeft <= 0 || len(pg.Frags) >= MaxFragsPerPage {
		pg.Truncated = true // nothing more would be kept: the rest of the text is not decoded
		return
	}
	g := pr.gs[len(pr.gs)-1]
	for _, gl := range glyphs {
		pg.Glyphs++
		// The glyph's origin and axes in the page.
		tr := mul([6]float64{ts.size * ts.th, 0, 0, ts.size, 0, ts.rise}, mul(ts.tm, g.ctm))
		x, y := tr[4], tr[5]
		hx, hy := tr[0], tr[1] // the baseline direction scaled by the size
		size := math.Hypot(tr[2], tr[3])
		adv := (gl.width/1000*ts.size + ts.tc)
		if gl.single {
			adv += ts.tw
		}
		adv *= ts.th
		// advance in the page, along the baseline
		unit := math.Hypot(ts.tm[0]*g.ctm[0]+ts.tm[1]*g.ctm[2], ts.tm[0]*g.ctm[1]+ts.tm[1]*g.ctm[3])
		dx := adv * unit
		ts.tm = mul([6]float64{1, 0, 0, 1, adv, 0}, ts.tm)
		switch {
		case !finite(x, y, size, dx, hx, hy):
			pg.Unmapped++ // coordinates that are not numbers: the glyph is not placed
			continue
		case ft.Vertical:
			pg.Vertical++
			continue
		case hx <= 0 || math.Abs(hy) > 0.2*math.Abs(hx):
			pg.Rotated++
			continue
		case ts.mode == 3:
			pg.Invisible++
		}
		if gl.text == "" {
			if !(gl.single || gl.width == 0) {
				pg.Unmapped++
			}
			// an unmapped glyph still takes room: it ends a word
			pr.advanceOnly(x, dx, y)
			continue
		}
		if *pr.textB += len(gl.text); *pr.textB > pr.f.lim.TextBytes {
			pg.Truncated = true
			continue
		}
		for _, r := range gl.text {
			if isRTL(r) {
				pg.RTL++
			}
		}
		pr.add(gl.text, x, dx, y, size, ft, ts.rise*unit, ts.mode == 3)
	}
}

func (pr *pageReader) advanceOnly(x, dx, y float64) {
	if c := pr.cur; c != nil && math.Abs(c.Y-y) < 0.25*c.Size {
		c.X1 = x + dx
	}
}

func (pr *pageReader) add(text string, x, dx, y, size float64, ft *Font, rise float64, invisible bool) {
	if c := pr.cur; c != nil && c.Font == ft && c.Invisible == invisible && math.Abs(c.Size-size) < 0.06*size && math.Abs(c.Y-y) < 0.25*size && math.Abs(c.Rise-rise) < 0.2*size {
		gap := x - c.X1
		switch {
		case gap > 1.2*size || gap < -0.6*size || pr.curB.Len() >= maxFragBytes:
			pr.flush()
		case gap > 0.2*size && text != " " && !pr.curSp:
			pr.curB.WriteByte(' ')
		}
	} else {
		pr.flush()
	}
	if pr.cur == nil {
		pr.cur = &Frag{X0: x, Y: y, Size: size, Font: ft, Rise: rise, Invisible: invisible}
	}
	pr.curB.WriteString(text)
	pr.curSp = strings.HasSuffix(text, " ")
	pr.cur.X1 = x + dx
}

func finite(v ...float64) bool {
	for _, x := range v {
		if x != x || x > 1e12 || x < -1e12 {
			return false
		}
	}
	return true
}

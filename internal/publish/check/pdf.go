package check

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// PDFLine is one line of text as drawn: the text matrix at its start, the font
// and size, and the characters its codes stand for (through the font's
// ToUnicode map).
type PDFLine struct {
	X, Y, Size float64
	Font       string // the BaseFont, subset tag removed
	Text       string
}

// PDFPage is what the validator found on a page.
type PDFPage struct {
	W, H   float64
	Trim   []float64 // the trim box, nil when absent
	Lines  []PDFLine
	Images int
	Label  string // the page label a viewer shows
}

// Text is the page's text, one line per text line.
func (p PDFPage) Text() string {
	var b strings.Builder
	for i, l := range p.Lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(l.Text)
	}
	return b.String()
}

// PDFDoc is a validated file's structure.
type PDFDoc struct {
	Objects  int
	Pages    []PDFPage
	Title    string
	Author   string
	Lang     string
	ID       string
	Outline  []string // titles, depth-first
	Fonts    int      // embedded fonts
	Images   int
	FontBase []string // BaseFont of each font, with the subset tag
}

// Text is the whole document's text, pages separated by form feeds.
func (d *PDFDoc) Text() string {
	var parts []string
	for _, p := range d.Pages {
		parts = append(parts, p.Text())
	}
	return strings.Join(parts, "\f")
}

// PDF validates a file written by internal/publish/pdfw and returns what is
// wrong with it (nothing, when it is sound). It checks the structure a viewer
// relies on (header, cross-reference table, trailer, object boundaries, stream
// lengths, references, the page tree, content streams, fonts and their
// subsets, ToUnicode maps, images, the outline and page labels) and that
// nothing active is present: no JavaScript, action, annotation, form, embedded
// file or external reference. It is a separate reader from the writer.
func PDF(data []byte) []string {
	_, probs := ParsePDF(data)
	return probs
}

type pdfCheck struct {
	data     []byte
	probs    []string
	objs     map[int]any
	offs     []int
	visited  map[int]bool
	pageObjs map[int]int // page object -> index
	fonts    map[int]*fontInfo
	nobj     int
}

func (c *pdfCheck) problem(format string, a ...any) {
	if len(c.probs) < 200 {
		c.probs = append(c.probs, fmt.Sprintf(format, a...))
	}
}

type fontInfo struct {
	base    string
	toUni   map[int]string
	maxCode int
	widths  map[int]bool
	ok      bool
}

// The only names and keys a file of this program's may use.
var (
	catalogKeys = setOf("Type", "Pages", "Lang", "ViewerPreferences", "PageLayout", "PageLabels", "Outlines", "PageMode")
	pageKeys    = setOf("Type", "Parent", "MediaBox", "CropBox", "TrimBox", "BleedBox", "Resources", "Contents")
	// Names (values) and keys that make a file act or reach outside itself.
	forbiddenNames = setOf("JavaScript", "JS", "Launch", "URI", "SubmitForm", "GoToR", "GoToE", "ImportData", "Rendition", "Movie", "Sound", "RichMedia",
		"3D", "EmbeddedFile", "EmbeddedFiles", "Filespec", "Annot", "Link", "Widget", "FileAttachment", "Action", "Thread")
	forbiddenKeys = setOf("JavaScript", "JS", "Launch", "URI", "OpenAction", "AA", "A", "AcroForm", "XFA", "Encrypt", "Names", "Annots", "AF", "EF", "F", "UF",
		"Collection", "Perms", "Legal", "SpiderInfo", "Requirements", "Metadata", "Threads", "Dests", "URI")
	contentOps = setOf("q", "Q", "cm", "w", "d", "g", "G", "rg", "RG", "re", "f", "S", "m", "l", "h", "n", "W", "BT", "ET", "Tf", "Tm", "Tc", "Ts", "Tr", "TJ", "Do")
)

func setOf(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, v := range s {
		m[v] = true
	}
	return m
}

func dictOf(v any) pdfDict {
	switch t := v.(type) {
	case pdfDict:
		return t
	case pdfStream:
		return t.d
	}
	return nil
}

func numOf(v any) (float64, bool) {
	n, ok := v.(pdfNum)
	return float64(n), ok
}

func (c *pdfCheck) get(v any) any {
	if r, ok := v.(pdfRef); ok {
		if o, ok := c.objs[int(r)]; ok {
			return o
		}
		c.problem("a reference to object %d, which does not exist", int(r))
		return nil
	}
	return v
}

// ParsePDF validates data and returns its structure along with the problems.
func ParsePDF(data []byte) (*PDFDoc, []string) {
	c := &pdfCheck{data: data, objs: map[int]any{}, visited: map[int]bool{}, pageObjs: map[int]int{}, fonts: map[int]*fontInfo{}}
	doc := &PDFDoc{}
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(pdfError); ok {
				c.problem("%s", e.msg)
				return
			}
			c.problem("the reader failed: %v", r)
		}
	}()
	root, info := c.structure(doc)
	if root == nil {
		return doc, c.probs
	}
	c.catalog(doc, root, info)
	return doc, c.probs
}

func (c *pdfCheck) structure(doc *PDFDoc) (root pdfDict, info pdfDict) {
	d := c.data
	if !bytes.HasPrefix(d, []byte("%PDF-1.7\n%")) {
		c.problem("the file does not start with %%PDF-1.7 and a binary comment")
		return nil, nil
	}
	if !bytes.HasSuffix(d, []byte("\n%%EOF\n")) {
		c.problem("the file does not end with %%%%EOF")
		return nil, nil
	}
	i := bytes.LastIndex(d, []byte("startxref\n"))
	if i < 0 {
		c.problem("no startxref")
		return nil, nil
	}
	rest := string(d[i+len("startxref\n"):])
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		c.problem("startxref has no offset")
		return nil, nil
	}
	xrefOff, err := strconv.Atoi(rest[:nl])
	if err != nil || xrefOff <= 0 || xrefOff >= i {
		c.problem("startxref says %q", rest[:nl])
		return nil, nil
	}
	if bytes.Count(d, []byte("\nxref\n")) != 1 || bytes.Count(d, []byte("startxref")) != 1 {
		c.problem("more than one cross-reference section (updates and linearisation are not written)")
	}
	if !bytes.HasPrefix(d[xrefOff:], []byte("xref\n0 ")) {
		c.problem("startxref does not point at the cross-reference table")
		return nil, nil
	}
	lx := &lexer{b: d, pos: xrefOff + len("xref\n0 ")}
	n, err := strconv.Atoi(lx.word())
	if err != nil || n < 2 || n > 20_000_000 {
		c.problem("the cross-reference table has a bad size")
		return nil, nil
	}
	lx.skip()
	tab := lx.pos
	if tab+20*n > len(d) {
		c.problem("the cross-reference table is cut short")
		return nil, nil
	}
	if string(d[tab:tab+20]) != "0000000000 65535 f \n" {
		c.problem("entry 0 of the cross-reference table is not the free list head")
	}
	c.offs = make([]int, n)
	for k := 1; k < n; k++ {
		e := d[tab+20*k : tab+20*k+20]
		if e[10] != ' ' || e[11] != '0' || e[16] != ' ' || e[17] != 'n' || e[18] != ' ' || e[19] != '\n' {
			c.problem("cross-reference entry %d is malformed", k)
			return nil, nil
		}
		off, err := strconv.Atoi(string(e[:10]))
		if err != nil {
			c.problem("cross-reference entry %d has no offset", k)
			return nil, nil
		}
		c.offs[k] = off
	}
	lx.pos = tab + 20*n
	lx.skip()
	if lx.word() != "trailer" {
		c.problem("no trailer after the table")
		return nil, nil
	}
	trailer, ok := lx.value(false, 0).(pdfDict)
	if !ok {
		c.problem("the trailer is not a dictionary")
		return nil, nil
	}
	for k := range trailer {
		if k != "Size" && k != "Root" && k != "Info" && k != "ID" {
			c.problem("the trailer has /%s", k)
		}
	}
	if sz, _ := numOf(trailer["Size"]); int(sz) != n {
		c.problem("the trailer's /Size is %v, the table has %d entries", trailer["Size"], n)
	}
	// ID: two equal 16-byte strings.
	if a, ok := trailer["ID"].(pdfArr); !ok || len(a) != 2 {
		c.problem("the trailer's /ID is not two strings")
	} else {
		s0, ok0 := a[0].(pdfStr)
		s1, ok1 := a[1].(pdfStr)
		if !ok0 || !ok1 || len(s0.b) != 16 || !bytes.Equal(s0.b, s1.b) {
			c.problem("the trailer's /ID is not two equal 16-byte strings")
		} else {
			doc.ID = fmt.Sprintf("%x", s0.b)
		}
	}
	// Objects: each exactly where the table says, back to back.
	c.nobj = n - 1
	doc.Objects = n - 1
	order := make([]int, 0, n-1)
	for k := 1; k < n; k++ {
		order = append(order, k)
	}
	sort.Slice(order, func(a, b int) bool { return c.offs[order[a]] < c.offs[order[b]] })
	// The first object follows the header's two lines.
	prevEnd := 9 + bytes.IndexByte(d[9:], '\n') + 1
	for _, k := range order {
		off := c.offs[k]
		if off != prevEnd {
			c.problem("object %d starts at %d, the previous object ended at %d", k, off, prevEnd)
			if off < prevEnd || off >= xrefOff {
				return nil, nil
			}
		}
		end, obj := c.readObject(k, off)
		if obj == nil {
			return nil, nil
		}
		c.objs[k] = obj
		prevEnd = end
	}
	if prevEnd != xrefOff {
		c.problem("the last object ends at %d, the table starts at %d", prevEnd, xrefOff)
	}
	rootRef, ok := trailer["Root"].(pdfRef)
	if !ok {
		c.problem("the trailer has no /Root")
		return nil, nil
	}
	root = dictOf(c.get(rootRef))
	c.visited[int(rootRef)] = true
	if ir, ok := trailer["Info"].(pdfRef); ok {
		info = dictOf(c.get(ir))
		c.visited[int(ir)] = true
	}
	return root, info
}

// readObject reads object k at off and returns where it ends.
func (c *pdfCheck) readObject(k, off int) (int, any) {
	lx := &lexer{b: c.data, pos: off}
	if lx.word() != strconv.Itoa(k) {
		c.problem("object %d does not start with its number", k)
		return 0, nil
	}
	lx.skip()
	if lx.word() != "0" {
		c.problem("object %d is not generation 0", k)
		return 0, nil
	}
	lx.skip()
	if lx.word() != "obj" {
		c.problem("object %d has no obj", k)
		return 0, nil
	}
	v := lx.value(false, 0)
	lx.skip()
	save := lx.pos
	w := lx.word()
	if w == "stream" {
		d, ok := v.(pdfDict)
		if !ok {
			c.problem("object %d: a stream without a dictionary", k)
			return 0, nil
		}
		// "stream" is followed by a line feed.
		if lx.pos >= len(c.data) || c.data[lx.pos] != '\n' {
			c.problem("object %d: stream is not followed by a line feed", k)
			return 0, nil
		}
		lx.pos++
		ln, ok := numOf(d["Length"])
		if !ok || ln < 0 || float64(int(ln)) != ln {
			c.problem("object %d: /Length is not a direct number", k)
			return 0, nil
		}
		L := int(ln)
		if lx.pos+L > len(c.data) {
			c.problem("object %d: the stream is longer than the file", k)
			return 0, nil
		}
		raw := c.data[lx.pos : lx.pos+L]
		lx.pos += L
		if !bytes.HasPrefix(c.data[lx.pos:], []byte("\nendstream")) {
			c.problem("object %d: /Length %d does not end the stream where endstream is", k, L)
			return 0, nil
		}
		lx.pos += len("\nendstream")
		lx.skip()
		if lx.word() != "endobj" {
			c.problem("object %d: no endobj after the stream", k)
			return 0, nil
		}
		end := lx.pos
		if end < len(c.data) && c.data[end] == '\n' {
			end++
		}
		return end, pdfStream{d: d, data: raw}
	}
	lx.pos = save
	if lx.word() != "endobj" {
		c.problem("object %d: no endobj", k)
		return 0, nil
	}
	end := lx.pos
	if end < len(c.data) && c.data[end] == '\n' {
		end++
	}
	return end, v
}

// walkNames reports a forbidden name or key anywhere in a value.
func (c *pdfCheck) walkNames(obj int, v any) {
	switch t := v.(type) {
	case pdfName:
		if forbiddenNames[string(t)] {
			c.problem("object %d names /%s", obj, string(t))
		}
	case pdfDict:
		for k, x := range t {
			if forbiddenKeys[k] {
				c.problem("object %d has the key /%s", obj, k)
			}
			c.walkNames(obj, x)
		}
	case pdfArr:
		for _, x := range t {
			c.walkNames(obj, x)
		}
	case pdfStream:
		c.walkNames(obj, t.d)
	}
}

func (c *pdfCheck) catalog(doc *PDFDoc, root, info pdfDict) {
	if root == nil || root["Type"] != pdfName("Catalog") {
		c.problem("the root is not a catalog")
		return
	}
	for k := range root {
		if !catalogKeys[k] {
			c.problem("the catalog has /%s", k)
		}
	}
	for k, o := range c.objs {
		c.walkNames(k, o)
	}
	if s, ok := root["Lang"].(pdfStr); ok {
		doc.Lang = string(s.b)
	}
	if vp := dictOf(root["ViewerPreferences"]); vp == nil || len(vp) != 1 {
		c.problem("ViewerPreferences is not just DisplayDocTitle")
	}
	// Info.
	if info != nil {
		for k, v := range info {
			s, isStr := v.(pdfStr)
			switch k {
			case "Title", "Author", "Subject", "Producer", "Creator":
				if !isStr || !s.hex || len(s.b) < 2 || s.b[0] != 0xFE || s.b[1] != 0xFF || len(s.b)%2 != 0 {
					c.problem("Info /%s is not a UTF-16BE hex string with a byte order mark", k)
					continue
				}
				text := decodeUTF16(s.b[2:])
				switch k {
				case "Title":
					doc.Title = text
				case "Author":
					doc.Author = text
				}
			case "CreationDate", "ModDate":
				if !isStr || !strings.HasPrefix(string(s.b), "D:") || len(s.b) != 17 || !strings.HasSuffix(string(s.b), "Z") {
					c.problem("Info /%s is not a date", k)
				}
			default:
				c.problem("Info has /%s", k)
			}
		}
	}
	// Page tree.
	pr, ok := root["Pages"].(pdfRef)
	if !ok {
		c.problem("the catalog has no /Pages")
		return
	}
	var leaves []int
	c.pageTree(int(pr), 0, &leaves, 0)
	for i, l := range leaves {
		c.pageObjs[l] = i
	}
	if len(leaves) == 0 {
		c.problem("the document has no pages")
	}
	doc.Pages = make([]PDFPage, len(leaves))
	for i, l := range leaves {
		c.page(doc, i, l)
	}
	// Labels.
	if pl := dictOf(root["PageLabels"]); pl != nil {
		c.labels(doc, pl)
	}
	// Outline.
	if or, ok := root["Outlines"].(pdfRef); ok {
		c.outline(doc, int(or))
	}
	// Every object is reachable.
	for k := 1; k <= c.nobj; k++ {
		if !c.visited[k] {
			c.problem("object %d is not used by anything", k)
		}
	}
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

func (c *pdfCheck) pageTree(obj int, parent int, leaves *[]int, depth int) int {
	if depth > 8 {
		c.problem("the page tree is too deep")
		return 0
	}
	if c.visited[obj] {
		c.problem("object %d is in the page tree twice", obj)
		return 0
	}
	c.visited[obj] = true
	d := dictOf(c.get(pdfRef(obj)))
	if d == nil {
		c.problem("page tree node %d is not a dictionary", obj)
		return 0
	}
	switch d["Type"] {
	case pdfName("Pages"):
		if depth > 0 {
			if p, _ := d["Parent"].(pdfRef); int(p) != parent {
				c.problem("page tree node %d has the wrong /Parent", obj)
			}
		} else if _, has := d["Parent"]; has {
			c.problem("the root of the page tree has a /Parent")
		}
		kids, _ := d["Kids"].(pdfArr)
		count := 0
		for _, k := range kids {
			r, ok := k.(pdfRef)
			if !ok {
				c.problem("a /Kids entry is not a reference")
				continue
			}
			count += c.pageTree(int(r), obj, leaves, depth+1)
		}
		if n, _ := numOf(d["Count"]); int(n) != count {
			c.problem("page tree node %d says /Count %v, it has %d pages", obj, d["Count"], count)
		}
		for k := range d {
			if k != "Type" && k != "Parent" && k != "Kids" && k != "Count" {
				c.problem("page tree node %d has /%s", obj, k)
			}
		}
		return count
	case pdfName("Page"):
		if p, _ := d["Parent"].(pdfRef); int(p) != parent {
			c.problem("page %d has the wrong /Parent", obj)
		}
		*leaves = append(*leaves, obj)
		return 1
	}
	c.problem("page tree node %d has no /Type /Pages or /Page", obj)
	return 0
}

func boxOf(c *pdfCheck, v any, what string) []float64 {
	a, ok := c.get(v).(pdfArr)
	if !ok || len(a) != 4 {
		c.problem("%s is not four numbers", what)
		return nil
	}
	out := make([]float64, 4)
	for i, x := range a {
		n, ok := numOf(x)
		if !ok {
			c.problem("%s is not four numbers", what)
			return nil
		}
		out[i] = n
	}
	return out
}

func (c *pdfCheck) page(doc *PDFDoc, idx, obj int) {
	d := dictOf(c.get(pdfRef(obj)))
	for k := range d {
		if !pageKeys[k] {
			c.problem("page %d has /%s", idx+1, k)
		}
	}
	pg := &doc.Pages[idx]
	mb := boxOf(c, d["MediaBox"], fmt.Sprintf("page %d MediaBox", idx+1))
	if mb == nil {
		return
	}
	if mb[0] != 0 || mb[1] != 0 || mb[2] <= 0 || mb[3] <= 0 || mb[2] > 14400 || mb[3] > 14400 {
		c.problem("page %d has MediaBox %v", idx+1, mb)
		return
	}
	pg.W, pg.H = mb[2], mb[3]
	if cb := boxOf(c, d["CropBox"], fmt.Sprintf("page %d CropBox", idx+1)); cb != nil && (cb[0] != mb[0] || cb[1] != mb[1] || cb[2] != mb[2] || cb[3] != mb[3]) {
		c.problem("page %d CropBox differs from MediaBox", idx+1)
	}
	for _, name := range []string{"TrimBox", "BleedBox"} {
		if _, has := d[name]; has {
			b := boxOf(c, d[name], fmt.Sprintf("page %d %s", idx+1, name))
			if b != nil && (b[0] < 0 || b[1] < 0 || b[2] > mb[2] || b[3] > mb[3] || b[0] >= b[2] || b[1] >= b[3]) {
				c.problem("page %d %s %v is not inside the MediaBox", idx+1, name, b)
			}
			if name == "TrimBox" {
				pg.Trim = b
			}
		}
	}
	res := dictOf(c.get(d["Resources"]))
	if res == nil {
		c.problem("page %d has no /Resources", idx+1)
		return
	}
	for k := range res {
		if k != "ProcSet" && k != "Font" && k != "XObject" {
			c.problem("page %d resources have /%s", idx+1, k)
		}
	}
	fonts := map[string]*fontInfo{}
	if fd := dictOf(res["Font"]); fd != nil {
		for name, ref := range fd {
			r, ok := ref.(pdfRef)
			if !ok {
				c.problem("page %d font /%s is not a reference", idx+1, name)
				continue
			}
			fonts[name] = c.font(int(r))
		}
	}
	xobjs := map[string]bool{}
	if xd := dictOf(res["XObject"]); xd != nil {
		for name, ref := range xd {
			r, ok := ref.(pdfRef)
			if !ok {
				c.problem("page %d xobject /%s is not a reference", idx+1, name)
				continue
			}
			c.image(int(r))
			xobjs[name] = true
		}
	}
	cr, ok := d["Contents"].(pdfRef)
	if !ok {
		c.problem("page %d has no /Contents reference", idx+1)
		return
	}
	c.visited[int(cr)] = true
	st, ok := c.get(cr).(pdfStream)
	if !ok {
		c.problem("page %d /Contents is not a stream", idx+1)
		return
	}
	c.content(doc, idx, st, fonts, xobjs)
}

func (c *pdfCheck) streamData(what string, st pdfStream) []byte {
	f, has := st.d["Filter"]
	if !has {
		return st.data
	}
	if f != pdfName("FlateDecode") {
		c.problem("%s uses a filter other than FlateDecode", what)
		return nil
	}
	out, err := inflate(st.data, maxInflate)
	if err != nil {
		c.problem("%s does not inflate: %v", what, err)
		return nil
	}
	return out
}

func (c *pdfCheck) content(doc *PDFDoc, idx int, st pdfStream, fonts map[string]*fontInfo, xobjs map[string]bool) {
	what := fmt.Sprintf("page %d content", idx+1)
	data := c.streamData(what, st)
	if data == nil {
		return
	}
	pg := &doc.Pages[idx]
	lx := &lexer{b: data}
	var stack []any
	qdepth := 0
	inText := false
	var cur *fontInfo
	var curBase string
	var size float64
	lineOpen := false
	nargs := map[string]int{"re": 4, "m": 2, "l": 2, "cm": 6, "Tf": 2, "Tm": 6, "TJ": 1, "g": 1, "G": 1, "rg": 3, "RG": 3, "w": 1, "Tc": 1, "Ts": 1, "Tr": 1, "d": 2, "Do": 1,
		"q": 0, "Q": 0, "f": 0, "S": 0, "h": 0, "n": 0, "W": 0, "BT": 0, "ET": 0}
	ops := 0
	for !lx.atEnd() {
		v := lx.value(true, 0)
		op, isOp := v.(pdfOp)
		if !isOp {
			stack = append(stack, v)
			if len(stack) > 100_000 {
				c.problem("%s: an operand list that long", what)
				return
			}
			continue
		}
		ops++
		name := string(op)
		if !contentOps[name] {
			c.problem("%s: the operator %q is not one this program writes", what, name)
			return
		}
		if len(stack) != nargs[name] {
			c.problem("%s: %s has %d operands, wants %d", what, name, len(stack), nargs[name])
			stack = stack[:0]
			continue
		}
		switch name {
		case "q":
			qdepth++
		case "Q":
			qdepth--
			if qdepth < 0 {
				c.problem("%s: Q without q", what)
				return
			}
		case "BT":
			if inText {
				c.problem("%s: BT inside a text object", what)
			}
			inText, cur, lineOpen = true, nil, false
		case "ET":
			if !inText {
				c.problem("%s: ET outside a text object", what)
			}
			inText, lineOpen = false, false
		case "Tf":
			fname, _ := stack[0].(pdfName)
			sz, _ := numOf(stack[1])
			fi := fonts[string(fname)]
			if fi == nil {
				c.problem("%s: Tf names the font /%s, which the page does not have", what, string(fname))
			}
			if sz <= 0 || sz > 1000 {
				c.problem("%s: font size %v", what, sz)
			}
			cur, size = fi, sz
			if fi != nil {
				curBase = fi.base
				if i := strings.IndexByte(curBase, '+'); i >= 0 {
					curBase = curBase[i+1:]
				}
			}
		case "Tm":
			if !inText {
				c.problem("%s: Tm outside a text object", what)
			}
			x, _ := numOf(stack[4])
			y, _ := numOf(stack[5])
			pg.Lines = append(pg.Lines, PDFLine{X: x, Y: y, Size: size, Font: curBase})
			lineOpen = true
		case "TJ":
			if !inText || cur == nil || !lineOpen {
				c.problem("%s: TJ with no text object, font or position", what)
				break
			}
			arr, ok := stack[0].(pdfArr)
			if !ok {
				c.problem("%s: TJ has no array", what)
				break
			}
			ln := &pg.Lines[len(pg.Lines)-1]
			ln.Font = curBase
			ln.Size = size
			for _, el := range arr {
				switch t := el.(type) {
				case pdfNum:
				case pdfStr:
					if !t.hex || len(t.b)%2 != 0 {
						c.problem("%s: a TJ string is not whole two-byte codes in hex", what)
						continue
					}
					for i := 0; i < len(t.b); i += 2 {
						code := int(t.b[i])<<8 | int(t.b[i+1])
						if code == 0 || code > cur.maxCode {
							c.problem("%s: the code %d is not in font %s (it has %d glyphs): the .notdef or a missing glyph would be drawn", what, code, cur.base, cur.maxCode)
							continue
						}
						u, ok := cur.toUni[code]
						if !ok {
							c.problem("%s: the code %d of %s has no ToUnicode entry", what, code, cur.base)
						}
						ln.Text += u
					}
				default:
					c.problem("%s: a TJ array holds something that is neither a string nor a number", what)
				}
			}
		case "Do":
			n, _ := stack[0].(pdfName)
			if !xobjs[string(n)] {
				c.problem("%s: Do names /%s, which the page does not have", what, string(n))
			}
			pg.Images++
		}
		stack = stack[:0]
	}
	if qdepth != 0 {
		c.problem("%s: q and Q do not balance", what)
	}
	if inText {
		c.problem("%s: BT without ET", what)
	}
	if len(stack) != 0 {
		c.problem("%s: operands left over at the end", what)
	}
}

// font checks a Type 0 font and its descendants and returns what the content
// check needs of it.
func (c *pdfCheck) font(obj int) *fontInfo {
	if f, ok := c.fonts[obj]; ok {
		return f
	}
	fi := &fontInfo{toUni: map[int]string{}, widths: map[int]bool{}}
	c.fonts[obj] = fi
	c.visited[obj] = true
	d := dictOf(c.get(pdfRef(obj)))
	if d == nil || d["Type"] != pdfName("Font") || d["Subtype"] != pdfName("Type0") || d["Encoding"] != pdfName("Identity-H") {
		c.problem("font %d is not a Type0 font with Identity-H", obj)
		return fi
	}
	base, _ := d["BaseFont"].(pdfName)
	fi.base = string(base)
	if len(fi.base) < 8 || fi.base[6] != '+' || strings.ToUpper(fi.base[:6]) != fi.base[:6] {
		c.problem("font %d: BaseFont %q has no subset tag", obj, fi.base)
	}
	desc, _ := d["DescendantFonts"].(pdfArr)
	if len(desc) != 1 {
		c.problem("font %d has no single descendant", obj)
		return fi
	}
	cr, ok := desc[0].(pdfRef)
	if !ok {
		c.problem("font %d: the descendant is not a reference", obj)
		return fi
	}
	c.visited[int(cr)] = true
	cid := dictOf(c.get(cr))
	if cid == nil || cid["Subtype"] != pdfName("CIDFontType2") || cid["CIDToGIDMap"] != pdfName("Identity") || cid["BaseFont"] != base {
		c.problem("font %d: the descendant is not a CIDFontType2 of the same name with an identity map", obj)
		return fi
	}
	fdr, ok := cid["FontDescriptor"].(pdfRef)
	if !ok {
		c.problem("font %d has no descriptor", obj)
		return fi
	}
	c.visited[int(fdr)] = true
	fd := dictOf(c.get(fdr))
	if fd == nil || fd["FontName"] != base {
		c.problem("font %d: the descriptor names another font", obj)
		return fi
	}
	for _, bad := range []string{"FontFile", "FontFile3"} {
		if _, has := fd[bad]; has {
			c.problem("font %d embeds /%s", obj, bad)
		}
	}
	ffr, ok := fd["FontFile2"].(pdfRef)
	if !ok {
		c.problem("font %d embeds no font file", obj)
		return fi
	}
	c.visited[int(ffr)] = true
	ff, ok := c.get(ffr).(pdfStream)
	if !ok {
		c.problem("font %d: FontFile2 is not a stream", obj)
		return fi
	}
	raw := c.streamData(fmt.Sprintf("font %d file", obj), ff)
	if l1, _ := numOf(ff.d["Length1"]); int(l1) != len(raw) {
		c.problem("font %d: Length1 is %v, the file is %d bytes", obj, ff.d["Length1"], len(raw))
	}
	glyphs := c.trueType(obj, raw)
	fi.maxCode = glyphs - 1
	// Widths: "1 [w w w ...]" for the used codes.
	wa, _ := cid["W"].(pdfArr)
	if len(wa) == 2 {
		if first, _ := numOf(wa[0]); first == 1 {
			if arr, ok := wa[1].(pdfArr); ok {
				for i := range arr {
					fi.widths[i+1] = true
				}
			}
		}
	} else if len(wa) != 0 {
		c.problem("font %d: /W is not one range", obj)
	}
	tr, ok := d["ToUnicode"].(pdfRef)
	if !ok {
		c.problem("font %d has no ToUnicode map", obj)
		return fi
	}
	c.visited[int(tr)] = true
	tu, ok := c.get(tr).(pdfStream)
	if !ok {
		c.problem("font %d: ToUnicode is not a stream", obj)
		return fi
	}
	cm := c.streamData(fmt.Sprintf("font %d ToUnicode", obj), tu)
	c.toUnicode(obj, cm, fi)
	// Every used glyph (the codes with a width) has a ToUnicode entry, and the other way round.
	for code := range fi.widths {
		if _, ok := fi.toUni[code]; !ok {
			c.problem("font %d: code %d has a width and no ToUnicode entry", obj, code)
		}
	}
	for code := range fi.toUni {
		if !fi.widths[code] {
			c.problem("font %d: code %d has a ToUnicode entry and no width", obj, code)
		}
	}
	fi.ok = true
	if len(fi.widths) > fi.maxCode {
		c.problem("font %d: %d used codes in a font of %d glyphs", obj, len(fi.widths), fi.maxCode+1)
	}
	return fi
}

func (c *pdfCheck) toUnicode(obj int, cm []byte, fi *fontInfo) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(pdfError); ok {
				c.problem("font %d ToUnicode: %s", obj, e.msg)
				return
			}
			panic(r)
		}
	}()
	lx := &lexer{b: cm}
	var stack []any
	inChar := false
	entries := 0
	for !lx.atEnd() {
		v := lx.value(true, 0)
		op, isOp := v.(pdfOp)
		if !isOp {
			stack = append(stack, v)
			if len(stack) > 10_000 {
				c.problem("font %d ToUnicode: too many operands", obj)
				return
			}
			continue
		}
		switch string(op) {
		case "beginbfchar":
			inChar, entries = true, 0
			if n, _ := numOf(stack[len(stack)-1]); n > 100 {
				c.problem("font %d ToUnicode: a block of %v entries (the limit is 100)", obj, n)
			}
			stack = stack[:0]
			continue
		case "endbfchar":
			if len(stack)%2 != 0 {
				c.problem("font %d ToUnicode: a bfchar block with an odd number of items", obj)
			}
			for i := 0; i+1 < len(stack); i += 2 {
				src, ok1 := stack[i].(pdfStr)
				dst, ok2 := stack[i+1].(pdfStr)
				if !ok1 || !ok2 || len(src.b) != 2 || len(dst.b) < 2 || len(dst.b)%2 != 0 {
					c.problem("font %d ToUnicode: a malformed bfchar entry", obj)
					continue
				}
				code := int(src.b[0])<<8 | int(src.b[1])
				if _, dup := fi.toUni[code]; dup {
					c.problem("font %d ToUnicode: code %d is mapped twice", obj, code)
				}
				fi.toUni[code] = decodeUTF16(dst.b)
				entries++
			}
			if entries > 100 {
				c.problem("font %d ToUnicode: a block of %d entries", obj, entries)
			}
			inChar = false
		}
		stack = stack[:0]
	}
	_ = inChar
	if len(fi.toUni) == 0 {
		c.problem("font %d: the ToUnicode map is empty", obj)
	}
}

// trueType checks an embedded subset and returns its glyph count.
func (c *pdfCheck) trueType(obj int, d []byte) int {
	if len(d) < 12 || binary.BigEndian.Uint32(d) != 0x00010000 {
		c.problem("font %d: the embedded file is not TrueType", obj)
		return 1
	}
	n := int(binary.BigEndian.Uint16(d[4:]))
	if 12+16*n > len(d) {
		c.problem("font %d: the table directory is cut short", obj)
		return 1
	}
	tables := map[string][]byte{}
	var total uint32
	words := func(b []byte) uint32 {
		var s uint32
		for i := 0; i < len(b); i += 4 {
			var w [4]byte
			copy(w[:], b[i:min(i+4, len(b))])
			s += binary.BigEndian.Uint32(w[:])
		}
		return s
	}
	for i := 0; i < n; i++ {
		rec := d[12+16*i:]
		tag := string(rec[:4])
		sum := binary.BigEndian.Uint32(rec[4:])
		off, ln := int(binary.BigEndian.Uint32(rec[8:])), int(binary.BigEndian.Uint32(rec[12:]))
		if off < 0 || ln < 0 || off+ln > len(d) || off%4 != 0 {
			c.problem("font %d: table %q is outside the file", obj, tag)
			return 1
		}
		t := d[off : off+ln]
		tables[tag] = t
		if tag != "head" && words(t) != sum {
			c.problem("font %d: table %q has the wrong checksum", obj, tag)
		}
	}
	total = words(d)
	if total != 0xB1B0AFBA {
		c.problem("font %d: the file's checksum adjustment is wrong", obj)
	}
	for _, need := range []string{"head", "hhea", "maxp", "hmtx", "loca", "glyf"} {
		if tables[need] == nil {
			c.problem("font %d: no %q table", obj, need)
			return 1
		}
	}
	for tag := range tables {
		switch tag {
		case "head", "hhea", "maxp", "hmtx", "loca", "glyf", "name":
		default:
			c.problem("font %d: the subset carries a %q table", obj, tag)
		}
	}
	// the font's copyright notice travels with it
	if nm := tables["name"]; len(nm) < 6 || binary.BigEndian.Uint16(nm[2:]) == 0 {
		c.problem("font %d: the subset carries no copyright notice (a name table)", obj)
	} else if !bytes.Contains(nm, []byte("C\x00o\x00p\x00y\x00r\x00i\x00g\x00h\x00t")) && !bytes.Contains(nm, []byte("c\x00o\x00p\x00y\x00r\x00i\x00g\x00h\x00t")) {
		c.problem("font %d: the name table has no copyright statement", obj)
	}
	head, hhea, maxp := tables["head"], tables["hhea"], tables["maxp"]
	if len(head) < 54 || binary.BigEndian.Uint32(head[12:]) != 0x5F0F3CF5 {
		c.problem("font %d: head is damaged", obj)
		return 1
	}
	if binary.BigEndian.Uint16(head[50:]) != 1 {
		c.problem("font %d: the subset does not use long loca offsets", obj)
	}
	if len(maxp) < 6 || len(hhea) < 36 {
		c.problem("font %d: maxp or hhea is short", obj)
		return 1
	}
	ng := int(binary.BigEndian.Uint16(maxp[4:]))
	nh := int(binary.BigEndian.Uint16(hhea[34:]))
	if nh != ng || len(tables["hmtx"]) != 4*ng {
		c.problem("font %d: hmtx does not match the glyph count", obj)
	}
	loca, glyf := tables["loca"], tables["glyf"]
	if len(loca) != 4*(ng+1) {
		c.problem("font %d: loca has %d bytes for %d glyphs", obj, len(loca), ng)
		return ng
	}
	prev := uint32(0)
	for g := 0; g <= ng; g++ {
		o := binary.BigEndian.Uint32(loca[4*g:])
		if o < prev || int(o) > len(glyf) || o%4 != 0 {
			c.problem("font %d: loca[%d] = %d is out of order or out of range", obj, g, o)
			return ng
		}
		if g > 0 && o-prev >= 10 {
			gd := glyf[prev:o]
			nc := int(int16(binary.BigEndian.Uint16(gd)))
			if nc >= 0 {
				end := 10 + 2*nc
				if end+2 > len(gd) {
					c.problem("font %d: glyph %d is cut short", obj, g-1)
				} else if il := int(binary.BigEndian.Uint16(gd[end:])); il != 0 {
					c.problem("font %d: glyph %d still carries %d bytes of hinting", obj, g-1, il)
				}
			} else {
				// A composite: every component is a glyph of the subset.
				off := 10
				for off+4 <= len(gd) {
					flags := int(binary.BigEndian.Uint16(gd[off:]))
					kid := int(binary.BigEndian.Uint16(gd[off+2:]))
					if kid >= ng {
						c.problem("font %d: glyph %d uses component %d of a font of %d glyphs", obj, g-1, kid, ng)
					}
					if flags&0x0100 != 0 {
						c.problem("font %d: composite glyph %d announces instructions", obj, g-1)
					}
					off += 4
					if flags&1 != 0 {
						off += 4
					} else {
						off += 2
					}
					switch {
					case flags&0x0008 != 0:
						off += 2
					case flags&0x0040 != 0:
						off += 4
					case flags&0x0080 != 0:
						off += 8
					}
					if flags&0x0020 == 0 {
						break
					}
				}
			}
		}
		prev = o
	}
	return ng
}

func (c *pdfCheck) image(obj int) {
	if c.visited[obj] {
		return
	}
	c.visited[obj] = true
	st, ok := c.get(pdfRef(obj)).(pdfStream)
	if !ok || st.d["Subtype"] != pdfName("Image") || st.d["Type"] != pdfName("XObject") {
		c.problem("xobject %d is not an image", obj)
		return
	}
	w, _ := numOf(st.d["Width"])
	h, _ := numOf(st.d["Height"])
	bpc, _ := numOf(st.d["BitsPerComponent"])
	if w < 1 || h < 1 || w*h > 1<<28 {
		c.problem("image %d has a size of %vx%v", obj, w, h)
		return
	}
	switch bpc {
	case 1, 2, 4, 8, 16:
	default:
		c.problem("image %d has %v bits per component", obj, bpc)
	}
	comps := 0
	switch cs := st.d["ColorSpace"].(type) {
	case pdfName:
		switch cs {
		case "DeviceGray":
			comps = 1
		case "DeviceRGB":
			comps = 3
		default:
			c.problem("image %d has colour space /%s", obj, string(cs))
		}
	case pdfArr:
		if len(cs) == 4 && cs[0] == pdfName("Indexed") && cs[1] == pdfName("DeviceRGB") {
			comps = 1
			if hi, _ := numOf(cs[2]); hi < 0 || hi > 255 {
				c.problem("image %d has a palette of %v colours", obj, hi)
			} else if s, ok := cs[3].(pdfStr); !ok || len(s.b) != 3*(int(hi)+1) {
				c.problem("image %d: the palette has the wrong size", obj)
			}
		} else {
			c.problem("image %d has an unsupported colour space", obj)
		}
	default:
		c.problem("image %d has no colour space", obj)
	}
	for k := range st.d {
		switch k {
		case "Type", "Subtype", "Width", "Height", "ColorSpace", "BitsPerComponent", "Filter", "DecodeParms", "SMask", "Length":
		default:
			c.problem("image %d has /%s", obj, k)
		}
	}
	switch st.d["Filter"] {
	case pdfName("DCTDecode"):
		if len(st.data) < 4 || st.data[0] != 0xFF || st.data[1] != 0xD8 || st.data[len(st.data)-2] != 0xFF || st.data[len(st.data)-1] != 0xD9 {
			c.problem("image %d is not a JPEG (no SOI or EOI)", obj)
		}
	case pdfName("FlateDecode"):
		raw, err := inflate(st.data, maxInflate)
		if err != nil {
			c.problem("image %d does not inflate: %v", obj, err)
			break
		}
		rowBytes := (int(w)*comps*int(bpc) + 7) / 8
		want := rowBytes * int(h)
		if dp := dictOf(st.d["DecodeParms"]); dp != nil {
			if p, _ := numOf(dp["Predictor"]); p >= 10 {
				want = (rowBytes + 1) * int(h)
				cols, _ := numOf(dp["Columns"])
				colors, _ := numOf(dp["Colors"])
				bp, _ := numOf(dp["BitsPerComponent"])
				if int(cols) != int(w) || int(colors) != comps || int(bp) != int(bpc) {
					c.problem("image %d: the predictor's parameters do not match the image", obj)
				}
			}
		}
		if len(raw) != want {
			c.problem("image %d has %d bytes of samples, wants %d", obj, len(raw), want)
		}
	case nil:
		if len(st.data) != ((int(w)*comps*int(bpc)+7)/8)*int(h) {
			c.problem("image %d has the wrong number of raw bytes", obj)
		}
	default:
		c.problem("image %d uses an unsupported filter", obj)
	}
	if m, ok := st.d["SMask"].(pdfRef); ok {
		mst, ok := c.get(m).(pdfStream)
		if !ok || mst.d["ColorSpace"] != pdfName("DeviceGray") {
			c.problem("image %d: the soft mask is not a grey image", obj)
		} else if mw, _ := numOf(mst.d["Width"]); mw != w {
			c.problem("image %d: the soft mask is another size", obj)
		}
		c.image(int(m))
	}
}

func roman(n int, upper bool) string {
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"m", "cm", "d", "cd", "c", "xc", "l", "xl", "x", "ix", "v", "iv", "i"}
	var b strings.Builder
	for i, v := range vals {
		for n >= v {
			b.WriteString(syms[i])
			n -= v
		}
	}
	if upper {
		return strings.ToUpper(b.String())
	}
	return b.String()
}

func (c *pdfCheck) labels(doc *PDFDoc, pl pdfDict) {
	nums, _ := pl["Nums"].(pdfArr)
	if len(nums) == 0 || len(nums)%2 != 0 {
		c.problem("PageLabels /Nums is malformed")
		return
	}
	type rng struct {
		page   int
		style  string
		prefix string
		start  int
	}
	var rs []rng
	for i := 0; i < len(nums); i += 2 {
		p, ok := numOf(nums[i])
		d, ok2 := nums[i+1].(pdfDict)
		if !ok || !ok2 {
			c.problem("PageLabels /Nums is malformed")
			return
		}
		r := rng{page: int(p), start: 1}
		if s, ok := d["S"].(pdfName); ok {
			switch s {
			case "D", "r", "R", "a", "A":
				r.style = string(s)
			default:
				c.problem("PageLabels style /%s", string(s))
			}
		}
		if s, ok := d["P"].(pdfStr); ok && len(s.b) >= 2 {
			r.prefix = decodeUTF16(s.b[2:])
		}
		if st, ok := numOf(d["St"]); ok {
			r.start = int(st)
		}
		rs = append(rs, r)
	}
	if rs[0].page != 0 {
		c.problem("PageLabels does not start at page 0")
	}
	for i := range doc.Pages {
		var cur rng
		for _, r := range rs {
			if r.page <= i {
				cur = r
			}
		}
		n := cur.start + i - cur.page
		lab := cur.prefix
		switch cur.style {
		case "D":
			lab += strconv.Itoa(n)
		case "r":
			lab += roman(n, false)
		case "R":
			lab += roman(n, true)
		}
		doc.Pages[i].Label = lab
	}
}

func (c *pdfCheck) outline(doc *PDFDoc, root int) {
	c.visited[root] = true
	rd := dictOf(c.get(pdfRef(root)))
	if rd == nil || rd["Type"] != pdfName("Outlines") {
		c.problem("the outline root is not /Type /Outlines")
		return
	}
	var walk func(parent, first int, depth int) int
	walk = func(parent, first, depth int) int {
		if depth > 32 {
			c.problem("the outline is nested too deep")
			return 0
		}
		count := 0
		prev := 0
		for cur := first; cur != 0; {
			if c.visited[cur] {
				c.problem("outline item %d is reached twice", cur)
				return count
			}
			c.visited[cur] = true
			d := dictOf(c.get(pdfRef(cur)))
			if d == nil {
				c.problem("outline item %d is not a dictionary", cur)
				return count
			}
			if p, _ := d["Parent"].(pdfRef); int(p) != parent {
				c.problem("outline item %d has the wrong /Parent", cur)
			}
			if pv, _ := d["Prev"].(pdfRef); int(pv) != prev {
				c.problem("outline item %d has the wrong /Prev", cur)
			}
			t, ok := d["Title"].(pdfStr)
			if !ok || !t.hex || len(t.b) < 2 || t.b[0] != 0xFE || t.b[1] != 0xFF {
				c.problem("outline item %d has no hex UTF-16BE title", cur)
			} else {
				doc.Outline = append(doc.Outline, decodeUTF16(t.b[2:]))
			}
			if dest, ok := d["Dest"].(pdfArr); !ok || len(dest) != 5 {
				c.problem("outline item %d has no destination", cur)
			} else if pr, ok := dest[0].(pdfRef); !ok || func() bool { _, isPage := c.pageObjs[int(pr)]; return !isPage }() {
				c.problem("outline item %d points at something that is not a page", cur)
			} else if dest[1] != pdfName("XYZ") {
				c.problem("outline item %d does not use an XYZ destination", cur)
			}
			for k := range d {
				switch k {
				case "Title", "Parent", "Prev", "Next", "First", "Last", "Count", "Dest":
				default:
					c.problem("outline item %d has /%s", cur, k)
				}
			}
			n := 1
			if f, ok := d["First"].(pdfRef); ok {
				sub := walk(cur, int(f), depth+1)
				n += sub
				if cnt, _ := numOf(d["Count"]); int(cnt) != sub {
					c.problem("outline item %d says /Count %v, it has %d descendants", cur, d["Count"], sub)
				}
				if l, _ := d["Last"].(pdfRef); int(l) == 0 {
					c.problem("outline item %d has no /Last", cur)
				}
			}
			count += n
			prev = cur
			nx, _ := d["Next"].(pdfRef)
			cur = int(nx)
		}
		return count
	}
	f, _ := rd["First"].(pdfRef)
	total := walk(root, int(f), 0)
	if cnt, _ := numOf(rd["Count"]); int(cnt) != total {
		c.problem("the outline says /Count %v, it has %d items", rd["Count"], total)
	}
}

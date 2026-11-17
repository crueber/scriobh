// Package pdfw writes PDF 1.7 files: objects, streams, a cross-reference
// table, embedded subset TrueType fonts addressed by two-byte codes with a
// ToUnicode map (so the text can be searched and copied), images, a page tree,
// page labels and an outline.
//
// It writes only what a printed book needs and never anything active: no
// JavaScript, no actions, no annotations, no embedded files, no forms, no
// external references. Output is deterministic: objects are written in the
// order they are created, there are no clocks (dates, when given, come from
// the caller), and the document id is the caller's.
//
// Lengths are integers in thousandths of a point (milli-points). The layout
// computes in integers so the same input gives the same bytes on any
// processor; this package only prints them.
package pdfw

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Doc is a PDF file being written.
type Doc struct {
	buf     bytes.Buffer
	offsets []int // offset of object n at index n-1; -1 until it is written
	fonts   []*Font
	images  []*Image
	pages   []*Page
	outline []OutlineItem
	labels  []PageLabel

	Title, Author, Subject, Language string
	// Created is written as the creation and modification date when it is not
	// zero: it must come from an input of the file, never from the clock.
	Created [6]int // year, month, day, hour, minute, second (UTC); year 0 means none
	// ID is the document identifier (16 bytes) written twice in the trailer.
	ID [16]byte
	// Producer names the program; no version, so a new build does not change the bytes.
	Producer string
	// Uncompressed writes streams without Flate: for tests that read or change
	// a content stream. Never set in production.
	Uncompressed bool
	// TwoPage asks a viewer to show facing pages, the first page alone on the right.
	TwoPage bool
}

// New starts a document.
func New() *Doc {
	d := &Doc{Producer: "Scriobh"}
	d.buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	return d
}

// Alloc reserves an object number.
func (d *Doc) Alloc() int {
	d.offsets = append(d.offsets, -1)
	return len(d.offsets)
}

// Put writes object n with the given body (a dictionary, array or number).
func (d *Doc) Put(n int, body string) {
	d.offsets[n-1] = d.buf.Len()
	fmt.Fprintf(&d.buf, "%d 0 obj\n%s\nendobj\n", n, body)
}

// PutStream writes object n as a stream. dict is the dictionary's entries
// without the enclosing brackets and without /Length (and /Filter, when
// compress is set).
func (d *Doc) PutStream(n int, dict string, data []byte, compress bool) {
	if compress && !d.Uncompressed {
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		_, _ = w.Write(data)
		_ = w.Close()
		data = z.Bytes()
		dict += " /Filter /FlateDecode"
	}
	d.offsets[n-1] = d.buf.Len()
	fmt.Fprintf(&d.buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, strings.TrimSpace(dict), len(data))
	d.buf.Write(data)
	d.buf.WriteString("\nendstream\nendobj\n")
}

// PutRawStream writes a stream whose data is already in the filter named in dict.
func (d *Doc) PutRawStream(n int, dict string, data []byte) {
	d.offsets[n-1] = d.buf.Len()
	fmt.Fprintf(&d.buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, strings.TrimSpace(dict), len(data))
	d.buf.Write(data)
	d.buf.WriteString("\nendstream\nendobj\n")
}

// Obj allocates and writes an object in one step.
func (d *Doc) Obj(body string) int {
	n := d.Alloc()
	d.Put(n, body)
	return n
}

// ---- values -------------------------------------------------------------------------------

// U prints a length in milli-points as a PDF number with up to three decimals.
func U(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	whole, frac := v/1000, v%1000
	s := strconv.FormatInt(whole, 10)
	if frac != 0 {
		f := fmt.Sprintf("%03d", frac)
		f = strings.TrimRight(f, "0")
		s += "." + f
	}
	if neg && s != "0" {
		s = "-" + s
	}
	return s
}

// HexString writes s as a UTF-16BE text string in hex with a byte order mark:
// whatever s holds, nothing in it can end the string or start an object.
func HexString(s string) string {
	u := utf16.Encode([]rune(s))
	var b strings.Builder
	b.WriteString("<FEFF")
	for _, c := range u {
		fmt.Fprintf(&b, "%04X", c)
	}
	b.WriteString(">")
	return b.String()
}

// Literal writes an ASCII-safe literal string, escaping what PDF requires.
// Use HexString for anything a user wrote.
func Literal(s string) string {
	var b strings.Builder
	b.WriteByte('(')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '(' || c == ')' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c >= 0x7f:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte(')')
	return b.String()
}

func pdfDate(t [6]int) string {
	return fmt.Sprintf("(D:%04d%02d%02d%02d%02d%02dZ)", t[0], t[1], t[2], t[3], t[4], t[5])
}

// ---- pages, labels, outline -------------------------------------------------------------

// Page is one page: its size, its content and what it uses.
type Page struct {
	W, H int64 // media box, milli-points
	// Trim is the trim box inside the media box (x0, y0, x1, y1 from the bottom
	// left), when it differs from it; zero means none.
	Trim, Bleed [4]int64
	Content     *Content
	obj         int
}

// NewPage adds a page.
func (d *Doc) NewPage(w, h int64) *Page {
	p := &Page{W: w, H: h, Content: &Content{fonts: map[*Font]bool{}, images: map[*Image]bool{}}}
	d.pages = append(d.pages, p)
	return p
}

// Pages returns how many pages there are.
func (d *Doc) Pages() int { return len(d.pages) }

// PageLabel starts a run of page labels at a page (0-based).
type PageLabel struct {
	Page   int
	Style  string // "D" decimal, "r" lower roman, "R" upper roman, "" none
	Prefix string
	Start  int
}

// SetLabels sets the page label ranges (sorted by page, the first at page 0).
func (d *Doc) SetLabels(l []PageLabel) { d.labels = l }

// OutlineItem is a bookmark.
type OutlineItem struct {
	Title string
	Page  int // 0-based
	Level int // 0 top
	// Y is the distance from the top of the page the viewer scrolls to, in milli-points.
	Y int64
}

// SetOutline sets the bookmarks.
func (d *Doc) SetOutline(o []OutlineItem) { d.outline = o }

// ---- finishing -----------------------------------------------------------------------------

const maxKids = 64

// Finish writes every font, image, page, the page tree, the outline and the
// trailer, and returns the file.
func (d *Doc) Finish() ([]byte, error) {
	// Page objects first so the outline can point at them.
	for _, p := range d.pages {
		p.obj = d.Alloc()
	}
	for _, f := range d.fonts {
		if err := f.write(d); err != nil {
			return nil, err
		}
	}
	for _, im := range d.images {
		im.write(d)
	}
	fontObj := map[*Font]int{}
	for _, f := range d.fonts {
		fontObj[f] = f.obj
	}
	imgObj := map[*Image]int{}
	for _, im := range d.images {
		imgObj[im] = im.obj
	}
	// Pages tree: leaves of up to maxKids pages under intermediate nodes.
	rootObj := d.Alloc()
	var leaves []int
	for start := 0; start < len(d.pages); start += maxKids {
		end := min(start+maxKids, len(d.pages))
		leaf := rootObj
		if len(d.pages) > maxKids {
			leaf = d.Alloc()
			leaves = append(leaves, leaf)
		}
		var kids strings.Builder
		for _, p := range d.pages[start:end] {
			fmt.Fprintf(&kids, "%d 0 R ", p.obj)
		}
		if len(d.pages) > maxKids {
			d.Put(leaf, fmt.Sprintf("<< /Type /Pages /Parent %d 0 R /Kids [%s] /Count %d >>", rootObj, strings.TrimSpace(kids.String()), end-start))
		}
		for _, p := range d.pages[start:end] {
			parent := leaf
			content := d.Alloc()
			d.PutStream(content, "", p.Content.Bytes(), true)
			var res strings.Builder
			res.WriteString("<< /ProcSet [/PDF /Text /ImageB /ImageC]")
			if fs := p.Content.sortedFonts(); len(fs) > 0 {
				res.WriteString(" /Font <<")
				for _, f := range fs {
					fmt.Fprintf(&res, " /%s %d 0 R", f.Res, fontObj[f])
				}
				res.WriteString(" >>")
			}
			if ims := p.Content.sortedImages(); len(ims) > 0 {
				res.WriteString(" /XObject <<")
				for _, im := range ims {
					fmt.Fprintf(&res, " /%s %d 0 R", im.Res, imgObj[im])
				}
				res.WriteString(" >>")
			}
			res.WriteString(" >>")
			box := fmt.Sprintf("[0 0 %s %s]", U(p.W), U(p.H))
			extra := ""
			if p.Trim != [4]int64{} {
				extra += fmt.Sprintf(" /TrimBox [%s %s %s %s]", U(p.Trim[0]), U(p.Trim[1]), U(p.Trim[2]), U(p.Trim[3]))
			}
			if p.Bleed != [4]int64{} {
				extra += fmt.Sprintf(" /BleedBox [%s %s %s %s]", U(p.Bleed[0]), U(p.Bleed[1]), U(p.Bleed[2]), U(p.Bleed[3]))
			}
			d.Put(p.obj, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox %s /CropBox %s%s /Resources %s /Contents %d 0 R >>", parent, box, box, extra, res.String(), content))
		}
	}
	if len(d.pages) > maxKids {
		var kids strings.Builder
		for _, l := range leaves {
			fmt.Fprintf(&kids, "%d 0 R ", l)
		}
		d.Put(rootObj, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.TrimSpace(kids.String()), len(d.pages)))
	} else {
		var kids strings.Builder
		for _, p := range d.pages {
			fmt.Fprintf(&kids, "%d 0 R ", p.obj)
		}
		d.Put(rootObj, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.TrimSpace(kids.String()), len(d.pages)))
	}
	catalog := fmt.Sprintf("/Type /Catalog /Pages %d 0 R", rootObj)
	if d.Language != "" {
		catalog += " /Lang " + Literal(d.Language)
	}
	catalog += " /ViewerPreferences << /DisplayDocTitle true >>"
	if d.TwoPage {
		catalog += " /PageLayout /TwoPageRight"
	}
	if len(d.labels) > 0 {
		var l strings.Builder
		for _, pl := range d.labels {
			fmt.Fprintf(&l, "%d <<", pl.Page)
			if pl.Style != "" {
				fmt.Fprintf(&l, " /S /%s", pl.Style)
			}
			if pl.Prefix != "" {
				fmt.Fprintf(&l, " /P %s", HexString(pl.Prefix))
			}
			if pl.Start > 1 {
				fmt.Fprintf(&l, " /St %d", pl.Start)
			}
			l.WriteString(" >> ")
		}
		catalog += fmt.Sprintf(" /PageLabels << /Nums [%s] >>", strings.TrimSpace(l.String()))
	}
	if n := d.writeOutline(); n > 0 {
		catalog += fmt.Sprintf(" /Outlines %d 0 R /PageMode /UseOutlines", n)
	}
	cat := d.Obj("<< " + catalog + " >>")
	info := "/Producer " + HexString(d.Producer) + " /Creator " + HexString(d.Producer)
	if d.Title != "" {
		info += " /Title " + HexString(d.Title)
	}
	if d.Author != "" {
		info += " /Author " + HexString(d.Author)
	}
	if d.Subject != "" {
		info += " /Subject " + HexString(d.Subject)
	}
	if d.Created[0] > 0 {
		info += " /CreationDate " + pdfDate(d.Created) + " /ModDate " + pdfDate(d.Created)
	}
	infoObj := d.Obj("<< " + info + " >>")

	for i, off := range d.offsets {
		if off < 0 {
			return nil, fmt.Errorf("pdfw: object %d was never written", i+1)
		}
	}
	xref := d.buf.Len()
	fmt.Fprintf(&d.buf, "xref\n0 %d\n0000000000 65535 f \n", len(d.offsets)+1)
	for _, off := range d.offsets {
		fmt.Fprintf(&d.buf, "%010d 00000 n \n", off)
	}
	id := fmt.Sprintf("<%X>", d.ID[:])
	fmt.Fprintf(&d.buf, "trailer\n<< /Size %d /Root %d 0 R /Info %d 0 R /ID [%s %s] >>\nstartxref\n%d\n%%%%EOF\n", len(d.offsets)+1, cat, infoObj, id, id, xref)
	return d.buf.Bytes(), nil
}

func (d *Doc) writeOutline() int {
	if len(d.outline) == 0 {
		return 0
	}
	n := len(d.outline)
	nums := make([]int, n)
	for i := range nums {
		nums[i] = d.Alloc()
	}
	root := d.Alloc()
	parent := make([]int, n) // index of the parent item, -1 for the root
	first, last := make([]int, n), make([]int, n)
	next, prev := make([]int, n), make([]int, n)
	for i := range parent {
		parent[i], first[i], last[i], next[i], prev[i] = -1, -1, -1, -1, -1
	}
	var stack []int // indices of the open ancestors
	rootFirst, rootLast := -1, -1
	for i, it := range d.outline {
		lv := max(it.Level, 0)
		for len(stack) > 0 && d.outline[stack[len(stack)-1]].Level >= lv {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			p := stack[len(stack)-1]
			parent[i] = p
			if first[p] < 0 {
				first[p] = i
			} else {
				next[last[p]] = i
				prev[i] = last[p]
			}
			last[p] = i
		} else {
			if rootFirst < 0 {
				rootFirst = i
			} else {
				next[rootLast] = i
				prev[i] = rootLast
			}
			rootLast = i
		}
		stack = append(stack, i)
	}
	// /Count is the number of visible descendants: children come after their
	// parent, so one pass from the end adds each item to its parent.
	desc := make([]int, n)
	for i := n - 1; i >= 0; i-- {
		if p := parent[i]; p >= 0 {
			desc[p] += 1 + desc[i]
		}
	}
	total := n
	for i, it := range d.outline {
		pg := 0
		if it.Page >= 0 && it.Page < len(d.pages) {
			pg = it.Page
		}
		par := root
		if parent[i] >= 0 {
			par = nums[parent[i]]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "<< /Title %s /Parent %d 0 R /Dest [%d 0 R /XYZ 0 %s null]", HexString(it.Title), par, d.pages[pg].obj, U(d.pages[pg].H-it.Y))
		if first[i] >= 0 {
			fmt.Fprintf(&b, " /First %d 0 R /Last %d 0 R /Count %d", nums[first[i]], nums[last[i]], desc[i])
		}
		if next[i] >= 0 {
			fmt.Fprintf(&b, " /Next %d 0 R", nums[next[i]])
		}
		if prev[i] >= 0 {
			fmt.Fprintf(&b, " /Prev %d 0 R", nums[prev[i]])
		}
		b.WriteString(" >>")
		d.Put(nums[i], b.String())
	}
	d.Put(root, fmt.Sprintf("<< /Type /Outlines /First %d 0 R /Last %d 0 R /Count %d >>", nums[rootFirst], nums[rootLast], total))
	return root
}

// DocumentID derives a 16-byte document id from any number of strings.
func DocumentID(parts ...string) [16]byte {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	var id [16]byte
	copy(id[:], h.Sum(nil))
	return id
}

func sortedKeys[K comparable](m map[K]bool, less func(a, b K) bool) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}

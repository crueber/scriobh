package pdfread

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf16"
)

// Doc is a PDF opened for reading its text.
type Doc struct {
	f       *File
	pages   []pageRef
	byObj   map[int]int
	Info    Info
	root    Dict
	dests   map[string]any
	opsUsed int
	textB   int
}

type pageRef struct {
	obj  int
	dict Dict
	res  Dict
	box  [4]float64
	rot  int
}

// Info is what the file says about itself and what was found in it that is
// never run or opened.
type Info struct {
	Title, Author, Producer, Creator string
	Version                          string
	Lang                             string
	Pages                            int
	Tagged                           bool
	RebuiltXref                      bool
	JavaScript                       int // actions of JavaScript or launch type found and ignored
	Launch                           int
	EmbeddedFiles                    int // counted, never extracted
	Annotations                      int // link and other annotations, ignored
	DamagedStreams                   int
	PageTreeProblem                  string // a loop, a depth or a count limit in the page tree
}

// Open reads the structure of a PDF: the page tree and the document's facts.
// It reads no page content yet.
func Open(ctx context.Context, data []byte, lim Limits) (*Doc, error) {
	lim = lim.fill()
	f, err := openFile(ctx, data, lim)
	if err != nil {
		return nil, err
	}
	d := &Doc{f: f, byObj: map[int]int{}}
	d.root, _ = f.Resolve(f.trailer["Root"]).(Dict)
	pages, _ := f.Resolve(d.root["Pages"]).(Dict)
	pagesRef, _ := d.root["Pages"].(Ref)
	d.collect(pages, nil, pagesRef, 0, map[int]bool{}, new(int), [4]float64{0, 0, 612, 792}, 0)
	if len(d.pages) == 0 {
		return nil, ErrNoPages
	}
	problem := d.Info.PageTreeProblem
	d.Info = d.facts()
	d.Info.PageTreeProblem = problem
	d.Info.Pages = len(d.pages)
	d.noteParseOver()
	return d, nil
}

// NumPages is the number of pages in the page tree.
func (d *Doc) NumPages() int { return len(d.pages) }

func (d *Doc) collect(n Dict, inherit Dict, self Ref, depth int, seen map[int]bool, visited *int, box [4]float64, rot int) {
	f := d.f
	if n == nil {
		return
	}
	if depth > f.lim.Depth {
		d.Info.PageTreeProblem = "the page tree is nested too deeply; pages below that depth were left out"
		return
	}
	if f.cancelled() {
		return
	}
	if *visited++; *visited > f.lim.TreeNodes {
		d.Info.PageTreeProblem = "the page tree has more nodes than the import limit; the rest was left out"
		return
	}
	if len(d.pages) >= f.lim.Pages {
		d.Info.PageTreeProblem = "the PDF has more pages than the import limit; the rest was left out"
		return
	}
	if self.Num != 0 {
		if seen[self.Num] {
			d.Info.PageTreeProblem = "the page tree refers back to itself; the loop was cut"
			return
		}
		seen[self.Num] = true
		defer delete(seen, self.Num)
	}
	res := inherit
	if r, ok := f.Resolve(n["Resources"]).(Dict); ok {
		res = r
	}
	if mb, ok := f.Resolve(n["MediaBox"]).(Array); ok && len(mb) == 4 {
		var b [4]float64
		okb := true
		for i := range b {
			v, ok := f.Resolve(mb[i]).(float64)
			okb = okb && ok
			b[i] = v
		}
		if okb {
			box = [4]float64{math.Min(b[0], b[2]), math.Min(b[1], b[3]), math.Max(b[0], b[2]), math.Max(b[1], b[3])}
		}
	}
	if r, ok := f.Resolve(n["Rotate"]).(float64); ok {
		rot = int(((f64i(r)%360 + 360) % 360) / 90 * 90)
	}
	kids, hasKids := f.Resolve(n["Kids"]).(Array)
	t, _ := f.Resolve(n["Type"]).(Name)
	if hasKids && t != "Page" {
		if !f.spendWork(len(kids)) {
			d.Info.PageTreeProblem = "the page tree is larger than the import's limit for walking trees; the rest was left out"
			return
		}
		for _, k := range kids {
			ref, _ := k.(Ref)
			kd, _ := f.Resolve(k).(Dict)
			d.collect(kd, res, ref, depth+1, seen, visited, box, rot)
			if f.cancelled() {
				return
			}
		}
		return
	}
	if self.Num != 0 {
		d.byObj[self.Num] = len(d.pages)
	}
	d.pages = append(d.pages, pageRef{obj: self.Num, dict: n, res: res, box: box, rot: rot})
}

func (d *Doc) facts() Info {
	f := d.f
	var in Info
	in.Version = f.version
	in.RebuiltXref = f.rebuilt
	if info, ok := f.Resolve(f.trailer["Info"]).(Dict); ok {
		in.Title = pdfText(f.Resolve(info["Title"]))
		in.Author = pdfText(f.Resolve(info["Author"]))
		in.Producer = pdfText(f.Resolve(info["Producer"]))
		in.Creator = pdfText(f.Resolve(info["Creator"]))
	}
	in.Lang = pdfText(f.Resolve(d.root["Lang"]))
	if mi, ok := f.Resolve(d.root["MarkInfo"]).(Dict); ok {
		in.Tagged, _ = f.Resolve(mi["Marked"]).(bool)
	}
	// Active content, counted and never followed.
	act := func(o any) {
		a, ok := f.Resolve(o).(Dict)
		if !ok {
			return
		}
		switch s, _ := f.Resolve(a["S"]).(Name); s {
		case "JavaScript":
			in.JavaScript++
		case "Launch", "ImportData", "SubmitForm", "URI", "GoToR":
			in.Launch++
		}
	}
	act(d.root["OpenAction"])
	if aa, ok := f.Resolve(d.root["AA"]).(Dict); ok {
		for _, v := range aa {
			act(v)
		}
	}
	if names, ok := f.Resolve(d.root["Names"]).(Dict); ok {
		if js, ok := f.Resolve(names["JavaScript"]).(Dict); ok {
			in.JavaScript += max(1, countNameTree(f, js, map[int]bool{}, 0))
		}
		if ef, ok := f.Resolve(names["EmbeddedFiles"]).(Dict); ok {
			in.EmbeddedFiles += max(1, countNameTree(f, ef, map[int]bool{}, 0))
		}
	}
	for i, p := range d.pages {
		if i > 2000 {
			break
		}
		if annots, ok := f.Resolve(p.dict["Annots"]).(Array); ok {
			for k, a := range annots {
				if k > 200 {
					break
				}
				in.Annotations++
				if ad, ok := f.Resolve(a).(Dict); ok {
					act(ad["A"])
					if aa, ok := f.Resolve(ad["AA"]).(Dict); ok {
						for _, v := range aa {
							act(v)
						}
					}
					if st, _ := f.Resolve(ad["Subtype"]).(Name); st == "FileAttachment" {
						in.EmbeddedFiles++
					}
				}
			}
		}
	}
	in.DamagedStreams = f.Damaged
	return in
}

// countNameTree counts the entries of a name tree. A node is visited once,
// whatever lists it (a tree that lists itself ends), and the whole file shares a
// budget of nodes and the deadline.
func countNameTree(f *File, n Dict, seen map[int]bool, depth int) int {
	if depth > 8 || !f.spendNode() {
		return 0
	}
	c := 0
	if names, ok := f.Resolve(n["Names"]).(Array); ok {
		c += len(names) / 2
	}
	if kids, ok := f.Resolve(n["Kids"]).(Array); ok {
		if !f.spendWork(min(len(kids), 501)) {
			return c
		}
		for i, k := range kids {
			if i > 500 {
				break
			}
			if ref, isRef := k.(Ref); isRef {
				if seen[ref.Num] {
					continue
				}
				seen[ref.Num] = true
			}
			if kd, ok := f.Resolve(k).(Dict); ok {
				c += countNameTree(f, kd, seen, depth+1)
			}
		}
	}
	return c
}

// pdfText decodes a PDF text string (UTF-16BE with a mark, UTF-8 with a mark,
// or PDFDocEncoding, read as Latin-1 for its printable part).
func pdfText(o any) string {
	s, ok := o.(String)
	if !ok {
		return ""
	}
	b := []byte(s)
	switch {
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		}
		return clean(string(utf16.Decode(u)))
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return clean(strings.ToValidUTF8(string(b[3:]), ""))
	}
	rs := make([]rune, 0, len(b))
	for _, c := range b {
		switch {
		case c >= 0x20 && c < 0x7f || c >= 0xa1:
			rs = append(rs, rune(c))
		case c == 0x95:
			rs = append(rs, '•')
		case c == 0x84:
			rs = append(rs, '—')
		case c == 0x85:
			rs = append(rs, '–')
		case c == 0x8d:
			rs = append(rs, '“')
		case c == 0x8e:
			rs = append(rs, '”')
		case c == 0x8f:
			rs = append(rs, '‘')
		case c == 0x90:
			rs = append(rs, '’')
		case c == 0x9c:
			rs = append(rs, 'œ'+0)
		}
	}
	return clean(string(rs))
}

func clean(s string) string {
	s = stripOverrides(normalise(s))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 500 {
		s = s[:500]
		for len(s) > 0 && !validEnd(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func validEnd(s string) bool {
	return strings.ToValidUTF8(s, "\x00") == s
}

// ReadPages interprets every page's content, in order, and calls fn with each
// as it is done (the page is not kept by the reader). It stops at the limits:
// a page that hits a limit is marked Truncated and the next is still read.
func (d *Doc) ReadPages(ctx context.Context, fn func(*Page) error) error {
	d.f.ctx = ctx
	for i := range d.pages {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !d.f.deadline.IsZero() && time.Now().After(d.f.deadline) {
			return errors.New("reading the PDF took longer than the import allows")
		}
		p := d.readOne(i)
		if err := fn(p); err != nil {
			return err
		}
		if d.opsUsed > d.f.lim.OpsTotal {
			return errors.New("the PDF's pages hold more drawing operations than the import limit")
		}
	}
	d.Info.DamagedStreams = d.f.Damaged
	d.noteParseOver()
	return nil
}

// noteParseOver says so when objects were left out because the file's objects are bigger than the parse limit.
func (d *Doc) noteParseOver() {
	if d.f.parseOver && d.Info.PageTreeProblem == "" {
		d.Info.PageTreeProblem = "some of the file's objects are larger than the import's limit for parsing them and were left out"
	}
}

func (d *Doc) readOne(i int) *Page {
	pr := d.pages[i]
	w, h := pr.box[2]-pr.box[0], pr.box[3]-pr.box[1]
	// The matrix that puts user space on the page as it is displayed.
	var base [6]float64
	pw, ph := w, h
	switch pr.rot {
	case 90:
		base = [6]float64{0, -1, 1, 0, -pr.box[1], w + pr.box[0]}
		pw, ph = h, w
	case 180:
		base = [6]float64{-1, 0, 0, -1, w + pr.box[0], h + pr.box[1]}
	case 270:
		base = [6]float64{0, 1, -1, 0, h + pr.box[1], -pr.box[0]}
		pw, ph = h, w
	default:
		base = [6]float64{1, 0, 0, 1, -pr.box[0], -pr.box[1]}
	}
	p := &Page{Index: i, W: pw, H: ph, obj: pr.obj}
	d.f.readPage(p, pr.dict, pr.res, base, &d.opsUsed, &d.textB)
	return p
}

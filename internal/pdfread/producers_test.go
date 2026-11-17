package pdfread_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/pdfread"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// These files are assembled by hand to have the structure the named programs
// write (their font kinds, encodings and text operators). They are imitations
// made for the tests: no file written by Word, LibreOffice, pdfTeX, Chrome or
// InDesign was available, and a real file from any of them may differ.

type producer string

const (
	pWord     producer = "word"
	pLO       producer = "libreoffice"
	pLaTeX    producer = "latex"
	pChrome   producer = "chrome"
	pInDesign producer = "indesign"
)

var glyphName = map[rune]string{' ': "space", '.': "period", ',': "comma", '-': "hyphen", '\'': "quoteright", '*': "asterisk", 'ﬁ': "fi", '’': "quoteright",
	'0': "zero", '1': "one", '2': "two", '3': "three", '4': "four", '5': "five", '6': "six", '7': "seven", '8': "eight", '9': "nine"}

type fontSet struct {
	res   string
	codes map[string]map[rune]int // font resource -> rune -> code
}

// makeFonts writes the three faces (regular F1, bold F2, italic F3) in the way p does.
func makeFonts(d *pdftest.Doc, p producer, text string) fontSet {
	seen := map[rune]bool{' ': true}
	for _, r := range text {
		seen[r] = true
	}
	var rs []rune
	for r := range seen {
		rs = append(rs, r)
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
	fs := fontSet{codes: map[string]map[rune]int{}}
	faces := []struct{ res, name, style string }{{"F1", "Serif", ""}, {"F2", "Serif-Bold", "Bold"}, {"F3", "Serif-Italic", "Italic"}}
	var res []string
	for i, f := range faces {
		codes := map[rune]int{}
		for k, r := range rs {
			codes[r] = k + 1
		}
		fs.codes[f.res] = codes
		base := fmt.Sprintf("ABCDE%c+%s", 'A'+i, f.name)
		var tu strings.Builder
		tu.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
		fmt.Fprintf(&tu, "%d beginbfchar\n", len(rs))
		for _, r := range rs {
			if r == 'ﬁ' && p == pInDesign {
				fmt.Fprintf(&tu, "<%04X> <00660069>\n", codes[r])
				continue
			}
			fmt.Fprintf(&tu, "<%04X> <%04X>\n", codes[r], r)
		}
		tu.WriteString("endbfchar endcmap end end\n")
		desc := d.Add(fmt.Sprintf("<< /Type /FontDescriptor /FontName /%s /Flags 32 /ItalicAngle %d >>", base, map[string]int{"Italic": -12}[f.style]))
		var obj int
		switch p {
		case pWord, pInDesign, pChrome:
			var w strings.Builder
			for _, r := range rs {
				wd := 500
				if r == ' ' {
					wd = 250
				}
				fmt.Fprintf(&w, "%d [%d] ", codes[r], wd)
			}
			cid := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /%s /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> /FontDescriptor %d 0 R /DW 500 /W [%s] >>", base, desc, w.String()))
			tuo := d.Stream("", []byte(tu.String()), "Flate")
			obj = d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /%s /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", base, cid, tuo))
		case pLO:
			var w strings.Builder
			for k, r := range rs {
				wd := 500
				if r == ' ' {
					wd = 250
				}
				if k > 0 {
					w.WriteByte(' ')
				}
				fmt.Fprintf(&w, "%d", wd)
			}
			tuo := d.Stream("", []byte(tu.String()), "Flate")
			obj = d.Add(fmt.Sprintf("<< /Type /Font /Subtype /TrueType /BaseFont /%s /FirstChar 1 /LastChar %d /Widths [%s] /FontDescriptor %d 0 R /ToUnicode %d 0 R >>", base, len(rs), w.String(), desc, tuo))
		case pLaTeX:
			var diff, w strings.Builder
			for k, r := range rs {
				name := glyphName[r]
				if name == "" {
					name = string(r)
				}
				fmt.Fprintf(&diff, "%d /%s ", k+1, name)
				wd := 500
				if r == ' ' {
					wd = 250
				}
				if k > 0 {
					w.WriteByte(' ')
				}
				fmt.Fprintf(&w, "%d", wd)
			}
			obj = d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /FirstChar 1 /LastChar %d /Widths [%s] /FontDescriptor %d 0 R /Encoding << /Type /Encoding /Differences [%s] >> >>", base, len(rs), w.String(), desc, diff.String()))
		}
		res = append(res, fmt.Sprintf("/%s %d 0 R", f.res, obj))
	}
	fs.res = "/Font << " + strings.Join(res, " ") + " >>"
	return fs
}

type line struct {
	text    string
	x, y    float64
	size    float64
	face    string // F1 F2 F3
	centred bool
}

// show writes one line the way p writes text.
func (fs fontSet) show(p producer, l line) string {
	codes := fs.codes[l.face]
	x := l.x
	if l.centred {
		x = 306 - float64(len([]rune(l.text)))*l.size*0.5/2
	}
	words := strings.Split(l.text, " ")
	hex := func(w string) string {
		var b strings.Builder
		for _, r := range w {
			if p == pLO {
				fmt.Fprintf(&b, "%02X", codes[r])
			} else {
				fmt.Fprintf(&b, "%04X", codes[r])
			}
		}
		return b.String()
	}
	lit := func(w string) string {
		var b strings.Builder
		for _, r := range w {
			b.WriteByte(byte(codes[r]))
		}
		return pdftest.Esc(b.String())
	}
	switch p {
	case pWord:
		// word by word, each with a kern, and the space as a glyph
		var arr strings.Builder
		for i, w := range words {
			if i > 0 {
				fmt.Fprintf(&arr, "<%s>", hex(" "))
			}
			if len(w) > 2 {
				rw := []rune(w)
				fmt.Fprintf(&arr, "<%s> -3 <%s>", hex(string(rw[:1])), hex(string(rw[1:])))
			} else {
				fmt.Fprintf(&arr, "<%s>", hex(w))
			}
		}
		return fmt.Sprintf("/P <</MCID 0>> BDC BT /%s %.2f Tf 1 0 0 1 %.2f %.2f Tm [%s] TJ ET EMC\n", l.face, l.size, x, l.y, arr.String())
	case pLO:
		return fmt.Sprintf("BT %.2f %.2f Td /%s %.2f Tf [<%s>] TJ ET\n", x, l.y, l.face, l.size, hex(l.text))
	case pLaTeX:
		var arr strings.Builder
		for i, w := range words {
			if i > 0 {
				arr.WriteString("-250")
			}
			arr.WriteString(lit(w))
		}
		return fmt.Sprintf("BT /%s %.4f Tf %.3f %.3f Td [%s]TJ ET\n", l.face, l.size, x, l.y, arr.String())
	case pChrome:
		// a page flipped to a top-left origin, glyphs drawn with a flipped text matrix
		return fmt.Sprintf("q 1 0 0 -1 0 792 cm BT /%s %.2f Tf 1 0 0 -1 %.2f %.2f Tm <%s> Tj ET Q\n", l.face, l.size, x, 792-l.y, hex(l.text))
	default: // InDesign: one string, hex, with a line matrix
		return fmt.Sprintf("/Span <</MCID 1>> BDC BT /%s %.2f Tf 1 0 0 1 %.2f %.2f Tm <%s> Tj ET EMC\n", l.face, l.size, x, l.y, hex(l.text))
	}
}

// manuscript is the same four pages for every producer.
func manuscript() [][]line {
	body := func(y float64, x float64, t string) line { return line{text: t, x: x, y: y, size: 11, face: "F1"} }
	head := func(pg int) []line {
		return []line{{text: "The Salt Road", x: 250, y: 750, size: 9, face: "F3"}, {text: fmt.Sprint(pg), x: 300, y: 40, size: 9, face: "F1"}}
	}
	p1 := append(head(1),
		line{text: "Chapter One", x: 72, y: 690, size: 20, face: "F2"},
		body(650, 72, "The boat came in at dawn and the harbour was quiet."),
		body(636, 72, "Nobody spoke of the winter that had passed."),
		body(622, 72, "She carried a small bag and a letter."),
		body(594, 86, "The road ran east along the shore past the ﬁrst kilns"),
		body(580, 72, "and the drowned fields where the sea had taken back what it"),
		body(566, 72, "had lent, and they walked until the light failed and the"),
		body(552, 72, "exam-"),
		body(538, 72, "ple of the well-"),
		body(524, 72, "known road was plain."),
		body(510, 72, "A paragraph that runs over the page"))
	p2 := append(head(2),
		body(700, 72, "and ends here on the next one."),
		body(672, 86, "A short paragraph."))
	p3 := append(head(3),
		line{text: "Chapter Two", x: 72, y: 690, size: 20, face: "F2"},
		body(650, 72, "Some words in the first line of the second chapter"),
		line{text: "italic words", x: 72, y: 636, size: 11, face: "F3"},
		line{text: "* * *", y: 608, size: 11, face: "F1", centred: true},
		body(580, 86, "After the break the story goes on."))
	p4 := append(head(4), body(700, 72, "The end of the book."))
	return [][]line{p1, p2, p3, p4}
}

func buildManuscript(p producer, extra string, opt pdftest.Opt) []byte {
	return buildPages(p, manuscript(), extra, opt)
}

func buildPages(p producer, pages [][]line, extra string, opt pdftest.Opt) []byte {
	var all strings.Builder
	for _, pg := range pages {
		for _, l := range pg {
			all.WriteString(l.text)
		}
	}
	d := pdftest.New()
	fs := makeFonts(d, p, all.String())
	var objs []int
	for _, pg := range pages {
		var c strings.Builder
		for _, l := range pg {
			c.WriteString(fs.show(p, l))
		}
		objs = append(objs, d.Page(612, 792, fs.res, c.String(), "Flate"))
	}
	root := d.Pages(objs, extra)
	return d.Write(root, opt)
}

func analyse(t *testing.T, data []byte) (*pdfread.Result, *pdfread.Doc) {
	t.Helper()
	d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var pages []*pdfread.Page
	if err := d.ReadPages(context.Background(), func(p *pdfread.Page) error { pages = append(pages, p); return nil }); err != nil {
		t.Fatal(err)
	}
	res, err := pdfread.Analyse(context.Background(), pages, d.Outline(), d.Info)
	if err != nil {
		t.Fatal(err)
	}
	return res, d
}

func blockTexts(bs []pdfread.Block) []string {
	var out []string
	for _, b := range bs {
		switch b.Kind {
		case pdfread.BlockScene:
			out = append(out, "* * *")
		default:
			var sb strings.Builder
			for _, r := range b.Runs {
				switch {
				case r.Italic:
					sb.WriteString("*" + r.Text + "*")
				case r.Bold:
					sb.WriteString("**" + r.Text + "**")
				default:
					sb.WriteString(r.Text)
				}
			}
			out = append(out, sb.String())
		}
	}
	return out
}

func TestProducerImitationsReadTheSame(t *testing.T) {
	want1 := []string{
		"The boat came in at dawn and the harbour was quiet. Nobody spoke of the winter that had passed. She carried a small bag and a letter.",
		"The road ran east along the shore past the first kilns and the drowned fields where the sea had taken back what it had lent, and they walked until the light failed and the example of the well-known road was plain. A paragraph that runs over the page and ends here on the next one.",
		"A short paragraph.",
	}
	want2 := []string{
		"Some words in the first line of the second chapter *italic words*",
		"* * *",
		"After the break the story goes on. The end of the book.",
	}
	for _, p := range []producer{pWord, pLO, pLaTeX, pChrome, pInDesign} {
		t.Run(string(p), func(t *testing.T) {
			res, _ := analyse(t, buildManuscript(p, "", pdftest.Opt{}))
			if len(res.Chapters) != 2 || res.Chapters[0].Title != "Chapter One" || res.Chapters[1].Title != "Chapter Two" {
				t.Fatalf("chapters: %+v (report %+v)", res.Chapters, res.Report)
			}
			if res.Report.ChapterSource != "headings" {
				t.Errorf("source %q", res.Report.ChapterSource)
			}
			got1, got2 := blockTexts(res.Chapters[0].Blocks), blockTexts(res.Chapters[1].Blocks)
			if strings.Join(got1, "\n") != strings.Join(want1, "\n") {
				t.Errorf("chapter one\n got: %q\nwant: %q", got1, want1)
			}
			if strings.Join(got2, "\n") != strings.Join(want2, "\n") {
				t.Errorf("chapter two\n got: %q\nwant: %q", got2, want2)
			}
			r := res.Report
			if r.HeadLines != 4 || r.PageNumbers != 4 {
				t.Errorf("running heads %d, page numbers %d; want 4 and 4", r.HeadLines, r.PageNumbers)
			}
			if r.HyphenJoined != 1 || r.HyphenKept != 1 {
				t.Errorf("hyphens joined %d kept %d; want 1 and 1", r.HyphenJoined, r.HyphenKept)
			}
			if r.JoinedAcross != 2 || r.Confidence < 95 {
				t.Errorf("joined across pages %d, confidence %d", r.JoinedAcross, r.Confidence)
			}
		})
	}
}

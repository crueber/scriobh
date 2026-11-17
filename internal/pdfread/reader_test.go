package pdfread_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/pdfread"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

func openPDF(t *testing.T, data []byte, lim pdfread.Limits) (*pdfread.Doc, []*pdfread.Page) {
	t.Helper()
	d, err := pdfread.Open(context.Background(), data, lim)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var pages []*pdfread.Page
	if err := d.ReadPages(context.Background(), func(p *pdfread.Page) error { pages = append(pages, p); return nil }); err != nil {
		t.Fatalf("read: %v", err)
	}
	return d, pages
}

func pageText(p *pdfread.Page) string {
	var parts []string
	for _, f := range p.Frags {
		parts = append(parts, f.Text)
	}
	return strings.Join(parts, "|")
}

// type1 is a simple font with a named encoding or a Differences array.
func type1(d *pdftest.Doc, base, enc string) int {
	return d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding %s >>", base, enc))
}

func simplePage(d *pdftest.Doc, font int, content string) int {
	return d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", font), content, "")
}

func TestSimpleFontEncodings(t *testing.T) {
	cases := []struct {
		name, enc, show, want string
	}{
		{"winansi", "/WinAnsiEncoding", "(caf\\351 \\223quoted\\224 \\226 \\200)", "café “quoted” – €"},
		{"macroman", "/MacRomanEncoding", "(caf\\216 \\322quoted\\323 \\320)", "café “quoted” –"},
		{"standard", "/StandardEncoding", "(\\256rst \\252quoted\\272 \\261 \\140x\\047)", "ﬁrst “quoted” – ‘x’"},
		{"differences", "<< /Type /Encoding /BaseEncoding /WinAnsiEncoding /Differences [65 /ae /uni00E9 /fi /ffi /emdash /u2603] >>", "(ABCDEF)", "æéﬁﬃ—☃"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := pdftest.New()
			f := type1(d, "Times-Roman", c.enc)
			pg := simplePage(d, f, "BT /F1 12 Tf 72 700 Td "+c.show+" Tj ET")
			root := d.Pages([]int{pg}, "")
			_, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
			if got := pageText(pages[0]); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestStreamFiltersAndFileStructures(t *testing.T) {
	content := "BT /F1 12 Tf 72 700 Td (Filtered text) Tj ET"
	for _, filter := range []string{"", "Flate", "A85", "AHx", "RL", "LZW", "A85+Flate", "AHx+RL", "Flate+A85"} {
		for _, opt := range []pdftest.Opt{{}, {XrefStream: true}, {XrefStream: true, ObjStm: true}, {NoXref: true}, {BadXref: true}, {Prefix: "junk junk\n"}} {
			name := fmt.Sprintf("%s/%+v", filter, opt)
			d := pdftest.New()
			f := type1(d, "Times-Roman", "/WinAnsiEncoding")
			pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), content, filter)
			root := d.Pages([]int{pg}, "")
			data := d.Write(root, opt)
			dd, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
			if err != nil {
				t.Errorf("%s: %v", name, err)
				continue
			}
			var got string
			dd.ReadPages(context.Background(), func(p *pdfread.Page) error { got = pageText(p); return nil })
			if got != "Filtered text" {
				t.Errorf("%s: %q", name, got)
			}
			if (opt.NoXref || opt.BadXref) && !dd.Info.RebuiltXref {
				t.Errorf("%s: the table was not rebuilt", name)
			}
		}
	}
}

func TestTruncatedFileIsReadAsFarAsItGoes(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	var pgs []int
	for i := 0; i < 3; i++ {
		pgs = append(pgs, simplePage(d, f, fmt.Sprintf("BT /F1 12 Tf 72 700 Td (Page %d) Tj ET", i+1)))
	}
	root := d.Pages(pgs, "")
	data := d.Write(root, pdftest.Opt{Truncate: 160})
	dd, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !dd.Info.RebuiltXref || dd.NumPages() != 3 {
		t.Errorf("rebuilt %v pages %d", dd.Info.RebuiltXref, dd.NumPages())
	}
}

func TestEncryptedIsRefused(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	pg := simplePage(d, f, "BT /F1 12 Tf 72 700 Td (Secret) Tj ET")
	enc := d.Add("<< /Filter /Standard /V 1 /R 2 /O (aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa) /U (bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb) /P -4 >>")
	root := d.Pages([]int{pg}, "")
	for _, opt := range []pdftest.Opt{{Trailer: fmt.Sprintf("/Encrypt %d 0 R", enc)}, {Trailer: fmt.Sprintf("/Encrypt %d 0 R", enc), NoXref: true}, {XrefStream: true, ObjStm: true, Trailer: fmt.Sprintf("/Encrypt %d 0 R", enc)}} {
		_, err := pdfread.Open(context.Background(), d.Write(root, opt), pdfread.Limits{})
		if !errors.Is(err, pdfread.ErrEncrypted) {
			t.Errorf("%+v: %v", opt, err)
		}
	}
}

func TestNotAPDF(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("hello"), []byte("PK\x03\x04 a zip"), bytes.Repeat([]byte("%PD"), 100)} {
		if _, err := pdfread.Open(context.Background(), b, pdfread.Limits{}); err == nil {
			t.Errorf("%q: opened", b[:min(len(b), 10)])
		}
	}
}

func TestActiveContentIsCountedNeverFollowed(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	js := d.Add("<< /S /JavaScript /JS (app.alert\\('x'\\)) >>")
	launch := d.Add("<< /S /Launch /F (cmd.exe) >>")
	ann1 := d.Add(fmt.Sprintf("<< /Type /Annot /Subtype /Link /Rect [0 0 10 10] /A %d 0 R >>", launch))
	ef := d.Add("<< /Type /Filespec /F (evil.exe) >>")
	cs := d.Stream("", []byte("BT /F1 12 Tf 72 700 Td (Safe text) Tj ET"), "")
	pg := d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R /Annots [%d 0 R] >>", f, cs, ann1))
	root := d.Pages([]int{pg}, fmt.Sprintf("/OpenAction %d 0 R /Names << /JavaScript << /Names [(a) %d 0 R] >> /EmbeddedFiles << /Names [(evil.exe) %d 0 R] >> >>", js, js, ef))
	dd, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	if got := pageText(pages[0]); got != "Safe text" {
		t.Errorf("text %q", got)
	}
	in := dd.Info
	if in.JavaScript < 1 || in.Launch != 1 || in.EmbeddedFiles != 1 || in.Annotations != 1 {
		t.Errorf("info: %+v", in)
	}
}

func TestRotatedPageAndRotatedTextAndInvisibleText(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	// a page with /Rotate 90 whose text is drawn sideways in user space, so it reads upright when shown
	content := "BT /F1 12 Tf 0 1 -1 0 100 100 Tm (Turned page text) Tj ET BT /F1 12 Tf 1 0 0 1 300 300 Tm (Upright text on a turned page) Tj ET"
	cs := d.Stream("", []byte(content), "")
	pg := d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /Rotate 90 /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", f, cs))
	pg2 := simplePage(d, f, "BT /F1 12 Tf 72 700 Td 3 Tr (Invisible OCR text) Tj ET")
	root := d.Pages([]int{pg, pg2}, "")
	_, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	p := pages[0]
	if p.W != 792 || p.H != 612 {
		t.Errorf("rotated page is %vx%v", p.W, p.H)
	}
	if got := pageText(p); got != "Turned page text" || p.Rotated != len("Upright text on a turned page") {
		t.Errorf("text %q, rotated %d", got, p.Rotated)
	}
	if pages[1].Invisible != len("Invisible OCR text") {
		t.Errorf("invisible chars %d", pages[1].Invisible)
	}
}

func TestPicturesAreCountedNotRead(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	img := d.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode", []byte{0xff, 0xd8, 1, 2, 3}, "")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >> /XObject << /Im1 %d 0 R >>", f, img), "q 200 0 0 100 50 500 cm /Im1 Do Q BT /F1 12 Tf 72 700 Td (Caption) Tj ET BI /W 1 /H 1 /CS /G /BPC 8 ID \x00 EI", "")
	root := d.Pages([]int{pg}, "")
	_, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	if len(pages[0].Images) != 2 || pages[0].Images[0].W != 200 || pageText(pages[0]) != "Caption" {
		t.Errorf("images %+v text %q", pages[0].Images, pageText(pages[0]))
	}
}

func TestFormXObjectTextIsRead(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	form := d.Stream(fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 612 792] /Matrix [1 0 0 1 10 0] /Resources << /Font << /F1 %d 0 R >> >>", f), []byte("BT /F1 12 Tf 62 700 Td (Inside a form) Tj ET"), "")
	pg := d.Page(612, 792, fmt.Sprintf("/XObject << /Fm1 %d 0 R >>", form), "/Fm1 Do", "")
	root := d.Pages([]int{pg}, "")
	_, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	if got := pageText(pages[0]); got != "Inside a form" || pages[0].Frags[0].X0 != 72 {
		t.Errorf("%q at %v", got, pages[0].Frags[0].X0)
	}
}

func TestOutlineAndNamedDestinations(t *testing.T) {
	d := pdftest.New()
	f := type1(d, "Times-Roman", "/WinAnsiEncoding")
	var pgs []int
	for i := 0; i < 3; i++ {
		pgs = append(pgs, simplePage(d, f, fmt.Sprintf("BT /F1 12 Tf 72 700 Td (Page %d) Tj ET", i+1)))
	}
	o1, o2, o3, root1 := d.Alloc(), d.Alloc(), d.Alloc(), d.Alloc()
	d.Set(root1, fmt.Sprintf("<< /Type /Outlines /First %d 0 R /Last %d 0 R /Count 3 >>", o1, o3))
	d.Set(o1, fmt.Sprintf("<< /Title (One) /Parent %d 0 R /Next %d 0 R /Dest [%d 0 R /XYZ 0 400 null] >>", root1, o2, pgs[0]))
	d.Set(o2, fmt.Sprintf("<< /Title <FEFF0054007700F3> /Parent %d 0 R /Prev %d 0 R /Next %d 0 R /Dest (second) >>", root1, o1, o3))
	d.Set(o3, fmt.Sprintf("<< /Title (Three) /Parent %d 0 R /Prev %d 0 R /A << /S /GoTo /D [%d 0 R /Fit] >> >>", root1, o2, pgs[2]))
	cat := d.Pages(pgs, fmt.Sprintf("/Outlines %d 0 R /Names << /Dests << /Names [(second) [%d 0 R /XYZ 0 300 null]] >> >>", root1, pgs[1]))
	dd, _ := openPDF(t, d.Write(cat, pdftest.Opt{}), pdfread.Limits{})
	ol := dd.Outline()
	if len(ol) != 3 || ol[0].Title != "One" || ol[1].Title != "Twó" || ol[0].Page != 0 || ol[1].Page != 1 || ol[2].Page != 2 || ol[0].Y != 400 || ol[1].Y != 300 {
		t.Errorf("outline: %+v", ol)
	}
	// a loop in the outline ends
	d2 := pdftest.New()
	f2 := type1(d2, "Times-Roman", "/WinAnsiEncoding")
	pg := simplePage(d2, f2, "BT /F1 12 Tf 72 700 Td (x) Tj ET")
	a, b, r := d2.Alloc(), d2.Alloc(), d2.Alloc()
	d2.Set(r, fmt.Sprintf("<< /Type /Outlines /First %d 0 R >>", a))
	d2.Set(a, fmt.Sprintf("<< /Title (A) /Next %d 0 R /First %d 0 R >>", b, a))
	d2.Set(b, fmt.Sprintf("<< /Title (B) /Next %d 0 R >>", a))
	cat2 := d2.Pages([]int{pg}, fmt.Sprintf("/Outlines %d 0 R", r))
	dd2, _ := openPDF(t, d2.Write(cat2, pdftest.Opt{}), pdfread.Limits{})
	if got := dd2.Outline(); len(got) > 3 {
		t.Errorf("a looping outline gave %d items", len(got))
	}
}

func TestCIDFontWithCMapRangesAndUCS2(t *testing.T) {
	d := pdftest.New()
	tu := d.Stream("", []byte("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n1 beginbfrange <0001> <001A> <0041> endbfrange\n1 beginbfrange <001B> <001C> [<0066> <00660069>] endbfrange\nendcmap end end"), "Flate")
	cid := d.Add("<< /Type /Font /Subtype /CIDFontType0 /BaseFont /X /CIDSystemInfo << /Registry (Adobe) /Ordering (Japan1) /Supplement 2 >> /DW 600 /W [1 [500 500] 3 5 700] >>")
	f := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", cid, tu))
	f2 := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /Y /Encoding /UniJIS-UCS2-H /DescendantFonts [%d 0 R] >>", cid))
	vert := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /Z /Encoding /Identity-V /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", cid, tu))
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R /F2 %d 0 R /F3 %d 0 R >>", f, f2, vert), "BT /F1 12 Tf 72 700 Td <0001000200030004001B001C> Tj ET BT /F2 12 Tf 72 680 Td <00480069> Tj ET BT /F3 12 Tf 72 660 Td <0001> Tj ET BT /F1 12 Tf 72 640 Td <0300> Tj ET", "")
	root := d.Pages([]int{pg}, "")
	_, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	p := pages[0]
	if got := pageText(p); got != "ABCDffi|Hi" {
		t.Errorf("text %q", got)
	}
	if p.Vertical != 1 || p.Unmapped != 1 {
		t.Errorf("vertical %d unmapped %d", p.Vertical, p.Unmapped)
	}
	// widths: A and B are 500, C D 700, the two ranges' glyphs fall back to the default 600
	want := 12 * (0.5 + 0.5 + 0.7 + 0.7 + 0.6 + 0.6)
	if x := p.Frags[0].X1 - p.Frags[0].X0; x < want-0.01 || x > want+0.01 {
		t.Errorf("advance %v, want %v", x, want)
	}
}

// An embedded Type 1 font with no /Encoding takes the encoding written in its
// own program (a real groff/Ghostscript file puts the fi ligature at code 0),
// and a space that no encoding names is still a space.
func TestEmbeddedType1BuiltInEncoding(t *testing.T) {
	d := pdftest.New()
	prog := "%!PS-AdobeFont-1.0: Sub 1.0\n/FontName /ABCDEF+Sub def\n/Encoding 256 array\n0 1 255 {1 index exch /.notdef put} for\ndup 0 /fi put\ndup 1 /fl put\ndup 65 /A put\ndup 66 /B put\nreadonly def\ncurrentfile eexec\n\x00\x01binary"
	ff := d.Stream("/Length1 150", []byte(prog), "Flate")
	desc := d.Add(fmt.Sprintf("<< /Type /FontDescriptor /FontName /ABCDEF+Sub /Flags 4 /FontFile %d 0 R >>", ff))
	f := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /ABCDEF+Sub /FirstChar 0 /LastChar 66 /Widths [%s] /FontDescriptor %d 0 R >>", strings.Repeat("500 ", 67), desc))
	pg := simplePage(d, f, "BT /F1 12 Tf 72 700 Td (\\000nd A\\001 B) Tj ET")
	root := d.Pages([]int{pg}, "")
	_, pages := openPDF(t, d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	if got := pageText(pages[0]); got != "ﬁnd Aﬂ B" {
		t.Errorf("got %q", got)
	}
	if pages[0].Unmapped != 0 {
		t.Errorf("unmapped %d", pages[0].Unmapped)
	}
}

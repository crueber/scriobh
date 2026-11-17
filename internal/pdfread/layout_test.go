package pdfread_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/pdfread"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

func ln(text string, x, y float64) line { return line{text: text, x: x, y: y, size: 11, face: "F1"} }

// two columns: each line is 300 pt wide at x=72 and x=340
func TestTwoColumnsAreReadLeftThenRight(t *testing.T) {
	var pg []line
	left := []string{"Left column first line of text", "left column second line of text", "left column third line of text", "left column fourth line of text", "left column fifth line of text", "left column sixth line of text", "left column seventh line of text", "left column eighth line of text", "left column ninth line of text", "left column tenth line of text", "left column eleventh line of text", "left column last line ends here."}
	right := []string{"Right column first line of text", "right column second line of text", "right column third line of text", "right column fourth line of text", "right column fifth line of text", "right column sixth line of text", "right column seventh line of text", "right column eighth line of text", "right column ninth line of text", "right column tenth line of text", "right column eleventh line of text", "right column last line ends here."}
	for i := range left {
		y := 700 - float64(i)*14
		l := ln(left[i], 72, y)
		r := ln(right[i], 330, y)
		pg = append(pg, l, r)
	}
	// a heading across both columns, above them
	pg = append(pg, line{text: "A heading across both columns of the page", x: 72, y: 740, size: 18, face: "F2"})
	body := buildPages(pWord, [][]line{pg}, "", pdftest.Opt{})
	res, _ := analyse(t, body)
	if res.Report.TwoColumnPages != 1 {
		t.Fatalf("columns: %+v", res.Report)
	}
	all := strings.Join(blockTexts(res.Chapters[0].Blocks), "\n")
	li, ri := strings.Index(all, "Left column first"), strings.Index(all, "Right column first")
	if li < 0 || ri < li || strings.Index(all, "left column last") > ri {
		t.Errorf("reading order:\n%s", all)
	}
	// the same lines at full width are one column
	var wide []line
	for i := range left {
		wide = append(wide, ln(left[i]+" and a good deal more text on the same line to the right margin here", 72, 700-float64(i)*14))
	}
	res2, _ := analyse(t, buildPages(pWord, [][]line{wide}, "", pdftest.Opt{}))
	if res2.Report.TwoColumnPages != 0 {
		t.Errorf("one wide column read as two")
	}
}

func TestTablesAreFlattenedAndSaid(t *testing.T) {
	pg := []line{{text: "A heading", x: 72, y: 720, size: 18, face: "F2"}, ln("Some text before the table that runs on a little.", 72, 690)}
	for i := 0; i < 5; i++ {
		y := 640 - float64(i)*14
		pg = append(pg, ln(fmt.Sprintf("Row%d", i), 72, y), ln("alpha", 220, y), ln("beta", 380, y), ln("gamma", 500, y))
	}
	pg = append(pg, ln("Text after the table.", 72, 540))
	res, _ := analyse(t, buildPages(pWord, [][]line{pg}, "", pdftest.Opt{}))
	if res.Report.TableRows != 5 || len(res.Report.TablePages) != 1 {
		t.Fatalf("report: rows %d pages %v", res.Report.TableRows, res.Report.TablePages)
	}
	got := blockTexts(res.Chapters[0].Blocks)
	if len(got) != 7 || got[1] != "Row0 | alpha | beta | gamma" {
		t.Errorf("blocks: %q", got)
	}
	if res.Report.Confidence >= 100 {
		t.Errorf("a page with a flattened table is not fully trusted: %d", res.Report.Confidence)
	}
}

func TestContentsPageIsRemoved(t *testing.T) {
	toc := []line{{text: "Contents", x: 72, y: 700, size: 18, face: "F2"}}
	for i := 0; i < 6; i++ {
		y := 650 - float64(i)*16
		toc = append(toc, ln(fmt.Sprintf("Chapter %d", i+1), 72, y), ln(fmt.Sprintf(". . . . . . . . . . . . . . . . . . . . . %d", 3+i*10), 200, y))
	}
	body := []line{{text: "Chapter 1", x: 72, y: 700, size: 18, face: "F2"}, ln("The first chapter text is here and it is a line.", 72, 660)}
	res, _ := analyse(t, buildPages(pWord, [][]line{toc, body}, "", pdftest.Opt{}))
	if len(res.Report.TOCPages) != 1 || res.Report.TOCLines < 7 {
		t.Fatalf("contents: %+v", res.Report.TOCPages)
	}
	for _, c := range res.Chapters {
		for _, b := range blockTexts(c.Blocks) {
			if strings.Contains(b, ". . .") {
				t.Errorf("leader line kept: %q", b)
			}
		}
	}
}

func TestAScannedPDFIsRefused(t *testing.T) {
	d := pdftest.New()
	img := d.Stream("/Type /XObject /Subtype /Image /Width 2 /Height 2 /ColorSpace /DeviceGray /BitsPerComponent 8 /Filter /DCTDecode", []byte{0xff, 0xd8}, "")
	var pgs []int
	for i := 0; i < 5; i++ {
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/XObject << /Im1 %d 0 R >>", img), "q 612 0 0 792 0 0 cm /Im1 Do Q", ""))
	}
	root := d.Pages(pgs, "")
	dd, err := pdfread.Open(context.Background(), d.Write(root, pdftest.Opt{}), pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var pages []*pdfread.Page
	dd.ReadPages(context.Background(), func(p *pdfread.Page) error { pages = append(pages, p); return nil })
	if _, err := pdfread.Analyse(context.Background(), pages, nil, dd.Info); !errors.Is(err, pdfread.ErrNoTextLayer) {
		t.Errorf("a scan: %v", err)
	}
	// a book with a few picture pages is read, and they are listed
	mixed := manuscript()
	data := buildPages(pWord, mixed, "", pdftest.Opt{})
	res, _ := analyse(t, data)
	if res.Report.PictureOnlyTotal != 0 {
		t.Errorf("no picture pages here")
	}
}

func TestRightToLeftAndUnmappedTextLowerConfidence(t *testing.T) {
	d := pdftest.New()
	tu := d.Stream("", []byte("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n2 beginbfchar <0001> <05D0> <0002> <0041> endbfchar\nendcmap end end"), "")
	cid := d.Add("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /DW 500 >>")
	f := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", cid, tu))
	content := "BT /F1 11 Tf 72 700 Td <00010001000100010001000200020002> Tj ET BT /F1 11 Tf 72 686 Td <0009000900090009> Tj ET"
	for i := 0; i < 8; i++ {
		content += fmt.Sprintf(" BT /F1 11 Tf 72 %d Td <000200020002000200020002> Tj ET", 650-14*i)
	}
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), content, "")
	root := d.Pages([]int{pg}, "")
	res, _ := analyse(t, d.Write(root, pdftest.Opt{}))
	r := res.Report
	if len(r.RTLPages) != 1 || r.UnmappedChars != 4 || r.Confidence >= 70 {
		t.Errorf("rtl %v unmapped %d confidence %d", r.RTLPages, r.UnmappedChars, r.Confidence)
	}
}

func TestOutlineMakesPartsAndChapters(t *testing.T) {
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var pgs []int
	titles := []string{"Part One", "First", "Second", "Part Two", "Third"}
	for i, ttl := range titles {
		c := fmt.Sprintf("BT /F1 11 Tf 72 700 Td (%s) Tj ET BT /F1 11 Tf 72 650 Td (Body text of page %d is here.) Tj ET BT /F1 11 Tf 72 636 Td (And a second line of it.) Tj ET", ttl, i)
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), c, ""))
	}
	ids := make([]int, len(titles)+1)
	for i := range ids {
		ids[i] = d.Alloc()
	}
	root1 := ids[len(titles)]
	// Part One owns First, Second; Part Two owns Third
	d.Set(ids[0], fmt.Sprintf("<< /Title (Part One) /Parent %d 0 R /Next %d 0 R /First %d 0 R /Last %d 0 R /Dest [%d 0 R /XYZ 0 710 null] >>", root1, ids[3], ids[1], ids[2], pgs[0]))
	d.Set(ids[1], fmt.Sprintf("<< /Title (First) /Parent %d 0 R /Next %d 0 R /Dest [%d 0 R /XYZ 0 710 null] >>", ids[0], ids[2], pgs[1]))
	d.Set(ids[2], fmt.Sprintf("<< /Title (Second) /Parent %d 0 R /Dest [%d 0 R /XYZ 0 710 null] >>", ids[0], pgs[2]))
	d.Set(ids[3], fmt.Sprintf("<< /Title (Part Two) /Parent %d 0 R /First %d 0 R /Last %d 0 R /Dest [%d 0 R /XYZ 0 710 null] >>", root1, ids[4], ids[4], pgs[3]))
	d.Set(ids[4], fmt.Sprintf("<< /Title (Third) /Parent %d 0 R /Dest [%d 0 R /XYZ 0 710 null] >>", ids[3], pgs[4]))
	d.Set(root1, fmt.Sprintf("<< /Type /Outlines /First %d 0 R /Last %d 0 R >>", ids[0], ids[3]))
	cat := d.Pages(pgs, fmt.Sprintf("/Outlines %d 0 R", root1))
	res, _ := analyse(t, d.Write(cat, pdftest.Opt{}))
	var got []string
	for _, c := range res.Chapters {
		got = append(got, fmt.Sprintf("%s:%v", c.Title, c.Part))
	}
	if strings.Join(got, ",") != "Part One:true,First:false,Second:false,Part Two:true,Third:false" || res.Report.ChapterSource != "outline" {
		t.Errorf("chapters %v (%s)", got, res.Report.ChapterSource)
	}
}

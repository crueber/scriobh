package pdfread_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pdfread"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
	"github.com/crueber/scriobh/internal/testutil"
)

// An encrypted file is refused when its cross-reference stream is found by the
// rebuild too, however long the entries before /Type are, and when a classic
// trailer is the only place that names /Encrypt.
func TestEncryptedFilesAreRefusedWhateverTheDamage(t *testing.T) {
	build := func(opt pdftest.Opt) []byte {
		d := pdftest.New()
		f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
		pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), "BT /F1 12 Tf 72 700 Td (Secret) Tj ET", "")
		return d.Write(d.Pages([]int{pg}, ""), opt)
	}
	longID := "/ID [<" + strings.Repeat("ab", 1500) + "> <" + strings.Repeat("cd", 1500) + ">] "
	enc := "/Encrypt << /Filter /Standard /V 1 /R 2 /O (" + strings.Repeat("o", 32) + ") /U (" + strings.Repeat("u", 32) + ") >>"
	for name, opt := range map[string]pdftest.Opt{
		"xref stream, long /ID, startxref cut": {XrefStream: true, Trailer: longID + enc, Truncate: 20},
		"xref stream, /Encrypt first":          {XrefStream: true, Trailer: enc + " " + longID, Truncate: 20},
		"classic trailer, no xref":             {NoXref: true, Trailer: enc},
		"classic trailer, bad table":           {BadXref: true, Trailer: enc},
		"object streams, startxref cut":        {XrefStream: true, ObjStm: true, Trailer: enc, Truncate: 20},
	} {
		if _, err := pdfread.Open(context.Background(), build(opt), pdfread.Limits{}); err != pdfread.ErrEncrypted {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Every number in a file that becomes an int is clamped: none of these panics, on any processor.
func TestExtremeNumbersInStructuresDoNotPanic(t *testing.T) {
	for _, n := range []string{"9223372036854775807", "-9223372036854775808", "1e400", "99999999999999999999999999", "-1", "4294967296"} {
		for _, key := range []string{"Length", "FirstChar", "Rotate"} {
			data := reviewSkeleton("", nil)
			c := "BT /F1 12 Tf 72 700 Td (hello world this is text) Tj ET"
			switch key {
			case "Length":
				data = bytes.Replace(data, []byte(fmt.Sprintf("/Length %d", len(c))), []byte("/Length "+n), 1)
			case "FirstChar":
				data = bytes.Replace(data, []byte("/BaseFont /Times-Roman"), []byte("/BaseFont /Times-Roman /FirstChar "+n+" /Widths [500 500]"), 1)
			case "Rotate":
				data = bytes.Replace(data, []byte("/MediaBox"), []byte("/Rotate "+n+" /MediaBox"), 1)
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s %s: panic %v", key, n, r)
					}
				}()
				d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
				if err == nil {
					var pages []*pdfread.Page
					d.ReadPages(context.Background(), func(p *pdfread.Page) error { pages = append(pages, p); return nil })
					pdfread.Analyse(context.Background(), pages, d.Outline(), d.Info)
				}
			}()
		}
	}
}

// The layout's cost does not grow with the product of its parts: a page whose
// lines alternate between full-width and two columns (every one its own
// region), a page of 30,000 pieces, 300 pages of 100 lines.
func TestLayoutCostWithManyRegionsAndLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 12_000; i++ {
		y := 29900 - float64(i)*2.4
		switch i % 3 {
		case 0:
			fmt.Fprintf(&sb, "BT /F1 6 Tf 40 %.2f Td (A heading line that crosses the whole width of the page and the gutter) Tj ET\n", y)
		case 1:
			fmt.Fprintf(&sb, "BT /F1 6 Tf 40 %.2f Td (left column text here) Tj ET\n", y)
		default:
			fmt.Fprintf(&sb, "BT /F1 6 Tf 340 %.2f Td (right column text here) Tj ET\n", y)
		}
	}
	d := pdftest.New()
	ft := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	pg := d.Page(612, 30000, fmt.Sprintf("/Font << /F1 %d 0 R >>", ft), sb.String(), "")
	data := d.Write(d.Pages([]int{pg}, ""), pdftest.Opt{})
	mb, took := peak(func() {
		dd, pages, err := readAll(data, pdfread.Limits{})
		if err != nil {
			t.Error(err)
			return
		}
		if _, err := pdfread.Analyse(context.Background(), pages, dd.Outline(), dd.Info); err != nil {
			t.Error(err)
		}
	})
	t.Logf("12,000 lines in alternating regions on one page: peak %d MB, %v", mb, took)
	if mb > 300*memFactor {
		t.Errorf("peak %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second*memFactor {
			t.Errorf("peak %d MB in %v", mb, took)
		}
	})

	// many pages of many lines, with an outline that points into each
	var content strings.Builder
	for l := 0; l < 100; l++ {
		fmt.Fprintf(&content, "BT /F1 11 Tf 72 %d Td (Line %d of the page, in plain words that run on.) Tj ET\n", 740-7*l, l)
	}
	d2 := pdftest.New()
	f := d2.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	var pgs []int
	for i := 0; i < 300; i++ {
		pgs = append(pgs, d2.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), content.String(), ""))
	}
	ids := make([]int, 300)
	for i := range ids {
		ids[i] = d2.Alloc()
	}
	for i := range ids {
		next := ""
		if i+1 < len(ids) {
			next = fmt.Sprintf("/Next %d 0 R", ids[i+1])
		}
		d2.Set(ids[i], fmt.Sprintf("<< /Title (Chapter %d) %s /Dest [%d 0 R /XYZ 0 770 null] >>", i, next, pgs[i]))
	}
	ol := d2.Add(fmt.Sprintf("<< /Type /Outlines /First %d 0 R >>", ids[0]))
	data2 := d2.Write(d2.Pages(pgs, fmt.Sprintf("/Outlines %d 0 R", ol)), pdftest.Opt{})
	_, took2 := peak(func() {
		dd, pages, err := readAll(data2, pdfread.Limits{})
		if err != nil {
			t.Error(err)
			return
		}
		pdfread.Analyse(context.Background(), pages, dd.Outline(), dd.Info)
	})
	t.Logf("300 pages of 100 lines with 300 bookmarks: %v", took2)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took2 > 10*time.Second*memFactor {
			t.Errorf("took %v", took2)
		}
	})
}

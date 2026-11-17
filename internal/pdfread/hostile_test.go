package pdfread_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pdfread"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
	"github.com/crueber/scriobh/internal/testutil"
)

// Each limit has a file that hits it, and the reader neither runs away nor
// fails to say so. memory is the allocation the whole read caused.

func measure(t *testing.T, f func()) (mb uint64, took time.Duration) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	took = time.Since(start)
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) >> 20, took
}

// readAll opens and reads every page, returning the error of either step.
func readAll(data []byte, lim pdfread.Limits) (*pdfread.Doc, []*pdfread.Page, error) {
	d, err := pdfread.Open(context.Background(), data, lim)
	if err != nil {
		return nil, nil, err
	}
	var pages []*pdfread.Page
	err = d.ReadPages(context.Background(), func(p *pdfread.Page) error { pages = append(pages, p); return nil })
	return d, pages, err
}

func oneText(d *pdftest.Doc, content string, filter string) (int, int) {
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), content, filter)
	return f, pg
}

func TestAFlateBombIsRefusedByRatioWithABoundedPeak(t *testing.T) {
	// 200 MiB of zeros in about 200 KB
	var buf bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	zeros := make([]byte, 1<<20)
	for i := 0; i < 200; i++ {
		zw.Write(zeros)
	}
	zw.Close()
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	cs := d.Alloc()
	d.Raw(cs, append([]byte(fmt.Sprintf("<< /Filter /FlateDecode /Length %d >>\nstream\n", buf.Len())), append(buf.Bytes(), []byte("\nendstream")...)...))
	pg := d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", f, cs))
	// and a second, readable page after it
	_, pg2 := oneText(d, "BT /F1 12 Tf 72 700 Td (Still here) Tj ET", "")
	root := d.Pages([]int{pg, pg2}, "")
	data := d.Write(root, pdftest.Opt{})
	t.Logf("the file is %d KB; it would inflate to 200 MB", len(data)>>10)
	var pages []*pdfread.Page
	mb, took := measure(t, func() {
		var err error
		_, pages, err = readAll(data, pdfread.Limits{})
		if err != nil {
			t.Error(err)
		}
	})
	if len(pages) != 2 || pageText(pages[1]) != "Still here" || len(pages[0].Frags) != 0 {
		t.Errorf("pages: %d", len(pages))
	}
	if mb > 40*memFactor {
		t.Errorf("a flate bomb allocated %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second*memFactor {
			t.Errorf("a flate bomb allocated %d MB in %v", mb, took)
		}
	})
	t.Logf("flate bomb: %d MB allocated, %v", mb, took)
}

func TestTheInflateBudgetOfAWholeFileIsShared(t *testing.T) {
	// many streams, each under every per-stream limit, together over the file's
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	var tb strings.Builder
	for i := 0; i < 30_000; i++ {
		fmt.Fprintf(&tb, "BT /F1 12 Tf 72 %d Td (w%d) Tj ET\n", 100+i%600, i*7919%100003)
	}
	text := []byte(tb.String()) // about 1 MB, compresses about 5 to 1
	var pgs []int
	for i := 0; i < 120; i++ {
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), string(text), "Flate"))
	}
	root := d.Pages(pgs, "")
	lim := pdfread.Limits{InflateBytes: 20 << 20}
	_, pages, err := readAll(d.Write(root, pdftest.Opt{}), lim)
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, p := range pages {
		if len(p.Frags) > 0 {
			read++
		}
	}
	if read == 0 || read >= 120 || len(pages) != 120 {
		t.Errorf("%d of 120 pages were read with a 20 MB budget", read)
	}
}

func TestObjectLoopsEnd(t *testing.T) {
	d := pdftest.New()
	a, b := d.Alloc(), d.Alloc()
	d.Set(a, fmt.Sprintf("%d 0 R", b))
	d.Set(b, fmt.Sprintf("%d 0 R", a))
	f, pg := oneText(d, "BT /F1 12 Tf 72 700 Td (Fine) Tj ET", "")
	_ = f
	// a stream whose Length is itself
	self := d.Alloc()
	d.Raw(self, []byte(fmt.Sprintf("<< /Length %d 0 R >>\nstream\nabc\nendstream", self)))
	// a page tree node that is its own kid
	pn := d.Alloc()
	d.Set(pn, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 1 >>", pn, pg))
	d.Raw(pg, bytes.Replace(d.RawObj(pg), []byte("/Parent 0 0 R"), []byte(fmt.Sprintf("/Parent %d 0 R /Loop %d 0 R /X %d 0 R", pn, a, self)), 1))
	cat := d.Add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R /Dests %d 0 R >>", pn, a))
	_, pages, err := readAll(d.Write(cat, pdftest.Opt{}), pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 1 || pageText(pages[0]) != "Fine" {
		t.Errorf("pages %d", len(pages))
	}
	dd, _, _ := readAll(d.Write(cat, pdftest.Opt{}), pdfread.Limits{})
	if !strings.Contains(dd.Info.PageTreeProblem, "itself") {
		t.Errorf("the loop was not reported: %q", dd.Info.PageTreeProblem)
	}
}

func TestDeepNestingDoesNotOverflowTheStack(t *testing.T) {
	d := pdftest.New()
	_, pg := oneText(d, "BT /F1 12 Tf 72 700 Td (Fine) Tj ET", "")
	deep := d.Alloc()
	d.Raw(deep, []byte(strings.Repeat("[", 2_000_000)))
	deepd := d.Alloc()
	d.Raw(deepd, []byte(strings.Repeat("<< /A ", 1_000_000)))
	root := d.Pages([]int{pg}, fmt.Sprintf("/X %d 0 R /Y %d 0 R", deep, deepd))
	mb, took := measure(t, func() {
		_, pages, err := readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{})
		if err != nil || len(pages) != 1 || pageText(pages[0]) != "Fine" {
			t.Errorf("err %v", err)
		}
	})
	if mb > 50 {
		t.Errorf("deep nesting: %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 3*time.Second {
			t.Errorf("deep nesting: %d MB in %v", mb, took)
		}
	})
	// a page tree chain 100,000 deep
	d2 := pdftest.New()
	_, leaf := oneText(d2, "BT /F1 12 Tf 72 700 Td (Leaf) Tj ET", "")
	prev := leaf
	d2.Raw(leaf, bytes.Replace(d2.RawObj(leaf), []byte("/Parent 0 0 R"), []byte("/Parent 1 0 R"), 1))
	for i := 0; i < 100_000; i++ {
		n := d2.Add(fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", prev))
		prev = n
	}
	cat := d2.Add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", prev))
	dd, pages, err := readAll(d2.Write(cat, pdftest.Opt{}), pdfread.Limits{})
	if err != nil && !strings.Contains(err.Error(), "pages") {
		t.Logf("a deep page tree: %v", err)
	}
	if dd != nil && len(pages) != 0 {
		t.Errorf("a tree 100,000 deep gave %d pages", len(pages))
	}
}

func TestAbsurdPageCountsAreCapped(t *testing.T) {
	d := pdftest.New()
	_, pg := oneText(d, "BT /F1 12 Tf 72 700 Td (p) Tj ET", "")
	var kids strings.Builder
	for i := 0; i < 50_000; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", pg)
	}
	pn := d.Add(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count 50000 >>", kids.String()))
	d.Raw(pg, bytes.Replace(d.RawObj(pg), []byte("/Parent 0 0 R"), []byte(fmt.Sprintf("/Parent %d 0 R", pn)), 1))
	cat := d.Add(fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pn))
	var dd *pdfread.Doc
	var pages []*pdfread.Page
	mb, took := measure(t, func() {
		var err error
		dd, pages, err = readAll(d.Write(cat, pdftest.Opt{}), pdfread.Limits{})
		if err != nil {
			t.Error(err)
		}
	})
	if len(pages) != pdfread.DefaultLimits.Pages || !strings.Contains(dd.Info.PageTreeProblem, "import limit") {
		t.Errorf("%d pages, problem %q", len(pages), dd.Info.PageTreeProblem)
	}
	if mb > 100 {
		t.Errorf("50,000 page references: %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second {
			t.Errorf("50,000 page references: %d MB in %v", mb, took)
		}
	})
}

func TestAFontWithAGiantCMapIsCut(t *testing.T) {
	d := pdftest.New()
	var tu strings.Builder
	tu.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
	for i := 0; i < 400; i++ {
		tu.WriteString("1 beginbfrange <0000> <FFFF> <0041> endbfrange\n")
	}
	for i := 0; i < 200_000; i++ {
		fmt.Fprintf(&tu, "1 beginbfchar <%04X> <0042> endbfchar\n", i%65536)
	}
	tu.WriteString("endcmap end end")
	tuo := d.Stream("", []byte(tu.String()), "Flate")
	cid := d.Add("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /DW 500 >>")
	f := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding /Identity-H /DescendantFonts [%d 0 R] /ToUnicode %d 0 R >>", cid, tuo))
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), "BT /F1 12 Tf 72 700 Td <0001> Tj ET", "")
	root := d.Pages([]int{pg}, "")
	mb, took := measure(t, func() {
		_, pages, err := readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{})
		if err != nil || !pages[0].FontsDamaged {
			t.Errorf("err %v damaged %v", err, pages[0].FontsDamaged)
		}
	})
	if mb > 60 {
		t.Errorf("giant cmap: %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second {
			t.Errorf("giant cmap: %d MB in %v", mb, took)
		}
	})
}

func TestMalformedCrossReferenceIsRebuiltWithinALimit(t *testing.T) {
	d := pdftest.New()
	f, _ := oneText(d, "", "")
	_ = f
	var pgs []int
	for i := 0; i < 5; i++ {
		_, p := oneText(d, fmt.Sprintf("BT /F1 12 Tf 72 700 Td (Page %d) Tj ET", i+1), "")
		pgs = append(pgs, p)
	}
	root := d.Pages(pgs, "")
	data := d.Write(root, pdftest.Opt{})
	// a table that claims a billion entries, then one whose Prev points at itself
	bad := bytes.Replace(data, []byte("xref\n0 "), []byte("xref\n0 1000000000 "), 1)
	dd, pages, err := readAll(bad, pdfread.Limits{})
	if err != nil || len(pages) != 5 || !dd.Info.RebuiltXref {
		t.Errorf("a billion entries: err %v pages %d", err, len(pages))
	}
	loop := bytes.Replace(data, []byte("/Size "), []byte("/Prev 0 /Size "), 1)
	if _, _, err := readAll(loop, pdfread.Limits{}); err != nil {
		t.Errorf("prev loop: %v", err)
	}
	// the rebuild is bounded: a file of nothing but "1 0 obj" markers
	flood := []byte("%PDF-1.4\n" + strings.Repeat("1 0 obj\n", 3_000_000))
	mb, took := measure(t, func() {
		_, _, err = readAll(flood, pdfread.Limits{RebuildObjects: 1000})
	})
	if err == nil {
		t.Error("a file of object markers was read")
	}
	if mb > 80 {
		t.Errorf("obj flood: %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second {
			t.Errorf("obj flood: %d MB in %v", mb, took)
		}
	})
}

func TestAnOperatorFloodStopsAtTheLimit(t *testing.T) {
	d := pdftest.New()
	content := "BT /F1 12 Tf 72 700 Td (first) Tj ET\n" + strings.Repeat("q Q ", 3_000_000)
	_, pg := oneText(d, content, "")
	_, pg2 := oneText(d, "BT /F1 12 Tf 72 700 Td (second page) Tj ET", "")
	root := d.Pages([]int{pg, pg2}, "")
	var pages []*pdfread.Page
	mb, took := measure(t, func() {
		var err error
		_, pages, err = readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{OpsPerPage: 100_000})
		if err != nil {
			t.Error(err)
		}
	})
	if len(pages) != 2 || !pages[0].Truncated || pageText(pages[0]) != "first" || pageText(pages[1]) != "second page" {
		t.Errorf("truncated %v", pages[0].Truncated)
	}
	if mb > 100 {
		t.Errorf("operator flood: %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second {
			t.Errorf("operator flood: %d MB in %v", mb, took)
		}
	})
	// the whole file has a budget too
	_, _, err := readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{OpsPerPage: 1_000_000, OpsTotal: 500_000})
	if err == nil {
		t.Error("a file over the total operator budget was read")
	}
}

func TestFormsThatDrawThemselvesEnd(t *testing.T) {
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	a, b := d.Alloc(), d.Alloc()
	res := fmt.Sprintf("/Font << /F1 %d 0 R >> /XObject << /A %d 0 R /B %d 0 R >>", f, a, b)
	d.SetStream(a, "/Type /XObject /Subtype /Form /BBox [0 0 1 1] /Resources << "+res+" >>", []byte("BT /F1 12 Tf 72 700 Td (A) Tj ET /B Do /B Do"), "")
	d.SetStream(b, "/Type /XObject /Subtype /Form /BBox [0 0 1 1] /Resources << "+res+" >>", []byte("/A Do /A Do"), "")
	pg := d.Page(612, 792, res, "/A Do", "")
	root := d.Pages([]int{pg}, "")
	mb, took := measure(t, func() {
		_, pages, err := readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{})
		if err != nil || len(pages) != 1 {
			t.Errorf("err %v", err)
		}
	})
	if mb > 50 {
		t.Errorf("mutual forms: %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 3*time.Second {
			t.Errorf("mutual forms: %d MB in %v", mb, took)
		}
	})
}

func TestObjectStreamWithAHugeCountIsBounded(t *testing.T) {
	d := pdftest.New()
	_, pg := oneText(d, "BT /F1 12 Tf 72 700 Td (Fine) Tj ET", "")
	root := d.Pages([]int{pg}, "")
	os := d.Alloc()
	d.SetStream(os, "/Type /ObjStm /N 2000000000 /First 5", []byte("1 0 2 0 3 0"), "Flate")
	mb, _ := measure(t, func() {
		_, pages, err := readAll(d.Write(root, pdftest.Opt{XrefStream: true}), pdfread.Limits{})
		if err != nil || len(pages) != 1 {
			t.Errorf("err %v", err)
		}
	})
	if mb > 50 {
		t.Errorf("%d MB", mb)
	}
}

func TestTextLimitStopsAHugeTextLayer(t *testing.T) {
	d := pdftest.New()
	var pgs []int
	content := "BT /F1 12 Tf 72 700 Td (" + strings.Repeat("word ", 4000) + ") Tj ET"
	for i := 0; i < 40; i++ {
		_, p := oneText(d, content, "Flate")
		pgs = append(pgs, p)
	}
	root := d.Pages(pgs, "")
	_, pages, err := readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{TextBytes: 100_000})
	if err != nil {
		t.Fatal(err)
	}
	trunc := 0
	for _, p := range pages {
		if p.Truncated {
			trunc++
		}
	}
	if trunc == 0 {
		t.Error("the text limit was not reached")
	}
}

func TestACancelledReadStops(t *testing.T) {
	d := pdftest.New()
	var pgs []int
	for i := 0; i < 50; i++ {
		_, p := oneText(d, "BT /F1 12 Tf 72 700 Td (page) Tj ET", "")
		pgs = append(pgs, p)
	}
	root := d.Pages(pgs, "")
	data := d.Write(root, pdftest.Opt{})
	ctx, cancel := context.WithCancel(context.Background())
	dd, err := pdfread.Open(ctx, data, pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	err = dd.ReadPages(ctx, func(*pdfread.Page) error {
		if n++; n == 3 {
			cancel()
		}
		return nil
	})
	if err == nil || n > 4 {
		t.Errorf("err %v after %d pages", err, n)
	}
	// and a time limit
	dd2, _ := pdfread.Open(context.Background(), data, pdfread.Limits{Time: time.Nanosecond})
	time.Sleep(time.Millisecond)
	if err := dd2.ReadPages(context.Background(), func(*pdfread.Page) error { return nil }); err == nil {
		t.Error("a read over its time limit went on")
	}
}

func TestAFileOverTheSizeLimitIsRefused(t *testing.T) {
	d := pdftest.New()
	_, pg := oneText(d, "BT /F1 12 Tf 72 700 Td (x) Tj ET", "")
	root := d.Pages([]int{pg}, "")
	_, err := pdfread.Open(context.Background(), d.Write(root, pdftest.Opt{}), pdfread.Limits{FileBytes: 200})
	if err != pdfread.ErrTooLarge {
		t.Errorf("%v", err)
	}
}

// Whatever bytes a file holds, reading it neither panics nor hangs.
func TestRandomDamageNeverPanics(t *testing.T) {
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var pgs []int
	for i := 0; i < 4; i++ {
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), fmt.Sprintf("BT /F1 12 Tf 72 700 Td (Page %d here) Tj ET", i), "Flate"))
	}
	root := d.Pages(pgs, "")
	for _, opt := range []pdftest.Opt{{}, {XrefStream: true, ObjStm: true}} {
		data := d.Write(root, opt)
		seed := uint32(12345)
		next := func() uint32 { seed = seed*1664525 + 1013904223; return seed }
		for i := 0; i < 400; i++ {
			b := append([]byte(nil), data...)
			for k := 0; k < 1+int(next()%12); k++ {
				b[int(next())%len(b)] = byte(next())
			}
			if i%7 == 0 {
				b = b[:int(next())%len(b)]
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic on damage %d: %v", i, r)
					}
				}()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				dd, err := pdfread.Open(ctx, b, pdfread.Limits{})
				if err != nil {
					return
				}
				dd.ReadPages(ctx, func(p *pdfread.Page) error { return nil })
				dd.Outline()
			}()
		}
	}
}

func TestOneHugeStringIsReadInBoundedTimeAndMemory(t *testing.T) {
	// 8 MB of letters in one Tj
	var sb strings.Builder
	for i := 0; i < 8<<20; i++ {
		sb.WriteByte(byte('a' + i*7%26))
	}
	d := pdftest.New()
	_, pg := oneText(d, "BT /F1 12 Tf 72 700 Td ("+sb.String()+") Tj ET", "")
	root := d.Pages([]int{pg}, "")
	data := d.Write(root, pdftest.Opt{})
	var res *pdfread.Result
	mb, took := measure(t, func() {
		dd, pages, err := readAll(data, pdfread.Limits{})
		if err != nil {
			t.Error(err)
			return
		}
		res, err = pdfread.Analyse(context.Background(), pages, dd.Outline(), dd.Info)
		if err != nil {
			t.Error(err)
		}
	})
	t.Logf("one 8 MB string: %d MB allocated, %v", mb, took)
	if res == nil || mb > 800*memFactor {
		t.Errorf("%d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 20*time.Second*memFactor {
			t.Errorf("%d MB in %v", mb, took)
		}
	})
}

func TestAPageOfTwoHundredThousandLinesIsCapped(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 200_000; i++ {
		fmt.Fprintf(&sb, "BT /F1 6 Tf 72 %.2f Td (x) Tj ET\n", 780-float64(i)*0.003)
	}
	d := pdftest.New()
	_, pg := oneText(d, sb.String(), "")
	_, pg2 := oneText(d, "BT /F1 12 Tf 72 700 Td (A second page, normal.) Tj ET", "")
	root := d.Pages([]int{pg, pg2}, "")
	data := d.Write(root, pdftest.Opt{})
	var res *pdfread.Result
	mb, took := measure(t, func() {
		dd, pages, err := readAll(data, pdfread.Limits{})
		if err != nil {
			t.Error(err)
			return
		}
		res, err = pdfread.Analyse(context.Background(), pages, dd.Outline(), dd.Info)
		if err != nil {
			t.Error(err)
		}
	})
	t.Logf("200,000 lines on a page: %d MB allocated, %v", mb, took)
	if res == nil || len(res.Report.TruncatedPages) != 1 || mb > 600*memFactor {
		t.Errorf("%d MB in %v; report %+v", mb, took, res)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 30*time.Second*memFactor {
			t.Errorf("%d MB in %v; report %+v", mb, took, res)
		}
	})
}

func TestACorruptStreamSearchHasABudget(t *testing.T) {
	// 24 MiB of "n 0 obj << /Type /XRef >> stream" with no endstream anywhere: each would be searched to the end of the file
	chunk := "0 0 obj\n<< /Type /XRef /W [1 1 1] >>\nstream\nabcdefghij\n"
	data := []byte("%PDF-1.5\n" + strings.Repeat(chunk, (24<<20)/len(chunk)))
	mb, took := measure(t, func() {
		_, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
		if err == nil {
			t.Error("a file of unterminated streams opened")
		}
	})
	t.Logf("unterminated streams: %d MB allocated, %v", mb, took)
	if mb > 200*memFactor {
		t.Errorf("%d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second*memFactor {
			t.Errorf("%d MB in %v", mb, took)
		}
	})
}

func TestTheTimeLimitCoversOpenAndRebuild(t *testing.T) {
	flood := []byte("%PDF-1.4\n" + strings.Repeat("1 0 obj\n<< /A 1 >>\nendobj\n", 2_000_000))
	start := time.Now()
	_, err := pdfread.Open(context.Background(), flood, pdfread.Limits{Time: 20 * time.Millisecond, RebuildObjects: 10_000_000, Objects: 10_000_000})
	if err == nil {
		t.Error("a file that cannot be read opened")
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 5*time.Second*memFactor {
			t.Errorf("took %v with a limit of 20 ms", took)
		}
	})
}

func TestEachRemainingLimitHasATest(t *testing.T) {
	// StreamBytes: a run-length stream that expands past the limit is refused, the page is marked
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	rl := d.Alloc()
	payload := []byte{0xC0, ' '} // 63 spaces per pair
	var big []byte
	for i := 0; i < 20_000; i++ {
		big = append(big, payload...)
	}
	big = append(big, 128)
	d.Raw(rl, append([]byte(fmt.Sprintf("<< /Filter /RunLengthDecode /Length %d >>\nstream\n", len(big))), append(big, []byte("\nendstream")...)...))
	pg := d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", f, rl))
	root := d.Pages([]int{pg}, "")
	_, pages, err := readAll(d.Write(root, pdftest.Opt{}), pdfread.Limits{StreamBytes: 100_000})
	if err != nil || !pages[0].Truncated {
		t.Errorf("StreamBytes: err %v truncated %v", err, pages[0].Truncated)
	}

	// Fonts: past the limit a font is not read, the text in it is not kept, and the page says so
	d2 := pdftest.New()
	var res, content strings.Builder
	for i := 0; i < 12; i++ {
		fo := d2.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
		fmt.Fprintf(&res, "/F%d %d 0 R ", i, fo)
		fmt.Fprintf(&content, "BT /F%d 12 Tf 72 %d Td (font %d) Tj ET\n", i, 700-i*14, i)
	}
	pg2 := d2.Page(612, 792, "/Font << "+res.String()+" >>", content.String(), "")
	_, pages2, err := readAll(d2.Write(d2.Pages([]int{pg2}, ""), pdftest.Opt{}), pdfread.Limits{Fonts: 5})
	if err != nil || len(pages2[0].Frags) != 5 || !pages2[0].Truncated {
		t.Errorf("Fonts: err %v frags %d truncated %v", err, len(pages2[0].Frags), pages2[0].Truncated)
	}

	// OutlineItems
	d3 := pdftest.New()
	f3 := d3.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	pg3 := d3.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f3), "BT /F1 12 Tf 72 700 Td (x) Tj ET", "")
	ids := make([]int, 40)
	for i := range ids {
		ids[i] = d3.Alloc()
	}
	for i := range ids {
		next := ""
		if i+1 < len(ids) {
			next = fmt.Sprintf("/Next %d 0 R", ids[i+1])
		}
		d3.Set(ids[i], fmt.Sprintf("<< /Title (Item %d) %s /Dest [%d 0 R /Fit] >>", i, next, pg3))
	}
	ol := d3.Add(fmt.Sprintf("<< /Type /Outlines /First %d 0 R >>", ids[0]))
	dd, _, _ := readAll(d3.Write(d3.Pages([]int{pg3}, fmt.Sprintf("/Outlines %d 0 R", ol)), pdftest.Opt{}), pdfread.Limits{OutlineItems: 10})
	if got := len(dd.Outline()); got != 10 {
		t.Errorf("OutlineItems: %d items", got)
	}

	// FormDepth: a chain of distinct forms deeper than the limit stops there
	d4 := pdftest.New()
	f4 := d4.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	forms := make([]int, 8)
	for i := range forms {
		forms[i] = d4.Alloc()
	}
	var xo strings.Builder
	for i, n := range forms {
		fmt.Fprintf(&xo, "/X%d %d 0 R ", i, n)
	}
	for i, n := range forms {
		body := fmt.Sprintf("BT /F1 12 Tf 72 %d Td (level %d) Tj ET ", 700-i*14, i)
		if i+1 < len(forms) {
			body += fmt.Sprintf("/X%d Do", i+1)
		}
		d4.SetStream(n, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 1 1] /Resources << /Font << /F1 %d 0 R >> /XObject << %s >> >>", f4, xo.String()), []byte(body), "")
	}
	pg4 := d4.Page(612, 792, "/XObject << "+xo.String()+" >>", "/X0 Do", "")
	_, pages4, err := readAll(d4.Write(d4.Pages([]int{pg4}, ""), pdftest.Opt{}), pdfread.Limits{FormDepth: 3})
	if err != nil || len(pages4[0].Frags) != 3 {
		t.Errorf("FormDepth: err %v frags %d", err, len(pages4[0].Frags))
	}
}

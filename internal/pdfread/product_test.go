package pdfread_test

import (
	"bytes"
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

// Limits that are counts multiply: fonts by the size of a map, pairs by the size
// of an object, pieces by pages, draws by the size of a form. Each product has
// a test here at the size that was found to cost gigabytes, with the peak of the
// heap sampled (not only what was allocated in total).

// peak runs f and returns the highest heap in use during it, above what was in use before, and the time it took.
func peak(f func()) (mb uint64, took time.Duration) {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	base := m.HeapAlloc
	var hi uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var s runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				runtime.ReadMemStats(&s)
				hi = max(hi, s.HeapAlloc)
			}
		}
	}()
	start := time.Now()
	f()
	took = time.Since(start)
	close(stop)
	<-done
	if hi < base {
		return 0, took
	}
	return (hi - base) >> 20, took
}

func TestProductNameTreeFanOut(t *testing.T) {
	var kids strings.Builder
	for i := 0; i < 501; i++ {
		kids.WriteString("5 0 R ")
	}
	data := reviewSkeleton("/Names << /EmbeddedFiles 5 0 R /JavaScript 5 0 R /Dests 5 0 R >> /Outlines 6 0 R", func(b *bytes.Buffer) {
		fmt.Fprintf(b, "5 0 obj\n<< /Kids [%s] >>\nendobj\n6 0 obj\n<< /First 7 0 R >>\nendobj\n7 0 obj\n<< /Title (A) /Dest (x) /Next 7 0 R >>\nendobj\n", kids.String())
	})
	_, took := peak(func() {
		d, err := pdfread.Open(context.Background(), data, pdfread.Limits{Time: time.Second})
		if err == nil {
			d.Outline()
		}
	})
	t.Logf("a %d KB name tree that lists itself 501 times: %v", len(data)>>10, took)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 2*time.Second*memFactor {
			t.Errorf("took %v", took)
		}
	})
}

func TestProductObjectStreamsOfManyPairsAndBigObjects(t *testing.T) {
	const pairs, elems = 800, 1_000_000
	var big strings.Builder
	big.WriteString("[")
	for i := 0; i < elems; i++ {
		big.WriteString("0 ")
	}
	big.WriteString("]")
	var hdr strings.Builder
	for i := 0; i < pairs; i++ {
		fmt.Fprintf(&hdr, "%d 0 ", 100+i)
	}
	stm := hdr.String() + big.String()
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), "BT /F1 12 Tf 72 700 Td (x) Tj ET", "")
	root := d.Pages([]int{pg}, "")
	d.Alloc()
	d.SetStream(50, fmt.Sprintf("/Type /ObjStm /N %d /First %d", pairs, hdr.Len()), []byte(stm), "")
	data := d.Write(root, pdftest.Opt{NoXref: true})
	var err error
	mb, took := peak(func() {
		var dd *pdfread.Doc
		dd, err = pdfread.Open(context.Background(), data, pdfread.Limits{})
		if err == nil {
			dd.ReadPages(context.Background(), func(*pdfread.Page) error { return nil })
		}
	})
	t.Logf("%d pairs at one %d-element array, %d KB file: peak %d MB, %v", pairs, elems, len(data)>>10, mb, took)
	if mb > 150*memFactor {
		t.Errorf("peak %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second*memFactor {
			t.Errorf("peak %d MB in %v", mb, took)
		}
	})
}

// Many object streams of distinct big arrays: the parse budget of the file bounds what they keep.
func TestProductManyObjectStreamsOfDistinctArrays(t *testing.T) {
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), "BT /F1 12 Tf 72 700 Td (x) Tj ET", "")
	root := d.Pages([]int{pg}, "")
	arr := "[" + strings.Repeat("0 ", 250_000) + "]"
	for i := 0; i < 60; i++ {
		n := d.Alloc()
		d.SetStream(n, fmt.Sprintf("/Type /ObjStm /N 1 /First %d", len(fmt.Sprintf("%d 0 ", 1000+i))), []byte(fmt.Sprintf("%d 0 %s", 1000+i, arr)), "Flate")
	}
	data := d.Write(root, pdftest.Opt{NoXref: true})
	mb, took := peak(func() {
		dd, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
		if err == nil {
			dd.ReadPages(context.Background(), func(*pdfread.Page) error { return nil })
		}
	})
	t.Logf("60 object streams of a 250,000-element array each, %d KB file: peak %d MB, %v", len(data)>>10, mb, took)
	if mb > 700*memFactor {
		t.Errorf("peak %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 15*time.Second*memFactor {
			t.Errorf("peak %d MB in %v", mb, took)
		}
	})
}

func TestProductManyFontsAndABigToUnicode(t *testing.T) {
	const fonts = 400
	cm := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <00> <FF> endcodespacerange\n2 beginbfrange\n<0000> <FFFF> <0041>\n<00010000> <00018000> <0041>\nendbfrange\nendcmap end end"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	var res, content strings.Builder
	for i := 0; i < fonts; i++ {
		fmt.Fprintf(&res, "/F%d %d 0 R ", i, 100+i)
		fmt.Fprintf(&content, "BT /F%d 12 Tf 72 %d Td (hello) Tj ET\n", i, 780-i%60*12)
	}
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /Font << %s >> >> /Contents 4 0 R >>\nendobj\n", res.String())
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", content.Len(), content.String())
	// each font names its own copy of the map, and a different one: nothing is shared
	for i := 0; i < fonts; i++ {
		c := cm + strings.Repeat(" ", i)
		fmt.Fprintf(&b, "%d 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", 1000+i, len(c), c)
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /ToUnicode %d 0 R >>\nendobj\n", 100+i, 1000+i)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n")
	mb, took := peak(func() {
		d, err := pdfread.Open(context.Background(), b.Bytes(), pdfread.Limits{})
		if err != nil {
			t.Error(err)
			return
		}
		d.ReadPages(context.Background(), func(*pdfread.Page) error { return nil })
	})
	t.Logf("%d fonts with a map of 100,000 entries each, %d KB file: peak %d MB, %v", fonts, b.Len()>>10, mb, took)
	if mb > 200*memFactor {
		t.Errorf("peak %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second*memFactor {
			t.Errorf("peak %d MB in %v", mb, took)
		}
	})
}

func TestProductDirectFontDictionariesAreCountedToo(t *testing.T) {
	var res, content strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&res, "/F%d << /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >> ", i)
		fmt.Fprintf(&content, "BT /F%d 12 Tf 72 %d Td (x) Tj ET\n", i, 780-i%60*12)
	}
	d := pdftest.New()
	pg := d.Page(612, 792, "/Font << "+res.String()+" >>", content.String(), "")
	data := d.Write(d.Pages([]int{pg}, ""), pdftest.Opt{})
	_, pages, err := readAll(data, pdfread.Limits{Fonts: 20})
	if err != nil || len(pages[0].Frags) != 20 || !pages[0].Truncated {
		t.Errorf("err %v frags %d truncated %v", err, len(pages[0].Frags), pages[0].Truncated)
	}
}

func TestProductPagesTimesPieces(t *testing.T) {
	// 1300 pages share one stream of 9,400 one-letter pieces (a gap between each letter)
	var sb strings.Builder
	sb.WriteString("BT /F1 8 Tf 72 700 Td [")
	for i := 0; i < 9400; i++ {
		sb.WriteString("(a) -2000 ")
	}
	sb.WriteString("] TJ ET")
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	cs := d.Stream("", []byte(sb.String()), "Flate")
	var pgs []int
	for i := 0; i < 1300; i++ {
		pgs = append(pgs, d.Add(fmt.Sprintf("<< /Type /Page /Parent 0 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", f, cs)))
	}
	data := d.Write(d.Pages(pgs, ""), pdftest.Opt{})
	var res *pdfread.Result
	mb, took := peak(func() {
		dd, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
		if err != nil {
			t.Error(err)
			return
		}
		var pages []*pdfread.Page
		dd.ReadPages(context.Background(), func(p *pdfread.Page) error { pages = append(pages, p); return nil })
		res, _ = pdfread.Analyse(context.Background(), pages, nil, dd.Info)
	})
	t.Logf("1300 pages x 9,400 pieces, %d KB file: peak %d MB, %v", len(data)>>10, mb, took)
	if mb > 700*memFactor {
		t.Errorf("peak %d MB in %v (result %v)", mb, took, res != nil)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 30*time.Second*memFactor {
			t.Errorf("peak %d MB in %v (result %v)", mb, took, res != nil)
		}
	})
}

func TestProductDrawsTimesFormSize(t *testing.T) {
	form := strings.Repeat("0 ", 500_000) // 1 MB, operands only
	content := strings.Repeat("/X0 Do ", 20_000)
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /XObject << /X0 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /XObject /Subtype /Form /BBox [0 0 1 1] /Length %d >>\nstream\n%s\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n", len(form), form)
	mb, took := peak(func() {
		d, err := pdfread.Open(context.Background(), b.Bytes(), pdfread.Limits{Time: time.Second})
		if err == nil {
			d.ReadPages(context.Background(), func(*pdfread.Page) error { return nil })
		}
	})
	t.Logf("20,000 draws of a 1 MB form, %d KB file: peak %d MB, %v", b.Len()>>10, mb, took)
	if mb > 100*memFactor {
		t.Errorf("peak %d MB in %v", mb, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 3*time.Second*memFactor {
			t.Errorf("peak %d MB in %v", mb, took)
		}
	})
}

// A character map with 100,000 ranges, and a long string through a font that uses it:
// the lookup is a search, not a scan for every glyph.
func TestProductBigEncodingCMapAndALongString(t *testing.T) {
	var cm strings.Builder
	cm.WriteString("/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <0000> <FFFF> endcodespacerange\n")
	for i := 0; i < 100_000; i += 100 {
		fmt.Fprintf(&cm, "100 begincidrange\n")
		for k := 0; k < 100; k++ {
			fmt.Fprintf(&cm, "<%04X> <%04X> %d\n", (i+k)%65536, (i+k)%65536, i+k)
		}
		cm.WriteString("endcidrange\n")
	}
	cm.WriteString("endcmap end end")
	d := pdftest.New()
	enc := d.Stream("", []byte(cm.String()), "Flate")
	cid := d.Add("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /X /DW 500 /W [0 [500]] >>")
	f := d.Add(fmt.Sprintf("<< /Type /Font /Subtype /Type0 /BaseFont /X /Encoding %d 0 R /DescendantFonts [%d 0 R] >>", enc, cid))
	var s strings.Builder
	s.WriteString("BT /F1 8 Tf 72 700 Td <")
	for i := 0; i < 500_000; i++ {
		fmt.Fprintf(&s, "%04X", (i*37)%65536)
	}
	s.WriteString("> Tj ET")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), s.String(), "")
	data := d.Write(d.Pages([]int{pg}, ""), pdftest.Opt{})
	_, took := peak(func() { readAll(data, pdfread.Limits{}) })
	t.Logf("500,000 glyphs through 100,000 cid ranges: %v", took)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second*memFactor {
			t.Errorf("took %v", took)
		}
	})
}

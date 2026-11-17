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

// Review tests for the hostile-input findings. Each fails by assertion at a
// small size. The two that hang are run in a goroutine and abandoned, so run
// them one at a time: go test ./internal/pdfread -run 'TestReview...' .

func reviewSkeleton(catExtra string, extra func(b *bytes.Buffer)) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	fmt.Fprintf(&b, "1 0 obj\n<< /Type /Catalog /Pages 2 0 R %s >>\nendobj\n", catExtra)
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >> >> >> /Contents 4 0 R >>\nendobj\n")
	c := "BT /F1 12 Tf 72 700 Td (hello world this is text) Tj ET"
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(c), c)
	if extra != nil {
		extra(&b)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n")
	return b.Bytes()
}

// returnsWithin runs f and fails if it has not returned after d. A stuck f is abandoned.
func returnsWithin(t *testing.T, d time.Duration, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v although the read had a time limit and a context", what, d)
	}
}

// countNameTree (pages.go) has no visited set and no cancellation: a name tree
// node that lists itself 501 times is 501^8 calls.
func TestReviewNameTreeFanOutHonoursTheTimeLimit(t *testing.T) {
	var kids strings.Builder
	for i := 0; i < 501; i++ {
		kids.WriteString("5 0 R ")
	}
	data := reviewSkeleton("/Names << /EmbeddedFiles 5 0 R >>", func(b *bytes.Buffer) {
		fmt.Fprintf(b, "5 0 obj\n<< /Kids [%s] >>\nendobj\n", kids.String())
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	returnsWithin(t, 3*time.Second, "Open", func() {
		pdfread.Open(ctx, data, pdfread.Limits{Time: 300 * time.Millisecond})
	})
}

// treeLookup (outline.go): a named destination looked up in a Names tree whose
// node lists itself, reached from any outline item.
func TestReviewNamedDestinationTreeFanOutHonoursTheTimeLimit(t *testing.T) {
	var kids strings.Builder
	for i := 0; i < 1000; i++ {
		kids.WriteString("6 0 R ")
	}
	data := reviewSkeleton("/Outlines 5 0 R /Names << /Dests 6 0 R >>", func(b *bytes.Buffer) {
		b.WriteString("5 0 obj\n<< /First 7 0 R >>\nendobj\n")
		fmt.Fprintf(b, "6 0 obj\n<< /Kids [%s] >>\nendobj\n", kids.String())
		b.WriteString("7 0 obj\n<< /Title (A) /Dest (nothere) >>\nendobj\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	returnsWithin(t, 3*time.Second, "Outline", func() {
		d, err := pdfread.Open(ctx, data, pdfread.Limits{Time: 300 * time.Millisecond})
		if err == nil {
			d.Outline()
		}
	})
}

func heapAfterGC() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// loadObjStm (file.go): every (number, offset) pair is parsed again from its own
// offset and kept, so pairs that all point at one large object keep N copies.
func TestReviewObjectStreamPairsPointingAtOneBigObjectAreNotMultiplied(t *testing.T) {
	const pairs, elems = 40, 200_000
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
	data := reviewSkeleton("", func(b *bytes.Buffer) {
		fmt.Fprintf(b, "50 0 obj\n<< /Type /ObjStm /N %d /First %d /Length %d >>\nstream\n%s\nendstream\nendobj\n", pairs, hdr.Len(), len(stm), stm)
	})
	before := heapAfterGC()
	d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
	if err != nil {
		t.Skip(err)
	}
	held := int64(heapAfterGC()) - int64(before)
	runtime.KeepAlive(d)
	if held > 48<<20 {
		t.Errorf("a %d KB file holds %d MB after Open (%d pairs of one %d-element array); the cost grows with pairs x object size", len(data)>>10, held>>20, pairs, elems)
	}
}

// loadFont (font.go) parses a font's ToUnicode into a map of up to
// CMapEntries entries for every font object, and nothing shares a stream's
// parsed map between fonts. Fonts is 3000.
func TestReviewManyFontsSharingOneBigToUnicodeDoNotMultiplyItsMap(t *testing.T) {
	const fonts = 20
	cm := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <00> <FF> endcodespacerange\n2 beginbfrange\n<0000> <FFFF> <0041>\n<00010000> <00018000> <0041>\nendbfrange\nendcmap end end"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	var res, content strings.Builder
	for i := 0; i < fonts; i++ {
		fmt.Fprintf(&res, "/F%d %d 0 R ", i, 100+i)
		fmt.Fprintf(&content, "BT /F%d 12 Tf 72 %d Td (hello) Tj ET\n", i, 700-i*12)
	}
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /Font << %s >> >> /Contents 4 0 R >>\nendobj\n", res.String())
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", content.Len(), content.String())
	fmt.Fprintf(&b, "5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(cm), cm)
	for i := 0; i < fonts; i++ {
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /ToUnicode 5 0 R >>\nendobj\n", 100+i)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n")
	before := heapAfterGC()
	d, err := pdfread.Open(context.Background(), b.Bytes(), pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.ReadPages(context.Background(), func(*pdfread.Page) error { return nil }); err != nil {
		t.Fatal(err)
	}
	held := int64(heapAfterGC()) - int64(before)
	runtime.KeepAlive(d)
	if held > 24<<20 {
		t.Errorf("%d fonts in a %d KB file hold %d MB (about %d MB each); at the limit of 3000 fonts that is %d GB", fonts, b.Len()>>10, held>>20, held>>20/fonts, held*3000/fonts>>30)
	}
}

// readStream (file.go) resolves /Length through other streams: a chain of
// streams whose /Length is the next stream recurses once per link, and the
// object-count limit is the only bound.
func TestReviewLengthChainDoesNotRecurseOncePerLink(t *testing.T) {
	const n = 200_000
	data := reviewSkeleton("", func(b *bytes.Buffer) {
		for i := 0; i < n; i++ {
			fmt.Fprintf(b, "%d 0 obj<</Length %d 0 R>>stream\n", 100000+i, 100001+i)
		}
	})
	data = bytes.Replace(data, []byte("/Contents 4 0 R"), []byte("/Contents 100000 0 R"), 1)
	var stack uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.StackInuse > stack {
					stack = m.StackInuse
				}
			}
		}
	}()
	d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
	if err == nil {
		d.ReadPages(context.Background(), func(*pdfread.Page) error { return nil })
	}
	close(stop)
	<-done // the sampler has stopped: stack is read after, not while it is written
	if stack > 64<<20 {
		t.Errorf("a %d KB file made the goroutine stack grow to %d MB (a 32 MiB file reaches 512 MB, and a -race build dies with 'stack overflow')", len(data)>>10, stack>>20)
	}
}

// The in-page check runs every 16,384 operators, and an operand-only form costs
// the same to lex however it is drawn: the 90 s limit is not a bound.
func TestReviewDeadlineIsCheckedInsideAPageThatDrawsAFormManyTimes(t *testing.T) {
	form := strings.Repeat("0 ", 500_000)
	content := strings.Repeat("/X0 Do ", 600)
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /XObject << /X0 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)
	fmt.Fprintf(&b, "5 0 obj\n<< /Type /XObject /Subtype /Form /BBox [0 0 1 1] /Length %d >>\nstream\n%s\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n", len(form), form)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	d, err := pdfread.Open(ctx, b.Bytes(), pdfread.Limits{Time: 100 * time.Millisecond})
	if err == nil {
		d.ReadPages(ctx, func(*pdfread.Page) error { return nil })
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 1500*time.Millisecond {
			t.Errorf("a 100 ms limit let the read run %v (600 draws of a 1 MB form; 20,000 draws took 109 s with a 1 s limit)", took.Round(time.Millisecond))
		}
	})
}

// A damaged startxref sends an xref-stream file to the rebuild, whose "peek
// 400 bytes for /XRef" takes an earlier object as the trailer; /Encrypt is lost.
func TestReviewEncryptedXrefStreamFileWithDamagedStartxrefIsStillRefused(t *testing.T) {
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
	pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), "BT /F1 12 Tf 72 700 Td (Fine) Tj ET", "")
	root := d.Pages([]int{pg}, "")
	data := d.Write(root, pdftest.Opt{XrefStream: true, Trailer: "/Encrypt << /Filter /Standard /V 1 /R 2 >>", Truncate: 20})
	if _, err := pdfread.Open(context.Background(), data, pdfread.Limits{}); err != pdfread.ErrEncrypted {
		t.Errorf("an encrypted file whose startxref is damaged opened as an ordinary file (err %v)", err)
	}
	_ = zlib.BestSpeed
}

// readStream (file.go:361): int(float64) saturates on arm64, so p+n wraps.
func TestReviewHugeStreamLengthIsNotAPanic(t *testing.T) {
	data := reviewSkeleton("", nil)
	c := "BT /F1 12 Tf 72 700 Td (hello world this is text) Tj ET"
	data = bytes.Replace(data, []byte(fmt.Sprintf("/Length %d", len(c))), []byte("/Length 9223372036854775807"), 1)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Open or ReadPages panicked: %v", r)
		}
	}()
	d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
	if err == nil {
		d.ReadPages(context.Background(), func(*pdfread.Page) error { return nil })
	}
}

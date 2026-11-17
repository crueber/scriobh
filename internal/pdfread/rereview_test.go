package pdfread_test

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pdfread"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// Re-review of the fix round. Each test fails by assertion at a small size on the
// code as it stands. Helpers (reviewSkeleton, returnsWithin, heapAfterGC) are those
// of review_hostile_test.go. The two slow ones (the trailer scan, the piece flood)
// are skipped with -short.

// rebuild's trailer scan (file.go, the loop over "trailer") parses from every
// occurrence of the keyword to the end of the data, and resumes right after the
// keyword, not after what it parsed: "trailer(" repeated is quadratic, and the loop
// asks neither the clock nor the context. 64 KB takes 0.4 s, 256 KB 6.7 s, 1 MB
// does not end in 60 s, whatever the time limit.
func TestRereviewTrailerScanHonoursTheTimeLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("abandons a spinning goroutine for a few seconds")
	}
	data := []byte("%PDF-1.7\n" + strings.Repeat("trailer(", (160<<10)/8))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	returnsWithin(t, 1500*time.Millisecond, "Open of 160 KB of trailer(", func() {
		pdfread.Open(ctx, data, pdfread.Limits{Time: 100 * time.Millisecond})
	})
	_ = start
}

// parseCMap keeps one string per code, and an entry costs 1 of the 100,000 whatever
// its length: <0000> <FFFF> <a 1000-rune string> is 65,536 strings of 3 KB from a
// 2 KB file. At 100,000 runes it is about 25 GB from a 400 KB file (extrapolated;
// 8,000 runes measured 2.0 GB from a 31 KB file, 20,000 runes 5.3 GB while a 1 s
// time limit was ignored).
func TestRereviewBfrangeDestinationLengthIsCharged(t *testing.T) {
	hex := strings.Repeat("4E00", 500)
	cm := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <00> <FF> endcodespacerange\n1 beginbfrange\n<0000> <FFFF> <" + hex + ">\nendbfrange\nendcmap end end"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	c := "BT /F1 12 Tf 72 700 Td (a) Tj ET"
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(c), c)
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /ToUnicode 6 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "6 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n", len(cm), cm)
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
		t.Errorf("a %d KB file holds %d MB after reading one font: a character map entry is charged 1 whatever the length of its text", b.Len()>>10, held>>20)
	}
}

// A chain of object streams whose /Filter is an object held in the next one:
// Object -> loadObjStm -> Decode -> filterList -> Resolve -> Object, once per link,
// and the file size and the 16 MiB parse budget are all that bound it. At 58 bytes
// a link (this generator) the stack grows by about 1.3 KB a link: 380,000 links
// (22 MB) make a 512 MB stack, and 420,000 links (24.6 MB, under the 32 MiB limit)
// die with "runtime: goroutine stack exceeds 1000000000-byte limit / fatal error:
// stack overflow" in the ordinary build, arm64 and amd64 alike; no recover catches
// it, so one upload ends the server process. The /Length chain of the first review
// was fixed; this is the same recursion through another door. (This test is a depth
// test, so it is quick and does not itself overflow.)
func TestRereviewFilterChainOfObjectStreamsDoesNotRecurseOncePerLink(t *testing.T) {
	const n = 40_000
	var b bytes.Buffer
	type ent struct {
		typ byte
		a   uint32
	}
	ents := map[int]ent{}
	b.WriteString("%PDF-1.7\n")
	ents[1] = ent{1, uint32(b.Len())}
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	ents[2] = ent{1, uint32(b.Len())}
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	ents[3] = ent{1, uint32(b.Len())}
	bBase := 10 + n + 5
	fmt.Fprintf(&b, "3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Rotate %d 0 R >>\nendobj\n", bBase+1)
	for k := 1; k <= n+1; k++ {
		sn := 10 + k - 1
		ents[sn] = ent{1, uint32(b.Len())}
		if k >= 2 {
			ents[bBase+k-1] = ent{2, uint32(sn)} // the object the previous link's /Filter names, "inside" this stream
		}
		filt := ""
		if k <= n {
			filt = fmt.Sprintf("/Filter %d 0 R", bBase+k)
		}
		fmt.Fprintf(&b, "%d 0 obj<<%s>>stream\nendstream", sn, filt) // 58 bytes a link: the recursion happens before /N, /First or the data are read
	}
	xoff := b.Len()
	maxn := bBase + n + 2
	var idx strings.Builder
	var rows bytes.Buffer
	runStart, runLen := -1, 0
	flush := func() {
		if runLen > 0 {
			fmt.Fprintf(&idx, "%d %d ", runStart, runLen)
		}
		runLen = 0
	}
	for num := 0; num <= maxn; num++ {
		e, ok := ents[num]
		if !ok {
			flush()
			continue
		}
		if runLen > 0 && num == runStart+runLen {
			runLen++
		} else {
			flush()
			runStart, runLen = num, 1
		}
		rows.WriteByte(e.typ)
		rows.Write([]byte{byte(e.a >> 24), byte(e.a >> 16), byte(e.a >> 8), byte(e.a)})
	}
	flush()
	fmt.Fprintf(&b, "\n%d 0 obj<</Type/XRef/Size %d/W[1 4 0]/Index[%s]/Root 1 0 R/Length %d>>stream\n", maxn+1, maxn+1, idx.String(), rows.Len())
	b.Write(rows.Bytes())
	fmt.Fprintf(&b, "\nendstream\nstartxref\n%d\n%%%%EOF\n", xoff)
	data := b.Bytes()

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
	<-done
	if stack > 32<<20 {
		t.Errorf("a %d KB file made the goroutine stack grow to %d MB (420,000 links, 24.6 MB, die with a fatal stack overflow)", len(data)>>10, stack>>20)
	}
}

// The 16 MiB parse budget is asked before an object is parsed and charged after, so
// one object may be as big as the file; and a dictionary of 8 bytes ("<</A 1>>")
// is about 50 bytes of map and boxes. 22 MB of them measured 1,136 MB at Open
// (the builder's note says 250 MB for the whole budget).
func TestRereviewParseBudgetIsChargedWhileParsing(t *testing.T) {
	var obj strings.Builder
	obj.WriteString("[")
	for i := 0; i < 600_000; i++ {
		obj.WriteString("<</A 1>>")
	}
	obj.WriteString("]")
	data := reviewSkeleton("", func(b *bytes.Buffer) { fmt.Fprintf(b, "5 0 obj\n%s\nendobj\n", obj.String()) })
	data = bytes.Replace(data, []byte("/Type /Page "), []byte("/Type /Page /Annots 5 0 R "), 1)
	before := heapAfterGC()
	d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	held := int64(heapAfterGC()) - int64(before)
	runtime.KeepAlive(d)
	if held > int64(len(data))*12 {
		t.Errorf("a %d KB file holds %d MB after Open (%dx its size): the parse budget does not count what a parsed object costs", len(data)>>10, held>>20, held/int64(len(data)))
	}
}

// The refusal of an encrypted file looks for "/XRef" in raw bytes and reads at
// most 256 cross-reference streams; a Name written with a #xx escape, or 300
// decoys in front of the real one, hides /Encrypt from the rebuild.
func TestRereviewEncryptedFileWithAHiddenXRefIsStillRefused(t *testing.T) {
	mk := func() []byte {
		d := pdftest.New()
		f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>")
		pg := d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), "BT /F1 12 Tf 72 700 Td (Fine text here) Tj ET", "")
		root := d.Pages([]int{pg}, "")
		return d.Write(root, pdftest.Opt{XrefStream: true, Trailer: "/Encrypt << /Filter /Standard /V 1 /R 2 >>", Truncate: 20})
	}
	var dec bytes.Buffer
	dec.WriteString("%PDF-1.7\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&dec, "%d 0 obj\n<< /Type /XRef /Size 1 /W [1 1 1] /Length 3 >>\nstream\n\x00\x00\x00\nendstream\nendobj\n", 5000+i)
	}
	real := mk()
	for name, data := range map[string][]byte{
		"name with an escape": bytes.Replace(mk(), []byte("/XRef"), []byte("/XR#65f"), 1),
		"300 decoys first":    append(dec.Bytes(), real[bytes.Index(real, []byte("%PDF"))+9:]...),
	} {
		if _, err := pdfread.Open(context.Background(), data, pdfread.Limits{}); err != pdfread.ErrEncrypted {
			t.Errorf("%s: an encrypted file with a damaged startxref opened (err %v)", name, err)
		}
	}
}

// The builder's note says 600,000 pieces cost "about 300 MB through the layout".
// 20 pages of 30,000 lines of 80 letters (2.5 MB stored, 1.4 MB deflated) peaked at 1,097 MB and
// took 7 s in Open + ReadPages + Analyse (the op: 1,136 MB, 7.8 s, then refused as
// larger than 32 MB). Two import slots: 2.2 GB from two 1.4 MB uploads.
func TestRereviewPieceFloodStaysNearTheNote(t *testing.T) {
	if testing.Short() {
		t.Skip("holds about 1 GB for 7 s")
	}
	rnd := rand.New(rand.NewSource(1))
	var c strings.Builder
	c.WriteString("BT /F1 12 Tf 12 TL 72 700 Td\n")
	for i := 0; i < 30000; i++ {
		c.WriteString("(")
		for k := 0; k < 80; k++ {
			if k%6 == 5 {
				c.WriteByte(' ')
			} else {
				c.WriteByte(byte('a' + rnd.Intn(26)))
			}
		}
		c.WriteString(") '\n")
	}
	c.WriteString("ET")
	data := rr2MultiPage(20, c.String())
	var hi uint64
	base := heapAfterGC()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
				runtime.ReadMemStats(&m)
				hi = max(hi, m.HeapAlloc)
			}
		}
	}()
	ctx := context.Background()
	d, err := pdfread.Open(ctx, data, pdfread.Limits{})
	if err == nil {
		var pages []*pdfread.Page
		d.ReadPages(ctx, func(p *pdfread.Page) error { pages = append(pages, p); return nil })
		pdfread.Analyse(ctx, pages, d.Outline(), d.Info)
	}
	close(stop)
	<-done
	if mb := (int64(hi) - int64(base)) >> 20; mb > 450 {
		t.Errorf("a %d KB file of 600,000 one-line pieces peaked at %d MB (the note says about 300 MB)", len(data)>>10, mb)
	}
}

func rr2MultiPage(pages int, content string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	var kids strings.Builder
	for i := 0; i < pages; i++ {
		fmt.Fprintf(&kids, "%d 0 R ", 10+i)
	}
	fmt.Fprintf(&b, "2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", kids.String(), pages)
	for i := 0; i < pages; i++ {
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n", 10+i)
	}
	// stored, not deflated: random-looking text does not compress past the ratio limit anyway
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(content), content)
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n")
	return b.Bytes()
}

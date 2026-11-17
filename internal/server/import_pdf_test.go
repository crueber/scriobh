package server

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// The bodies of imports in flight share one budget: a big one that does not fit is told to wait (429) before
// a byte of it is read.
func TestBigImportBodiesShareABudget(t *testing.T) {
	if !takeBodyBudget(100<<20) || !takeBodyBudget(80<<20) {
		t.Fatal("two bodies that fit were refused")
	}
	if takeBodyBudget(20 << 20) {
		t.Error("a body over the budget was taken")
	}
	giveBodyBudget(100 << 20)
	if !takeBodyBudget(20 << 20) {
		t.Error("the budget was not given back")
	}
	giveBodyBudget(80 << 20)
	giveBodyBudget(20 << 20)
	if bodiesInFlight.Load() != 0 {
		t.Errorf("%d bytes still held", bodiesInFlight.Load())
	}
	// through the handler: a body that cannot fit is refused at once
	s := newAuthzServer(t)
	w := s.newCaller(t, "w", ops.RoleWriter)
	takeBodyBudget(maxBodiesInFlight - 1<<20)
	defer giveBodyBudget(maxBodiesInFlight - 1<<20)
	rec := s.do(w, "POST", "/api/v1/imports/preview", `{"files":[{"path":"a.md","content":"`+strings.Repeat("a", 1<<20)+`"}]}`)
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "busy") {
		t.Errorf("with the budget spent: %d %.200s", rec.Code, rec.Body)
	}
}

// Two 31 MiB PDFs uploaded at once by two people: both are read (in children), and the parent's heap is
// what holds the two bodies and nothing of the parsing.
func TestTwoLargePdfBodiesAtOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("builds two 31 MiB bodies")
	}
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var c strings.Builder
	c.WriteString("BT /F1 20 Tf 72 740 Td (A heading) Tj ET\n")
	for l := 0; l < 140; l++ {
		fmt.Fprintf(&c, "BT /F1 11 Tf 72 %d Td (Line %d of a page in plain words that run on to the edge of it.) Tj ET\n", 720-5*l, l)
	}
	var pgs []int
	for i := 0; i < 2400; i++ {
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), c.String()+strings.Repeat(" ", i%7), ""))
	}
	pdf := d.Write(d.Pages(pgs, ""), pdftest.Opt{})
	if len(pdf) > imports.MaxPDF {
		t.Fatalf("%d bytes", len(pdf))
	}
	b, _ := json.Marshal(map[string]any{"pdf": pdf, "name": "big.pdf"})
	body := string(b)
	pdf, b = nil, nil
	s := newAuthzServer(t)
	a, bb := s.newCaller(t, "a", ops.RoleWriter), s.newCaller(t, "b", ops.RoleWriter)
	if sseFactor > 1 {
		// a race-instrumented child needs several times the memory of the real one: the product's ceiling (512 MiB) is
		// unchanged, the test's child gets room (the same as TestPdfAtTheSizeLimitHasABoundedPeak in internal/imports)
		imports.SetPDFChildForTest(nil, 120*time.Second, 4096)
		t.Cleanup(func() { imports.SetPDFChildForTest(nil, 0, 0) })
	}

	var peak uint64
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
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	runtime.GC()
	var wg sync.WaitGroup
	codes := make([]int, 2)
	start := time.Now()
	for i, who := range []caller{a, bb} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := s.do(who, "POST", "/api/v1/imports/preview", body)
			codes[i] = rec.Code
			if rec.Code != 200 {
				t.Logf("caller %d: %d %.200s", i, rec.Code, rec.Body)
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-done
	if cs := imports.LastPDFChildStats(); cs != nil {
		t.Logf("the last of the two reading children: ran %v, peak resident memory %d MB", cs.Wall.Round(time.Millisecond), cs.MaxRSS>>20)
	}
	t.Logf("two %.1f MiB bodies at once: %v, peak parent heap %d MB (the body strings themselves are %d MB)", float64(len(body))/(1<<20), time.Since(start).Round(time.Millisecond), peak>>20, 2*len(body)>>20)
	if codes[0] != 200 || codes[1] != 200 {
		t.Errorf("codes %v", codes)
	}
}

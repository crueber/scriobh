package imports_test

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// The largest PDF the op accepts (32 MiB, 43 MB as base64 in a JSON body): the
// heap while the body is decoded and the file is previewed.
func TestPdfAtTheSizeLimitHasABoundedPeak(t *testing.T) {
	if testing.Short() || timingFactor > 1 {
		// The race-built reader child needs more memory than the four-vCPU CI
		// runner has for the 31 MiB, 2,400-page PDF; it stopped unexpectedly in
		// run #534. The plain build runs the test and enforces the budget.
		t.Skip("builds a 32 MiB PDF; the race-built child needs more than the runner has")
	}
	e := newEnv(t)
	d := pdftest.New()
	f := d.Add("<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /Encoding /WinAnsiEncoding >>")
	var pgs []int
	var c strings.Builder
	c.WriteString("BT /F1 20 Tf 72 740 Td (A heading) Tj ET\n")
	for l := 0; l < 140; l++ {
		fmt.Fprintf(&c, "BT /F1 11 Tf 72 %d Td (Line %d of a page in plain words that run on to the edge of it.) Tj ET\n", 720-5*l, l)
	}
	for i := 0; i < 2400; i++ {
		pgs = append(pgs, d.Page(612, 792, fmt.Sprintf("/Font << /F1 %d 0 R >>", f), c.String()+strings.Repeat(" ", i%7), ""))
	}
	pdf := d.Write(d.Pages(pgs, ""), pdftest.Opt{})
	t.Logf("the PDF is %.1f MiB", float64(len(pdf))/(1<<20))
	if len(pdf) > imports.MaxPDF {
		t.Fatalf("over the limit: %d", len(pdf))
	}
	body, _ := json.Marshal(imports.PreviewIn{Pdf: pdf, Name: "big.pdf"})
	pdf = nil
	var peak uint64
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
				peak = max(peak, m.HeapAlloc)
			}
		}
	}()
	runtime.GC()
	start := time.Now()
	var in imports.PreviewIn
	if err := json.Unmarshal(body, &in); err != nil {
		t.Fatal(err)
	}
	body = nil
	pv := e.must(e.w, "import.preview", in).(imports.PreviewOut)
	took := time.Since(start)
	close(stop)
	<-done
	t.Logf("a %.1f MiB PDF through a JSON body: preview %v, peak heap %d MB, %d pages", float64(len(in.Pdf))/(1<<20), took, peak>>20, int(pv.Report["pdf"].(map[string]any)["report"].(map[string]any)["pages"].(float64)))
	if cs := imports.LastPDFChildStats(); cs != nil {
		t.Logf("its reading child: ran %v, peak resident memory %d MB", cs.Wall.Round(time.Millisecond), cs.MaxRSS>>20)
	}
	if peak>>20 > 1500*uint64(max(1, timingFactor/5)) {
		t.Errorf("peak heap %d MB", peak>>20)
	}
}

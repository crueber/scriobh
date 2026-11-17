//go:build unix

package imports_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
)

// The hostile files of the re-review, at the sizes the review gave, read through the real route (import.preview, the
// reading child, the parent that decodes the answer). Nothing is asserted but that the parent lives and answers; the
// numbers are logged for the notes: the child's peak resident memory as the kernel reports it, how long it ran, the
// parent's peak live heap while it waited (sampled), and the parent's own peak resident memory so far (a lifetime
// figure: run one case with -run to read it alone). Skipped unless PDF_MEASURE is set.

func chainPDF(n int) []byte {
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
	return b.Bytes()
}

func rrSkeleton(extra string) []byte {
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Annots 5 0 R /Resources << /Font << /F1 << /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >> >> >> /Contents 4 0 R >>\nendobj\n")
	c := "BT /F1 12 Tf 72 700 Td (hello world this is text) Tj ET"
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(c), c)
	b.WriteString(extra)
	b.WriteString("trailer\n<< /Root 1 0 R >>\n")
	return b.Bytes()
}

func bfrangePDF(runes int) []byte {
	hex := strings.Repeat("4E00", runes)
	cm := "/CIDInit /ProcSet findresource begin 12 dict begin begincmap\n1 begincodespacerange <00> <FF> endcodespacerange\n1 beginbfrange\n<0000> <FFFF> <" + hex + ">\nendbfrange\nendcmap end end"
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>\nendobj\n")
	c := "BT /F1 12 Tf 72 700 Td (a) Tj ET"
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(c), c)
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman /ToUnicode 6 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "6 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\ntrailer\n<< /Root 1 0 R >>\n", len(cm), cm)
	return b.Bytes()
}

func flood(pages int) []byte {
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
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", c.Len(), c.String())
	b.WriteString("5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Times-Roman >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n")
	return b.Bytes()
}

func TestMeasureHostilePDFsThroughTheChild(t *testing.T) {
	if os.Getenv("PDF_MEASURE") == "" {
		t.Skip("set PDF_MEASURE=1 to measure")
	}
	e := newEnv(t)
	amp := func(n int) []byte {
		var o strings.Builder
		o.WriteString("[")
		for i := 0; i < n; i++ {
			o.WriteString("<</A 1>>")
		}
		o.WriteString("]")
		return rrSkeleton("5 0 obj\n" + o.String() + "\nendobj\n")
	}
	cases := []struct {
		name string
		mk   func() []byte
	}{
		{"trailer-160KB", func() []byte { return []byte("%PDF-1.7\n" + strings.Repeat("trailer(", (160<<10)/8)) }},
		{"trailer-1MB", func() []byte { return []byte("%PDF-1.7\n" + strings.Repeat("trailer(", (1<<20)/8)) }},
		{"bfrange-8000-runes-31KB", func() []byte { return bfrangePDF(8000) }},
		{"bfrange-100000-runes-400KB", func() []byte { return bfrangePDF(100000) }},
		{"parse-amplification-22MB", func() []byte { return amp(2_750_000) }},
		{"piece-flood-20-pages", func() []byte { return flood(20) }},
		{"filter-chain-420000-links-24MB", func() []byte { return chainPDF(420_000) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := c.mk()
			e.t = t
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
			_, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: data, Name: "h.pdf"})
			took := time.Since(start)
			close(stop)
			<-done
			cs := imports.LastPDFChildStats()
			var ru syscall.Rusage
			syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
			parent := int64(ru.Maxrss)
			if runtime.GOOS != "darwin" {
				parent <<= 10
			}
			child := int64(-1)
			var cwall time.Duration
			if cs != nil {
				child, cwall = cs.MaxRSS, cs.Wall
			}
			msg := "answered"
			if err != nil {
				msg = err.Error()
				if len(msg) > 90 {
					msg = msg[:90]
				}
			}
			t.Logf("MEASURE %-32s file %6.1f MB  total %8v  child ran %8v  child peak RSS %5d MB  parent peak heap %4d MB  parent peak RSS so far %5d MB  -> %s",
				c.name, float64(len(data))/(1<<20), took.Round(time.Millisecond), cwall.Round(time.Millisecond), child>>20, peak>>20, parent>>20, msg)
		})
	}
}

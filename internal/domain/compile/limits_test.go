package compile_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/testutil"
)

// A PDF compile or preview of a book over the word cap is refused in plain
// words; the DOCX of the same book is not.
func TestPDFWordCap(t *testing.T) {
	f := newFx(t, "Cap")
	f.novel()
	f.reg.Env().SetService(compile.LimitsService, compile.NewGate(2, 5))
	_, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9"})
	if ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "SCRIOBH_COMPILE_PDF_MAX_WORDS") {
		t.Fatalf("export: %v", err)
	}
	p := f.preview("print-6x9", publish.Options{})
	var warned bool
	for _, w := range p.Warnings {
		if w.Code == "pdf_too_large" && strings.Contains(w.Message, "refused") {
			warned = true
		}
	}
	if !warned || p.PageCount != 0 {
		t.Errorf("preview: %+v", p.Warnings)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "shunn-us"}); err != nil {
		t.Errorf("a DOCX is not capped: %v", err)
	}
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.cover_spec", compile.CoverIn{ProjectID: f.pid, PageThicknessIn: 0.0025}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("cover_spec from the layout of a capped book: %v", err)
	}
}

// With one slot, compiles that cannot get it are told to try again, quickly,
// and the others finish: nothing hangs.
func TestCompilesAreGated(t *testing.T) {
	f := newFx(t, "Gate")
	var seed uint32 = 3
	for i := 1; i <= 12; i++ {
		f.doc(fmt.Sprintf("Chapter %d", i), "chapter", "", prose(&seed, 2500))
	}
	g := compile.NewGate(1, 600_000)
	g.Wait = 5 * time.Millisecond
	f.reg.Env().SetService(compile.LimitsService, g)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, busy := 0, 0
	start := time.Now()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case ops.AsError(err).Code == ops.CodeRateLimited && ops.AsError(err).RetryAfterSeconds > 0 && strings.Contains(err.Error(), "busy"):
				busy++
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok == 0 || busy == 0 || ok+busy != 8 {
		t.Errorf("%d done, %d told to retry", ok, busy)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 20*time.Second*timingFactor {
			t.Errorf("took %v", took)
		}
	})
	// the slot is free again afterwards
	if _, err := f.reg.Call(f.ctx, f.owner, "compile.preview", compile.PreviewIn{ProjectID: f.pid, Preset: "print-6x9"}); err != nil {
		t.Errorf("after: %v", err)
	}
}

// A request that is cancelled while it waits for a slot, or while it lays a
// book out, stops.
func TestCancelledCompileStops(t *testing.T) {
	f := newFx(t, "Cancel")
	var seed uint32 = 5
	for i := 1; i <= 6; i++ {
		f.doc(fmt.Sprintf("Chapter %d", i), "chapter", "", prose(&seed, 2000))
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err := f.reg.Call(ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9"})
	if err == nil {
		t.Errorf("a cancelled request completed")
	}
}

// A book that does not fit the layout is refused, not written without its text.
func TestTruncatedBookIsRefused(t *testing.T) {
	f := newFx(t, "Cut")
	var md strings.Builder
	for j := 0; j < 400; j++ {
		md.WriteString("ref[^a] ")
	}
	md.WriteString("\n\n[^a]: " + strings.Repeat("note text that is long ", 700) + "\n")
	for i := 0; i < 3; i++ {
		f.doc(fmt.Sprintf("Chapter %d", i+1), "chapter", "", md.String())
	}
	_, err := f.reg.Call(f.ctx, f.owner, "export.compile", compile.CompileIn{ProjectID: f.pid, Preset: "print-6x9"})
	if ops.AsError(err).Code != ops.CodeInvalid || !strings.Contains(err.Error(), "larger than the PDF layout") {
		t.Fatalf("export: %v", err)
	}
	p := f.preview("print-6x9", publish.Options{})
	var said bool
	for _, w := range p.Warnings {
		if w.Code == "layout_limit" && strings.Contains(w.Message, "refused") {
			said = true
		}
	}
	if !said {
		t.Errorf("the preview does not say it would be refused: %+v", p.Warnings)
	}
}

package imports_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
)

// This file sorts last on purpose: the hostile reads below never end, so they hold
// the process-wide import slots until the test binary exits, and every import test
// that ran after them would be refused with rate_limited.

// "trailer(" repeated is a read that never ends, and the caller's context does not
// stop it (pdfread's rebuild scan): two such previews hold both import slots, and a
// benign one is refused with rate_limited 4 s after both callers gave up. (Slow:
// about 9 s; the hostile reads are abandoned and end with the test binary.)
func TestZZRereviewTwoHostilePreviewsHoldBothImportSlots(t *testing.T) {
	if testing.Short() {
		t.Skip("about 9 s")
	}
	e := newEnv(t)
	hostile := []byte("%PDF-1.7\n" + strings.Repeat("trailer(", (1<<20)/8))
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			e.reg.Call(ctx, e.w, "import.preview", imports.PreviewIn{Pdf: hostile})
		}()
	}
	time.Sleep(6 * time.Second) // the callers' contexts ended at 2 s
	_, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: tinyPDF(t, "", pdftest.Opt{})})
	if oe := ops.AsError(err); oe != nil && oe.Code == ops.CodeRateLimited {
		t.Fatalf("4 s after both callers cancelled, a benign preview is still refused: %v", err)
	} else if err != nil {
		t.Fatalf("benign preview: %v", err)
	}
}

package compile_test

import (
	"os"
	"runtime"
	"runtime/debug"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
)

// TestMeasurePrintCoverMemory is run by hand to measure what the print cover costs in memory (docs/COVERS.md, "As built in
// C2", has the figures): build this package's test binary, then
//
//	SCRIOBH_COVER_RSS=/path/to/picture SCRIOBH_COVER_PART=wrap /usr/bin/time -l ./compile.test -test.run TestMeasurePrintCoverMemory -test.v
//
// and read "maximum resident set size". The picture is uploaded (as a wrap at 6x9 with a 0.125 in bleed, or, with
// SCRIOBH_COVER_PART=front, as an image front), the layout is set, and export.cover is run, the whole of the real path. It
// does nothing unless SCRIOBH_COVER_RSS is set.
func TestMeasurePrintCoverMemory(t *testing.T) {
	path := os.Getenv("SCRIOBH_COVER_RSS")
	if path == "" {
		t.Skip("set SCRIOBH_COVER_RSS to a picture to measure")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f := newFx(t, "The Salt Road")
	f.novel()
	start := time.Now()
	if os.Getenv("SCRIOBH_COVER_PART") == "wrap" {
		up := f.uploadCover("wrap", raw, "6x9", "0.125")
		f.setLayout(coverlayout.SetIn{Shape: "wrap", Trim: "6x9", Alt: "x", Wrap: &coverlayout.Wrap{Attachment: up.AttachmentID}})
	} else {
		up := f.uploadCover("front", raw, "6x9", "")
		f.setLayout(coverlayout.SetIn{Shape: "sides", Trim: "6x9", Alt: "x", Front: &coverlayout.Side{Mode: "image", Attachment: up.AttachmentID}})
	}
	t.Logf("uploaded and set in %v", time.Since(start))
	// what the export alone takes: the Go heap sampled every millisecond, from a quiet start
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	base := ms.HeapAlloc
	var peak atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Millisecond):
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	start = time.Now()
	out, err := f.exportCover(compile.CoverIn{PageCount: 220, PageThicknessIn: 0.0025})
	close(stop)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cover: %d bytes in %v, %d warnings; the export's heap peak was %d MB over the %d MB before it", len(out.Body), time.Since(start), len(out.Warnings), (int64(peak.Load())-int64(base))>>20, base>>20)
}

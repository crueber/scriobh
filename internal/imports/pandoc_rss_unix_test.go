//go:build unix

package imports

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pandoc"
)

// With SCRIOBH_MEASURE_PANDOC=<shape name> this test parses one shape and prints the process's peak resident set, so
// each shape is measured in a process of its own (go test -run TestPandocPeakRSS -v, one shape at a time).
func TestPandocPeakRSS(t *testing.T) {
	name := os.Getenv("SCRIOBH_MEASURE_PANDOC")
	if name == "" {
		t.Skip("set SCRIOBH_MEASURE_PANDOC to a shape name to measure it")
	}
	md, ok := pandocShapes()[name]
	if !ok {
		t.Fatalf("no shape %q", name)
	}
	start := time.Now()
	_, err := convertPandoc(&pandoc.Imported{Markdown: []byte(md), Format: "rst", Version: "x"}, []byte("src"), "doc", "chapters", convOpts{})
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	rss := int64(ru.Maxrss) // bytes on macOS, kilobytes on Linux
	if runtime.GOOS != "darwin" {
		rss *= 1024
	}
	t.Logf("PEAK_RSS shape=%q input=%d KiB time=%v peak_rss=%d MiB err=%v", name, len(md)>>10, time.Since(start).Round(time.Millisecond), rss>>20, err)
}

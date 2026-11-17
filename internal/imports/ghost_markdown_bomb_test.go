package imports

import (
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// measurePeak runs f and reports the time and the most heap in use at any moment
// it sampled (every 5 ms), which is what a server has to have, not what it allocated.
//
// The heap in use counts garbage the collector has not reached yet, so it follows the
// collector's pacing as much as the code: the same 9 million tiny XML elements peak at
// 291 MB with the defaults, 328 MB with GOMAXPROCS=1 and 1.3 GB with GOGC=800. The
// collector is therefore held to GOGC=25 for the measurement, whatever the environment
// says, so that the peak is the live data plus a quarter and what the code allocates and
// drops adds little to it. It still depends on GOMAXPROCS and on the machine; a budget
// here is for the live memory of a hostile input. What a call allocates in total, which
// does not depend on any of that, is measured by allocatedBy (fixround_test.go).
func measurePeak(f func()) (time.Duration, uint64) {
	defer debug.SetGCPercent(debug.SetGCPercent(25))
	runtime.GC()
	var peak uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			peak = max(peak, m.HeapInuse)
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	f()
	d := time.Since(start)
	close(stop)
	wg.Wait()
	return d, peak >> 20
}

// Markdown from a foreign export goes to a parser. These are the inputs that make a
// Markdown parser spend more than it should (deep quotes and lists, delimiters that
// never pair, unbalanced brackets and code spans); each is the largest a document may
// be, and must cost seconds and a few hundred megabytes, not more. Before the
// text is bounded (mdbound.go) four of them took minutes.
func TestGhostMarkdownBombs(t *testing.T) {
	size := MaxFileBytes
	if hostileFactor > 1 {
		size /= 4 // the race detector is ten times slower; the shapes are the same
	}
	rep := func(unit string) string { return strings.Repeat(unit, size/len(unit)) }
	cases := map[string]string{
		"nested quotes":  strings.Repeat(">", size) + " x",
		"quote per line": rep("> > > > > > > > x\n"),
		"nested lists":   rep("- ") + "x",
		"list per line":  rep("- - - - - - - - x\n"),
		"indented lists": func() string {
			var b strings.Builder
			for i := 0; b.Len() < size; i++ {
				b.WriteString(strings.Repeat(" ", i%200) + "- x\n")
			}
			return b.String()
		}(),
		"unpaired emphasis":   rep("*a"),
		"unpaired underscore": rep("_a "),
		"open brackets":       rep("["),
		"links that fail":     rep("[a](b"),
		"images that fail":    rep("![a]("),
		"links on lines":      rep("[a](b\n"),
		"backticks":           rep("`a``"),
		"autolinks":           rep("<http://a.b"),
		"html":                rep("<div><span>"),
		"setext":              rep("a\n---\n"),
		"tables":              rep("|a|b|\n|-|-|\n|c|d|\n"),
		"definitions":         rep("[a]: http://b\n"),
		"entities":            rep("&amp;&#35;&bogus;"),
		"one huge paragraph":  strings.Repeat("word ", size/5),
	}
	md := goldmark.New(goldmark.WithExtensions(extension.Strikethrough, extension.Table))
	for name, src := range cases {
		c := &ghostConv{md: md, sk: newSink(), hs: &htmlStats{}}
		d, peak := measurePeak(func() { c.markdown(src[:min(len(src), size)]); c.sk.take() })
		t.Logf("%s: %v, peak heap %d MB, %d places bounded", name, d, peak, c.bounded)
		if peak > uint64(1500*(1+(int(hostileFactor)-1)/5)) {
			t.Errorf("%s took %v and %d MB", name, d, peak)
		}
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if d > 12*time.Second*hostileFactor {
				t.Errorf("%s took %v and %d MB", name, d, peak)
			}
		})
	}
}

// What a person writes is not changed by the bounds. (Code is no longer exempt: a line of code with more than 16
// list markers is capped like any other line, because a line that looks like a fence to boundMarkdown may not be one
// to the parser.)
func TestBoundMarkdownLeavesRealTextAlone(t *testing.T) {
	real := "# Title\n\nA paragraph of prose, with *emphasis* and a [link](https://example.org).\n\n> A quote\n> > nested once\n\n- one\n  - two\n    - three\n\n1. a\n2. b\n\n```\n" +
		strings.Repeat("- - - - - - - - - - -\n", 3) + "```\n\n    indented code\n\n" + strings.Repeat("sentence of prose. ", 100) + "\n"
	got, n := boundMarkdown(real)
	if got != real || n != 0 {
		t.Errorf("real text was changed (%d places):\n%q", n, got)
	}
	long := strings.Repeat("x", 30_000)
	got, n = boundMarkdown("a\n\n" + long + "\n\nb\n")
	if n == 0 || strings.Count(got, "x") != 30_000 || !strings.HasSuffix(got, "b\n") {
		t.Errorf("a long line: %d places, %d x's", n, strings.Count(got, "x"))
	}
}

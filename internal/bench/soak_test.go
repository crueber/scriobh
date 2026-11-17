package bench

import (
	"strings"
	"testing"
	"time"
)

// The growth rule: steady samples pass, a steady climb fails, and a warm-up
// spike is ignored. A budget missed in the last third fails too.
func TestSoakGrowthRule(t *testing.T) {
	so := SoakOptions{Duration: 600 * time.Second, Warmup: 60 * time.Second}
	mk := func(heap func(at float64) int64) *SoakResult {
		r := &SoakResult{Seconds: 600}
		for at := 0.0; at <= 600; at += 5 {
			r.Samples = append(r.Samples, SoakSample{At: at, RSS: 200 << 20, HeapLive: heap(at), Goroutines: 20, OpenFiles: 15, WAL: 4 << 20, DB: 50 << 20})
		}
		return r
	}
	rec := func(ms float64) *recorder {
		r := &recorder{ops: map[string][]timing{}, errs: map[string]int{}, first: map[string]string{}}
		for at := 0.0; at < 600; at++ {
			r.ops["http.save"] = append(r.ops["http.save"], timing{at: at, ms: ms})
		}
		return r
	}
	budgets := soakBudgets(150_000)

	steady := mk(func(at float64) int64 {
		if at < 60 {
			return 400 << 20 // the warm-up does not count
		}
		return 80 << 20
	})
	judgeSoak(steady, rec(5), so, budgets)
	if !steady.OK() {
		t.Errorf("steady samples failed: %v", steady.Problems)
	}

	leak := mk(func(at float64) int64 { return int64(80<<20) + int64(at)*(200<<10) }) // 200 KB a second
	judgeSoak(leak, rec(5), so, budgets)
	if leak.OK() || !strings.Contains(strings.Join(leak.Problems, " "), "heap_live grew") {
		t.Errorf("a climbing heap passed: %v", leak.Problems)
	}

	slow := mk(func(float64) int64 { return 80 << 20 })
	judgeSoak(slow, rec(450), so, budgets)
	if slow.OK() || !strings.Contains(strings.Join(slow.Problems, " "), "http.save p95") {
		t.Errorf("a save over its budget passed: %v", slow.Problems)
	}
}

func TestParseFootprint(t *testing.T) {
	for in, want := range map[string]int64{
		"scriobh [99521]: 64-bit    Footprint: 237 MB (16384 bytes per page)": 237 << 20,
		"zsh [1]: 64-bit    Footprint: 1744 KB (16384 bytes per page)":        1744 << 10,
		"x [2]: 64-bit    Footprint: 1.5 GB (16384 bytes per page)":           3 << 29,
		"nothing here": 0,
	} {
		if got := parseFootprint(in); got != want {
			t.Errorf("parseFootprint(%q) = %d, want %d", in, got, want)
		}
	}
	if got := parseRssAnon("Name:\tscriobh\nRssAnon:\t  123456 kB\nRssFile:\t 9000 kB\n"); got != 123456<<10 {
		t.Errorf("parseRssAnon = %d", got)
	}
	if parseRssAnon("Name:\tx\n") != 0 {
		t.Error("parseRssAnon without the line is not 0")
	}
	if footprint() < 0 {
		t.Error("footprint is negative")
	}
}

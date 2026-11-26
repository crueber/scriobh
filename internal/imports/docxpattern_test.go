package imports

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

// An expression that would make a program of millions of instructions is refused at the door, in microseconds and with no memory.
func TestPatternCompileBombsAreRefusedCheaply(t *testing.T) {
	for _, p := range []string{
		`(?:(?:a{1000}){1000}){1000}`, `(?:\pL{1000}){1000}`, `(?i)(?:[\pL\pN]{1000}){100}`, `((((((((((a{1,1000}){1,1000}){1,1000}){1,1000}){1,1000})))))))`,
		`(?:\p{Greek}|\p{Han}|\p{Latin}|\p{Cyrillic}){1000}`, `[\p{L}\p{M}\p{N}\p{P}\p{S}]{1000}`, `(?i)(?:\p{Lu}\p{Ll}){500}`,
	} {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		start := time.Now()
		_, perr := compilePattern(p, MatchRegex)
		took := time.Since(start)
		runtime.ReadMemStats(&m1)
		if perr == nil {
			t.Errorf("%.40q was accepted", p)
		}
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if took > 100*time.Millisecond || (m1.TotalAlloc-m0.TotalAlloc)>>20 > 16 {
				t.Errorf("%.40q took %v and %d MB", p, took, (m1.TotalAlloc-m0.TotalAlloc)>>20)
			}
		})
	}
}

// The spaces of a pattern mean something: "Scene " is not "Scene". Only a pattern of nothing but spaces is refused.
func TestPatternKeepsItsSpaces(t *testing.T) {
	pf, perr := compilePattern("Scene ", MatchStarts)
	if perr != nil {
		t.Fatal(perr)
	}
	if !pf.match("Scene 1") || pf.match("Scenery") {
		t.Error("the trailing space is part of the pattern")
	}
	if _, perr := compilePattern("   ", MatchStarts); perr == nil || perr.reason != "pattern_empty" {
		t.Errorf("spaces alone: %v", perr)
	}
	if got := cleanPattern(" a  b\n\t "); got != " a  b " {
		t.Errorf("only line breaks and controls go: %q", got)
	}
	if !strings.Contains(cleanPattern("a‮b"), "ab") {
		t.Error("direction overrides go")
	}
}

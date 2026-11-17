package site

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/site/check"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestGiantPostsStayLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	in := sample()
	var b strings.Builder
	for i := 0; i < 40_000; i++ {
		fmt.Fprintf(&b, "Paragraph %d has *some* words in it, a [link](https://example.org/%d) and a note.[^%d]\n\n[^%d]: Note %d.\n\n", i, i, i%500, i%500, i)
	}
	huge := b.String() // about 4 MB
	list := strings.Repeat("- item\n", 50_000)
	deep := strings.Repeat("> ", 200) + "deeply quoted\n"
	in.Entries = []Entry{{ID: "g1", Title: "Giant", Role: RolePost, Published: day(0), Content: huge}, {ID: "g2", Title: "List", Role: RolePost, Published: day(1), Content: list},
		{ID: "g3", Title: "Deep", Role: RolePost, Published: day(2), Content: deep}}
	start := time.Now()
	r := mustBuild(t, in)
	took := time.Since(start)
	t.Logf("a %d MB post, a 50,000-item list and 200 nested quotes: %v, %.1f MB out", len(huge)>>20, took, float64(r.Bytes)/1e6)
	budget := 10 * time.Second
	if raceEnabled {
		budget *= 10
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > budget {
			t.Errorf("took %v, budget %v", took, budget)
		}
	})
	if rep := check.Site(files(r), check.Options{Base: baseOf(in.Options)}); !rep.OK() {
		t.Fatalf("%v", rep.Problems[:min(5, len(rep.Problems))])
	}
}

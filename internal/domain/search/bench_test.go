package search_test

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/testutil"
)

// manuscript fills a project with `n` documents of about `words` words each,
// drawn from a Zipf-distributed vocabulary so that some words are common and
// most are rare, as in prose. It returns two words that occur in a good share
// of the documents, for a realistic two-word query.
func manuscript(tb testing.TB, f *fx, n, words int) (string, string) {
	tb.Helper()
	r := rand.New(rand.NewSource(7))
	vocab := make([]string, 4000)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("w%dx%s", i, strings.Repeat("a", i%5))
	}
	vocab[40], vocab[90] = "harbour", "lantern"
	zipf := rand.NewZipf(r, 1.15, 12, uint64(len(vocab)-1))
	for d := 0; d < n; d++ {
		var b strings.Builder
		for w := 0; w < words; w++ {
			b.WriteString(vocab[zipf.Uint64()])
			switch {
			case w%14 == 13:
				b.WriteString(".\n\n")
			default:
				b.WriteByte(' ')
			}
		}
		out, err := f.reg.Call(f.ctx, f.a, "doc.create", docs.CreateIn{ProjectID: f.pidA, Title: fmt.Sprintf("Chapter %d", d), Content: b.String()})
		if err != nil {
			tb.Fatal(err)
		}
		_ = out
	}
	return "harbour", "lantern"
}

func medianQuery(tb testing.TB, f *fx, q string, runs int) (time.Duration, search.QueryOut) {
	tb.Helper()
	times := make([]time.Duration, runs)
	var last search.QueryOut
	for i := range times {
		start := time.Now()
		out, err := f.reg.Call(f.ctx, f.a, "search.query", search.QueryIn{ProjectID: f.pidA, Q: q})
		times[i] = time.Since(start)
		if err != nil {
			tb.Fatal(err)
		}
		last = out.(search.QueryOut)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	return times[len(times)/2], last
}

// TestSearchIsFastAtManuscriptScale reports the median time of a two-word query
// over about 500,000 words (300 documents of 1,700 words). The target is under
// 80 ms; the assertion is looser so a loaded machine does not fail the build.
func TestSearchIsFastAtManuscriptScale(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 500,000-word project")
	}
	f := newFx(t)
	a, b := manuscript(t, f, 300, 1700)
	median, out := medianQuery(t, f, a+" "+b, 41)
	t.Logf("500k words: %q matched %d of 300 documents (first page %d); median of 41 two-word queries: %v", a+" "+b, out.Total, len(out.Items), median)
	if out.Total < 30 {
		t.Fatalf("the query is not selective enough to mean anything: %d matches", out.Total)
	}
	t.Run("time of a two-word query", func(t *testing.T) {
		testutil.Timing(t)
		if median > 250*time.Millisecond*timingFactor {
			t.Errorf("a two-word query took %v at 500k words; the target is 80ms", median)
		}
	})
	// And a single large document: 20,000 words in one.
	f2 := newFx(t)
	var w strings.Builder
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&w, "word%d ", i%900)
		if i == 12000 {
			w.WriteString("needleword ")
		}
	}
	if _, err := f2.reg.Call(f2.ctx, f2.a, "doc.create", docs.CreateIn{ProjectID: f2.pidA, Title: "Long", Content: w.String()}); err != nil {
		t.Fatal(err)
	}
	m2, out2 := medianQuery(t, f2, "needleword", 21)
	t.Logf("one 20,000-word document: median %v, offset %d", m2, out2.Items[0].Offset)
	if len(out2.Items) != 1 || out2.Items[0].Offset == 0 {
		t.Errorf("%+v", out2.Items)
	}
	t.Run("time of a query in one long document", func(t *testing.T) {
		testutil.Timing(t)
		if m2 > 100*time.Millisecond*timingFactor {
			t.Errorf("a query in one 20,000-word document took %v", m2)
		}
	})
}

func BenchmarkSearchTwoWords500kWords(b *testing.B) {

	f := newFx(b)
	x, y := manuscript(b, f, 300, 1700)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.reg.Call(f.ctx, f.a, "search.query", search.QueryIn{ProjectID: f.pidA, Q: x + " " + y}); err != nil {
			b.Fatal(err)
		}
	}
}

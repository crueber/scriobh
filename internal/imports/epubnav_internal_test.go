package imports

import (
	"github.com/crueber/scriobh/internal/testutil"
	"runtime"
	"strings"
	"testing"
	"time"
)

// An <li> that is never closed costs a count, not a frame: two million of them in a navigation document of 8 MiB are read
// in a few megabytes, and a closed one still pairs with its end tag.
func TestNavigationUnclosedItemsCostACount(t *testing.T) {
	src := `<html><body><nav epub:type="toc"><ol><li><a href="a.xhtml">A</a>` + strings.Repeat("<li>", 2_000_000) + `</ol></nav></body></html>`
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	t0 := time.Now()
	toc, _ := parseNavDoc(src, "nav.xhtml", nil)
	took := time.Since(t0)
	runtime.ReadMemStats(&after)
	alloc := after.TotalAlloc - before.TotalAlloc
	t.Logf("2,000,000 unclosed <li>: %v, %.1f MB allocated", took.Round(time.Millisecond), float64(alloc)/1e6)
	if len(toc) != 1 || toc[0].label != "A" {
		t.Errorf("toc: %+v", toc)
	}
	if alloc > 150<<20 {
		t.Errorf("%d MB allocated", alloc>>20)
	}
	// nested to the limit: still a tree of at most maxTocDepth levels
	deep := `<nav epub:type="toc"><ol>` + strings.Repeat(`<li><a href="a.xhtml">x</a><ol>`, 500) + `</nav>`
	toc, _ = parseNavDoc(deep, "nav.xhtml", nil)
	depth := 0
	for e := toc; len(e) > 0; e = e[0].children {
		depth++
	}
	if depth > maxTocDepth {
		t.Errorf("depth %d", depth)
	}
}

// A stray <li> before the navigation does not change how its entries nest.
func TestNavigationStrayItemBeforeTheNav(t *testing.T) {
	src := `<html><body><ul><li>stray</ul><li><nav epub:type="toc"><ol><li><a href="a.xhtml">A</a></li><li><a href="b.xhtml">B</a></li></ol></nav></body></html>`
	toc, _ := parseNavDoc(src, "nav.xhtml", nil)
	if len(toc) != 2 || toc[0].label != "A" || toc[1].label != "B" || len(toc[0].children) != 0 {
		t.Errorf("toc: %+v", toc)
	}
}

// Fifty thousand notes and a hundred thousand empty containers of notes: each note climbs to the containers it is in, so the
// work is the notes times their depth, not the notes times the containers.
func TestFindNotesWithManyContainersIsLinear(t *testing.T) {
	const notesN, containers = 50_000, 250_000
	var refs, targets strings.Builder
	refs.WriteString(`<html><body>`)
	for i := 0; i < notesN; i++ {
		refs.WriteString(`<a epub:type="noteref" href="t.xhtml#n` + itoa(i) + `">1</a>`)
	}
	refs.WriteString(`</body></html>`)
	targets.WriteString(`<html><body>`)
	for i := 0; i < containers; i++ {
		targets.WriteString(`<div class="footnotes"></div>`)
	}
	for i := 0; i < notesN; i++ {
		targets.WriteString(`<p epub:type="footnote" id="n` + itoa(i) + `">x</p>`)
	}
	targets.WriteString(`<section class="footnotes">`)
	for i := 0; i < 10; i++ {
		targets.WriteString(`<p epub:type="footnote" id="m` + itoa(i) + `">y</p>`)
	}
	targets.WriteString(`</section></body></html>`)
	budget := 1_200_000
	mk := func(path, src string) *epubDoc {
		d := &epubDoc{path: path, src: prepareXHTML([]byte(src))}
		d.sc = scanXHTML(d.src, &budget)
		return d
	}
	a, b := mk("a.xhtml", refs.String()), mk("t.xhtml", targets.String())
	t0 := time.Now()
	notes := findNotes([]*epubDoc{a, b}, map[string]int{"a.xhtml": 0, "t.xhtml": 1}, nil)
	took := time.Since(t0)
	t.Logf("%d notes, %d empty containers: %v (the document of %.1f MB)", len(notes), containers, took.Round(time.Millisecond), float64(len(b.src))/1e6)
	if len(notes) != notesN {
		t.Errorf("%d notes found", len(notes))
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if limit := 2 * time.Second * hostileFactor; took > limit {
			t.Errorf("took %v; the budget is %v", took, limit)
		}
	})
}

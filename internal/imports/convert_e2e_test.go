package imports_test

import (
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

// The whole of an import at 150,000 words, through the registry: the preview, then
// the apply that writes documents, research, links and the search index into the
// database in one transaction. The budget is generous (a shared machine, the race
// detector); the numbers are logged.
func TestConvertedImportsAt150000Words(t *testing.T) {
	type src struct {
		name   string
		layout string
		files  []imports.File
		docs   int
	}
	sources := []src{
		{"scrivener", imports.LayoutScrivener, imports.ScrivProject(150_000), 300 + 30},
		{"wordpress", imports.LayoutWordPress, []imports.File{{Path: "e.xml", Data: imports.WXRProject(150_000)}}, 187},
		{"ghost", imports.LayoutGhost, []imports.File{{Path: "e.json", Data: imports.GhostProject(150_000)}}, 187},
	}
	for _, s := range sources {
		e := newEnv(t)
		t0 := time.Now()
		pv := e.previewConv(imports.PreviewIn{Name: "Big", Files: s.files, Layout: s.layout})
		tp := time.Since(t0)
		t0 = time.Now()
		ap := e.applyConv(pv, imports.ApplyIn{Name: "Big", Files: s.files, Layout: s.layout})
		ta := time.Since(t0)
		t.Logf("%s: %d documents, %d words (%d research notes): preview %v, apply %v", s.name, ap.Documents, ap.Words, ap.ResearchNotes, tp.Round(time.Millisecond), ta.Round(time.Millisecond))
		if ap.Documents < s.docs-40 || ap.Words < 100_000 {
			t.Errorf("%s: %d documents and %d words", s.name, ap.Documents, ap.Words)
		}
		took := tp + ta
		t.Run(s.name+" time", func(t *testing.T) {
			testutil.Timing(t)
			if limit := 30 * time.Second * timingFactor; took > limit {
				t.Errorf("%s: preview and apply took %v (budget %v)", s.name, took, limit)
			}
		})
	}
}

package imports_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/testutil"
)

// A big import whose documents are full of wiki links must not hold the single
// writer for seconds: db's writer wait is five seconds, so a save elsewhere
// would otherwise fail with ErrWriterBusy while the import is still linking.
func TestLinkHeavyImportDoesNotStarveOtherWriters(t *testing.T) {
	if testing.Short() {
		t.Skip("imports 2,000 documents with 150 links each")
	}
	if timingFactor > 1 {
		// The writer's five-second wait does not stretch under the race
		// detector, so the end-to-end bound cannot be scaled; the batch's own
		// chunk bound is asserted, scaled, in internal/domain/links.
		t.Skip("wall-clock bound against the writer timeout; see links.TestSyncBatchCutsTheWorkIntoShortChunks")
	}
	const ndocs, nlinks = 2000, 150
	e := newEnv(t)

	// Another project, saved to continuously while the import runs.
	other := e.must(e.w, "project.create", projects.CreateIn{Title: "Elsewhere"}).(projects.Item).Item.ID
	first := e.must(e.w, "doc.list", docs.ListIn{ProjectID: other}).(docs.ListOut).Items[0]

	m := map[string]string{}
	for i := 0; i < ndocs; i++ {
		var b strings.Builder
		fmt.Fprintf(&b, "# Note %d\n\nText of note %d.\n\n", i, i)
		for k := 0; k < nlinks; k++ {
			fmt.Fprintf(&b, "[[Note %d]] ", (i+1+k)%ndocs)
		}
		m[fmt.Sprintf("%04d-note.md", i)] = b.String() + "\n"
	}
	f := files(m)
	pv := e.preview(f, "")

	var (
		mu      sync.Mutex
		longest time.Duration
		saves   int
		failed  error
	)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		rev := first.Revision
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			body := fmt.Sprintf("Saved %d times while the import runs.", i)
			start := time.Now()
			out, err := e.call(e.w, "doc.update", docs.UpdateIn{ID: first.ID, BaseRevision: rev, Content: &body})
			el := time.Since(start)
			mu.Lock()
			if el > longest { // timing: not a budget: tracks the slowest save; the bound is asserted in the "time" subtest
				longest = el
			}
			saves++
			if err != nil && failed == nil {
				failed = err
			}
			mu.Unlock()
			if err != nil {
				return
			}
			rev = out.(docs.Item).Item.Revision
			time.Sleep(5 * time.Millisecond)
		}
	}()

	start := time.Now()
	out := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "the-salt-road", Files: f})
	took := time.Since(start)
	close(stop)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	t.Logf("import of %d documents with %d links each took %v; %d saves ran meanwhile, the slowest took %v", ndocs, nlinks, took, saves, longest)
	if failed != nil {
		t.Fatalf("a save failed while the import ran: %v", failed)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if longest > 3*time.Second {
			t.Errorf("a save waited %v for the writer", longest)
		}
		if saves < 5 {
			t.Errorf("only %d saves got through while the import ran", saves)
		}
	})

	pid := out.(imports.ApplyOut).ProjectID
	var rows, resolved int
	if err := e.d.Read().QueryRow(`SELECT count(*), coalesce(sum(l.to_id IS NOT NULL), 0) FROM document_links l JOIN documents d ON d.id = l.from_id WHERE d.project_id = ?`, pid).Scan(&rows, &resolved); err != nil {
		t.Fatal(err)
	}
	if rows != ndocs*nlinks || resolved != rows {
		t.Errorf("links %d, resolved %d; want %d of both", rows, resolved, ndocs*nlinks)
	}
	var markers int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'links.pending.%'`).Scan(&markers); err != nil || markers != 0 {
		t.Errorf("the repair marker must be cleared once the links are written: %d %v", markers, err)
	}
}

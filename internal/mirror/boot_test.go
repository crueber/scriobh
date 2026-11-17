package mirror_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

// Checking a hundred current mirrors at start-up costs two queries and writes
// nothing; the first start (everything missing) is the only slow one.
func TestStartupCheckOfAHundredProjectsIsCheap(t *testing.T) {
	e := newEnv(t)
	var pids []string
	for i := 0; i < 100; i++ {
		pid := e.project(fmt.Sprintf("Book %03d", i))
		pids = append(pids, pid)
		e.doc(pid, "One", "chapter", strings.Repeat("word ", 500))
		e.doc(pid, "Two", "chapter", strings.Repeat("word ", 500))
	}
	boot := func() (time.Duration, *countFS) {
		fs := &countFS{}
		start := time.Now()
		m, stop := e.bootMirror(fs)
		d := time.Since(start)
		for _, id := range pids {
			if !m.Status(id).InSync {
				t.Fatalf("project %s is not in sync after the start-up check", id)
			}
		}
		stop()
		return d, fs
	}
	first, fs1 := boot()
	t.Logf("first start, 100 projects written: %v (%d files)", first, fs1.writes())
	if fs1.writes() < 500 {
		t.Fatalf("the first start wrote %d files, want at least 500 (5 per project)", fs1.writes())
	}
	second, fs2 := boot()
	t.Logf("second start, 100 projects checked: %v", second)
	if fs2.writes() != 0 {
		t.Errorf("a current mirror was rewritten at start-up: %d writes", fs2.writes())
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if second > 2*time.Second {
			t.Errorf("checking 100 current projects took %v", second)
		}
	})
}

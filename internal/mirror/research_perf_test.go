package mirror_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

// A chapter save in a project with a large research library: how long the
// mirror's flush takes while the research is unchanged. The research is read and
// compared (not re-written) on every flush, so this is the cost that matters;
// the bound is generous (the machine is shared, and the race detector is an
// order of magnitude slower) and there to catch something that scales badly.
func TestChapterSaveWithALargeResearchLibrary(t *testing.T) {
	// 600 notes is the size the time bound is about. With the timing tests off (the push
	// gate) only the behaviour is checked, and 60 notes check it as well: the first write
	// of 600 files, each made durable, can take longer than the helpers' idle wait on a
	// disk that really syncs.
	notes := 600
	if testutil.TimingOff() {
		notes = 60
	}
	e := newEnv(t)
	e.start()
	pid := e.project("Big library")
	ch := e.doc(pid, "Chapter", "chapter", "text\n")
	body := strings.Repeat("A paragraph of research text, about as long as a clipping might be, with a few words. ", 25) // about 2 KB
	err := e.d.WithTx(e.ctx, func(tx *sql.Tx) error {
		now := db.Now()
		for i := 0; i < notes; i++ {
			id := db.NewID("rsn")
			if _, err := tx.Exec(`INSERT INTO research_notes(id, project_id, kind, title, body, position, word_count, content_hash, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, id, pid, "note", fmt.Sprintf("Note %d", i), body, float64(i+1)*65536, 400, fmt.Sprintf("h%d", i), now, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.m.Mark(pid)
	start := time.Now()
	e.settle(pid)
	first := time.Since(start)
	// Now a chapter save: nothing in the research changed.
	e.fs.reset()
	e.setContent(ch, "a changed chapter\n")
	start = time.Now()
	e.settle(pid)
	save := time.Since(start)
	// Behaviour, checked in every run: the unchanged research is compared, not re-written.
	if n := e.fs.writes(); n >= notes/2 {
		t.Errorf("a chapter save re-wrote the research: %d writes with %d unchanged notes", n, notes)
	}
	t.Logf("%d notes of about 2 KB: first write %v, flush after a chapter save %v", notes, first.Round(time.Millisecond), save.Round(time.Millisecond))
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if bound := 4 * time.Second * timingFactor; save > bound {
			t.Errorf("a chapter save took %v to reach the mirror with %d research notes (bound %v)", save, notes, bound)
		}
	})
}

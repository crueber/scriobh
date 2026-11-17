package links_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

type store struct {
	t   *testing.T
	d   *db.DB
	pid string
}

func newStore(t *testing.T) *store {
	t.Helper()
	d := testutil.DB(t)
	uid := testutil.User(t, d, "writer", "w@t.local")
	now := db.Now()
	pid := db.NewID("prj")
	if _, err := d.Writer().Exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES (?,?,?,?,?,?)`, pid, uid, "P", "p", now, now); err != nil {
		t.Fatal(err)
	}
	return &store{t: t, d: d, pid: pid}
}

// doc inserts a document row the way an importer that knows nothing about
// links would, and returns a job for its links.
func (s *store) doc(project, title, content string) (string, links.Job) {
	s.t.Helper()
	id := db.NewID("doc")
	now := db.Now()
	hash := fmt.Sprintf("hash-%s", id)
	if _, err := s.d.Writer().Exec(`INSERT INTO documents(id, project_id, title, content, content_hash, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		id, project, title, content, hash, now, now); err != nil {
		s.t.Fatal(err)
	}
	return id, links.Job{DocumentID: id, Hash: hash, Links: text.WikiLinks(content), Fresh: true}
}

func (s *store) count(q string, args ...any) int {
	s.t.Helper()
	var n int
	if err := s.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func TestSyncBatchCutsTheWorkIntoShortChunks(t *testing.T) {
	s := newStore(t)
	const ndocs, nlinks = 500, 100
	var jobs []links.Job
	for i := 0; i < ndocs; i++ {
		var b strings.Builder
		for k := 0; k < nlinks; k++ {
			fmt.Fprintf(&b, "[[Note %d]] ", (i+1+k)%ndocs)
		}
		_, j := s.doc(s.pid, fmt.Sprintf("Note %d", i), b.String())
		jobs = append(jobs, j)
	}
	st, err := links.SyncBatch(context.Background(), s.d, s.pid, "", jobs)
	if err != nil {
		t.Fatal(err)
	}
	if st.Jobs != ndocs || st.Rows != ndocs*nlinks || st.Skipped != 0 {
		t.Errorf("stats %+v", st)
	}
	if st.Chunks < 5 {
		t.Errorf("%d rows must not go in %d transactions", st.Rows, st.Chunks)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if st.Longest > time.Second*timingFactor {
			t.Errorf("the writer was held %v by one chunk", st.Longest)
		}
	})
	t.Logf("%d rows in %d chunks, longest %v", st.Rows, st.Chunks, st.Longest)
	if n := s.count(`SELECT count(*) FROM document_links WHERE to_id IS NOT NULL`); n != ndocs*nlinks {
		t.Errorf("resolved links %d", n)
	}
}

func TestSyncBatchSkipsDocumentsThatChangedOrWentAway(t *testing.T) {
	s := newStore(t)
	s.doc(s.pid, "Target", "")
	changed, jc := s.doc(s.pid, "Changed", "[[Target]]")
	gone, jg := s.doc(s.pid, "Gone", "[[Target]]")
	_, jok := s.doc(s.pid, "Fine", "[[Target]]")
	// Between parsing and writing, one document was saved (and linked itself) and one was purged.
	if _, err := s.d.Writer().Exec(`UPDATE documents SET content_hash = 'newer' WHERE id = ?`, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.d.Writer().Exec(`DELETE FROM documents WHERE id = ?`, gone); err != nil {
		t.Fatal(err)
	}
	st, err := links.SyncBatch(context.Background(), s.d, s.pid, "", []links.Job{jc, jg, jok})
	if err != nil {
		t.Fatalf("a stale job must be skipped, not fail the batch: %v", err)
	}
	if st.Skipped != 2 || st.Jobs != 1 {
		t.Errorf("stats %+v", st)
	}
	if n := s.count(`SELECT count(*) FROM document_links`); n != 1 {
		t.Errorf("only the fine document gets links: %d", n)
	}
	// A job for another project's document is skipped too.
	other := newStore(t)
	oid, oj := other.doc(other.pid, "Other", "[[X]]")
	_ = oid
	if st, err := links.SyncBatch(context.Background(), s.d, s.pid, "", []links.Job{oj}); err != nil || st.Skipped != 1 {
		t.Errorf("a foreign document: %+v %v", st, err)
	}
}

func TestSyncBatchUpdatesExistingLinksWithoutDuplicating(t *testing.T) {
	s := newStore(t)
	s.doc(s.pid, "A", "")
	s.doc(s.pid, "B", "")
	id, j := s.doc(s.pid, "Src", "[[A]] [[Missing]]")
	if _, err := links.SyncBatch(context.Background(), s.d, s.pid, "", []links.Job{j}); err != nil {
		t.Fatal(err)
	}
	j.Fresh = false
	j.Links = text.WikiLinks("[[B]] [[A]]")
	if _, err := links.SyncBatch(context.Background(), s.d, s.pid, "", []links.Job{j}); err != nil {
		t.Fatal(err)
	}
	if n := s.count(`SELECT count(*) FROM document_links WHERE from_id = ?`, id); n != 2 {
		t.Errorf("rows %d", n)
	}
	if n := s.count(`SELECT count(*) FROM document_links WHERE from_id = ? AND link_text = 'Missing'`, id); n != 0 {
		t.Error("a link that left the text must go")
	}
	j.Links = nil
	links.SyncBatch(context.Background(), s.d, s.pid, "", []links.Job{j})
	if n := s.count(`SELECT count(*) FROM document_links WHERE from_id = ?`, id); n != 0 {
		t.Errorf("no links left: %d", n)
	}
}

func TestRebuildReadsInPagesAndRepairsEverything(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 250; i++ {
		s.doc(s.pid, fmt.Sprintf("Doc %d", i), fmt.Sprintf("See [[Doc %d]] and [[Nowhere]].", (i+1)%250))
	}
	if n, err := links.Rebuild(context.Background(), s.d, ""); err != nil || n != 250 {
		t.Fatalf("rebuild %d %v", n, err)
	}
	if n := s.count(`SELECT count(*) FROM document_links`); n != 500 {
		t.Fatalf("rows %d", n)
	}
	if n := s.count(`SELECT count(*) FROM document_links WHERE to_id IS NOT NULL`); n != 250 {
		t.Errorf("resolved %d", n)
	}
	// Damage the index, rebuild one project, and it is whole again.
	if _, err := s.d.Writer().Exec(`DELETE FROM document_links WHERE rowid % 2 = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.d.Writer().Exec(`UPDATE document_links SET target_key = 'stale', to_id = NULL WHERE rowid % 3 = 0`); err != nil {
		t.Fatal(err)
	}
	if n, err := links.Rebuild(context.Background(), s.d, s.pid); err != nil || n != 250 {
		t.Fatalf("rebuild %d %v", n, err)
	}
	if n := s.count(`SELECT count(*) FROM document_links`); n != 500 {
		t.Errorf("rows after repair %d", n)
	}
	if n := s.count(`SELECT count(*) FROM document_links WHERE target_key = 'stale'`); n != 0 {
		t.Errorf("stale keys %d", n)
	}
	if n := s.count(`SELECT count(*) FROM document_links WHERE to_id IS NOT NULL`); n != 250 {
		t.Errorf("resolved after repair %d", n)
	}
	if n, err := links.Rebuild(context.Background(), s.d, "prj_nothing"); err != nil || n != 0 {
		t.Errorf("an empty project: %d %v", n, err)
	}
}

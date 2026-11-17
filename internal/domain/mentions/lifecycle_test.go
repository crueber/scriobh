package mentions_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/search"
)

// `scriobh search rebuild` rewrites doc_text, which the mention index reads, so
// it leaves every project owing a scan.
func TestSearchRebuildMarksEveryProjectForAScan(t *testing.T) {
	b := newBook(t)
	b.doc(b.owner, b.pid, "One", "Elara walked.")
	other := b.project(b.owner, "Other", b.cid)
	b.doc(b.owner, other, "Two", "Máire waited.")
	b.settle()
	if n := b.count(`SELECT count(*) FROM mention_state WHERE stamp <> indexed_stamp`); n != 0 {
		t.Fatalf("settled, yet %d projects owe a scan", n)
	}
	if err := search.Rebuild(b.ctx, b.d, nil); err != nil {
		t.Fatal(err)
	}
	if n := b.count(`SELECT count(*) FROM mention_state WHERE stamp <> indexed_stamp`); n != 2 {
		t.Errorf("after a search rebuild %d projects owe a scan, want 2", n)
	}
	b.settle()
	if n := b.count(`SELECT count(*) FROM mention_state WHERE stamp <> indexed_stamp`); n != 0 {
		t.Errorf("%d projects still owe a scan", n)
	}
}

// A project edited continuously can move its vocabulary under every attempt of
// a scan. After the restarts the scan keeps what it did and records the stamp it
// started with, leaving the project owing a scan for the newer one, instead of
// failing and waiting out a back-off.
func TestAScanThatKeepsBeingOutrunAcceptsItsStamp(t *testing.T) {
	b := newBook(t)
	b.doc(b.owner, b.pid, "One", "Elara walked.")
	b.settle()
	b.exec(`DELETE FROM entity_mentions`)
	b.exec(`UPDATE mention_state SET stamp = 'start', indexed_stamp = '' WHERE project_id = '` + b.pid + `'`)
	mentions.ResetCache()
	// An edit lands after every attempt has read its vocabulary, as an endless
	// stream of edits would.
	attempts := 0
	defer mentions.SetScanHook(func() {
		attempts++
		b.exec(`UPDATE mention_state SET stamp = lower(hex(randomblob(8))) WHERE project_id = ?`, b.pid)
	})()
	st, err := mentions.ScanProject(b.ctx, b.d, b.pid)
	if err != nil {
		t.Fatalf("the scan should settle for the stamp it began with: %v", err)
	}
	if attempts < 6 {
		t.Errorf("only %d attempts: the scan was not outrun", attempts)
	}
	if st.Written == 0 || b.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, b.pid) == 0 {
		t.Errorf("the work was thrown away: %+v", st)
	}
	if n := b.count(`SELECT count(*) FROM mention_state WHERE project_id = ? AND stamp <> indexed_stamp`, b.pid); n != 1 {
		t.Errorf("the project should still owe a scan for the newer stamp")
	}
}

// The worker pages through a project's documents by id; the page query must be
// served by an index, not sorted on every page.
func TestTheWorkersPageQueryUsesAnIndex(t *testing.T) {
	b := newBook(t)
	rows, err := b.d.Read().Query(`EXPLAIN QUERY PLAN SELECT d.id, d.content_hash FROM documents d WHERE d.project_id = ? AND d.id > ? AND d.kind <> 'research' ORDER BY d.id LIMIT 200`, b.pid, "")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, bb, c int
		var detail string
		if err := rows.Scan(&a, &bb, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	all := strings.Join(plan, "; ")
	if !strings.Contains(all, "documents_project_id") || strings.Contains(all, "TEMP B-TREE") {
		t.Errorf("plan: %s", all)
	}
}

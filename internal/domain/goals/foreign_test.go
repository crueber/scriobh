package goals_test

import (
	"context"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
)

// Words another member wrote are not the saver's words.
func TestOtherPeoplesWordsEarnNothing(t *testing.T) {
	e := newEnv(t)
	alice := e.person("alice", "UTC")
	bob := e.person("bob", "UTC")
	pid := e.project(alice)
	e.join(pid, bob, ops.ProjectEditor)
	d := e.doc(alice, pid)

	mine := lorem(10, "a")
	e.save(alice, d, mine)
	e.advance(time.Minute)
	e.save(bob, d, mine+lorem(500, "b"))
	e.advance(time.Minute)
	e.save(alice, d, mine+lorem(500, "b")+"one more")

	if got := e.today(alice, pid); got.WordsAdded != 12 || got.WordsNet != 12 {
		// Net is her own change at each save.
		t.Errorf("alice: added %d net %d, want added 12", got.WordsAdded, got.WordsNet)
	}
	if got := e.today(bob, pid); got.WordsAdded != 500 {
		t.Errorf("bob: added %d, want 500", got.WordsAdded)
	}
}

// A restore by another member rewrites the document under a writer's session.
func TestARestoreByAnotherMemberEarnsTheWriterNothing(t *testing.T) {
	e := newEnv(t)
	alice := e.person("alice", "UTC")
	bob := e.person("bob", "UTC")
	pid := e.project(alice)
	e.join(pid, bob, ops.ProjectEditor)
	d := e.doc(alice, pid)

	e.at("2026-03-02T09:00:00Z")
	e.save(bob, d, lorem(500, "old"))
	e.advance(time.Hour)
	e.save(bob, d, "")
	e.advance(time.Hour)
	e.save(alice, d, lorem(10, "a"))
	// Bob restores the 500-word version.
	list := e.call(bob, "revision.list", revisions.ListIn{ID: d}).(revisions.ListOut)
	var rid string
	for _, r := range list.Items {
		if r.WordCount == 500 {
			rid = r.ID
		}
	}
	if rid == "" {
		t.Fatalf("no 500-word version in %+v", list.Items)
	}
	e.advance(time.Minute)
	cur := e.call(bob, "doc.get", docs.DocIn{ID: d}).(docs.Item).Item
	e.call(bob, "revision.restore", docs.RestoreIn{ID: d, RevisionID: rid, BaseRevision: cur.Revision})
	e.advance(time.Minute)
	e.save(alice, d, lorem(500, "old")+"my new words")

	if got := e.today(alice, pid); got.WordsAdded != 13 {
		t.Errorf("alice: added %d, want 13 (her 10 words and 3 more)", got.WordsAdded)
	}
	if got := e.today(bob, pid); got.WordsAdded != 500 {
		t.Errorf("bob: added %d, want 500 and nothing for the restore", got.WordsAdded)
	}
}

// One person saving in turn from two places is one writer, and a save that
// lost a conflict earns nothing.
func TestTheSamePersonInTwoTabs(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	first := lorem(20, "a")
	e.save(w, d, first)
	stale := e.call(w, "doc.get", docs.DocIn{ID: d}).(docs.Item).Item // tab B loads here
	e.advance(time.Minute)
	second := first + lorem(20, "b")
	e.save(w, d, second) // tab A writes on
	text := stale.Content + lorem(500, "lost")
	if _, err := e.reg.Call(context.Background(), w.p, "doc.update", docs.UpdateIn{ID: d, BaseRevision: stale.Revision, Content: &text}); ops.AsError(err).Code != ops.CodeConflict {
		t.Fatalf("a stale save must conflict: %v", err)
	}
	e.advance(time.Minute)
	e.save(w, d, second+lorem(5, "c")) // tab B reloads and writes
	if got := e.today(w, pid); got.WordsAdded != 45 || got.WordsNet != 45 {
		t.Errorf("added %d net %d, want 45 and 45", got.WordsAdded, got.WordsNet)
	}
}

// A stored word count from an older counter must not distort the first save
// of a session.
func TestAWrongStoredWordCountDoesNotDistortTheFirstSave(t *testing.T) {
	e := newEnv(t)
	w := e.person("w", "UTC")
	pid := e.project(w)
	d := e.doc(w, pid)
	text := lorem(100, "a")
	e.save(w, d, text)
	// The document was written under an older counter: its stored count is wrong.
	if _, err := e.d.Writer().Exec(`UPDATE documents SET word_count = 5000 WHERE id = ?`, d); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Hour) // a new session
	e.save(w, d, text+lorem(10, "b"))
	got := e.today(w, pid)
	// The first save's own count is right (110 words); the 100 before it were
	// credited and counted earlier. This save adds 10 and its net is 10, not -4890.
	if got.WordsAdded != 110 || got.WordsNet != 110 {
		t.Errorf("added %d net %d, want 110 and 110", got.WordsAdded, got.WordsNet)
	}
}

func baselineRows(t *testing.T, e *env, where string, args ...any) int {
	t.Helper()
	var n int
	if err := e.d.Read().QueryRow(`SELECT count(*) FROM writing_session_docs `+where, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Baselines are copies of manuscript text: they go with the access, the
// document and the session that made them.
func TestBaselinesDoNotOutliveTheirAccess(t *testing.T) {
	e := newEnv(t)
	alice := e.person("alice", "UTC")
	bob := e.person("bob", "UTC")
	pid := e.project(alice)
	e.join(pid, bob, ops.ProjectEditor)
	d1, d2 := e.doc(alice, pid), e.doc(alice, pid)
	e.save(alice, d1, lorem(5, "a"))
	e.save(bob, d1, lorem(5, "a")+lorem(5, "b"))
	if n := baselineRows(t, e, ""); n != 2 {
		t.Fatalf("expected one baseline each, got %d", n)
	}

	// Leaving the project removes the person's baselines at once.
	if _, err := e.d.Writer().Exec(`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, pid, bob.id); err != nil {
		t.Fatal(err)
	}
	if n := baselineRows(t, e, `WHERE session_id IN (SELECT id FROM writing_sessions WHERE user_id = ?)`, bob.id); n != 0 {
		t.Errorf("bob still has %d baselines after leaving", n)
	}
	if n := baselineRows(t, e, ""); n != 1 {
		t.Errorf("alice's baseline should stay: %d rows", n)
	}

	// A purged document takes its baselines with it.
	if _, err := e.d.Writer().Exec(`DELETE FROM documents WHERE id = ?`, d1); err != nil {
		t.Fatal(err)
	}
	if n := baselineRows(t, e, ""); n != 0 {
		t.Errorf("%d baselines left for a purged document", n)
	}

	// A baseline whose session ended long ago is pruned by the next save, by anyone.
	e.save(alice, d2, lorem(5, "c"))
	if n := baselineRows(t, e, ""); n != 1 {
		t.Fatalf("rows %d", n)
	}
	e.advance(3 * time.Hour)
	carol := e.person("carol", "UTC")
	e.join(pid, carol, ops.ProjectEditor)
	d3 := e.doc(carol, pid)
	e.save(carol, d3, lorem(5, "d"))
	if n := baselineRows(t, e, `WHERE document_id = ?`, d2); n != 0 {
		t.Errorf("an old baseline survived: %d", n)
	}
	// Alice's credit is not lost: the next save simply starts a new baseline.
	e.save(alice, d2, lorem(5, "c")+lorem(3, "e"))
	if got := e.today(alice, pid); got.WordsAdded != 5+5+3 {
		t.Errorf("alice added %d, want 13", got.WordsAdded)
	}
	_ = goals.PaceWindow
}

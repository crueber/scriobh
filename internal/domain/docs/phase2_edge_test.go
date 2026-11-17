package docs_test

import (
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
)

// Awkward inputs on the save path.

func TestEmptyTextIsAVersionLikeAnyOther(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Wiped"})
	// Snapshot of a document nobody has written in.
	out, err := g.call(g.ownA, "revision.snapshot", revisions.SnapshotIn{ID: d.ID, Label: "Blank"})
	if err != nil {
		t.Fatal(err)
	}
	if got := g.content(d.ID, out.(revisions.Item).Item.ID); got != "" {
		t.Errorf("empty snapshot content %q", got)
	}
	// Write, clear, write again: the cleared state is kept like any other.
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "some words")
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "") // keeps "some words"
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "words again") // keeps the empty text
	rs := g.revs(g.ownA, d.ID)
	if len(rs) != 3 || reasons(rs) != "autosave,autosave,manual" {
		t.Fatalf("versions %s", reasons(rs))
	}
	if rs[0].WordCount != 0 || g.content(d.ID, rs[0].ID) != "" || g.content(d.ID, rs[1].ID) != "some words" {
		t.Errorf("the cleared state: %+v", rs[0])
	}
	cur := g.head(g.ownA, d.ID)
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: rs[0].ID, BaseRevision: cur.Revision}); err != nil {
		t.Fatal(err)
	}
	if got := g.head(g.ownA, d.ID).Content; got != "" {
		t.Errorf("restored empty text %q", got)
	}
	if n := g.count(`SELECT count(*) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, d.ID, revisions.Hash("")); n != 1 {
		t.Errorf("the empty text is stored once: %d", n)
	}
}

func TestRowsWrittenWithoutAContentStampStillGetHistory(t *testing.T) {
	g := newFx2(t)
	// A row as an importer that knows nothing about content_at would write it.
	const stamp = "2026-03-01T08:30:00.000Z"
	id := db.NewID("doc")
	if _, err := g.d.Writer().Exec(`INSERT INTO documents(id, project_id, title, content, word_count, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		id, g.pidA, "Imported", "imported text here", 3, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	g.save(g.ownA, id, "edited once") // the imported text is the head being replaced
	g.advance(10 * time.Minute)
	g.save(g.ownA, id, "edited twice")
	rs := g.revs(g.ownA, id)
	if len(rs) != 2 {
		t.Fatalf("versions %d", len(rs))
	}
	imported := rs[1]
	if imported.CreatedAt != stamp || imported.AuthorID != "" || imported.WordCount != 3 || g.content(id, imported.ID) != "imported text here" {
		t.Errorf("a row without a stamp falls back to updated_at and no author: %+v", imported)
	}
	// Once saved through the ops the row carries its own stamp and author.
	if rs[0].AuthorID != g.ownA.UserID || rs[0].CreatedAt == stamp {
		t.Errorf("the second version %+v", rs[0])
	}
}

func TestAPersonWhoIsGoneDoesNotBlockTheNextSave(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Shared"})
	g.save(g.editor, d.ID, "the editor wrote this")
	if _, err := g.d.Writer().Exec(`DELETE FROM users WHERE id = ?`, g.editor.UserID); err != nil {
		t.Fatal(err)
	}
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "the owner carries on") // captures the departed editor's text
	rs := g.revs(g.ownA, d.ID)
	if len(rs) != 1 || rs[0].AuthorID != "" || g.content(d.ID, rs[0].ID) != "the editor wrote this" {
		t.Errorf("versions %+v", rs)
	}
}

// Restoring old text changes the net words but never earns words added, or a
// restore of a long chapter would count as a day of writing.
func TestRestoreEarnsNoWordsAdded(t *testing.T) {
	g := newFx2(t)
	d := g.doc(g.ownA, g.pidA, docs.CreateIn{Title: "Credit"})
	long := "one two three four five six seven eight nine ten eleven twelve"
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, long)
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "short")
	g.advance(10 * time.Minute)
	g.save(g.ownA, d.ID, "short again")
	added := func() int {
		return g.count(`SELECT COALESCE(sum(words_added),0) FROM daily_stats WHERE user_id = ?`, g.ownA.UserID)
	}
	before := added()
	rs := g.revs(g.ownA, d.ID)
	var target string
	for _, r := range rs {
		if g.content(d.ID, r.ID) == long {
			target = r.ID
		}
	}
	if target == "" {
		t.Fatalf("no version holds the long text: %s", reasons(rs))
	}
	cur := g.head(g.ownA, d.ID)
	if _, err := g.call(g.ownA, "revision.restore", docs.RestoreIn{ID: d.ID, RevisionID: target, BaseRevision: cur.Revision}); err != nil {
		t.Fatal(err)
	}
	if got := g.head(g.ownA, d.ID).Content; got != long {
		t.Fatalf("restored %q", got)
	}
	if after := added(); after != before {
		t.Errorf("a restore earned %d words added (before %d, after %d)", after-before, before, after)
	}
}

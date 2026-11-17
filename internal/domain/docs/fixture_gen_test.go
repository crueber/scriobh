package docs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
)

// TestGenerateReleaseFixture writes a database holding real documents, a tree,
// a trashed branch and search rows, as the current schema builds them. It runs
// only when GEN_FIXTURE names the output file. Each release commits one so the
// next release's migrations are tested against data written by the last:
//
//	GEN_FIXTURE=$PWD/internal/db/testdata/v0.1.db go test ./internal/domain/docs -run GenerateReleaseFixture
func TestGenerateReleaseFixture(t *testing.T) {
	out := os.Getenv("GEN_FIXTURE")
	if out == "" {
		t.Skip("set GEN_FIXTURE to write a release fixture")
	}
	f := newFx(t)
	part := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "The Shore", Kind: "part", Content: "Epigraph."})
	ch1 := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "The Last Ferry", ParentID: part.ID, Content: "The ferry left at four. Máire waited on the pier.\n\nShe counted the gulls."})
	f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "The Harbour at Low Tide", ParentID: part.ID, Content: "The tide had gone out.\n\n* * *\n\nBy four o'clock the harbour was a bowl of black mud."})
	gone := f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Cut Scene", Kind: "scene", ParentID: ch1.ID, Content: "This one was cut."})
	f.doc(f.ownA, f.pidA, docs.CreateIn{Title: "Notes", Kind: "note", Content: "Remember the lighthouse."})
	if _, err := f.call(f.ownA, "doc.trash", docs.DocIn{ID: gone.ID}); err != nil {
		t.Fatal(err)
	}
	body := "The ferry left at five, this time. Máire waited on the pier."
	if _, err := f.call(f.ownA, "doc.update", docs.UpdateIn{ID: ch1.ID, BaseRevision: 1, Content: &body}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(out)
	if _, err := f.d.Writer().Exec("VACUUM INTO ?", out); err != nil {
		t.Fatal(err)
	}
}

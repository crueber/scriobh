package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
)

// An archive made without files/ replaces the database (and the trees it
// carries) and leaves everything else where it is. It used to move the live
// files/ aside, install nothing and delete the moved copy.
func TestAPartialArchiveReplacesOnlyWhatItCarries(t *testing.T) {
	src := newData(t)
	src.project("Alpha", 2)
	src.populate()
	archive := src.create(backup.Options{NoFiles: true, NoConflicts: true})

	dst := newData(t)
	dst.project("Live", 1)
	dst.file("files/projects/live/attachments/cover.png", "LIVE PICTURE")
	dst.file("conflicts/live/doc.md", "LIVE CONFLICT COPY")
	dst.file("mirror/live-book--zzzzzz/project.json", "{}")
	dst.d.Close()

	res, err := backup.Restore(context.Background(), archive.Path, dst.dir, backup.RestoreOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"files/projects/live/attachments/cover.png": "LIVE PICTURE",
		"conflicts/live/doc.md":                     "LIVE CONFLICT COPY",
	} {
		b, err := os.ReadFile(filepath.Join(dst.dir, filepath.FromSlash(rel)))
		if err != nil || string(b) != want {
			t.Errorf("%s was not left alone: %q %v", rel, b, err)
		}
	}
	// The mirror was carried: replaced by the archive's.
	if _, err := os.Stat(filepath.Join(dst.dir, "mirror", "live-book--zzzzzz")); !os.IsNotExist(err) {
		t.Errorf("the carried mirror was not replaced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst.dir, "mirror", "the-book--abc123", "manuscript", "01-one.md")); err != nil {
		t.Errorf("the archive's mirror was not installed: %v", err)
	}
	if got := count(t, filepath.Join(dst.dir, "scriobh.db"), `SELECT count(*) FROM projects WHERE title = 'Alpha'`); got != 1 {
		t.Errorf("the database was not restored (%d)", got)
	}
	if res.PreRestore == "" {
		t.Error("no pre-restore backup was taken")
	}
	// And nothing is left half-moved.
	if entries, _ := filepath.Glob(filepath.Join(dst.dir, ".restore-*")); len(entries) != 0 {
		t.Errorf("leftovers: %v", entries)
	}
}

// A damaged live database is the usual reason to restore; it must not stop the
// restore by making the pre-restore backup fail.
func TestARestoreOverADamagedDatabaseGoesAheadAndKeepsWhatWasThere(t *testing.T) {
	src := newData(t)
	src.project("Alpha", 2)
	src.populate()
	archive := src.create(backup.Options{})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scriobh.db"), []byte("this is not a database, it is garbage that sqlite cannot open at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "files", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files", "x", "old.txt"), []byte("old file"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := backup.Restore(context.Background(), archive.Path, dir, backup.RestoreOptions{Force: true})
	if err != nil {
		t.Fatalf("restore over a damaged database: %v", err)
	}
	if res.PreRestoreError == "" || res.PreRestore != "" || res.KeptAside == "" {
		t.Fatalf("result = %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(res.KeptAside, "scriobh.db"))
	if err != nil || !strings.Contains(string(b), "garbage") {
		t.Errorf("the damaged database was not kept: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(res.KeptAside, "files", "x", "old.txt")); err != nil || string(b) != "old file" {
		t.Errorf("the old files were not kept: %q %v", b, err)
	}
	if got := count(t, filepath.Join(dir, "scriobh.db"), `SELECT count(*) FROM projects`); got != 1 {
		t.Errorf("restored projects = %d", got)
	}
}

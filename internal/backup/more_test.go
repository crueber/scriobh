package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

// A written archive always restores: names restore would refuse (a colon or a
// backslash, both legal on Linux) are left out and counted, not archived.
func TestCreateNeverWritesAnArchiveRestoreRefuses(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	x.file("files/imports/ok.txt", "fine")
	x.file("files/imports/a:b.txt", "colon")
	x.file("files/imports/back\\slash.txt", "backslash")
	r := x.create(backup.Options{})
	if r.Skipped != 2 {
		t.Errorf("skipped %d, want 2", r.Skipped)
	}
	names := zipNames(t, r.Path)
	if !has(names, "files/imports/ok.txt") {
		t.Errorf("the ordinary file is missing: %v", names)
	}
	for _, n := range names {
		if strings.ContainsAny(n, ":\\") {
			t.Errorf("an unrestorable name was archived: %q", n)
		}
	}
	if _, err := backup.Restore(context.Background(), r.Path, t.TempDir(), backup.RestoreOptions{}); err != nil {
		t.Fatalf("the archive does not restore: %v", err)
	}
}

// The size cap bounds what is written, not only what is checked afterwards: a
// manifest that declares a huge entry is refused before anything is extracted.
func TestACraftedSizeIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	snap := filepath.Join(t.TempDir(), "s.db")
	if err := db.Snapshot(context.Background(), x.d.Path, snap); err != nil {
		t.Fatal(err)
	}
	dbBytes, _ := os.ReadFile(snap)
	counts := backup.Counts{Projects: 1, Documents: 2}
	lying := craft(t, dbBytes, counts, []zent{{name: "files/a.txt", data: "hi"}}, func(m *backup.Manifest) { m.Entries[1].Size = 1 << 40 })
	dst := t.TempDir()
	_, err := backup.Restore(context.Background(), lying, dst, backup.RestoreOptions{MaxBytes: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("a manifest declaring 1 TiB: %v", err)
	}
	// Refused before staging was even filled: nothing is left behind.
	if m, _ := filepath.Glob(filepath.Join(dst, ".restore-*")); len(m) != 0 {
		t.Errorf("leftover %v", m)
	}
	if _, err := os.Stat(filepath.Join(dst, "scriobh.db")); err == nil {
		t.Error("a database was installed")
	}
}

// A backup killed part way leaves a work directory and a partial archive; the
// next one sweeps old ones and leaves fresh ones alone.
func TestLeftoversOfAKilledBackupAreSwept(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	dir := filepath.Join(x.dir, "backups")
	os.MkdirAll(filepath.Join(dir, ".work-old"), 0o700)
	os.WriteFile(filepath.Join(dir, ".work-old", "scriobh.db"), []byte("a full copy"), 0o600)
	os.WriteFile(filepath.Join(dir, "x.zip.partial"), []byte("half"), 0o600)
	os.MkdirAll(filepath.Join(dir, ".work-fresh"), 0o700)
	old := time.Now().Add(-3 * time.Hour)
	os.Chtimes(filepath.Join(dir, ".work-old"), old, old)
	os.Chtimes(filepath.Join(dir, "x.zip.partial"), old, old)
	x.create(backup.Options{})
	for _, gone := range []string{".work-old", "x.zip.partial"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("%s survived", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".work-fresh")); err != nil {
		t.Error("a fresh work directory (maybe another process's) was removed")
	}
}

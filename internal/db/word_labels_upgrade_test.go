package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Migration 1820 adds a label column to suggestions. A database
// from the last release takes it, and a row written before it has an empty
// label (so it reads as it always did).
func TestV05DatabaseGainsTheAuthorLabelColumns(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "v0.5.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "scriobh.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"suggestions"} {
		var n int
		if err := d.Read().QueryRow(`SELECT count(*) FROM pragma_table_info('` + tbl + `') WHERE name = 'author_label' AND "notnull" = 1 AND dflt_value = "''"`).Scan(&n); err != nil || n != 1 {
			t.Errorf("%s: author_label column: %d %v", tbl, n, err)
		}
		var labelled int
		if err := d.Read().QueryRow(`SELECT count(*) FROM ` + tbl + ` WHERE author_label <> ''`).Scan(&labelled); err != nil || labelled != 0 {
			t.Errorf("%s: %d rows with a label after the upgrade", tbl, labelled)
		}
	}
}

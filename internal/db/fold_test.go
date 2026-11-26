package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// scr_fold is the search's folding as a SQL function. It is registered when the package loads, so both pools of an opened database have it:
// the writer runs the search too, and a query that ran on one pool and not the other would be a silent miss.
func TestScrFoldIsOnEveryPool(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "fold.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for name, pool := range map[string]*sql.DB{"the writer": d.Writer(), "a reader": d.Read()} {
		var got string
		if err := pool.QueryRow(`SELECT scr_fold('ÉIRE Máire')`).Scan(&got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if want := "éire máire"; got != want {
			t.Errorf("%s: scr_fold = %q, want %q", name, got, want)
		}
	}
}

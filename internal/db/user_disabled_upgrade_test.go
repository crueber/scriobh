package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Migration 0110 adds the disabled state to users of an existing database without touching anyone: nobody is switched
// off by an upgrade, and the people who were there are still there. (The table-structure equivalence of a fresh install
// and an upgraded one is TestUpgradedDatabasesMatchAFreshInstall, which covers the new columns too.)
func TestUsersGainTheDisabledStateWithoutChange(t *testing.T) {
	for _, name := range []string{"phase0.db", "v0.1.db", "v0.2.db"} {
		src, err := os.ReadFile(filepath.Join("testdata", name))
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
		var before int
		_ = d.Read().QueryRow(`SELECT count(*) FROM users`).Scan(&before)
		if _, err := d.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
			t.Fatal(err)
		}
		var after, off int
		if err := d.Read().QueryRow(`SELECT count(*), count(disabled_at) FROM users`).Scan(&after, &off); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if after != before || off != 0 {
			t.Errorf("%s: %d users before, %d after, %d switched off", name, before, after, off)
		}
		d.Close()
	}
}

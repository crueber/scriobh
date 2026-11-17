package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Migration 1910 adds two tables for custom typography (F-085) and changes nothing that exists: after an upgrade from a
// v0.5 database every project and person is where it was, no project has typography (so every page renders as before),
// and deleting a project or a person removes their rows. (Table structure against a fresh install is
// TestUpgradedDatabasesMatchAFreshInstall.)
func TestTypographyTablesAreNewAndEmptyAfterAnUpgrade(t *testing.T) {
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
	var users, projects int
	_ = d.Read().QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	_ = d.Read().QueryRow(`SELECT count(*) FROM projects`).Scan(&projects)
	if _, err := d.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	var u2, p2, pt, ut int
	_ = d.Read().QueryRow(`SELECT count(*) FROM users`).Scan(&u2)
	_ = d.Read().QueryRow(`SELECT count(*) FROM projects`).Scan(&p2)
	if err := d.Read().QueryRow(`SELECT (SELECT count(*) FROM project_typography), (SELECT count(*) FROM user_typography)`).Scan(&pt, &ut); err != nil {
		t.Fatal(err)
	}
	if u2 != users || p2 != projects || pt != 0 || ut != 0 {
		t.Fatalf("users %d->%d, projects %d->%d, typography rows %d and %d", users, u2, projects, p2, pt, ut)
	}
	// the rows follow their owner: a project's typography goes with the project (foreign keys are on for every connection)
	var pid string
	if err := d.Read().QueryRow(`SELECT id FROM projects LIMIT 1`).Scan(&pid); err != nil {
		t.Skipf("the fixture has no project: %v", err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO project_typography(project_id, settings_json, updated_at) VALUES (?, '{"size":110}', 'x')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`DELETE FROM projects WHERE id = ?`, pid); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.Read().QueryRow(`SELECT count(*) FROM project_typography WHERE project_id = ?`, pid).Scan(&n)
	if n != 0 {
		t.Fatal("a project's typography outlived the project")
	}
}

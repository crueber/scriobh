package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// schemaOf is DescribeSchema for a test.
func schemaOf(t *testing.T, d *DB) map[string]string {
	t.Helper()
	out, err := DescribeSchema(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A database that went through each release's migrations must end up with the
// same tables, columns, indexes and triggers as a fresh install. Migrations in
// lower-numbered ranges run before 0900 and 1000 on a fresh install but after
// them on an upgrade, so a table rebuild would silently drop a trigger on one
// path only; this test is what notices.
func TestUpgradedDatabasesMatchAFreshInstall(t *testing.T) {
	ctx := context.Background()
	fresh := openTemp(t)
	if _, err := fresh.Migrate(ctx, ""); err != nil {
		t.Fatal(err)
	}
	want := schemaOf(t, fresh)

	for _, name := range releaseFixtures {
		t.Run(name, func(t *testing.T) {
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
			defer d.Close()
			if _, err := d.Migrate(ctx, filepath.Join(dir, "backups")); err != nil {
				t.Fatal(err)
			}
			got := schemaOf(t, d)
			for k, w := range want {
				if g, ok := got[k]; !ok {
					t.Errorf("%s is missing after the upgrade", k)
				} else if g != w {
					t.Errorf("%s differs after the upgrade:\n  fresh:    %s\n  upgraded: %s", k, w, g)
				}
			}
			for k := range got {
				if _, ok := want[k]; !ok {
					t.Errorf("%s exists only after the upgrade", k)
				}
			}
		})
	}
}

// The v0.2 database keeps its data through every later migration.
func TestV02DatabaseUpgrades(t *testing.T) {
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/v0.2.db")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "scriobh.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	res, err := d.Migrate(context.Background(), filepath.Join(dir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) > 0 && res.Backup == "" {
		t.Errorf("migrations were applied to a database with data but no pre-flight backup was taken: %+v", res)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM documents`:                              5,
		`SELECT count(*) FROM document_revisions`:                     4,
		`SELECT count(*) FROM document_links`:                         1,
		`SELECT count(*) FROM document_links WHERE to_id IS NOT NULL`: 1,
		`SELECT count(*) FROM goals`:                                  1,
		`SELECT count(*) FROM writing_sessions`:                       1,
		`SELECT count(*) FROM daily_stats`:                            1,
		`SELECT count(*) FROM git_remotes`:                            1,
		`SELECT count(*) FROM project_sources`:                        1,
		`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'harbour'`:  2,
	} {
		var n int
		if err := d.Read().QueryRow(q).Scan(&n); err != nil || n != want {
			t.Errorf("%s = %d, %v; want %d", q, n, err, want)
		}
	}
	var chk string
	if err := d.Read().QueryRow(`PRAGMA integrity_check`).Scan(&chk); err != nil || chk != "ok" {
		t.Errorf("integrity_check = %q, %v", chk, err)
	}
	var _ sql.NullString
	rows, err := d.Read().Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Error("foreign_key_check reports violations after the upgrade")
	}
}

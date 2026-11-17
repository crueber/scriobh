package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Migration 1890 adds the semantic index's tables and 1895 rebuilds exports so that
// the formats Pandoc writes can be recorded. A database written by the v0.5 release,
// with exports in it, keeps every one of them exactly, accepts the new formats, and
// still refuses a format that is not on the list; no project gets an index.
func TestV05DatabaseKeepsItsExportsThroughTheSemanticAndPandocMigrations(t *testing.T) {
	ctx := context.Background()
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
	var project, user string
	if err := d.Read().QueryRowContext(ctx, `SELECT p.id, m.user_id FROM projects p JOIN project_members m ON m.project_id = p.id ORDER BY p.id LIMIT 1`).Scan(&project, &user); err != nil {
		t.Fatal(err)
	}
	var docs int
	_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&docs)
	for i, f := range []string{"md", "docx", "pdf", "epub", "html"} {
		if _, err := d.Writer().ExecContext(ctx, `INSERT INTO exports(id, project_id, user_id, preset, format, filename, bytes, sha256, manifest_json, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			"exp_"+f, project, user, "preset", f, "book."+f, 100+i, "abc"+f, `{"k":1}`, "2025-01-0"+string(rune('1'+i))+"T00:00:00.000Z"); err != nil {
			t.Fatal(err)
		}
	}
	var before string
	if err := d.Read().QueryRowContext(ctx, `SELECT group_concat(id||format||filename||bytes||sha256||manifest_json||created_at, '|') FROM (SELECT * FROM exports ORDER BY id)`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(ctx, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := d.Read().QueryRowContext(ctx, `SELECT group_concat(id||format||filename||bytes||sha256||manifest_json||created_at, '|') FROM (SELECT * FROM exports ORDER BY id)`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Errorf("the exports changed:\n%s\n%s", before, after)
	}
	for _, f := range []string{"odt", "rtf", "latex", "rst", "mediawiki"} {
		if _, err := d.Writer().ExecContext(ctx, `INSERT INTO exports(id, project_id, user_id, preset, format, filename, created_at) VALUES (?,?,?,'p',?,?,'t')`, "new_"+f, project, user, f, "b."+f); err != nil {
			t.Errorf("%s was not accepted: %v", f, err)
		}
	}
	if _, err := d.Writer().ExecContext(ctx, `INSERT INTO exports(id, project_id, user_id, preset, format, filename, created_at) VALUES ('bad',?,?,'p','exe','b.exe','t')`, project, user); err == nil {
		t.Error("a format that is not on the list was accepted")
	}
	var idx int
	_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'exports_project'`).Scan(&idx)
	if idx != 1 {
		t.Error("the exports index was lost")
	}
	// The semantic tables exist and are empty: no project has an index it did not ask for.
	for _, table := range []string{"embed_projects", "embed_docs", "embed_chunks"} {
		var n int
		if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows, %v", table, n, err)
		}
	}
	var after2 int
	_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM documents`).Scan(&after2)
	if after2 != docs {
		t.Errorf("documents: %d before, %d after", docs, after2)
	}
	var check string
	if err := d.Read().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Errorf("integrity_check = %q %v", check, err)
	}
	rows, err := d.Read().QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			t.Error("foreign_key_check found a violation")
		}
	}
}

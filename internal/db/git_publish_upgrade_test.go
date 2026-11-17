package db

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// Migrations 1830 to 1834 change git_remotes and add the tables of publishing. A database written by the v0.5
// release, with a remote in it made the old way, keeps that remote exactly as it was and is read as a private
// one: it was already pushing the whole mirror, and the upgrade must not change what it pushes.
func TestV05DatabaseKeepsItsRemotesAsPrivateThroughThePublishingMigrations(t *testing.T) {
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
	var project string
	if err := d.Read().QueryRowContext(ctx, `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	var hasRemotes int
	_ = d.Read().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name = 'git_remotes'`).Scan(&hasRemotes)
	if hasRemotes == 0 {
		t.Skip("the v0.5 fixture predates the Git tables")
	}
	// A remote as it was made before these migrations: no audience, no signing, no creator.
	if _, err := d.Writer().ExecContext(ctx, `INSERT INTO git_remotes(id, project_id, name, url, include_notes, created_at, updated_at) VALUES ('grm_old', ?, 'Backup', 'https://forge.example.org/o/r.git', 1, '2025-01-01T00:00:00.000Z', '2025-01-01T00:00:00.000Z')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(ctx, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	var audience, via string
	var onlyDone, sign, notes int
	if err := d.Read().QueryRowContext(ctx, `SELECT audience, only_done, sign_commits, created_via, include_notes FROM git_remotes WHERE id = 'grm_old'`).Scan(&audience, &onlyDone, &sign, &via, &notes); err != nil {
		t.Fatal(err)
	}
	if audience != "private" || onlyDone != 0 || sign != 0 || via != "" || notes != 1 {
		t.Errorf("the old remote reads as audience=%q only_done=%d sign=%d via=%q notes=%d", audience, onlyDone, sign, via, notes)
	}
	for _, table := range []string{"git_accounts", "git_releases", "git_triggers", "git_trigger_deliveries", "git_signing_keys"} {
		var n int
		if err := d.Read().QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
	// The one-running-release rule is in the database.
	if _, err := d.Writer().ExecContext(ctx, `INSERT INTO git_releases(id, remote_id, project_id, tag, format, status, started_at) VALUES ('a','grm_old',?,'v1','epub','running','t')`, project); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().ExecContext(ctx, `INSERT INTO git_releases(id, remote_id, project_id, tag, format, status, started_at) VALUES ('b','grm_old',?,'v1','epub','running','t')`, project); err == nil {
		t.Error("two releases of one tag ran at once")
	}
	var check string
	if err := d.Read().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Errorf("integrity_check = %q %v", check, err)
	}
	var rows *sql.Rows
	if rows, err = d.Read().QueryContext(ctx, `PRAGMA foreign_key_check`); err == nil {
		defer rows.Close()
		if rows.Next() {
			t.Error("foreign_key_check found a violation")
		}
	}
}

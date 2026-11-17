package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

// `scriobh restore` that cannot pause mail says so on stderr and exits non-zero, so a script stops.
func TestRestoreCommandFailsLoudlyWhenMailCannotBePaused(t *testing.T) {
	src := t.TempDir()
	d, err := dbOpenMigrated(t, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO mail_config(id, enabled, host, port, security, from_address, updated_at) VALUES (1, 1, 'mail.example.org', 587, 'starttls', 'a@example.org', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`CREATE TRIGGER no_pause BEFORE UPDATE ON mail_config BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	r, err := backup.Create(context.Background(), filepath.Join(src, "scriobh.db"), src, backup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dst)
	t.Setenv("SCRIOBH_SECRET_KEY", "test-secret-key-test-secret-key-0123456789")
	var out, errb bytes.Buffer
	code := restoreCmd([]string{r.Path}, &out, &errb)
	if code == 0 {
		t.Fatalf("exit %d, stdout %s", code, out.String())
	}
	for _, want := range []string{"restore failed", "mail is NOT paused", "mail.restore-pause"} {
		if !strings.Contains(errb.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, errb.String())
		}
	}
	if !strings.Contains(out.String(), "the data was restored, but:") {
		t.Errorf("stdout: %s", out.String())
	}
}

func dbOpenMigrated(t *testing.T, dir string) (*db.DB, error) {
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { d.Close() })
	_, err = d.Migrate(context.Background(), "")
	return d, err
}

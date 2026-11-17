package backup_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

// The real Restore calls the install hooks after the files are in place and before the steps, runs every step even
// when a hook failed, and reports both.
func TestRestoreRunsInstallHooksBeforeTheStepsAndEveryStepEvenIfAHookFailed(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	r := x.create(backup.Options{})
	oldInstall, oldSteps := backup.PostInstall, backup.PostRestore
	defer func() { backup.PostInstall, backup.PostRestore = oldInstall, oldSteps }()
	var order []string
	backup.PostInstall = append(append([]func(string) error(nil), oldInstall...), func(dir string) error {
		if _, err := os.Stat(filepath.Join(dir, "scriobh.db")); err != nil {
			t.Errorf("the install hook ran before the database was in place: %v", err)
		}
		order = append(order, "install")
		return nil
	})
	backup.PostRestore = append(append([]func(context.Context, *db.DB, []byte) (string, error)(nil), oldSteps...), func(context.Context, *db.DB, []byte) (string, error) {
		order = append(order, "step")
		return "", nil
	})
	dst := t.TempDir()
	if _, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{}); err != nil {
		t.Fatal(err)
	}
	// The steps run twice: on the staged database before the files are installed (no window with senders on), and on the
	// installed one after the install hooks.
	if strings.Join(order, ",") != "step,install,step" {
		t.Errorf("order = %v", order)
	}
	// A hook that fails: the restore reports it, and the steps still ran (a step is what pauses).
	order = nil
	backup.PostInstall = append(append([]func(string) error(nil), oldInstall...), func(string) error { return errors.New("read-only file system") })
	dst2 := t.TempDir()
	_, err := backup.Restore(context.Background(), r.Path, dst2, backup.RestoreOptions{})
	if err == nil || !strings.Contains(err.Error(), "safety marker could not be written") {
		t.Fatalf("a failing hook: %v", err)
	}
	if strings.Join(order, ",") != "step,step" {
		t.Errorf("the steps did not all run after a failed hook: %v", order)
	}
}

// A mail step that cannot pause mail leaves the marker, fails the restore, and says so.
func TestRestoreSaysMailIsNotPausedWhenItCannotPauseIt(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	if _, err := x.d.Writer().Exec(`INSERT INTO mail_config(id, enabled, host, port, security, from_address, updated_at) VALUES (1, 1, 'mail.example.org', 587, 'starttls', 'a@example.org', 'x')`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.d.Writer().Exec(`CREATE TRIGGER no_pause BEFORE UPDATE ON mail_config BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatal(err)
	}
	r := x.create(backup.Options{})
	dst := t.TempDir()
	res, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{})
	if err == nil || res == nil {
		t.Fatalf("the restore said nothing was wrong: %v", err)
	}
	for _, want := range []string{"mail is NOT paused", "UPDATE mail_config SET enabled = 0", "mail.restore-pause"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "mail.restore-pause")); err != nil {
		t.Errorf("the marker is gone although mail is not paused: %v", err)
	}
}

package backup_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

type installHook = func(string) error
type stepFn = func(context.Context, *db.DB, []byte) (string, error)

// A hook or a step that panics is one failure among others: every hook and every step after it still runs, and the
// restore reports the panic with the rest. A pause that is skipped because an earlier step crashed would leave a
// copy that sends.
func TestAPanickingHookOrStepCannotSkipTheOthers(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	r := x.create(backup.Options{})
	oldInstall, oldSteps := backup.PostInstall, backup.PostRestore
	defer func() { backup.PostInstall, backup.PostRestore = oldInstall, oldSteps }()
	var order []string
	backup.PostInstall = []installHook{
		func(string) error { order = append(order, "install-1"); return nil },
		func(string) error { order = append(order, "install-2"); panic("a broken marker writer") },
		func(string) error { order = append(order, "install-3"); return nil },
	}
	backup.PostRestore = []stepFn{
		func(context.Context, *db.DB, []byte) (string, error) {
			order = append(order, "step-1")
			return "one", nil
		},
		func(context.Context, *db.DB, []byte) (string, error) {
			order = append(order, "step-2")
			var m map[string]int
			m["x"] = 1 // a nil map write: a runtime panic, not one a test raised on purpose
			return "", nil
		},
		func(context.Context, *db.DB, []byte) (string, error) {
			order = append(order, "step-3")
			return "three", nil
		},
	}
	res, err := backup.Restore(context.Background(), r.Path, t.TempDir(), backup.RestoreOptions{})
	if err == nil || res == nil {
		t.Fatalf("a panicking hook and step were reported as a clean restore: %v", err)
	}
	if got := strings.Join(order, ","); got != "step-1,step-2,step-3,install-1,install-2,install-3,step-1,step-2,step-3" {
		t.Errorf("order = %s", got)
	}
	for _, want := range []string{"install hook 2", "a broken marker writer", "restore step 2", "may NOT be paused"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	if len(res.Notes) != 2 || res.Notes[0] != "one" || res.Notes[1] != "three" {
		t.Errorf("notes = %v", res.Notes)
	}
}

// The restored database is opened a second time when the first try fails; when both fail the restore says, in
// words, that nothing was paused and what to do.
func TestRestoreRetriesTheReopenAndSaysWhatIsNotPausedWhenItCannot(t *testing.T) {
	x := newData(t)
	x.project("Alpha", 1)
	r := x.create(backup.Options{})
	oldSteps := backup.PostRestore
	defer func() { backup.PostRestore = oldSteps }()
	var ran atomic.Int32
	backup.PostRestore = []stepFn{func(context.Context, *db.DB, []byte) (string, error) { ran.Add(1); return "", nil }}

	// The first try fails, the second works: the restore is complete and the steps ran.
	var tries atomic.Int32
	restore := backup.SetOpenDB(func(p string) (*db.DB, error) {
		if tries.Add(1) == 1 {
			return nil, errors.New("database is locked")
		}
		return db.Open(p)
	})
	if _, err := backup.Restore(context.Background(), r.Path, t.TempDir(), backup.RestoreOptions{}); err != nil {
		t.Fatalf("a transient failure to reopen failed the restore: %v", err)
	}
	restore()
	// The staged database opened on its second try, the installed one at once: three opens, and the step ran on each.
	if tries.Load() != 3 || ran.Load() != 2 {
		t.Errorf("tries = %d, steps run = %d", tries.Load(), ran.Load())
	}

	// Both tries fail.
	ran.Store(0)
	restore = backup.SetOpenDB(func(string) (*db.DB, error) { return nil, errors.New("disk I/O error") })
	defer restore()
	dst := t.TempDir()
	res, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{})
	if err == nil || res == nil {
		t.Fatalf("an unreadable database was reported as a clean restore: %v", err)
	}
	for _, want := range []string{"NOT paused", "Webhooks", "Git remotes", "Git triggers", "Pandoc", "Mail", "semantic", "refuse to run", "restore.pending", "disk I/O error"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q:\n%v", want, err)
		}
	}
	if ran.Load() != 0 {
		t.Error("steps ran without a database")
	}
	// What can pause itself without the database left its markers.
	for _, m := range []string{"mail.restore-pause", "semantic.restore-pause", backup.MarkerName} {
		if _, err := os.Stat(filepath.Join(dst, m)); err != nil {
			t.Errorf("marker %s: %v", m, err)
		}
	}
}

// However the restore is cancelled, the installed database is already paused: the steps run on the staged database before
// anything is installed and cannot be cancelled, so there is no boundary (a step, a hook) at which a cancel leaves a
// sender on. An interrupted restore leaves the marker that makes the next start finish; an uninterrupted one leaves none.
func TestCancelAtEveryBoundaryLeavesTheInstalledCopyPaused(t *testing.T) {
	oldInstall, oldSteps := backup.PostInstall, backup.PostRestore
	defer func() { backup.PostInstall, backup.PostRestore = oldInstall, oldSteps }()
	pause := func(name string, cancel func()) stepFn {
		return func(ctx context.Context, d *db.DB, _ []byte) (string, error) {
			if cancel != nil {
				cancel()
			}
			// A write that fails once the context is cancelled, as every step's own transaction does.
			_, err := d.Writer().ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, 'paused', 't') ON CONFLICT(key) DO UPDATE SET value = 'paused'`, "fake."+name)
			return "", err
		}
	}
	// Boundaries: before step k of the staged phase (k = 0..2), and before each of two install hooks.
	for _, tc := range []struct {
		name        string
		cancelStep  int // -1: none
		cancelHook  int // -1: none
		wantMarker  bool
		wantSuccess bool
	}{
		{"no cancel", -1, -1, false, true},
		{"cancel in the first step", 0, -1, true, false},
		{"cancel in the second step", 1, -1, true, false},
		{"cancel in the last step", 2, -1, true, false},
		{"cancel in the first install hook", -1, 0, true, false},
		{"cancel in the second install hook", -1, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := newData(t)
			x.project("Alpha", 1)
			r := x.create(backup.Options{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var steps []stepFn
			for k := 0; k < 3; k++ {
				var c func()
				if k == tc.cancelStep {
					c = cancel
				}
				steps = append(steps, pause(fmt.Sprint(k), c))
			}
			var hooks []installHook
			for j := 0; j < 2; j++ {
				j := j
				hooks = append(hooks, func(string) error {
					if j == tc.cancelHook {
						cancel()
					}
					return nil
				})
			}
			backup.PostRestore, backup.PostInstall = steps, hooks
			dst := t.TempDir()
			_, err := backup.Restore(ctx, r.Path, dst, backup.RestoreOptions{})
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("error = %v, want success %v", err, tc.wantSuccess)
			}
			if tc.cancelStep >= 0 && err != nil && !strings.Contains(err.Error(), "interrupted") {
				t.Errorf("the error does not say the restore was interrupted: %v", err)
			}
			d, err := db.Open(filepath.Join(dst, "scriobh.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			var n int
			if err := d.Read().QueryRow(`SELECT count(*) FROM settings WHERE key LIKE 'fake.%' AND value = 'paused'`).Scan(&n); err != nil || n != 3 {
				t.Errorf("the installed database holds %d of 3 pauses (%v): a cancel left a sender on", n, err)
			}
			_, serr := os.Stat(filepath.Join(dst, backup.MarkerName))
			if (serr == nil) != tc.wantMarker {
				t.Errorf("marker present = %v, want %v", serr == nil, tc.wantMarker)
			}
		})
	}
}

// The marker is honoured by FinishPending: it runs the steps and removes the marker only when every step succeeded, and
// a second restore over an interrupted one ends clean.
func TestFinishPendingAndASecondRestoreOverAnInterruptedOne(t *testing.T) {
	oldInstall, oldSteps := backup.PostInstall, backup.PostRestore
	defer func() { backup.PostInstall, backup.PostRestore = oldInstall, oldSteps }()
	var fail atomic.Bool
	backup.PostInstall = nil
	backup.PostRestore = []stepFn{func(ctx context.Context, d *db.DB, _ []byte) (string, error) {
		if fail.Load() {
			return "", errors.New("database is locked")
		}
		_, err := d.Writer().ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES ('fake.p', 'paused', 't') ON CONFLICT(key) DO UPDATE SET value = 'paused'`)
		return "paused", err
	}}
	x := newData(t)
	x.project("Alpha", 1)
	r := x.create(backup.Options{})
	dst := t.TempDir()

	// Interrupted: the cancel comes before the live phase.
	ctx, cancel := context.WithCancel(context.Background())
	backup.PostInstall = []installHook{func(string) error { cancel(); return nil }}
	if _, err := backup.Restore(ctx, r.Path, dst, backup.RestoreOptions{}); err == nil {
		t.Fatal("an interrupted restore reported success")
	}
	backup.PostInstall = nil
	marker := filepath.Join(dst, backup.MarkerName)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no marker after an interrupted restore: %v", err)
	}

	// FinishPending that cannot pause keeps the marker and says what is not paused; one that can removes it.
	d, err := db.Open(filepath.Join(dst, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	fail.Store(true)
	pending, _, ferr := backup.FinishPending(context.Background(), d, dst)
	if !pending || ferr == nil || !strings.Contains(ferr.Error(), "NOT paused") || !strings.Contains(ferr.Error(), "database is locked") {
		t.Fatalf("FinishPending with a failing step: pending %v, %v", pending, ferr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the marker was removed although a pause failed")
	}
	fail.Store(false)
	if pending, notes, ferr := backup.FinishPending(context.Background(), d, dst); !pending || ferr != nil || len(notes) != 1 {
		t.Fatalf("FinishPending: pending %v notes %v err %v", pending, notes, ferr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the marker stayed after every pause was made")
	}
	if pending, _, _ := backup.FinishPending(context.Background(), d, dst); pending {
		t.Fatal("FinishPending found a marker that was removed")
	}
	d.Close()

	// A second restore over an interrupted one: interrupt again, then restore with Force and no interruption.
	ctx2, cancel2 := context.WithCancel(context.Background())
	backup.PostInstall = []installHook{func(string) error { cancel2(); return nil }}
	if _, err := backup.Restore(ctx2, r.Path, dst, backup.RestoreOptions{Force: true}); err == nil {
		t.Fatal("a second interrupted restore reported success")
	}
	backup.PostInstall = nil
	res, err := backup.Restore(context.Background(), r.Path, dst, backup.RestoreOptions{Force: true})
	if err != nil || res == nil {
		t.Fatalf("the restore over the interrupted one: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the marker of the interrupted restore is still there after a clean one")
	}
	d, err = db.Open(filepath.Join(dst, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var v string
	if err := d.Read().QueryRow(`SELECT value FROM settings WHERE key = 'fake.p'`).Scan(&v); err != nil || v != "paused" {
		t.Errorf("after the second restore: %q %v", v, err)
	}
}

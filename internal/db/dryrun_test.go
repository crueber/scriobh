package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openDryDB(t *testing.T) *DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Writer().Exec(`CREATE TABLE t(id TEXT PRIMARY KEY, n INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	return d
}

func count(t *testing.T, d *DB) int {
	t.Helper()
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestDryRunSeesItsOwnWritesAndRollsBack: an op that writes in one WithTx,
// reads the row back through Read, and writes again sees all of it, and none
// of it survives.
func TestDryRunSeesItsOwnWritesAndRollsBack(t *testing.T) {
	d := openDryDB(t)
	ctx := context.Background()
	if err := d.WithTx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`INSERT INTO t(id) VALUES ('real')`); return err }); err != nil {
		t.Fatal(err)
	}
	changes, err := d.DryRun(ctx, func(v *DB) error {
		if err := v.WithTx(ctx, func(tx *sql.Tx) error {
			// A read from the reader pool while a transaction is open must not deadlock.
			if got := count(t, v); got != 1 {
				t.Errorf("inside the tx, saw %d rows, want 1", got)
			}
			_, err := tx.Exec(`INSERT INTO t(id) VALUES ('a'),('b')`)
			return err
		}); err != nil {
			return err
		}
		if got := count(t, v); got != 3 {
			t.Errorf("after the tx the view saw %d rows, want 3", got)
		}
		return v.WithTx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE t SET n = 5 WHERE id = 'a'`); return err })
	})
	if err != nil {
		t.Fatal(err)
	}
	if changes != 3 {
		t.Errorf("changes = %d, want 3", changes)
	}
	if got := count(t, d); got != 1 {
		t.Errorf("after the dry run the database holds %d rows, want 1", got)
	}
	// The writer is free again.
	if err := d.WithTx(ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`INSERT INTO t(id) VALUES ('after')`); return err }); err != nil {
		t.Fatalf("writer after a dry run: %v", err)
	}
}

func TestDryRunErrorsAndFailedTx(t *testing.T) {
	d := openDryDB(t)
	ctx := context.Background()
	boom := errors.New("boom")
	_, err := d.DryRun(ctx, func(v *DB) error {
		if err := v.WithTx(ctx, func(tx *sql.Tx) error {
			_, _ = tx.Exec(`INSERT INTO t(id) VALUES ('x')`)
			return boom
		}); !errors.Is(err, boom) {
			t.Errorf("WithTx err = %v", err)
		}
		if got := count(t, v); got != 0 {
			t.Errorf("a failed transaction left %d rows in the view", got)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Errorf("err %v", err)
	}
	// A constraint failure inside the view is an error, as in the real thing.
	_, err = d.DryRun(ctx, func(v *DB) error {
		return v.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`INSERT INTO t(id) VALUES ('d')`); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO t(id) VALUES ('d')`)
			return err
		})
	})
	if !IsUniqueViolation(err) {
		t.Errorf("want a unique violation, got %v", err)
	}
	if got := count(t, d); got != 0 {
		t.Errorf("rows after: %d", got)
	}
}

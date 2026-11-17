package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Snapshot copies a live database without holding the writer: saves keep going
// while it runs, and the copy is a consistent, openable database.
func TestSnapshotDoesNotBlockTheWriter(t *testing.T) {
	dir := t.TempDir()
	d, err := Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Writer().Exec(`CREATE TABLE t(id INTEGER PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 200_000)
	for i := 0; i < 100; i++ {
		if _, err := d.Writer().Exec(`INSERT INTO t(body) VALUES (?)`, blob); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var saves int
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := d.Writer().Exec(`INSERT INTO t(body) VALUES (?)`, "save"); err != nil {
				t.Errorf("a save failed during a snapshot: %v", err)
				return
			}
			saves++
			time.Sleep(time.Millisecond)
		}
	}()
	dest := filepath.Join(dir, "out", "snap.db")
	err = Snapshot(context.Background(), d.Path, dest)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if saves == 0 {
		t.Error("no save went through while the snapshot ran")
	}
	if fi, err := os.Stat(dest); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file: %v %v", fi, err)
	}
	c, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.Read().QueryRow(`SELECT count(*) FROM t`).Scan(&n); err != nil || n < 100 {
		t.Fatalf("snapshot rows %d err %v", n, err)
	}
	var check string
	if err := c.Read().QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity %q %v", check, err)
	}
	if err := Snapshot(context.Background(), d.Path, dest); err == nil {
		t.Error("a snapshot must not overwrite a file")
	}
	if err := Snapshot(context.Background(), filepath.Join(dir, "missing.db"), filepath.Join(dir, "x.db")); err == nil {
		t.Error("a snapshot of nothing must fail")
	}
	if _, err := os.Stat(filepath.Join(dir, "missing.db")); err == nil {
		t.Error("a snapshot of nothing created a database")
	}
}

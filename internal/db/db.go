// Package db owns the SQLite connection, the embedded migrations, IDs,
// timestamps and sibling positions.
//
// One writer, one replica: all writes go through a single connection with
// IMMEDIATE transactions; reads use a small separate pool. WAL means readers
// never block the writer.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO
)

// acquireTimeout bounds the wait for the single writer connection.
var acquireTimeout = 5 * time.Second

// ErrWriterBusy means the writer connection could not be acquired in time:
// either the database is badly overloaded or a transaction was nested inside
// another one, which would otherwise deadlock.
var ErrWriterBusy = errors.New("db: the writer is busy (a nested transaction would deadlock here)")

// DB is a writer connection plus a read pool over the same file.
type DB struct {
	Path string
	w    *sql.DB
	r    *sql.DB
}

// pragmas are per-connection in SQLite, so they go in the DSN and apply to
// every connection the pool opens.
var pragmas = []string{
	"busy_timeout(5000)",
	"journal_mode(WAL)",
	"synchronous(NORMAL)",
	"foreign_keys(1)",
	"mmap_size(268435456)",
	"temp_store(MEMORY)",
	"wal_autocheckpoint(1000)",
	pageCache,
}

// Every connection keeps its own page cache, and the writer's is the one thing in
// the process that grows with the database: SQLite keeps each page it has read or
// written until the cache is full, and under the pure-Go driver a cached page costs
// about four times its bytes (3 to 5 in the runs) in the driver's allocator
// (measured, docs/ARCHITECTURE.md, "Soak"). A 64 MB writer cache therefore climbed
// with the file, about 180 MB above the Go runtime's own memory after an hour and
// still rising. The readers' caches stay nearly empty, because the memory map
// serves their reads. 8 MB on every connection leaves every budget as it was, 500,000
// words and a search rebuild included (the bench times were the same from 64 MB
// down to 2 MB); 8 MB rather than 2 is margin for a bulk import or restore, which
// the bench does not time.
const pageCache = "cache_size(-8192)"

func dsn(path string, readonly bool) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	if readonly {
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Set("_txlock", "immediate")
	}
	return "file:" + filepath.ToSlash(path) + "?" + q.Encode()
}

// Open opens (creating if needed) the database at path. It does not migrate.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("db: create data dir: %w", err)
	}
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, fmt.Errorf("db: open writer: %w", err)
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(4)
	if err := r.Ping(); err != nil {
		w.Close()
		r.Close()
		return nil, fmt.Errorf("db: open reader: %w", err)
	}
	return &DB{Path: path, w: w, r: r}, nil
}

// Close runs PRAGMA optimize and closes both pools.
func (d *DB) Close() error {
	_, _ = d.w.Exec("PRAGMA optimize")
	e1 := d.r.Close()
	e2 := d.w.Close()
	return errors.Join(e1, e2)
}

// Read is the read-only pool. Use it for queries that do not write.
func (d *DB) Read() *sql.DB { return d.r }

// Writer is the single write connection, for statements outside WithTx that
// must not race (rare). Prefer WithTx.
func (d *DB) Writer() *sql.DB { return d.w }

// WithTx runs fn in an IMMEDIATE transaction on the writer and commits when
// fn returns nil. Keep fn short: no network, no AI, no file I/O.
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	// The writer pool has one connection. A WithTx called while another is open
	// on this goroutine (a nested call) would wait for itself forever; waiting
	// under a deadline turns that bug into an error. Ordinary contention is a
	// few milliseconds, far inside the deadline.
	cctx, cancel := context.WithTimeout(ctx, acquireTimeout)
	conn, err := d.w.Conn(cctx)
	cancel()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return ErrWriterBusy
		}
		return err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// IsUniqueViolation reports whether err is a UNIQUE or PRIMARY KEY failure.
func IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "PRIMARY KEY")
}

// Backup writes a consistent copy of the database to dest using VACUUM INTO.
// dest must not exist.
func (d *DB) Backup(ctx context.Context, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("db: backup target %s already exists", dest)
	}
	_, err := d.w.ExecContext(ctx, "VACUUM INTO ?", dest)
	return err
}

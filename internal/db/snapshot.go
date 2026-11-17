package db

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
)

// Snapshot writes a consistent copy of the database at path to dest with
// VACUUM INTO, like Backup, but on its own short-lived connection instead of
// the writer's. In WAL mode the copy is one read transaction: the single writer
// keeps saving while it runs, and a save never waits on it (Backup holds the
// writer connection for the whole copy, and a save waits at most five seconds
// for that connection). It is also safe from a second process, which is how
// `scriobh backup` works beside a running server. dest must not exist.
func Snapshot(ctx context.Context, path, dest string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("db: no database at %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("db: snapshot target %s already exists", dest)
	}
	c, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetMaxOpenConns(1)
	if _, err := c.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("db: snapshot: %w", err)
	}
	return os.Chmod(dest, 0o600)
}

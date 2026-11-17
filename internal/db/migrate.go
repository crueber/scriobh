package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one numbered file. Files run once, in filename order, each in
// its own transaction, and are never edited after they have been applied: the
// stored checksum makes an edit a startup error rather than silent drift.
type Migration struct {
	Version  string // "0100"
	Name     string // "auth"
	File     string // "0100_auth.sql"
	SQL      string
	Checksum string
}

// Migrations returns the embedded migrations in order.
func Migrations() ([]Migration, error) { return loadMigrations(migrationFS, "migrations") }

func loadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	var out []Migration
	seen := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		stem := strings.TrimSuffix(name, ".sql")
		ver, rest, ok := strings.Cut(stem, "_")
		if !ok || len(ver) != 4 {
			return nil, fmt.Errorf("db: migration %q must be named NNNN_name.sql", name)
		}
		if prev, dup := seen[ver]; dup {
			return nil, fmt.Errorf("db: migrations %q and %q share version %s", prev, name, ver)
		}
		seen[ver] = name
		b, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		out = append(out, Migration{Version: ver, Name: rest, File: name, SQL: string(b), Checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// MigrateResult reports what a Migrate call did.
type MigrateResult struct {
	Applied []string // versions applied just now
	Backup  string   // path of the pre-flight backup, if one was taken
}

// Migrate applies pending migrations. Before applying anything to a database
// that already has data, it writes a pre-flight backup into backupDir (skipped
// when backupDir is empty). An applied migration whose checksum has changed is
// refused.
func (d *DB) Migrate(ctx context.Context, backupDir string) (MigrateResult, error) {
	all, err := Migrations()
	if err != nil {
		return MigrateResult{}, err
	}
	return d.migrateWith(ctx, all, backupDir)
}

func (d *DB) migrateWith(ctx context.Context, all []Migration, backupDir string) (MigrateResult, error) {
	var res MigrateResult
	if _, err := d.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return res, err
	}
	applied := map[string]string{}
	rows, err := d.w.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			rows.Close()
			return res, err
		}
		applied[v] = c
	}
	if err := rows.Close(); err != nil {
		return res, err
	}
	var pending []Migration
	known := map[string]bool{}
	for _, m := range all {
		known[m.Version] = true
		if sum, ok := applied[m.Version]; ok {
			if sum != m.Checksum {
				return res, fmt.Errorf("db: migration %s was edited after it was applied; add a new migration instead", m.File)
			}
			continue
		}
		pending = append(pending, m)
	}
	for v := range applied {
		if !known[v] {
			return res, fmt.Errorf("db: database has migration %s that this binary does not know; refusing to run an older build against a newer database", v)
		}
	}
	if len(pending) == 0 {
		return res, nil
	}
	if backupDir != "" && len(applied) > 0 {
		dest := filepath.Join(backupDir, "pre-migrate-"+strings.NewReplacer(":", "-", ".", "-").Replace(Now())+".db")
		if err := d.Backup(ctx, dest); err != nil {
			return res, fmt.Errorf("db: pre-flight backup failed, nothing was migrated: %w", err)
		}
		res.Backup = dest
	}
	for _, m := range pending {
		m := m
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES (?,?,?,?)`,
				m.Version, m.Name, m.Checksum, Now())
			return err
		})
		if err != nil {
			return res, fmt.Errorf("db: applying %s: %w", m.File, err)
		}
		res.Applied = append(res.Applied, m.Version)
	}
	return res, nil
}

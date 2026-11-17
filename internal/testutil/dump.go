package testutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
)

// Dump returns a digest of every row of every table, so a test can prove that
// an operation changed nothing. Virtual-table shadow tables are included; the
// digest is over rows in rowid order, and sqlite's own bookkeeping tables are
// skipped.
func Dump(t testing.TB, d *db.DB) string {
	t.Helper()
	rows, err := d.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	h := sha256.New()
	for _, tb := range tables {
		fmt.Fprintf(h, "table %s\n", tb)
		r, err := d.Read().Query(`SELECT * FROM "` + tb + `" ORDER BY rowid`)
		if err != nil {
			// WITHOUT ROWID tables have no rowid: fall back to the natural order.
			if r, err = d.Read().Query(`SELECT * FROM "` + tb + `"`); err != nil {
				t.Fatal(err)
			}
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(h, "%v\n", vals)
		}
		r.Close()
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TreeDigest returns a digest of every file path, size and content under dir.
func TreeDigest(t testing.TB, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(dir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if e.IsDir() {
			lines = append(lines, "d "+rel)
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		lines = append(lines, "f "+rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

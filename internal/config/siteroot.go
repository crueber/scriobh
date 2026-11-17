package config

import (
	"os"
	"path/filepath"
)

// resolveExisting follows symlinks through as much of p as exists, so a folder
// reached through a link is compared by where it really is.
func resolveExisting(p string) string {
	rest := ""
	for q := p; ; q = filepath.Dir(q) {
		if r, err := filepath.EvalSymlinks(q); err == nil {
			return filepath.Join(r, rest)
		}
		if _, err := os.Lstat(q); err == nil || filepath.Dir(q) == q {
			return p
		}
		rest = filepath.Join(filepath.Base(q), rest)
	}
}

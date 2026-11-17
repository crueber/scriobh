// Package repocheck holds checks about the repository itself.
package repocheck

import (
	"bufio"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test")
		}
		dir = parent
	}
}

// Every file a //go:embed pattern names is in the repository's index: a file
// that .gitignore hides builds on the machine that has it and breaks on a clean
// clone (a `data/` rule once hid the embedded fonts).
func TestEmbeddedFilesAreTracked(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Skip("not a git checkout")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Skipf("git ls-files: %v", err)
	}
	tracked := map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		if f != "" {
			tracked[f] = true
		}
	}
	checked := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".claude":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "//go:embed ") {
				continue
			}
			for _, pat := range strings.Fields(strings.TrimPrefix(line, "//go:embed ")) {
				pat = strings.TrimPrefix(pat, "all:")
				matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), pat))
				if len(matches) == 0 {
					t.Errorf("%s: the pattern %q matches no file", path, pat)
				}
				for _, m := range matches {
					_ = filepath.WalkDir(m, func(p string, de fs.DirEntry, err error) error {
						if err != nil || de.IsDir() {
							return nil
						}
						rel, err := filepath.Rel(root, p)
						if err != nil {
							return nil
						}
						checked++
						if !tracked[filepath.ToSlash(rel)] {
							t.Errorf("%s is embedded by %s but is not tracked by git (ignored or never added): a clean clone cannot build", rel, filepath.Base(path))
						}
						return nil
					})
				}
			}
		}
		return nil
	})
	if checked == 0 {
		t.Error("no embedded file was checked")
	}
}

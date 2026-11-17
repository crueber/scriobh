package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// isolate points the temp directory at a fresh one, so these tests build their own templates
// and never touch the one the other tests share.
func isolate(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	return tmp
}

func TestTemplateIsRebuiltWhenTheCachedFileIsBroken(t *testing.T) {
	isolate(t)
	ms, _ := db.Migrations()
	first, err := buildTemplate()
	if err != nil {
		t.Fatal(err)
	}
	if err := validTemplate(first, len(ms)); err != nil {
		t.Fatal(err)
	}
	for name, damage := range map[string]func(){
		"emptied":   func() { _ = os.Truncate(first, 0) },
		"cut short": func() { fi, _ := os.Stat(first); _ = os.Truncate(first, fi.Size()/2) },
		"garbage":   func() { _ = os.WriteFile(first, []byte(strings.Repeat("not a database ", 1000)), 0o600) },
	} {
		damage()
		if validTemplate(first, len(ms)) == nil {
			t.Fatalf("%s: the damaged file passes validation", name)
		}
		again, err := buildTemplate()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if again != first {
			t.Errorf("%s: rebuilt under %s, not %s", name, again, first)
		}
		if err := validTemplate(again, len(ms)); err != nil {
			t.Fatalf("%s: the rebuilt file is not whole: %v", name, err)
		}
	}
}

func TestTemplateDirectoryIsPrivateToTheUser(t *testing.T) {
	tmp := isolate(t)
	dir, err := templateDir()
	if err != nil || filepath.Dir(dir) != tmp || !strings.HasPrefix(filepath.Base(dir), "scriobh-testdb-"+userKey()) {
		t.Fatalf("dir %q err %v", dir, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", fi.Mode())
	}
	// A link planted where the directory should be is not followed.
	_ = os.Remove(dir)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, dir); err != nil {
		t.Skip(err)
	}
	got, err := templateDir()
	if err != nil || got == dir || strings.HasPrefix(got, elsewhere) {
		t.Errorf("through a link: %q %v", got, err)
	}
	// A directory that others can enter is not trusted either.
	_ = os.Remove(dir)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(dir, 0o755)
	if got, err := templateDir(); err != nil || got == dir {
		t.Errorf("a 0755 directory: %q %v", got, err)
	}
}

func TestTemplateHousekeepingRemovesOnlyOldLeftovers(t *testing.T) {
	isolate(t)
	ms, _ := db.Migrations()
	dir, err := templateDir()
	if err != nil {
		t.Fatal(err)
	}
	current := "migrated-" + templateKey(ms) + ".db"
	old := time.Now().Add(-48 * time.Hour)
	mk := func(name string, dirp bool, mod time.Time) string {
		p := filepath.Join(dir, name)
		if dirp {
			_ = os.Mkdir(p, 0o700)
		} else {
			_ = os.WriteFile(p, []byte("x"), 0o600)
		}
		_ = os.Chtimes(p, mod, mod)
		return p
	}
	oldBuild := mk("build-old", true, old)
	newBuild := mk("build-new", true, time.Now())
	oldDB := mk("migrated-0000000000000000.db", false, old)
	newDB := mk("migrated-1111111111111111.db", false, time.Now())
	cur := mk(current, false, old) // the current key is never removed, however old
	tidyTemplates(dir, current)
	for p, want := range map[string]bool{oldBuild: false, newBuild: true, oldDB: false, newDB: true, cur: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}

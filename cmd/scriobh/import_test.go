package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func count(t *testing.T, dir, query string) int {
	t.Helper()
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.Read().QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dir)
	t.Setenv("SCRIOBH_LISTEN", "127.0.0.1:0")
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	testutil.User(t, d, "writer", "w@t.local")
	d.Close()

	src := filepath.Join(t.TempDir(), "the-salt-road")
	writeTree(t, src, map[string]string{
		"01-part/01-arrival.md": "# Arrival\n\nThey came at dusk.\n\n## The Dock\n\nShe waited.\n",
		"01-part/02-road.md":    "They walked.\n",
		"cover.png":             "not really a png",
		".git/config":           "[core]",
		".notes.md":             "hidden file",
	})
	if err := os.Symlink("01-part", filepath.Join(src, "link")); err != nil {
		t.Logf("no symlinks here: %v", err)
	}

	// Preview writes nothing.
	var out, errb bytes.Buffer
	if code := run([]string{"import", src}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{"generic Markdown folder", "The Salt Road", "Arrival", "1 H2 headings", "not a Markdown file", "hidden folder", "Nothing was written"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("preview output lacks %q:\n%s", want, out.String())
		}
	}
	if n := count(t, dir, `SELECT count(*) FROM projects`); n != 0 {
		t.Fatalf("a preview created %d projects", n)
	}

	// Apply with scene splitting.
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", src, "--apply", "--split-h2"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if !strings.Contains(out.String(), `Created project "The Salt Road"`) {
		t.Errorf("output: %s", out.String())
	}
	if n := count(t, dir, `SELECT count(*) FROM documents WHERE kind = 'scene'`); n != 1 {
		t.Errorf("scenes = %d", n)
	}
	if n := count(t, dir, `SELECT count(*) FROM documents`); n != 4 {
		t.Errorf("documents = %d, want part + 2 chapters + 1 scene", n)
	}
	if n := count(t, dir, `SELECT count(*) FROM events WHERE type = 'import.applied'`); n != 1 {
		t.Errorf("import.applied events = %d: the command must write events like the server does", n)
	}

	// Import again into the same project, after what is there.
	d, _ = db.Open(filepath.Join(dir, "scriobh.db"))
	var pid string
	d.Read().QueryRow(`SELECT id FROM projects`).Scan(&pid)
	d.Close()
	more := t.TempDir()
	writeTree(t, more, map[string]string{"epilogue.md": "The end.\n"})
	out.Reset()
	errb.Reset()
	if code := run([]string{"import", more, "--into", pid, "--apply"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if n := count(t, dir, `SELECT count(*) FROM documents`); n != 5 {
		t.Errorf("documents after append = %d", n)
	}

	// Usage errors and a missing folder.
	for _, args := range [][]string{{"import"}, {"import", src, "--parent", "x"}, {"import", src, "--bogus"}} {
		out.Reset()
		errb.Reset()
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code := run([]string{"import", filepath.Join(src, "nope")}, &out, &errb); code != 1 {
		t.Errorf("missing folder: exit %d", code)
	}
}

func TestImportCommandNeedsAnOwnerWhenSeveralPeopleExist(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dir)
	t.Setenv("SCRIOBH_LISTEN", "127.0.0.1:0")
	d, _ := db.Open(filepath.Join(dir, "scriobh.db"))
	d.Migrate(context.Background(), "")
	testutil.User(t, d, "writer", "a@t.local")
	testutil.User(t, d, "writer", "b@t.local")
	d.Close()
	src := t.TempDir()
	writeTree(t, src, map[string]string{"a.md": "x"})
	var out, errb bytes.Buffer
	if code := run([]string{"import", src}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "--owner") {
		t.Errorf("exit %d: %s", code, errb.String())
	}
	if code := run([]string{"import", src, "--owner", "B@T.local", "--apply"}, &out, &errb); code != 0 {
		t.Errorf("exit %d: %s", code, errb.String())
	}
	if n := count(t, dir, `SELECT count(*) FROM projects p JOIN users u ON u.id = p.owner_id WHERE u.email = 'b@t.local'`); n != 1 {
		t.Errorf("owner: %d", n)
	}
}

func TestStatsRecomputeCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dir)
	t.Setenv("SCRIOBH_LISTEN", "127.0.0.1:0")
	d, _ := db.Open(filepath.Join(dir, "scriobh.db"))
	d.Migrate(context.Background(), "")
	d.Close()
	var out, errb bytes.Buffer
	if code := run([]string{"stats", "recompute"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "recomputed 0 days") {
		t.Errorf("exit %d: %s %s", code, out.String(), errb.String())
	}
	if code := run([]string{"stats"}, &out, &errb); code != 2 {
		t.Errorf("usage: exit %d", code)
	}
}

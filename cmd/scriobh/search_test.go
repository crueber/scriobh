package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestSearchRebuildCommand(t *testing.T) {
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
	reg := registry.Build(d, nil, dir)
	p := ops.Principal{UserID: testutil.User(t, d, "writer", "w@t.local"), Role: ops.RoleWriter}
	out, err := reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Book"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	for i := 0; i < 3; i++ {
		if _, err := reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter", Content: "the lighthouse keeper"}); err != nil {
			t.Fatal(err)
		}
	}
	// Lose the derived text, as a damaged index might.
	if _, err := d.Writer().Exec(`UPDATE doc_text SET body = 'stale'`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"search", "rebuild"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "indexed 4 of 4 documents") || !strings.Contains(stdout.String(), "search index rebuilt") {
		t.Errorf("output: %q", stdout.String())
	}

	d, err = db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM doc_fts WHERE doc_fts MATCH 'lighthouse'`).Scan(&n); err != nil || n != 3 {
		t.Errorf("after rebuild the index finds %d documents (%v), want 3", n, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"search"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: scriobh search rebuild") {
		t.Errorf("bare `search`: exit %d, %q", code, stderr.String())
	}
}

// `scriobh --help` and `--version` answer; they must not fall through to the
// server, which is the command when no word names another.
func TestFlagsInTheCommandsPlace(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{{"--help", "usage: scriobh"}, {"-h", "usage: scriobh"}, {"--version", version}, {"-version", version}} {
		var out, errb bytes.Buffer
		if code := run([]string{tc.arg}, &out, &errb); code != 0 || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: code %d, stdout %q, stderr %q", tc.arg, code, out.String(), errb.String())
		}
	}
}

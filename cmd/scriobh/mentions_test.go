package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestMentionsRebuildCommand(t *testing.T) {
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
	ctx := context.Background()
	reg := registry.Build(d, nil, dir)
	p := ops.Principal{UserID: testutil.User(t, d, "writer", "w@t.local"), Role: ops.RoleWriter}
	call := func(op string, in any) any {
		out, err := reg.Call(ctx, p, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return out
	}
	cid := call("compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	call("entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Elara"})
	pid := call("project.create", projects.CreateIn{Title: "Book"}).(projects.Item).Item.ID
	call("project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	for i := 0; i < 3; i++ {
		call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter", Content: "Elara walked."})
	}
	// Lose the derived index, as an upgrade from a database that never had one would.
	if _, err := d.Writer().Exec(`DELETE FROM entity_mentions`); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"mentions", "rebuild"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "mention index rebuilt") {
		t.Errorf("output: %q", stdout.String())
	}
	d, err = db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM entity_mentions`).Scan(&n); err != nil || n != 3 {
		t.Errorf("after the rebuild there are %d rows (%v), want 3", n, err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"mentions"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "usage: scriobh mentions rebuild") {
		t.Errorf("usage: %d %q", code, stderr.String())
	}
}

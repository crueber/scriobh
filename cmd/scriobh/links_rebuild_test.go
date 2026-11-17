package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestStartRebuildsLinksWhenVersionIsOldOrAnImportWasCutShort(t *testing.T) {
	ctx := context.Background()
	d := testutil.DB(t)
	reg := registry.Build(d, nil, t.TempDir())
	p := ops.Principal{UserID: testutil.User(t, d, "writer", "w@t.local"), Role: ops.RoleWriter}
	pid := func(title string) string {
		out, err := reg.Call(ctx, p, "project.create", projects.CreateIn{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return out.(projects.Item).Item.ID
	}
	a, b := pid("A"), pid("B")
	for _, project := range []string{a, b} {
		for _, in := range []docs.CreateIn{{Title: "Target"}, {Title: "Source", Content: "See [[Target]]."}} {
			in.ProjectID = project
			if _, err := reg.Call(ctx, p, "doc.create", in); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func(project string) int {
		var n int
		if err := d.Read().QueryRow(`SELECT count(*) FROM document_links l JOIN documents x ON x.id = l.from_id WHERE x.project_id = ?`, project).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	set := func(k, v string) {
		if _, err := d.Writer().Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v, db.Now()); err != nil {
			t.Fatal(err)
		}
	}
	get := func(k string) string {
		var v string
		_ = d.Read().QueryRow(`SELECT value FROM settings WHERE key = ?`, k).Scan(&v)
		return v
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	wipe := func() {
		if _, err := d.Writer().Exec(`DELETE FROM document_links`); err != nil {
			t.Fatal(err)
		}
	}

	// A database whose index is at an older version is rebuilt, and says so.
	wipe()
	set(links.IndexKey, "1")
	rebuildLinksOnce(ctx, d, log)
	if count(a) != 1 || count(b) != 1 || get(links.IndexKey) != links.IndexVersion {
		t.Fatalf("version rebuild: %d %d %q", count(a), count(b), get(links.IndexKey))
	}
	// Up to date and nothing pending: left alone, however damaged.
	wipe()
	rebuildLinksOnce(ctx, d, log)
	if count(a) != 0 || count(b) != 0 {
		t.Error("an index at the current version with no marker is not rebuilt at every start")
	}
	// A marker from an import that was cut short rebuilds that project only, then goes.
	set(links.PendingPrefix+a, a)
	rebuildLinksOnce(ctx, d, log)
	if count(a) != 1 || count(b) != 0 || get(links.PendingPrefix+a) != "" {
		t.Errorf("marker repair: %d %d %q", count(a), count(b), get(links.PendingPrefix+a))
	}
}

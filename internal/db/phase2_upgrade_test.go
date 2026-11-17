package db

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// A database that already holds documents (written before the Phase 2
// migrations existed) takes them even though they sort below migrations it has
// already applied, and its documents keep their text and gain empty scene
// metadata and an empty content stamp.
func TestPhase1DatabaseTakesThePhase2Migrations(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	// 0420 to 0460 (review: the revision reason, comments, suggestions, share links,
	// notifications) build on 0400 and so belong to the same later batch; so does 1820
	// (the label of a suggestion that came from a Word file), which alters 0440's table.
	phase2 := []string{"0310", "0320", "0330", "0400", "0410", "0420", "0430", "0440", "0450", "0460", "0470", "1820"}
	var phase1 []Migration
	for _, m := range all {
		if !slices.Contains(phase2, m.Version) {
			phase1 = append(phase1, m)
		}
	}
	if len(phase1) != len(all)-len(phase2) {
		t.Fatalf("expected the Phase 2 migrations to exist: %d of %d", len(phase1), len(all))
	}
	if _, err := d.migrateWith(ctx, phase1, ""); err != nil {
		t.Fatal(err)
	}
	now := Now()
	for _, q := range []string{
		`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','t','s','a@t','A','writer','` + now + `','` + now + `')`,
		`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p','` + now + `','` + now + `')`,
		`INSERT INTO documents(id, project_id, title, content, created_at, updated_at) VALUES ('doc_1','prj_1','Old','Text with [[Old]] link.','` + now + `','` + now + `')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	res, err := d.migrateWith(ctx, all, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Applied, phase2) || res.Backup == "" {
		t.Fatalf("applied %v with backup %q, want %v and a pre-flight backup", res.Applied, res.Backup, phase2)
	}
	var content, pov, contentAt string
	var by *string
	if err := d.Read().QueryRow(`SELECT content, pov, content_at, content_by FROM documents WHERE id = 'doc_1'`).Scan(&content, &pov, &contentAt, &by); err != nil {
		t.Fatal(err)
	}
	if content != "Text with [[Old]] link." || pov != "" || contentAt != "" || by != nil {
		t.Errorf("document after upgrade: %q %q %q %v", content, pov, contentAt, by)
	}
	for _, tbl := range []string{"document_links", "document_revisions", "revision_blobs"} {
		var n int
		if err := d.Read().QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d %v", tbl, n, err)
		}
	}
}

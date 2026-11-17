package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Migration 0810 adds the cover layouts and rebuilds attachment_refs for the new `cover` kind: after an upgrade from
// the v1.0 database every reference (here one made before the upgrade) is where it was, the table accepts `cover` and
// still refuses anything else, an attachment is not cover-clean until the cover upload says so, no project has a
// layout, and a layout goes with its project and its references with their attachment.
func TestCoverLayoutsMigrationKeepsReferencesAndAddsTheCoverKind(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "v1.0.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "scriobh.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	var pid string
	if err := d.Read().QueryRow(`SELECT id FROM projects LIMIT 1`).Scan(&pid); err != nil {
		t.Skipf("the fixture has no project: %v", err)
	}
	// a picture and the two kinds of reference the old table knew, made before the upgrade
	if _, err := d.Writer().Exec(`INSERT INTO attachments(id, project_id, sha256, bytes, mime, ext, kind, filename, alt, width, height, created_at, updated_at)
		VALUES ('att_00000000000000000000000001', ?, 'aa', 10, 'image/png', 'png', 'image', 'x.png', '', 1, 1, 'x', 'x')`, pid); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"doc", "research"} {
		if _, err := d.Writer().Exec(`INSERT INTO attachment_refs(attachment_id, ref_kind, ref_id) VALUES ('att_00000000000000000000000001', ?, ?)`, k, k+"_1"); err != nil {
			t.Fatal(err)
		}
	}
	var before int
	_ = d.Read().QueryRow(`SELECT count(*) FROM attachment_refs`).Scan(&before)
	if _, err := d.Migrate(ctx, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	var after, clean, layouts int
	_ = d.Read().QueryRow(`SELECT count(*) FROM attachment_refs`).Scan(&after)
	if before != after || after < 2 {
		t.Fatalf("references %d before, %d after", before, after)
	}
	if err := d.Read().QueryRow(`SELECT cover_clean FROM attachments WHERE id = 'att_00000000000000000000000001'`).Scan(&clean); err != nil || clean != 0 {
		t.Errorf("cover_clean = %d, %v: an ordinary attachment is not clean for a cover", clean, err)
	}
	if err := d.Read().QueryRow(`SELECT count(*) FROM cover_layouts`).Scan(&layouts); err != nil || layouts != 0 {
		t.Errorf("layouts: %d %v", layouts, err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO attachment_refs(attachment_id, ref_kind, ref_id) VALUES ('att_00000000000000000000000001', 'cover', ?)`, pid); err != nil {
		t.Errorf("the cover kind is refused: %v", err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO attachment_refs(attachment_id, ref_kind, ref_id) VALUES ('att_00000000000000000000000001', 'bogus', 'x')`); err == nil {
		t.Error("an unknown kind is accepted")
	}
	if _, err := d.Writer().Exec(`UPDATE attachments SET cover_clean = 2 WHERE id = 'att_00000000000000000000000001'`); err == nil {
		t.Error("cover_clean takes a value that is not 0 or 1")
	}
	if _, err := d.Writer().Exec(`INSERT INTO cover_layouts(project_id, shape, hash, created_at, updated_at) VALUES (?, 'wrap', 'h', 'x', 'x')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO cover_layouts(project_id, shape, hash, created_at, updated_at) VALUES (?, 'sides', 'h', 'x', 'x')`, pid); err == nil {
		t.Error("two layouts for one project")
	}
	// the references go with the attachment; the layout with the project
	if _, err := d.Writer().Exec(`DELETE FROM attachments WHERE id = 'att_00000000000000000000000001'`); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = d.Read().QueryRow(`SELECT count(*) FROM attachment_refs WHERE attachment_id = 'att_00000000000000000000000001'`).Scan(&left)
	if left != 0 {
		t.Errorf("%d references outlived their attachment", left)
	}
	if _, err := d.Writer().Exec(`DELETE FROM projects WHERE id = ?`, pid); err != nil {
		t.Fatal(err)
	}
	_ = d.Read().QueryRow(`SELECT count(*) FROM cover_layouts WHERE project_id = ?`, pid).Scan(&left)
	if left != 0 {
		t.Error("a layout outlived its project")
	}
}

// Migration 1837 adds a Git remote's include_cover. A remote made before the audience existed becomes a private one (the
// whole copy, as 1830 says) and so carries the cover; a public remote made after has it off until a person turns it on.
func TestGitRemotesAfterAnUpgradeKeepThePublicOnesCoverFree(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "v1.0.db"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "scriobh.db")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var pid string
	if err := d.Read().QueryRow(`SELECT id FROM projects LIMIT 1`).Scan(&pid); err != nil {
		t.Skipf("the fixture has no project: %v", err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO git_remotes(id, project_id, name, url, created_at, updated_at) VALUES ('grm_00000000000000000000000001', ?, 'Backup', 'ssh://git@h.example/o/b.git', 'x', 'x')`, pid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Migrate(context.Background(), filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	var aud string
	var cover int
	if err := d.Read().QueryRow(`SELECT audience, include_cover FROM git_remotes WHERE id = 'grm_00000000000000000000000001'`).Scan(&aud, &cover); err != nil {
		t.Fatal(err)
	}
	if aud != "private" || cover != 1 {
		t.Errorf("a remote from before the audience: %s, include_cover %d", aud, cover)
	}
	// a public remote made now has it off unless it is said
	if _, err := d.Writer().Exec(`INSERT INTO git_remotes(id, project_id, name, url, audience, created_at, updated_at) VALUES ('grm_00000000000000000000000002', ?, 'Mirror', 'ssh://git@h.example/o/m.git', 'public', 'x', 'x')`, pid); err != nil {
		t.Fatal(err)
	}
	if err := d.Read().QueryRow(`SELECT include_cover FROM git_remotes WHERE id = 'grm_00000000000000000000000002'`).Scan(&cover); err != nil || cover != 0 {
		t.Errorf("a public remote: include_cover %d, %v", cover, err)
	}
	if _, err := d.Writer().Exec(`UPDATE git_remotes SET include_cover = 2`); err == nil {
		t.Error("include_cover takes a value that is not 0 or 1")
	}
}

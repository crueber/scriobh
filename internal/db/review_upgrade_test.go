package db

import (
	"context"
	"testing"
)

// Migration 0420 rebuilds document_revisions to allow one more reason. A database that already holds
// history keeps every revision, its blob, its indexes and its cascade, and then takes the new reason.
func TestRevisionReasonMigrationKeepsHistory(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var before []Migration
	for _, m := range all {
		if m.Version < "0420" {
			before = append(before, m)
		}
	}
	if _, err := d.migrateWith(ctx, before, ""); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Writer().Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	now := Now()
	exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','t','s','a@t','A','writer',?,?)`, now, now)
	exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p',?,?)`, now, now)
	exec(`INSERT INTO documents(id, project_id, created_at, updated_at) VALUES ('doc_1','prj_1',?,?)`, now, now)
	for i, reason := range []string{"autosave", "manual", "snapshot", "restore", "import", "ai-accepted", "alternative"} {
		hash := "hash" + string(rune('a'+i))
		exec(`INSERT INTO revision_blobs(document_id, content_hash, encoding, content, raw_size) VALUES ('doc_1', ?, 'identity', ?, 4)`, hash, []byte("text"))
		exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, word_count, author_id, reason, label, note, created_at, recorded_at)
			VALUES (?, 'doc_1', ?, ?, 1, 'usr_1', ?, ?, 'a note', ?, ?)`, "rev_"+reason, i+1, hash, reason, "label "+reason, now, now)
	}
	// the old list refuses the new reason
	if _, err := d.Writer().Exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, reason, created_at, recorded_at) VALUES ('rev_x','doc_1',9,'hasha','suggestion-accepted',?,?)`, now, now); err == nil {
		t.Fatal("the old CHECK accepted the new reason; the test proves nothing")
	}
	res, err := d.migrateWith(ctx, all, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) < 5 || res.Applied[0] != "0420" {
		t.Fatalf("applied %v", res.Applied)
	}
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM document_revisions WHERE note = 'a note' AND label = 'label ' || reason AND author_id = 'usr_1'`).Scan(&n); err != nil || n != 7 {
		t.Fatalf("revisions after the migration: %d (%v)", n, err)
	}
	// the new reason is accepted, an unknown one is not, the blob foreign key still holds, and the cascade still works
	if _, err := d.Writer().Exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, reason, created_at, recorded_at) VALUES ('rev_new','doc_1',9,'hasha','suggestion-accepted',?,?)`, now, now); err != nil {
		t.Fatalf("the new reason: %v", err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, reason, created_at, recorded_at) VALUES ('rev_bad','doc_1',10,'hasha','bogus',?,?)`, now, now); err == nil {
		t.Error("an unknown reason was accepted")
	}
	if _, err := d.Writer().Exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, reason, created_at, recorded_at) VALUES ('rev_orphan','doc_1',11,'nope','manual',?,?)`, now, now); err == nil {
		t.Error("a revision without its blob was accepted")
	}
	for _, idx := range []string{"document_revisions_shown", "document_revisions_recorded"} {
		if err := d.Read().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s: %d %v", idx, n, err)
		}
	}
	exec(`DELETE FROM documents WHERE id = 'doc_1'`)
	if err := d.Read().QueryRow(`SELECT count(*) FROM document_revisions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("deleting the document left %d revisions", n)
	}
}

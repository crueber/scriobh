package db

import (
	"context"
	"path/filepath"
	"testing"
)

// Snapshots kept by an earlier build for a restore or a split carried their
// explanation in the label, which pins them forever. The migration moves it to
// the note and touches nothing a person wrote.
func TestSnapshotLabelsMoveToNotes(t *testing.T) {
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
	var before []Migration
	for _, m := range all {
		if m.Version != "0410" {
			before = append(before, m)
		}
	}
	if _, err := d.migrateWith(ctx, before, ""); err != nil {
		t.Fatal(err)
	}
	now := Now()
	for _, q := range []string{
		`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','t','s','a@t','A','writer','` + now + `','` + now + `')`,
		`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p','` + now + `','` + now + `')`,
		`INSERT INTO documents(id, project_id, title, created_at, updated_at) VALUES ('doc_1','prj_1','D','` + now + `','` + now + `')`,
		`INSERT INTO revision_blobs(document_id, content_hash, encoding, content, raw_size) VALUES ('doc_1','h','identity',x'61',1)`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	rev := func(id, reason, label, note string) {
		if _, err := d.Writer().Exec(`INSERT INTO document_revisions(id, document_id, revision, content_hash, reason, label, note, created_at, recorded_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			id, "doc_1", 1, "h", reason, label, note, now, now); err != nil {
			t.Fatal(err)
		}
	}
	rev("r_restore", "snapshot", "Before restore", "")
	rev("r_split", "snapshot", "Before split", "")
	rev("r_user", "snapshot", "My own name", "")
	rev("r_manual", "manual", "Before restore", "")
	rev("r_noted", "snapshot", "Before split", "already explained")
	if _, err := d.migrateWith(ctx, all, ""); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][2]string{
		"r_restore": {"", "Before restore"}, "r_split": {"", "Before split"}, "r_user": {"My own name", ""},
		"r_manual": {"Before restore", ""}, "r_noted": {"Before split", "already explained"},
	} {
		var label, note string
		if err := d.Read().QueryRow(`SELECT label, note FROM document_revisions WHERE id = ?`, id).Scan(&label, &note); err != nil {
			t.Fatal(err)
		}
		if label != want[0] || note != want[1] {
			t.Errorf("%s: label %q note %q, want %q %q", id, label, note, want[0], want[1])
		}
	}
}

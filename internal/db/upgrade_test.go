package db

import (
	"context"
	"testing"
)

// Real databases reach 0710 with session baselines in them: rows for documents
// that still exist are kept (and rebased by the next save), rows for documents
// that are gone are dropped, and the new constraints apply afterwards.
func TestSessionBaselinesMigrationKeepsLiveRowsAndDropsOrphans(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var before []Migration
	for _, m := range all {
		if m.Version < "0710" {
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
	exec(`INSERT INTO users(id, email, role, created_at, updated_at) VALUES ('usr_1','a@t','writer','2026-01-01','2026-01-01')`)
	exec(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p','2026-01-01','2026-01-01')`)
	exec(`INSERT INTO documents(id, project_id, created_at, updated_at) VALUES ('doc_live','prj_1','2026-01-01','2026-01-01')`)
	exec(`INSERT INTO writing_sessions(id, user_id, project_id, day, tz, started_at, ended_at) VALUES ('ses_1','usr_1','prj_1','2026-01-01','UTC','2026-01-01','2026-01-01')`)
	exec(`INSERT INTO writing_session_docs(session_id, document_id, baseline_text, baseline_words, carried, credit) VALUES ('ses_1','doc_live','kept text',2,3,4)`)
	exec(`INSERT INTO writing_session_docs(session_id, document_id, baseline_text, baseline_words, carried, credit) VALUES ('ses_1','doc_gone','orphan text',2,0,0)`)

	if _, err := d.migrateWith(ctx, all, ""); err != nil {
		t.Fatal(err)
	}

	var text, hash string
	var carried, credit int
	if err := d.Read().QueryRow(`SELECT baseline_text, carried, credit, last_hash FROM writing_session_docs WHERE document_id = 'doc_live'`).Scan(&text, &carried, &credit, &hash); err != nil {
		t.Fatalf("the live row was lost: %v", err)
	}
	if text != "kept text" || carried != 3 || credit != 4 || hash != "" {
		t.Errorf("row after migration: %q %d %d %q", text, carried, credit, hash)
	}
	var n int
	d.Read().QueryRow(`SELECT count(*) FROM writing_session_docs WHERE document_id = 'doc_gone'`).Scan(&n)
	if n != 0 {
		t.Error("an orphaned baseline survived")
	}
	// The foreign key now holds, and the leave-a-project trigger exists.
	if _, err := d.Writer().Exec(`INSERT INTO writing_session_docs(session_id, document_id, baseline_text) VALUES ('ses_1','doc_nope','x')`); err == nil {
		t.Error("a baseline for a document that does not exist was accepted")
	}
	exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES ('prj_1','usr_1','owner','2026-01-01')`)
	exec(`DELETE FROM project_members WHERE project_id = 'prj_1' AND user_id = 'usr_1'`)
	d.Read().QueryRow(`SELECT count(*) FROM writing_session_docs`).Scan(&n)
	if n != 0 {
		t.Errorf("%d baselines left after the member left", n)
	}
}

// A server that already has mail waiting reaches 1934 with messages in the outbox: the rebuild that adds the third kind keeps
// every row and every index, a dedupe key still holds, and the new kind is accepted.
func TestMailProgressMigrationKeepsTheOutbox(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var before []Migration
	for _, m := range all {
		if m.Version < "1934" {
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
	for i, kind := range []string{"digest", "kindle", "digest"} {
		exec(`INSERT INTO mail_outbox(id, kind, person_id, recipient, subject, body, dedupe_key, message_id, state, next_try_at, created_at, window_since, window_until)
			VALUES (?,?,'usr_1','a@x.y','S','B',?,?,'pending','2026-01-01','2026-01-01','w1','w2')`, "mel_"+string(rune('a'+i)), kind, "k"+string(rune('a'+i)), "m"+string(rune('a'+i)))
	}
	if _, err := d.migrateWith(ctx, all, ""); err != nil {
		t.Fatal(err)
	}
	var n int
	var since string
	if err := d.Read().QueryRow(`SELECT count(*), min(window_since) FROM mail_outbox`).Scan(&n, &since); err != nil || n != 3 || since != "w1" {
		t.Fatalf("rows after the rebuild: %d %q %v", n, since, err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO mail_outbox(id, kind, person_id, recipient, dedupe_key, message_id, state, next_try_at, created_at) VALUES ('mel_z','progress','usr_1','a@x.y','ka','mz','pending','2026-01-01','2026-01-01')`); err == nil {
		t.Error("a repeated dedupe key was accepted: the index was lost")
	}
	if _, err := d.Writer().Exec(`INSERT INTO mail_outbox(id, kind, person_id, recipient, dedupe_key, message_id, state, next_try_at, created_at) VALUES ('mel_y','progress','usr_1','a@x.y','kz','my','pending','2026-01-01','2026-01-01')`); err != nil {
		t.Errorf("the progress kind: %v", err)
	}
	if _, err := d.Writer().Exec(`INSERT INTO mail_outbox(id, kind, person_id, recipient, dedupe_key, message_id, state, next_try_at, created_at) VALUES ('mel_x','other','usr_1','a@x.y','kx','mx','pending','2026-01-01','2026-01-01')`); err == nil {
		t.Error("an unknown kind was accepted")
	}
}

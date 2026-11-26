package db

import (
	"context"
	"testing"
)

// Migration 0480 gives every comment a visibility. A database that already holds comments keeps each of them, as shared (they were
// all visible to everyone who could read comments), with shared_at set to when it was written, and then takes private ones; a reply
// cannot disagree with its thread.
func TestCommentVisibilityMigrationKeepsEveryOldCommentShared(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	all, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var before []Migration
	for _, m := range all {
		if m.Version < "0480" {
			before = append(before, m)
		}
	}
	if _, err := d.migrateWith(ctx, before, ""); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) error {
		_, err := d.Writer().Exec(q, args...)
		return err
	}
	must := func(q string, args ...any) {
		t.Helper()
		if err := exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	now := Now()
	must(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','t','s','a@t','A','writer',?,?)`, now, now)
	must(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p',?,?)`, now, now)
	must(`INSERT INTO comments(id, project_id, author_id, body, created_at, updated_at) VALUES ('cmt_old','prj_1','usr_1','written before',?,?)`, "2024-01-02T03:04:05.000Z", now)
	must(`INSERT INTO comments(id, project_id, parent_id, author_id, body, created_at, updated_at) VALUES ('cmt_oldreply','prj_1','cmt_old','usr_1','a reply',?,?)`, "2024-01-03T03:04:05.000Z", now)
	res, err := d.migrateWith(ctx, all, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) == 0 || res.Applied[0] != "0480" {
		t.Fatalf("applied %v, want 0480 first", res.Applied)
	}
	for id, at := range map[string]string{"cmt_old": "2024-01-02T03:04:05.000Z", "cmt_oldreply": "2024-01-03T03:04:05.000Z"} {
		var vis, shared string
		if err := d.Read().QueryRow(`SELECT visibility, shared_at FROM comments WHERE id = ?`, id).Scan(&vis, &shared); err != nil {
			t.Fatal(err)
		}
		if vis != "shared" || shared != at {
			t.Errorf("%s: visibility %q, shared_at %q; want shared and %q", id, vis, shared, at)
		}
	}
	// a private thread, a reply that agrees with it, and replies that do not
	must(`INSERT INTO comments(id, project_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_p','prj_1','usr_1','mine','private',?,?)`, now, now)
	if err := exec(`INSERT INTO comments(id, project_id, parent_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_pr','prj_1','cmt_p','usr_1','also mine','private',?,?)`, now, now); err != nil {
		t.Errorf("a private reply to a private thread: %v", err)
	}
	if err := exec(`INSERT INTO comments(id, project_id, parent_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_bad1','prj_1','cmt_p','usr_1','x','shared',?,?)`, now, now); err == nil {
		t.Error("a shared reply under a private thread was accepted")
	}
	if err := exec(`INSERT INTO comments(id, project_id, parent_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_bad2','prj_1','cmt_old','usr_1','x','private',?,?)`, now, now); err == nil {
		t.Error("a private reply under a shared thread was accepted")
	}
	if err := exec(`INSERT INTO comments(id, project_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_bad3','prj_1','usr_1','x','everyone',?,?)`, now, now); err == nil {
		t.Error("an unknown visibility was accepted")
	}
	var n int
	if err := d.Read().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = 'comments_private'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("index comments_private: %d %v", n, err)
	}
}

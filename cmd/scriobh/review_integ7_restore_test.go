package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

// A backup taken with every outbound feature switched on and restored (as `scriobh restore` does it) must come back with
// every one of them off, with no marker left over, and nothing waiting to send.
// seedEverythingOn makes an instance with every outbound feature switched on and returns the archive of it.
func seedEverythingOn(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	d, err := dbOpenMigrated(t, src)
	if err != nil {
		t.Fatal(err)
	}
	uid := testutil.User(t, d, "admin", "a@example.org")
	ex := func(q string, a ...any) {
		t.Helper()
		if _, err := d.Writer().Exec(q, a...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ex(`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1',?, 'P','p','t','t')`, uid)
	ex(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES ('prj_1',?,'owner','t')`, uid)
	ex(`INSERT INTO mail_config(id, enabled, host, port, security, from_address, updated_at) VALUES (1, 1, 'mail.example.org', 587, 'starttls', 'a@example.org', 'x')`)
	ex(`INSERT INTO mail_outbox(id, kind, person_id, recipient, message_id, state, next_try_at, created_at) VALUES ('mo1','digest',?, 'a@example.org','<m1>','pending','t','t')`, uid)
	ex(`INSERT INTO mail_outbox(id, kind, person_id, recipient, message_id, state, next_try_at, created_at) VALUES ('mo2','kindle',?, 'a@kindle.com','<m2>','sending','t','t')`, uid)
	ex(`INSERT INTO git_remotes(id, project_id, name, url, enabled, state, created_at, updated_at, audience) VALUES ('gr1','prj_1','r','https://example.org/x.git',1,'synced','t','t','public')`)
	ex(`INSERT INTO git_remotes(id, project_id, name, url, enabled, state, created_at, updated_at) VALUES ('gr2','prj_1','r2','https://example.org/y.git',1,'new','t','t')`)
	ex(`INSERT INTO git_triggers(remote_id, project_id, url, secret_sealed, secret_hint, enabled, created_at, updated_at) VALUES ('gr1','prj_1','https://ci.example.org','x','h',1,'t','t')`)
	ex(`INSERT INTO git_trigger_deliveries(id, remote_id, project_id, push_id, commit_sha, branch, state, next_attempt_at, created_at) VALUES ('gd1','gr1','prj_1','p','c','main','pending','t','t')`)
	ex(`INSERT INTO webhooks(id, project_id, name, url, events_json, enabled, secret_sealed, secret_hint, created_at, updated_at) VALUES ('wh1','prj_1','w','https://hook.example.org','[]',1,'x','h','t','t')`)
	ex(`INSERT INTO webhook_deliveries(id, webhook_id, project_id, event, payload_json, state, next_attempt_at, created_at) VALUES ('wd1','wh1','prj_1','doc.saved','{}','pending','t','t')`)
	ex(`INSERT INTO embed_projects(project_id, enabled, provider_id, model, created_at, updated_at) VALUES ('prj_1',1,'p','m','t','t')`)
	ex(`INSERT INTO settings(key, value, updated_at) VALUES ('pandoc.enabled','1','t'),('pandoc.path','/usr/bin/pandoc','t')`)

	r, err := backup.Create(context.Background(), filepath.Join(src, "scriobh.db"), src, backup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return r.Path
}

func TestReviewInteg7RestoreWithEverythingOn(t *testing.T) {
	archive := seedEverythingOn(t)
	dst := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dst)
	t.Setenv("SCRIOBH_SECRET_KEY", "test-secret-key-test-secret-key-0123456789")
	var out, errb bytes.Buffer
	if code := restoreCmd([]string{archive}, &out, &errb); code != 0 {
		t.Fatalf("restore: %d %s %s", code, out.String(), errb.String())
	}
	t.Log(out.String())
	d2, err := dbOpenMigrated(t, dst)
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := d2.Read().QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for what, q := range map[string]string{
		"mail on":                `SELECT count(*) FROM mail_config WHERE enabled = 1`,
		"mail outbox waiting":    `SELECT count(*) FROM mail_outbox WHERE state IN ('pending','sending')`,
		"git remote on":          `SELECT count(*) FROM git_remotes WHERE enabled = 1`,
		"git remote not paused":  `SELECT count(*) FROM git_remotes WHERE restore_paused = 0`,
		"git trigger on":         `SELECT count(*) FROM git_triggers WHERE enabled = 1`,
		"git trigger deliveries": `SELECT count(*) FROM git_trigger_deliveries WHERE state = 'pending'`,
		"webhook on":             `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		"webhook deliveries":     `SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`,
		"semantic index on":      `SELECT count(*) FROM embed_projects WHERE enabled = 1`,
		"pandoc on":              `SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`,
	} {
		if n := count(q); n != 0 {
			t.Errorf("after the restore: %s = %d", what, n)
		}
	}
	// the start-up credential check must not switch anything back on
	if _, err := gitmirror.CheckCredentials(context.Background(), d2, secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), false); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM git_remotes WHERE enabled = 1`); n != 0 {
		t.Errorf("the start-up credential check switched %d paused remote(s) on", n)
	}
	for _, m := range []string{"mail.restore-pause", "semantic.restore-pause"} {
		if _, err := os.Stat(filepath.Join(dst, m)); err == nil {
			t.Errorf("marker %s left in the data directory after a restore whose steps all succeeded", m)
		}
	}
}

// A restore that is interrupted after the files are installed (Ctrl-C, which `scriobh restore` turns into a cancelled
// context, a kill, a power cut, a step that panics) must not leave a copy that sends. Mail and the semantic index
// have a marker for this; the Git remotes, the Git triggers, the webhooks and the Pandoc bridge do not.
func TestReviewInteg7InterruptedRestoreLeavesNothingSwitchedOn(t *testing.T) {
	archive := seedEverythingOn(t)
	dst := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := backup.PostRestore
	defer func() { backup.PostRestore = saved }()
	// the operator presses Ctrl-C as the follow-up steps begin
	backup.PostRestore = append([]func(context.Context, *db.DB, []byte) (string, error){
		func(context.Context, *db.DB, []byte) (string, error) { cancel(); return "", nil },
	}, saved...)
	_, err := backup.Restore(ctx, archive, dst, backup.RestoreOptions{SecretKey: []byte("test-secret-key-test-secret-key-0123456789")})
	t.Logf("restore returned: %v", err)
	d2, err := dbOpenMigrated(t, dst)
	if err != nil {
		t.Fatal(err)
	}
	count := func(q string) int {
		var n int
		if err := d2.Read().QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	for what, q := range map[string]string{
		"git remotes switched on":        `SELECT count(*) FROM git_remotes WHERE enabled = 1`,
		"git triggers switched on":       `SELECT count(*) FROM git_triggers WHERE enabled = 1`,
		"git trigger deliveries waiting": `SELECT count(*) FROM git_trigger_deliveries WHERE state = 'pending'`,
		"webhooks switched on":           `SELECT count(*) FROM webhooks WHERE enabled = 1`,
		"webhook deliveries waiting":     `SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`,
		"the Pandoc bridge switched on":  `SELECT count(*) FROM settings WHERE key = 'pandoc.enabled' AND value = '1'`,
	} {
		if n := count(q); n != 0 {
			t.Errorf("after an interrupted restore: %s = %d, and no marker makes the next start pause it", what, n)
		}
	}
	for _, m := range []string{"mail.restore-pause", "semantic.restore-pause"} {
		if _, err := os.Stat(filepath.Join(dst, m)); err != nil {
			t.Errorf("control: the marker %s should be there (mail and semantic are covered): %v", m, err)
		}
	}
}

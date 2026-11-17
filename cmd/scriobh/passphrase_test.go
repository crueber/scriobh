package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestPassphraseClearCommand(t *testing.T) {
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
	uid := testutil.User(t, d, ops.RoleAdmin, "solo@t.local")
	if _, err := d.Writer().Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES ('ses_solo', ?, 'h', ?, ?, '2999-01-01T00:00:00.000Z')`, uid, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	p := ops.Principal{UserID: uid, Role: ops.RoleAdmin, Via: "session", SessionID: "ses_solo"}
	out, err := reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Mine"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	if _, err := reg.Call(context.Background(), p, "project.passphrase.set", projectgate.SetIn{ID: pid, Passphrase: "forgotten soon"}); err != nil {
		t.Fatal(err)
	}
	// a sole administrator who owns the project needs the passphrase to clear it through the op: that is the point of it
	if _, err := reg.Call(context.Background(), p, "project.passphrase.clear", projectgate.ClearIn{ID: pid}); err == nil {
		t.Fatal("an owner who is also the administrator cleared a passphrase without it")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"passphrase", "clear"}, &stdout, &stderr); code != 2 {
		t.Errorf("no project: exit %d", code)
	}
	if code := run([]string{"passphrase", "clear", "--project", "prj_00000000000000000000000000"}, &stdout, &stderr); code != 1 {
		t.Errorf("no such project: exit %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"passphrase", "clear", "--project", pid}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "removed") {
		t.Fatalf("exit %d: %s %s", code, stdout.String(), stderr.String())
	}
	d, err = db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var gates, audits int
	_ = d.Read().QueryRow(`SELECT count(*) FROM project_gates`).Scan(&gates)
	_ = d.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'project.passphrase.operator_clear' AND target_id = ?`, pid).Scan(&audits)
	if gates != 0 || audits != 1 {
		t.Errorf("gates %d, audit rows %d", gates, audits)
	}
	stdout.Reset()
	if code := run([]string{"passphrase", "clear", "--project", pid}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "no passphrase") {
		t.Errorf("clearing twice: %d %s", code, stdout.String())
	}
}

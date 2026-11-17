package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/secrets"
)

// The pauses a start or `restore --finish` makes must not touch credentials: a server started with a new or another
// secret key while a restore's marker stands would otherwise have the Git credential check delete sealed credentials
// that the original key still opens.
func TestFinishingARestoreNeverClearsCredentials(t *testing.T) {
	archive := seedEverythingOn(t)
	dst := t.TempDir()
	t.Setenv("SCRIOBH_DATA_DIR", dst)
	t.Setenv("SCRIOBH_SECRET_KEY", "test-secret-key-test-secret-key-0123456789")
	var out, errb bytes.Buffer
	if code := restoreCmd([]string{archive}, &out, &errb); code != 0 {
		t.Fatalf("restore: %d %s", code, errb.String())
	}
	d, err := dbOpenMigrated(t, dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE git_remotes SET secret_cipher = 'v1.sealed-by-the-original-key', enabled = 1, restore_paused = 0 WHERE id = 'gr1'`,
		`UPDATE webhooks SET enabled = 1 WHERE id = 'wh1'`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := backup.WriteMarker(dst, "test"); err != nil {
		t.Fatal(err)
	}
	pending, _, err := backup.FinishPending(context.Background(), d, dst)
	if !pending || err != nil {
		t.Fatalf("FinishPending: %v %v", pending, err)
	}
	var cipher string
	var on, hooks int
	d.Read().QueryRow(`SELECT secret_cipher, enabled FROM git_remotes WHERE id = 'gr1'`).Scan(&cipher, &on)
	d.Read().QueryRow(`SELECT count(*) FROM webhooks WHERE enabled = 1`).Scan(&hooks)
	if cipher != "v1.sealed-by-the-original-key" {
		t.Errorf("finishing the restore changed the sealed credential: %q", cipher)
	}
	if on != 0 || hooks != 0 {
		t.Errorf("finishing did not pause: remote on %d, webhooks on %d", on, hooks)
	}
	// Control: the credential check with a key and clear set (what a restore does with the key it was given) does clear
	// what that key cannot open, which is why the finish must not run it.
	if _, err := gitmirror.CheckCredentials(context.Background(), d, secrets.New([]byte("another-key-another-key-another-key-0123456789")), true); err != nil {
		t.Fatal(err)
	}
	d.Read().QueryRow(`SELECT secret_cipher FROM git_remotes WHERE id = 'gr1'`).Scan(&cipher)
	if cipher != "" {
		t.Logf("control: CheckCredentials(clear) left %q (the finish is safe either way)", cipher)
	}
}

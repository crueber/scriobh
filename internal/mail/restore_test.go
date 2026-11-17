package mail

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

func (w *world) runPostRestore() []string {
	w.t.Helper()
	var notes []string
	for _, step := range backup.PostRestore {
		msg, err := step(context.Background(), w.d, []byte("test-secret-key-test-secret-key-0123456789"))
		if err != nil {
			w.t.Fatal(err)
		}
		if msg != "" {
			notes = append(notes, msg)
		}
	}
	return notes
}

func TestARestorePausesMailAndOnlyAnAdministratorLiftsIt(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	pv := w.preview(w.bob)
	w.must(w.alice, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily})
	w.notify(w.alice, w.project, "comment", 1, w.clk.Now().Add(time.Hour))
	id := w.onlyRow().ID

	notes := w.runPostRestore()
	if len(notes) == 0 || !strings.Contains(strings.Join(notes, " "), "Mail:") {
		t.Fatalf("the restore said nothing about mail: %v", notes)
	}
	cfg := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if cfg.Enabled || !cfg.PausedByRestore || cfg.DisabledReason != PausedByRestore {
		t.Fatalf("%+v", cfg)
	}
	if len(w.rows()) != 0 {
		t.Errorf("the outbox was not emptied: %+v", w.rows())
	}
	if _, err := os.Stat(w.svc.filePath(id)); err == nil {
		w.svc.removeOrphanFilesNow(context.Background())
		if _, err := os.Stat(w.svc.filePath(id)); !os.IsNotExist(err) {
			t.Error("the attachment of a removed message stayed")
		}
	}
	// A second machine, a day later: nothing is sent, nothing is made.
	w.clk.Advance(48 * time.Hour)
	if w.pump() != 0 || w.svc.DigestOnce(context.Background()) != 0 {
		t.Error("work after a restore")
	}
	if w.smtp.Conns() != 0 || len(w.smtp.Mails()) != 0 {
		t.Error("a restored copy talked to the relay")
	}
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}))); e.Data["reason"] != "mail_off" {
		t.Errorf("send after a restore: %+v", e)
	}
	// Changing a setting, a new password and a test do not lift it (the pause is its own mark).
	w.must(w.admin, "mail.config.set", ConfigSetIn{FromName: str("Else"), Password: str(relayPW), Username: str("relayuser")})
	w.clk.Advance(10 * time.Second)
	if out := w.must(w.admin, "mail.config.test", TestIn{}).(TestOut); !out.OK {
		t.Fatalf("%+v", out)
	}
	cfg = w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if cfg.Enabled || !cfg.PausedByRestore {
		t.Fatalf("a setting or a test lifted the pause: %+v", cfg)
	}
	// Only switching it on does, and then no backlog comes with it.
	w.must(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(true)})
	cfg = w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if !cfg.Enabled || cfg.PausedByRestore || cfg.DisabledReason != "" {
		t.Fatalf("%+v", cfg)
	}
	before := len(w.smtp.Mails())
	w.pump()
	w.svc.DigestOnce(context.Background())
	w.pump()
	if len(w.smtp.Mails()) != before {
		t.Error("an old message came back")
	}
}

func TestARestoreOfAServerWithoutMailDoesNothing(t *testing.T) {
	w := newWorld(t)
	if notes := w.runPostRestore(); len(notes) != 0 {
		t.Errorf("%v", notes)
	}
	if cfg := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut); cfg.PausedByRestore {
		t.Error("a server that never had mail is marked paused")
	}
}

func TestStartingWithAPasswordTheKeyCannotOpenSwitchesMailOff(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.svc.o.Box = secretsOther()
	w.svc.checkPassword(context.Background())
	cfg := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if cfg.Enabled || !strings.Contains(cfg.DisabledReason, "secret key") {
		t.Errorf("%+v", cfg)
	}
	// Enabling again needs the password entered again.
	if _, err := w.call(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(true)}); err == nil {
		t.Error("enabled with a password nothing can read")
	}
}

func TestStartRunsTheLoopsAndStopsWithItsContext(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	ctx, cancel := context.WithCancel(context.Background())
	w.svc.Start(ctx)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	deadline := time.Now().Add(5 * time.Second)
	for len(w.smtp.Mails()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(w.smtp.Mails()) != 1 {
		t.Fatal("the dispatcher did not send")
	}
	cancel()
	done := make(chan struct{})
	go func() { w.svc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loops did not stop")
	}
	_ = db.Now()
}

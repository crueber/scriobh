package mail

import (
	"context"
	"errors"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func asOpsErr(t *testing.T, err error) *ops.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return ops.AsError(err)
}

func TestMailIsOffUntilAnAdministratorSwitchesItOn(t *testing.T) {
	w := newWorld(t)
	out := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if out.Enabled || out.Ready || out.Host != "" || out.PasswordSet {
		t.Fatalf("a fresh server: %+v", out)
	}
	// Nothing can be sent, queued or opted into while it is off.
	w.setKindle(w.bob, "bob@kindle.com")
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Code != ops.CodeConflict || e.Data["reason"] != "mail_off" {
		t.Errorf("preview with mail off: %+v", e)
	}
	if e := asOpsErr(t, mustErr(w.call(w.bob, "mail.digest.set", DigestSetIn{Enabled: true, Frequency: Daily}))); e.Data["reason"] != "mail_off" {
		t.Errorf("digest with mail off: %+v", e)
	}
	if n := w.pump(); n != 0 || len(w.smtp.Mails()) != 0 || w.smtp.Conns() != 0 {
		t.Error("something connected to a relay with mail off")
	}
	// Enabling an incomplete relay is refused.
	if e := asOpsErr(t, mustErr(w.call(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(true)}))); e.Code != ops.CodeConflict {
		t.Errorf("enable with nothing set: %+v", e)
	}
	// A person who is not an administrator cannot read or set it (404, as for the other admin pages).
	for _, op := range []string{"mail.config.get", "mail.config.set", "mail.config.test", "mail.outbox.status"} {
		var in any = ConfigGetIn{}
		switch op {
		case "mail.config.set":
			in = ConfigSetIn{}
		case "mail.config.test":
			in = TestIn{}
		case "mail.outbox.status":
			in = OutboxIn{}
		}
		if _, err := w.call(w.alice, op, in); ops.AsError(err).Code != ops.CodeNotFound {
			t.Errorf("%s as a writer: %v", op, err)
		}
	}
}

func mustErr(_ any, err error) error { return err }

func TestConfigValidationAndPasswordHandling(t *testing.T) {
	w := newWorld(t)
	bad := []struct {
		name string
		in   ConfigSetIn
	}{
		{"host with a scheme", ConfigSetIn{Host: str("smtp://mail.example.org")}},
		{"host with a port", ConfigSetIn{Host: str("mail.example.org:587")}},
		{"host with a path", ConfigSetIn{Host: str("mail.example.org/x")}},
		{"a bad port", ConfigSetIn{Port: intp(70000)}},
		{"a bad security", ConfigSetIn{Security: str("ssl")}},
		{"a sender list", ConfigSetIn{FromAddress: str("a@example.org, b@example.org")}},
		{"a sender name with a line break", ConfigSetIn{FromName: str("A\r\nBcc: x@y.z")}},
		{"a user name with a line break", ConfigSetIn{Username: str("u\r\nx")}},
		{"a size of zero", ConfigSetIn{MaxAttachmentMB: intp(0)}},
		{"a size over the limit", ConfigSetIn{MaxAttachmentMB: intp(500)}},
		{"a private key as the authority", ConfigSetIn{TLSCAPEM: str("-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----")}},
		{"text that is no certificate", ConfigSetIn{TLSCAPEM: str("hello")}},
		{"plain text to a public host", ConfigSetIn{Host: str("mail.example.org"), Security: str(SecNone), AllowPrivate: boolp(true)}},
		{"plain text without allow_private", ConfigSetIn{Host: str("127.0.0.1"), Security: str(SecNone)}},
		{"a password over plain text", ConfigSetIn{Host: str("127.0.0.1"), Security: str(SecNone), AllowPrivate: boolp(true), Username: str("u"), Password: str("p")}},
		{"a password with no user name", ConfigSetIn{Host: str("mail.example.org"), Password: str("p")}},
	}
	for _, c := range bad {
		if _, err := w.call(w.admin, "mail.config.set", c.in); err == nil {
			t.Errorf("%s was accepted", c.name)
		}
	}
	w.configure(func(in *ConfigSetIn) { in.Enabled = nil })
	var sealed string
	_ = w.d.Read().QueryRow(`SELECT password_sealed FROM mail_config`).Scan(&sealed)
	if sealed == "" || strings.Contains(sealed, relayPW) {
		t.Fatalf("stored password = %q", sealed)
	}
	got := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut)
	if !got.PasswordSet || strings.Contains(jsonOf(t, got), relayPW) {
		t.Fatalf("get: %s", jsonOf(t, got))
	}
	// Moving the relay drops the password it was sealed for; an unrelated change keeps it.
	out := w.must(w.admin, "mail.config.set", ConfigSetIn{FromName: str("Other")}).(ConfigSetOut)
	if out.PasswordCleared || !out.Item.PasswordSet {
		t.Errorf("an unrelated change dropped the password: %+v", out)
	}
	out = w.must(w.admin, "mail.config.set", ConfigSetIn{Host: str("localhost")}).(ConfigSetOut)
	if !out.PasswordCleared || out.Item.PasswordSet {
		t.Errorf("a new host kept the password: %+v", out)
	}
	if e := asOpsErr(t, mustErr(w.call(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(true)}))); e.Code != ops.CodeConflict {
		t.Errorf("enabling with a user name and no password: %+v", e)
	}
	// A password cannot be stored without a secret key.
	w2 := newWorld(t)
	w2.svc.o.Box = nil
	w2.svc.o.Box = secretsNone()
	if _, err := w2.call(w2.admin, "mail.config.set", ConfigSetIn{Host: str("mail.example.org"), Username: str("u"), Password: str("p")}); err == nil {
		t.Error("a password was stored with no secret key")
	}
}

func TestOnlyASessionAtTheBrowserCanUseTheMailOps(t *testing.T) {
	w := newWorld(t)
	calls := map[string]any{
		"mail.config.get": ConfigGetIn{}, "mail.config.set": ConfigSetIn{}, "mail.config.test": TestIn{}, "mail.outbox.status": OutboxIn{},
		"mail.digest.get": DigestGetIn{}, "mail.digest.set": DigestSetIn{Enabled: false},
		"mail.progress.get": ProgressGetIn{}, "mail.progress.set": ProgressSetIn{Enabled: false},
		"kindle.address.get": KindleAddressIn{}, "kindle.address.set": KindleAddressSetIn{Address: "a@kindle.com"}, "kindle.address.delete": KindleAddressIn{},
		"kindle.preview": KindleProjectIn{ProjectID: w.project}, "kindle.send": KindleSendIn{ProjectID: w.project, Token: strings.Repeat("x", 40)}, "kindle.log": KindleProjectIn{ProjectID: w.project},
	}
	n := 0
	for _, e := range w.reg.Entries() {
		if !strings.HasPrefix(e.Op.Name, "mail.") && !strings.HasPrefix(e.Op.Name, "kindle.") {
			continue
		}
		n++
		if !e.Op.Auth.SessionOnly {
			t.Errorf("%s is not session-only", e.Op.Name)
		}
		in, ok := calls[e.Op.Name]
		if !ok {
			t.Errorf("%s has no case here", e.Op.Name)
			continue
		}
		for _, scope := range []string{"read", "write", "admin"} {
			for _, who := range []person{w.admin, w.alice, w.bob} {
				p := who.p
				p.Via, p.Scope, p.KeyID = "apikey", scope, "key_1"
				if _, err := w.reg.Call(context.Background(), p, e.Op.Name, in); ops.AsError(err).Code != ops.CodeForbidden {
					t.Errorf("%s with an API key (%s scope): %v", e.Op.Name, scope, err)
				}
			}
		}
	}
	if n != len(calls) {
		t.Errorf("%d ops in the groups, %d cases", n, len(calls))
	}
}

func TestConfigTestGoesToTheAdministratorsOwnAddressOnly(t *testing.T) {
	w := newWorld(t)
	w.configure()
	out := w.must(w.admin, "mail.config.test", TestIn{}).(TestOut)
	if !out.OK || out.To != adminMail {
		t.Fatalf("%+v", out)
	}
	m := w.smtp.Mails()
	if len(m) != 1 || m[0].To != adminMail || m[0].From != "scriobh@example.org" || !m[0].UsedTLS || !m[0].Authed {
		t.Fatalf("%+v", m)
	}
	// The op has no input to name anyone else.
	var raw = []byte(`{"to":"victim@example.org"}`)
	if _, err := w.reg.CallJSON(context.Background(), w.admin.p, "mail.config.test", raw); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("a recipient in the request: %v", err)
	}
	// Five seconds apart at least.
	if e := asOpsErr(t, mustErr(w.call(w.admin, "mail.config.test", TestIn{}))); e.Code != ops.CodeRateLimited {
		t.Errorf("a second test at once: %+v", e)
	}
	w.clk.Advance(6 * time.Second)
	if out := w.must(w.admin, "mail.config.test", TestIn{}).(TestOut); !out.OK {
		t.Errorf("%+v", out)
	}
	// An administrator with no address has nowhere to send it.
	if _, err := w.d.Writer().Exec(`UPDATE users SET email = NULL WHERE id = ?`, w.admin.id); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(6 * time.Second)
	if e := asOpsErr(t, mustErr(w.call(w.admin, "mail.config.test", TestIn{}))); e.Code != ops.CodeConflict {
		t.Errorf("no address: %+v", e)
	}
	// A failure is a class and a sentence, and is recorded.
	if _, err := w.d.Writer().Exec(`UPDATE users SET email = ? WHERE id = ?`, adminMail, w.admin.id); err != nil {
		t.Fatal(err)
	}
	w.must(w.admin, "mail.config.set", ConfigSetIn{Password: str("wrong-password-CANARY"), Username: str("relayuser")})
	w.clk.Advance(6 * time.Second)
	bad := w.must(w.admin, "mail.config.test", TestIn{}).(TestOut)
	if bad.OK || bad.Class != string(ClassAuth) || strings.Contains(jsonOf(t, bad), "CANARY") {
		t.Errorf("%+v", bad)
	}
	if got := w.must(w.admin, "mail.config.get", ConfigGetIn{}).(ConfigOut); got.LastTestClass != "auth" {
		t.Errorf("last test = %+v", got)
	}
}

func TestKindleSendsTheEditionToTheStoredAddressOnly(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	pv := w.preview(w.bob)
	if pv.Recipient != "bob_kindle@kindle.com" || pv.FileName != "My-Book-Draft.epub" || pv.Format != "EPUB" || pv.Bytes != 5000 || pv.Sender != "scriobh@example.org" ||
		!strings.Contains(pv.Notice, "leaves this server") || pv.Token == "" || !strings.Contains(pv.SenderNote, "Approved Personal Document") {
		t.Fatalf("preview: %+v", pv)
	}
	if got := len(w.rows()); got != 0 || len(w.smtp.Mails()) != 0 {
		t.Fatal("a preview queued or sent something")
	}
	// What the edition is held to, whatever a preset says.
	req := w.requests()[0]
	if req.Preset != "epub3" || req.Options.Notes != "none" || req.Options.Ghosts != "omit" || req.Options.Contact == nil || *req.Options.Contact {
		t.Errorf("compile request: %+v", req)
	}
	out := w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}).(KindleSendOut)
	if !out.Queued || out.RecipientDomain != "kindle.com" {
		t.Fatalf("%+v", out)
	}
	if n := w.pump(); n != 1 {
		t.Fatalf("pump = %d", n)
	}
	m := w.smtp.Mails()
	if len(m) != 1 || m[0].To != "bob_kindle@kindle.com" || !m[0].UsedTLS {
		t.Fatalf("%+v", m)
	}
	if !strings.Contains(m[0].Data, "application/epub+zip") || !strings.Contains(m[0].Data, `filename="My-Book-Draft.epub"`) {
		t.Errorf("attachment headers missing:\n%.800s", m[0].Data)
	}
	r := w.onlyRow()
	if r.State != "sent" || r.Recipient != "" || r.Subject != "" || r.Body != "" || r.FileName != "" {
		t.Errorf("a sent row keeps text: %+v", r)
	}
	if _, err := os.Stat(w.svc.filePath(r.ID)); !os.IsNotExist(err) {
		t.Error("the attachment file was kept after sending")
	}
	// The log: who, what, when, hash, size, domain; no address.
	lg := w.must(w.bob, "kindle.log", KindleProjectIn{ProjectID: w.project}).(KindleLogOut)
	if len(lg.Items) != 1 || lg.Items[0].By != "Bob Editor" || lg.Items[0].State != "sent" || lg.Items[0].RecipientDomain != "kindle.com" || lg.Items[0].Bytes != 5000 || lg.Items[0].InputHash == "" || len(lg.Items[0].SHA256) != 64 {
		t.Errorf("log: %+v", lg)
	}
	if strings.Contains(jsonOf(t, lg), "bob_kindle") {
		t.Error("the send log carries the address")
	}
	// The token is used once.
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}))); e.Data["reason"] != "preview_gone" {
		t.Errorf("a used token: %+v", e)
	}
}

func TestKindleRecipientCannotBeOverridden(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.setKindle(w.alice, "alice_kindle@kindle.com")
	pv := w.preview(w.bob)
	for _, field := range []string{"to", "recipient", "address", "email", "cc", "bcc", "kindle_address", "user_id", "person"} {
		raw := []byte(`{"token":"` + pv.Token + `","` + field + `":"attacker@example.org"}`)
		for _, op := range []string{"kindle.send", "kindle.preview"} {
			in := ops.Entry{}
			_ = in
			if _, err := w.reg.CallJSON(context.Background(), w.bob.p, op, raw); err == nil || ops.AsError(err).Code != ops.CodeInvalid {
				t.Errorf("%s accepted the field %q: %v", op, field, err)
			}
		}
	}
	// The struct itself has no such field.
	for _, typ := range []any{KindleSendIn{}, KindleProjectIn{}} {
		if s := jsonOf(t, typ); strings.Contains(s, "address") || strings.Contains(s, "recipient") {
			t.Errorf("%T carries %s", typ, s)
		}
	}
	// Another person's preview token does nothing for me, and mine is bound to my project and address.
	if e := asOpsErr(t, mustErr(w.call(w.alice, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}))); e.Data["reason"] != "preview_gone" {
		t.Errorf("another person's token: %+v", e)
	}
	other := w.addProject("Other Book", w.bob, nil)
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.send", KindleSendIn{ProjectID: other, Token: pv.Token}))); e.Data["reason"] != "preview_gone" {
		t.Errorf("another project's token: %+v", e)
	}
	// The address changed after the preview: refused, and nothing waited for the old one.
	w.setKindle(w.bob, "new_bob@kindle.com")
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}))); e.Data["reason"] != "preview_gone" && e.Data["reason"] != "preview_changed" {
		t.Errorf("a preview for another address: %+v", e)
	}
	pv = w.preview(w.bob)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	w.pump()
	if m := w.smtp.Mails(); len(m) != 1 || m[0].To != "new_bob@kindle.com" {
		t.Fatalf("%+v", m)
	}
}

func TestKindleChangedEditionDeletedAddressAndRoles(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	pv := w.preview(w.bob)
	w.editionN++ // the manuscript changed after the preview
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}))); e.Data["reason"] != "preview_changed" {
		t.Errorf("a changed edition: %+v", e)
	}
	if len(w.rows()) != 0 {
		t.Error("a stale preview queued a message")
	}
	// A reader cannot send; an editor can; a stranger cannot even see the project.
	if e := asOpsErr(t, mustErr(w.call(w.carol, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Code != ops.CodeForbidden {
		t.Errorf("a reader: %+v", e)
	}
	stranger := w.user(ops.RoleWriter, "stranger@example.com", "Stranger")
	if e := asOpsErr(t, mustErr(w.call(stranger, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Code != ops.CodeNotFound {
		t.Errorf("a stranger: %+v", e)
	}
	// No address, no preview.
	if e := asOpsErr(t, mustErr(w.call(w.alice, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Data["reason"] != "no_kindle_address" {
		t.Errorf("no address: %+v", e)
	}
	// Deleting the address cancels what waits for it.
	pv = w.preview(w.bob)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	id := w.onlyRow().ID
	if _, err := os.Stat(w.svc.filePath(id)); err != nil {
		t.Fatalf("no attachment file to cancel: %v", err)
	}
	w.must(w.bob, "kindle.address.delete", KindleAddressIn{})
	if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" || r.Recipient != "" {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(w.svc.filePath(id)); !os.IsNotExist(err) {
		t.Error("the attachment of a cancelled message stayed on disk")
	}
	if w.pump() != 0 || len(w.smtp.Mails()) != 0 {
		t.Error("a message went to an address that was deleted")
	}
	if got := w.must(w.bob, "kindle.address.get", KindleAddressIn{}).(KindleAddressOut); got.Address != "" {
		t.Errorf("%+v", got)
	}
	// The address is validated: one address, no list, no name.
	for _, a := range []string{"a@kindle.com, b@kindle.com", "Name <a@kindle.com>", "a@kindle.com\r\nBcc: x@y.z", "", "nodomain"} {
		if _, err := w.call(w.bob, "kindle.address.set", KindleAddressSetIn{Address: a}); err == nil {
			t.Errorf("stored %q", a)
		}
	}
}

func TestKindleRefusesATrashedProjectAndAnOversizedEdition(t *testing.T) {
	w := newWorld(t)
	w.configure(func(in *ConfigSetIn) { in.MaxAttachmentMB = intp(1) })
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.bodySize = 2 << 20
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Code != ops.CodeTooLarge {
		t.Errorf("an oversized edition: %+v", e)
	}
	w.bodySize = 5000
	pv := w.preview(w.bob)
	if _, err := w.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, "2026-06-01T12:00:00.000Z", w.project); err != nil {
		t.Fatal(err)
	}
	for _, op := range []struct {
		name string
		in   any
	}{{"kindle.preview", KindleProjectIn{ProjectID: w.project}}, {"kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token}}} {
		if e := asOpsErr(t, mustErr(w.call(w.alice, op.name, op.in))); e.Code != ops.CodeConflict || e.Data["reason"] != "project_trashed" {
			t.Errorf("%s on a trashed project: %+v", op.name, e)
		}
	}
	if len(w.rows()) != 0 {
		t.Error("something was queued for a trashed project")
	}
}

func TestAMessageForAProjectTrashedMeanwhileIsNotSent(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	pv := w.preview(w.bob)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	if _, err := w.d.Writer().Exec(`UPDATE projects SET trashed_at = ? WHERE id = ?`, "2026-06-01T12:00:00.000Z", w.project); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" {
		t.Errorf("%+v", r)
	}
	if len(w.smtp.Mails()) != 0 {
		t.Error("sent about a trashed project")
	}
	if _, err := os.Stat(w.svc.filePath(w.rows()[0].ID)); !os.IsNotExist(err) {
		t.Error("the attachment stayed")
	}
}

func TestDisabledOrRemovedPeopleAndMembersReceiveNothing(t *testing.T) {
	cases := map[string]string{
		"disabled":  `UPDATE users SET disabled_at = '2026-06-01T12:00:00.000Z' WHERE id = ?`,
		"deleted":   `UPDATE users SET deleted_at = '2026-06-01T12:00:00.000Z' WHERE id = ?`,
		"demoted":   `UPDATE project_members SET role = 'reader' WHERE user_id = ?`,
		"removed":   `DELETE FROM project_members WHERE user_id = ?`,
		"no-more-a": `UPDATE users SET is_agent = 1 WHERE id = ?`,
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.configure()
			w.setKindle(w.bob, "bob_kindle@kindle.com")
			pv := w.preview(w.bob)
			w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
			if _, err := w.d.Writer().Exec(q, w.bob.id); err != nil {
				t.Fatal(err)
			}
			w.pump()
			if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" {
				t.Errorf("%+v", r)
			}
			if len(w.smtp.Mails()) != 0 {
				t.Error("sent")
			}
		})
	}
}

func TestRetriesBackOffAndPermanentFailuresStop(t *testing.T) {
	w := newWorld(t, func(o *wopt) {
		o.smtpMod = append(o.smtpMod, func(f *fakeSMTP) { f.Replies["RCPT"] = "451 4.3.0 try later" })
	})
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	var waits []time.Duration
	for i := 1; i <= 4; i++ {
		if w.pump() != 1 {
			t.Fatalf("attempt %d did not run", i)
		}
		r := w.onlyRow()
		if r.State != "pending" || r.Err != "temporary" || r.Attempts != i {
			t.Fatalf("after attempt %d: %+v", i, r)
		}
		next, _ := time.Parse("2006-01-02T15:04:05.000Z", r.Next)
		waits = append(waits, next.Sub(w.clk.Now()))
		if w.pump() != 0 {
			t.Fatal("retried before the backoff")
		}
		w.clk.Advance(next.Sub(w.clk.Now()))
	}
	want := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i+1, waits[i], want[i])
		}
	}
	// The relay recovers: the next attempt delivers once.
	w.smtp.mu.Lock()
	delete(w.smtp.Replies, "RCPT")
	w.smtp.mu.Unlock()
	if w.pump() != 1 {
		t.Fatal("no attempt")
	}
	if r := w.onlyRow(); r.State != "sent" || r.Attempts != 5 {
		t.Errorf("%+v", r)
	}
	if len(w.smtp.Mails()) != 1 {
		t.Errorf("%d messages", len(w.smtp.Mails()))
	}
}

func TestTheAttemptCapGivesUp(t *testing.T) {
	w := newWorld(t, func(o *wopt) {
		o.lim = Limits{MaxAttempts: 3}
		o.smtpMod = append(o.smtpMod, func(f *fakeSMTP) { f.Replies["MAIL"] = "421 busy" })
	})
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	for i := 0; i < 3; i++ {
		w.pump()
		w.clk.Advance(3 * time.Hour)
	}
	r := w.onlyRow()
	if r.State != "dead" || r.Attempts != 3 || r.Err != "temporary" {
		t.Errorf("%+v", r)
	}
	if w.pump() != 0 {
		t.Error("tried a dead message")
	}
}

func TestPermanentRejectionIsDeadAtOnceAndKeepsNoText(t *testing.T) {
	w := newWorld(t, func(o *wopt) {
		o.smtpMod = append(o.smtpMod, func(f *fakeSMTP) {
			f.Replies["RCPT"] = "550 5.1.1 mailbox bob_kindle@kindle.com does not exist SECRET-RELAY-TEXT"
		})
	})
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	r := w.onlyRow()
	if r.State != "dead" || r.Err != "rejected" || r.Attempts != 1 || r.Recipient != "" {
		t.Errorf("%+v", r)
	}
	if lg := w.must(w.bob, "kindle.log", KindleProjectIn{ProjectID: w.project}).(KindleLogOut); lg.Items[0].State != "failed" {
		t.Errorf("%+v", lg)
	}
	if strings.Contains(w.logs.String(), "SECRET-RELAY-TEXT") || strings.Contains(w.dump(), "SECRET-RELAY-TEXT") {
		t.Error("what the relay said was kept")
	}
}

func TestACrashAfterTheBodyDoesNotResend(t *testing.T) {
	crash := errors.New("crash")
	w := newWorld(t, func(o *wopt) { o.afterSend = func(string) error { return crash } })
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	if r := w.onlyRow(); r.State != "sending" || r.DataStarted == "" {
		t.Fatalf("the crash left %+v", r)
	}
	if len(w.smtp.Mails()) != 1 {
		t.Fatal("the relay did not get it")
	}
	// A restarted process: the message is given up on as unknown, never sent again.
	svc2 := New(Options{DB: w.d, Box: w.box, DataDir: w.dir, Now: w.clk.Now, Client: w.svc.client})
	if err := svc2.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := w.onlyRow(); r.State != "dead" || r.Err != "delivery_unknown" {
		t.Errorf("%+v", r)
	}
	w.clk.Advance(2 * time.Hour)
	svc2.DispatchOnce(context.Background())
	w.pump()
	if len(w.smtp.Mails()) != 1 {
		t.Errorf("sent %d times", len(w.smtp.Mails()))
	}
	if lg := w.must(w.bob, "kindle.log", KindleProjectIn{ProjectID: w.project}).(KindleLogOut); lg.Items[0].State != "unknown" {
		t.Errorf("%+v", lg)
	}
}

func TestACrashBeforeTheBodyIsTriedAgainOnce(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	// The process died after claiming the message and before giving the relay its body.
	if _, err := w.d.Writer().Exec(`UPDATE mail_outbox SET state = 'sending', claimed_at = ?, attempts = 1`, "2026-06-01T12:00:00.000Z"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r := w.onlyRow(); r.State != "pending" {
		t.Fatalf("%+v", r)
	}
	w.pump()
	w.pump()
	if len(w.smtp.Mails()) != 1 || w.onlyRow().State != "sent" {
		t.Errorf("%d messages, %+v", len(w.smtp.Mails()), w.onlyRow())
	}
}

func TestAnAnswerLostAfterTheBodyIsNotRetried(t *testing.T) {
	w := newWorld(t, func(o *wopt) { o.smtpMod = append(o.smtpMod, func(f *fakeSMTP) { f.DropAfterData = true }) })
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	if r := w.onlyRow(); r.State != "dead" || r.Err != "delivery_unknown" {
		t.Errorf("%+v", r)
	}
	w.clk.Advance(time.Hour)
	if w.pump() != 0 {
		t.Error("tried again")
	}
}

func TestARelayLevelFailureHoldsSendingWithoutSpendingAttempts(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.setKindle(w.alice, "alice_kindle@kindle.com")
	for _, p := range []person{w.bob, w.alice} {
		w.must(p, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(p).Token})
	}
	w.must(w.admin, "mail.config.set", ConfigSetIn{Password: str("wrong-CANARY"), Username: str("relayuser")})
	w.pump()
	conns := w.smtp.Conns()
	for _, r := range w.rows() {
		if r.State != "pending" || r.Err != "auth" && r.Err != "" || r.Attempts != 0 {
			t.Errorf("%+v", r)
		}
	}
	if conns != 1 {
		t.Errorf("%d connections: the relay was hammered", conns)
	}
	if _, _, held := w.svc.Hold(); !held {
		t.Error("sending is not held")
	}
	w.clk.Advance(time.Minute)
	if w.pump() != 0 || w.smtp.Conns() != conns {
		t.Error("tried during the hold")
	}
	// Fixing the settings lifts the hold; both messages go.
	w.must(w.admin, "mail.config.set", ConfigSetIn{Password: str(relayPW)})
	w.pump()
	if len(w.smtp.Mails()) != 2 {
		t.Errorf("%d delivered", len(w.smtp.Mails()))
	}
	if strings.Contains(w.logs.String(), "CANARY") {
		t.Error("a password reached the log")
	}
}

func TestAMessageTooOldIsNotSent(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.clk.Advance(25 * time.Hour)
	w.pump()
	if r := w.onlyRow(); r.State != "dead" || r.Err != "expired" {
		t.Errorf("%+v", r)
	}
	if len(w.smtp.Mails()) != 0 {
		t.Error("sent a stale message")
	}
}

func TestSwitchingMailOffDropsWhatWaits(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	id := w.onlyRow().ID
	w.must(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(false)})
	if r := w.onlyRow(); r.State != "dead" || r.Err != "paused" || r.Recipient != "" {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(w.svc.filePath(id)); !os.IsNotExist(err) {
		t.Error("the attachment stayed")
	}
	if w.pump() != 0 || len(w.smtp.Mails()) != 0 {
		t.Error("sent with mail off")
	}
	// Switched back on, nothing old comes back.
	w.configure(func(in *ConfigSetIn) { in.Password = nil; in.Host = nil; in.Port = nil; in.Username = nil })
	w.pump()
	if len(w.smtp.Mails()) != 0 {
		t.Error("an old message was sent after switching back on")
	}
}

func TestQuotasAndRateLimits(t *testing.T) {
	w := newWorld(t, func(o *wopt) {
		o.lim = Limits{PerPersonQueue: 3, TotalQueue: 5, TotalQueueBytes: 3000, KindlePerHour: 4, GlobalPerHour: 7}
	})
	w.configure()
	job := func(p person, n int, file int) Job {
		j := Job{Kind: KindKindle, PersonID: p.id, ProjectID: w.project, Recipient: p.email, Subject: "s", Body: "b"}
		if file > 0 {
			j.File = &File{Name: "a.epub", Type: epubType, Data: make([]byte, file)}
		}
		return j
	}
	var le *LimitError
	// Per-person queue bound.
	for i := 0; i < 3; i++ {
		if err := w.enqueueRaw(job(w.bob, i, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.enqueueRaw(job(w.bob, 4, 0)); !errors.As(err, &le) || le.Scope != "person_queue" {
		t.Errorf("fourth waiting message: %v", err)
	}
	// Another person is not held back by Bob's queue; the total bound is shared.
	for i := 0; i < 2; i++ {
		if err := w.enqueueRaw(job(w.alice, i, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.enqueueRaw(job(w.carol, 0, 0)); !errors.As(err, &le) || le.Scope != "queue" {
		t.Errorf("past the total: %v", err)
	}
	// Attachment bytes waiting are bounded.
	w2 := newWorld(t, func(o *wopt) { o.lim = Limits{TotalQueueBytes: 3000} })
	w2.configure()
	if err := w2.enqueueRaw(Job{Kind: KindKindle, PersonID: w2.bob.id, Recipient: "b@kindle.com", Subject: "s", File: &File{Name: "a.epub", Type: epubType, Data: make([]byte, 2000)}}); err != nil {
		t.Fatal(err)
	}
	if err := w2.enqueueRaw(Job{Kind: KindKindle, PersonID: w2.alice.id, Recipient: "a@kindle.com", Subject: "s", File: &File{Name: "a.epub", Type: epubType, Data: make([]byte, 2000)}}); !errors.As(err, &le) || le.Scope != "queue_bytes" {
		t.Errorf("past the byte bound: %v", err)
	}
	// An oversized file is refused before it is queued.
	if err := w2.enqueueRaw(Job{Kind: KindKindle, PersonID: w2.carol.id, Recipient: "c@kindle.com", Subject: "s", MaxFileLen: 100, File: &File{Name: "a.epub", Type: epubType, Data: make([]byte, 200)}}); !errors.As(err, &le) || le.Scope != "file_size" {
		t.Errorf("oversized: %v", err)
	}
	// The hourly limits count what was made, sent or not.
	w3 := newWorld(t, func(o *wopt) { o.lim = Limits{KindlePerHour: 4, GlobalPerHour: 7, PerPersonQueue: 50} })
	w3.configure()
	for i := 0; i < 4; i++ {
		if err := w3.enqueueRaw(job(w3.bob, i, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w3.enqueueRaw(job(w3.bob, 5, 0)); !errors.As(err, &le) || le.Scope != "person_rate" || le.RetryAfter == 0 {
		t.Errorf("fifth in an hour: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := w3.enqueueRaw(job(w3.alice, i, 0)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w3.enqueueRaw(job(w3.carol, 0, 0)); !errors.As(err, &le) || le.Scope != "global_rate" {
		t.Errorf("past the instance's hour: %v", err)
	}
	w3.clk.Advance(61 * time.Minute)
	if err := w3.enqueueRaw(job(w3.bob, 6, 0)); err != nil {
		t.Errorf("an hour later: %v", err)
	}
	// Through the op: a rate limit is a 429 with a wait.
	w4 := newWorld(t, func(o *wopt) { o.lim = Limits{KindlePerHour: 1} })
	w4.configure()
	w4.setKindle(w4.bob, "bob_kindle@kindle.com")
	w4.must(w4.bob, "kindle.send", KindleSendIn{ProjectID: w4.project, Token: w4.preview(w4.bob).Token})
	if e := asOpsErr(t, mustErr(w4.call(w4.bob, "kindle.preview", KindleProjectIn{ProjectID: w4.project}))); e.Code != ops.CodeRateLimited || e.RetryAfterSeconds == 0 {
		t.Errorf("preview past the rate: %+v", e)
	}
}

func TestTheDispatcherIsRateLimited(t *testing.T) {
	w := newWorld(t, func(o *wopt) { o.lim = Limits{RatePerMin: 1, PerPersonQueue: 50, KindlePerHour: 50} })
	w.configure()
	for i := 0; i < 7; i++ {
		if err := w.enqueueRaw(Job{Kind: KindKindle, PersonID: w.bob.id, ProjectID: w.project, Recipient: "bob@kindle.com", Subject: "s", Body: "b"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.d.Writer().Exec(`INSERT INTO kindle_addresses(user_id, address, created_at, updated_at) VALUES (?,?,?,?)`, w.bob.id, "bob@kindle.com", "x", "x"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	for i := 0; i < 4 && ctx.Err() == nil; i++ {
		w.svc.DispatchOnce(ctx)
	}
	sent, pending := 0, 0
	for _, r := range w.rows() {
		switch r.State {
		case "sent":
			sent++
		case "pending":
			pending++
			if r.Attempts != 0 {
				t.Errorf("a message waiting for a turn spent an attempt: %+v", r)
			}
		}
	}
	if sent != 5 || pending != 2 {
		t.Errorf("sent %d, pending %d (the burst is five, then one a minute)", sent, pending)
	}
}

func TestOrphanFilesAreSweptAndNewOnesAreLeftAlone(t *testing.T) {
	w := newWorld(t)
	old := w.svc.filePath("mel_01HZZZZZZZZZZZZZZZZZZZZZZZ")
	fresh := w.svc.filePath("mel_01HYYYYYYYYYYYYYYYYYYYYYYY")
	if err := os.MkdirAll(filepath.Dir(old), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := w.clk.Now().Add(-time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(fresh, w.clk.Now(), w.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an orphan stayed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a new file was removed under the enqueue that wrote it")
	}
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	id := w.onlyRow().ID
	fi, err := os.Stat(w.svc.filePath(id))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("attachment file: %v %v", fi, err)
	}
}

func TestFinishedRowsAreSweptAfterTheirKeepTime(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	w.clk.Advance(13 * 24 * time.Hour)
	_ = w.svc.Sweep(context.Background())
	if len(w.rows()) != 1 {
		t.Fatal("swept too early")
	}
	w.clk.Advance(2 * 24 * time.Hour)
	_ = w.svc.Sweep(context.Background())
	if len(w.rows()) != 0 {
		t.Fatal("not swept")
	}
}

func TestAnIdleDispatchIsCheap(t *testing.T) {
	w := newWorld(t)
	w.configure()
	start := time.Now()
	const n = 2000
	for i := 0; i < n; i++ {
		if w.svc.DispatchOnce(context.Background()) != 0 {
			t.Fatal("work from nowhere")
		}
	}
	per := time.Since(start) / n
	t.Logf("an idle dispatch pass: %v", per)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if per > 2*time.Millisecond {
			t.Errorf("an idle pass takes %v (budget 2 ms: one indexed query)", per)
		}
	})
	if w.smtp.Conns() != 0 {
		t.Error("an idle pass connected")
	}
}

// The largest edition the default allows goes through the whole path: preview, send, queue, the relay, intact.
func TestATwelveMegabyteEditionGoesThroughTheWholePath(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.bodySize = 12<<20 - 10
	start := time.Now()
	pv := w.preview(w.bob)
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	w.pump()
	t.Logf("preview, send and delivery of %d bytes: %v", pv.Bytes, time.Since(start))
	m := w.smtp.Mails()
	if len(m) != 1 {
		t.Fatalf("%d messages", len(m))
	}
	if len(m[0].Data) < 12<<20 || len(m[0].Data) > 17<<20 {
		t.Errorf("the relay got %d bytes", len(m[0].Data))
	}
	w.bodySize = 12<<20 + 1<<20
	if e := asOpsErr(t, mustErr(w.call(w.bob, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Code != ops.CodeTooLarge {
		t.Errorf("over the default cap: %+v", e)
	}
}

func TestAnAddressChangeRemovesTheFilesOfWhatWaited(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	id := w.onlyRow().ID
	w.setKindle(w.bob, "bob_new@kindle.com")
	if r := w.onlyRow(); r.State != "dead" || r.Err != "cancelled" {
		t.Errorf("%+v", r)
	}
	if _, err := os.Stat(w.svc.filePath(id)); !os.IsNotExist(err) {
		t.Error("the file of a message cancelled by a new address stayed")
	}
}

// The registry lets an instance administrator act as the owner of any project (the authorization sweeps pin that), so
// an administrator may send a project's edition to their own Kindle; the message is then sent, not silently
// cancelled when the dispatcher asks the same question. A person who is nobody on the project is refused at the op.
func TestAnAdministratorActsAsOwnerAtTheOpAndAtSendTime(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.admin, "admin_kindle@kindle.com")
	pv := w.preview(w.admin)
	w.must(w.admin, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	w.pump()
	if m := w.smtp.Mails(); len(m) != 1 || m[0].To != "admin_kindle@kindle.com" {
		t.Fatalf("%+v", m)
	}
	// An ordinary person who is not on the project is refused at the op.
	stranger := w.user(ops.RoleWriter, "stranger2@example.com", "Stranger")
	w.setKindle(stranger, "s_kindle@kindle.com")
	if e := asOpsErr(t, mustErr(w.call(stranger, "kindle.preview", KindleProjectIn{ProjectID: w.project}))); e.Code != ops.CodeNotFound {
		t.Errorf("%+v", e)
	}
}

func TestKindleMailIsNotMarkedAsAutomatic(t *testing.T) {
	w := newWorld(t)
	w.configure()
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	w.must(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: w.preview(w.bob).Token})
	w.pump()
	m := w.smtp.Mails()
	if len(m) != 1 {
		t.Fatal("not sent")
	}
	msg, err := mail.ReadMessage(strings.NewReader(m[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if h := msg.Header; h.Get("Auto-Submitted") != "" || h.Get("List-Unsubscribe") != "" {
		t.Errorf("headers: %v", h)
	}
}

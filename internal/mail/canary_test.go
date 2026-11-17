package mail

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func canaryForms(s string) []string {
	return []string{s, base64.StdEncoding.EncodeToString([]byte(s)), base64.RawStdEncoding.EncodeToString([]byte(s)), base64.URLEncoding.EncodeToString([]byte(s)),
		base64.RawURLEncoding.EncodeToString([]byte(s)), hex.EncodeToString([]byte(s)), url.QueryEscape(s), "\x00" + "relayuser" + "\x00" + s}
}

// The relay's password is in the sealed column and nowhere else: not in a result, an error, the log, an event,
// the audit trail, the database as a whole (and so a backup of it), or any file under the data directory.
func TestTheRelayPasswordIsNowhereButSealed(t *testing.T) {
	w := newWorld(t)
	var seen []string
	rec := func(v any, err error) {
		if err != nil {
			seen = append(seen, err.Error(), jsonOf(t, ops_asError(err)))
		}
		if v != nil {
			seen = append(seen, jsonOf(t, v))
		}
	}
	call := func(p person, op string, in any) { rec(w.call(p, op, in)) }
	call(w.admin, "mail.config.set", ConfigSetIn{Enabled: boolp(true), Host: str("127.0.0.1"), Port: intp(w.smtp.Port()), Security: str(SecStartTLS), AllowPrivate: boolp(true), TLSCAPEM: str(string(w.smtp.CertPEM)),
		Username: str("relayuser"), Password: str(relayPW), FromAddress: str("scriobh@example.org")})
	call(w.admin, "mail.config.get", ConfigGetIn{})
	w.clk.Advance(10 * time.Second)
	call(w.admin, "mail.config.test", TestIn{})
	call(w.admin, "mail.outbox.status", OutboxIn{})
	w.setKindle(w.bob, "bob_kindle@kindle.com")
	pv := w.preview(w.bob)
	call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	w.pump()
	call(w.bob, "kindle.log", KindleProjectIn{ProjectID: w.project})
	// A failed login: the relay echoes what it was sent.
	bad := "wrong-" + relayPW
	call(w.admin, "mail.config.set", ConfigSetIn{Password: str(bad)})
	w.clk.Advance(10 * time.Second)
	call(w.admin, "mail.config.test", TestIn{})
	pv = w.preview(w.bob)
	call(w.bob, "kindle.send", KindleSendIn{ProjectID: w.project, Token: pv.Token})
	w.pump()
	call(w.admin, "mail.outbox.status", OutboxIn{})
	call(w.admin, "mail.config.set", ConfigSetIn{Password: str(relayPW)})
	w.pump()

	var hay = map[string]string{"results": strings.Join(seen, "\n"), "log": w.logs.String(), "database": w.dump()}
	var files strings.Builder
	_ = filepath.Walk(w.dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			files.WriteString(p + "\n")
			files.Write(b)
		}
		return nil
	})
	hay["data directory"] = files.String()
	bk := filepath.Join(t.TempDir(), "copy.db")
	if err := w.d.Backup(context.Background(), bk); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(bk)
	hay["a backup of the database"] = string(b)
	for _, secret := range []string{relayPW, bad} {
		for _, form := range canaryForms(secret) {
			for where, text := range hay {
				if strings.Contains(text, form) {
					t.Errorf("the password (%q form) is in %s", form[:min(len(form), 12)], where)
				}
			}
		}
	}
	// What is stored is sealed, and bound to the relay it was entered for.
	var sealed string
	_ = w.d.Read().QueryRow(`SELECT password_sealed FROM mail_config`).Scan(&sealed)
	if !strings.HasPrefix(sealed, "v1.") || !w.box.CanOpen(sealed, sealContext("127.0.0.1", w.smtp.Port(), "relayuser")) || w.box.CanOpen(sealed, sealContext("127.0.0.2", w.smtp.Port(), "relayuser")) {
		t.Errorf("sealed = %q", sealed)
	}
	// Every failure the outbox kept is a class from the list, never a sentence.
	known := map[string]bool{"": true}
	for _, c := range []Class{ClassConfig, ClassBlocked, ClassDNS, ClassRefused, ClassNetwork, ClassTimeout, ClassTLS, ClassCert, ClassNoStartTLS, ClassAuth, ClassTemporary, ClassRejected, ClassTooLarge,
		ClassProtocol, ClassUnknown, ClassExpired, ClassCancelled, ClassPaused, ClassRateLimited, ClassInternal} {
		known[string(c)] = true
	}
	for _, r := range w.rows() {
		if !known[r.Err] {
			t.Errorf("last_error = %q", r.Err)
		}
	}
	// The audit trail names what changed, not the values.
	var detail string
	_ = w.d.Read().QueryRow(`SELECT group_concat(detail_json, " ") FROM audit_log WHERE action LIKE 'mail.%' OR action LIKE 'kindle.%'`).Scan(&detail)
	if detail == "" || !strings.Contains(detail, `"fields"`) || strings.Contains(detail, "relayuser") || strings.Contains(detail, "bob_kindle") {
		t.Errorf("audit: %s", detail)
	}
}

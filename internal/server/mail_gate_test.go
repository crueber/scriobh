package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/mail"
	"github.com/crueber/scriobh/internal/ops"
)

// Send to Kindle on a project with a passphrase: a member's session that has not unlocked it is refused at the
// preview and the send (by the registry's gate and the nested export), one that has may send, and a passphrase set
// or changed while an edition waits in the outbox stops it at the moment of sending.
func TestKindleOnAProtectedProjectNeedsTheUnlockAndAChangedPassphraseStopsWhatWaits(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	admin := s.newCaller(t, "mailadmin", ops.RoleAdmin)
	if rec := s.do(admin, "PUT", "/api/v1/admin/mail", `{"host":"127.0.0.1","port":2525,"security":"none","allow_private":true,"from_address":"scriobh@example.org","enabled":true}`); rec.Code != 200 {
		t.Fatalf("mail config: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.editor, "PUT", "/api/v1/me/kindle", `{"address":"editor@kindle.com"}`); rec.Code != 200 {
		t.Fatalf("address: %d %s", rec.Code, rec.Body)
	}
	// Locked: refused, and nothing was compiled or queued.
	for _, c := range []struct{ path, body string }{{"/kindle/preview", `{}`}, {"/kindle/send", `{"token":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`}} {
		if rec := s.do(w.editor, "POST", w.base()+c.path, c.body); rec.Code != 403 || !lockedBody(rec.Body.String()) {
			t.Errorf("%s with a locked session: %d %s", c.path, rec.Code, rec.Body)
		}
	}
	if rec := s.do(w.editor, "GET", w.base()+"/kindle/log", ""); rec.Code != 403 {
		t.Errorf("the log of a locked project: %d", rec.Code)
	}
	if rec := w.unlock(w.editor, w.pass); rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	pv := s.do(w.editor, "POST", w.base()+"/kindle/preview", `{}`)
	if pv.Code != 200 {
		t.Fatalf("an unlocked session's preview: %d %s", pv.Code, pv.Body)
	}
	var preview mail.KindlePreviewOut
	if err := json.Unmarshal(pv.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if rec := s.do(w.editor, "POST", w.base()+"/kindle/send", fmt.Sprintf(`{"token":%q}`, preview.Token)); rec.Code != 200 {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	// The owner changes the passphrase while the edition waits: it is not sent.
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"another pass phrase","current":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	s.Mail.DispatchOnce(t.Context())
	var state, class string
	if err := s.DB.Read().QueryRow(`SELECT state, last_error FROM mail_outbox`).Scan(&state, &class); err != nil {
		t.Fatal(err)
	}
	if state != "dead" || class != "cancelled" {
		t.Errorf("the edition after the passphrase changed: %s %s", state, class)
	}
	// And a digest never names the protected project, with or without a session.
	body, err := s.Mail.BuildDigest(t.Context(), w.editor.userID, "", "9999-12-31T00:00:00.000Z")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "Gate Book") {
		t.Errorf("a digest named a protected project:\n%s", body)
	}
}

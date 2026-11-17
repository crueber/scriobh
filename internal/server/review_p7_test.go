package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// REVIEW p7-pass: the passphrase check and the unlock are two steps. unlock verifies the passphrase
// against the hash it read first, then a later transaction reads whatever epoch is current. A concurrent
// project.passphrase.set (the rotation that is meant to void the old passphrase) commits in between, and the
// unlock row lands in the NEW epoch although only the OLD passphrase was proved.
func TestP7ReviewOldPassphraseCannotUnlockAfterRotation(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	old := w.pass
	var hits []string
	for i := 0; i < 16; i++ {
		neu := fmt.Sprintf("rotated passphrase %d", i)
		e := s.sessionFor(t, "late", w.editor.userID)
		done := make(chan int, 1)
		delay := time.Duration(15+i*3) * time.Millisecond
		go func() {
			time.Sleep(delay)
			done <- w.unlock(e, old).Code
		}()
		rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":`+jsonString(neu)+`,"current":`+jsonString(old)+`}`)
		if rec.Code != 200 {
			t.Fatalf("rotate: %d %s", rec.Code, rec.Body)
		}
		code := <-done
		if code == 200 && !w.isLocked(e) {
			hits = append(hits, fmt.Sprintf("delay %v", delay))
		}
		old = neu
	}
	if len(hits) > 0 {
		t.Errorf("the OLD passphrase unlocked the project after it was rotated, %d of 16 times (%v)", len(hits), hits)
	}
}

// REVIEW p7-pass: CallDry builds an Env with no services, so the projects the gate has locked are not
// found (ops.Service returns nothing and LockedProjects answers "none"). A preview of notification.read_all counts
// the notifications of a locked project, the real call does not.
func TestP7ReviewDryRunAgreesWithTheRealCallWhileLocked(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	dry := s.doH(w.editor, "POST", "/api/v1/notifications/read-all", "", map[string]string{"X-Scriobh-Dry-Run": "1"})
	var d struct{ Changed int }
	_ = json.Unmarshal(dry.Body.Bytes(), &d)
	if d.Changed != 0 {
		t.Errorf("a locked session's preview of read-all says %d notifications of the locked project would change: %s", d.Changed, dry.Body)
	}
	real := s.do(w.editor, "POST", "/api/v1/notifications/read-all", "")
	var r struct{ Changed int }
	_ = json.Unmarshal(real.Body.Bytes(), &r)
	if r.Changed != 0 {
		t.Errorf("the real call changed %d", r.Changed)
	}
}

// REVIEW p7-pass: the per-address bucket is shared by everyone behind one address (a household, an
// office, or every user when SCRIOBH_TRUST_PROXY is off behind a proxy). A read-only member's four wrong tries make
// the owner's own correct passphrase a 429 from another session of theirs. (TestGateLockout pins this as intended.)
func TestP7ReviewReaderCannotLockTheOwnerOut(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	reader := s.newCaller(t, "reader", ops.RoleWriter)
	s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	const addr = "198.51.100.7:4000"
	for i := 0; i < 4; i++ {
		s.doFrom(reader, addr, "POST", w.base()+"/unlock", `{"passphrase":"nope nope nope"}`)
	}
	rec := s.doFrom(w.owner2, addr, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`)
	if rec.Code != http.StatusOK {
		t.Errorf("the owner's correct passphrase from the shared address: %d %s", rec.Code, rec.Body)
	}
}

// REVIEW p7-pass: TestEveryRouteIsClassifiedForTheGate exempts every route under /api/, /admin and
// /c/{slug} by prefix, and the page gate matches only /p/, /ui/projects, /ui/docs and /files. A hand-written route
// such as GET /api/v1/docs/{id}/anything that reads the database itself is therefore neither gated nor flagged.
func TestP7ReviewRouteClassificationDoesNotExemptWholePrefixes(t *testing.T) {
	for _, pat := range []string{"GET /api/v1/docs/{id}/anything", "GET /api/v1/projects/{id}/anything"} {
		if why, ok := notProjectRoute(pat); ok && !projectScoped.MatchString(pat) {
			t.Errorf("%q passes the classification test without being listed (%s) and is not matched by the page gate", pat, why)
		}
	}
}

// REVIEW p7-pass: allow_keys is project-wide. An editor who was never given the passphrase, and whose own
// API key was made before the owner protected the project, reads the manuscript with that key as soon as the owner
// lets keys in for the owner's own agent. CheckMint only stops keys made after the lock.
func TestP7ReviewEditorKeyMadeEarlierDoesNotBypassTheirMissingPassphrase(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if rec := s.do(w.owner, "DELETE", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	editorKey, _ := s.mintKey(t, w.editor, `{"name":"editor agent","scope":"read"}`)
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":`+jsonString(w.pass)+`,"allow_keys":true}`); rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body)
	}
	if !w.isLocked(w.editor) {
		t.Fatal("the editor's session must be locked")
	}
	rec := s.doKey(editorKey, "GET", "/api/v1/docs/"+w.docID, "")
	if rec.Code == 200 && strings.Contains(rec.Body.String(), "Zqtitle") {
		t.Errorf("an editor without the passphrase read the chapter with their own key: %d", rec.Code)
	}
}

// REVIEW p7-pass: the record of a merge or split holds the text that was merged away. The MCP catalogue excludes
// compendium.change.get for that reason ("the owner reads it in the browser"), but the op declares no session-only auth
// and no scope above read, so a read-scope API key pinned to the Compendium (or any unpinned key) fetches it over REST.
func TestP7ReviewChangeRecordIsNotForKeys(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	s.changeFixture(t, w.owner, w.compID)
	ch := sweepChildren["change_id"]
	key, _ := s.mintKey(t, w.owner, `{"name":"comp","scope":"read","compendium_id":"`+w.compID+`"}`)
	rec := s.doKey(key, "GET", "/api/v1/compendiums/"+w.compID+"/changes/"+ch, "")
	if rec.Code == 200 && strings.Contains(rec.Body.String(), "before_state") {
		t.Errorf("a read-scope key pinned to the Compendium read the before-state of a merge or split: %d", rec.Code)
	}
}

// doH is do with extra request headers.
func (s *Server) doH(c caller, method, path, body string, h map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range h {
		req.Header.Set(k, v)
	}
	req.AddCookie(c.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

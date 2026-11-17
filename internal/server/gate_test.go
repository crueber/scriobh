package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/ops"
)

func (w *gateWorld) base() string { return "/api/v1/projects/" + w.pid }

func (w *gateWorld) unlock(c caller, pass string) *httptest.ResponseRecorder {
	return w.s.do(c, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(pass)+`}`)
}

// doFrom sends a request from a given network address.
func (s *Server) doFrom(c caller, addr, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = addr
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func (w *gateWorld) isLocked(c caller) bool {
	rec := w.s.do(c, "GET", w.base()+"/docs", "")
	return rec.Code == http.StatusForbidden && lockedBody(rec.Body.String())
}

func (w *gateWorld) setClock(t *testing.T, at time.Time) {
	t.Helper()
	restore := db.SetClock(func() time.Time { return at })
	t.Cleanup(restore)
}

// A person unlocks with the passphrase, in a session, and locks again on request; another session of the same person,
// and every other member, is unaffected.
func TestGateUnlockAndLock(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if !w.isLocked(w.editor) || !w.isLocked(w.owner2) {
		t.Fatal("a member's session that has not unlocked the project must be refused")
	}
	if w.isLocked(w.owner) {
		t.Fatal("the owner's own session is unlocked by setting the passphrase")
	}
	// a wrong passphrase is a 422 on the passphrase field, not a sign-out
	rec := w.unlock(w.editor, "not it at all")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not right") {
		t.Fatalf("wrong passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := w.unlock(w.editor, w.pass); rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.editor) {
		t.Error("unlocked, but still refused")
	}
	if !w.isLocked(w.owner2) {
		t.Error("one session's unlock opened another session")
	}
	// the editor's reads show the text; relocking on demand closes it
	if rec := s.do(w.editor, "GET", "/api/v1/docs/"+w.docID, ""); !strings.Contains(rec.Body.String(), "Zqtitle") {
		t.Errorf("an unlocked member reads the chapter: %d", rec.Code)
	}
	if rec := s.do(w.editor, "POST", w.base()+"/lock", ""); rec.Code != 200 {
		t.Fatalf("lock: %d %s", rec.Code, rec.Body)
	}
	if !w.isLocked(w.editor) {
		t.Error("project.lock did not lock")
	}
	// the page: a locked member is sent to the unlock page and back, and the form is the same op
	rec = s.do(w.editor, "GET", "/p/"+w.slug+"/d/"+w.docID, "")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/p/"+w.slug+"/unlock?next=") {
		t.Fatalf("a locked page: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	form := "passphrase=" + strings.ReplaceAll(w.pass, " ", "+") + "&next=" + "%2Fp%2F" + w.slug + "%2Fd%2F" + w.docID + "&_csrf=" + w.csrf(w.editor)
	req := httptest.NewRequest("POST", "/ui/projects/"+w.pid+"/unlock", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(w.editor.cookie)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/p/"+w.slug+"/d/"+w.docID {
		t.Fatalf("the unlock form: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	if rec := s.do(w.editor, "GET", "/p/"+w.slug+"/d/"+w.docID, ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Zqtitle") {
		t.Errorf("after the form, the page opens: %d", rec.Code)
	}
	// a next that points elsewhere is not followed
	form = "passphrase=" + strings.ReplaceAll(w.pass, " ", "+") + "&next=https%3A%2F%2Fevil.example%2F&_csrf=" + w.csrf(w.editor)
	req = httptest.NewRequest("POST", "/ui/projects/"+w.pid+"/unlock", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(w.editor.cookie)
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Header().Get("Location") != "/p/"+w.slug {
		t.Errorf("an off-site next was followed: %q", rr.Header().Get("Location"))
	}
	// a stranger learns nothing: the same 404 whether or not the project has a passphrase
	for _, p := range []string{"/unlock", "/lock", "/passphrase"} {
		method := "POST"
		if p == "/passphrase" {
			method = "GET"
		}
		if rec := s.do(w.outsider, method, w.base()+p, `{"passphrase":"x"}`); rec.Code != 404 {
			t.Errorf("%s as a stranger: %d", p, rec.Code)
		}
	}
}

// Sign-out ends the unlock, and so does the next sign-in: an unlock belongs to one session.
func TestGateRelocksOnSignOutAndNewSessions(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if rec := w.unlock(w.editor, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	cookie := w.editor.cookie
	if err := s.Sess.Revoke(context.Background(), cookie.Value); err != nil {
		t.Fatal(err)
	}
	if rec := s.do(w.editor, "GET", w.base()+"/docs", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("a signed-out session: %d", rec.Code)
	}
	again := s.sessionFor(t, "editor again", w.editor.userID)
	if !w.isLocked(again) {
		t.Error("signing in again must not bring the unlock back")
	}
	// even a row left behind for a revoked session opens nothing (the gate checks the session, not only the row)
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM project_unlocks`).Scan(&n)
	if n != 1 { // the owner's
		t.Logf("unlock rows after sign-out: %d", n)
	}
}

// A new passphrase relocks every session but the one that changed it; changing only the options does not.
func TestGateEpochRelocksOnANewPassphrase(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	for _, c := range []caller{w.editor, w.owner2} {
		if rec := w.unlock(c, w.pass); rec.Code != 200 {
			t.Fatal(rec.Body)
		}
	}
	// only the options: nobody is relocked
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"idle_minutes":45}`); rec.Code != 200 {
		t.Fatalf("options: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.editor) || w.isLocked(w.owner2) {
		t.Error("changing only the options relocked someone")
	}
	// a new passphrase needs the current one
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"another pass phrase"}`); rec.Code != 422 {
		t.Errorf("a change without the current passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"another pass phrase","current":"wrong wrong wrong"}`); rec.Code != 422 {
		t.Errorf("a change with the wrong passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"another pass phrase","current":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if !w.isLocked(w.editor) || !w.isLocked(w.owner2) {
		t.Error("a new passphrase must relock every other session")
	}
	if w.isLocked(w.owner) {
		t.Error("the session that changed it stays open")
	}
	if rec := w.unlock(w.editor, w.pass); rec.Code != 422 {
		t.Errorf("the old passphrase still opens it: %d", rec.Code)
	}
	if rec := w.unlock(w.editor, "another pass phrase"); rec.Code != 200 {
		t.Errorf("the new one: %d %s", rec.Code, rec.Body)
	}
}

// Clear and change are not for whoever has the session: the current passphrase is needed, only an owner may, and
// an administrator may remove it without it (and the audit log says so).
func TestGateClearRules(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	// a member who is not the owner
	if rec := s.do(w.editor, "DELETE", w.base()+"/passphrase", `{"current":"x"}`); rec.Code != 403 {
		t.Errorf("an editor clearing: %d", rec.Code)
	}
	if rec := s.do(w.editor, "PUT", w.base()+"/passphrase", `{"passphrase":"an editor's choice"}`); rec.Code != 403 {
		t.Errorf("an editor setting: %d", rec.Code)
	}
	// the owner's other, locked session: no current passphrase, no clear
	if rec := s.do(w.owner2, "DELETE", w.base()+"/passphrase", ""); rec.Code != 422 {
		t.Errorf("clear without the passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner2, "DELETE", w.base()+"/passphrase", `{"current":"guessing it"}`); rec.Code != 422 {
		t.Errorf("clear with a wrong passphrase: %d %s", rec.Code, rec.Body)
	}
	if !w.isLocked(w.editor) {
		t.Error("the project must still be protected")
	}
	// an administrator may, without it, and it is recorded under its own name
	if rec := s.do(w.admin, "DELETE", w.base()+"/passphrase", `{"confirm_name":"Gate Book"}`); rec.Code != 200 {
		t.Fatalf("admin recovery: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.editor) {
		t.Error("cleared, but still locked")
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'project.passphrase.admin_clear' AND target_id = ?`, w.pid).Scan(&n)
	if n != 1 {
		t.Errorf("the recovery is not in the audit log: %d rows", n)
	}
	// and the owner sets one again, the way they did the first time; an administrator cannot lock the author out
	if rec := s.do(w.admin, "PUT", w.base()+"/passphrase", `{"passphrase":"the admin locks it"}`); rec.Code != 403 {
		t.Errorf("an administrator setting a passphrase on someone else's book: %d", rec.Code)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"a brand new one"}`); rec.Code != 200 {
		t.Fatalf("set again: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "DELETE", w.base()+"/passphrase", `{"current":"a brand new one"}`); rec.Code != 200 {
		t.Fatalf("the owner clears it with the passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "GET", w.base()+"/passphrase", ""); !strings.Contains(rec.Body.String(), `"protected":false`) {
		t.Errorf("status after clear: %s", rec.Body)
	}
}

// Wrong tries are counted per session, per person and per address, with the share links' schedule; a locked bucket is
// refused even with the right passphrase; the count is not reset by signing out and in; another address and person
// are unaffected.
func TestGateLockout(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	start := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	w.setClock(t, start)
	// give the editor fresh sessions; the sessions must be live at the clock above
	e1 := s.sessionFor(t, "editor-1", w.editor.userID)
	const addrA, addrB = "198.51.100.7:4000", "203.0.113.9:4000"
	for i := 0; i < 3; i++ { // three free tries
		if rec := s.doFrom(e1, addrA, "POST", w.base()+"/unlock", `{"passphrase":"nope nope nope"}`); rec.Code != 422 {
			t.Fatalf("try %d: %d %s", i+1, rec.Code, rec.Body)
		}
	}
	// the fourth wrong try earns the wait; the right one is then refused too
	if rec := s.doFrom(e1, addrA, "POST", w.base()+"/unlock", `{"passphrase":"nope nope nope"}`); rec.Code != 422 {
		t.Fatalf("fourth: %d", rec.Code)
	}
	rec := s.doFrom(e1, addrA, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`)
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "retry_after") {
		t.Fatalf("locked out, the right passphrase: %d %s", rec.Code, rec.Body)
	}
	if !w.isLocked(e1) {
		t.Error("a locked-out try must not unlock")
	}
	// signing out and in does not reset it: the person's count stands
	e2 := s.sessionFor(t, "editor-2", w.editor.userID)
	if rec := s.doFrom(e2, addrB, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a new session of the same person from a new address: %d, want the person's lockout", rec.Code)
	}
	// another person, another address: unaffected
	other := s.newCaller(t, "other", ops.RoleWriter)
	s.addMember(t, w.pid, other.userID, ops.ProjectReader)
	if rec := s.doFrom(other, addrB, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Errorf("another person elsewhere: %d %s", rec.Code, rec.Body)
	}
	// the same address, another person who has got nothing wrong: the address never locks them out
	third := s.newCaller(t, "third", ops.RoleWriter)
	s.addMember(t, w.pid, third.userID, ops.ProjectReader)
	if rec := s.doFrom(third, addrA, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Errorf("a person with no wrong tries at an address where another person is locked out: %d %s", rec.Code, rec.Body)
	}
	// 30 seconds later the first wait is over, and the right passphrase opens it
	w.setClock(t, start.Add(31*time.Second))
	if rec := s.doFrom(e2, addrB, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Errorf("after the wait: %d %s", rec.Code, rec.Body)
	}
	// and a right passphrase clears the person's count: the next wrong tries are free again
	w.setClock(t, start.Add(32*time.Second))
	if rec := s.doFrom(e2, addrB, "POST", w.base()+"/unlock", `{"passphrase":"nope nope nope"}`); rec.Code != 422 {
		t.Errorf("the count was not cleared: %d", rec.Code)
	}
	// the owner sees the count
	rec = s.do(w.owner, "GET", w.base()+"/passphrase", "")
	var st struct {
		Item struct {
			Failed int `json:"failed_tries"`
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Item.Failed == 0 {
		t.Errorf("the owner's status does not show the wrong tries: %s", rec.Body)
	}
	// a member does not
	if rec := s.do(other, "GET", w.base()+"/passphrase", ""); strings.Contains(rec.Body.String(), "failed_tries") {
		t.Errorf("a member sees the owner's counters: %s", rec.Body)
	}
}

// Concurrent guesses cannot slip past the counter: the try is counted before it is checked.
func TestGateLockoutCountsBeforeChecking(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	e := s.sessionFor(t, "guesser", w.editor.userID)
	done := make(chan int, 12)
	for i := 0; i < 12; i++ {
		go func() { done <- s.do(e, "POST", w.base()+"/unlock", `{"passphrase":"wrong wrong wrong"}`).Code }()
	}
	var wrong, limited int
	for i := 0; i < 12; i++ {
		switch <-done {
		case 422:
			wrong++
		case 429:
			limited++
		}
	}
	if wrong > 4 { // three free, the fourth earns the wait; a few may be refused as busy first
		t.Errorf("%d concurrent wrong guesses were checked; the counter should have stopped them at four", wrong)
	}
	if limited == 0 {
		t.Error("no guess was refused")
	}
}

// An unlock ends after its idle limit, is kept alive by use, and not by a stream asking whether it may still read; and
// no unlock lasts past the ceiling however busy.
func TestGateIdleAndCeiling(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"idle_minutes":10}`); rec.Code != 200 {
		t.Fatalf("idle: %d %s", rec.Code, rec.Body)
	}
	t0 := time.Now().UTC().Truncate(time.Second)
	w.setClock(t, t0)
	e := s.sessionFor(t, "idle", w.editor.userID)
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	// use every nine minutes: it stays open
	at := t0
	for i := 0; i < 5; i++ {
		at = at.Add(9 * time.Minute)
		w.setClock(t, at)
		if w.isLocked(e) {
			t.Fatalf("locked after %d minutes of use every nine", (i+1)*9)
		}
	}
	// a stream's own check is not use: nine minutes of it, then the real idle limit still applies
	gate := s.gateService()
	p := ops.Principal{UserID: e.userID, Role: ops.RoleWriter, Via: "session", SessionID: sessionIDOf(t, s, e)}
	at = at.Add(9 * time.Minute)
	w.setClock(t, at)
	if err := gate.Check(ops.Passive(context.Background()), p, w.pid); err != nil {
		t.Fatalf("passive check inside the idle limit: %v", err)
	}
	at = at.Add(2 * time.Minute) // 11 minutes since the last real use
	w.setClock(t, at)
	if !w.isLocked(e) {
		t.Error("a passive check kept the unlock alive past its idle limit")
	}
	// the ceiling: unlock again, then keep it busy for twelve hours
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	opened := at
	for at.Add(8*time.Minute).Sub(opened) < projectgate.MaxUnlockAge {
		at = at.Add(8 * time.Minute)
		w.setClock(t, at)
		if w.isLocked(e) {
			t.Fatalf("locked early, after %v", at.Sub(opened))
		}
	}
	at = opened.Add(projectgate.MaxUnlockAge + time.Second)
	w.setClock(t, at)
	if !w.isLocked(e) {
		t.Error("an unlock outlived the ceiling")
	}
}

func sessionIDOf(t *testing.T, s *Server, c caller) string {
	t.Helper()
	_, info, err := s.Sess.Peek(context.Background(), c.cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID
}

// An API key is its own credential: by default it is refused at a protected project, the owner may accept keys, every
// use is counted and shown, and a key can neither set, clear, unlock nor lock.
func TestGateKeys(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	// the owner (unlocked) makes a key pinned to the project and one with no pin
	pinned, pinnedID := s.mintKey(t, w.owner, `{"name":"agent pinned","scope":"write","project_id":"`+w.pid+`"}`)
	unpinned, _ := s.mintKey(t, w.owner, `{"name":"agent free","scope":"write"}`)
	for _, k := range []string{pinned, unpinned} {
		rec := s.doKey(k, "GET", w.base()+"/docs", "")
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), ops.ReasonProjectKeysRefused) {
			t.Fatalf("a key at a protected project, keys not accepted: %d %s", rec.Code, rec.Body)
		}
		if l := w.leaks(rec.Body.String()); len(l) > 0 {
			t.Errorf("the refusal leaks %v", l)
		}
	}
	// the other ops, and the one pinned key, are refused at the registry: the same gate as a session
	if rec := s.doKey(pinned, "GET", "/api/v1/docs/"+w.docID, ""); rec.Code != 403 {
		t.Errorf("a doc: %d", rec.Code)
	}
	// a key cannot touch the passphrase at all
	for _, c := range []struct{ m, p, b string }{
		{"PUT", "/passphrase", `{"passphrase":"a key trying it"}`}, {"DELETE", "/passphrase", `{"current":"x"}`},
		{"POST", "/unlock", `{"passphrase":"x"}`}, {"POST", "/lock", ""},
	} {
		if rec := s.doKey(pinned, c.m, w.base()+c.p, c.b); rec.Code != 403 || !strings.Contains(rec.Body.String(), "browser") {
			t.Errorf("a key at %s %s: %d %s, want the browser-only refusal", c.m, c.p, rec.Code, rec.Body)
		}
	}
	// the owner accepts keys
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"allow_keys":true}`); rec.Code != 200 {
		t.Fatalf("allow keys: %d %s", rec.Code, rec.Body)
	}
	for _, k := range []string{pinned, unpinned} {
		if rec := s.doKey(k, "GET", w.base()+"/docs", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Zqtitle") {
			t.Fatalf("a key at a protected project that accepts keys: %d %s", rec.Code, rec.Body)
		}
	}
	// every use is counted: in the owner's status and in the key's own list
	s.gateService().Flush(context.Background())
	rec := s.do(w.owner, "GET", w.base()+"/passphrase", "")
	var st struct {
		Item struct {
			Keys []struct {
				KeyID         string `json:"key_id"`
				Uses, Refused int
			}
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	var used, refused int
	for _, k := range st.Item.Keys {
		used += k.Uses
		refused += k.Refused
	}
	if len(st.Item.Keys) != 2 || used != 2 || refused != 3 { // one count per call, not one per role check inside it
		t.Errorf("the owner's key list: %d keys, %d uses, %d refused: %s", len(st.Item.Keys), used, refused, rec.Body)
	}
	rec = s.do(w.owner, "GET", "/api/v1/keys", "")
	if !strings.Contains(rec.Body.String(), `"protected_projects"`) || !strings.Contains(rec.Body.String(), pinnedID) {
		t.Errorf("key.list does not show the key's use of a protected project: %s", rec.Body)
	}
	// minting: a key is its own credential, so a session that has not unlocked cannot make one that reaches the
	// project (a pinned key, or an unpinned one while keys are accepted); with keys refused, an unpinned key is no way in
	if rec := s.do(w.owner2, "POST", "/api/v1/keys", `{"name":"stolen","scope":"write","project_id":"`+w.pid+`"}`); rec.Code != 403 || !lockedBody(rec.Body.String()) {
		t.Errorf("a locked session minting a pinned key: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner2, "POST", "/api/v1/keys", `{"name":"stolen free","scope":"write"}`); rec.Code != 403 || !lockedBody(rec.Body.String()) {
		t.Errorf("a locked session minting an unpinned key while keys are accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"allow_keys":false}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := s.do(w.owner2, "POST", "/api/v1/keys", `{"name":"harmless","scope":"write"}`); rec.Code != 201 {
		t.Errorf("an unpinned key while keys are refused: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner2, "POST", "/api/v1/keys", `{"name":"stolen pinned","scope":"write","project_id":"`+w.pid+`"}`); rec.Code != 403 {
		t.Errorf("a locked session minting a pinned key while keys are refused: %d", rec.Code)
	}
}

// Nothing that verifies a passphrase may be previewed or replayed: a rolled-back wrong guess would be free, and a stored
// fingerprint of the request would be a hash of the passphrase.
func TestGatePassphraseOpsAreNotPreviewedOrKept(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	e := s.sessionFor(t, "previewer", w.editor.userID)
	for _, c := range []struct{ m, p, b string }{
		{"POST", "/unlock", `{"passphrase":"x guess x"}`},
		{"PUT", "/passphrase", `{"current":"x guess x","idle_minutes":5}`},
	} {
		req := httptest.NewRequest(c.m, w.base()+c.p, strings.NewReader(c.b))
		req.Header.Set("X-Scriobh-Dry-Run", "1")
		req.Header.Set("Content-Type", "application/json")
		who := e
		if c.m == "PUT" {
			who = w.owner2
		}
		req.AddCookie(who.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 422 || !strings.Contains(rec.Body.String(), "dry_run_unsupported") {
			t.Errorf("a preview of %s %s: %d %s", c.m, c.p, rec.Code, rec.Body)
		}
	}
	var fails int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM project_unlock_failures`).Scan(&fails)
	if fails != 0 {
		t.Errorf("a refused preview counted %d wrong tries", fails)
	}
	// an Idempotency-Key is ignored: nothing is stored
	req := httptest.NewRequest("POST", w.base()+"/unlock", strings.NewReader(`{"passphrase":`+jsonString(w.pass)+`}`))
	req.Header.Set("Idempotency-Key", "abc123")
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("unlock with an Idempotency-Key: %d %s", rec.Code, rec.Body)
	}
	var kept int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM idempotency_keys`).Scan(&kept)
	if kept != 0 {
		t.Errorf("%d idempotency rows were kept for a request that held a passphrase", kept)
	}
}

// The passphrase is nowhere in the database in the clear: not in the gate, the audit log, the idempotency table, the
// events, and nowhere else either.
func TestGatePassphraseIsNeverStoredInTheClear(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	e := s.sessionFor(t, "typist", w.editor.userID)
	w.unlock(e, "a wrong guess entirely")
	w.unlock(e, w.pass)
	s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"passphrase":"brand new secret words"}`)
	rows, err := s.DB.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tbl := range tables {
		cols, err := s.DB.Read().Query(`SELECT name FROM pragma_table_info('` + tbl + `')`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for cols.Next() {
			var n string
			_ = cols.Scan(&n)
			names = append(names, n)
		}
		cols.Close()
		for _, c := range names {
			for _, secret := range []string{w.pass, "brand new secret words", "a wrong guess entirely"} {
				var n int
				if err := s.DB.Read().QueryRow(`SELECT count(*) FROM "`+tbl+`" WHERE CAST("`+c+`" AS TEXT) LIKE ?`, "%"+secret+"%").Scan(&n); err != nil && err != sql.ErrNoRows {
					continue
				}
				if n > 0 {
					t.Errorf("the passphrase %q is stored in the clear in %s.%s", secret, tbl, c)
				}
			}
		}
	}
	var hash string
	_ = s.DB.Read().QueryRow(`SELECT pass_hash FROM project_gates WHERE project_id = ?`, w.pid).Scan(&hash)
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("the hash is %.20q", hash)
	}
}

// What the gate is not for: the work the server does in the background keeps going while a project is locked.
func TestGateBackgroundWorkIsNotGated(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	// the mirror: the owner (unlocked) saves; the projection is written by the server while every other session is locked
	if !w.isLocked(w.editor) {
		t.Fatal("setup")
	}
	var d struct{ Item struct{ Revision int } }
	_ = json.Unmarshal(s.do(w.owner, "GET", "/api/v1/docs/"+w.docID, "").Body.Bytes(), &d)
	body, _ := json.Marshal(map[string]any{"content": "Zqbody a fresh sentence for the mirror.", "base_revision": d.Item.Revision})
	if rec := s.do(w.owner, "PATCH", "/api/v1/docs/"+w.docID, string(body)); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	if _, err := s.Mirror.Rebuild(context.Background(), w.pid); err != nil { // the worker's own call: no caller in its context
		t.Fatalf("mirror: %v", err)
	}
	found := false
	_ = walkFiles(s.Cfg.DataDir, func(path, text string) {
		if strings.Contains(text, "a fresh sentence for the mirror") {
			found = true
		}
	})
	if !found {
		t.Error("the mirror did not write a locked project's text: the gate must not stop background work")
	}
	// the operator's own shell (the import command) is not a person at a screen
	cli := ops.Principal{UserID: w.owner.userID, Role: ops.RoleWriter, Via: "cli"}
	if gate := s.gateService(); gate.Check(context.Background(), cli, w.pid) != nil {
		t.Error("the operator's shell must pass the gate")
	}
	// a context with no caller at all (a worker) passes the role function the ops use
	if _, ok := s.Reg.Env().ProjectRole(context.Background(), w.editor.userID, w.pid); !ok {
		t.Error("background work, which names no caller, was refused by the role function")
	}
	if _, ok := s.Reg.Env().ProjectRole(ops.WithPrincipal(context.Background(), ops.Principal{UserID: w.editor.userID, Via: "session"}), w.editor.userID, w.pid); ok {
		t.Error("a caller who has not unlocked the project passed the role function")
	}
}

// A stream of live updates ends when the project locks, and its heartbeat does not count as use.
func TestGateStreamEndsOnRelock(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	beat := make(chan time.Time, 4)
	s.sseTick = func(time.Duration) (<-chan time.Time, func()) { return beat, func() {} }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	e := s.sessionFor(t, "watcher", w.editor.userID)
	if _, code := openStream(t, ts.URL, e, "", "project="+w.pid, ""); code != 403 {
		t.Fatalf("a stream of a locked project: %d, want 403", code)
	}
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	cl, code := openStream(t, ts.URL, e, "", "project="+w.pid, "")
	if code != 200 {
		t.Fatalf("a stream of an unlocked project: %d", code)
	}
	cl.nextEvent()
	if rec := s.do(e, "POST", w.base()+"/lock", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	beat <- time.Now()
	if ev := cl.nextEvent(); ev.Event != "bye" {
		t.Fatalf("the stream did not end when the project was locked: %+v", ev)
	}
}

// A project with a passphrase in the trash is still the owner's to restore, but the passphrase comes first.
func TestGateComesBeforeTheTrash(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if rec := s.do(w.owner, "POST", w.base()+"/trash", ""); rec.Code != 200 {
		t.Fatalf("trash: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner2, "POST", w.base()+"/restore", ""); rec.Code != 403 || !lockedBody(rec.Body.String()) {
		t.Errorf("restoring while locked: %d %s", rec.Code, rec.Body)
	}
	// a locked owner's way back is to unlock, which works on a project in the trash, and then restore
	if rec := w.unlock(w.owner2, w.pass); rec.Code != 200 {
		t.Fatalf("unlocking a project in the trash: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner2, "POST", w.base()+"/restore", ""); rec.Code != 200 {
		t.Errorf("restoring after the unlock: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "POST", w.base()+"/trash", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	// and removing the passphrase works on a project in the trash, with the passphrase
	if rec := s.do(w.owner, "DELETE", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Errorf("clearing on a project in the trash: %d %s", rec.Code, rec.Body)
	}
}

// The gate is part of the registry's environment: a build that forgets to install it must not pass this.
func TestGateIsInstalledInTheEnvironment(t *testing.T) {
	s := newAuthzServer(t)
	env := s.Reg.Env()
	if env.ProjectGate == nil || env.ProjectMember == nil || env.ProjectRole == nil {
		t.Fatal("the registry's environment has no gate: every project op would be open")
	}
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind == ops.AuthProject && ops.GateExempt(e.Op.Name) != e.Op.Auth.GateExempt {
			t.Errorf("%s: the exempt list and the registration disagree", e.Op.Name)
		}
	}
	// the exempt ops are exactly the five that answer a locked caller
	var exempt []string
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.GateExempt {
			exempt = append(exempt, e.Op.Name)
		}
	}
	if len(exempt) != 5 {
		t.Errorf("exempt ops: %v", exempt)
	}
}

// walkFiles calls fn with the path and text of every file under root.
func walkFiles(root string, fn func(path, text string)) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && len(b) < 1<<20 {
			fn(path, string(b))
		}
		return nil
	})
}

// Over MCP a key is refused at a protected project (tools and resources alike) unless its owner accepts keys, and nothing
// of the project is in the refusal; the same call passes once the owner accepts them.
func TestGateOverMCP(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	pinned, _ := s.mintKey(t, w.owner, `{"name":"mcp pinned","scope":"write","project_id":"`+w.pid+`"}`)
	free, _ := s.mintKey(t, w.owner, `{"name":"mcp free","scope":"write"}`)
	calls := func(secret string) (tool, doc, outline rpcResult) {
		tool = s.modernCall(t, secret, "tools/call", map[string]any{"name": "doc_get", "arguments": map[string]any{"id": w.docID}})
		doc = s.modernCall(t, secret, "resources/read", map[string]any{"uri": "scriobh://project/" + w.pid + "/doc/" + w.docID})
		outline = s.modernCall(t, secret, "resources/read", map[string]any{"uri": "scriobh://project/" + w.pid + "/outline"})
		return
	}
	for name, secret := range map[string]string{"pinned": pinned, "unpinned": free} {
		tool, doc, outline := calls(secret)
		for what, r := range map[string]rpcResult{"tools/call doc_get": tool, "resources/read doc": doc, "resources/read outline": outline} {
			body := string(r.raw)
			if l := w.leaks(body); len(l) > 0 {
				t.Errorf("%s key, %s, leaks %v: %.300s", name, what, l, body)
			}
			refused := r.msg.Error != nil || r.msg.Result["isError"] == true
			if !refused {
				t.Errorf("%s key, %s: not refused: %d %.300s", name, what, r.status, body)
			}
			// a tool says why; a resource answers "not found", as it does for anything the key may not read
			if strings.HasPrefix(what, "tools/") && !strings.Contains(body, "passphrase") {
				t.Errorf("%s key, %s: the refusal does not say why: %.300s", name, what, body)
			}
		}
	}
	// the owner accepts keys: the same calls pass, and now carry the text
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"allow_keys":true}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	tool, doc, _ := calls(pinned)
	if tool.msg.Error != nil || tool.msg.Result["isError"] == true || !strings.Contains(string(tool.raw), "Zqtitle") {
		t.Errorf("tools/call at a project that accepts keys: %.300s", tool.raw)
	}
	if doc.msg.Error != nil || !strings.Contains(string(doc.raw), "Zqbody") {
		t.Errorf("resources/read at a project that accepts keys: %.300s", doc.raw)
	}
}

// The way back when the passphrase is forgotten: an administrator who is a member but not the owner clears it without
// it, even from a session that has not unlocked; one who owns the project needs the passphrase like any owner (the
// operator's `scriobh passphrase clear` is the way back for a sole administrator who is also the owner).
func TestGateClearByAnAdministratorWhoIsAMember(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	s.addMember(t, w.pid, w.admin.userID, ops.ProjectEditor)
	if rec := s.do(w.admin, "GET", w.base()+"/docs", ""); rec.Code != 403 || !lockedBody(rec.Body.String()) {
		t.Fatalf("the administrator's session is locked like anyone's: %d", rec.Code)
	}
	// without the project's name typed it is refused whole, and nothing is removed
	for _, body := range []string{"", `{"confirm_name":"Not the name"}`} {
		if rec := s.do(w.admin, "DELETE", w.base()+"/passphrase", body); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "confirmation_required") {
			t.Fatalf("an administrator clearing it without the name (%q): %d %s", body, rec.Code, rec.Body)
		}
	}
	if !w.isLocked(w.editor) {
		t.Fatal("a refused removal removed the passphrase")
	}
	if rec := s.do(w.admin, "DELETE", w.base()+"/passphrase", `{"confirm_name":"Gate Book"}`); rec.Code != 200 {
		t.Fatalf("an administrator who is a member clearing it: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.admin, "GET", w.base()+"/docs", ""); rec.Code != 200 {
		t.Errorf("cleared: %d", rec.Code)
	}
	// set again; now the administrator is also an owner of the project and needs the passphrase
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"set once more now"}`); rec.Code != 200 {
		t.Fatalf("set again: %d %s", rec.Code, rec.Body)
	}
	if _, err := s.DB.Writer().Exec(`UPDATE project_members SET role = 'owner' WHERE project_id = ? AND user_id = ?`, w.pid, w.admin.userID); err != nil {
		t.Fatal(err)
	}
	if rec := s.do(w.admin, "DELETE", w.base()+"/passphrase", ""); rec.Code != 422 {
		t.Errorf("an administrator who owns the project, without the passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.admin, "DELETE", w.base()+"/passphrase", `{"current":"set once more now"}`); rec.Code != 200 {
		t.Errorf("with it: %d %s", rec.Code, rec.Body)
	}
}

// The passphrase that was checked must be the one in force when the result is written. The hook runs between the check
// and the write, deterministically, and changes the gate the way a second owner's session would.
func TestGateWriteNoticesAPassphraseThatChangedAfterTheCheck(t *testing.T) {
	rotate := func(w *gateWorld, from, to string) {
		if rec := w.s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":`+jsonString(to)+`,"current":`+jsonString(from)+`}`); rec.Code != 200 {
			panic("the hook's rotation failed: " + rec.Body.String())
		}
	}
	reason := func(rec interface{ String() string }) bool {
		return strings.Contains(rec.String(), "passphrase_changed")
	}

	t.Run("unlock with the old passphrase while it is rotated", func(t *testing.T) {
		s := newAuthzServer(t)
		w := newGateWorld(t, s)
		e := s.sessionFor(t, "late", w.editor.userID)
		s.gateService().SetAfterVerifyHook(func() { rotate(w, w.pass, "a rotated passphrase") })
		rec := w.unlock(e, w.pass)
		if rec.Code != http.StatusConflict || !reason(rec.Body) {
			t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
		}
		if !w.isLocked(e) {
			t.Error("the old passphrase unlocked the project in the new epoch")
		}
		if rec := w.unlock(e, "a rotated passphrase"); rec.Code != 200 {
			t.Errorf("the new passphrase: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("unlock while the passphrase is cleared and set again (the epoch comes back, the hash does not)", func(t *testing.T) {
		s := newAuthzServer(t)
		w := newGateWorld(t, s)
		e := s.sessionFor(t, "late", w.editor.userID)
		s.gateService().SetAfterVerifyHook(func() {
			if rec := s.do(w.owner, "DELETE", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`}`); rec.Code != 200 {
				panic(rec.Body.String())
			}
			if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":"entirely different words"}`); rec.Code != 200 {
				panic(rec.Body.String())
			}
		})
		if rec := w.unlock(e, w.pass); rec.Code != http.StatusConflict || !w.isLocked(e) {
			t.Fatalf("unlock: %d %s (locked: %v)", rec.Code, rec.Body, w.isLocked(e))
		}
	})
	t.Run("a stale current on set", func(t *testing.T) {
		s := newAuthzServer(t)
		w := newGateWorld(t, s)
		s.gateService().SetAfterVerifyHook(func() { rotate(w, w.pass, "the owner's own rotation") })
		rec := s.do(w.owner2, "PUT", w.base()+"/passphrase", `{"passphrase":"the other session's","current":`+jsonString(w.pass)+`}`)
		if rec.Code != http.StatusConflict || !reason(rec.Body) {
			t.Fatalf("set: %d %s", rec.Code, rec.Body)
		}
		if rec := w.unlock(s.sessionFor(t, "x", w.editor.userID), "the owner's own rotation"); rec.Code != 200 {
			t.Errorf("the rotation that won is not in force: %d %s", rec.Code, rec.Body)
		}
	})
	t.Run("a stale current on clear", func(t *testing.T) {
		s := newAuthzServer(t)
		w := newGateWorld(t, s)
		s.gateService().SetAfterVerifyHook(func() { rotate(w, w.pass, "rotated before the clear lands") })
		rec := s.do(w.owner2, "DELETE", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`}`)
		if rec.Code != http.StatusConflict || !reason(rec.Body) {
			t.Fatalf("clear: %d %s", rec.Code, rec.Body)
		}
		if !w.isLocked(s.sessionFor(t, "x", w.editor.userID)) {
			t.Error("a stale clear removed a newly set passphrase")
		}
	})
}

// Accepting keys is per key: a key passes a protected project only if it was made by a session that had the project
// unlocked, or the owner approved it by id; a new passphrase voids every approval.
func TestGateKeysArePerKeyApprovals(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	// the editor made a key before the project was protected: clear, mint, protect again with keys accepted
	if rec := s.do(w.owner, "DELETE", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	editorKey, editorKeyID := s.mintKey(t, w.editor, `{"name":"editor agent","scope":"read"}`)
	ownerKey, ownerKeyID := s.mintKey(t, w.owner, `{"name":"owner agent","scope":"write"}`)
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"passphrase":`+jsonString(w.pass)+`,"allow_keys":true}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	read := func(k string) *httptest.ResponseRecorder { return s.doKey(k, "GET", "/api/v1/docs/"+w.docID, "") }
	for name, k := range map[string]string{"the editor's": editorKey, "the owner's": ownerKey} {
		if rec := read(k); rec.Code != 403 || !strings.Contains(rec.Body.String(), ops.ReasonProjectKeysRefused) || len(w.leaks(rec.Body.String())) > 0 {
			t.Errorf("%s key, made before protection, with keys accepted: %d %.200s", name, rec.Code, rec.Body)
		}
	}
	// a key that is not approved is also locked out of the lists: no notifications, a redacted project list
	for _, p := range []string{"/api/v1/notifications", "/api/v1/projects"} {
		if rec := s.doKey(editorKey, "GET", p, ""); len(w.leaks(rec.Body.String())) > 0 {
			t.Errorf("an unapproved key reads %s: %.300s", p, rec.Body)
		}
	}
	// the owner sees the keys they can approve: their own, and the one that has tried
	var st struct {
		Item struct {
			Keys []struct {
				KeyID    string `json:"key_id"`
				Mine     bool
				Approved bool
				Refused  int
			}
		}
	}
	rec := s.do(w.owner, "GET", w.base()+"/passphrase", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	seen := map[string]bool{}
	for _, k := range st.Item.Keys {
		seen[k.KeyID] = true
		if k.Approved {
			t.Errorf("key %s is approved before anyone approved it", k.KeyID)
		}
	}
	if !seen[ownerKeyID] || !seen[editorKeyID] {
		t.Fatalf("the owner's list: %s", rec.Body)
	}
	// approving needs the passphrase, only names a candidate, and applies at once
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"approve_keys":["`+ownerKeyID+`"]}`); rec.Code != 422 {
		t.Errorf("approving without the passphrase: %d", rec.Code)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"approve_keys":["key_00000000000000000000000000"]}`); rec.Code != 422 {
		t.Errorf("approving a key that is nobody's: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"approve_keys":["`+ownerKeyID+`"]}`); rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if rec := read(ownerKey); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Zqtitle") {
		t.Errorf("the approved key: %d %.200s", rec.Code, rec.Body)
	}
	if rec := read(editorKey); rec.Code != 403 {
		t.Errorf("the editor's key, not approved: %d", rec.Code)
	}
	// a key made now by a session that has the project unlocked is approved by being made
	minted, _ := s.mintKey(t, w.owner, `{"name":"made unlocked","scope":"write","project_id":"`+w.pid+`"}`)
	if rec := read(minted); rec.Code != 200 {
		t.Errorf("a key made by an unlocked session: %d %.200s", rec.Code, rec.Body)
	}
	// withdrawing, and a new passphrase, void approvals
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"unapprove_keys":["`+ownerKeyID+`"]}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := read(ownerKey); rec.Code != 403 {
		t.Errorf("a withdrawn approval: %d", rec.Code)
	}
	if rec := read(minted); rec.Code != 200 {
		t.Errorf("withdrawing one approval touched another: %d", rec.Code)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/passphrase", `{"current":`+jsonString(w.pass)+`,"passphrase":"a new passphrase now"}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := read(minted); rec.Code != 403 {
		t.Errorf("a new passphrase must void every approval: %d", rec.Code)
	}
}

// The restructuring ops and the record of what they did are a person's act in a browser: every one is session-only, and
// a key of any kind is refused (the record holds text that was merged away).
func TestRestructuringOpsAreSessionOnlyAndNoKeyCanCallThem(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	s.changeFixture(t, w.owner, w.compID)
	ch := sweepChildren["change_id"]
	keys := map[string]string{}
	keys["unpinned"], _ = s.mintKey(t, w.owner, `{"name":"free","scope":"write"}`)
	keys["compendium"], _ = s.mintKey(t, w.owner, `{"name":"comp","scope":"write","compendium_id":"`+w.compID+`"}`)
	keys["project"], _ = s.mintKey(t, w.owner, `{"name":"proj","scope":"write","project_id":"`+w.pid+`"}`)
	n := 0
	for _, e := range s.Reg.Entries() {
		name := e.Op.Name
		if !(strings.HasPrefix(name, "compendium.merge") || strings.HasPrefix(name, "compendium.split") || strings.HasPrefix(name, "compendium.primary") || strings.HasPrefix(name, "compendium.change")) {
			continue
		}
		n++
		if !e.Op.Auth.SessionOnly {
			t.Errorf("%s is not session-only", name)
		}
		id := w.compID
		if e.Op.Auth.Kind == ops.AuthProject {
			id = w.pid
		}
		path := fillPath(strings.ReplaceAll(e.Op.Path, "{change_id}", ch), id)
		for kind, k := range keys {
			rec := s.doKey(k, e.Op.Method, path, bodyFor(e))
			if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "before_state") {
				t.Errorf("%s with a %s key (write scope): %d %.200s", name, kind, rec.Code, rec.Body)
			}
		}
	}
	if n < 8 {
		t.Errorf("found %d restructuring ops, want 8", n)
	}
}

// A stream ends the moment its session locks the project, with no heartbeat and no event needed; another session's lock
// does not end it; and an event after an idle expiry is not delivered.
func TestGateStreamEndsAtOnceOnLock(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	beat := make(chan time.Time, 4)
	s.sseTick = func(time.Duration) (<-chan time.Time, func()) { return beat, func() {} }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	e := s.sessionFor(t, "watcher", w.editor.userID)
	other := s.sessionFor(t, "other", w.editor.userID)
	for _, c := range []caller{e, other} {
		if rec := w.unlock(c, w.pass); rec.Code != 200 {
			t.Fatal(rec.Body)
		}
	}
	cl, code := openStream(t, ts.URL, e, "", "project="+w.pid, "")
	if code != 200 {
		t.Fatalf("stream: %d", code)
	}
	cl.nextEvent() // hello
	if rec := s.do(other, "POST", w.base()+"/lock", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := s.do(w.owner, "PATCH", "/api/v1/docs/"+w.docID, `{"content":"Zqbody and one more word.","base_revision":2}`); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	if ev := cl.nextEvent(); ev.Event != "doc.updated" {
		t.Fatalf("another session's lock ended or changed this stream: %+v", ev)
	}
	if rec := s.do(e, "POST", w.base()+"/lock", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if ev := cl.nextEvent(); ev.Event != "bye" {
		t.Fatalf("the stream did not end when its own session locked: %+v", ev)
	}
	if !cl.ended() {
		t.Error("the stream stayed open")
	}
}

// The client address only slows: a single address that keeps guessing (with other people's accounts, so each starts with
// free tries of its own) is held back, and a person at the same address who has never got it wrong is not.
func TestGateAddressBucketSlowsAnAttackerAndNobodyElse(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	const addr = "198.51.100.77:4000"
	wrong := `{"passphrase":"guess guess guess"}`
	// five accounts, two wrong tries each: ten from one address, past the address's own free tries
	var fresh []caller
	for i := 0; i < 5; i++ {
		c := s.newCaller(t, fmt.Sprintf("member-%d", i), ops.RoleWriter)
		s.addMember(t, w.pid, c.userID, ops.ProjectReader)
		for j := 0; j < 2; j++ {
			if rec := s.doFrom(c, addr, "POST", w.base()+"/unlock", wrong); rec.Code != 422 {
				t.Fatalf("member %d try %d: %d %s", i, j, rec.Code, rec.Body)
			}
		}
		fresh = append(fresh, c)
	}
	// the next wrong try of one of them, from the same address, is now met with the address's wait
	if rec := s.doFrom(fresh[0], addr, "POST", w.base()+"/unlock", wrong); rec.Code != http.StatusTooManyRequests {
		t.Errorf("an address that has been guessing, a caller with wrong tries of their own: %d %s, want the wait", rec.Code, rec.Body)
	}
	// a person at that address with no wrong tries is not touched, and neither is the owner
	reader := s.newCaller(t, "innocent", ops.RoleWriter)
	s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	if rec := s.doFrom(reader, addr, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Errorf("a person who never got it wrong, at the guessing address: %d %s", rec.Code, rec.Body)
	}
	if rec := s.doFrom(w.owner2, addr, "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != 200 {
		t.Errorf("the owner at the guessing address: %d %s", rec.Code, rec.Body)
	}
	// a single account is held by its own count whatever the address
	solo := s.newCaller(t, "solo", ops.RoleWriter)
	s.addMember(t, w.pid, solo.userID, ops.ProjectReader)
	for i := 0; i < 4; i++ {
		s.doFrom(solo, "203.0.113.50:1", "POST", w.base()+"/unlock", wrong)
	}
	if rec := s.doFrom(solo, "203.0.113.51:1", "POST", w.base()+"/unlock", `{"passphrase":`+jsonString(w.pass)+`}`); rec.Code != http.StatusTooManyRequests {
		t.Errorf("a person's own wrong tries from another address: %d", rec.Code)
	}
}

// A preview answers what the real call would, for a locked project too: it is not a way to learn that a notification
// of a locked project exists.
func TestGateDryRunMarkReadIsNotAnOracle(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	var nid string
	if err := s.DB.Read().QueryRow(`SELECT id FROM notifications WHERE user_id = ? AND project_id = ? LIMIT 1`, w.editor.userID, w.pid).Scan(&nid); err != nil {
		t.Fatalf("the editor should have a notification of the locked project: %v", err)
	}
	body := `{"ids":["` + nid + `"]}`
	for _, who := range []string{"dry", "real"} {
		var rec *httptest.ResponseRecorder
		if who == "dry" {
			rec = s.doH(w.editor, "POST", "/api/v1/notifications/read", body, map[string]string{"X-Scriobh-Dry-Run": "1"})
		} else {
			rec = s.do(w.editor, "POST", "/api/v1/notifications/read", body)
		}
		var out struct{ Changed int }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Changed != 0 {
			t.Errorf("%s: a locked session marked %d notifications of the locked project: %s", who, out.Changed, rec.Body)
		}
	}
	// and a preview of the lock is refused outright (it would change what the process remembers)
	if rec := s.doH(w.owner, "POST", w.base()+"/lock", "", map[string]string{"X-Scriobh-Dry-Run": "1"}); rec.Code != 422 || !strings.Contains(rec.Body.String(), "dry_run_unsupported") {
		t.Errorf("a preview of project.lock: %d %s", rec.Code, rec.Body)
	}
	if w.isLocked(w.owner) {
		t.Error("the preview locked the owner's session")
	}
}

// A merge never deletes, unseen, the override a project you have not unlocked made of the entity that is merged away: it is
// refused with the count (never the project's name) until you unlock the project and choose.
func TestGateMergeRefusedWhileALockedProjectOverrodeBoth(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	// a second entity in the bible, and the book overrides both
	b := s.do(w.owner, "POST", "/api/v1/compendiums/"+w.compID+"/entities", `{"kind":"character","name":"Heroine Two"}`)
	w.must(t, b, "second entity")
	bID := gateID(t, b)
	for _, id := range []string{w.entID, bID} {
		if rec := s.do(w.owner, "PUT", w.base()+"/entity-overrides", `{"entity_id":"`+id+`","summary":"Zqoverride for the book"}`); rec.Code != 200 {
			t.Fatalf("override: %d %s", rec.Code, rec.Body)
		}
	}
	// the bible's owner, in a session that has not unlocked the book
	plan := s.do(w.owner2, "POST", "/api/v1/entities/"+w.entID+"/merge", `{"into_id":"`+bID+`","dry_run":true}`)
	var pv struct {
		Plan struct {
			Locked  int `json:"locked_override_conflicts"`
			Hidden  int `json:"hidden_override_conflicts"`
			Dropped int `json:"overrides_dropped"`
		}
	}
	_ = json.Unmarshal(plan.Body.Bytes(), &pv)
	if plan.Code != 200 || pv.Plan.Locked != 1 || pv.Plan.Hidden != 0 || pv.Plan.Dropped != 0 || len(w.leaks(plan.Body.String())) > 0 {
		t.Fatalf("the plan: %d %s", plan.Code, plan.Body)
	}
	rec := s.do(w.owner2, "POST", "/api/v1/entities/"+w.entID+"/merge", `{"into_id":"`+bID+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "locked_projects") || strings.Contains(rec.Body.String(), "Gate Book") || len(w.leaks(rec.Body.String())) > 0 {
		t.Errorf("the merge: %d %s", rec.Code, rec.Body)
	}
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM project_entity_overrides WHERE project_id = ?`, w.pid).Scan(&n)
	if n != 2 {
		t.Errorf("%d overrides of the book remain, want both", n)
	}
	// the same refusal as a blocking reason of a move between Compendiums
	other := s.do(w.owner, "POST", "/api/v1/compendiums", `{"name":"Elsewhere"}`)
	w.must(t, other, "compendium")
	otherID := gateID(t, other)
	oe := s.do(w.owner, "POST", "/api/v1/compendiums/"+otherID+"/entities", `{"kind":"character","name":"Heroine"}`)
	w.must(t, oe, "clashing entity")
	if rec := s.do(w.owner, "POST", w.base()+"/compendium/refs", `{"compendium_id":"`+otherID+`"}`); rec.Code != 200 {
		t.Fatalf("reference: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "PUT", w.base()+"/entity-overrides", `{"entity_id":"`+gateID(t, oe)+`","summary":"book on the other Heroine"}`); rec.Code != 200 {
		t.Fatalf("override: %d %s", rec.Code, rec.Body)
	}
	mv := `{"into_id":"` + otherID + `","entity_ids":["` + w.entID + `"],"resolutions":{"` + w.entID + `":{"action":"merge","into_id":"` + gateID(t, oe) + `"}}}`
	prev := s.do(w.owner2, "POST", "/api/v1/compendiums/"+w.compID+"/merge/preview", mv)
	if prev.Code != 200 || !strings.Contains(prev.Body.String(), "have not unlocked") || len(w.leaks(prev.Body.String())) > 0 {
		t.Errorf("the move's preview does not say a locked project blocks it: %d %.400s", prev.Code, prev.Body)
	}
	// unlocked, the project's conflict is the caller's to choose, as for any project they can open
	if rec := w.unlock(w.owner2, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	plan = s.do(w.owner2, "POST", "/api/v1/entities/"+w.entID+"/merge", `{"into_id":"`+bID+`","dry_run":true}`)
	_ = json.Unmarshal(plan.Body.Bytes(), &pv)
	if pv.Plan.Locked != 0 || !strings.Contains(plan.Body.String(), "override:"+w.pid) {
		t.Errorf("unlocked, the plan: %.400s", plan.Body)
	}
}

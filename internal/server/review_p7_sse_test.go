package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// REVIEW p7-pass: after a session locks a project (project.lock) its open stream must not deliver the project's
// events. The gate is only re-asked on the heartbeat tick, so every event published between the lock and the tick
// still reaches the locked session's stream.
func TestP7ReviewStreamDeliversNothingAfterLock(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	beat := make(chan time.Time, 4)
	s.sseTick = func(time.Duration) (<-chan time.Time, func()) { return beat, func() {} }
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	e := s.sessionFor(t, "watcher", w.editor.userID)
	if rec := w.unlock(e, w.pass); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	cl, code := openStream(t, ts.URL, e, "", "project="+w.pid, "")
	if code != 200 {
		t.Fatalf("stream: %d", code)
	}
	cl.nextEvent() // hello
	if rec := s.do(e, "POST", w.base()+"/lock", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if !w.isLocked(e) {
		t.Fatal("not locked")
	}
	// someone else edits the chapter; no heartbeat has fired yet
	if rec := s.do(w.owner, "PATCH", "/api/v1/docs/"+w.docID, `{"content":"Zqbody and some new words.","base_revision":2}`); rec.Code != 200 {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body)
	}
	select {
	case f, ok := <-cl.frames:
		if ok && f.Event != "" && f.Event != "bye" {
			t.Errorf("a locked session's stream still delivered %q: %s", f.Event, f.Data)
		}
	case <-time.After(2 * time.Second):
	}
	_ = strings.Contains
}

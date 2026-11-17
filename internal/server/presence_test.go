package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// presFixture is a project with an owner, an editor, a reviewer and a reader who all share presence, a stranger, and a
// controllable presence clock for the streams.
type presFixture struct {
	*sseFixture
	editor, reviewer         caller
	docB                     string
	mu                       sync.Mutex
	ticks                    []chan time.Time
	now                      time.Time
	restore                  func()
	ownerPres, readerPresSes caller
}

const (
	presTitleA = "Chapter one"
	presTitleB = "Zqcanary Beta"
)

func newPresFixture(t *testing.T) *presFixture {
	t.Helper()
	f := &presFixture{sseFixture: newSSEFixture(t, nil)}
	f.now = time.Now().UTC()
	f.restore = db.SetClock(func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now })
	// The clock is a process-wide variable: put it back only after every stream that reads it has ended.
	t.Cleanup(func() {
		f.s.StopStreams()
		f.ts.CloseClientConnections()
		f.ts.Close()
		f.restore()
	})
	f.s.presTick = func(time.Duration) (<-chan time.Time, func()) {
		ch := make(chan time.Time, 4)
		f.mu.Lock()
		f.ticks = append(f.ticks, ch)
		f.mu.Unlock()
		return ch, func() {}
	}
	f.editor = f.s.newCaller(t, "edgar", ops.RoleWriter)
	f.reviewer = f.s.newCaller(t, "rhea", ops.RoleReader)
	f.s.addMember(t, f.pid, f.editor.userID, ops.ProjectEditor)
	f.s.addMember(t, f.pid, f.reviewer.userID, ops.ProjectReviewer)
	rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/docs", `{"title":"`+presTitleB+`"}`)
	f.docB = idOf(t, rec.Body.Bytes())
	for _, c := range []caller{f.owner, f.editor, f.reviewer, f.reader} {
		f.setShare(t, c, "true")
		f.name(t, c)
	}
	return f
}

func (f *presFixture) name(t *testing.T, c caller) {
	t.Helper()
	if rec := f.s.do(c, "PATCH", "/api/v1/me", `{"display_name":"`+strings.ToUpper(c.name[:1])+c.name[1:]+`"}`); rec.Code != 200 {
		t.Fatalf("profile: %d %s", rec.Code, rec.Body)
	}
}

func (f *presFixture) setShare(t *testing.T, c caller, v string) {
	t.Helper()
	if rec := f.s.do(c, "PUT", "/api/v1/me/prefs", `{"items":{"presence.share":"`+v+`"}}`); rec.Code != 200 {
		t.Fatalf("prefs.set: %d %s", rec.Code, rec.Body)
	}
}

func (f *presFixture) advance(d time.Duration) { f.mu.Lock(); f.now = f.now.Add(d); f.mu.Unlock() }

func (f *presFixture) tick() {
	f.mu.Lock()
	chs := append([]chan time.Time(nil), f.ticks...)
	f.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- time.Time{}:
		default:
		}
	}
}

func (f *presFixture) beat(t *testing.T, c caller, doc string) *httptest.ResponseRecorder {
	t.Helper()
	body := "{}"
	if doc != "" {
		body = `{"document_id":"` + doc + `"}`
	}
	return f.s.do(c, "POST", "/api/v1/projects/"+f.pid+"/presence", body)
}

type presItems struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
	Items   []struct {
		UserID   string `json:"user_id"`
		Name     string `json:"name"`
		Document *struct{ ID, Title string }
	} `json:"items"`
}

func parsePres(t *testing.T, body string) presItems {
	t.Helper()
	var v presItems
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatalf("not a presence answer: %s", body)
	}
	return v
}

// nextPresence returns the next presence frame of a stream, skipping the other events.
func nextPresence(t *testing.T, c *sseClient) frame {
	t.Helper()
	for {
		f := c.next()
		if f.Event == "presence" {
			return f
		}
	}
}

// The stream carries presence, built for each recipient: the editor's frame names the chapter, the reviewer's names
// no chapter, the reader's has no one, and a person who shares nothing is sent nothing. A frame has no id line (it must
// never move the resume cursor) and a canary title never reaches a stream that may not be told it.
func TestStreamPresenceIsBuiltPerRecipient(t *testing.T) {
	f := newPresFixture(t)
	open := func(c caller) *sseClient {
		cl, code := openStream(t, f.ts.URL, c, "", "project="+f.pid, "")
		if code != 200 {
			t.Fatalf("stream: %d", code)
		}
		if h := cl.nextEvent(); h.Event != "hello" {
			t.Fatalf("first event %q", h.Event)
		}
		return cl
	}
	owner, editor, reviewer, reader := open(f.owner), open(f.editor), open(f.reviewer), open(f.reader)
	// everyone's first frame is "you share, no one else is here"
	for who, cl := range map[string]*sseClient{"owner": owner, "editor": editor, "reviewer": reviewer, "reader": reader} {
		fr := nextPresence(t, cl)
		if v := parsePres(t, fr.Data); !v.Enabled || len(v.Items) != 0 {
			t.Fatalf("%s first frame: %s", who, fr.Data)
		}
		if fr.ID != "" {
			t.Fatalf("%s: a presence frame carries an id (%q); it would move the resume cursor", who, fr.ID)
		}
	}
	// the editor opens chapter Beta and the owner chapter one
	if rec := f.beat(t, f.editor, f.docB); rec.Code != 200 {
		t.Fatalf("beat: %d %s", rec.Code, rec.Body)
	}
	if rec := f.beat(t, f.reader, f.docB); rec.Code != 200 {
		t.Fatalf("beat: %d %s", rec.Code, rec.Body)
	}
	f.advance(6 * time.Second)
	f.tick()
	of := nextPresence(t, owner)
	ov := parsePres(t, of.Data)
	if len(ov.Items) != 2 || ov.Items[0].Name != "Edgar" || ov.Items[0].Document == nil || ov.Items[0].Document.Title != presTitleB {
		t.Fatalf("owner's frame: %s", of.Data)
	}
	rv := nextPresence(t, reviewer)
	if strings.Contains(rv.Data, presTitleB) || strings.Contains(rv.Data, f.docB) || strings.Contains(rv.Data, "Rory") || !strings.Contains(rv.Data, "Edgar") {
		t.Fatalf("reviewer's frame: %s", rv.Data)
	}
	// the reader's list is empty before and after
	reader.quiet(50 * time.Millisecond)
	editor.quiet(50 * time.Millisecond) // the editor is not shown a reader, so nothing about their list changed
	// a person who does not share is sent nothing, ever
	f.setShare(t, f.stranger, "false")
	f.s.addMember(t, f.pid, f.stranger.userID, ops.ProjectReader)
	st := open(f.stranger)
	f.advance(6 * time.Second)
	f.tick()
	st.quiet(100 * time.Millisecond)
	// a key's stream gets no presence either
	pin, _ := f.s.mintKey(t, f.owner, `{"name":"k","project_id":"`+f.pid+`"}`)
	kc, _ := openStream(t, f.ts.URL, caller{}, pin, "project="+f.pid, "")
	kc.nextEvent() // hello
	f.advance(6 * time.Second)
	f.tick()
	kc.quiet(100 * time.Millisecond)
}

// Switching presence off ends the frames and tells the stream once; the others lose the person at once.
func TestStreamPresenceStopsWhenSharingIsSwitchedOff(t *testing.T) {
	f := newPresFixture(t)
	owner, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "")
	owner.nextEvent()
	nextPresence(t, owner)
	f.beat(t, f.editor, "")
	f.advance(6 * time.Second)
	f.tick()
	if v := parsePres(t, nextPresence(t, owner).Data); len(v.Items) != 1 {
		t.Fatalf("setup: %+v", v)
	}
	f.setShare(t, f.editor, "false") // the preferences op takes the person off every list at once
	f.advance(6 * time.Second)
	f.tick()
	if v := parsePres(t, nextPresence(t, owner).Data); len(v.Items) != 0 {
		t.Fatalf("a person who switched sharing off is still shown: %+v", v)
	}
	if v := parsePres(t, f.beat(t, f.editor, "").Body.String()); v.Enabled || v.Reason != "off" {
		t.Fatalf("their own beat: %+v", v)
	}
}

// A stream whose client has stopped reading holds nothing up: heartbeats stay fast, and the other streams go on
// receiving both project events and presence. Presence never passes through the hub, and the service never writes to a
// client (each stream builds its own frame in its own goroutine), so a stalled client can only stall itself. This test
// does not fill the socket far enough to make the server's own write block (that path has its 10 second write limit and
// the hub's drop-the-slow-client rule, tested with the stream); it proves nothing else waits for such a client.
func TestAStalledStreamDoesNotSlowPresence(t *testing.T) {
	f := newPresFixture(t)
	// a connection that sent its request and never reads a byte of the answer
	addr := strings.TrimPrefix(f.ts.URL, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(1)
	}
	fmt.Fprintf(conn, "GET /api/v1/events?project=%s HTTP/1.1\r\nHost: x\r\nAccept: text/event-stream\r\nCookie: %s=%s\r\n\r\n", f.pid, f.reviewer.cookie.Name, f.reviewer.cookie.Value)
	live, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "")
	live.nextEvent()
	nextPresence(t, live)
	start := time.Now()
	rev := 1
	for i := 0; i < 300; i++ {
		rec := f.beat(t, f.editor, f.docB)
		if rec.Code != 200 {
			t.Fatalf("beat %d: %d", i, rec.Code)
		}
		if i%10 == 0 {
			f.advance(6 * time.Second)
			f.tick()
			f.save(t, "words "+strings.Repeat("x", i), rev)
			rev++
		}
	}
	d := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if d > 10*time.Second*sseFactor {
			t.Errorf("300 beats took %v beside a stalled stream", d)
		}
	})
	f.advance(6 * time.Second)
	f.tick()
	got := map[string]bool{}
	for len(got) < 2 {
		fr := live.next()
		got[fr.Event] = true
	}
	if !got["presence"] || !got["doc.updated"] {
		t.Fatalf("the live stream saw %v", got)
	}
}

// A heartbeat is not activity. The session is read without refreshing its idle timer (an ordinary request does refresh
// it: the control), nothing is written to the database, and a project with a passphrase is not kept unlocked by a page
// that only beats: its idle limit still ends the unlock, after which beats are refused.
func TestPresenceBeatsAreNotActivity(t *testing.T) {
	f := newPresFixture(t)
	old := db.Format(f.now.Add(-time.Hour))
	setOld := func() {
		if _, err := f.s.DB.Writer().Exec(`UPDATE sessions SET last_seen_at = ?`, old); err != nil {
			t.Fatal(err)
		}
	}
	lastSeen := func() string {
		var s string
		_ = f.s.DB.Read().QueryRow(`SELECT group_concat(last_seen_at) FROM (SELECT last_seen_at FROM sessions ORDER BY id)`).Scan(&s)
		return s
	}
	setOld()
	want := lastSeen()
	for i := 0; i < 5; i++ {
		f.beat(t, f.owner, "")
		f.s.do(f.owner, "GET", "/api/v1/projects/"+f.pid+"/presence", "")
		f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/presence/leave", "")
	}
	if got := lastSeen(); got != want {
		t.Fatalf("a presence request refreshed a session:\n%s\n%s", want, got)
	}
	// the control: an ordinary request does
	f.s.do(f.owner, "GET", "/api/v1/me/prefs", "")
	if lastSeen() == want {
		t.Fatal("control: an ordinary request did not refresh the session, so the check above proves nothing")
	}

	// the gate: unlocked with a one-minute idle limit; a page that only beats does not keep it open
	gate := f.s.gateService()
	clock := f.now
	gate.SetClock(func() time.Time { return clock })
	defer gate.SetClock(time.Now)
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/passphrase", `{"passphrase":"a long enough one","idle_minutes":1}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := f.s.do(f.editor, "POST", "/api/v1/projects/"+f.pid+"/unlock", `{"passphrase":"a long enough one"}`); rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 2; i++ { // 20 s and 40 s: inside the limit
		clock = clock.Add(20 * time.Second)
		if rec := f.beat(t, f.editor, ""); rec.Code != 200 {
			t.Fatalf("beat at +%ds: %d %s", 20*(i+1), rec.Code, rec.Body)
		}
	}
	clock = clock.Add(30 * time.Second) // 70 s since the unlock, 30 s since the last beat: the beats must not have counted
	rec := f.beat(t, f.editor, "")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "project_locked") {
		t.Fatalf("beats kept an unlock alive past its idle limit: %d %s", rec.Code, rec.Body)
	}
}

// presence.share is an ordinary preference: off for a new account, allowed only as true or false.
func TestPresenceShareIsAPreference(t *testing.T) {
	f := newPresFixture(t)
	fresh := f.s.newCaller(t, "fresh", ops.RoleWriter)
	rec := f.s.do(fresh, "GET", "/api/v1/me/prefs", "")
	var got struct{ Items map[string]string }
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Items["presence.share"] != "false" {
		t.Fatalf("a new account's presence.share is %q, want false", got.Items["presence.share"])
	}
	if rec := f.s.do(fresh, "PUT", "/api/v1/me/prefs", `{"items":{"presence.share":"maybe"}}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bad value: %d", rec.Code)
	}
}

// The gate sweep walks every project op as a locked person; presence is covered by it. This one names what the sweep
// cannot: the unlock page's neighbours answer a locked editor with project_locked, never a list.
func TestLockedEditorGetsNoPresenceList(t *testing.T) {
	f := newPresFixture(t)
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/passphrase", `{"passphrase":"a long enough one"}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d", rec.Code)
	}
	for _, req := range [][2]string{{"POST", "/presence"}, {"GET", "/presence"}, {"POST", "/presence/leave"}} {
		rec := f.s.do(f.editor, req[0], "/api/v1/projects/"+f.pid+req[1], "{}")
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "project_locked") || strings.Contains(rec.Body.String(), "Edgar") {
			t.Errorf("%s %s while locked: %d %s", req[0], req[1], rec.Code, rec.Body)
		}
	}
	// and the locked person's stream is refused
	if _, code := openStream(t, f.ts.URL, f.editor, "", "project="+f.pid, ""); code != http.StatusForbidden {
		t.Fatalf("a locked stream: %d", code)
	}
}

// A frame is for a session that may still read the project, like every event: when the unlock of a protected project has
// idled out, the stream ends with bye and no name or title is written first.
func TestStreamPresenceEndsWhenTheUnlockIdlesOut(t *testing.T) {
	f := newPresFixture(t)
	gate := f.s.gateService()
	gate.SetClock(func() time.Time { f.mu.Lock(); defer f.mu.Unlock(); return f.now })
	defer gate.SetClock(time.Now)
	const pass = "a long enough one"
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/passphrase", `{"passphrase":"`+pass+`","idle_minutes":1}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := f.s.do(f.editor, "POST", "/api/v1/projects/"+f.pid+"/unlock", `{"passphrase":"`+pass+`"}`); rec.Code != 200 {
		t.Fatalf("unlock: %d %s", rec.Code, rec.Body)
	}
	cl, code := openStream(t, f.ts.URL, f.editor, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatalf("stream: %d", code)
	}
	cl.nextEvent()
	nextPresence(t, cl)
	f.advance(90 * time.Second) // past the one-minute idle limit: the editor's unlock has ended
	if rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/unlock", `{"passphrase":"`+pass+`"}`); rec.Code != 200 {
		t.Fatalf("owner unlock: %d", rec.Code)
	}
	f.beat(t, f.owner, f.docB) // someone else arrives: the editor's frame would name them and the chapter
	f.tick()
	for {
		fr := cl.next()
		if fr.Event == "presence" || strings.Contains(fr.Data, presTitleB) {
			t.Fatalf("a session that is locked out was sent presence: %s %s", fr.Event, fr.Data)
		}
		if fr.Event == "bye" {
			break
		}
	}
}

// presence.share is the person's to switch at a browser; a key cannot start broadcasting for them. Other preferences are
// unchanged for a key.
func TestAKeyCannotSwitchPresenceOn(t *testing.T) {
	f := newPresFixture(t)
	secret, _ := f.s.mintKey(t, f.stranger, `{"name":"k","scope":"write"}`)
	put := func(body string) int {
		req := typoReq("PUT", "/api/v1/me/prefs", body)
		req.Header.Set("Authorization", "Bearer "+secret)
		return typoServe(f.s, req).Code
	}
	if c := put(`{"items":{"presence.share":"true"}}`); c != http.StatusForbidden {
		t.Fatalf("a key switching presence on: %d", c)
	}
	if c := put(`{"items":{"theme":"dark"}}`); c != 200 {
		t.Fatalf("a key setting another preference: %d", c)
	}
	var got struct{ Items map[string]string }
	_ = json.Unmarshal(f.s.do(f.stranger, "GET", "/api/v1/me/prefs", "").Body.Bytes(), &got)
	if got.Items["presence.share"] != "false" {
		t.Fatalf("presence.share is %q", got.Items["presence.share"])
	}
}

// presence.leave takes no body (a page sends it with sendBeacon as it unloads) and answers 200.
func TestLeaveWithAnEmptyBody(t *testing.T) {
	f := newPresFixture(t)
	f.beat(t, f.editor, "")
	rec := f.s.do(f.editor, "POST", "/api/v1/projects/"+f.pid+"/presence/leave", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"left":true`) {
		t.Fatalf("leave: %d %s", rec.Code, rec.Body)
	}
	if v := parsePres(t, f.s.do(f.owner, "GET", "/api/v1/projects/"+f.pid+"/presence", "").Body.String()); len(v.Items) != 0 {
		t.Fatalf("still shown: %+v", v)
	}
}

// Over HTTP, with an Idempotency-Key on every request: presence takes no part in idempotency, so the database is the same
// after the calls (the key table included) and a repeat is answered afresh, never replayed.
func TestPresenceOverHTTPWritesNothingEvenWithAnIdempotencyKey(t *testing.T) {
	f := newPresFixture(t)
	snap := func() string {
		var sb strings.Builder
		for _, q := range []string{
			`SELECT group_concat(id || last_seen_at || coalesce(idle_expires_at,''), ',') FROM (SELECT * FROM sessions ORDER BY id)`,
			`SELECT group_concat(id || coalesce(last_seen_at,'') || updated_at, ',') FROM (SELECT * FROM users ORDER BY id)`,
			`SELECT count(*) FROM events`, `SELECT count(*) FROM audit_log`, `SELECT count(*) FROM idempotency_keys`,
		} {
			var s *string
			if err := f.s.DB.Read().QueryRow(q).Scan(&s); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if s != nil {
				sb.WriteString(*s)
			}
			sb.WriteString("|")
		}
		return sb.String()
	}
	if _, err := f.s.DB.Writer().Exec(`UPDATE sessions SET last_seen_at = ?`, db.Format(f.now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	before := snap()
	for i := 0; i < 20; i++ {
		for _, c := range []caller{f.owner, f.editor, f.reviewer, f.reader} {
			for _, path := range []string{"/presence", "/presence/leave"} {
				req := typoReq("POST", "/api/v1/projects/"+f.pid+path, "{}")
				req.Header.Set("Idempotency-Key", "k-"+c.name+path)
				req.AddCookie(c.cookie)
				if rec := typoServe(f.s, req); rec.Code != 200 || rec.Header().Get("Idempotent-Replay") != "" {
					t.Fatalf("%s %s: %d replay=%q", c.name, path, rec.Code, rec.Header().Get("Idempotent-Replay"))
				}
			}
			req := typoReq("GET", "/api/v1/projects/"+f.pid+"/presence", "")
			req.AddCookie(c.cookie)
			if rec := typoServe(f.s, req); rec.Code != 200 {
				t.Fatal(rec.Code)
			}
		}
	}
	if after := snap(); after != before {
		t.Fatalf("the database changed under keyed presence calls:\n%s\n%s", before, after)
	}
}

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// The stream tests are timing-sensitive in one way only: they wait for frames
// that a running server produces. Waits are generous and scaled under the race
// detector (sseFactor), and nothing asserts a duration.

type frame struct {
	ID, Event, Data string
	Comment         string
}

// sseClient is a minimal EventSource: it reads frames off one response.
type sseClient struct {
	t      *testing.T
	resp   *http.Response
	frames chan frame
	cancel context.CancelFunc
}

func (c *sseClient) close() {
	c.cancel()
	_ = c.resp.Body.Close()
}

// next returns the next frame or fails after the wait.
func (c *sseClient) next() frame {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if !ok {
			c.t.Fatal("the stream ended")
		}
		return f
	case <-time.After(10 * time.Second * sseFactor):
		c.t.Fatal("no frame arrived")
	}
	return frame{}
}

// nextEvent skips comments and returns the next real event.
func (c *sseClient) nextEvent() frame {
	c.t.Helper()
	for {
		if f := c.next(); f.Comment == "" && f.Event != "" {
			return f
		}
	}
}

// ended reports whether the stream closes within the wait.
func (c *sseClient) ended() bool {
	for {
		select {
		case _, ok := <-c.frames:
			if !ok {
				return true
			}
		case <-time.After(10 * time.Second * sseFactor):
			return false
		}
	}
}

// quiet fails if a real event arrives within a short time.
func (c *sseClient) quiet(d time.Duration) {
	c.t.Helper()
	select {
	case f, ok := <-c.frames:
		if ok && f.Event != "" {
			c.t.Fatalf("unexpected event %s %s", f.Event, f.Data)
		}
	case <-time.After(d):
	}
}

// openStream connects with a cookie or a key. It returns the status when the
// stream is refused.
func openStream(t *testing.T, base string, c caller, key, query, lastID string) (*sseClient, int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/events?"+query, nil)
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, resp.StatusCode
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	cl := &sseClient{t: t, resp: resp, frames: make(chan frame, 256), cancel: cancel}
	go func() {
		defer close(cl.frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<16), 1<<20)
		var f frame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f != (frame{}) {
					cl.frames <- f
				}
				f = frame{}
			case strings.HasPrefix(line, ":"):
				cl.frames <- frame{Comment: strings.TrimSpace(line[1:])}
			case strings.HasPrefix(line, "id: "):
				f.ID = line[4:]
			case strings.HasPrefix(line, "event: "):
				f.Event = line[7:]
			case strings.HasPrefix(line, "data: "):
				f.Data = line[6:]
			}
		}
	}()
	t.Cleanup(cl.close)
	return cl, 200
}

type sseFixture struct {
	s                        *Server
	ts                       *httptest.Server
	owner, reader, stranger  caller
	pid, did, cid            string
	beat                     chan time.Time
	ownerKey, otherProjectKy string
}

func newSSEFixture(t *testing.T, env map[string]string) *sseFixture {
	t.Helper()
	cfg := map[string]string{"SCRIOBH_RATE_LIMIT": "0"}
	for k, v := range env {
		cfg[k] = v
	}
	d := testutil.DB(t)
	s, err := New(Deps{Cfg: testutil.Config(t, cfg), DB: d})
	if err != nil {
		t.Fatal(err)
	}
	f := &sseFixture{s: s, beat: make(chan time.Time, 8)}
	s.sseTick = func(time.Duration) (<-chan time.Time, func()) { return f.beat, func() {} }
	f.ts = httptest.NewServer(s.Handler())
	t.Cleanup(f.ts.Close)
	f.owner = s.newCaller(t, "owner", ops.RoleWriter)
	f.reader = s.newCaller(t, "reader", ops.RoleReader)
	f.stranger = s.newCaller(t, "stranger", ops.RoleWriter)
	mk := func(path, body string) string {
		rec := s.do(f.owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		return idOf(t, rec.Body.Bytes())
	}
	f.pid = mk("/api/v1/projects", `{"title":"Streamed Book"}`)
	f.cid = mk("/api/v1/compendiums", `{"name":"Streamed Bible"}`)
	f.did = mk("/api/v1/projects/"+f.pid+"/docs", `{"title":"Chapter one"}`)
	s.addMember(t, f.pid, f.reader.userID, ops.ProjectReader)
	return f
}

func (f *sseFixture) save(t *testing.T, content string, rev int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"base_revision": rev, "content": content})
	if rec := f.s.do(f.owner, "PATCH", "/api/v1/docs/"+f.did, string(body)); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
}

func TestStreamAuthorization(t *testing.T) {
	f := newSSEFixture(t, nil)
	other := func() string { // a second project, for a key pinned elsewhere
		rec := f.s.do(f.owner, "POST", "/api/v1/projects", `{"title":"Elsewhere"}`)
		return idOf(t, rec.Body.Bytes())
	}()
	pinOther, _ := f.s.mintKey(t, f.owner, `{"name":"k","project_id":"`+other+`"}`)
	pinThis, _ := f.s.mintKey(t, f.owner, `{"name":"k2","project_id":"`+f.pid+`"}`)
	pinCmp, _ := f.s.mintKey(t, f.owner, `{"name":"k3","compendium_id":"`+f.cid+`"}`)
	for _, tc := range []struct {
		name, key, query string
		who              caller
		want             int
	}{
		{"anonymous", "", "project=" + f.pid, caller{}, 401},
		{"owner", "", "project=" + f.pid, f.owner, 200},
		{"project reader", "", "project=" + f.pid, f.reader, 200},
		{"stranger", "", "project=" + f.pid, f.stranger, 404},
		{"missing project", "", "project=prj_00000000000000000000000000", f.owner, 404},
		{"reader of a Compendium they are not on", "", "compendium=" + f.cid, f.reader, 404},
		{"owner of the Compendium", "", "compendium=" + f.cid, f.owner, 200},
		{"neither named", "", "", f.owner, 422},
		{"both named", "", "project=" + f.pid + "&compendium=" + f.cid, f.owner, 422},
		{"key pinned to another project", pinOther, "project=" + f.pid, caller{}, 404},
		{"key pinned to this project", pinThis, "project=" + f.pid, caller{}, 200},
		{"key pinned to this project asking for the Compendium", pinThis, "compendium=" + f.cid, caller{}, 404},
		{"key pinned to a Compendium asking for a project", pinCmp, "project=" + f.pid, caller{}, 404},
		{"key pinned to the Compendium", pinCmp, "compendium=" + f.cid, caller{}, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cl, code := openStream(t, f.ts.URL, tc.who, tc.key, tc.query, "")
			if code != tc.want {
				t.Fatalf("status %d, want %d", code, tc.want)
			}
			if cl != nil {
				if h := cl.nextEvent(); h.Event != "hello" {
					t.Fatalf("first event %q, want hello", h.Event)
				}
			}
		})
	}
	// A missing project and one you cannot see look the same.
	a, _ := openStream(t, f.ts.URL, f.stranger, "", "project="+f.pid, "")
	b, _ := openStream(t, f.ts.URL, f.stranger, "", "project=prj_00000000000000000000000000", "")
	if a != nil || b != nil {
		t.Fatal("a stranger got a stream")
	}
}

func TestStreamDeliversAndResumes(t *testing.T) {
	f := newSSEFixture(t, nil)
	cl, _ := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, "")
	hello := cl.nextEvent()
	if hello.Event != "hello" || !strings.Contains(hello.Data, `"resumed":false`) {
		t.Fatalf("hello: %+v", hello)
	}
	f.save(t, "Secret words that must never travel.", 1)
	ev := cl.nextEvent()
	if ev.Event != "doc.updated" {
		t.Fatalf("event %q: %s", ev.Event, ev.Data)
	}
	var got struct {
		Seq     int64
		Type    string
		Payload map[string]any
		ByYou   bool `json:"by_you"`
	}
	if err := json.Unmarshal([]byte(ev.Data), &got); err != nil {
		t.Fatal(err)
	}
	if got.ByYou || got.Payload["revision"] == nil || got.Payload["words"] == nil {
		t.Errorf("payload: %s", ev.Data)
	}
	if strings.Contains(ev.Data, "Secret") || strings.Contains(ev.Data, "Chapter one") {
		t.Errorf("text in an event: %s", ev.Data)
	}
	lastID := ev.ID
	cl.close()

	// Two saves while disconnected, then resume from the last id: both replayed, in order.
	f.save(t, "one", 2)
	f.save(t, "two", 3)
	cl2, _ := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, lastID)
	e1, e2 := cl2.nextEvent(), cl2.nextEvent()
	if e1.Event != "doc.updated" || e2.Event != "doc.updated" {
		t.Fatalf("replay: %+v %+v", e1, e2)
	}
	if !strings.Contains(e1.Data, `"revision":3`) || !strings.Contains(e2.Data, `"revision":4`) {
		t.Errorf("replayed out of order: %s %s", e1.Data, e2.Data)
	}
	if h := cl2.nextEvent(); h.Event != "hello" || !strings.Contains(h.Data, `"resumed":true`) {
		t.Fatalf("resume hello: %+v", h)
	}
	// And live events keep flowing on the resumed stream without repeating the replayed ones.
	f.save(t, "three", 4)
	if e := cl2.nextEvent(); !strings.Contains(e.Data, `"revision":5`) {
		t.Errorf("live after resume: %s", e.Data)
	}
}

func TestStreamResetWhenTooFarBehindOrFromTheFuture(t *testing.T) {
	f := newSSEFixture(t, map[string]string{"SCRIOBH_SSE_REPLAY_EVENTS": "2"})
	for i := 0; i < 5; i++ {
		f.save(t, strings.Repeat("x", i+1), i+1)
	}
	cl, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "1")
	if e := cl.nextEvent(); e.Event != "reset" || !strings.Contains(e.Data, "too_far_behind") {
		t.Fatalf("too far behind: %+v", e)
	}
	cl.close()
	cl, _ = openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "99999999")
	if e := cl.nextEvent(); e.Event != "reset" {
		t.Fatalf("from the future: %+v", e)
	}
	// After a reset the stream carries on live from the new cursor.
	f.save(t, "later", 6)
	if e := cl.nextEvent(); e.Event != "doc.updated" {
		t.Fatalf("after reset: %+v", e)
	}
	cl.close()
	// Too old: the first event after the client's number is older than the age window.
	f2 := newSSEFixture(t, map[string]string{"SCRIOBH_SSE_REPLAY_HOURS": "1"})
	f2.save(t, "old", 1)
	if _, err := f2.s.DB.Writer().Exec(`UPDATE events SET created_at = '2001-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	cl, _ = openStream(t, f2.ts.URL, f2.owner, "", "project="+f2.pid, "0")
	if e := cl.nextEvent(); e.Event != "reset" {
		t.Fatalf("too old: %+v", e)
	}
}

func TestStreamHeartbeat(t *testing.T) {
	f := newSSEFixture(t, nil)
	cl, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "")
	cl.nextEvent()
	f.beat <- time.Now()
	for {
		if fr := cl.next(); fr.Comment == "heartbeat" {
			break
		}
	}
}

func TestStreamEndsWhenAccessIsLost(t *testing.T) {
	f := newSSEFixture(t, nil)
	cl, _ := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, "")
	cl.nextEvent()
	if _, err := f.s.DB.Writer().Exec(`DELETE FROM project_members WHERE user_id = ?`, f.reader.userID); err != nil {
		t.Fatal(err)
	}
	f.beat <- time.Now()
	if e := cl.nextEvent(); e.Event != "bye" || !strings.Contains(e.Data, "access") {
		t.Fatalf("expected bye: %+v", e)
	}
	if !cl.ended() {
		t.Fatal("the stream stayed open")
	}
}

func TestStreamEndsWhenTheKeyIsRevoked(t *testing.T) {
	f := newSSEFixture(t, nil)
	secret, id := f.s.mintKey(t, f.owner, `{"name":"watch","project_id":"`+f.pid+`"}`)
	cl, _ := openStream(t, f.ts.URL, caller{}, secret, "project="+f.pid, "")
	cl.nextEvent()
	if rec := f.s.do(f.owner, "POST", "/api/v1/keys/"+id+"/revoke", ""); rec.Code >= 300 {
		t.Fatalf("revoke: %d", rec.Code)
	}
	f.beat <- time.Now()
	if e := cl.nextEvent(); e.Event != "bye" {
		t.Fatalf("expected bye: %+v", e)
	}
}

func TestStreamLimits(t *testing.T) {
	f := newSSEFixture(t, map[string]string{"SCRIOBH_SSE_PER_USER": "2", "SCRIOBH_SSE_TOTAL": "3"})
	for i := 0; i < 2; i++ {
		if _, code := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, ""); code != 200 {
			t.Fatalf("stream %d: %d", i, code)
		}
	}
	if _, code := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, ""); code != 429 {
		t.Errorf("third stream for one person: %d, want 429", code)
	}
	if _, code := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, ""); code != 200 {
		t.Errorf("another person's first: %d", code)
	}
	// The instance cap: three are open.
	if _, code := openStream(t, f.ts.URL, f.stranger, "", "project="+f.pid, ""); code != 404 {
		t.Errorf("authorization comes first: %d", code)
	}
	f.s.addMember(t, f.pid, f.stranger.userID, ops.ProjectReader)
	if _, code := openStream(t, f.ts.URL, f.stranger, "", "project="+f.pid, ""); code != 429 {
		t.Errorf("instance cap: %d, want 429", code)
	}
}

// A stream is released when the client goes away, so the limit does not leak.
func TestStreamSlotIsReleased(t *testing.T) {
	f := newSSEFixture(t, map[string]string{"SCRIOBH_SSE_PER_USER": "1"})
	cl, code := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatal(code)
	}
	cl.nextEvent()
	cl.close()
	deadline := time.Now().Add(10 * time.Second * sseFactor)
	for f.s.OpenStreams() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.s.OpenStreams(); n != 0 {
		t.Fatalf("%d streams still counted", n)
	}
	if f.s.Hub.Subscribers() != 0 {
		t.Errorf("the hub still has %d subscribers", f.s.Hub.Subscribers())
	}
	if _, code := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, ""); code != 200 {
		t.Errorf("after release: %d", code)
	}
}

// What a Compendium event may tell a project stream, and the reverse: ids of
// the other kind of object never cross.
func TestStreamScopesDoNotLeak(t *testing.T) {
	f := newSSEFixture(t, nil)
	// The reader is on the project, not on the Compendium. The owner wires the
	// Compendium to the project and edits the bible.
	pc, _ := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, "")
	cc, _ := openStream(t, f.ts.URL, f.owner, "", "compendium="+f.cid, "")
	pc.nextEvent()
	cc.nextEvent()
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/compendium", `{"compendium_id":"`+f.cid+`"}`); rec.Code != 200 {
		t.Fatalf("set primary: %d %s", rec.Code, rec.Body)
	}
	ent := idOf(t, f.s.do(f.owner, "POST", "/api/v1/compendiums/"+f.cid+"/entities", `{"kind":"character","name":"Máire Ní Bhriain"}`).Body.Bytes())
	if rec := f.s.do(f.owner, "PUT", "/api/v1/projects/"+f.pid+"/entity-overrides", `{"entity_id":"`+ent+`","name":"Mary"}`); rec.Code >= 300 {
		t.Fatalf("override: %d %s", rec.Code, rec.Body)
	}
	// The project stream sees the project's own events, with no Compendium or entity id.
	var seen []string
	for i := 0; i < 2; i++ {
		e := pc.nextEvent()
		seen = append(seen, e.Event)
		for _, banned := range []string{f.cid, ent, "Máire", "Mary", "cmp_", "ent_"} {
			if strings.Contains(e.Data, banned) {
				t.Errorf("%s leaked %q to a project reader: %s", e.Event, banned, e.Data)
			}
		}
	}
	// Someone on the book who is not on the Compendium hears that the bible
	// changed, not which operation it was.
	if seen[0] != "compendium.changed" || seen[1] != "compendium.changed" {
		t.Errorf("project stream saw %v", seen)
	}
	pc.quiet(200 * time.Millisecond)
	// The Compendium stream sees the Compendium's events and never a project's.
	var ctypes []string
	for i := 0; i < 2; i++ {
		e := cc.nextEvent()
		ctypes = append(ctypes, e.Event)
		if strings.Contains(e.Data, f.pid) || strings.Contains(e.Data, "prj_") || strings.Contains(e.Data, "Streamed Book") {
			t.Errorf("%s leaked a project to the Compendium stream: %s", e.Event, e.Data)
		}
	}
	if ctypes[0] != "compendium.attached" || ctypes[1] != "entity.created" {
		t.Errorf("compendium stream saw %v", ctypes)
	}
	cc.quiet(200 * time.Millisecond)
	// A document save in the project is not a Compendium event.
	f.save(t, "text", 1)
	cc.quiet(200 * time.Millisecond)
	if e := pc.nextEvent(); e.Event != "doc.updated" {
		t.Errorf("project stream: %+v", e)
	}
}

// The server stops its streams before it shuts down; Serve would otherwise wait
// for them.
func TestStreamsStopOnShutdown(t *testing.T) {
	d := testutil.DB(t)
	s, err := New(Deps{Cfg: testutil.Config(t, map[string]string{"SCRIOBH_RATE_LIMIT": "0"}), DB: d})
	if err != nil {
		t.Fatal(err)
	}
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	pid := idOf(t, s.do(owner, "POST", "/api/v1/projects", `{"title":"Stop"}`).Body.Bytes())
	ln := listenLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	base := "http://" + ln.Addr().String()
	cl, code := openStream(t, base, owner, "", "project="+pid, "")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	cl.nextEvent()
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(20 * time.Second * sseFactor):
		t.Fatal("Serve did not return with a stream open")
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 8*time.Second*sseFactor {
			t.Errorf("shutdown took %v", took)
		}
	})
	if e := cl.nextEvent(); e.Event != "bye" {
		t.Errorf("the stream ended without a bye: %+v", e)
	}
	if s.OpenStreams() != 0 {
		t.Errorf("%d streams left", s.OpenStreams())
	}
}

// Concurrent writers and many streams: nothing is delivered twice or out of order.
func TestStreamOrderingUnderLoad(t *testing.T) {
	f := newSSEFixture(t, nil)
	const n = 30
	var clients []*sseClient
	for i := 0; i < 3; i++ {
		cl, _ := openStream(t, f.ts.URL, f.owner, "", "project="+f.pid, "")
		cl.nextEvent()
		clients = append(clients, cl)
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := `{"title":"n` + string(rune('a'+i%26)) + `"}`
			f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/docs", body)
		}(i)
	}
	wg.Wait()
	for _, cl := range clients {
		var prev int64
		for i := 0; i < n; i++ {
			e := cl.nextEvent()
			var got struct{ Seq int64 }
			_ = json.Unmarshal([]byte(e.Data), &got)
			if got.Seq <= prev {
				t.Fatalf("event %d (seq %d) after seq %d", i, got.Seq, prev)
			}
			prev = got.Seq
		}
	}
	_ = docs.Kinds
}

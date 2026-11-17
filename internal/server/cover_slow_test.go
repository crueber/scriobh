package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// slowWorld is a project with an owner and two other editors behind a real http.Server (the production one, with its
// timeouts, unless the test changes them), for the tests of how slowly a cover body may arrive.
type slowWorld struct {
	s                    *Server
	ts                   *httptest.Server
	owner, second, third caller
	pid                  string
}

func newSlowWorld(t *testing.T, tune func(*http.Server)) *slowWorld {
	t.Helper()
	s := newAuthzServer(t)
	w := &slowWorld{s: s}
	w.owner, w.second, w.third = s.newCaller(t, "owner", ops.RoleWriter), s.newCaller(t, "second", ops.RoleWriter), s.newCaller(t, "third", ops.RoleWriter)
	w.pid = idOf(t, s.do(w.owner, "POST", "/api/v1/projects", `{"title":"Slow Book"}`).Body.Bytes())
	s.addMember(t, w.pid, w.second.userID, ops.ProjectEditor)
	s.addMember(t, w.pid, w.third.userID, ops.ProjectEditor)
	w.ts = httptest.NewUnstartedServer(nil)
	w.ts.Config = s.httpServer() // the production timeouts
	if tune != nil {
		tune(w.ts.Config)
	}
	w.ts.Start()
	t.Cleanup(w.ts.Close)
	return w
}

const slowBoundary = "slowboundary"

// open starts a cover post on its own connection: the headers say length bytes are coming, and the body so far is a field
// and the start of a picture. The caller sends the rest, or stops.
func (w *slowWorld) open(t *testing.T, c caller, length int, head string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", w.ts.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	tok := w.s.Sess.CSRFToken(sessionOf(t, w.s, c))
	fmt.Fprintf(conn, "POST /ui/projects/%s/cover HTTP/1.1\r\nHost: x\r\nCookie: %s=%s\r\nX-CSRF-Token: %s\r\nContent-Type: multipart/form-data; boundary=%s\r\nContent-Length: %d\r\n\r\n",
		w.pid, c.cookie.Name, c.cookie.Value, tok, slowBoundary, length)
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	return conn
}

func slowHead() string {
	return "--" + slowBoundary + "\r\nContent-Disposition: form-data; name=\"shape\"\r\n\r\nsides\r\n" +
		"--" + slowBoundary + "\r\nContent-Disposition: form-data; name=\"front_file\"; filename=\"a.jpg\"\r\nContent-Type: image/jpeg\r\n\r\n"
}

// small is a whole, ordinary cover post (the generated cover, saved), on a connection of its own.
func (w *slowWorld) small(t *testing.T, c caller) int {
	t.Helper()
	body := "--" + slowBoundary + "\r\nContent-Disposition: form-data; name=\"shape\"\r\n\r\ngenerated\r\n--" + slowBoundary + "\r\nContent-Disposition: form-data; name=\"action\"\r\n\r\nsave\r\n--" + slowBoundary + "--\r\n"
	conn := w.open(t, c, len(body), "")
	_, _ = io.WriteString(conn, body)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("a small post: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// One person cannot hold the places that every other editor needs. With the production timeouts: a first body that
// trickles a byte a second holds one place; the same person's second body is turned away at once (429), another editor is
// admitted meanwhile, and when the trickler goes quiet the connection is dropped after about the idle limit, not the
// absolute ReadTimeout, and the person's place is free again.
func TestOnePersonCannotHoldTheCoverBodySlots(t *testing.T) {
	w := newSlowWorld(t, nil)
	const length = 70 << 20
	a1 := w.open(t, w.owner, length, slowHead())
	// a byte a second, for a few seconds
	for i := 0; i < 3; i++ {
		time.Sleep(time.Second)
		if _, err := a1.Write([]byte{1}); err != nil {
			t.Fatalf("the trickle was cut early: %v", err)
		}
	}
	if code := w.small(t, w.owner); code != 429 {
		t.Errorf("the same person's second body while the first is open: %d, want 429", code)
	}
	if code := w.small(t, w.second); code != 303 {
		t.Errorf("another editor while one person trickles: %d, want 303 (admitted)", code)
	}
	if code := w.small(t, w.third); code != 303 {
		t.Errorf("a third editor: %d, want 303", code)
	}
	// the trickler goes quiet: the server drops it when the idle limit runs out
	quiet := time.Now()
	_ = a1.SetReadDeadline(quiet.Add(40 * time.Second))
	_, err := io.Copy(io.Discard, a1)
	dropped := time.Since(quiet)
	if err != nil && !strings.Contains(err.Error(), "reset") {
		t.Fatalf("waiting to be dropped: %v after %v", err, dropped)
	}
	// timing: not a budget: tells the 15 s idle deadline from the 60 s absolute ReadTimeout; the window is wide
	if dropped < bodyIdle-3*time.Second || dropped > bodyIdle+10*time.Second {
		t.Errorf("a connection that went quiet was dropped after %v, want about %v (the absolute ReadTimeout is %v)", dropped.Round(time.Second), bodyIdle, w.ts.Config.ReadTimeout)
	}
	if code := w.small(t, w.owner); code != 303 {
		t.Errorf("the person's place after the drop: %d, want 303", code)
	}
}

// A writer on a slow uplink is not cut off by the absolute ReadTimeout and WriteTimeout (here made shorter than the
// upload): as long as bytes keep coming the picture is received and answered.
func TestASlowUplinkIsNotCutByTheAbsoluteTimeout(t *testing.T) {
	w := newSlowWorld(t, func(hs *http.Server) { hs.ReadTimeout, hs.WriteTimeout = 2*time.Second, 2*time.Second })
	w.s.bodyIdle, w.s.bodyCeiling = 1500*time.Millisecond, 30*time.Second
	pic := coverPicture(300, 450)
	tail := "\r\n--" + slowBoundary + "\r\nContent-Disposition: form-data; name=\"action\"\r\n\r\nupload\r\n--" + slowBoundary + "--\r\n"
	head := slowHead()
	length := len(head) + len(pic) + len(tail)
	conn := w.open(t, w.owner, length, head)
	start := time.Now()
	const chunks = 25
	step := len(pic)/chunks + 1
	for off := 0; off < len(pic); off += step {
		time.Sleep(200 * time.Millisecond)
		if _, err := conn.Write(pic[off:min(off+step, len(pic))]); err != nil {
			t.Fatalf("cut after %v: %v", time.Since(start), err)
		}
	}
	_, _ = io.Copy(conn, bytes.NewReader([]byte(tail)))
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer after %v: %v", time.Since(start), err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if took := time.Since(start); took < 3*time.Second {
		t.Fatalf("the upload took only %v: it did not outlast the 2 s ReadTimeout", took)
	}
	if resp.StatusCode != 200 || !strings.Contains(string(b), "a.jpg") {
		t.Errorf("the slow picture: %d\n%.300s", resp.StatusCode, b)
	}
}

// The idle limit measures the time spent waiting for the sender, not the time the server spends working between reads: a
// handler that reads a part, works for three times the idle limit (cleaning the front picture on a slow machine) and
// reads on must find the rest of the body, every byte of which was sent at once. On the production http.Server, with
// the idle limit shortened.
func TestIdleLimitDoesNotCountTheServersOwnWork(t *testing.T) {
	s := newAuthzServer(t)
	const idle = 300 * time.Millisecond
	type result struct {
		first, second int
		err           error
	}
	got := make(chan result, 1)
	hs := s.httpServer()
	hs.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pb := &pacedBody{ReadCloser: r.Body, rc: http.NewResponseController(w), start: time.Now(), idle: idle, ceiling: time.Minute}
		pb.arm()
		var res result
		buf := make([]byte, 1<<20)
		res.first, res.err = io.ReadFull(pb, buf)
		time.Sleep(3 * idle)
		if res.err == nil {
			var rest []byte
			rest, res.err = io.ReadAll(pb)
			res.second = len(rest)
		}
		got <- res
	})
	ts := httptest.NewUnstartedServer(nil)
	ts.Config = hs
	ts.Start()
	defer ts.Close()
	resp, err := http.Post(ts.URL, "application/octet-stream", bytes.NewReader(make([]byte, 2<<20)))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	res := <-got
	if res.err != nil || res.first != 1<<20 || res.second != 1<<20 {
		t.Errorf("read %d, slept, then read %d more, err %v; want 1 MiB then 1 MiB and no error", res.first, res.second, res.err)
	}
}

// A sender who dribbles a byte every few seconds is never idle for long enough to be dropped, but would hold a place until
// the ceiling: once the grace has passed, a body whose average rate (over the time spent waiting) is under the floor is
// dropped and its places are freed. Times and floor shortened; a steady slow uplink (the test above) is not affected.
func TestATricklingBodyIsDroppedBelowTheFloor(t *testing.T) {
	w := newSlowWorld(t, nil)
	w.s.bodyIdle, w.s.bodyCeiling = 1500*time.Millisecond, 60*time.Second
	w.s.bodyGrace, w.s.bodyFloor = 2*time.Second, 1000
	a1 := w.open(t, w.owner, 70<<20, slowHead())
	start := time.Now()
	var werr error
	for time.Since(start) < 20*time.Second && werr == nil {
		time.Sleep(400 * time.Millisecond) // well inside the idle limit
		_, werr = a1.Write([]byte{1})
	}
	// the server has dropped it; a write error may take a moment, a read shows it at once
	_ = a1.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rerr := io.Copy(io.Discard, a1)
	dropped := time.Since(start)
	if rerr != nil && !strings.Contains(rerr.Error(), "reset") {
		t.Fatalf("waiting to be dropped: %v after %v", rerr, dropped)
	}
	// timing: not a budget: tells a drop at the floor (seconds) from one at the idle limit or the ceiling (a minute); the window is wide
	if dropped > 10*time.Second {
		t.Errorf("a body trickling a byte every 400 ms was dropped after %v, want within a few seconds of the 2 s grace (the ceiling is %v)", dropped.Round(time.Second), w.s.bodyCeiling)
	}
	if code := w.small(t, w.owner); code != 303 {
		t.Errorf("the person's place after the drop: %d, want 303", code)
	}
}

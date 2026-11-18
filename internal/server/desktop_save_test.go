package server

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/desktopidp"
)

// The shell's "save now" (desktop_save.go): proved with a key derived from the launch token, delivered to a writing page
// over its live stream, answered by the page. The vector is held by the shell too (desktop/src-tauri/src/launch.rs).
const (
	saveVectorToken = "5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e"
	saveVectorKey   = "df273ae541b1d2b556b04201920d974ac90f102420e414904784074538a002c7"
)

func TestTheShellKeyVectorIsTheShells(t *testing.T) {
	l := desktopidp.NewLaunch(saveVectorToken)
	ok := func(v ...string) bool {
		r, _ := http.NewRequest("POST", "/desktop/save-now", nil)
		for _, x := range v {
			r.Header.Add(desktopidp.ShellHeader, x)
		}
		return l.ShellOK(r)
	}
	if !ok(saveVectorKey) {
		t.Fatal("the key derived from the shared token is not the vector the shell holds")
	}
	for name, v := range map[string][]string{"none": nil, "empty": {""}, "the token itself": {saveVectorToken}, "upper case": {strings.ToUpper(saveVectorKey)},
		"a prefix": {saveVectorKey[:63]}, "twice": {saveVectorKey, saveVectorKey}, "the healthz key": {"68976f01ca10fc56efcfd44364fc8a604e14f46723d71f4d717517802c04c899"}} {
		if ok(v...) {
			t.Errorf("%s was accepted as the shell's key", name)
		}
	}
}

func hexHMAC(key []byte, msg string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// registered counts the open writing streams.
func (h *saveHub) registered() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.flushers)
}

// hopBody makes one request with a body, without following its redirect.
func (win *window) hopBody(method, path, body string, edit func(*http.Request)) page {
	old := win.c.CheckRedirect
	win.c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	defer func() { win.c.CheckRedirect = old }()
	req, err := http.NewRequest(method, win.w.base+path, strings.NewReader(body))
	if err != nil {
		win.w.t.Fatal(err)
	}
	edit(req)
	resp, err := win.c.Do(req)
	if err != nil {
		win.w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return page{status: resp.StatusCode, body: b.String(), header: resp.Header}
}

// shellKey is the key the shell derives from the launch token of the test world.
func shellKey(token string) string {
	return hexHMAC([]byte(token), desktopidp.ShellKeyLabel)
}

func (win *window) shell(method, path string, key string) page {
	return win.hop(method, path, nil, func(r *http.Request) {
		if key != "" {
			r.Header.Set(desktopidp.ShellHeader, key)
		}
	})
}

// liveStream is an open /api/v1/events stream read in the background.
type liveStream struct {
	frames chan [2]string // event name, data
	closed chan struct{}
	body   interface{ Close() error }
}

func (win *window) openLive(t *testing.T, query string) *liveStream {
	t.Helper()
	win.c.Timeout = 0
	resp, err := win.c.Get(win.w.base + "/api/v1/events?" + query)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("the live stream answered %d", resp.StatusCode)
	}
	ls := &liveStream{frames: make(chan [2]string, 32), closed: make(chan struct{}), body: resp.Body}
	go func() {
		defer close(ls.closed)
		sc := bufio.NewScanner(resp.Body)
		ev, data := "", ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev != "" {
					ls.frames <- [2]string{ev, data}
				}
				ev, data = "", ""
			case strings.HasPrefix(line, "event: "):
				ev = line[len("event: "):]
			case strings.HasPrefix(line, "data: "):
				data = line[len("data: "):]
			}
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return ls
}

// next waits for a frame of the given event name; "" means no frame within the wait.
func (ls *liveStream) next(name string, wait time.Duration) string {
	deadline := time.After(wait)
	for {
		select {
		case f := <-ls.frames:
			if f[0] == name {
				return f[1]
			}
		case <-deadline:
			return ""
		}
	}
}

func TestDesktopSaveNowReachesAWritingPageAndWaitsForItsAnswer(t *testing.T) {
	w := newDesk(t, nil)
	win := w.firstRun("Owner", "")
	st, out := win.json("POST", "/api/v1/projects", `{"title":"A Book"}`)
	pid := findString(out, "id")
	if st >= 300 || pid == "" {
		t.Fatalf("making a project: %d %v", st, out)
	}
	key := shellKey(deskToken)

	// Only the shell may start a round or read one.
	for name, k := range map[string]string{"no key": "", "a wrong key": strings.Repeat("0", 64), "the token": deskToken} {
		if p := win.shell("POST", "/desktop/save-now", k); p.status != 404 {
			t.Errorf("save-now with %s: %d, want 404", name, p.status)
		}
		if p := win.shell("GET", "/desktop/save-status?n=1", k); p.status != 404 {
			t.Errorf("save-status with %s: %d, want 404", name, p.status)
		}
	}
	// A foreign Host is refused before anything (the Host check is in front of every route).
	if p := win.hop("POST", "/desktop/save-now", nil, func(r *http.Request) { r.Host = "evil.example"; r.Header.Set(desktopidp.ShellHeader, key) }); p.status != http.StatusMisdirectedRequest {
		t.Errorf("save-now for a foreign Host: %d", p.status)
	}

	// No page is open: the shell is told that nobody was asked, which is not the same as "all saved".
	var first struct {
		N     int64 `json:"n"`
		Pages int   `json:"pages"`
	}
	p := win.shell("POST", "/desktop/save-now", key)
	if p.status != 200 || json.Unmarshal([]byte(p.body), &first) != nil || first.Pages != 0 {
		t.Fatalf("save-now with no page open: %d %s", p.status, p.body)
	}

	// A plain live stream (no flush=1) is not a writing page and is not asked, however many there are.
	plain := win.openLive(t, "project="+pid)
	writing := win.openLive(t, "project="+pid+"&flush=1")
	waitFor(t, "the writing page's stream is registered", func() bool { return w.s.desk.save.registered() == 1 })

	p = win.shell("POST", "/desktop/save-now", key)
	var round struct {
		N     int64 `json:"n"`
		Pages int   `json:"pages"`
	}
	if p.status != 200 || json.Unmarshal([]byte(p.body), &round) != nil || round.Pages != 1 || round.N != first.N+1 {
		t.Fatalf("save-now with one writing page: %d %s", p.status, p.body)
	}
	frame := writing.next("desktop.save", 5*time.Second)
	if frame == "" {
		t.Fatal("the writing page was not told to save")
	}
	if got := plain.next("desktop.save", 300*time.Millisecond); got != "" {
		t.Errorf("a stream that is not a writing page was told to save: %s", got)
	}
	var ev struct {
		N       int64  `json:"n"`
		Flusher string `json:"flusher"`
	}
	if err := json.Unmarshal([]byte(frame), &ev); err != nil || ev.N != round.N || len(ev.Flusher) != 24 {
		t.Fatalf("the event is %s (%v)", frame, err)
	}
	if strings.Contains(frame, pid) || strings.Contains(frame, deskToken) {
		t.Error("the event names the project or carries the token")
	}

	status := func() (int, int, int) {
		p := win.shell("GET", "/desktop/save-status?n="+strconv.FormatInt(round.N, 10), key)
		var s struct{ Pending, Unsaved int }
		_ = json.Unmarshal([]byte(p.body), &s)
		return p.status, s.Pending, s.Unsaved
	}
	if code, pending, unsaved := status(); code != 200 || pending != 1 || unsaved != 0 {
		t.Fatalf("before the page answers: %d pending %d unsaved %d", code, pending, unsaved)
	}

	// The page's answer needs the launch cookie, the fetch header and the id its event carried.
	answer := func(win *window, headers bool, flusher string, n int64, saved bool) page {
		body := `{"n":` + strconv.FormatInt(n, 10) + `,"flusher":"` + flusher + `","saved":` + strconv.FormatBool(saved) + `}`
		req := func(r *http.Request) {
			r.Header.Set("Content-Type", "application/json")
			if headers {
				r.Header.Set("X-Scriobh-Fetch", "1")
			}
		}
		return win.hopBody("POST", "/desktop/saved", body, req)
	}
	if p := answer(w.window(), true, ev.Flusher, ev.N, true); p.status != 404 {
		t.Errorf("an answer with no launch cookie: %d, want 404", p.status)
	}
	if p := answer(win, false, ev.Flusher, ev.N, true); p.status != 404 {
		t.Errorf("an answer without the fetch header: %d, want 404", p.status)
	}
	if p := answer(win, true, strings.Repeat("0", 24), ev.N, true); p.status != http.StatusConflict {
		t.Errorf("an answer from a page that was not asked: %d, want 409", p.status)
	}
	if p := answer(win, true, ev.Flusher, ev.N+1, true); p.status != http.StatusConflict {
		t.Errorf("an answer for another round: %d, want 409", p.status)
	}
	if code, pending, _ := status(); code != 200 || pending != 1 {
		t.Errorf("a refused answer counted: pending %d", pending)
	}
	if p := answer(win, true, ev.Flusher, ev.N, false); p.status != 204 {
		t.Fatalf("the page's answer (text left): %d %s", p.status, p.body)
	}
	if code, pending, unsaved := status(); code != 200 || pending != 0 || unsaved != 1 {
		t.Errorf("after an answer that says text is left: %d pending %d unsaved %d", code, pending, unsaved)
	}
	if p := answer(win, true, ev.Flusher, ev.N, true); p.status != 204 {
		t.Fatalf("the page's second answer: %d", p.status)
	}
	if code, pending, unsaved := status(); code != 200 || pending != 0 || unsaved != 0 {
		t.Errorf("after the page saved: %d pending %d unsaved %d", code, pending, unsaved)
	}

	// A new round makes the old one stale.
	p = win.shell("POST", "/desktop/save-now", key)
	if p.status != 200 {
		t.Fatal(p.body)
	}
	if code, _, _ := status(); code != http.StatusConflict {
		t.Errorf("the old round's status: %d, want 409", code)
	}
	if p := win.shell("GET", "/desktop/save-status?n=abc", key); p.status != 400 {
		t.Errorf("a status with no round number: %d, want 400", p.status)
	}

	// A page that goes away before it answers is not waited for: leaving a page saves it, and the draft is the rest.
	var third struct{ N int64 }
	_ = json.Unmarshal([]byte(win.shell("POST", "/desktop/save-now", key).body), &third)
	_ = writing.next("desktop.save", 5*time.Second)
	writing.body.Close()
	<-writing.closed
	waitFor(t, "the closed stream is no longer waited for, and is counted as gone, not as saved", func() bool {
		p := win.shell("GET", "/desktop/save-status?n="+strconv.FormatInt(third.N, 10), key)
		return p.status == 200 && strings.Contains(p.body, `"pending":0`) && strings.Contains(p.body, `"gone":1`) && strings.Contains(p.body, `"unsaved":0`)
	})
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/cli"
)

// stdio runs `scriobh-cli mcp` with the given lines on stdin against a server
// and returns the lines it wrote to stdout.
func (e *env) stdio(key string, lines []string, args ...string) (out []string, stderr string) {
	e.t.Helper()
	var o, errb bytes.Buffer
	vars := map[string]string{"SCRIOBH_CONFIG_DIR": e.config, "SCRIOBH_CACHE_DIR": e.cache, "HOME": e.home, "SCRIOBH_URL": e.ts.URL, "SCRIOBH_API_KEY": key}
	rt := &cli.Runtime{
		Stdin: strings.NewReader(strings.Join(lines, "\n") + "\n"), Stdout: &o, Stderr: &errb,
		Getenv: func(k string) string { return vars[k] }, IsTTY: func(io.Writer) bool { return false }, Now: time.Now,
	}
	if code := cli.Main(rt, append([]string{"mcp"}, args...)); code != 0 {
		e.t.Fatalf("mcp exited %d: %s", code, errb.String())
	}
	sc := bufio.NewScanner(&o)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			out = append(out, sc.Text())
		}
	}
	return out, errb.String()
}

func byID(t *testing.T, lines []string) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	for _, l := range lines {
		var x map[string]any
		if err := json.Unmarshal([]byte(l), &x); err != nil {
			t.Fatalf("stdout line is not JSON: %q", l)
		}
		m[fmt.Sprint(x["id"])] = x
	}
	return m
}

const modernMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

func TestStdioProxyBothEras(t *testing.T) {
	e := newEnv(t, nil)
	out, stderr := e.stdio(e.key, []string{
		// Legacy: handshake, notification, then calls that carry no version.
		`{"jsonrpc":"2.0","id":"i","method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"l","method":"tools/list","params":{}}`,
		// Modern: no handshake; the proxy supplies the headers.
		`{"jsonrpc":"2.0","id":"d","method":"server/discover","params":{` + modernMeta + `}}`,
		`{"jsonrpc":"2.0","id":"c","method":"tools/call","params":{"name":"project_create","arguments":{"title":"Over stdio"},` + modernMeta + `}}`,
		`{"jsonrpc":"2.0","id":"u","method":"tools/call","params":{"name":"héllo","arguments":{},` + modernMeta + `}}`,
		`{"jsonrpc":"2.0","id":"p","method":"ping"}`,
	})
	if strings.Contains(stderr, e.key) {
		t.Error("the key reached stderr")
	}
	m := byID(t, out)
	if len(m) != 6 { // the notification gets no answer
		t.Fatalf("%d answers: %v", len(m), out)
	}
	if r := m["i"]["result"].(map[string]any); r["protocolVersion"] != "2025-06-18" {
		t.Errorf("initialize: %v", m["i"])
	}
	if r := m["l"]["result"].(map[string]any); len(r["tools"].([]any)) != 100 || r["resultType"] != nil {
		t.Errorf("legacy list: %.200v", m["l"])
	}
	if r := m["d"]["result"].(map[string]any); r["resultType"] != "complete" || r["supportedVersions"] == nil {
		t.Errorf("discover: %v", m["d"])
	}
	if r := m["c"]["result"].(map[string]any); r["isError"] != false || !strings.Contains(fmt.Sprint(r["content"]), "Over stdio") {
		t.Errorf("a tool call: %v", m["c"])
	}
	// A name that is not header-safe goes through the base64 form of Mcp-Name.
	if m["u"]["error"] == nil || !strings.Contains(fmt.Sprint(m["u"]["error"]), "Unknown tool") {
		t.Errorf("a non-ASCII tool name should reach the server intact and be unknown: %v", m["u"])
	}
	if m["p"]["result"] == nil {
		t.Errorf("ping: %v", m["p"])
	}
}

func TestStdioProxyFiltersAndErrors(t *testing.T) {
	e := newEnv(t, nil)
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{` + modernMeta + `}}`
	out, _ := e.stdio(e.key, []string{list}, "--groups", "doc,search", "--read-only")
	tools := byID(t, out)["1"]["result"].(map[string]any)["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	for _, x := range tools {
		n := x.(map[string]any)["name"].(string)
		if !(strings.HasPrefix(n, "doc_") || strings.HasPrefix(n, "search_")) || strings.Contains(n, "update") || strings.Contains(n, "create") {
			t.Errorf("%s should not be offered under --groups doc,search --read-only", n)
		}
	}
	// A bad key: the server's refusal is answered to the request that was sent (its id), not to null.
	out, _ = e.stdio("scr_notakey", []string{list})
	m := byID(t, out)
	if m["1"] == nil || m["1"]["error"] == nil {
		t.Errorf("a refused key: %v", out)
	}
	// An unknown group is an error answered to the request.
	out, _ = e.stdio(e.key, []string{list}, "--groups", "nonsense")
	if m := byID(t, out); m["1"]["error"] == nil {
		t.Errorf("an unknown group: %v", out)
	}
	// Junk lines get an answer when they carry an id and a parse error when they cannot.
	out, _ = e.stdio(e.key, []string{`not json`, `{"jsonrpc":"2.0","id":5,"method":"nope"}`})
	if len(out) != 2 {
		t.Errorf("junk: %v", out)
	}
}

func TestStdioProxyHandlesALongLineAndAFloodOfNotifications(t *testing.T) {
	e := newEnv(t, nil)
	long := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"project_create","arguments":{"title":"` + strings.Repeat("x", 300) + `","description":"` + strings.Repeat("é", 900000) + `"},` + modernMeta + `}}`
	lines := []string{long}
	for i := 0; i < 200; i++ {
		lines = append(lines, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"n":`+fmt.Sprint(i)+`}}`)
	}
	lines = append(lines, `{"jsonrpc":"2.0","id":10,"method":"ping"}`)
	out, _ := e.stdio(e.key, lines)
	m := byID(t, out)
	if m["9"] == nil || m["10"] == nil || len(m) != 2 {
		t.Errorf("answers: %d %.300v", len(m), out)
	}
}

// A line over the proxy's own cap is dropped with an error and the session goes on.
func TestStdioProxyDropsAnOversizedLine(t *testing.T) {
	e := newEnv(t, nil)
	huge := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", 9<<20) + `"}}`
	out, _ := e.stdio(e.key, []string{huge, `{"jsonrpc":"2.0","id":2,"method":"ping"}`})
	if len(out) != 2 || !strings.Contains(out[0], "too large") || !strings.Contains(out[1], `"id":2`) {
		t.Errorf("%.200v", out)
	}
}

// With nothing to reach, the answer is a JSON-RPC error, never a crash or silence.
func TestStdioProxyUnreachableServer(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	var o, errb bytes.Buffer
	vars := map[string]string{"SCRIOBH_URL": url, "SCRIOBH_API_KEY": "scr_x", "HOME": t.TempDir(), "SCRIOBH_CONFIG_DIR": t.TempDir()}
	rt := &cli.Runtime{Stdin: strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"), Stdout: &o, Stderr: &errb,
		Getenv: func(k string) string { return vars[k] }, IsTTY: func(io.Writer) bool { return false }, Now: time.Now}
	if code := cli.Main(rt, []string{"mcp"}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(o.String(), `"error"`) || !strings.Contains(o.String(), "Could not reach") || strings.Contains(o.String()+errb.String(), "scr_x") {
		t.Errorf("stdout %q stderr %q", o.String(), errb.String())
	}
	// No credentials: a usage failure on stderr and nothing on stdout.
	vars = map[string]string{"HOME": t.TempDir(), "SCRIOBH_CONFIG_DIR": t.TempDir()}
	o.Reset()
	errb.Reset()
	rt.Stdin = strings.NewReader("")
	if code := cli.Main(rt, []string{"mcp"}); code == 0 || o.Len() != 0 {
		t.Errorf("no credentials: exit %d stdout %q", code, o.String())
	}
}

// notifications/cancelled closes the in-flight request's connection (that is how the server hears of it) and is never forwarded.
func TestStdioProxyCancelClosesTheRequest(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	started := make(chan struct{})
	gone := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(b))
		mu.Unlock()
		if strings.Contains(string(b), `"id":5`) {
			close(started)
			select {
			case <-r.Context().Done():
				close(gone)
			case <-time.After(5 * time.Second):
			}
			return
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":6,"result":{}}`)
	}))
	defer ts.Close()
	pr, pw := io.Pipe()
	var o bytes.Buffer
	vars := map[string]string{"SCRIOBH_URL": ts.URL, "SCRIOBH_API_KEY": "scr_x", "HOME": t.TempDir(), "SCRIOBH_CONFIG_DIR": t.TempDir()}
	rt := &cli.Runtime{Stdin: pr, Stdout: &o, Stderr: io.Discard, Getenv: func(k string) string { return vars[k] }, IsTTY: func(io.Writer) bool { return false }, Now: time.Now}
	done := make(chan int, 1)
	go func() { done <- cli.Main(rt, []string{"mcp"}) }()
	fmt.Fprintln(pw, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"x"}}`)
	<-started
	fmt.Fprintln(pw, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`)
	select {
	case <-gone:
	case <-time.After(3 * time.Second):
		t.Fatal("the request was not closed")
	}
	fmt.Fprintln(pw, `{"jsonrpc":"2.0","id":6,"method":"ping"}`)
	pw.Close()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range seen {
		if strings.Contains(s, "notifications/cancelled") {
			t.Error("the cancel notification was forwarded")
		}
	}
	if strings.Contains(o.String(), `"id":5`) || !strings.Contains(o.String(), `"id":6`) {
		t.Errorf("a cancelled request must not be answered: %s", o.String())
	}
}

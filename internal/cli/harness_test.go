package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/server"
	"github.com/crueber/scriobh/internal/testutil"
)

// env is a running server and a place for the CLI to keep its files.
type env struct {
	t      *testing.T
	srv    *server.Server
	ts     *httptest.Server
	userID string
	key    string // a write-scope key of the writer
	config string
	cache  string
	// requests counts what reached the server, by "METHOD path".
	requests atomic.Int64
	paths    chan string
	home     string
}

func newEnv(t *testing.T, cfg map[string]string) *env {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // nothing a test does may reach the real home directory
	d := testutil.DB(t)
	s, err := server.New(server.Deps{Cfg: testutil.Config(t, cfg), DB: d})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, srv: s, config: t.TempDir(), cache: t.TempDir(), home: t.TempDir(), paths: make(chan string, 4096)}
	e.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.requests.Add(1)
		select {
		case e.paths <- r.Method + " " + r.URL.Path:
		default:
		}
		s.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(e.ts.Close)
	e.userID = testutil.User(t, d, ops.RoleWriter, "writer@test.local")
	e.key = e.mint(e.userID, ops.RoleWriter, `{"name":"test","scope":"write"}`)
	return e
}

// mint creates a key through the op, as that user, and returns its secret.
func (e *env) mint(uid, role, body string) string {
	e.t.Helper()
	raw, err := e.srv.Reg.CallJSON(context.Background(), ops.Principal{UserID: uid, Role: role}, "key.create", []byte(body))
	if err != nil {
		e.t.Fatal(err)
	}
	return raw.(keys.CreateOut).Secret
}

// seen returns and clears the requests recorded so far.
func (e *env) seen() []string {
	var out []string
	for {
		select {
		case p := <-e.paths:
			out = append(out, p)
		default:
			return out
		}
	}
}

// result is one CLI run.
type result struct {
	code   int
	stdout string
	stderr string
}

// envelope decodes the success envelope on stdout.
func (r result) envelope(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &m); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\nstdout: %s\nstderr: %s", err, r.stdout, r.stderr)
	}
	return m
}

func (r result) data(t *testing.T) map[string]any {
	t.Helper()
	d, _ := r.envelope(t)["data"].(map[string]any)
	if d == nil {
		t.Fatalf("no data in %s", r.stdout)
	}
	return d
}

// fail decodes the error object on stderr.
func (r result) fail(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(r.stderr), &m); err != nil {
		t.Fatalf("stderr is not one JSON object: %v\nstdout: %s\nstderr: %s", err, r.stdout, r.stderr)
	}
	e, _ := m["error"].(map[string]any)
	if e == nil || m["ok"] != false {
		t.Fatalf("not an error envelope: %s", r.stderr)
	}
	return e
}

// runWith runs the CLI against the server with a given key and stdin.
func (e *env) runWith(key, stdin string, args ...string) result {
	e.t.Helper()
	var out, errb bytes.Buffer
	vars := map[string]string{
		"SCRIOBH_CONFIG_DIR": e.config,
		"SCRIOBH_CACHE_DIR":  e.cache,
		"HOME":               e.home,
	}
	if e.ts != nil {
		vars["SCRIOBH_URL"] = e.ts.URL
	}
	if key != "" {
		vars["SCRIOBH_API_KEY"] = key
	}
	rt := &cli.Runtime{
		Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb,
		Getenv: func(k string) string { return vars[k] },
		IsTTY:  func(io.Writer) bool { return false },
		Now:    time.Now,
	}
	code := cli.Main(rt, args)
	return result{code, out.String(), errb.String()}
}

func (e *env) run(args ...string) result { return e.runWith(e.key, "", args...) }

// ok runs and requires exit 0.
func (e *env) ok(args ...string) result {
	e.t.Helper()
	r := e.run(args...)
	if r.code != 0 {
		e.t.Fatalf("scriobh-cli %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

// id digs data.item.id out of a result.
func itemID(t *testing.T, r result) string {
	t.Helper()
	item, _ := r.data(t)["item"].(map[string]any)
	id, _ := item["id"].(string)
	if id == "" {
		t.Fatalf("no item id in %s", r.stdout)
	}
	return id
}

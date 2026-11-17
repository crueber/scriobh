package cli_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
)

// fakeServer serves a made-up schema, to prove the CLI works with ops it has
// never heard of (the compendium ops arrive that way) and to count schema
// fetches.
type fakeServer struct {
	t      *testing.T
	ts     *httptest.Server
	mu     sync.Mutex
	schema cli.Schema
	hits   map[string]int
	notMod int
}

func thingSchema(hash string, extra ...cli.Op) cli.Schema {
	str := map[string]any{"type": "string"}
	ops := []cli.Op{
		{Name: "thing.list", Method: "GET", Path: "/api/v1/things", Scope: "read", Status: 200, Desc: "List things.",
			Input:  map[string]any{"type": "object", "properties": map[string]any{"cursor": map[string]any{"type": "string", "in": "query"}}},
			Output: map[string]any{"type": "object", "properties": map[string]any{"items": map[string]any{"type": "array"}, "next_cursor": str}}},
		{Name: "thing.make", Method: "POST", Path: "/api/v1/things", Scope: "write", Status: 201, Desc: "Make a thing.",
			Input: map[string]any{"type": "object", "required": []any{"name"}, "properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "What to call it."},
				"tags": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"size": map[string]any{"type": "integer"}}},
			Output: map[string]any{"type": "object"}},
	}
	return cli.Schema{Version: 1, Hash: hash, Ops: append(ops, extra...)}
}

func newFake(t *testing.T, s cli.Schema) *fakeServer {
	f := &fakeServer{t: t, schema: s, hits: map[string]int{}}
	f.ts = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.ts.Close)
	return f
}

func (f *fakeServer) setSchema(s cli.Schema) { f.mu.Lock(); f.schema = s; f.mu.Unlock() }

func (f *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.hits[r.Method+" "+r.URL.Path]++
	schema := f.schema
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer scr_fake" {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"error":{"code":"unauthenticated","message":"no"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/api/v1/_ops":
		etag := `"` + schema.Hash + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			f.mu.Lock()
			f.notMod++
			f.mu.Unlock()
			w.WriteHeader(304)
			return
		}
		_ = json.NewEncoder(w).Encode(schema)
	case r.URL.Path == "/api/v1/_whoami":
		fmt.Fprintf(w, `{"user":{"role":"writer"},"via":"apikey","schema_hash":%q}`, schema.Hash)
	case r.Method == "GET" && r.URL.Path == "/api/v1/things":
		var page int
		fmt.Sscanf(r.URL.Query().Get("cursor"), "p%d", &page)
		next := ""
		if page < 2 {
			next = fmt.Sprintf("p%d", page+1)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{map[string]any{"id": fmt.Sprintf("thing_%d", page)}}, "next_cursor": next})
	case r.Method == "POST" && r.URL.Path == "/api/v1/things":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"item": body, "idempotency_key": r.Header.Get("Idempotency-Key"), "prefer": r.Header.Get("Prefer")})
	default:
		w.WriteHeader(404)
		fmt.Fprint(w, `{"error":{"code":"not_found","message":"endpoint not found"}}`)
	}
}

func (f *fakeServer) runner(t *testing.T) *env {
	e := &env{t: t, config: t.TempDir(), cache: t.TempDir(), home: t.TempDir()}
	e.ts = f.ts
	return e
}

func (f *fakeServer) count(path string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.hits[path] }

// TestUnknownOpsBecomeCommands: the CLI has never heard of thing.make and still
// turns it into a command with typed flags, repeated list flags and the
// idempotency and dry-run headers.
func TestUnknownOpsBecomeCommands(t *testing.T) {
	f := newFake(t, thingSchema("sha256:one"))
	e := f.runner(t)
	r := e.runWith("scr_fake", "", "thing", "make", "--name", "Anvil", "--tags", "iron", "--tags", "heavy", "--size", "40")
	if r.code != 0 {
		t.Fatalf("exit %d: %s %s", r.code, r.stdout, r.stderr)
	}
	d := r.data(t)
	item := d["item"].(map[string]any)
	if item["name"] != "Anvil" || item["size"].(float64) != 40 || len(item["tags"].([]any)) != 2 {
		t.Errorf("body: %v", item)
	}
	if k, _ := d["idempotency_key"].(string); len(k) < 16 {
		t.Errorf("a write should carry a fresh Idempotency-Key, got %q", k)
	}
	r2 := e.runWith("scr_fake", "", "thing", "make", "--name", "Anvil")
	if d2 := r2.data(t); d2["idempotency_key"] == d["idempotency_key"] {
		t.Error("two invocations must not share a key")
	}
	r3 := e.runWith("scr_fake", "", "thing", "make", "--name", "Anvil", "--idempotency-key", "mine-1")
	if r3.data(t)["idempotency_key"] != "mine-1" {
		t.Errorf("--idempotency-key: %s", r3.stdout)
	}
	r4 := e.runWith("scr_fake", "", "thing", "make", "--name", "Anvil", "--no-idempotency")
	if r4.data(t)["idempotency_key"] != "" {
		t.Errorf("--no-idempotency: %s", r4.stdout)
	}
	r5 := e.runWith("scr_fake", "", "thing", "make", "--name", "Anvil", "--dry-run")
	if d5 := r5.data(t); d5["prefer"] != "dry-run" || d5["idempotency_key"] != "" {
		t.Errorf("a dry run asks to be previewed and carries no key: %v", d5)
	}
	// Required flag and types come from the schema.
	if r := e.runWith("scr_fake", "", "thing", "make"); r.code != cli.ExitUsage {
		t.Errorf("missing --name: exit %d", r.code)
	}
	if r := e.runWith("scr_fake", "", "thing", "make", "--name", "x", "--size", "big"); r.code != cli.ExitUsage {
		t.Errorf("bad --size: exit %d", r.code)
	}
}

func TestAllFollowsNextCursor(t *testing.T) {
	f := newFake(t, thingSchema("sha256:one"))
	e := f.runner(t)
	r := e.runWith("scr_fake", "", "thing", "list", "--all")
	if r.code != 0 {
		t.Fatal(r.stderr)
	}
	items := r.data(t)["items"].([]any)
	meta := r.envelope(t)["meta"].(map[string]any)
	if len(items) != 3 || meta["pages"].(float64) != 3 {
		t.Errorf("items %d pages %v: %s", len(items), meta["pages"], r.stdout)
	}
	if _, has := r.data(t)["next_cursor"]; has {
		t.Error("a complete listing has no next_cursor")
	}
	// One page without --all, and --max-pages caps the walk.
	if r := e.runWith("scr_fake", "", "thing", "list"); len(r.data(t)["items"].([]any)) != 1 || r.data(t)["next_cursor"] != "p1" {
		t.Errorf("without --all: %s", r.stdout)
	}
	if r := e.runWith("scr_fake", "", "thing", "list", "--all", "--max-pages", "2"); len(r.data(t)["items"].([]any)) != 2 {
		t.Errorf("--max-pages: %s", r.stdout)
	}
}

// TestSchemaCache: one fetch serves many commands; a stale copy is revalidated
// with its ETag; an unknown command refetches once; and with the server gone
// the saved copy, then the embedded snapshot, keeps the CLI usable.
func TestSchemaCache(t *testing.T) {
	f := newFake(t, thingSchema("sha256:one"))
	e := f.runner(t)
	run := func(args ...string) result { return e.runWith("scr_fake", "", args...) }
	run("thing", "list")
	run("thing", "list")
	run("thing", "list")
	if got := f.count("GET /api/v1/_ops"); got != 1 {
		t.Errorf("three commands fetched the schema %d times, want 1", got)
	}
	// The server grows a command: an unknown command refetches and then works.
	f.setSchema(thingSchema("sha256:two", cli.Op{Name: "gadget.build", Method: "POST", Path: "/api/v1/gadgets", Scope: "write", Status: 201, Desc: "Build.",
		Input: map[string]any{"type": "object", "properties": map[string]any{}}, Output: map[string]any{"type": "object"}}))
	r := run("gadget", "build")
	if r.code == cli.ExitUsage {
		t.Fatalf("a command added on the server should be found: %s", r.stderr)
	}
	if got := f.count("GET /api/v1/_ops"); got != 2 {
		t.Errorf("the unknown command should refetch once: %d fetches", got)
	}
	// --no-cache fetches in full, whatever is saved.
	before := f.count("GET /api/v1/_ops")
	run("thing", "list", "--no-cache")
	if f.count("GET /api/v1/_ops") != before+1 {
		t.Error("--no-cache should fetch the schema")
	}
	// With the server down, the saved schema still drives the CLI (and the
	// request itself then fails with unreachable).
	f.ts.Close()
	r = run("thing", "list", "--retries", "0", "--no-cache")
	if r.code != cli.ExitUnreachable {
		t.Errorf("exit %d: %s", r.code, r.stderr)
	}
	if r := run("ops", "--json"); r.code != 0 || r.data(t)["schema_hash"] == "" {
		t.Errorf("ops offline: %d %s", r.code, r.stderr)
	}
	if r := run("--skill", "compendium"); r.code != 0 || r.stdout == "" {
		t.Errorf("skill offline: %d %s", r.code, r.stderr)
	}
}

// TestConfigIsPrivate: login saves 0600 and never prints the key; config and
// errors do not leak it either.
func TestConfigIsPrivate(t *testing.T) {
	e := newEnv(t, nil)
	key := e.key
	r := e.runWith("", key+"\n", "login", "--url", e.ts.URL, "--key", "-")
	if r.code != 0 {
		t.Fatalf("login: %d %s %s", r.code, r.stdout, r.stderr)
	}
	for _, out := range []string{r.stdout, r.stderr} {
		if strings.Contains(out, key) {
			t.Fatalf("login printed the key: %s", out)
		}
	}
	st, err := os.Stat(filepath.Join(e.config, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		t.Errorf("config mode %o, want 0600", st.Mode().Perm())
	}
	// A bad key is refused and does not replace the good one.
	if r := e.runWith("", "", "login", "--url", e.ts.URL, "--key", "scr_"+strings.Repeat("z", 43)); r.code != cli.ExitAuth {
		t.Errorf("login with a bad key: %d", r.code)
	}
	// The saved login drives commands with no environment at all.
	bare := &env{t: t, config: e.config, cache: e.cache, home: e.home, ts: nil}
	cfg := bare.runWith("", "", "config")
	if cfg.code != 0 || strings.Contains(cfg.stdout, key) || cfg.data(t)["key_set"] != true || cfg.data(t)["key_from"] != "config file" {
		t.Errorf("config: %d %s", cfg.code, cfg.stdout)
	}
	if r := bare.runWith("", "", "whoami"); r.code != 0 || r.data(t)["via"] != "apikey" || strings.Contains(r.stdout, key) {
		t.Errorf("whoami from the saved login: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// logout removes the key.
	if r := bare.runWith("", "", "logout"); r.code != 0 {
		t.Fatal(r.stderr)
	}
	if r := bare.runWith("", "", "whoami"); r.code == 0 {
		t.Errorf("after logout whoami should fail")
	}
}

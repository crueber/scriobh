package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/mcp"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Path arguments that the router would clean away, or that carry a separator, an
// escape, a query or a fragment, are refused before anything is sent. (A "." or ".."
// used to pass the id check; the router answered a redirect for another route and the
// tool reported success for a write that never ran.)
func TestPathArgumentsThatAreNotIds(t *testing.T) {
	inv := &fakeInvoker{}
	s := newTestServer(t, inv, nil)
	for _, id := range []string{".", "..", "...", "....", "", "a/b", `a\b`, "a\x00b", "a%2fb", "a%2Fb", "a?b", "a#b", "../x", "a b", "a\nb", "%2e%2e", "a;b"} {
		for _, tool := range []string{"doc_get", "doc_update"} {
			jid, _ := json.Marshal(id)
			args := fmt.Sprintf(`{"id":%s,"base_revision":1}`, jid)
			if tool == "doc_get" {
				args = fmt.Sprintf(`{"id":%s}`, jid)
			}
			res, m := call(t, s, writeKey, tool, args)
			if res == nil || res["isError"] != true {
				t.Errorf("%s with the id %q was not refused: %v", tool, id, m)
			}
		}
	}
	if inv.saw.Load() != 0 {
		t.Errorf("%d refused ids reached the invoker", inv.saw.Load())
	}
	// Real ids, including ones with dots inside, still work.
	for _, id := range []string{"doc_01J8", "a.b", "v1.2", "x-y_z~w", "a:b"} {
		if res, m := call(t, s, writeKey, "doc_get", fmt.Sprintf(`{"id":%q}`, id)); res == nil || res["isError"] == true {
			t.Errorf("%q was refused: %v", id, m)
		}
	}
}

// Anything that is not a 2xx is an error result, a redirect included.
func TestOnlyA2xxIsSuccess(t *testing.T) {
	inv := &fakeInvoker{}
	s := newTestServer(t, inv, nil)
	for _, st := range []int{100, 301, 302, 304, 307, 399, 400, 404, 500, 0} {
		inv.resp = func(mcp.Request) mcp.Response {
			return mcp.Response{Status: st, ContentType: "text/html", Body: []byte("<a href=\"/x\">Found</a>")}
		}
		res, _ := call(t, s, writeKey, "doc_update", `{"id":"doc_1","base_revision":1}`)
		if res["isError"] != true {
			t.Errorf("status %d was reported as success", st)
		}
	}
	for _, st := range []int{200, 201, 202, 204} {
		inv.resp = func(mcp.Request) mcp.Response {
			return mcp.Response{Status: st, ContentType: "application/json", Body: []byte(`{}`)}
		}
		if res, _ := call(t, s, writeKey, "doc_update", `{"id":"doc_1","base_revision":1}`); res["isError"] == true {
			t.Errorf("status %d was reported as an error", st)
		}
	}
}

func permanentSchema() ops.Schema {
	sc := smallSchema()
	str := map[string]any{"type": "string", "in": "path"}
	sc.Ops = append(sc.Ops,
		ops.OpSchema{Name: "thing.delete", Method: "DELETE", Path: "/api/v1/things/{id}", Desc: "Delete a thing for good.", Scope: "write", Permanent: true,
			Auth: ops.Auth{Kind: ops.AuthProject, Role: "editor"}, Input: schemaOf(map[string]any{"id": str}, "id")},
		ops.OpSchema{Name: "thing.merge", Method: "POST", Path: "/api/v1/things/{id}/merge", Desc: "Merge a thing away.", Scope: "write", Permanent: true,
			Auth: ops.Auth{Kind: ops.AuthCompendium, Role: "editor"}, Input: schemaOf(map[string]any{"id": str}, "id")})
	return sc
}

func permanentServer(t *testing.T, flag bool) *mcp.Server {
	mcp.ExposeGroupForTest(t, "thing")
	cat, err := mcp.BuildCatalog(permanentSchema())
	if err != nil {
		t.Fatal(err)
	}
	return mcp.New(mcp.Config{PermanentTools: flag, AdminTools: true}, cat, &fakeInvoker{})
}

// The permanent tools: off unless the server allows them, the connection asks and the key can write.
func TestPermanentToolsAreOptIn(t *testing.T) {
	names := func(s *mcp.Server, id mcp.Identity, f mcp.Filters) string {
		var out []string
		for _, tl := range s.VisibleTools(id, f) {
			if tl.Permanent {
				out = append(out, tl.Name)
			}
		}
		return strings.Join(out, ",")
	}
	off, on := permanentServer(t, false), permanentServer(t, true)
	write, read, admin := mcp.Identity{KeyID: "k", Scope: "write"}, mcp.Identity{KeyID: "k", Scope: "read"}, mcp.Identity{KeyID: "k", Scope: "admin"}
	ask := mcp.Filters{Permanent: true}
	cases := []struct {
		name string
		s    *mcp.Server
		id   mcp.Identity
		f    mcp.Filters
		want string
	}{
		{"default", off, write, mcp.Filters{}, ""},
		{"asked but the server did not allow", off, write, ask, ""},
		{"allowed but not asked", on, write, mcp.Filters{}, ""},
		{"allowed, asked, read key", on, read, ask, ""},
		{"allowed, asked, write key", on, write, ask, "thing_delete,thing_merge"},
		{"allowed, asked, admin key", on, admin, ask, "thing_delete,thing_merge"},
		{"allowed, asked, pinned to a project", on, mcp.Identity{KeyID: "k", Scope: "write", ProjectID: "prj_1"}, ask, "thing_delete,thing_merge"},
		{"allowed, asked, pinned to a compendium", on, mcp.Identity{KeyID: "k", Scope: "write", CompendiumID: "cmp_1"}, ask, "thing_merge"},
		{"allowed, asked, but read-only requested", on, write, mcp.Filters{Permanent: true, ReadOnly: true}, ""},
	}
	for _, c := range cases {
		if got := names(c.s, c.id, c.f); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// A hidden permanent tool cannot be called.
	if _, m := call(t, off, write, "thing_delete", `{"id":"x1"}`); m["error"] == nil {
		t.Error("a permanent tool was called on a server that does not allow them")
	}
	if _, m := call(t, on, write, "thing_delete", `{"id":"x1"}`); m["error"] == nil {
		t.Error("a permanent tool was called on a connection that did not ask for them")
	}
	// What they say about themselves.
	tl, _ := on.Catalog().Lookup("thing_delete")
	if !tl.Permanent || !tl.Annotations.Destructive || !strings.Contains(tl.Description, "PERMANENT") {
		t.Errorf("%+v", tl)
	}
	// A cursor made without the permission is not good with it.
	if tl2, _ := on.Catalog().Lookup("thing_merge"); !tl2.Annotations.Destructive {
		t.Error("a merge that discards its source is not marked destructive")
	}
}

// What the server says it does not do is true of the catalog.
func TestTheCatalogKeepsItsPromises(t *testing.T) {
	cat, schema := liveCatalog(t)
	for _, o := range schema.Ops {
		tl, isTool := cat.Lookup(mcp.ToolName(o.Name))
		switch {
		case o.Permanent && isTool && !tl.Permanent:
			t.Errorf("%s is permanent and its tool is not marked", o.Name)
		case o.Permanent && isTool && !tl.Annotations.Destructive:
			t.Errorf("%s is permanent and its tool is not destructive", o.Name)
		case strings.HasSuffix(o.Name, ".purge") && isTool:
			t.Errorf("%s purges a whole project or Compendium and is a tool", o.Name)
		}
	}
	for _, n := range []string{"stash_delete", "revision_prune", "entity_merge", "comment_delete", "entity_field_remove", "project_tag_merge", "project_attachment_delete"} {
		tl, ok := cat.Lookup(n)
		if !ok || !tl.Permanent {
			t.Errorf("%s should be a permanent tool", n)
		}
	}
	// The author's contact block is personal data: no tool holds it or writes it into a file it returns.
	for _, n := range []string{"compile_contact_get", "compile_contact_set", "compile_preview", "export_compile", "export_submission"} {
		if _, ok := cat.Lookup(n); ok {
			t.Errorf("%s is a tool", n)
		}
	}
	found := 0
	for _, e := range cat.Excluded {
		if strings.HasPrefix(e.Op, "compile.contact.") || e.Op == "compile.preview" || e.Op == "export.compile" || e.Op == "export.submission" {
			found++
			if !strings.Contains(e.Reason, "contact block") {
				t.Errorf("%s: reason %q", e.Op, e.Reason)
			}
		}
	}
	if found != 5 {
		t.Errorf("%d of the 5 contact-block ops are excluded with a reason", found)
	}
}

// A batch has one budget; what is not reached is answered, with its own id, and a notification is not answered at all.
func TestBatchHasOneDeadlineAndRefusalsCarryIDs(t *testing.T) {
	inv := &fakeInvoker{block: make(chan struct{})}
	s := newTestServer(t, inv, func(c *mcp.Config) { c.CallTimeout = 80 * time.Millisecond })
	callMsg := func(id int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"doc_get","arguments":{"id":"doc_1"}}}`, id)
	}
	start := time.Now()
	st, _, raw := send(t, s, writeKey, "["+callMsg(11)+`,{"jsonrpc":"2.0","method":"notifications/x"},`+callMsg(12)+","+callMsg(13)+"]", nil)
	took := time.Since(start)
	if st != 200 {
		t.Fatalf("status %d after %s: %s", st, took, raw)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 600*time.Millisecond {
			t.Errorf("the batch took %s: it has one budget", took)
		}
	})
	body := string(raw)
	for _, id := range []string{`"id":11`, `"id":12`, `"id":13`} {
		if !strings.Contains(body, id) {
			t.Errorf("no answer carries %s: %s", id, body)
		}
	}
	if strings.Count(body, "used up its time") != 2 {
		t.Errorf("the two messages after the budget should be refused: %s", body)
	}
	if strings.Contains(body, `"id":null`) {
		t.Errorf("a refusal lost its id: %s", body)
	}
	if inv.saw.Load() != 1 {
		t.Errorf("%d calls ran; the budget allows the first only", inv.saw.Load())
	}
	// Rate-limit refusals carry ids too, and skip notifications.
	s2 := newTestServer(t, &fakeInvoker{}, func(c *mcp.Config) {
		c.Allow = func(context.Context, mcp.Identity) (bool, int) { return false, 3 }
	})
	ping := func(id int) string { return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, id) }
	_, _, raw = send(t, s2, writeKey, "["+ping(1)+","+ping(2)+`,{"jsonrpc":"2.0","method":"notifications/y"},`+ping(3)+"]", nil)
	body = string(raw)
	if strings.Count(body, "Rate limited") != 2 || !strings.Contains(body, `"id":2`) || !strings.Contains(body, `"id":3`) || strings.Contains(body, `"id":null`) {
		t.Errorf("rate-limit refusals: %s", body)
	}
}

// Two clients that share a key and a request id do not cancel each other's calls.
func TestCancelIsMatchedToTheCallersAddress(t *testing.T) {
	inv := &fakeInvoker{block: make(chan struct{})}
	s := newTestServer(t, inv, nil)
	var done atomic.Bool
	go func() {
		s.Handle(context.Background(), mcp.Input{Client: "198.51.100.1", ID: writeKey, Body: []byte(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"doc_get","arguments":{"id":"doc_1"}}}`)})
		done.Store(true)
	}()
	for i := 0; i < 200 && inv.saw.Load() == 0; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	s.Handle(context.Background(), mcp.Input{Client: "203.0.113.9", ID: writeKey, Body: []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`)})
	time.Sleep(80 * time.Millisecond)
	if done.Load() {
		t.Fatal("another address cancelled the call")
	}
	s.Handle(context.Background(), mcp.Input{Client: "198.51.100.1", ID: writeKey, Body: []byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":5}}`)})
	for i := 0; i < 200 && !done.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !done.Load() {
		t.Fatal("the caller's own cancel did not work")
	}
}

// A resource too large for the cap says so; it is not "not found".
func TestAnOversizeResourceSaysTooLarge(t *testing.T) {
	inv := &fakeInvoker{resp: func(r mcp.Request) mcp.Response {
		return mcp.Response{Status: 200, ContentType: "application/json", Truncated: true, Body: []byte("x")}
	}}
	s := newTestServer(t, inv, nil)
	_, m, raw := send(t, s, writeKey, `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"scriobh://project/prj_1/doc/doc_1"}}`, nil)
	code, msg := errOf(m)
	if code != mcp.CodeInvalidParams || !strings.Contains(msg, "larger") || strings.Contains(msg, "not found") {
		t.Errorf("%s", raw)
	}
}

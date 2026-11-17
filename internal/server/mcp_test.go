package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/mcp"
	"github.com/crueber/scriobh/internal/ops"
)

// rpc is a JSON-RPC client for the tests: it posts to /mcp with a key and
// decodes the answer.
type rpcResult struct {
	status int
	header http.Header
	raw    []byte
	msg    struct {
		ID     json.RawMessage `json:"id"`
		Result map[string]any  `json:"result"`
		Error  *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
}

func (s *Server) postMCP(secret, body string, hdr ...string) *httptest.ResponseRecorder {
	h := []string{"Content-Type", "application/json", "Accept", "application/json, text/event-stream"}
	h = append(h, hdr...)
	return s.doKey(secret, "POST", "/mcp", body, h...)
}

// modernCall posts a modern-era request, with the headers the protocol requires.
func (s *Server) modernCall(t testing.TB, secret, method string, params map[string]any, extra ...string) rpcResult {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    mcp.VersionModern,
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	hdr := []string{"MCP-Protocol-Version", mcp.VersionModern, "Mcp-Method", method}
	if n, ok := params["name"].(string); ok && (method == "tools/call" || method == "prompts/get") {
		hdr = append(hdr, "Mcp-Name", n)
	}
	if u, ok := params["uri"].(string); ok && method == "resources/read" {
		hdr = append(hdr, "Mcp-Name", u)
	}
	hdr = append(hdr, extra...)
	return decodeRPC(t, s.postMCP(secret, string(b), hdr...))
}

func decodeRPC(t testing.TB, rec *httptest.ResponseRecorder) rpcResult {
	t.Helper()
	r := rpcResult{status: rec.Code, header: rec.Header(), raw: rec.Body.Bytes()}
	if len(r.raw) > 0 {
		if err := json.Unmarshal(r.raw, &r.msg); err != nil {
			t.Fatalf("not JSON-RPC (%d): %s", rec.Code, rec.Body)
		}
	}
	return r
}

// toolText is the first text block of a tool result.
func toolText(res map[string]any) string {
	c, _ := res["content"].([]any)
	if len(c) == 0 {
		return ""
	}
	m, _ := c[0].(map[string]any)
	s, _ := m["text"].(string)
	return s
}

func toolNames(t testing.TB, res map[string]any) []string {
	t.Helper()
	var out []string
	for _, x := range res["tools"].([]any) {
		out = append(out, x.(map[string]any)["name"].(string))
	}
	return out
}

// allToolNames follows tools/list to its end. query is appended to the address (?groups=...).
func (s *Server) allToolNames(t testing.TB, secret, query string, extra ...string) []string {
	t.Helper()
	var names []string
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    mcp.VersionModern,
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": params})
		hdr := append([]string{"MCP-Protocol-Version", mcp.VersionModern, "Mcp-Method", "tools/list"}, extra...)
		path := "/mcp"
		if query != "" {
			path += "?" + query
		}
		h := append([]string{"Content-Type", "application/json"}, hdr...)
		r := decodeRPC(t, s.doKey(secret, "POST", path, string(b), h...))
		if r.status != 200 {
			t.Fatalf("tools/list: %d %s", r.status, r.raw)
		}
		names = append(names, toolNames(t, r.msg.Result)...)
		next, _ := r.msg.Result["nextCursor"].(string)
		if next == "" {
			return names
		}
		cursor = next
	}
	t.Fatal("tools/list never ended")
	return nil
}

func has(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestMCPSmoke(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"write"}`)

	// The modern era: no handshake, the version rides on the request.
	r := s.modernCall(t, secret, "server/discover", nil)
	if r.status != 200 || r.msg.Result["resultType"] != "complete" {
		t.Fatalf("discover: %d %s", r.status, r.raw)
	}
	names := s.allToolNames(t, secret, "")
	if !has(names, "doc_update") || !has(names, "project_list") {
		t.Fatalf("tool list lacks the basics: %v", names[:5])
	}
	for _, banned := range []string{"key_create", "ai_run", "git_push", "share_create", "project_purge", "webhook_create"} {
		if has(names, banned) {
			t.Errorf("%s is offered", banned)
		}
	}

	// Create a project through a tool, preview first.
	r = s.modernCall(t, secret, "tools/call", map[string]any{"name": "project_create", "arguments": map[string]any{"title": "Via MCP", "dry_run": true}})
	if r.status != 200 || r.msg.Result["isError"] == true || !strings.Contains(toolText(r.msg.Result), `"dry_run":true`) {
		t.Fatalf("dry run: %d %s", r.status, r.raw)
	}
	r = s.modernCall(t, secret, "tools/call", map[string]any{"name": "project_create", "arguments": map[string]any{"title": "Via MCP", "idempotency_key": "k1"}})
	if r.msg.Result["isError"] == true {
		t.Fatalf("create: %s", r.raw)
	}
	var created struct {
		Item struct{ ID string } `json:"item"`
	}
	_ = json.Unmarshal([]byte(toolText(r.msg.Result)), &created)
	if created.Item.ID == "" {
		t.Fatalf("no project id: %s", r.raw)
	}
	// The same key and arguments replay, they do not write twice.
	r2 := s.modernCall(t, secret, "tools/call", map[string]any{"name": "project_create", "arguments": map[string]any{"title": "Via MCP", "idempotency_key": "k1"}})
	if toolText(r2.msg.Result) != toolText(r.msg.Result) {
		t.Errorf("a replayed key should return the first result:\n%s\n%s", toolText(r.msg.Result), toolText(r2.msg.Result))
	}

	// Resources, as the key sees them.
	r = s.modernCall(t, secret, "resources/list", nil)
	if r.status != 200 || !strings.Contains(string(r.raw), created.Item.ID) {
		t.Fatalf("resources/list: %d %s", r.status, r.raw)
	}
	r = s.modernCall(t, secret, "resources/read", map[string]any{"uri": "scriobh://project/" + created.Item.ID + "/outline"})
	if r.status != 200 || !strings.Contains(string(r.raw), "Outline of Via MCP") {
		t.Fatalf("outline: %d %s", r.status, r.raw)
	}

	// The legacy era: initialize, then calls without _meta.
	rec := s.postMCP(secret, `{"jsonrpc":"2.0","id":7,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	lr := decodeRPC(t, rec)
	if lr.status != 200 || lr.msg.Result["protocolVersion"] != "2025-06-18" || rec.Header().Get("Mcp-Session-Id") != "" {
		t.Fatalf("initialize: %d %s (session %q)", lr.status, lr.raw, rec.Header().Get("Mcp-Session-Id"))
	}
	rec = s.postMCP(secret, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "MCP-Protocol-Version", "2025-06-18")
	if rec.Code != 202 || rec.Body.Len() != 0 {
		t.Errorf("a notification is 202 with no body: %d %q", rec.Code, rec.Body)
	}
	rec = s.postMCP(secret, `{"jsonrpc":"2.0","id":8,"method":"tools/list"}`, "MCP-Protocol-Version", "2025-06-18")
	lr = decodeRPC(t, rec)
	if lr.status != 200 || lr.msg.Result["resultType"] != nil || len(toolNames(t, lr.msg.Result)) != 100 {
		t.Fatalf("legacy tools/list: %d %.300s", lr.status, lr.raw)
	}
}

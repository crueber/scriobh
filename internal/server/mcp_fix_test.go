package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// A "." or ".." as an id used to be reported as a success for a write that never ran.
func TestMCPDotIDsAreRefusedThroughTheRealServer(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"write"}`)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"doc_update", map[string]any{"id": ".", "base_revision": 1, "content": "hello"}},
		{"doc_trash", map[string]any{"id": ".."}},
		{"doc_get", map[string]any{"id": ".."}},
		{"project_update", map[string]any{"id": "..", "title": "x"}},
		{"project_get", map[string]any{"id": "..."}},
	} {
		r := s.modernCall(t, secret, "tools/call", map[string]any{"name": tc.tool, "arguments": tc.args})
		if r.msg.Result["isError"] != true {
			t.Errorf("%s with a dot id: %s", tc.tool, r.raw)
		}
	}
	for _, u := range []string{"scriobh://project/../outline", "scriobh://project/./outline", "scriobh://project/../../x/outline"} {
		if r := s.modernCall(t, secret, "resources/read", map[string]any{"uri": u}); strings.Contains(string(r.raw), `"contents"`) {
			t.Errorf("%s returned contents", u)
		}
	}
}

// The permanent tools through the real server: the instance flag, the connection's ask, the key's scope.
func TestMCPPermanentToolsThroughTheRealServer(t *testing.T) {
	for _, flag := range []bool{false, true} {
		env := map[string]string{}
		if flag {
			env["SCRIOBH_MCP_PERMANENT_TOOLS"] = "1"
		}
		s := newMCPServer(t, env)
		w := s.newCaller(t, "writer", ops.RoleWriter)
		write, _ := s.mintKey(t, w, `{"name":"w","scope":"write"}`)
		read, _ := s.mintKey(t, w, `{"name":"r","scope":"read"}`)
		for _, c := range []struct {
			key, query string
			want       bool
		}{
			{write, "", false}, {write, "permanent=1", flag}, {read, "permanent=1", false}, {write, "permanent=1&groups=doc", false},
		} {
			names := s.allToolNames(t, c.key, c.query)
			got := has(names, "stash_delete") && has(names, "revision_prune") && has(names, "entity_merge")
			if c.query == "permanent=1&groups=doc" {
				got = has(names, "stash_delete") // filtered out by group
			}
			if got != c.want {
				t.Errorf("flag=%v query=%q: permanent tools offered=%v, want %v", flag, c.query, got, c.want)
			}
		}
		for _, n := range s.allToolNames(t, write, "permanent=1") {
			if strings.HasSuffix(n, "_purge") {
				t.Errorf("%s is offered", n)
			}
		}
		rec := s.mcpReq(write, "POST", "/mcp?permanent=maybe", listBody, "Content-Type", "application/json")
		if rec.Code != 400 {
			t.Errorf("permanent=maybe: %d", rec.Code)
		}
	}
}

// Browser MCP clients at a listed origin: the preflight, the echo, and nothing for anyone else.
func TestMCPCORS(t *testing.T) {
	s := newMCPServer(t, map[string]string{"SCRIOBH_BASE_URL": "https://write.example.com", "SCRIOBH_MCP_ALLOWED_ORIGINS": "https://app.example.org"})
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"read"}`)
	pre := func(origin string) (int, map[string][]string) {
		hdr := []string{"Access-Control-Request-Method", "POST", "Access-Control-Request-Headers", "authorization,content-type,mcp-protocol-version"}
		if origin != "" {
			hdr = append(hdr, "Origin", origin)
		}
		rec := s.mcpReq("", "OPTIONS", "/mcp", "", hdr...)
		return rec.Code, rec.Header()
	}
	code, h := pre("https://app.example.org")
	if code != 204 || httpHeader(h).Get("Access-Control-Allow-Origin") == "" {
		t.Fatalf("preflight: %d %v", code, h)
	}
	hh := httpHeader(h)
	if hh.Get("Access-Control-Allow-Origin") != "https://app.example.org" || !strings.Contains(hh.Get("Vary"), "Origin") ||
		hh.Get("Access-Control-Allow-Credentials") != "" || hh.Get("Access-Control-Allow-Origin") == "*" ||
		!strings.Contains(strings.ToLower(hh.Get("Access-Control-Allow-Headers")), "authorization") || !strings.Contains(strings.ToLower(hh.Get("Access-Control-Allow-Headers")), "mcp-protocol-version") ||
		hh.Get("Access-Control-Allow-Methods") != "POST, OPTIONS" || hh.Get("Access-Control-Max-Age") != "300" {
		t.Errorf("preflight headers: %v", h)
	}
	// The instance's own origin works too; a stranger's does not, and learns nothing.
	if code, _ := pre("https://write.example.com"); code != 204 {
		t.Errorf("own origin: %d", code)
	}
	code, h = pre("https://evil.example")
	if code != 403 || httpHeader(h).Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("a stranger's preflight: %d %v", code, h)
	}
	if code, _ := pre(""); code != 405 {
		t.Errorf("OPTIONS without an Origin: %d", code)
	}
	// The call itself, and its refusals, name the origin that may read them.
	rec := s.mcpReq(secret, "POST", "/mcp", listBody, "Content-Type", "application/json", "Origin", "https://app.example.org")
	if rec.Code != 200 || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.org" || !strings.Contains(rec.Header().Get("Vary"), "Origin") {
		t.Errorf("the call: %d %v", rec.Code, rec.Header())
	}
	rec = s.mcpReq("scr_wrong", "POST", "/mcp", listBody, "Content-Type", "application/json", "Origin", "https://app.example.org")
	if rec.Code != 401 || rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.org" {
		t.Errorf("a refused key must still be readable by the page: %d %v", rec.Code, rec.Header())
	}
	rec = s.mcpReq(secret, "POST", "/mcp", listBody, "Content-Type", "application/json", "Origin", "https://evil.example")
	if rec.Code != 403 || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("a stranger's call: %d %v", rec.Code, rec.Header())
	}
	// No CORS on anything but /mcp.
	rec = s.mcpReq("", "OPTIONS", "/api/v1/projects", "", "Origin", "https://app.example.org", "Access-Control-Request-Method", "GET")
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("CORS on /api: %v", rec.Header())
	}
}

func httpHeader(h map[string][]string) headerGetter { return headerGetter(h) }

type headerGetter map[string][]string

func (h headerGetter) Get(k string) string {
	for name, v := range h {
		if strings.EqualFold(name, k) && len(v) > 0 {
			return strings.Join(v, ", ")
		}
	}
	return ""
}

var _ = json.Marshal

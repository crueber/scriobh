package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func newMCPServer(t *testing.T, env map[string]string) *Server {
	t.Helper()
	cfg := map[string]string{"SCRIOBH_RATE_LIMIT": "0"}
	for k, v := range env {
		cfg[k] = v
	}
	s, err := New(Deps{Cfg: testutil.Config(t, cfg), DB: testutil.DB(t)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (s *Server) mcpReq(secret, method, path, body string, hdr ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const listBody = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`

// The transport rules of the endpoint: methods, content types, sessions, origins, keys, sizes.
func TestMCPTransport(t *testing.T) {
	s := newMCPServer(t, nil)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"read"}`)
	json := "Content-Type"

	// Only POST. GET would open the legacy stream and DELETE would end a session; neither exists.
	for _, m := range []string{"GET", "DELETE", "PUT", "PATCH", "HEAD"} {
		rec := s.mcpReq(secret, m, "/mcp", "")
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s /mcp: %d allow=%q", m, rec.Code, rec.Header().Get("Allow"))
		}
	}
	// No key: 401 as JSON-RPC, with the challenge. A bad key: the same.
	for name, sec := range map[string]string{"no key": "", "bad key": "scr_nonsense", "wrong prefix": "hello"} {
		rec := s.mcpReq(sec, "POST", "/mcp", listBody, json, "application/json")
		var m map[string]any
		if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") || jsonErr(rec.Body.Bytes(), &m) != nil || m["jsonrpc"] != "2.0" || m["error"] == nil {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// A browser session cookie is not a credential here.
	if rec := s.do(w, "POST", "/mcp", listBody); rec.Code != 401 {
		t.Errorf("cookie: %d", rec.Code)
	}
	// Content type must be JSON.
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/jsonx", "multipart/form-data; boundary=x"} {
		rec := s.mcpReq(secret, "POST", "/mcp", listBody, json, ct)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q: %d", ct, rec.Code)
		}
	}
	if rec := s.mcpReq(secret, "POST", "/mcp", listBody, json, "application/json; charset=utf-8"); rec.Code != 200 {
		t.Errorf("charset parameter: %d %s", rec.Code, rec.Body)
	}
	// Session ids are never minted, and one that arrives is ignored.
	rec := s.mcpReq(secret, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`, json, "application/json", "Mcp-Session-Id", "abc", "Last-Event-ID", "5")
	if rec.Code != 200 || rec.Header().Get("Mcp-Session-Id") != "" {
		t.Errorf("session: %d %q", rec.Code, rec.Header().Get("Mcp-Session-Id"))
	}
	// The response is one JSON document, never a stream, and not cacheable.
	rec = s.mcpReq(secret, "POST", "/mcp", listBody, json, "application/json", "Accept", "text/event-stream")
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}
	// A notification: 202, no body.
	rec = s.mcpReq(secret, "POST", "/mcp", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, json, "application/json")
	if rec.Code != 202 || rec.Body.Len() != 0 {
		t.Errorf("notification: %d %q", rec.Code, rec.Body)
	}
	// An oversized body is 413 before it is parsed.
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", 3<<20) + `"}}`
	if rec := s.mcpReq(secret, "POST", "/mcp", big, json, "application/json"); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("3 MB body: %d", rec.Code)
	}
	// A body that fails midway is a 400, not a hang or a panic.
	req := httptest.NewRequest("POST", "/mcp", io.MultiReader(strings.NewReader(`{"jsonrpc":"2.0",`), errReader{}))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set(json, "application/json")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 400 {
		t.Errorf("a broken body: %d", rr.Code)
	}
	// Filters: groups and readonly in the address.
	for q, want := range map[string]int{"groups=doc": 200, "groups=core,bible": 200, "groups=nope": 400, "readonly=1": 200, "readonly=maybe": 400, "groups=all": 200, "groups=" + strings.Repeat("a", 400): 400} {
		rec := s.mcpReq(secret, "POST", "/mcp?"+q, listBody, json, "application/json")
		if rec.Code != want {
			t.Errorf("?%.40s: %d, want %d (%.100s)", q, rec.Code, want, rec.Body)
		}
	}
	// The security headers of every other route are on this one too.
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff is missing")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func jsonErr(b []byte, v any) error { return json.Unmarshal(b, v) }

// DNS rebinding: a browser's cross-origin request is refused before anything is read, a request without an Origin is judged by its key alone.
func TestMCPOrigin(t *testing.T) {
	s := newMCPServer(t, map[string]string{"SCRIOBH_BASE_URL": "https://write.example.com", "SCRIOBH_MCP_ALLOWED_ORIGINS": "https://app.example.org, http://localhost:3000"})
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"read"}`)
	cases := map[string]int{
		"": 200, "https://write.example.com": 200, "HTTPS://WRITE.EXAMPLE.COM": 200, "https://write.example.com:443": 200, "https://write.example.com:8443": 403,
		"https://app.example.org": 200, "http://localhost:3000": 200,
		"https://evil.example": 403, "http://write.example.com": 403, "https://write.example.com.evil.example": 403,
		"null": 403, "file://": 403, "https://app.example.org:8443": 403, "http://localhost:3001": 403, "garbage": 403, "https://user@write.example.com": 403,
		"https://write.example.com/path": 403, "http://127.0.0.1:8080": 403,
	}
	for origin, want := range cases {
		hdr := []string{"Content-Type", "application/json"}
		if origin != "" {
			hdr = append(hdr, "Origin", origin)
		}
		rec := s.mcpReq(secret, "POST", "/mcp", listBody, hdr...)
		if rec.Code != want {
			t.Errorf("Origin %q: %d, want %d", origin, rec.Code, want)
		}
		if want == 403 && !strings.Contains(rec.Body.String(), `"jsonrpc":"2.0"`) {
			t.Errorf("Origin %q: the refusal is not JSON-RPC: %s", origin, rec.Body)
		}
	}
	// Two Origin headers are refused outright.
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader(listBody))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Origin", "https://write.example.com")
	req.Header.Add("Origin", "https://write.example.com")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("two origins: %d", rec.Code)
	}
	// The pages are another matter: an origin that may call /mcp is not thereby trusted with the app's own forms.
	rec = s.mcpReq("", "POST", "/ui/csrf", "", "Origin", "https://app.example.org", "Sec-Fetch-Site", "cross-site")
	if rec.Code != 403 {
		t.Errorf("an MCP origin must not be trusted with the pages: %d", rec.Code)
	}
}

func TestMCPCanBeSwitchedOff(t *testing.T) {
	s := newMCPServer(t, map[string]string{"SCRIOBH_MCP": "off"})
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"read"}`)
	if rec := s.mcpReq(secret, "POST", "/mcp", listBody, "Content-Type", "application/json"); rec.Code != 404 {
		t.Errorf("MCP off: %d", rec.Code)
	}
	if s.MCPCatalog() != nil {
		t.Error("a catalog exists with MCP off")
	}
}

func TestMCPRateLimit(t *testing.T) {
	s := newMCPServer(t, map[string]string{"SCRIOBH_RATE_LIMIT": "60"})
	w := s.newCaller(t, "writer", ops.RoleWriter)
	secret, _ := s.mintKey(t, w, `{"name":"agent","scope":"read"}`)
	limited := 0
	for i := 0; i < 80; i++ {
		rec := s.mcpReq(secret, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"ping"}`, "Content-Type", "application/json")
		if rec.Code == 429 {
			limited++
			if rec.Header().Get("Retry-After") == "" || !strings.Contains(rec.Body.String(), `"jsonrpc":"2.0"`) {
				t.Fatalf("429 without Retry-After or JSON-RPC body: %v %s", rec.Header(), rec.Body)
			}
		}
	}
	if limited == 0 {
		t.Error("80 calls in a burst were never limited")
	}
	// A batch pays for each message.
	s2 := newMCPServer(t, map[string]string{"SCRIOBH_RATE_LIMIT": "40"})
	w2 := s2.newCaller(t, "writer", ops.RoleWriter)
	sec2, _ := s2.mintKey(t, w2, `{"name":"agent","scope":"read"}`)
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	batch := "[" + strings.Repeat(ping+",", 15) + ping + "]"
	refused := 0
	for i := 0; i < 4; i++ {
		rec := s2.mcpReq(sec2, "POST", "/mcp", batch, "Content-Type", "application/json")
		refused += strings.Count(rec.Body.String(), "Rate limited")
	}
	if refused == 0 {
		t.Error("batches were not charged per message")
	}
}

// What a key offers is its scope and pin and the server's switch, and a hidden tool is a refused one.
func TestMCPAdminToolsNeedTheFlagAndTheScope(t *testing.T) {
	for _, flag := range []bool{false, true} {
		env := map[string]string{}
		if flag {
			env["SCRIOBH_MCP_ADMIN_TOOLS"] = "1"
		}
		s := newMCPServer(t, env)
		admin := s.newCaller(t, "admin", ops.RoleAdmin)
		owner := s.newCaller(t, "owner", ops.RoleWriter)
		pid := func() string {
			out, err := s.Reg.Call(t.Context(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Admin tools"})
			if err != nil {
				t.Fatal(err)
			}
			return out.(projects.Item).Item.ID
		}()
		adminKey, _ := s.mintKey(t, admin, `{"name":"admin key","scope":"admin"}`)
		ownerAdminKey, _ := s.mintKey(t, owner, fmt.Sprintf(`{"name":"owner write","scope":"write","project_id":%q}`, pid))
		names := s.allToolNames(t, adminKey, "")
		member := has(names, "project_member_list")
		if member != flag {
			t.Errorf("flag=%v: project_member_list offered=%v", flag, member)
		}
		// Even with the flag, a write-scope key does not see them, and calling one is refused.
		wnames := s.allToolNames(t, ownerAdminKey, "")
		if has(wnames, "project_member_list") {
			t.Errorf("flag=%v: a project write key sees member tools", flag)
		}
		r := s.modernCall(t, ownerAdminKey, "tools/call", map[string]any{"name": "project_member_list", "arguments": map[string]any{"id": pid}})
		if r.msg.Error == nil {
			t.Errorf("flag=%v: a hidden tool was called: %s", flag, r.raw)
		}
		// The ones that are never offered stay out for an admin key with the flag on.
		for _, n := range []string{"key_create", "key_list", "backup_create", "ai_provider_create", "git_remote_create", "share_create", "project_purge", "webhook_create", "session_revoke", "import_apply"} {
			if has(names, n) {
				t.Errorf("flag=%v: %s is offered", flag, n)
			}
		}
	}
}

// A pinned key's resources start from its pin, and what it reads is what the tools show.
func TestMCPResourcesAndPins(t *testing.T) {
	s := newMCPServer(t, nil)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	mk := func(title string) (string, string) {
		out, err := s.Reg.Call(t.Context(), p, "project.create", projects.CreateIn{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		pid := out.(projects.Item).Item.ID
		d, err := s.Reg.Call(t.Context(), p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter of " + title, Content: "The words of " + title + "."})
		if err != nil {
			t.Fatal(err)
		}
		return pid, d.(docs.Item).Item.ID
	}
	p1, d1 := mk("First Book")
	p2, d2 := mk("Second Book")
	free, _ := s.mintKey(t, owner, `{"name":"free","scope":"read"}`)
	pinned, _ := s.mintKey(t, owner, fmt.Sprintf(`{"name":"pinned","scope":"read","project_id":%q}`, p1))

	list := func(sec string) string {
		return string(s.modernCall(t, sec, "resources/list", nil).raw)
	}
	if got := list(free); !strings.Contains(got, p1) || !strings.Contains(got, p2) {
		t.Errorf("an unpinned key should list both projects: %.300s", got)
	}
	if got := list(pinned); !strings.Contains(got, p1) || strings.Contains(got, p2) || strings.Contains(got, "Second Book") {
		t.Errorf("a pinned key lists only its project: %.300s", got)
	}
	read := func(sec, uri string) rpcResult {
		return s.modernCall(t, sec, "resources/read", map[string]any{"uri": uri})
	}
	if r := read(pinned, "scriobh://project/"+p1+"/doc/"+d1); r.msg.Error != nil || !strings.Contains(string(r.raw), "The words of First Book.") {
		t.Errorf("own document: %s", r.raw)
	}
	for name, uri := range map[string]string{
		"another project's document":         "scriobh://project/" + p2 + "/doc/" + d2,
		"a document under the wrong project": "scriobh://project/" + p1 + "/doc/" + d2,
		"another project's outline":          "scriobh://project/" + p2 + "/outline",
	} {
		r := read(pinned, uri)
		if r.msg.Error == nil || strings.Contains(string(r.raw), "Second Book") || strings.Contains(string(r.raw), "words of Second") {
			t.Errorf("%s: %s", name, r.raw)
		}
	}
	// A project that does not exist and one that is not yours answer the same.
	a, b := read(pinned, "scriobh://project/"+p2+"/outline"), read(pinned, "scriobh://project/prj_00000000000000000000000000/outline")
	if string(a.raw) != strings.Replace(string(b.raw), "prj_00000000000000000000000000", p2, 1) {
		t.Errorf("exists-but-forbidden and absent differ:\n%s\n%s", a.raw, b.raw)
	}
	r := read(free, "scriobh://project/"+p1+"/outline")
	if !strings.Contains(string(r.raw), "Chapter of First Book") || !strings.Contains(string(r.raw), "scriobh://project/"+p1+"/doc/"+d1) {
		t.Errorf("outline: %s", r.raw)
	}
	// The template list names what can be read.
	if r := s.modernCall(t, free, "resources/templates/list", nil); !strings.Contains(string(r.raw), "scriobh://project/{projectId}/doc/{docId}") {
		t.Errorf("templates: %s", r.raw)
	}
	// A trashed project's documents are not offered.
	if _, err := s.Reg.Call(t.Context(), p, "project.trash", projects.StateIn{ID: p2}); err != nil {
		t.Fatal(err)
	}
	if got := list(free); strings.Contains(got, p2) {
		t.Errorf("a trashed project is listed: %.200s", got)
	}
	_ = db.Now
}

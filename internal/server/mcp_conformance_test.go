package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/mcp"
	"github.com/crueber/scriobh/internal/ops"
)

// The MCP conformance sweep. Every tool of the catalog is called through the
// JSON-RPC endpoint by a table of principals, and the same call is made over
// REST as the same key. The two must agree:
//
//   - a tool the principal sees is answered exactly as REST answers (status and
//     error code; and the whole body for a refusal, which carries no data);
//   - a tool the principal does not see is refused by the endpoint as unknown, and
//     REST never allows less than MCP: if MCP succeeded, REST succeeds;
//   - nothing in any answer to a pinned key names what it cannot reach (the leak
//     list of the key sweep);
//   - a write that is allowed is only ever made as a preview, so the sweep does
//     not disturb its own fixtures, and ops that cannot be previewed are called
//     only by principals that must be refused.
//
// The principals: no key, a stranger, a reader, an editor and the owner (write
// scope), the owner with a read key, keys pinned to the project, to a project the
// owner has but the key is not pinned to, and to the Compendium.

type mcpPrincipal struct {
	name   string
	secret string
	id     mcp.Identity
	// refused says the principal must be refused by every op that needs the
	// project (so even a non-previewable write is safe to call).
	refused bool
}

func jsonArg(t string) any {
	switch t {
	case "integer", "number":
		return 1
	case "boolean":
		return false
	case "array":
		return []any{}
	case "object":
		return map[string]any{}
	}
	return "x"
}

// toolArgs builds the arguments of a tool call against one candidate of the sweep
// world: the first path parameter is the candidate's id, other path parameters are
// ids that exist nowhere, every other required field gets a value of its type.
func toolArgs(t *mcp.Tool, universes map[string][]cand, pick int) (map[string]any, bool) {
	args := map[string]any{}
	// The first path parameter in the address is the one that names the owner.
	firstParam := ""
	if m := placeholder.FindStringSubmatch(t.Path); m != nil {
		firstParam = m[1]
	}
	for _, f := range t.Fields {
		switch {
		case f.In == "path" && f.Name == firstParam:
			seg := segmentOf(t.Path, f.Name)
			u, ok := universes[seg]
			if !ok || pick >= len(u) {
				return nil, false
			}
			args[f.Name] = u[pick].id
		case f.In == "path":
			args[f.Name] = bogusFor(f.Name)
		case f.Required:
			args[f.Name] = jsonArg(f.Type)
		}
	}
	return args, true
}

// restCall makes the call over REST as the same key: the op's own path, query and body.
func (s *Server) restCall(secret string, t *mcp.Tool, args map[string]any, dry bool) (int, []byte) {
	path := t.Path
	q := url.Values{}
	body := map[string]any{}
	for _, f := range t.Fields {
		v, ok := args[f.Name]
		if !ok {
			continue
		}
		switch f.In {
		case "path":
			path = strings.Replace(path, "{"+f.Name+"}", fmt.Sprint(v), 1)
		case "query":
			q.Set(f.Name, fmt.Sprint(v))
		default:
			body[f.Name] = v
		}
	}
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	raw := ""
	if t.Mutating() {
		for _, f := range t.Fields {
			if f.In == "body" {
				b, _ := json.Marshal(body)
				raw = string(b)
				break
			}
		}
	}
	var hdr []string
	if dry {
		hdr = []string{"X-Scriobh-Dry-Run", "1"}
	}
	rec := s.doKey(secret, t.Method, path, raw, hdr...)
	return rec.Code, rec.Body.Bytes()
}

type restErr struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func errCode(b []byte) string {
	var e restErr
	_ = json.Unmarshal(b, &e)
	return e.Error.Code
}

func TestMCPConformance(t *testing.T) {
	runMCPConformance(t, newAuthzServer(t), false, ops.RoleWriter)
}

// The same sweep on a server that offers the administration tools, with an
// administrator's world: admin-scope keys, unpinned and pinned, and the membership
// tools they alone are offered. What an admin key may call is still what REST lets
// it call, and a write key still sees none of them.
func TestMCPConformanceWithAdministrationTools(t *testing.T) {
	runMCPConformance(t, newMCPServer(t, map[string]string{"SCRIOBH_MCP_ADMIN_TOOLS": "1"}), true, ops.RoleAdmin)
}

func runMCPConformance(t *testing.T, s *Server, adminTools bool, ownerRole string) {
	foreign := s.newCaller(t, "mcp-foreign", ops.RoleWriter)
	foreignP := ops.Principal{UserID: foreign.userID, Role: ops.RoleWriter}
	w := buildSweepWorld(t, s, ownerRole, "m", foreign, foreignP)
	pid := w.universes["projects"][0].id
	sibling := w.universes["projects"][1].id

	stranger := s.newCaller(t, "mcp-stranger", ops.RoleWriter)
	reader := s.newCaller(t, "mcp-reader", ops.RoleWriter)
	editor := s.newCaller(t, "mcp-editor", ops.RoleWriter)
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	s.addMember(t, pid, editor.userID, ops.ProjectEditor)
	mint := func(c caller, body string) string { sec, _ := s.mintKey(t, c, body); return sec }
	pinnedElsewhere := mint(w.owner, fmt.Sprintf(`{"name":"pinned elsewhere","scope":"write","project_id":%q}`, sibling))
	principals := []mcpPrincipal{
		{name: "stranger write key", secret: mint(stranger, `{"name":"stranger","scope":"write"}`), id: mcp.Identity{Scope: "write"}, refused: true},
		{name: "reader write key", secret: mint(reader, `{"name":"reader","scope":"write"}`), id: mcp.Identity{Scope: "write"}},
		{name: "editor write key", secret: mint(editor, `{"name":"editor","scope":"write"}`), id: mcp.Identity{Scope: "write"}},
		{name: "owner read key", secret: mint(w.owner, `{"name":"owner read","scope":"read"}`), id: mcp.Identity{Scope: "read"}},
		{name: "owner write key", secret: mint(w.owner, `{"name":"owner write","scope":"write"}`), id: mcp.Identity{Scope: "write"}},
		{name: "project key (write)", secret: w.projKeys["write"], id: mcp.Identity{Scope: "write", ProjectID: pid}},
		{name: "project key (read)", secret: w.projKeys["read"], id: mcp.Identity{Scope: "read", ProjectID: pid}},
		{name: "key pinned to another project", secret: pinnedElsewhere, id: mcp.Identity{Scope: "write", ProjectID: sibling}},
		{name: "compendium key (write)", secret: w.compKeys["write"], id: mcp.Identity{Scope: "write", CompendiumID: w.universes["compendiums"][0].id}},
	}
	if ownerRole == ops.RoleAdmin {
		principals = append(principals,
			mcpPrincipal{name: "owner admin key", secret: mint(w.owner, `{"name":"owner admin","scope":"admin"}`), id: mcp.Identity{Scope: "admin"}},
			mcpPrincipal{name: "project key (admin)", secret: w.projKeys["admin"], id: mcp.Identity{Scope: "admin", ProjectID: pid}},
			mcpPrincipal{name: "compendium key (admin)", secret: w.compKeys["admin"], id: mcp.Identity{Scope: "admin", CompendiumID: w.universes["compendiums"][0].id}})
	}
	cat := s.MCPCatalog()
	if cat == nil || len(cat.Tools) < 150 {
		t.Fatalf("catalog has %d tools", len(cat.Tools))
	}
	compared, hidden, previews := 0, 0, 0
	byStatus := map[int]int{}
	for _, p := range principals {
		p := p
		var leaks []string // what a pinned key must never be told
		if p.id.CompendiumID != "" {
			leaks = w.leaksComp
		} else if p.id.ProjectID == pid {
			leaks = w.leaksProj
		}
		listed := map[string]bool{}
		for _, n := range s.allToolNames(t, p.secret, "") {
			listed[n] = true
		}
		t.Run(p.name, func(t *testing.T) {
			// The listing itself follows the rules.
			for i := range cat.Tools {
				tool := &cat.Tools[i]
				should := scopeOrder[p.id.Scope] >= scopeOrder[tool.Access] && (!tool.AdminTier() || (adminTools && p.id.Scope == "admin")) && !tool.Permanent
				switch {
				case p.id.CompendiumID != "":
					should = should && tool.AuthKind() == "compendium"
				case p.id.ProjectID != "":
					should = should && (tool.AuthKind() == "project" || tool.AuthKind() == "compendium")
				}
				if listed[tool.Name] != should {
					t.Errorf("%s: listed=%v, the rules say %v", tool.Name, listed[tool.Name], should)
				}
			}
			for i := range cat.Tools {
				tool := &cat.Tools[i]
				// Every candidate of the tool's universe: the key's own and the ones it must not reach.
				for pick := 0; pick < 4; pick++ {
					args, ok := toolArgs(tool, w.universes, pick)
					if !ok {
						if pick == 0 && (tool.AuthKind() == "project" || tool.AuthKind() == "compendium") {
							t.Errorf("%s: no universe for its first path parameter", tool.Name)
						}
						break
					}
					// A permitted write is only previewed; a write that cannot be previewed is called only
					// when the principal must be refused (or the candidate is not theirs).
					dry := false
					if tool.Mutating() {
						switch {
						case tool.DryRun:
							dry = true
						case !p.refused && pick == 0 && tool.AuthKind() != "writer" && tool.AuthKind() != "authenticated":
							continue
						case !p.refused && (tool.AuthKind() == "writer" || tool.AuthKind() == "authenticated"):
							continue
						}
					}
					restArgs := args
					mcpArgs := map[string]any{}
					for k, v := range args {
						mcpArgs[k] = v
					}
					if dry {
						mcpArgs["dry_run"] = true
						previews++
					}
					status, restBody := s.restCall(p.secret, tool, restArgs, dry)
					res := s.modernCall(t, p.secret, "tools/call", map[string]any{"name": tool.Name, "arguments": mcpArgs})
					compared++
					for _, l := range leaks {
						if strings.Contains(string(res.raw), l) {
							t.Errorf("%s as %s: the answer names %q, which the key cannot reach: %.300s", tool.Name, p.name, l, res.raw)
						}
					}
					if !listed[tool.Name] {
						hidden++
						if res.msg.Error == nil || res.msg.Error.Code != mcp.CodeInvalidParams || !strings.Contains(res.msg.Error.Message, "Unknown tool") {
							t.Errorf("%s as %s: not listed, but not refused as unknown: %.300s", tool.Name, p.name, res.raw)
						}
						continue
					}
					if res.status != 200 || res.msg.Error != nil {
						t.Errorf("%s as %s: a listed tool answered with a protocol error: %d %.300s", tool.Name, p.name, res.status, res.raw)
						continue
					}
					isErr, _ := res.msg.Result["isError"].(bool)
					meta, _ := res.msg.Result["_meta"].(map[string]any)
					mStatus := 0
					if f, ok := meta["scriobh/status"].(float64); ok {
						mStatus = int(f)
					}
					if !isErr && mStatus >= 300 {
						t.Errorf("%s as %s: success with status %d", tool.Name, p.name, mStatus)
					}
					if isErr && mStatus == 0 {
						// Refused before it was sent: only the argument checks do that, and the arguments are well formed.
						t.Errorf("%s as %s: refused by the argument check: %s", tool.Name, p.name, toolText(res.msg.Result))
						continue
					}
					byStatus[status]++
					if mStatus != status {
						t.Errorf("%s as %s (candidate %d): MCP %d, REST %d\n  MCP:  %.200s\n  REST: %.200s", tool.Name, p.name, pick, mStatus, status, toolText(res.msg.Result), restBody)
						continue
					}
					if isErr {
						if got, want := errCode(restBody), errCode([]byte(toolText(res.msg.Result))); got != want {
							t.Errorf("%s as %s: error code MCP %q, REST %q", tool.Name, p.name, want, got)
						}
						if status == 401 || status == 403 || status == 404 {
							if strings.TrimSpace(string(restBody)) != strings.TrimSpace(toolText(res.msg.Result)) {
								t.Errorf("%s as %s: a refusal differs between MCP and REST:\n  MCP:  %s\n  REST: %s", tool.Name, p.name, toolText(res.msg.Result), restBody)
							}
						}
					}
				}
			}
		})
	}
	if compared < 3000 {
		t.Errorf("only %d calls were compared", compared)
	}
	t.Logf("%d calls compared (%d previews), %d refused as hidden tools, by status %v", compared, previews, hidden, byStatus)
	if byStatus[404] < 300 || byStatus[403] < 50 || byStatus[200]+byStatus[201] < 300 {
		t.Errorf("the comparison did not exercise the refusals and the successes: %v", byStatus)
	}

	// No key at all, and a cookie where a key belongs.
	rec := s.postMCP("", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no key: %d %s", rec.Code, rec.Body)
	}
	cookieReq := s.do(w.owner, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	if cookieReq.Code != 401 {
		t.Errorf("a session cookie must not open /mcp: %d %s", cookieReq.Code, cookieReq.Body)
	}
}

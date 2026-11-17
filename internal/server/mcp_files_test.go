package server

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// An op that answers with a file (an export) comes back as an embedded resource,
// the file's own text, and one over the result cap is refused rather than cut.
func TestMCPFilesAndTheResultCap(t *testing.T) {
	s := newMCPServer(t, map[string]string{"SCRIOBH_MCP_MAX_RESULT_KB": "2"})
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Files"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	if _, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Short", Content: "A short chapter."}); err != nil {
		t.Fatal(err)
	}
	key, _ := s.mintKey(t, owner, `{"name":"agent","scope":"write"}`)
	r := s.modernCall(t, key, "tools/call", map[string]any{"name": "export_markdown", "arguments": map[string]any{"id": pid, "mode": "single"}})
	if r.msg.Error != nil || r.msg.Result["isError"] == true {
		t.Fatalf("export: %s", r.raw)
	}
	blk := r.msg.Result["content"].([]any)[0].(map[string]any)
	rsrc, _ := blk["resource"].(map[string]any)
	if blk["type"] != "resource" || rsrc == nil || !strings.Contains(rsrc["text"].(string), "A short chapter.") || !strings.HasPrefix(rsrc["uri"].(string), "scriobh://download/") {
		t.Fatalf("a file result: %.400s", r.raw)
	}
	// A document of 40 KB does not fit through a 2 KB cap: refused, and the message says what to do.
	if _, err := s.Reg.Call(context.Background(), p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Long", Content: strings.Repeat("Words go on. ", 3000)}); err != nil {
		t.Fatal(err)
	}
	r = s.modernCall(t, key, "tools/call", map[string]any{"name": "export_markdown", "arguments": map[string]any{"id": pid, "mode": "single"}})
	txt := toolText(r.msg.Result)
	if r.msg.Result["isError"] != true || !strings.Contains(txt, "larger than") || strings.Contains(txt, "Words go on") {
		t.Errorf("a file over the cap: %.300s", r.raw)
	}
	// The same cap holds for a JSON result.
	var longID string
	_ = s.DB.Read().QueryRow(`SELECT id FROM documents WHERE project_id = ? AND title = 'Long'`, pid).Scan(&longID)
	r = s.modernCall(t, key, "tools/call", map[string]any{"name": "doc_get", "arguments": map[string]any{"id": longID}})
	if r.msg.Result["isError"] != true || strings.Contains(toolText(r.msg.Result), "Words go on") {
		t.Errorf("a JSON result over the cap: %.300s", r.raw)
	}
}

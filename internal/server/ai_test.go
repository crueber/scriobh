package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/crueber/scriobh/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

type sseEvent struct{ name, data string }

func parseSSE(body string) []sseEvent {
	var out []sseEvent
	for _, block := range strings.Split(body, "\n\n") {
		var e sseEvent
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				e.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				e.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if e.name != "" {
			out = append(out, e)
		}
	}
	return out
}

func TestTheStreamRoute(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	var bodies []string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 1<<16)
		n, _ := r.Body.Read(b)
		bodies = append(bodies, string(b[:n]))
		w.Header().Set("Content-Type", "text/event-stream")
		for _, p := range []string{"Short ", "answer ", "here."} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", p)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":6}}\n\ndata: [DONE]\n\n")
	}))
	defer provider.Close()
	// A provider on loopback is local only when an administrator says so.
	rec := s.do(admin, "POST", "/api/v1/ai/providers", fmt.Sprintf(`{"name":"Local","kind":"openai_compatible","base_url":%q,"default_model":"m","local":true,"api_key":"sk-stream-test-key-0123"}`, provider.URL+"/v1"))
	if rec.Code != 201 {
		t.Fatalf("provider: %d %s", rec.Code, rec.Body)
	}
	op := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	pid := func() string {
		out, err := s.Reg.Call(context.Background(), op, "project.create", projects.CreateIn{Title: "Streamed"})
		if err != nil {
			t.Fatal(err)
		}
		return out.(projects.Item).Item.ID
	}()
	d, err := s.Reg.Call(context.Background(), op, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: "The harbour was quiet."})
	if err != nil {
		t.Fatal(err)
	}
	did := d.(docs.Item).Item.ID
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	base := "/api/v1/projects/" + pid + "/ai"
	if rec := s.do(owner, "PUT", base+"/policy", `{"mode":"local_only"}`); rec.Code != 200 {
		t.Fatalf("policy: %d %s", rec.Code, rec.Body)
	}
	req := fmt.Sprintf(`{"recipe":"freeform","document_id":%q,"params":{"instruction":"Shorten it."},"chips":[{"kind":"document"}],"stream":true`, did)
	pvRec := s.do(owner, "POST", base+"/preview", req+"}")
	if pvRec.Code != 200 {
		t.Fatalf("preview: %d %s", pvRec.Code, pvRec.Body)
	}
	var pv struct {
		Item struct {
			Nonce       string `json:"nonce"`
			PayloadHash string `json:"payload_hash"`
			Attempts    []struct {
				Body string `json:"body"`
			} `json:"attempts"`
		} `json:"item"`
	}
	_ = json.Unmarshal(pvRec.Body.Bytes(), &pv)
	run := req + fmt.Sprintf(`,"nonce":%q,"payload_hash":%q}`, pv.Item.Nonce, pv.Item.PayloadHash)

	// A stream.
	rec = s.do(owner, "POST", base+"/run/stream", run)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	evs := parseSSE(rec.Body.String())
	var names []string
	text := ""
	for _, e := range evs {
		names = append(names, e.name)
		if e.name == "delta" {
			var d struct{ Text string }
			_ = json.Unmarshal([]byte(e.data), &d)
			text += d.Text
		}
	}
	if names[0] != "start" || names[len(names)-1] != "done" || text != "Short answer here." {
		t.Fatalf("events %v text %q\n%s", names, text, rec.Body)
	}
	var done struct {
		Item struct {
			Text         string `json:"text"`
			Status       string `json:"status"`
			InputTokens  int    `json:"input_tokens"`
			OutputTokens int    `json:"output_tokens"`
		} `json:"item"`
	}
	_ = json.Unmarshal([]byte(evs[len(evs)-1].data), &done)
	if done.Item.Text != text || done.Item.Status != "ok" || done.Item.InputTokens != 50 {
		t.Errorf("done %+v", done)
	}
	if len(bodies) != 1 || bodies[0] != pv.Item.Attempts[0].Body {
		t.Errorf("the provider did not receive the previewed bytes:\n%v\n%s", bodies, pv.Item.Attempts[0].Body)
	}
	if strings.Contains(rec.Header().Get("Cache-Control"), "public") {
		t.Error("a stream must not be cacheable")
	}
	// The streamed text is not in the events table.
	var n int
	_ = s.DB.Read().QueryRow(`SELECT count(*) FROM events WHERE payload_json LIKE '%Short%' OR payload_json LIKE '%harbour%'`).Scan(&n)
	if n != 0 {
		t.Error("completion text reached the events table")
	}

	// A stale hash is an ordinary 409 before any stream starts, and nothing is sent.
	bad := req + fmt.Sprintf(`,"nonce":%q,"payload_hash":%q}`, pv.Item.Nonce, strings.Repeat("0", 64))
	if rec := s.do(owner, "POST", base+"/run/stream", bad); rec.Code != 409 || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Errorf("stale: %d %s", rec.Code, rec.Body)
	}
	// The same checks as every other route.
	if rec := s.do(caller{name: "anon"}, "POST", base+"/run/stream", run); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := s.do(reader, "POST", base+"/run/stream", run); rec.Code != 403 {
		t.Errorf("reader: %d", rec.Code)
	}
	if rec := s.do(stranger, "POST", base+"/run/stream", run); rec.Code != 404 {
		t.Errorf("stranger: %d", rec.Code)
	}
	if rec := s.do(owner, "POST", base+"/run/stream", `{"recipe":"freeform","bogus":1}`); rec.Code != 422 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	if rec := s.doDry(owner, "POST", base+"/run/stream", run); rec.Code != 422 || !strings.Contains(rec.Body.String(), "dry_run_unsupported") {
		t.Errorf("dry run: %d %s", rec.Code, rec.Body)
	}
	x := httptest.NewRequest("POST", base+"/run/stream", strings.NewReader(run))
	x.Header.Set("Sec-Fetch-Site", "cross-site")
	x.AddCookie(owner.cookie)
	xr := httptest.NewRecorder()
	s.Handler().ServeHTTP(xr, x)
	if xr.Code != 403 {
		t.Errorf("cross-site: %d", xr.Code)
	}
	// The ops are on the same routes for the CLI: the schema lists them.
	for _, name := range []string{"ai.run", "ai.context.preview", "ai.policy.set", "ai.provider.create", "ai.usage.get", "ai.budget.set"} {
		if _, ok := s.Reg.Lookup(name); !ok {
			t.Errorf("%s is not registered", name)
		}
	}
}

// With the kill switch on, nothing answers, and no provider is contacted.
func TestTheKillSwitchOverHTTP(t *testing.T) {
	s, err := New(Deps{Cfg: testutil.Config(t, map[string]string{"SCRIOBH_RATE_LIMIT": "0", "SCRIOBH_AI": "off"}), DB: testutil.DB(t)})
	if err != nil {
		t.Fatal(err)
	}
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	rec := s.do(owner, "GET", "/api/v1/ai/providers", "")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "SCRIOBH_AI=off") {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	rec = s.do(owner, "GET", "/api/v1/ai/status", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":false`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}

	// What sends nothing still answers with SCRIOBH_AI=off: kept chats can be
	// read and deleted, never-send flags set and lifted, use and the run log read.
	op := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, cerr := s.Reg.Call(context.Background(), op, "project.create", projects.CreateIn{Title: "Off"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	pid := out.(projects.Item).Item.ID
	d, cerr := s.Reg.Call(context.Background(), op, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Content: "The harbour was quiet."})
	if cerr != nil {
		t.Fatal(cerr)
	}
	did := d.(docs.Item).Item.ID
	// a chat kept while AI was on
	chat := db.NewID("cht")
	if err := s.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO ai_chats(id, project_id, user_id, title, created_at, updated_at) VALUES (?, ?, ?, 'Kept', '2026-01-01T00:00:00.000Z', '2026-01-01T00:00:00.000Z')`, chat, pid, owner.userID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + pid + "/ai"
	for _, c := range []struct{ method, path, body string }{
		{"GET", base + "/chats", ""},
		{"GET", base + "/chat?chat_id=" + chat, ""},
		{"PUT", base + "/exclude", fmt.Sprintf(`{"kind":"document","id":%q,"exclude":true}`, did)},
		{"GET", base + "/exclude", ""},
		{"PUT", base + "/exclude", fmt.Sprintf(`{"kind":"document","id":%q,"exclude":false}`, did)},
		{"GET", "/api/v1/ai/usage", ""},
		{"GET", base + "/usage", ""},
		{"GET", "/api/v1/ai/budget?project_id=" + pid, ""},
		{"GET", base + "/runs", ""},
		{"DELETE", base + "/chat?chat_id=" + chat, ""},
	} {
		if rec := s.do(owner, c.method, c.path, c.body); rec.Code != 200 {
			t.Errorf("%s %s with AI off: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	if rec := s.do(owner, "GET", base+"/chats", ""); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("the chat was not deleted: %s", rec.Body)
	}
	ask := fmt.Sprintf(`{"recipe":"freeform","document_id":%q,"params":{"instruction":"Shorten it."},"chips":[{"kind":"document"}]}`, did)
	for _, c := range []struct{ method, path, body string }{
		{"POST", base + "/chats", `{}`},
		{"POST", base + "/chat/preview", `{"message":"Hello?"}`},
		{"POST", base + "/preview", ask},
		{"POST", base + "/run", ask[:len(ask)-1] + `,"nonce":"` + strings.Repeat("0", 32) + `","payload_hash":"` + strings.Repeat("0", 64) + `"}`},
		{"PUT", base + "/policy", `{"mode":"ask"}`},
	} {
		if rec := s.do(owner, c.method, c.path, c.body); rec.Code != 409 {
			t.Errorf("%s %s with AI off: want 409, got %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
}

package aiwork_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

// timeScale loosens time bounds under the race detector.
const timeScale = raceFactor

const chapterText = "The harbour was quiet. Maire Nolan walked down to the water. The gulls were loud.\n\nA second paragraph about nothing."

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

type env struct {
	t        *testing.T
	d        *db.DB
	reg      *ops.Registry
	core     *aicore.Service
	admin    ops.Principal
	owner    ops.Principal
	editor   ops.Principal
	editor2  ops.Principal
	reader   ops.Principal
	other    ops.Principal
	project  string
	doc      string
	provider string
	fake     *fake
}

func principal(t *testing.T, d *db.DB, role, name string) ops.Principal {
	id := testutil.User(t, d, role, name+"@test.local")
	return ops.Principal{UserID: id, Role: role, Name: name, Via: "session"}
}

// fake is a provider that answers every completion with whatever answer returns
// for the request body, and records what it received.
type fake struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
	n      atomic.Int64
	answer func(body string) string
}

func (f *fake) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.bodies...)
}

func newFake(t *testing.T, answer func(body string) string) *fake {
	f := &fake{answer: answer}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"model-a"}]}`)
			return
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, string(b))
		f.mu.Unlock()
		f.n.Add(1)
		text := f.answer(string(b))
		switch r.URL.Path {
		case "/api/chat": // Ollama
			w.Header().Set("Content-Type", "application/x-ndjson")
			enc, _ := json.Marshal(text)
			fmt.Fprintf(w, `{"message":{"role":"assistant","content":%s},"done":true,"done_reason":"stop","prompt_eval_count":100,"eval_count":40}`+"\n", enc)
			return
		case "/api/experimental/generate/text": // OpenCode (assumed shape)
			w.Header().Set("Content-Type", "application/json")
			enc, _ := json.Marshal(text)
			fmt.Fprintf(w, `{"text":%s}`, enc)
			return
		}
		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(b, &req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			enc, _ := json.Marshal(text)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", enc)
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":40}}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc, _ := json.Marshal(text)
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":40}}`, enc)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// newEnv builds a registry over a temp database with a project, an editor, a
// fake provider and AI switched on for the project.
func newEnv(t *testing.T) *env { return newEnvOpts(t, nil) }

// newEnvOpts is newEnv with a change to the core's options (no secret key, ...).
func newEnvOpts(t *testing.T, mut func(*aicore.Options)) *env {
	t.Helper()
	d := testutil.DB(t)
	e := &env{t: t, d: d}
	lg := slog.New(slog.NewTextHandler(&lockedBuf{}, nil))
	guard := netguard.Options{Classify: func(ip net.IP) netguard.Class {
		if ip.IsLoopback() {
			return netguard.Public
		}
		return netguard.Classify(ip)
	}}
	o := aicore.Options{Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), Log: lg, Guard: guard, AllowPlainHTTP: true,
		Config: aicore.Config{Retries: 0, RatePerMinute: 100000, DialTimeout: 2 * time.Second}}
	if mut != nil {
		mut(&o)
	}
	e.reg = registry.BuildWith(d, events.NewStore(d, lg), t.TempDir(), registry.Options{Log: lg, AI: o})
	core, ok := aicore.ServiceOf(e.reg.Env())
	if !ok {
		t.Fatal("no AI service")
	}
	e.core = core
	e.admin = principal(t, d, ops.RoleAdmin, "admin")
	e.owner = principal(t, d, ops.RoleWriter, "owner")
	e.editor = principal(t, d, ops.RoleWriter, "editor")
	e.editor2 = principal(t, d, ops.RoleWriter, "editor2")
	e.reader = principal(t, d, ops.RoleReader, "reader")
	e.other = principal(t, d, ops.RoleWriter, "other")
	e.project = e.call(e.owner, "project.create", projects.CreateIn{Title: "The Salt Road"}).(projects.Item).Item.ID
	e.member(e.editor, ops.ProjectEditor)
	e.member(e.editor2, ops.ProjectEditor)
	e.member(e.reader, ops.ProjectReader)
	e.doc = e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: "Chapter One", Kind: "chapter", Content: chapterText}).(docs.Item).Item.ID
	e.fake = newFake(t, func(string) string { return "A suggestion." })
	e.provider = e.addProvider(e.fake)
	e.enable("ask", nil)
	return e
}

func (e *env) member(p ops.Principal, role string) {
	e.t.Helper()
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, e.project, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) call(p ops.Principal, name string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(context.Background(), p, name, in)
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	return out
}

func (e *env) try(p ops.Principal, name string, in any) (any, *ops.Error) {
	out, err := e.reg.Call(context.Background(), p, name, in)
	return out, ops.AsError(err)
}

func wantCode(t *testing.T, err *ops.Error, code ops.Code) {
	t.Helper()
	if err == nil || err.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func (e *env) addProvider(f *fake) string {
	e.t.Helper()
	in := aicore.ProviderCreateIn{Name: "Fake", Kind: lib.KindOpenAICompatible, Scope: "instance", BaseURL: f.srv.URL + "/v1", APIKey: "sk-test-PLANTED-0123456789", DefaultModel: "model-a"}
	if !e.core.Box().Enabled() {
		in.APIKey = "" // a server with no secret key stores no credentials
	}
	return e.call(e.admin, "ai.provider.create", in).(aicore.ProviderItem).Item.ID
}

func (e *env) enable(mode string, mut func(*aicore.PolicySetIn)) {
	e.t.Helper()
	in := aicore.PolicySetIn{ProjectID: e.project, Mode: &mode}
	if mut != nil {
		mut(&in)
	}
	e.call(e.owner, "ai.policy.set", in)
}

func (e *env) say(f func(body string) string) { e.fake.answer = f }

func (e *env) say1(text string) { e.fake.answer = func(string) string { return text } }

func (e *env) docNow() docs.Doc {
	return e.call(e.owner, "doc.get", docs.DocIn{ID: e.doc}).(docs.Item).Item
}

func (e *env) revisionOf(id string) int {
	return e.call(e.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item.Revision
}

func (e *env) setDoc(content string) docs.Doc {
	e.t.Helper()
	d := e.docNow()
	return e.call(e.owner, "doc.update", docs.UpdateIn{ID: e.doc, BaseRevision: d.Revision, Content: &content}).(docs.Item).Item
}

// run previews and runs a request as p.
func (e *env) run(p ops.Principal, in aicore.RunIn) (aicore.RunOut, *ops.Error) {
	e.t.Helper()
	pv := e.call(p, "ai.context.preview", in).(aicore.PreviewOut).Item
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	out, err := e.try(p, "ai.run", in)
	if err != nil {
		return aicore.RunOut{}, err
	}
	return out.(aicore.RunItem).Item, nil
}

func (e *env) mustRun(p ops.Principal, in aicore.RunIn) aicore.RunOut {
	e.t.Helper()
	out, err := e.run(p, in)
	if err != nil {
		e.t.Fatalf("run %s: %v", in.Recipe, err)
	}
	return out
}

// sel selects a quote of the chapter.
func (e *env) sel(quote string) *aicore.Selection {
	rev := e.docNow().Revision
	return &aicore.Selection{Quote: quote, BaseRevision: &rev}
}

func (e *env) count(q string, args ...any) int {
	var n int
	if err := e.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// data decodes a run's data into v.
func data(t *testing.T, out aicore.RunOut, v any) {
	t.Helper()
	if len(out.Data) == 0 {
		t.Fatalf("no data (warnings %v, text %q)", out.Warnings, out.Text)
	}
	if err := json.Unmarshal(out.Data, v); err != nil {
		t.Fatal(err)
	}
}

func (e *env) rewrite(quote, instruction string) aicore.RunIn {
	return aicore.RunIn{ProjectID: e.project, Recipe: "rewrite", DocumentID: e.doc, Selection: e.sel(quote), Params: map[string]string{"instruction": instruction}}
}

var _ = aiwork.PendingTTL

func docsHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (e *env) compendium(name string) string {
	e.t.Helper()
	return e.call(e.owner, "compendium.create", compendiums.CreateIn{Name: name}).(compendiums.Item).Item.ID
}

func (e *env) setCompendium(cid string) {
	e.t.Helper()
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: e.project, CompendiumID: cid})
}

// exec runs a statement against the database, for tests that plant or damage rows.
func (e *env) exec(q string, args ...any) (sql.Result, error) {
	var res sql.Result
	err := e.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		res, err = tx.Exec(q, args...)
		return err
	})
	return res, err
}

func toJSONString(t *testing.T, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

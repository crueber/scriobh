package semantic_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
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
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/domain/stash"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

// No real embedding model is used anywhere in these tests. The fake endpoint
// below hashes words into a vector, so texts that share words have close
// vectors; it proves the plumbing (what is sent, what is kept, what is shown)
// and says nothing about the quality of a real model's results.

const planted = "sk-embed-PLANTED-KEY-0123456789abcdef"

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ---- the fake endpoint -----------------------------------------------------------------------

const fakeDim = 96

// hashVec turns text into a unit vector of word counts, hashed into fakeDim slots.
func hashVec(text string, dim int) []float64 {
	v := make([]float64, dim)
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		h := fnv.New32a()
		h.Write([]byte(w))
		s := h.Sum32()
		sign := 1.0
		if s&0x80000000 != 0 {
			sign = -1
		}
		v[int(s%uint32(dim))] += sign
	}
	var n float64
	for _, x := range v {
		n += x * x
	}
	if n == 0 {
		v[0], n = 1, 1
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] /= n
	}
	return v
}

type fakeEmbed struct {
	t    *testing.T
	srv  *httptest.Server
	mode string // ollama, openai or ollama_old: which route answers; the others are 404
	dim  atomic.Int64

	mu     sync.Mutex
	texts  []string
	paths  []string
	auth   []string
	bodies [][]byte
	calls  atomic.Int64

	// hostile, when set, answers instead of the normal route; it returns true when it did.
	hostile atomic.Pointer[func(w http.ResponseWriter, r *http.Request, body []byte) bool]
}

func newFakeEmbed(t *testing.T, mode string) *fakeEmbed {
	f := &fakeEmbed{t: t, mode: mode}
	f.dim.Store(fakeDim)
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeEmbed) setHostile(h func(w http.ResponseWriter, r *http.Request, body []byte) bool) {
	if h == nil {
		f.hostile.Store(nil)
		return
	}
	f.hostile.Store(&h)
}

func (f *fakeEmbed) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	f.calls.Add(1)
	f.mu.Lock()
	f.paths = append(f.paths, r.URL.Path)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()
	if h := f.hostile.Load(); h != nil && (*h)(w, r, body) {
		return
	}
	dim := int(f.dim.Load())
	switch {
	case f.mode == "ollama" && r.URL.Path == "/api/embed":
		var in struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if json.Unmarshal(body, &in) != nil || len(in.Input) == 0 {
			http.Error(w, `{"error":"bad"}`, 400)
			return
		}
		f.record(in.Input...)
		out := make([][]float64, len(in.Input))
		for i, s := range in.Input {
			out[i] = hashVec(s, dim)
		}
		json.NewEncoder(w).Encode(map[string]any{"model": in.Model, "embeddings": out, "prompt_eval_count": len(in.Input) * 10})
	case f.mode == "ollama_old" && r.URL.Path == "/api/embeddings":
		var in struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if json.Unmarshal(body, &in) != nil || in.Prompt == "" {
			http.Error(w, `{"error":"bad"}`, 400)
			return
		}
		f.record(in.Prompt)
		json.NewEncoder(w).Encode(map[string]any{"embedding": hashVec(in.Prompt, dim)})
	case f.mode == "openai" && r.URL.Path == "/v1/embeddings":
		var in struct {
			Model  string   `json:"model"`
			Input  []string `json:"input"`
			Format string   `json:"encoding_format"`
		}
		if json.Unmarshal(body, &in) != nil || len(in.Input) == 0 {
			http.Error(w, `{"error":{"message":"bad"}}`, 400)
			return
		}
		f.record(in.Input...)
		data := make([]map[string]any, len(in.Input))
		// answered out of order, to prove the index field is honoured
		for i := range in.Input {
			j := len(in.Input) - 1 - i
			data[i] = map[string]any{"object": "embedding", "index": j, "embedding": hashVec(in.Input[j], dim)}
		}
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "model": in.Model, "data": data, "usage": map[string]any{"prompt_tokens": len(in.Input) * 10, "total_tokens": len(in.Input) * 10}})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeEmbed) record(texts ...string) {
	f.mu.Lock()
	f.texts = append(f.texts, texts...)
	f.mu.Unlock()
}

// received is everything the endpoint was ever sent, joined.
func (f *fakeEmbed) received() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.texts, "\n---\n")
}

func (f *fakeEmbed) textCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.texts)
}

func (f *fakeEmbed) rawBodies() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var b strings.Builder
	for _, x := range f.bodies {
		b.Write(x)
	}
	return b.String()
}

// ---- the environment -------------------------------------------------------------------------

type env struct {
	t       *testing.T
	d       *db.DB
	reg     *ops.Registry
	ai      *aicore.Service
	sem     *semantic.Service
	log     *lockedBuf
	admin   ops.Principal
	owner   ops.Principal
	editor  ops.Principal
	reader  ops.Principal
	other   ops.Principal
	project string
	ctx     context.Context
	dir     string // the data directory the registry was given

	loopbackLocal atomic.Bool
}

func principal(t *testing.T, d *db.DB, role, name string) ops.Principal {
	id := testutil.User(t, d, role, name+"@test.local")
	return ops.Principal{UserID: id, Role: role, Name: name, Via: "session"}
}

func newEnv(t *testing.T, scfg semantic.Config, aimut func(*aicore.Options)) *env {
	t.Helper()
	d := testutil.DB(t)
	e := &env{t: t, d: d, log: &lockedBuf{}, ctx: context.Background()}
	guard := netguard.Options{Classify: func(ip net.IP) netguard.Class {
		if ip.IsLoopback() {
			if e.loopbackLocal.Load() {
				return netguard.Local
			}
			return netguard.Public
		}
		return netguard.Classify(ip)
	}}
	lg := slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := aicore.Options{Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), Log: lg, Guard: guard, AllowPlainHTTP: true,
		Config: aicore.Config{Retries: 0, RatePerMinute: 100000, DialTimeout: 2 * time.Second}}
	if aimut != nil {
		aimut(&o)
	}
	if scfg.Quiet == 0 {
		scfg.Quiet = -1
	}
	if scfg.Poll == 0 {
		scfg.Poll = 20 * time.Millisecond
	}
	e.dir = t.TempDir()
	e.reg = registry.BuildWith(d, events.NewStore(d, lg), e.dir, registry.Options{Log: lg, AI: o, Semantic: scfg})
	ai, ok := aicore.ServiceOf(e.reg.Env())
	if !ok {
		t.Fatal("no AI service")
	}
	e.ai = ai
	sem, ok := semantic.ServiceOf(e.reg.Env())
	if !ok {
		t.Fatal("no semantic service")
	}
	e.sem = sem
	e.admin = principal(t, d, ops.RoleAdmin, "admin")
	e.owner = principal(t, d, ops.RoleWriter, "owner")
	e.editor = principal(t, d, ops.RoleWriter, "editor")
	e.reader = principal(t, d, ops.RoleWriter, "reader")
	e.other = principal(t, d, ops.RoleWriter, "other")
	e.project = e.call(e.owner, "project.create", projects.CreateIn{Title: "The Salt Road"}).(projects.Item).Item.ID
	e.member(e.editor, ops.ProjectEditor)
	e.member(e.reader, ops.ProjectReader)
	return e
}

func (e *env) member(p ops.Principal, role string) {
	e.t.Helper()
	err := e.d.WithTx(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, e.project, p.UserID, role, db.Now())
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) call(p ops.Principal, name string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(e.ctx, p, name, in)
	if err != nil {
		e.t.Fatalf("%s: %v", name, err)
	}
	return out
}

func (e *env) try(p ops.Principal, name string, in any) (any, *ops.Error) {
	out, err := e.reg.Call(e.ctx, p, name, in)
	return out, ops.AsError(err)
}

func wantCode(t *testing.T, err *ops.Error, code ops.Code) {
	t.Helper()
	if err == nil || err.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// doc creates a document and returns its id.
func (e *env) doc(title, kind, parent, content string) string {
	e.t.Helper()
	return e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: title, Kind: kind, ParentID: parent, Content: content}).(docs.Item).Item.ID
}

func (e *env) update(id, content string) {
	e.t.Helper()
	cur := e.call(e.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: id, BaseRevision: cur.Revision, Content: &content})
}

// provider adds an instance provider for the fake. kind is ollama or openai_compatible.
func (e *env) provider(f *fakeEmbed, name string, local bool) string {
	e.t.Helper()
	kind, base := lib.KindOllama, f.srv.URL
	if f.mode == "openai" {
		kind, base = lib.KindOpenAICompatible, f.srv.URL+"/v1"
	}
	in := aicore.ProviderCreateIn{Name: name, Kind: kind, Scope: "instance", BaseURL: base, APIKey: planted, DefaultModel: "embed-a", Local: local}
	return e.call(e.admin, "ai.provider.create", in).(aicore.ProviderItem).Item.ID
}

func (e *env) policy(mode string, mut func(*aicore.PolicySetIn)) {
	e.t.Helper()
	in := aicore.PolicySetIn{ProjectID: e.project, Mode: &mode}
	if mut != nil {
		mut(&in)
	}
	e.call(e.owner, "ai.policy.set", in)
}

// setup makes an AI-enabled project with a local fake endpoint and returns the provider id.
func (e *env) setup(f *fakeEmbed) string {
	e.t.Helper()
	e.loopbackLocal.Store(true)
	pid := e.provider(f, "Embeddings", true)
	e.policy("ask", nil)
	return pid
}

// turnOn previews and enables with the preview's token.
func (e *env) turnOn(pid string) semantic.StatusOut {
	e.t.Helper()
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	if pv.Blocked != nil || pv.ConsentToken == "" {
		e.t.Fatalf("preview blocked: %+v", pv.Blocked)
	}
	in := semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: pv.ConsentToken}
	if pv.Remote {
		in.ConfirmRemote = pv.ConfirmHost
	}
	return e.call(e.owner, "search.semantic.enable", in).(semantic.StatusOut)
}

func (e *env) settle() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 60*time.Second)
	defer cancel()
	e.sem.Settle(ctx)
}

func (e *env) status(p ops.Principal) semantic.StatusOut {
	e.t.Helper()
	return e.call(p, "search.semantic.status", semantic.StatusIn{ProjectID: e.project}).(semantic.StatusOut)
}

func (e *env) search(p ops.Principal, q string) (semantic.SearchOut, *ops.Error) {
	out, err := e.try(p, "search.semantic", semantic.SearchIn{ProjectID: e.project, Q: q})
	if err != nil {
		return semantic.SearchOut{}, err
	}
	return out.(semantic.SearchOut), nil
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// words builds a paragraph around one theme: its words repeat, so the hashed vector is close to the theme's.
func para(theme string, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "%s detail%d ", theme, i%5)
	}
	return strings.TrimSpace(b.String()) + "."
}

func (e *env) docID(title string) string {
	e.t.Helper()
	var id string
	if err := e.d.Read().QueryRow(`SELECT id FROM documents WHERE project_id = ? AND title = ?`, e.project, title).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) stash(doc, content string) {
	e.t.Helper()
	e.call(e.owner, "stash.create", stash.CreateIn{ID: e.project, Content: content, SourceDocumentID: doc})
}

func searchQuery(project, q string) search.QueryIn { return search.QueryIn{ProjectID: project, Q: q} }

func stateIn(id string) projects.StateIn { return projects.StateIn{ID: id} }

func (e *env) doc2(title string) { e.doc(title, "scene", "", para("filler words for the "+title, 30)) }

func (e *env) recv(f *fakeEmbed) string { return f.received() }

func fmtJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

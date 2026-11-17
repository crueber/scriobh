package ai_test

import (
	"bytes"
	"context"
	"database/sql"
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
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
)

// race scales time-based bounds: the race detector slows everything down.
const race = raceEnabled

// A secret the tests plant as a provider key; it must never appear anywhere.
const planted = "sk-test-PLANTED-KEY-0123456789abcdef"

// lockedBuf is a log sink safe for concurrent use.
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

type env struct {
	t       *testing.T
	d       *db.DB
	reg     *ops.Registry
	svc     *aicore.Service
	log     *lockedBuf
	dir     string
	admin   ops.Principal
	owner   ops.Principal
	editor  ops.Principal
	reader  ops.Principal
	other   ops.Principal // a writer with no access to the project
	project string
	doc     string
	cfg     aicore.Config
	guard   netguard.Options

	loopbackLocal atomic.Bool
}

func principal(t *testing.T, d *db.DB, role, name string) ops.Principal {
	id := testutil.User(t, d, role, name+"@test.local")
	return ops.Principal{UserID: id, Role: role, Name: name, Via: "session"}
}

// newEnv builds a registry with the AI core over a temp database. Tests that
// reach fake providers on loopback treat them as public through the guard's
// classifier seam unless they mark the provider local.
func newEnv(t *testing.T, mut func(*aicore.Options)) *env {
	t.Helper()
	d := testutil.DB(t)
	e := &env{t: t, d: d, log: &lockedBuf{}, dir: t.TempDir()}
	e.guard = netguard.Options{Classify: func(ip net.IP) netguard.Class {
		if ip.IsLoopback() {
			if e.loopbackLocal.Load() {
				return netguard.Local // a test that wants its fake to be a LAN server
			}
			return netguard.Public
		}
		return netguard.Classify(ip)
	}}
	lg := slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	o := aicore.Options{Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), Log: lg, Guard: e.guard, AllowPlainHTTP: true,
		Config: aicore.Config{Retries: 1, RatePerMinute: 100000, DialTimeout: 2 * time.Second}}
	if mut != nil {
		mut(&o)
	}
	e.cfg = o.Config
	e.reg = registry.BuildWith(d, events.NewStore(d, lg), e.dir, registry.Options{Log: lg, AI: o})
	svc, ok := aicore.ServiceOf(e.reg.Env())
	if !ok {
		t.Fatal("no AI service")
	}
	e.svc = svc
	e.admin = principal(t, d, ops.RoleAdmin, "admin")
	e.owner = principal(t, d, ops.RoleWriter, "owner")
	e.editor = principal(t, d, ops.RoleWriter, "editor")
	e.reader = principal(t, d, ops.RoleWriter, "reader")
	e.other = principal(t, d, ops.RoleWriter, "other")
	e.project = e.call(e.owner, "project.create", projects.CreateIn{Title: "The Salt Road"}).(projects.Item).Item.ID
	e.member(e.editor, ops.ProjectEditor)
	e.member(e.reader, ops.ProjectReader)
	e.doc = e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: "Chapter One", Kind: "chapter",
		Content: "The harbour was quiet. Maire Nolan walked down to the water. The gulls were loud.\n\nA second paragraph about nothing."}).(docs.Item).Item.ID
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

// ---- fake providers ---------------------------------------------------------------------------

type recorded struct {
	Path   string
	Header http.Header
	Body   []byte
}

type fake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []recorded
	n    atomic.Int64
	// handler answers; nil uses an OpenAI-shaped completion.
	handler func(w http.ResponseWriter, r *http.Request, body []byte, n int)
}

func (f *fake) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded{}, f.reqs...)
}

func newFake(t *testing.T, h func(w http.ResponseWriter, r *http.Request, body []byte, n int)) *fake {
	f := &fake{handler: h}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recorded{Path: r.URL.Path, Header: r.Header.Clone(), Body: b})
		f.mu.Unlock()
		n := int(f.n.Add(1))
		if f.handler != nil {
			f.handler(w, r, b, n)
			return
		}
		oaiReply(w, r, b, "A suggestion.")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// oaiReply answers in the OpenAI shape, streamed when the request asked for it.
func oaiReply(w http.ResponseWriter, r *http.Request, body []byte, text string) {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	if r.URL.Path == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"model-a","name":"Model A","context_length":32768,"pricing":{"prompt":"0.000001","completion":"0.000002"}},{"id":"model-b"}]}`)
		return
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, piece := range splitPieces(text) {
			b, _ := json.Marshal(piece)
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%s}}]}\n\n", b)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":30}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(text)
	fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":30}}`, b)
}

func splitPieces(s string) []string {
	// Pieces of six characters (a provider's JSON never splits a character).
	var out []string
	rs := []rune(s)
	for len(rs) > 6 {
		out = append(out, string(rs[:6]))
		rs = rs[6:]
	}
	return append(out, string(rs))
}

// addProvider creates an instance provider (as the admin) pointing at a fake.
func (e *env) addProvider(f *fake, name string, local bool) string {
	e.t.Helper()
	in := aicore.ProviderCreateIn{Name: name, Kind: lib.KindOpenAICompatible, Scope: "instance", BaseURL: f.srv.URL + "/v1", APIKey: planted,
		DefaultModel: "model-a", Local: local}
	out := e.call(e.admin, "ai.provider.create", in).(aicore.ProviderItem)
	return out.Item.ID
}

// enable turns AI on for the project with the given mode.
func (e *env) enable(mode string, mut func(*aicore.PolicySetIn)) {
	e.t.Helper()
	in := aicore.PolicySetIn{ProjectID: e.project, Mode: &mode}
	if mut != nil {
		mut(&in)
	}
	e.call(e.owner, "ai.policy.set", in)
}

// freeform is a request for the core's own recipe on the project's document.
func (e *env) freeform(instruction string) aicore.RunIn {
	return aicore.RunIn{ProjectID: e.project, Recipe: "freeform", DocumentID: e.doc, Params: map[string]string{"instruction": instruction},
		Chips: []aicore.ChipSpec{{Kind: lib.ChipDocument}}}
}

func (e *env) preview(p ops.Principal, in aicore.RunIn) aicore.Preview {
	e.t.Helper()
	return e.call(p, "ai.context.preview", in).(aicore.PreviewOut).Item
}

// run previews and runs.
func (e *env) run(p ops.Principal, in aicore.RunIn) (aicore.RunOut, *ops.Error) {
	e.t.Helper()
	pv := e.preview(p, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	out, err := e.try(p, "ai.run", in)
	if err != nil {
		return aicore.RunOut{}, err
	}
	return out.(aicore.RunItem).Item, nil
}

func (e *env) entity(compendium, name string, aliases ...string) string {
	e.t.Helper()
	return e.call(e.owner, "entity.create", entities.CreateIn{ID: compendium, Kind: "character", Name: name, Aliases: aliases, Summary: name + " is a fisher."}).(entities.Item).Item.ID
}

func (e *env) compendium(name string) string {
	e.t.Helper()
	return e.call(e.owner, "compendium.create", compendiums.CreateIn{Name: name}).(compendiums.Item).Item.ID
}

func (e *env) setCompendium(cid string) {
	e.t.Helper()
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: e.project, CompendiumID: cid})
}

func (e *env) updateDoc(id, content string) {
	e.t.Helper()
	d := e.call(e.owner, "doc.get", docs.DocIn{ID: id}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: id, BaseRevision: d.Revision, Content: &content})
}

func (e *env) count(q string, args ...any) int {
	var n int
	if err := e.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// dump is every row of every table, as text, for "this string is nowhere" checks.
func (e *env) dump() string {
	rows, err := e.d.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		e.t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	var b strings.Builder
	for _, tb := range tables {
		r, err := e.d.Read().Query(`SELECT * FROM "` + tb + `"`)
		if err != nil {
			continue
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			for _, v := range vals {
				switch x := v.(type) {
				case []byte:
					b.Write(x)
				default:
					fmt.Fprint(&b, x)
				}
				b.WriteByte(' ')
			}
			b.WriteByte('\n')
		}
		r.Close()
	}
	return b.String()
}

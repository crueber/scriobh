package semantic_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) dbPath() string {
	var p string
	if err := e.d.Read().QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&p); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// A restore on another machine must not start sending text to an endpoint by itself.
func TestRestorePausesTheIndexAndKeepsOnlyWhatMatches(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	e.settle()
	chunksBefore := e.count(`SELECT count(*) FROM embed_chunks`)

	// The backup is taken now; the text moves on after it (as if the restored copy were older than the embeddings).
	dataDir := t.TempDir()
	res, err := backup.Create(context.Background(), e.dbPath(), dataDir, backup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Restored into an empty directory, as on a clean machine.
	target := t.TempDir()
	rr, err := backup.Restore(context.Background(), res.Path, target, backup.RestoreOptions{SecretKey: []byte("test-secret-key-test-secret-key-0123456789")})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rr.Notes, "\n")
	if !strings.Contains(joined, "Semantic search: 1 project(s)") {
		t.Fatalf("the restore did not report the index:\n%s", joined)
	}
	d2, err := db.Open(filepath.Join(target, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	var enabled int
	var reason string
	if err := d2.Read().QueryRow(`SELECT enabled, paused_reason FROM embed_projects`).Scan(&enabled, &reason); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || reason != "restore" {
		t.Fatalf("enabled=%d reason=%q: a restored index must come back off", enabled, reason)
	}
	var n int
	d2.Read().QueryRow(`SELECT count(*) FROM embed_chunks`).Scan(&n)
	if n != chunksBefore {
		t.Fatalf("the embeddings match the restored text and are kept: %d of %d", n, chunksBefore)
	}
	_ = ids
}

func TestPauseAllDropsEmbeddingsThatDoNotMatchTheText(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	e.settle()
	// One document's text differs from the text its embeddings were made from.
	if _, err := e.d.Writer().Exec(`UPDATE documents SET content = 'Different text.', content_hash = 'other-hash' WHERE id = ?`, ids["The Forge"]); err != nil {
		t.Fatal(err)
	}
	paused, kept, dropped, err := semantic.PauseAll(context.Background(), e.d)
	if err != nil || paused != 1 || dropped != 1 || kept < 3 {
		t.Fatalf("paused=%d kept=%d dropped=%d err=%v", paused, kept, dropped, err)
	}
	if e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Forge"]) != 0 {
		t.Fatal("embeddings of a text that is not the document's were kept")
	}
	if e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Harbour"]) == 0 {
		t.Fatal("embeddings that match were dropped")
	}
	// Nothing is sent, and nothing is searched, until the owner switches it on.
	calls := f.calls.Load()
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("a restored index sent text by itself")
	}
	if _, serr := e.search(e.editor, "harbour"); serr == nil || !strings.Contains(serr.Message, "restored") {
		t.Fatalf("%v", serr)
	}
	if st := e.status(e.owner); st.State != "paused" || st.Reason != "restore" || st.Enabled {
		t.Fatalf("%+v", st)
	}
	// The owner switches it on again from a fresh preview; only the dropped document is embedded.
	base := f.textCount()
	e.turnOn(pid)
	e.settle()
	got := f.textCount() - base
	if got < 1 || got > 2 {
		t.Fatalf("resuming sent %d texts; one document needed embedding", got)
	}
	if st := e.status(e.owner); st.State != "ready" {
		t.Fatalf("%+v", st)
	}
}

func TestPauseAllDropsEmbeddingsMadeUnderOlderRules(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	if _, err := e.d.Writer().Exec(`UPDATE embed_docs SET index_version = 0`); err != nil {
		t.Fatal(err)
	}
	if _, _, dropped, err := semantic.PauseAll(context.Background(), e.d); err != nil || dropped == 0 {
		t.Fatalf("dropped %d: %v", dropped, err)
	}
	if e.count(`SELECT count(*) FROM embed_chunks`) != 0 {
		t.Fatal("embeddings made under older rules were kept")
	}
}

func TestBudgetCapsAndUsageAreRecorded(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	// Every embedding request is in the run log and the day's usage, with tokens and no text.
	if n := e.count(`SELECT count(*) FROM ai_runs WHERE recipe = 'embed.index' AND status = 'ok'`); n == 0 {
		t.Fatal("no run recorded for the index")
	}
	var tokens, runs int
	e.d.Read().QueryRow(`SELECT COALESCE(SUM(input_tokens),0), COALESCE(SUM(runs),0) FROM ai_usage_daily WHERE project_id = ?`, e.project).Scan(&tokens, &runs)
	if tokens == 0 || runs == 0 {
		t.Fatalf("usage: %d tokens, %d runs", tokens, runs)
	}
	rows, _ := e.d.Read().Query(`SELECT scope_json, payload_hash, output_hash FROM ai_runs WHERE recipe LIKE 'embed.%'`)
	for rows.Next() {
		var scope, ph, oh string
		rows.Scan(&scope, &ph, &oh)
		if strings.Contains(scope, "harbour") || strings.Contains(scope, "forge") || ph != "" || oh != "" {
			t.Fatalf("a run row holds text or a hash of it: %s", scope)
		}
	}
	rows.Close()
	e.search(e.editor, "forge")
	if e.count(`SELECT count(*) FROM ai_runs WHERE recipe = 'embed.query' AND user_id = ?`, e.editor.UserID) != 1 {
		t.Fatal("a search is charged to the person who asked")
	}

	// A token cap on the project stops the index and a search, with the cap's words, and nothing is sent.
	e.call(e.admin, "ai.budget.set", aicore.BudgetSetIn{Scope: "project", ScopeID: e.project, MaxTokens: 1})
	calls := f.calls.Load()
	if _, err := e.search(e.editor, "forge hammer"); err == nil || !strings.Contains(err.Message, "limit") {
		t.Fatalf("a search under an exhausted cap: %v", err)
	}
	e.doc("More", "scene", "", para("words after the cap", 30))
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("text was sent over the cap")
	}
	st := e.status(e.owner)
	if st.State == "ready" || st.QueueDepth == 0 {
		t.Fatalf("%+v", st)
	}
	if st.LastError != "budget_exceeded" {
		t.Fatalf("last error %q", st.LastError)
	}
}

// One document the endpoint refuses does not stop the others, and is tried again when it changes.
func TestOneRefusedDocumentDoesNotStopTheRest(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	f.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if strings.Contains(string(body), "anvil") {
			http.Error(w, `{"error":"input too long"}`, 400)
			return true
		}
		return false
	})
	e.turnOn(pid)
	e.settle()
	st := e.status(e.owner)
	if st.Failed != 1 || st.State != "ready" || st.Chunks == 0 {
		t.Fatalf("%+v", st)
	}
	if e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Forge"]) != 0 {
		t.Fatal("the refused document has chunks")
	}
	if e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Orchard"]) == 0 {
		t.Fatal("the others were not embedded")
	}
	// Not tried again while it is the same text.
	calls := f.calls.Load()
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("a refused document was sent again unchanged")
	}
	// New text is a new try.
	e.update(ids["The Forge"], para("forge fire coal bellows", 30))
	e.settle()
	if e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Forge"]) == 0 {
		t.Fatal("changed text was not tried again")
	}
}

func TestKeyAndEndpointWordsNeverSurface(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	e.turnOn(pid)
	e.settle()
	// The key reached the endpoint in the header and nowhere else.
	f.mu.Lock()
	auth := strings.Join(f.auth, ",")
	f.mu.Unlock()
	if !strings.Contains(auth, planted) {
		t.Fatal("the key was not sent as the credential")
	}
	all := fmtJSON(pv) + fmtJSON(e.status(e.owner)) + fmtJSON(e.status(e.editor)) + e.log.String()
	out, _ := e.search(e.editor, "forge")
	all += fmtJSON(out)
	if strings.Contains(all, planted) {
		t.Fatal("the provider's key is in an answer or the log")
	}
	// Where a local endpoint is, is for administrators: not an editor, and not the project's owner, who could
	// otherwise read the address of every instance provider off a preview.
	for _, who := range []ops.Principal{e.editor, e.owner} {
		if st := e.status(who); st.Endpoint == nil || st.Endpoint.Host != "" || !st.Endpoint.Local {
			t.Fatalf("%s was shown the endpoint's address or not told it is local: %+v", who.Name, st.Endpoint)
		}
	}
	if st := e.status(e.admin); st.Endpoint == nil || st.Endpoint.Host == "" {
		t.Fatal("an administrator is shown the endpoint")
	}
	if strings.Contains(fmtJSON(pv), "127.0.0.1") {
		t.Fatalf("the preview shows a local address to a writer: %s", fmtJSON(pv.Endpoint))
	}
	_ = ops.CodeForbidden
}

package semantic_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func vj(n int, v string) string {
	p := make([]string, n)
	for i := range p {
		p[i] = v
	}
	return "[" + strings.Join(p, ",") + "]"
}

// hostileEnv has an enabled index over a small book and an endpoint that will misbehave when told to.
func hostileEnv(t *testing.T, mut func(*aicore.Options)) (*env, *fakeEmbed, map[string]string) {
	e := newEnv(t, semantic.Config{BackoffBase: 30 * time.Second}, mut)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	return e, f, ids
}

func (e *env) nothingStored(t *testing.T) {
	t.Helper()
	if n := e.count(`SELECT count(*) FROM embed_chunks`); n != 0 {
		t.Fatalf("%d chunks were stored from a bad answer", n)
	}
}

func TestHostileAnswersStoreNothingAndBackOff(t *testing.T) {
	cases := []struct {
		name  string
		reply func(w http.ResponseWriter, r *http.Request, body []byte) bool
		// docLevel answers make a document fail; the rest stop the project.
	}{
		{"not json", func(w http.ResponseWriter, r *http.Request, b []byte) bool { fmt.Fprint(w, "<html>"); return true }},
		{"wrong number of vectors", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			fmt.Fprintf(w, `{"embeddings":[%s]}`, vj(96, "1"))
			return true
		}},
		{"wrong dimension within a batch", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			var in struct{ Input []string }
			json.Unmarshal(b, &in)
			var vs []string
			for i := range in.Input {
				vs = append(vs, vj(96-i%2, "1"))
			}
			fmt.Fprintf(w, `{"embeddings":[%s]}`, strings.Join(vs, ","))
			return true
		}},
		{"a vector with a number that is not finite", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			var in struct{ Input []string }
			json.Unmarshal(b, &in)
			var vs []string
			for range in.Input {
				vs = append(vs, vj(96, "1e39"))
			}
			fmt.Fprintf(w, `{"embeddings":[%s]}`, strings.Join(vs, ","))
			return true
		}},
		{"a vector of zeros", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			var in struct{ Input []string }
			json.Unmarshal(b, &in)
			var vs []string
			for range in.Input {
				vs = append(vs, vj(96, "0"))
			}
			fmt.Fprintf(w, `{"embeddings":[%s]}`, strings.Join(vs, ","))
			return true
		}},
		{"a huge body", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			w.Write([]byte(`{"embeddings":[`))
			for i := 0; i < 2000; i++ {
				w.Write([]byte(vj(256, "0.123456789") + ","))
			}
			return true
		}},
		{"a server error", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			http.Error(w, "SECRET_RESPONSE_WORDS", 503)
			return true
		}},
		{"a bad key", func(w http.ResponseWriter, r *http.Request, b []byte) bool {
			http.Error(w, `{"error":"SECRET_RESPONSE_WORDS"}`, 401)
			return true
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := hostileEnv(t, func(o *aicore.Options) { o.Config.MaxResponseBytes = 256 << 10 })
			f.setHostile(tc.reply)
			e.settle()
			e.nothingStored(t)
			st := e.status(e.owner)
			if st.State == "ready" || st.QueueDepth == 0 && st.Failed == 0 {
				t.Fatalf("a bad answer must leave the project not ready: %+v", st)
			}
			if st.Indexed != 1 && st.Chunks != 0 { // only the empty initial document may count as done
				t.Fatalf("%+v", st)
			}
			// What was said by the endpoint, and the key, are nowhere.
			if l := e.log.String(); strings.Contains(l, "SECRET_RESPONSE_WORDS") || strings.Contains(l, planted) {
				t.Fatalf("the log holds the endpoint's words or the key:\n%s", l)
			}
			// No hammering: a second pass in the back-off sends nothing.
			calls := f.calls.Load()
			e.settle()
			e.settle()
			if st.State == "backoff" && f.calls.Load() != calls {
				t.Fatalf("the worker sent %d more requests during a back-off", f.calls.Load()-calls)
			}
		})
	}
}

func TestBackoffGrowsThenRecovers(t *testing.T) {
	e := newEnv(t, semantic.Config{BackoffBase: 40 * time.Millisecond, BackoffMax: 400 * time.Millisecond}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	f.setHostile(func(w http.ResponseWriter, r *http.Request, b []byte) bool { http.Error(w, "down", 503); return true })
	e.turnOn(pid)
	var waits []time.Duration
	for i := 0; i < 3; i++ {
		e.settle()
		var next, at string
		var fails int
		if err := e.d.Read().QueryRow(`SELECT next_attempt_at, last_error_at, fail_count FROM embed_projects`).Scan(&next, &at, &fails); err != nil {
			t.Fatal(err)
		}
		if fails != i+1 {
			t.Fatalf("pass %d: %d failures", i, fails)
		}
		a, _ := time.Parse("2006-01-02T15:04:05.000Z", at)
		n, _ := time.Parse("2006-01-02T15:04:05.000Z", next)
		waits = append(waits, n.Sub(a))
		time.Sleep(time.Until(n) + 20*time.Millisecond)
	}
	if !(waits[1] > waits[0] && waits[2] > waits[1]) {
		t.Fatalf("the wait should double: %v", waits)
	}
	st := e.status(e.owner)
	if st.State != "backoff" && st.State != "indexing" || st.LastError != "unavailable" {
		t.Fatalf("%+v", st)
	}
	if st := e.status(e.editor); st.LastError != "error" {
		t.Fatalf("an editor is not told the class of a network failure: %q", st.LastError)
	}
	// The endpoint comes back: the next pass succeeds and clears the failure.
	f.setHostile(nil)
	e.settle()
	st = e.status(e.owner)
	if st.State != "ready" || st.FailCount != 0 || st.LastError != "" || st.Chunks == 0 {
		t.Fatalf("after recovery: %+v", st)
	}
	_ = ids
}

func TestRedirectIsNotFollowed(t *testing.T) {
	var hits atomic.Int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(internal.Close)
	e, f, _ := hostileEnv(t, nil)
	f.setHostile(func(w http.ResponseWriter, r *http.Request, b []byte) bool {
		http.Redirect(w, r, internal.URL+"/private", http.StatusTemporaryRedirect)
		return true
	})
	e.settle()
	e.nothingStored(t)
	if hits.Load() != 0 {
		t.Fatal("a redirect to another address was followed, with the manuscript in its body")
	}
}

func TestSlowEndpointTimesOut(t *testing.T) {
	e, f, _ := hostileEnv(t, nil)
	one := 1
	e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: e.providerID(), TimeoutSeconds: &one})
	// The update changed the provider's address key? No: only the timeout. The index still holds.
	f.setHostile(func(w http.ResponseWriter, r *http.Request, b []byte) bool {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
		return true
	})
	start := time.Now()
	e.settle()
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 4*time.Second {
			t.Errorf("the pass waited %v on a slow endpoint", took)
		}
	})
	e.nothingStored(t)
	st := e.status(e.owner)
	if st.LastError != "timeout" || st.FailCount == 0 {
		t.Fatalf("%+v", st)
	}
}

func (e *env) providerID() string {
	var id string
	if err := e.d.Read().QueryRow(`SELECT provider_id FROM embed_projects WHERE project_id = ?`, e.project).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// The model behind the endpoint changes its vector size under the same name.
func TestModelThatChangesItsDimensionPausesTheIndex(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	e.settle()
	before := e.count(`SELECT count(*) FROM embed_chunks`)
	f.dim.Store(48)
	e.update(ids["The Harbour"], para("new words about the sea", 40))
	e.settle()
	st := e.status(e.owner)
	if st.State != "paused" || st.Reason != "model_changed" {
		t.Fatalf("%+v", st)
	}
	if got := e.count(`SELECT count(*) FROM embed_chunks WHERE dim = 48`); got != 0 {
		t.Fatalf("%d chunks of a different size were mixed into the index", got)
	}
	if e.count(`SELECT count(*) FROM embed_chunks`) > before {
		t.Fatal("the index grew")
	}
	if _, err := e.search(e.editor, "forge"); err == nil || err.Code != ops.CodeConflict {
		t.Fatalf("a paused index answered: %v", err)
	}
	calls := f.calls.Load()
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("a paused index kept sending")
	}
	// Rebuild lifts the pause and builds the index again at the new size.
	e.call(e.owner, "search.semantic.rebuild", semantic.ProjectIn{ProjectID: e.project})
	e.settle()
	st = e.status(e.owner)
	if st.State != "ready" || st.Dim != 48 {
		t.Fatalf("%+v", st)
	}
	if out, err := e.search(e.editor, "forge hammer anvil"); err != nil || len(out.Items) == 0 || out.Items[0].DocumentID != ids["The Forge"] {
		t.Fatalf("%v %+v", err, out.Items)
	}
}

// The change is noticed by a search too, with nothing edited.
func TestSearchNoticesAModelThatChanged(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	f.dim.Store(48)
	_, err := e.search(e.editor, "forge hammer")
	wantCode(t, err, ops.CodeConflict)
	if st := e.status(e.owner); st.State != "paused" || st.Reason != "model_changed" {
		t.Fatalf("%+v", st)
	}
}

func TestOnlyChunkSizedTextsAreSent(t *testing.T) {
	e, f, _ := hostileEnv(t, nil)
	e.settle()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.texts {
		if n := len(strings.Fields(s)); n > semantic.ChunkMaxWords {
			t.Fatalf("a text of %d words was sent", n)
		}
	}
}

func TestEndpointFailuresNeverBlockTheEditorialPath(t *testing.T) {
	e, f, ids := hostileEnv(t, nil)
	f.setHostile(func(w http.ResponseWriter, r *http.Request, b []byte) bool { http.Error(w, "down", 500); return true })
	e.settle()
	// Everything editorial still works while the endpoint is down.
	e.update(ids["The Harbour"], para("saved while the endpoint is down", 20))
	if out, err := e.try(e.reader, "search.query", searchQuery(e.project, "saved while")); err != nil || out == nil {
		t.Fatalf("full-text search: %v", err)
	}
	e.doc("Written during the outage", "scene", "", "Still writes.")
}

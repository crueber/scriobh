package server

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
)

// The semantic ops over REST: path, query and body binding, who may call what (the switches are for a person in
// a browser; an API key is refused), and a search that goes through the real provider layer to a stand-in
// embedding endpoint on loopback (declared local, as an administrator would for a model on the machine).
// No real model is used.
func TestSemanticSearchOverREST(t *testing.T) {
	s := newAuthzServer(t)
	var asked atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Input []string }
		asked.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/embed" || json.Unmarshal(b, &in) != nil {
			http.NotFound(w, r)
			return
		}
		out := make([][]float64, len(in.Input))
		for i, text := range in.Input {
			if strings.Contains(text, "tinyvec") { // a misbehaving model: numbers too small to scale to unit length in float32
				tiny := make([]float64, 32)
				for j := range tiny {
					tiny[j] = 1e-40
				}
				out[i] = tiny
				continue
			}
			v := make([]float64, 32)
			for _, word := range strings.Fields(strings.ToLower(text)) {
				h := fnv.New32a()
				h.Write([]byte(word))
				v[h.Sum32()%32]++
			}
			var n float64
			for _, x := range v {
				n += x * x
			}
			if n == 0 {
				v[0], n = 1, 1
			}
			for j := range v {
				v[j] /= math.Sqrt(n)
			}
			out[i] = v
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	}))
	t.Cleanup(endpoint.Close)

	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleWriter)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	call := func(c caller, method, path, body string, want int) map[string]any {
		t.Helper()
		return send(t, s, c, method, path, body, want)
	}
	pr := call(admin, "POST", "/api/v1/ai/providers", `{"name":"Local embeddings","kind":"ollama","scope":"instance","base_url":"`+endpoint.URL+`","local":true,"default_model":"embed-a"}`, 201)
	provider := pr["item"].(map[string]any)["id"].(string)
	project := call(owner, "POST", "/api/v1/projects", `{"title":"The Salt Road"}`, 201)["item"].(map[string]any)["id"].(string)
	s.addMember(t, project, reader.userID, "reader")
	s.addMember(t, project, editor.userID, "editor")
	call(owner, "PUT", "/api/v1/projects/"+project+"/ai/policy", `{"mode":"ask"}`, 200)
	var forge, harbour string
	for title, body := range map[string]string{"The Forge": "forge anvil hammer iron sparks forge anvil hammer", "The Harbour": "harbour boats gulls tide rope harbour boats gulls"} {
		id := call(owner, "POST", "/api/v1/projects/"+project+"/docs", fmt.Sprintf(`{"title":%q,"kind":"scene","content":%q}`, title, body), 201)["item"].(map[string]any)["id"].(string)
		if title == "The Forge" {
			forge = id
		} else {
			harbour = id
		}
	}
	base := "/api/v1/projects/" + project + "/search/semantic"

	// Off: a reader is told so, and the status says off.
	call(editor, "GET", base+"?q=forge", "", 409)
	call(reader, "GET", base+"?q=forge", "", 403) // a reader's question would use the owner's provider: readers have search.query
	if st := call(owner, "GET", base+"/status", "", 200); st["state"] != "off" {
		t.Fatalf("%v", st)
	}
	// A stranger learns nothing; a reader may not see the status or switch anything on.
	call(stranger, "GET", base+"?q=forge", "", 404)
	call(reader, "GET", base+"/status", "", 403)
	call(reader, "POST", base+"/preview", fmt.Sprintf(`{"provider_id":%q,"model":"embed-a"}`, provider), 403)

	pv := call(owner, "POST", base+"/preview", fmt.Sprintf(`{"provider_id":%q,"model":"embed-a"}`, provider), 200)
	if pv["remote"] != false || pv["consent_token"] == "" || pv["chunks"].(float64) < 2 {
		t.Fatalf("preview: %v", pv)
	}
	call(owner, "POST", base+"/enable", fmt.Sprintf(`{"provider_id":%q,"model":"embed-a"}`, provider), 422) // no token
	call(owner, "POST", base+"/enable", fmt.Sprintf(`{"provider_id":%q,"model":"embed-a","consent_token":%q}`, provider, pv["consent_token"]), 200)

	// The switches are for a browser: an API key is refused at every scope.
	for _, scope := range []string{"read", "write", "admin"} {
		who := owner
		if scope == "admin" {
			who = admin // only an administrator can mint one
		}
		secret, _ := s.mintKey(t, who, `{"name":"agent `+scope+`","scope":"`+scope+`"}`)
		for _, c := range []struct{ method, path, body string }{
			{"POST", base + "/preview", fmt.Sprintf(`{"provider_id":%q,"model":"embed-a"}`, provider)},
			{"POST", base + "/enable", fmt.Sprintf(`{"provider_id":%q,"model":"embed-a","consent_token":"x"}`, provider)},
			{"POST", base + "/disable", ""}, {"POST", base + "/rebuild", ""}, {"POST", base + "/purge", ""},
		} {
			if got := s.doKey(secret, c.method, c.path, c.body); got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "browser") {
				t.Errorf("%s key: %s %s = %d %s", scope, c.method, c.path, got.Code, got.Body)
			}
		}
	}

	// Index it (the worker waits for a document to be left alone; here they are old already).
	if _, err := s.DB.Writer().Exec(`UPDATE documents SET updated_at = '2020-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	svc, ok := semantic.ServiceOf(s.Reg.Env())
	if !ok {
		t.Fatal("no service")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	svc.Settle(ctx)
	if st := call(owner, "GET", base+"/status", "", 200); st["state"] != "ready" || st["chunks"].(float64) < 2 {
		t.Fatalf("status: %v", st)
	}

	// A search over REST: query binding, limits, the result shape.
	res := call(editor, "GET", base+"?q=hammer+anvil+forge&limit=3&per_document=1", "", 200)
	items := res["items"].([]any)
	if len(items) == 0 || items[0].(map[string]any)["document_id"] != forge {
		t.Fatalf("search: %v", res)
	}
	first := items[0].(map[string]any)
	if first["snippet"] == "" || first["score"].(float64) <= 0 || first["end"].(float64) <= first["start"].(float64) {
		t.Errorf("hit: %v", first)
	}
	call(editor, "GET", base+"?q=", "", 422)
	call(editor, "GET", base+"?q="+strings.Repeat("x", 2100), "", 422)
	// Scenes like this one, from a chunk and from the document.
	sim := "/api/v1/projects/" + project + "/search/similar"
	r1 := call(reader, "GET", sim+"?document_id="+harbour+"&chunk=0&include_neighbours=true", "", 200)
	if r1["chunk"].(float64) != 0 || len(r1["items"].([]any)) == 0 {
		t.Errorf("similar: %v", r1)
	}
	call(reader, "GET", sim+"?document_id="+harbour+"&chunk=abc", "", 422)
	call(reader, "GET", sim+"?document_id="+harbour+"&chunk=99", "", 422)
	call(reader, "GET", sim, "", 422)
	call(stranger, "GET", sim+"?document_id="+harbour, "", 404)

	// A dry run of the search is refused before its handler runs: the question does not leave.
	before := asked.Load()
	if rec := s.doDry(editor, "GET", base+"?q=DRY_RUN_QUESTION", ""); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "dry_run_unsupported") {
		t.Errorf("a dry run of search.semantic: %d %s", rec.Code, rec.Body)
	}
	if asked.Load() != before {
		t.Error("a dry run of search.semantic sent the question to the endpoint")
	}
	// A question whose vector is tiny gives a clean answer: scores are finite and encodable, or a refusal that is not a 500.
	rec := s.do(editor, "GET", base+"?q=tinyvec", "")
	if rec.Code == http.StatusInternalServerError || !json.Valid(rec.Body.Bytes()) || strings.Contains(rec.Body.String(), "NaN") {
		t.Errorf("a tiny question vector: %d %s", rec.Code, rec.Body)
	}
	// An API key searches only where the project lets agents use AI.
	secret, _ := s.mintKey(t, editor, `{"name":"editor agent","scope":"read"}`)
	if got := s.doKey(secret, "GET", base+"?q=forge", ""); got.Code != http.StatusForbidden {
		t.Errorf("a key searched without the project's say-so: %d %s", got.Code, got.Body)
	}
	if got := s.doKey(secret, "GET", sim+"?document_id="+harbour, ""); got.Code != http.StatusOK {
		t.Errorf("similar sends nothing, so a read key may ask: %d %s", got.Code, got.Body)
	}

	// Trashed: nothing is sent, and a reader gets a conflict.
	call(owner, "POST", "/api/v1/projects/"+project+"/trash", "", 200)
	call(owner, "GET", base+"?q=forge", "", 409)
	call(owner, "POST", "/api/v1/projects/"+project+"/restore", "", 200)

	// Purge: everything goes.
	pg := call(owner, "POST", base+"/purge", "", 200)
	if pg["deleted_chunks"].(float64) < 2 {
		t.Errorf("purge: %v", pg)
	}
	if st := call(owner, "GET", base+"/status", "", 200); st["state"] != "off" || st["chunks"].(float64) != 0 {
		t.Errorf("status after purge: %v", st)
	}
}

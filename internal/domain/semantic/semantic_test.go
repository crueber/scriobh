package semantic_test

import (
	"fmt"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
)

var modes = []string{"ollama", "openai", "ollama_old"}

// book makes four scenes on four themes and returns their ids by theme.
func book(e *env) map[string]string {
	ids := map[string]string{}
	themes := []struct{ title, theme string }{
		{"The Harbour", "harbour boats gulls tide rope"},
		{"The Forge", "forge anvil hammer iron sparks"},
		{"The Orchard", "orchard apples blossom ladder bees"},
		{"The Library", "library scrolls candle ink shelves"},
	}
	for _, th := range themes {
		ids[th.title] = e.doc(th.title, "scene", "", para(th.theme, 40)+"\n\n"+para(th.theme, 40))
	}
	return ids
}

func TestPreviewEnableIndexAndSearch(t *testing.T) {
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t, semantic.Config{}, nil)
			f := newFakeEmbed(t, mode)
			pid := e.setup(f)
			ids := book(e)

			// Off by default: nothing is sent, and a search says so.
			if st := e.status(e.owner); st.State != "off" || st.Enabled {
				t.Fatalf("a new project is off, got %+v", st)
			}
			_, err := e.search(e.editor, "harbour")
			wantCode(t, err, ops.CodeConflict)
			if f.calls.Load() != 0 {
				t.Fatal("something was sent while the index was off")
			}

			pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
			// An administrator is the only one shown a local endpoint's address.
			if apv := e.call(e.admin, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut); apv.Endpoint.Host == "" {
				t.Fatalf("an administrator is not shown the endpoint: %+v", apv.Endpoint)
			}
			if pv.Blocked != nil || pv.Documents != 4 || pv.Chunks < 4 || pv.Bytes <= 0 || pv.EstimatedTokens <= 0 || pv.ConsentToken == "" {
				t.Fatalf("preview: %+v", pv)
			}
			if pv.Endpoint.Name != "Embeddings" || !pv.Endpoint.Local || pv.Remote || pv.Endpoint.Model != "embed-a" || pv.Endpoint.Host != "" {
				t.Fatalf("endpoint: %+v", pv.Endpoint)
			}
			if f.calls.Load() != 0 {
				t.Fatal("a preview sent text")
			}

			st := e.call(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: pv.ConsentToken}).(semantic.StatusOut)
			if !st.Enabled {
				t.Fatalf("not enabled: %+v", st)
			}
			e.settle()
			st = e.status(e.owner)
			if st.State != "ready" || st.Indexed != st.Eligible || st.Eligible < 4 || st.QueueDepth != 0 || st.Chunks != pv.Chunks || st.Dim != fakeDim || st.IndexedAt == "" {
				t.Fatalf("after indexing: %+v (preview said %d chunks)", st, pv.Chunks)
			}
			if st.VectorBytes != int64(st.Chunks*fakeDim) {
				t.Errorf("vectors are %d bytes for %d chunks of %d: int8 is one byte a number", st.VectorBytes, st.Chunks, fakeDim)
			}

			out, serr := e.search(e.editor, "forge hammer anvil sparks")
			if serr != nil {
				t.Fatal(serr)
			}
			if len(out.Items) == 0 || out.Items[0].DocumentID != ids["The Forge"] {
				t.Fatalf("the forge scene should come first: %+v", out.Items)
			}
			top := out.Items[0]
			if top.Score < 0.5 || top.End <= top.Start || !strings.Contains(top.Snippet, "forge") || top.Title != "The Forge" || top.Kind != "scene" {
				t.Fatalf("top hit: %+v", top)
			}
			for i := 1; i < len(out.Items); i++ {
				if out.Items[i].Score > out.Items[i-1].Score {
					t.Fatal("results are not best first")
				}
			}
			// At most two chunks of a document by default.
			per := map[string]int{}
			for _, it := range out.Items {
				per[it.DocumentID]++
			}
			for id, n := range per {
				if n > 2 {
					t.Errorf("%s has %d chunks in the results", id, n)
				}
			}
			// The endpoint was reached by the route its kind uses, with the key only in the Authorization header.
			f.mu.Lock()
			paths := strings.Join(f.paths, ",")
			f.mu.Unlock()
			want := map[string]string{"ollama": "/api/embed", "ollama_old": "/api/embeddings", "openai": "/v1/embeddings"}[mode]
			if !strings.Contains(paths, want) {
				t.Errorf("paths %s do not include %s", paths, want)
			}
			if strings.Contains(f.rawBodies(), planted) {
				t.Error("the key was sent in a body")
			}
		})
	}
}

func TestSimilarScenesExcludesItselfAndNeighbours(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	// Six scenes in order: two forge scenes far apart, and a forge scene next to the first.
	order := []struct{ title, theme string }{
		{"S1 forge", "forge anvil hammer iron sparks"},
		{"S2 forge", "forge anvil hammer iron smith"}, // the neighbour of S1
		{"S3 harbour", "harbour boats gulls tide rope"},
		{"S4 orchard", "orchard apples blossom ladder bees"},
		{"S5 library", "library scrolls candle ink shelves"},
		{"S6 forge", "forge anvil hammer iron sparks smith"},
	}
	ids := map[string]string{}
	for _, s := range order {
		ids[s.title] = e.doc(s.title, "scene", "", para(s.theme, 60))
	}
	e.turnOn(pid)
	e.settle()

	out := e.call(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids["S1 forge"]}).(semantic.SimilarOut)
	if len(out.Items) == 0 {
		t.Fatal("no results")
	}
	for _, it := range out.Items {
		if it.DocumentID == ids["S1 forge"] {
			t.Error("a document is like itself: it must be left out")
		}
		if it.DocumentID == ids["S2 forge"] {
			t.Error("the next scene in reading order is left out by default")
		}
	}
	if out.Items[0].DocumentID != ids["S6 forge"] {
		t.Fatalf("the far forge scene is the closest one: %+v", out.Items)
	}
	with := e.call(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids["S1 forge"], IncludeNeighbours: true}).(semantic.SimilarOut)
	found := false
	for _, it := range with.Items {
		found = found || it.DocumentID == ids["S2 forge"]
	}
	if !found {
		t.Error("include_neighbours keeps the neighbour")
	}
	// From one chunk.
	one := e.call(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids["S3 harbour"], Chunk: "0"}).(semantic.SimilarOut)
	if one.Chunk == nil || len(one.Items) == 0 {
		t.Fatalf("similar from a chunk: %+v", one)
	}
	_, err := e.try(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids["S3 harbour"], Chunk: "99"})
	wantCode(t, err, ops.CodeInvalid)
	// A similar search sends nothing.
	before := f.calls.Load()
	e.call(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids["S4 orchard"]})
	if f.calls.Load() != before {
		t.Error("search.similar sent a request")
	}
}

func ptr[T any](v T) *T { return &v }

func TestIndexFollowsEditsTrashAndRestore(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	e.settle()
	base := f.textCount()
	e.settle()
	if f.textCount() != base {
		t.Fatal("an index that is current sent text again")
	}

	// An edit is embedded again, and only that document.
	e.update(ids["The Harbour"], para("lighthouse keeper lamp storm", 50))
	e.settle()
	if got := f.textCount() - base; got < 1 || got > 4 {
		t.Fatalf("one document changed and %d texts were sent", got)
	}
	out, err := e.search(e.editor, "lighthouse keeper lamp storm")
	if err != nil || len(out.Items) == 0 || out.Items[0].DocumentID != ids["The Harbour"] {
		t.Fatalf("the edited scene should be found by its new words: %v %+v", err, out.Items)
	}

	// Trash: its embeddings go, and it is not found.
	e.call(e.owner, "doc.trash", docs.DocIn{ID: ids["The Forge"]})
	e.settle()
	if n := e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Forge"]); n != 0 {
		t.Fatalf("a trashed document keeps %d chunks", n)
	}
	out, _ = e.search(e.editor, "forge hammer anvil")
	for _, it := range out.Items {
		if it.DocumentID == ids["The Forge"] {
			t.Fatal("a trashed document was found")
		}
	}
	// Restore: embedded again.
	e.call(e.owner, "doc.restore", docs.DocIn{ID: ids["The Forge"]})
	e.settle()
	if n := e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Forge"]); n == 0 {
		t.Fatal("a restored document was not embedded again")
	}
	st := e.status(e.owner)
	if st.State != "ready" || st.QueueDepth != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestPrivateTextNeverReachesTheEndpoint(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)

	// Everything that must stay home, in a document that is otherwise embedded.
	body := strings.Join([]string{
		"The kept wording of the harbour scene stays here, for the embedding.",
		"An aside %%CANARY_PRIVATE_NOTE the author's own words%% and then more kept words follow.",
		"A comment <!-- CANARY_HTML_COMMENT --> sits inside a sentence of kept words.",
		"<scriobh-ghost>CANARY_GHOSTED deleted but kept as a ghost</scriobh-ghost> and the sentence goes on.",
		"%%CANARY_UNCLOSED_PRIVATE runs to the end of its paragraph",
		"After the unclosed note, the next paragraph is plain kept text.",
		"<scriobh-ghost>CANARY_UNPAIRED_GHOST has no closing tag",
		"The paragraph after an unpaired ghost is plain kept text too.",
		"```\nCANARY_CODE_BLOCK let x = 1\n```",
		"<div>CANARY_RAW_HTML</div>",
		"<!-- scriobh:alts v1 {\"CANARY_ALT\":\"the wording not chosen\"} -->",
	}, "\n\n")
	main := e.doc("Harbour", "scene", "", body)

	// Documents that are not embedded at all.
	e.doc("Never sent", "scene", "", "CANARY_NEVER_SEND scene text about nothing that should travel.")
	neverID := e.docID("Never sent")
	e.call(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: neverID, Exclude: true})
	nc := e.doc("Not compiled", "scene", "", "CANARY_NO_COMPILE scene text about nothing that should travel.")
	off := false
	cur := e.call(e.owner, "doc.get", docs.DocIn{ID: nc}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: nc, BaseRevision: cur.Revision, IncludeInCompile: &off})
	e.doc("A note", "note", "", "CANARY_NOTE_KIND notes are the author's working material.")
	trash := e.doc("Trashed", "scene", "", "CANARY_TRASHED scene text about nothing that should travel.")
	e.call(e.owner, "doc.trash", docs.DocIn{ID: trash})
	part := e.doc("A part", "part", "", "")
	e.call(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: part, Exclude: true})
	e.doc("Inside the excluded part", "scene", part, "CANARY_INSIDE_EXCLUDED scene text about nothing that should travel.")
	// Other tables that hold words about the manuscript: a stash and a comment.
	e.stash(main, "CANARY_STASH words cut from the scene")
	e.call(e.owner, "comment.create", review.CreateIn{ProjectID: e.project, DocumentID: main, Body: "CANARY_COMMENT a note to the author about this scene"})

	e.turnOn(pid)
	e.settle()

	got := e.recv(f)
	for _, c := range []string{"CANARY_PRIVATE_NOTE", "CANARY_HTML_COMMENT", "CANARY_GHOSTED", "CANARY_UNCLOSED_PRIVATE", "CANARY_UNPAIRED_GHOST", "CANARY_CODE_BLOCK", "CANARY_RAW_HTML",
		"CANARY_ALT", "CANARY_NEVER_SEND", "CANARY_NO_COMPILE", "CANARY_NOTE_KIND", "CANARY_TRASHED", "CANARY_INSIDE_EXCLUDED", "CANARY_STASH", "CANARY_COMMENT"} {
		if strings.Contains(got, c) {
			t.Errorf("%s reached the endpoint", c)
		}
	}
	for _, kept := range []string{"kept wording of the harbour scene", "more kept words follow", "plain kept text"} {
		if !strings.Contains(got, kept) {
			t.Errorf("the chosen wording %q was not sent", kept)
		}
	}
	// Nothing of the other documents in the index either.
	for _, title := range []string{"Never sent", "Not compiled", "A note", "Trashed", "Inside the excluded part"} {
		if n := e.count(`SELECT count(*) FROM embed_chunks c JOIN documents d ON d.id = c.document_id WHERE d.title = ?`, title); n != 0 {
			t.Errorf("%q has %d chunks", title, n)
		}
	}
	st := e.status(e.owner)
	if st.NotEmbedded["never_send"] < 2 || st.NotEmbedded["do_not_compile"] != 1 || st.NotEmbedded["notes_and_research"] != 1 {
		t.Errorf("not embedded: %+v", st.NotEmbedded)
	}
}

// A flag set after the document was embedded removes its chunks, and hides them at once.
func TestFlagAfterIndexingDeletesAndHidesChunks(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	e.settle()
	if out, _ := e.search(e.editor, "forge hammer anvil"); len(out.Items) == 0 || out.Items[0].DocumentID != ids["The Forge"] {
		t.Fatalf("setup: %+v", out.Items)
	}

	e.call(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: ids["The Forge"], Exclude: true})
	// Before the worker has run: the flag already hides it.
	out, err := e.search(e.editor, "forge hammer anvil")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out.Items {
		if it.DocumentID == ids["The Forge"] {
			t.Fatal("a document flagged never-send was shown before the worker deleted its chunks")
		}
	}
	sim, serr := e.try(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids["The Forge"]})
	wantCode(t, serr, ops.CodeConflict)
	_ = sim
	e.settle()
	if n := e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, ids["The Forge"]); n != 0 {
		t.Fatalf("%d chunks remain of a flagged document", n)
	}
	if n := e.count(`SELECT count(*) FROM embed_docs WHERE document_id = ?`, ids["The Forge"]); n != 0 {
		t.Fatalf("the record of embedding a flagged document remains")
	}
	// Do-not-compile does the same, and a flag on a parent covers its children.
	part := e.doc("Part Two", "part", "", "")
	kid := e.doc("Kid", "scene", part, para("kitchen pots pans steam", 40))
	e.settle()
	if e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, kid) == 0 {
		t.Fatal("the child was not embedded")
	}
	off := false
	cur := e.call(e.owner, "doc.get", docs.DocIn{ID: part}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: part, BaseRevision: cur.Revision, IncludeInCompile: &off})
	e.settle()
	if n := e.count(`SELECT count(*) FROM embed_chunks WHERE document_id = ?`, kid); n != 0 {
		t.Fatalf("a document inside a do-not-compile part keeps %d chunks", n)
	}
}

// Words changed after a search found a chunk are not shown from the stale chunk.
func TestSnippetIsCheckedAgainstTheCurrentText(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.turnOn(pid)
	e.settle()
	e.update(ids["The Forge"], para("completely different words about weather", 40)) // not yet re-embedded
	out, err := e.search(e.editor, "forge hammer anvil sparks")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out.Items {
		if it.DocumentID == ids["The Forge"] {
			t.Fatalf("a stale chunk was shown for text that no longer says it: %+v", it)
		}
	}
	if !out.Partial || out.Pending == 0 {
		t.Errorf("the index is behind and says so: %+v", out)
	}
}

func TestOffMeansNoSendingAndDisableKeepsTheIndex(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	ids := book(e)
	e.settle()
	if f.calls.Load() != 0 {
		t.Fatal("the worker sent text for a project that never switched the index on")
	}
	e.turnOn(pid)
	e.settle()
	chunks := e.count(`SELECT count(*) FROM embed_chunks`)
	e.call(e.owner, "search.semantic.disable", semantic.ProjectIn{ProjectID: e.project})
	calls := f.calls.Load()
	e.update(ids["The Harbour"], para("changed while off", 30))
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("text was sent while the index was disabled")
	}
	if e.count(`SELECT count(*) FROM embed_chunks`) != chunks {
		t.Fatal("disable deleted embeddings: it only stops")
	}
	if _, err := e.search(e.editor, "forge"); err == nil {
		t.Fatal("a disabled index answered a search")
	}
	if st := e.status(e.owner); st.State != "off" {
		t.Fatalf("state %s", st.State)
	}
	// Switched on again, the changed document is embedded and the others are kept.
	base := f.textCount()
	e.turnOn(pid)
	e.settle()
	if got := f.textCount() - base; got < 1 || got > 4 {
		t.Fatalf("resuming re-embedded %d texts; only the changed document needed it", got)
	}
}

func TestPurgeDeletesEverything(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	if e.count(`SELECT count(*) FROM embed_chunks`) == 0 {
		t.Fatal("nothing to purge")
	}
	out := e.call(e.owner, "search.semantic.purge", semantic.ProjectIn{ProjectID: e.project}).(semantic.PurgeOut)
	if out.DeletedChunks == 0 {
		t.Fatalf("purge: %+v", out)
	}
	for _, tb := range []string{"embed_chunks", "embed_docs", "embed_projects"} {
		if n := e.count(`SELECT count(*) FROM ` + tb); n != 0 {
			t.Errorf("%s holds %d rows after a purge", tb, n)
		}
	}
	if st := e.status(e.owner); st.State != "off" || st.Chunks != 0 {
		t.Fatalf("status after purge: %+v", st)
	}
	calls := f.calls.Load()
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("a purged index sent text")
	}
}

func TestRebuildStartsOver(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	n := f.textCount()
	e.call(e.owner, "search.semantic.rebuild", semantic.ProjectIn{ProjectID: e.project})
	if e.count(`SELECT count(*) FROM embed_chunks`) != 0 {
		t.Fatal("rebuild keeps old embeddings")
	}
	e.settle()
	if f.textCount() != 2*n {
		t.Fatalf("rebuild sent %d texts, want %d", f.textCount()-n, n)
	}
	if st := e.status(e.owner); st.State != "ready" {
		t.Fatalf("%+v", st)
	}
}

// A project purge takes the index with it.
func TestProjectPurgeCascades(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	e.call(e.owner, "project.trash", projects.StateIn{ID: e.project})
	e.call(e.owner, "project.purge", projects.PurgeIn{ID: e.project, ConfirmTitle: "The Salt Road"})
	for _, tb := range []string{"embed_chunks", "embed_docs", "embed_projects"} {
		if n := e.count(`SELECT count(*) FROM ` + tb); n != 0 {
			t.Errorf("%s holds %d rows after the project was purged", tb, n)
		}
	}
}

var _ = fmt.Sprint

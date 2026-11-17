package semantic_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
)

// threeChunkDoc is a document of three chunks (each paragraph is over the 120-word target), one canary word each.
func threeChunkDoc() string {
	return para("canaryone", 70) + "\n\n" + para("canarytwo", 70) + "\n\n" + para("canarythree", 70)
}

// onFirstRequest runs fn once, in the endpoint's handler, on the first embedding request: the moment the worker is
// in the middle of a pass and the owner acts.
func onFirstRequest(f *fakeEmbed, fn func()) {
	var n atomic.Int64
	f.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if n.Add(1) == 1 {
			fn()
		}
		return false
	})
}

// A never-send flag set while a document is being embedded must stop the rest of that document's text from leaving.
// Today the eligibility is read once, at the start of the pass, and checked again only when the result is written:
// the batches after the flag are still sent.
func TestReviewFlagSetBetweenBatchesStopsTheRest(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	x := e.doc("Chapter X", "scene", "", threeChunkDoc())
	e.turnOn(pid)
	onFirstRequest(f, func() {
		e.try(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: x, Exclude: true})
	})
	e.settle()
	got := f.received()
	if !strings.Contains(got, "canaryone") {
		t.Fatalf("test setup: the first batch should have been sent: %q", got)
	}
	if strings.Contains(got, "canarytwo") || strings.Contains(got, "canarythree") {
		t.Fatalf("text of a document flagged never-send reached the endpoint after the flag was set (%d texts in all)", f.textCount())
	}
}

// The same, for a document that is still waiting in the pass when it is flagged.
func TestReviewFlagSetOnAQueuedDocumentStopsIt(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	e.doc("A first", "scene", "", threeChunkDoc())
	y := e.doc("B queued", "scene", "", para("queuedsecret", 70))
	e.turnOn(pid)
	onFirstRequest(f, func() {
		e.try(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: y, Exclude: true})
	})
	e.settle()
	if !strings.Contains(f.received(), "canaryone") {
		t.Fatalf("test setup: the first document should have been in flight when the flag was set")
	}
	if got := f.received(); strings.Contains(got, "queuedsecret") {
		t.Fatalf("a document flagged never-send while it waited in the queue was sent (%d texts in all)", f.textCount())
	}
}

// Switching the index off is the owner withdrawing consent: nothing more may be sent once the call has returned.
func TestReviewDisableDuringAPassStopsSending(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	e.doc("Chapter X", "scene", "", threeChunkDoc())
	e.doc("Chapter Y", "scene", "", para("laterdoc", 70))
	e.turnOn(pid)
	onFirstRequest(f, func() {
		e.try(e.owner, "search.semantic.disable", semantic.ProjectIn{ProjectID: e.project})
	})
	e.settle()
	if n := f.textCount(); n > 1 {
		t.Fatalf("%d texts were sent; the owner disabled the index during the first request, so at most that one may have left", n)
	}
}

// Purge forgets the owner's consent altogether.
func TestReviewPurgeDuringAPassStopsSending(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	e.doc("Chapter X", "scene", "", threeChunkDoc())
	e.turnOn(pid)
	onFirstRequest(f, func() {
		e.try(e.owner, "search.semantic.purge", semantic.ProjectIn{ProjectID: e.project})
	})
	e.settle()
	if n := f.textCount(); n > 1 {
		t.Fatalf("%d texts were sent after the owner purged the index (one was in flight)", n)
	}
}

func (e *env) withSession(p ops.Principal, id string) ops.Principal {
	e.t.Helper()
	now := db.Now()
	_, err := e.d.Writer().Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, p.UserID, "hash-"+id, now, now, db.Format(time.Now().Add(24*time.Hour)))
	if err != nil {
		e.t.Fatal(err)
	}
	p.SessionID = id
	return p
}

// Both searches, and the status, are closed to a member whose session has not unlocked a protected project, and a
// refused search sends nothing to the endpoint; after the unlock they work. (The author's branch had no gate to test with.)
func TestReviewPassphraseGateClosesTheSemanticOps(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	a := e.doc("Harbour", "scene", "", para("harbour gulls and salt", 40))
	e.doc("Quay", "scene", "", para("quay cranes and rope", 40))
	e.turnOn(pid)
	e.settle()

	owner := e.withSession(e.owner, "ses_review_owner")
	editor := e.withSession(e.editor, "ses_review_editor")
	e.call(owner, "project.passphrase.set", projectgate.SetIn{ID: e.project, Passphrase: "a long enough passphrase"})

	calls := f.calls.Load()
	for name, run := range map[string]func(p ops.Principal) *ops.Error{
		"search.semantic": func(p ops.Principal) *ops.Error {
			_, err := e.try(p, "search.semantic", semantic.SearchIn{ProjectID: e.project, Q: "harbour gulls"})
			return err
		},
		"search.similar": func(p ops.Principal) *ops.Error {
			_, err := e.try(p, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: a, IncludeNeighbours: true})
			return err
		},
		"search.semantic.status": func(p ops.Principal) *ops.Error {
			_, err := e.try(p, "search.semantic.status", semantic.StatusIn{ProjectID: e.project})
			return err
		},
	} {
		if err := run(editor); err == nil {
			t.Errorf("%s answered a session that has not unlocked the project", name)
		} else if _, locked := ops.IsLocked(err); !locked {
			t.Errorf("%s refused with %v, not the locked answer", name, err)
		}
	}
	if f.calls.Load() != calls {
		t.Errorf("a locked caller's search reached the endpoint (%d calls)", f.calls.Load()-calls)
	}
	// A second session of the OWNER that never unlocked: the owner-only ops are closed to it too.
	ownerLocked := e.withSession(e.owner, "ses_review_owner2")
	for name, run := range map[string]func() *ops.Error{
		"preview": func() *ops.Error {
			_, err := e.try(ownerLocked, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"})
			return err
		},
		"disable": func() *ops.Error {
			_, err := e.try(ownerLocked, "search.semantic.disable", semantic.ProjectIn{ProjectID: e.project})
			return err
		},
		"rebuild": func() *ops.Error {
			_, err := e.try(ownerLocked, "search.semantic.rebuild", semantic.ProjectIn{ProjectID: e.project})
			return err
		},
		"purge": func() *ops.Error {
			_, err := e.try(ownerLocked, "search.semantic.purge", semantic.ProjectIn{ProjectID: e.project})
			return err
		},
	} {
		if err := run(); err == nil {
			t.Errorf("owner op %s answered a session that has not unlocked the project", name)
		} else if _, locked := ops.IsLocked(err); !locked {
			t.Errorf("owner op %s refused with %v, not the locked answer", name, err)
		}
	}
	if st := e.status(owner); !st.Enabled {
		t.Errorf("the index was switched off by a locked session: %+v", st)
	}
	// The owner's session unlocked it by setting the passphrase.
	if _, err := e.search(owner, "harbour gulls"); err != nil {
		t.Fatalf("the unlocked owner: %v", err)
	}
	// An editor who unlocks gets answers.
	e.call(editor, "project.unlock", projectgate.UnlockIn{ID: e.project, Passphrase: "a long enough passphrase"})
	if _, err := e.search(editor, "harbour gulls"); err != nil {
		t.Fatalf("the unlocked editor: %v", err)
	}
}

// A query vector of tiny (denormal) numbers is finite, so the client accepts it, but scaling it to unit length
// overflows float32 (1/sqrt(1e-78) is not representable): every score is NaN or Inf and the answer cannot even be
// encoded as JSON. A search must give a clean result or a clean refusal.
func TestReviewTinyQueryVectorDoesNotProduceNaNScores(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	e.doc("Harbour", "scene", "", para("harbour gulls and salt", 40))
	e.doc("Quay", "scene", "", para("quay cranes and rope", 40))
	e.turnOn(pid)
	e.settle()
	f.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		fmt.Fprintf(w, `{"embeddings":[%s]}`, vj(96, "1e-40"))
		return true
	})
	out, err := e.search(e.owner, "harbour")
	if err != nil {
		t.Logf("refused: %v", err)
		return
	}
	for _, h := range out.Items {
		if math.IsNaN(h.Score) || math.IsInf(h.Score, 0) {
			t.Fatalf("a hit has score %v", h.Score)
		}
	}
	if b, jerr := json.Marshal(out); jerr != nil {
		t.Fatalf("the result cannot be encoded: %v", jerr)
	} else {
		t.Logf("%s", b)
	}
}

// TIMING-BASED (400 ms a request against a 1 s bound; flaky on a very loaded machine).
// The worker visits projects one after another and a visit waits for its endpoint, so one project whose endpoint is
// slow (or hanging until the 60 s first-byte limit) holds every other project's index back for that long.
func TestReviewSlowEndpointOfOneProjectDoesNotStallAnother(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	slow := newFakeEmbed(t, "ollama")
	fast := newFakeEmbed(t, "ollama")
	e.loopbackLocal.Store(true)
	slowPID := e.provider(slow, "Slow", true)
	fastPID := e.provider(fast, "Fast", true)
	// project A: e.project, on the slow endpoint (a book of 6 chunks, so a pass is 6 slow requests)
	e.policy("ask", nil)
	e.doc("A book", "scene", "", threeChunkDoc()+"\n\n"+para("canaryfour", 70)+"\n\n"+para("canaryfive", 70)+"\n\n"+para("canarysix", 70))
	e.turnOn(slowPID)
	// project B on the fast one
	b := e.call(e.owner, "project.create", projects.CreateIn{Title: "Another book"}).(projects.Item).Item.ID
	mode := "ask"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: b, Mode: &mode})
	e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: b, Title: "B1", Kind: "scene", Content: para("fastbook", 30)})
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: b, ProviderID: fastPID, Model: "embed-a"}).(semantic.PreviewOut)
	e.call(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: b, ProviderID: fastPID, Model: "embed-a", ConsentToken: pv.ConsentToken})

	slow.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		time.Sleep(400 * time.Millisecond)
		return false
	})
	ctx, cancel := context.WithCancel(e.ctx)
	e.sem.Start(ctx) // the real worker loop
	defer func() { cancel(); e.sem.Wait() }()
	start := time.Now()
	for fast.textCount() == 0 && time.Since(start) < 10*time.Second {
		time.Sleep(10 * time.Millisecond)
	}
	waited := time.Since(start)
	// timing: not a budget: an isolation check. Serialised behind project A's slow endpoint the first text would take 2.4 s; the second is a margin around "it was not held", not a performance figure.
	if waited > time.Second {
		t.Fatalf("project B's first text reached its (fast) endpoint only after %v, while project A's slow endpoint (400 ms a request, 6 requests) held the worker", waited)
	}
}

// A personal provider is "yours alone" (docs/AI.md 2.5). The index uses the owner's personal provider, and so does every
// later search by every reader of the project: the reader's question goes to the owner's personal endpoint, on the
// owner's key, charged to the owner.
func TestReviewPersonalProviderIsNotUsedForOtherPeoplesQueries(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	yes := true
	e.policy("ask", func(in *aicore.PolicySetIn) { in.AllowPersonal = &yes; in.AgentsMayUse = &yes })
	pid := e.call(e.owner, "ai.provider.create", aicore.ProviderCreateIn{Name: "Mine", Kind: "ollama", Scope: "user", BaseURL: f.srv.URL, APIKey: planted, DefaultModel: "embed-a"}).(aicore.ProviderItem).Item.ID
	e.doc("Harbour", "scene", "", para("harbour gulls and salt", 40))
	e.turnOn(pid)
	e.settle()
	before := f.textCount()
	out, err := e.search(e.reader, "READER_PRIVATE_QUESTION about harbour")
	if err == nil {
		t.Fatalf("a reader's question was sent to someone else's personal provider (%d hits, %d texts reached it)", len(out.Items), f.textCount()-before)
	}
}

// The project's AI policy can ask for redaction ("entities": the names and aliases of the project's entities are replaced
// by placeholders before anything is sent). Every other request that carries manuscript text honours it; the embedding
// path does not look at it, so the names of the book go to the endpoint in the clear, in the index and in every query.
func TestReviewRedactionPolicyIsHonouredByEmbeddings(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	cid := e.call(e.owner, "compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: e.project, CompendiumID: cid})
	e.call(e.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Zephyrine Quillfeather"})
	red := "entities"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Redaction: &red})
	d := e.doc("Harbour", "scene", "", "Zephyrine Quillfeather walked the quay at dawn. "+para("harbour gulls and salt", 40))
	// Control: an ordinary AI request on the same project, document and policy does redact the name. If this fails the
	// scenario is not set up (entity out of scope), and the assertion below proves nothing.
	pv := e.call(e.owner, "ai.context.preview", aicore.RunIn{ProjectID: e.project, Recipe: "freeform", DocumentID: d,
		Params: map[string]string{"instruction": "Summarise."}, Chips: []aicore.ChipSpec{{Kind: lib.ChipDocument}}}).(aicore.PreviewOut).Item
	var ctl strings.Builder
	for _, a := range pv.Attempts {
		ctl.WriteString(a.Body)
	}
	if strings.Contains(ctl.String(), "Zephyrine") || !strings.Contains(ctl.String(), "{{") {
		t.Fatalf("control: the ordinary AI preview did not redact the name: %.300s", ctl.String())
	}
	e.turnOn(pid)
	e.settle()
	e.search(e.owner, "where was Zephyrine Quillfeather at dawn")
	if got := f.received(); strings.Contains(got, "Zephyrine") {
		t.Fatalf("the project's policy asks for entity names to be redacted before sending, but the embedding endpoint received the name:\n%.300s", got)
	}
}

// Choosing another endpoint is also withdrawing consent from the old one (the owner moved away from it, perhaps from a
// remote service to a local one). The pass in flight goes on sending to the old endpoint.
func TestReviewSwitchingEndpointDuringAPassStopsTheOldOne(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	old := newFakeEmbed(t, "ollama")
	nw := newFakeEmbed(t, "ollama")
	e.loopbackLocal.Store(true)
	oldPID := e.provider(old, "Old", true)
	newPID := e.provider(nw, "New", true)
	e.policy("ask", nil)
	e.doc("Chapter X", "scene", "", threeChunkDoc())
	e.turnOn(oldPID)
	onFirstRequest(old, func() {
		pv, err := e.try(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: newPID, Model: "embed-a"})
		if err != nil {
			t.Errorf("preview: %v", err)
			return
		}
		_, err = e.try(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: newPID, Model: "embed-a", ConsentToken: pv.(semantic.PreviewOut).ConsentToken})
		if err != nil {
			t.Errorf("enable: %v", err)
		}
	})
	e.settle()
	if n := old.textCount(); n > 1 {
		t.Fatalf("%d texts reached the old endpoint; the owner switched to another one during its first request", n)
	}
}

// skipReason and liveEligible walk at most 64 ancestors (maxDepth). The outline has no depth limit, so a document more
// than 64 levels below a never-send, do-not-compile or trashed ancestor is embedded: the flag "applies down the tree"
// only to depth 64.
func TestReviewNeverSendFlagOnADeepAncestorStillApplies(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	root := e.doc("Root", "part", "", "")
	parent := root
	for i := 0; i < 70; i++ {
		parent = e.doc(fmt.Sprintf("Level %d", i), "part", parent, "")
	}
	e.doc("Deep scene", "scene", parent, "DEEP_CANARY "+para("buried words", 40))
	e.call(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: root, Exclude: true})
	e.turnOn(pid)
	e.settle()
	if got := f.received(); strings.Contains(got, "DEEP_CANARY") {
		t.Fatalf("a scene 71 levels below a never-send document reached the endpoint")
	}
}

// CanonText runs first and cuts a %% that is never closed to the end of its paragraph; front matter is only
// recognised by text.SearchBody afterwards, and by then the closing --- is gone. The keys before the stray %% are
// sent as if they were the first lines of the scene.
func TestReviewFrontMatterWithAStrayPrivateMarkerIsNotSent(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	e.doc("Scene", "scene", "", "---\ntitle: FM_CANARY\nnote: %%oops\n---\n\nThe kept text of the scene. "+para("harbour gulls", 40))
	e.turnOn(pid)
	e.settle()
	if got := f.received(); strings.Contains(got, "FM_CANARY") {
		t.Fatalf("front matter reached the endpoint: %.200s", got)
	}
}

// A dry run of the search is refused (NoDryRun) and sends nothing; a dry run of similar reads only.
func TestReviewDryRunSendsNothing(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	a := e.doc("Harbour", "scene", "", para("harbour gulls and salt", 40))
	e.doc("Quay", "scene", "", para("quay cranes and rope", 40))
	e.turnOn(pid)
	e.settle()
	calls := f.calls.Load()
	if _, err := e.reg.CallDry(e.ctx, e.owner, "search.semantic", semantic.SearchIn{ProjectID: e.project, Q: "DRY_RUN_QUESTION"}); err == nil {
		t.Errorf("a dry run of search.semantic was accepted")
	}
	if _, err := e.reg.CallDry(e.ctx, e.owner, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: a, IncludeNeighbours: true}); err != nil {
		t.Errorf("a dry run of search.similar: %v", err)
	}
	if f.calls.Load() != calls || strings.Contains(f.rawBodies(), "DRY_RUN_QUESTION") {
		t.Errorf("a dry run reached the endpoint")
	}
}

// Odd table shapes: a ghost, a private note or an HTML comment in a cell, in rows the parser does not make a table of,
// in cells past the header, unclosed, in a list or a quote. PASSES today: ghosts in rows are swept by StripGhosts
// (stripGhostsInRows), and private notes and comments are caught by the global %% cut and by SearchBody; the row sweep
// cutHiddenInRows is only called from plainmap.go and is not what protects this path.
func TestReviewTableCellShapesNeverReachTheEndpoint(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	G, GC := "<scriobh-ghost>", "</scriobh-ghost>"
	shapes := []string{
		"| a | b |\n|---|---|\n| keep | %%CANARY%% |",
		"| a | b |\n|---|---|\n| keep | " + G + "CANARY" + GC + " |",
		"| a | b |\n|---|---|\n| keep | <!-- CANARY --> |",
		"| a | b |\n|---|---|\n| keep | ok | %%CANARY%% | " + G + "CANARY" + GC + " |",
		"a | b\n--|--\nkeep | %%CANARY%%",
		"a | b\n--|--\nkeep | " + G + "CANARY" + GC,
		"| a | b |\n|---|---|\n| keep | %%CANARY | c |",
		"| a | b |\n|---|---|\n| keep | " + G + "CANARY | c |",
		"| a | b |\n|---|---|\n| keep \\| %%CANARY%% | c |",
		"| a | b |\n|---|---|\n| keep %%CANARY | still %% | c |",
		"|---|---|\n| keep | %%CANARY%% |",
		"    | a | b |\n    |---|---|\n    | keep | %%CANARY%% |",
		"- item\n\n  | a | b |\n  |---|---|\n  | keep | %%CANARY%% |",
		"> | a | b |\n> |---|---|\n> | keep | %%CANARY%% |",
		"| a |\n|---|\n| ![%%CANARY%%](x.png) |",
		"| a " + G + "CANARY" + GC + " | b |\n|---|---|\n| k | v |",
		"| a %%CANARY%% | b |\n|---|---|\n| k | v |",
	}
	for i, body := range shapes {
		e.doc(fmt.Sprintf("T%d", i), "scene", "", "Opening kept words.\n\n"+body+"\n\nClosing kept words.")
	}
	e.turnOn(pid)
	e.settle()
	if got := f.received(); strings.Contains(got, "CANARY") {
		t.Fatalf("a table shape leaked: %.300s", got)
	}
}

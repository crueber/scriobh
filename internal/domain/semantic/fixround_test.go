package semantic_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
)

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gave up waiting for %s", what)
}

// ---- S1: the check before every request ------------------------------------------------------------

// A document edited while it is being embedded: the rest of the old text must not go (the author may have made a
// paragraph private).
func TestEditedMidDocumentStopsTheRestOfTheOldText(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	x := e.doc("Chapter X", "scene", "", threeChunkDoc())
	e.turnOn(pid)
	onFirstRequest(f, func() {
		e.update(x, para("canaryone", 70)+"\n\n%%"+para("canarytwo", 70)+"%%\n\n"+para("canarythree", 70))
	})
	e.settle()
	if got := f.received(); strings.Contains(got, "canarytwo") {
		t.Fatalf("text that was made private during the pass was sent: %d texts", f.textCount())
	}
}

// A document trashed, and a project trashed, during a pass.
func TestTrashedMidPassStopsSending(t *testing.T) {
	for _, what := range []string{"document", "project"} {
		t.Run(what, func(t *testing.T) {
			e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
			f := newFakeEmbed(t, "ollama")
			pid := e.setup(f)
			x := e.doc("Chapter X", "scene", "", threeChunkDoc())
			e.turnOn(pid)
			onFirstRequest(f, func() {
				if what == "document" {
					e.try(e.owner, "doc.trash", docs.DocIn{ID: x})
				} else {
					e.try(e.owner, "project.trash", projects.StateIn{ID: e.project})
				}
			})
			e.settle()
			if n := f.textCount(); n > 1 {
				t.Fatalf("%d texts were sent after the %s was trashed", n, what)
			}
		})
	}
}

// The project's policy turned off, or its redaction turned on, during a pass.
func TestPolicyOrRedactionChangedMidPassStopsSending(t *testing.T) {
	for _, what := range []string{"policy off", "redaction on"} {
		t.Run(what, func(t *testing.T) {
			e := newEnv(t, semantic.Config{Parallel: 1, BatchSize: 1}, nil)
			f := newFakeEmbed(t, "ollama")
			pid := e.setup(f)
			e.doc("Chapter X", "scene", "", threeChunkDoc())
			e.turnOn(pid)
			onFirstRequest(f, func() {
				if what == "policy off" {
					off := "off"
					e.try(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &off})
				} else {
					red := "entities"
					e.try(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Redaction: &red})
				}
			})
			e.settle()
			if n := f.textCount(); n > 1 {
				t.Fatalf("%d texts were sent after the owner changed the policy (%s)", n, what)
			}
		})
	}
}

// A search's question: the index switched off between the check and the send.
func TestTheQuestionIsNotSentIfTheIndexWasSwitchedOff(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	calls := f.calls.Load()
	// The guard runs just before the request leaves; the index goes off in the same instant the call is made.
	e.call(e.owner, "search.semantic.disable", semantic.ProjectIn{ProjectID: e.project})
	if _, err := e.search(e.editor, "QUESTION_AFTER_DISABLE"); err == nil {
		t.Fatal("a disabled index answered")
	}
	if f.calls.Load() != calls || strings.Contains(f.rawBodies(), "QUESTION_AFTER_DISABLE") {
		t.Fatal("the question was sent")
	}
}

// ---- S2: redaction ---------------------------------------------------------------------------------

func (e *env) redactEntity(name string) string {
	e.t.Helper()
	cid := e.call(e.owner, "compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	e.call(e.owner, "project.compendium.set", compendiums.SetIn{ID: e.project, CompendiumID: cid})
	e.call(e.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: name})
	return cid
}

// Redaction switched on after the index was built: every document is embedded again, with the names hidden; switching
// it off embeds them again plainly; a new name is hidden from then on.
func TestAChangeOfTheRedactionSettingEmbedsAgain(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	cid := e.redactEntity("Zephyrine Quillfeather")
	e.doc("Harbour", "scene", "", "Zephyrine Quillfeather walked the quay at dawn. "+para("harbour gulls and salt", 40))
	e.doc("Quay", "scene", "", "Barnaby Thistlewood met her there. "+para("quay cranes and rope", 40))
	e.turnOn(pid)
	e.settle()
	if !strings.Contains(f.received(), "Zephyrine") {
		t.Fatal("setup: without redaction the name is sent")
	}
	base := f.textCount()
	red := "entities"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Redaction: &red})
	if st := e.status(e.owner); st.QueueDepth == 0 {
		t.Fatalf("a changed redaction setting leaves the index current: %+v", st)
	}
	e.settle()
	after := f.received()
	tail := after[len(after)-300:]
	if f.textCount() <= base || strings.Contains(tail, "Zephyrine") {
		t.Fatalf("the documents were not embedded again with the name hidden (%d new texts): %q", f.textCount()-base, tail)
	}
	if st := e.status(e.owner); st.QueueDepth != 0 || st.State != "ready" {
		t.Fatalf("%+v", st)
	}
	// A name added to the Bible later changes what must be hidden: embedded again, hidden.
	e.settle()
	n := f.textCount()
	e.settle()
	if f.textCount() != n {
		t.Fatal("a current index sent text again")
	}
	if !strings.Contains(f.received(), "Barnaby") {
		t.Fatal("setup: the name was not yet an entity, so it was sent")
	}
	e.call(e.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Barnaby Thistlewood"})
	if st := e.status(e.owner); st.QueueDepth == 0 {
		t.Fatalf("a new name to hide leaves the index current: %+v", st)
	}
	seen := strings.Count(f.received(), "Barnaby")
	e.settle()
	if strings.Count(f.received(), "Barnaby") != seen {
		t.Fatal("the new name was sent again, though it is an entity now")
	}
	// Off again: plain.
	off := "off"
	hidden := strings.Count(f.received(), "Zephyrine")
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Redaction: &off})
	e.settle()
	if strings.Count(f.received(), "Zephyrine") <= hidden {
		t.Fatal("with redaction off again the text is sent as it is")
	}
}

// ---- S4 --------------------------------------------------------------------------------------------

func TestFrontMatterIsGoneBeforeAnythingElseIsCut(t *testing.T) {
	for name, content := range map[string]string{
		"stray marker":     "---\ntitle: FM_ONE\nnote: %%oops\n---\n\nThe kept text. " + para("harbour gulls", 30),
		"crlf":             "---\r\ntitle: FM_TWO\r\nnote: %%oops\r\n---\r\n\r\nThe kept text. " + para("harbour gulls", 30),
		"marker pairs up":  "---\ntitle: FM_THREE %%a\n---\n\nThe kept text. " + para("harbour gulls", 30) + " %%a private note%% after.",
		"ghost in the key": "---\ntitle: <scriobh-ghost>FM_FOUR\n---\n\nThe kept text. " + para("harbour gulls", 30),
	} {
		t.Run(name, func(t *testing.T) {
			if got := semantic.EmbedBody(content); strings.Contains(got, "FM_") || !strings.Contains(got, "The kept text") {
				t.Fatalf("%q", got)
			}
		})
	}
	// The same text without front matter keeps what comes after a closed pair.
	if got := semantic.EmbedBody("---\ntitle: FM_X %%a\n---\n\nKept. %%gone%% After."); !strings.Contains(got, "After") {
		t.Fatalf("the text after a private note was lost: %q", got)
	}
}

// ---- S7: one slow project holds only itself --------------------------------------------------------

// two projects on two endpoints; the first endpoint blocks until the test says
func twoProjects(t *testing.T, cfg semantic.Config) (e *env, slow, fast *fakeEmbed, slowProject, fastProject string) {
	e = newEnv(t, cfg, nil)
	slow, fast = newFakeEmbed(t, "ollama"), newFakeEmbed(t, "ollama")
	e.loopbackLocal.Store(true)
	slowPID := e.provider(slow, "Slow", true)
	fastPID := e.provider(fast, "Fast", true)
	e.policy("ask", nil)
	e.doc("A book", "scene", "", threeChunkDoc())
	e.turnOn(slowPID)
	fastProject = e.call(e.owner, "project.create", projects.CreateIn{Title: "Another book"}).(projects.Item).Item.ID
	mode := "ask"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: fastProject, Mode: &mode})
	e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: fastProject, Title: "B1", Kind: "scene", Content: para("fastbook", 30)})
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: fastProject, ProviderID: fastPID, Model: "embed-a"}).(semantic.PreviewOut)
	e.call(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: fastProject, ProviderID: fastPID, Model: "embed-a", ConsentToken: pv.ConsentToken})
	return e, slow, fast, e.project, fastProject
}

// blockUntil makes an endpoint hold every request until release is closed or the request is cancelled.
func blockUntil(f *fakeEmbed, release chan struct{}, started chan struct{}) {
	f.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return false
	})
}

func TestADeadEndpointHoldsOnlyItsOwnProject(t *testing.T) {
	e, slow, fast, _, _ := twoProjects(t, semantic.Config{Parallel: 1, BatchSize: 1, ProjectWorkers: 2, ProjectTimeout: time.Minute})
	release, started := make(chan struct{}), make(chan struct{}, 1)
	blockUntil(slow, release, started)
	ctx, cancel := context.WithCancel(e.ctx)
	e.sem.Start(ctx)
	defer func() { close(release); cancel(); e.sem.Wait() }()
	<-started // the first project's request is out and is not answered
	waitFor(t, "the other project's text to reach its endpoint while the first is held", 20*time.Second, func() bool { return fast.textCount() > 0 })
	select {
	case <-release:
		t.Fatal("setup")
	default:
	}
}

// With a single place, a visit that does not end is ended by its deadline, the project waits in back-off, and the
// next project is visited. A project in back-off is skipped, not waited for.
func TestAVisitThatDoesNotEndIsEndedAndTheNextProjectGoes(t *testing.T) {
	e, slow, fast, slowProject, _ := twoProjects(t, semantic.Config{Parallel: 1, BatchSize: 1, ProjectWorkers: 1, ProjectTimeout: 300 * time.Millisecond, BackoffBase: time.Hour})
	release, started := make(chan struct{}), make(chan struct{}, 8)
	blockUntil(slow, release, started)
	ctx, cancel := context.WithCancel(e.ctx)
	e.sem.Start(ctx)
	defer func() { close(release); cancel(); e.sem.Wait() }()
	waitFor(t, "the other project's text", 20*time.Second, func() bool { return fast.textCount() > 0 })
	waitFor(t, "the held project to be in back-off", 20*time.Second, func() bool {
		var fails int
		var class string
		e.d.Read().QueryRow(`SELECT fail_count, last_error_class FROM embed_projects WHERE project_id = ?`, slowProject).Scan(&fails, &class)
		return fails > 0 && class == "timeout"
	})
	n := slow.calls.Load()
	// More waking does not visit a project that is in back-off.
	for i := 0; i < 20; i++ {
		e.sem.Wake()
		time.Sleep(10 * time.Millisecond)
	}
	if slow.calls.Load() != n {
		t.Fatalf("a project in back-off was visited again: %d requests, then %d", n, slow.calls.Load())
	}
}

// ---- S9: a restore that cannot pause leaves a marker, and nothing is sent until it is paused --------

func TestRestoreMarkerBlocksSendingUntilTheIndexesArePaused(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	marker := filepath.Join(e.dir, semantic.MarkerName)
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.doc("New", "scene", "", para("owed after the restore", 30))
	calls := f.calls.Load()
	restore := semantic.FailPause(errors.New("disk full"))
	defer restore()
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("text was sent while a restore's pause was still owed")
	}
	if _, err := e.search(e.editor, "forge"); err == nil || err.Code != ops.CodeConflict || f.calls.Load() != calls {
		t.Fatalf("a search while the pause is owed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the marker was removed though nothing was paused")
	}
	// The pause works again: it happens, the marker goes, and still nothing is sent.
	restore()
	e.settle()
	var enabled int
	var reason string
	e.d.Read().QueryRow(`SELECT enabled, paused_reason FROM embed_projects`).Scan(&enabled, &reason)
	if enabled != 0 || reason != "restore" {
		t.Fatalf("enabled=%d reason=%q", enabled, reason)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the marker stayed after the pause")
	}
	if f.calls.Load() != calls {
		t.Fatal("text was sent")
	}
}

// Through the real backup and restore: the hooks write the marker, and a pause that fails fails the restore loudly.
func TestFailedPauseFailsTheRestoreAndTheStartPausesIt(t *testing.T) {
	e := newEnv(t, semantic.Config{Poll: 10 * time.Millisecond}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	res, err := backup.Create(context.Background(), e.dbPath(), t.TempDir(), backup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	restore := semantic.FailPause(errors.New("disk full"))
	defer restore()
	_, rerr := backup.Restore(context.Background(), res.Path, target, backup.RestoreOptions{SecretKey: []byte("test-secret-key-test-secret-key-0123456789")})
	if rerr == nil || !strings.Contains(rerr.Error(), "semantic indexes are NOT paused") {
		t.Fatalf("the restore did not say the indexes are not paused: %v", rerr)
	}
	if _, err := os.Stat(filepath.Join(target, semantic.MarkerName)); err != nil {
		t.Fatalf("no marker: %v", err)
	}
	d2, err := db.Open(filepath.Join(target, "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	var enabled int
	d2.Read().QueryRow(`SELECT enabled FROM embed_projects`).Scan(&enabled)
	if enabled != 1 {
		t.Fatalf("setup: the failed pause should have left the index on (enabled=%d)", enabled)
	}
	// A document owed in the restored copy, and a service that could send it.
	if _, err := d2.Writer().Exec(`UPDATE documents SET content = content || ' changed after the backup', content_hash = 'x' || content_hash, updated_at = '2020-01-01T00:00:00.000Z' WHERE title = 'The Forge'`); err != nil {
		t.Fatal(err)
	}
	opts := aicore.Options{Box: secrets.New([]byte("test-secret-key-test-secret-key-0123456789")), AllowPlainHTTP: true, Config: aicore.Config{RatePerMinute: 100000}}
	reg := registry.BuildWith(d2, events.NewStore(d2, nil), target, registry.Options{AI: opts, Semantic: semantic.Config{Poll: 10 * time.Millisecond, Quiet: -1}})
	svc, _ := semantic.ServiceOf(reg.Env())
	calls := f.calls.Load()
	ctx, cancel := context.WithCancel(context.Background())
	svc.Start(ctx) // honours the marker, which cannot be honoured while the pause fails
	time.Sleep(300 * time.Millisecond)
	if f.calls.Load() != calls {
		cancel()
		svc.Wait()
		t.Fatal("a restored copy whose pause failed sent text at start")
	}
	restore() // the disk has room now: the next tick pauses, and removes the marker
	waitFor(t, "the pause at start", 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(target, semantic.MarkerName))
		return err != nil
	})
	d2.Read().QueryRow(`SELECT enabled FROM embed_projects`).Scan(&enabled)
	cancel()
	svc.Wait()
	if enabled != 0 || f.calls.Load() != calls {
		t.Fatalf("enabled=%d, %d new requests", enabled, f.calls.Load()-calls)
	}
}

// ---- S8: a tiny question vector gives a clean answer -----------------------------------------------

func TestTinyQueryVectorGivesFiniteScoresOrARefusal(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	e.doc("Harbour", "scene", "", para("harbour gulls and salt", 40))
	e.turnOn(pid)
	e.settle()
	for _, v := range []string{"1e-40", "1e-300", "5e-324"} {
		v := v
		f.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
			w.Write([]byte(`{"embeddings":[` + vj(96, v) + `]}`))
			return true
		})
		out, err := e.search(e.editor, "harbour")
		if err != nil {
			continue // a clean refusal
		}
		b, jerr := json.Marshal(out)
		if jerr != nil {
			t.Fatalf("%s: %v", v, jerr)
		}
		if strings.Contains(string(b), "NaN") || strings.Contains(string(b), "Inf") {
			t.Fatalf("%s: %s", v, b)
		}
	}
}

// An old Ollama (no /api/embed) is sent a batch one text at a time; the document withdrawn after the first text must
// not have the others sent, though they are all in one batch of one call.
func TestFlagMidBatchOnTheOldOllamaRoute(t *testing.T) {
	e := newEnv(t, semantic.Config{Parallel: 1}, nil) // the default batch: all three chunks in one call
	f := newFakeEmbed(t, "ollama_old")
	pid := e.setup(f)
	x := e.doc("Chapter X", "scene", "", threeChunkDoc())
	e.turnOn(pid)
	var n atomic.Int64
	f.setHostile(func(w http.ResponseWriter, r *http.Request, body []byte) bool {
		if n.Add(1) == 2 { // the probe of /api/embed was the first request; this is the first text
			e.try(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: x, Exclude: true})
		}
		return false
	})
	e.settle()
	got := f.received()
	if !strings.Contains(got, "canaryone") {
		t.Fatalf("setup: the first text should have been sent: %q", got)
	}
	if strings.Contains(got, "canarytwo") || strings.Contains(got, "canarythree") {
		t.Fatalf("the rest of a batch was sent one text at a time after the document was flagged (%d texts)", f.textCount())
	}
}

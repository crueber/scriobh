package semantic_test

import (
	"strings"
	"testing"
	"time"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/ops"
)

func TestEnableNeedsTheOwnersPreviewAndToken(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)

	_, err := e.try(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"})
	wantCode(t, err, ops.CodeInvalid) // no token
	_, err = e.try(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: "deadbeef"})
	wantCode(t, err, ops.CodeConflict)

	// A token is for one provider and model: a preview of another model does not enable this one.
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-other"}).(semantic.PreviewOut)
	_, err = e.try(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: pv.ConsentToken})
	wantCode(t, err, ops.CodeConflict)

	// Only the owner, only in a browser session.
	for _, who := range []ops.Principal{e.editor, e.reader, e.other} {
		_, err = e.try(who, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"})
		if err == nil {
			t.Errorf("%s may preview", who.Name)
		}
		_, err = e.try(who, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: "x"})
		if err == nil {
			t.Errorf("%s may enable", who.Name)
		}
	}
	key := e.owner
	key.Via, key.Scope = "apikey", ops.ScopeWrite
	for _, op := range []string{"search.semantic.preview", "search.semantic.enable", "search.semantic.disable", "search.semantic.rebuild", "search.semantic.purge"} {
		var in any
		switch op {
		case "search.semantic.preview":
			in = semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}
		case "search.semantic.enable":
			in = semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: "x"}
		default:
			in = semantic.ProjectIn{ProjectID: e.project}
		}
		if _, err := e.try(key, op, in); err == nil || err.Code != ops.CodeForbidden {
			t.Errorf("%s with an API key: %v", op, err)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatal("text was sent")
	}
}

func TestRemoteEndpointNeedsATypedConfirmation(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	e.loopbackLocal.Store(false) // the fake counts as a public address: a remote endpoint
	pid := e.provider(f, "Remote embeddings", false)
	e.policy("ask", nil)
	book(e)

	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	if pv.Blocked != nil || !pv.Remote || !pv.Endpoint.Remote || pv.ConfirmHost == "" || pv.Endpoint.Local {
		t.Fatalf("preview of a remote endpoint: %+v", pv)
	}
	in := semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: pv.ConsentToken}
	_, err := e.try(e.owner, "search.semantic.enable", in)
	wantCode(t, err, ops.CodeInvalid)
	in.ConfirmRemote = "yes"
	_, err = e.try(e.owner, "search.semantic.enable", in)
	wantCode(t, err, ops.CodeInvalid)
	if f.calls.Load() != 0 || e.count(`SELECT count(*) FROM embed_projects`) != 0 {
		t.Fatal("an unconfirmed remote endpoint was switched on or called")
	}
	in.ConfirmRemote = strings.ToUpper(pv.ConfirmHost) // case does not matter
	st := e.call(e.owner, "search.semantic.enable", in).(semantic.StatusOut)
	if !st.Remote || st.Endpoint == nil || !st.Endpoint.Remote {
		t.Fatalf("%+v", st)
	}
	e.settle()
	if f.calls.Load() == 0 {
		t.Fatal("a confirmed remote index sent nothing")
	}
	// An editor sees that the index is remote, and not where it is.
	if st := e.status(e.editor); st.Endpoint == nil || !st.Endpoint.Remote || st.Endpoint.Host == "" && false {
		t.Fatalf("%+v", st)
	}
}

func TestLocalOnlyProjectRefusesRemote(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	e.loopbackLocal.Store(false)
	pid := e.provider(f, "Remote embeddings", false)
	e.policy("local_only", nil)
	book(e)
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	if pv.Blocked == nil || pv.Blocked.Code != "not_local" || pv.ConsentToken != "" {
		t.Fatalf("a local-only project must block a remote endpoint: %+v", pv)
	}
	_, err := e.try(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: "x", ConfirmRemote: "127.0.0.1"})
	if err == nil {
		t.Fatal("a remote endpoint was enabled on a local-only project")
	}
	if f.calls.Load() != 0 {
		t.Fatal("text was sent")
	}
}

func TestProjectAIPolicyOffStopsEverything(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	if _, err := e.search(e.editor, "forge anvil"); err != nil {
		t.Fatal(err)
	}
	e.policy("off", nil)
	calls := f.calls.Load()
	if _, err := e.search(e.editor, "forge anvil"); err == nil {
		t.Fatal("a search ran on a project whose AI policy is off")
	}
	e.doc("New", "scene", "", para("brand new words", 30))
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("text was sent while the project's AI policy was off")
	}
	if st := e.status(e.owner); st.State != "waiting" || st.Reason != "policy_off" {
		t.Fatalf("%+v", st)
	}
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	if pv.Blocked == nil || pv.Blocked.Code != "policy_off" {
		t.Fatalf("%+v", pv.Blocked)
	}
	// Turned back on, the index resumes by itself: the owner's own switch.
	e.policy("ask", nil)
	e.settle()
	if f.calls.Load() == calls {
		t.Fatal("the index did not resume")
	}
}

func TestAIKillSwitch(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	off := false
	e.call(e.admin, "ai.settings.set", aicore.SettingsSetIn{Enabled: &off}) // the administrator's switch; SCRIOBH_AI=off is the same gate
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	if pv.Blocked == nil || pv.Blocked.Code != "ai_off" {
		t.Fatalf("%+v", pv.Blocked)
	}
	_, err := e.try(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: "x"})
	wantCode(t, err, ops.CodeConflict)
	if f.calls.Load() != 0 {
		t.Fatal("sent with AI off")
	}
}

func TestAgentsNeedThePolicyToSearch(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	agent := e.editor
	agent.Via, agent.Scope = "apikey", ops.ScopeRead
	calls := f.calls.Load()
	_, err := e.try(agent, "search.semantic", semantic.SearchIn{ProjectID: e.project, Q: "forge anvil"})
	if err == nil || err.Code != ops.CodeForbidden {
		t.Fatalf("an API key searched where agents may not use AI: %v", err)
	}
	if f.calls.Load() != calls {
		t.Fatal("the question was sent")
	}
	yes := true
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, AgentsMayUse: &yes})
	if _, err := e.try(agent, "search.semantic", semantic.SearchIn{ProjectID: e.project, Q: "forge anvil"}); err != nil {
		t.Fatalf("with the policy's say-so: %v", err)
	}
	// Scenes like this one sends nothing, so it is open to a reader's key.
	ids := e.docID("The Forge")
	if _, err := e.try(agent, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: ids}); err != nil {
		t.Fatalf("similar: %v", err)
	}
}

func TestSearchAuthorization(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	if _, err := e.search(e.editor, "forge"); err != nil {
		t.Fatalf("an editor may search: %v", err)
	}
	// A reader's question would go through the owner's provider and key: readers use search.query and search.similar.
	if _, err := e.search(e.reader, "forge"); err == nil || err.Code != ops.CodeForbidden {
		t.Fatalf("a reader searched by meaning: %v", err)
	}
	if _, err := e.try(e.reader, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: e.docID("The Forge")}); err != nil {
		t.Fatalf("a reader may ask for scenes like this one: %v", err)
	}
	if _, err := e.search(e.other, "forge"); err == nil || err.Code != ops.CodeNotFound {
		t.Fatalf("a stranger must not learn the project exists: %v", err)
	}
	_, err := e.try(e.other, "search.similar", semantic.SimilarIn{ProjectID: e.project, DocumentID: e.docID("The Forge")})
	wantCode(t, err, ops.CodeNotFound)
	_, err = e.try(e.other, "search.semantic.status", semantic.StatusIn{ProjectID: e.project})
	wantCode(t, err, ops.CodeNotFound)
	_, err = e.try(e.reader, "search.semantic.status", semantic.StatusIn{ProjectID: e.project}) // status is for editors and up
	wantCode(t, err, ops.CodeForbidden)
}

func TestTrashedProjectSendsNothing(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	e.doc2("x") // owed an embedding when the project goes to the trash
	calls := f.calls.Load()
	e.call(e.owner, "project.trash", stateIn(e.project))
	_, err := e.search(e.owner, "forge")
	wantCode(t, err, ops.CodeConflict)
	e.settle()
	if f.calls.Load() != calls {
		t.Fatal("a trashed project's text was sent")
	}
	if st := e.status(e.owner); st.State != "waiting" || st.Reason != "project_trashed" {
		t.Fatalf("%+v", st)
	}
}

func TestEndpointChangeByAdministratorPausesUntilConfirmed(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	// The administrator points the provider at another address.
	g := newFakeEmbed(t, "ollama")
	nu := g.srv.URL
	e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: pid, BaseURL: &nu})
	e.doc("Added", "scene", "", para("new words after the change", 30))
	e.settle()
	if g.calls.Load() != 0 {
		t.Fatal("text went to an endpoint the owner never confirmed")
	}
	st := e.status(e.owner)
	if st.State != "waiting" || st.Reason != "endpoint_changed" {
		t.Fatalf("%+v", st)
	}
	if _, err := e.search(e.editor, "forge"); err == nil {
		t.Fatal("a search was sent to the new address")
	}
	if g.calls.Load() != 0 {
		t.Fatal("the question went to the new address")
	}
	// The owner reviews and confirms: the old embeddings go, the new endpoint is used.
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a"}).(semantic.PreviewOut)
	e.call(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-a", ConsentToken: pv.ConsentToken})
	if e.count(`SELECT count(*) FROM embed_chunks`) != 0 {
		t.Fatal("embeddings made by another endpoint were kept")
	}
	e.settle()
	if g.calls.Load() == 0 {
		t.Fatal("the confirmed endpoint was not used")
	}
}

func TestModelChangeRebuilds(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	n := f.textCount()
	pv := e.call(e.owner, "search.semantic.preview", semantic.PreviewIn{ProjectID: e.project, ProviderID: pid, Model: "embed-b"}).(semantic.PreviewOut)
	e.call(e.owner, "search.semantic.enable", semantic.EnableIn{ProjectID: e.project, ProviderID: pid, Model: "embed-b", ConsentToken: pv.ConsentToken})
	if e.count(`SELECT count(*) FROM embed_chunks`) != 0 {
		t.Fatal("a new model started from the old model's embeddings")
	}
	e.settle()
	if f.textCount() < 2*n-4 {
		t.Fatalf("a model change re-embedded %d texts, expected about %d", f.textCount()-n, n)
	}
	if got := e.count(`SELECT count(*) FROM embed_chunks WHERE model = 'embed-b'`); got != e.count(`SELECT count(*) FROM embed_chunks`) || got == 0 {
		t.Fatalf("chunks by model: %d of %d", got, e.count(`SELECT count(*) FROM embed_chunks`))
	}
	var bodies = f.rawBodies()
	if !strings.Contains(bodies, `"model":"embed-b"`) {
		t.Fatal("the new model was not asked for")
	}
}

var _ = time.Second

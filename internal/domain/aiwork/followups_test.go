package aiwork_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) key(p ops.Principal) ops.Principal {
	p.Via = "apikey"
	return p
}

// Configuration is for a person in a browser, as the policy is: an API key may
// not write the style guide or the default chains, at any scope.
func TestConfigurationOpsAreForSessionsOnly(t *testing.T) {
	e := newEnv(t)
	editor := e.key(e.editor)
	_, err := e.try(editor, "ai.style.set", aiwork.StyleSetIn{ProjectID: e.project, StyleGuide: "x"})
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.try(e.key(e.owner), "ai.defaults.set", aiwork.DefaultsSetIn{ProjectID: e.project, Scope: "user", Class: "cheap", Chain: []aicore.ChainEntry{{ProviderID: e.provider}}})
	wantCode(t, err, ops.CodeForbidden)
	if n := e.count(`SELECT count(*) FROM ai_user_defaults`); n != 0 {
		t.Fatal("a key wrote a default")
	}
	// reading is open to a key (the owner allowed agents)
	if _, err := e.try(editor, "ai.style.get", aiwork.StyleGetIn{ProjectID: e.project}); err != nil {
		t.Fatal(err)
	}
}

// What became of one person's suggestion is nobody else's business: after the
// accept, another person's request with the run id gets the answer an unknown id gets.
func TestTheLedgerIsPrivateToo(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan strolled to the water.")
	run := e.mustRun(e.editor, e.rewrite(target, "x"))
	if _, err := e.accept(e.editor, run.RunID, e.docNow().Revision); err != nil {
		t.Fatal(err)
	}
	for _, who := range []ops.Principal{e.editor2, e.owner} {
		_, err := e.accept(who, run.RunID, e.docNow().Revision)
		wantCode(t, err, ops.CodeNotFound)
	}
	_, err := e.accept(e.other, run.RunID, e.docNow().Revision)
	wantCode(t, err, ops.CodeNotFound)
	e.say1(`{"long": "A summary."}`)
	sr := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "summary.chapter", DocumentID: e.doc})
	if _, err := e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: sr.RunID, BaseRevision: e.docNow().Revision}); err != nil {
		t.Fatal(err)
	}
	_, err = e.try(e.editor2, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: sr.RunID, BaseRevision: e.docNow().Revision})
	wantCode(t, err, ops.CodeNotFound)
}

// A key pinned to a project reaches its primary Compendium only: a referenced
// Compendium the person may edit is not reachable by it.
func TestExtractAcceptHonoursAProjectPin(t *testing.T) {
	e := newEnv(t)
	primary, _, _ := e.bible()
	ref := e.compendium("Referenced")
	e.call(e.owner, "compendium.member.add", compendiums.MemberAddIn{ID: ref, Email: "editor@test.local", Role: "editor"})
	if _, err := e.exec(`INSERT INTO compendium_refs(id, owner_kind, owner_id, target_compendium_id, mode, created_at) VALUES ('ref_1','project',?,?, 'reference', '2026-01-01T00:00:00.000Z')`, e.project, ref); err != nil {
		t.Fatal(err)
	}
	e.enable("ask", func(in *aicore.PolicySetIn) { b := true; in.AgentsMayUse = &b })
	e.setDoc("Old Tom met Young Ann at the Salt Gate.")
	e.say1(`{"entities": [{"name": "Old Tom", "kind": "character"}, {"name": "Young Ann", "kind": "character"}]}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "extract.chapter", DocumentID: e.doc})
	var rv aiwork.ReviewOut
	data(t, run, &rv)
	pinned := e.key(e.editor)
	pinned.ProjectID = e.project
	_, err := e.try(pinned, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: rv.Notes[0].ID, CompendiumID: ref})
	wantCode(t, err, ops.CodeNotFound)
	if n := e.count(`SELECT count(*) FROM entities WHERE compendium_id = ?`, ref); n != 0 {
		t.Fatal("a pinned key created an entity in a referenced Compendium")
	}
	if _, err := e.try(pinned, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: rv.Notes[0].ID, CompendiumID: primary}); err != nil {
		t.Fatalf("the primary Compendium: %v", err)
	}
	// the session (unpinned) may use the referenced one
	if _, err := e.try(e.editor, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: rv.Notes[1].ID, CompendiumID: ref}); err != nil {
		t.Fatalf("the referenced Compendium by session: %v", err)
	}
	_ = entities.Kinds
}

// The same rewrite runs against every kind of provider and is accepted the same way.
func TestRecipesRunAgainstEveryProviderKind(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan went down to the water at dawn.")
	for _, kind := range []string{lib.KindOllama, lib.KindOpenCode, lib.KindOpenAICompatible} {
		f := newFake(t, func(string) string { return "Maire Nolan went down to the water at dawn." })
		in := aicore.ProviderCreateIn{Name: "K-" + kind, Kind: kind, Scope: "instance", BaseURL: f.srv.URL, APIKey: "sk-test-KIND-0123456789", DefaultModel: "m1", Local: false}
		if kind == lib.KindOpenAICompatible {
			in.BaseURL += "/v1"
		}
		id := e.call(e.admin, "ai.provider.create", in).(aicore.ProviderItem).Item.ID
		// a paid model with no known price is refused: say it is free by pricing it
		e.call(e.admin, "ai.price.set", aicore.PriceSetIn{ProviderID: id, Model: "m1", InputMicros: 1, OutputMicros: 1})
		ri := e.rewrite(target, "x")
		ri.Provider = id
		run := e.mustRun(e.editor, ri)
		var res aiwork.EditResult
		data(t, run, &res)
		if len(res.Suggestions) != 1 || res.Suggestions[0].Text != "Maire Nolan went down to the water at dawn." {
			t.Errorf("%s: %+v warnings %v", kind, res, run.Warnings)
			continue
		}
		if len(f.requests()) != 1 {
			t.Errorf("%s: %d requests", kind, len(f.requests()))
		}
		// the preview is the send, for the recipe whose wire body differs (the JSON one)
		ex := aicore.RunIn{ProjectID: e.project, Recipe: "extract.chapter", DocumentID: e.doc, Provider: id}
		pv := e.call(e.editor, "ai.context.preview", ex).(aicore.PreviewOut).Item
		body := pv.Attempts[0].Body
		if kind != lib.KindOpenCode && !strings.Contains(body, "json") { // OpenCode's one-shot route has no JSON mode: the prompt asks
			t.Errorf("%s: the extraction request does not ask for JSON: %s", kind, body)
		}
		e.say1("{\"entities\": []}")
		ex.Nonce, ex.PayloadHash = pv.Nonce, pv.PayloadHash
		if _, err := e.try(e.editor, "ai.run", ex); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		last := f.requests()[len(f.requests())-1]
		if last != body {
			t.Errorf("%s: what was previewed is not what was sent", kind)
		}
		e.say1("Maire Nolan went down to the water at dawn.")
		// accepting works whatever the provider
		if out, err := e.accept(e.editor, run.RunID, e.docNow().Revision); err != nil || !out.Applied {
			t.Errorf("%s: accept %+v %v", kind, out, err)
		}
		e.setDoc(chapterText)
	}
}

// A rolling summary's second chapter is asked with the first one's accepted
// summary in the request; a skipped chapter leaves the next with the previous
// chapter's synopsis, which is empty.
func TestRollingSummaryCarriesThePreviousSummaryForward(t *testing.T) {
	e := newEnv(t)
	c2 := e.chapter("Chapter Two", "The tide goes out.")
	e.say(func(body string) string {
		if strings.Contains(body, "The tide goes out.") {
			return `{"short": "s2", "long": "Second chapter summary."}`
		}
		return `{"short": "s1", "long": "FIRST-CHAPTER-SUMMARY-TEXT"}`
	})
	job := e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project}).(aiwork.JobItem).Item.JobID
	for _, want := range []string{e.doc, c2} {
		nx := e.call(e.editor, "ai.summary.rolling.next", aiwork.RollingNextIn{ProjectID: e.project, JobID: job}).(aiwork.NextOut)
		if nx.Step.DocumentID != want {
			t.Fatalf("%+v", nx)
		}
		run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: nx.RunIn.Recipe, DocumentID: want, Params: nx.RunIn.Params})
		rev := e.revisionOf(want)
		if _, err := e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run.RunID, BaseRevision: rev, JobID: job}); err != nil {
			t.Fatal(err)
		}
	}
	reqs := e.fake.requests()
	if !strings.Contains(reqs[len(reqs)-1], "FIRST-CHAPTER-SUMMARY-TEXT") {
		t.Fatal("chapter two was not asked with chapter one's accepted summary")
	}
	if strings.Contains(reqs[0], "FIRST-CHAPTER-SUMMARY-TEXT") {
		t.Fatal("chapter one was asked with a summary that did not exist yet")
	}
}

// A preview of an accept (a dry run) changes nothing; the real accept afterwards works.
func TestDryRunOfAnAcceptChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan strolled to the water.")
	run := e.mustRun(e.editor, e.rewrite(target, "x"))
	in := aiwork.AcceptEditIn{ProjectID: e.project, RunID: run.RunID, BaseRevision: e.docNow().Revision}
	before := e.docNow()
	res, err := e.reg.CallDry(context.Background(), e.editor, "ai.accept.edit", in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changes == 0 {
		t.Fatal("the preview reports no change")
	}
	if out := res.Out.(aiwork.AcceptOut); !out.Applied {
		t.Fatalf("%+v", out)
	}
	after := e.docNow()
	if after.Revision != before.Revision || after.Content != before.Content {
		t.Fatal("a dry run wrote the document")
	}
	if n := e.count(`SELECT count(*) FROM ai_accepts`); n != 0 {
		t.Fatal("a dry run wrote the ledger")
	}
	if n := e.count(`SELECT count(*) FROM ai_pending WHERE run_id = ?`, run.RunID); n != 1 {
		t.Fatal("a dry run released the held suggestion")
	}
	if out, err := e.accept(e.editor, run.RunID, before.Revision); err != nil || !out.Applied {
		t.Fatal(out, err)
	}
	_ = fmt.Sprint
}

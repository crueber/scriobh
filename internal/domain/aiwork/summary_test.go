package aiwork_test

import (
	"fmt"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) chapter(title, content string) string {
	e.t.Helper()
	return e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: title, Kind: "chapter", Content: content}).(docs.Item).Item.ID
}

func TestSummarySuggestionThenAcceptWritesTheSynopsis(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"short": "Maire goes to the harbour.", "long": "Maire Nolan walks down to the water at dawn. The gulls are loud. Nothing else happens."}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "summary.chapter", DocumentID: e.doc})
	var res aiwork.SummaryResult
	data(t, run, &res)
	if res.Suggestion.Short == "" || res.Suggestion.Long == "" || res.Current.Long != "" {
		t.Fatalf("%+v", res)
	}
	if d := e.docNow(); d.SynopsisShort != "" {
		t.Fatal("the run wrote the synopsis")
	}
	rev := e.docNow().Revision
	out, err := e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run.RunID, BaseRevision: rev, ExpectHash: res.Suggestion.TextHash})
	if err != nil {
		t.Fatal(err)
	}
	so := out.(aiwork.SummaryAcceptOut)
	if !so.Applied || !so.Wrote.Short || !so.Wrote.Long || so.Revision != rev+1 {
		t.Fatalf("%+v", so)
	}
	d := e.docNow()
	if d.SynopsisShort != "Maire goes to the harbour." || !strings.HasPrefix(d.SynopsisLong, "Maire Nolan walks") || d.Content != chapterText {
		t.Fatalf("%+v", d)
	}
	if e.words(e.editor) != 0 {
		t.Fatal("a synopsis earns no words")
	}
	// a stale revision, idempotent repeat, wrong person
	again, err := e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run.RunID, BaseRevision: rev})
	if err != nil || !again.(aiwork.SummaryAcceptOut).Already {
		t.Fatalf("%+v %v", again, err)
	}
	e.say1(`{"short": "x", "long": "A different summary."}`)
	run2 := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "summary.chapter", DocumentID: e.doc, Params: map[string]string{"length": "long"}})
	_, err = e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run2.RunID, BaseRevision: rev})
	wantCode(t, err, ops.CodeConflict)
	_, err = e.try(e.editor2, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run2.RunID, BaseRevision: e.docNow().Revision})
	wantCode(t, err, ops.CodeNotFound)
	out, err = e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run2.RunID, BaseRevision: e.docNow().Revision})
	if err != nil || out.(aiwork.SummaryAcceptOut).Wrote.Short {
		t.Fatalf("a long-only run wrote the short synopsis: %+v %v", out, err)
	}
	if e.docNow().SynopsisShort != "Maire goes to the harbour." {
		t.Fatal("the short synopsis changed")
	}
	// a plain-text answer is the long summary
	e.say1("Just a paragraph of summary, no JSON.")
	run3 := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "summary.chapter", DocumentID: e.doc})
	var r3 aiwork.SummaryResult
	data(t, run3, &r3)
	if r3.Suggestion.Long != "Just a paragraph of summary, no JSON." {
		t.Fatalf("%+v", r3)
	}
}

func TestRollingSummaryIsPlannedUpFrontAndRunsOneChapterAtATime(t *testing.T) {
	e := newEnv(t)
	c2 := e.chapter("Chapter Two", "The second chapter, in which the tide goes out.")
	c3 := e.chapter("Chapter Three", "The third chapter, in which the tide returns.")
	say := func(string) string { return `{"short": "s", "long": "A summary of a chapter."}` }
	e.say(say)

	start := e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project}).(aiwork.JobItem)
	if start.Decision == nil || !start.Decision.Allowed || start.Item.Total != 3 || start.Item.Estimate.Chapters != 3 || start.Item.Estimate.InputTokens <= 0 || !start.Item.Estimate.Approximate {
		t.Fatalf("%+v", start)
	}
	job := start.Item.JobID
	if got := []string{start.Item.Steps[0].DocumentID, start.Item.Steps[1].DocumentID, start.Item.Steps[2].DocumentID}; got[0] != e.doc || got[1] != c2 || got[2] != c3 {
		t.Fatalf("not in reading order: %v", got)
	}
	// nothing was sent by planning
	if n := len(e.fake.requests()); n != 0 {
		t.Fatalf("planning called the provider %d times", n)
	}
	for i, want := range []string{e.doc, c2, c3} {
		nx := e.call(e.editor, "ai.summary.rolling.next", aiwork.RollingNextIn{ProjectID: e.project, JobID: job}).(aiwork.NextOut)
		if nx.Step == nil || nx.Step.DocumentID != want || nx.RunIn.Recipe != "summary.chapter" {
			t.Fatalf("step %d: %+v", i, nx)
		}
		if i == 1 { // resumable: ask again, same step; skip is explicit
			again := e.call(e.editor, "ai.summary.rolling.next", aiwork.RollingNextIn{ProjectID: e.project, JobID: job}).(aiwork.NextOut)
			if again.Step.DocumentID != want {
				t.Fatal("next moved by itself")
			}
			sk := e.call(e.editor, "ai.summary.rolling.skip", aiwork.RollingNextIn{ProjectID: e.project, JobID: job}).(aiwork.JobItem)
			if sk.Item.Position != 2 {
				t.Fatalf("%+v", sk.Item)
			}
			continue
		}
		run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: nx.RunIn.Recipe, DocumentID: nx.RunIn.DocumentID, Params: nx.RunIn.Params})
		doc := e.call(e.owner, "doc.get", docs.DocIn{ID: want}).(docs.Item).Item
		out, err := e.try(e.editor, "ai.summary.accept", aiwork.SummaryAcceptIn{ProjectID: e.project, RunID: run.RunID, BaseRevision: doc.Revision, JobID: job})
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && !out.(aiwork.SummaryAcceptOut).JobDone {
			t.Fatal("the job did not finish")
		}
	}
	// the third chapter's request carried the previous chapter's summary as a chip (the second was skipped,
	// so the previous one with a summary is the one before it: the core's previous_summary reads the previous chapter)
	last := e.fake.requests()[len(e.fake.requests())-1]
	if !strings.Contains(last, "Summary of the previous chapter") && !strings.Contains(last, "previous") {
		t.Fatalf("no previous-summary chip in %s", last)
	}
	got := e.call(e.editor, "ai.summary.rolling.get", aiwork.RollingGetIn{ProjectID: e.project, JobID: job}).(aiwork.JobItem).Item
	if got.Status != "done" || got.Position != 3 {
		t.Fatalf("%+v", got)
	}
	// nothing open to resume
	none := e.call(e.editor, "ai.summary.rolling.get", aiwork.RollingGetIn{ProjectID: e.project}).(aiwork.JobItem).Item
	if none.Status != "none" {
		t.Fatalf("%+v", none)
	}
	// cancel
	st := e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project, DocumentIDs: []string{c2}}).(aiwork.JobItem)
	if st.Item.Total != 1 {
		t.Fatalf("%+v", st)
	}
	resume := e.call(e.editor, "ai.summary.rolling.get", aiwork.RollingGetIn{ProjectID: e.project}).(aiwork.JobItem).Item
	if resume.JobID != st.Item.JobID {
		t.Fatal("not resumable")
	}
	e.call(e.editor, "ai.summary.rolling.cancel", aiwork.RollingNextIn{ProjectID: e.project, JobID: st.Item.JobID})
	nx := e.call(e.editor, "ai.summary.rolling.next", aiwork.RollingNextIn{ProjectID: e.project, JobID: st.Item.JobID}).(aiwork.NextOut)
	if nx.Step != nil || nx.Job.Status != "cancelled" {
		t.Fatalf("%+v", nx)
	}
	// another person's job is not theirs to touch
	_, err := e.try(e.editor2, "ai.summary.rolling.next", aiwork.RollingNextIn{ProjectID: e.project, JobID: job})
	wantCode(t, err, ops.CodeNotFound)
}

func TestRollingSummaryChecksTheBudgetBeforeStarting(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.chapter(fmt.Sprintf("Chapter %d", i+2), strings.Repeat("A line of the chapter. ", 200))
	}
	// a small monthly token cap for the project
	e.call(e.admin, "ai.budget.set", aicore.BudgetSetIn{Scope: "project", ScopeID: e.project, MaxTokens: 3000})
	st := e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project}).(aiwork.JobItem)
	if st.Decision == nil || st.Decision.Allowed || st.Decision.Code != "budget_exceeded" {
		t.Fatalf("%+v", st.Decision)
	}
	if n := e.count(`SELECT count(*) FROM ai_jobs`); n != 0 {
		t.Fatal("a refused plan made a job")
	}
	if len(e.fake.requests()) != 0 {
		t.Fatal("a provider was called")
	}
	// with the policy off the plan says so too
	mode := "off"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
	st = e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project}).(aiwork.JobItem)
	if st.Decision.Allowed || st.Decision.Code != "policy_off" {
		t.Fatalf("%+v", st.Decision)
	}
}

func ptr[T any](v T) *T { return &v }

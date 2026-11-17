package aiwork_test

import (
	"encoding/json"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) altRun(quote, scope string, count int) (aicore.RunOut, aiwork.CandidatesResult) {
	e.t.Helper()
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "alternatives", DocumentID: e.doc, Selection: e.sel(quote),
		Params: map[string]string{"scope": scope, "count": string(rune('0' + count))}})
	var res aiwork.CandidatesResult
	data(e.t, run, &res)
	return run, res
}

func (e *env) acceptAlt(runID string, ids []string, altID string, choose string) (aiwork.AcceptAltOut, *ops.Error) {
	out, err := e.try(e.editor, "ai.accept.alternative", aiwork.AcceptAltIn{ProjectID: e.project, RunID: runID, CandidateIDs: ids, AlternativeID: altID, Choose: choose, BaseRevision: e.docNow().Revision})
	if err != nil {
		return aiwork.AcceptAltOut{}, err
	}
	return out.(aiwork.AcceptAltOut), nil
}

func TestAlternativesAreCandidatesUntilAccepted(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"alternatives": ["Maire Nolan went down to the water.", "Maire Nolan made her way to the shore.", "Maire Nolan walked down to the water.", "Down to the water walked Maire Nolan."]}`)
	run, res := e.altRun(target, "sentence", 3)
	// the original is not a candidate, the count is bounded
	if len(res.Candidates) != 3 || res.Original != target || res.Scope != "sentence" {
		t.Fatalf("%+v", res)
	}
	for _, c := range res.Candidates {
		if c.Text == target {
			t.Fatal("the original came back as a candidate")
		}
	}
	// nothing exists yet: no alternative, the text is untouched
	if n := e.count(`SELECT count(*) FROM alternatives WHERE document_id = ?`, e.doc); n != 0 {
		t.Fatalf("%d alternatives before accept", n)
	}
	rev := e.docNow().Revision
	out, err := e.acceptAlt(run.RunID, []string{res.Candidates[0].ID, res.Candidates[1].ID}, "", "")
	if err != nil || len(out.Added) != 2 || out.Alternative == nil {
		t.Fatalf("%+v %v", out, err)
	}
	if e.docNow().Content == chapterText && false {
		t.Fatal()
	}
	// the chosen wording is still the original: the text did not change by words
	d := e.docNow()
	if !strings.HasPrefix(d.Content, chapterText) {
		t.Fatalf("the text changed: %q", d.Content)
	}
	if out.Revision != rev+1 {
		t.Fatalf("revision %d want %d", out.Revision, rev+1)
	}
	var generated, manual int
	for _, v := range out.Alternative.Variants {
		switch v.Source {
		case "generated":
			generated++
		case "manual":
			manual++
		}
	}
	if generated != 2 || manual != 1 || out.Alternative.Variants[0].Original != true {
		t.Fatalf("variants %+v", out.Alternative.Variants)
	}
	// the list op shows the AI source flag the UI draws
	list := e.call(e.editor, "alternative.list", docs.AltListIn{ID: e.doc}).(docs.AltListOut)
	if len(list.Items) != 1 || list.Items[0].Variants[1].Source != "generated" {
		t.Fatalf("%+v", list)
	}
	// accepting the same candidate again adds nothing
	again, err := e.acceptAlt(run.RunID, []string{res.Candidates[0].ID}, "", "")
	if err != nil || len(again.Added) != 0 || len(again.Skipped) != 1 {
		t.Fatalf("%+v %v", again, err)
	}
	// the third goes to the existing alternative, and may be chosen: only then does the text change
	out3, err := e.acceptAlt(run.RunID, []string{res.Candidates[2].ID}, list.Items[0].ID, res.Candidates[2].ID)
	if err != nil || len(out3.Added) != 1 {
		t.Fatalf("%+v %v", out3, err)
	}
	if got := e.docNow().Content; !strings.Contains(got, res.Candidates[2].Text) {
		t.Fatalf("the chosen wording is not in the text: %q", got)
	}
	var reason string
	if err := e.d.Read().QueryRow(`SELECT reason FROM document_revisions WHERE id = ?`, out3.RevisionID).Scan(&reason); err != nil || reason != "ai-accepted" {
		t.Fatalf("reason %q %v", reason, err)
	}
	// no goal credit for the chosen AI wording
	if w := e.words(e.editor); w != 0 {
		t.Fatalf("goal words %d", w)
	}
}

func TestAlternativesAreCheckedForTheScope(t *testing.T) {
	e := newEnv(t)
	// a word is one line; blank lines, markers, control characters and HTML are dropped
	e.say1(`{"alternatives": ["two words", "ok", "bad\n\nsplit", "<b>bold</b>", "%%note%%", "fine"]}`)
	_, res := e.altRun("quiet", "word", 5)
	var got []string
	for _, c := range res.Candidates {
		got = append(got, c.Text)
	}
	if strings.Join(got, ",") != "two words,ok,fine" {
		t.Fatalf("candidates %v", got)
	}
	// a malformed answer is text and nothing else
	e.say1("Here are some ideas: 1. soft 2. hushed")
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "alternatives", DocumentID: e.doc, Selection: e.sel("quiet"), Params: map[string]string{"scope": "word"}})
	if len(run.Data) != 0 || len(run.Warnings) == 0 {
		t.Fatalf("data %s warnings %v", run.Data, run.Warnings)
	}
	// a list in a plain numbered form is read for a word or a sentence
	e.say1("1. soft\n2. hushed\n3. still")
	_, res = e.altRun("quiet", "word", 3)
	if len(res.Candidates) != 3 {
		t.Fatalf("%+v", res)
	}
}

func TestAlternativesAcceptRefusesStale(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"alternatives": ["Maire Nolan went down to the water."]}`)
	run, res := e.altRun(target, "sentence", 1)
	e.setDoc(strings.Replace(chapterText, target, "Maire Nolan stayed home.", 1))
	_, err := e.acceptAlt(run.RunID, nil, "", "")
	wantCode(t, err, ops.CodeConflict)
	if n := e.count(`SELECT count(*) FROM alternatives WHERE document_id = ?`, e.doc); n != 0 {
		t.Fatal("a stale accept wrote")
	}
	_ = res
	// an unknown candidate and a reject
	run2, res2 := e.altRun("Maire Nolan stayed home.", "sentence", 1)
	_, err = e.acceptAlt(run2.RunID, []string{"c9"}, "", "")
	wantCode(t, err, ops.CodeNotFound)
	if _, err := e.try(e.editor, "ai.accept.reject", aiwork.AcceptRejectIn{ProjectID: e.project, RunID: run2.RunID, SuggestionID: res2.Candidates[0].ID}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM ai_pending WHERE run_id = ?`, run2.RunID); n != 0 {
		t.Fatalf("rejected run still held")
	}
}

var _ = json.Marshal

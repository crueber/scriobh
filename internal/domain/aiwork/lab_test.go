package aiwork_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/ops"
)

// labChapter is twenty paragraphs, each with a filler word the model will mark,
// a hedge word and a double space the local analysers find.
func labChapter() string {
	var ps []string
	for i := 1; i <= 20; i++ {
		ps = append(ps, fmt.Sprintf("Note %d: the lamp was very dim,  and perhaps the tide would turn before dark.", i))
	}
	return strings.Join(ps, "\n\n") + "\n"
}

func trimAnswer() string {
	var ms []string
	for i := 1; i <= 20; i++ {
		ms = append(ms, fmt.Sprintf(`{"quote": "very", "before": "Note %d: the lamp was ", "after": " dim,", "reason": "filler"}`, i))
	}
	return `{"marks": [` + strings.Join(ms, ",") + `]}`
}

func (e *env) labMarks(status string) aiwork.MarksOut {
	e.t.Helper()
	return e.call(e.editor, "ai.lab.marks", aiwork.LabMarksIn{ProjectID: e.project, DocumentID: e.doc, Status: status}).(aiwork.MarksOut)
}

func (e *env) analyse() aiwork.MarksOut {
	e.t.Helper()
	return e.call(e.editor, "ai.lab.analyse", aiwork.LabAnalyseIn{ProjectID: e.project, DocumentID: e.doc}).(aiwork.MarksOut)
}

func TestLabMarksTwentyAndEveryOneIsSettledIndividually(t *testing.T) {
	e := newEnv(t)
	e.setDoc(labChapter())

	// local analysers: no provider, no policy, no cost
	reqs := len(e.fake.requests())
	loc := e.analyse()
	if len(e.fake.requests()) != reqs {
		t.Fatal("the local analysers called a provider")
	}
	if loc.Counts["hedge_words"] != 20 || loc.Counts["punctuation_fix"] != 20 {
		t.Fatalf("local counts %v", loc.Counts)
	}
	// the model marks the trim candidates
	e.say1(trimAnswer())
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.trim", DocumentID: e.doc, Params: map[string]string{"mode": "filler", "max_marks": "30"}})
	var mr struct {
		aiwork.MarksOut
		Unplaced int `json:"unplaced"`
	}
	data(t, run, &mr)
	if len(mr.Marks) != 20 || mr.Unplaced != 0 {
		t.Fatalf("%d model marks, %d unplaced, warnings %v", len(mr.Marks), mr.Unplaced, run.Warnings)
	}
	all := e.labMarks("open")
	if len(all.Marks) < 60 {
		t.Fatalf("%d marks, want at least 20 (here 60)", len(all.Marks))
	}
	sources := map[string]int{}
	for _, m := range all.Marks {
		sources[m.Source]++
		if m.Anchor.State != "placed" {
			t.Fatalf("a fresh mark is not placed: %+v", m)
		}
	}
	if sources["local"] != 40 || sources["model"] != 20 {
		t.Fatalf("sources %v", sources)
	}
	// the local run is idempotent: a second analysis replaces the open local marks
	e.analyse()
	if n := len(e.labMarks("open").Marks); n != 60 {
		t.Fatalf("%d marks after analysing twice", n)
	}

	// accept one trim, on its own: exactly that deletion
	var trim, hedge, punct aiwork.MarkOut
	for _, m := range e.labMarks("open").Marks {
		switch {
		case m.Kind == "trim_filler" && trim.ID == "":
			trim = m
		case m.Kind == "hedge_words" && hedge.ID == "":
			hedge = m
		case m.Kind == "punctuation_fix" && punct.ID == "":
			punct = m
		}
	}
	if trim.Action != "delete" || hedge.Action != "none" || punct.Action != "replace" || punct.Fix != " " {
		t.Fatalf("%+v %+v %+v", trim, hedge, punct)
	}
	rev := e.docNow().Revision
	acc := e.call(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: trim.ID, BaseRevision: rev}).(aiwork.LabAcceptOut)
	if len(acc.Applied) != 1 || !acc.Changed || acc.Revision != rev+1 {
		t.Fatalf("%+v", acc)
	}
	body := e.docNow().Content
	if strings.Count(body, "very") != 19 || !strings.Contains(body, "Note 1: the lamp was dim,") {
		t.Fatalf("a trim must delete the word and one space, nothing else: %q", body[:120])
	}
	// every mark still anchors after that edit (they moved with the text)
	for _, m := range e.labMarks("open").Marks {
		if m.Anchor.State != "placed" {
			t.Fatalf("mark %s went stale after an unrelated edit: %+v", m.Kind, m)
		}
	}
	// an observation is accepted as "resolved", not applied
	acc = e.call(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: hedge.ID, BaseRevision: e.docNow().Revision}).(aiwork.LabAcceptOut)
	if len(acc.Resolved) != 1 || acc.Changed {
		t.Fatalf("%+v", acc)
	}
	// dismiss two
	dis := e.call(e.editor, "ai.lab.dismiss", aiwork.LabDismissIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{punct.ID}}).(aiwork.LabDismissOut)
	if dis.Dismissed != 1 {
		t.Fatalf("%+v", dis)
	}
	// a settled mark is not applied again
	again := e.call(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: trim.ID, BaseRevision: e.docNow().Revision}).(aiwork.LabAcceptOut)
	if len(again.Applied) != 0 || again.Changed {
		t.Fatalf("%+v", again)
	}

	// accept_all of a chosen set: one transaction, one history entry
	var ids []string
	for _, m := range e.labMarks("open").Marks {
		if m.Action != "none" {
			ids = append(ids, m.ID)
		}
	}
	before := e.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND reason = 'ai-accepted'`, e.doc)
	words := e.words(e.editor)
	rev = e.docNow().Revision
	pv := e.call(e.editor, "ai.lab.preview", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: ids, BaseRevision: rev}).(aiwork.LabPreviewOut)
	if pv.Edits != len(ids) || len(pv.Diff) == 0 || e.docNow().Revision != rev {
		t.Fatalf("preview %+v", pv)
	}
	all2 := e.call(e.editor, "ai.lab.accept_all", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: ids, BaseRevision: rev}).(aiwork.LabAcceptOut)
	if len(all2.Applied) != len(ids) || all2.Revision != rev+1 || all2.RevisionID == "" || len(all2.Skipped) != 0 {
		t.Fatalf("%+v", all2)
	}
	if got := e.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND reason = 'ai-accepted'`, e.doc); got != before+1 {
		t.Fatalf("%d ai-accepted entries, want %d: one revision for the set", got, before+1)
	}
	final := e.docNow().Content
	if strings.Contains(final, "very") || strings.Contains(final, "  ") && strings.Count(final, "  ") > 1 {
		t.Fatalf("not all edits applied: %q", final[:160])
	}
	if e.words(e.editor) != words {
		t.Fatal("goal credit for AI edits")
	}
	if d := e.docNow(); !strings.Contains(d.Content, "Note 20: the lamp was dim, and perhaps") {
		t.Fatalf("%q", d.Content[len(d.Content)-120:])
	}
	// the result is exactly what the preview said
	// every mark has a settled status
	open := e.labMarks("open").Marks
	for _, m := range open {
		if m.Action != "none" {
			t.Fatalf("an applicable mark is still open: %+v", m)
		}
	}
	if st := e.labMarks("all"); len(st.Marks) < 60 {
		t.Fatalf("%d marks in all", len(st.Marks))
	}
}

func TestLabMarksGoStaleWhenTheirTextChangesAndAreNeverGuessed(t *testing.T) {
	e := newEnv(t)
	e.setDoc("The lamp was very dim.\n\nThe lamp was very bright.\n")
	e.say1(`{"marks": [{"quote": "very", "before": "The lamp was ", "after": " dim."}, {"quote": "very", "before": "The lamp was ", "after": " bright."}, {"quote": "very"}]}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.trim", DocumentID: e.doc, Params: map[string]string{"mode": "filler"}})
	var mr struct {
		aiwork.MarksOut
		Unplaced int `json:"unplaced"`
	}
	data(t, run, &mr)
	// the third has no context and "very" occurs twice: not found exactly once, so it is not a mark
	if len(mr.Marks) != 2 || mr.Unplaced != 1 {
		t.Fatalf("%d marks, %d unplaced", len(mr.Marks), mr.Unplaced)
	}
	// the text around one mark changes
	e.setDoc("The lamp was somewhat dim.\n\nThe lamp was very bright.\n")
	ms := e.labMarks("open")
	if ms.Stale != 1 {
		t.Fatalf("stale %d: %+v", ms.Stale, ms.Marks)
	}
	var stale, live aiwork.MarkOut
	for _, m := range ms.Marks {
		if m.Anchor.State == "stale" {
			stale = m
		} else {
			live = m
		}
	}
	// a stale mark cannot be accepted; the revision is checked first
	_, err := e.try(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: stale.ID, BaseRevision: e.docNow().Revision})
	wantCode(t, err, ops.CodeConflict)
	_, err = e.try(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: live.ID, BaseRevision: e.docNow().Revision - 1})
	wantCode(t, err, ops.CodeConflict)
	if err.Data["current_revision"] == nil {
		t.Fatalf("%v", err.Data)
	}
	// accept_all skips the stale one and lists it
	out := e.call(e.editor, "ai.lab.accept_all", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{stale.ID, live.ID}, BaseRevision: e.docNow().Revision}).(aiwork.LabAcceptOut)
	if len(out.Applied) != 1 || len(out.Skipped) != 1 || out.Skipped[0].Reason != "stale" || out.Skipped[0].MarkID != stale.ID {
		t.Fatalf("%+v", out)
	}
	if got := e.docNow().Content; got != "The lamp was somewhat dim.\n\nThe lamp was bright.\n" {
		t.Fatalf("%q", got)
	}
	// a stale mark can still be dismissed
	e.call(e.editor, "ai.lab.dismiss", aiwork.LabDismissIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{stale.ID}})
}

func TestLabOverlapsAreResolvedDeterministically(t *testing.T) {
	e := newEnv(t)
	e.setDoc("She was very very tired indeed.\n")
	e.say1(`{"marks": [
		{"quote": "very very", "before": "She was ", "after": " tired"},
		{"quote": "very", "before": "She was ", "after": " very tired"},
		{"quote": "very tired", "before": "very ", "after": " indeed"}]}`)
	e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.trim", DocumentID: e.doc, Params: map[string]string{"mode": "filler"}})
	ms := e.labMarks("open").Marks
	if len(ms) != 3 {
		t.Fatalf("%+v", ms)
	}
	var ids []string
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	rev := e.docNow().Revision
	a := e.call(e.editor, "ai.lab.preview", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: ids, BaseRevision: rev}).(aiwork.LabPreviewOut)
	b := e.call(e.editor, "ai.lab.preview", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{ids[2], ids[0], ids[1]}, BaseRevision: rev}).(aiwork.LabPreviewOut)
	ja, _ := json.Marshal(a.Applied)
	jb, _ := json.Marshal(b.Applied)
	if string(ja) != string(jb) || len(a.Skipped) != 2 || a.Edits != 1 {
		t.Fatalf("the order of the request changed the result: %s vs %s, %+v", ja, jb, a)
	}
	for _, s := range a.Skipped {
		if s.Reason != "overlap" {
			t.Fatal(s)
		}
	}
	// the longest mark that starts first wins
	out := e.call(e.editor, "ai.lab.accept_all", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: ids, BaseRevision: rev}).(aiwork.LabAcceptOut)
	if len(out.Applied) != 1 || e.docNow().Content != "She was tired indeed.\n" {
		t.Fatalf("%+v %q", out, e.docNow().Content)
	}
}

func TestLabOnlyChangesWhatTheMarkNames(t *testing.T) {
	e := newEnv(t)
	// private notes and ghosts are not analysed and never edited
	e.setDoc("A fine  day. %%a  private  note, perhaps%% Perhaps it rains.\n")
	m := e.analyse()
	for _, x := range m.Marks {
		if strings.Contains(x.Quote, "private") {
			t.Fatalf("a private note was marked: %+v", x)
		}
	}
	// quotes: only the odd form out is marked
	e.setDoc("\"Straight,\" she said. “Curly,” he said. “More curly.”\n")
	m = e.analyse()
	n := 0
	for _, x := range m.Marks {
		if x.Kind == "punctuation_fix" && x.Fix == "“" || x.Fix == "”" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%+v", m.Marks)
	}
	// code and URLs are skipped
	e.setDoc("See `a  b` and https://example.com/a,,b perhaps.\n")
	for _, x := range e.analyse().Marks {
		if x.Kind == "punctuation_fix" {
			t.Fatalf("code or a URL was marked: %+v", x)
		}
	}
}

func TestLabModelRecipesNeedPolicyButLocalAnalysersDoNot(t *testing.T) {
	e := newEnv(t)
	e.setDoc(labChapter())
	mode := "off"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
	_, err := e.run(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.trim", DocumentID: e.doc, Params: map[string]string{"mode": "filler"}})
	if err == nil {
		t.Fatal("a model recipe ran with AI off")
	}
	if m := e.analyse(); len(m.Marks) == 0 {
		t.Fatal("the local analysers need no policy")
	}
	// another person's marks are theirs: not listed, not accepted
	other := e.call(e.editor2, "ai.lab.marks", aiwork.LabMarksIn{ProjectID: e.project, DocumentID: e.doc}).(aiwork.MarksOut)
	if len(other.Marks) != 0 {
		t.Fatal("marks are private to the person who made them")
	}
	mine := e.labMarks("open").Marks
	_, err = e.try(e.editor2, "ai.lab.dismiss", aiwork.LabDismissIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{mine[0].ID}})
	wantCode(t, err, ops.CodeNotFound)
	// model-made marks follow the policy when accepted later
	e.enable("ask", nil)
	e.say1(trimAnswer())
	e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.trim", DocumentID: e.doc, Params: map[string]string{"mode": "filler"}})
	var model []string
	for _, m := range e.labMarks("open").Marks {
		if m.Source == "model" {
			model = append(model, m.ID)
		}
	}
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
	_, err = e.try(e.editor, "ai.lab.accept_all", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: model[:2], BaseRevision: e.docNow().Revision})
	wantCode(t, err, ops.CodeForbidden)
}

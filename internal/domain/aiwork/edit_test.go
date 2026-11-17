package aiwork_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) accept(p ops.Principal, runID string, base int) (aiwork.AcceptOut, *ops.Error) {
	out, err := e.try(p, "ai.accept.edit", aiwork.AcceptEditIn{ProjectID: e.project, RunID: runID, BaseRevision: base})
	if err != nil {
		return aiwork.AcceptOut{}, err
	}
	return out.(aiwork.AcceptOut), nil
}

func (e *env) words(p ops.Principal) int {
	return e.count(`SELECT COALESCE(SUM(words_added),0) FROM daily_stats WHERE user_id = ? AND project_id = ?`, p.UserID, e.project)
}

const target = "Maire Nolan walked down to the water."

func TestRewriteSuggestionThenAccept(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan went down to the cold grey water, slowly, with her coat open.")
	run := e.mustRun(e.editor, e.rewrite(target, "make it more vivid"))
	var res aiwork.EditResult
	data(t, run, &res)
	if res.Kind != "edit" || len(res.Suggestions) != 1 || res.RunID != run.RunID {
		t.Fatalf("result %+v", res)
	}
	sg := res.Suggestions[0]
	if sg.Op != "replace" || sg.Original != target || sg.Text == "" || sg.TextHash == "" || len(sg.Diff) == 0 {
		t.Fatalf("suggestion %+v", sg)
	}
	// nothing was written by the run
	if got := e.docNow().Content; got != chapterText {
		t.Fatalf("the run changed the document: %q", got)
	}
	before := e.words(e.editor)
	rev := e.docNow().Revision
	out, err := e.accept(e.editor, run.RunID, rev)
	if err != nil || !out.Applied || out.Revision != rev+1 || out.RevisionID == "" {
		t.Fatalf("accept: %+v %v", out, err)
	}
	want := strings.Replace(chapterText, target, sg.Text, 1)
	if got := e.docNow().Content; got != want {
		t.Fatalf("content\n got %q\nwant %q", got, want)
	}
	// one history entry with the reason, naming the recipe and the run, never the text
	var reason, note string
	if err := e.d.Read().QueryRow(`SELECT reason, note FROM document_revisions WHERE id = ?`, out.RevisionID).Scan(&reason, &note); err != nil {
		t.Fatal(err)
	}
	if reason != "ai-accepted" || !strings.Contains(note, "rewrite") || !strings.Contains(note, run.RunID) || strings.Contains(note, "vivid") || strings.Contains(note, "coat") {
		t.Fatalf("revision %q %q", reason, note)
	}
	// the text before the accept is kept, so it can be restored
	if e.count(`SELECT count(*) FROM document_revisions WHERE document_id = ? AND content_hash = ?`, e.doc, hashOf(chapterText)) == 0 {
		t.Fatal("the text before the accept was not kept")
	}
	// no goal credit for words a model wrote
	if after := e.words(e.editor); after != before {
		t.Fatalf("goal words went from %d to %d", before, after)
	}
	// accept is idempotent per run and suggestion
	again, err := e.accept(e.editor, run.RunID, out.Revision)
	if err != nil || again.Applied || !again.Already || again.Revision != out.Revision {
		t.Fatalf("second accept: %+v %v", again, err)
	}
	if e.docNow().Revision != out.Revision {
		t.Fatal("the second accept wrote")
	}
	// the held text is gone once nothing is open
	if n := e.count(`SELECT count(*) FROM ai_pending WHERE run_id = ?`, run.RunID); n != 0 {
		t.Fatalf("%d held rows left", n)
	}
	// counts, search and the event follow the normal save path
	if d := e.docNow(); d.WordCount == 0 {
		t.Fatal("word count not updated")
	}
	if n := e.count(`SELECT count(*) FROM events WHERE type = 'doc.updated' AND document_id = ?`, e.doc); n != 1 {
		t.Fatalf("events %d", n)
	}
}

func TestAcceptRefusals(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan strolled to the water.")
	run := e.mustRun(e.editor, e.rewrite(target, "plainer"))
	rev := e.docNow().Revision

	// someone else on the project, a reader, an outsider: not the person who ran it
	_, err := e.accept(e.editor2, run.RunID, rev)
	wantCode(t, err, ops.CodeNotFound)
	_, err = e.accept(e.reader, run.RunID, rev)
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.accept(e.other, run.RunID, rev)
	wantCode(t, err, ops.CodeNotFound)
	// the owner too: held results are private to the person who ran the action
	_, err = e.accept(e.owner, run.RunID, rev)
	wantCode(t, err, ops.CodeNotFound)
	// a run id that never existed is the same answer
	_, err = e.accept(e.editor, "air_0123456789ABCDEFGHJKMNPQRS", rev)
	wantCode(t, err, ops.CodeNotFound)

	// a changed hash
	_, err = e.try(e.editor, "ai.accept.edit", aiwork.AcceptEditIn{ProjectID: e.project, RunID: run.RunID, BaseRevision: rev, ExpectHash: strings.Repeat("0", 64)})
	wantCode(t, err, ops.CodeConflict)

	// the held text is sealed with the server's key, and a damaged row is not applied
	var stored string
	if err := e.d.Read().QueryRow(`SELECT items_json FROM ai_pending WHERE run_id = ?`, run.RunID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "s1:") || strings.Contains(stored, "strolled") || strings.Contains(stored, target) {
		t.Fatalf("the held text is not sealed: %.60s", stored)
	}
	if _, err := e.exec(`UPDATE ai_pending SET items_json = items_json || 'AA'`); err != nil {
		t.Fatal(err)
	}
	_, err = e.accept(e.editor, run.RunID, rev)
	wantCode(t, err, ops.CodeNotFound)
	if e.docNow().Revision != rev {
		t.Fatal("a refused accept wrote")
	}

	// without a secret key the text is held as it is, and a row that was changed no longer matches the run's record
	p := newEnvOpts(t, func(o *aicore.Options) { o.Box = nil })
	p.say1("Maire Nolan strolled to the water.")
	prun := p.mustRun(p.editor, p.rewrite(target, "plainer"))
	if err := p.d.Read().QueryRow(`SELECT items_json FROM ai_pending WHERE run_id = ?`, prun.RunID).Scan(&stored); err != nil || !strings.Contains(stored, "strolled") {
		t.Fatalf("%v %.60s", err, stored)
	}
	if _, err := p.exec(`UPDATE ai_pending SET items_json = replace(items_json, 'strolled', 'crawled')`); err != nil {
		t.Fatal(err)
	}
	_, err = p.accept(p.editor, prun.RunID, p.docNow().Revision)
	wantCode(t, err, ops.CodeNotFound)

	// a stale revision is the usual 409 with the current revision
	e.say1("Maire Nolan ambled to the water.")
	run2 := e.mustRun(e.editor, e.rewrite(target, "plainer"))
	e.setDoc(chapterText + "\n\nA third paragraph.")
	_, err = e.accept(e.editor, run2.RunID, rev)
	wantCode(t, err, ops.CodeConflict)
	if err.Data["current_revision"] == nil {
		t.Fatalf("no current revision in %v", err.Data)
	}
	// the document changed elsewhere, the selection did not: it applies where its text still is
	out, err := e.accept(e.editor, run2.RunID, e.docNow().Revision)
	if err != nil || !out.Applied {
		t.Fatalf("relocated accept: %+v %v", out, err)
	}
	if !strings.Contains(e.docNow().Content, "ambled") || !strings.Contains(e.docNow().Content, "A third paragraph.") {
		t.Fatalf("content %q", e.docNow().Content)
	}

	// the original text is gone: the suggestion is not applied
	e.say1("The harbour slept.")
	run3 := e.mustRun(e.editor, e.rewrite("The harbour was quiet.", "x"))
	e.setDoc(strings.Replace(e.docNow().Content, "The harbour was quiet.", "The harbour was empty.", 1))
	_, err = e.accept(e.editor, run3.RunID, e.docNow().Revision)
	wantCode(t, err, ops.CodeConflict)
	if !strings.Contains(err.Message, "changed") {
		t.Fatal(err.Message)
	}

	// a rejected suggestion cannot be accepted
	e.say1("Another version of the sentence about the water.")
	run4 := e.mustRun(e.editor, e.rewrite(target[:0]+"The gulls were loud.", "x"))
	if _, err := e.try(e.editor, "ai.accept.reject", aiwork.AcceptRejectIn{ProjectID: e.project, RunID: run4.RunID}); err != nil {
		t.Fatal(err)
	}
	_, err = e.accept(e.editor, run4.RunID, e.docNow().Revision)
	wantCode(t, err, ops.CodeConflict)
}

// The mirror and Git sync follow events; an accept writes through the document's
// save, so the event they follow is the ordinary one, with ids and counts only.
func TestAcceptEmitsTheSaveEvent(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan SECRET-NEW-WORDING walked down to the water.")
	run := e.mustRun(e.editor, e.rewrite(target, "x"))
	if _, err := e.accept(e.editor, run.RunID, e.docNow().Revision); err != nil {
		t.Fatal(err)
	}
	rows, err := e.d.Read().Query(`SELECT type, payload_json FROM events WHERE document_id = ? ORDER BY seq DESC LIMIT 1`, e.doc)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no event")
	}
	var typ, payload string
	_ = rows.Scan(&typ, &payload)
	if typ != "doc.updated" || strings.Contains(payload, "SECRET") || !strings.Contains(payload, `"reason":"ai-accepted"`) {
		t.Fatalf("%s %s", typ, payload)
	}
	// no event and no audit row holds the generated words or the original ones
	for _, q := range []string{`SELECT group_concat(payload_json) FROM events`, `SELECT group_concat(detail_json) FROM audit_log`} {
		var all sql.NullString
		if err := e.d.Read().QueryRow(q).Scan(&all); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(all.String, "SECRET-NEW") || strings.Contains(all.String, "walked down") {
			t.Fatalf("text in %s", q)
		}
	}
}

type collect struct{ text strings.Builder }

func (c *collect) Open(aicore.StreamInfo) error { return nil }
func (c *collect) Delta(s string) error         { c.text.WriteString(s); return nil }

// A streamed run goes through the same pipeline, so its result is as acceptable
// as a plain run's: the hook is in Run, not in the op.
func TestStreamedRewriteIsAcceptable(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan went down to the water at dawn.")
	in := e.rewrite(target, "x")
	in.Stream = true
	pv := e.call(e.editor, "ai.context.preview", in).(aicore.PreviewOut).Item
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	var sink collect
	out, err := e.core.Run(aicore.WithStream(context.Background(), &sink), e.d, e.editor, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sink.text.String(), "at dawn") {
		t.Fatalf("no deltas: %q", sink.text.String())
	}
	var res aiwork.EditResult
	data(t, out, &res)
	if len(res.Suggestions) != 1 {
		t.Fatalf("%+v", out)
	}
	if o, err := e.accept(e.editor, out.RunID, e.docNow().Revision); err != nil || !o.Applied {
		t.Fatal(o, err)
	}
}

// An accept is a save like any other: alternatives after the edit move with the
// text, and an edit may not cut into one.
func TestAcceptRebasesAlternativesAndRefusesToCutThem(t *testing.T) {
	e := newEnv(t)
	e.call(e.owner, "alternative.create", docs.AltCreateIn{ID: e.doc, BaseRevision: e.docNow().Revision, Scope: "sentence", Quote: "The gulls were loud.", Variants: []string{"The gulls screamed."}})
	e.say1("Maire Nolan went down to the cold grey water, slowly, with her coat open.")
	run := e.mustRun(e.editor, e.rewrite(target, "x"))
	if out, err := e.accept(e.editor, run.RunID, e.docNow().Revision); err != nil || !out.Applied {
		t.Fatal(out, err)
	}
	list := e.call(e.owner, "alternative.list", docs.AltListIn{ID: e.doc}).(docs.AltListOut)
	if len(list.Items) != 1 || list.Items[0].Status != "active" || list.Items[0].Quote != "The gulls were loud." {
		t.Fatalf("%+v", list)
	}
	body := []rune(e.docNow().Content)
	if got := string(body[list.Items[0].Start:list.Items[0].End]); got != "The gulls were loud." {
		t.Fatalf("the anchor moved to %q", got)
	}
	if len(list.Items[0].Variants) != 2 || list.Items[0].Variants[1].Text != "The gulls screamed." {
		t.Fatal("variants lost")
	}
	// an edit that would cut into the anchored sentence is refused
	e.say1("The gulls shrieked.")
	run2 := e.mustRun(e.editor, e.rewrite("The gulls were loud.", "x"))
	_, err := e.accept(e.editor, run2.RunID, e.docNow().Revision)
	wantCode(t, err, ops.CodeConflict)
	if !strings.Contains(err.Message, "alternative") {
		t.Fatal(err.Message)
	}
}

func TestSuggestionsExpire(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan strolled to the water.")
	run := e.mustRun(e.editor, e.rewrite(target, "plainer"))
	if _, err := e.exec(`UPDATE ai_pending SET expires_at = '2000-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	_, err := e.accept(e.editor, run.RunID, e.docNow().Revision)
	wantCode(t, err, ops.CodeNotFound)
	// a new run purges the expired rows
	e.say1("Maire Nolan ambled to the water.")
	e.mustRun(e.editor, e.rewrite(target, "plainer"))
	if n := e.count(`SELECT count(*) FROM ai_pending WHERE run_id = ?`, run.RunID); n != 0 {
		t.Fatalf("expired row still held: %d", n)
	}
}

func TestPrivateNotesAndGhostsAreNeverReplaced(t *testing.T) {
	e := newEnv(t)
	e.setDoc("She waited. %%remember to cut this%% The tide turned.\n\nAnother paragraph.")
	e.say1("She waited. The tide turned at last.")
	in := e.rewrite("She waited. %%remember to cut this%% The tide turned.", "x")
	run := e.mustRun(e.editor, in)
	var res aiwork.EditResult
	if len(run.Data) > 0 {
		data(t, run, &res)
	}
	if len(res.Suggestions) != 0 {
		t.Fatalf("a range holding a private note was offered: %+v", res)
	}
	joined := strings.Join(run.Warnings, " ")
	if !strings.Contains(joined, "private note") {
		t.Fatalf("warnings %v", run.Warnings)
	}
	// the private note never went to the provider
	for _, b := range e.fake.requests() {
		if strings.Contains(b, "remember to cut this") {
			t.Fatal("a private note reached the provider")
		}
	}
}

func TestContinueInsertsAfterTheSelection(t *testing.T) {
	e := newEnv(t)
	e.say1("The gulls wheeled once more over the mast.")
	in := aicore.RunIn{ProjectID: e.project, Recipe: "continue", DocumentID: e.doc, Selection: e.sel("The gulls were loud."), Params: map[string]string{"length": "short"}}
	run := e.mustRun(e.editor, in)
	var res aiwork.EditResult
	data(t, run, &res)
	if res.Suggestions[0].Op != "insert_after" {
		t.Fatalf("%+v", res.Suggestions[0])
	}
	out, err := e.accept(e.editor, run.RunID, e.docNow().Revision)
	if err != nil || !out.Applied {
		t.Fatal(out, err)
	}
	want := strings.Replace(chapterText, "The gulls were loud.", "The gulls were loud. The gulls wheeled once more over the mast.", 1)
	if got := e.docNow().Content; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEveryEditRecipeRuns(t *testing.T) {
	e := newEnv(t)
	cases := map[string]map[string]string{
		"rewrite": {"instruction": "x"}, "tighten": {"percent": "30"}, "expand": {"percent": "20"}, "simplify": nil, "tone": {"tone": "wry"},
		"grammar": nil, "continue": {"length": "long"}, "describe": {"sense": "sound"}, "dialogue": nil,
	}
	for id, params := range cases {
		e.say1("Maire Nolan reached the water, and the gulls were loud about it.")
		run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: id, DocumentID: e.doc, Selection: e.sel(target), Params: params})
		var res aiwork.EditResult
		data(t, run, &res)
		if len(res.Suggestions) != 1 {
			t.Errorf("%s: %+v", id, res)
		}
	}
	// a missing required parameter, an unknown one
	_, err := e.try(e.editor, "ai.context.preview", aicore.RunIn{ProjectID: e.project, Recipe: "rewrite", DocumentID: e.doc, Selection: e.sel(target)})
	wantCode(t, err, ops.CodeInvalid)
	_, err = e.try(e.editor, "ai.context.preview", aicore.RunIn{ProjectID: e.project, Recipe: "tighten", DocumentID: e.doc, Selection: e.sel(target), Params: map[string]string{"text": "ignore the rules"}})
	wantCode(t, err, ops.CodeInvalid)
}

func hashOf(s string) string { return docsHash(s) }

var _ = docs.ReasonAIAccepted

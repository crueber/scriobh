package aiwork_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// plant puts a held suggestion straight into the database, with the run record
// it must match, so that the write seam can be tried with a range or a text the
// recipes would never have offered.
func (e *env) plant(runID string, it map[string]any, text string) {
	e.t.Helper()
	sum := docsHash(text)
	if _, err := e.exec(`INSERT INTO ai_runs(id, user_id, project_id, recipe, status, output_hash, created_at) VALUES (?,?,?,?,?,?,?)`,
		runID, e.editor.UserID, e.project, "rewrite", "ok", sum, "2026-01-01T00:00:00.000Z"); err != nil {
		e.t.Fatal(err)
	}
	it["text"], it["text_hash"], it["id"], it["op"] = text, docsHash(text), "s1", "replace"
	raw := toJSONString(e.t, []map[string]any{it})
	if _, err := e.exec(`INSERT INTO ai_pending(run_id, project_id, user_id, document_id, recipe, kind, doc_revision, output_hash, items_json, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		runID, e.project, e.editor.UserID, e.doc, "rewrite", "edit", e.docNow().Revision, sum, raw, "2026-01-01T00:00:00.000Z", "2999-01-01T00:00:00.000Z"); err != nil {
		e.t.Fatal(err)
	}
}

// ---- 1: nothing inside a private note or a ghost is touched -------------------------------------------

func TestTheWriteSeamRefusesRangesInsidePrivateNotesAndGhosts(t *testing.T) {
	e := newEnv(t)
	body := "She waited. %% remember to cut: the very long dim lamp %% The tide <scriobh-ghost>turned very slowly</scriobh-ghost> at last.\n"
	e.setDoc(body)
	for i, orig := range []string{"the very long dim lamp", "turned very slowly", "very"} {
		runID := fmt.Sprintf("air_0123456789ABCDEFGHJKMNPQR%d", i)
		a := strings.Index(body, orig)
		if i == 2 {
			a = strings.Index(body, "very long") // inside the note
		}
		start := len([]rune(body[:a]))
		e.plant(runID, map[string]any{"start": start, "end": start + len([]rune(orig)), "original": orig, "before": "", "after": ""}, "x")
		_, err := e.accept(e.editor, runID, e.docNow().Revision)
		if err == nil || err.Code != ops.CodeConflict || !strings.Contains(err.Message, "private note or ghosted") {
			t.Fatalf("%q: %v", orig, err)
		}
	}
	if e.docNow().Content != body {
		t.Fatal("a private note or a ghost was changed")
	}
	// a Lab mark whose quote is inside a note is not made, and a planted one is skipped when accepted
	e.say1(`{"marks": [{"quote": "the very long dim lamp"}, {"quote": "turned very slowly"}]}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.trim", DocumentID: e.doc, Params: map[string]string{"mode": "filler"}})
	var mr struct {
		aiwork.MarksOut
		Unplaced int `json:"unplaced"`
	}
	data(t, run, &mr)
	if len(mr.Marks) != 0 || mr.Unplaced != 2 {
		t.Fatalf("%+v", mr)
	}
	a := strings.Index(body, "very long")
	u := len([]rune(body[:a]))
	if _, err := e.exec(`INSERT INTO ai_marks(id, project_id, user_id, document_id, run_id, source, kind, reason, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, has_fix, fix, status, created_at)
		VALUES ('mrk_0123456789ABCDEFGHJKMNPQRS',?,?,?,'lab_PLANTED00000000000000000A','local','punctuation_fix','x','very',?,?,?,?,?,1,'','open','2026-01-01T00:00:00.000Z')`,
		e.project, e.editor.UserID, e.doc, "\x02She waited. %% remember to cut: the ", " long dim lamp %% The tide <scriobh-ghost>turned very slowly</scriobh-ghost> at last.\x03"[:20], u, u+4, e.docNow().Revision); err != nil {
		t.Fatal(err)
	}
	out := e.call(e.editor, "ai.lab.accept_all", aiwork.LabAcceptAllIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{"mrk_0123456789ABCDEFGHJKMNPQRS"}, BaseRevision: e.docNow().Revision}).(aiwork.LabAcceptOut)
	if len(out.Applied) != 0 || e.docNow().Content != body {
		t.Fatalf("%+v", out)
	}
}

// ---- 2 and 3: a lookalike elsewhere is not the same place --------------------------------------------

func TestMarksAndSuggestionsDoNotMoveOntoALookalike(t *testing.T) {
	e := newEnv(t)
	e.setDoc("Alpha beta gamma.\n\nAn ordinary second paragraph.\n")
	e.say1(`{"marks": [{"quote": "Alpha beta gamma.", "reason": "x"}]}`)
	e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "lab.convoluted", DocumentID: e.doc})
	// a held rewrite of the second paragraph and candidates for the first sentence
	e.say1("A plain second paragraph.")
	rw := e.mustRun(e.editor, e.rewrite("An ordinary second paragraph.", "x"))
	e.say1(`{"alternatives": ["Alpha beta delta."]}`)
	alt, _ := e.altRun("Alpha beta gamma.", "sentence", 1)
	// the paragraphs go, and the same words are typed again inside another sentence
	e.setDoc("A new opening. Alpha beta gamma. And An ordinary second paragraph. said someone.\n")
	ms := e.labMarks("open")
	if len(ms.Marks) != 1 || ms.Marks[0].Anchor.State != "stale" {
		t.Fatalf("a whole-paragraph mark moved onto a lookalike: %+v", ms.Marks)
	}
	_, err := e.accept(e.editor, rw.RunID, e.docNow().Revision)
	wantCode(t, err, ops.CodeConflict)
	_, aerr := e.acceptAlt(alt.RunID, nil, "", "")
	wantCode(t, aerr, ops.CodeConflict)
	if e.docNow().Content != "A new opening. Alpha beta gamma. And An ordinary second paragraph. said someone.\n" {
		t.Fatal("a stale accept wrote")
	}
}

// ---- 4: a continuation may not restructure the document ---------------------------------------------

func TestContinueCannotRestructureTheDocument(t *testing.T) {
	e := newEnv(t)
	cont := func(out string) (aiwork.AcceptOut, *ops.Error) {
		e.say1(out)
		run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "continue", DocumentID: e.doc, Selection: e.sel("The gulls were loud."), Params: map[string]string{"length": "short"}})
		if len(run.Data) == 0 {
			return aiwork.AcceptOut{}, &ops.Error{Code: ops.CodeInvalid, Message: strings.Join(run.Warnings, " ")}
		}
		return e.accept(e.editor, run.RunID, e.docNow().Revision)
	}
	for name, out := range map[string]string{
		"heading": "Then she left.\n\n# A heading\n\nMore.", "list": "Then she left.\n\n- one\n- two", "quote": "Then she left.\n\n> said who", "fence": "Then she left.\n\n```\ncode\n```", "tilde fence": "Then she left.\n\n~~~\ncode\n~~~",
		"table": "Then she left.\n\n| a | b |\n|---|---|", "rule": "Then she left.\n\n---\n\nMore.", "indent": "Then she left.\n\n    code line", "ordered": "Then she left.\n1. first",
		"colon": "Then she left.\n\n::: note\nx\n:::",
	} {
		if _, err := cont(out); err == nil {
			t.Errorf("%s: a restructuring continuation was applied", name)
		}
	}
	if e.docNow().Content != chapterText {
		t.Fatalf("the text changed: %q", e.docNow().Content)
	}
	// plain paragraphs are fine
	if o, err := cont("Then she left.\n\nThe door shut behind her."); err != nil || !o.Applied {
		t.Fatal(o, err)
	}
	// and a replacement may not start a block the text did not have either
	e.setDoc(chapterText)
	e.say1("Maire Nolan walked.\n\n## Heading")
	run := e.mustRun(e.editor, e.rewrite(target, "x"))
	if len(run.Data) != 0 {
		_, err := e.accept(e.editor, run.RunID, e.docNow().Revision)
		if err == nil {
			t.Fatal("a replacement started a heading")
		}
	}
	// a continuation inside a list item or after a quote line would break the block it sits in
	e.setDoc("- A list item. The gulls were loud.\n- another\n")
	if _, err := cont("And more.\n\nA new paragraph."); err == nil {
		t.Error("a paragraph break was inserted into a list item")
	}
}

// ---- 5: no links, images or addresses that were not there --------------------------------------------

func TestNoRemoteImagesLinksOrWikiLinksAreWritten(t *testing.T) {
	e := newEnv(t)
	bad := []string{
		"Maire Nolan walked ![](https://evil.example/p.png?x=1) down.", "Maire Nolan [click](javascript:alert(1)) walked.", "Maire Nolan walked to [[Secret Entity]].",
		"Maire Nolan [here](https://evil.example/) walked.", "Maire Nolan walked, see https://evil.example/x.", "Maire Nolan [x][1] walked.\n\n[1]: https://evil.example/",
		"Maire Nolan [d](data:text/html;base64,AAAA) walked.", "Maire Nolan walked www.evil.example.",
	}
	for _, out := range bad {
		e.say1(out)
		run := e.mustRun(e.editor, e.rewrite(target, "x"))
		var res aiwork.EditResult
		if len(run.Data) > 0 {
			data(t, run, &res)
		}
		if len(res.Suggestions) != 0 {
			t.Errorf("%q became a suggestion", out)
		}
	}
	// the other recipes: candidates, summaries, sheets and chat wordings are filtered the same way
	e.say1(`{"alternatives": ["Maire went ![](https://evil.example/p.png).", "Maire went [x](https://evil.example/).", "Maire went home."]}`)
	_, res := e.altRun(target, "sentence", 3)
	if len(res.Candidates) != 1 || res.Candidates[0].Text != "Maire went home." {
		t.Fatalf("%+v", res.Candidates)
	}
	e.say1(`{"long": "See https://evil.example/ for [[Secret Entity]]."}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "summary.chapter", DocumentID: e.doc})
	if len(run.Data) != 0 {
		t.Fatalf("a summary with an address was offered: %s", run.Data)
	}
	e.say1(`{"entities": [{"name": "Tom", "kind": "character", "summary": "See [x](https://evil.example/)."}, {"name": "Ann", "kind": "character", "fields": {"home": "https://evil.example/"}}]}`)
	run = e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "extract.chapter", DocumentID: e.doc})
	var rv aiwork.ReviewOut
	data(t, run, &rv)
	for _, n := range rv.Notes {
		if strings.Contains(fmt.Sprint(n.Sheet.Summary, n.Sheet.Fields), "evil") {
			t.Fatalf("an address in a sheet: %+v", n.Sheet)
		}
	}
	e.say1("```replacement\nMaire Nolan walked ![](https://evil.example/p.png).\n```")
	run = e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "chat", DocumentID: e.doc, Selection: e.sel(target), Params: map[string]string{"message": "x"}})
	var cr aiwork.EditResult
	data(t, run, &cr)
	if len(cr.Suggestions) != 0 {
		t.Fatal("a chat wording with an image")
	}
	// the final gate holds even if a held row were planted
	e.plant("air_0123456789ABCDEFGHJKMNPQRS", map[string]any{"start": 23, "end": 23 + len([]rune(target)), "original": target, "before": "\x02The harbour was quiet. ", "after": " The gulls were loud."}, "Maire ![](https://evil.example/p.png) walked.")
	_, err := e.accept(e.editor, "air_0123456789ABCDEFGHJKMNPQRS", e.docNow().Revision)
	if err == nil || err.Code != ops.CodeInvalid {
		t.Fatalf("%v", err)
	}
	// a link that is in the text may stay
	e.setDoc("She read [the map](https://maps.example/a) twice.\n\nAnother paragraph.\n")
	e.say1("She studied [the map](https://maps.example/a) three times.")
	run = e.mustRun(e.editor, e.rewrite("She read [the map](https://maps.example/a) twice.", "x"))
	var kept aiwork.EditResult
	data(t, run, &kept)
	if len(kept.Suggestions) != 1 {
		t.Fatalf("a link already in the text was refused: %v", run.Warnings)
	}
	if o, err := e.accept(e.editor, run.RunID, e.docNow().Revision); err != nil || !o.Applied {
		t.Fatal(o, err)
	}
}

// ---- 6: never-send reaches the timeline and summaries chips too -----------------------------------------

func TestNeverSendIsHonouredByTheContinuityChips(t *testing.T) {
	e := newEnv(t)
	comp := e.compendium("Bible")
	e.setCompendium(comp)
	e.call(e.owner, "compendium.member.add", compendiums.MemberAddIn{ID: comp, Email: "editor@test.local", Role: "editor"})
	place := e.call(e.owner, "entity.create", entities.CreateIn{ID: comp, Kind: "place", Name: "Hidden Cove", Summary: "x"}).(entities.Item).Item.ID
	person := e.call(e.owner, "entity.create", entities.CreateIn{ID: comp, Kind: "character", Name: "Nobody Special", Summary: "x"}).(entities.Item).Item.ID
	e.call(e.owner, "entity.create", entities.CreateIn{ID: comp, Kind: "character", Name: "Open Person", Summary: "x"})
	e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: comp, Title: "Landing in the cove", StoryDate: "1998-04-12", Location: "Hidden Cove"})
	e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: comp, Title: "A private meeting", StoryDate: "1998-04-13", Participants: []timeline.Participant{{Entity: person}}})
	e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: comp, Title: "A public feast", StoryDate: "1998-04-14"})
	// a chapter with a bad date range gives a finding that names the chapter
	c2 := e.chapter("Secret Chapter Title", "The tide goes out.")
	end, start := "2000-04-01", "2000-05-01"
	d := e.call(e.owner, "doc.get", docs.DocIn{ID: c2}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: c2, BaseRevision: d.Revision, StoryDate: &start, StoryDateEnd: &end})
	// an earlier chapter with a summary
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: e.doc, BaseRevision: e.docNow().Revision, SynopsisLong: ptr("EARLIER-SECRET-SUMMARY")})
	c3 := e.chapter("Hidden Third Title", "Later still.")
	d3 := e.call(e.owner, "doc.get", docs.DocIn{ID: c3}).(docs.Item).Item
	e.call(e.owner, "doc.update", docs.UpdateIn{ID: c3, BaseRevision: d3.Revision, StoryDate: &start, StoryDateEnd: &end})
	e.say1(`{"findings": []}`)
	body := func() string {
		e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "continuity.chapter", DocumentID: c2})
		r := e.fake.requests()
		return r[len(r)-1]
	}
	b := body()
	for _, want := range []string{"Landing in the cove", "A private meeting", "A public feast", "Secret Chapter Title", "Hidden Third Title", "[finding:", "EARLIER-SECRET-SUMMARY"} {
		if !strings.Contains(b, want) {
			t.Fatalf("control: %q is not in the request", want)
		}
	}
	for _, x := range []struct{ kind, id string }{{"entity", place}, {"entity", person}, {"document", c3}, {"document", e.doc}} {
		e.call(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: x.kind, ID: x.id, Exclude: true})
	}
	b = body()
	for _, banned := range []string{"Landing in the cove", "A private meeting", "Hidden Cove", "Nobody Special", "EARLIER-SECRET-SUMMARY", "Hidden Third Title"} {
		if strings.Contains(b, banned) {
			t.Errorf("%q went to the provider although it is never-send", banned)
		}
	}
	if !strings.Contains(b, "A public feast") {
		t.Error("an event that names nothing excluded was dropped too")
	}
	// the finding about the other chapter, whose date range is as bad, is still there: only the excluded one went
	if !strings.Contains(b, "[finding:") || !strings.Contains(b, "Secret Chapter Title") {
		t.Error("a finding about a chapter that is not excluded was dropped too")
	}
}

// ---- 7: one rule for invisible characters, in every layer ------------------------------------------------

func TestInvisibleCharactersAreOneRuleEverywhere(t *testing.T) {
	e := newEnv(t)
	comp, _, _ := e.bible()
	_ = comp
	e.setDoc("Maire Nolan walked to the water.\n\nAnother.\n")
	for name, out := range map[string]string{"soft hyphen": "Maire Nolan wal­ked.", "arabic letter mark": "Maire Nolan ؜walked.", "tag characters": "Maire Nolan \U000e0041\U000e0042walked.", "variation": "Maire Nolan wal︁ked."} {
		e.say1(out)
		run := e.mustRun(e.editor, e.rewrite("Maire Nolan walked to the water.", "x"))
		if len(run.Data) > 0 {
			var res aiwork.EditResult
			data(t, run, &res)
			for _, sg := range res.Suggestions {
				for _, r := range sg.Text {
					if r == 0xad || r == 0x61c || r >= 0xe0000 && r <= 0xe007f || r == 0xfe01 {
						t.Errorf("%s: U+%04X in a suggestion", name, r)
					}
				}
			}
		}
	}
	// a name written with an invisible character is the same name: it cannot dodge the duplicate check
	e.say1(`{"entities": [{"name": "Mai­re No​lan", "kind": "character"}]}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "extract.chapter", DocumentID: e.doc})
	var rv aiwork.ReviewOut
	data(t, run, &rv)
	got := e.call(e.editor, "ai.review.get", aiwork.ReviewGetIn{ProjectID: e.project, ReviewID: rv.ID}).(aiwork.ReviewItem).Item
	if len(got.Notes) != 1 || got.Notes[0].Sheet.Name != "Maire Nolan" || got.Notes[0].Sheet.ExistsAs == nil {
		t.Fatalf("%+v", got.Notes)
	}
}

// ---- 8: dismissed stays dismissed -----------------------------------------------------------------------

func TestDismissedMarksStayDismissedAndSettledMarksArePruned(t *testing.T) {
	e := newEnv(t)
	e.setDoc("Perhaps the tide was quite high.  It turned.\n")
	first := e.analyse()
	var hedge, punct aiwork.MarkOut
	for _, m := range first.Marks {
		if m.Kind == "hedge_words" && hedge.ID == "" {
			hedge = m
		}
		if m.Kind == "punctuation_fix" {
			punct = m
		}
	}
	e.call(e.editor, "ai.lab.dismiss", aiwork.LabDismissIn{ProjectID: e.project, DocumentID: e.doc, MarkIDs: []string{hedge.ID}})
	again := e.analyse()
	for _, m := range again.Marks {
		if m.Kind == "hedge_words" && m.Quote == hedge.Quote && m.Anchor.Start == hedge.Anchor.Start {
			t.Fatalf("a dismissed mark came back: %+v", m)
		}
	}
	if len(again.Marks) != len(first.Marks)-1 {
		t.Fatalf("%d marks, was %d", len(again.Marks), len(first.Marks))
	}
	// a resolved observation stays resolved too
	var q aiwork.MarkOut
	for _, m := range again.Marks {
		if m.Kind == "hedge_words" {
			q = m
		}
	}
	e.call(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: q.ID, BaseRevision: e.docNow().Revision})
	for _, m := range e.analyse().Marks {
		if m.Kind == "hedge_words" && m.Quote == q.Quote && m.Anchor.Start == q.Anchor.Start {
			t.Fatalf("a resolved mark came back")
		}
	}
	_ = punct
	// settled marks do not pile up: only the newest 1000 are kept, and they do not count against the cap
	if _, err := e.exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 1200)
		INSERT INTO ai_marks(id, project_id, user_id, document_id, run_id, source, kind, reason, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, has_fix, fix, status, created_at, resolved_at)
		SELECT 'mrk_OLD' || printf('%022d', i), ?, ?, ?, 'lab_OLD', 'local', 'hedge_words', '', 'x' || i, '', '', 0, 1, 1, 0, '', 'dismissed', '2026-01-01T00:00:00.000Z', strftime('%Y-%m-%dT%H:%M:%fZ','now') FROM n`,
		e.project, e.editor.UserID, e.doc); err != nil {
		t.Fatal(err)
	}
	e.analyse()
	if n := e.count(`SELECT count(*) FROM ai_marks WHERE document_id = ? AND status <> 'open'`, e.doc); n > 1000 {
		t.Fatalf("%d settled marks kept", n)
	}
	// settled marks older than the keep time go
	if _, err := e.exec(`UPDATE ai_marks SET resolved_at = '2000-01-01T00:00:00.000Z' WHERE status <> 'open'`); err != nil {
		t.Fatal(err)
	}
	e.analyse()
	if n := e.count(`SELECT count(*) FROM ai_marks WHERE document_id = ? AND status <> 'open'`, e.doc); n != 0 {
		t.Fatalf("%d old settled marks kept", n)
	}
}

// ---- 9: the analysers do not run inside the writer ---------------------------------------------------------

func TestAnalysisDoesNotBlockSaves(t *testing.T) {
	e := newEnv(t)
	other := e.chapter("Other", "Small.")
	big := strings.Repeat("Perhaps  she was quite tired,, and so, rather, it goes. He said perhaps. ", 21000) // about 1.5 MiB
	e.setDoc(big)
	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		e.call(e.editor, "ai.lab.analyse", aiwork.LabAnalyseIn{ProjectID: e.project, DocumentID: e.doc})
	}()
	// saves go on for as long as the analysis runs; the slowest one is what counts
	var worst time.Duration
	saves := 0
loop:
	for i := 0; ; i++ {
		select {
		case <-done:
			break loop
		case <-time.After(15 * time.Millisecond):
		}
		t0 := time.Now()
		content := fmt.Sprintf("Small %d.", i)
		d := e.call(e.owner, "doc.get", docs.DocIn{ID: other}).(docs.Item).Item
		e.call(e.owner, "doc.update", docs.UpdateIn{ID: other, BaseRevision: d.Revision, Content: &content})
		saves++
		// timing: not a budget: keeps the slowest save; the limit is asserted in the "time" subtest below
		if d := time.Since(t0); d > worst {
			worst = d
		}
	}
	analysis := time.Since(started)
	if analysis < 150*time.Millisecond || saves < 2 {
		t.Skipf("the analysis took only %v here (%d saves): too quick to show that a save is not held up by it", analysis, saves)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if limit := 200 * time.Millisecond * timeScale; worst > limit {
			t.Errorf("a save waited %v while a %v analysis ran (limit %v)", worst, analysis, limit)
		}
	})
	// and a document that changes during the analysis is analysed again, not marked from stale text
	if n := e.count(`SELECT count(*) FROM ai_marks WHERE document_id = ?`, e.doc); n == 0 {
		t.Fatal("no marks")
	}
}

// ---- 10: retention ---------------------------------------------------------------------------------------------

func TestSweepRemovesWhatHasOutlivedItsUse(t *testing.T) {
	e := newEnv(t)
	e.say1("Maire Nolan strolled to the water.")
	run := e.mustRun(e.editor, e.rewrite(target, "x"))
	e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "slow"}]}`)
	_, rv := e.critique("critique.chapter", nil)
	if _, err := e.exec(`UPDATE ai_pending SET expires_at = '2000-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	// settled and stale things, by their dates
	if _, err := e.exec(`INSERT INTO ai_jobs(id, project_id, user_id, kind, status, plan_json, created_at, updated_at) VALUES
		('job_OLDDONE00000000000000000A', ?, ?, 'rolling_summary', 'done', '[]', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z'),
		('job_OLDOPEN00000000000000000A', ?, ?, 'rolling_summary', 'open', '[]', '2000-01-01T00:00:00.000Z', '2000-01-01T00:00:00.000Z')`,
		e.project, e.editor.UserID, e.project, e.editor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.exec(`INSERT INTO ai_accepts(run_id, item_id, project_id, user_id, action, created_at) VALUES ('air_OLD', 's1', ?, ?, 'accepted', '2000-01-01T00:00:00.000Z')`, e.project, e.editor.UserID); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{`ai_notes`} {
		if _, err := e.exec(`UPDATE ` + n + ` SET status = 'dismissed'`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.exec(`UPDATE ai_reviews SET created_at = '2000-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	n, err := aiwork.Sweep(t.Context(), e.d)
	if err != nil || n < 5 {
		t.Fatalf("%d %v", n, err)
	}
	for _, q := range []string{`SELECT count(*) FROM ai_pending`, `SELECT count(*) FROM ai_jobs`, `SELECT count(*) FROM ai_accepts WHERE run_id = 'air_OLD'`, `SELECT count(*) FROM ai_reviews WHERE id = '` + rv.ID + `'`} {
		if c := e.count(q); c != 0 {
			t.Errorf("%s = %d after the sweep", q, c)
		}
	}
	_ = run
	// a review with an open note is kept however old
	e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "slow"}]}`)
	_, keep := e.critique("critique.chapter", nil)
	if _, err := e.exec(`UPDATE ai_reviews SET created_at = '2000-01-01T00:00:00.000Z'`); err != nil {
		t.Fatal(err)
	}
	if _, err := aiwork.Sweep(t.Context(), e.d); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM ai_reviews WHERE id = ?`, keep.ID) != 1 {
		t.Fatal("a review with an open note was swept")
	}
}

func TestWhatIsKeptGoesWhenTheAccessGoes(t *testing.T) {
	e := newEnv(t)
	e.setDoc(chapterText + " Perhaps so.  Yes.")
	keep := func() {
		e.say1("Maire Nolan strolled to the water.")
		e.mustRun(e.editor, e.rewrite(target, "x"))
		e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "slow"}]}`)
		e.critique("critique.chapter", nil)
		e.call(e.editor, "ai.lab.analyse", aiwork.LabAnalyseIn{ProjectID: e.project, DocumentID: e.doc})
		e.call(e.editor, "ai.chat.create", aiwork.ChatCreateIn{ProjectID: e.project})
		e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project})
	}
	tables := []string{"ai_pending", "ai_reviews", "ai_marks", "ai_chats", "ai_jobs"}
	counts := func() map[string]int {
		m := map[string]int{}
		for _, tb := range tables {
			m[tb] = e.count(`SELECT count(*) FROM `+tb+` WHERE user_id = ?`, e.editor.UserID)
		}
		return m
	}
	keep()
	for _, tb := range tables {
		if counts()[tb] == 0 {
			t.Fatalf("%s: nothing kept to remove (%v)", tb, counts())
		}
	}
	// demoted below editor
	e.call(e.owner, "project.member.update", projects.MemberUpdateIn{ID: e.project, UserID: e.editor.UserID, Role: "reviewer"})
	for tb, n := range counts() {
		if n != 0 {
			t.Errorf("demoted: %s still holds %d rows", tb, n)
		}
	}
	e.call(e.owner, "project.member.update", projects.MemberUpdateIn{ID: e.project, UserID: e.editor.UserID, Role: "editor"})
	keep()
	// removed from the project
	e.call(e.owner, "project.member.remove", projects.MemberRemoveIn{ID: e.project, UserID: e.editor.UserID})
	for tb, n := range counts() {
		if n != 0 {
			t.Errorf("removed: %s still holds %d rows", tb, n)
		}
	}
	// a person's Compendium access ending takes their continuity and extraction reviews, and only those
	e.member(e.editor, ops.ProjectEditor)
	comp, _, _ := e.bible()
	e.say1(`{"findings": []}`)
	e.critique("continuity.chapter", nil)
	e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "slow"}]}`)
	e.critique("critique.chapter", nil)
	if e.count(`SELECT count(*) FROM ai_reviews WHERE user_id = ?`, e.editor.UserID) != 2 {
		t.Fatal("setup")
	}
	e.call(e.owner, "compendium.member.remove", compendiums.MemberRemoveIn{ID: comp, UserID: e.editor.UserID})
	if got := e.count(`SELECT count(*) FROM ai_reviews WHERE user_id = ? AND kind = 'continuity'`, e.editor.UserID); got != 0 {
		t.Fatalf("%d continuity reviews left", got)
	}
	if got := e.count(`SELECT count(*) FROM ai_reviews WHERE user_id = ? AND kind = 'critique'`, e.editor.UserID); got != 1 {
		t.Fatalf("the critique went too: %d", got)
	}
}

// ---- 11: what a person may keep is bounded -----------------------------------------------------------------

func TestKeptChatsAreBounded(t *testing.T) {
	e := newEnv(t)
	mk := func() string {
		return e.call(e.editor, "ai.chat.create", aiwork.ChatCreateIn{ProjectID: e.project}).(aiwork.ChatItem).Item.ID
	}
	plant := func(chat string, bytes int) {
		if _, err := e.exec(`INSERT INTO ai_chat_turns(chat_id, seq, role, content, created_at) VALUES (?, 1, 'user', hex(zeroblob(?)), '2026-01-01T00:00:00.000Z')`, chat, bytes/2); err != nil {
			t.Fatal(err)
		}
	}
	e.say1("An answer.")
	exchange := func(chat string) *ops.Error {
		run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "chat", Params: map[string]string{"message": "hi"}})
		_, err := e.try(e.editor, "ai.chat.append", aiwork.ChatAppendIn{ProjectID: e.project, ChatID: chat, RunID: run.RunID, Message: "hi", Answer: "An answer."})
		return err
	}
	// one chat is full at 2 MB
	c1 := mk()
	plant(c1, aiwork.MaxChatBytes-5)
	wantCode(t, exchange(c1), ops.CodeConflict)
	// and a person's chats on a project at 20 MB together
	for i := 0; i < 10; i++ {
		plant(mk(), aiwork.MaxChatBytes-100)
	}
	wantCode(t, exchange(mk()), ops.CodeConflict)
}

// ---- 12: the rest -------------------------------------------------------------------------------------------------

func TestReadOpsOfKeptResultsFollowTheAgentsRule(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "slow"}]}`)
	_, rv := e.critique("critique.chapter", nil)
	e.call(e.editor, "ai.chat.create", aiwork.ChatCreateIn{ProjectID: e.project})
	key := e.key(e.editor)
	calls := map[string]func() *ops.Error{
		"ai.review.list": func() *ops.Error {
			_, err := e.try(key, "ai.review.list", aiwork.ReviewListIn{ProjectID: e.project})
			return err
		},
		"ai.review.get": func() *ops.Error {
			_, err := e.try(key, "ai.review.get", aiwork.ReviewGetIn{ProjectID: e.project, ReviewID: rv.ID})
			return err
		},
		"ai.chat.list": func() *ops.Error {
			_, err := e.try(key, "ai.chat.list", aiwork.ChatListIn{ProjectID: e.project})
			return err
		},
		"ai.lab.marks": func() *ops.Error {
			_, err := e.try(key, "ai.lab.marks", aiwork.LabMarksIn{ProjectID: e.project, DocumentID: e.doc})
			return err
		},
		"ai.summary.rolling.get": func() *ops.Error {
			_, err := e.try(key, "ai.summary.rolling.get", aiwork.RollingGetIn{ProjectID: e.project})
			return err
		},
	}
	for name, f := range calls {
		if err := f(); err == nil || err.Code != ops.CodeForbidden {
			t.Errorf("%s as a key where agents are not allowed: %v", name, err)
		}
	}
	e.enable("ask", func(in *aicore.PolicySetIn) { b := true; in.AgentsMayUse = &b })
	for name, f := range calls {
		if err := f(); err != nil {
			t.Errorf("%s as a key where agents are allowed: %v", name, err)
		}
	}
}

func TestNoteLimitsAreHardInTheParserAndTheDatabase(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "` + strings.Repeat("Word. ", 400) + `", "example": "` + strings.Repeat("ex ", 200) + `"}]}`)
	_, rv := e.critique("critique.chapter", nil)
	if n := rv.Notes[0]; len([]rune(n.Note)) > 600 || len([]rune(n.Example)) > 160 {
		t.Fatalf("%d %d", len([]rune(n.Note)), len([]rune(n.Example)))
	}
	_, err := e.exec(`INSERT INTO ai_notes(id, review_id, position, body, at_revision) VALUES ('not_BIG', ?, 99, ?, 1)`, rv.ID, strings.Repeat("x", 3000))
	if err == nil {
		t.Fatal("the database accepted a note of 3,000 characters")
	}
}

func TestChoosingAnAIWordingIsRecordedAsAccepted(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"alternatives": ["Maire Nolan went down to the water."]}`)
	run, res := e.altRun(target, "sentence", 1)
	out, err := e.acceptAlt(run.RunID, nil, "", res.Candidates[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var reason, note string
	if err := e.d.Read().QueryRow(`SELECT reason, note FROM document_revisions WHERE id = ?`, out.RevisionID).Scan(&reason, &note); err != nil || reason != "ai-accepted" || !strings.Contains(note, "alternative") {
		t.Fatalf("%q %q %v", reason, note, err)
	}
}

// ---- the Lab works with AI switched off, and the rolling plan skips white-space chapters ----------------------

func TestLabLocalMarksWorkWithTheKillSwitchOn(t *testing.T) {
	e := newEnv(t)
	e.setDoc("A fine  day, perhaps.\n\nAnother paragraph.\n")
	e.call(e.admin, "ai.settings.set", aicore.SettingsSetIn{Enabled: ptr(false)})
	m := e.analyse()
	var fix aiwork.MarkOut
	for _, x := range m.Marks {
		if x.Kind == "punctuation_fix" {
			fix = x
		}
	}
	if fix.ID == "" {
		t.Fatalf("%+v", m.Marks)
	}
	if got := e.labMarks("open"); len(got.Marks) != len(m.Marks) {
		t.Fatalf("the marks cannot be listed with AI off: %d of %d", len(got.Marks), len(m.Marks))
	}
	out := e.call(e.editor, "ai.lab.accept", aiwork.LabAcceptIn{ProjectID: e.project, DocumentID: e.doc, MarkID: fix.ID, BaseRevision: e.docNow().Revision}).(aiwork.LabAcceptOut)
	if len(out.Applied) != 1 || !strings.HasPrefix(e.docNow().Content, "A fine day,") {
		t.Fatalf("%+v %q", out, e.docNow().Content)
	}
	// an API key lists marks only where agents may use AI
	_, err := e.try(e.key(e.editor), "ai.lab.marks", aiwork.LabMarksIn{ProjectID: e.project, DocumentID: e.doc})
	wantCode(t, err, ops.CodeForbidden)
}

func TestRollingPlanSkipsChaptersOfOnlyWhiteSpace(t *testing.T) {
	e := newEnv(t)
	blank := e.chapter("Blank", " \n\t\n")
	e.say1(`{"long": "x"}`)
	st := e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project}).(aiwork.JobItem)
	if st.Decision == nil || !st.Decision.Allowed {
		t.Fatalf("%+v", st)
	}
	for _, step := range st.Item.Steps {
		if step.DocumentID == blank {
			t.Fatal("a chapter of white space is in the plan")
		}
	}
	// and when it is the first chapter, the plan still starts
	e.call(e.owner, "doc.move", docs.MoveIn{ID: blank, After: ptr("")})
	st = e.call(e.editor, "ai.summary.rolling.start", aiwork.RollingStartIn{ProjectID: e.project}).(aiwork.JobItem)
	if st.Decision == nil || !st.Decision.Allowed || st.Item.Total != 1 {
		t.Fatalf("%+v", st)
	}
}

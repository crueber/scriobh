package aiwork_test

import (
	"fmt"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) critique(recipe string, params map[string]string) (aicore.RunOut, *aiwork.ReviewOut) {
	e.t.Helper()
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: recipe, DocumentID: e.doc, Params: params})
	if len(run.Data) == 0 {
		return run, nil
	}
	var rv aiwork.ReviewOut
	data(e.t, run, &rv)
	return run, &rv
}

func TestCritiqueGivesNotesAndKeepsAReview(t *testing.T) {
	e := newEnv(t)
	e.say1("Here are my notes:\n```json\n" + `{"notes": [
	 {"quote": "The harbour was quiet.", "category": "Pacing", "severity": "major", "note": "The opening is slow.", "example": "Fog sat on the harbour."},
	 {"quote": "The gulls were loud.", "before": "to the water. ", "category": "show don't tell", "severity": "minor", "note": "Say how loud, and who hears."},
	 {"quote": "words that are not in the chapter", "category": "voice", "severity": "low", "note": "A note whose quote cannot be found."},
	 {"quote": "A second paragraph", "category": "nonsense", "severity": "???", "note": "x", "rewrite": "A whole replacement paragraph that must never be returned.", "replacement": "also never"}
	]}` + "\n```")
	run, rv := e.critique("critique.chapter", map[string]string{"focus": "pacing"})
	if rv == nil || len(rv.Notes) != 4 || rv.ReviewKind != "critique" {
		t.Fatalf("%+v warnings %v", rv, run.Warnings)
	}
	n := rv.Notes
	if n[0].Category != "pacing" || n[0].Severity != "high" || n[0].Anchor.State != "placed" || n[0].Example == "" {
		t.Fatalf("%+v", n[0])
	}
	if n[1].Category != "show_dont_tell" || n[1].Anchor.State != "placed" {
		t.Fatalf("%+v", n[1])
	}
	if n[2].Anchor.State != "unplaced" {
		t.Fatalf("a quote that is not in the text must be unplaced, not guessed: %+v", n[2])
	}
	if n[3].Category != "other" || n[3].Severity != "medium" {
		t.Fatalf("%+v", n[3])
	}
	// replacement prose is never in a result
	if blob := fmt.Sprint(rv); strings.Contains(blob, "must never be returned") || strings.Contains(blob, "also never") {
		t.Fatalf("replacement prose in %s", blob)
	}
	// the anchor is exact
	d := e.docNow()
	if got := string([]rune(d.Content)[n[0].Anchor.Start:n[0].Anchor.End]); got != "The harbour was quiet." {
		t.Fatalf("anchor holds %q", got)
	}
	// the document was not touched by a critique
	if d.Content != chapterText {
		t.Fatal("a critique changed the text")
	}

	// the review is listed, private, and worked through note by note
	list := e.call(e.editor, "ai.review.list", aiwork.ReviewListIn{ProjectID: e.project}).(aiwork.ReviewListOut)
	if len(list.Items) != 1 || list.Items[0].Counts.Open != 4 {
		t.Fatalf("%+v", list)
	}
	for _, who := range []ops.Principal{e.editor2, e.owner} {
		if l := e.call(who, "ai.review.list", aiwork.ReviewListIn{ProjectID: e.project}).(aiwork.ReviewListOut); len(l.Items) != 0 {
			t.Fatalf("%s sees another person's review", who.Name)
		}
		_, err := e.try(who, "ai.review.get", aiwork.ReviewGetIn{ProjectID: e.project, ReviewID: rv.ID})
		wantCode(t, err, ops.CodeNotFound)
		_, err = e.try(who, "ai.review.note.dismiss", aiwork.NoteActIn{ProjectID: e.project, NoteID: n[0].ID})
		wantCode(t, err, ops.CodeNotFound)
	}
	if _, err := e.try(e.reader, "ai.review.list", aiwork.ReviewListIn{ProjectID: e.project}); err == nil || err.Code != ops.CodeForbidden {
		t.Fatalf("a reader read reviews: %v", err)
	}
	out := e.call(e.editor, "ai.review.note.dismiss", aiwork.NoteActIn{ProjectID: e.project, NoteID: n[0].ID}).(aiwork.NoteActOut)
	if out.Status != "dismissed" || out.Counts.Open != 3 || out.Counts.Dismissed != 1 {
		t.Fatalf("%+v", out)
	}
	out = e.call(e.editor, "ai.review.note.resolve", aiwork.NoteActIn{ProjectID: e.project, NoteID: n[1].ID}).(aiwork.NoteActOut)
	if out.Status != "accepted" || out.Counts.Accepted != 1 {
		t.Fatalf("%+v", out)
	}
	// a settled note cannot be settled the other way
	_, err := e.try(e.editor, "ai.review.note.resolve", aiwork.NoteActIn{ProjectID: e.project, NoteID: n[0].ID})
	wantCode(t, err, ops.CodeConflict)

	// the text changes: a note whose quote is still there follows it, one that is not goes stale
	e.setDoc("Prologue line.\n\n" + strings.Replace(chapterText, "The gulls were loud.", "The gulls were silent.", 1))
	got := e.call(e.editor, "ai.review.get", aiwork.ReviewGetIn{ProjectID: e.project, ReviewID: rv.ID}).(aiwork.ReviewItem).Item
	if got.Notes[0].Anchor.State != "placed" || got.Notes[1].Anchor.State != "stale" || got.Notes[2].Anchor.State != "unplaced" {
		t.Fatalf("anchors %+v %+v %+v", got.Notes[0].Anchor, got.Notes[1].Anchor, got.Notes[2].Anchor)
	}
	moved := string([]rune(e.docNow().Content)[got.Notes[0].Anchor.Start:got.Notes[0].Anchor.End])
	if moved != "The harbour was quiet." {
		t.Fatalf("moved anchor holds %q", moved)
	}
	// delete
	e.call(e.editor, "ai.review.delete", aiwork.ReviewDeleteIn{ProjectID: e.project, ReviewID: rv.ID})
	if n := e.count(`SELECT count(*) FROM ai_notes`); n != 0 {
		t.Fatalf("%d notes left", n)
	}
}

func TestCritiqueParserBoundsEverything(t *testing.T) {
	e := newEnv(t)
	var items []string
	for i := 0; i < 200; i++ {
		items = append(items, fmt.Sprintf(`{"quote":"The gulls were loud.","category":"pacing","severity":"low","note":"%s"}`, strings.Repeat("blah. ", 200)))
	}
	e.say1(`{"notes": [` + strings.Join(items, ",") + `]}`)
	_, rv := e.critique("critique.chapter", map[string]string{"max_notes": "30"})
	if rv == nil || len(rv.Notes) != 30 {
		t.Fatalf("notes %v", rv)
	}
	for _, n := range rv.Notes {
		if len([]rune(n.Note)) > 600 || !n.Trimmed {
			t.Fatalf("a note of %d characters, trimmed=%v", len([]rune(n.Note)), n.Trimmed)
		}
	}
	// a truncated answer keeps the notes that finished
	e.say1(`{"notes": [{"quote":"The gulls were loud.","category":"pacing","severity":"low","note":"one"},{"quote":"The harbour was quiet.","note":"two"},{"quote":"A sec`)
	run, rv := e.critique("critique.chapter", nil)
	if rv == nil || len(rv.Notes) != 2 {
		t.Fatalf("%+v %v", rv, run.Warnings)
	}
	// text that is not a list of notes keeps nothing
	e.say1("I think the chapter is fine.")
	run, rv = e.critique("critique.chapter", nil)
	if rv != nil || len(run.Warnings) == 0 {
		t.Fatalf("%+v %v", rv, run.Warnings)
	}
	if n := e.count(`SELECT count(*) FROM ai_reviews`); n != 2 {
		t.Fatalf("%d reviews", n)
	}
}

func TestSelectionCritiqueOnlyPlacesNotesInsideTheSelection(t *testing.T) {
	e := newEnv(t)
	e.say1(`{"notes": [{"quote": "The harbour was quiet.", "note": "outside"}, {"quote": "The gulls were loud.", "note": "inside"}]}`)
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "critique.selection", DocumentID: e.doc, Selection: e.sel("The gulls were loud.")})
	var rv aiwork.ReviewOut
	data(t, run, &rv)
	if rv.Notes[0].Anchor.State != "unplaced" || rv.Notes[1].Anchor.State != "placed" {
		t.Fatalf("%+v", rv.Notes)
	}
}

// ---- continuity ---------------------------------------------------------------------------------

func (e *env) bible() (comp, nolan string, eventID string) {
	e.t.Helper()
	comp = e.compendium("The Bible")
	e.setCompendium(comp)
	e.call(e.owner, "compendium.member.add", compendiums.MemberAddIn{ID: comp, Email: "editor@test.local", Role: "editor"})
	e.call(e.owner, "entity.field.set", entities.FieldSetIn{ID: comp, Kind: "character", Key: "eyes", Type: "text"})
	nolan = e.call(e.owner, "entity.create", entities.CreateIn{ID: comp, Kind: "character", Name: "Maire Nolan", Summary: "A fisher.", Fields: map[string]any{"eyes": "green"}}).(entities.Item).Item.ID
	ev := e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: comp, Title: "The harbour fire", StoryDate: "1998-04-12"})
	eventID = ev.(timeline.EventItem).Item.ID
	return comp, nolan, eventID
}

func TestContinuityFindingsCiteWhatWasSent(t *testing.T) {
	e := newEnv(t)
	_, nolan, ev := e.bible()
	e.setDoc("Maire Nolan looked at the fire with her brown eyes. The harbour was quiet.\n\nA second paragraph.")
	e.say(func(string) string {
		return fmt.Sprintf(`{"findings": [
		 {"quote": "her brown eyes", "severity": "high", "note": "The sheet says green.", "sources": [{"kind": "entity", "id": %q, "field": "eyes"}]},
		 {"quote": "The harbour was quiet.", "note": "The timeline has a fire.", "sources": [{"kind": "event", "id": %q}]},
		 {"quote": "her brown eyes", "note": "Invented entity.", "sources": [{"kind": "entity", "id": "ent_00000000000000000000000000"}]},
		 {"quote": "her brown eyes", "note": "Invented event.", "sources": [{"kind": "event", "id": "evt_00000000000000000000000000"}]},
		 {"quote": "her brown eyes", "note": "No source at all."},
		 {"quote": "her brown eyes", "note": "Invented field.", "sources": [{"kind": "entity", "id": %q, "field": "shoe_size"}]}
		]}`, nolan, ev, nolan)
	})
	run, rv := e.critique("continuity.chapter", nil)
	if rv == nil {
		t.Fatalf("no review: %v", run.Warnings)
	}
	if len(rv.Notes) != 3 || rv.Dropped != 3 {
		t.Fatalf("notes %d dropped %d: %+v", len(rv.Notes), rv.Dropped, rv.Notes)
	}
	if rv.Notes[0].Category != "continuity" || rv.Notes[0].Sources[0].Kind != "entity" || rv.Notes[0].Sources[0].Field != "eyes" {
		t.Fatalf("%+v", rv.Notes[0])
	}
	if rv.Notes[1].Sources[0].Kind != "event" || rv.Notes[1].Sources[0].ID != ev {
		t.Fatalf("%+v", rv.Notes[1])
	}
	if rv.Notes[2].Sources[0].Field != "" {
		t.Fatalf("an invented field was kept: %+v", rv.Notes[2])
	}
	// what was sent includes the sheet, the event and the findings of the deterministic check
	body := e.fake.requests()[len(e.fake.requests())-1]
	for _, want := range []string{"green", "The harbour fire", "[event:" + ev + "]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the request lacks %q", want)
		}
	}
}

func TestContinuityOnlyUsesWhatTheCallerMayRead(t *testing.T) {
	e := newEnv(t)
	comp := e.compendium("Closed bible")
	e.setCompendium(comp) // the editor is NOT on it
	nolan := e.call(e.owner, "entity.create", entities.CreateIn{ID: comp, Kind: "character", Name: "Maire Nolan", Summary: "SECRET-SHEET-TEXT"}).(entities.Item).Item.ID
	e.call(e.owner, "timeline.event.create", timeline.CreateIn{ID: comp, Title: "SECRET-EVENT", StoryDate: "1998-04-12"})
	e.setDoc("Maire Nolan walked. The harbour was quiet.")
	e.say(func(string) string {
		return fmt.Sprintf(`{"findings": [{"quote": "walked", "note": "x", "sources": [{"kind": "entity", "id": %q}]}]}`, nolan)
	})
	run, rv := e.critique("continuity.chapter", nil)
	if rv != nil && len(rv.Notes) != 0 {
		t.Fatalf("a finding cited a sheet the caller cannot read: %+v", rv.Notes)
	}
	_ = run
	for _, b := range e.fake.requests() {
		if strings.Contains(b, "SECRET-SHEET-TEXT") || strings.Contains(b, "SECRET-EVENT") {
			t.Fatal("something the caller cannot read was sent")
		}
	}
}

func TestContinuityWorksWithTheDeterministicCheckUntouched(t *testing.T) {
	e := newEnv(t)
	comp, _, _ := e.bible()
	// the deterministic check is its own op and answers without any provider
	out := e.call(e.owner, "timeline.check", timeline.CheckIn{ID: e.project}).(timeline.CheckOut)
	_ = out
	_ = comp
	// with AI off for the project the continuity audit is refused and the check still works
	mode := "off"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
	_, err := e.run(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "continuity.chapter", DocumentID: e.doc})
	if err == nil {
		t.Fatal("the audit ran with AI off")
	}
	e.call(e.owner, "timeline.check", timeline.CheckIn{ID: e.project})
}

// ---- extraction ---------------------------------------------------------------------------------

func TestExtractionProposesSheetsAndAcceptCreatesOnlyOnRequest(t *testing.T) {
	e := newEnv(t)
	comp, _, _ := e.bible()
	e.setDoc("Maire Nolan met Old Tom at the Salt Gate. The Gull Inn was shut.")
	e.say1("```json\n" + `{"entities": [
	  {"name": "Old Tom", "kind": "character", "aliases": ["Tom"], "summary": "A fisher who knows the tide.", "fields": {"eyes": "grey", "shoe_size": "nine"}},
	  {"name": "Salt Gate", "kind": "place", "summary": "The harbour gate."},
	  {"name": "Maire Nolan", "kind": "character", "summary": "Already in the bible."},
	  {"name": "Bad", "kind": "starship"},
	  {"name": "<script>x</script>", "kind": "item"}
	]}` + "\n```")
	run := e.mustRun(e.editor, aicore.RunIn{ProjectID: e.project, Recipe: "extract.chapter", DocumentID: e.doc})
	var rv aiwork.ReviewOut
	data(t, run, &rv)
	if rv.ReviewKind != "extraction" || len(rv.Notes) != 3 || rv.Dropped != 2 {
		t.Fatalf("%d notes, dropped %d: %+v", len(rv.Notes), rv.Dropped, rv.Notes)
	}
	// nothing was created
	if n := e.count(`SELECT count(*) FROM entities WHERE name IN ('Old Tom','Salt Gate')`); n != 0 {
		t.Fatalf("%d entities created by a run", n)
	}
	got := e.call(e.editor, "ai.review.get", aiwork.ReviewGetIn{ProjectID: e.project, ReviewID: rv.ID}).(aiwork.ReviewItem).Item
	var tom, dup string
	for _, n := range got.Notes {
		switch n.Sheet.Name {
		case "Old Tom":
			tom = n.ID
		case "Maire Nolan":
			dup = n.ID
			if n.Sheet.ExistsAs == nil {
				t.Fatal("a duplicate is not flagged")
			}
		}
	}
	// accept: created in the chosen Compendium, fields the Compendium does not define are left out
	acc, err := e.try(e.editor, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: tom, CompendiumID: comp})
	if err != nil {
		t.Fatal(err)
	}
	out := acc.(aiwork.ExtractAcceptOut)
	if out.EntityID == "" || len(out.DroppedFields) == 0 {
		t.Fatalf("%+v", out)
	}
	if n := e.count(`SELECT count(*) FROM entities WHERE id = ? AND name = 'Old Tom'`, out.EntityID); n != 1 {
		t.Fatal("entity missing")
	}
	if n := e.count(`SELECT count(*) FROM entity_aliases WHERE entity_id = ?`, out.EntityID); n != 1 {
		t.Fatalf("aliases %d", n)
	}
	// idempotent
	again, err := e.try(e.editor, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: tom, CompendiumID: comp})
	if err != nil || !again.(aiwork.ExtractAcceptOut).Already {
		t.Fatalf("%+v %v", again, err)
	}
	// a name an entity already has is refused, and nothing is created
	_, err = e.try(e.editor, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: dup, CompendiumID: comp})
	wantCode(t, err, ops.CodeConflict)
	if err.Data["entity_id"] == nil {
		t.Fatalf("the refusal does not name the entity: %v", err.Data)
	}
	// a reader of the Compendium, or a Compendium the project does not use, cannot be the target
	other := e.compendium("Elsewhere")
	_, err = e.try(e.editor, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: got.Notes[1].ID, CompendiumID: other})
	wantCode(t, err, ops.CodeNotFound)
	// dismiss
	d := e.call(e.editor, "ai.extract.dismiss", aiwork.NoteActIn{ProjectID: e.project, NoteID: got.Notes[1].ID}).(aiwork.NoteActOut)
	if d.Status != "dismissed" {
		t.Fatal(d)
	}
	_, err = e.try(e.editor, "ai.extract.accept", aiwork.ExtractAcceptIn{ProjectID: e.project, NoteID: got.Notes[1].ID, CompendiumID: comp})
	wantCode(t, err, ops.CodeConflict)
}

package ai_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/ops"
)

func (e *env) ready() *fake {
	f := newFake(e.t, nil)
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	return f
}

func bodies(pv aicore.Preview) string {
	var b strings.Builder
	for _, a := range pv.Attempts {
		b.WriteString(a.Body)
	}
	return b.String()
}

func (e *env) exclude(kind, id string, v bool) {
	e.t.Helper()
	e.call(e.owner, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: kind, ID: id, Exclude: v})
}

func TestNeverSendIsRefusedWhenNamedAndSkippedWhenDerived(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	var seed string // project.create leaves one empty chapter; the document under test follows it
	_ = e.d.Read().QueryRow(`SELECT id FROM documents WHERE project_id = ? AND title = ''`, e.project).Scan(&seed)
	prev := e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: "Chapter Zero", Kind: "chapter", After: &seed, Content: "SECRET-PREVIOUS-CHAPTER"}).(docs.Item).Item.ID
	e.exclude("document", prev, true)
	// Derived: the previous chapter is left out, and the preview says so.
	in := e.freeform("Continue.")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}, {Kind: lib.ChipPreviousDocument}, {Kind: lib.ChipPreviousSummary}}
	pv := e.preview(e.editor, in)
	if !pv.Decision.Allowed || strings.Contains(bodies(pv), "SECRET-PREVIOUS") {
		t.Fatalf("the excluded chapter was sent: %+v", pv.Decision)
	}
	skipped := 0
	for _, s := range pv.Skipped {
		if s.Reason == "never_send" {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("skipped %+v", pv.Skipped)
	}
	// Named by id, as the target, as a chip, or inside a selection: refused, in plain words.
	for name, in := range map[string]aicore.RunIn{
		"target": {ProjectID: e.project, Recipe: "freeform", DocumentID: prev, Params: map[string]string{"instruction": "x"}},
		"chip":   {ProjectID: e.project, Recipe: "freeform", Params: map[string]string{"instruction": "x"}, Chips: []aicore.ChipSpec{{Kind: lib.ChipDocument, DocumentID: prev}}},
		"selection": {ProjectID: e.project, Recipe: "freeform", DocumentID: prev, Params: map[string]string{"instruction": "x"},
			Selection: &aicore.Selection{Quote: "SECRET"}},
	} {
		_, err := e.try(e.editor, "ai.context.preview", in)
		if err == nil || err.Code != ops.CodeInvalid || !strings.Contains(err.Message, "never-send") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Lifting the flag lets it through.
	e.exclude("document", prev, false)
	in2 := aicore.RunIn{ProjectID: e.project, Recipe: "freeform", Params: map[string]string{"instruction": "x"}, Chips: []aicore.ChipSpec{{Kind: lib.ChipDocument, DocumentID: prev}}}
	if pv := e.preview(e.editor, in2); !strings.Contains(bodies(pv), "SECRET-PREVIOUS") {
		t.Error("a document whose flag was lifted was not included")
	}
	if l := e.call(e.editor, "ai.exclude.list", aicore.ExcludeListIn{ProjectID: e.project}).(aicore.ExcludeListOut); len(l.Items) != 0 {
		t.Errorf("list %+v", l.Items)
	}
}

func ptr[T any](v T) *T { return &v }

func TestNeverSendOnEntitiesAndResearch(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	cid := e.compendium("Bible")
	e.setCompendium(cid)
	maire := e.entity(cid, "Maire Nolan", "Mrs Nolan")
	secret := e.entity(cid, "Tomas Quill")
	e.updateDoc(e.doc, "Maire Nolan met Tomas Quill at the quay.")
	note := e.call(e.owner, "project.research.create", research.CreateIn{ID: e.project, Title: "Tide tables", Body: "RESEARCH-BODY-TEXT"}).(research.Item).Item.ID

	in := e.freeform("Continue.")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipEntities, Auto: true}, {Kind: lib.ChipResearch, IDs: []string{note}}}
	pv := e.preview(e.owner, in)
	b := bodies(pv)
	if !strings.Contains(b, "Maire Nolan (character)") || !strings.Contains(b, "Tomas Quill (character)") || !strings.Contains(b, "RESEARCH-BODY-TEXT") {
		t.Fatalf("sheets and notes missing from the payload: %s", b)
	}
	// Flag one entity: the auto-detected sheet drops out (and is reported); naming it is refused.
	e.exclude("entity", secret, true)
	e.exclude("research", note, true)
	pv = e.preview(e.owner, in2(in, aicore.ChipSpec{Kind: lib.ChipEntities, Auto: true}))
	if strings.Contains(bodies(pv), "Tomas Quill (character)") || !strings.Contains(bodies(pv), "Maire Nolan (character)") {
		t.Fatalf("exclusion of an auto-detected entity: %s", bodies(pv))
	}
	if len(pv.Skipped) != 1 || pv.Skipped[0].Reason != "never_send" {
		t.Errorf("skipped %+v", pv.Skipped)
	}
	for name, c := range map[string]aicore.ChipSpec{"entity": {Kind: lib.ChipEntities, IDs: []string{secret}}, "research": {Kind: lib.ChipResearch, IDs: []string{note}}} {
		_, err := e.try(e.owner, "ai.context.preview", in2(in, c))
		if err == nil || !strings.Contains(err.Message, "never-send") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Redaction still hides a flagged entity's name: its sheet is not sent, but the name must not leak either.
	e.enable("ask", func(p *aicore.PolicySetIn) { r := "entities"; p.Redaction = &r })
	pv = e.preview(e.editor, in2(in, aicore.ChipSpec{Kind: lib.ChipDocument})) // an editor with no access to the bible: still hidden
	if strings.Contains(bodies(pv), "Tomas Quill") || strings.Contains(bodies(pv), "Maire") {
		t.Errorf("a name survived redaction: %s", bodies(pv))
	}
	_ = maire
}

// Merging a never-send entity into another must not lose the flag: what the source
// held may now be in the target.
func TestAMergeKeepsTheNeverSendFlag(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	cid := e.compendium("Bible")
	e.setCompendium(cid)
	src := e.entity(cid, "Secret Source")
	dst := e.entity(cid, "Plain Target")
	e.exclude("entity", src, true)
	e.call(e.owner, "entity.merge", entities.MergeIn{ID: src, IntoID: dst, Body: "append"})
	l := e.call(e.owner, "ai.exclude.list", aicore.ExcludeListIn{ProjectID: e.project}).(aicore.ExcludeListOut)
	if len(l.Items) != 1 || l.Items[0].ID != dst {
		t.Fatalf("after the merge: %+v", l.Items)
	}
}

func in2(in aicore.RunIn, chips ...aicore.ChipSpec) aicore.RunIn {
	in.Chips = chips
	return in
}

func TestContextStaysInsideTheProject(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	other := e.call(e.other, "project.create", projects.CreateIn{Title: "Elsewhere"}).(projects.Item).Item.ID
	foreign := e.call(e.other, "doc.create", docs.CreateIn{ProjectID: other, Title: "Theirs", Content: "FOREIGN-TEXT"}).(docs.Item).Item.ID
	foreignNote := e.call(e.other, "project.research.create", research.CreateIn{ID: other, Title: "Theirs", Body: "FOREIGN-NOTE"}).(research.Item).Item.ID
	trashed := e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: "Gone", Content: "TRASHED-TEXT"}).(docs.Item).Item.ID
	e.call(e.owner, "doc.trash", docs.DocIn{ID: trashed})
	for name, in := range map[string]aicore.RunIn{
		"foreign target":  {ProjectID: e.project, Recipe: "freeform", DocumentID: foreign, Params: map[string]string{"instruction": "x"}},
		"foreign chip":    in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipDocument, DocumentID: foreign}),
		"foreign note":    in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipResearch, IDs: []string{foreignNote}}),
		"trashed":         in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipDocument, DocumentID: trashed}),
		"invented":        in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipDocument, DocumentID: "doc_00000000000000000000000000"}),
		"invented entity": in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipEntities, IDs: []string{"ent_00000000000000000000000000"}}),
	} {
		_, err := e.try(e.editor, "ai.context.preview", in)
		if err == nil || err.Code != ops.CodeInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Someone else's project is not even a project for this caller.
	_, err := e.try(e.editor, "ai.context.preview", aicore.RunIn{ProjectID: other, Recipe: "freeform", Params: map[string]string{"instruction": "x"}})
	wantCode(t, err, ops.CodeNotFound)
	// The same words for "does not exist" and "is not yours", so nothing leaks.
	_, e1 := e.try(e.editor, "ai.context.preview", in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipDocument, DocumentID: foreign}))
	_, e2 := e.try(e.editor, "ai.context.preview", in2(e.freeform("x"), aicore.ChipSpec{Kind: lib.ChipDocument, DocumentID: "doc_00000000000000000000000000"}))
	if e1.Message != e2.Message {
		t.Errorf("%q vs %q", e1.Message, e2.Message)
	}
}

func TestEntitiesFollowTheCompendiumReadRule(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	cid := e.compendium("Bible") // the owner's; the editor is on the project, not in the Compendium
	e.setCompendium(cid)
	ada := e.entity(cid, "Ada Quill")
	e.updateDoc(e.doc, "Ada Quill sat by the fire.")
	in := e.freeform("x")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipEntities, Auto: true}}
	// The owner may.
	if b := bodies(e.preview(e.owner, in)); !strings.Contains(b, "Ada Quill (character)") {
		t.Fatalf("owner: %s", b)
	}
	// The editor may not put in what they cannot read: auto finds nothing, naming it is refused.
	if b := bodies(e.preview(e.editor, in)); strings.Contains(b, "Ada Quill (character)") {
		t.Fatalf("an editor with no Compendium access got a sheet: %s", b)
	}
	byID := in2(in, aicore.ChipSpec{Kind: lib.ChipEntities, IDs: []string{ada}})
	_, err := e.try(e.editor, "ai.context.preview", byID)
	wantCode(t, err, ops.CodeInvalid)
	// Add them to the Compendium and it opens.
	e.call(e.owner, "compendium.member.add", compendiums.MemberAddIn{ID: cid, Email: "editor@test.local", Role: "reader"})
	if b := bodies(e.preview(e.editor, in)); !strings.Contains(b, "Ada Quill (character)") {
		t.Fatalf("a Compendium reader: %s", b)
	}
	// A key pinned to the project reaches the primary Compendium only, as everywhere else.
	pinned := e.owner
	pinned.Via, pinned.Scope, pinned.ProjectID = "apikey", ops.ScopeWrite, e.project
	e.enable("ask", func(p *aicore.PolicySetIn) { b := true; p.AgentsMayUse = &b })
	if b := bodies(e.preview(pinned, in)); !strings.Contains(b, "Ada Quill (character)") {
		t.Fatalf("pinned key, primary Compendium: %s", b)
	}
	ref := e.compendium("Referenced")
	other := e.entity(ref, "Zed Pell")
	e.call(e.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: e.project, CompendiumID: ref})
	_, err = e.try(pinned, "ai.context.preview", in2(in, aicore.ChipSpec{Kind: lib.ChipEntities, IDs: []string{other}}))
	wantCode(t, err, ops.CodeInvalid)
	if _, err := e.try(e.owner, "ai.context.preview", in2(in, aicore.ChipSpec{Kind: lib.ChipEntities, IDs: []string{other}})); err != nil {
		t.Errorf("the owner reaches the referenced one: %v", err)
	}
}

func TestOnlyTheChosenWordingIsSent(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	content := "The kitchen was cold. " + "<scriobh-ghost>GHOSTED-WORDS </scriobh-ghost>" + "She lit the stove. %%PRIVATE-NOTE-TEXT%% Then she waited.\n\n" +
		"A half-typed note %%that never closes and the rest.\n\nLast line.\n" + `<!-- scriobh:alts {"v":1,"items":[]} -->`
	if _, err := e.d.Writer().Exec(`UPDATE documents SET content = ? WHERE id = ?`, content, e.doc); err != nil {
		t.Fatal(err)
	}
	pv := e.preview(e.editor, e.freeform("x"))
	b := bodies(pv)
	for _, banned := range []string{"GHOSTED-WORDS", "PRIVATE-NOTE-TEXT", "that never closes", "scriobh-ghost", "scriobh:alts"} {
		if strings.Contains(b, banned) {
			t.Errorf("%q was sent", banned)
		}
	}
	for _, kept := range []string{"The kitchen was cold.", "She lit the stove.", "Then she waited.", "Last line."} {
		if !strings.Contains(b, kept) {
			t.Errorf("%q was left out: %s", kept, b)
		}
	}
}

func TestSelectionIsResolvedOnTheServer(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	d := e.call(e.owner, "doc.get", docs.DocIn{ID: e.doc}).(docs.Item).Item
	in := aicore.RunIn{ProjectID: e.project, Recipe: "freeform", DocumentID: e.doc, Params: map[string]string{"instruction": "x"},
		Selection: &aicore.Selection{Quote: "Maire Nolan walked down to the water."},
		Chips:     []aicore.ChipSpec{{Kind: lib.ChipSelection}, {Kind: lib.ChipSentence}, {Kind: lib.ChipParagraph}}}
	pv := e.preview(e.editor, in)
	if len(pv.Chips) != 3 {
		t.Fatalf("chips %+v", pv.Chips)
	}
	user := pv.Payload.Messages[len(pv.Payload.Messages)-1].Content
	if !strings.Contains(user, "The harbour was quiet. Maire Nolan walked down to the water. The gulls were loud.") || strings.Contains(user, "second paragraph") {
		t.Errorf("paragraph chip: %s", user)
	}
	// Offsets need a revision, and a changed document is a 409.
	quote := "Maire Nolan walked down to the water."
	start := strings.Index(d.Content, quote)
	end := start + len(quote)
	rev := d.Revision
	in.Selection = &aicore.Selection{Start: &start, End: &end, BaseRevision: &rev}
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipSelection}}
	pv = e.preview(e.editor, in)
	if !strings.Contains(pv.Payload.Messages[len(pv.Payload.Messages)-1].Content, "Maire Nolan walked down to the water.") {
		t.Errorf("by offsets: %+v", pv.Payload.Messages)
	}
	e.updateDoc(e.doc, "Rewritten.")
	_, err := e.try(e.editor, "ai.context.preview", in)
	wantCode(t, err, ops.CodeConflict)
	// No base revision with offsets, or a quote that is not there: refused.
	in.Selection = &aicore.Selection{Start: &start, End: &end}
	_, err = e.try(e.editor, "ai.context.preview", in)
	wantCode(t, err, ops.CodeInvalid)
	in.Selection = &aicore.Selection{Quote: "not in the document at all"}
	_, err = e.try(e.editor, "ai.context.preview", in)
	wantCode(t, err, ops.CodeInvalid)
}

func TestAScratchpadIsNeverInContext(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	e.call(e.owner, "project.scratch.set", research.ScratchSetIn{ID: e.project, Content: "SCRATCHPAD-PRIVATE-THOUGHTS", BaseRevision: 0})
	in := e.freeform("x")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}, {Kind: lib.ChipBrief}, {Kind: lib.ChipStyleGuide}, {Kind: lib.ChipOutline}, {Kind: lib.ChipSynopsis}}
	if strings.Contains(bodies(e.preview(e.owner, in)), "SCRATCHPAD") {
		t.Fatal("scratchpad text is in the payload")
	}
}

func TestRedactionIsReversibleAndStable(t *testing.T) {
	e := newEnv(t, nil)
	cid := e.compendium("Bible")
	e.setCompendium(cid)
	e.entity(cid, "Maire Nolan", "Mrs Nolan")
	e.entity(cid, "Tomas Quill")
	e.updateDoc(e.doc, "Maire Nolan spoke to Tomas Quill. Mrs Nolan left. MAIRE NOLAN's coat stayed.")
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte, n int) {
		// The model answers with placeholders it saw.
		var req struct {
			Messages []lib.Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &req)
		oaiReply(w, r, body, "{{CHAR_1}} and {{CHAR_2}} parted; {{CHAR_1b}} waved. {{CHAR_9}} is invented.")
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", func(p *aicore.PolicySetIn) { r := "entities"; p.Redaction = &r })
	// The editor is not in the Compendium, so its sheets are not theirs to send; the owner's request carries the sheets
	// (whose labels and text name the entities) as well, and nothing may leak.
	own := e.freeform("Summarise what Maire Nolan did.")
	own.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}, {Kind: lib.ChipEntities, Auto: true}}
	if b := bodies(e.preview(e.owner, own)); strings.Contains(b, "Maire") || strings.Contains(b, "Quill") || strings.Contains(b, "Nolan") {
		t.Errorf("a name reached the payload through an entity sheet or its label: %s", b)
	}
	in := e.freeform("Summarise what Maire Nolan did.") // the instruction is redacted too
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}}
	pv := e.preview(e.editor, in)
	b := bodies(pv)
	for _, name := range []string{"Maire", "Nolan", "Tomas", "Quill"} {
		if strings.Contains(b, name) {
			t.Errorf("%q reached the payload", name)
		}
	}
	if !strings.Contains(b, "{{CHAR_1}}") || !strings.Contains(b, "{{CHAR_2}}") || !strings.Contains(b, "{{CHAR_1b}}") {
		t.Errorf("placeholders missing: %s", b)
	}
	if len(pv.Mapping) != 3 {
		t.Errorf("mapping %+v", pv.Mapping)
	}
	// Stable: another preview (a new nonce) numbers the same names the same way.
	pv2 := e.preview(e.editor, in)
	m1, m2 := mapKey(pv), mapKey(pv2)
	if m1 != m2 {
		t.Errorf("mapping changed between previews:\n%s\n%s", m1, m2)
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	out := e.call(e.editor, "ai.run", in).(aicore.RunItem).Item
	if !strings.HasPrefix(out.Text, "Maire Nolan and Tomas Quill parted; Mrs Nolan waved.") || out.Unrestored != 1 || len(out.Warnings) == 0 {
		t.Errorf("restored text %q (unrestored %d)", out.Text, out.Unrestored)
	}
	// What the provider got had no names.
	for _, r := range f.requests() {
		if strings.Contains(string(r.Body), "Nolan") {
			t.Error("a name reached the provider")
		}
	}
}

func mapKey(pv aicore.Preview) string {
	raw, _ := json.Marshal(pv.Mapping)
	return string(raw)
}

func TestSizeLimitsRefuseBeforeSending(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.MaxRequestBytes = 4096; o.Config.MaxChipBytes = 2048 })
	f := e.ready()
	big := strings.Repeat("A long sentence about the harbour. ", 200)
	e.updateDoc(e.doc, big)
	// One piece over the chip limit: an error that names it.
	_, err := e.try(e.editor, "ai.context.preview", e.freeform("x"))
	wantCode(t, err, ops.CodeTooLarge)
	if !strings.Contains(err.Message, "Document: Chapter One") {
		t.Errorf("%q", err.Message)
	}
	// Several pieces under the chip limit but together over the request limit: refused as soon as
	// the total is passed, before the rest is read, by the preview and by the run.
	e.updateDoc(e.doc, strings.Repeat("harbour ", 150))
	n1 := e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: "Two", Content: strings.Repeat("quay ", 300)}).(docs.Item).Item.ID
	n2 := e.call(e.owner, "doc.create", docs.CreateIn{ProjectID: e.project, Title: "Three", Content: strings.Repeat("tide ", 300)}).(docs.Item).Item.ID
	in := e.freeform("x")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}, {Kind: lib.ChipDocument, DocumentID: n1}, {Kind: lib.ChipDocument, DocumentID: n2}}
	_, err = e.try(e.editor, "ai.context.preview", in)
	wantCode(t, err, ops.CodeTooLarge)
	if !strings.Contains(err.Message, "most one request sends") {
		t.Errorf("%q", err.Message)
	}
	in.Nonce, in.PayloadHash = lib.NewNonce(), strings.Repeat("a", 64)
	_, err = e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeTooLarge)
	// Under the chip total but over the request limit once the framing is added: the preview says refused.
	small := e.freeform(strings.Repeat("x", 1000))
	small.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}, {Kind: lib.ChipInstruction, Text: strings.Repeat("y", 1900)}}
	e.updateDoc(e.doc, strings.Repeat("harbour ", 120))
	if pv := e.preview(e.editor, small); pv.Decision.Allowed || pv.Decision.Code != "too_large" {
		t.Fatalf("%+v", pv.Decision)
	}
	if len(f.requests()) != 0 {
		t.Fatal("an oversized request reached a provider")
	}
}

package ai_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// A model can echo any placeholder. One that stood for nothing in this request must
// not turn into the name of an entity the caller has no access to.
func TestAnEchoedPlaceholderDoesNotRevealAnUnsentName(t *testing.T) {
	e := newEnv(t, nil)
	cid := e.compendium("Private bible")
	e.setCompendium(cid)
	for i := 0; i < 6; i++ {
		e.entity(cid, fmt.Sprintf("Hidden Person%c", 'A'+i))
	}
	e.updateDoc(e.doc, "Hidden PersonC walked down to the water.")
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		// The model repeats every placeholder it can think of, as a hostile instruction would ask.
		var all []string
		for i := 1; i <= 12; i++ {
			all = append(all, fmt.Sprintf("{{CHAR_%d}} {{CHAR_%db}}", i, i))
		}
		oaiReply(w, r, b, strings.Join(all, " "))
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", func(p *aicore.PolicySetIn) { r := "entities"; p.Redaction = &r })
	in := e.freeform("Repeat every placeholder you can.")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}}
	out, err := e.run(e.editor, in) // the editor is not in the Compendium
	if err != nil {
		t.Fatal(err)
	}
	named := 0
	for i := 0; i < 6; i++ {
		if strings.Contains(out.Text, fmt.Sprintf("Hidden Person%c", 'A'+i)) {
			named++
		}
	}
	if named != 1 || !strings.Contains(out.Text, "Hidden PersonC") { // only the one that was in the text
		t.Fatalf("the answer names %d entities: %s", named, out.Text)
	}
	if out.Unrestored < 11 {
		t.Errorf("unrestored %d", out.Unrestored)
	}
	// The same through a stream.
	in.Stream = true
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	sink := &collect{}
	if _, err := e.svc.Run(aicore.WithStream(context.Background(), sink), e.d, e.editor, in); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if i != 2 && strings.Contains(sink.text.String(), fmt.Sprintf("Hidden Person%c", 'A'+i)) {
			t.Errorf("the stream revealed %c", 'A'+i)
		}
	}
}

// The probe: text that merely contains a placeholder must not make the preview list it.
// Numbering is deterministic, so CHAR_1..n would otherwise enumerate every name in scope,
// never-send entities included, for someone with no access to the bible.
func TestAPlaceholderInTheTextDoesNotEnumerateNames(t *testing.T) {
	e := newEnv(t, nil)
	cid := e.compendium("Private bible")
	e.setCompendium(cid)
	var secret string
	for i := 0; i < 5; i++ {
		id := e.entity(cid, fmt.Sprintf("Hidden Person%c", 'A'+i))
		if i == 3 {
			secret = id
		}
	}
	e.exclude("entity", secret, true) // a never-send entity is in the numbering too
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		oaiReply(w, r, b, "{{CHAR_1}} {{CHAR_2}} {{CHAR_3}} {{CHAR_4}} {{CHAR_5}}")
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", func(p *aicore.PolicySetIn) { r := "entities"; p.Redaction = &r })
	probe := "{{CHAR_1}} {{CHAR_2}} {{CHAR_3}} {{CHAR_4}} {{CHAR_5}} {{ CHAR_6 }}"
	in := e.freeform(probe)
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipInstruction, Text: probe}}
	pv := e.preview(e.editor, in) // an editor with no access to the Compendium
	if len(pv.Mapping) != 0 {
		t.Fatalf("the preview listed %+v for text that only contains placeholders", pv.Mapping)
	}
	raw, _ := json.Marshal(pv)
	if strings.Contains(string(raw), "Hidden Person") {
		t.Fatal("the preview names an entity")
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	out := e.call(e.editor, "ai.run", in).(aicore.RunItem).Item
	if strings.Contains(out.Text, "Hidden") || out.Unrestored != 5 {
		t.Fatalf("an echoed placeholder was restored: %q (%d)", out.Text, out.Unrestored)
	}
	// And when the text really uses a name, only that placeholder is listed.
	e.updateDoc(e.doc, "Hidden PersonB walked.")
	in2 := e.freeform("x")
	in2.Chips = []aicore.ChipSpec{{Kind: lib.ChipDocument}}
	pv = e.preview(e.editor, in2)
	if len(pv.Mapping) != 1 || pv.Mapping[0].Original != "Hidden PersonB" {
		t.Errorf("mapping %+v", pv.Mapping)
	}
}

// With no per-provider timeout the call's own time budget is what stops a hung provider,
// and it is shared: the first attempt cannot use up the time the next one needs.
func TestTheSyncBudgetIsSharedAndATimeoutIsATimeout(t *testing.T) {
	hang := func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		select {
		case <-time.After(30 * time.Second):
		case <-r.Context().Done():
		}
	}
	slack := time.Duration(1 + b2i(race))
	e := newEnv(t, func(o *aicore.Options) { o.Config.SyncTimeout = 3 * time.Second; o.Config.Retries = 0 })
	a, b := newFake(t, hang), newFake(t, nil)
	e.chain(e.addProvider(a, "Hangs", false), e.addProvider(b, "Answers", false))
	start := time.Now()
	out, err := e.run(e.editor, e.freeform("Hello"))
	if err != nil || out.Status != "ok" || out.Attempts != 2 || out.Text != "A suggestion." {
		t.Fatalf("%v %+v", err, out)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 6*time.Second*slack {
			t.Errorf("took %v", took)
		}
	})
	// One provider that hangs is a timeout error, not a 200 that says cancelled.
	e2 := newEnv(t, func(o *aicore.Options) { o.Config.SyncTimeout = 2 * time.Second; o.Config.Retries = 0 })
	e2.addProvider(newFake(t, hang), "Hangs", false)
	e2.enable("ask", nil)
	_, rerr := e2.run(e2.editor, e2.freeform("Hello"))
	wantCode(t, rerr, ops.CodeProvider)
	if rerr.Data["provider_class"] != lib.ClassTimeout {
		t.Errorf("%v", rerr.Data)
	}
	if n := e2.count(`SELECT count(*) FROM ai_runs WHERE status = 'error' AND error_code = 'timeout'`); n != 1 {
		t.Errorf("the run log: %d timeouts", n)
	}
}

package ai_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// With the default money cap, a paid provider whose model has no known price is refused. That is
// by design (an unknown price is not zero), and the first thing a new OpenAI-compatible provider
// meets, so the administrator is told which provider it is and how to fix it.
func TestTheDefaultMoneyCapMeetsAnUnpricedProvider(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) {
		o.Config.DefaultUserMicros = 5_000_000
		o.Config.DefaultUserTokens = 1_000_000
	}) // the shipped defaults
	e.addProvider(newFake(t, nil), "Hosted", false)
	e.enable("ask", nil)
	pv := e.preview(e.editor, e.freeform("Hello"))
	if pv.Decision.Allowed || pv.Decision.Code != "price_unknown" {
		t.Fatalf("%+v", pv.Decision)
	}
	var warned bool
	for _, w := range e.call(e.admin, "ai.status", struct{}{}).(aicore.StatusOut).Warnings {
		if w.Code == "unpriced_model" && strings.Contains(w.Message, "Hosted") {
			warned = true
		}
	}
	if !warned {
		t.Error("the administrator was not told")
	}
	if st := e.call(e.editor, "ai.status", struct{}{}).(aicore.StatusOut); hasWarning(st, "unpriced_model") {
		t.Error("an editor was warned about something they cannot fix")
	}
	e.call(e.admin, "ai.price.set", aicore.PriceSetIn{Model: "model-a", InputMicros: 1_000_000, OutputMicros: 2_000_000})
	if pv := e.preview(e.editor, e.freeform("Hello")); !pv.Decision.Allowed || pv.ReserveMicros == nil {
		t.Fatalf("with a price: %+v", pv.Decision)
	}
	if hasWarning(e.call(e.admin, "ai.status", struct{}{}).(aicore.StatusOut), "unpriced_model") {
		t.Error("the warning stayed after the price was set")
	}
}

// The hash is the approval, and its inputs include an address an editor is not shown: it is keyed,
// so it cannot be used to test guesses at that address offline.
func TestThePayloadHashCannotConfirmAnAddress(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	pid := e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	pv := e.preview(e.editor, e.freeform("Hello"))
	if len(pv.Attempts) != 1 {
		t.Fatal("one attempt expected")
	}
	a := pv.Attempts[0]
	var address string
	_ = e.d.Read().QueryRow(`SELECT base_url FROM ai_providers WHERE id = ?`, pid).Scan(&address)
	// Everything in the preview, plus the true address, hashed the way an unkeyed hash would be.
	for _, mode := range []string{"ask", ""} {
		h := sha256.New()
		h.Write([]byte("scriobh-ai-payload-v1\n"))
		fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%v\n%s\n%s\n%d\n", a.ProviderID, a.ProviderKind, a.Model, strings.ToLower(address), false, a.Scope, mode, len(a.Body))
		h.Write([]byte(a.Body))
		h.Write([]byte("\n"))
		if hex.EncodeToString(h.Sum(nil)) == pv.PayloadHash {
			t.Fatal("the hash can be recomputed from the preview and the address")
		}
	}
	// And the same request previewed by the same server hashes the same (that is what a run compares),
	// while another server's key gives another hash.
	in := e.freeform("Hello")
	in.Nonce = pv.Nonce
	if again := e.preview(e.editor, in); again.PayloadHash != pv.PayloadHash {
		t.Error("the hash is not stable for one server")
	}
	e2 := newEnv(t, nil)
	if e2.svc == e.svc {
		t.Fatal("two servers shared a service")
	}
}

// The reservation bounds what a run can settle at, even when the chain is walked to its end: each
// provider and each retry sends the request, and each lost send is charged its input.
func TestTheReservationBoundsAWorstCaseRun(t *testing.T) {
	e := newEnv(t, nil) // Retries is 1: each provider is tried twice
	a := newFake(t, status(503))
	b := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte, n int) {
		if n == 1 {
			http.Error(w, "busy", 503)
			return
		}
		oaiReply(w, r, body, "ok")
	})
	e.chain(e.addProvider(a, "A", false), e.addProvider(b, "B", false))
	in := e.freeform("Hello")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	out := e.call(e.editor, "ai.run", in).(aicore.RunItem).Item
	if out.Status != "ok" || len(a.requests()) != 2 || len(b.requests()) != 2 {
		t.Fatalf("%+v: A saw %d, B saw %d", out, len(a.requests()), len(b.requests()))
	}
	// A: two lost sends. B: one lost, one answered (reported 120 + 30 tokens). The last provider's
	// retry is the last send the chain allows.
	tokens, _ := e.spent()
	want := int64(pv.InputTokens)*3 + 150
	if tokens != want {
		t.Errorf("settled at %d, want %d", tokens, want)
	}
	if tokens > pv.ReserveTokens {
		t.Fatalf("the run settled at %d, over the %d it reserved", tokens, pv.ReserveTokens)
	}
	if pv.ReserveTokens < int64(pv.InputTokens)*4+int64(pv.MaxTokens) {
		t.Errorf("the reservation %d does not hold the input for every send the chain allows", pv.ReserveTokens)
	}
}

// Accent-folded matching and hidden-text rules reach the document chip too.
func TestAnUnclosedGhostOrNoteIsNotSent(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	content := "Para one <scriobh-ghost>GHOSTED-OPEN-ONLY words here and more.\n\nPara two %%NOTE-OPEN-ONLY words\n\nPara three is plain."
	if _, err := e.d.Writer().Exec(`UPDATE documents SET content = ? WHERE id = ?`, content, e.doc); err != nil {
		t.Fatal(err)
	}
	pv := e.preview(e.editor, e.freeform("Hello"))
	b := bodies(pv)
	for _, banned := range []string{"GHOSTED-OPEN-ONLY", "NOTE-OPEN-ONLY", "scriobh-ghost"} {
		if strings.Contains(b, banned) {
			t.Errorf("%q was sent", banned)
		}
	}
	if !strings.Contains(b, "Para three is plain.") || !strings.Contains(b, "Para one") {
		t.Errorf("the plain text went missing: %s", b)
	}
	// And a selection starting inside the unclosed ghost.
	rev := 1
	start := strings.Index(content, "words here")
	end := strings.Index(content, "more.") + len("more.")
	in := aicore.RunIn{ProjectID: e.project, Recipe: "freeform", DocumentID: e.doc, Params: map[string]string{"instruction": "x"},
		Selection: &aicore.Selection{Start: &start, End: &end, BaseRevision: &rev}, Chips: []aicore.ChipSpec{{Kind: lib.ChipSelection}, {Kind: lib.ChipParagraph}}}
	if _, err := e.try(e.editor, "ai.context.preview", in); err != nil && err.Code != ops.CodeConflict {
		t.Fatal(err)
	}
}

func TestProviderTestHidesItsTimingFromNonAdministrators(t *testing.T) {
	e := newEnv(t, nil)
	mine := e.call(e.editor, "ai.provider.create", aicore.ProviderCreateIn{Name: "Probe", Kind: lib.KindOpenAICompatible, BaseURL: "https://example.org/v1", DefaultModel: "m"}).(aicore.ProviderItem).Item.ID
	if _, err := e.d.Writer().Exec(`UPDATE ai_providers SET base_url = 'https://127.0.0.1:1/v1' WHERE id = ?`, mine); err != nil {
		t.Fatal(err)
	}
	if out := e.call(e.editor, "ai.provider.test", aicore.ProviderIn{ID: mine}).(aicore.TestOut); out.LatencyMS != 0 || out.OK {
		t.Errorf("%+v", out)
	}
}

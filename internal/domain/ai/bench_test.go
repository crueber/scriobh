package ai_test

import (
	"strings"
	"testing"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/testutil"
)

// The numbers behind the budgets in OPERATIONS: what building a preview and
// running a request cost the server, apart from the provider's own time. Run with
//
//	go test ./internal/domain/ai -run Numbers -v
func TestNumbers(t *testing.T) {
	testutil.Timing(t)
	if testing.Short() || raceEnabled {
		t.Skip("timing numbers are for a plain run")
	}
	e := newEnv(t, nil)
	e.ready()
	cid := e.compendium("Bible")
	e.setCompendium(cid)
	for i := 0; i < 200; i++ {
		e.entity(cid, "Person Number"+strings.Repeat("x", i%7)+string(rune('A'+i%26))+string(rune('a'+i/26)), "Alias"+string(rune('A'+i%26))+string(rune('a'+i/26)))
	}
	words := strings.Repeat("The harbour was quiet and Person NumberxA walked down to the water. ", 3500) // about 45,000 words
	e.updateDoc(e.doc, words[:180<<10])
	for _, tc := range []struct {
		name  string
		chips []aicore.ChipSpec
		pol   string
	}{
		{"a 180 KB document", []aicore.ChipSpec{{Kind: lib.ChipDocument}}, "off"},
		{"a 180 KB document, names redacted (200 entities)", []aicore.ChipSpec{{Kind: lib.ChipDocument}}, "entities"},
		{"a paragraph and 40 entity sheets", []aicore.ChipSpec{{Kind: lib.ChipEntities, Auto: true}, {Kind: lib.ChipOutline}}, "off"},
	} {
		e.enable("ask", func(p *aicore.PolicySetIn) { r := tc.pol; p.Redaction = &r })
		in := e.freeform("x")
		in.Chips = tc.chips
		start := time.Now()
		const n = 20
		var pv aicore.Preview
		for i := 0; i < n; i++ {
			pv = e.preview(e.owner, in)
		}
		t.Logf("preview of %s: %v each, %d KB sent, about %d tokens", tc.name, time.Since(start)/n, len(pv.Attempts[0].Body)>>10, pv.InputTokens)
	}
	in := e.freeform("x")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipBrief}}
	pv := e.preview(e.owner, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	start := time.Now()
	const n = 200
	for i := 0; i < n; i++ {
		if _, err := e.try(e.owner, "ai.run", in); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("run (small request, instant provider on loopback): %v each, including reservation, settlement, audit and event", time.Since(start)/n)
}

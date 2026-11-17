//go:build !race

// Timing budgets are for the ordinary build: the race detector makes SQLite 40 times slower.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/webhook"
)

// The MCP and webhook budgets at 150,000 words (150 chapters of 1,000 words, one
// of 10,000 words to save), run alone on an idle machine:
//
//   - a tool call costs at most 2 ms more than the same request over REST (it is
//     the same handler; the cost is the JSON-RPC envelope, the argument check and
//     the answer's second encoding);
//   - tools/list for a read key answers in 25 ms, for a write key in 60 ms;
//   - resources/read of the outline of the 150-chapter project in 60 ms;
//   - saving the 10,000-word chapter with three webhooks configured costs at most
//     1 ms more than without (the save path is untouched; the worker is beside it).
const (
	mcpCallOverhead = 2 * time.Millisecond
	listReadBudget  = 25 * time.Millisecond
	listWriteBudget = 60 * time.Millisecond
	outlineBudget   = 60 * time.Millisecond
	hookSaveBudget  = time.Millisecond
)

func med(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func timeIt(n int, f func()) []time.Duration {
	var out []time.Duration
	for i := 0; i < n; i++ {
		t := time.Now()
		f()
		out = append(out, time.Since(t))
	}
	return out
}

func TestMCPAndWebhookBudgetsAt150kWords(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	s := newMCPServer(t, map[string]string{"SCRIOBH_WEBHOOK_ALLOW_PRIVATE": "1"})
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	ctx := context.Background()
	out, err := s.Reg.Call(ctx, p, "project.create", projects.CreateIn{Title: "A Long Book"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	para := strings.Repeat("The harbour was quiet that morning, and nothing moved but the gulls. ", 15) // ~170 words
	chapter := strings.Repeat(para+"\n\n", 6)                                                           // ~1,000 words
	var first string
	for i := 0; i < 150; i++ {
		d, err := s.Reg.Call(ctx, p, "doc.create", docs.CreateIn{ProjectID: pid, Title: fmt.Sprintf("Chapter %d", i+1), Content: chapter})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = d.(docs.Item).Item.ID
		}
	}
	big, err := s.Reg.Call(ctx, p, "doc.create", docs.CreateIn{ProjectID: pid, Title: "The long one", Content: strings.Repeat(chapter, 10)})
	if err != nil {
		t.Fatal(err)
	}
	bigID, rev := big.(docs.Item).Item.ID, big.(docs.Item).Item.Revision
	writeKey, _ := s.mintKey(t, owner, `{"name":"agent","scope":"write"}`)
	readKey, _ := s.mintKey(t, owner, `{"name":"agent read","scope":"read"}`)

	// A tool call against the same request over REST.
	const n = 60
	rest := timeIt(n, func() {
		if rec := s.doKey(readKey, "GET", "/api/v1/docs/"+first, ""); rec.Code != 200 {
			t.Fatal(rec.Code)
		}
	})
	mcpCall := timeIt(n, func() {
		r := s.modernCall(t, readKey, "tools/call", map[string]any{"name": "doc_get", "arguments": map[string]any{"id": first}})
		if r.status != 200 || r.msg.Result["isError"] == true {
			t.Fatalf("%s", r.raw)
		}
	})
	over := med(mcpCall) - med(rest)
	t.Logf("a 1,000-word doc_get: %s over REST, %s over MCP (%s added)", med(rest), med(mcpCall), over)

	listRead := timeIt(20, func() { s.allToolNames(t, readKey, "") })
	listWrite := timeIt(20, func() { s.allToolNames(t, writeKey, "") })
	t.Logf("tools/list (all pages): read key %s, write key %s", med(listRead), med(listWrite))

	outline := timeIt(20, func() {
		r := s.modernCall(t, readKey, "resources/read", map[string]any{"uri": "scriobh://project/" + pid + "/outline"})
		if r.msg.Error != nil || len(r.raw) < 5000 {
			t.Fatalf("outline: %.200s", r.raw)
		}
	})
	t.Logf("outline resource of 151 documents: %s", med(outline))

	// Saving a 10,000-word chapter, with and without three webhooks and every receiver stalled.
	save := func(k int) []time.Duration {
		var out []time.Duration
		for i := 0; i < k; i++ {
			content := strings.Repeat(chapter, 10) + fmt.Sprintf("\nedit %d %d", time.Now().UnixNano(), i)
			start := time.Now()
			res, err := s.Reg.Call(ctx, p, "doc.update", docs.UpdateIn{ID: bigID, BaseRevision: rev, Content: &content})
			out = append(out, time.Since(start))
			if err != nil {
				t.Fatal(err)
			}
			rev = res.(docs.Item).Item.Revision
		}
		return out
	}
	base := save(60)
	stall := make(chan struct{})
	recv := newStallServer(t, stall)
	s.Webhooks.SetSender(webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, TLS: recv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}))
	for _, name := range []string{"a", "b", "c"} {
		body := fmt.Sprintf(`{"name":%q,"url":%q,"events":["document.updated","comment.created"]}`, name, recv.URL+"/"+name)
		if rec := s.do(owner, "POST", "/api/v1/projects/"+pid+"/webhooks", body); rec.Code != 201 {
			t.Fatalf("webhook: %d %s", rec.Code, rec.Body)
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.Webhooks.Start(runCtx)
	t.Cleanup(func() { cancel(); close(stall); s.Webhooks.Wait() })
	with := save(60)
	t.Logf("saving a 10,000-word chapter in a 150,000-word project: %s without webhooks, %s with three and every receiver stalled", med(base), med(with))
	var worst time.Duration
	for _, d := range with {
		// timing: not a budget: finds the slowest save; the budget is asserted in the time subtest
		if d > worst {
			worst = d
		}
	}
	// The behaviour above (statuses, the tool list, the outline, the webhooks and every save succeeding) runs in
	// the push gate; the budgets are asserted only when timing tests run.
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if over > mcpCallOverhead {
			t.Errorf("a tool call costs %s more than REST (budget %s)", over, mcpCallOverhead)
		}
		if med(listRead) > listReadBudget || med(listWrite) > listWriteBudget {
			t.Errorf("tools/list: read %s (budget %s), write %s (budget %s)", med(listRead), listReadBudget, med(listWrite), listWriteBudget)
		}
		if med(outline) > outlineBudget {
			t.Errorf("the outline resource takes %s (budget %s)", med(outline), outlineBudget)
		}
		if d := med(with) - med(base); d > hookSaveBudget {
			t.Errorf("webhooks add %s to a save (budget %s)", d, hookSaveBudget)
		}
		if worst > 2*time.Second {
			t.Errorf("a save took %s", worst)
		}
	})
	_ = json.Marshal
}

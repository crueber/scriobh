//go:build !race

// Timing budgets are for the ordinary build: the race detector makes SQLite 40 times slower.

package webhook_test

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/webhook"
)

// The budgets. Webhooks are an integration surface; the writing surface is not
// allowed to pay for them.
//
//   - Saving: with three webhooks configured, the worker running and every
//     receiver stalled, the median save may cost at most 1 ms more than with no
//     webhook at all (the save path only writes the events it always wrote; the
//     work is off to the side) and no save may wait on a delivery.
//   - Enqueueing: 1,000 events (a minute's worth at the design rate) become
//     3,000 outbox rows in well under a second.
const (
	saveOverheadBudget = time.Millisecond
	enqueueBudget      = 2 * time.Second
)

func median(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func p99(ds []time.Duration) time.Duration {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)*99/100]
}

// saves makes n saves of one document through the registry and times each.
func saves(t *testing.T, w *world, n int) []time.Duration {
	t.Helper()
	ctx := context.Background()
	out := w.mustCall("doc.create", docs.CreateIn{ProjectID: w.project, Title: "Perf", Content: strings.Repeat("A paragraph of words to save. ", 200)}).(docs.Item)
	id, rev := out.Item.ID, out.Item.Revision
	var ds []time.Duration
	for i := 0; i < n; i++ {
		content := strings.Repeat("A paragraph of words to save. ", 200) + fmt.Sprintf(" edit %d", i)
		start := time.Now()
		res, err := w.reg.Call(ctx, w.owner, "doc.update", docs.UpdateIn{ID: id, BaseRevision: rev, Content: &content})
		ds = append(ds, time.Since(start))
		if err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
		rev = res.(docs.Item).Item.Revision
	}
	return ds
}

func TestSavePathCostWithWebhooksAndAStalledReceiver(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	const n = 300
	base := newWorld(t, func(o *worldOpts) { o.noHook = true })
	baseline := saves(t, base, n)

	busy := newWorld(t, func(o *worldOpts) { o.noHook = false; o.lim.DocDebounce = 0 })
	busy.create("two", []string{"document.updated"}, false)
	busy.create("three", []string{"document.updated"}, false)
	stalled := make(chan struct{})
	busy.recv.hook = func(http.ResponseWriter, *http.Request) { <-stalled } // every receiver is stuck
	ctx, cancel := context.WithCancel(context.Background())
	busy.svc.Start(ctx)
	t.Cleanup(func() { cancel(); close(stalled) })
	with := saves(t, busy, n)

	b50, w50 := median(baseline), median(with)
	t.Logf("save median: %s without webhooks, %s with three webhooks and stalled receivers (p99 %s vs %s), %d saves each", b50, w50, p99(baseline), p99(with), n)
	var longest time.Duration
	for _, d := range with {
		if d > longest {
			longest = d
		}
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if over := w50 - b50; over > saveOverheadBudget {
			t.Errorf("the median save costs %s more with webhooks (budget %s)", over, saveOverheadBudget)
		}
		if longest > 2*time.Second {
			t.Errorf("a save took %s: it waited on something", longest)
		}
	})
	// And the work did happen off to the side: deliveries were queued and sent attempts are stuck, not the saves.
	time.Sleep(200 * time.Millisecond)
	if busy.count(`SELECT count(*) FROM webhook_deliveries`) == 0 {
		t.Error("no delivery was queued")
	}
}

func TestEnqueueThroughput(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) {
		o.lim.DocDebounce = -1
		o.lim.PerWebhookQueueMax = 100000
		o.lim.QueueMax = 100000
		o.lim.ProjectQueueMax = 100000
	})
	w.create("two", []string{"document.updated"}, false)
	w.create("three", []string{"document.updated"}, false)
	evs := make([]ops.Event, 0, 1000)
	for i := 0; i < 1000; i++ {
		evs = append(evs, docUpdated(w.project, fmt.Sprintf("doc_%d", i%700), i))
	}
	for i := 0; i < len(evs); i += 100 {
		w.emit(evs[i : i+100]...)
	}
	start := time.Now()
	for {
		more, err := w.svc.Feed(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	took := time.Since(start)
	rows := w.count(`SELECT count(*) FROM webhook_deliveries`)
	t.Logf("1,000 events into %d outbox rows for 3 webhooks in %s", rows, took)
	if rows < 2000 {
		t.Errorf("%d rows", rows)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > enqueueBudget {
			t.Errorf("enqueueing took %s (budget %s)", took, enqueueBudget)
		}
	})
	_ = webhook.EventNames
}

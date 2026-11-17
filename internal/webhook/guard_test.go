package webhook_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/webhook"
)

// A delivery that was listed for a pass and sent by an earlier send before the
// pass reached it is not sent again. (This was a real double delivery: the list
// is read before a webhook's slot is taken, and a send that finished in between
// left the row in the list looking due.)
func TestAStaleListingDoesNotSendADeliveryTwice(t *testing.T) {
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := w.d.Read().QueryRow(`SELECT id FROM webhook_deliveries`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	listed, err := w.svc.ListAnyState(context.Background(), id) // the pass lists it ...
	if err != nil {
		t.Fatal(err)
	}
	w.pump() // ... another send delivers it ...
	if len(w.recv.requests()) != 1 {
		t.Fatalf("%d requests", len(w.recv.requests()))
	}
	if w.svc.StartListed(context.Background(), listed) { // ... and the pass reaches it only now.
		w.svc.Wait()
		t.Fatalf("the stale listing started a send; the receiver has seen %d requests", len(w.recv.requests()))
	}
	w.svc.Wait()
	if len(w.recv.requests()) != 1 {
		t.Errorf("the delivery was sent %d times", len(w.recv.requests()))
	}
	// A retry that was listed before its webhook was switched off, or its project trashed, is not sent either.
	w.recv.status.Store(500)
	w.emit(docUpdated(w.project, "doc_2", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := w.d.Read().QueryRow(`SELECT id FROM webhook_deliveries WHERE state = 'pending'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	listed, _ = w.svc.ListAnyState(context.Background(), id)
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(false)})
	n := len(w.recv.requests())
	if w.svc.StartListed(context.Background(), listed) {
		w.svc.Wait()
	}
	if len(w.recv.requests()) != n {
		t.Error("a delivery of a webhook that was switched off was sent")
	}
}

// Many passes at once cannot send one delivery twice either: one webhook, one
// slot, and the row is checked after the slot is taken.
func TestConcurrentPassesSendEachDeliveryOnce(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.RatePerMin = 60000; o.lim.ProjectRatePerMin = 60000 })
	for i := 0; i < 40; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%d", i)}})
	}
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				w.svc.Dispatch(context.Background()) // passes overlap; the service's Wait is for shutdown, not for these
			}
		}()
	}
	wg.Wait()
	w.svc.Wait() // the sends in flight finish ...
	w.pump()     // ... and whatever is left goes out
	ids := map[string]int{}
	for _, r := range w.recv.requests() {
		ids[r.Header.Get(webhook.HeaderID)]++
	}
	if len(ids) != 40 {
		t.Errorf("%d distinct deliveries of 40", len(ids))
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("%s was sent %d times", id, n)
		}
	}
}

// A test ping is the asker's, never the dispatcher's: one that could not be sent
// is over, and does not turn up later as a retry.
func TestAPingThatCouldNotBeSentIsNotRetriedByTheDispatcher(t *testing.T) {
	w := newWorld(t)
	// Make its secret unreadable, so the attempt stops before anything is sent or recorded.
	if _, err := w.d.Writer().Exec(`UPDATE webhooks SET secret_sealed = 'v1.AAAA' WHERE id = ?`, w.hookID); err != nil {
		t.Fatal(err)
	}
	out := w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.TestOut)
	if out.OK || out.Delivery.State != "dead" {
		t.Fatalf("%+v", out)
	}
	// Rotating switches the webhook back on; the old ping must not be sent now.
	w.mustCall("webhook.rotate_secret", webhook.RotateIn{ProjectID: w.project, WebhookID: w.hookID})
	w.pump()
	for _, r := range w.recv.requests() {
		if r.Header.Get(webhook.HeaderEvent) == "ping" {
			t.Error("the dispatcher sent a ping")
		}
	}
	if d := w.deliveries("pending"); len(d) != 0 {
		t.Errorf("%+v", d)
	}
}

// One project cannot use the instance's whole rate: with a project share of 6 a minute
// (a burst of 5), a flood from one project goes out five at a time while another
// project's webhook is served in full.
func TestOneProjectCannotUseTheWholeRate(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.RatePerMin = 6000; o.lim.ProjectRatePerMin = 6; o.lim.Concurrency = 8 })
	otherProject := w.mustCall("project.create", projects.CreateIn{Title: "Quiet"}).(projects.Item).Item.ID
	w.mustCall("webhook.create", webhook.CreateIn{ProjectID: otherProject, Name: "q", URL: w.recv.URL + "/quiet", Events: []string{"comment.created"}})
	for i := 0; i < 40; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%d", i)}})
	}
	for i := 0; i < 3; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: otherProject, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_q%d", i)}})
	}
	w.pump()
	loud, quiet := 0, 0
	for _, r := range w.recv.requests() {
		if strings.Contains(string(r.Body), otherProject) {
			quiet++
		} else {
			loud++
		}
	}
	if loud > 6 || loud < 4 {
		t.Errorf("the busy project sent %d in the first pass; its share is a burst of 5", loud)
	}
	if quiet != 3 {
		t.Errorf("the quiet project sent %d of 3: it was held back by another project", quiet)
	}
	// What is held back is still waiting, not lost.
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 40-loud {
		t.Errorf("%d waiting, want %d", n, 40-loud)
	}
}

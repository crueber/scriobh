package webhook_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/webhook"
)

// A webhook that is on holds the event log back from pruning until its feeder has read the rows. With the webhook
// feature switched off no feeder runs, so the cursor must not hold the log (it would grow without bound), and
// switching the feature on again must not replay what piled up meanwhile, nor send what was waiting in the outbox.
func TestWebhooksOffLetTheLogBePrunedAndTurningThemOnReplaysNothing(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	w := newWorld(t) // the feature is on and one webhook is enabled
	far := time.Now().Add(400 * 24 * time.Hour)
	rows := func() int { return w.count(`SELECT count(*) FROM events`) }
	prune := func() {
		t.Helper()
		if _, err := events.Sweep(ctx, w.d, events.Keep, far); err != nil {
			t.Fatal(err)
		}
	}

	// Feature on, an enabled webhook, three events it has not read: none is pruned (but the newest would stay anyway).
	w.emit(docUpdated(w.project, "doc_1", 1), docUpdated(w.project, "doc_2", 2), docUpdated(w.project, "doc_3", 3))
	prune()
	if got := rows(); got != 3 { // the one row the feeder had passed is gone
		t.Fatalf("events the webhook had not read were pruned: %d rows, want 3", got)
	}
	// The feeder reads them (the outbox now holds three) and the process stops before sending.
	for {
		more, err := w.svc.Feed(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 3 {
		t.Fatalf("%d deliveries waiting, want 3", n)
	}

	// The server comes back with webhooks off. The enabled row is still there.
	off := webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{Off: true}})
	off.Start(ctx)
	off.Wait()
	if n := w.count(`SELECT count(*) FROM webhooks WHERE enabled = 1`); n != 1 {
		t.Fatalf("the webhook row changed: %d enabled", n)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 0 {
		t.Errorf("%d deliveries still wait in the outbox of a server with webhooks off", n)
	}
	w.emit(docUpdated(w.project, "doc_4", 4), docUpdated(w.project, "doc_5", 5))
	prune()
	if got := rows(); got != 1 {
		t.Errorf("with webhooks off the log was not pruned: %d rows left, want only the newest", got)
	}

	// Webhooks on again: nothing from before is sent, and what happens now is.
	w.emit(docUpdated(w.project, "doc_6", 6)) // written while off, still inside the retention window
	on := webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{AllowPrivate: true, DocDebounce: -1},
		Sender: webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, TLS: w.recv.Client().Transport.(*http.Transport).TLSClientConfig})})
	on.Start(ctx)
	defer func() { stop(); on.Wait() }()
	time.Sleep(700 * time.Millisecond)
	if n := len(w.recv.requests()); n != 0 {
		t.Fatalf("%d stale notifications were sent when the feature came back", n)
	}
	w.emit(docUpdated(w.project, "doc_7", 7))
	deadline := time.Now().Add(10 * time.Second)
	for len(w.recv.requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := len(w.recv.requests()); n != 1 {
		t.Errorf("%d requests after a new event, want exactly 1", n)
	}
}

// A backup restored on another machine holds the old outbox and the feeder's place in the event log. With the same
// secret key the webhooks could sign for the receivers, so a restore comes back with every webhook switched off, the
// outbox empty and the feeder parked; an owner switches a webhook on again and it starts at the newest event.
func TestARestorePausesWebhooksAndTurningOneOnStartsAtNow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1), docUpdated(w.project, "doc_2", 2))
	for {
		more, err := w.svc.Feed(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 2 {
		t.Fatalf("%d deliveries waiting, want 2", n)
	}
	w.emit(docUpdated(w.project, "doc_3", 3)) // after the cursor: the restored copy would feed it

	var notes []string
	for _, step := range backup.PostRestore {
		msg, err := step(ctx, w.d, []byte("test-secret-key-test-secret-key-0123456789")) // the same key
		if err != nil {
			t.Fatal(err)
		}
		if msg != "" {
			notes = append(notes, msg)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "paused") || strings.Contains(notes[0], "cannot be read") {
		t.Fatalf("what the restore says: %q", notes)
	}
	list := w.mustCall("webhook.list", webhook.ProjectIn{ProjectID: w.project}).(webhook.List)
	if len(list.Items) != 1 || list.Items[0].Enabled || list.Items[0].DisabledReason != webhook.PausedByRestore {
		t.Fatalf("the webhook after a restore: %+v", list.Items)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 0 {
		t.Errorf("%d deliveries still wait after a restore", n)
	}
	// Even a feature-on server that starts now sends nothing: no webhook is on.
	w.pump()
	if n := len(w.recv.requests()); n != 0 {
		t.Fatalf("%d notifications went out from a restored copy", n)
	}
	// The owner turns it on: only what happens from now is sent.
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(true)})
	w.pump()
	if n := len(w.recv.requests()); n != 0 {
		t.Fatalf("switching a paused webhook on replayed %d old events", n)
	}
	w.emit(docUpdated(w.project, "doc_4", 4))
	w.pump()
	if n := len(w.recv.requests()); n != 1 {
		t.Errorf("%d notifications after a new event, want 1", n)
	}
	list = w.mustCall("webhook.list", webhook.ProjectIn{ProjectID: w.project}).(webhook.List)
	if !list.Items[0].Enabled || list.Items[0].DisabledReason != "" {
		t.Errorf("the webhook after the owner switched it on: %+v", list.Items[0])
	}
}

// Switching a webhook off and on again (or rotating its secret) while the feature is off must not un-park the
// feeder: that would hold the log back again and replay everything since at the next start.
func TestTogglingAWebhookWhileTheFeatureIsOffKeepsTheFeederParked(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	w := newWorld(t)
	off := webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{Off: true}})
	off.Start(ctx)
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(false)})
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(true)})
	w.emit(docUpdated(w.project, "doc_2", 2), docUpdated(w.project, "doc_3", 3))
	if _, err := events.Sweep(ctx, w.d, events.Keep, time.Now().Add(400*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM events`); n != 1 {
		t.Errorf("%d events left after a toggle with webhooks off, want only the newest", n)
	}
	on := webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{AllowPrivate: true, DocDebounce: -1},
		Sender: webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, TLS: w.recv.Client().Transport.(*http.Transport).TLSClientConfig})})
	on.Start(ctx)
	defer func() { stop(); on.Wait() }()
	time.Sleep(700 * time.Millisecond)
	if n := len(w.recv.requests()); n != 0 {
		t.Errorf("%d stale notifications after the feature came back", n)
	}
}

package webhook_test

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/webhook"
)

// ---- a world to drive ---------------------------------------------------------------------

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type received struct {
	Header http.Header
	Body   []byte
}

// receiver is a TLS server that records what it is sent and answers as told.
type receiver struct {
	*httptest.Server
	mu     sync.Mutex
	got    []received
	status atomic.Int32
	delay  atomic.Int64
	open   atomic.Int32 // requests being served now
	peak   atomic.Int32
	hook   func(w http.ResponseWriter, r *http.Request)
}

func newReceiver(t *testing.T) *receiver {
	rc := &receiver{}
	rc.status.Store(200)
	rc.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := rc.open.Add(1)
		for {
			p := rc.peak.Load()
			if n <= p || rc.peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer rc.open.Add(-1)
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.got = append(rc.got, received{r.Header.Clone(), b})
		rc.mu.Unlock()
		if d := rc.delay.Load(); d > 0 {
			time.Sleep(time.Duration(d))
		}
		if rc.hook != nil {
			rc.hook(w, r)
			return
		}
		w.WriteHeader(int(rc.status.Load()))
	}))
	t.Cleanup(rc.Close)
	return rc
}

func (rc *receiver) requests() []received {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]received(nil), rc.got...)
}

type world struct {
	t       *testing.T
	d       *db.DB
	store   *events.Store
	reg     *ops.Registry
	svc     *webhook.Service
	clk     *clock
	owner   ops.Principal
	project string
	recv    *receiver
	box     *secrets.Box
	secret  string // the secret of the last webhook made through create
	hookID  string
}

type worldOpts struct {
	lim       webhook.Limits
	afterSend func(string) error
	noHook    bool
	noKey     bool
}

func newWorld(t *testing.T, mod ...func(*worldOpts)) *world {
	t.Helper()
	o := worldOpts{lim: webhook.Limits{AllowPrivate: true, DocDebounce: -1}}
	for _, m := range mod {
		m(&o)
	}
	d := testutil.DB(t)
	w := &world{t: t, d: d, clk: &clock{t: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)}, recv: newReceiver(t)}
	w.store = events.NewStore(d, nil)
	key := []byte("test-secret-key-test-secret-key-0123456789")
	if o.noKey {
		key = nil
	}
	w.box = secrets.New(key)
	tlsCfg := w.recv.Client().Transport.(*http.Transport).TLSClientConfig
	sender := webhook.NewSender(webhook.SenderOptions{AllowPrivate: o.lim.AllowPrivate, Timeout: 3 * time.Second, TLS: tlsCfg})
	w.svc = webhook.New(webhook.Options{DB: d, Box: w.box, Events: w.store, Limits: o.lim, Sender: sender, Now: w.clk.Now, Rand: func() float64 { return 0.5 }, AfterSend: o.afterSend})
	w.reg = registry.BuildWith(d, w.store, t.TempDir(), registry.Options{Webhook: o.lim})
	w.reg.Env().SetService(webhook.ServiceName, w.svc)
	w.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "owner@t.local"), Role: ops.RoleWriter}
	out, err := w.reg.Call(context.Background(), w.owner, "project.create", projects.CreateIn{Title: "Hooked"})
	if err != nil {
		t.Fatal(err)
	}
	w.project = out.(projects.Item).Item.ID
	if !o.noHook {
		w.create("main", []string{"document.updated", "comment.created", "suggestion.created", "export.completed"}, false)
	}
	return w
}

func (w *world) call(op string, in any) (any, error) {
	return w.reg.Call(context.Background(), w.owner, op, in)
}

func (w *world) mustCall(op string, in any) any {
	w.t.Helper()
	out, err := w.call(op, in)
	if err != nil {
		w.t.Fatalf("%s: %v", op, err)
	}
	return out
}

// create makes a webhook that points at the receiver.
func (w *world) create(name string, evs []string, text bool) webhook.Webhook {
	w.t.Helper()
	out := w.mustCall("webhook.create", webhook.CreateIn{ProjectID: w.project, Name: name, URL: w.recv.URL + "/hook", Events: evs, IncludeText: &text}).(webhook.CreateOut)
	w.secret, w.hookID = out.Secret, out.Item.ID
	return out.Item
}

// emit writes events as the save path does.
func (w *world) emit(evs ...ops.Event) {
	w.t.Helper()
	if err := w.store.Flush(context.Background(), "", evs, nil); err != nil {
		w.t.Fatal(err)
	}
}

func docUpdated(project, doc string, rev int) ops.Event {
	return ops.Event{Type: "doc.updated", ProjectID: project, DocumentID: doc, Payload: map[string]any{"revision": rev, "words": 100 + rev, "content": true}}
}

// pump runs the feeder until it has nothing more, then one dispatch pass, and waits for the sends.
func (w *world) pump() {
	w.t.Helper()
	ctx := context.Background()
	for {
		more, err := w.svc.Feed(ctx)
		if err != nil {
			w.t.Fatal(err)
		}
		if !more {
			break
		}
	}
	// One webhook has one send in flight, so a pass sends one delivery to each; the
	// service signals itself after every send, and so does this loop.
	for i := 0; i < 200; i++ {
		started := w.svc.Dispatch(ctx)
		w.svc.Wait()
		if started == 0 {
			break
		}
	}
}

func (w *world) count(q string, args ...any) int {
	var n int
	if err := w.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

func (w *world) deliveries(state string) []webhook.Delivery {
	w.t.Helper()
	out := w.mustCall("webhook.deliveries", webhook.DeliveriesIn{ProjectID: w.project, WebhookID: w.hookID, State: state, Limit: 200}).(webhook.DeliveriesOut)
	return out.Items
}

// ---- feeding ---------------------------------------------------------------------------

func TestOneEventIsOneDeliveryPerSubscribedWebhook(t *testing.T) {
	w := newWorld(t)
	first := w.hookID
	w.create("only comments", []string{"comment.created"}, false)
	second := w.hookID
	w.emit(docUpdated(w.project, "doc_1", 1),
		ops.Event{Type: "comment.created", ProjectID: w.project, DocumentID: "doc_1", Payload: map[string]any{"comment_id": "cmt_1", "thread_id": "cmt_1", "kind": "comment"}})
	w.pump()
	got := w.recv.requests()
	if len(got) != 3 {
		t.Fatalf("%d requests, want 3 (document.updated to the first, comment.created to both)", len(got))
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE webhook_id = ?`, first); n != 2 {
		t.Errorf("first webhook: %d deliveries", n)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE webhook_id = ?`, second); n != 1 {
		t.Errorf("second webhook: %d deliveries", n)
	}
	// Feeding again changes nothing: the cursor moved in the same transaction.
	w.pump()
	if len(w.recv.requests()) != 3 {
		t.Error("an event was delivered twice")
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries`); n != 3 {
		t.Errorf("%d rows", n)
	}
}

func TestOnlyWhatHappensAfterAWebhookExistsIsSent(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.noHook = true })
	w.emit(docUpdated(w.project, "doc_old", 1))
	w.create("late", []string{"document.updated"}, false)
	w.pump()
	if len(w.recv.requests()) != 0 {
		t.Fatal("an event from before the webhook was delivered")
	}
	w.emit(docUpdated(w.project, "doc_new", 2))
	w.pump()
	if len(w.recv.requests()) != 1 {
		t.Fatalf("%d requests", len(w.recv.requests()))
	}
	// Switching a webhook off and on again does not replay what happened while it was off.
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(false)})
	w.emit(docUpdated(w.project, "doc_off", 3))
	w.pump()
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(true)})
	w.pump()
	if len(w.recv.requests()) != 1 {
		t.Errorf("events from while it was off were delivered: %d", len(w.recv.requests()))
	}
}

func ptr[T any](v T) *T { return &v }

func TestOtherProjectsAndOtherTypesAreNotDelivered(t *testing.T) {
	w := newWorld(t)
	other := w.mustCall("project.create", projects.CreateIn{Title: "Elsewhere"}).(projects.Item).Item.ID
	w.emit(
		docUpdated(other, "doc_x", 1),
		ops.Event{Type: "entity.updated", ProjectID: w.project, Payload: map[string]any{"entity_id": "ent_1"}},
		ops.Event{Type: "compendium.changed", ProjectID: w.project},
		ops.Event{Type: "doc.created", ProjectID: w.project, DocumentID: "doc_1"},
		ops.Event{Type: "ai.run.finished", ProjectID: w.project, Payload: map[string]any{"run_id": "run_1"}},
		ops.Event{Type: "doc.updated", CompendiumID: "cmp_1", Payload: map[string]any{"revision": 1}}, // no project
		ops.Event{Type: "share.viewed", ProjectID: w.project},
	)
	w.pump()
	if len(w.recv.requests()) != 0 {
		t.Fatalf("%d requests for events that were not subscribed to", len(w.recv.requests()))
	}
	// The cursor still moved past them.
	if w.count(`SELECT cursor FROM webhook_state`) < w.count(`SELECT max(seq) FROM events`) {
		t.Error("the cursor did not move over events that are not for webhooks")
	}
}

// Payloads carry ids and counts, in the envelope, and nothing a person wrote.
func TestPayloadIsIdsAndCountsOnly(t *testing.T) {
	w := newWorld(t)
	w.emit(
		ops.Event{Type: "doc.updated", ProjectID: w.project, DocumentID: "doc_1", Payload: map[string]any{
			"revision": 4, "words": 1200, "content": true, "title": "A Secret Title", "text": "the words of the chapter", "entity_id": "ent_7", "name": "Ada Lovelace"}},
		ops.Event{Type: "export.completed", ProjectID: w.project, Payload: map[string]any{"export_id": "exp_1", "format": "md", "documents": 3, "bytes": 999, "filename": "My Book.md"}},
		ops.Event{Type: "suggestion.created", ProjectID: w.project, DocumentID: "doc_1", Payload: map[string]any{"suggestion_id": "sug_1", "text": "replace this"}},
	)
	w.pump()
	got := w.recv.requests()
	if len(got) != 3 {
		t.Fatalf("%d requests", len(got))
	}
	allowed := map[string]bool{"revision": true, "words": true, "content": true, "document_id": true, "export_id": true, "format": true, "documents": true, "bytes": true, "suggestion_id": true, "entity_id": true}
	for _, r := range got {
		var p struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			CreatedAt string `json:"created_at"`
			ProjectID string `json:"project_id"`
			Data      map[string]any
		}
		if err := json.Unmarshal(r.Body, &p); err != nil {
			t.Fatal(err)
		}
		if p.ID == "" || p.ProjectID != w.project || p.Type == "" || p.CreatedAt == "" {
			t.Errorf("envelope: %s", r.Body)
		}
		for k := range p.Data {
			if !allowed[k] {
				t.Errorf("%s carries %q: %s", p.Type, k, r.Body)
			}
		}
		for _, leak := range []string{"Secret Title", "words of the chapter", "Ada Lovelace", "My Book", "replace this"} {
			if strings.Contains(string(r.Body), leak) {
				t.Errorf("a payload carries %q: %s", leak, r.Body)
			}
		}
		if _, has := p.Data["project_id"]; has {
			t.Error("the project id belongs to the envelope")
		}
	}
}

// Saves of one document that arrive inside the debounce window are one notification, the latest.
func TestDocumentUpdatesAreDebouncedIntoOne(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.DocDebounce = 5 * time.Second })
	ctx := context.Background()
	for rev := 1; rev <= 30; rev++ {
		w.emit(docUpdated(w.project, "doc_1", rev))
	}
	w.emit(docUpdated(w.project, "doc_2", 1))
	if _, err := w.svc.Feed(ctx); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries`); n != 2 {
		t.Fatalf("%d deliveries for 31 saves of two documents, want 2", n)
	}
	w.svc.Dispatch(ctx)
	w.svc.Wait()
	if len(w.recv.requests()) != 0 {
		t.Fatal("sent before the window passed")
	}
	w.clk.Advance(6 * time.Second)
	w.pump()
	got := w.recv.requests()
	if len(got) != 2 {
		t.Fatalf("%d requests", len(got))
	}
	found := false
	for _, r := range got {
		if strings.Contains(string(r.Body), `"revision":30`) {
			found = true
		}
		if strings.Contains(string(r.Body), `"revision":29`) {
			t.Error("an older save was sent")
		}
	}
	if !found {
		t.Error("the latest save was not the one sent")
	}
	// After it was sent, the next save is a new notification.
	w.emit(docUpdated(w.project, "doc_1", 31))
	w.pump()
	w.clk.Advance(6 * time.Second)
	w.pump()
	if len(w.recv.requests()) != 3 {
		t.Errorf("%d requests", len(w.recv.requests()))
	}
}

// ---- sending ---------------------------------------------------------------------------

func TestADeliveryIsSignedAndAttributed(t *testing.T) {
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	got := w.recv.requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	r := got[0]
	ts, id := r.Header.Get(webhook.HeaderTimestamp), r.Header.Get(webhook.HeaderID)
	if !strings.HasPrefix(id, "whd_") || r.Header.Get(webhook.HeaderEvent) != "document.updated" || r.Header.Get(webhook.HeaderAttempt) != "1" ||
		r.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(r.Header.Get("User-Agent"), "Scriobh-Webhook/") {
		t.Errorf("headers: %v", r.Header)
	}
	if n, _ := strconv.ParseInt(ts, 10, 64); n != w.clk.Now().Unix() {
		t.Errorf("timestamp %s, want %d", ts, w.clk.Now().Unix())
	}
	if !webhook.Verify(w.secret, r.Header.Get(webhook.HeaderSignature), ts, r.Body, w.clk.Now(), 5*time.Minute) {
		t.Error("the signature does not verify with the secret that was shown at creation")
	}
	if webhook.Verify("whsec_wrong", r.Header.Get(webhook.HeaderSignature), ts, r.Body, w.clk.Now(), 5*time.Minute) {
		t.Error("verified with the wrong secret")
	}
	var p struct{ ID string }
	_ = json.Unmarshal(r.Body, &p)
	if p.ID != id {
		t.Errorf("the body's id %s is not the header's %s", p.ID, id)
	}
	d := w.deliveries("delivered")
	if len(d) != 1 || d[0].Attempts != 1 || d[0].Status != 200 || d[0].DeliveredAt == "" {
		t.Errorf("record: %+v", d)
	}
}

func TestRetriesBackOffAndStopAtTheLimit(t *testing.T) {
	w := newWorld(t)
	w.recv.status.Store(503)
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	d := w.deliveries("pending")
	if len(d) != 1 || d[0].Attempts != 1 || d[0].Status != 503 || d[0].Error != webhook.ErrClassServer {
		t.Fatalf("after the first failure: %+v", d)
	}
	want := w.clk.Now().Add(webhook.Backoff(1, 0.5)).UTC().Format("2006-01-02T15:04:05.000Z")
	if d[0].NextAttemptAt != want {
		t.Errorf("next attempt %s, want %s", d[0].NextAttemptAt, want)
	}
	// Not due yet: another pass sends nothing.
	n := len(w.recv.requests())
	w.pump()
	if len(w.recv.requests()) != n {
		t.Fatal("retried before it was due")
	}
	// Walk the whole schedule: one attempt each time it falls due.
	for attempt := 2; attempt <= 8; attempt++ {
		w.clk.Advance(webhook.Backoff(attempt-1, 0.5) + time.Second)
		w.pump()
		if got := len(w.recv.requests()); got != attempt {
			t.Fatalf("attempt %d: %d requests", attempt, got)
		}
	}
	dead := w.deliveries("dead")
	if len(dead) != 1 || dead[0].Attempts != 8 || dead[0].Error != webhook.ErrClassServer {
		t.Fatalf("after the last attempt: %+v", dead)
	}
	// Dead is final: nothing more is sent, however long we wait.
	w.clk.Advance(1000 * time.Hour)
	w.pump()
	if len(w.recv.requests()) != 8 {
		t.Error("a dead delivery was sent again")
	}
	// Every attempt carried the same delivery id and a rising attempt number.
	ids := map[string]bool{}
	for i, r := range w.recv.requests() {
		ids[r.Header.Get(webhook.HeaderID)] = true
		if r.Header.Get(webhook.HeaderAttempt) != strconv.Itoa(i+1) {
			t.Errorf("attempt header %q on request %d", r.Header.Get(webhook.HeaderAttempt), i+1)
		}
	}
	if len(ids) != 1 {
		t.Errorf("%d distinct delivery ids over the attempts", len(ids))
	}
}

func TestFailureKindsDecideWhetherToRetry(t *testing.T) {
	for _, c := range []struct {
		status int
		state  string
	}{{200, "delivered"}, {204, "delivered"}, {500, "pending"}, {502, "pending"}, {408, "pending"}, {429, "pending"}, {400, "dead"}, {401, "dead"}, {404, "dead"}, {410, "dead"}, {302, "dead"}} {
		w := newWorld(t)
		w.recv.status.Store(int32(c.status))
		w.emit(docUpdated(w.project, "doc_1", 1))
		w.pump()
		if n := len(w.deliveries(c.state)); n != 1 {
			t.Errorf("status %d: %d deliveries %s, want 1", c.status, n, c.state)
		}
	}
	// A 429 with Retry-After waits at least that long.
	w := newWorld(t)
	w.recv.hook = func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Retry-After", "1800")
		rw.WriteHeader(429)
	}
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	d := w.deliveries("pending")
	next, _ := time.Parse("2006-01-02T15:04:05.000Z", d[0].NextAttemptAt)
	if next.Sub(w.clk.Now()) < 30*time.Minute {
		t.Errorf("Retry-After was not honoured: %s", next.Sub(w.clk.Now()))
	}
}

func TestADeadStreakSwitchesTheWebhookOffAndASuccessResetsIt(t *testing.T) {
	w := newWorld(t)
	w.recv.status.Store(404)
	for i := 0; i < 9; i++ {
		w.emit(docUpdated(w.project, fmt.Sprintf("doc_%d", i), 1))
	}
	w.pump()
	h := w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if !h.Enabled || h.FailureStreak != 9 || h.Dead != 9 {
		t.Fatalf("after nine dead: %+v", h)
	}
	w.recv.status.Store(200)
	w.emit(docUpdated(w.project, "doc_ok", 1))
	w.pump()
	h = w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if h.FailureStreak != 0 || h.LastStatus != 200 {
		t.Fatalf("a success should reset the streak: %+v", h)
	}
	w.recv.status.Store(404)
	for i := 0; i < 10; i++ {
		w.emit(docUpdated(w.project, fmt.Sprintf("doc_b%d", i), 1))
	}
	w.pump()
	h = w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if h.Enabled || !strings.Contains(h.DisabledReason, "failed for good") {
		t.Fatalf("ten dead in a row should switch it off: %+v", h)
	}
	// Switching it on clears the reason and the streak.
	h = w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(true)}).(webhook.Item).Item
	if !h.Enabled || h.DisabledReason != "" || h.FailureStreak != 0 {
		t.Errorf("re-enabled: %+v", h)
	}
}

// A project in the trash sends nothing; what was waiting goes out when it is restored.
func TestTrashedProjectPausesDeliveries(t *testing.T) {
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.mustCall("project.trash", projects.StateIn{ID: w.project})
	w.pump()
	if len(w.recv.requests()) != 0 {
		t.Fatal("a trashed project sent a webhook")
	}
	if d := w.deliveries("pending"); len(d) != 1 {
		t.Errorf("the delivery should wait: %+v", d)
	}
	w.mustCall("project.restore", projects.StateIn{ID: w.project})
	w.pump()
	if len(w.recv.requests()) != 1 {
		t.Fatalf("restoring did not resume: %d requests", len(w.recv.requests()))
	}
	// And a test ping, a new webhook and a change are refused while it is in the trash.
	w.mustCall("project.trash", projects.StateIn{ID: w.project})
	if _, err := w.call("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}); err == nil {
		t.Error("a ping was sent for a trashed project")
	}
	if _, err := w.call("webhook.create", webhook.CreateIn{ProjectID: w.project, Name: "x", URL: w.recv.URL, Events: []string{"document.updated"}}); err == nil {
		t.Error("a webhook was created on a trashed project")
	}
	if _, err := w.call("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Name: ptr("renamed")}); err == nil {
		t.Error("a webhook of a trashed project was changed")
	}
}

func TestPurgingAProjectRemovesItsWebhooksAndDeliveries(t *testing.T) {
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	w.mustCall("project.trash", projects.StateIn{ID: w.project})
	w.mustCall("project.purge", projects.PurgeIn{ID: w.project, ConfirmTitle: "Hooked"})
	if n := w.count(`SELECT count(*) FROM webhooks`) + w.count(`SELECT count(*) FROM webhook_deliveries`); n != 0 {
		t.Errorf("%d rows survived the purge", n)
	}
}

// ---- the queue's limits ----------------------------------------------------------------------

func TestQueueCapsDropAndCount(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.PerWebhookQueueMax = 5; o.lim.QueueMax = 8 })
	for i := 0; i < 20; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%d", i)}})
	}
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 5 {
		t.Errorf("%d waiting, the per-webhook cap is 5", n)
	}
	if n := w.count(`SELECT dropped FROM webhook_state`); n != 15 {
		t.Errorf("%d dropped, want 15", n)
	}
	// A second webhook has a cap of its own, and the instance cap holds them together.
	w.create("second", []string{"comment.created"}, false)
	for i := 0; i < 20; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_b%d", i)}})
	}
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 8 {
		t.Errorf("%d waiting, the instance cap is 8", n)
	}
}

func TestOneInFlightPerWebhookAndTheInstanceRate(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.Concurrency = 4 })
	w.recv.delay.Store(int64(30 * time.Millisecond))
	for i := 0; i < 6; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%d", i)}})
	}
	for i := 0; i < 20 && len(w.recv.requests()) < 6; i++ {
		w.pump()
	}
	if len(w.recv.requests()) != 6 {
		t.Fatalf("%d requests", len(w.recv.requests()))
	}
	if p := w.recv.peak.Load(); p != 1 {
		t.Errorf("%d requests to one webhook were in flight at once", p)
	}
	// The rate: a bucket of 6 a minute holds back what is over its burst (a burst of 5).
	r := newWorld(t, func(o *worldOpts) { o.lim.RatePerMin = 6; o.lim.Concurrency = 8 })
	r.create("two", []string{"comment.created"}, false)
	r.create("three", []string{"comment.created"}, false)
	for i := 0; i < 30; i++ {
		r.emit(ops.Event{Type: "comment.created", ProjectID: r.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%d", i)}})
	}
	r.pump()
	if got := len(r.recv.requests()); got > 6 {
		t.Errorf("%d deliveries in the first pass; the rate allows a burst of 5 and one more a ten seconds", got)
	}
}

// ---- secrets ---------------------------------------------------------------------------------

func TestSecretIsSealedAndBoundToItsRow(t *testing.T) {
	w := newWorld(t)
	dump := func() string {
		return testutil.Dump(t, w.d)
	}
	_ = dump
	var sealed string
	if err := w.d.Read().QueryRow(`SELECT secret_sealed FROM webhooks WHERE id = ?`, w.hookID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, w.secret) || strings.Contains(sealed, strings.TrimPrefix(w.secret, "whsec_")) || !strings.HasPrefix(sealed, "v1.") {
		t.Errorf("the secret is stored as %q", sealed)
	}
	// No table, no column holds it in the clear.
	rows, err := w.d.Read().Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, tb := range tables {
		r, err := w.d.Read().Query(`SELECT * FROM "` + tb + `"`)
		if err != nil {
			continue
		}
		cols, _ := r.Columns()
		for r.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = r.Scan(ptrs...)
			for _, v := range vals {
				if strings.Contains(fmt.Sprint(v), w.secret) {
					t.Errorf("the secret is in the clear in %s", tb)
				}
			}
		}
		r.Close()
	}
	// A sealed value copied into another webhook's row does not open there: the webhook is switched off, not retried.
	first := w.hookID
	w.create("other", []string{"document.updated"}, false)
	if _, err := w.d.Writer().Exec(`UPDATE webhooks SET secret_sealed = ? WHERE id = ?`, sealed, w.hookID); err != nil {
		t.Fatal(err)
	}
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	h := w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if h.Enabled || !strings.Contains(h.DisabledReason, "secret") {
		t.Errorf("a secret that cannot be opened must switch the webhook off with a reason: %+v", h)
	}
	if d := w.deliveries("pending"); len(d) != 1 || d[0].Attempts != 0 {
		t.Errorf("the delivery should wait unsent: %+v", d)
	}
	// The first webhook, whose secret is its own, delivered.
	if n := len(w.recv.requests()); n != 1 {
		t.Errorf("%d requests, want the first webhook's one", n)
	}
	_ = first
	// Rotating makes a fresh sealed secret and switches it back on.
	out := w.mustCall("webhook.rotate_secret", webhook.RotateIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.CreateOut)
	if !out.Item.Enabled || out.Item.DisabledReason != "" || out.Secret == "" || out.Secret == w.secret {
		t.Errorf("rotate: %+v", out.Item)
	}
	w.pump()
	if n := len(w.recv.requests()); n != 2 {
		t.Errorf("the waiting delivery was not sent after the rotation: %d", n)
	}
}

func TestRotationSignsWithBothSecretsForAWhile(t *testing.T) {
	w := newWorld(t)
	old := w.secret
	out := w.mustCall("webhook.rotate_secret", webhook.RotateIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.CreateOut)
	if !out.Item.PrevSecretLive || out.Item.SecretHint == webhook.Hint(old) {
		t.Errorf("rotated: %+v", out.Item)
	}
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	r := w.recv.requests()[0]
	hdr, ts := r.Header.Get(webhook.HeaderSignature), r.Header.Get(webhook.HeaderTimestamp)
	if strings.Count(hdr, "v1=") != 2 || !webhook.Verify(out.Secret, hdr, ts, r.Body, w.clk.Now(), time.Minute) || !webhook.Verify(old, hdr, ts, r.Body, w.clk.Now(), time.Minute) {
		t.Errorf("signature header: %s", hdr)
	}
	// With no grace period only the new secret signs.
	out2 := w.mustCall("webhook.rotate_secret", webhook.RotateIn{ProjectID: w.project, WebhookID: w.hookID, KeepOldHours: ptr(0)}).(webhook.CreateOut)
	w.emit(docUpdated(w.project, "doc_2", 1))
	w.pump()
	r = w.recv.requests()[1]
	if strings.Count(r.Header.Get(webhook.HeaderSignature), "v1=") != 1 || !webhook.Verify(out2.Secret, r.Header.Get(webhook.HeaderSignature), r.Header.Get(webhook.HeaderTimestamp), r.Body, w.clk.Now(), time.Minute) {
		t.Errorf("after rotating with no grace: %s", r.Header.Get(webhook.HeaderSignature))
	}
}

// ---- comment text ------------------------------------------------------------------------------

func TestCommentWordsLeaveOnlyWhenAskedForAndAreNeverStored(t *testing.T) {
	for _, text := range []bool{false, true} {
		w := newWorld(t, func(o *worldOpts) { o.noHook = true })
		w.create("c", []string{"comment.created"}, text)
		cid := db.NewID("cmt")
		if _, err := w.d.Writer().Exec(`INSERT INTO comments(id, project_id, author_id, kind, body, created_at, updated_at) VALUES (?,?,?, 'comment', ?, ?, ?)`,
			cid, w.project, w.owner.UserID, "The comment's own WORDS about the ending.", db.Now(), db.Now()); err != nil {
			t.Fatalf("insert comment: %v", err)
		}
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, DocumentID: "doc_1", Payload: map[string]any{"comment_id": cid, "thread_id": cid, "kind": "comment"}})
		w.pump()
		r := w.recv.requests()
		if len(r) != 1 {
			t.Fatalf("%d requests", len(r))
		}
		sent := strings.Contains(string(r[0].Body), "WORDS about the ending")
		if sent != text {
			t.Errorf("include_text=%v but the words were sent=%v: %s", text, sent, r[0].Body)
		}
		if text && !webhook.Verify(w.secret, r[0].Header.Get(webhook.HeaderSignature), r[0].Header.Get(webhook.HeaderTimestamp), r[0].Body, w.clk.Now(), time.Minute) {
			t.Error("the signature does not cover the body that carried the words")
		}
		// Whatever was sent, the outbox never kept the words.
		var stored string
		_ = w.d.Read().QueryRow(`SELECT group_concat(payload_json || last_excerpt) FROM webhook_deliveries`).Scan(&stored)
		if strings.Contains(stored, "WORDS") {
			t.Errorf("the outbox holds the comment's words: %s", stored)
		}
	}
	// A comment deleted before it was sent is not sent.
	w := newWorld(t, func(o *worldOpts) { o.noHook = true })
	w.create("c", []string{"comment.created"}, true)
	cid := db.NewID("cmt")
	if _, err := w.d.Writer().Exec(`INSERT INTO comments(id, project_id, author_id, kind, body, created_at, updated_at, deleted_at) VALUES (?,?,?, 'comment', 'GONE WORDS', ?, ?, ?)`,
		cid, w.project, w.owner.UserID, db.Now(), db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": cid}})
	w.pump()
	if r := w.recv.requests(); len(r) != 1 || strings.Contains(string(r[0].Body), "GONE") {
		t.Errorf("a deleted comment's words were sent: %v", r)
	}
	// A long comment is cut, and says so.
	w2 := newWorld(t, func(o *worldOpts) { o.noHook = true })
	w2.create("c", []string{"comment.created"}, true)
	cid = db.NewID("cmt")
	if _, err := w2.d.Writer().Exec(`INSERT INTO comments(id, project_id, author_id, kind, body, created_at, updated_at) VALUES (?,?,?, 'comment', ?, ?, ?)`,
		cid, w2.project, w2.owner.UserID, strings.Repeat("é", 5000), db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	w2.emit(ops.Event{Type: "comment.created", ProjectID: w2.project, Payload: map[string]any{"comment_id": cid}})
	w2.pump()
	var p struct {
		Data struct {
			Text      string
			Truncated bool `json:"text_truncated"`
		}
	}
	_ = json.Unmarshal(w2.recv.requests()[0].Body, &p)
	if len([]rune(p.Data.Text)) != webhook.MaxCommentText || !p.Data.Truncated {
		t.Errorf("long comment: %d characters, truncated=%v", len([]rune(p.Data.Text)), p.Data.Truncated)
	}
}

// ---- crashes -----------------------------------------------------------------------------------

// Killed between the feeder and the send: the outbox row is on disk, and a new
// process sends it.
func TestCrashBetweenEnqueueAndSend(t *testing.T) {
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil { // the process "dies" here
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 1 || len(w.recv.requests()) != 0 {
		t.Fatalf("setup: %d pending, %d sent", n, len(w.recv.requests()))
	}
	w2 := &world{t: t, d: w.d, store: w.store, clk: w.clk, owner: w.owner, project: w.project, recv: w.recv, hookID: w.hookID, reg: w.reg}
	w2.svc = webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{AllowPrivate: true, DocDebounce: -1}, Now: w.clk.Now,
		Sender: webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, TLS: w.recv.Client().Transport.(*http.Transport).TLSClientConfig})})
	w2.pump()
	if len(w.recv.requests()) != 1 {
		t.Errorf("the restarted service sent %d", len(w.recv.requests()))
	}
	// And the event is not made into a second delivery by the new process.
	w2.pump()
	if n := w.count(`SELECT count(*) FROM webhook_deliveries`); n != 1 {
		t.Errorf("%d deliveries", n)
	}
}

// Killed between the send and writing its result: the delivery is still due, and
// goes again with the same id (at least once). A receiver that stores ids applies it once.
func TestCrashBetweenSendAndRecord(t *testing.T) {
	var crash atomic.Bool
	crash.Store(true)
	w := newWorld(t, func(o *worldOpts) {
		o.afterSend = func(string) error {
			if crash.Load() {
				return fmt.Errorf("killed")
			}
			return nil
		}
	})
	w.emit(docUpdated(w.project, "doc_1", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.svc.Dispatch(context.Background()) // one pass: the "process" dies after the send
	w.svc.Wait()
	if len(w.recv.requests()) != 1 {
		t.Fatalf("%d requests", len(w.recv.requests()))
	}
	if d := w.deliveries("pending"); len(d) != 1 || d[0].Attempts != 0 {
		t.Fatalf("the unrecorded attempt left the row as %+v", d)
	}
	crash.Store(false)
	// The "dead" process left its claim on the row; once the claim is old enough nobody is
	// holding it, and the delivery goes again. (A restart gives it back at once: see
	// TestAStartGivesBackWhatTheLastProcessClaimed.)
	w.clk.Advance(time.Duration(w.svc.ClaimTTL()) + time.Second)
	w.pump()
	got := w.recv.requests()
	if len(got) != 2 {
		t.Fatalf("%d requests after the restart", len(got))
	}
	if got[0].Header.Get(webhook.HeaderID) != got[1].Header.Get(webhook.HeaderID) {
		t.Error("the repeat carried a different delivery id, so a receiver could not tell it was a repeat")
	}
	if d := w.deliveries("delivered"); len(d) != 1 {
		t.Errorf("%+v", d)
	}
	// A receiver that stores the ids it has seen applies each notification once.
	seen := map[string]bool{}
	applied := 0
	for _, r := range got {
		if id := r.Header.Get(webhook.HeaderID); !seen[id] {
			seen[id] = true
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("applied %d times", applied)
	}
}

// A server that is stopping does not count the attempt it cut short.
func TestShutdownMidSendDoesNotSpendAnAttempt(t *testing.T) {
	w := newWorld(t)
	started := make(chan struct{}, 1)
	w.recv.hook = func(rw http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}
	w.emit(docUpdated(w.project, "doc_1", 1))
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := w.svc.Feed(ctx); err != nil {
		t.Fatal(err)
	}
	w.svc.Dispatch(ctx)
	<-started
	cancel()
	w.svc.Wait()
	if d := w.deliveries("pending"); len(d) != 1 || d[0].Attempts != 0 {
		t.Errorf("%+v", d)
	}
}

// ---- the running service --------------------------------------------------------------------

func TestStartedServiceDeliversWithoutBeingDriven(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	// The real clock for the loops: they run on tickers.
	svc := webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{AllowPrivate: true, DocDebounce: -1},
		Sender: webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, TLS: w.recv.Client().Transport.(*http.Transport).TLSClientConfig})})
	svc.Start(ctx)
	w.emit(docUpdated(w.project, "doc_1", 1))
	deadline := time.Now().Add(10 * time.Second)
	for len(w.recv.requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(w.recv.requests()) != 1 {
		t.Fatalf("%d requests", len(w.recv.requests()))
	}
	cancel()
	svc.Wait()
}

// ---- housekeeping -----------------------------------------------------------------------------

func TestSweepKeepsRecentAndDropsOld(t *testing.T) {
	w := newWorld(t)
	ins := func(state, when string) {
		if _, err := w.d.Writer().Exec(`INSERT INTO webhook_deliveries(id, webhook_id, project_id, event, payload_json, state, attempts, next_attempt_at, created_at, delivered_at)
			VALUES (?,?,?, 'document.updated', '{}', ?, 1, ?, ?, ?)`, db.NewID("whd"), w.hookID, w.project, state, when, when, when); err != nil {
			t.Fatal(err)
		}
	}
	old := w.clk.Now().Add(-20 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	older := w.clk.Now().Add(-40 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")
	recent := w.clk.Now().Add(-time.Hour).Format("2006-01-02T15:04:05.000Z")
	ins("delivered", old)
	ins("delivered", recent)
	ins("dead", old)
	ins("dead", older)
	ins("pending", older)
	ins("pending", recent)
	if err := w.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Kept: the recent delivered, the 20-day dead (dead ones are kept twice as long), the recent pending.
	// Gone: the 20-day delivered, the 40-day dead, and the pending one that has waited 40 days.
	if n := w.count(`SELECT count(*) FROM webhook_deliveries`); n != 3 {
		t.Errorf("%d rows left, want 3", n)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 1 {
		t.Errorf("%d pending rows left, want the recent one", n)
	}
}

var _ = sql.ErrNoRows
var _ = tls.VersionTLS12

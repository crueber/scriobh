package webhook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/webhook"
)

// anotherPerson makes a second user with a project and a webhook of their own, and returns the project's id.
func (w *world) anotherPerson(name string) string {
	w.t.Helper()
	p := ops.Principal{UserID: testutil.User(w.t, w.d, ops.RoleWriter, name+"@t.local"), Role: ops.RoleWriter}
	out, err := w.reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: name})
	if err != nil {
		w.t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	if _, err := w.reg.Call(context.Background(), p, "webhook.create", webhook.CreateIn{ProjectID: pid, Name: name, URL: w.recv.URL + "/" + name, Events: []string{"comment.created"}}); err != nil {
		w.t.Fatal(err)
	}
	w.svc.Changed()
	return pid
}

func (w *world) sentTo(project string) int {
	n := 0
	for _, r := range w.recv.requests() {
		if strings.Contains(string(r.Body), project) {
			n++
		}
	}
	return n
}

func (w *world) comments(project string, n int, prefix string) {
	for i := 0; i < n; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%s%d", prefix, i)}})
	}
}

// One person fills the instance's queue and switches their webhooks off; the rows they
// left must not keep another project's events out. (They used to stay pending, never
// sent and never swept, and counted toward the queue for ever.)
func TestSwitchingOffAWebhookFreesItsQueue(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.PerWebhookQueueMax = 5; o.lim.QueueMax = 10 })
	w.create("second", []string{"comment.created"}, false)
	hooks := w.mustCall("webhook.list", webhook.ProjectIn{ProjectID: w.project}).(webhook.List).Items
	w.comments(w.project, 10, "a")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 10 {
		t.Fatalf("setup: %d pending", n)
	}
	for _, h := range hooks {
		w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: h.ID, Enabled: ptr(false)})
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE state = 'pending'`); n != 0 {
		t.Errorf("a webhook that was switched off still has %d deliveries waiting", n)
	}
	victim := w.anotherPerson("victim")
	w.comments(victim, 1, "v")
	w.pump()
	if w.sentTo(victim) != 1 {
		t.Errorf("another project's event was not delivered (dropped=%d)", w.count(`SELECT dropped FROM webhook_state`))
	}
	// Deleting a webhook takes its queue with it, and switching it back on does not replay what it missed.
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE webhook_id = ? AND state = 'pending'`, hooks[0].ID); n != 0 {
		t.Error("rows survived")
	}
}

// A project in the trash keeps its queue (it resumes when restored), but the queue is
// not counted against anyone else, and it is not kept for ever.
func TestATrashedProjectsQueueTakesNoRoomFromOthers(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.PerWebhookQueueMax = 100; o.lim.QueueMax = 5 })
	w.comments(w.project, 5, "a")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.mustCall("project.trash", projects.StateIn{ID: w.project})
	victim := w.anotherPerson("victim")
	w.comments(victim, 3, "v")
	w.pump()
	if w.sentTo(victim) != 3 {
		t.Errorf("%d of 3 delivered: the trashed project's waiting rows held the instance's queue", w.sentTo(victim))
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE project_id = ? AND state = 'pending'`, w.project); n != 5 {
		t.Errorf("the trashed project's queue should wait for a restore: %d", n)
	}
	// After a keeping time without a restore, the sweep drops it.
	w.clk.Advance(15 * 24 * time.Hour)
	if err := w.svc.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE project_id = ? AND state = 'pending'`, w.project); n != 0 {
		t.Errorf("%d rows of a project that stayed in the trash survived the sweep", n)
	}
}

// One project cannot fill the queue however many webhooks it makes.
func TestOneProjectCannotFillTheQueue(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.PerWebhookQueueMax = 100; o.lim.QueueMax = 1000; o.lim.ProjectQueueMax = 7 })
	for i := 0; i < 4; i++ {
		w.create(fmt.Sprintf("more%d", i), []string{"comment.created"}, false)
	}
	w.comments(w.project, 20, "a")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries WHERE project_id = ? AND state = 'pending'`, w.project); n != 7 {
		t.Errorf("%d waiting for one project, its cap is 7", n)
	}
	if w.count(`SELECT dropped FROM webhook_state`) == 0 {
		t.Error("the excess was not counted")
	}
}

// 150 due deliveries of one webhook must not hide another project's from the pass:
// the list holds one delivery for each webhook.
func TestAWebhookWithABacklogDoesNotBlockTheOthers(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) {
		o.lim.Concurrency, o.lim.RatePerMin, o.lim.ProjectRatePerMin = 4, 60000, 60000
		o.lim.PerWebhookQueueMax, o.lim.ProjectQueueMax, o.lim.QueueMax = 1000, 1000, 10000
	})
	w.recv.delay.Store(int64(30 * time.Millisecond))
	w.comments(w.project, 150, "a")
	for {
		more, err := w.svc.Feed(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	quiet := w.anotherPerson("quiet")
	w.comments(quiet, 1, "q")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if started := w.svc.Dispatch(context.Background()); started != 2 {
		t.Errorf("the first pass started %d sends; one for the busy webhook and one for the quiet one make two", started)
	}
	w.svc.Wait()
	if w.sentTo(quiet) != 1 {
		t.Errorf("the quiet project's delivery was not in the first pass")
	}
}

// A project's webhooks share a part of the send slots, so one project cannot hold them all.
func TestOneProjectCannotHoldEverySendSlot(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.Concurrency, o.lim.RatePerMin, o.lim.ProjectRatePerMin = 4, 60000, 60000 })
	for i := 0; i < 5; i++ {
		w.create(fmt.Sprintf("h%d", i), []string{"comment.created"}, false)
	}
	w.recv.delay.Store(int64(100 * time.Millisecond))
	w.comments(w.project, 1, "a")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	quiet := w.anotherPerson("quiet")
	w.comments(quiet, 1, "q")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if started := w.svc.Dispatch(context.Background()); started != 3 {
		t.Errorf("started %d: the busy project is held to two of the four slots, the quiet one gets one", started)
	}
	w.svc.Wait()
	if w.sentTo(quiet) != 1 {
		t.Error("the quiet project waited behind the busy one")
	}
}

// A save that arrives while the notification for the previous one is being sent is a
// notification of its own: it used to be folded into the row in flight and lost.
func TestASaveDuringASendIsNotLost(t *testing.T) {
	w := newWorld(t)
	w.recv.delay.Store(int64(300 * time.Millisecond))
	w.emit(docUpdated(w.project, "doc_1", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.svc.Dispatch(context.Background()); n != 1 {
		t.Fatalf("started %d", n)
	}
	time.Sleep(80 * time.Millisecond) // the send is in flight
	w.emit(docUpdated(w.project, "doc_1", 2))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	w.pump()
	saw := map[int]bool{}
	for _, r := range w.recv.requests() {
		var p struct{ Data struct{ Revision int } }
		_ = json.Unmarshal(r.Body, &p)
		saw[p.Data.Revision] = true
	}
	if !saw[1] || !saw[2] {
		t.Errorf("revisions delivered: %v; both must arrive", saw)
	}
}

// Folding merges: a change of the scene after a text save does not turn the waiting
// notification into "the text did not change, no words".
func TestFoldingKeepsWhatTheEarlierSaveSaid(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.lim.DocDebounce = 5 * time.Second })
	w.emit(docUpdated(w.project, "doc_1", 1)) // words 101, content true
	w.emit(ops.Event{Type: "doc.updated", ProjectID: w.project, DocumentID: "doc_1", Payload: map[string]any{"revision": 3, "content": false, "scene": "pov"}})
	w.emit(ops.Event{Type: "doc.updated", ProjectID: w.project, DocumentID: "doc_1", Payload: map[string]any{"revision": 2, "content": false}}) // out of order
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(10 * time.Second)
	w.pump()
	got := w.recv.requests()
	if len(got) != 1 {
		t.Fatalf("%d notifications", len(got))
	}
	var p struct{ Data map[string]any }
	_ = json.Unmarshal(got[0].Body, &p)
	if p.Data["content"] != true || p.Data["words"] != float64(101) || p.Data["revision"] != float64(3) || p.Data["scene"] != "pov" {
		t.Errorf("merged: %s", got[0].Body)
	}
	// A later text save brings its own words.
	w.emit(ops.Event{Type: "doc.updated", ProjectID: w.project, DocumentID: "doc_2", Payload: map[string]any{"revision": 1, "content": false, "scene": "pov"}})
	w.emit(docUpdated(w.project, "doc_2", 2))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.clk.Advance(10 * time.Second)
	w.pump()
	var q struct{ Data map[string]any }
	_ = json.Unmarshal(w.recv.requests()[1].Body, &q)
	if q.Data["content"] != true || q.Data["words"] != float64(102) || q.Data["revision"] != float64(2) {
		t.Errorf("merged: %s", w.recv.requests()[1].Body)
	}
}

// A process that stopped with a delivery claimed gives it back when the next one starts.
func TestAStartGivesBackWhatTheLastProcessClaimed(t *testing.T) {
	w := newWorld(t)
	w.emit(docUpdated(w.project, "doc_1", 1))
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The fixture clock is months behind the real one the new service uses; the row is dated by the real one, so the sweep at start leaves it.
	if _, err := w.d.Writer().Exec(`UPDATE webhook_deliveries SET created_at = ?, claimed_at = ?`, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}
	w.pump()
	if len(w.recv.requests()) != 0 {
		t.Fatal("a claimed delivery was sent by someone else")
	}
	ctx, cancel := context.WithCancel(context.Background())
	svc := webhook.New(webhook.Options{DB: w.d, Box: w.box, Events: w.store, Limits: webhook.Limits{AllowPrivate: true, DocDebounce: -1},
		Sender: webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, TLS: w.recv.Client().Transport.(*http.Transport).TLSClientConfig})})
	svc.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for len(w.recv.requests()) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	svc.Wait()
	if len(w.recv.requests()) != 1 {
		var st, cl string
		_ = w.d.Read().QueryRow(`SELECT state || ' ' || attempts || ' ' || next_attempt_at, COALESCE(claimed_at, 'NULL') FROM webhook_deliveries`).Scan(&st, &cl)
		t.Errorf("%d requests after the restart: row %s claimed %s now %s", len(w.recv.requests()), st, cl, time.Now().UTC().Format(time.RFC3339))
	}
}

// With local targets allowed, a failing answer could be a page of an internal service:
// only its status and the kind of failure are kept.
func TestNoExcerptWhenLocalTargetsAreAllowed(t *testing.T) {
	w := newWorld(t) // allows private targets
	w.recv.hook = func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(500)
		_, _ = rw.Write([]byte("INTERNAL ADMIN PAGE with a secret token"))
	}
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	out := w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.TestOut)
	b, _ := json.Marshal([]any{out, w.deliveries("")})
	if strings.Contains(string(b), "INTERNAL") || out.Delivery.Excerpt != "" || out.Delivery.Error != webhook.ErrClassServer || out.Delivery.Status != 500 {
		t.Errorf("%s", b)
	}
	var stored string
	_ = w.d.Read().QueryRow(`SELECT COALESCE(group_concat(last_excerpt), '') FROM webhook_deliveries`).Scan(&stored)
	if strings.Trim(stored, ",") != "" {
		t.Errorf("the excerpt was stored: %q", stored)
	}
}

// The address's path and query are sealed: not in any table, not in a listing, but sent.
func TestTheAddressPathIsSealed(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.noHook = true })
	out := w.mustCall("webhook.create", webhook.CreateIn{ProjectID: w.project, Name: "tok", URL: w.recv.URL + "/hooks/T0KEN-in-path?key=QUERYSECRET", Events: []string{"document.updated"}}).(webhook.CreateOut)
	w.hookID = out.Item.ID
	b, _ := json.Marshal([]any{out, w.mustCall("webhook.list", webhook.ProjectIn{ProjectID: w.project}), w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})})
	if strings.Contains(string(b), "T0KEN") || strings.Contains(string(b), "QUERYSECRET") || !out.Item.URLPathSet || out.Item.URL != w.recv.URL {
		t.Errorf("an op showed the path: %s", b)
	}
	var stored string
	_ = w.d.Read().QueryRow(`SELECT url || '|' || url_path_sealed FROM webhooks WHERE id = ?`, w.hookID).Scan(&stored)
	if strings.Contains(stored, "T0KEN") || strings.Contains(stored, "QUERYSECRET") {
		t.Errorf("stored in the clear: %s", stored)
	}
	// The delivery goes to the whole address.
	var got []string
	w.recv.hook = func(rw http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.RequestURI())
		rw.WriteHeader(200)
	}
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	if len(got) != 1 || got[0] != "/hooks/T0KEN-in-path?key=QUERYSECRET" {
		t.Errorf("the receiver was called at %v", got)
	}
	// The test ping goes there too, and the answer to it names nothing.
	w.clk.Advance(10 * time.Second)
	got = nil
	pb, _ := json.Marshal(w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}))
	if len(got) != 1 || strings.Contains(string(pb), "T0KEN") {
		t.Errorf("ping at %v: %s", got, pb)
	}
	// Setting the address again replaces the whole of it; one without a path clears the sealed part.
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, URL: ptr(w.recv.URL + "/other")})
	w.clk.Advance(10 * time.Second)
	got = nil
	w.emit(docUpdated(w.project, "doc_2", 1))
	w.pump()
	if len(got) != 1 || got[0] != "/other" {
		t.Errorf("after an update: %v", got)
	}
	h := w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, URL: ptr(w.recv.URL)}).(webhook.Item).Item
	if h.URLPathSet {
		t.Error("an address with no path still says it has one")
	}
	// An address that cannot be opened (the key changed) switches the webhook off with a reason.
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, URL: ptr(w.recv.URL + "/again")})
	if _, err := w.d.Writer().Exec(`UPDATE webhooks SET url_path_sealed = 'v1.AAAA' WHERE id = ?`, w.hookID); err != nil {
		t.Fatal(err)
	}
	w.emit(docUpdated(w.project, "doc_3", 1))
	w.pump()
	g := w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if g.Enabled || !strings.Contains(g.DisabledReason, "address") {
		t.Errorf("%+v", g)
	}
}

// An event that was already made into a delivery is not made into a second one, and is not counted as queued.
func TestAReplayedEventIsNotADuplicate(t *testing.T) {
	w := newWorld(t)
	w.comments(w.project, 3, "a")
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.d.Writer().Exec(`UPDATE webhook_state SET cursor = 0`); err != nil { // the feeder reads them again
		t.Fatal(err)
	}
	if _, err := w.svc.Feed(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := w.count(`SELECT count(*) FROM webhook_deliveries`); n != 3 {
		t.Errorf("%d deliveries for 3 events", n)
	}
	if n := w.count(`SELECT dropped FROM webhook_state`); n != 0 {
		t.Errorf("a duplicate was counted as dropped: %d", n)
	}
}

// The record of test pings does not grow without bound.
func TestThePingRecordIsBounded(t *testing.T) {
	w := newWorld(t)
	w.svc.SeedPinged(1500, w.clk.Now().Add(-time.Hour))
	w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})
	if n := w.svc.PingedLen(); n > 5 {
		t.Errorf("%d entries kept", n)
	}
}

var _ = db.Now

package gitmirror_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/webhook"
)

// A receiver that records what it is sent and answers as a test tells it to.
type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	got  []received
	code int
	body string
	slow time.Duration
	to   string
}

type received struct {
	Path    string
	Query   string
	Header  http.Header
	Body    []byte
	Attempt string
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{code: 204}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		b, _ := io.ReadAll(q.Body)
		r.mu.Lock()
		r.got = append(r.got, received{q.URL.Path, q.URL.RawQuery, q.Header.Clone(), b, q.Header.Get(webhook.HeaderAttempt)})
		code, body, slow, to := r.code, r.body, r.slow, r.to
		r.mu.Unlock()
		if slow > 0 {
			// Slow, but gone as soon as the sender gives up: the server's Close waits for its handlers,
			// and a handler that slept out the whole delay made every test that used it wait for it.
			select {
			case <-time.After(slow):
			case <-q.Context().Done():
			}
		}
		if to != "" {
			http.Redirect(w, q, to, http.StatusFound)
			return
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) set(code int, body string) {
	r.mu.Lock()
	r.code, r.body = code, body
	r.mu.Unlock()
}

func (r *receiver) received() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received{}, r.got...)
}

func (r *receiver) url(path string) string { return r.srv.URL + path }

func triggerHarness(t *testing.T, rc *receiver, timeout time.Duration) *harness {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(rc.srv.Certificate())
	sender := webhook.NewSender(webhook.SenderOptions{AllowPrivate: true, Timeout: timeout, TLS: &tls.Config{RootCAs: pool}})
	h := newHarness(t, func(o *gitmirror.Options) { o.TriggerSender = sender })
	h.start()
	return h
}

func (h *harness) setTrigger(remoteID, url string) gitmirror.TriggerOut {
	h.t.Helper()
	return h.must(h.owner, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: remoteID, URL: &url}).(gitmirror.TriggerOut)
}

func (h *harness) deliveries(remoteID string) []struct {
	State, Status string
	Attempts      int
} {
	h.t.Helper()
	rows, err := h.d.Read().Query(`SELECT state, last_status, attempts FROM git_trigger_deliveries WHERE remote_id = ? ORDER BY created_at, id`, remoteID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []struct {
		State, Status string
		Attempts      int
	}
	for rows.Next() {
		var x struct {
			State, Status string
			Attempts      int
		}
		if err := rows.Scan(&x.State, &x.Status, &x.Attempts); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, x)
	}
	return out
}

// waitFor polls (the sender runs in the background) until f is true or a few seconds pass.
func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 400; i++ {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAPushCallsTheTriggerOnceSignedWithNothingOfTheManuscript(t *testing.T) {
	rc := newReceiver(t)
	h := triggerHarness(t, rc, 2*time.Second)
	url, _, dir := h.bareRemote()
	r := h.addRemote(url)
	out := h.setTrigger(r.ID, rc.url("/hooks/SECRET-PATH-CANARY?token=QUERY-CANARY"))
	if !strings.HasPrefix(out.Secret, "whsec_") || out.Item.Enabled != true || out.Item.Origin != rc.srv.URL || !out.Item.HasPath || out.Item.SecretHint == out.Secret {
		t.Fatalf("trigger = %+v", out)
	}
	// Nothing about where it calls, beyond the origin, is shown or kept in the clear.
	if b := mustJSON(t, h.remoteByID(r.ID)); strings.Contains(b, "CANARY") || strings.Contains(b, out.Secret) {
		t.Errorf("the remote shows the trigger's path or secret: %s", b)
	}
	got := h.must(h.owner, "git.trigger.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID}).(struct {
		Item *gitmirror.Trigger `json:"item,omitempty"`
	})
	if got.Item == nil || strings.Contains(mustJSON(t, got), "CANARY") || strings.Contains(mustJSON(t, got), out.Secret) {
		t.Errorf("get = %+v", got)
	}
	var dump string
	_ = h.d.Read().QueryRow(`SELECT url || path_sealed || secret_sealed || secret_hint FROM git_triggers`).Scan(&dump)
	if strings.Contains(dump, "CANARY") || strings.Contains(dump, out.Secret) {
		t.Errorf("the table holds the path or the secret in the clear: %s", dump)
	}

	if p := h.pushRemote(r.ID); p.Status != "ok" {
		t.Fatalf("push = %+v", p)
	}
	waitFor(t, "the delivery", func() bool { return len(rc.received()) == 1 })
	d := rc.received()[0]
	if d.Path != "/hooks/SECRET-PATH-CANARY" || d.Query != "token=QUERY-CANARY" {
		t.Errorf("called %s?%s", d.Path, d.Query)
	}
	if d.Header.Get(webhook.HeaderEvent) != "git.pushed" || d.Header.Get(webhook.HeaderAttempt) != "1" || d.Header.Get(webhook.HeaderID) == "" {
		t.Errorf("headers = %v", d.Header)
	}
	if !webhook.Verify(out.Secret, d.Header.Get(webhook.HeaderSignature), d.Header.Get(webhook.HeaderTimestamp), d.Body, h.clk.Now(), 5*time.Minute) {
		t.Errorf("the signature does not verify with the secret the owner was shown")
	}
	var body map[string]string
	if err := json.Unmarshal(d.Body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"event": "git.pushed", "delivery": d.Header.Get(webhook.HeaderID), "project": "the-salt-road", "repository": body["repository"], "branch": "main", "commit": head(t, dir, "main").String()}
	if len(body) != len(want) || !strings.HasSuffix(body["repository"], "remote") {
		t.Errorf("body = %v", body)
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("body[%s] = %q, want %q", k, body[k], v)
		}
	}
	for _, w := range []string{"harbour", "Salt on the wind", "Maybe a storm"} {
		if strings.Contains(string(d.Body), w) {
			t.Errorf("the body carries manuscript text: %s", d.Body)
		}
	}
	waitFor(t, "the record", func() bool { ds := h.deliveries(r.ID); return len(ds) == 1 && ds[0].State == "delivered" })
	if ds := h.deliveries(r.ID); ds[0].Status != "delivered, HTTP 204" {
		t.Errorf("status = %q", ds[0].Status)
	}

	// No change, no push, no call.
	if p := h.pushRemote(r.ID); p.Outcome != "up_to_date" {
		t.Fatalf("second push = %+v", p)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(rc.received()); n != 1 {
		t.Errorf("%d calls after a push that changed nothing", n)
	}
	// A rotated secret signs the next delivery; the old one no longer verifies.
	rot := h.must(h.owner, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: r.ID, RotateSecret: true}).(gitmirror.TriggerOut)
	if rot.Secret == "" || rot.Secret == out.Secret {
		t.Fatalf("rotated = %+v", rot)
	}
	res := h.must(h.owner, "git.trigger.test", gitmirror.RemoteBody{ProjectID: h.pid, RemoteID: r.ID}).(gitmirror.TriggerTestOut)
	if !res.OK || res.Status != "delivered, HTTP 204" {
		t.Errorf("test = %+v", res)
	}
	tr := rc.received()[1]
	if tr.Header.Get(webhook.HeaderEvent) != "git.ping" || webhook.Verify(out.Secret, tr.Header.Get(webhook.HeaderSignature), tr.Header.Get(webhook.HeaderTimestamp), tr.Body, h.clk.Now(), 5*time.Minute) ||
		!webhook.Verify(rot.Secret, tr.Header.Get(webhook.HeaderSignature), tr.Header.Get(webhook.HeaderTimestamp), tr.Body, h.clk.Now(), 5*time.Minute) {
		t.Errorf("the test delivery is not signed with the new secret alone")
	}
}

func TestATriggerRetriesWithBackoffUpToACapAndKeepsNoResponse(t *testing.T) {
	rc := newReceiver(t)
	rc.set(500, "RESPONSE-CANARY an internal stack trace")
	h := triggerHarness(t, rc, 2*time.Second)
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	h.setTrigger(r.ID, rc.url("/hook"))
	h.pushRemote(r.ID)
	waitFor(t, "the first attempt", func() bool { return len(rc.received()) == 1 })
	waitFor(t, "the record", func() bool { ds := h.deliveries(r.ID); return len(ds) == 1 && ds[0].Attempts == 1 })
	ds := h.deliveries(r.ID)
	if ds[0].State != "pending" || ds[0].Status != "failed: HTTP 500 (server_error); will retry" {
		t.Fatalf("after the first failure: %+v", ds)
	}
	id := rc.received()[0].Header.Get(webhook.HeaderID)

	// The waits follow the webhook schedule (10 s, 1 min, 5 min, 30 min, each within 20%, and the periodic check
	// that notices them runs every 30 s), and every attempt carries the same delivery id and its own number.
	for i, base := range []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute} {
		var waited time.Duration
		for len(rc.received()) == i+1 && waited < 2*time.Hour {
			h.clk.Advance(5 * time.Second)
			waited += 5 * time.Second
		}
		if n := len(rc.received()); n != i+2 {
			t.Fatalf("after attempt %d: %d attempts", i+1, n)
		}
		if lo, hi := base*8/10-5*time.Second, base*12/10+40*time.Second; waited < lo || waited > hi {
			t.Errorf("the wait after attempt %d was %v, want %v to %v", i+1, waited, lo, hi)
		}
	}
	for i, d := range rc.received() {
		if d.Header.Get(webhook.HeaderID) != id || d.Header.Get(webhook.HeaderAttempt) != string(rune('1'+i)) {
			t.Errorf("attempt %d: id %s attempt %s", i+1, d.Header.Get(webhook.HeaderID), d.Header.Get(webhook.HeaderAttempt))
		}
	}
	ds = h.deliveries(r.ID)
	if ds[0].State != "dead" || ds[0].Attempts != 5 || !strings.Contains(ds[0].Status, "gave up after 5 attempts") {
		t.Errorf("after the cap: %+v", ds)
	}
	// The cap is a cap: another day, another check, still five; and a receiver that has recovered is not sent the dead one.
	rc.set(204, "")
	h.clk.Advance(48 * time.Hour)
	h.svc.Check(t.Context())
	time.Sleep(50 * time.Millisecond)
	if n := len(rc.received()); n != 5 {
		t.Errorf("%d attempts in all", n)
	}
	// What the receiver said is nowhere.
	var all string
	_ = h.d.Read().QueryRow(`SELECT (SELECT group_concat(last_status) FROM git_trigger_deliveries) || (SELECT group_concat(last_status) FROM git_triggers) || (SELECT group_concat(detail_json) FROM audit_log)`).Scan(&all)
	if strings.Contains(all, "RESPONSE-CANARY") || strings.Contains(h.logs.String(), "RESPONSE-CANARY") || strings.Contains(h.logs.String(), "stack trace") {
		t.Errorf("a response body was kept: %s", all)
	}
}

func TestATriggerThatRecoversIsDeliveredOnceAndOneThatIsRefusedIsNotRetried(t *testing.T) {
	rc := newReceiver(t)
	rc.set(503, "")
	h := triggerHarness(t, rc, 2*time.Second)
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	h.setTrigger(r.ID, rc.url("/hook"))
	h.pushRemote(r.ID)
	waitFor(t, "the first attempt", func() bool { return len(rc.received()) == 1 })
	rc.set(200, "ok")
	h.clk.Advance(time.Hour)
	h.svc.Check(t.Context())
	waitFor(t, "delivery", func() bool { ds := h.deliveries(r.ID); return len(ds) == 1 && ds[0].State == "delivered" })
	if n := len(rc.received()); n != 2 {
		t.Errorf("%d attempts", n)
	}
	h.clk.Advance(time.Hour)
	h.svc.Check(t.Context())
	if n := len(rc.received()); n != 2 {
		t.Errorf("a delivered one was sent again: %d", n)
	}

	// A 4xx is the receiver saying no: one attempt, then dead. So is a redirect, which is never followed.
	for name, setup := range map[string]func(){"404": func() { rc.set(404, "") }, "redirect": func() { rc.mu.Lock(); rc.to = rc.url("/elsewhere"); rc.mu.Unlock() }} {
		before := len(rc.received())
		setup()
		h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-" + name + ".md", "chapter", name, 5, name + " words.\n"}))
		h.pushRemote(r.ID)
		waitFor(t, name+" attempt", func() bool { return len(rc.received()) == before+1 })
		waitFor(t, name+" record", func() bool { ds := h.deliveries(r.ID); return ds[len(ds)-1].State == "dead" })
		h.clk.Advance(48 * time.Hour)
		h.svc.Check(t.Context())
		if n := len(rc.received()); n != before+1 {
			t.Errorf("%s: %d attempts, want 1", name, n-before)
		}
		rc.mu.Lock()
		rc.to = ""
		rc.mu.Unlock()
	}
}

func TestATriggerNeedsAnAddressTheGuardAllowsAndTimesOut(t *testing.T) {
	rc := newReceiver(t)
	h := newHarness(t, func(o *gitmirror.Options) { o.AllowPrivate = false })
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	for _, bad := range []string{rc.url("/hook"), "http://ci.example.org/hook", "https://user:pw@ci.example.org/hook", "https://localhost/hook", "https://192.168.1.5/hook", "https://169.254.169.254/latest", "https://2130706433/hook", "ftp://ci.example.org/", "https://ci.example.org/#frag", ""} {
		if _, err := h.call(h.owner, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: r.ID, URL: &bad}); h.code(err) != ops.CodeInvalid {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := h.call(h.owner, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: r.ID}); h.code(err) != ops.CodeInvalid {
		t.Errorf("a trigger with no address: %v", err)
	}

	// A receiver that does not answer is cut off at the timeout.
	slow := newReceiver(t)
	slow.slow = 2 * time.Second
	h2 := triggerHarness(t, slow, 300*time.Millisecond)
	url2, _, _ := h2.bareRemote()
	r2 := h2.addRemote(url2)
	h2.setTrigger(r2.ID, slow.url("/hook"))
	start := time.Now()
	res := h2.must(h2.owner, "git.trigger.test", gitmirror.RemoteBody{ProjectID: h2.pid, RemoteID: r2.ID}).(gitmirror.TriggerTestOut)
	took := time.Since(start)
	if res.OK || res.Status != "failed: timeout" {
		t.Errorf("slow receiver: %+v after %v", res, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 1500*time.Millisecond {
			t.Errorf("slow receiver: cut off after %v", took)
		}
	})
}

func TestATriggerWaitsInTheTrashAndIsPausedByARestore(t *testing.T) {
	rc := newReceiver(t)
	h := triggerHarness(t, rc, 2*time.Second)
	url, _, _ := h.bareRemote()
	r := h.addRemote(url)
	h.setTrigger(r.ID, rc.url("/hook"))
	rc.set(500, "")
	h.pushRemote(r.ID)
	waitFor(t, "the first attempt", func() bool { return len(rc.received()) == 1 })
	waitFor(t, "pending", func() bool { ds := h.deliveries(r.ID); return len(ds) == 1 && ds[0].State == "pending" })
	rc.set(204, "")
	trashProject(t, h)
	h.clk.Advance(time.Hour)
	h.svc.Check(t.Context())
	time.Sleep(50 * time.Millisecond)
	if n := len(rc.received()); n != 1 {
		t.Fatalf("a trashed project's trigger fired: %d", n)
	}
	restoreProject(t, h)
	h.clk.Advance(2 * time.Hour)
	h.svc.Check(t.Context())
	waitFor(t, "delivery after the restore from the trash", func() bool { ds := h.deliveries(r.ID); return len(ds) == 1 && ds[0].State == "delivered" })

	// A restore from a backup pauses it and drops what waits.
	rc.set(500, "")
	n0 := len(rc.received())
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/03-more.md", "chapter", "More", 5, "More words.\n"}))
	h.pushRemote(r.ID)
	waitFor(t, "a pending delivery that has been tried once", func() bool {
		ds := h.deliveries(r.ID)
		return len(ds) == 2 && ds[1].State == "pending" && ds[1].Attempts == 1 && len(rc.received()) == n0+1
	})
	pausedByRestore(t, h)
	tr := h.must(h.owner, "git.trigger.get", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: r.ID}).(struct {
		Item *gitmirror.Trigger `json:"item,omitempty"`
	}).Item
	if tr == nil || tr.Enabled || tr.DisabledReason != gitmirror.PausedTriggerByRestore || tr.Pending != 0 {
		t.Fatalf("trigger after a restore = %+v", tr)
	}
	before := len(rc.received())
	rc.set(204, "")
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/04-more.md", "chapter", "More", 5, "Even more words.\n"}))
	h.must(h.owner, "git.remote.update", gitmirror.UpdateIn{ProjectID: h.pid, RemoteID: r.ID, Enabled: ptr(true)})
	h.pushRemote(r.ID)
	time.Sleep(100 * time.Millisecond)
	if len(rc.received()) != before {
		t.Errorf("a paused trigger fired")
	}
	h.must(h.owner, "git.trigger.set", gitmirror.TriggerSetIn{ProjectID: h.pid, RemoteID: r.ID, Enabled: ptr(true)})
	h.src.Write("The Salt Road", true, append(append([]mdoc{}, chapters...), mdoc{"manuscript/05-more.md", "chapter", "More", 5, "Yet more words.\n"}))
	h.pushRemote(r.ID)
	waitFor(t, "a delivery once switched on", func() bool { return len(rc.received()) == before+1 })
}

func TestOnlyTheRemoteWithATriggerCallsOneAndDeletingTheRemoteTakesItAway(t *testing.T) {
	rc := newReceiver(t)
	h := triggerHarness(t, rc, 2*time.Second)
	u1, _, _ := h.bareRemote()
	u2, _, _ := h.bareRemote()
	a := h.addRemote(u1, func(in *gitmirror.CreateIn) { in.Name = "A" })
	b := h.addRemote(u2, func(in *gitmirror.CreateIn) { in.Name = "B" })
	h.setTrigger(a.ID, rc.url("/a"))
	h.pushRemote(b.ID)
	time.Sleep(100 * time.Millisecond)
	if n := len(rc.received()); n != 0 {
		t.Fatalf("a remote with no trigger called one: %d", n)
	}
	h.pushRemote(a.ID)
	waitFor(t, "A's delivery", func() bool { return len(rc.received()) == 1 })
	h.must(h.owner, "git.remote.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: a.ID})
	var n int
	_ = h.d.Read().QueryRow(`SELECT (SELECT count(*) FROM git_triggers) + (SELECT count(*) FROM git_trigger_deliveries)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d trigger rows left after the remote was deleted", n)
	}
	if _, err := h.call(h.owner, "git.trigger.delete", gitmirror.RemoteQuery{ProjectID: h.pid, RemoteID: b.ID}); h.code(err) != ops.CodeNotFound {
		t.Errorf("deleting a trigger that is not there: %v", err)
	}
}

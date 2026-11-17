package webhook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/webhook"
)

func wantCode(t *testing.T, err error, code ops.Code, what string) *ops.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: no error, want %s", what, code)
	}
	e := ops.AsError(err)
	if e.Code != code {
		t.Fatalf("%s: %s (%s), want %s", what, e.Code, e.Message, code)
	}
	return e
}

func TestCreateShowsTheSecretOnceAndNoOpReturnsItAgain(t *testing.T) {
	w := newWorld(t)
	secret := w.secret
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("secret %q", secret)
	}
	var all []any
	all = append(all,
		w.mustCall("webhook.list", webhook.ProjectIn{ProjectID: w.project}),
		w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}),
		w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Name: ptr("renamed")}),
		w.mustCall("webhook.deliveries", webhook.DeliveriesIn{ProjectID: w.project, WebhookID: w.hookID}))
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	out := w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})
	all = append(all, out, w.mustCall("webhook.deliveries", webhook.DeliveriesIn{ProjectID: w.project, WebhookID: w.hookID}))
	for i, v := range all {
		b, _ := json.Marshal(v)
		if strings.Contains(string(b), secret) || strings.Contains(string(b), strings.TrimPrefix(secret, "whsec_")) {
			t.Errorf("result %d carries the secret: %s", i, b)
		}
	}
	// The op's own schema marks the response as secret, so an idempotent replay never stores it.
	for _, n := range []string{"webhook.create", "webhook.rotate_secret"} {
		e, _ := w.reg.Lookup(n)
		if !e.Op.SecretOutput || !e.Op.NoDryRun {
			t.Errorf("%s: SecretOutput=%v NoDryRun=%v", n, e.Op.SecretOutput, e.Op.NoDryRun)
		}
	}
	// Nothing about it reaches the audit log: not the secret, not the address.
	rows, err := w.d.Read().Query(`SELECT action, detail_json FROM audit_log WHERE action LIKE 'webhook.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var action, detail string
		_ = rows.Scan(&action, &detail)
		n++
		if strings.Contains(detail, secret) || strings.Contains(detail, "127.0.0.1") || strings.Contains(detail, "/hook") {
			t.Errorf("%s audit entry: %s", action, detail)
		}
	}
	if n < 3 {
		t.Errorf("%d audit entries for create, update and test", n)
	}
}

func TestCreateValidation(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.noHook = true; o.lim.AllowPrivate = false })
	ok := []string{"document.updated"}
	create := func(in webhook.CreateIn) error {
		in.ProjectID = w.project
		_, err := w.call("webhook.create", in)
		return err
	}
	e := wantCode(t, create(webhook.CreateIn{Name: "x", URL: "http://127.0.0.1/x", Events: ok}), ops.CodeInvalid, "internal address")
	if e.Field != "url" {
		t.Errorf("the field is %q", e.Field)
	}
	for _, c := range []webhook.CreateIn{
		{Name: "", URL: "https://example.com/", Events: ok},
		{Name: strings.Repeat("n", 81), URL: "https://example.com/", Events: ok},
		{Name: "x", URL: "https://example.com/", Events: nil},
		{Name: "x", URL: "https://example.com/", Events: []string{"document.deleted"}},
		{Name: "x", URL: "https://example.com/", Events: []string{"*"}},
		{Name: "x", URL: "", Events: ok},
		{Name: "x", URL: "https://u:p@example.com/", Events: ok},
		{Name: "x", URL: "https://2130706433/", Events: ok},
	} {
		wantCode(t, create(c), ops.CodeInvalid, fmt.Sprintf("%+v", c))
	}
	// Events are stored in one fixed order and once.
	out := w.mustCall("webhook.create", webhook.CreateIn{ProjectID: w.project, Name: " ok ", URL: "HTTPS://Example.com/Hook", Events: []string{"export.completed", "document.updated", "document.updated"}}).(webhook.CreateOut)
	if strings.Join(out.Item.Events, ",") != "document.updated,export.completed" || out.Item.Name != "ok" || out.Item.URL != "https://example.com" || !out.Item.URLPathSet || out.Item.IncludeText {
		t.Errorf("created: %+v", out.Item)
	}
	if !out.Item.Enabled || out.Item.SecretHint == "" || out.Item.SecretHint == out.Secret {
		t.Errorf("created: %+v", out.Item)
	}
}

func TestCapsAndSwitches(t *testing.T) {
	w := newWorld(t, func(o *worldOpts) { o.noHook = true; o.lim.MaxPerProject = 2; o.lim.MaxTotal = 3 })
	for i := 0; i < 2; i++ {
		w.create(fmt.Sprintf("h%d", i), []string{"document.updated"}, false)
	}
	_, err := w.call("webhook.create", webhook.CreateIn{ProjectID: w.project, Name: "third", URL: w.recv.URL, Events: []string{"document.updated"}})
	wantCode(t, err, ops.CodeConflict, "the per-project cap")
	other := w.mustCall("project.create", projects.CreateIn{Title: "Other"}).(projects.Item).Item.ID
	w.mustCall("webhook.create", webhook.CreateIn{ProjectID: other, Name: "elsewhere", URL: w.recv.URL, Events: []string{"document.updated"}})
	other2 := w.mustCall("project.create", projects.CreateIn{Title: "Other 2"}).(projects.Item).Item.ID
	_, err = w.call("webhook.create", webhook.CreateIn{ProjectID: other2, Name: "elsewhere", URL: w.recv.URL, Events: []string{"document.updated"}})
	wantCode(t, err, ops.CodeConflict, "the instance cap")
	// Deleting one makes room.
	w.mustCall("webhook.delete", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})
	w.mustCall("webhook.create", webhook.CreateIn{ProjectID: w.project, Name: "again", URL: w.recv.URL, Events: []string{"document.updated"}})

	// Webhooks switched off, and a server with no secret key.
	off := newWorld(t, func(o *worldOpts) { o.noHook = true; o.lim.Off = true })
	_, err = off.call("webhook.create", webhook.CreateIn{ProjectID: off.project, Name: "x", URL: off.recv.URL, Events: []string{"document.updated"}})
	wantCode(t, err, ops.CodeConflict, "webhooks off")
	l := off.mustCall("webhook.list", webhook.ProjectIn{ProjectID: off.project}).(webhook.List)
	if l.Enabled || l.Reason == "" {
		t.Errorf("list when off: %+v", l)
	}
	nokey := newWorld(t, func(o *worldOpts) { o.noHook = true; o.noKey = true })
	_, err = nokey.call("webhook.create", webhook.CreateIn{ProjectID: nokey.project, Name: "x", URL: nokey.recv.URL, Events: []string{"document.updated"}})
	wantCode(t, err, ops.CodeConflict, "no secret key")
	if l := nokey.mustCall("webhook.list", webhook.ProjectIn{ProjectID: nokey.project}).(webhook.List); l.Enabled {
		t.Errorf("no key, but list says enabled: %+v", l)
	}
}

func TestUpdateGetDeleteAreScopedToTheProject(t *testing.T) {
	w := newWorld(t)
	other := w.mustCall("project.create", projects.CreateIn{Title: "Other"}).(projects.Item).Item.ID
	for _, name := range []string{"webhook.get", "webhook.delete", "webhook.rotate_secret", "webhook.test", "webhook.deliveries"} {
		var in any = webhook.HookIn{ProjectID: other, WebhookID: w.hookID}
		switch name {
		case "webhook.rotate_secret":
			in = webhook.RotateIn{ProjectID: other, WebhookID: w.hookID}
		case "webhook.deliveries":
			in = webhook.DeliveriesIn{ProjectID: other, WebhookID: w.hookID}
		}
		_, err := w.call(name, in)
		wantCode(t, err, ops.CodeNotFound, name+" with another project's id")
	}
	_, err := w.call("webhook.update", webhook.UpdateIn{ProjectID: other, WebhookID: w.hookID, Name: ptr("hijacked")})
	wantCode(t, err, ops.CodeNotFound, "update")
	_, err = w.call("webhook.redeliver", webhook.RedeliverIn{ProjectID: other, WebhookID: w.hookID, DeliveryID: "whd_x"})
	wantCode(t, err, ops.CodeNotFound, "redeliver")
	h := w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if h.Name != "main" {
		t.Errorf("it was changed: %+v", h)
	}
}

func TestUpdate(t *testing.T) {
	w := newWorld(t)
	u := func(in webhook.UpdateIn) (webhook.Webhook, error) {
		in.ProjectID, in.WebhookID = w.project, w.hookID
		out, err := w.call("webhook.update", in)
		if err != nil {
			return webhook.Webhook{}, err
		}
		return out.(webhook.Item).Item, nil
	}
	h, err := u(webhook.UpdateIn{Name: ptr("renamed"), Events: &[]string{"comment.created"}, IncludeText: ptr(true)})
	if err != nil || h.Name != "renamed" || len(h.Events) != 1 || !h.IncludeText {
		t.Fatalf("%+v %v", h, err)
	}
	if _, err := u(webhook.UpdateIn{URL: ptr("http://169.254.169.254/latest/")}); err == nil {
		t.Error("an internal address was accepted on update")
	}
	if _, err := u(webhook.UpdateIn{Events: &[]string{}}); err == nil {
		t.Error("no events was accepted")
	}
	if _, err := u(webhook.UpdateIn{Name: ptr(" ")}); err == nil {
		t.Error("an empty name was accepted")
	}
	h, err = u(webhook.UpdateIn{})
	if err != nil || h.Name != "renamed" {
		t.Errorf("an empty update: %+v %v", h, err)
	}
	// A changed address starts the failure count again.
	if _, err := w.d.Writer().Exec(`UPDATE webhooks SET failure_streak = 7 WHERE id = ?`, w.hookID); err != nil {
		t.Fatal(err)
	}
	h, err = u(webhook.UpdateIn{URL: ptr(w.recv.URL + "/new")})
	if err != nil || h.FailureStreak != 0 || h.URL != w.recv.URL || !h.URLPathSet {
		t.Errorf("%+v %v", h, err)
	}
}

func TestDeleteTakesTheDeliveriesWithIt(t *testing.T) {
	w := newWorld(t)
	w.recv.status.Store(500)
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	if w.count(`SELECT count(*) FROM webhook_deliveries`) != 1 {
		t.Fatal("setup")
	}
	w.mustCall("webhook.delete", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})
	if w.count(`SELECT count(*) FROM webhook_deliveries`) != 0 || w.count(`SELECT count(*) FROM webhooks`) != 0 {
		t.Error("rows survived")
	}
	_, err := w.call("webhook.delete", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})
	wantCode(t, err, ops.CodeNotFound, "deleting twice")
}

func TestTestPing(t *testing.T) {
	w := newWorld(t)
	out := w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.TestOut)
	if !out.OK || out.Delivery.Event != "ping" || out.Delivery.State != "delivered" || out.Delivery.Status != 200 {
		t.Fatalf("%+v", out)
	}
	r := w.recv.requests()
	if len(r) != 1 || r[0].Header.Get(webhook.HeaderEvent) != "ping" || !webhook.Verify(w.secret, r[0].Header.Get(webhook.HeaderSignature), r[0].Header.Get(webhook.HeaderTimestamp), r[0].Body, w.clk.Now(), time.Minute) {
		t.Fatalf("the ping is not a signed ping: %v", r)
	}
	// At most one every five seconds.
	_, err := w.call("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID})
	e := wantCode(t, err, ops.CodeRateLimited, "a second ping")
	if e.RetryAfterSeconds != 5 {
		t.Errorf("retry after %d", e.RetryAfterSeconds)
	}
	w.clk.Advance(6 * time.Second)
	w.recv.status.Store(500)
	out = w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.TestOut)
	if out.OK || out.Delivery.State != "dead" || out.Delivery.Attempts != 1 || out.Delivery.Error != webhook.ErrClassServer {
		t.Errorf("a failing ping is final: %+v", out)
	}
	// A ping that fails does not count against the webhook.
	h := w.mustCall("webhook.get", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.Item).Item
	if h.FailureStreak != 0 || !h.Enabled {
		t.Errorf("%+v", h)
	}
	// The ping can be tried even while the webhook is switched off, and cannot be queued again.
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Enabled: ptr(false)})
	w.clk.Advance(6 * time.Second)
	w.recv.status.Store(200)
	if out := w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.TestOut); !out.OK {
		t.Errorf("a switched-off webhook could not be tested: %+v", out)
	}
	_, err = w.call("webhook.redeliver", webhook.RedeliverIn{ProjectID: w.project, WebhookID: w.hookID, DeliveryID: out.Delivery.ID})
	wantCode(t, err, ops.CodeConflict, "redelivering a ping")
	// The ping failing to reach an internal address says so by kind and nothing else.
	w.mustCall("webhook.update", webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, URL: ptr("https://203.0.113.9:1/x")})
	w.clk.Advance(6 * time.Second)
	b, _ := json.Marshal(w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}))
	if strings.Contains(string(b), "203.0.113") {
		t.Errorf("the result names the address: %s", b)
	}
}

func TestDeliveriesPagesAndFilters(t *testing.T) {
	w := newWorld(t)
	for i := 0; i < 7; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_%d", i)}})
	}
	w.pump()
	w.recv.status.Store(404)
	for i := 0; i < 3; i++ {
		w.emit(ops.Event{Type: "comment.created", ProjectID: w.project, Payload: map[string]any{"comment_id": fmt.Sprintf("cmt_d%d", i)}})
	}
	w.pump()
	if n := len(w.deliveries("delivered")); n != 7 {
		t.Errorf("%d delivered", n)
	}
	if n := len(w.deliveries("dead")); n != 3 {
		t.Errorf("%d dead", n)
	}
	var seen []string
	cursor := ""
	for page := 0; page < 10; page++ {
		out := w.mustCall("webhook.deliveries", webhook.DeliveriesIn{ProjectID: w.project, WebhookID: w.hookID, Limit: 4, Before: cursor}).(webhook.DeliveriesOut)
		for _, d := range out.Items {
			seen = append(seen, d.ID)
		}
		if out.NextCursor == "" {
			break
		}
		cursor = out.NextCursor
	}
	if len(seen) != 10 {
		t.Errorf("paging saw %d of 10", len(seen))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Errorf("not newest first: %v", seen)
			break
		}
	}
	for _, bad := range []webhook.DeliveriesIn{{State: "nope"}, {Before: "x; DROP TABLE webhooks"}, {Before: strings.Repeat("whd_", 30)}} {
		bad.ProjectID, bad.WebhookID = w.project, w.hookID
		wantCode(t, func() error { _, err := w.call("webhook.deliveries", bad); return err }(), ops.CodeInvalid, fmt.Sprintf("%+v", bad))
	}
	if out := w.mustCall("webhook.deliveries", webhook.DeliveriesIn{ProjectID: w.project, WebhookID: w.hookID, Limit: 100000}).(webhook.DeliveriesOut); len(out.Items) != 10 {
		t.Errorf("a huge limit returned %d", len(out.Items))
	}
	// A listing of dead letters carries a kind and a short excerpt, no payload and no body.
	b, _ := json.Marshal(w.deliveries("dead"))
	if strings.Contains(string(b), "payload") || strings.Contains(string(b), "cmt_d") {
		t.Errorf("the dead-letter list carries the payload: %s", b)
	}
}

func TestRedeliver(t *testing.T) {
	w := newWorld(t)
	w.recv.status.Store(404)
	w.emit(docUpdated(w.project, "doc_1", 1))
	w.pump()
	d := w.deliveries("dead")
	if len(d) != 1 {
		t.Fatal("setup")
	}
	w.recv.status.Store(200)
	out := w.mustCall("webhook.redeliver", webhook.RedeliverIn{ProjectID: w.project, WebhookID: w.hookID, DeliveryID: d[0].ID}).(webhook.DeliveryItem)
	if out.Item.State != "pending" || out.Item.Attempts != 0 || out.Item.Error != "" {
		t.Errorf("%+v", out.Item)
	}
	_, err := w.call("webhook.redeliver", webhook.RedeliverIn{ProjectID: w.project, WebhookID: w.hookID, DeliveryID: d[0].ID})
	wantCode(t, err, ops.CodeConflict, "redelivering a waiting delivery")
	w.pump()
	got := w.recv.requests()
	if len(got) != 2 || got[0].Header.Get(webhook.HeaderID) != got[1].Header.Get(webhook.HeaderID) || got[1].Header.Get(webhook.HeaderID) != d[0].ID {
		t.Fatalf("the replay must carry the original id: %v", got)
	}
	if n := len(w.deliveries("delivered")); n != 1 {
		t.Errorf("%d delivered", n)
	}
	// A delivered one can be replayed too.
	w.mustCall("webhook.redeliver", webhook.RedeliverIn{ProjectID: w.project, WebhookID: w.hookID, DeliveryID: d[0].ID})
	w.pump()
	if len(w.recv.requests()) != 3 {
		t.Errorf("%d requests", len(w.recv.requests()))
	}
	_, err = w.call("webhook.redeliver", webhook.RedeliverIn{ProjectID: w.project, WebhookID: w.hookID, DeliveryID: "whd_nothing"})
	wantCode(t, err, ops.CodeNotFound, "an unknown delivery")
}

// Only the owner reaches any of it: a reader, a reviewer and an editor are told no, and a stranger is told nothing exists.
func TestOnlyTheOwnerManagesWebhooks(t *testing.T) {
	w := newWorld(t)
	member := func(email, role string) ops.Principal {
		id := testutil.User(t, w.d, ops.RoleWriter, email)
		if _, err := w.d.Writer().Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, w.project, id, role, db.Now()); err != nil {
			t.Fatal(err)
		}
		return ops.Principal{UserID: id, Role: ops.RoleWriter}
	}
	stranger := ops.Principal{UserID: testutil.User(t, w.d, ops.RoleWriter, "stranger@t.local"), Role: ops.RoleWriter}
	deliveryID := w.mustCall("webhook.test", webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID}).(webhook.TestOut).Delivery.ID
	inputs := map[string]any{
		"webhook.create":        webhook.CreateIn{ProjectID: w.project, Name: "x", URL: w.recv.URL, Events: []string{"document.updated"}},
		"webhook.list":          webhook.ProjectIn{ProjectID: w.project},
		"webhook.get":           webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID},
		"webhook.update":        webhook.UpdateIn{ProjectID: w.project, WebhookID: w.hookID, Name: ptr("x")},
		"webhook.delete":        webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID},
		"webhook.rotate_secret": webhook.RotateIn{ProjectID: w.project, WebhookID: w.hookID},
		"webhook.test":          webhook.HookIn{ProjectID: w.project, WebhookID: w.hookID},
		"webhook.deliveries":    webhook.DeliveriesIn{ProjectID: w.project, WebhookID: w.hookID},
		"webhook.redeliver":     webhook.RedeliverIn{ProjectID: w.project, WebhookID: w.hookID, DeliveryID: deliveryID},
	}
	for _, role := range []string{ops.ProjectReader, ops.ProjectReviewer, ops.ProjectEditor} {
		p := member(role+"@t.local", role)
		for name, in := range inputs {
			_, err := w.reg.Call(context.Background(), p, name, in)
			wantCode(t, err, ops.CodeForbidden, name+" as "+role)
		}
	}
	for name, in := range inputs {
		_, err := w.reg.Call(context.Background(), stranger, name, in)
		wantCode(t, err, ops.CodeNotFound, name+" as a stranger")
	}
	// A key needs the admin scope, and a read key cannot even list.
	for _, scope := range []string{ops.ScopeRead, ops.ScopeWrite} {
		k := w.owner
		k.Scope, k.IsAgent, k.Via = scope, true, "apikey"
		for name, in := range inputs {
			_, err := w.reg.Call(context.Background(), k, name, in)
			wantCode(t, err, ops.CodeForbidden, name+" with a "+scope+" key")
		}
	}
	k := w.owner
	k.Scope, k.IsAgent, k.Via = ops.ScopeAdmin, true, "apikey"
	if _, err := w.reg.Call(context.Background(), k, "webhook.list", inputs["webhook.list"]); err != nil {
		t.Errorf("an admin key: %v", err)
	}
	// ... and a key pinned elsewhere cannot.
	k.ProjectID = "prj_elsewhere"
	_, err := w.reg.Call(context.Background(), k, "webhook.list", inputs["webhook.list"])
	wantCode(t, err, ops.CodeNotFound, "a key pinned to another project")
}

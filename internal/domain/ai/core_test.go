package ai_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// The promise that the preview is what the provider receives, byte for byte.
func TestThePreviewIsWhatTheProviderReceives(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Tighten the first paragraph.")
	pv := e.preview(e.editor, in)
	if !pv.Decision.Allowed || len(pv.Attempts) != 1 || pv.PayloadHash == "" || pv.Nonce == "" {
		t.Fatalf("preview: %+v", pv.Decision)
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	out := e.call(e.editor, "ai.run", in).(aicore.RunItem).Item
	if out.Status != "ok" || out.Text != "A suggestion." || out.RunID == "" {
		t.Fatalf("run: %+v", out)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("provider saw %d requests", len(reqs))
	}
	if string(reqs[0].Body) != pv.Attempts[0].Body {
		t.Fatalf("the provider received different bytes than the preview showed:\nsent:    %s\nvisible: %s", reqs[0].Body, pv.Attempts[0].Body)
	}
	// And the neutral payload the person reads is the same messages.
	var wire struct {
		Messages []lib.Message `json:"messages"`
	}
	if err := json.Unmarshal(reqs[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != len(pv.Payload.Messages) {
		t.Fatalf("messages differ: %d vs %d", len(wire.Messages), len(pv.Payload.Messages))
	}
	for i := range wire.Messages {
		if wire.Messages[i] != pv.Payload.Messages[i] {
			t.Errorf("message %d differs", i)
		}
	}
	if reqs[0].Header.Get("Authorization") != "Bearer "+planted {
		t.Error("the key must travel in the Authorization header")
	}
}

func TestARunNeedsThePreviewsHashAndRefusesAStaleOne(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Summarise.")
	// No hash: refused, nothing sent.
	_, err := e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeInvalid)
	pv := e.preview(e.editor, in)
	// The text changes between the preview and the run: 409, nothing sent.
	e.updateDoc(e.doc, "Entirely new text.")
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	_, err = e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeConflict)
	if err.Data["ai_code"] != "payload_changed" {
		t.Errorf("data %v", err.Data)
	}
	// A different nonce changes the bytes too.
	pv2 := e.preview(e.editor, in)
	in.PayloadHash = pv2.PayloadHash
	in.Nonce = lib.NewNonce()
	_, err = e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeConflict)
	if n := len(f.requests()); n != 0 {
		t.Fatalf("a refused run sent %d requests", n)
	}
	// With the right pair it goes through.
	in.Nonce, in.PayloadHash = pv2.Nonce, pv2.PayloadHash
	if _, err := e.try(e.editor, "ai.run", in); err != nil {
		t.Fatal(err)
	}
}

func TestPolicyIsEnforcedOnTheServer(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	pid := e.addProvider(f, "Main", false)
	in := e.freeform("Hello")

	// Off by default: the preview says so, the run refuses, nothing is sent.
	pv := e.preview(e.editor, in)
	if pv.Decision.Allowed || pv.Decision.Code != "policy_off" || pv.Payload != nil {
		t.Fatalf("decision %+v", pv.Decision)
	}
	in.Nonce, in.PayloadHash = "00000000000000000000000000000000", strings.Repeat("a", 64)
	_, err := e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeForbidden)
	if err.Data["ai_code"] != "policy_off" {
		t.Errorf("%v", err.Data)
	}

	// Only the owner sets the policy.
	mode := "ask"
	_, err = e.try(e.editor, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
	wantCode(t, err, ops.CodeForbidden)

	// An action not allowed is refused.
	e.enable("ask", func(in *aicore.PolicySetIn) { a := []string{"critique"}; in.Actions = &a })
	pv = e.preview(e.editor, e.freeform("Hello"))
	if pv.Decision.Code != "action_not_allowed" {
		t.Fatalf("decision %+v", pv.Decision)
	}

	// A provider the policy does not list is not used.
	other := e.addProvider(newFake(t, nil), "Other", false)
	e.enable("ask", func(in *aicore.PolicySetIn) {
		a := []string{"chat"}
		p := []string{other}
		in.Actions, in.Providers = &a, &p
	})
	pv = e.preview(e.editor, e.freeform("Hello"))
	if !pv.Decision.Allowed || pv.Attempts[0].ProviderID != other {
		t.Fatalf("attempts %+v", pv.Attempts)
	}
	found := false
	for _, s := range pv.SkippedProviders {
		found = found || (s.ProviderID == pid && s.Reason == "not_allowed")
	}
	if !found {
		t.Errorf("the unlisted provider should be reported as skipped: %+v", pv.SkippedProviders)
	}
	if len(f.requests()) != 0 {
		t.Fatal("a refusal reached a provider")
	}
}

func TestLocalOnlyRefusesAProviderThatIsNotLocal(t *testing.T) {
	e := newEnv(t, nil)
	remote := newFake(t, nil)
	e.addProvider(remote, "Hosted", false)
	e.enable("local_only", nil)
	pv := e.preview(e.editor, e.freeform("Hello"))
	if pv.Decision.Allowed || pv.Decision.Code != "no_provider" || !strings.Contains(pv.Decision.Message, "local-only") {
		t.Fatalf("decision %+v", pv.Decision)
	}
	if len(pv.SkippedProviders) != 1 || pv.SkippedProviders[0].Reason != "not_local" {
		t.Fatalf("skipped %+v", pv.SkippedProviders)
	}
	// A provider an administrator marked local is used.
	e.loopbackLocal.Store(true) // the fakes now sit on "the LAN"
	localSrv := newFake(t, nil)
	lid := e.addProvider(localSrv, "LAN Ollama", true)
	in := e.freeform("Hello")
	pv = e.preview(e.editor, in)
	if !pv.Decision.Allowed || pv.Attempts[0].ProviderID != lid || !pv.Attempts[0].Local || !pv.Cost.Local {
		t.Fatalf("decision %+v attempts %+v", pv.Decision, pv.Attempts)
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	if _, err := e.try(e.editor, "ai.run", in); err != nil {
		t.Fatal(err)
	}
	if len(remote.requests()) != 0 {
		t.Fatal("a local-only project reached a hosted provider")
	}
}

// The flag is not trusted: a provider declared local that connects to a public
// address is refused at connect time in a local-only project.
func TestLocalOnlyChecksTheAddressAtSendTime(t *testing.T) {
	e := newEnv(t, nil)
	srv := newFake(t, nil)
	e.addProvider(srv, "Claims to be local", true) // declared local, but the guard sees a public address
	e.enable("local_only", nil)
	in := e.freeform("Hello")
	pv := e.preview(e.editor, in)
	if !pv.Decision.Allowed {
		t.Fatalf("%+v", pv.Decision)
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	_, err := e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeProvider)
	if err.Data["provider_class"] != lib.ClassBlocked {
		t.Fatalf("class %v", err.Data)
	}
	if len(srv.requests()) != 0 {
		t.Fatal("the connection should have been refused before any byte was sent")
	}
}

func TestAgentsNeedTheOwnersPermission(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, nil)
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	agent := e.owner
	agent.Via, agent.Scope = "apikey", ops.ScopeWrite
	pv := e.preview(agent, e.freeform("Hello"))
	if pv.Decision.Code != "agents_not_allowed" {
		t.Fatalf("decision %+v", pv.Decision)
	}
	e.enable("ask", func(in *aicore.PolicySetIn) { b := true; in.AgentsMayUse = &b })
	pv = e.preview(agent, e.freeform("Hello"))
	if !pv.Decision.Allowed {
		t.Fatalf("decision %+v", pv.Decision)
	}
	// A read-scope key cannot run AI at all.
	read := agent
	read.Scope = ops.ScopeRead
	_, err := e.try(read, "ai.context.preview", e.freeform("Hello"))
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.try(read, "ai.run", e.freeform("Hello"))
	wantCode(t, err, ops.CodeForbidden)
}

func TestReadersAndOutsidersCannotSpendTheOwnersMoney(t *testing.T) {
	e := newEnv(t, nil)
	e.addProvider(newFake(t, nil), "Main", false)
	e.enable("ask", nil)
	_, err := e.try(e.reader, "ai.context.preview", e.freeform("Hello"))
	wantCode(t, err, ops.CodeForbidden)
	_, err = e.try(e.other, "ai.context.preview", e.freeform("Hello"))
	wantCode(t, err, ops.CodeNotFound)
	_, err = e.try(e.other, "ai.run", e.freeform("Hello"))
	wantCode(t, err, ops.CodeNotFound)
}

func TestTheKillSwitchRefusesEverything(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.Off = true })
	f := newFake(t, nil)
	for _, c := range []struct {
		p    ops.Principal
		name string
		in   any
	}{
		{e.admin, "ai.provider.list", struct{}{}},
		{e.admin, "ai.provider.create", aicore.ProviderCreateIn{Name: "x", Kind: lib.KindOllama, BaseURL: f.srv.URL}},
		{e.owner, "ai.policy.get", aicore.PolicyGetIn{ProjectID: e.project}},
		{e.owner, "ai.context.preview", e.freeform("Hello")},
		{e.owner, "ai.recipe.list", struct{}{}},
	} {
		_, err := e.try(c.p, c.name, c.in)
		if err == nil || err.Code != ops.CodeConflict || !strings.Contains(err.Message, "SCRIOBH_AI=off") {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// Status still answers, and says why.
	st := e.call(e.owner, "ai.status", struct{}{}).(aicore.StatusOut)
	if st.Enabled || st.Reason != "env" {
		t.Errorf("status %+v", st)
	}
	// The administrator's setting cannot turn it back on.
	yes := true
	_, err := e.try(e.admin, "ai.settings.set", aicore.SettingsSetIn{Enabled: &yes})
	wantCode(t, err, ops.CodeConflict)
	if len(f.requests()) != 0 {
		t.Fatal("a provider was contacted")
	}
}

func TestTheAdminSwitchTurnsAIOffAndOn(t *testing.T) {
	e := newEnv(t, nil)
	e.addProvider(newFake(t, nil), "Main", false)
	e.enable("ask", nil)
	no := false
	e.call(e.admin, "ai.settings.set", aicore.SettingsSetIn{Enabled: &no})
	_, err := e.try(e.owner, "ai.context.preview", e.freeform("Hello"))
	wantCode(t, err, ops.CodeConflict)
	st := e.call(e.owner, "ai.status", struct{}{}).(aicore.StatusOut)
	if st.Enabled || st.Reason != "admin" {
		t.Errorf("status %+v", st)
	}
	yes := true
	e.call(e.admin, "ai.settings.set", aicore.SettingsSetIn{Enabled: &yes})
	if pv := e.preview(e.owner, e.freeform("Hello")); !pv.Decision.Allowed {
		t.Errorf("%+v", pv.Decision)
	}
}

func TestTheRunLogHoldsNoText(t *testing.T) {
	e := newEnv(t, nil)
	marker := "ZEBRA-MANUSCRIPT-MARKER"
	e.updateDoc(e.doc, "Some prose with "+marker+" inside it.")
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte, n int) {
		oaiReply(w, r, body, "COMPLETION-MARKER-XYZ")
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	out, err := e.run(e.owner, e.freeform("Do the thing."))
	if err != nil || out.Text != "COMPLETION-MARKER-XYZ" {
		t.Fatalf("%v %+v", err, out)
	}
	// The manuscript marker is in the document, and nowhere that records AI use.
	for _, tb := range []string{"ai_runs", "ai_usage_daily", "audit_log", "events", "ai_policies"} {
		var b strings.Builder
		rows, qerr := e.d.Read().Query(`SELECT * FROM ` + tb)
		if qerr != nil {
			t.Fatal(qerr)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			for _, v := range vals {
				if bs, ok := v.([]byte); ok {
					b.Write(bs)
				} else {
					b.WriteString(toString(v))
				}
			}
		}
		rows.Close()
		if strings.Contains(b.String(), marker) || strings.Contains(b.String(), "COMPLETION-MARKER") {
			t.Errorf("%s holds manuscript or completion text", tb)
		}
	}
	runs := e.call(e.owner, "ai.run.list", aicore.RunListIn{ProjectID: e.project}).(aicore.RunListOut)
	if len(runs.Items) != 1 || runs.Items[0].Status != "ok" || runs.Items[0].InputTokens != 120 || runs.Items[0].OutputTokens != 30 || !runs.Items[0].Reported {
		t.Fatalf("run log %+v", runs.Items)
	}
	if runs.Items[0].PayloadHash == "" || runs.Items[0].OutputHash == "" {
		t.Errorf("hashes missing: %+v", runs.Items[0])
	}
	// An audit entry and an event, with ids and counts.
	if n := e.count(`SELECT count(*) FROM audit_log WHERE action = 'ai.run'`); n != 1 {
		t.Errorf("audit entries %d", n)
	}
	var payload string
	if err := e.d.Read().QueryRow(`SELECT payload_json FROM events WHERE type = 'ai.run.finished'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(payload), &p)
	for k := range p {
		if k != "run_id" && k != "status" && k != "input_tokens" && k != "output_tokens" && k != "cost_micros" {
			t.Errorf("unexpected key %q in the event payload", k)
		}
	}
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v) + " "
}

func TestNothingIsWrittenToTheManuscript(t *testing.T) {
	e := newEnv(t, nil)
	e.addProvider(newFake(t, nil), "Main", false)
	e.enable("ask", nil)
	tables := `SELECT group_concat(content || revision || content_hash || updated_at, '|') FROM documents`
	var before, after string
	_ = e.d.Read().QueryRow(tables).Scan(&before)
	revs := e.count(`SELECT count(*) FROM document_revisions`)
	for i := 0; i < 3; i++ {
		if _, err := e.run(e.owner, e.freeform("Rewrite everything.")); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.d.Read().QueryRow(tables).Scan(&after)
	if before != after || e.count(`SELECT count(*) FROM document_revisions`) != revs {
		t.Fatal("an AI run changed the manuscript or its history")
	}
}

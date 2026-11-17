package ai_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// A provider that reports exactly the usage given, so the books are predictable.
func usageFake(t *testing.T, in, out int) *fake {
	return newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, in, out)
	})
}

func (e *env) setBudget(scope, id string, cost, tokens int64) {
	e.t.Helper()
	e.call(e.admin, "ai.budget.set", aicore.BudgetSetIn{Scope: scope, ScopeID: id, MaxCostMicros: cost, MaxTokens: tokens})
}

func (e *env) spent() (tokens int64, cost int64) {
	_ = e.d.Read().QueryRow(`SELECT COALESCE(SUM(input_tokens + output_tokens), 0), COALESCE(SUM(cost_micros), 0) FROM ai_usage_daily`).Scan(&tokens, &cost)
	return
}

// The runaway loop: a thousand requests stop at the cap, and the books balance.
func TestAMonthlyCapStopsARunawayLoop(t *testing.T) {
	e := newEnv(t, nil)
	f := usageFake(t, 100, 50) // 150 tokens a run
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	const cap = 20000
	e.setBudget("user", e.editor.UserID, 0, cap)
	in := e.freeform("Again.")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	ok, refused := 0, 0
	for i := 0; i < 1000; i++ {
		_, err := e.try(e.editor, "ai.run", in)
		switch {
		case err == nil:
			ok++
		case err.Code == ops.CodeForbidden && err.Data["ai_code"] == "budget_exceeded":
			refused++
		default:
			t.Fatalf("run %d: %v", i, err)
		}
	}
	tokens, _ := e.spent()
	if ok == 0 || refused == 0 || tokens > cap || tokens != int64(ok)*150 {
		t.Fatalf("ok %d refused %d spent %d (cap %d)", ok, refused, tokens, cap)
	}
	if ok+refused != 1000 || len(f.requests()) != ok {
		t.Errorf("the provider saw %d requests for %d allowed runs: a refused run must not reach it", len(f.requests()), ok)
	}
	// The refusal says whose cap it is, in words.
	_, err := e.try(e.editor, "ai.run", in)
	if !strings.Contains(err.Message, "Your monthly AI limit") {
		t.Errorf("%q", err.Message)
	}
	// Someone else is unaffected; the cap is per person.
	pv2 := e.preview(e.owner, e.freeform("Again."))
	in2 := e.freeform("Again.")
	in2.Nonce, in2.PayloadHash = pv2.Nonce, pv2.PayloadHash
	if _, err := e.try(e.owner, "ai.run", in2); err != nil {
		t.Errorf("another person's run: %v", err)
	}
	// Users can read their own limits.
	b := e.call(e.editor, "ai.budget.get", aicore.BudgetGetIn{}).(aicore.BudgetOut)
	if !b.Limit || len(b.Item.Lines) == 0 || b.Item.Lines[0].MaxTokens != cap {
		t.Errorf("budget %+v", b.Item)
	}
	u := e.call(e.editor, "ai.usage.get", aicore.UsageIn{}).(aicore.UsageOut)
	if u.Totals.Runs != ok || u.Totals.InputTokens != int64(ok)*100 {
		t.Errorf("usage %+v", u.Totals)
	}
}

// The cap holds when requests arrive together: reservations count.
func TestTheCapHoldsUnderConcurrency(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.UserConcurrency = 100; o.Config.ProviderConcurrency = 100 })
	in := e.freeform("Together.")
	e.enable("ask", nil)
	pv0 := e.preview(e.editor, in) // no provider yet: refused, but the sizes are known below
	_ = pv0
	probe := newFake(t, nil)
	e.addProvider(probe, "Probe", false)
	pv := e.preview(e.editor, in)
	reserve := pv.ReserveTokens // what a run holds: its input for every send the chain allows, and the longest answer
	// The provider reports usage equal to the reservation, so every finished run uses up one slot.
	slow := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`, reserve-int64(pv.MaxTokens), pv.MaxTokens)
	})
	in.Provider = e.addProvider(slow, "Slow", false)
	pv = e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	e.setBudget("user", e.editor.UserID, 0, reserve*5+10)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, refused := 0, 0
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.try(e.editor, "ai.run", in)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case err.Data["ai_code"] == "budget_exceeded":
				refused++
			default:
				t.Errorf("%v", err)
			}
		}()
	}
	wg.Wait()
	tokens, _ := e.spent()
	if ok != 5 || refused != 55 || tokens > reserve*5+10 || len(slow.requests()) != 5 {
		t.Fatalf("ok %d refused %d spent %d cap %d provider saw %d", ok, refused, tokens, reserve*5+10, len(slow.requests()))
	}
	if n := e.count(`SELECT count(*) FROM ai_runs WHERE status = 'running'`); n != 0 {
		t.Errorf("%d reservations left", n)
	}
}

func TestMoneyCapsNeedAPrice(t *testing.T) {
	e := newEnv(t, nil)
	f := usageFake(t, 100000, 100000) // 200,000 tokens a run
	pid := e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	e.setBudget("instance", "", 5_000_000, 0) // five units a month, for everyone
	in := e.freeform("Hello")
	in.Provider = pid
	// A paid model with no known price under a money cap: refused, and the preview says why.
	pv := e.preview(e.editor, in)
	if pv.Decision.Allowed || pv.Decision.Code != "price_unknown" || pv.Cost.Known || pv.Cost.EstimateMicros != nil {
		t.Fatalf("decision %+v cost %+v", pv.Decision, pv.Cost)
	}
	// The administrator prices it (1 unit per million tokens each way): 200,000 tokens cost 0.4 units.
	e.call(e.admin, "ai.price.set", aicore.PriceSetIn{Model: "model-a", InputMicros: 1_000_000, OutputMicros: 1_000_000})
	pv = e.preview(e.editor, in)
	if !pv.Decision.Allowed || !pv.Cost.Known || pv.Cost.EstimateMicros == nil || pv.Cost.MaxMicros == nil {
		t.Fatalf("decision %+v cost %+v", pv.Decision, pv.Cost)
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	ok := 0
	for i := 0; i < 100; i++ {
		_, err := e.try(e.editor, "ai.run", in)
		if err != nil {
			if err.Data["ai_code"] != "budget_exceeded" || !strings.Contains(err.Message, "USD") {
				t.Fatalf("%v", err)
			}
			break
		}
		ok++
	}
	_, cost := e.spent()
	if ok != 25 || cost != 25*200_000 || cost > 5_000_000 { // 0.2 units a run, five a month, the 26th would not fit
		t.Fatalf("ok %d cost %d", ok, cost)
	}
	// The cost is in the run log, known and exact.
	runs := e.call(e.editor, "ai.run.list", aicore.RunListIn{ProjectID: e.project, Limit: 3}).(aicore.RunListOut)
	if runs.Items[0].Status != "blocked" || runs.Items[0].ErrorCode != "budget_exceeded" { // the refusal is logged too
		t.Errorf("%+v", runs.Items[0])
	}
	if runs.Items[1].CostMicros != 200_000 || !runs.Items[1].CostKnown || runs.Items[1].Status != "ok" {
		t.Errorf("%+v", runs.Items[1])
	}
}

func TestOwnAndLocalProvidersDoNotSpendTheInstancesMoney(t *testing.T) {
	e := newEnv(t, nil)
	e.enable("ask", func(p *aicore.PolicySetIn) { b := true; p.AllowPersonal = &b }) // the owner allows personal providers
	e.setBudget("instance", "", 1, 0)                                                // a money cap nothing instance-paid could pass
	// A person's own provider, price unknown: allowed (the person pays), counted in tokens.
	f := usageFake(t, 100, 50)
	mine := e.call(e.editor, "ai.provider.create", aicore.ProviderCreateIn{Name: "Mine", Kind: lib.KindOpenAICompatible, BaseURL: f.srv.URL + "/v1", DefaultModel: "m"}).(aicore.ProviderItem).Item.ID
	in := e.freeform("Hello")
	in.Provider = mine
	out, err := e.run(e.editor, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.CostKnown {
		t.Errorf("no price is known for it: %+v", out)
	}
	tokens, cost := e.spent()
	if tokens != 150 || cost != 0 {
		t.Errorf("tokens %d cost %d", tokens, cost)
	}
	// A local provider is free: cost known and zero.
	e.loopbackLocal.Store(true)
	lf := usageFake(t, 100, 50)
	lid := e.addProvider(lf, "LAN", true)
	in.Provider = lid
	pv := e.preview(e.editor, in)
	if !pv.Cost.Known || !pv.Cost.Local || pv.Cost.EstimateMicros == nil || *pv.Cost.EstimateMicros != 0 {
		t.Fatalf("cost %+v", pv.Cost)
	}
	out, err = e.run(e.editor, in)
	if err != nil || !out.CostKnown || out.CostMicros != 0 || !out.Local {
		t.Fatalf("%v %+v", err, out)
	}
	// An instance provider with no price is refused under that cap.
	e.loopbackLocal.Store(false)
	paid := e.addProvider(usageFake(t, 1, 1), "Paid", false)
	in.Provider = paid
	pv = e.preview(e.editor, in)
	if pv.Decision.Code != "price_unknown" {
		t.Errorf("%+v", pv.Decision)
	}
}

func TestProjectAndOwnerCaps(t *testing.T) {
	e := newEnv(t, nil)
	f := usageFake(t, 100, 50)
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	// The owner's own cap on the project, set in the policy.
	tok := int64(2000)
	e.enable("ask", func(p *aicore.PolicySetIn) { p.BudgetTokens = &tok })
	in := e.freeform("Hello")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	var err *ops.Error
	for i := 0; i < 50 && err == nil; i++ {
		_, err = e.try(e.editor, "ai.run", in)
	}
	if err == nil || !strings.Contains(err.Message, "owner's monthly AI limit") {
		t.Fatalf("%v", err)
	}
	// An administrator's cap on the project too; both must have room.
	e.enable("ask", func(p *aicore.PolicySetIn) { z := int64(0); p.BudgetTokens = &z })
	e.setBudget("project", e.project, 0, pv.ReserveTokens+600)
	var got *ops.Error
	for i := 0; i < 50 && got == nil; i++ {
		_, got = e.try(e.editor, "ai.run", in)
	}
	if got == nil || !strings.Contains(got.Message, "This project's monthly AI limit") {
		t.Fatalf("%v", got)
	}
	// Only an administrator sets caps; only the project's owner sees the project's usage by person.
	_, perr := e.try(e.owner, "ai.budget.set", aicore.BudgetSetIn{Scope: "instance", MaxTokens: 1})
	wantCode(t, perr, ops.CodeForbidden)
	_, perr = e.try(e.editor, "ai.usage.project", aicore.UsageProjectIn{ProjectID: e.project})
	wantCode(t, perr, ops.CodeForbidden)
	pu := e.call(e.owner, "ai.usage.project", aicore.UsageProjectIn{ProjectID: e.project}).(aicore.UsageProjectOut)
	if pu.Totals.Runs == 0 || len(pu.ByUser) != 1 || pu.ByUser[0].UserID != e.editor.UserID {
		t.Errorf("%+v", pu)
	}
}

func TestTheLedgerSurvivesAProjectBeingPurged(t *testing.T) {
	e := newEnv(t, nil)
	e.addProvider(usageFake(t, 100, 50), "Main", false)
	e.enable("ask", nil)
	if _, err := e.run(e.owner, e.freeform("Hello")); err != nil {
		t.Fatal(err)
	}
	before, _ := e.spent()
	if before != 150 {
		t.Fatalf("spent %d", before)
	}
	// Deleting the project must not reset anyone's month.
	e.call(e.owner, "project.trash", projects.StateIn{ID: e.project})
	e.call(e.owner, "project.purge", projects.PurgeIn{ID: e.project, ConfirmTitle: "The Salt Road"})
	if after, _ := e.spent(); after != before {
		t.Fatalf("purging the project changed the month's spend from %d to %d", before, after)
	}
	if e.count(`SELECT count(*) FROM ai_runs`) != 1 {
		t.Error("the run log went with the project")
	}
}

func TestRunsAreRateLimited(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.RatePerMinute = 6 })
	f := e.ready()
	in := e.freeform("Hello")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	limited := 0
	for i := 0; i < 30; i++ {
		if _, err := e.try(e.editor, "ai.run", in); err != nil {
			if err.Code != ops.CodeRateLimited || err.RetryAfterSeconds < 1 {
				t.Fatalf("%v", err)
			}
			limited++
		}
	}
	if limited == 0 || len(f.requests())+limited != 30 {
		t.Fatalf("limited %d, provider saw %d", limited, len(f.requests()))
	}
	// A malformed request does not spend the allowance.
	e2 := newEnv(t, func(o *aicore.Options) { o.Config.RatePerMinute = 1 })
	for i := 0; i < 20; i++ {
		_, err := e2.try(e2.owner, "ai.run", aicore.RunIn{ProjectID: e2.project})
		wantCode(t, err, ops.CodeInvalid)
	}
}

func TestHugeMaxTokensIsClamped(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.MaxOutputTokens = 500 })
	f := e.ready()
	in := e.freeform("Hello")
	in.MaxTokens = 2_000_000_000
	pv := e.preview(e.editor, in)
	if pv.MaxTokens != 500 || pv.Attempts[0].MaxTokens != 500 {
		t.Fatalf("clamped to %d", pv.MaxTokens)
	}
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	e.call(e.editor, "ai.run", in)
	if !strings.Contains(string(f.requests()[0].Body), `"max_tokens":500`) {
		t.Errorf("%s", f.requests()[0].Body[len(f.requests()[0].Body)-80:])
	}
	// A model that lists a smaller limit lowers it further.
	e2 := newEnv(t, nil)
	f2 := newFake(t, nil)
	pid := e2.addProvider(f2, "Main", false)
	e2.call(e2.admin, "ai.provider.model.set", aicore.ModelSetIn{ID: pid, Model: "model-a", MaxOutput: 300, ContextWindow: 100000})
	e2.enable("ask", nil)
	if pv := e2.preview(e2.editor, e2.freeform("Hello")); pv.Attempts[0].MaxTokens != 300 {
		t.Errorf("model limit: %d", pv.Attempts[0].MaxTokens)
	}
}

func TestAStoppedServersRunsStopHoldingBudget(t *testing.T) {
	e := newEnv(t, nil)
	e.enable("ask", nil)
	e.setBudget("user", e.editor.UserID, 0, 5000)
	// A run left running by a crash, holding most of the cap.
	_, err := e.d.Writer().Exec(`INSERT INTO ai_runs(id, user_id, project_id, recipe, status, reserved_tokens, created_at) VALUES ('air_crashed', ?, ?, 'freeform', 'running', 4900, ?)`,
		e.editor.UserID, e.project, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	e.addProvider(usageFake(t, 10, 10), "Main", false)
	pv := e.preview(e.editor, e.freeform("Hello"))
	if pv.Decision.Allowed || pv.Decision.Code != "budget_exceeded" {
		t.Fatalf("a reservation counts: %+v", pv.Decision)
	}
	e.svc.CloseInterrupted(t.Context(), e.d)
	if pv := e.preview(e.editor, e.freeform("Hello")); !pv.Decision.Allowed {
		t.Errorf("after the restart: %+v", pv.Decision)
	}
	if n := e.count(`SELECT count(*) FROM ai_runs WHERE id = 'air_crashed' AND status = 'error' AND error_code = 'interrupted'`); n != 1 {
		t.Error("the crashed run was not closed")
	}
}

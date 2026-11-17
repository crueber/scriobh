package ai_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// A provider that takes the request and then says nothing until it is hung up on.
func silentFake(t *testing.T, got *atomic.Int32) *fake {
	return newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		got.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(20 * time.Second):
		}
	})
}

// Start-and-cancel is not free: the provider received the whole prompt. A loop of streams cancelled
// before the first token must reach the monthly cap, not run for nothing.
func TestCancelledStreamsAreChargedAndReachTheCap(t *testing.T) {
	e := newEnv(t, nil)
	var received atomic.Int32
	e.addProvider(silentFake(t, &received), "Silent", false)
	e.enable("ask", nil)
	in := e.freeform("Again.")
	in.Stream = true
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	input, reserve := int64(pv.InputTokens), pv.ReserveTokens
	capacity := reserve + 6*input // room for the reservation and about six lost sends
	e.setBudget("user", e.editor.UserID, 0, capacity)
	refused, ran := 0, 0
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		want := received.Load() + 1
		done := make(chan error, 1)
		go func() {
			_, err := e.svc.Run(aicore.WithStream(ctx, &collect{}), e.d, e.editor, in)
			done <- err
		}()
		deadline := time.Now().Add(5 * time.Second * time.Duration(1+b2i(race)))
		var err error
	wait:
		for {
			select {
			case err = <-done: // refused before it started
				break wait
			default:
			}
			if received.Load() >= want {
				cancel()
				err = <-done
				break wait
			}
			if time.Now().After(deadline) {
				t.Fatal("neither refused nor started")
			}
			time.Sleep(2 * time.Millisecond)
		}
		cancel()
		if err != nil {
			if ae := ops.AsError(err); ae.Data["ai_code"] != "budget_exceeded" {
				t.Fatalf("%v", err)
			}
			refused++
		} else {
			ran++
		}
	}
	tokens, _ := e.spent()
	if refused == 0 || ran < 3 || tokens < input*int64(ran-1) || tokens > capacity {
		t.Fatalf("ran %d, refused %d, spent %d (input %d, cap %d)", ran, refused, tokens, input, capacity)
	}
	if n := e.count(`SELECT count(*) FROM ai_runs WHERE status = 'cancelled' AND input_tokens >= ?`, input); n != ran {
		t.Errorf("%d of %d cancelled runs carry the input", n, ran)
	}
}

// A call that timed out was sent whole.
func TestATimedOutCallIsCharged(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.SyncTimeout = time.Second; o.Config.Retries = 0 })
	var received atomic.Int32
	e.addProvider(silentFake(t, &received), "Silent", false)
	e.enable("ask", nil)
	pv := e.preview(e.editor, e.freeform("Hello"))
	_, err := e.run(e.editor, e.freeform("Hello"))
	wantCode(t, err, ops.CodeProvider)
	tokens, _ := e.spent()
	if tokens < int64(pv.InputTokens) || tokens > int64(pv.InputTokens)+50 {
		t.Errorf("a timed-out call was charged %d tokens, the input is about %d", tokens, pv.InputTokens)
	}
}

// The first try of a retry is thrown away but was sent: both count.
func TestADiscardedRetryIsCharged(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		if n == 1 {
			http.Error(w, "busy", 503)
			return
		}
		oaiReply(w, r, b, "ok")
	})
	e.addProvider(f, "Flaky", false)
	e.enable("ask", nil)
	pv := e.preview(e.editor, e.freeform("Hello"))
	out, err := e.run(e.editor, e.freeform("Hello"))
	if err != nil || out.Status != "ok" {
		t.Fatalf("%v %+v", err, out)
	}
	tokens, _ := e.spent()
	want := int64(pv.InputTokens) + 150 // the lost first send, and the answered one (the provider reports 120 + 30)
	if tokens != want || out.InputTokens != pv.InputTokens+120 || len(f.requests()) != 2 {
		t.Errorf("charged %d, want %d (the run reports %d in): %d requests", tokens, want, out.InputTokens, len(f.requests()))
	}
}

// Failures that were stopped before any work are free: a refused key, a rate limit, a refused address.
func TestFailuresBeforeAnyWorkAreFree(t *testing.T) {
	for name, code := range map[string]int{"auth": 401, "rate": 429, "rejected": 400} {
		e := newEnv(t, func(o *aicore.Options) { o.Config.Retries = 0 })
		e.addProvider(newFake(t, status(code)), "P", false)
		e.enable("ask", nil)
		_, err := e.run(e.editor, e.freeform("Hello"))
		wantCode(t, err, ops.CodeProvider)
		if tokens, _ := e.spent(); tokens != 0 {
			t.Errorf("%s: charged %d", name, tokens)
		}
	}
}

// The settlement row that fails to write must not take the cost with it.
func TestAnUnsettledRunStillCountsAgainstTheBudget(t *testing.T) {
	e := newEnv(t, nil)
	e.addProvider(usageFake(t, 100, 50), "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Hello")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	reserve := pv.ReserveTokens
	// Writing a finished run fails (as a busy or broken database might); marking it unsettled does not.
	if _, err := e.d.Writer().Exec(`CREATE TRIGGER fail_settle BEFORE UPDATE OF status ON ai_runs WHEN NEW.status = 'ok' BEGIN SELECT RAISE(ABORT, 'disk trouble'); END`); err != nil {
		t.Fatal(err)
	}
	out := e.call(e.editor, "ai.run", in).(aicore.RunItem).Item
	if out.Text != "A suggestion." && out.Text != "ok" || len(out.Warnings) == 0 {
		t.Fatalf("the person still gets the answer, with a note: %+v", out)
	}
	var status string
	var held int64
	_ = e.d.Read().QueryRow(`SELECT status, reserved_tokens FROM ai_runs`).Scan(&status, &held)
	if status != "unsettled" || held != reserve {
		t.Fatalf("status %q holding %d, want unsettled holding %d", status, held, reserve)
	}
	// It is counted as spent: a cap that only fits the reservation once refuses the next run.
	e.setBudget("user", e.editor.UserID, 0, reserve+reserve/2)
	_, err := e.try(e.editor, "ai.run", in)
	wantCode(t, err, ops.CodeForbidden)
	b := e.call(e.editor, "ai.budget.get", aicore.BudgetGetIn{}).(aicore.BudgetOut)
	if b.Item.Lines[0].ReservedTokens != reserve {
		t.Errorf("the budget holds %d, want %d", b.Item.Lines[0].ReservedTokens, reserve)
	}
	// A restart does not give it back (it is not a run left running).
	e.svc.CloseInterrupted(context.Background(), e.d)
	if n := e.count(`SELECT count(*) FROM ai_runs WHERE status = 'unsettled'`); n != 1 {
		t.Error("the unsettled run was closed")
	}
}

// A settings table that cannot be read is not "AI on".
func TestUnreadableSettingsFailClosed(t *testing.T) {
	e := newEnv(t, nil)
	e.addProvider(newFake(t, nil), "Main", false)
	e.enable("ask", nil)
	if _, err := e.d.Writer().Exec(`ALTER TABLE settings RENAME TO settings_away`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ai.provider.list", "ai.recipe.list"} {
		_, err := e.try(e.owner, name, struct{}{})
		if err == nil || err.Code != ops.CodeConflict || !strings.Contains(err.Message, "could not read its settings") {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := e.try(e.editor, "ai.context.preview", e.freeform("Hello"))
	wantCode(t, err, ops.CodeConflict)
	if !strings.Contains(e.log.String(), "settings could not be read") {
		t.Error("nothing was logged")
	}
	if _, err := e.d.Writer().Exec(`ALTER TABLE settings_away RENAME TO settings`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.try(e.owner, "ai.provider.list", struct{}{}); err != nil {
		t.Errorf("with the table back: %v", err)
	}
	// An empty table (no row) is the default: on.
	if pv := e.preview(e.editor, e.freeform("Hello")); !pv.Decision.Allowed {
		t.Errorf("%+v", pv.Decision)
	}
}

// The log has the class and the status of a provider failure, never its words.
func TestProviderTextNeverReachesTheLog(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"could not parse: %s"}}`, "MANUSCRIPT-FRAGMENT-XYZ she lit the stove"), 400)
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	_, _ = e.run(e.editor, e.freeform("Hello"))
	log := e.log.String()
	if strings.Contains(log, "MANUSCRIPT-FRAGMENT") || strings.Contains(log, "she lit the stove") {
		t.Error("the provider's words are in the log")
	}
	if !strings.Contains(log, "class=rejected") || !strings.Contains(log, "status=400") {
		t.Errorf("the log should have the class and status:\n%s", log)
	}
}

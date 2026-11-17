package ai_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func status(code int) func(http.ResponseWriter, *http.Request, []byte, int) {
	return func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		http.Error(w, `{"error":{"message":"nope"}}`, code)
	}
}

func (e *env) chain(ids ...string) {
	e.t.Helper()
	var c []aicore.ChainEntry
	for _, id := range ids {
		c = append(c, aicore.ChainEntry{ProviderID: id})
	}
	m := map[string][]aicore.ChainEntry{"default": c}
	e.enable("ask", func(in *aicore.PolicySetIn) { in.Chains = &m })
}

func TestTheChainFallsBackOnTrouble(t *testing.T) {
	for name, code := range map[string]int{"5xx": 503, "rate limit": 429} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			a, b := newFake(t, status(code)), newFake(t, nil)
			ida, idb := e.addProvider(a, "A", false), e.addProvider(b, "B", false)
			e.chain(ida, idb)
			in := e.freeform("Hello")
			pv := e.preview(e.editor, in)
			if len(pv.Attempts) != 2 {
				t.Fatalf("the preview must show both providers' bodies: %+v", pv.Attempts)
			}
			in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
			out := e.call(e.editor, "ai.run", in).(aicore.RunItem).Item
			if out.Status != "ok" || out.Provider != idb || out.Attempts != 2 {
				t.Fatalf("%+v", out)
			}
			if len(a.requests()) != 2 { // one try and one retry
				t.Errorf("A saw %d requests", len(a.requests()))
			}
			if string(b.requests()[0].Body) != pv.Attempts[1].Body {
				t.Error("the fallback provider received something other than its previewed body")
			}
		})
	}
}

func TestAKeyOrRequestProblemDoesNotFallBack(t *testing.T) {
	for name, code := range map[string]int{"auth": 401, "forbidden": 403, "rejected": 400, "not found": 404} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			a, b := newFake(t, status(code)), newFake(t, nil)
			e.chain(e.addProvider(a, "A", false), e.addProvider(b, "B", false))
			_, err := e.run(e.editor, e.freeform("Hello"))
			wantCode(t, err, ops.CodeProvider)
			if len(a.requests()) != 1 || len(b.requests()) != 0 {
				t.Fatalf("A %d, B %d: a refusal must not be routed around", len(a.requests()), len(b.requests()))
			}
			if strings.Contains(err.Message, "nope") {
				t.Error("the provider's own text reached the caller")
			}
		})
	}
}

func TestATimeoutFallsBack(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.Retries = 0 })
	a := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	})
	b := newFake(t, nil)
	ida := e.addProvider(a, "Slow", false)
	one := 1
	e.call(e.admin, "ai.provider.update", aicore.ProviderUpdateIn{ID: ida, TimeoutSeconds: &one})
	e.chain(ida, e.addProvider(b, "B", false))
	start := time.Now()
	out, err := e.run(e.editor, e.freeform("Hello"))
	if err != nil || out.Attempts != 2 || out.Status != "ok" {
		t.Fatalf("%v %+v", err, out)
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 8*time.Second*time.Duration(1+b2i(race)) {
			t.Errorf("took %v", took)
		}
	})
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

type collect struct {
	mu       sync.Mutex
	opens    []aicore.StreamInfo
	text     strings.Builder
	deltas   int
	failFrom int // return an error from the nth delta (1-based); 0 never
}

func (c *collect) Open(i aicore.StreamInfo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens = append(c.opens, i)
	return nil
}

func (c *collect) Delta(t string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deltas++
	if c.failFrom > 0 && c.deltas >= c.failFrom {
		return fmt.Errorf("the browser went away")
	}
	c.text.WriteString(t)
	return nil
}

func TestStreamingDeliversTextAsItArrivesAndRestoresNames(t *testing.T) {
	e := newEnv(t, nil)
	cid := e.compendium("Bible")
	e.setCompendium(cid)
	e.entity(cid, "Maire Nolan")
	e.entity(cid, "Tomas Quill")
	e.updateDoc(e.doc, "Maire Nolan met Tomas Quill at the quay.") // only placeholders that stand in the request are restored
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		oaiReply(w, r, b, "{{CHAR_1}} said hello to {{CHAR_2}}, and {{CHAR_1}} left.")
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", func(p *aicore.PolicySetIn) { r := "entities"; p.Redaction = &r })
	in := e.freeform("Hello")
	in.Stream = true
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	sink := &collect{}
	out, err := e.svc.Run(aicore.WithStream(context.Background(), sink), e.d, e.editor, in)
	if err != nil {
		t.Fatal(err)
	}
	want := "Maire Nolan said hello to Tomas Quill, and Maire Nolan left."
	if out.Text != want || sink.text.String() != want {
		t.Fatalf("final %q streamed %q", out.Text, sink.text.String())
	}
	if sink.deltas < 3 || len(sink.opens) != 1 || sink.opens[0].RunID != out.RunID {
		t.Errorf("deltas %d, opens %+v", sink.deltas, sink.opens)
	}
	if !strings.Contains(string(f.requests()[0].Body), `"stream":true`) || !strings.Contains(string(f.requests()[0].Body), `"include_usage":true`) {
		t.Error("the streamed body must ask for a stream with usage")
	}
	if string(f.requests()[0].Body) != pv.Attempts[0].Body {
		t.Error("the streamed bytes are not the previewed ones")
	}
	if !out.UsageReported || out.InputTokens != 120 || out.OutputTokens != 30 {
		t.Errorf("usage %+v", out)
	}
	// Not through the events hub: no event carries any text.
	if n := e.count(`SELECT count(*) FROM events WHERE payload_json LIKE '%hello%'`); n != 0 {
		t.Error("completion text reached the events table")
	}
	// A request previewed as a plain call is a different payload from the streamed one.
	plain := e.freeform("Hello")
	pp := e.preview(e.editor, plain)
	plain.Nonce, plain.PayloadHash = pp.Nonce, pp.PayloadHash
	_, err = e.svc.Run(aicore.WithStream(context.Background(), &collect{}), e.d, e.editor, plain)
	if err == nil || err.(*ops.Error).Code != ops.CodeConflict {
		t.Errorf("a payload previewed without streaming must not run as a stream: %v", err)
	}
}

func TestCancellationReachesTheProvider(t *testing.T) {
	e := newEnv(t, nil)
	var sawCancel atomic.Bool
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 200; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"word%d \"}}]}\n\n", i)
			fl.Flush()
			select {
			case <-r.Context().Done():
				sawCancel.Store(true)
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Hello")
	in.Stream = true
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash

	// The browser goes away after three pieces.
	sink := &collect{failFrom: 3}
	out, err := e.svc.Run(aicore.WithStream(context.Background(), sink), e.d, e.editor, in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != "cancelled" || !out.Incomplete || !strings.HasPrefix(out.Text, "word0 word1 ") {
		t.Fatalf("%+v", out)
	}
	deadline := time.Now().Add(5 * time.Second * time.Duration(1+b2i(race)))
	for !sawCancel.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !sawCancel.Load() {
		t.Fatal("the provider's request was never cancelled")
	}
	runs := e.call(e.editor, "ai.run.list", aicore.RunListIn{ProjectID: e.project}).(aicore.RunListOut)
	if runs.Items[0].Status != "cancelled" {
		t.Errorf("%+v", runs.Items[0])
	}
}

func TestACancelOpStopsARunningRequest(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 300; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"w%d \"}}]}\n\n", i)
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Hello")
	in.Stream = true
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	sink := &collect{}
	done := make(chan aicore.RunOut, 1)
	go func() {
		out, _ := e.svc.Run(aicore.WithStream(context.Background(), sink), e.d, e.editor, in)
		done <- out
	}()
	var runID string
	for i := 0; i < 500 && runID == ""; i++ {
		sink.mu.Lock()
		if len(sink.opens) > 0 {
			runID = sink.opens[0].RunID
		}
		sink.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	if runID == "" {
		t.Fatal("the run never started")
	}
	// Someone else cannot cancel it; its person can.
	_, err := e.try(e.reader, "ai.run.cancel", aicore.CancelIn{ProjectID: e.project, RunID: runID})
	wantCode(t, err, ops.CodeForbidden)
	got := e.call(e.editor, "ai.run.cancel", aicore.CancelIn{ProjectID: e.project, RunID: runID}).(aicore.CancelOut)
	if !got.Cancelled {
		t.Fatal("not cancelled")
	}
	select {
	case out := <-done:
		if out.Status != "cancelled" {
			t.Errorf("%+v", out)
		}
	case <-time.After(5 * time.Second * time.Duration(1+b2i(race))):
		t.Fatal("the run did not end")
	}
	if again := e.call(e.editor, "ai.run.cancel", aicore.CancelIn{ProjectID: e.project, RunID: runID}).(aicore.CancelOut); again.Cancelled {
		t.Error("a finished run cannot be cancelled again")
	}
}

// ---- hostile providers --------------------------------------------------------------------------

func TestMaliciousProviderResponses(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.MaxResponseBytes = 64 << 10; o.Config.Retries = 0 })
	json := func(s string) func(http.ResponseWriter, *http.Request, []byte, int) {
		return func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, s)
		}
	}
	for name, c := range map[string]struct {
		h     func(http.ResponseWriter, *http.Request, []byte, int)
		class string
	}{
		"a giant body": {func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"content":"`)
			chunk := strings.Repeat("x", 4096)
			for i := 0; i < 64; i++ {
				fmt.Fprint(w, chunk)
			}
			fmt.Fprint(w, `"}}]}`)
		}, lib.ClassTooLarge},
		"invalid JSON": {json(`{"choices": [`), lib.ClassBadResponse},
		"no choices":   {json(`{"choices": []}`), lib.ClassBadResponse},
		"an HTML page": {json(`<html><script>alert(1)</script></html>`), lib.ClassBadResponse},
		"a redirect": {func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
			http.Redirect(w, r, "http://127.0.0.1:1/x", 302)
		}, lib.ClassBadResponse},
		"a server error": {status(500), lib.ClassUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, c.h)
			in := e.freeform("Hello")
			pid := e.addProvider(f, "Evil "+name, false)
			in.Provider = pid
			e.enable("ask", nil)
			_, err := e.run(e.editor, in)
			wantCode(t, err, ops.CodeProvider)
			if err.Data["provider_class"] != c.class {
				t.Errorf("class %v, want %s", err.Data, c.class)
			}
			if strings.Contains(err.Message, "script") || strings.Contains(err.Message, "127.0.0.1") {
				t.Errorf("the message echoes the response: %q", err.Message)
			}
		})
	}
	// A call the provider may have processed (a body it could not be read from, a 500, an answer too
	// large) is charged what was sent; a redirect did no work and is not. No price is known, so no money.
	var runs, tokens, cost int64
	_ = e.d.Read().QueryRow(`SELECT COALESCE(SUM(runs),0), COALESCE(SUM(input_tokens),0), COALESCE(SUM(cost_micros),0) FROM ai_usage_daily`).Scan(&runs, &tokens, &cost)
	if runs != 5 || tokens <= 0 || cost != 0 {
		t.Errorf("usage for the failed runs: %d runs, %d tokens, %d micros (want 5 charged, the redirect not)", runs, tokens, cost)
	}
	if n := e.count(`SELECT count(*) FROM ai_runs WHERE status = 'running'`); n != 0 {
		t.Errorf("%d runs left running", n)
	}
}

func TestOutputIsDataNeverMarkup(t *testing.T) {
	e := newEnv(t, nil)
	nasty := "<script>alert('x')</script> <img src=x onerror=alert(1)> [click](javascript:alert(1))\x00 bad\xffbyte"
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) { oaiReply(w, r, b, nasty) })
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	out, err := e.run(e.editor, e.freeform("Hello"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Text, "<script>alert('x')</script>") || strings.ContainsRune(out.Text, 0) || !strings.Contains(out.Text, "bad�byte") {
		t.Errorf("text %q", out.Text)
	}
	if out.Kind != "text" {
		t.Errorf("kind %s", out.Kind)
	}
}

func TestToolCallsInAnAnswerAreNotActedOn(t *testing.T) {
	e := newEnv(t, nil)
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"delete_everything","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	})
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	out, err := e.run(e.editor, e.freeform("Hello"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "" || len(out.Warnings) == 0 {
		t.Errorf("%+v", out)
	}
}

func TestHostileUsageNumbersAreIgnored(t *testing.T) {
	e := newEnv(t, nil)
	for name, usage := range map[string]string{
		"negative": `{"prompt_tokens":-5,"completion_tokens":-9}`,
		"absurd":   `{"prompt_tokens":99999999999999,"completion_tokens":99999999999999}`,
		"missing":  `{"prompt_tokens":10}`,
	} {
		f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"message":{"content":"Fine."},"finish_reason":"stop"}],"usage":%s}`, usage)
		})
		in := e.freeform("Hello")
		in.Provider = e.addProvider(f, name, false)
		e.enable("ask", nil)
		out, err := e.run(e.editor, in)
		if err != nil {
			t.Fatal(name, err)
		}
		if out.UsageReported || out.InputTokens <= 0 || out.InputTokens > 10000 || out.OutputTokens < 0 || out.OutputTokens > 1000 {
			t.Errorf("%s: %+v", name, out)
		}
	}
}

func TestAnEndlessStreamIsCutOff(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.MaxResponseBytes = 32 << 10; o.Config.Retries = 0 })
	f := newFake(t, func(w http.ResponseWriter, r *http.Request, b []byte, n int) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 100000; i++ {
			if _, err := fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"blah blah blah blah \"}}]}\n\n"); err != nil {
				return
			}
			fl.Flush()
			if r.Context().Err() != nil {
				return
			}
		}
	})
	in := e.freeform("Hello")
	in.Stream = true
	in.Provider = e.addProvider(f, "Endless", false)
	e.enable("ask", nil)
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	start := time.Now()
	sink := &collect{}
	out, err := e.svc.Run(aicore.WithStream(context.Background(), sink), e.d, e.editor, in)
	if err != nil {
		t.Fatal(err)
	}
	// The lower bound proves some text was kept before the cut. How much arrives
	// before the cut depends on scheduling: under the race detector, and in the
	// push gate with four packages sharing four cores, it was 520 bytes. So the
	// bound only asks for a few chunks; the cap and the class are the point.
	minText := 100
	if out.Status != "error" || !out.Incomplete || out.ErrorClass != lib.ClassTooLarge || len(out.Text) > 40<<10 || len(out.Text) < minText {
		t.Fatalf("status %s class %s text %d bytes", out.Status, out.ErrorClass, len(out.Text))
	}
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second*time.Duration(1+b2i(race)) {
			t.Error("too slow")
		}
	})
}

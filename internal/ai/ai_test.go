package ai_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/testutil"
)

// Fake provider servers speak each protocol. They sit on loopback, which the
// guard's classifier seam calls public, so the guard itself is not in the way.
func publicLoopback(ip net.IP) netguard.Class {
	if ip.IsLoopback() {
		return netguard.Public
	}
	return netguard.Classify(ip)
}

type seen struct {
	mu   sync.Mutex
	reqs []struct {
		Path   string
		Header http.Header
		Body   []byte
	}
}

func (s *seen) add(r *http.Request, b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, struct {
		Path   string
		Header http.Header
		Body   []byte
	}{r.URL.Path, r.Header.Clone(), b})
}

func client(t *testing.T, kind string, h http.HandlerFunc, mut func(*ai.Endpoint)) (*ai.Client, *seen) {
	t.Helper()
	sn := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sn.add(r, b)
		r.Body = io.NopCloser(bytes.NewReader(b))
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	ep := ai.Endpoint{Kind: kind, BaseURL: srv.URL, APIKey: "sk-secret-key-123456", Mode: netguard.PublicOnly}
	if mut != nil {
		mut(&ep)
	}
	c, err := ai.NewClient(ep, ai.ClientOptions{Guard: netguard.Options{Classify: publicLoopback}, Limits: ai.Limits{MaxResponse: 64 << 10, Idle: 2 * time.Second, FirstByte: 5 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, sn
}

var payload = ai.Payload{Messages: []ai.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hello <b>&</b>"}}, MaxTokens: 200}

func TestNoProviderShapeEverCarriesTools(t *testing.T) {
	for _, kind := range ai.Kinds {
		for _, stream := range []bool{false, true} {
			for _, jsonMode := range []bool{false, true} {
				c, err := ai.NewClient(ai.Endpoint{Kind: kind, BaseURL: "https://example.org/v1", Routing: ai.Routing{ZDR: true, Order: []string{"a"}}, KeepAlive: "5m"}, ai.ClientOptions{})
				if err != nil {
					t.Fatal(err)
				}
				p := payload
				p.JSON = jsonMode
				temp := 0.4
				p.Temperature = &temp
				body, err := c.Encode("vendor/model-1", p, stream)
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if err := json.Unmarshal(body, &m); err != nil {
					t.Fatalf("%s: %v", kind, err)
				}
				for _, banned := range []string{"tools", "functions", "tool_choice", "function_call", "parallel_tool_calls", "tool_config"} {
					if _, ok := m[banned]; ok {
						t.Errorf("%s (stream %v) carries %q", kind, stream, banned)
					}
				}
				// Deterministic: the same inputs, the same bytes.
				again, _ := c.Encode("vendor/model-1", p, stream)
				if string(again) != string(body) {
					t.Errorf("%s: the body is not a function of its inputs", kind)
				}
				c.Close()
			}
		}
	}
	// A bad model name, an empty request and an unknown role are refused, not sent.
	c, _ := ai.NewClient(ai.Endpoint{Kind: ai.KindOllama, BaseURL: "http://localhost:11434"}, ai.ClientOptions{})
	for _, model := range []string{"", "has space", "quote\"", strings.Repeat("m", 300), "line\nbreak"} {
		if _, err := c.Encode(model, payload, false); err == nil {
			t.Errorf("model %q accepted", model)
		}
	}
	if _, err := c.Encode("m", ai.Payload{}, false); err == nil {
		t.Error("an empty request was accepted")
	}
	if _, err := c.Encode("m", ai.Payload{Messages: []ai.Message{{Role: "tool", Content: "x"}}}, false); err == nil {
		t.Error("a tool message was accepted")
	}
}

func TestOpenAICompatible(t *testing.T) {
	c, sn := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Hi"},"finish_reason":"length"}],"usage":{"prompt_tokens":11,"completion_tokens":3}}`)
	}, func(ep *ai.Endpoint) {
		ep.Headers = []ai.Header{{Name: "OpenAI-Organization", Value: "org-1"}, {Name: "Authorization", Value: "Bearer evil"}}
	})
	body, _ := c.Encode("gpt-x", payload, false)
	res, err := c.Chat(context.Background(), body)
	if err != nil || res.Text != "Hi" || !res.Truncated || res.Usage.InputTokens != 11 || res.Usage.OutputTokens != 3 || !res.Usage.Reported {
		t.Fatalf("%v %+v", err, res)
	}
	r := sn.reqs[0]
	if r.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-secret-key-123456" || r.Header.Get("OpenAI-Organization") != "org-1" {
		t.Errorf("%s %v", r.Path, r.Header)
	}
	if string(r.Body) != string(body) || strings.Contains(string(r.Body), "sk-secret") {
		t.Errorf("body %s", r.Body)
	}
	// The text is sent as it is, not as HTML-safe escapes.
	if !strings.Contains(string(body), "hello <b>&</b>") {
		t.Errorf("%s", body)
	}
}

func TestOpenAIStream(t *testing.T) {
	c, _ := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, ": keep-alive\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}, nil)
	body, _ := c.Encode("m", payload, true)
	var got []string
	res, err := c.Stream(context.Background(), body, func(s string) error { got = append(got, s); return nil })
	if err != nil || res.Text != "Hello" || strings.Join(got, "|") != "Hel|lo" || res.Truncated || !res.Usage.Reported || res.Usage.OutputTokens != 2 {
		t.Fatalf("%v %+v %v", err, res, got)
	}
}

func TestOpenRouterShape(t *testing.T) {
	c, sn := client(t, ai.KindOpenRouter, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/models" {
			fmt.Fprint(w, `{"data":[{"id":"anthropic/claude-x","name":"Claude X","context_length":200000,"pricing":{"prompt":"0.000003","completion":"0.000015"},"top_provider":{"max_completion_tokens":8192}},{"id":"openrouter/auto","pricing":{"prompt":"-1","completion":"-1"}}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"Yes"}}],"usage":{"prompt_tokens":20,"completion_tokens":5,"cost":0.000123}}`)
	}, nil)
	body, _ := c.Encode("anthropic/claude-x", payload, false)
	var wire map[string]any
	_ = json.Unmarshal(body, &wire)
	prov, _ := wire["provider"].(map[string]any)
	if prov["data_collection"] != "deny" || wire["usage"] == nil {
		t.Errorf("OpenRouter routing: %s", body)
	}
	res, err := c.Chat(context.Background(), body)
	if err != nil || res.Usage.CostMicros == nil || *res.Usage.CostMicros != 123 {
		t.Fatalf("%v %+v", err, res)
	}
	if sn.reqs[0].Header.Get("X-Title") != "Scriobh" || sn.reqs[0].Header.Get("HTTP-Referer") != "" {
		t.Errorf("headers %v: the instance's address must not be sent unless the administrator sets it", sn.reqs[0].Header)
	}
	ms, err := c.Models(context.Background())
	if err != nil || len(ms) != 2 {
		t.Fatalf("%v %+v", err, ms)
	}
	if ms[0].ContextWindow != 200000 || ms[0].MaxOutput != 8192 || ms[0].InputMicros == nil || *ms[0].InputMicros != 3_000_000 || *ms[0].OutputMicros != 15_000_000 {
		t.Errorf("%+v", ms[0])
	}
	if ms[1].InputMicros != nil { // "-1" means it varies: unknown, not a negative price
		t.Errorf("a variable price was read as %d", *ms[1].InputMicros)
	}
	// Data collection can be allowed, as a setting.
	c2, _ := ai.NewClient(ai.Endpoint{Kind: ai.KindOpenRouter, BaseURL: "https://openrouter.ai/api/v1", Routing: ai.Routing{AllowDataCollection: true}}, ai.ClientOptions{})
	b2, _ := c2.Encode("m", payload, false)
	if strings.Contains(string(b2), "data_collection") {
		t.Errorf("%s", b2)
	}
}

func TestOllama(t *testing.T) {
	c, sn := client(t, ai.KindOllama, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"llama3:8b","details":{"family":"llama","parameter_size":"8B"}}]}`)
		case "/api/chat":
			var req struct{ Stream bool }
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Stream {
				fmt.Fprintln(w, `{"message":{"content":"Hel"},"done":false}`)
				fmt.Fprintln(w, `{"message":{"content":"lo"},"done":false}`)
				fmt.Fprintln(w, `{"message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":9,"eval_count":4}`)
				return
			}
			fmt.Fprint(w, `{"message":{"content":"Hello"},"done":true,"done_reason":"stop","prompt_eval_count":9,"eval_count":4}`)
		}
	}, func(ep *ai.Endpoint) { ep.APIKey = ""; ep.KeepAlive = "10m" })
	ms, err := c.Models(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "llama3:8b" || ms[0].Label != "llama 8B" {
		t.Fatalf("%v %+v", err, ms)
	}
	body, _ := c.Encode("llama3:8b", payload, false)
	if !strings.Contains(string(body), `"stream":false`) || !strings.Contains(string(body), `"keep_alive":"10m"`) || !strings.Contains(string(body), `"num_predict":200`) {
		t.Errorf("%s", body)
	}
	res, err := c.Chat(context.Background(), body)
	if err != nil || res.Text != "Hello" || res.Usage.InputTokens != 9 || res.Usage.OutputTokens != 4 {
		t.Fatalf("%v %+v", err, res)
	}
	if sn.reqs[len(sn.reqs)-1].Header.Get("Authorization") != "" {
		t.Error("a key-less Ollama must send no Authorization header")
	}
	sbody, _ := c.Encode("llama3:8b", payload, true)
	var got []string
	res, err = c.Stream(context.Background(), sbody, func(s string) error { got = append(got, s); return nil })
	if err != nil || res.Text != "Hello" || len(got) != 2 || res.Usage.OutputTokens != 4 {
		t.Fatalf("%v %+v %v", err, res, got)
	}
}

// The OpenCode shapes are ASSUMED from the documented routes only (see wire.go);
// this checks the adapter against that assumption and nothing more.
func TestOpenCodeAssumedShape(t *testing.T) {
	c, sn := client(t, ai.KindOpenCode, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/model":
			fmt.Fprint(w, `[{"providerID":"anthropic","modelID":"claude-x","name":"Claude X"},"local/other"]`)
		case "/api/experimental/generate/text":
			fmt.Fprint(w, `{"text":"Rewritten."}`)
		default:
			http.NotFound(w, r)
		}
	}, nil)
	ms, err := c.Models(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "anthropic/claude-x" || ms[1].ID != "local/other" {
		t.Fatalf("%v %+v", err, ms)
	}
	body, _ := c.Encode("anthropic/claude-x", payload, false)
	res, err := c.Chat(context.Background(), body)
	if err != nil || res.Text != "Rewritten." {
		t.Fatalf("%v %+v", err, res)
	}
	r := sn.reqs[len(sn.reqs)-1]
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Basic ") {
		t.Errorf("OpenCode's server uses HTTP basic auth: %v", r.Header)
	}
	var wire map[string]any
	_ = json.Unmarshal(r.Body, &wire)
	if wire["model"] != "anthropic/claude-x" || wire["system"] != "sys" || !strings.Contains(fmt.Sprint(wire["prompt"]), "hello") {
		t.Errorf("%s", r.Body)
	}
	for _, banned := range []string{"tools", "agent", "parts"} {
		if _, ok := wire[banned]; ok {
			t.Errorf("OpenCode request carries %q", banned)
		}
	}
	// Streaming is one piece: the one-shot route returns the whole answer.
	sbody, _ := c.Encode("m", payload, true)
	var got []string
	if _, err := c.Stream(context.Background(), sbody, func(s string) error { got = append(got, s); return nil }); err != nil || len(got) != 1 {
		t.Errorf("%v %v", err, got)
	}
}

func TestErrorClassesAndTheirWords(t *testing.T) {
	for status, class := range map[int]string{401: ai.ClassAuth, 403: ai.ClassAuth, 402: ai.ClassCredit, 404: ai.ClassNotFound, 429: ai.ClassRate, 500: ai.ClassUnavailable, 503: ai.ClassUnavailable, 400: ai.ClassRejected, 422: ai.ClassRejected} {
		c, _ := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"error":{"message":"it said sk-secret-key-123456 at http://10.9.9.9"}}`, status)
		}, nil)
		body, _ := c.Encode("m", payload, false)
		_, err := c.Chat(context.Background(), body)
		pe, ok := ai.AsProviderError(err)
		if !ok || pe.Class != class || pe.Status != status {
			t.Errorf("%d: %v", status, err)
			continue
		}
		if strings.Contains(pe.Message(), "10.9.9.9") || strings.Contains(pe.Message(), "sk-") || strings.Contains(pe.Detail, "sk-secret-key-123456") {
			t.Errorf("%d leaks: %q / %q", status, pe.Message(), pe.Detail)
		}
		if status == 429 && pe.RetryAfter != 7 {
			t.Errorf("retry-after %d", pe.RetryAfter)
		}
		wantRetry := class == ai.ClassRate || class == ai.ClassUnavailable
		if pe.Retryable() != wantRetry {
			t.Errorf("%d retryable %v", status, pe.Retryable())
		}
	}
}

func TestTheGuardIsInTheDialer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}]}`)
	}))
	defer srv.Close()
	// The real classifier: loopback is not public, so a provider that is not marked local cannot connect.
	for mode, wantBlocked := range map[netguard.Mode]bool{netguard.PublicOnly: true, netguard.AllowLocal: false, netguard.RequireLocal: false} {
		c, _ := ai.NewClient(ai.Endpoint{Kind: ai.KindOpenAICompatible, BaseURL: srv.URL, Mode: mode}, ai.ClientOptions{})
		body, _ := c.Encode("m", payload, false)
		_, err := c.Chat(context.Background(), body)
		pe, _ := ai.AsProviderError(err)
		if wantBlocked != (pe != nil && pe.Class == ai.ClassBlocked) {
			t.Errorf("mode %d: %v", mode, err)
		}
		if pe != nil && strings.Contains(pe.Message()+pe.Detail, "127.0.0.1") {
			t.Errorf("mode %d echoes an address: %q", mode, pe.Message()+pe.Detail)
		}
		c.Close()
	}
	// Redirects are never followed, even to a harmless place.
	hit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redir.Close()
	c, _ := ai.NewClient(ai.Endpoint{Kind: ai.KindOpenAICompatible, BaseURL: redir.URL, Mode: netguard.AllowLocal, APIKey: "sk-secret-key-123456"}, ai.ClientOptions{})
	body, _ := c.Encode("m", payload, false)
	if _, err := c.Chat(context.Background(), body); err == nil || hit {
		t.Errorf("a redirect was followed (%v, hit %v)", err, hit)
	}
	// No proxy from the environment is used.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	c3, _ := ai.NewClient(ai.Endpoint{Kind: ai.KindOpenAICompatible, BaseURL: srv.URL, Mode: netguard.AllowLocal}, ai.ClientOptions{})
	b3, _ := c3.Encode("m", payload, false)
	if _, err := c3.Chat(context.Background(), b3); err != nil {
		t.Errorf("a proxy from the environment was used: %v", err)
	}
}

func TestDNSRebindingIsCaughtAtConnectTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}]}`)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	// The name resolves to a public address on the first lookup (the check a creation-time
	// resolver would make) and to loopback after that.
	var mu sync.Mutex
	n := 0
	lookup := func(ctx context.Context, host string) ([]net.IP, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return []net.IP{net.ParseIP("93.184.216.34")}, nil
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	c, err := ai.NewClient(ai.Endpoint{Kind: ai.KindOpenAICompatible, BaseURL: "http://rebind.example:" + port, Mode: netguard.PublicOnly},
		ai.ClientOptions{Guard: netguard.Options{LookupIP: lookup}, Limits: ai.Limits{Dial: 500 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := c.Encode("m", payload, false)
	for i := 0; i < 3; i++ {
		c.Close() // no kept-alive connection to hide behind
		_, err := c.Chat(context.Background(), body)
		pe, _ := ai.AsProviderError(err)
		if pe == nil || (pe.Class != ai.ClassUnreachable && pe.Class != ai.ClassTimeout && pe.Class != ai.ClassBlocked) {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if i >= 1 && pe.Class != ai.ClassBlocked {
			t.Errorf("attempt %d after the answer changed: %v (want blocked)", i, pe.Class)
		}
	}
}

func TestResponseBoundsAndHostileBodies(t *testing.T) {
	huge := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"`)
		chunk := strings.Repeat("y", 8192)
		for i := 0; i < 64; i++ {
			fmt.Fprint(w, chunk)
		}
		fmt.Fprint(w, `"}}]}`)
	}
	c, _ := client(t, ai.KindOpenAICompatible, huge, nil)
	body, _ := c.Encode("m", payload, false)
	_, err := c.Chat(context.Background(), body)
	if pe, _ := ai.AsProviderError(err); pe == nil || pe.Class != ai.ClassTooLarge {
		t.Errorf("a response over the cap: %v", err)
	}
	// A stream that never says anything is dropped after the idle limit, not waited for.
	c2, _ := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}, nil)
	sbody, _ := c2.Encode("m", payload, true)
	start := time.Now()
	res, err := c2.Stream(context.Background(), sbody, func(string) error { return nil })
	pe, _ := ai.AsProviderError(err)
	took := time.Since(start)
	if pe == nil || pe.Class != ai.ClassTimeout || res.Text != "a" || !res.Truncated {
		t.Errorf("an idle stream: %v %+v after %v", err, res, took)
	}
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 10*time.Second {
			t.Errorf("an idle stream was dropped after %v", took)
		}
	})
	// A line with no end is refused after a megabyte.
	c3, _ := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 100; i++ {
			fmt.Fprint(w, strings.Repeat("z", 1000))
		}
	}, nil)
	b4, _ := c3.Encode("m", payload, true)
	if _, err := c3.Stream(context.Background(), b4, func(string) error { return nil }); err == nil {
		t.Error("an endless line was accepted")
	}
}

func TestRequestsAreCancelledWithTheirContext(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	c, _ := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(cancelled)
	}, nil)
	body, _ := c.Encode("m", payload, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.Chat(ctx, body); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if pe, _ := ai.AsProviderError(err); pe == nil || pe.Class != ai.ClassCancelled {
			t.Errorf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call did not return")
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the cancellation")
	}
}

func TestTheExtraHeaderAllowList(t *testing.T) {
	for _, bad := range []ai.Header{{"Authorization", "Bearer x"}, {"authorization", "x"}, {"Cookie", "a=b"}, {"Host", "evil"}, {"X-Forwarded-For", "1.2.3.4"}, {"Proxy-Authorization", "x"}, {"X-Title", "line\r\nInjected: 1"}, {"X-Title", strings.Repeat("a", 300)}, {"X-Title", "Scríobh"}} {
		if ai.CheckHeader(bad) == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	for _, good := range []ai.Header{{"HTTP-Referer", "https://example.org"}, {"x-title", "My App"}, {"OpenAI-Project", "proj_1"}} {
		if err := ai.CheckHeader(good); err != nil {
			t.Errorf("%v: %v", good, err)
		}
	}
	// And the adapter does not trust a list it was handed: a header that is not allowed is dropped at send time.
	c, sn := client(t, ai.KindOpenAICompatible, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"x"}}]}`)
	}, func(ep *ai.Endpoint) {
		ep.Headers = []ai.Header{{Name: "Cookie", Value: "x=y"}, {Name: "X-Title", Value: "ok"}}
	})
	body, _ := c.Encode("m", payload, false)
	_, _ = c.Chat(context.Background(), body)
	if sn.reqs[0].Header.Get("Cookie") != "" || sn.reqs[0].Header.Get("X-Title") != "ok" {
		t.Errorf("%v", sn.reqs[0].Header)
	}
}

func TestEstimates(t *testing.T) {
	if ai.EstimateTokens("") != 0 || ai.EstimateTokens("abcd") != 1 || ai.EstimateTokens("abcde") != 2 {
		t.Error("ASCII is four characters a token, rounded up")
	}
	if ai.EstimateTokens("日本語") != 3 || ai.EstimateTokens("😀") != 2 {
		t.Error("wide characters count a token each, emoji two")
	}
	p := ai.Price{InputMicros: 3_000_000, OutputMicros: 15_000_000}
	if got := p.Cost(1000, 500); got != 3000+7500 {
		t.Errorf("cost %d", got)
	}
	if p.Cost(-5, -5) != 0 || (ai.Price{InputMicros: 1}).Cost(1, 0) != 1 { // rounded up, never negative
		t.Error("clamping and rounding")
	}
	huge := int(^uint(0) >> 1) // the largest int, whatever its size (1<<40 does not fit a 32-bit int)
	if got := (ai.Price{InputMicros: 1 << 62, OutputMicros: 1 << 62}).Cost(huge, huge); got < 0 {
		t.Errorf("overflow: %d", got)
	}
}

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/config"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// aiOptions maps the configuration onto the AI core's options.
func aiOptions(cfg *config.Config, box *secrets.Box, log *slog.Logger) aicore.Options {
	a := cfg.AI
	sec := func(n int) time.Duration { return time.Duration(n) * time.Second }
	return aicore.Options{Box: box, Log: log, Config: aicore.Config{
		Off: a.Off, MaxRequestBytes: a.MaxRequestKB << 10, MaxChipBytes: a.MaxChipKB << 10, MaxOutputTokens: a.MaxOutputTokens,
		MaxResponseBytes: int64(a.MaxResponseKB) << 10, DialTimeout: sec(a.DialTimeoutS), RequestTimeout: sec(a.TimeoutS), SyncTimeout: sec(a.SyncTimeoutS),
		StreamIdle: sec(a.StreamIdleS), StreamMax: sec(a.StreamMaxS), ProviderConcurrency: a.ProviderConcurrency, UserConcurrency: a.UserConcurrency,
		RatePerMinute: a.RatePerMin, Retries: a.Retries, NoUserProviders: !a.UserProviders, DefaultUserTokens: a.DefaultUserTokens,
		DefaultUserMicros: a.DefaultUserCostMicros, Currency: a.Currency}}
}

// registerAI adds the one AI route that is not an op: the streaming form of
// ai.run. It is ai.run with a delta sink in the request context (the way a raw
// upload body travels to its op), so authorization, the policy, the budget, the
// audit entry and the event are the op's, not a copy of them here.
//
// The response is text/event-stream with these events, each one line of JSON:
//
//	start   {run_id, provider, model}   an attempt began (again if the chain falls back before any text)
//	delta   {text}                      a piece of the answer, redaction already undone
//	done    {item}                      the finished run, as ai.run returns it
//	error   {error}                     the run failed after the stream began
//
// A failure before the first start is an ordinary JSON error with its status.
// Closing the connection cancels the run and the cancellation reaches the
// provider. Completion text travels on this response only, never through the
// events hub. No Idempotency-Key is honoured: a stream is never replayed.
func (s *Server) registerAI(m *http.ServeMux) {
	m.HandleFunc("POST /api/v1/projects/{id}/ai/run/stream", s.aiStream)
}

func (s *Server) aiStream(w http.ResponseWriter, r *http.Request) {
	e, ok := s.Reg.Lookup("ai.run")
	if !ok {
		writeJSONError(w, ops.NotFound("endpoint"))
		return
	}
	p := authFrom(r.Context()).P
	if err := s.Reg.PreAuthorize(p, e); err != nil {
		writeJSONError(w, ops.AsError(err))
		return
	}
	if dry, _ := dryRunRequested(r); dry {
		writeJSONError(w, ops.DryRunUnsupported("ai.run"))
		return
	}
	in := aicore.RunIn{ProjectID: r.PathValue("id")}
	if err := s.Reg.AuthorizeInput(ops.Precheck(r.Context()), p, e, in); err != nil {
		writeJSONError(w, ops.AsError(err))
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if asMaxBytes(err, &mbe) {
			writeJSONError(w, ops.TooLarge())
			return
		}
		writeJSONError(w, ops.Invalid("", "Could not read the request body."))
		return
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeJSONError(w, ops.Invalid("", "Malformed JSON: "+err.Error()))
		return
	}
	if _, err := dec.Token(); err != io.EOF {
		writeJSONError(w, ops.Invalid("", "Unexpected data after the JSON body."))
		return
	}
	in.ProjectID = r.PathValue("id")
	in.Stream = true
	sink := newSSESink(w)
	defer sink.stop()
	out, cerr := s.Reg.Call(aicore.WithStream(r.Context(), sink), p, "ai.run", in)
	if !sink.started() {
		if cerr != nil {
			writeJSONError(w, ops.AsError(cerr))
			return
		}
		writeJSONBody(w, http.StatusOK, encodeJSON(out), false)
		return
	}
	if cerr != nil {
		sink.event("error", map[string]any{"error": ops.AsError(cerr)})
		return
	}
	sink.event("done", out)
}

// sseSink writes a run to the browser as server-sent events.
type sseSink struct {
	w      http.ResponseWriter
	rc     *http.ResponseController
	mu     sync.Mutex
	open   bool
	closed bool
	done   chan struct{}
}

const aiStreamWriteLimit = 20 * time.Second

func newSSESink(w http.ResponseWriter) *sseSink {
	return &sseSink{w: w, rc: http.NewResponseController(w), done: make(chan struct{})}
}

func (k *sseSink) started() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.open
}

func (k *sseSink) stop() {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.closed {
		k.closed = true
		close(k.done)
	}
}

// put writes one frame. The caller holds k.mu; after stop nothing is written, so
// the keep-alive goroutine cannot touch the response once the handler has returned.
func (k *sseSink) put(frame string) error {
	if k.closed {
		return http.ErrHandlerTimeout
	}
	_ = k.rc.SetWriteDeadline(time.Now().Add(aiStreamWriteLimit))
	if _, err := fmt.Fprint(k.w, frame); err != nil {
		return err
	}
	return k.rc.Flush()
}

func (k *sseSink) event(name string, v any) {
	b, _ := json.Marshal(v)
	k.mu.Lock()
	defer k.mu.Unlock()
	_ = k.put("event: " + name + "\ndata: " + string(b) + "\n\n")
}

// Open starts the response on the first attempt and announces each one.
func (k *sseSink) Open(i aicore.StreamInfo) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.open {
		k.open = true
		_ = k.rc.SetReadDeadline(time.Time{})
		_ = k.rc.SetWriteDeadline(time.Time{})
		h := k.w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache, no-store")
		h.Set("X-Accel-Buffering", "no")
		h.Set("Connection", "keep-alive")
		k.w.WriteHeader(http.StatusOK)
		go k.keepAlive()
	}
	b, _ := json.Marshal(map[string]any{"run_id": i.RunID, "provider": i.ProviderName, "model": i.Model})
	return k.put("event: start\ndata: " + string(b) + "\n\n")
}

// Delta sends a piece of the answer. A write that fails (the browser went away)
// cancels the run.
func (k *sseSink) Delta(t string) error {
	b, _ := json.Marshal(map[string]string{"text": t})
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.put("event: delta\ndata: " + string(b) + "\n\n")
}

// keepAlive writes a comment now and then so a proxy does not close a stream
// that is waiting for its first token.
func (k *sseSink) keepAlive() {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-k.done:
			return
		case <-t.C:
			k.mu.Lock()
			_ = k.put(": keep-alive\n\n")
			k.mu.Unlock()
		}
	}
}

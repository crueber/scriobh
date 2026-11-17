package ai

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/ai/redact"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The run pipeline. One request goes through, in this order and on the server:
//
//  1. the kill switch, and the shape of the request (nothing is read or counted);
//  2. the person's rate allowance;
//  3. build: policy, context, redaction, the exact bodies, the hash (the same
//     function the preview uses), and the refusals that depend on them;
//  4. the payload hash the preview gave: a different hash is a 409;
//  5. the person's concurrency slot, then the budget reservation, which records
//     the run;
//  6. the providers of the chain, one after another, each behind its own
//     concurrency limit, with a retry or two for failures retrying can fix;
//  7. the answer, restored and parsed by the recipe, returned as a suggestion;
//  8. the settlement: the run's real usage replaces the reservation, in one
//     transaction with the audit entry and the event.
//
// Nothing here writes to the manuscript.

// RunOut is a finished run: a suggestion. The text is data. It is never HTML
// and never inserted anywhere by this code; a screen shows it as text and
// accepting it is a separate editor op.
type RunOut struct {
	RunID         string          `json:"run_id"`
	Status        string          `json:"status" enum:"ok,cancelled,error" desc:"ok; cancelled (the text so far is kept); error (only with text: a stream broke part way, and what arrived is kept)."`
	Recipe        string          `json:"recipe"`
	RecipeVersion int             `json:"recipe_version"`
	Kind          string          `json:"kind" enum:"text,notes,marks,json"`
	Text          string          `json:"text" desc:"What the model said, with redacted names put back. Untrusted text: show it as text, never as HTML."`
	Data          json.RawMessage `json:"data,omitempty" desc:"The structured form, when the recipe parses one (notes, marks, JSON). Absent when the answer did not parse; the text is then all there is."`
	Warnings      []string        `json:"warnings,omitempty"`
	Incomplete    bool            `json:"incomplete" desc:"The answer was cut off (length limit, cancellation or a broken stream)."`
	Provider      string          `json:"provider_id"`
	ProviderName  string          `json:"provider_name"`
	Model         string          `json:"model"`
	Local         bool            `json:"local"`
	InputTokens   int             `json:"input_tokens"`
	OutputTokens  int             `json:"output_tokens"`
	UsageReported bool            `json:"usage_reported" desc:"The counts are the provider's own; otherwise they are estimates."`
	CostMicros    int64           `json:"cost_micros"`
	CostKnown     bool            `json:"cost_known"`
	Currency      string          `json:"currency"`
	LatencyMS     int64           `json:"latency_ms"`
	Attempts      int             `json:"attempts" desc:"Providers tried; more than one means a fallback was used."`
	Unrestored    int             `json:"unrestored_placeholders,omitempty" desc:"Placeholders in the answer that did not match any name."`
	ErrorClass    string          `json:"error_class,omitempty"`
	ErrorMessage  string          `json:"error_message,omitempty"`
}

// RunItem wraps a run.
type RunItem struct {
	Item RunOut `json:"item"`
}

// StreamInfo is what a stream's consumer learns when an attempt starts.
type StreamInfo struct {
	RunID        string
	ProviderName string
	Model        string
}

// StreamSink receives a run as it happens. Open is called when an attempt
// starts (again if the chain falls back before any text arrived); Delta with
// each piece of text. An error from Delta cancels the run, and cancellation
// reaches the provider. Completion text goes here and nowhere else: not through
// the events hub.
type StreamSink interface {
	Open(StreamInfo) error
	Delta(text string) error
}

type streamKey struct{}

// WithStream makes ai.run deliver its text to sink as it arrives.
func WithStream(ctx context.Context, sink StreamSink) context.Context {
	return context.WithValue(ctx, streamKey{}, sink)
}

func streamFrom(ctx context.Context) StreamSink {
	s, _ := ctx.Value(streamKey{}).(StreamSink)
	return s
}

// Preview builds the request and says what would be sent, to whom, at what
// cost, and whether it is allowed. It reads, never writes, and calls no
// provider.
func (s *Service) Preview(ctx context.Context, d *db.DB, p ops.Principal, in RunIn) (PreviewOut, error) {
	if err := s.gate(ctx, d); err != nil {
		return PreviewOut{}, err
	}
	if err := in.Validate(); err != nil {
		return PreviewOut{}, err
	}
	// A preview reads the project and builds a whole request: it is limited too, more
	// generously than a run (a screen previews as the chips change).
	if e := s.allow("preview:"+p.UserID, s.cfg.RatePerMinute*4); e != nil {
		return PreviewOut{}, e
	}
	b, err := s.build(ctx, d, p, in)
	if err != nil {
		return PreviewOut{}, err
	}
	return PreviewOut{Item: b.pv}, nil
}

var blockedCodes = map[string]bool{"policy_off": true, "action_not_allowed": true, "agents_not_allowed": true, "no_provider": true,
	"too_large": true, "over_context": true, "budget_exceeded": true, "price_unknown": true}

// Run executes a request that was previewed. See the package comment for the
// order of events. in must carry the Nonce and PayloadHash of the preview.
func (s *Service) Run(ctx context.Context, d *db.DB, p ops.Principal, in RunIn) (RunOut, error) {
	if err := s.gate(ctx, d); err != nil {
		return RunOut{}, err
	}
	if err := in.Validate(); err != nil {
		return RunOut{}, err
	}
	if in.PayloadHash == "" || in.Nonce == "" {
		return RunOut{}, ops.Invalid("payload_hash", "Preview the request first (ai.context.preview) and send its payload_hash and nonce: a run sends exactly what was previewed.")
	}
	sink := streamFrom(ctx)
	if in.Stream && sink == nil {
		return RunOut{}, ops.Invalid("stream", "A streaming request goes through POST /api/v1/projects/{id}/ai/run/stream.")
	}
	if !in.Stream && sink != nil {
		in.Stream = true
	}
	if e := s.allowRun(p.UserID); e != nil {
		return RunOut{}, e
	}
	b, err := s.build(ctx, d, p, in)
	if err != nil {
		return RunOut{}, err
	}
	if b.refusal != nil {
		if blockedCodes[b.refusalKV] {
			s.recordBlocked(ctx, d, p, in, b)
		}
		return RunOut{}, b.refusal
	}
	if b.pv.PayloadHash != in.PayloadHash {
		return RunOut{}, ops.ConflictWith("The text or the settings changed since you previewed this request, so nothing was sent. Preview it again.",
			map[string]any{"ai_code": "payload_changed", "payload_hash": b.pv.PayloadHash})
	}

	// The person's own concurrency slot.
	rel, serr := s.semFor(s.userSem, p.UserID, s.cfg.UserConcurrency).acquire(ctx, 2*time.Second)
	if serr != nil {
		if errors.Is(serr, errBusy) {
			e := ops.RateLimited(5)
			e.Message = "You already have the most AI requests this server allows running at once. Wait for one to finish."
			return RunOut{}, e
		}
		return RunOut{}, serr
	}
	defer rel()

	first := b.attempts[0]
	run := runRow{ID: db.NewID("air"), UserID: p.UserID, ProjectID: in.ProjectID, DocumentID: in.DocumentID, Recipe: b.recipe.ID, RecipeVersion: b.recipe.Version,
		ProviderID: first.p.ID, Model: first.model, Via: viaOf(p), Currency: b.pv.Cost.Currency, PayloadHash: b.pv.PayloadHash, ScopeJSON: b.scopeJSON(), CreatedAt: now()}
	refusal, rerr := s.reserve(ctx, d, run, b.pol, b.reserve)
	if rerr != nil {
		return RunOut{}, ops.Internal(rerr)
	}
	if refusal != nil {
		s.recordBlocked(ctx, d, p, in, b)
		return RunOut{}, refusal
	}

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	s.active[run.ID] = activeRun{cancel: cancel, userID: p.UserID, project: in.ProjectID}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.active, run.ID)
		s.mu.Unlock()
	}()
	// A call that is not streamed must finish under the server's write timeout: it has a
	// budget, which the attempts share (see execute). A stream has its own limits, in the client.
	var deadline time.Time
	if !in.Stream {
		deadline = time.Now().Add(s.cfg.SyncTimeout)
	}

	started := time.Now()
	ex := s.execute(rctx, deadline, d, b, in, run.ID, sink)
	out := s.finish(ctx, d, p, b, in, run, ex, time.Since(started))
	if ex.err != nil && ex.text == "" && out.Status == "error" {
		return RunOut{}, ex.err
	}
	return out, nil
}

func viaOf(p ops.Principal) string {
	if p.Via == "apikey" {
		return "apikey"
	}
	return "session"
}

func (b *built) scopeJSON() string {
	type chip struct {
		Kind   string `json:"kind"`
		Label  string `json:"label"`
		Source string `json:"source,omitempty"`
		Tokens int    `json:"tokens"`
	}
	v := struct {
		Chips    []chip `json:"chips"`
		Skipped  int    `json:"skipped"`
		Redact   string `json:"redaction"`
		Preamble string `json:"preamble"`
		Stream   bool   `json:"stream"`
	}{Skipped: len(b.pv.Skipped), Redact: b.pol.Redaction, Preamble: lib.PreambleVersion, Stream: b.pv.Stream}
	for _, c := range b.pv.Chips {
		v.Chips = append(v.Chips, chip{c.Kind, c.Label, c.Source, c.Tokens})
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// recordBlocked writes a refused request into the run log: who asked, for what,
// and which rule said no. It holds no text.
func (s *Service) recordBlocked(ctx context.Context, d *db.DB, p ops.Principal, in RunIn, b *built) {
	id := db.NewID("air")
	at := now()
	_ = d.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO ai_runs(id, user_id, project_id, document_id, recipe, recipe_version, via, status, error_code, currency, payload_hash, scope_json, created_at, finished_at)
			VALUES (?,?,?,?,?,?,?,'blocked',?,?,?,?,?,?)`, id, p.UserID, in.ProjectID, in.DocumentID, b.recipe.ID, b.recipe.Version, viaOf(p), b.refusalKV, b.pv.Cost.Currency, b.pv.PayloadHash, b.scopeJSON(), at, at); err != nil {
			return err
		}
		return s.flush(tx, p.UserID,
			[]ops.AuditEntry{{Action: "ai.run.blocked", TargetType: "ai_run", TargetID: id, Detail: map[string]any{"recipe": b.recipe.ID, "reason": b.refusalKV, "project_id": in.ProjectID}}}, nil)
	})
}

func (s *Service) flush(tx *sql.Tx, actor string, audit []ops.AuditEntry, evs []ops.Event) error {
	if s.env == nil || s.env.Sink == nil {
		return nil
	}
	if ts, ok := s.env.Sink.(ops.TxSink); ok {
		return ts.FlushTx(tx, actor, evs, audit)
	}
	return nil
}

// ---- execution ------------------------------------------------------------------------------------------------

type execResult struct {
	text       string
	res        lib.Result
	used       *builtAttempt
	attempts   int
	delivered  int
	err        *ops.Error
	pe         *lib.ProviderError
	cancelled  bool
	unrestored int
	// failed are the sends that reached a provider and did not give an answer that was kept: a
	// timeout, a stream cut off, a retry that was thrown away. The provider received the whole
	// prompt each time, so each is charged at least the input (see chargeable).
	failed []failedSend
}

type failedSend struct {
	att  *builtAttempt
	text string // what arrived before it failed
}

// chargeable says whether a failure of this class means the provider had the prompt:
// a call that timed out, was cancelled, was cut off, or answered with something unreadable
// may well have been processed and billed. A refused address, a connection that never came
// up, a bad key, a refused request and a rate limit were stopped before any work.
func chargeable(pe *lib.ProviderError) bool {
	if pe.Status >= 300 && pe.Status < 400 { // a redirect is an answer that did no work
		return false
	}
	switch pe.Class {
	case lib.ClassTimeout, lib.ClassCancelled, lib.ClassUnavailable, lib.ClassBadResponse, lib.ClassTooLarge:
		return true
	}
	return false
}

// execute tries the providers of the chain. ctx is cancelled only by the person
// or the caller going away (that is a cancellation); deadline, when set, is the
// time budget of a call that is not streamed. Each attempt gets its own share of
// what is left (the whole remainder for the last one), so a first provider that
// hangs cannot use up the time the next one needs, and running out of time is a
// timeout the chain can move past, not a cancellation.
func (s *Service) execute(ctx context.Context, deadline time.Time, d *db.DB, b *built, in RunIn, runID string, sink StreamSink) execResult {
	var ex execResult
	for i := range b.attempts {
		at := &b.attempts[i]
		if ctx.Err() != nil {
			ex.cancelled = true
			return ex
		}
		actx, acancel := ctx, context.CancelFunc(func() {})
		if !deadline.IsZero() {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				ex.pe = &lib.ProviderError{Class: lib.ClassTimeout, Detail: "the time budget of the call is used up"}
				break
			}
			actx, acancel = context.WithTimeout(ctx, remaining/time.Duration(len(b.attempts)-i))
		}
		ex.attempts++
		requireLocal := b.pol.Mode == ModeLocalOnly
		cl, e := s.client(at.p, requireLocal)
		if e != nil {
			acancel()
			ex.err = e
			return ex // a key that cannot be read is not something another provider fixes
		}
		release, serr := s.semFor(s.provSem, at.p.ID, s.provLimit(at.p)).acquire(actx, 2*time.Second)
		if serr != nil {
			acancel()
			if ctx.Err() != nil {
				ex.cancelled = true
				return ex
			}
			ex.pe = &lib.ProviderError{Class: lib.ClassRate, Detail: "provider concurrency limit"}
			if i < len(b.attempts)-1 {
				continue
			}
			break
		}
		if sink != nil {
			if err := sink.Open(StreamInfo{RunID: runID, ProviderName: at.p.Name, Model: at.model}); err != nil {
				release()
				acancel()
				ex.cancelled = true
				return ex
			}
		}
		var restorer *redact.StreamRestorer
		if b.rmap != nil && sink != nil {
			restorer = b.rmap.NewStreamRestorer()
		}
		onDelta := func(t string) error {
			if sink == nil {
				return nil
			}
			t = cleanOutput(t) // before the restorer, so an invisible character cannot hide inside a placeholder
			if restorer != nil {
				t = restorer.Write(t)
			}
			if t == "" {
				return nil
			}
			ex.delivered++
			return sink.Delta(t)
		}
		var res lib.Result
		var err error
		for try := 0; ; try++ {
			if in.Stream {
				res, err = cl.Stream(actx, at.body, onDelta)
			} else {
				res, err = cl.Chat(actx, at.body)
			}
			if err == nil {
				break
			}
			pe, _ := lib.AsProviderError(err)
			if pe == nil || chargeable(pe) {
				ex.failed = append(ex.failed, failedSend{att: at, text: res.Text})
			}
			if pe == nil || !pe.Retryable() || actx.Err() != nil || ex.delivered > 0 || res.Text != "" || try >= s.cfg.Retries {
				break
			}
			if !sleepCtx(actx, backoff(try, pe)) {
				break
			}
		}
		release()
		acancel()
		if restorer != nil && ex.delivered > 0 {
			if tail := restorer.Flush(); tail != "" {
				_ = sink.Delta(tail)
			}
		}
		if err == nil {
			ex.res, ex.used, ex.text = res, at, res.Text
			ex.pe, ex.err = nil, nil // an earlier provider's failure is history once a later one answers
			return ex
		}
		pe, ok := lib.AsProviderError(err)
		if !ok {
			pe = &lib.ProviderError{Class: lib.ClassUnreachable}
		}
		s.logFailure(at.p, "run", pe)
		ex.pe, ex.res, ex.used, ex.text = pe, res, at, res.Text
		if ctx.Err() != nil || pe.Class == lib.ClassCancelled { // the person (or the browser) went away
			ex.cancelled = true
			return ex
		}
		// Only what retrying can fix moves on, and only before any text reached the
		// person: a refusal by a key or a rule never does.
		if !pe.Retryable() || ex.delivered > 0 || res.Text != "" || i == len(b.attempts)-1 {
			break
		}
	}
	if ex.pe != nil {
		ex.err = &ops.Error{Code: ops.CodeProvider, Message: ex.pe.Message(), Data: map[string]any{"provider_class": ex.pe.Class}}
		if ex.pe.Status > 0 {
			ex.err.Data["status"] = ex.pe.Status
		}
		if ex.pe.Class == lib.ClassRate && ex.pe.RetryAfter > 0 {
			ex.err.Data["retry_after"] = ex.pe.RetryAfter
		}
	}
	return ex
}

func (s *Service) provLimit(p provider) int {
	if p.MaxConc > 0 {
		return p.MaxConc
	}
	return s.cfg.ProviderConcurrency
}

func backoff(try int, pe *lib.ProviderError) time.Duration {
	d := 400 * time.Millisecond << try
	if pe.RetryAfter > 0 {
		if ra := time.Duration(pe.RetryAfter) * time.Second; ra < 5*time.Second {
			d = ra
		} else {
			d = 5 * time.Second
		}
	}
	return min(d, 5*time.Second)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// ---- settlement -----------------------------------------------------------------------------------------------

// cleanOutput is the one filter every piece of provider output passes, streamed or whole,
// before anything else sees it: invalid UTF-8 becomes U+FFFD, and control characters that can
// hide or reorder text are removed (C0 controls other than newline and tab, DEL and the C1
// range; zero-width characters, the byte order mark and the bidirectional overrides and
// isolates), so accepting a suggestion into the manuscript cannot bring in text that reads
// differently from what it is.
func cleanOutput(t string) string {
	t = strings.ToValidUTF8(t, "\uFFFD")
	clean := true
	for _, r := range t {
		if dropped(r) {
			clean = false
			break
		}
	}
	if clean {
		return t
	}
	var b strings.Builder
	b.Grow(len(t))
	for _, r := range t {
		if !dropped(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func dropped(r rune) bool {
	switch {
	case r == '\n' || r == '\t':
		return false
	case r < 0x20, r >= 0x7f && r <= 0x9f:
		return true
	}
	// Every other invisible or direction-changing character, by the one shared rule:
	// it keeps ZWJ, ZWNJ and the emoji variation selectors, so a model's emoji
	// sequences and Persian or Indic words arrive whole.
	return text.IsInvisible(r)
}

// finish turns the execution into the run's record and the suggestion.
func (s *Service) finish(ctx context.Context, d *db.DB, p ops.Principal, b *built, in RunIn, run runRow, ex execResult, took time.Duration) RunOut {
	out := RunOut{RunID: run.ID, Recipe: b.recipe.ID, RecipeVersion: b.recipe.Version, Kind: string(b.recipe.Result), Currency: run.Currency,
		LatencyMS: took.Milliseconds(), Attempts: ex.attempts, CostKnown: true}
	status, errCode := "ok", ""
	text := cleanOutput(ex.text)
	switch {
	case ex.cancelled:
		status = "cancelled"
		out.Incomplete = true
	case ex.pe != nil && text == "":
		status, errCode = "error", ex.pe.Class
	case ex.pe != nil:
		status, errCode = "error", ex.pe.Class
		out.Incomplete = true
		out.ErrorClass, out.ErrorMessage = ex.pe.Class, ex.pe.Message()
	case ex.res.Truncated:
		out.Incomplete = true
		out.Warnings = append(out.Warnings, "The answer was cut off at the length limit.")
	}
	if ex.err != nil && ex.err.Code != ops.CodeProvider && text == "" { // a key that could not be read
		status, errCode = "error", "key_unreadable"
	}
	if text != "" && b.rmap != nil {
		var n int
		text, n = b.rmap.Restore(text)
		out.Unrestored = n
		if n > 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("%d placeholder(s) in the answer did not match any name and were left as they are.", n))
		}
	}
	if ex.used != nil {
		out.Provider, out.ProviderName, out.Model, out.Local = ex.used.p.ID, ex.used.p.Name, ex.used.model, ex.used.p.Local
	}
	// Parse into the recipe's shape. A parse that fails keeps the raw text and says so.
	out.Text = text
	if text != "" && status != "error" || (status == "error" && text != "") {
		out.Data, out.Warnings = s.parse(b, text, out.Warnings)
	}
	if text == "" && status == "ok" {
		out.Warnings = append(out.Warnings, "The model returned nothing. The run is recorded; you can run it again.")
	}
	out.Status = status
	if status == "error" && text == "" {
		out.Status = "error"
	}

	// Usage and cost. Every send that reached a provider counts, answered or not: a call that
	// timed out or was cancelled, and a retry that was thrown away, were sent whole, so each is
	// charged at least what was sent (the input estimate, and an estimate of whatever arrived),
	// at that provider's price. A call that gave an answer is charged the provider's own counts
	// when it reported them.
	var in_, out_ int
	var totalMicros, instMicros, ownMicros int64
	costKnown := true
	account := func(a *builtAttempt, in, outTok int, reported *int64) {
		in_ += in
		out_ += outTok
		var m int64
		switch {
		case reported != nil:
			m = *reported
		case a.known:
			m = a.price.Cost(in, outTok)
		default:
			costKnown = false
		}
		totalMicros += m
		switch {
		case !a.p.instance():
			ownMicros += m // the person's own provider pays
		case a.p.Local:
			// a local one costs nothing against the money caps
		default:
			instMicros += m
		}
	}
	for _, fs := range ex.failed {
		account(fs.att, b.inTokens, lib.EstimateTokens(fs.text), nil)
	}
	if ex.used != nil && ex.pe == nil && !ex.cancelled && ex.err == nil {
		a := ex.used
		inT, outT := b.inTokens, lib.EstimateTokens(ex.text)
		if ex.res.Usage.Reported {
			inT, outT, out.UsageReported = ex.res.Usage.InputTokens, ex.res.Usage.OutputTokens, true
		}
		account(a, inT, outT, ex.res.Usage.CostMicros)
	}
	costMicros := totalMicros
	out.InputTokens, out.OutputTokens, out.CostMicros, out.CostKnown = in_, out_, costMicros, costKnown
	own := ex.used != nil && !ex.used.p.instance()
	charged := instMicros
	sum := sha256.Sum256([]byte(out.Text))
	outHash := ""
	if out.Text != "" {
		outHash = hex.EncodeToString(sum[:])
	}
	fctx := context.WithoutCancel(ctx)
	settle := func(tx *sql.Tx) error {
		provID, model, local := run.ProviderID, run.Model, 0
		if ex.used != nil {
			provID, model, local = ex.used.p.ID, ex.used.model, b2i(ex.used.p.Local)
		}
		if _, err := tx.Exec(`UPDATE ai_runs SET status=?, error_code=?, provider_id=?, model=?, input_tokens=?, output_tokens=?, usage_reported=?, cost_micros=?, cost_known=?, own_cost=?,
			reserved_micros=0, reserved_tokens=0, local=?, attempts=?, duration_ms=?, output_hash=?, finished_at=? WHERE id=?`,
			status, errCode, provID, model, in_, out_, b2i(out.UsageReported), charged, b2i(costKnown), b2i(own), local, ex.attempts, took.Milliseconds(), outHash, now(), run.ID); err != nil {
			return err
		}
		if in_+out_ > 0 || charged > 0 {
			day := run.CreatedAt[:10]
			if _, err := tx.Exec(`INSERT INTO ai_usage_daily(user_id, project_id, day, runs, input_tokens, output_tokens, cost_micros, own_micros) VALUES (?,?,?,?,?,?,?,?)
				ON CONFLICT(user_id, project_id, day) DO UPDATE SET runs = runs + 1, input_tokens = input_tokens + excluded.input_tokens, output_tokens = output_tokens + excluded.output_tokens,
					cost_micros = cost_micros + excluded.cost_micros, own_micros = own_micros + excluded.own_micros`,
				run.UserID, run.ProjectID, day, 1, in_, out_, charged, ownMicros); err != nil {
				return err
			}
		}
		detail := map[string]any{"recipe": b.recipe.ID, "status": status, "provider_id": provID, "model": model, "input_tokens": in_, "output_tokens": out_,
			"cost_micros": costMicros, "attempts": ex.attempts, "local": local == 1, "project_id": run.ProjectID, "via": run.Via}
		if errCode != "" {
			detail["error"] = errCode
		}
		return s.flush(tx, run.UserID,
			[]ops.AuditEntry{{Action: "ai.run", TargetType: "ai_run", TargetID: run.ID, Detail: detail}},
			[]ops.Event{{Type: "ai.run.finished", ProjectID: run.ProjectID, Payload: map[string]any{"run_id": run.ID, "status": status, "input_tokens": in_, "output_tokens": out_, "cost_micros": costMicros}}})
	}
	werr := d.WithTx(fctx, settle)
	if werr != nil { // once more: a busy writer is the usual cause
		s.log.Warn("ai: settling a run failed, trying once more", "run", run.ID, "err", werr)
		werr = d.WithTx(fctx, settle)
	}
	if werr != nil {
		// The cost of a call that happened must not vanish with the row that was to record it. The
		// run is marked unsettled and keeps what was reserved for it, which the budget counts as
		// spent for the rest of the month (see sums); nothing is released.
		s.log.Error("ai: settling a run failed; it is marked unsettled and its reservation counts as spent", "run", run.ID, "err", werr)
		if merr := d.WithTx(fctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE ai_runs SET status = 'unsettled', error_code = 'unsettled', duration_ms = ?, finished_at = ? WHERE id = ?`, took.Milliseconds(), now(), run.ID)
			return err
		}); merr != nil {
			s.log.Error("ai: a run could not even be marked unsettled; its reservation stays held", "run", run.ID, "err", merr)
		}
		out.Warnings = append(out.Warnings, "The cost of this run could not be recorded; it is counted at its worst case against the budget.")
	} else if w, ok := s.env.Sink.(interface{ Wake() }); ok && s.env != nil {
		w.Wake()
	}
	s.deliver(ctx, d, p, b, in, run, &out)
	return out
}

// parse runs the recipe's parser, never trusting it: a panic or a bad result
// leaves the raw text and a warning.
func (s *Service) parse(b *built, text string, warnings []string) (data json.RawMessage, w []string) {
	w = warnings
	if b.recipe.Parse == nil {
		return nil, w
	}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("ai: a recipe's parser panicked", "recipe", b.recipe.ID, "panic", fmt.Sprint(r))
			data = nil
			w = append(w, "The answer could not be read in the recipe's format; the text is shown as it came.")
		}
	}()
	parsed, err := b.recipe.Parse(lib.ParseContext{Params: b.params, Chips: b.chips, Raw: text})
	if err != nil {
		return nil, append(w, "The answer could not be read in the recipe's format; the text is shown as it came.")
	}
	w = append(w, parsed.Warnings...)
	if len(parsed.Data) > 0 {
		if json.Valid(parsed.Data) {
			data = parsed.Data
		} else {
			w = append(w, "The recipe produced data that is not valid JSON; the text is shown as it came.")
		}
	}
	return data, w
}

// ---- cancel and the run log ------------------------------------------------------------------------------------

// CancelIn names a run to cancel.
type CancelIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	RunID     string `json:"run_id" desc:"A run id from the stream's first event."`
}

// ProjectRef lets the registry authorize by membership.
func (in CancelIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *CancelIn) Validate() error {
	if !strings.HasPrefix(in.RunID, "air_") || len(in.RunID) > 40 {
		return ops.Invalid("run_id", "That is not a run id.")
	}
	return nil
}

// CancelOut says whether a run was running.
type CancelOut struct {
	Cancelled bool `json:"cancelled" desc:"False when the run had already finished."`
}

func cancelRun(s *Service) func(*ops.Ctx, CancelIn) (CancelOut, error) {
	return func(c *ops.Ctx, in CancelIn) (CancelOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return CancelOut{}, err
		}
		s.mu.Lock()
		a, ok := s.active[in.RunID]
		s.mu.Unlock()
		if !ok || a.project != in.ProjectID {
			return CancelOut{}, nil
		}
		// Your own run; or any run on a project you own.
		if a.userID != c.P.UserID {
			if role, ok := c.Env.ProjectRole(c, c.P.UserID, in.ProjectID); !ok || role != ops.ProjectOwner && c.P.Role != ops.RoleAdmin {
				return CancelOut{}, ops.NotFound("run")
			}
		}
		a.cancel()
		return CancelOut{Cancelled: true}, nil
	}
}

// RunListIn lists runs.
type RunListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Limit     int    `query:"limit" desc:"Default 50, at most 200."`
	Cursor    string `query:"cursor" desc:"The next_cursor of the previous page."`
}

// ProjectRef lets the registry authorize by membership.
func (in RunListIn) ProjectRef() string { return in.ProjectID }

// RunRecord is a row of the run log: no prompt, no answer.
type RunRecord struct {
	ID           string          `json:"id"`
	UserID       string          `json:"user_id"`
	DocumentID   string          `json:"document_id,omitempty"`
	Recipe       string          `json:"recipe"`
	ProviderID   string          `json:"provider_id"`
	Model        string          `json:"model"`
	Via          string          `json:"via"`
	Status       string          `json:"status"`
	ErrorCode    string          `json:"error_code,omitempty"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
	Reported     bool            `json:"usage_reported"`
	CostMicros   int64           `json:"cost_micros"`
	CostKnown    bool            `json:"cost_known"`
	Local        bool            `json:"local"`
	Attempts     int             `json:"attempts"`
	DurationMS   int64           `json:"duration_ms"`
	PayloadHash  string          `json:"payload_hash,omitempty"`
	OutputHash   string          `json:"output_hash,omitempty"`
	Scope        json.RawMessage `json:"scope" desc:"What was sent, by chip: kind, label and token count. Never the text."`
	CreatedAt    string          `json:"created_at"`
	FinishedAt   string          `json:"finished_at,omitempty"`
}

// RunListOut is a page of the run log.
type RunListOut struct {
	Items      []RunRecord `json:"items"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// listRuns skips the kill switch: the run log is a record of what was sent, and
// reading it sends nothing.
func listRuns(s *Service) func(*ops.Ctx, RunListIn) (RunListOut, error) {
	return func(c *ops.Ctx, in RunListIn) (RunListOut, error) {
		limit := in.Limit
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		q := `SELECT id, user_id, document_id, recipe, provider_id, model, via, status, error_code, input_tokens, output_tokens, usage_reported, cost_micros, cost_known, local, attempts,
			duration_ms, payload_hash, output_hash, scope_json, created_at, finished_at FROM ai_runs WHERE project_id = ?`
		args := []any{in.ProjectID}
		// Owners (and administrators) see everyone's runs on the project; editors their own.
		if role, ok := c.Env.ProjectRole(c, c.P.UserID, in.ProjectID); !ok || (role != ops.ProjectOwner && c.P.Role != ops.RoleAdmin) {
			q += ` AND user_id = ?`
			args = append(args, c.P.UserID)
		}
		if in.Cursor != "" {
			q += ` AND created_at < ?`
			args = append(args, in.Cursor)
		}
		q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
		args = append(args, limit+1)
		rows, err := c.DB().Read().QueryContext(c, q, args...)
		if err != nil {
			return RunListOut{}, ops.Internal(err)
		}
		defer rows.Close()
		out := RunListOut{Items: []RunRecord{}}
		for rows.Next() {
			var r RunRecord
			var rep, known, local int
			var scope string
			if err := rows.Scan(&r.ID, &r.UserID, &r.DocumentID, &r.Recipe, &r.ProviderID, &r.Model, &r.Via, &r.Status, &r.ErrorCode, &r.InputTokens, &r.OutputTokens, &rep, &r.CostMicros, &known, &local,
				&r.Attempts, &r.DurationMS, &r.PayloadHash, &r.OutputHash, &scope, &r.CreatedAt, &r.FinishedAt); err != nil {
				return RunListOut{}, ops.Internal(err)
			}
			r.Reported, r.CostKnown, r.Local = rep == 1, known == 1, local == 1
			r.Scope = json.RawMessage(scope)
			if len(out.Items) == limit {
				out.NextCursor = out.Items[len(out.Items)-1].CreatedAt
				break
			}
			out.Items = append(out.Items, r)
		}
		return out, nil
	}
}

package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/ai/redact"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Embeddings go through the same provider layer as every other AI call: the
// same sealed keys, the same connection guard, the same project policy, the
// same budgets and the same run log. This file is the narrow seam the semantic
// index (internal/domain/semantic) uses; it holds no index logic.
//
// What it decides, in this order, before a byte of text leaves:
//
//   - AI is on for the server (the kill switch and the administrator's setting);
//   - the project's policy is not "off" (an owner who turned AI off for a project
//     has turned off every request that carries its text, this one too), and an
//     API key may use it only where the policy says agents may use AI;
//   - the provider exists, is switched on, has an embedding route (not OpenCode),
//     is an instance provider or the owner's own, is on the policy's list and
//     model list, and is the endpoint the owner confirmed (its address, its
//     local flag and its model are what the owner agreed to; an administrator
//     who changes the address pauses the index until the owner confirms again);
//   - a local-only project reaches only local endpoints, and any endpoint that is
//     not declared local is "remote" and needs the owner's separate typed
//     confirmation (EmbedSpec.AllowRemote), because the whole manuscript goes to it;
//   - the caps (instance, person, project, the owner's own) have room.

// EmbedPurposes.
const (
	EmbedIndex = "index"
	EmbedQuery = "query"
)

// EmbedSpec says what an embedding call is for and who stands behind it.
type EmbedSpec struct {
	ProjectID string
	// UserID is whose budget is charged and whose personal provider may be used:
	// the owner who switched the index on (for a query, the person asking).
	UserID string
	// ChargeUserID is whose caps and usage record the call is charged to when it is
	// not the same person: a collaborator's search is charged to them. A personal
	// provider is always charged to its owner (UserID), whoever asks.
	ChargeUserID string
	ProviderID   string
	Model        string
	// Purpose is EmbedIndex or EmbedQuery.
	Purpose string
	// AllowRemote is the owner's typed confirmation that a remote endpoint
	// receives the manuscript.
	AllowRemote bool
	// ExpectKey, when set, is the endpoint key the owner confirmed (see
	// EmbedTarget.Key); a provider that has changed since is refused.
	ExpectKey string
	// ViaAPIKey is true when an API key made the request.
	ViaAPIKey bool
	// Documents counts how many documents the texts come from, for the log.
	Documents int
	// CheckRedaction makes the call refuse (redaction_changed) when the project's redaction setting is no longer
	// Redaction: the texts were redacted under one setting and must not be sent under another.
	CheckRedaction bool
	Redaction      string
	// Guard is called after the call has its place with the provider and immediately before the request is sent,
	// the last moment anything can be stopped. It is where a caller re-reads whether what it is about to send may
	// still be sent (the index is on, the consent stands, the documents are eligible and unchanged). An error from it
	// stops the request, and nothing is sent.
	Guard func(ctx context.Context) error
}

// EmbedTarget describes where embeddings would go, without any network call.
type EmbedTarget struct {
	ProviderID string
	Name       string
	Kind       string
	Model      string
	Scope      string // instance or user
	Local      bool
	// Host is the endpoint's host and port. It is for the person confirming and
	// for administrators: a caller who may not see an instance provider's address
	// (see Provider.BaseURL) does not get it for a local endpoint.
	Host string
	// Key identifies what was agreed to: provider, normalised address, local flag, model.
	Key string
	// PriceKnown and InputMicros are the cost of a million input tokens (0 for a local endpoint).
	PriceKnown  bool
	InputMicros int64
	// RequireLocal is true when the project's policy is local-only.
	RequireLocal bool
	// Mode is the project's policy mode, and Redaction its redaction setting (off, entities or names).
	Mode      string
	Redaction string
}

// EmbedGate refuses while AI is off for the server.
func (s *Service) EmbedGate(ctx context.Context, d *db.DB) error { return s.gate(ctx, d) }

// EmbedRefused is a refusal by policy, configuration or budget: nothing was sent
// and trying again changes nothing until a person changes something.
type EmbedRefused struct{ Err *ops.Error }

func (e *EmbedRefused) Error() string { return e.Err.Message }

// Code is the refusal's short code (policy_off, not_local, remote_not_allowed,
// endpoint_changed, provider_unusable, agents_not_allowed, key_unreadable,
// budget_exceeded, price_unknown).
func (e *EmbedRefused) Code() string {
	if e.Err == nil || e.Err.Data == nil {
		return ""
	}
	c, _ := e.Err.Data["ai_code"].(string)
	return c
}

func embedRefuse(code, msg string) *EmbedRefused { return &EmbedRefused{Err: refuse(code, msg)} }

// EmbedFailure classifies an error from EmbedResolve or Embed: the class, and
// whether trying again later can help.
func EmbedFailure(err error) (class string, retryable bool) {
	var r *EmbedRefused
	if errors.As(err, &r) {
		if c := r.Code(); c != "" {
			return c, false
		}
		return "refused", false
	}
	if pe, ok := lib.AsProviderError(err); ok {
		return pe.Class, pe.Retryable()
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", true
	}
	return "error", true
}

// EndpointKey is what an owner agrees to when switching an index on.
func endpointKey(p provider, model string) string {
	return p.ID + "|" + normAddr(p.BaseURL) + "|local=" + b2s(p.Local) + "|" + model
}

func b2s(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

// embedProvider loads the provider an index names. It is read by id, not by the
// person asking, because the worker has no person: an instance provider is open
// to every project the policy lets use it, and a personal provider only to the
// person who owns it.
func (s *Service) embedProvider(ctx context.Context, d *db.DB, spec EmbedSpec) (provider, *EmbedRefused) {
	p, err := scanProvider(d.Read().QueryRowContext(ctx, `SELECT `+providerCols+` FROM ai_providers WHERE id = ?`, spec.ProviderID))
	if err != nil || !p.Enabled || (!p.instance() && p.UserID != spec.UserID) {
		return provider{}, embedRefuse("provider_unusable", "The provider for this index is gone, switched off or no longer yours to use. Choose a provider again in the project's search settings.")
	}
	if !lib.SupportsEmbeddings(p.Kind) {
		return provider{}, embedRefuse("provider_unusable", "That kind of provider has no embedding route. Use an Ollama or an OpenAI-compatible endpoint.")
	}
	return p, nil
}

// EmbedResolve checks everything that needs no network and says where
// embeddings would go. A preview calls it with no ExpectKey; every real call
// goes through it again with the key that was confirmed.
func (s *Service) EmbedResolve(ctx context.Context, d *db.DB, spec EmbedSpec) (EmbedTarget, error) {
	pol, err := loadPolicy(ctx, d.Read(), spec.ProjectID)
	if err != nil {
		return EmbedTarget{}, err
	}
	if pol.Mode != ModeAsk && pol.Mode != ModeLocalOnly {
		return EmbedTarget{}, embedRefuse("policy_off", "AI is off for this project, so nothing is sent to an embedding endpoint either. The project's owner can turn it on in the project's AI settings.")
	}
	if spec.ViaAPIKey && !pol.AgentsMayUse {
		return EmbedTarget{}, embedRefuse("agents_not_allowed", "This project's owner has not allowed agents (API keys) to use AI on it.")
	}
	p, ref := s.embedProvider(ctx, d, spec)
	if ref != nil {
		return EmbedTarget{}, ref
	}
	allowed := false
	for _, id := range pol.Providers {
		allowed = allowed || id == p.ID
	}
	switch {
	case len(pol.Providers) > 0 && !allowed:
		return EmbedTarget{}, embedRefuse("provider_unusable", "This project's AI policy does not allow that provider.")
	case !p.instance() && !pol.AllowPersonal && !allowed:
		return EmbedTarget{}, embedRefuse("provider_unusable", "This project's owner has not allowed personal providers.")
	}
	if ms := pol.Models[p.ID]; len(ms) > 0 {
		ok := false
		for _, m := range ms {
			ok = ok || m == spec.Model
		}
		if !ok {
			return EmbedTarget{}, embedRefuse("provider_unusable", "This project's AI policy does not allow that model.")
		}
	}
	if !lib.ValidModel(spec.Model) {
		return EmbedTarget{}, embedRefuse("provider_unusable", "That is not a usable model name.")
	}
	t := EmbedTarget{ProviderID: p.ID, Name: p.Name, Kind: p.Kind, Model: spec.Model, Scope: scopeOf(p), Local: p.Local, Host: hostOf(p.BaseURL),
		Key: endpointKey(p, spec.Model), RequireLocal: pol.Mode == ModeLocalOnly, Mode: pol.Mode}
	if price, known, perr := s.priceFor(ctx, d, p, spec.Model); perr == nil {
		t.PriceKnown, t.InputMicros = known, price.InputMicros
	}
	t.Redaction = pol.Redaction
	if spec.CheckRedaction && pol.Redaction != spec.Redaction {
		return t, embedRefuse("redaction_changed", "The project's redaction setting changed while text was being prepared, so nothing more is sent from that preparation.")
	}
	if spec.ExpectKey != "" && spec.ExpectKey != t.Key {
		return t, embedRefuse("endpoint_changed", "The provider's address or model changed since the owner confirmed this index. The index is paused until the owner reviews and confirms it again.")
	}
	if t.RequireLocal && !p.Local {
		return t, embedRefuse("not_local", "This project is local-only and the provider is not declared local, so no text is sent to it.")
	}
	if !p.Local && !spec.AllowRemote {
		return t, embedRefuse("remote_not_allowed", "This provider is not on this machine or its network. The owner has to confirm, with a typed confirmation, that the whole manuscript may be sent to it.")
	}
	return t, nil
}

// Embed sends texts to the endpoint and returns their vectors. One call is one
// request, at most lib.MaxEmbedBatch texts. It is accounted per request: the
// caps are checked first, and an already-finished run row and the day's usage
// are written afterwards, so a long index never holds a "running" reservation
// that would block a person's own requests under a cap. Concurrent requests
// can therefore overshoot a cap by at most one request each, which is why a
// cap is checked again before every request.
//
// The returned error is an *EmbedRefused (nothing was sent), a
// *lib.ProviderError (the endpoint failed; see EmbedFailure) or a context error.
func (s *Service) Embed(ctx context.Context, d *db.DB, spec EmbedSpec, inputs []string) (lib.EmbedResult, error) {
	if err := s.gate(ctx, d); err != nil {
		return lib.EmbedResult{}, embedRefuse("ai_off", "AI is switched off on this server.")
	}
	t, err := s.EmbedResolve(ctx, d, spec)
	if err != nil {
		return lib.EmbedResult{}, err
	}
	pol, err := loadPolicy(ctx, d.Read(), spec.ProjectID)
	if err != nil {
		return lib.EmbedResult{}, err
	}
	p, ref := s.embedProvider(ctx, d, spec)
	if ref != nil {
		return lib.EmbedResult{}, ref
	}
	var estTok int
	bytes := 0
	for _, x := range inputs {
		estTok += lib.EstimateTokens(x)
		bytes += len(x)
	}
	est := reserveEst{Tokens: int64(estTok)}
	if p.instance() && !p.Local {
		est.Paid, est.PriceKnown = true, t.PriceKnown
		est.Micros = lib.Price{InputMicros: t.InputMicros}.Cost(estTok, 0)
	}
	if spec.ChargeUserID == "" || !p.instance() {
		spec.ChargeUserID = spec.UserID
	}
	bres, err := s.budgetCheck(ctx, d.Read(), spec.ChargeUserID, spec.ProjectID, pol, est, false)
	if err != nil {
		return lib.EmbedResult{}, err
	}
	if bres.Refusal != nil {
		return lib.EmbedResult{}, &EmbedRefused{Err: bres.Refusal}
	}
	cl, cerr := s.client(p, t.RequireLocal)
	if cerr != nil {
		return lib.EmbedResult{}, embedRefuse("key_unreadable", cerr.Message)
	}
	wait := 2 * time.Minute
	if spec.Purpose == EmbedQuery {
		wait = 5 * time.Second
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	rel, serr := s.semFor(s.provSem, p.ID, s.provLimit(p)).acquire(ctx, wait)
	if serr != nil {
		if errors.Is(serr, errBusy) {
			return lib.EmbedResult{}, &lib.ProviderError{Class: lib.ClassRate}
		}
		return lib.EmbedResult{}, serr
	}
	var stopped error // the caller's guard stopped a request after the first (an old Ollama sends one text at a time)
	before := func(c context.Context) error {
		if spec.Guard == nil {
			return nil
		}
		if err := spec.Guard(c); err != nil {
			stopped = err
			return err
		}
		return nil
	}
	if spec.Guard != nil {
		if gerr := spec.Guard(ctx); gerr != nil {
			rel()
			if _, isRefusal := gerr.(*EmbedRefused); isRefusal {
				return lib.EmbedResult{}, gerr
			}
			return lib.EmbedResult{}, embedRefuse("consent_changed", "What was about to be sent may no longer be sent, so nothing was.")
		}
	}
	started := time.Now()
	res, callErr := cl.EmbedChecked(ctx, spec.Model, inputs, before)
	rel()
	took := time.Since(started)

	var pe *lib.ProviderError
	errClass := ""
	inTok := 0
	switch {
	case callErr == nil:
		inTok = estTok
		if res.Reported {
			inTok = res.InputTokens
		}
	case errors.As(callErr, &pe):
		errClass = pe.Class
		if chargeable(pe) { // the endpoint had the text: charge at least what was sent
			inTok = estTok
		}
	default:
		errClass = "cancelled"
	}
	if stopped != nil {
		// Some texts left, the rest were held back: charged as if all had (the estimate is the bound), and refused.
		inTok = estTok
		s.settleEmbed(ctx, d, spec, p, t, "cancelled", inTok, len(inputs), bytes, took)
		if r, ok := stopped.(*EmbedRefused); ok {
			return lib.EmbedResult{}, r
		}
		return lib.EmbedResult{}, embedRefuse("consent_changed", "What was about to be sent may no longer be sent, so nothing more was.")
	}
	s.settleEmbed(ctx, d, spec, p, t, errClass, inTok, len(inputs), bytes, took)
	if callErr != nil {
		if pe != nil {
			s.logFailure(p, "embed", pe)
			return lib.EmbedResult{}, pe
		}
		return lib.EmbedResult{}, callErr
	}
	return res, nil
}

// settleEmbed writes the finished run row and the day's usage in one writer
// transaction. Input tokens only: an embedding has no output.
func (s *Service) settleEmbed(ctx context.Context, d *db.DB, spec EmbedSpec, p provider, t EmbedTarget, errClass string, inTok, texts, bytes int, took time.Duration) {
	var micros int64
	known := true
	switch {
	case p.Local:
	case t.PriceKnown:
		micros = lib.Price{InputMicros: t.InputMicros}.Cost(inTok, 0)
	default:
		known = false
	}
	own := !p.instance()
	var instMicros, ownMicros int64
	if own {
		ownMicros = micros
	} else if !p.Local {
		instMicros = micros
	}
	status := "ok"
	if errClass != "" {
		status = "error"
		if errClass == "cancelled" {
			status = "cancelled"
		}
	}
	scope, _ := json.Marshal(map[string]any{"purpose": spec.Purpose, "texts": texts, "bytes": bytes, "documents": spec.Documents})
	via := "session"
	if spec.ViaAPIKey {
		via = "apikey"
	}
	at := now()
	rec := func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO ai_runs(id, user_id, project_id, document_id, recipe, recipe_version, provider_id, model, via, status, error_code, input_tokens, output_tokens,
				usage_reported, cost_micros, cost_known, own_cost, currency, local, attempts, duration_ms, scope_json, created_at, finished_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			db.NewID("air"), spec.ChargeUserID, spec.ProjectID, "", "embed."+spec.Purpose, 0, p.ID, spec.Model, via, status, errClass, inTok, 0,
			0, instMicros, b2i(known), b2i(own), s.currencyOf(ctx, tx), b2i(p.Local), 1, took.Milliseconds(), string(scope), at, at); err != nil {
			return err
		}
		if inTok > 0 || instMicros > 0 || ownMicros > 0 {
			_, err := tx.Exec(`INSERT INTO ai_usage_daily(user_id, project_id, day, runs, input_tokens, output_tokens, cost_micros, own_micros) VALUES (?,?,?,?,?,?,?,?)
				ON CONFLICT(user_id, project_id, day) DO UPDATE SET runs = runs + 1, input_tokens = input_tokens + excluded.input_tokens,
					cost_micros = cost_micros + excluded.cost_micros, own_micros = own_micros + excluded.own_micros`,
				spec.ChargeUserID, spec.ProjectID, at[:10], 1, inTok, 0, instMicros, ownMicros)
			return err
		}
		return nil
	}
	fctx := context.WithoutCancel(ctx)
	err := d.WithTx(fctx, rec)
	if err != nil {
		err = d.WithTx(fctx, rec)
	}
	if err != nil {
		s.log.Error("ai: an embedding request could not be recorded", "project", spec.ProjectID, "err", err)
	}
}

func (s *Service) currencyOf(ctx context.Context, q queryer) string { return s.currencyQ(ctx, q) }

// CanonText is the text of a document as it may be sent to a provider: the chosen
// wording only. The alternatives comment, ghosted text and the author's
// %%private notes%% are removed, and a %% or a ghost tag that is never closed
// removes the rest of its paragraph, so a half-typed note is never sent. Every
// request that carries manuscript text, an embedding included, is cut from
// this, so there is one rule for what may leave.
func CanonText(content string) string { return canonText(content) }

// Redactor applies a project's redaction setting to text about to be embedded: the same map, made by the same
// function, as the one an AI request uses (see newRedactMap). With the setting off it changes nothing.
type Redactor struct {
	m           *redact.Map
	mode        string
	fingerprint string
}

// NewRedactor makes the redactor for a project's current setting. Fingerprint is "" when redaction is off.
func (s *Service) NewRedactor(ctx context.Context, d *db.DB, projectID string) (*Redactor, error) {
	pol, err := loadPolicy(ctx, d.Read(), projectID)
	if err != nil {
		return nil, err
	}
	if pol.Redaction == RedactOff || pol.Redaction == "" {
		return &Redactor{mode: RedactOff}, nil
	}
	m, fp, err := newRedactMap(ctx, d, projectID, pol.Redaction)
	if err != nil {
		return nil, err
	}
	return &Redactor{m: m, mode: pol.Redaction, fingerprint: fp}, nil
}

// Mode is the setting: off, entities or names.
func (r *Redactor) Mode() string { return r.mode }

// Fingerprint identifies the setting and every name it hides; "" when off.
func (r *Redactor) Fingerprint() string { return r.fingerprint }

// Learn lets the "names" setting find the capitalised names of a text, so that its placeholders are the same
// across the chunks of a document.
func (r *Redactor) Learn(texts ...string) {
	if r.m != nil {
		r.m.Learn(texts...)
	}
}

// Apply returns the text as it may be sent.
func (r *Redactor) Apply(text string) string {
	if r.m == nil {
		return text
	}
	return r.m.Apply(text)
}

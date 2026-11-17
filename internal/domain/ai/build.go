package ai

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/ai/redact"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// TurnIn is one earlier message of a chat.
type TurnIn struct {
	Role    string `json:"role" enum:"user,assistant"`
	Content string `json:"content"`
}

// RunIn is a request to the AI, the same for the preview and for the run: a
// preview with these fields gives a payload hash and a nonce, and a run with
// the same fields plus that hash and nonce is refused (409) if building the
// payload now gives anything else.
type RunIn struct {
	ProjectID   string            `path:"id" json:"-" desc:"Project id."`
	Recipe      string            `json:"recipe" desc:"The recipe id; ai.recipe.list lists them."`
	DocumentID  string            `json:"document_id,omitempty" desc:"The document the request is about."`
	Selection   *Selection        `json:"selection,omitempty" desc:"A range of that document."`
	Chips       []ChipSpec        `json:"chips,omitempty" desc:"Exactly what to include. Omit it to get the recipe's default chips; send an empty list to include none."`
	Params      map[string]string `json:"params,omitempty" desc:"The recipe's parameters."`
	Turns       []TurnIn          `json:"turns,omitempty" desc:"Earlier messages of a chat, oldest first."`
	Provider    string            `json:"provider,omitempty" desc:"Use this provider alone (no chain). Empty: the project's or the instance's chain, then the first usable provider."`
	Model       string            `json:"model,omitempty" desc:"With provider: the model. Empty: the provider's default."`
	MaxTokens   int               `json:"max_tokens,omitempty" desc:"Ask for a shorter answer than the recipe's limit. It is clamped to the recipe's, the model's and the server's limits."`
	Stream      bool              `json:"stream,omitempty" desc:"The request will be sent as a stream (ai/run/stream). It changes the bytes sent, so preview with the same value."`
	Nonce       string            `json:"nonce,omitempty" desc:"Preview: leave empty to get one. Run: the nonce the preview returned."`
	PayloadHash string            `json:"payload_hash,omitempty" desc:"Run: the payload_hash the preview returned. A run needs it."`
}

// ProjectRef lets the registry authorize by membership.
func (in RunIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape. It does nothing that reads the database or needs
// the rate allowance, so a malformed request costs nothing.
func (in *RunIn) Validate() error {
	in.Recipe = strings.TrimSpace(in.Recipe)
	if in.Recipe == "" {
		return ops.Invalid("recipe", "Name the recipe. ai.recipe.list lists them.")
	}
	if len(in.Recipe) > 40 {
		return ops.Invalid("recipe", "Unknown recipe.")
	}
	if len(in.DocumentID) > 60 || len(in.Provider) > 60 || len(in.Model) > lib.MaxModelLen || (in.Model != "" && !lib.ValidModel(in.Model)) {
		return ops.Invalid("document_id", "An id or a model name is too long or malformed.")
	}
	if in.Provider != "" {
		if e := providerID(in.Provider); e != nil {
			return ops.Invalid("provider", "That is not a provider id.")
		}
	}
	if in.Model != "" && in.Provider == "" {
		return ops.Invalid("model", "Name the provider with the model.")
	}
	if len(in.Chips) > 30 {
		return ops.Invalid("chips", "At most 30 pieces of context.")
	}
	for i, c := range in.Chips {
		if !lib.ValidChipKind(c.Kind) {
			return ops.Invalid(fmt.Sprintf("chips[%d].kind", i), "Unknown chip kind.")
		}
		if len(c.IDs) > 60 || len(c.Text) > 4000 || len(c.DocumentID) > 60 {
			return ops.Invalid(fmt.Sprintf("chips[%d]", i), "Too many ids, or an instruction over 4,000 characters.")
		}
	}
	if len(in.Params) > 20 {
		return ops.Invalid("params", "Too many parameters.")
	}
	if len(in.Turns) > 40 {
		return ops.Invalid("turns", "At most 40 earlier messages.")
	}
	for _, t := range in.Turns {
		if (t.Role != lib.RoleUser && t.Role != lib.RoleAssistant) || len(t.Content) > 32<<10 {
			return ops.Invalid("turns", "A turn is from the user or the assistant, up to 32 KB.")
		}
	}
	if in.MaxTokens < 0 || in.MaxTokens > 1<<31-1 {
		return ops.Invalid("max_tokens", "Not a token count.")
	}
	if in.Nonce != "" && !lib.ValidNonce(in.Nonce) {
		return ops.Invalid("nonce", "A nonce is the 32 hex characters ai.context.preview returns.")
	}
	if in.PayloadHash != "" && len(in.PayloadHash) != 64 {
		return ops.Invalid("payload_hash", "A payload hash is the 64 hex characters ai.context.preview returns.")
	}
	return nil
}

// Decision says whether the request may go ahead.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Code    string `json:"code,omitempty" desc:"policy_off, action_not_allowed, agents_not_allowed, no_provider, too_large, over_context, budget_exceeded, price_unknown."`
	Message string `json:"message,omitempty"`
}

// ChipInfo describes a chip in the preview.
type ChipInfo struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Source   string `json:"source,omitempty"`
	Tokens   int    `json:"tokens" desc:"Estimated, after redaction."`
	Bytes    int    `json:"bytes"`
	Redacted bool   `json:"redacted" desc:"Names in it are replaced by placeholders."`
}

// Attempt is one provider the request may go to, with the exact body it would
// receive.
type Attempt struct {
	ProviderID    string `json:"provider_id"`
	ProviderName  string `json:"provider_name"`
	ProviderKind  string `json:"provider_kind"`
	Scope         string `json:"scope" enum:"instance,user"`
	Model         string `json:"model"`
	Local         bool   `json:"local"`
	MaxTokens     int    `json:"max_tokens" desc:"After clamping to the model's limit."`
	ContextWindow int    `json:"context_window,omitempty"`
	PriceKnown    bool   `json:"price_known"`
	Body          string `json:"body" desc:"The request body, byte for byte as it is sent to this provider. Its SHA-256, with the other attempts', is the payload hash."`
}

// SkippedProvider is a provider that was passed over, and why.
type SkippedProvider struct {
	ProviderID string `json:"provider_id"`
	Name       string `json:"name,omitempty"`
	Reason     string `json:"reason" enum:"not_usable,disabled,not_allowed,model_not_allowed,no_model,not_local,context_too_long"`
	Message    string `json:"message"`
}

// Cost is the estimate for the first provider in the chain.
type Cost struct {
	Currency       string `json:"currency"`
	Known          bool   `json:"known" desc:"False when no price is known: the cost is unknown, not zero."`
	Local          bool   `json:"local" desc:"The provider is local; the call costs nothing."`
	EstimateMicros *int64 `json:"estimate_micros,omitempty" desc:"Input plus a typical answer, in micro-units (1,000,000 = one unit of the currency)."`
	MaxMicros      *int64 `json:"max_micros,omitempty" desc:"Input plus the longest answer allowed: what is reserved against the budget."`
}

// Preview is what the person sees before anything is sent.
type Preview struct {
	Recipe           string            `json:"recipe"`
	RecipeVersion    int               `json:"recipe_version"`
	Action           string            `json:"action"`
	ResultKind       string            `json:"result_kind"`
	Decision         Decision          `json:"decision"`
	PolicyMode       string            `json:"policy_mode"`
	Redaction        string            `json:"redaction"`
	Stream           bool              `json:"stream"`
	Nonce            string            `json:"nonce,omitempty" desc:"Send it back with payload_hash to ai.run."`
	PayloadHash      string            `json:"payload_hash,omitempty"`
	PreambleVersion  string            `json:"preamble_version,omitempty"`
	Chips            []ChipInfo        `json:"chips"`
	Skipped          []Skipped         `json:"skipped"`
	Payload          *lib.Payload      `json:"payload,omitempty" desc:"The messages, provider-neutral: what a person reads. Each attempt's body is the same messages in that provider's own format."`
	Attempts         []Attempt         `json:"attempts"`
	SkippedProviders []SkippedProvider `json:"skipped_providers"`
	InputTokens      int               `json:"input_tokens" desc:"Estimated; the provider's own count replaces it after the run."`
	MaxTokens        int               `json:"max_tokens"`
	ContextWindow    int               `json:"context_window,omitempty"`
	Cost             Cost              `json:"cost"`
	ReserveTokens    int64             `json:"reserve_tokens" desc:"The most this request can count against a token cap: its input once for every send the chain allows (each provider, each retry) plus the longest answer. What a run holds while it is running; the real usage replaces it."`
	ReserveMicros    *int64            `json:"reserve_micros,omitempty" desc:"The same in micro-units, for the instance's money; absent when no provider in the chain draws on it or a price is unknown."`
	Mapping          []redact.Entry    `json:"redaction_mapping" desc:"Which names were replaced by which placeholders."`
	Budget           *BudgetView       `json:"budget,omitempty"`
}

// PreviewOut wraps the preview.
type PreviewOut struct {
	Item Preview `json:"item"`
}

// built is a request resolved as far as it can be.
type built struct {
	pv        Preview
	recipe    lib.Recipe
	pol       policy
	payload   lib.Payload
	attempts  []builtAttempt
	rmap      *redact.Map
	params    map[string]string
	docID     string
	doc       *docRow      // the target document as the request was built from it
	sel       *resolvedSel // the selection, resolved, when the request had one
	chips     []lib.Chip
	inTokens  int
	reserve   reserveEst
	refusal   *ops.Error
	refusalKV string
}

type builtAttempt struct {
	p     provider
	model string
	body  []byte
	price lib.Price
	known bool
	max   int
}

type reserveEst struct {
	Tokens     int64
	Micros     int64
	PriceKnown bool // every attempt that is paid for has a known price
	Paid       bool // some attempt is paid by the instance
}

func (b *built) refuse(code, msg string, e *ops.Error) {
	b.pv.Decision = Decision{Allowed: false, Code: code, Message: msg}
	if e == nil {
		e = refuse(code, msg)
	}
	b.refusal, b.refusalKV = e, code
}

// hash is the approval. It covers everything the preview shows and the destination, which the
// preview does not show an editor (an instance provider's address): so it is keyed with a secret
// the server makes at start. Without the key an editor could hash guesses at an internal address
// offline and compare them with the payload_hash they were given. A restart changes the key: a
// preview made before it has to be made again.
func (b *built) hash(key []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte("scriobh-ai-payload-v1\n"))
	for _, a := range b.attempts {
		// The destination is part of what was approved: an address changed (or a provider made
		// local, or not) between the preview and the run is a different request.
		fmt.Fprintf(h, "%s\n%s\n%s\n%s\n%v\n%s\n%s\n%d\n", a.p.ID, a.p.Kind, a.model, normAddr(a.p.BaseURL), a.p.Local, scopeOf(a.p), b.pol.Mode, len(a.body))
		h.Write(a.body)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// build resolves a request: policy, context, redaction, the exact bodies, the
// hash, the estimates and the decision. Preview and Run both call it, so they
// cannot disagree. A hard problem with the request is an error; a refusal by a
// rule is in the result, so a preview can say it.
func (s *Service) build(ctx context.Context, d *db.DB, p ops.Principal, in RunIn) (*built, error) {
	recipe, ok := lib.LookupRecipe(in.Recipe)
	if !ok {
		return nil, ops.Invalid("recipe", "Unknown recipe. ai.recipe.list lists them.")
	}
	params, err := recipe.CheckParams(in.Params)
	if err != nil {
		return nil, ops.Invalid("params", err.Error())
	}
	b := &built{recipe: recipe, params: params, docID: in.DocumentID}
	b.pv = Preview{Recipe: recipe.ID, RecipeVersion: recipe.Version, Action: recipe.Action, ResultKind: string(recipe.Result), Stream: in.Stream,
		Chips: []ChipInfo{}, Skipped: []Skipped{}, Attempts: []Attempt{}, SkippedProviders: []SkippedProvider{}, Mapping: []redact.Entry{},
		Cost: Cost{Currency: s.currency(ctx, d)}}
	l := &loader{s: s, ctx: ctx, d: d, p: p, projectID: in.ProjectID, cfg: s.cfg}
	if e := l.projectInfo(); e != nil {
		return nil, e
	}
	if b.pol, err = loadPolicy(ctx, d.Read(), in.ProjectID); err != nil {
		return nil, ops.Internal(err)
	}
	l.pol = b.pol
	b.pv.PolicyMode, b.pv.Redaction = b.pol.Mode, b.pol.Redaction

	// Rules that need no manuscript text. Checked first, so that a refused request
	// has read nothing of the project.
	switch {
	case b.pol.Mode != ModeAsk && b.pol.Mode != ModeLocalOnly:
		b.refuse("policy_off", "AI is off for this project. The project's owner can turn it on in the project's AI settings.", nil)
		return b, nil
	case !b.pol.allows(recipe.Action):
		b.refuse("action_not_allowed", "This project's AI policy does not allow “"+recipe.Action+"” actions.", nil)
		return b, nil
	case p.Via == "apikey" && !b.pol.AgentsMayUse:
		b.refuse("agents_not_allowed", "This project's owner has not allowed agents (API keys) to use AI on it.", nil)
		return b, nil
	}

	// The target and the selection.
	var sel *resolvedSel
	if in.DocumentID != "" {
		if _, e := l.loadDoc(in.DocumentID, "document_id", true); e != nil {
			return nil, e
		}
	}
	switch recipe.Scope {
	case lib.ScopeSelection:
		if in.Selection == nil || in.DocumentID == "" {
			return nil, ops.Invalid("selection", "This action works on a selection: give document_id and selection.")
		}
	case lib.ScopeDocument:
		if in.DocumentID == "" {
			return nil, ops.Invalid("document_id", "This action works on a document: give document_id.")
		}
	}
	if in.Selection != nil {
		if in.DocumentID == "" {
			return nil, ops.Invalid("document_id", "A selection belongs to a document: give document_id.")
		}
		var e *ops.Error
		if sel, e = l.resolveSelection(in.DocumentID, in.Selection); e != nil {
			return nil, e
		}
	}
	specs := in.Chips
	if specs == nil {
		for _, k := range recipe.DefaultChips {
			specs = append(specs, ChipSpec{Kind: k, Auto: k == lib.ChipEntities})
		}
	}
	chips, e := l.loadChips(specs, in.DocumentID, sel)
	if e != nil {
		return nil, e
	}
	b.chips, b.sel = chips, sel
	if d, ok := l.docs[in.DocumentID]; ok && in.DocumentID != "" {
		b.doc = d
	}
	b.pv.Skipped = append(b.pv.Skipped, l.skipped...)

	// The prompt, from the recipe.
	var turns []lib.Turn
	for _, t := range in.Turns {
		turns = append(turns, lib.Turn{Role: t.Role, Content: t.Content})
	}
	prompt, perr := recipe.Build(lib.BuildContext{Params: params, Chips: chips, Turns: turns, Language: l.language, Title: l.title})
	if perr != nil {
		return nil, ops.Invalid("params", perr.Error())
	}

	// Redaction.
	if b.pol.Redaction != RedactOff {
		// Every name in the project's scope is hidden, not only those the caller may
		// read: redaction protects the manuscript from the provider, and a name the
		// caller cannot look up still appears in the text they are sending.
		rm, _, err := newRedactMap(ctx, d, in.ProjectID, b.pol.Redaction)
		if err != nil {
			return nil, ops.Internal(err)
		}
		b.rmap = rm
		var corpus []string
		corpus = append(corpus, prompt.Task)
		for _, bl := range prompt.Blocks {
			corpus = append(corpus, bl.Text)
		}
		for _, t := range prompt.Turns {
			corpus = append(corpus, t.Content)
		}
		b.rmap.Learn(corpus...)
		prompt = prompt.MapText(b.rmap.Apply)
		// Only the placeholders Apply really put into the text are listed: the preview shows
		// what was replaced here, never the names of entities this request does not mention
		// (a request that merely contains the text "{{CHAR_1}}" does not use that placeholder).
		b.pv.Mapping = append(b.pv.Mapping, b.rmap.UsedEntries()...)
	}
	for _, c := range chips {
		ci := ChipInfo{Kind: c.Kind, Label: c.Label, Source: c.Source, Tokens: c.Tokens, Bytes: c.Bytes}
		if b.rmap != nil {
			if red := b.rmap.Apply(c.Text); red != c.Text {
				ci.Redacted, ci.Tokens = true, lib.EstimateTokens(red)
			}
		}
		b.pv.Chips = append(b.pv.Chips, ci)
	}

	// The payload.
	nonce := in.Nonce
	if nonce == "" {
		nonce = lib.NewNonce()
	}
	b.pv.Nonce = nonce
	maxTok := recipe.MaxOutputTokens
	if in.MaxTokens > 0 && in.MaxTokens < maxTok {
		maxTok = in.MaxTokens
	}
	if maxTok > s.cfg.MaxOutputTokens {
		maxTok = s.cfg.MaxOutputTokens
	}
	asm, aerr := lib.Assemble(lib.AssembleIn{Prompt: prompt, Nonce: nonce, MaxTokens: maxTok, Temperature: recipe.Temperature, JSON: recipe.Result == lib.ResultJSON})
	if aerr != nil {
		return nil, ops.Invalid("turns", aerr.Error())
	}
	b.payload = asm.Payload
	b.pv.Payload = &asm.Payload
	b.pv.PreambleVersion = lib.PreambleVersion
	b.pv.MaxTokens = maxTok
	b.inTokens = lib.EstimatePayload(asm.Payload)
	b.pv.InputTokens = b.inTokens
	size := 0
	for _, m := range asm.Payload.Messages {
		size += len(m.Content)
	}
	if size > s.cfg.MaxRequestBytes {
		b.refuse("too_large", fmt.Sprintf("The request is %d KB, over the %d KB the server sends in one request. Remove some context.", size>>10, s.cfg.MaxRequestBytes>>10),
			tooLarge(fmt.Sprintf("The request is %d KB, over the %d KB the server sends in one request. Remove some context.", size>>10, s.cfg.MaxRequestBytes>>10)))
	}

	// Where it would go.
	if err := s.resolveAttempts(ctx, d, p, in, b, recipe); err != nil {
		return nil, err
	}
	if b.refusal == nil && len(b.attempts) == 0 {
		msg := "No provider can take this request."
		code := "no_provider"
		for _, sp := range b.pv.SkippedProviders {
			if sp.Reason == "not_local" {
				msg = "This project is local-only, and none of its providers is local. An administrator can mark a provider on this machine or network as local."
			}
		}
		b.refuse(code, msg, nil)
	}
	for i := range b.attempts {
		a := &b.attempts[i]
		cl, cerr := lib.NewClient(lib.Endpoint{Kind: a.p.Kind, BaseURL: a.p.BaseURL, Routing: a.p.settings().Routing, KeepAlive: a.p.settings().KeepAlive}, lib.ClientOptions{})
		if cerr != nil {
			return nil, ops.Invalid("provider", "A provider's address is not usable.")
		}
		pl := asm.Payload
		pl.MaxTokens = a.max
		body, eerr := cl.Encode(a.model, pl, in.Stream)
		cl.Close()
		if eerr != nil {
			return nil, ops.Invalid("model", eerr.Error())
		}
		a.body = body
		b.pv.Attempts = append(b.pv.Attempts, Attempt{ProviderID: a.p.ID, ProviderName: a.p.Name, ProviderKind: a.p.Kind, Scope: scopeOf(a.p), Model: a.model, Local: a.p.Local,
			MaxTokens: a.max, ContextWindow: modelWindow(a.p, a.model), PriceKnown: a.known, Body: string(body)})
	}
	if len(b.attempts) > 0 {
		b.pv.PayloadHash = b.hash(s.hashKey)
		b.estimate(s.cfg.Retries)
		if b.pv.Attempts[0].ContextWindow > 0 {
			b.pv.ContextWindow = b.pv.Attempts[0].ContextWindow
		}
	}
	if b.refusal == nil {
		bv, berr := s.budgetCheck(ctx, d.Read(), p.UserID, in.ProjectID, b.pol, b.reserve, false)
		if berr != nil {
			return nil, ops.Internal(berr)
		}
		b.pv.Budget = &bv.View
		if bv.Refusal != nil {
			b.refuse(bv.Code, bv.Refusal.Message, bv.Refusal)
		}
	}
	if b.refusal == nil {
		b.pv.Decision = Decision{Allowed: true}
	}
	return b, nil
}

func scopeOf(p provider) string {
	if p.instance() {
		return "instance"
	}
	return "user"
}

func modelWindow(p provider, model string) int {
	for _, m := range p.models() {
		if m.ID == model {
			return m.ContextWindow
		}
	}
	return 0
}

func modelMaxOut(p provider, model string) int {
	for _, m := range p.models() {
		if m.ID == model {
			return m.MaxOutput
		}
	}
	return 0
}

// estimate fills the cost and the reservation from the attempts.
// estimate fills the cost and the reservation. The reservation is a bound on what the run can
// settle at, not a typical figure: a send that reached a provider is charged at least its input
// (see finish), and the chain may send the request once per attempt and once more per retry
// before one answers, so the input is held for every send the chain allows, plus the longest
// answer of any provider that could give it.
func (b *built) estimate(retries int) {
	sends := int64(len(b.attempts) * (1 + retries))
	var sumIn, outMax int64
	known := true
	paid := false
	maxOut := 0
	for _, a := range b.attempts {
		if a.max > maxOut {
			maxOut = a.max
		}
		if !(a.p.instance() && !a.p.Local) {
			continue // a person's own or a local provider does not draw on the instance's money
		}
		paid = true
		if !a.known {
			known = false
			continue
		}
		sumIn += a.price.Cost(b.inTokens, 0) * int64(1+retries)
		if c := a.price.Cost(0, a.max); c > outMax {
			outMax = c
		}
	}
	b.reserve = reserveEst{Tokens: int64(b.inTokens)*sends + int64(maxOut), Micros: sumIn + outMax, PriceKnown: known, Paid: paid}
	b.pv.ReserveTokens = b.reserve.Tokens
	if paid && known {
		m := b.reserve.Micros
		b.pv.ReserveMicros = &m
	}
	first := b.attempts[0]
	cost := &b.pv.Cost
	cost.Local = first.p.Local
	switch {
	case first.known:
		cost.Known = true
		expOut := b.inTokens * b.recipe.ExpectedOutputRatio / 100
		if expOut > first.max {
			expOut = first.max
		}
		if expOut < 50 {
			expOut = min(50, first.max)
		}
		e, m := first.price.Cost(b.inTokens, expOut), first.price.Cost(b.inTokens, first.max)
		cost.EstimateMicros, cost.MaxMicros = &e, &m
	default:
		cost.Known = false
	}
}

// ---- attempts ---------------------------------------------------------------------------------------

func (s *Service) resolveAttempts(ctx context.Context, d *db.DB, p ops.Principal, in RunIn, b *built, recipe lib.Recipe) error {
	ps, err := providersFor(ctx, d, p.UserID)
	if err != nil {
		return ops.Internal(err)
	}
	byID := map[string]provider{}
	for _, x := range ps {
		byID[x.ID] = x
	}
	var cand []ChainEntry
	switch {
	case in.Provider != "":
		cand = []ChainEntry{{ProviderID: in.Provider, Model: in.Model}}
	default:
		pick := func(m map[string][]ChainEntry) []ChainEntry {
			for _, k := range []string{recipe.ID, recipe.Action, "class:" + recipe.Class, "default"} {
				if c := m[k]; len(c) > 0 {
					return c
				}
			}
			return nil
		}
		// The request's own choice (Provider) came first; then the project's chain, the
		// person's default for the recipe's class, the instance's.
		cand = pick(b.pol.Chains)
		if len(cand) == 0 {
			cand = s.userDefault(ctx, d, p.UserID, recipe.Class)
		}
		if len(cand) == 0 {
			cand = pick(s.instanceChains(ctx, d))
		}
		if len(cand) == 0 { // no chain: the first provider that qualifies, instance providers first
			for _, x := range ps {
				cand = append(cand, ChainEntry{ProviderID: x.ID})
			}
		}
	}
	allowedProv := map[string]bool{}
	for _, id := range b.pol.Providers {
		allowedProv[id] = true
	}
	skip := func(id, name, reason, msg string) {
		b.pv.SkippedProviders = append(b.pv.SkippedProviders, SkippedProvider{ProviderID: id, Name: name, Reason: reason, Message: msg})
	}
	explicit := in.Provider != ""
	for _, e := range cand {
		x, ok := byID[e.ProviderID]
		switch {
		case !ok:
			skip(e.ProviderID, "", "not_usable", "That provider does not exist or is not yours to use.")
			continue
		case !x.Enabled:
			skip(x.ID, x.Name, "disabled", "The provider is switched off.")
			continue
		case len(allowedProv) > 0 && !allowedProv[x.ID]:
			skip(x.ID, x.Name, "not_allowed", "This project's policy does not allow that provider.")
			continue
		case !x.instance() && !b.pol.AllowPersonal && !allowedProv[x.ID]:
			// A personal provider is on a host its owner chose, not the project's owner: it needs
			// the project owner's say-so, either for all personal providers or by naming it.
			skip(x.ID, x.Name, "not_allowed", "This project's owner has not allowed personal providers.")
			continue
		case b.pol.Mode == ModeLocalOnly && !x.Local:
			skip(x.ID, x.Name, "not_local", "This project is local-only and the provider is not local.")
			continue
		}
		model := e.Model
		if model == "" {
			model = x.DefaultModel
		}
		if model == "" {
			skip(x.ID, x.Name, "no_model", "The provider has no default model and none was named.")
			continue
		}
		if allowed := b.pol.Models[x.ID]; len(allowed) > 0 {
			okm := false
			for _, m := range allowed {
				okm = okm || m == model
			}
			if !okm {
				skip(x.ID, x.Name, "model_not_allowed", "This project's policy does not allow that model.")
				continue
			}
		}
		if win := modelWindow(x, model); win > 0 && b.inTokens+b.pv.MaxTokens > win {
			skip(x.ID, x.Name, "context_too_long", fmt.Sprintf("The request (about %d tokens) does not fit that model's %d-token window.", b.inTokens+b.pv.MaxTokens, win))
			continue
		}
		max := b.pv.MaxTokens
		if mo := modelMaxOut(x, model); mo > 0 && mo < max {
			max = mo
		}
		price, known, err := s.priceFor(ctx, d, x, model)
		if err != nil {
			return ops.Internal(err)
		}
		b.attempts = append(b.attempts, builtAttempt{p: x, model: model, price: price, known: known, max: max})
		if len(b.attempts) >= 4 || (explicit) {
			break
		}
	}
	// The order of attempts is the chain's; a request whose only candidates were over the
	// context window says so rather than "no provider".
	if len(b.attempts) == 0 && b.refusal == nil {
		for _, sp := range b.pv.SkippedProviders {
			if sp.Reason == "context_too_long" {
				b.refuse("over_context", "The request is longer than the model can take. "+sp.Message+" Remove some context.", nil)
				break
			}
		}
	}
	return nil
}

// priceFor finds a model's price: the administrator's table (this provider's
// entry before the any-provider one), then what the provider listed. A local
// provider with no price is free. Anything else is unknown, never zero.
func (s *Service) priceFor(ctx context.Context, d *db.DB, p provider, model string) (lib.Price, bool, error) {
	var in, out int64
	err := d.Read().QueryRowContext(ctx, `SELECT input_micros, output_micros FROM ai_prices WHERE model = ? AND provider_id IN (?, '') ORDER BY provider_id DESC LIMIT 1`, model, p.ID).Scan(&in, &out)
	if err == nil {
		return lib.Price{InputMicros: in, OutputMicros: out}, true, nil
	}
	if !isNoRows(err) {
		return lib.Price{}, false, err
	}
	for _, m := range p.models() {
		if m.ID == model && m.InputMicros != nil && m.OutputMicros != nil {
			return lib.Price{InputMicros: *m.InputMicros, OutputMicros: *m.OutputMicros}, true, nil
		}
	}
	if p.Local {
		return lib.Price{}, true, nil
	}
	return lib.Price{}, false, nil
}

func (s *Service) currency(ctx context.Context, d *db.DB) string {
	var v string
	if err := d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'ai.currency'`).Scan(&v); err == nil && v != "" {
		return v
	}
	return s.cfg.Currency
}

var _ = strconv.Itoa

// newRedactMap makes the map that replaces the names of a project's entities (and, in mode "names", the
// capitalised names found in the text: see Map.Learn) with placeholders. Every name in the project's scope is
// hidden, not only those a caller may read. It is the one place the map is made: a request's prompt and the
// text of an embedding are redacted by the same function. The second result is a fingerprint of the mode and of
// every name, to tell when text embedded under one map is out of date.
func newRedactMap(ctx context.Context, d *db.DB, projectID, mode string) (*redact.Map, string, error) {
	names, err := entities.ScopeNames(ctx, d, projectID)
	if err != nil {
		return nil, "", err
	}
	var terms []redact.Term
	sort.SliceStable(names, func(i, j int) bool {
		if names[i].EntityID != names[j].EntityID {
			return names[i].EntityID < names[j].EntityID
		}
		if names[i].Primary != names[j].Primary {
			return names[i].Primary
		}
		return names[i].Surface < names[j].Surface
	})
	h := sha256.New()
	fmt.Fprintf(h, "redaction/1\x00%s\x00", mode)
	for _, n := range names {
		terms = append(terms, redact.Term{Surface: n.Surface, Group: n.EntityID, Kind: n.Kind})
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", n.EntityID, n.Kind, n.Surface)
	}
	return redact.New(terms, redact.Options{Capitalised: mode == RedactNames}), hex.EncodeToString(h.Sum(nil))[:16], nil
}

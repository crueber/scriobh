package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceOf returns the running service of an environment, if Register made one.
func ServiceOf(env *ops.Env) (*Service, bool) {
	return ops.Service[*Service](env, ServiceName)
}

// CloseInterrupted ends the runs a stopped server left running and releases the
// budget they held. The server calls it once at start.
func (s *Service) CloseInterrupted(ctx context.Context, d *db.DB) { s.closeInterrupted(ctx, d) }

// Register adds the AI ops. Everything the ops need beyond the database (the
// configuration, the key box, the connection guard) is captured here, so a
// preview run in a dry-run view, which has no services, behaves as the real
// call does.
func Register(r *ops.Registry, o Options) *Service {
	s := newService(r.Env(), o)
	if r.Env() != nil {
		r.Env().SetService(ServiceName, s)
	}
	reg := func(op ops.Op) ops.Op { return op }
	_ = reg
	ops.Register(r, ops.Op{Name: "ai.status", Method: "GET", Path: "/api/v1/ai/status", Auth: ops.Authenticated(),
		Desc: "Whether AI is available on this server and why not when it is not: the server's kill switch (SCRIOBH_AI=off), the administrator's setting, no secret key for credentials. Works while AI is off."}, status(s))
	ops.Register(r, ops.Op{Name: "ai.settings.get", Method: "GET", Path: "/api/v1/ai/settings", Auth: ops.Admin(),
		Desc: "Administrators. The instance's AI settings: the on/off switch, the default provider chains per action, the currency. Works while AI is off."}, getSettings(s))
	ops.Register(r, ops.Op{Name: "ai.settings.set", Method: "PUT", Path: "/api/v1/ai/settings", Auth: ops.Admin().ForSessions(),
		Desc: "Administrators, browser sessions only. Switch AI on or off for the whole server (an environment kill switch, SCRIOBH_AI=off, overrides it), set the default provider chains per action, and the currency label. Works while AI is off."}, setSettings(s))
	ops.Register(r, ops.Op{Name: "ai.recipe.list", Method: "GET", Path: "/api/v1/ai/recipes", Auth: ops.Writer(),
		Desc: "The recipes (actions) the server offers: id, what each does, the policy action it needs, the result kind, the parameters, and the context it uses by default."}, listRecipes(s))

	ops.Register(r, ops.Op{Name: "ai.provider.list", Method: "GET", Path: "/api/v1/ai/providers", Auth: ops.Writer(),
		Desc: "The providers you can use: the instance's and your own. Keys are never returned, only whether one is stored and its last four characters. An instance provider's address is shown to administrators only."}, listProviders(s))
	ops.Register(r, ops.Op{SecretInput: true, Name: "ai.provider.create", Method: "POST", Path: "/api/v1/ai/providers", Status: 201, Auth: ops.Writer().ForSessions(),
		Desc: "Add a provider: OpenRouter, an OpenAI-compatible endpoint, Ollama or OpenCode. Administrators add instance providers (usable by anyone a project's policy allows, who never hold the key) and may mark one local, which lets it sit on this machine or the local network; anyone may add a personal one, public https addresses only. The key is sealed with the server's secret key and never returned. Browser sessions only."}, createProvider(s))
	ops.Register(r, ops.Op{Name: "ai.provider.get", Method: "GET", Path: "/api/v1/ai/providers/{id}", Auth: ops.Writer(),
		Desc: "One provider you can use."}, getProvider(s))
	ops.Register(r, ops.Op{SecretInput: true, Name: "ai.provider.update", Method: "PATCH", Path: "/api/v1/ai/providers/{id}", Auth: ops.Writer().ForSessions(),
		Desc: "Change a provider. Changing its address drops the stored key (a key belongs to one address): send api_key to store one for the new address. Only an administrator changes an instance provider or sets local. Browser sessions only."}, updateProvider(s))
	ops.Register(r, ops.Op{Name: "ai.provider.delete", Permanent: true, Method: "DELETE", Path: "/api/v1/ai/providers/{id}", Auth: ops.Writer().ForSessions(),
		Desc: "Remove a provider and its sealed key. Browser sessions only."}, deleteProvider(s))
	ops.Register(r, ops.Op{NoDryRun: true, Name: "ai.provider.test", Method: "POST", Path: "/api/v1/ai/providers/{id}/test", Auth: ops.Writer().ForSessions(),
		Desc: "Test a provider with a short request (its model list, or a tiny completion when it has none). The answer is a plain-language result for every kind of failure; it never contains an address, a key or the provider's own text. Browser sessions only."}, testProvider(s))
	ops.Register(r, ops.Op{Name: "ai.provider.models", Method: "GET", Path: "/api/v1/ai/providers/{id}/models", Auth: ops.Writer(),
		Desc: "A provider's cached model list (context size and price where the provider gave them), with models entered by hand."}, listModels(s))
	ops.Register(r, ops.Op{NoDryRun: true, Name: "ai.provider.models.refresh", Method: "POST", Path: "/api/v1/ai/providers/{id}/models/refresh", Auth: ops.Writer().ForSessions(),
		Desc: "Ask the provider for its model list and cache it. Models entered by hand are kept. Nothing refreshes by itself: this is the only request that lists models. Browser sessions only."}, refreshModels(s))
	ops.Register(r, ops.Op{Name: "ai.provider.model.set", Method: "PUT", Path: "/api/v1/ai/providers/{id}/models", Auth: ops.Writer().ForSessions(),
		Desc: "Add or change a model by hand (for a provider with no model list): its id, and optionally its context window and longest answer. Browser sessions only."}, setModel(s))
	ops.Register(r, ops.Op{Name: "ai.provider.model.remove", Permanent: true, Method: "DELETE", Path: "/api/v1/ai/providers/{id}/models", Auth: ops.Writer().ForSessions(),
		Desc: "Remove a model from a provider's list. Browser sessions only."}, removeModel(s))

	ops.Register(r, ops.Op{Name: "ai.price.list", Method: "GET", Path: "/api/v1/ai/prices", Auth: ops.Writer(),
		Desc: "The model prices an administrator set. A model with no price anywhere has an unknown cost, which is never shown or counted as zero."}, listPrices(s))
	ops.Register(r, ops.Op{Name: "ai.price.set", Method: "PUT", Path: "/api/v1/ai/prices", Auth: ops.Admin().ForSessions(),
		Desc: "Administrators, browser sessions only. Set a model's price in micro-units of the instance currency per million input and output tokens (1,000,000 micro-units = one unit). For one provider, or for any."}, setPrice(s))
	ops.Register(r, ops.Op{Name: "ai.price.delete", Permanent: true, Method: "DELETE", Path: "/api/v1/ai/prices", Auth: ops.Admin().ForSessions(),
		Desc: "Administrators, browser sessions only. Remove a model's price."}, deletePrice(s))
	ops.Register(r, ops.Op{Name: "ai.budget.get", Method: "GET", Path: "/api/v1/ai/budget", Auth: ops.Writer(),
		Desc: "The monthly caps that apply to you (the instance's, yours, and with project_id that project's) and how much of each is used or held for requests in flight. Administrators also get every cap that was set. Works while AI is switched off: it sends nothing."}, getBudget(s))
	ops.Register(r, ops.Op{Name: "ai.budget.set", Method: "PUT", Path: "/api/v1/ai/budget", Auth: ops.Admin().ForSessions(),
		Desc: "Administrators, browser sessions only. Set a monthly cap for the instance, a person or a project, in tokens and in money (micro-units of the instance currency). 0 for both removes the cap. A cap is enforced before each request: a request that could take the month over it is refused."}, setBudget(s))
	ops.Register(r, ops.Op{Name: "ai.usage.get", Method: "GET", Path: "/api/v1/ai/usage", Auth: ops.Writer(),
		Desc: "Your own AI usage for a month, by day, with your caps. Works while AI is switched off: it sends nothing."}, getUsage(s))

	ops.Register(r, ops.Op{Name: "ai.policy.get", Method: "GET", Path: "/api/v1/projects/{id}/ai/policy", Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "The project's AI policy: mode (off by default), the actions allowed, providers and models, chains, redaction, whether agents may use AI, the owner's budget, the style guide. With the providers you could name."}, getPolicy(s))
	ops.Register(r, ops.Op{Name: "ai.policy.set", Method: "PUT", Path: "/api/v1/projects/{id}/ai/policy", Auth: ops.RequireProject(ops.ProjectOwner).ForSessions(),
		Desc: "Owner only, browser sessions only: an API key cannot change what leaves the server. Change the project's AI policy; only the fields sent change. mode off sends nothing to any provider; ask makes AI available with a preview before every request; local_only allows only providers on this machine or its network, checked when connecting. The policy is enforced on the server for every request."}, setPolicy(s))
	ops.Register(r, ops.Op{Name: "ai.exclude.set", Method: "PUT", Path: "/api/v1/projects/{id}/ai/exclude", Auth: ops.RequireProject(ops.ProjectEditor).ForSessions(),
		Desc: "Browser sessions only: an API key cannot lift a never-send flag. Flag a document, research note or entity never-send (or lift the flag). A flagged item is left out of every AI request and refused if a request names it. A flag on an entity or a shared note needs editing rights on its Compendium, since it applies to every book that uses it. Works while AI is switched off: a flag only narrows what may be sent."}, setExclude(s))
	ops.Register(r, ops.Op{Name: "ai.exclude.list", Method: "GET", Path: "/api/v1/projects/{id}/ai/exclude", Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "What is flagged never-send in the project (entities as far as you may read them). Works while AI is switched off: it sends nothing."}, listExcluded(s))
	ops.Register(r, ops.Op{Name: "ai.context.preview", Method: "POST", Path: "/api/v1/projects/{id}/ai/preview", Auth: ops.RequireProject(ops.ProjectEditor), SecretOutput: true,
		Desc: "Build an AI request without sending it: the chips with their sizes, the exact request body for every provider that may receive it, the token and cost estimates, the redaction mapping, the policy and budget decision, and a payload_hash with a nonce. Writes nothing and calls no provider. The response holds manuscript text, so it is never kept for idempotent replay."}, previewOp(s))
	ops.Register(r, ops.Op{NoDryRun: true, Name: "ai.run", Method: "POST", Path: "/api/v1/projects/{id}/ai/run", Auth: ops.RequireProject(ops.ProjectEditor), SecretOutput: true,
		Desc: "Run a previewed request. Send the same fields as ai.context.preview plus its payload_hash and nonce: if building the request now gives anything else the call is a 409 and nothing is sent. Policy, redaction, budgets and the provider chain are applied on the server. The result is a suggestion with a run id; nothing is written to the manuscript. Spends money, so it cannot be dry-run, and is limited to 50 seconds (use the stream route for longer answers). The response holds generated text, so it is never kept for idempotent replay."}, runOp(s))
	ops.Register(r, ops.Op{Name: "ai.run.cancel", Method: "POST", Path: "/api/v1/projects/{id}/ai/run/cancel", Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "Cancel a running request of yours (or any on a project you own). The cancellation reaches the provider."}, cancelRun(s))
	ops.Register(r, ops.Op{Name: "ai.run.list", Method: "GET", Path: "/api/v1/projects/{id}/ai/runs", Auth: ops.RequireProject(ops.ProjectEditor),
		Desc: "The project's AI run log, newest first: who, which recipe, provider and model, tokens, cost, status, and a hash of what was sent, with which chips by label and token count. No prompt and no answer text is stored. Owners see everyone's runs; editors their own. Works while AI is switched off: it sends nothing."}, listRuns(s))
	ops.Register(r, ops.Op{Name: "ai.usage.project", Method: "GET", Path: "/api/v1/projects/{id}/ai/usage", Auth: ops.RequireProject(ops.ProjectOwner),
		Desc: "Owner only. The project's AI usage for a month, by day and by person, with the caps that apply. Works while AI is switched off: it sends nothing."}, projectUsage(s))
	return s
}

func previewOp(s *Service) func(*ops.Ctx, RunIn) (PreviewOut, error) {
	return func(c *ops.Ctx, in RunIn) (PreviewOut, error) { return s.Preview(c, c.DB(), c.P, in) }
}

func runOp(s *Service) func(*ops.Ctx, RunIn) (RunItem, error) {
	return func(c *ops.Ctx, in RunIn) (RunItem, error) {
		out, err := s.Run(c, c.DB(), c.P, in)
		if err != nil {
			return RunItem{}, err
		}
		return RunItem{Item: out}, nil
	}
}

// ---- status, settings, recipes ---------------------------------------------------------------------

// StatusOut says whether AI is available.
type StatusOut struct {
	Enabled           bool   `json:"enabled"`
	Reason            string `json:"reason,omitempty" enum:"env,admin" desc:"Why it is off: env (SCRIOBH_AI=off) or admin (switched off in the AI settings)."`
	Message           string `json:"message,omitempty"`
	CredentialsOK     bool   `json:"credentials_ok" desc:"False when the server has no secret key, so no key can be stored."`
	PersonalProviders bool   `json:"personal_providers_allowed"`
	Providers         int    `json:"providers" desc:"Enabled providers you can use."`
	Recipes           int    `json:"recipes"`
	// Warnings are things an administrator would want to know and a person using AI would not.
	Warnings []StatusWarning `json:"warnings" desc:"Conditions worth an administrator's attention, such as no monthly cap anywhere."`
}

// StatusWarning is one such condition.
type StatusWarning struct {
	Code    string `json:"code" enum:"no_caps,no_secret_key,unpriced_model"`
	Message string `json:"message"`
}

func status(s *Service) func(*ops.Ctx, struct{}) (StatusOut, error) {
	return func(c *ops.Ctx, _ struct{}) (StatusOut, error) {
		out := StatusOut{CredentialsOK: s.box.Enabled(), PersonalProviders: !s.cfg.NoUserProviders, Recipes: len(lib.Recipes()), Warnings: []StatusWarning{}}
		switch {
		case s.cfg.Off:
			out.Reason, out.Message = "env", offMessage+" The administrator set SCRIOBH_AI=off."
		case !s.enabled(c, c.DB()):
			out.Reason, out.Message = "admin", offMessage+" An administrator switched it off in the AI settings."
		default:
			out.Enabled = true
			ps, err := providersFor(c, c.DB(), c.P.UserID)
			if err != nil {
				return out, ops.Internal(err)
			}
			for _, p := range ps {
				if p.Enabled {
					out.Providers++
				}
			}
			if out.Providers > 0 && c.P.Role == ops.RoleAdmin {
				if _, ok := loadCap(c, c.DB().Read(), "instance", ""); !ok && s.cfg.DefaultUserTokens == 0 && s.cfg.DefaultUserMicros == 0 {
					out.Warnings = append(out.Warnings, StatusWarning{Code: "no_caps", Message: "No monthly cap applies to anyone: neither an instance cap nor a default per-person cap is set, so a runaway request loop is limited only by the rate limit. Set SCRIOBH_AI_DEFAULT_USER_TOKENS and SCRIOBH_AI_DEFAULT_USER_COST_MICROS, or a cap with ai.budget.set."})
				}
			}
			if c.P.Role == ops.RoleAdmin {
				// A money cap refuses a paid model whose price is unknown. The default cap makes that
				// the first thing a new provider meets, so say which providers it is.
				capRow, hasCap := loadCap(c, c.DB().Read(), "instance", "")
				if (hasCap && capRow.cost > 0) || s.cfg.DefaultUserMicros > 0 {
					var names []string
					for _, p := range ps {
						if p.Enabled && p.instance() && !p.Local && p.DefaultModel != "" {
							if _, known, err := s.priceFor(c, c.DB(), p, p.DefaultModel); err == nil && !known {
								names = append(names, p.Name)
							}
						}
					}
					if len(names) > 0 {
						out.Warnings = append(out.Warnings, StatusWarning{Code: "unpriced_model", Message: "A money cap applies, and no price is known for the default model of " + strings.Join(names, ", ") +
							". Requests to it are refused until a price is set (ai.price.set) or its model list, which may carry prices, is refreshed. Token caps work without prices."})
					}
				}
			}
			if !s.box.Enabled() {
				out.Warnings = append(out.Warnings, StatusWarning{Code: "no_secret_key", Message: "The server has no secret key, so no provider key can be stored."})
			}
		}
		return out, nil
	}
}

// SettingsOut is the instance's AI settings.
type SettingsOut struct {
	Enabled       bool                    `json:"enabled" desc:"The administrator's switch. The server's environment switch (env_off) overrides it."`
	EnvOff        bool                    `json:"env_off" desc:"SCRIOBH_AI=off: AI is off whatever the switch says."`
	Currency      string                  `json:"currency"`
	Chains        map[string][]ChainEntry `json:"chains" desc:"Default provider chains per recipe, action or default, used by projects whose policy sets none."`
	UserProviders bool                    `json:"personal_providers_allowed" desc:"Set with SCRIOBH_AI_USER_PROVIDERS."`
}

func getSettings(s *Service) func(*ops.Ctx, struct{}) (SettingsOut, error) {
	return func(c *ops.Ctx, _ struct{}) (SettingsOut, error) {
		ch := s.instanceChains(c, c.DB())
		if ch == nil {
			ch = map[string][]ChainEntry{}
		}
		var v string
		en := true
		if err := c.DB().Read().QueryRowContext(c, `SELECT value FROM settings WHERE key = ?`, settingEnabled).Scan(&v); err == nil && v == "0" {
			en = false
		}
		return SettingsOut{Enabled: en, EnvOff: s.cfg.Off, Currency: s.currency(c, c.DB()), Chains: ch, UserProviders: !s.cfg.NoUserProviders}, nil
	}
}

// SettingsSetIn changes the settings; only the fields sent change.
type SettingsSetIn struct {
	Enabled  *bool                    `json:"enabled,omitempty"`
	Currency *string                  `json:"currency,omitempty" desc:"A three-letter code such as USD or EUR; a label only, prices are entered in it."`
	Chains   *map[string][]ChainEntry `json:"chains,omitempty"`
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// Validate checks the shape.
func (in *SettingsSetIn) Validate() error {
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if !currencyRe.MatchString(c) {
			return ops.Invalid("currency", "A currency is a three-letter code such as USD.")
		}
		in.Currency = &c
	}
	if in.Chains != nil {
		for k, chain := range *in.Chains {
			if k != "default" && !lib.ValidAction(k) && !validRecipeKey(k) {
				return ops.Invalid("chains", "A chain is named by a recipe id, an action or default.")
			}
			if e := checkChain(chain); e != nil {
				return e
			}
		}
	}
	return nil
}

func setSettings(s *Service) func(*ops.Ctx, SettingsSetIn) (SettingsOut, error) {
	return func(c *ops.Ctx, in SettingsSetIn) (SettingsOut, error) {
		if in.Enabled != nil && *in.Enabled && s.cfg.Off {
			return SettingsOut{}, ops.Conflict("AI is switched off by the server's environment (SCRIOBH_AI=off); the setting cannot turn it on.")
		}
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			put := func(key, value string) error {
				_, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, value, now())
				return err
			}
			if in.Enabled != nil {
				v := "1"
				if !*in.Enabled {
					v = "0"
				}
				if err := put(settingEnabled, v); err != nil {
					return err
				}
			}
			if in.Currency != nil {
				if err := put("ai.currency", *in.Currency); err != nil {
					return err
				}
			}
			if in.Chains != nil {
				for _, chain := range *in.Chains {
					for _, e := range chain {
						p, err := providerByID(c, tx, c.P.UserID, e.ProviderID)
						if err != nil || !p.instance() {
							return ops.Invalid("chains", "An instance chain names instance providers.")
						}
					}
				}
				raw, _ := json.Marshal(*in.Chains)
				if err := put(settingChains, string(raw)); err != nil {
					return err
				}
			}
			c.Audit(ops.AuditEntry{Action: "ai.settings.set", TargetType: "settings", TargetID: "ai",
				Detail: map[string]any{"enabled_changed": in.Enabled != nil, "chains_changed": in.Chains != nil, "currency_changed": in.Currency != nil}})
			return c.FlushTo(tx)
		})
		if err != nil {
			return SettingsOut{}, ops.AsError(err)
		}
		return getSettings(s)(c, struct{}{})
	}
}

// RecipeInfo describes a recipe.
type RecipeInfo struct {
	ID              string      `json:"id"`
	Version         int         `json:"version"`
	Name            string      `json:"name"`
	Desc            string      `json:"desc"`
	Action          string      `json:"action" desc:"The policy action that allows it."`
	Result          string      `json:"result" enum:"text,notes,marks,json"`
	Scope           string      `json:"scope" enum:"none,selection,document,project" desc:"What it needs as its target."`
	DefaultChips    []string    `json:"default_chips"`
	Params          []ParamInfo `json:"params"`
	MaxOutputTokens int         `json:"max_output_tokens"`
	Class           string      `json:"class" desc:"cheap, strong or local: the kind of model it prefers."`
}

// ParamInfo describes a recipe parameter.
type ParamInfo struct {
	Name     string   `json:"name"`
	Desc     string   `json:"desc,omitempty"`
	Required bool     `json:"required"`
	Enum     []string `json:"enum,omitempty"`
	Default  string   `json:"default,omitempty"`
	MaxLen   int      `json:"max_len"`
}

// RecipeListOut lists the recipes.
type RecipeListOut struct {
	Items []RecipeInfo `json:"items"`
}

func listRecipes(s *Service) func(*ops.Ctx, struct{}) (RecipeListOut, error) {
	return func(c *ops.Ctx, _ struct{}) (RecipeListOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return RecipeListOut{}, err
		}
		out := RecipeListOut{Items: []RecipeInfo{}}
		for _, r := range lib.Recipes() {
			ri := RecipeInfo{ID: r.ID, Version: r.Version, Name: r.Name, Desc: r.Desc, Action: r.Action, Result: string(r.Result), Scope: r.Scope,
				DefaultChips: append([]string{}, r.DefaultChips...), Params: []ParamInfo{}, MaxOutputTokens: r.MaxOutputTokens, Class: r.Class}
			for _, p := range r.Params {
				max := p.MaxLen
				if max <= 0 {
					max = 2000
				}
				ri.Params = append(ri.Params, ParamInfo{Name: p.Name, Desc: p.Desc, Required: p.Required, Enum: p.Enum, Default: p.Default, MaxLen: max})
			}
			out.Items = append(out.Items, ri)
		}
		return out, nil
	}
}

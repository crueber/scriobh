package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// Policy modes.
const (
	ModeOff       = "off"        // nothing is sent; the default
	ModeAsk       = "ask"        // AI is on; each request is previewed and approved
	ModeLocalOnly = "local_only" // AI is on, but only to providers on this machine or its network
)

// Redaction modes.
const (
	RedactOff      = "off"
	RedactEntities = "entities" // names and aliases of the project's entities
	RedactNames    = "names"    // those, and capitalised names found mid-sentence
)

// policy is a project's AI policy. A project with no row has the zero policy:
// mode off.
type policy struct {
	Mode          string
	Actions       []string
	Providers     []string
	Models        map[string][]string
	Chains        map[string][]ChainEntry
	Redaction     string
	AgentsMayUse  bool
	AllowPersonal bool
	BudgetMicros  int64
	BudgetTokens  int64
	StyleGuide    string
	UpdatedAt     string
	exists        bool
}

func (p policy) allows(action string) bool {
	for _, a := range p.Actions {
		if a == action {
			return true
		}
	}
	return false
}

func loadPolicy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID string) (policy, error) {
	var p policy
	var actions, providers, models, chains string
	var agents, personal int
	err := q.QueryRowContext(ctx, `SELECT mode, actions_json, providers_json, models_json, chains_json, redaction, agents_may_use, allow_personal_providers, budget_cost_micros, budget_tokens, style_guide, updated_at
		FROM ai_policies WHERE project_id = ?`, projectID).Scan(&p.Mode, &actions, &providers, &models, &chains, &p.Redaction, &agents, &personal, &p.BudgetMicros, &p.BudgetTokens, &p.StyleGuide, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return policy{Mode: ModeOff, Redaction: RedactOff, Actions: []string{}, Providers: []string{}, Models: map[string][]string{}, Chains: map[string][]ChainEntry{}}, nil
	}
	if err != nil {
		return policy{}, err
	}
	p.exists, p.AgentsMayUse, p.AllowPersonal = true, agents == 1, personal == 1
	_ = json.Unmarshal([]byte(actions), &p.Actions)
	_ = json.Unmarshal([]byte(providers), &p.Providers)
	_ = json.Unmarshal([]byte(models), &p.Models)
	_ = json.Unmarshal([]byte(chains), &p.Chains)
	if p.Actions == nil {
		p.Actions = []string{}
	}
	if p.Providers == nil {
		p.Providers = []string{}
	}
	if p.Models == nil {
		p.Models = map[string][]string{}
	}
	if p.Chains == nil {
		p.Chains = map[string][]ChainEntry{}
	}
	return p, nil
}

// Policy is a project's AI policy on the wire.
type Policy struct {
	Mode          string                  `json:"mode" enum:"off,ask,local_only" desc:"off: nothing is sent (the default). ask: AI is available and every request is previewed. local_only: AI is available, but only to providers on this machine or its network; the address is checked when connecting."`
	Actions       []string                `json:"actions" desc:"The actions allowed: selection, alternatives, chat, critique, summaries, continuity, extraction, lab."`
	Providers     []string                `json:"providers" desc:"Provider ids allowed. Empty: any provider the person running the request may use."`
	Models        map[string][]string     `json:"models" desc:"Per provider id, the models allowed. A provider with no entry allows any of its models."`
	Chains        map[string][]ChainEntry `json:"chains" desc:"Per recipe id, per action, or default: the providers and models to try, in order. A failure that retrying can fix (timeout, 5xx, rate limit) moves to the next entry; a refusal by policy, a key or a request never does."`
	Redaction     string                  `json:"redaction" enum:"off,entities,names" desc:"entities: names and aliases of the project's entities are replaced by placeholders before sending and restored in the answer. names: and capitalised names found in the text."`
	AgentsMayUse  bool                    `json:"agents_may_use_ai" desc:"An API key may run AI on this project only when this is on."`
	AllowPersonal bool                    `json:"allow_personal_providers" desc:"Off by default: with no provider list, only the instance's providers are used. On, a collaborator may also send this project's text to a personal provider of their own, on a host the project's owner has not seen. Naming one of your own providers in the list works without it."`
	BudgetMicros  int64                   `json:"budget_cost_micros" desc:"The owner's own monthly cap for this project, in micro-units of the instance currency (1,000,000 = one unit). 0: none."`
	BudgetTokens  int64                   `json:"budget_tokens" desc:"The owner's own monthly token cap for this project. 0: none."`
	StyleGuide    string                  `json:"style_guide" desc:"The author's voice rules, sent as a labelled block with requests that use the style guide chip. Up to 8,000 characters."`
	UpdatedAt     string                  `json:"updated_at,omitempty"`
}

func (p policy) wire() Policy {
	return Policy{Mode: p.Mode, Actions: p.Actions, Providers: p.Providers, Models: p.Models, Chains: p.Chains, Redaction: p.Redaction,
		AgentsMayUse: p.AgentsMayUse, AllowPersonal: p.AllowPersonal, BudgetMicros: p.BudgetMicros, BudgetTokens: p.BudgetTokens, StyleGuide: p.StyleGuide, UpdatedAt: p.UpdatedAt}
}

// PolicyGetIn names the project.
type PolicyGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in PolicyGetIn) ProjectRef() string { return in.ProjectID }

// PolicyOut is a policy with what is in effect around it.
type PolicyOut struct {
	Item      Policy `json:"item"`
	AIEnabled bool   `json:"ai_enabled" desc:"False while AI is switched off for the whole server."`
	Providers []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
		Local bool   `json:"local"`
	} `json:"usable_providers" desc:"The providers you could name here: instance providers and your own."`
}

func getPolicy(s *Service) func(*ops.Ctx, PolicyGetIn) (PolicyOut, error) {
	return func(c *ops.Ctx, in PolicyGetIn) (PolicyOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return PolicyOut{}, err
		}
		p, err := loadPolicy(c, c.DB().Read(), in.ProjectID)
		if err != nil {
			return PolicyOut{}, ops.Internal(err)
		}
		out := PolicyOut{Item: p.wire(), AIEnabled: s.enabled(c, c.DB())}
		ps, err := providersFor(c, c.DB(), c.P.UserID)
		if err != nil {
			return PolicyOut{}, ops.Internal(err)
		}
		for _, x := range ps {
			if !x.Enabled {
				continue
			}
			scope := "user"
			if x.instance() {
				scope = "instance"
			}
			out.Providers = append(out.Providers, struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Kind  string `json:"kind"`
				Scope string `json:"scope"`
				Local bool   `json:"local"`
			}{x.ID, x.Name, x.Kind, scope, x.Local})
		}
		return out, nil
	}
}

// PolicySetIn changes a policy. Only the fields sent change.
type PolicySetIn struct {
	ProjectID     string                   `path:"id" json:"-" desc:"Project id."`
	Mode          *string                  `json:"mode,omitempty" enum:"off,ask,local_only"`
	Actions       *[]string                `json:"actions,omitempty"`
	Providers     *[]string                `json:"providers,omitempty"`
	Models        *map[string][]string     `json:"models,omitempty"`
	Chains        *map[string][]ChainEntry `json:"chains,omitempty"`
	Redaction     *string                  `json:"redaction,omitempty" enum:"off,entities,names"`
	AgentsMayUse  *bool                    `json:"agents_may_use_ai,omitempty"`
	AllowPersonal *bool                    `json:"allow_personal_providers,omitempty"`
	BudgetMicros  *int64                   `json:"budget_cost_micros,omitempty"`
	BudgetTokens  *int64                   `json:"budget_tokens,omitempty"`
	StyleGuide    *string                  `json:"style_guide,omitempty"`
}

// ProjectRef lets the registry authorize by membership.
func (in PolicySetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the fields that stand alone.
func (in *PolicySetIn) Validate() error {
	if in.Mode != nil && *in.Mode != ModeOff && *in.Mode != ModeAsk && *in.Mode != ModeLocalOnly {
		return ops.Invalid("mode", "Mode is off, ask or local_only.")
	}
	if in.Redaction != nil && *in.Redaction != RedactOff && *in.Redaction != RedactEntities && *in.Redaction != RedactNames {
		return ops.Invalid("redaction", "Redaction is off, entities or names.")
	}
	if in.Actions != nil {
		seen := map[string]bool{}
		for _, a := range *in.Actions {
			if !lib.ValidAction(a) {
				return ops.Invalid("actions", "Unknown action "+a+". Actions: "+strings.Join(lib.Actions, ", ")+".")
			}
			seen[a] = true
		}
		out := make([]string, 0, len(seen))
		for _, a := range lib.Actions {
			if seen[a] {
				out = append(out, a)
			}
		}
		in.Actions = &out
	}
	if in.Providers != nil {
		if len(*in.Providers) > 50 {
			return ops.Invalid("providers", "Too many providers.")
		}
		for _, id := range *in.Providers {
			if e := providerID(id); e != nil {
				return ops.Invalid("providers", "Not a provider id: "+clipText(id, 40))
			}
		}
	}
	if in.Models != nil {
		for id, ms := range *in.Models {
			if e := providerID(id); e != nil || len(ms) > 200 {
				return ops.Invalid("models", "Models are listed per provider id, up to 200 each.")
			}
			for _, m := range ms {
				if !lib.ValidModel(m) {
					return ops.Invalid("models", "Not a usable model name: "+clipText(m, 40))
				}
			}
		}
	}
	if in.Chains != nil {
		if len(*in.Chains) > 40 {
			return ops.Invalid("chains", "Too many chains.")
		}
		for k, chain := range *in.Chains {
			if k != "default" && !lib.ValidAction(k) && !validRecipeKey(k) {
				return ops.Invalid("chains", "A chain is named by a recipe id, an action or default: "+clipText(k, 40))
			}
			if err := checkChain(chain); err != nil {
				return err
			}
		}
	}
	if in.BudgetMicros != nil && (*in.BudgetMicros < 0 || *in.BudgetMicros > 1_000_000_000_000_000) ||
		in.BudgetTokens != nil && (*in.BudgetTokens < 0 || *in.BudgetTokens > 1_000_000_000_000) {
		return ops.Invalid("budget_cost_micros", "Budgets cannot be negative.")
	}
	if in.StyleGuide != nil {
		g := strings.TrimSpace(*in.StyleGuide)
		if utf8.RuneCountInString(g) > 8000 {
			return ops.Invalid("style_guide", "The style guide is limited to 8,000 characters.")
		}
		in.StyleGuide = &g
	}
	return nil
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func validRecipeKey(k string) bool {
	if strings.HasPrefix(k, "class:") {
		return ValidClass(strings.TrimPrefix(k, "class:"))
	}
	_, ok := lib.LookupRecipe(k)
	return ok
}

// Model classes a recipe may prefer, and a chain may be set for ("class:cheap").
var Classes = []string{"cheap", "strong", "fast", "local"}

// ValidClass reports whether c is a model class.
func ValidClass(c string) bool {
	for _, x := range Classes {
		if x == c {
			return true
		}
	}
	return false
}

func checkChain(chain []ChainEntry) *ops.Error {
	if len(chain) > 6 {
		return ops.Invalid("chains", "A chain has at most 6 entries.")
	}
	for _, e := range chain {
		if pe := providerID(e.ProviderID); pe != nil {
			return ops.Invalid("chains", "A chain entry names a provider id.")
		}
		if e.Model != "" && !lib.ValidModel(e.Model) {
			return ops.Invalid("chains", "Not a usable model name: "+clipText(e.Model, 40))
		}
	}
	return nil
}

func setPolicy(s *Service) func(*ops.Ctx, PolicySetIn) (PolicyOut, error) {
	return func(c *ops.Ctx, in PolicySetIn) (PolicyOut, error) {
		if err := s.gate(c, c.DB()); err != nil {
			return PolicyOut{}, err
		}
		var out policy
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			p, err := loadPolicy(c, tx, in.ProjectID)
			if err != nil {
				return err
			}
			visible := func(id string) error {
				if _, err := providerByID(c, tx, c.P.UserID, id); err != nil {
					return ops.Invalid("providers", "You cannot name that provider: it is not an instance provider or one of yours.")
				}
				return nil
			}
			old := p.Mode
			if in.Mode != nil {
				p.Mode = *in.Mode
			}
			if in.Actions != nil {
				p.Actions = *in.Actions
			}
			if in.Providers != nil {
				for _, id := range *in.Providers {
					if err := visible(id); err != nil {
						return err
					}
				}
				p.Providers = *in.Providers
			}
			if in.Models != nil {
				for id := range *in.Models {
					if err := visible(id); err != nil {
						return err
					}
				}
				p.Models = *in.Models
			}
			if in.Chains != nil {
				for _, chain := range *in.Chains {
					for _, e := range chain {
						if err := visible(e.ProviderID); err != nil {
							return err
						}
					}
				}
				p.Chains = *in.Chains
			}
			if in.Redaction != nil {
				p.Redaction = *in.Redaction
			}
			if in.AgentsMayUse != nil {
				p.AgentsMayUse = *in.AgentsMayUse
			}
			if in.AllowPersonal != nil {
				p.AllowPersonal = *in.AllowPersonal
			}
			if in.BudgetMicros != nil {
				p.BudgetMicros = *in.BudgetMicros
			}
			if in.BudgetTokens != nil {
				p.BudgetTokens = *in.BudgetTokens
			}
			if in.StyleGuide != nil {
				p.StyleGuide = *in.StyleGuide
			}
			// Turning AI on without saying which actions are allowed allows all of them: the
			// person chose "on", and an empty list reading as "nothing allowed" is a trap.
			if old == ModeOff && p.Mode != ModeOff && in.Actions == nil && len(p.Actions) == 0 {
				p.Actions = append([]string{}, lib.Actions...)
			}
			act, _ := json.Marshal(p.Actions)
			prv, _ := json.Marshal(p.Providers)
			mod, _ := json.Marshal(p.Models)
			chn, _ := json.Marshal(p.Chains)
			p.UpdatedAt = now()
			if _, err := tx.Exec(`INSERT INTO ai_policies(project_id, mode, actions_json, providers_json, models_json, chains_json, redaction, agents_may_use,
					allow_personal_providers, budget_cost_micros, budget_tokens, style_guide, updated_by, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(project_id) DO UPDATE SET mode=excluded.mode, actions_json=excluded.actions_json, providers_json=excluded.providers_json,
					models_json=excluded.models_json, chains_json=excluded.chains_json, redaction=excluded.redaction, agents_may_use=excluded.agents_may_use, allow_personal_providers=excluded.allow_personal_providers,
					budget_cost_micros=excluded.budget_cost_micros, budget_tokens=excluded.budget_tokens, style_guide=excluded.style_guide,
					updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
				in.ProjectID, p.Mode, string(act), string(prv), string(mod), string(chn), p.Redaction, b2i(p.AgentsMayUse), b2i(p.AllowPersonal), p.BudgetMicros, p.BudgetTokens, p.StyleGuide, c.P.UserID, p.UpdatedAt); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "ai.policy.set", TargetType: "project", TargetID: in.ProjectID,
				Detail: map[string]any{"mode": p.Mode, "previous_mode": old, "redaction": p.Redaction, "agents": p.AgentsMayUse, "actions": len(p.Actions)}})
			c.Emit(ops.Event{Type: "ai.policy.updated", ProjectID: in.ProjectID, Payload: map[string]any{"mode": p.Mode}})
			out = p
			return c.FlushTo(tx)
		})
		if err != nil {
			return PolicyOut{}, ops.AsError(err)
		}
		res, err := getPolicy(s)(c, PolicyGetIn{ProjectID: in.ProjectID})
		if err != nil {
			return PolicyOut{}, err
		}
		res.Item = out.wire()
		return res, nil
	}
}

// ---- never-send flags ---------------------------------------------------------------------------------

// ExcludeSetIn flags or unflags something as never-send.
type ExcludeSetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Kind      string `json:"kind" enum:"document,research,entity" desc:"What the id names."`
	ID        string `json:"id" desc:"The document, research note or entity."`
	Exclude   bool   `json:"exclude" desc:"true: never send it to any provider; false: lift the flag."`
}

// ProjectRef lets the registry authorize by membership.
func (in ExcludeSetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *ExcludeSetIn) Validate() error {
	switch in.Kind {
	case "document", "research", "entity":
	default:
		return ops.Invalid("kind", "Kind is document, research or entity.")
	}
	if strings.TrimSpace(in.ID) == "" || len(in.ID) > 60 {
		return ops.Invalid("id", "Name the item.")
	}
	return nil
}

// ExcludeOut is the flag after the change.
type ExcludeOut struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Exclude bool   `json:"exclude"`
}

// setExclude and listExcluded skip the kill switch: a never-send flag only
// narrows what may ever be sent, so it can be set or lifted while AI is off.
func setExclude(s *Service) func(*ops.Ctx, ExcludeSetIn) (ExcludeOut, error) {
	return func(c *ops.Ctx, in ExcludeSetIn) (ExcludeOut, error) {
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			var res sql.Result
			var err error
			switch in.Kind {
			case "document":
				res, err = tx.Exec(`UPDATE documents SET ai_exclude = ? WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, b2i(in.Exclude), in.ID, in.ProjectID)
			case "research":
				var cid sql.NullString
				var pid sql.NullString
				e := tx.QueryRowContext(c, `SELECT project_id, compendium_id FROM research_notes WHERE id = ? AND deleted_at IS NULL`, in.ID).Scan(&pid, &cid)
				if e != nil {
					return ops.NotFound("research note")
				}
				switch {
				case pid.Valid && pid.String == in.ProjectID:
				case cid.Valid && s.compendiumInScope(c, tx, in.ProjectID, cid.String):
					// A shared note: only someone who can edit its Compendium may flag it.
					if role, ok := compaccess.Role(c, tx, c.P.UserID, cid.String); !ok || !compaccess.AtLeast(role, compaccess.Editor) {
						return ops.NotFound("research note")
					}
				default:
					return ops.NotFound("research note")
				}
				res, err = tx.Exec(`UPDATE research_notes SET ai_exclude = ? WHERE id = ?`, b2i(in.Exclude), in.ID)
			case "entity":
				cid, e := entities.ScopeCompendium(c, c.DB(), c.P, in.ProjectID, in.ID)
				if e != nil {
					return e
				}
				if role, ok := compaccess.Role(c, tx, c.P.UserID, cid); !ok || !compaccess.AtLeast(role, compaccess.Editor) {
					return ops.Forbidden("Only someone who can edit the entity's Compendium can flag it, because the flag applies to every book that uses it.")
				}
				res, err = tx.Exec(`UPDATE entities SET ai_exclude = ? WHERE id = ? AND trashed_at IS NULL`, b2i(in.Exclude), in.ID)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return ops.NotFound(in.Kind)
			}
			c.Audit(ops.AuditEntry{Action: "ai.exclude.set", TargetType: in.Kind, TargetID: in.ID, Detail: map[string]any{"exclude": in.Exclude, "project_id": in.ProjectID}})
			return c.FlushTo(tx)
		})
		if err != nil {
			return ExcludeOut{}, ops.AsError(err)
		}
		return ExcludeOut{Kind: in.Kind, ID: in.ID, Exclude: in.Exclude}, nil
	}
}

// compendiumInScope reports whether a Compendium is the project's primary or one it references.
func (s *Service) compendiumInScope(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, projectID, compendiumID string) bool {
	var n int
	_ = q.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT compendium_id AS id FROM projects WHERE id = ?1 AND compendium_id IS NOT NULL
		UNION SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1) WHERE id = ?2`, projectID, compendiumID).Scan(&n)
	return n > 0
}

// ExcludeListIn names the project.
type ExcludeListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ExcludeListIn) ProjectRef() string { return in.ProjectID }

// Excluded is one never-send item.
type Excluded struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Title string `json:"title"`
}

// ExcludeListOut lists what is flagged in a project, as far as the caller may see.
type ExcludeListOut struct {
	Items []Excluded `json:"items"`
}

func listExcluded(s *Service) func(*ops.Ctx, ExcludeListIn) (ExcludeListOut, error) {
	return func(c *ops.Ctx, in ExcludeListIn) (ExcludeListOut, error) {
		out := ExcludeListOut{Items: []Excluded{}}
		q := c.DB().Read()
		rows, err := q.QueryContext(c, `SELECT id, title FROM documents WHERE project_id = ? AND ai_exclude = 1 AND deleted_at IS NULL ORDER BY position, id`, in.ProjectID)
		if err != nil {
			return out, ops.Internal(err)
		}
		for rows.Next() {
			var x Excluded
			x.Kind = "document"
			if err := rows.Scan(&x.ID, &x.Title); err != nil {
				rows.Close()
				return out, ops.Internal(err)
			}
			out.Items = append(out.Items, x)
		}
		rows.Close()
		rows, err = q.QueryContext(c, `SELECT id, title FROM research_notes WHERE project_id = ? AND ai_exclude = 1 AND deleted_at IS NULL ORDER BY position, id`, in.ProjectID)
		if err != nil {
			return out, ops.Internal(err)
		}
		for rows.Next() {
			var x Excluded
			x.Kind = "research"
			if err := rows.Scan(&x.ID, &x.Title); err != nil {
				rows.Close()
				return out, ops.Internal(err)
			}
			out.Items = append(out.Items, x)
		}
		rows.Close()
		sheets, err := entities.ReadableSheets(c, c.DB(), c.P, in.ProjectID)
		if err != nil {
			return out, ops.Internal(err)
		}
		sort.Slice(sheets, func(i, j int) bool { return sheets[i].Name < sheets[j].Name })
		for _, sh := range sheets {
			if sh.AIExclude {
				out.Items = append(out.Items, Excluded{Kind: "entity", ID: sh.ID, Title: sh.Name})
			}
		}
		return out, nil
	}
}

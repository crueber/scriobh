package ai

import (
	"context"
	"encoding/json"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/secrets"
)

// What the recipes' layer (internal/domain/aiwork) needs of the core, and
// nothing more: whether AI is on, what a project's policy says, and the chains
// the defaults ops read and write.

// Gate is the kill switch and the administrator's setting: nil when AI is on.
func (s *Service) Gate(ctx context.Context, d *db.DB) error { return s.gate(ctx, d) }

// Box is the server's secret box (it may be disabled: no key).
func (s *Service) Box() *secrets.Box { return s.box }

// Off reports whether the server's environment switch is set.
func (s *Service) Off() bool { return s.cfg.Off }

// PolicyView is the part of a project's policy the recipes' layer reads.
type PolicyView struct {
	Mode    string
	Actions []string
	Chains  map[string][]ChainEntry
	Exists  bool
	// AgentsMayUse: an API key may run AI on the project.
	AgentsMayUse bool
}

// Allows reports whether the policy allows an action.
func (v PolicyView) Allows(action string) bool {
	for _, a := range v.Actions {
		if a == action {
			return true
		}
	}
	return false
}

// On reports whether the project may use AI at all.
func (v PolicyView) On() bool { return v.Mode == ModeAsk || v.Mode == ModeLocalOnly }

// PolicyOf reads a project's policy.
func (s *Service) PolicyOf(ctx context.Context, d *db.DB, projectID string) (PolicyView, error) {
	p, err := loadPolicy(ctx, d.Read(), projectID)
	if err != nil {
		return PolicyView{}, err
	}
	return PolicyView{Mode: p.Mode, Actions: p.Actions, Chains: p.Chains, Exists: p.exists, AgentsMayUse: p.AgentsMayUse}, nil
}

// ProviderRef is a provider as the defaults ops validate chains against.
type ProviderRef struct {
	ID       string
	Local    bool
	Instance bool
	Enabled  bool
}

// UsableProviders lists the providers a person may name: the instance's and
// their own.
func (s *Service) UsableProviders(ctx context.Context, d *db.DB, userID string) ([]ProviderRef, error) {
	ps, err := providersFor(ctx, d, userID)
	if err != nil {
		return nil, err
	}
	out := make([]ProviderRef, 0, len(ps))
	for _, p := range ps {
		out = append(out, ProviderRef{ID: p.ID, Local: p.Local, Instance: p.instance(), Enabled: p.Enabled})
	}
	return out, nil
}

// ValidateChain checks the shape of a chain as the policy does.
func ValidateChain(chain []ChainEntry) error {
	if e := checkChain(chain); e != nil {
		return e
	}
	return nil
}

// SettingChainsKey is the settings key that holds the instance's chains.
const SettingChainsKey = settingChains

// userDefault reads a person's default chain for a class.
func (s *Service) userDefault(ctx context.Context, d *db.DB, userID, class string) []ChainEntry {
	var raw string
	if err := d.Read().QueryRowContext(ctx, `SELECT chain_json FROM ai_user_defaults WHERE user_id = ? AND class = ?`, userID, class).Scan(&raw); err != nil {
		return nil
	}
	var out []ChainEntry
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

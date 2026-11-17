package config

import "testing"

func aiLoad(t *testing.T, env map[string]string) (*Config, error) {
	t.Helper()
	base := map[string]string{"SCRIOBH_OIDC_STUB": "1"}
	for k, v := range env {
		base[k] = v
	}
	return Load(func(k string) (string, bool) { v, ok := base[k]; return v, ok })
}

func TestAIDefaults(t *testing.T) {
	c, err := aiLoad(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := c.AI
	if a.Off || !a.UserProviders || a.MaxRequestKB != 256 || a.MaxChipKB != 200 || a.MaxOutputTokens != 4096 || a.MaxResponseKB != 4096 ||
		a.TimeoutS != 120 || a.SyncTimeoutS != 50 || a.StreamIdleS != 60 || a.StreamMaxS != 600 || a.ProviderConcurrency != 4 ||
		a.UserConcurrency != 3 || a.RatePerMin != 30 || a.Retries != 1 || a.Currency != "USD" || a.DefaultUserTokens != 1_000_000 || a.DefaultUserCostMicros != 5_000_000 {
		t.Errorf("%+v", a)
	}
}

func TestAIEnvironment(t *testing.T) {
	c, err := aiLoad(t, map[string]string{"SCRIOBH_AI": "off", "SCRIOBH_AI_USER_PROVIDERS": "off", "SCRIOBH_AI_MAX_REQUEST_KB": "64",
		"SCRIOBH_AI_DEFAULT_USER_TOKENS": "100000", "SCRIOBH_AI_DEFAULT_USER_COST_MICROS": "5000000", "SCRIOBH_AI_CURRENCY": "eur", "SCRIOBH_AI_RETRIES": "0"})
	if err != nil {
		t.Fatal(err)
	}
	a := c.AI
	if !a.Off || a.UserProviders || a.MaxRequestKB != 64 || a.DefaultUserTokens != 100000 || a.DefaultUserCostMicros != 5_000_000 || a.Currency != "EUR" || a.Retries != 0 {
		t.Errorf("%+v", a)
	}
	// Caps are on by default; zero is the way to say "no cap".
	c, err = aiLoad(t, map[string]string{"SCRIOBH_AI_DEFAULT_USER_TOKENS": "0", "SCRIOBH_AI_DEFAULT_USER_COST_MICROS": "0"})
	if err != nil || c.AI.DefaultUserTokens != 0 || c.AI.DefaultUserCostMicros != 0 {
		t.Errorf("%+v %v", c.AI, err)
	}
	for name, env := range map[string]map[string]string{
		"a switch that is neither":       {"SCRIOBH_AI": "maybe"},
		"a negative limit":               {"SCRIOBH_AI_MAX_REQUEST_KB": "-1"},
		"a limit of nothing":             {"SCRIOBH_AI_MAX_CHIP_KB": "0"},
		"a word for a number":            {"SCRIOBH_AI_RATE_PER_MIN": "many"},
		"a sync timeout past the server": {"SCRIOBH_AI_SYNC_TIMEOUT_S": "90"},
		"a currency that is not a code":  {"SCRIOBH_AI_CURRENCY": "dollars"},
	} {
		if _, err := aiLoad(t, env); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

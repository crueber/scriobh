package config

import (
	"fmt"
	"strconv"
	"strings"
)

// AI is the AI core's configuration (SCRIOBH_AI_*). Zero values take the
// defaults in internal/domain/ai; every limit here has one.
type AI struct {
	// Off is the kill switch (SCRIOBH_AI=off): every AI op refuses with a clear
	// message and no provider is ever contacted.
	Off bool
	// MaxRequestKB caps the payload of one request (SCRIOBH_AI_MAX_REQUEST_KB, default 256).
	MaxRequestKB int
	// MaxChipKB caps one piece of context (SCRIOBH_AI_MAX_CHIP_KB, default 200).
	MaxChipKB int
	// MaxOutputTokens caps the answer a request may ask for (SCRIOBH_AI_MAX_OUTPUT_TOKENS, default 4096).
	MaxOutputTokens int
	// MaxResponseKB caps a provider's response body (SCRIOBH_AI_MAX_RESPONSE_KB, default 4096).
	MaxResponseKB int
	// Timeouts in seconds: a provider call when the provider sets none
	// (SCRIOBH_AI_TIMEOUT_S, 120); a non-streamed run, which must finish under the
	// server's write timeout (SCRIOBH_AI_SYNC_TIMEOUT_S, 50); the longest silence
	// in a stream (SCRIOBH_AI_STREAM_IDLE_S, 60); the longest stream
	// (SCRIOBH_AI_STREAM_MAX_S, 600); connecting (SCRIOBH_AI_DIAL_TIMEOUT_S, 10).
	TimeoutS, SyncTimeoutS, StreamIdleS, StreamMaxS, DialTimeoutS int
	// ProviderConcurrency is the simultaneous calls to one provider
	// (SCRIOBH_AI_PROVIDER_CONCURRENCY, 4); UserConcurrency the simultaneous runs
	// of one person (SCRIOBH_AI_USER_CONCURRENCY, 3); RatePerMin the runs per
	// person per minute (SCRIOBH_AI_RATE_PER_MIN, 30).
	ProviderConcurrency, UserConcurrency, RatePerMin int
	// Retries is how often a failed call is repeated against the same provider
	// before the next one in the chain (SCRIOBH_AI_RETRIES, 1; 0 for none).
	Retries int
	// UserProviders lets people add providers of their own
	// (SCRIOBH_AI_USER_PROVIDERS, on by default).
	UserProviders bool
	// DefaultUserTokens and DefaultUserCostMicros are the monthly caps for a
	// person with no budget row (SCRIOBH_AI_DEFAULT_USER_TOKENS, default
	// 1,000,000 tokens; SCRIOBH_AI_DEFAULT_USER_COST_MICROS, default 5,000,000,
	// five units of the currency). 0 means no cap: say so on purpose.
	DefaultUserTokens, DefaultUserCostMicros int64
	// Currency labels money (SCRIOBH_AI_CURRENCY, USD).
	Currency string
}

func loadAI(lookup func(string) (string, bool), c *Config) error {
	get := func(k string) string {
		v, _ := lookup(k)
		return strings.TrimSpace(v)
	}
	num := func(k string, def int64) (int64, error) {
		v := get(k)
		if v == "" {
			return def, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%s: %q is not a non-negative integer", k, v)
		}
		return n, nil
	}
	a := &c.AI
	on, err := ParseBool("SCRIOBH_AI", get("SCRIOBH_AI"), true)
	if err != nil {
		return err
	}
	a.Off = !on
	if a.UserProviders, err = ParseBool("SCRIOBH_AI_USER_PROVIDERS", get("SCRIOBH_AI_USER_PROVIDERS"), true); err != nil {
		return err
	}
	for _, f := range []struct {
		env string
		def int64
		dst *int
		min int64
	}{
		{"SCRIOBH_AI_MAX_REQUEST_KB", 256, &a.MaxRequestKB, 1}, {"SCRIOBH_AI_MAX_CHIP_KB", 200, &a.MaxChipKB, 1},
		{"SCRIOBH_AI_MAX_OUTPUT_TOKENS", 4096, &a.MaxOutputTokens, 1}, {"SCRIOBH_AI_MAX_RESPONSE_KB", 4096, &a.MaxResponseKB, 1},
		{"SCRIOBH_AI_TIMEOUT_S", 120, &a.TimeoutS, 1}, {"SCRIOBH_AI_SYNC_TIMEOUT_S", 50, &a.SyncTimeoutS, 1},
		{"SCRIOBH_AI_STREAM_IDLE_S", 60, &a.StreamIdleS, 1}, {"SCRIOBH_AI_STREAM_MAX_S", 600, &a.StreamMaxS, 1},
		{"SCRIOBH_AI_DIAL_TIMEOUT_S", 10, &a.DialTimeoutS, 1},
		{"SCRIOBH_AI_PROVIDER_CONCURRENCY", 4, &a.ProviderConcurrency, 1}, {"SCRIOBH_AI_USER_CONCURRENCY", 3, &a.UserConcurrency, 1},
		{"SCRIOBH_AI_RATE_PER_MIN", 30, &a.RatePerMin, 1}, {"SCRIOBH_AI_RETRIES", 1, &a.Retries, 0},
	} {
		n, err := num(f.env, f.def)
		if err != nil {
			return err
		}
		if n < f.min || n > 1<<30 {
			return fmt.Errorf("%s must be at least %d", f.env, f.min)
		}
		*f.dst = int(n)
	}
	if a.DefaultUserTokens, err = num("SCRIOBH_AI_DEFAULT_USER_TOKENS", 1_000_000); err != nil {
		return err
	}
	if a.DefaultUserCostMicros, err = num("SCRIOBH_AI_DEFAULT_USER_COST_MICROS", 5_000_000); err != nil {
		return err
	}
	if a.SyncTimeoutS >= 58 {
		return fmt.Errorf("SCRIOBH_AI_SYNC_TIMEOUT_S must be under the server's 60 second write timeout (use the stream route for longer answers)")
	}
	a.Currency = strings.ToUpper(get("SCRIOBH_AI_CURRENCY"))
	if a.Currency == "" {
		a.Currency = "USD"
	}
	if len(a.Currency) != 3 {
		return fmt.Errorf("SCRIOBH_AI_CURRENCY must be a three-letter code")
	}
	return nil
}

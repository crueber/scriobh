// Package ai is the AI core of Scríobh: providers, the per-project policy, the
// context builder and its exact-payload preview, the run pipeline with
// fallback chains, streaming, budgets and the run log. It is the seam that
// recipes (internal/ai) and screens build on.
//
// Everything that decides whether text may leave the server happens here, on
// the server, in one path: preview and run build the payload with the same
// function, so what a person approved is what is sent.
//
// # For recipe authors
//
// A recipe is an internal/ai.Recipe registered with ai.RegisterRecipe. It never
// touches the database, a provider or the policy: it receives validated
// parameters and loaded, authorized chips and returns a Prompt. The core does
// the rest. A recipe that needs several calls (a continuity audit over many
// chapters) calls Service.Run once per call from its own op; each call is
// authorized, budgeted, logged and previewable on its own.
//
// # What is never done here
//
// Nothing writes to the manuscript. A run returns a suggestion with a run id;
// accepting it is an ordinary editor op the screen performs.
package ai

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/netguard"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// ServiceName is the name the service is registered under in the op
// environment.
const ServiceName = "ai"

// Config is what the core reads from the server's configuration. Zero values
// take the defaults.
type Config struct {
	// Off is the kill switch (SCRIOBH_AI=off): every AI op refuses, no provider
	// is contacted.
	Off bool
	// MaxRequestBytes caps the payload sent in one request (default 256 KiB).
	MaxRequestBytes int
	// MaxChipBytes caps one piece of context (default 200 KiB).
	MaxChipBytes int
	// MaxOutputTokens caps the answer a request may ask for (default 4096).
	MaxOutputTokens int
	// MaxResponseBytes caps a provider's response body (default 4 MiB).
	MaxResponseBytes int64
	// Timeouts: connecting; a provider call when it sets none; the longest a
	// non-streamed run may take (it must finish under the server's write
	// timeout); the longest silence in a stream; the longest stream.
	DialTimeout         time.Duration
	RequestTimeout      time.Duration
	SyncTimeout         time.Duration
	StreamIdle          time.Duration
	StreamMax           time.Duration
	ProviderConcurrency int // simultaneous calls to one provider (default 4)
	UserConcurrency     int // simultaneous runs of one person (default 3)
	RatePerMinute       int // runs per person per minute (default 30)
	// Retries is how often a call is repeated against the same provider after a
	// timeout, a 5xx or a rate limit, before the next in the chain (default 1).
	Retries int
	// NoUserProviders switches personal providers off (SCRIOBH_AI_USER_PROVIDERS=off).
	NoUserProviders bool
	// DefaultUserTokens and DefaultUserMicros are the monthly caps for a person
	// with no budget row (0: none).
	DefaultUserTokens int64
	DefaultUserMicros int64
	// Currency labels money amounts (default USD).
	Currency string
}

func (c Config) fill() Config {
	d := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	dd := func(v *time.Duration, def time.Duration) {
		if *v <= 0 {
			*v = def
		}
	}
	d(&c.MaxRequestBytes, 256<<10)
	d(&c.MaxChipBytes, 200<<10)
	d(&c.MaxOutputTokens, 4096)
	if c.MaxResponseBytes <= 0 {
		c.MaxResponseBytes = 4 << 20
	}
	dd(&c.DialTimeout, 10*time.Second)
	dd(&c.RequestTimeout, 120*time.Second)
	dd(&c.SyncTimeout, 50*time.Second)
	dd(&c.StreamIdle, 60*time.Second)
	dd(&c.StreamMax, 10*time.Minute)
	d(&c.ProviderConcurrency, 4)
	d(&c.UserConcurrency, 3)
	d(&c.RatePerMinute, 30)
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.Currency == "" {
		c.Currency = "USD"
	}
	return c
}

// Options are what Register takes.
type Options struct {
	Config Config
	Box    *secrets.Box
	Log    *slog.Logger
	// Guard carries the test seams of the connection guard (a resolver, a
	// classifier). Production leaves it empty.
	Guard netguard.Options
	// AllowPlainHTTP lets a provider that is not local use an http:// address.
	// A test seam (httptest servers speak plain HTTP); no configuration sets it.
	AllowPlainHTTP bool
}

// Service is the running AI core.
type Service struct {
	cfg   Config
	box   *secrets.Box
	log   *slog.Logger
	guard netguard.Options
	env   *ops.Env

	allowHTTP bool
	hashKey   []byte // keys payload_hash; made at start, so a restart asks for a new preview

	mu      sync.Mutex
	clients map[string]*lib.Client // by provider id, mode and version
	provSem map[string]*sem
	userSem map[string]*sem
	rates   map[string]*rate.Limiter
	active  map[string]activeRun
	hook    ResultHook
}

type activeRun struct {
	cancel  context.CancelFunc
	userID  string
	project string
}

// Config returns the configuration in effect.
func (s *Service) Config() Config { return s.cfg }

func newService(env *ops.Env, o Options) *Service {
	lg := o.Log
	if lg == nil {
		lg = slog.Default()
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("ai: entropy source failed: " + err.Error())
	}
	return &Service{hashKey: key, cfg: o.Config.fill(), box: o.Box, log: lg, guard: o.Guard, env: env, allowHTTP: o.AllowPlainHTTP,
		clients: map[string]*lib.Client{}, provSem: map[string]*sem{}, userSem: map[string]*sem{}, rates: map[string]*rate.Limiter{}, active: map[string]activeRun{}}
}

// ---- instance settings ------------------------------------------------------------------------

const (
	settingEnabled = "ai.enabled"
	settingChains  = "ai.chains"
)

// enabledState reports whether AI is on: the environment's switch first, then
// the administrator's setting (on unless set to 0). Only "no row" means the
// default, on: a settings table that cannot be read is an error, and the caller
// refuses rather than guess.
func (s *Service) enabledState(ctx context.Context, d *db.DB) (bool, error) {
	if s.cfg.Off {
		return false, nil
	}
	var v string
	err := d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingEnabled).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return true, nil
	case err != nil:
		return false, err
	}
	return v != "0", nil
}

// enabled is enabledState for a caller that only wants a yes or no: an unreadable
// setting is a no, and is logged.
func (s *Service) enabled(ctx context.Context, d *db.DB) bool {
	on, err := s.enabledState(ctx, d)
	if err != nil {
		s.log.Error("ai: the settings could not be read; AI is refused until they can", "err", err)
		return false
	}
	return on
}

// ErrOff is the message every AI op gives while AI is switched off.
const offMessage = "AI is switched off on this server."

func (s *Service) gate(ctx context.Context, d *db.DB) error {
	if s.cfg.Off {
		return ops.Conflict(offMessage + " The administrator set SCRIOBH_AI=off, so no provider is contacted and no AI feature is available.")
	}
	on, err := s.enabledState(ctx, d)
	if err != nil {
		s.log.Error("ai: the settings could not be read; AI is refused until they can", "err", err)
		return ops.Conflict("AI is unavailable because the server could not read its settings. Nothing was sent; try again in a moment.")
	}
	if !on {
		return ops.Conflict(offMessage + " An administrator switched it off in the AI settings.")
	}
	return nil
}

func (s *Service) instanceChains(ctx context.Context, d *db.DB) map[string][]ChainEntry {
	var v string
	if err := d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingChains).Scan(&v); err != nil {
		return nil
	}
	out := map[string][]ChainEntry{}
	if json.Unmarshal([]byte(v), &out) != nil {
		return nil
	}
	return out
}

// ChainEntry is one provider and model to try.
type ChainEntry struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model,omitempty" desc:"Empty: the provider's default model."`
}

// ---- concurrency and rate -----------------------------------------------------------------------

type sem struct {
	ch chan struct{}
}

func newSem(n int) *sem { return &sem{ch: make(chan struct{}, n)} }

// errBusy is returned when a concurrency limit stays full.
var errBusy = errors.New("busy")

func (s *sem) acquire(ctx context.Context, wait time.Duration) (release func(), err error) {
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch }, nil
	default:
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case s.ch <- struct{}{}:
		return func() { <-s.ch }, nil
	case <-t.C:
		return nil, errBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Service) semFor(m map[string]*sem, key string, n int) *sem {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := fmt.Sprintf("%s|%d", key, n)
	if x, ok := m[k]; ok {
		return x
	}
	// a changed size replaces the old semaphore; calls in flight finish on it
	for old := range m {
		if strings.HasPrefix(old, key+"|") {
			delete(m, old)
		}
	}
	x := newSem(n)
	m[k] = x
	return x
}

// allowRun applies the per-person rate limit to runs. It is checked after cheap
// validation, so a malformed request does not spend the allowance.
func (s *Service) allowRun(userID string) *ops.Error {
	return s.allow("run:"+userID, s.cfg.RatePerMinute)
}

// allow is a token bucket per key: perMinute a minute, with a short burst.
func (s *Service) allow(key string, perMinute int) *ops.Error {
	s.mu.Lock()
	l, ok := s.rates[key]
	if !ok {
		per := float64(perMinute) / 60.0
		burst := max(3, min(perMinute, 10)) // a few in quick succession, then the steady rate
		if perMinute > 120 {                // a deliberately generous limit: tests and scripted use
			burst = min(perMinute/10, 1000)
		}
		l = rate.NewLimiter(rate.Limit(per), burst)
		if len(s.rates) > 5000 {
			s.rates = map[string]*rate.Limiter{}
		}
		s.rates[key] = l
	}
	s.mu.Unlock()
	r := l.Reserve()
	if !r.OK() {
		return ops.RateLimited(60)
	}
	if d := r.Delay(); d > 0 {
		r.Cancel()
		e := ops.RateLimited(int(d.Seconds()) + 1)
		e.Message = "You are asking faster than this server allows. Wait a moment."
		return e
	}
	return nil
}

// ---- helpers --------------------------------------------------------------------------------------

func tx(ctx context.Context, d *db.DB, fn func(*sql.Tx) error) error { return d.WithTx(ctx, fn) }

func now() string { return db.Now() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func tooLarge(msg string) *ops.Error {
	return &ops.Error{Code: ops.CodeTooLarge, Message: msg}
}

// refuse is a refusal by a rule (policy, budget, kill switch): a 403 that says
// which rule, in a code a screen can act on.
func refuse(code, msg string) *ops.Error {
	return &ops.Error{Code: ops.CodeForbidden, Message: msg, Data: map[string]any{"ai_code": code}}
}

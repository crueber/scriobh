// Package semantic is the optional semantic index: an embedding for each chunk
// of a project's chosen wording, kept current by a background worker, and the
// two ways to ask it questions: search by meaning (search.semantic) and scenes
// like this one (search.similar).
//
// What it is not: it is never needed. Outline, entities, timeline, research,
// compile and the full-text search do not read it, and a failure here (an
// endpoint that is down, a model that changed, a cap that is reached) is
// recorded and shown in the status, and affects nothing else. It is off for
// every project until the owner has seen a preview (how many chunks, how many
// bytes, which endpoint, local or remote) and confirmed, and it stays off after
// a restore until the owner switches it on again.
//
// Where text goes: only to the endpoint the owner confirmed, through the AI
// provider layer (internal/domain/ai: sealed keys, the connection guard, the
// project's AI policy, budgets and the run log). A local endpoint is the
// default; a remote one needs a separate typed confirmation, because the whole
// manuscript goes to it.
//
// What is embedded: the document body that the full-text index holds
// (doc_text.body), cut into chunks. That is the chosen wording only: private
// notes, ghosted text, HTML comments, raw HTML, code and the alternatives
// comment are not in it. Comments and stashes are other tables and never read.
// Documents flagged never-send, marked do not compile, inside such a document,
// in the trash, notes and research are not embedded, and the chunks of a
// document that becomes one of those are deleted.
package semantic

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the service is registered under in the op environment.
const ServiceName = "semantic"

// Config is the service's limits. Zero values take the defaults.
type Config struct {
	// Poll is how often the worker looks for work (default 2s).
	Poll time.Duration
	// Quiet is how long a document must have been left alone before it is
	// embedded again, so that typing is not a stream of requests (default 20s;
	// a negative value means no wait).
	Quiet time.Duration
	// BatchSize is the texts in one request (default 32, at most 64).
	BatchSize int
	// Parallel is the documents embedded at once (default 2).
	Parallel int
	// DocsPerPass is the documents one visit to a project handles (default 50).
	DocsPerPass int
	// MaxChunksPerProject caps an index: the search scans at most this many chunks
	// (default 20,000, twice the 10,000 the budget is stated at).
	MaxChunksPerProject int
	// MaxCacheBytes bounds the in-memory copies of the vectors (default 96 MiB).
	MaxCacheBytes int64
	// BackoffBase and BackoffMax shape the wait after a failure (30s, 15m).
	BackoffBase, BackoffMax time.Duration
	// ProjectWorkers is how many projects are visited at once (default 4), and ProjectTimeout the longest one visit may
	// take (default 10 minutes): a slow endpoint holds its own project's visit, then the project waits in back-off.
	ProjectWorkers int
	ProjectTimeout time.Duration
	// FailedRetry is how long a document the endpoint refused is left before it is
	// tried again (default 1 hour; the text changing, or a rebuild, tries it at once).
	FailedRetry time.Duration
	// QueryRatePerMinute limits search.semantic per person (default 60).
	QueryRatePerMinute int
}

func (c Config) fill() Config {
	if c.Poll <= 0 {
		c.Poll = 2 * time.Second
	}
	if c.Quiet == 0 {
		c.Quiet = 20 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 32
	}
	c.BatchSize = min(c.BatchSize, 64)
	if c.Parallel <= 0 {
		c.Parallel = 2
	}
	if c.DocsPerPass <= 0 {
		c.DocsPerPass = 50
	}
	if c.MaxChunksPerProject <= 0 {
		c.MaxChunksPerProject = 20_000
	}
	if c.MaxCacheBytes <= 0 {
		c.MaxCacheBytes = 96 << 20
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 30 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 15 * time.Minute
	}
	if c.ProjectWorkers <= 0 {
		c.ProjectWorkers = 4
	}
	if c.ProjectTimeout <= 0 {
		c.ProjectTimeout = 10 * time.Minute
	}
	if c.FailedRetry == 0 {
		c.FailedRetry = time.Hour
	}
	if c.QueryRatePerMinute <= 0 {
		c.QueryRatePerMinute = 60
	}
	return c
}

// Service is the semantic index.
type Service struct {
	env *ops.Env
	cfg Config
	log *slog.Logger

	cache *vecCache
	wake  chan struct{}
	wg    sync.WaitGroup

	mu       sync.Mutex
	busy     map[string]bool      // projects being visited now
	visited  map[string]time.Time // when each was last visited, for round robin
	progress map[string]int       // documents written per project, to tell a slow visit from a dead one
	rates    map[string]*rate.Limiter
	hmac     []byte // keys consent tokens; made at start, so a restart asks for a new preview
}

// ServiceOf returns the running service from the op environment.
func ServiceOf(env *ops.Env) (*Service, bool) { return ops.Service[*Service](env, ServiceName) }

func (s *Service) db() *db.DB { return s.env.DB }

func (s *Service) ai() (*aicore.Service, bool) { return aicore.ServiceOf(s.env) }

// Wake asks the worker to look for work now.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) allow(key string, perMinute int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.rates[key]
	if !ok {
		if len(s.rates) > 10_000 {
			s.rates = map[string]*rate.Limiter{}
		}
		l = rate.NewLimiter(rate.Limit(float64(perMinute)/60), max(5, perMinute/6))
		s.rates[key] = l
	}
	return l.Allow()
}

// Start runs the worker until ctx ends. It returns at once.
func (s *Service) Start(ctx context.Context) {
	if s.env == nil || s.env.DB == nil {
		return
	}
	s.honourMarker(ctx) // before anything can be sent
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
}

// Wait blocks until the worker has stopped.
func (s *Service) Wait() { s.wg.Wait() }

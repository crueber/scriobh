package site

import (
	"context"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// This is the compile gate's twin: the same variable (SCRIOBH_COMPILE_CONCURRENCY),
// the same default and the same answer, a 429 that says the server is busy. The
// server builds it over the compile gate's own slots (NewGateOn), so a compile and
// a site build count against one limit. It is a separate type so this package
// does not import the compile package.

// LimitsService is the name the server registers a *Gate under in ops.Env.
const LimitsService = "site.limits"

// DefaultConcurrency is how many site builds run at once.
const DefaultConcurrency = 2

// busyWait is how long a build waits for a slot before it answers "busy".
const busyWait = 2 * time.Second

// Gate bounds how many site previews, builds and publishes run at once. A build
// of a long blog holds a few hundred megabytes for a moment, and a few at once
// would exhaust the machine, so the rest wait briefly and are then told to try
// again, rather than queueing without end.
type Gate struct {
	slots chan struct{}
	Wait  time.Duration
}

// NewGate makes a gate with that many slots (at least one).
func NewGate(concurrency int) *Gate {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	return &Gate{slots: make(chan struct{}, concurrency), Wait: busyWait}
}

// NewGateOn makes a gate over slots that another gate owns (the compile gate's), so that compiles and site builds share
// one limit.
func NewGateOn(slots chan struct{}) *Gate { return &Gate{slots: slots, Wait: busyWait} }

var (
	defaultGateOnce sync.Once
	defaultGate     *Gate
)

func gateOf(c *ops.Ctx) *Gate {
	if g, ok := ops.Service[*Gate](c.Env, LimitsService); ok && g != nil {
		return g
	}
	defaultGateOnce.Do(func() { defaultGate = NewGate(DefaultConcurrency) })
	return defaultGate
}

// Acquire takes a slot, waiting up to the gate's wait. It gives the slot back
// through the function it returns. A cancelled request gives up at once.
func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	release := func() { <-g.slots }
	select {
	case g.slots <- struct{}{}:
		return release, nil
	default:
	}
	t := time.NewTimer(g.Wait)
	defer t.Stop()
	select {
	case g.slots <- struct{}{}:
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy building other sites. Try again in a few seconds.", RetryAfterSeconds: 5}
	}
}

// heavy runs an op inside the gate.
func heavy[I, O any](f func(*ops.Ctx, I) (O, error)) func(*ops.Ctx, I) (O, error) {
	return func(c *ops.Ctx, in I) (O, error) {
		var zero O
		release, err := gateOf(c).Acquire(c)
		if err != nil {
			return zero, err
		}
		defer release()
		return f(c, in)
	}
}

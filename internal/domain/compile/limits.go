package compile

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// LimitsService is the name the server registers a *Gate under in ops.Env.
const LimitsService = "compile.limits"

// Defaults for the compile gate (SCRIOBH_COMPILE_CONCURRENCY,
// SCRIOBH_COMPILE_PDF_MAX_WORDS).
const (
	DefaultConcurrency = 2
	DefaultPDFMaxWords = 600_000
	// busyWait is how long a compile waits for a slot before it answers "busy".
	busyWait = 2 * time.Second
)

// Gate bounds how many compiles run at once, and how large a PDF compile may
// be. A compile of a long book holds a gigabyte for a second or two; a few at
// once would exhaust the machine, so the rest wait briefly and are then told to
// try again, rather than queueing without end.
type Gate struct {
	slots       chan struct{}
	PDFMaxWords int
	Wait        time.Duration
}

// NewGate makes a gate with that many slots (at least one) and a PDF word cap.
func NewGate(concurrency, pdfMaxWords int) *Gate {
	if concurrency < 1 {
		concurrency = DefaultConcurrency
	}
	if pdfMaxWords < 1 {
		pdfMaxWords = DefaultPDFMaxWords
	}
	return &Gate{slots: make(chan struct{}, concurrency), PDFMaxWords: pdfMaxWords, Wait: busyWait}
}

var (
	defaultGateOnce sync.Once
	defaultGate     *Gate
)

func gateOf(c *ops.Ctx) *Gate {
	if g, ok := ops.Service[*Gate](c.Env, LimitsService); ok && g != nil {
		return g
	}
	defaultGateOnce.Do(func() { defaultGate = NewGate(DefaultConcurrency, DefaultPDFMaxWords) })
	return defaultGate
}

// Slots is the gate's slot channel, for a gate of another kind of heavy work (site builds) that must share the same
// limit, so a build and a compile together never exceed it.
func (g *Gate) Slots() chan struct{} { return g.slots }

// acquire takes a slot, waiting up to the gate's wait. It gives the slot back
// through the function it returns. A cancelled request gives up at once.
func (g *Gate) acquire(ctx context.Context) (func(), error) {
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
		return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is busy compiling other books. Try again in a few seconds.", RetryAfterSeconds: 5}
	}
}

// gated runs a heavy compile step inside a slot.
func gated[T any](c *ops.Ctx, f func() (T, error)) (T, error) {
	var zero T
	release, err := gateOf(c).acquire(c)
	if err != nil {
		return zero, err
	}
	defer release()
	return f()
}

// tooBigForPDF is the refusal of a PDF compile above the word cap.
func tooBigForPDF(g *Gate, words int) error {
	if words <= g.PDFMaxWords {
		return nil
	}
	return ops.Invalid("", fmt.Sprintf("This book has %d words and a PDF compile is limited to %d words on this server (the layout of a long book needs about a gigabyte of memory per million words). Compile a part at a time, or ask the administrator to raise SCRIOBH_COMPILE_PDF_MAX_WORDS.", words, g.PDFMaxWords))
}

// cancelled maps a context error to the error an op returns.
func cancelled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// heavy runs an op inside the gate.
func heavy[I, O any](f func(*ops.Ctx, I) (O, error)) func(*ops.Ctx, I) (O, error) {
	return func(c *ops.Ctx, in I) (O, error) {
		return gated(c, func() (O, error) { return f(c, in) })
	}
}

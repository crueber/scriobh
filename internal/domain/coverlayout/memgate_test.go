package coverlayout

import (
	"context"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// The memory a cover decode may take is shared: what is in the gate is what decodes at once, whatever the compile gate's
// width. One that does not fit waits for another to finish and is then told the server is busy; one bigger than the whole
// goes through alone.
func TestTheDecodeMemoryGate(t *testing.T) {
	g := &memGate{cap: 100, wait: 50 * time.Millisecond, wake: make(chan struct{})}
	r1, err := g.acquire(context.Background(), 60)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := g.acquire(context.Background(), 40) // fits beside it
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.acquire(context.Background(), 10); ops.AsError(err).Code != ops.CodeRateLimited {
		t.Errorf("a third that does not fit: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		r, err := g.acquire(context.Background(), 50)
		if err == nil {
			r()
		}
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	r1() // 60 freed: the waiting 50 fits (40 + 50 = 90)
	if err := <-done; err != nil {
		t.Errorf("a waiter after room was made: %v", err)
	}
	r2()
	big, err := g.acquire(context.Background(), 1000) // more than the whole, and nothing else in: alone
	if err != nil {
		t.Errorf("one bigger than the whole, alone: %v", err)
	}
	if _, err := g.acquire(context.Background(), 1); ops.AsError(err).Code != ops.CodeRateLimited {
		t.Errorf("anything beside it: %v", err)
	}
	big()
	if g.used != 0 {
		t.Errorf("%d bytes left in the gate", g.used)
	}
	ctx, cancel := context.WithCancel(context.Background())
	hold, _ := g.acquire(context.Background(), 100)
	cancel()
	if _, err := g.acquire(ctx, 1); err == nil {
		t.Error("a cancelled request got memory")
	}
	hold()
}

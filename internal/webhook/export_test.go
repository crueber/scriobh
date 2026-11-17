package webhook

import (
	"context"
	"fmt"
	"time"
)

// Feed and Dispatch run one pass of the feeder and of the dispatcher, for the
// external tests: they drive the service by hand, with a clock they control,
// instead of waiting on tickers.
func (s *Service) Feed(ctx context.Context) (bool, error) { return s.feed(ctx) }

func (s *Service) Dispatch(ctx context.Context) int { return s.dispatch(ctx) }

// Listed is a delivery as a dispatch pass lists it, possibly stale by the time it is started.
type Listed = due

// ListAnyState reads a delivery the way a pass lists one, whatever its state.
func (s *Service) ListAnyState(ctx context.Context, id string) (Listed, error) {
	return scanDue(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+dueCols+` FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id WHERE d.id = ?`, id))
}

// StartListed tries to send a listed delivery and says whether it started a send.
func (s *Service) StartListed(ctx context.Context, d Listed) bool {
	ok, _ := s.startOne(ctx, d)
	return ok
}

// ClaimTTL is how long a dead claim lasts, for the test that lets one expire.
func (s *Service) ClaimTTL() int64 { return int64(s.claimTTL()) }

// SeedPinged and PingedLen let a test fill the per-webhook ping record and look at its size.
func (s *Service) SeedPinged(n int, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := 0; i < n; i++ {
		s.pinged[fmt.Sprintf("whk_seed%d", i)] = at
	}
}

func (s *Service) PingedLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pinged)
}

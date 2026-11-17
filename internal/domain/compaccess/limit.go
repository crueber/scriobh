package compaccess

import (
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// Adding a person by email answers differently for an address that belongs to
// someone and one that does not (it succeeds, or it does not), which is as much
// as adding needs to say. To keep it from being run down a list of addresses,
// lookups are limited per person: a small burst, refilling steadily.

const (
	// LookupBurst is how many addresses one person can try at once.
	LookupBurst = 20
	// lookupRefill is the time to earn one more lookup: 20 a minute.
	lookupRefill = time.Minute / LookupBurst
)

type bucket struct {
	tokens float64
	last   time.Time
}

var lookups = struct {
	mu    sync.Mutex
	m     map[string]*bucket
	clock func() time.Time
}{m: map[string]*bucket{}, clock: time.Now}

// SetLookupClock replaces the limiter's clock, for tests, and returns a restore function.
func SetLookupClock(f func() time.Time) (restore func()) {
	lookups.mu.Lock()
	old := lookups.clock
	lookups.clock = f
	lookups.mu.Unlock()
	return func() {
		lookups.mu.Lock()
		lookups.clock = old
		lookups.mu.Unlock()
	}
}

// AllowLookup spends one lookup of the person's allowance. When there is none
// left it returns a rate_limited error carrying how long to wait.
func AllowLookup(userID string) error {
	lookups.mu.Lock()
	defer lookups.mu.Unlock()
	now := lookups.clock()
	b := lookups.m[userID]
	if b == nil {
		if len(lookups.m) > 10_000 { // bound the table: a full bucket is the same as no bucket
			lookups.m = map[string]*bucket{}
		}
		b = &bucket{tokens: LookupBurst, last: now}
		lookups.m[userID] = b
	}
	b.tokens += float64(now.Sub(b.last)) / float64(lookupRefill)
	if b.tokens > LookupBurst {
		b.tokens = LookupBurst
	}
	b.last = now
	if b.tokens < 1 {
		wait := int((1-b.tokens)*float64(lookupRefill)/float64(time.Second)) + 1
		return ops.RateLimited(wait)
	}
	b.tokens--
	return nil
}

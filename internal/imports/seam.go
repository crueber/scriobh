package imports

import (
	"context"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// The places for conversions, for the tests of a caller (the import pages) that must show what it says when the server is busy. The tests
// of this package have their own in export_limiter_test.go.

// LimitPlacesForTest replaces the semaphore of conversions with one of n places that waits the given time; the returned function puts back the old one.
func LimitPlacesForTest(n int, wait time.Duration) (restore func()) {
	cacheMu.Lock()
	cached = nil // a conversion that is cached needs no place: a test of a busy reader needs it to be asked for one
	cacheMu.Unlock()
	slotMu.Lock()
	old, oldWait := slotCh, slotWait
	slotCh, slotWait = make(chan struct{}, n), wait
	slotMu.Unlock()
	return func() {
		slotMu.Lock()
		slotCh, slotWait = old, oldWait
		slotMu.Unlock()
	}
}

// HoldPlaceForTest takes a place as a request of this person does, until the returned function is called.
func HoldPlaceForTest(userID string) (release func(), err error) {
	return acquire(ops.WithPrincipal(context.Background(), ops.Principal{UserID: userID}))
}

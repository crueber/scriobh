package imports

import "time"

// SetConcurrency replaces the semaphore of conversions with one of n places that
// waits the given time, for tests; the returned function puts back the old one.
func SetConcurrency(n int, wait time.Duration) (restore func()) {
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

// HoldPlace takes one place of the semaphore until the returned function is called.
func HoldPlace() (release func()) {
	ch := slots()
	ch <- struct{}{}
	return func() { <-ch }
}

// ClearCache forgets the intakes kept between a preview and its apply.
func ClearCache() {
	cacheMu.Lock()
	cached = nil
	cacheMu.Unlock()
}

// CacheLen is how many intakes are kept.
func CacheLen() int {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	return len(cached)
}

// Conversions is how many sources have been read into an intake so far.
func Conversions() int64 { return conversions.Load() }

// HoldPlaceAs takes a place the way a request of this person does, until the returned function is called.
func HoldPlaceAs(userID string) (release func()) {
	rel, err := acquire(asPerson(userID))
	if err != nil {
		panic(err)
	}
	return rel
}

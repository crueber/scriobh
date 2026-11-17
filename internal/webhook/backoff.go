package webhook

import "time"

// schedule is the wait before the next attempt, after the first, second, third
// ... failed attempt. The last entry repeats. Eight attempts are seven waits, the
// first seven entries (the 24 hours is reached only if SCRIOBH_WEBHOOK_MAX_ATTEMPTS is
// raised), so with the default a receiver that is down gets about 20 hours of tries
// (20 h 36 min before jitter) before the delivery is dead.
var schedule = []time.Duration{
	10 * time.Second, time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
}

// jitterFraction spreads retries: each wait is its base plus or minus this much, so
// many deliveries that failed together do not come back together.
const jitterFraction = 0.2

// MaxRetryAfter is the longest a receiver's Retry-After is honoured.
const MaxRetryAfter = time.Hour

// Backoff is the wait before attempt number failed+1, given how many attempts
// have failed so far (at least 1). r is a random number in [0,1); the caller
// injects it so the schedule can be tested exactly.
func Backoff(failed int, r float64) time.Duration {
	if failed < 1 {
		failed = 1
	}
	base := schedule[len(schedule)-1]
	if failed <= len(schedule) {
		base = schedule[failed-1]
	}
	if r < 0 {
		r = 0
	}
	if r >= 1 {
		r = 0.999999
	}
	factor := 1 + jitterFraction*(2*r-1)
	return time.Duration(float64(base) * factor)
}

package editiondiff

// ShrinkLimits makes the ops use smaller limits until the returned function is called.
func ShrinkLimits(f func(*Limits)) func() {
	testLimits = f
	return func() { testLimits = nil }
}

// AcquireCompare is the per-person limit of edition.compare, for a test to hold.
var AcquireCompare = acquireCompare

// EndsWithAltsComment is the test of a live alternatives comment that edition.take applies.
func EndsWithAltsComment(text string) bool { return endsWithAltsComment(text) }

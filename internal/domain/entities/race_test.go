//go:build race

package entities_test

// The race detector slows Go code by an order of magnitude, so the timing
// budgets are scaled when it is on. The 2,000-entity move's apply is the
// heaviest operation under this package's bounds: it measured 49.7 s on the
// four-vCPU CI runner against 3 s unscaled (16.6x), so 20 leaves a margin. The
// plain build still holds the 3 s budget; the race detector is not a
// performance measurement.
const timingFactor = 20

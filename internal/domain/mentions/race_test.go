//go:build race

package mentions_test

// The race detector slows Go code by an order of magnitude, so the timing
// budgets are scaled when it is on. They are asserted at full strength in the
// ordinary run.
const timingFactor = 15

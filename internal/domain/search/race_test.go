//go:build race

package search_test

// The race detector slows Go code by an order of magnitude, so the timing
// budgets are scaled when it is on.
const timingFactor = 15

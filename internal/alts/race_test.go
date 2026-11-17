//go:build race

package alts

// The race detector slows Go code by an order of magnitude, so timing budgets are scaled.
const timingFactor = 15

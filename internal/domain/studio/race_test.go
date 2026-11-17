//go:build race

package studio_test

// The race detector slows Go code by an order of magnitude, and a loaded machine by more, so the timing bound is scaled and
// only logged when it is on; the bound is judged by the build without it.
const timingFactor = 15

const assertTiming = false

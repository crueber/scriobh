//go:build !race

package studio_test

const timingFactor = 1

// assertTiming says whether the timing bound is a failure: it is, in a normal build.
const assertTiming = true

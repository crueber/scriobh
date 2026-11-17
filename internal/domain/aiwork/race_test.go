//go:build race

package aiwork_test

// raceFactor scales time bounds: the race detector slows everything down.
const raceFactor = 8

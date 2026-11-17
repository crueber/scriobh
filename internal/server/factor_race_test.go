//go:build race

package server

// sseFactor scales the waits of the stream tests: the race detector slows Go
// code by an order of magnitude.
const sseFactor = 5

//go:build race

package compile_test

// timingFactor scales time bounds: the race detector slows everything several times over.
const timingFactor = 10

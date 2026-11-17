//go:build !race

package publish

// timingFactor scales time and memory bounds: the race detector slows everything several times over.
const timingFactor = 1

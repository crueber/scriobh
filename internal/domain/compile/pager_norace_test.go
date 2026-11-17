//go:build !race

package compile

// pagerFactor scales the time bound: the race detector slows everything several times over.
const pagerFactor = 1

//go:build race

package imports

// hostileFactor scales the time bounds of the hostile-input tests (the race detector slows them many times over).
const hostileFactor = 15

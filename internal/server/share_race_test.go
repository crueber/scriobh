//go:build race

package server

// shareRace is true under the race detector, where timing is not meaningful.
const shareRace = true

//go:build !unix && !windows

package db

// Lock is a no-op where neither advisory file locks nor exclusive opens are available.
func Lock(dataDir string) (func(), error) { return func() {}, nil }

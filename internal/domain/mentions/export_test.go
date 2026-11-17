package mentions

// ResetCache empties the engine cache, so a test can measure a cold save.
func ResetCache() {
	cache.Lock()
	defer cache.Unlock()
	cache.m = map[string]*engine{}
	cache.order = nil
}

// SetScanHook installs a hook that runs after each scan has read its
// vocabulary, and returns a function that removes it.
func SetScanHook(f func()) func() {
	scanHook = f
	return func() { scanHook = nil }
}

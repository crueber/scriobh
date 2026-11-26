package pandoc

// FreeBytes says how much room the volume holding dir has for an ordinary user. ok is false when it cannot be asked
// (another package's check then lets the work go on, as the runner does).
func FreeBytes(dir string) (uint64, bool) { return freeBytes(dir) }

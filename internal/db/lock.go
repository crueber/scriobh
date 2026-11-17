package db

// LockedError is what Lock returns when another process holds the data directory.
type LockedError struct{ Dir string }

func (e *LockedError) Error() string {
	return "another Scríobh process is already using " + e.Dir
}

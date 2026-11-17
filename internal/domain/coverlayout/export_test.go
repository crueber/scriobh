package coverlayout

import "context"

// SetAfterBuild sets the hook that runs between making a derivative and keeping it (a test's way to change the world there).
func SetAfterBuild(f func()) { afterBuild = f }

// HoldDecodeMemory takes all of the decode memory until the returned function is called.
func HoldDecodeMemory() func() {
	release, _ := decodeMem.acquire(context.Background(), decodeMem.cap)
	return release
}

// HoldProjectLock takes a project's derivative lock until the returned function is called.
func HoldProjectLock(projectID string) func() { return lockProjectWait(projectID) }

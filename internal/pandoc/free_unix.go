//go:build unix && !openbsd && !netbsd

package pandoc

import "syscall"

// freeBytes says how much room the volume holding dir has for an ordinary user. ok is false when the volume cannot
// be asked.
func freeBytes(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return freeFrom(st.Bavail, st.Bsize), true
}

//go:build openbsd

package pandoc

import "golang.org/x/sys/unix"

// freeBytes says how much room the volume holding dir has for an ordinary user. OpenBSD's syscall package has no
// usable Statfs_t (its fields are named F_bavail and F_bsize, and are not in the shared form), so this asks
// golang.org/x/sys/unix, whose struct has them.
func freeBytes(dir string) (uint64, bool) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, false
	}
	return freeFrom(st.F_bavail, st.F_bsize), true
}

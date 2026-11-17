//go:build netbsd

package pandoc

import "golang.org/x/sys/unix"

// freeBytes says how much room the volume holding dir has for an ordinary user. NetBSD has no statfs; statvfs
// reports blocks in units of the fragment size, Frsize (not Bsize, the preferred I/O size), which is what the
// available count is a count of.
func freeBytes(dir string) (uint64, bool) {
	var st unix.Statvfs_t
	if err := unix.Statvfs(dir, &st); err != nil {
		return 0, false
	}
	return freeFrom(st.Bavail, st.Frsize), true
}

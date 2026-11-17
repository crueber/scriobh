//go:build unix

package testutil

import (
	"io/fs"
	"os"
	"syscall"
)

// privateToMe reports whether the directory is owned by this user and closed to everyone else
// (mode 0700): the directory the migrated templates are kept in.
func privateToMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid() && fi.Mode().Perm() == 0o700
}

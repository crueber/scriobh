//go:build !unix

package testutil

import "io/fs"

// privateToMe: there is no owner or mode to check here; the directory is under the per-user
// temp directory and named for the user.
func privateToMe(fs.FileInfo) bool { return true }

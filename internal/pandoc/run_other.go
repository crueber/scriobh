//go:build !unix

package pandoc

import (
	"os"
	"os/exec"
)

// The bridge is not available off Unix: process groups, resource limits and the
// file ownership check are what make it safe, and there is no equivalent here.
const supported = false

func ownerOK(fi os.FileInfo) bool { return false }

func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

func freeBytes(dir string) (uint64, bool) { return 0, false }

func (r *Runner) command(b Binary, c *call, args []string) (*exec.Cmd, func(), error) {
	return nil, func() {}, fail(KindUnsupported, "Pandoc cannot be used on this platform.")
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

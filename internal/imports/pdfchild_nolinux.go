//go:build unix && !linux

package imports

import "syscall"

func setPdeathsig(a *syscall.SysProcAttr) {}

// limitData does nothing here: macOS and the BSDs do not honour RLIMIT_DATA or RLIMIT_AS for a Go program, so
// the supervisor's polling of the resident memory and GOMEMLIMIT are the limits.
func limitData(bytes int64) {}

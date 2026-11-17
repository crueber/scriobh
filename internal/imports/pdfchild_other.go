//go:build !unix

package imports

import (
	"os"
	"syscall"
)

func childSysProcAttr() *syscall.SysProcAttr { return nil }

func killGroup(p *os.Process) {
	if p != nil {
		p.Kill()
	}
}

func limitSelf() {}

func psRSS(pid int) (int64, bool) { return 0, false }

// maxRSSOf: the system does not say.
func maxRSSOf(ps *os.ProcessState) int64 { return 0 }

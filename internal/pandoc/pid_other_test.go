//go:build !unix

package pandoc_test

import (
	"errors"
	"os"
)

// Off Unix the bridge refuses to run (pandoc.Runner reports KindUnsupported), so no test starts a process whose
// group it must kill; these exist so the test files compile.
func probePID(pid int) error {
	if p, err := os.FindProcess(pid); err != nil || p == nil {
		return errors.New("no such process")
	}
	return nil
}

func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

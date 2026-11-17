//go:build unix

package pandoc_test

import "syscall"

// probePID is signal 0: an error means there is no such process (or it is not ours to signal).
func probePID(pid int) error { return syscall.Kill(pid, 0) }

func killPID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

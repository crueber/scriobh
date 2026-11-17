//go:build linux

package imports

import "syscall"

// setPdeathsig asks the kernel to kill the child when the server's thread that started it ends.
func setPdeathsig(a *syscall.SysProcAttr) { a.Pdeathsig = syscall.SIGKILL }

// limitData caps the data segment (Linux counts private writable mappings, the Go heap among them).
func limitData(bytes int64) {
	syscall.Setrlimit(syscall.RLIMIT_DATA, &syscall.Rlimit{Cur: uint64(bytes), Max: uint64(bytes)})
}

//go:build windows

package main

import (
	"net"
	"syscall"
)

// soExclusiveAddrUse is SO_EXCLUSIVEADDRUSE (the C header's ~SO_REUSEADDR, 0xfffffffb): no other socket, whatever
// options it sets, may bind the port while this one holds it.
const soExclusiveAddrUse = -5

// desktopListenConfig makes the desktop listener exclusive. On Windows, Go sets neither SO_REUSEADDR nor
// SO_EXCLUSIVEADDRUSE, and a program that binds the same loopback port with SO_REUSEADDR can share it, receiving
// some of the shell's connections (and the launch token in a start address). With the option set, it cannot.
// (Cross-compiled and vetted only: never run on Windows.)
func desktopListenConfig() net.ListenConfig {
	return net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, soExclusiveAddrUse, 1)
		}); err != nil {
			return err
		}
		return serr
	}}
}

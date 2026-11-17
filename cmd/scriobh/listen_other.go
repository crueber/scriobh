//go:build !windows

package main

import "net"

// desktopListenConfig is the plain listener where the system already keeps another program from sharing a bound port.
func desktopListenConfig() net.ListenConfig { return net.ListenConfig{} }

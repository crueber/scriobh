//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// surviveClosedOutput keeps the process alive when its standard output or error has no reader any more. Go's
// runtime exits on SIGPIPE for a write to descriptor 1 or 2 (and only for those) unless the program has asked to
// be told about the signal; a desktop shell that is killed outright leaves both pipes without a reader, and the
// server's next log line would end it before the clean stop and the checkpoint of the write-ahead log. Asking
// to be notified, rather than ignoring the signal, leaves the default action in place for the programs this one
// starts. The failed writes themselves return an error, which the logger drops.
func surviveClosedOutput() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGPIPE)
	go func() {
		for range c {
		}
	}()
}

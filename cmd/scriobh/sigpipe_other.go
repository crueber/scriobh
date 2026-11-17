//go:build !unix

package main

// surviveClosedOutput: there is no SIGPIPE here; a write to a closed pipe is an error and nothing more.
func surviveClosedOutput() {}

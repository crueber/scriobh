//go:build unix

package main

import "syscall"

// restrictUmask makes every file the process creates (database, WAL, backups,
// keys) private to the user, whatever the data directory's own mode was.
func restrictUmask() { syscall.Umask(0o077) }

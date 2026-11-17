//go:build !prod

// Command scriobh-dev-gitserver runs a development Git server for end-to-end tests: an SSH Git server over bare
// repositories on disk and a loopback JSON control API. Development only; production builds do not contain it.
//
//	scriobh-dev-gitserver --root DIR [--listen 127.0.0.1:0] [--allow-any-user] [--rsa]
//
// It prints "LISTEN ssh HOST:PORT" and "LISTEN control http://127.0.0.1:PORT" once both listeners are up.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/crueber/scriobh/internal/devgit"
)

func main() {
	root := flag.String("root", "", "directory for the bare repositories (required)")
	listen := flag.String("listen", "127.0.0.1:0", "SSH listen address")
	anyUser := flag.Bool("allow-any-user", false, "accept any SSH user name (default: only git)")
	rsa := flag.Bool("rsa", false, "also offer an RSA host key")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "scriobh-dev-gitserver: --root is required")
		os.Exit(2)
	}
	srv, err := devgit.Start(devgit.Options{Root: *root, Listen: *listen, AllowAnyUser: *anyUser, RSA: *rsa})
	if errors.Is(err, devgit.ErrNoGit) {
		fmt.Fprintln(os.Stderr, "scriobh-dev-gitserver:", err)
		os.Exit(3)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "scriobh-dev-gitserver:", err)
		os.Exit(1)
	}
	fmt.Printf("LISTEN ssh %s\n", srv.SSHAddr())
	fmt.Printf("LISTEN control %s\n", srv.ControlURL())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sig:
		srv.Stop()
	case <-srv.Done():
	}
}

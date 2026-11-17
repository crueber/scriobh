// Command scriobh-cli is the slim command line for scripts and agents. It
// speaks HTTP and JSON to a Scríobh server and imports neither the server nor
// the database; every command is read from the server's op schema.
package main

import (
	"os"

	"github.com/crueber/scriobh/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cli.Version = version
	os.Exit(cli.Main(cli.DefaultRuntime(), os.Args[1:]))
}

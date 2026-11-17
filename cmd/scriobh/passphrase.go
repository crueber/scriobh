package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
)

const passphraseUsage = `usage: scriobh passphrase clear --project ID

  clear     remove a project's passphrase, for an owner who has forgotten it
            (stop the server first). The passphrase is a lock for the screen,
            not encryption: nothing is lost, and nothing was ever hidden from
            you, the operator, who holds the database. It is recorded in the
            audit log as project.passphrase.operator_clear.
`

// passphraseCmd runs `scriobh passphrase <subcommand>`.
func passphraseCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "clear" {
		fmt.Fprint(stderr, passphraseUsage)
		return 2
	}
	fl := flag.NewFlagSet("passphrase clear", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	project := fl.String("project", "", "")
	if err := fl.Parse(args[1:]); err != nil || fl.NArg() > 0 || *project == "" {
		fmt.Fprint(stderr, passphraseUsage)
		return 2
	}
	restrictUmask()
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer release()
	d, err := openDB(cfg, logger(cfg, stderr))
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer d.Close()
	removed, err := projectgate.ClearOffline(context.Background(), d, *project)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: passphrase clear failed:", err)
		return 1
	}
	if removed {
		fmt.Fprintln(stdout, "the project's passphrase is removed; its owner can set a new one")
	} else {
		fmt.Fprintln(stdout, "the project has no passphrase")
	}
	return 0
}

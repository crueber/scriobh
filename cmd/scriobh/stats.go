package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/goals"
)

const statsUsage = `usage: scriobh stats recompute [--user EMAIL] [--project ID]

  recompute   rebuild the daily writing totals from the recorded sessions
              (stop the server first); each day keeps its goal and whether it
              was met
`

// statsCmd runs `scriobh stats <subcommand>`.
func statsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "recompute" {
		fmt.Fprint(stderr, statsUsage)
		return 2
	}
	fl := flag.NewFlagSet("stats recompute", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	user := fl.String("user", "", "")
	project := fl.String("project", "", "")
	if err := fl.Parse(args[1:]); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, statsUsage)
		return 2
	}
	restrictUmask()
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	log := logger(cfg, stderr)
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer release()
	d, err := openDB(cfg, log)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer d.Close()
	userID := ""
	if *user != "" {
		p, err := principalFor(d, *user)
		if err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
		userID = p.UserID
	}
	res, err := goals.Recompute(context.Background(), d, userID, *project)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: recompute failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "recomputed %d days\n", res.Days)
	return 0
}

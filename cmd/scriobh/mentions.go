package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/mentions"
)

const mentionsUsage = `usage: scriobh mentions rebuild [--project ID]

  rebuild   rebuild the mention index (which entity each document names) from
            the stored text, for every project or one (stop the server first).
            The index is derived and always safe to rebuild; the server keeps
            it current and repairs it once after an upgrade by itself.
`

// mentionsCmd runs `scriobh mentions <subcommand>`.
func mentionsCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "rebuild" {
		fmt.Fprint(stderr, mentionsUsage)
		return 2
	}
	fl := flag.NewFlagSet("mentions rebuild", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	project := fl.String("project", "", "")
	if err := fl.Parse(args[1:]); err != nil || fl.NArg() > 0 {
		fmt.Fprint(stderr, mentionsUsage)
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
	st, err := mentions.Rebuild(context.Background(), d, *project)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: mentions rebuild failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "mention index rebuilt: %d projects, %d documents, %d rows written\n", st.Projects, st.Documents, st.Written)
	return 0
}

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/search"
)

// searchCmd runs `scriobh search <subcommand>`.
func searchCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "rebuild" {
		return searchRebuild(stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: scriobh search rebuild\n\n  rebuild   re-derive the search text of every document and rebuild the index\n            (stop the server first)")
	return 2
}

// searchRebuild is the repair for a search index that has drifted or been
// damaged. It takes the data directory lock like migrate does, so it cannot run
// beside a server.
func searchRebuild(stdout, stderr io.Writer) int {
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
	err = search.Rebuild(context.Background(), d, func(done, total int) {
		fmt.Fprintf(stdout, "indexed %d of %d documents\n", done, total)
	})
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: search rebuild failed:", err)
		return 1
	}
	fmt.Fprintln(stdout, "search index rebuilt")
	return 0
}

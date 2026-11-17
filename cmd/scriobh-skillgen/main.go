// Command scriobh-skillgen writes the agent skill, the CLI snapshot and the op
// table of docs/API.md from the op registry. Run it through `make skill`.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/skillgen"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail if the committed files are stale instead of writing")
	flag.Parse()
	files, err := skillgen.GenerateAll(registry.Build(nil, nil, ""), *root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scriobh-skillgen:", err)
		os.Exit(1)
	}
	stale := false
	for rel, want := range files {
		path := filepath.Join(*root, rel)
		if *check {
			if have, err := os.ReadFile(path); err != nil || string(have) != string(want) {
				fmt.Fprintf(os.Stderr, "stale: %s (run `make skill`)\n", rel)
				stale = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "scriobh-skillgen:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "scriobh-skillgen:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", rel)
	}
	if stale {
		os.Exit(1)
	}
}

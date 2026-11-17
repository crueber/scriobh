package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	"github.com/crueber/scriobh/internal/bench"
)

const benchUsage = `usage: scriobh bench [flags]

Builds synthetic projects through the real ops, times each budgeted operation
and compares the result with bench/baseline.json and the budgets in
docs/ARCHITECTURE.md. Exits 1 on a regression or a missed budget.

  --sizes LIST        project sizes: 150k,500k,1m or a word count (default 150k,500k)
  --runs N            timings per operation (default 30)
  --json              print the report as JSON on stdout (the table goes to stderr)
  --out FILE          also write the JSON report to FILE
  --baseline FILE     baseline to compare with (default bench/baseline.json)
  --write-baseline    record this run in the baseline for this machine and exit 0
  --candidate FILE    write the baseline as it would be with this run recorded to FILE,
                      leaving the baseline alone (for CI to publish a baseline to adopt)
  --best-of N         run N times and keep each metric's best (for writing a baseline)
  --tolerance F       allowed p50 growth over the baseline (default 0.20)
  --data-dir DIR      build in DIR and keep it, reusing a project built earlier
                      (default: a temporary directory that is removed)
  --fresh             with --data-dir, build again instead of reusing
  --cpuprofile FILE   write a CPU profile of the run

  --soak DURATION     instead of timing each operation, serve each size on a
                      loopback socket with every background worker running and
                      keep a realistic mix going for DURATION (for example 20m):
                      saves, searches, page loads, compile previews, DOCX
                      compiles, exports, alternatives, snapshots, backups and
                      mirror writes. Fails when memory, goroutines, open files,
                      the WAL or the database keep growing (the last third's
                      median against the first third's, after the warm-up) or
                      when an operation misses its budget. --out writes the
                      samples and timings as JSON.
  --sample DURATION   soak sampling interval (default 5s)
  --warmup DURATION   soak warm-up left out of the growth rule (default a sixth
                      of the duration, 30s to 5m)

The environment of the process is not read: a bench run never touches a real
data directory.
`

// benchCmd runs `scriobh bench`.
func benchCmd(args []string, stdout, stderr io.Writer) int {
	fl := flag.NewFlagSet("bench", flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	sizes := fl.String("sizes", "150k,500k", "")
	runs := fl.Int("runs", 30, "")
	asJSON := fl.Bool("json", false, "")
	out := fl.String("out", "", "")
	basePath := fl.String("baseline", "bench/baseline.json", "")
	writeBase := fl.Bool("write-baseline", false, "")
	candidate := fl.String("candidate", "", "")
	tol := fl.Float64("tolerance", 0.20, "")
	bestOf := fl.Int("best-of", 1, "")
	dir := fl.String("data-dir", "", "")
	fresh := fl.Bool("fresh", false, "")
	prof := fl.String("cpuprofile", "", "")
	soak := fl.Duration("soak", 0, "")
	sampleEvery := fl.Duration("sample", 0, "")
	warmup := fl.Duration("warmup", 0, "")
	if err := fl.Parse(args); err != nil || fl.NArg() > 0 || *runs < 3 || *tol <= 0 || *bestOf < 1 {
		fmt.Fprint(stderr, benchUsage)
		return 2
	}
	list, err := bench.ParseSizes(*sizes)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh bench:", err)
		return 2
	}
	base, err := bench.LoadBaseline(*basePath)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh bench:", err)
		return 2
	}
	if *prof != "" {
		f, err := os.Create(*prof)
		if err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
		defer pprof.StopCPUProfile()
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *soak > 0 {
		return soakCmd(ctx, bench.Options{Sizes: list, DataDir: *dir, Fresh: *fresh, Log: stderr, Version: version},
			bench.SoakOptions{Duration: *soak, Sample: *sampleEvery, Warmup: *warmup}, *out, stdout, stderr)
	}
	pol := bench.Policy{Tolerance: *tol}
	if *dir == "" && *bestOf > 1 {
		// One temporary project for every pass, so the build is paid once.
		td, err := os.MkdirTemp("", "scriobh-bench-")
		if err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
		defer os.RemoveAll(td)
		*dir = td
	}
	var rep *bench.Report
	for i := 0; i < *bestOf; i++ {
		r, err := bench.Run(ctx, bench.Options{Sizes: list, DataDir: *dir, Fresh: *fresh && i == 0, Runs: *runs, Log: stderr, Version: version,
			Failing: base.Failing(bench.MachineID(), pol)})
		if err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
		if rep == nil {
			rep = r
		} else {
			rep = bench.Best(rep, r)
		}
	}
	raw, _ := json.MarshalIndent(rep, "", "  ")
	raw = append(raw, '\n')
	if *out != "" {
		if err := os.WriteFile(*out, raw, 0o644); err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
	}
	if *writeBase {
		base.Record(rep)
		if err := base.Save(*basePath); err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
		fmt.Fprintf(stderr, "baseline for %s written to %s\n", rep.Machine, *basePath)
	}
	if *candidate != "" {
		next := &bench.Baseline{Schema: base.Schema, Machines: map[string]bench.BaselineEntry{}}
		for k, e := range base.Machines {
			next.Machines[k] = e
		}
		next.Record(rep)
		if err := next.Save(*candidate); err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
	}
	v := bench.Judge(rep, base, pol)
	if *asJSON {
		stdout.Write(raw)
		bench.WriteTable(stderr, rep, v)
	} else {
		bench.WriteTable(stdout, rep, v)
	}
	if *writeBase {
		// Recording a baseline is not a gate; budgets still are.
		for _, p := range v.Problems {
			if p.Kind == "budget" {
				return 1
			}
		}
		return 0
	}
	if !v.OK() {
		return 1
	}
	return 0
}

// soakCmd runs `scriobh bench --soak`.
func soakCmd(ctx context.Context, o bench.Options, so bench.SoakOptions, out string, stdout, stderr io.Writer) int {
	res, err := bench.Soak(ctx, o, so)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh bench:", err)
		return 1
	}
	if out != "" {
		raw, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(out, append(raw, '\n'), 0o644); err != nil {
			fmt.Fprintln(stderr, "scriobh bench:", err)
			return 1
		}
	}
	code := 0
	for _, r := range res {
		bench.WriteSoakTable(stdout, r)
		if !r.OK() {
			code = 1
		}
	}
	return code
}

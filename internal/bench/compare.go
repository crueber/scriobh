package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// Baseline is the committed record of what a run measured on a given class of
// machine. Numbers from one machine say nothing about another, so entries are
// keyed by MachineID; a run on a machine with no entry is checked against the
// absolute budgets only, and says so.
type Baseline struct {
	Schema   int                      `json:"schema"`
	Machines map[string]BaselineEntry `json:"machines"`
}

// BaselineEntry is one machine class's numbers.
type BaselineEntry struct {
	Recorded  string                       `json:"recorded"`
	Go        string                       `json:"go"`
	Generator int                          `json:"generator"`
	Seed      int64                        `json:"seed"`
	Sizes     map[string]map[string]Number `json:"sizes"`
}

// Number is a recorded measurement.
type Number struct {
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Unit string  `json:"unit"`
	Runs int     `json:"runs"`
}

// LoadBaseline reads a baseline file. A missing file is an empty baseline.
func LoadBaseline(path string) (*Baseline, error) {
	b := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, b); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if b.Machines == nil {
		b.Machines = map[string]BaselineEntry{}
	}
	return b, nil
}

// Save writes the baseline with stable key order.
func (b *Baseline) Save(path string) error {
	raw, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// Record merges a report into the baseline under the report's machine. Sizes the
// report does not hold are kept as they were.
func (b *Baseline) Record(r *Report) {
	e := b.Machines[r.Machine]
	if e.Sizes == nil || e.Generator != r.Generator || e.Seed != r.Seed {
		e.Sizes = map[string]map[string]Number{}
	}
	e.Recorded, e.Go, e.Generator, e.Seed = r.Date, r.Go, r.Generator, r.Seed
	for _, s := range r.Sizes {
		m := map[string]Number{}
		for _, x := range s.Metrics {
			m[x.Name] = Number{P50: x.P50, P95: x.P95, Unit: x.Unit, Runs: x.Runs}
		}
		e.Sizes[s.Name] = m
	}
	b.Machines[r.Machine] = e
}

// Best merges two runs of the same sizes, keeping for each metric the run with
// the lower p50. A baseline written from the best of several runs is the speed
// the machine can reach, not the speed of one busy moment.
func Best(a, b *Report) *Report {
	out := *a
	out.Sizes = nil
	for i, sa := range a.Sizes {
		s := sa
		s.Metrics = append([]Metric(nil), sa.Metrics...)
		if i < len(b.Sizes) && b.Sizes[i].Name == sa.Name {
			for j := range s.Metrics {
				if j < len(b.Sizes[i].Metrics) && b.Sizes[i].Metrics[j].Name == s.Metrics[j].Name && b.Sizes[i].Metrics[j].P50 < s.Metrics[j].P50 {
					s.Metrics[j] = b.Sizes[i].Metrics[j]
				}
			}
		}
		out.Sizes = append(out.Sizes, s)
	}
	return &out
}

// Policy is how strictly a report is judged.
type Policy struct {
	// Tolerance is the allowed growth of p50 over the baseline (0.20 is 20%).
	Tolerance float64
}

// noiseFloor is the smallest absolute growth that can count as a regression: a
// metric that went from 0.4 ms to 0.6 ms has not regressed, it has jittered.
// The floor is larger for metrics timed few times, which are noisier.
func noiseFloor(m Metric) float64 {
	if m.Unit == UnitBytes {
		return 256 << 10
	}
	switch {
	case m.Runs >= 10:
		return 5
	case m.Runs >= 3:
		return 15
	}
	return 75
}

// tolerance is the allowed growth for a metric. A timing taken once is far
// noisier than the median of thirty, so it needs at least 50%.
func tolerance(m Metric, p Policy) float64 {
	if m.Unit == UnitMS && m.Runs < 3 && p.Tolerance < 0.5 {
		return 0.5
	}
	return p.Tolerance
}

// diskBound metrics are dominated by how fast the disk syncs, which varies by
// more than any regression tolerance from one run, and one machine's day, to
// the next. They are reported and held to their absolute budgets (the backup),
// but never judged against the baseline.
var diskBound = map[string]bool{"mirror.full_write": true, "backup.create": true}

// Problem is one failed check.
type Problem struct {
	Size, Metric, Kind string // Kind is "regression" or "budget"
	Detail             string
}

// Verdict is the result of judging a report.
type Verdict struct {
	Problems []Problem
	// Compared is true when a baseline entry for this machine was found.
	Compared bool
	Notes    []string
}

// OK is true when nothing failed.
func (v Verdict) OK() bool { return len(v.Problems) == 0 }

// Failing is the retry predicate for Options.Failing.
func (b *Baseline) Failing(machine string, p Policy) func(size string, m Metric) bool {
	return func(size string, m Metric) bool {
		return len(judgeOne(b, machine, p, size, m)) > 0
	}
}

func judgeOne(b *Baseline, machine string, p Policy, size string, m Metric) []Problem {
	var out []Problem
	if m.Budget > 0 && m.Enforced && m.P95 > m.Budget {
		out = append(out, Problem{size, m.Name, "budget", fmt.Sprintf("p95 %s exceeds the budget %s (%s)", fmtVal(m, m.P95), fmtVal(m, m.Budget), m.Note)})
	}
	if b == nil {
		return out
	}
	e, ok := b.Machines[machine]
	if !ok {
		return out
	}
	base, ok := e.Sizes[size][m.Name]
	if !ok || base.P50 <= 0 || diskBound[m.Name] {
		return out
	}
	grew := m.P50 - base.P50
	if grew > base.P50*tolerance(m, p) && grew > noiseFloor(m) {
		out = append(out, Problem{size, m.Name, "regression", fmt.Sprintf("p50 %s is %.0f%% over the baseline %s", fmtVal(m, m.P50), 100*grew/base.P50, fmtVal(m, base.P50))})
	}
	return out
}

// Judge checks a report against the absolute budgets and, when the baseline has
// an entry for this machine, against that.
func Judge(r *Report, b *Baseline, p Policy) Verdict {
	var v Verdict
	if b != nil {
		if e, ok := b.Machines[r.Machine]; ok {
			v.Compared = true
			if e.Generator != r.Generator || e.Seed != r.Seed {
				v.Compared = false
				v.Notes = append(v.Notes, fmt.Sprintf("the baseline for %s was made by generator %d (seed %d), this run used generator %d (seed %d); regression comparison skipped", r.Machine, e.Generator, e.Seed, r.Generator, r.Seed))
			}
		} else {
			v.Notes = append(v.Notes, fmt.Sprintf("the baseline has no entry for this machine (%s); only the absolute budgets were checked. Run with --write-baseline on this class of machine to enable the regression check.", r.Machine))
		}
	}
	for _, s := range r.Sizes {
		for _, m := range s.Metrics {
			for _, pr := range judgeOne(b, r.Machine, p, s.Name, m) {
				if pr.Kind == "regression" && !v.Compared {
					continue
				}
				v.Problems = append(v.Problems, pr)
			}
			if m.Budget > 0 && !m.Enforced && m.P95 > m.Budget {
				v.Notes = append(v.Notes, fmt.Sprintf("%s %s: p95 %s is over %s; the budgets are stated for 500,000 words, so this size is reported, not gated", s.Name, m.Name, fmtVal(m, m.P95), fmtVal(m, m.Budget)))
			}
		}
	}
	return v
}

func fmtVal(m Metric, v float64) string {
	if m.Unit == UnitBytes {
		switch {
		case v >= 1<<30:
			return fmt.Sprintf("%.2f GB", v/(1<<30))
		case v >= 1<<20:
			return fmt.Sprintf("%.1f MB", v/(1<<20))
		}
		return fmt.Sprintf("%.0f KB", v/1024)
	}
	if v >= 1000 {
		return fmt.Sprintf("%.2f s", v/1000)
	}
	return fmt.Sprintf("%.2f ms", v)
}

// WriteTable prints the report as one table per size.
func WriteTable(w io.Writer, r *Report, v Verdict) {
	fmt.Fprintf(w, "scriobh bench  %s  %s  %s  runs=%d  generator=%d seed=%d\n", r.Version, r.Machine, r.Go, r.Runs, r.Generator, r.Seed)
	bad := map[string]string{}
	for _, p := range v.Problems {
		bad[p.Size+"/"+p.Metric] += p.Kind + " "
	}
	for _, s := range r.Sizes {
		fmt.Fprintf(w, "\n%s: %d words, %d documents (built in %.1f s)\n", s.Name, s.Words, s.Documents, s.GenSeconds)
		tw := tabwriter.NewWriter(w, 2, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "metric\truns\tp50\tp95\tbudget\tstatus")
		for _, m := range s.Metrics {
			bud, status := "", "ok"
			if m.Budget > 0 {
				bud = fmtVal(m, m.Budget)
				if !m.Enforced {
					bud += " (info)"
				}
			}
			if f := strings.TrimSpace(bad[s.Name+"/"+m.Name]); f != "" {
				status = strings.ToUpper(f)
			} else if m.Budget > 0 && !m.Enforced && m.P95 > m.Budget {
				status = "over (not gated)"
			}
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", m.Name, m.Runs, fmtVal(m, m.P50), fmtVal(m, m.P95), bud, status)
		}
		tw.Flush()
	}
	fmt.Fprintln(w)
	for _, n := range v.Notes {
		fmt.Fprintln(w, "note:", n)
	}
	if v.OK() {
		fmt.Fprintln(w, "bench: ok")
		return
	}
	sort.Slice(v.Problems, func(i, j int) bool {
		return v.Problems[i].Size+v.Problems[i].Metric < v.Problems[j].Size+v.Problems[j].Metric
	})
	for _, p := range v.Problems {
		fmt.Fprintf(w, "FAIL %s %s: %s: %s\n", p.Size, p.Metric, p.Kind, p.Detail)
	}
}

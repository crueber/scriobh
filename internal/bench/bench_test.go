package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSizes(t *testing.T) {
	got, err := ParseSizes("150k, 500K,1m,2500")
	if err != nil {
		t.Fatal(err)
	}
	want := []Size{{"150k", 150_000}, {"500k", 500_000}, {"1m", 1_000_000}, {"2500", 2500}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%v want %v", got[i], want[i])
		}
	}
	for _, bad := range []string{"", "abc", "-5k", "0", "k"} {
		if _, err := ParseSizes(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestQuantile(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if quantile(v, 0.5) != 5 || quantile(v, 0.95) != 10 || quantile(v, 0) != 1 || quantile(nil, 0.5) != 0 {
		t.Errorf("p50 %v p95 %v", quantile(v, 0.5), quantile(v, 0.95))
	}
}

func TestShapeIsAboutOneThousandDocumentsAtHalfAMillionWords(t *testing.T) {
	sh := shapeFor(500_000)
	total := sh.parts + sh.chapters + sh.scenes + sh.bigChapters
	if total < 900 || total > 1300 {
		t.Errorf("%d documents for 500,000 words: %+v", total, sh)
	}
	if small := shapeFor(3000); small.scenes < 4 || small.bigChapters != 1 {
		t.Errorf("small shape %+v", small)
	}
}

func TestGeneratorIsDeterministic(t *testing.T) {
	a, b := newWriter(7), newWriter(7)
	pa, pb := a.prose(500, []string{"Chapter 1: X Y"}), b.prose(500, []string{"Chapter 1: X Y"})
	if pa != pb || !strings.Contains(pa, "[[Chapter 1: X Y]]") || strings.Count(pa, "\n\n") < 3 {
		t.Error("the generator is not deterministic or lacks structure")
	}
	if newWriter(8).prose(500, nil) == pa {
		t.Error("two seeds give one text")
	}
	if a.vocab[60] != "harbour" || a.vocab[400] != "lantern" {
		t.Error("the query words are not at their ranks")
	}
}

func fakeReport(machine string, p50 float64, runs int, words int) *Report {
	return &Report{Schema: 1, Generator: GeneratorVersion, Seed: Seed, Machine: machine, Sizes: []SizeResult{{Name: "500k", Words: words, Metrics: []Metric{
		{Name: "doc.get", Unit: UnitMS, Runs: runs, P50: p50, P95: p50 * 1.5, Budget: 150, Enforced: words <= BudgetMaxWords},
		{Name: "doc.list", Unit: UnitMS, Runs: runs, P50: 0.4, P95: 0.6},
		{Name: "db.size", Unit: UnitBytes, Runs: 1, P50: 20 << 20, P95: 20 << 20},
	}}}}
}

func TestJudgeRegressionsBudgetsAndNoiseFloor(t *testing.T) {
	pol := Policy{Tolerance: 0.20}
	base := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}
	base.Record(fakeReport("m1", 40, 30, 500_000))

	// The same numbers pass.
	if v := Judge(fakeReport("m1", 40, 30, 500_000), base, pol); !v.OK() || !v.Compared {
		t.Errorf("unchanged: %+v", v)
	}
	// 19% slower passes; 25% slower fails, and the failure says which.
	if v := Judge(fakeReport("m1", 47.5, 30, 500_000), base, pol); !v.OK() {
		t.Errorf("19%% over failed: %+v", v.Problems)
	}
	v := Judge(fakeReport("m1", 50, 30, 500_000), base, pol)
	if v.OK() || len(v.Problems) != 1 || v.Problems[0].Kind != "regression" || v.Problems[0].Metric != "doc.get" || !strings.Contains(v.Problems[0].Detail, "25%") {
		t.Errorf("25%% over: %+v", v)
	}
	// A tolerance is configurable.
	if v := Judge(fakeReport("m1", 50, 30, 500_000), base, Policy{Tolerance: 0.30}); !v.OK() {
		t.Errorf("tolerance 30%%: %+v", v)
	}
	// A tiny number doubling is under the noise floor: 0.4 ms to 0.8 ms is jitter.
	r := fakeReport("m1", 40, 30, 500_000)
	r.Sizes[0].Metrics[1].P50 = 0.8
	if v := Judge(r, base, pol); !v.OK() {
		t.Errorf("a sub-millisecond change flagged: %+v", v.Problems)
	}
	// A timing taken once needs 50% to count, a repeated one 20%; sizes stay at 20%.
	once := func(p50 float64) *Report {
		return &Report{Schema: 1, Generator: GeneratorVersion, Seed: Seed, Machine: "m1", Sizes: []SizeResult{{Name: "500k", Metrics: []Metric{
			{Name: "search.rebuild", Unit: UnitMS, Runs: 1, P50: p50, P95: p50}}}}}
	}
	ob := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}
	ob.Record(once(1000))
	if v := Judge(once(1400), ob, pol); !v.OK() {
		t.Errorf("40%% over on one timing failed: %+v", v.Problems)
	}
	if v := Judge(once(1600), ob, pol); v.OK() {
		t.Error("60% over on one timing passed")
	}
	// A single-run metric has a larger floor than a repeated one.
	if noiseFloor(Metric{Unit: UnitMS, Runs: 1}) <= noiseFloor(Metric{Unit: UnitMS, Runs: 30}) {
		t.Error("the floor does not grow for noisier metrics")
	}
	// Size on disk: 20% over fails, a few KB do not.
	r = fakeReport("m1", 40, 30, 500_000)
	r.Sizes[0].Metrics[2].P50 = 20<<20 + 100<<10
	if v := Judge(r, base, pol); !v.OK() {
		t.Errorf("100 KB flagged: %+v", v.Problems)
	}
	r.Sizes[0].Metrics[2].P50 = 26 << 20
	if v := Judge(r, base, pol); v.OK() || v.Problems[0].Metric != "db.size" {
		t.Errorf("26 MB from 20 MB: %+v", v)
	}
	// An absolute budget fails on p95 even with no baseline at all.
	r = fakeReport("m2", 140, 30, 500_000) // p95 = 210 ms, budget 150
	v = Judge(r, base, pol)
	if v.OK() || v.Problems[0].Kind != "budget" || v.Compared {
		t.Errorf("budget: %+v", v)
	}
	if len(v.Notes) == 0 || !strings.Contains(v.Notes[0], "no entry for this machine") {
		t.Errorf("a machine with no baseline entry must say so: %v", v.Notes)
	}
	// Over a size the budgets are not stated for, the budget is shown, not enforced.
	r = fakeReport("m2", 140, 30, 1_000_000)
	if v := Judge(r, base, pol); !v.OK() || len(v.Notes) < 2 {
		t.Errorf("1M words: %+v", v)
	}
	// A baseline made by another generator or seed is not compared.
	b2 := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{"m1": {Generator: GeneratorVersion + 1, Seed: Seed, Sizes: base.Machines["m1"].Sizes}}}
	if v := Judge(fakeReport("m1", 60, 30, 500_000), b2, pol); v.Compared || !v.OK() {
		t.Errorf("a stale baseline compared: %+v", v)
	}
}

func TestDiskBoundMetricsAreNotJudgedAgainstTheBaseline(t *testing.T) {
	mk := func(p50 float64) *Report {
		return &Report{Schema: 1, Generator: GeneratorVersion, Seed: Seed, Machine: "m", Sizes: []SizeResult{{Name: "500k", Metrics: []Metric{
			{Name: "mirror.full_write", Unit: UnitMS, Runs: 1, P50: p50, P95: p50},
			{Name: "backup.create", Unit: UnitMS, Runs: 1, P50: p50, P95: p50, Budget: 30_000, Enforced: true},
		}}}}
	}
	base := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}
	base.Record(mk(1000))
	if v := Judge(mk(5000), base, Policy{Tolerance: 0.2}); !v.OK() {
		t.Errorf("a disk-bound metric was judged against the baseline: %+v", v.Problems)
	}
	if v := Judge(mk(40_000), base, Policy{Tolerance: 0.2}); v.OK() {
		t.Error("the backup budget was not enforced")
	}
}

func TestFailingPredicateDrivesTheRetry(t *testing.T) {
	base := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}
	base.Record(fakeReport("m1", 40, 30, 500_000))
	f := base.Failing("m1", Policy{Tolerance: 0.2})
	slow := fakeReport("m1", 60, 30, 500_000).Sizes[0].Metrics[0]
	fine := fakeReport("m1", 41, 30, 500_000).Sizes[0].Metrics[0]
	if !f("500k", slow) || f("500k", fine) {
		t.Error("the retry predicate")
	}
}

func TestBestKeepsTheFasterRun(t *testing.T) {
	a, b := fakeReport("m", 50, 30, 500_000), fakeReport("m", 40, 30, 500_000)
	b.Sizes[0].Metrics[1].P50 = 0.9
	got := Best(a, b)
	if got.Sizes[0].Metrics[0].P50 != 40 || got.Sizes[0].Metrics[1].P50 != 0.4 {
		t.Errorf("%+v", got.Sizes[0].Metrics)
	}
	if a.Sizes[0].Metrics[0].P50 != 50 {
		t.Error("Best changed its argument")
	}
}

func TestBaselineRoundTripAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.json")
	b, err := LoadBaseline(path) // missing is empty
	if err != nil || len(b.Machines) != 0 {
		t.Fatal(err, b)
	}
	b.Record(fakeReport("m1", 40, 30, 500_000))
	r2 := fakeReport("m2", 80, 30, 500_000)
	r2.Sizes[0].Name = "150k"
	b.Record(r2)
	if err := b.Save(path); err != nil {
		t.Fatal(err)
	}
	again, err := LoadBaseline(path)
	if err != nil || again.Machines["m1"].Sizes["500k"]["doc.get"].P50 != 40 || again.Machines["m2"].Sizes["150k"]["doc.get"].P50 != 80 {
		t.Errorf("%+v %v", again, err)
	}
	// Recording a size on a machine keeps the machine's other sizes.
	r3 := fakeReport("m1", 41, 30, 500_000)
	r3.Sizes[0].Name = "150k"
	again.Record(r3)
	if len(again.Machines["m1"].Sizes) != 2 || again.Machines["m1"].Sizes["500k"]["doc.get"].P50 != 40 {
		t.Errorf("%+v", again.Machines["m1"].Sizes)
	}
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path); err == nil {
		t.Error("a broken baseline file loaded")
	}
}

func TestOnlyABenchDirectoryIsWrittenInto(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "scriobh.db"), []byte("a real library"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := claim(dir); err == nil || !strings.Contains(err.Error(), "not a bench directory") {
		t.Errorf("a directory holding a library was claimed: %v", err)
	}
	empty := t.TempDir()
	if err := claim(empty); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(empty, Marker)); err != nil {
		t.Error("no marker written")
	}
	if err := claim(empty); err != nil { // and again, now that it carries the marker
		t.Error(err)
	}
	if _, err := Run(context.Background(), Options{Sizes: []Size{{"3k", 3000}}, DataDir: dir, Runs: 3}); err == nil {
		t.Error("Run wrote into a directory that is not a bench directory")
	}
}

// The smoke test: a tiny project, built through the real ops and measured end to
// end, with the report in the shape the nightly job and the baseline read.
func TestRunSmoke(t *testing.T) {
	var log bytes.Buffer
	dir := t.TempDir()
	rep, err := Run(context.Background(), Options{Sizes: []Size{{"3k", 3000}}, DataDir: dir, Runs: 3, Log: &log, Version: "test"})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if len(rep.Sizes) != 1 || rep.Sizes[0].Documents < 6 || rep.Sizes[0].Words < 2000 {
		t.Fatalf("the project: %+v", rep.Sizes)
	}
	names := map[string]Metric{}
	for _, m := range rep.Sizes[0].Metrics {
		names[m.Name] = m
		if m.P50 < 0 || m.P95 < m.P50 || m.Min > m.P50 || m.Max < m.P95 {
			t.Errorf("%s: %+v", m.Name, m)
		}
	}
	for _, want := range []string{"doc.get", "page.chapter", "page.project", "doc.list", "outline.get", "project.list", "search.query", "search.rare", "search.prefix", "page.search",
		"doc.update", "http.save", "revision.list", "revision.diff", "export.md.single", "export.md.bundle", "export.html.single", "export.html.bundle",
		"mirror.full_write", "mirror.rebuild_noop", "search.rebuild", "backup.create", "import.epub", "import.docx.h1", "import.docx.labels", "import.docx.none", "import.docx.500k", "import.look.150k", "import.look.500k", "import.draft.split", "import.pattern", "db.size", "search.semantic", "search.semantic.load",
		"studio.dashboard", "studio.calendar.all"} {
		m, ok := names[want]
		if !ok {
			t.Errorf("no %s metric", want)
			continue
		}
		if m.P50 <= 0 && want != "mirror.rebuild_noop" {
			t.Errorf("%s measured nothing: %+v", want, m)
		}
	}
	if names["db.size"].Unit != UnitBytes || names["db.size"].P50 < 100<<10 {
		t.Errorf("db.size %+v", names["db.size"])
	}
	// Budgets are attached and enforced at this size; the budgeted metrics met them.
	for _, n := range []string{"studio.dashboard", "studio.calendar.all"} {
		t.Logf("%s: p50 %.1f ms, p95 %.1f ms, budget %.0f", n, names[n].P50, names[n].P95, names[n].Budget)
	}
	for _, n := range []string{"doc.get", "search.query", "doc.update", "page.chapter", "studio.dashboard"} {
		if m := names[n]; m.Budget == 0 || !m.Enforced {
			t.Errorf("%s: budget %+v", n, m)
		}
	}
	// The JSON shape: what the nightly job uploads and a script reads.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Schema, Generator int
		Seed              int64
		Machine, Go, Date string
		Runs              int
		Sizes             []struct {
			Name      string
			Words     int
			Documents int
			Metrics   []struct {
				Name, Unit string
				P50, P95   float64
			}
		}
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Schema != 1 || back.Generator != GeneratorVersion || back.Seed != Seed || back.Machine != MachineID() || back.Runs != 3 || back.Sizes[0].Name != "3k" || len(back.Sizes[0].Metrics) < 20 {
		t.Errorf("report shape: %+v", back)
	}
	// With no baseline the budgets alone decide, and a tiny project meets them.
	if v := Judge(rep, &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}, Policy{Tolerance: 0.2}); !v.OK() {
		t.Logf("budgets on a loaded machine: %+v", v.Problems)
	}
	var table bytes.Buffer
	WriteTable(&table, rep, Judge(rep, nil, Policy{Tolerance: 0.2}))
	for _, want := range []string{"3k:", "doc.get", "search.query", "db.size", "p95"} {
		if !strings.Contains(table.String(), want) {
			t.Errorf("table lacks %q:\n%s", want, table.String())
		}
	}
	// A second run in the same directory reuses the project it built (no build line).
	var log2 bytes.Buffer
	if _, err := Run(context.Background(), Options{Sizes: []Size{{"3k", 3000}}, DataDir: dir, Runs: 3, Log: &log2}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log2.String(), "reusing the project") {
		t.Errorf("the project was built again:\n%s", log2.String())
	}
	// And a regression against a baseline recorded from the first run is found when the
	// numbers move: take the report, make the baseline twice as fast, and judge.
	b := &Baseline{Schema: 1, Machines: map[string]BaselineEntry{}}
	fast := *rep
	fast.Sizes = []SizeResult{rep.Sizes[0]}
	fast.Sizes[0].Metrics = append([]Metric(nil), rep.Sizes[0].Metrics...)
	for i := range fast.Sizes[0].Metrics {
		fast.Sizes[0].Metrics[i].P50 /= 3
	}
	b.Record(&fast)
	if v := Judge(rep, b, Policy{Tolerance: 0.2}); v.OK() {
		// Only metrics slower than the floor can regress; the db size is always large enough.
		t.Errorf("a baseline three times faster found no regression (db.size at least): %+v", v)
	}
}

// A retry rebuilds the fixture of a case that keeps state (a staged draft that
// the case discards). Forcing every measurement with a repeat to be taken once
// more must finish the run; the draft cases are the ones that used to fail
// with not_found, because their retry ran against a draft already discarded.
func TestRetryOfAStatefulCaseRebuildsItsFixture(t *testing.T) {
	var log bytes.Buffer
	dir := t.TempDir()
	forced := 0
	rep, err := Run(context.Background(), Options{
		Sizes:   []Size{{"3k", 3000}},
		DataDir: dir,
		Runs:    3,
		Log:     &log,
		Version: "test",
		Failing: func(size string, m Metric) bool { forced++; return true },
	})
	if err != nil {
		t.Fatalf("the run failed on a retry: %v\n%s", err, log.String())
	}
	if forced == 0 {
		t.Fatal("no measurement was offered to the retry")
	}
	for _, want := range []string{"import.draft.split looks slow", "import.draft.edit looks slow", "import.look.150k looks slow", "import.pattern looks slow", "import.draft looks slow", "import.undo.plan looks slow"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the retry of %q did not run:\n%s", want, log.String())
		}
	}
	for _, m := range rep.Sizes[0].Metrics {
		if m.P50 <= 0 && m.Name != "mirror.rebuild_noop" && m.Unit == UnitMS {
			t.Errorf("%s measured nothing after the retry: %+v", m.Name, m)
		}
	}
}

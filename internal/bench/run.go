package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/server"
)

// Marker is the file that says a directory belongs to the benchmark. A
// directory that holds anything else and has no marker is refused, so a bench
// run can never write into a real library.
const Marker = ".scriobh-bench"

// Options configure a run.
type Options struct {
	Sizes []Size
	// DataDir is where projects are built. Empty means a temporary directory
	// that is removed afterwards. A directory that already holds a previous
	// bench run is reused (generation is the slow part) unless Fresh is set.
	DataDir string
	Fresh   bool
	// Runs is how many times each repeatable operation is timed.
	Runs int
	// Log receives progress lines; nil is quiet.
	Log io.Writer
	// Version is recorded in the report.
	Version string
	// Failing, when set, says whether a measurement already looks like a
	// regression or a missed budget. Such a measurement is taken once more
	// while the project is still open, and the better of the two is kept: one
	// slow moment on a shared machine is not a regression.
	Failing func(size string, m Metric) bool

	// overrides replace the configuration start gives the server (the soak turns
	// the mirror on).
	overrides map[string]string
}

// Unit of a metric.
const (
	UnitMS    = "ms"
	UnitBytes = "bytes"
)

// Metric is one measured operation.
type Metric struct {
	Name string  `json:"name"`
	Unit string  `json:"unit"`
	Runs int     `json:"runs"`
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	// Budget is the absolute limit for p95 from docs/ARCHITECTURE.md, in the
	// metric's unit; 0 means the metric has no stated budget.
	Budget float64 `json:"budget,omitempty"`
	// Enforced is false when the project is larger than the size the budgets
	// are stated for (500,000 words): the budget is shown, not gated.
	Enforced bool `json:"enforced,omitempty"`
	// What the budget is called in the architecture document.
	Note string `json:"note,omitempty"`

	repeat func() (Metric, error) // times it again, for the retry; nil when it cannot repeat
}

// SizeResult is the result of one project size.
type SizeResult struct {
	Name       string   `json:"name"`
	Words      int      `json:"words"`
	Documents  int      `json:"documents"`
	GenSeconds float64  `json:"generate_seconds"`
	Metrics    []Metric `json:"metrics"`
}

// Report is a whole run.
type Report struct {
	Schema    int          `json:"schema"`
	Version   string       `json:"version"`
	Generator int          `json:"generator"`
	Seed      int64        `json:"seed"`
	Machine   string       `json:"machine"`
	Go        string       `json:"go"`
	Date      string       `json:"date"`
	Runs      int          `json:"runs"`
	Sizes     []SizeResult `json:"sizes"`
}

// MachineID fingerprints the host class a baseline was recorded on.
func MachineID() string {
	return fmt.Sprintf("%s/%s/%dcpu", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
}

// BudgetMaxWords is the project size the architecture's budgets are stated for.
const BudgetMaxWords = 500_000

// budget is a p95 limit from docs/ARCHITECTURE.md "Performance budgets" and
// "Budgets at 500,000 words, 1,000 documents".
type budget struct {
	limit float64
	note  string
}

var budgets = map[string]budget{
	"doc.get":        {150, "open a chapter < 150 ms"},
	"page.chapter":   {150, "chapter page < 150 ms"},
	"doc.update":     {120, "save a chapter < 120 ms"},
	"http.save":      {400, "autosave round trip < 400 ms"},
	"search.query":   {80, "search < 80 ms"},
	"search.rare":    {80, "search < 80 ms"},
	"search.prefix":  {80, "search < 80 ms"},
	"page.search":    {150, "search page < 150 ms"},
	"page.project":   {400, "project open, 1,000 docs < 400 ms"},
	"doc.list":       {400, "project open, 1,000 docs < 400 ms"},
	"outline.get":    {400, "project open, 1,000 docs < 400 ms"},
	"search.rebuild": {60_000, "FTS rebuild < 60 s"},
	// 10,000 chunks of 768 dimensions, after the question has been embedded (internal/bench/semantic.go).
	"search.semantic": {80, "semantic search < 80 ms, 10,000 chunks of 768 dimensions, excluding the embedding request"},
	// Five years of daily writing over 20 projects, every project every day (internal/bench/studio.go).
	"studio.dashboard":    {150, "writing dashboard < 150 ms, five years of daily data over 20 projects"},
	"studio.calendar.all": {150, "all-time activity calendar < 150 ms, five years of daily data over 20 projects"},
	"backup.create":       {30_000, "backup < 30 s"},
	"db.size":             {1 << 30, "database < 1 GB"},
	"import.epub":         {60_000, "import a 300-page EPUB with pictures < 60 s"},
	// The front of a 6x9 wrap at 300 dpi (3825 by 2775 px), made from nothing kept (internal/bench/cover.go).
	"cover.derive": {1500, "make the front of a 300-dpi 6x9 wrap < 1.5 s"},
}

// ---- the run ---------------------------------------------------------------------

// Run builds every size and measures it.
func Run(ctx context.Context, o Options) (*Report, error) {
	if o.Runs < 3 {
		o.Runs = 30
	}
	root := o.DataDir
	temp := root == ""
	if temp {
		d, err := os.MkdirTemp("", "scriobh-bench-")
		if err != nil {
			return nil, err
		}
		root = d
		defer os.RemoveAll(root)
	}
	if err := claim(root); err != nil {
		return nil, err
	}
	rep := &Report{Schema: 1, Version: o.Version, Generator: GeneratorVersion, Seed: Seed, Machine: MachineID(), Go: runtime.Version(),
		Date: time.Now().UTC().Format(time.RFC3339), Runs: o.Runs}
	for _, sz := range o.Sizes {
		res, err := runSize(ctx, o, filepath.Join(root, sz.Name), sz)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", sz.Name, err)
		}
		rep.Sizes = append(rep.Sizes, *res)
	}
	return rep, nil
}

// claim makes root a bench directory: it creates it, and refuses one that holds
// other files and no marker.
func claim(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, Marker)); err == nil {
		return nil
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(ents) > 0 {
		return fmt.Errorf("%s is not empty and is not a bench directory (no %s file); refusing to write into it", root, Marker)
	}
	return os.WriteFile(filepath.Join(root, Marker), []byte("scriobh bench data\n"), 0o644)
}

func logf(w io.Writer, f string, a ...any) {
	if w != nil {
		fmt.Fprintf(w, f, a...)
	}
}

// env is one open project and the handles the measurements use.
type env struct {
	ctx    context.Context
	dir    string
	d      *db.DB
	srv    *server.Server
	who    ops.Principal
	cookie *http.Cookie
	fx     *Fixture
	runs   int
	log    io.Writer
	over   map[string]string
}

type stored struct {
	Generator int      `json:"generator"`
	Seed      int64    `json:"seed"`
	Words     int      `json:"words"`
	Fixture   *Fixture `json:"fixture"`
	GenSecs   float64  `json:"generate_seconds"`
}

// open opens a data directory's database and the server over it.
func (o Options) open(ctx context.Context, dir string) (*env, error) {
	d, err := db.Open(filepath.Join(dir, "scriobh.db"))
	if err != nil {
		return nil, err
	}
	if _, err := d.Migrate(ctx, ""); err != nil {
		d.Close()
		return nil, err
	}
	e := &env{ctx: ctx, dir: dir, d: d, runs: o.Runs, log: o.Log, over: o.overrides}
	if err := e.start(); err != nil {
		d.Close()
		return nil, err
	}
	return e, nil
}

// prepare builds a size once into <dir>/base, or finds the one built earlier,
// and copies its database into a fresh <dir>/work.
func (o Options) prepare(ctx context.Context, dir string, sz Size) (string, *stored, error) {
	base, work := filepath.Join(dir, "base"), filepath.Join(dir, "work")
	var prev *stored
	if !o.Fresh {
		if b, err := os.ReadFile(filepath.Join(dir, "fixture.json")); err == nil {
			var s stored
			if _, serr := os.Stat(filepath.Join(base, "scriobh.db")); serr == nil && json.Unmarshal(b, &s) == nil &&
				s.Generator == GeneratorVersion && s.Seed == Seed && s.Words == sz.Words && s.Fixture != nil {
				prev = &s
			}
		}
	}
	if prev != nil {
		logf(o.Log, "%s: reusing the project already built in %s\n", sz.Name, base)
	} else {
		if err := os.RemoveAll(dir); err != nil {
			return "", nil, err
		}
		if err := os.MkdirAll(base, 0o755); err != nil {
			return "", nil, err
		}
		g, err := o.open(ctx, base)
		if err != nil {
			return "", nil, err
		}
		logf(o.Log, "%s: building about %d words ...\n", sz.Name, sz.Words)
		t0 := time.Now()
		last := time.Now()
		fx, err := Build(ctx, g.srv.Reg, g.who, sz, func(done, total int) {
			if time.Since(last) > 5*time.Second {
				last = time.Now()
				logf(o.Log, "  %d of %d documents\n", done, total)
			}
		})
		if err == nil {
			_, err = g.d.Writer().ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		}
		g.d.Close()
		if err != nil {
			return "", nil, err
		}
		gen := time.Since(t0).Seconds()
		prev = &stored{Generator: GeneratorVersion, Seed: Seed, Words: sz.Words, Fixture: fx, GenSecs: gen}
		raw, _ := json.Marshal(prev)
		if err := os.WriteFile(filepath.Join(dir, "fixture.json"), raw, 0o644); err != nil {
			return "", nil, err
		}
		logf(o.Log, "  built %d documents, %d words in %.1fs\n", fx.Docs, fx.Words, gen)
	}
	if err := os.RemoveAll(work); err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", nil, err
	}
	if err := copyFile(filepath.Join(base, "scriobh.db"), filepath.Join(work, "scriobh.db")); err != nil {
		return "", nil, err
	}
	return work, prev, nil
}

// runSize builds a size once into <dir>/base (or finds the one built earlier),
// then measures a copy of it in <dir>/work. The copy keeps measurements from
// changing what the next run starts from: saves add revisions, and a database
// that grew between runs would read as a regression in its own size.
func runSize(ctx context.Context, o Options, dir string, sz Size) (*SizeResult, error) {
	work, prev, err := o.prepare(ctx, dir, sz)
	if err != nil {
		return nil, err
	}
	res := &SizeResult{Name: sz.Name, GenSeconds: prev.GenSecs}
	e, err := o.open(ctx, work)
	if err != nil {
		return nil, err
	}
	defer e.d.Close()
	e.fx = prev.Fixture
	res.Words, res.Documents = e.fx.Words, e.fx.Docs
	ms, err := e.measure()
	if err != nil {
		return nil, err
	}
	for i := range ms {
		if b, ok := budgets[ms[i].Name]; ok {
			ms[i].Budget, ms[i].Note = b.limit, b.note
			ms[i].Enforced = sz.Words <= BudgetMaxWords
		}
	}
	if o.Failing != nil {
		for i := range ms {
			if ms[i].repeat == nil || !o.Failing(sz.Name, ms[i]) {
				continue
			}
			logf(o.Log, "  %s looks slow; measuring it once more\n", ms[i].Name)
			again, err := ms[i].repeat()
			if err != nil {
				return nil, err
			}
			if again.P50 < ms[i].P50 {
				again.Budget, again.Note, again.Enforced, again.repeat = ms[i].Budget, ms[i].Note, ms[i].Enforced, ms[i].repeat
				ms[i] = again
			}
		}
	}
	res.Metrics = ms
	return res, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// start builds the server (no listener) and signs in the one person the
// benchmark acts as.
func (e *env) start() error {
	cfg, err := config.Load(func(k string) (string, bool) {
		if v, ok := e.over[k]; ok {
			return v, true
		}
		switch k {
		case "SCRIOBH_DATA_DIR":
			return e.dir, true
		case "SCRIOBH_SECRET_KEY":
			return "bench-secret-key-bench-secret-key-0123456789", true
		case "SCRIOBH_OIDC_ISSUER":
			return "https://idp.bench.invalid", true
		case "SCRIOBH_OIDC_CLIENT_ID", "SCRIOBH_OIDC_CLIENT_SECRET":
			return "bench", true
		case "SCRIOBH_BASE_URL":
			return "http://127.0.0.1:8080", true
		case "SCRIOBH_RATE_LIMIT":
			return "0", true
		case "SCRIOBH_MIRROR":
			return "off", true
		case "SCRIOBH_BACKUP_INTERVAL_HOURS":
			return "0", true
		}
		return "", false // the environment of the process is never read: a bench run must not inherit a real configuration
	})
	if err != nil {
		return err
	}
	srv, err := server.New(server.Deps{Cfg: cfg, DB: e.d, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		return err
	}
	e.srv = srv
	res, err := srv.Users.SignIn(e.ctx, "bench", auth.Claims{Subject: "bench", Email: "bench@bench.invalid", EmailVerified: true, Name: "Bench", Groups: []string{cfg.AdminGroup}})
	if err != nil {
		return err
	}
	e.who = ops.Principal{UserID: res.User.ID, Role: res.User.Role, Via: "session"}
	tok, _, err := srv.Sess.Create(e.ctx, res.User.ID, "bench", "127.0.0.1")
	if err != nil {
		return err
	}
	e.cookie = &http.Cookie{Name: auth.SessionCookie, Value: tok}
	return nil
}

// call runs an op as the bench user.
func (e *env) call(name string, in any) (any, error) { return e.srv.Reg.Call(e.ctx, e.who, name, in) }

// http sends a request through the full handler chain (middleware, authorization,
// templates) without a socket.
func (e *env) http(method, path string, body []byte) (int, int, error) {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(e.cookie)
	rec := httptest.NewRecorder()
	e.srv.Handler().ServeHTTP(rec, req)
	if rec.Code >= 400 {
		return rec.Code, 0, fmt.Errorf("%s %s answered %d: %.200s", method, path, rec.Code, rec.Body.String())
	}
	return rec.Code, rec.Body.Len(), nil
}

// ---- measurements -----------------------------------------------------------------------

// timeIt times fn runs times after one untimed warm-up.
func timeIt(name string, runs int, warm bool, fn func(i int) error) (Metric, error) {
	m := Metric{Name: name, Unit: UnitMS, Runs: runs}
	if warm {
		if err := fn(-1); err != nil {
			return m, fmt.Errorf("%s: %w", name, err)
		}
	}
	runtime.GC()
	t := make([]float64, 0, runs)
	for i := 0; i < runs; i++ {
		start := time.Now()
		if err := fn(i); err != nil {
			return m, fmt.Errorf("%s: %w", name, err)
		}
		t = append(t, float64(time.Since(start).Microseconds())/1000)
	}
	m.fill(t)
	return m, nil
}

func (m *Metric) fill(t []float64) {
	sort.Float64s(t)
	m.Runs = len(t)
	m.Min, m.Max = round(t[0]), round(t[len(t)-1])
	m.P50, m.P95 = round(quantile(t, 0.50)), round(quantile(t, 0.95))
}

// quantile is the nearest-rank percentile of sorted values.
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted))*q+0.999999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func round(v float64) float64 {
	if v >= 100 {
		return float64(int(v*10+0.5)) / 10
	}
	return float64(int(v*1000+0.5)) / 1000
}

func (e *env) measure() ([]Metric, error) {
	var out []Metric
	fx, n := e.fx, e.runs
	if len(fx.Chapters) == 0 {
		return nil, errors.New("the project has no long chapter")
	}
	exportRuns := n / 6
	if exportRuns < 3 {
		exportRuns = 3
	}
	add := func(m Metric, err error) error {
		if err != nil {
			return err
		}
		logf(e.log, "  %-18s p50 %9.3f  p95 %9.3f %s\n", m.Name, m.P50, m.P95, m.Unit)
		out = append(out, m)
		return nil
	}
	// repeatable times fn and remembers how to time it again, for the retry of a
	// measurement that looks like a regression.
	repeatable := func(name string, runs int, warm bool, fn func(i int) error) error {
		m, err := timeIt(name, runs, warm, fn)
		if err != nil {
			return err
		}
		m.repeat = func() (Metric, error) { return timeIt(name, runs, false, fn) }
		out = append(out, m)
		logf(e.log, "  %-18s p50 %9.3f  p95 %9.3f %s\n", m.Name, m.P50, m.P95, m.Unit)
		return nil
	}

	chapter := func(i int) string { return fx.Chapters[(i+len(fx.Chapters)*2)%len(fx.Chapters)] }
	slug := fx.Slug
	steps := []struct {
		name string
		runs int
		warm bool
		fn   func(i int) error
	}{
		{"doc.get", n, true, func(i int) error { _, err := e.call("doc.get", docs.DocIn{ID: chapter(i)}); return err }},
		{"page.chapter", n, true, func(i int) error {
			_, _, err := e.http("GET", "/p/"+slug+"/d/"+chapter(i), nil)
			return err
		}},
		{"page.project", n, true, func(i int) error { _, _, err := e.http("GET", "/p/"+slug, nil); return err }},
		{"doc.list", n, true, func(i int) error { _, err := e.call("doc.list", docs.ListIn{ProjectID: fx.ProjectID}); return err }},
		{"outline.get", n, true, func(i int) error {
			_, err := e.call("outline.get", docs.OutlineIn{ProjectID: fx.ProjectID})
			return err
		}},
		{"project.list", n, true, func(i int) error {
			_, err := e.call("project.list", projects.ListIn{State: projects.StateActive})
			return err
		}},
		{"project.get", n, true, func(i int) error { _, err := e.call("project.get", projects.GetIn{ID: fx.ProjectID}); return err }},
		{"search.query", n, true, func(i int) error {
			_, err := e.call("search.query", search.QueryIn{ProjectID: fx.ProjectID, Q: fx.CommonQuery})
			return err
		}},
		{"search.rare", n, true, func(i int) error {
			_, err := e.call("search.query", search.QueryIn{ProjectID: fx.ProjectID, Q: fx.RareQuery})
			return err
		}},
		{"search.prefix", n, true, func(i int) error {
			_, err := e.call("search.query", search.QueryIn{ProjectID: fx.ProjectID, Q: fx.PrefixQuery})
			return err
		}},
		{"page.search", n, true, func(i int) error {
			_, _, err := e.http("GET", "/p/"+slug+"/search?q="+strings.ReplaceAll(fx.CommonQuery, " ", "+"), nil)
			return err
		}},
	}
	for _, s := range steps {
		if err := repeatable(s.name, s.runs, s.warm, s.fn); err != nil {
			return nil, err
		}
	}

	// Saves change the text every time and chain the revision, as autosave does.
	target := fx.Chapters[0]
	cur, err := e.call("doc.get", docs.DocIn{ID: target})
	if err != nil {
		return nil, err
	}
	rev, body := cur.(docs.Item).Item.Revision, cur.(docs.Item).Item.Content
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	save := func(i int) error {
		lines[len(lines)-1] = fmt.Sprintf("The last line, edited on pass %d of the benchmark.", rev+i+100)
		c := strings.Join(lines, "\n") + "\n"
		o, err := e.call("doc.update", docs.UpdateIn{ID: target, BaseRevision: rev, Content: &c})
		if err != nil {
			return err
		}
		rev = o.(docs.Item).Item.Revision
		return nil
	}
	if err := repeatable("doc.update", n, true, save); err != nil {
		return nil, err
	}
	httpSave := func(i int) error {
		lines[len(lines)-1] = fmt.Sprintf("The last line, saved over HTTP on pass %d.", rev+i+100)
		c := strings.Join(lines, "\n") + "\n"
		b, _ := json.Marshal(map[string]any{"base_revision": rev, "content": c})
		if _, _, err := e.http("PATCH", "/api/v1/docs/"+target, b); err != nil {
			return err
		}
		rev++
		return nil
	}
	if err := repeatable("http.save", n, true, httpSave); err != nil {
		return nil, err
	}

	// Revisions: list and diff on the chapter that has a history.
	revs := fx.ChapterRevs[target]
	if len(revs) >= 2 {
		if err := repeatable("revision.list", n, true, func(i int) error {
			_, err := e.call("revision.list", revisions.ListIn{ID: target, Limit: 50})
			return err
		}); err != nil {
			return nil, err
		}
		if err := repeatable("revision.diff", n, true, func(i int) error {
			_, err := e.call("revision.diff", revisions.DiffIn{ID: target, From: revs[0], To: "current"})
			return err
		}); err != nil {
			return nil, err
		}
	}

	// Exports build the whole book, so they are timed fewer times.
	exports := []struct{ name, op, mode string }{
		{"export.md.single", "export.markdown", "single"},
		{"export.md.bundle", "export.markdown", "bundle"},
		{"export.html.single", "export.html", "single"},
		{"export.html.bundle", "export.html", "bundle"},
	}
	for _, x := range exports {
		if _, ok := e.srv.Reg.Lookup(x.op); !ok {
			continue
		}
		x := x
		in := any(export.MarkdownIn{ProjectID: fx.ProjectID, Mode: x.mode})
		if x.op == "export.html" {
			in = export.HTMLIn{ProjectID: fx.ProjectID, Mode: x.mode}
		}
		if err := repeatable(x.name, exportRuns, false, func(i int) error { _, err := e.call(x.op, in); return err }); err != nil {
			return nil, err
		}
	}

	// One-shot measurements: the mirror's first full write, a no-op rebuild, the search
	// index rebuild and a backup. They run once; they are not repeatable.
	one := func(name string, fn func() error) error {
		start := time.Now()
		if err := fn(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		m := Metric{Name: name, Unit: UnitMS, Runs: 1}
		m.fill([]float64{float64(time.Since(start).Microseconds()) / 1000})
		return add(m, nil)
	}
	mir := mirror.New(mirror.Options{DataDir: e.dir, DB: e.d})
	for _, name := range []string{"mirror.full_write", "mirror.rebuild_noop"} {
		if err := one(name, func() error {
			st, err := mir.Rebuild(e.ctx, fx.ProjectID)
			if err == nil && st.LastError != "" {
				err = errors.New(st.LastError)
			}
			return err
		}); err != nil {
			return nil, err
		}
	}
	if err := one("search.rebuild", func() error { return search.Rebuild(e.ctx, e.d, nil) }); err != nil {
		return nil, err
	}
	bk := &backup.Service{DB: e.d, DataDir: e.dir, Version: "bench"}
	if err := one("backup.create", func() error { _, err := bk.Create(e.ctx, ""); return err }); err != nil {
		return nil, err
	}

	// The size on disk, after the log is folded into the main file.
	if _, err := e.d.Writer().ExecContext(e.ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return nil, err
	}
	st, err := os.Stat(filepath.Join(e.dir, "scriobh.db"))
	if err != nil {
		return nil, err
	}
	size := Metric{Name: "db.size", Unit: UnitBytes, Runs: 1}
	size.fill([]float64{float64(st.Size())})
	out = append(out, size)
	logf(e.log, "  %-18s %d bytes\n", "db.size", st.Size())

	// A 300-page EPUB with pictures, previewed and applied as a new project, which is what a person waits for.
	book, err := SyntheticEPUB(60, 120)
	if err != nil {
		return nil, err
	}
	// Last, because each run adds a project to the database the other measurements read.
	if err := repeatable("import.epub", max(3, n/10), false, func(i int) error {
		pv, err := e.call("import.preview", imports.PreviewIn{Name: "bench.epub", Epub: book})
		if err != nil {
			return err
		}
		_, err = e.call("import.apply", imports.ApplyIn{Token: pv.(imports.PreviewOut).Token, Name: "bench.epub", Epub: book})
		return err
	}); err != nil {
		return nil, err
	}

	// After the size is taken: the synthetic index would add some 8 MB to it.
	if err := e.measureSemantic(n, repeatable, one); err != nil {
		return nil, err
	}
	// Likewise the dashboard's fixture (36,520 daily rows and as many sessions).
	if err := e.measureStudio(n, repeatable); err != nil {
		return nil, err
	}
	// A project of its own (a 10-megapixel picture): after the size, which it would add to.
	if err := e.measureCover(n, repeatable); err != nil {
		return nil, err
	}
	return out, nil
}

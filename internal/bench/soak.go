package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// The long-run soak (docs/ROADMAP.md, Phase 6): a generated project is served on
// a loopback socket with every background worker running (the mirror, the Git
// and webhook queues, the sweeps), and for the whole duration a writer saves,
// searches, previews and compiles the book, exports it, makes and deletes
// alternatives, takes snapshots and backups, while a reader opens pages. The
// process is sampled at a fixed interval.
//
// The rule for "grows without bound": after the warm-up, the samples are split
// into three equal spans of time, and the median of the last third may not
// exceed the median of the first third by more than the gauge's margin (the
// larger of a fraction and an absolute amount, so a tiny base does not make
// noise look like growth). Every budgeted operation must also keep its p95
// within the budget of docs/ARCHITECTURE.md over the whole soak and over its
// last third.

// SoakOptions configure a soak.
type SoakOptions struct {
	// Duration of the measured run for each size, warm-up included.
	Duration time.Duration
	// Sample is the sampling interval (default 5 s).
	Sample time.Duration
	// Warmup is left out of the growth rule (default a sixth of Duration, at
	// least 30 s and at most 5 min).
	Warmup time.Duration
}

// SoakSample is one sample of the process.
type SoakSample struct {
	At  float64 `json:"at_s"`
	RSS int64   `json:"rss_bytes"`
	// Footprint is the memory the process owns outright: dirty and swapped pages,
	// not the clean file pages (the binary, the mapped database) the system can
	// take back at any time. It is reported beside RSS and not judged; 0 where it
	// cannot be read.
	Footprint int64 `json:"footprint_bytes"`
	HeapLive  int64 `json:"heap_live_bytes"`
	// GoMapped is the memory the Go runtime holds from the system (all its
	// classes but the heap it has released). RSS minus this is what the process
	// holds outside the runtime: SQLite's page caches (the driver's own
	// allocator, not Go's) and the mapped database.
	GoMapped   int64 `json:"go_mapped_bytes"`
	Goroutines int64 `json:"goroutines"`
	OpenFiles  int64 `json:"open_files"`
	WAL        int64 `json:"wal_bytes"`
	DB         int64 `json:"db_bytes"`
}

// SoakGauge is the growth verdict for one sampled quantity.
type SoakGauge struct {
	Name    string  `json:"name"`
	Unit    string  `json:"unit"`
	First   float64 `json:"first_third_median"`
	Last    float64 `json:"last_third_median"`
	Max     float64 `json:"max"`
	Allowed float64 `json:"allowed"`
	Rule    string  `json:"rule"`
	OK      bool    `json:"ok"`
}

// SoakOp is the timing of one operation over the soak.
type SoakOp struct {
	Name    string  `json:"name"`
	Count   int     `json:"count"`
	Errors  int     `json:"errors"`
	P50     float64 `json:"p50_ms"`
	P95     float64 `json:"p95_ms"`
	Max     float64 `json:"max_ms"`
	LastP95 float64 `json:"last_third_p95_ms"`
	Budget  float64 `json:"budget_ms,omitempty"`
	Note    string  `json:"note,omitempty"`
	OK      bool    `json:"ok"`
}

// SoakResult is one size's soak.
type SoakResult struct {
	Size       string       `json:"size"`
	Words      int          `json:"words"`
	Documents  int          `json:"documents"`
	Seconds    float64      `json:"seconds"`
	Warmup     float64      `json:"warmup_s"`
	Machine    string       `json:"machine"`
	Go         string       `json:"go"`
	Date       string       `json:"date"`
	Gauges     []SoakGauge  `json:"gauges"`
	Ops        []SoakOp     `json:"ops"`
	Samples    []SoakSample `json:"samples"`
	Revisions  int          `json:"revisions_written"`
	Backups    int          `json:"backups"`
	MirrorSync bool         `json:"mirror_in_sync_at_end"`
	// What the last DOCX compile held, to show it compiled the whole book.
	CompileBytes int      `json:"compile_bytes"`
	CompileWords int      `json:"compile_words"`
	CompileDocs  int      `json:"compile_documents"`
	Problems     []string `json:"problems,omitempty"`
}

// OK reports whether the soak passed.
func (r *SoakResult) OK() bool { return len(r.Problems) == 0 }

// gaugeRule is the margin for one gauge: last-third median <= first-third
// median + max(Frac*first, Abs).
type gaugeRule struct {
	frac float64
	abs  float64
}

var gaugeRules = map[string]gaugeRule{
	"rss":        {0.25, 32 << 20},
	"heap_live":  {0.25, 16 << 20},
	"go_mapped":  {0.25, 32 << 20},
	"goroutines": {0.10, 10},
	"open_files": {0.10, 8},
	"wal":        {0.50, 16 << 20},
	// The database grows by design while a book is written: every save is an
	// event, an autosave revision is kept per document every five minutes, and
	// snapshots and exports are recorded. The margin allows that and catches
	// growth out of proportion to it.
	"db": {0.25, 32 << 20},
}

// soakBudgets are the p95 limits the soak holds each operation to, from
// docs/ARCHITECTURE.md. compile.docx is stated for 150k and 500k words.
func soakBudgets(words int) map[string]budget {
	b := map[string]budget{
		"http.save":     {400, "autosave round trip < 400 ms"},
		"doc.get":       {150, "open a chapter < 150 ms"},
		"page.chapter":  {150, "chapter page < 150 ms"},
		"page.project":  {400, "project open < 400 ms"},
		"search.query":  {80, "search < 80 ms"},
		"backup.create": {30_000, "backup < 30 s"},
	}
	switch {
	case words <= 150_000:
		b["compile.docx"] = budget{10_000, "DOCX compile, 150k words < 10 s"}
	case words <= BudgetMaxWords:
		b["compile.docx"] = budget{30_000, "DOCX compile, 500k words < 30 s"}
	}
	return b
}

// Soak runs the soak for each size in o.Sizes.
func Soak(ctx context.Context, o Options, so SoakOptions) ([]*SoakResult, error) {
	if so.Duration <= 0 {
		return nil, errors.New("a soak needs a duration")
	}
	if so.Sample <= 0 {
		so.Sample = 5 * time.Second
	}
	if so.Warmup <= 0 {
		so.Warmup = min(max(so.Duration/6, 30*time.Second), 5*time.Minute)
	}
	if so.Warmup >= so.Duration {
		so.Warmup = so.Duration / 6
	}
	root := o.DataDir
	if root == "" {
		d, err := os.MkdirTemp("", "scriobh-soak-")
		if err != nil {
			return nil, err
		}
		root = d
		defer os.RemoveAll(root)
	}
	if err := claim(root); err != nil {
		return nil, err
	}
	var out []*SoakResult
	for _, sz := range o.Sizes {
		r, err := soakSize(ctx, o, so, filepath.Join(root, sz.Name), sz)
		if err != nil {
			return out, fmt.Errorf("%s: %w", sz.Name, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// timing is one timed operation.
type timing struct {
	at float64 // seconds since the start
	ms float64
}

type recorder struct {
	mu    sync.Mutex
	start time.Time
	ops   map[string][]timing
	errs  map[string]int
	first map[string]string // the first error of each operation
}

func (r *recorder) time(name string, fn func() error) error {
	t0 := time.Now()
	err := fn()
	d := float64(time.Since(t0).Microseconds()) / 1000
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.errs[name]++
		if r.first[name] == "" {
			r.first[name] = err.Error()
		}
		return err
	}
	r.ops[name] = append(r.ops[name], timing{at: t0.Sub(r.start).Seconds(), ms: d})
	return nil
}

// soakClient talks to the served instance over the loopback socket.
type soakClient struct {
	base   string
	c      *http.Client
	cookie *http.Cookie
}

// raw sends in as JSON and returns the body of a successful answer.
func (s *soakClient) raw(method, path string, in any) ([]byte, error) {
	var buf bytes.Buffer
	err := s.send(method, path, in, func(r io.Reader) error { _, err := io.Copy(&buf, r); return err })
	return buf.Bytes(), err
}

func (s *soakClient) do(method, path string, in any, out any) error {
	return s.send(method, path, in, func(r io.Reader) error {
		if out != nil {
			return json.NewDecoder(r).Decode(out)
		}
		_, err := io.Copy(io.Discard, r)
		return err
	})
}

func (s *soakClient) send(method, path string, in any, read func(io.Reader) error) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(s.cookie)
	resp, err := s.c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("%s %s answered %d: %s", method, path, resp.StatusCode, b)
	}
	return read(resp.Body)
}

type docOut struct {
	Item struct {
		Revision int    `json:"revision"`
		Content  string `json:"content"`
	} `json:"item"`
}

func soakSize(ctx context.Context, o Options, so SoakOptions, dir string, sz Size) (*SoakResult, error) {
	work, prev, err := o.prepare(ctx, dir, sz)
	if err != nil {
		return nil, err
	}
	o.overrides = map[string]string{"SCRIOBH_MIRROR": "on", "SCRIOBH_MIRROR_DEBOUNCE_MS": "500", "SCRIOBH_BACKUP_KEEP": "1"}
	e, err := o.open(ctx, work)
	if err != nil {
		return nil, err
	}
	defer e.d.Close()
	fx := prev.Fixture
	if len(fx.Chapters) == 0 || len(fx.Chapters)+len(fx.Scenes) < 2 {
		return nil, errors.New("the project needs a long chapter and something else to save")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	sctx, stopServer := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- e.srv.Serve(sctx, ln) }()
	defer func() {
		stopServer()
		<-served
	}()
	cl := &soakClient{base: "http://" + ln.Addr().String(), cookie: e.cookie,
		c: &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}}}
	defer cl.c.CloseIdleConnections()

	res := &SoakResult{Size: sz.Name, Words: fx.Words, Documents: fx.Docs, Machine: MachineID(), Go: runtime.Version(),
		Date: time.Now().UTC().Format(time.RFC3339), Warmup: so.Warmup.Seconds()}
	rec := &recorder{start: time.Now(), ops: map[string][]timing{}, errs: map[string]int{}, first: map[string]string{}}
	var revsBefore int
	_ = e.d.Read().QueryRowContext(ctx, `SELECT count(*) FROM document_revisions`).Scan(&revsBefore)

	deadline := rec.start.Add(so.Duration)
	rctx, stop := context.WithDeadline(ctx, deadline)
	defer stop()
	var wg sync.WaitGroup

	// The sampler.
	var samples []SoakSample
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(so.Sample)
		defer t.Stop()
		for {
			samples = append(samples, sample(rec.start, work))
			select {
			case <-rctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	// The reader: pages and documents, as another tab would.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; rctx.Err() == nil; i++ {
			ch := fx.Chapters[i%len(fx.Chapters)]
			_ = rec.time("page.chapter", func() error { return cl.do("GET", "/p/"+fx.Slug+"/d/"+ch, nil, nil) })
			if i%10 == 0 {
				_ = rec.time("page.project", func() error { return cl.do("GET", "/p/"+fx.Slug, nil, nil) })
			}
			if len(fx.Scenes) > 0 {
				sc := fx.Scenes[i%len(fx.Scenes)]
				_ = rec.time("doc.get", func() error { return cl.do("GET", "/api/v1/docs/"+sc, nil, nil) })
			}
			sleep(rctx, 100*time.Millisecond)
		}
	}()

	// The writer. Chapter 0 carries the alternatives; the others are saved.
	saved := append(append([]string{}, fx.Chapters[1:]...), fx.Scenes...)
	if len(saved) > 8 {
		saved = saved[:8]
	}
	type doc struct {
		rev   int
		lines []string
	}
	state := map[string]*doc{}
	load := func(id string) (*doc, error) {
		var d docOut
		if err := cl.do("GET", "/api/v1/docs/"+id, nil, &d); err != nil {
			return nil, err
		}
		return &doc{rev: d.Item.Revision, lines: strings.Split(strings.TrimRight(d.Item.Content, "\n"), "\n")}, nil
	}
	for _, id := range saved {
		d, err := load(id)
		if err != nil {
			return nil, err
		}
		state[id] = d
	}
	queries := []string{fx.CommonQuery, fx.RareQuery, fx.PrefixQuery}
	every := func(last *time.Time, d time.Duration) bool {
		if time.Since(*last) >= d {
			*last = time.Now()
			return true
		}
		return false
	}
	var lastAlt, lastPreview, lastCompile, lastExport, lastSnap, lastBackup time.Time
	now := time.Now()
	lastAlt, lastPreview, lastCompile, lastExport, lastSnap = now, now, now, now, now
	lastBackup = now.Add(-4 * time.Minute) // the first backup a minute in
	backups := 0
	for i := 0; rctx.Err() == nil; i++ {
		id := saved[i%len(saved)]
		d := state[id]
		d.lines[len(d.lines)-1] = fmt.Sprintf("The last line, saved by the soak on pass %d at %s.", i, time.Now().Format(time.RFC3339Nano))
		content := strings.Join(d.lines, "\n") + "\n"
		var out docOut
		err := rec.time("http.save", func() error {
			return cl.do("PATCH", "/api/v1/docs/"+id, map[string]any{"base_revision": d.rev, "content": content}, &out)
		})
		if err == nil {
			d.rev = out.Item.Revision
		} else if nd, lerr := load(id); lerr == nil {
			state[id] = nd
		}
		q := queries[i%len(queries)]
		_ = rec.time("search.query", func() error {
			return cl.do("GET", "/api/v1/projects/"+fx.ProjectID+"/search?q="+strings.ReplaceAll(q, " ", "+"), nil, nil)
		})

		switch {
		case every(&lastAlt, 15*time.Second):
			_ = rec.time("alternative.cycle", func() error { return altCycle(cl, fx.Chapters[0], i) })
		case every(&lastPreview, 30*time.Second):
			_ = rec.time("compile.preview", func() error {
				return cl.do("POST", "/api/v1/projects/"+fx.ProjectID+"/compile/preview", map[string]any{}, nil)
			})
		case every(&lastCompile, 2*time.Minute):
			// Over REST the answer is the file itself.
			var file []byte
			if rec.time("compile.docx", func() error {
				var err error
				file, err = cl.raw("POST", "/api/v1/projects/"+fx.ProjectID+"/export/compile", map[string]any{"preset": "shunn-us"})
				if err == nil && !bytes.HasPrefix(file, []byte("PK")) {
					err = fmt.Errorf("the compile is not a DOCX (%d bytes)", len(file))
				}
				return err
			}) == nil {
				res.CompileBytes = len(file)
			}
		case every(&lastExport, time.Minute):
			_ = rec.time("export.md.bundle", func() error {
				return cl.do("POST", "/api/v1/projects/"+fx.ProjectID+"/export", map[string]any{"mode": "bundle"}, nil)
			})
		case every(&lastSnap, 2*time.Minute):
			_ = rec.time("revision.snapshot", func() error {
				return cl.do("POST", "/api/v1/docs/"+id+"/revisions", map[string]any{"label": fmt.Sprintf("Soak %d", i)}, nil)
			})
		case every(&lastBackup, 5*time.Minute):
			var b struct {
				Item struct {
					ID string `json:"id"`
				} `json:"item"`
			}
			if rec.time("backup.create", func() error { return cl.do("POST", "/api/v1/admin/backups", map[string]any{}, &b) }) == nil {
				backups++
				_ = os.Remove(filepath.Join(work, "backups", b.Item.ID+".zip")) // manual backups are never deleted by the server
			}
		}
		sleep(rctx, 50*time.Millisecond)
	}
	wg.Wait()
	res.Seconds = time.Since(rec.start).Seconds()
	res.Backups = backups

	// The mirror must be current shortly after the writing stops.
	waitUntil := time.Now().Add(30 * time.Second)
	for time.Now().Before(waitUntil) && ctx.Err() == nil {
		var st struct {
			Item struct {
				InSync bool `json:"in_sync"`
			} `json:"item"`
		}
		if cl.do("GET", "/api/v1/projects/"+fx.ProjectID+"/mirror", nil, &st) == nil && st.Item.InSync {
			res.MirrorSync = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !res.MirrorSync {
		var st map[string]any
		_ = cl.do("GET", "/api/v1/projects/"+fx.ProjectID+"/mirror", nil, &st)
		res.Problems = append(res.Problems, fmt.Sprintf("the mirror was not in sync 30 s after the writing stopped: %v", st))
	}
	var revsAfter int
	_ = e.d.Read().QueryRowContext(ctx, `SELECT count(*) FROM document_revisions`).Scan(&revsAfter)
	res.Revisions = revsAfter - revsBefore

	res.Samples = samples
	judgeSoak(res, rec, so, soakBudgets(sz.Words))
	return res, nil
}

// altCycle makes an alternative on one word of the chapter, then deletes it.
func altCycle(cl *soakClient, id string, i int) error {
	var d docOut
	if err := cl.do("GET", "/api/v1/docs/"+id, nil, &d); err != nil {
		return err
	}
	word := pickWord(d.Item.Content, i)
	if word == "" {
		return errors.New("no word to make an alternative of")
	}
	var a struct {
		Alternative struct {
			ID string `json:"id"`
		} `json:"alternative"`
		Revision int `json:"revision"`
	}
	if err := cl.do("POST", "/api/v1/docs/"+id+"/alternatives", map[string]any{"base_revision": d.Item.Revision, "scope": "word", "quote": word, "occurrence": 0,
		"variants": []string{"otherwise"}}, &a); err != nil {
		return err
	}
	return cl.do("POST", "/api/v1/docs/"+id+"/alternatives/delete", map[string]any{"base_revision": a.Revision, "alternative_id": a.Alternative.ID}, nil)
}

// pickWord returns a plain word of six letters or more from the text, a
// different one each pass.
func pickWord(text string, i int) string {
	var words []string
	seen := map[string]bool{}
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) }) {
		if len(f) >= 6 && !seen[f] {
			seen[f] = true
			words = append(words, f)
			if len(words) >= 50 {
				break
			}
		}
	}
	if len(words) == 0 {
		return ""
	}
	return words[i%len(words)]
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// ---- sampling ----------------------------------------------------------------

var memSamples = []metrics.Sample{{Name: "/gc/heap/live:bytes"}, {Name: "/memory/classes/total:bytes"}, {Name: "/memory/classes/heap/released:bytes"}}

func sample(start time.Time, dir string) SoakSample {
	s := SoakSample{At: time.Since(start).Seconds(), Goroutines: int64(runtime.NumGoroutine()), RSS: rss(), Footprint: footprint(), OpenFiles: openFiles()}
	metrics.Read(memSamples)
	u := func(i int) int64 {
		if memSamples[i].Value.Kind() == metrics.KindUint64 {
			return int64(memSamples[i].Value.Uint64())
		}
		return 0
	}
	s.HeapLive, s.GoMapped = u(0), u(1)-u(2)
	if fi, err := os.Stat(filepath.Join(dir, "scriobh.db-wal")); err == nil {
		s.WAL = fi.Size()
	}
	if fi, err := os.Stat(filepath.Join(dir, "scriobh.db")); err == nil {
		s.DB = fi.Size()
	}
	return s
}

// rss is the resident set size of this process now (not its peak), or 0 where
// it cannot be read.
func rss() int64 {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/self/statm")
		if err != nil {
			return 0
		}
		f := strings.Fields(string(b))
		if len(f) < 2 {
			return 0
		}
		pages, _ := strconv.ParseInt(f[1], 10, 64)
		return pages * int64(os.Getpagesize())
	case "darwin", "freebsd", "openbsd", "netbsd":
		out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			return 0
		}
		kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		return kb << 10
	}
	return 0
}

// footprint is the memory this process owns outright (see SoakSample), or 0
// where it cannot be read: "Footprint" of footprint(1) on macOS, RssAnon (the
// anonymous resident pages) on Linux.
func footprint() int64 {
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/self/status")
		if err != nil {
			return 0
		}
		return parseRssAnon(string(b))
	case "darwin":
		out, err := exec.Command("footprint", "-p", strconv.Itoa(os.Getpid())).Output()
		if err != nil {
			return 0
		}
		return parseFootprint(string(out))
	}
	return 0
}

// parseRssAnon reads the "RssAnon:   123456 kB" line of /proc/self/status.
func parseRssAnon(status string) int64 {
	for _, ln := range strings.Split(status, "\n") {
		if f := strings.Fields(ln); len(f) >= 2 && f[0] == "RssAnon:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

// parseFootprint reads the total from the header line of footprint(1):
// "name [pid]: 64-bit    Footprint: 237 MB (16384 bytes per page)".
func parseFootprint(out string) int64 {
	_, rest, ok := strings.Cut(out, "Footprint:")
	if !ok {
		return 0
	}
	f := strings.Fields(rest)
	if len(f) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0
	}
	switch f[1] {
	case "B":
	case "KB":
		v *= 1 << 10
	case "MB":
		v *= 1 << 20
	case "GB":
		v *= 1 << 30
	default:
		return 0
	}
	return int64(v)
}

// openFiles counts this process's open descriptors, or 0 where it cannot.
func openFiles() int64 {
	for _, d := range []string{"/proc/self/fd", "/dev/fd"} {
		if ents, err := os.ReadDir(d); err == nil {
			return int64(len(ents)) - 1 // the directory being read is one of them
		}
	}
	return 0
}

// ---- the verdict ---------------------------------------------------------------

func judgeSoak(res *SoakResult, rec *recorder, so SoakOptions, budgets map[string]budget) {
	warm := so.Warmup.Seconds()
	end := res.Seconds
	third := (end - warm) / 3
	var first, last []SoakSample
	for _, s := range res.Samples {
		switch {
		case s.At < warm:
		case s.At < warm+third:
			first = append(first, s)
		case s.At >= end-third:
			last = append(last, s)
		}
	}
	gauges := []struct {
		name, unit string
		get        func(SoakSample) float64
	}{
		{"rss", "bytes", func(s SoakSample) float64 { return float64(s.RSS) }},
		{"footprint", "bytes", func(s SoakSample) float64 { return float64(s.Footprint) }},
		{"heap_live", "bytes", func(s SoakSample) float64 { return float64(s.HeapLive) }},
		{"go_mapped", "bytes", func(s SoakSample) float64 { return float64(s.GoMapped) }},
		{"goroutines", "count", func(s SoakSample) float64 { return float64(s.Goroutines) }},
		{"open_files", "count", func(s SoakSample) float64 { return float64(s.OpenFiles) }},
		{"wal", "bytes", func(s SoakSample) float64 { return float64(s.WAL) }},
		{"db", "bytes", func(s SoakSample) float64 { return float64(s.DB) }},
	}
	if len(first) < 3 || len(last) < 3 {
		res.Problems = append(res.Problems, fmt.Sprintf("too few samples to judge growth (%d and %d after the warm-up); run longer or sample more often", len(first), len(last)))
	}
	for _, g := range gauges {
		r := gaugeRules[g.name]
		f, l := medianOf(first, g.get), medianOf(last, g.get)
		var mx float64
		for _, s := range res.Samples {
			mx = max(mx, g.get(s))
		}
		if g.name == "footprint" {
			// Beside RSS, not a second rule: in the 2026-11-14 runs the two climbed
			// together, so clean file pages were not what RSS was counting.
			res.Gauges = append(res.Gauges, SoakGauge{Name: g.name, Unit: g.unit, First: f, Last: l, Max: mx,
				Rule: "reported, not judged", OK: true})
			continue
		}
		allowed := f + max(r.frac*f, r.abs)
		gv := SoakGauge{Name: g.name, Unit: g.unit, First: f, Last: l, Max: mx, Allowed: allowed,
			Rule: fmt.Sprintf("last third median <= first third median + max(%.0f%%, %s)", r.frac*100, human(r.abs, g.unit)), OK: l <= allowed}
		if f == 0 && l == 0 && g.name == "rss" {
			gv.Rule = "not measured on this system"
		}
		if !gv.OK {
			res.Problems = append(res.Problems, fmt.Sprintf("%s grew from %s to %s (allowed %s)", g.name, human(f, g.unit), human(l, g.unit), human(allowed, g.unit)))
		}
		res.Gauges = append(res.Gauges, gv)
	}

	names := make([]string, 0, len(rec.ops))
	for n := range rec.ops {
		names = append(names, n)
	}
	for n := range rec.errs {
		if _, ok := rec.ops[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		ts := rec.ops[n]
		all := make([]float64, 0, len(ts))
		var tail []float64
		for _, t := range ts {
			all = append(all, t.ms)
			if t.at >= end-third {
				tail = append(tail, t.ms)
			}
		}
		sort.Float64s(all)
		sort.Float64s(tail)
		op := SoakOp{Name: n, Count: len(all), Errors: rec.errs[n], OK: true}
		if len(all) > 0 {
			op.P50, op.P95, op.Max = round(quantile(all, 0.5)), round(quantile(all, 0.95)), round(all[len(all)-1])
		}
		if len(tail) > 0 {
			op.LastP95 = round(quantile(tail, 0.95))
		}
		if b, ok := budgets[n]; ok {
			op.Budget, op.Note = b.limit, b.note
			if op.P95 > b.limit || op.LastP95 > b.limit {
				op.OK = false
				res.Problems = append(res.Problems, fmt.Sprintf("%s p95 %.1f ms (last third %.1f ms) is over its budget of %.0f ms", n, op.P95, op.LastP95, b.limit))
			}
		}
		if op.Errors > 0 {
			op.OK = false
			res.Problems = append(res.Problems, fmt.Sprintf("%s failed %d time(s); the first: %s", n, op.Errors, rec.first[n]))
		}
		res.Ops = append(res.Ops, op)
	}
}

func medianOf(s []SoakSample, get func(SoakSample) float64) float64 {
	if len(s) == 0 {
		return 0
	}
	v := make([]float64, len(s))
	for i := range s {
		v[i] = get(s[i])
	}
	sort.Float64s(v)
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}

func human(v float64, unit string) string {
	if unit != "bytes" {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return fmt.Sprintf("%.1f MB", v/(1<<20))
}

// WriteSoakTable prints a soak's result for a person.
func WriteSoakTable(w io.Writer, r *SoakResult) {
	fmt.Fprintf(w, "soak %s: %d words, %d documents, %.0f s (warm-up %.0f s), %d samples, %d revisions written, %d backups; %s, %s\n",
		r.Size, r.Words, r.Documents, r.Seconds, r.Warmup, len(r.Samples), r.Revisions, r.Backups, r.Machine, r.Go)
	if r.CompileBytes > 0 {
		fmt.Fprintf(w, "  the last DOCX compile: %d bytes\n", r.CompileBytes)
	}
	fmt.Fprintf(w, "  %-12s %14s %14s %14s %14s  %s\n", "gauge", "first third", "last third", "allowed", "max", "")
	for _, g := range r.Gauges {
		v := "ok"
		if !g.OK {
			v = "GROWING"
		}
		allowed := human(g.Allowed, g.Unit)
		if g.Allowed == 0 {
			allowed, v = "-", "reported"
		}
		fmt.Fprintf(w, "  %-12s %14s %14s %14s %14s  %s\n", g.Name, human(g.First, g.Unit), human(g.Last, g.Unit), allowed, human(g.Max, g.Unit), v)
	}
	fmt.Fprintf(w, "  %-18s %7s %6s %10s %10s %10s %12s %10s  %s\n", "operation", "count", "errors", "p50 ms", "p95 ms", "max ms", "last p95 ms", "budget", "")
	for _, o := range r.Ops {
		b, v := "", "ok"
		if o.Budget > 0 {
			b = strconv.FormatFloat(o.Budget, 'f', -1, 64)
		}
		if !o.OK {
			v = "FAIL"
		}
		fmt.Fprintf(w, "  %-18s %7d %6d %10.1f %10.1f %10.1f %12.1f %10s  %s\n", o.Name, o.Count, o.Errors, o.P50, o.P95, o.Max, o.LastP95, b, v)
	}
	if r.OK() {
		fmt.Fprintln(w, "  no unbounded growth, every budget held")
	}
	for _, p := range r.Problems {
		fmt.Fprintln(w, "  PROBLEM:", p)
	}
}

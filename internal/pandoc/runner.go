package pandoc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits bound one conversion. Zero values take the defaults.
type Limits struct {
	Timeout       time.Duration // wall clock for one run (default 60s)
	MaxInput      int64         // bytes of the file given to Pandoc (default 32 MiB)
	MaxOutput     int64         // bytes it may write (default 64 MiB)
	MaxMediaFiles int           // files of extracted media (default 200)
	MaxMediaFile  int64         // one of them (default 16 MiB)
	MaxMediaTotal int64         // all of them (default 64 MiB)
	CPUSeconds    uint64        // RLIMIT_CPU (default 120)
	OpenFiles     uint64        // RLIMIT_NOFILE (default 128)
	MemoryMB      int           // Pandoc's own heap limit (+RTS -M); 0 passes none
	Concurrent    int           // conversions at once (default 2, at most 2)
	MaxCallBytes  int64         // what a conversion may have in its folder while it runs; only ever lowered (default and ceiling 256 MiB)
	MinFreeBytes  int64         // free space the call folder's volume must have to start a conversion (default 512 MiB)
	Wait          time.Duration // how long a request waits for a place (default 3s)
}

func (l Limits) fill() Limits {
	if l.Timeout <= 0 {
		l.Timeout = 60 * time.Second
	}
	if l.MaxInput <= 0 {
		l.MaxInput = 32 << 20
	}
	if l.MaxOutput <= 0 {
		l.MaxOutput = 64 << 20
	}
	if l.MaxMediaFiles <= 0 {
		l.MaxMediaFiles = 200
	}
	if l.MaxMediaFile <= 0 {
		l.MaxMediaFile = 16 << 20
	}
	if l.MaxMediaTotal <= 0 {
		l.MaxMediaTotal = 64 << 20
	}
	if l.CPUSeconds == 0 {
		l.CPUSeconds = 120
	}
	if l.OpenFiles == 0 {
		l.OpenFiles = 128
	}
	const ceiling = 256 << 20
	if l.MaxCallBytes <= 0 || l.MaxCallBytes > ceiling {
		l.MaxCallBytes = ceiling
		if sum := 2 * (l.MaxInput + l.MaxOutput + l.MaxMediaTotal); sum < ceiling {
			l.MaxCallBytes = sum // the caps added up, twice: more than that is not a conversion's output
		}
	}
	if l.MinFreeBytes == 0 {
		l.MinFreeBytes = 512 << 20
	}
	if l.Concurrent <= 0 || l.Concurrent > 2 {
		l.Concurrent = 2
	}
	if l.Wait <= 0 {
		l.Wait = 3 * time.Second
	}
	return l
}

// Runner runs Pandoc. It is safe for concurrent use.
type Runner struct {
	mu       sync.Mutex
	lim      Limits
	tempRoot string
	helper   string
	sem      chan struct{}
	vsem     chan struct{}  // the one --version run at a time, so that checking the program never takes a conversion's place
	refuse   []string       // folders no Pandoc binary may live under (the data directory)
	who      map[string]int // conversions a person has running or waiting: one at a time each
	log      *slog.Logger
	freeWarn sync.Once // the one warning that the free-space check could not be made
	killWait time.Duration
}

// NewRunner makes a Runner. tempRoot is where the per-call directories go ("" for
// the system's). helper is an executable that calls RunHelperIfRequested first
// thing (the server's own binary), to put resource limits on Pandoc; empty runs
// Pandoc without them (the gap is stated in the documentation).
func NewRunner(lim Limits, tempRoot, helper string) *Runner {
	lim = lim.fill()
	return &Runner{lim: lim, tempRoot: tempRoot, helper: helper, sem: make(chan struct{}, lim.Concurrent), vsem: make(chan struct{}, 1), who: map[string]int{}, killWait: 2 * time.Second, log: slog.Default()}
}

// RefuseUnder makes the runner refuse a Pandoc binary that lives under any of these folders (the data directory:
// what a person can put there is not a program an administrator should run).
func (r *Runner) RefuseUnder(dirs ...string) {
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if x, err := filepath.EvalSymlinks(d); err == nil {
			d = x
		}
		r.refuse = append(r.refuse, filepath.Clean(d))
	}
}

func (r *Runner) refused(resolved string) bool {
	for _, d := range r.refuse {
		if resolved == d || strings.HasPrefix(resolved, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Limits reports the limits in effect.
func (r *Runner) Limits() Limits { r.mu.Lock(); defer r.mu.Unlock(); return r.lim }

// HasResourceLimits says whether Pandoc runs under resource limits.
func (r *Runner) HasResourceLimits() bool { return r.helper != "" && supported }

// ---- the call directory ---------------------------------------------------------------------

// call is one conversion's private directory: the input, the empty data
// directory, the media and the output are all inside it, and it is removed.
type call struct {
	dir    string
	data   string
	before map[string]bool // what the call folder's parent held before: see verifyOutside
}

// callPrefix names the call folders. Every other name under the temp root is something a conversion made.
const callPrefix = "scriobh-pandoc-"

// names lists the entries of a folder.
func names(dir string) map[string]bool {
	out := map[string]bool{}
	es, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range es {
		out[e.Name()] = true
	}
	return out
}

func (r *Runner) newCall() (*call, error) {
	if r.tempRoot != "" {
		free, ok := freeBytes(r.tempRoot)
		switch {
		case !ok:
			// The volume could not be asked (the call itself failed). The check is not applied then, as it never was; a
			// folder that is gone or unreadable fails at MkdirTemp below. Say so once, in the local log, so a server
			// that never checks is not silent about it. The folder is the server's own, so naming it leaks nothing.
			r.freeWarn.Do(func() {
				r.log.Warn("pandoc: the free space of the temporary folder could not be read, so the low-space check is not applied", "temp_root", r.tempRoot)
			})
		case free < uint64(r.lim.MinFreeBytes):
			return nil, fail(KindFailed, "There is too little free space on the server to run Pandoc.")
		}
	}
	var before map[string]bool
	if r.tempRoot != "" { // a private folder: a system temp folder is shared with everyone and is not judged
		before = names(r.tempRoot)
	}
	dir, err := os.MkdirTemp(r.tempRoot, callPrefix)
	if err != nil {
		return nil, fail(KindFailed, "The server could not make a working folder for Pandoc.")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fail(KindFailed, "The server could not make a working folder for Pandoc.")
	}
	c := &call{dir: dir, data: filepath.Join(dir, "data"), before: before}
	if err := os.Mkdir(c.data, 0o700); err != nil {
		os.RemoveAll(dir)
		return nil, fail(KindFailed, "The server could not make a working folder for Pandoc.")
	}
	return c, nil
}

func (c *call) remove() { _ = os.RemoveAll(c.dir) }

// verifyOutside checks that the conversion made nothing beside its own folder in the folder the call folders live in:
// the one write Pandoc has made outside a sandbox before (a path in an image, through --extract-media) is a file
// somewhere else. Anything new there that is not another call's folder is deleted and the conversion fails with a
// class in the log. It looks at that folder only (it is the server's own, so deleting is safe); a write further away
// is not seen, which is why the version floor and not this check is the control (see docs/p7-notes/pandoc.md).
func (r *Runner) verifyOutside(c *call) error {
	if c.before == nil || r.tempRoot == "" {
		return nil
	}
	bad := 0
	for n := range names(r.tempRoot) {
		if c.before[n] || strings.HasPrefix(n, callPrefix) {
			continue
		}
		bad++
		_ = os.RemoveAll(filepath.Join(r.tempRoot, n))
	}
	if bad > 0 {
		r.log.Error("pandoc: a conversion made files outside its folder; they were removed and the conversion refused", "class", "escaped_write", "files", bad)
		return fail(KindFailed, "Pandoc wrote outside its own folder, so the file was refused.")
	}
	return nil
}

// environment is the whole of what Pandoc is given. Nothing of the server's
// environment: no HOME (it would look for a user data directory there), no
// proxy, nothing starting PANDOC_. SOURCE_DATE_EPOCH is Pandoc's documented
// way to make the dates in a document it writes fixed.
func (c *call) environment() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + c.dir, "TMPDIR=" + c.dir, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "SOURCE_DATE_EPOCH=315532800"}
}

// ---- argument lists ---------------------------------------------------------------------------

// common is the start of every argument list: the sandbox, then an empty data directory.
func (r *Runner) common(c *call) []string {
	return []string{"--sandbox", "--data-dir=" + c.data}
}

func (r *Runner) rts() []string {
	if n := r.Limits().MemoryMB; n > 0 {
		return []string{"+RTS", "-M" + strconv.Itoa(n) + "m", "-RTS"}
	}
	return nil
}

// importArgs reads input as the named format and writes GitHub-flavoured
// Markdown to standard output. Media is extracted into ./media only when the
// import asked for pictures: --extract-media is the one write Pandoc makes
// besides standard output, and the one that has had arbitrary-file-write bugs.
func (r *Runner) importArgs(c *call, reader string, extractMedia bool) []string {
	a := r.common(c)
	a = append(a, "-f", reader, "-t", "gfm", "--wrap=none")
	if extractMedia {
		a = append(a, "--extract-media=media")
	}
	a = append(a, "input")
	return append(a, r.rts()...)
}

// exportArgs reads the compile Markdown and writes the named format to a file.
func (r *Runner) exportArgs(c *call, writer, out string, standalone bool) []string {
	a := r.common(c)
	// The compile Markdown is read with raw HTML and a YAML metadata block turned off: neither can be interpreted as
	// metadata or passed through as raw output, whatever the project's text holds.
	a = append(a, "-f", "gfm-raw_html-yaml_metadata_block", "-t", writer, "--wrap=none")
	if standalone {
		a = append(a, "--standalone")
	}
	a = append(a, "-o", out, "input.md")
	return append(a, r.rts()...)
}

// ---- running ----------------------------------------------------------------------------------

type whoKey struct{}

// WithWho names the person a conversion is for. A person has one conversion running or waiting at a time: two slow
// ones from one account cannot take every place and starve everyone else.
func WithWho(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, whoKey{}, id)
}

// acquire takes one of the (at most two) places.
func (r *Runner) acquire(ctx context.Context) (func(), error) {
	who, _ := ctx.Value(whoKey{}).(string)
	if who != "" {
		r.mu.Lock()
		if r.who[who] > 0 {
			r.mu.Unlock()
			return nil, fail(KindBusy, "You already have a conversion running. Wait for it to finish.")
		}
		r.who[who]++
		r.mu.Unlock()
	}
	free := func() {
		if who != "" {
			r.mu.Lock()
			if r.who[who] <= 1 {
				delete(r.who, who)
			} else {
				r.who[who]--
			}
			r.mu.Unlock()
		}
	}
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem; free() }, nil
	default:
	}
	t := time.NewTimer(r.lim.Wait)
	defer t.Stop()
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem; free() }, nil
	case <-t.C:
		free()
		return nil, fail(KindBusy, "The server is busy converting other files. Try again in a moment.")
	case <-ctx.Done():
		free()
		return nil, fail(KindCancelled, "The request was cancelled.")
	}
}

// capWriter takes what is written up to a limit; past it, it stops the process.
type capWriter struct {
	mu      sync.Mutex
	buf     []byte
	n       int64 // bytes seen, kept or not
	max     int64
	keep    bool // false: read and discard
	discard bool // past max, drop the rest quietly: it is not a reason to stop the process (standard error)
	over    bool
	onOver  func()
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.over {
		return len(p), nil
	}
	if w.discard && w.n+int64(len(p)) > w.max {
		return len(p), nil
	}
	if w.n+int64(len(p)) > w.max {
		w.over = true
		if w.onOver != nil {
			go w.onOver()
		}
		return len(p), nil
	}
	w.n += int64(len(p))
	if w.keep {
		w.buf = append(w.buf, p...)
	}
	return len(p), nil
}

func (w *capWriter) overflowed() bool { w.mu.Lock(); defer w.mu.Unlock(); return w.over }

// exec runs the binary with the arguments in the call's directory and returns
// its standard output (when keepOut). Standard error is read and discarded.
func (r *Runner) exec(ctx context.Context, b Binary, c *call, args []string, keepOut bool) ([]byte, error) {
	if e := b.recheck(); e != nil {
		return nil, e
	}
	cmd, afterStart, err := r.command(b, c, args)
	if err != nil {
		return nil, err
	}
	var killOnce sync.Once
	kill := func() { killOnce.Do(func() { killGroup(cmd) }) }
	out := &capWriter{max: r.lim.MaxOutput, keep: keepOut, onOver: kill}
	errw := &capWriter{max: 1 << 20, keep: false, discard: true} // a warning for every missing resource is not a failure
	cmd.Stdout, cmd.Stderr = out, errw
	cmd.Stdin = nil
	cmd.WaitDelay = r.killWait
	if err := cmd.Start(); err != nil {
		afterStart()
		return nil, fail(KindFailed, "Pandoc could not be started.")
	}
	afterStart()
	// While it runs, what it has written into its folder is counted; over the cap the whole group is killed.
	var tooBig atomic.Bool
	pollDone := make(chan struct{})
	defer close(pollDone)
	go func() {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-pollDone:
				return
			case <-t.C:
				if dirBytes(c.dir, r.lim.MaxCallBytes) > r.lim.MaxCallBytes {
					tooBig.Store(true)
					kill()
					return
				}
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(r.lim.Timeout)
	defer timer.Stop()
	var werr error
	var timedOut, cancelled bool
	select {
	case werr = <-done:
	case <-timer.C:
		timedOut = true
		kill()
		werr = <-done
	case <-ctx.Done():
		cancelled = true
		kill()
		werr = <-done
	}
	kill() // whatever it left running in its group
	switch {
	case timedOut:
		return nil, fail(KindTimeout, "Pandoc took too long and was stopped.")
	case cancelled:
		return nil, fail(KindCancelled, "The request was cancelled.")
	case tooBig.Load():
		return nil, fail(KindTooLarge, "Pandoc wrote more to its folder than the limit allows and was stopped.")
	case out.overflowed():
		return nil, fail(KindTooLarge, "Pandoc wrote more than the limit and was stopped.")
	case werr != nil:
		var ee interface{ ExitCode() int }
		if errors.As(werr, &ee) || errors.Is(werr, io.ErrClosedPipe) {
			return nil, fail(KindFailed, "Pandoc could not convert that file.")
		}
		return nil, fail(KindFailed, "Pandoc could not convert that file.")
	}
	return out.buf, nil
}

// version runs --version, once, under the same environment and limits.
func (r *Runner) version(ctx context.Context, b Binary) (string, error) {
	c, err := r.newCall()
	if err != nil {
		return "", err
	}
	defer c.remove()
	select { // one at a time, and never a conversion's place
	case r.vsem <- struct{}{}:
		defer func() { <-r.vsem }()
	case <-time.After(r.lim.Wait):
		return "", fail(KindFailed, "Pandoc is being checked already; try again in a moment.")
	case <-ctx.Done():
		return "", fail(KindCancelled, "The request was cancelled.")
	}
	vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := r.exec(vctx, b, c, []string{"--version"}, true)
	if err != nil {
		if e, ok := AsError(err); ok && e.Kind == KindFailed {
			return "", fail(KindVersion, "That program did not run as Pandoc does (--version failed).")
		}
		return "", err
	}
	return trimOutput(out), nil
}

// Imported is a document converted to Markdown.
type Imported struct {
	Markdown     []byte
	Media        []Media
	MediaSkipped int // links, special files and what was over the limits
	Version      string
	Format       string
}

// Media is an extracted file. Path is slash-separated, relative to the call's
// folder (as Pandoc wrote it into the Markdown). Its bytes are untrusted.
type Media struct {
	Path string
	Data []byte
}

// ImportOptions are what an import asks of Pandoc beyond the format.
type ImportOptions struct {
	// ExtractMedia has Pandoc extract the pictures of the document (--extract-media). Off by default: it is the
	// one write Pandoc makes besides standard output, so it happens only when the person wants pictures.
	ExtractMedia bool
}

// Import converts data, in an allow-listed format, to GitHub-flavoured Markdown, extracting no media.
func (r *Runner) Import(ctx context.Context, b Binary, format string, data []byte) (*Imported, error) {
	return r.ImportWith(ctx, b, format, data, ImportOptions{})
}

// ImportWith is Import with options.
func (r *Runner) ImportWith(ctx context.Context, b Binary, format string, data []byte, o ImportOptions) (*Imported, error) {
	reader, ok := importFormats[format]
	if !ok {
		return nil, fail(KindBadFormat, "That is not a format Pandoc may be asked to read here.")
	}
	if int64(len(data)) > r.lim.MaxInput {
		return nil, fail(KindTooLarge, "That file is larger than the %d MB Pandoc is given.", r.lim.MaxInput>>20)
	}
	if len(data) == 0 {
		return nil, fail(KindFailed, "That file is empty.")
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	c, err := r.newCall()
	if err != nil {
		return nil, err
	}
	defer c.remove()
	if err := os.WriteFile(filepath.Join(c.dir, "input"), data, 0o600); err != nil {
		return nil, fail(KindFailed, "The server could not write the file for Pandoc.")
	}
	out, err := r.exec(ctx, b, c, r.importArgs(c, reader, o.ExtractMedia), true)
	if verr := r.verifyOutside(c); verr != nil {
		return nil, verr
	}
	if err != nil {
		return nil, err
	}
	var media []Media
	skipped := 0
	if o.ExtractMedia {
		media, skipped = r.collectMedia(c.dir)
	}
	return &Imported{Markdown: out, Media: media, MediaSkipped: skipped, Version: b.Version, Format: format}, nil
}

// Exported is a document converted from Markdown.
type Exported struct {
	Body        []byte
	Version     string
	Format      string
	Ext         string
	ContentType string
}

// Export converts Markdown to an allow-listed format.
func (r *Runner) Export(ctx context.Context, b Binary, format string, markdown []byte) (*Exported, error) {
	w, ok := exportFormats[format]
	if !ok {
		return nil, fail(KindBadFormat, "That is not a format Pandoc may be asked to write here.")
	}
	if int64(len(markdown)) > r.lim.MaxInput {
		return nil, fail(KindTooLarge, "The text is larger than the %d MB Pandoc is given.", r.lim.MaxInput>>20)
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	c, err := r.newCall()
	if err != nil {
		return nil, err
	}
	defer c.remove()
	if err := os.WriteFile(filepath.Join(c.dir, "input.md"), markdown, 0o600); err != nil {
		return nil, fail(KindFailed, "The server could not write the file for Pandoc.")
	}
	outName := "output." + w.ext
	standalone := format == "odt" || format == "rtf" || format == "latex"
	_, xerr := r.exec(ctx, b, c, r.exportArgs(c, w.writer, outName, standalone), false)
	if verr := r.verifyOutside(c); verr != nil {
		return nil, verr
	}
	if xerr != nil {
		return nil, xerr
	}
	body, err := readRegular(filepath.Join(c.dir, outName), r.lim.MaxOutput)
	if err != nil {
		var te *Error
		if errors.As(err, &te) {
			return nil, te
		}
		return nil, fail(KindFailed, "Pandoc did not write the file.")
	}
	return &Exported{Body: body, Version: b.Version, Format: format, Ext: w.ext, ContentType: w.ctype}, nil
}

// readRegular reads a file Pandoc wrote: it must be a regular file (not a link
// to somewhere else) and no larger than max.
func readRegular(path string, max int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return nil, fail(KindFailed, "Pandoc did not write the file.")
	}
	if fi.Size() > max {
		return nil, fail(KindTooLarge, "Pandoc wrote more than the limit.")
	}
	f, err := openNoFollow(path)
	if err != nil {
		return nil, fail(KindFailed, "Pandoc did not write the file.")
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return nil, fail(KindFailed, "Pandoc did not write the file.")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, fail(KindFailed, "Pandoc did not write the file.")
	}
	if int64(len(b)) > max {
		return nil, fail(KindTooLarge, "Pandoc wrote more than the limit.")
	}
	return b, nil
}

// collectMedia reads what Pandoc extracted into <dir>/media: regular files only,
// never a link, a directory entry as a file or a special file; at most
// MaxMediaFiles files, each at most MaxMediaFile bytes, all at most MaxMediaTotal;
// at most eight folders deep. What is not taken is counted.
func (r *Runner) collectMedia(dir string) (media []Media, skipped int) {
	root := filepath.Join(dir, "media")
	var total int64
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			return fs.SkipDir
		}
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator)) > 8 {
				skipped++
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // a symbolic link, a device, a pipe, a socket
			skipped++
			return nil
		}
		if len(media) >= r.lim.MaxMediaFiles {
			skipped++
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > r.lim.MaxMediaFile || total+fi.Size() > r.lim.MaxMediaTotal {
			skipped++
			return nil
		}
		b, err := readRegular(path, r.lim.MaxMediaFile)
		if err != nil {
			skipped++
			return nil
		}
		total += int64(len(b))
		media = append(media, Media{Path: filepath.ToSlash(rel), Data: b})
		return nil
	})
	return media, skipped
}

func (r *Runner) helperArgs(pandoc string, args []string) ([]string, error) {
	lim, err := json.Marshal(struct {
		CPU    uint64 `json:"cpu"`
		Files  uint64 `json:"files"`
		FSize  uint64 `json:"fsize"`
		NoCore bool   `json:"nocore"`
	}{r.lim.CPUSeconds, r.lim.OpenFiles, uint64(r.lim.MaxOutput + r.lim.MaxMediaTotal + r.lim.MaxInput), true})
	if err != nil {
		return nil, err
	}
	return append([]string{helperName, helperFlag, string(lim), "--", pandoc}, args...), nil
}

var _ = fmt.Sprint

// SweepTemp removes working folders a stopped process left behind (older than
// an hour). The server calls it once at start.
func SweepTemp(root string) {
	if root == "" {
		return
	}
	es, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range es {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "scriobh-pandoc-") {
			continue
		}
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > time.Hour {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// dirBytes counts the bytes of the regular files under dir, stopping once the count passes limit. Links are not followed.
func dirBytes(dir string, limit int64) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
			if n > limit {
				return fs.SkipAll
			}
		}
		return nil
	})
	return n
}

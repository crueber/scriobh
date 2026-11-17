package imports

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pdfread"
)

// The PDF is read by a child process.
//
// A PDF is a program-shaped file from a stranger, and the reader is a parser
// with a great many loops: whatever its limits, a limit that is missed is a
// crash, a loop that does not end or a gigabyte, in the process that serves
// everybody. So everything from the bytes of the file to the chapters and the
// fidelity report is done by the same binary run again with one fixed argument
// (`pdf-extract`), on a pipe, under a supervisor in this process:
//
//   - the file goes in on its stdin (at most MaxPDF bytes) and one JSON document
//     comes out on its stdout (at most 64 MiB); its stderr is discarded; no part
//     of the file or of the request is in argv or in the environment;
//   - it runs with an environment of three variables (the marker, a memory limit
//     and a wall limit; no secrets, no paths), in a process group of its own;
//   - the supervisor kills the whole group with SIGKILL when the wall clock limit
//     passes (default 20 s, set down only with SCRIOBH_PDF_TIMEOUT_SECONDS), when
//     its resident memory passes the ceiling (default 512 MiB, set down only with
//     SCRIOBH_PDF_MEMORY_MB; polled every 75 ms), when it writes more than it may,
//     when the request is cancelled or the client goes, and when the server shuts
//     down (StopPDFChildren);
//   - the child sets GOMEMLIMIT and GOGC, RLIMIT_CPU, and where the platform
//     honours it RLIMIT_DATA (Linux); it asks to be killed with its parent
//     (Pdeathsig, Linux). macOS and the BSDs honour neither RLIMIT_AS nor
//     RLIMIT_DATA for Go, so there only the supervisor's polling and GOMEMLIMIT
//     hold the line; where the process table cannot be read (non-Unix systems)
//     the memory ceiling is GOMEMLIMIT alone and the process is killed singly;
//   - a crash of the child (a signal, a fatal error such as a stack overflow, the
//     kernel's out-of-memory kill) is an answer to the person ("the file could not
//     be read"), never the end of the server.
//
// The child is not a new program: the server's main calls MaybeRunPDFChild first
// thing, and so does a test binary's TestMain. The read counts as one of the
// importer's slots (it happens inside the one that the import holds), and one
// person has at most one PDF being read at a time.

const (
	pdfChildArg     = "pdf-extract"
	pdfChildEnv     = "PDFCHILD"
	pdfWallEnv      = "PDFCHILD_WALL_MS"
	defaultPDFWall  = 20 * time.Second
	defaultPDFMemMB = 512
	maxChildOutput  = 64 << 20
	rssPoll         = 75 * time.Millisecond
)

var (
	childCapable atomic.Bool
	childMu      sync.Mutex
	childTestEnv []string
	childTestMem int           // MB, tests only
	childTestWal time.Duration // tests only
	liveChildren = map[int]*os.Process{}
	stopping     atomic.Bool
	perUser      sync.Map // user id -> struct{}: a PDF being read for that person
)

// MaybeRunPDFChild is called first thing by the server's main and by a test
// binary's TestMain. When the process was started as the PDF child it reads the
// file on stdin, writes the result on stdout and exits; otherwise it only notes
// that this binary can be the child.
func MaybeRunPDFChild() {
	childCapable.Store(true)
	if len(os.Args) == 2 && os.Args[1] == pdfChildArg && os.Getenv(pdfChildEnv) == "1" {
		os.Exit(runPDFChild(os.Stdin, os.Stdout))
	}
}

// SetPDFChildForTest sets the child's environment (a test's way to make it
// misbehave) and its limits; zero values restore the defaults. Tests only.
func SetPDFChildForTest(env []string, wall time.Duration, memMB int) {
	childMu.Lock()
	childTestEnv, childTestWal, childTestMem = env, wall, memMB
	childMu.Unlock()
}

// StopPDFChildren kills every PDF child that is running and refuses new ones:
// the server calls it when it shuts down.
func StopPDFChildren() {
	stopping.Store(true)
	childMu.Lock()
	defer childMu.Unlock()
	for _, p := range liveChildren {
		killGroup(p)
	}
}

// ResumePDFChildren undoes StopPDFChildren (tests).
func ResumePDFChildren() { stopping.Store(false) }

func pdfWall() time.Duration {
	childMu.Lock()
	t := childTestWal
	childMu.Unlock()
	if t > 0 {
		return t
	}
	w := defaultPDFWall
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SCRIOBH_PDF_TIMEOUT_SECONDS"))); err == nil && v >= 1 && time.Duration(v)*time.Second < w {
		w = time.Duration(v) * time.Second
	}
	return w
}

func pdfMemMB() int {
	childMu.Lock()
	m := childTestMem
	childMu.Unlock()
	if m > 0 {
		return m
	}
	mb := defaultPDFMemMB
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("SCRIOBH_PDF_MEMORY_MB"))); err == nil && v >= 32 && v < mb {
		mb = v
	}
	return mb
}

// pdfLimits are the reader's limits as the importer sets them.
func pdfLimits() pdfread.Limits {
	return pdfread.Limits{FileBytes: MaxPDF, Time: pdfWall()}
}

// childOut is what the child writes: the refusal in words, or the result.
type childOut struct {
	Err    string          `json:"err,omitempty"`
	Result *pdfread.Result `json:"result,omitempty"`
	Info   pdfread.Info    `json:"info"`
}

// ErrPDFTooComplex is the refusal for a PDF from which no text came because its pages went over the reader's limits (and
// not because it is a scan): the file is too complex to read here.
var ErrPDFTooComplex = errors.New("this PDF is too complex to read within the import's limits (some of its parts are larger than they allow), so no text was recovered from it; it may also be pictures of pages. Scríobh does no character recognition. Try a simpler copy of the file, or import the manuscript from its source")

// extractPDF is the whole of the reading, in this process: from the file's bytes
// to the chapters and the report. The child runs it; nothing else does.
func extractPDF(ctx context.Context, data []byte) (out childOut) {
	defer func() {
		if r := recover(); r != nil {
			out = childOut{Err: "the PDF could not be read"}
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, pdfLimits().Time)
	defer cancel()
	doc, err := pdfread.Open(ctx, data, pdfLimits())
	if err != nil {
		return childOut{Err: pdfErr(err).Error()}
	}
	var pages []*pdfread.Page
	if err := doc.ReadPages(ctx, func(p *pdfread.Page) error { pages = append(pages, p); return nil }); err != nil {
		return childOut{Err: pdfErr(err).Error()}
	}
	res, err := pdfread.Analyse(ctx, pages, doc.Outline(), doc.Info)
	if errors.Is(err, pdfread.ErrNoTextLayer) {
		// Nothing was read: a scan, or a file whose pages could not be read within the limits. Say which when the reader knows.
		cut := doc.Info.PageTreeProblem != ""
		for _, p := range pages {
			cut = cut || p.Truncated
		}
		if cut {
			return childOut{Err: ErrPDFTooComplex.Error()}
		}
	}
	if err != nil {
		return childOut{Err: pdfErr(err).Error()}
	}
	return childOut{Result: res, Info: doc.Info}
}

// runPDFChild is the child's main.
func runPDFChild(in io.Reader, out io.Writer) int {
	limitSelf()
	mem := int64(defaultPDFMemMB) << 20
	if v, err := strconv.ParseInt(os.Getenv("GOMEMLIMIT"), 10, 64); err == nil && v > 0 {
		mem = v
	}
	debug.SetMemoryLimit(mem)
	data, err := io.ReadAll(io.LimitReader(in, MaxPDF+1))
	if err != nil {
		return 2
	}
	var co childOut
	if len(data) > MaxPDF {
		co = childOut{Err: pdfread.ErrTooLarge.Error()}
	} else {
		wall := defaultPDFWall
		if v, err := strconv.Atoi(os.Getenv(pdfWallEnv)); err == nil && v > 0 {
			wall = time.Duration(v) * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), wall)
		defer cancel()
		co = extractPDF(ctx, data)
	}
	if err := json.NewEncoder(out).Encode(co); err != nil {
		return 3
	}
	return 0
}

// runPDFChildProcess starts the child, feeds it the file and returns what it
// said, or the plain refusal for whatever went wrong.
func runPDFChildProcess(ctx context.Context, data []byte) (*childOut, error) {
	if stopping.Load() {
		return nil, errors.New("the server is shutting down: try the import again in a moment")
	}
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return nil, errors.New("a PDF cannot be read here: this build cannot start its reader")
	}
	if !childCapable.Load() {
		// A test binary that did not call MaybeRunPDFChild would run its tests again as the child.
		return nil, errors.New("a PDF cannot be read here: this program has no PDF reader of its own to start")
	}
	wall, memMB := pdfWall(), pdfMemMB()
	cmd := exec.Command(exe, pdfChildArg)
	cmd.Env = []string{
		pdfChildEnv + "=1",
		"GOMEMLIMIT=" + strconv.FormatInt(int64(memMB)<<20*3/4, 10),
		"GOGC=50",
		pdfWallEnv + "=" + strconv.FormatInt(int64(wall/time.Millisecond), 10),
	}
	childMu.Lock()
	cmd.Env = append(cmd.Env, childTestEnv...)
	childMu.Unlock()
	cmd.SysProcAttr = childSysProcAttr()
	cmd.Stderr = nil // /dev/null
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.New("the PDF could not be read")
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.New("the PDF could not be read")
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.New("the PDF could not be read: its reader could not be started")
	}
	pid := cmd.Process.Pid
	childMu.Lock()
	liveChildren[pid] = cmd.Process
	childMu.Unlock()
	if stopping.Load() { // StopPDFChildren ran between the check and the registration
		killGroup(cmd.Process)
	}

	var (
		wg     sync.WaitGroup
		why    atomic.Value // string: why the supervisor killed it
		output bytes.Buffer
		done   = make(chan struct{})
	)
	kill := func(reason string) {
		if why.CompareAndSwap(nil, reason) {
			killGroup(cmd.Process)
		}
	}
	wg.Add(2)
	go func() { // the file in
		defer wg.Done()
		stdin.Write(data)
		stdin.Close()
	}()
	outDone := make(chan struct{})
	go func() { // the answer out, bounded
		defer wg.Done()
		defer close(outDone)
		n, _ := io.Copy(&output, io.LimitReader(stdout, maxChildOutput+1))
		if n > maxChildOutput {
			kill("it wrote more than it may")
		}
		io.Copy(io.Discard, stdout) // let a child that was killed finish writing into nothing
	}()
	wg.Add(1)
	go func() { // the supervisor
		defer wg.Done()
		tick := time.NewTicker(rssPoll)
		defer tick.Stop()
		timer := time.NewTimer(wall + time.Second) // the child's own wall limit comes first; this is the hard one
		defer timer.Stop()
		ceiling := int64(memMB) << 20
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				kill("the request ended")
				return
			case <-timer.C:
				kill("it took longer than the import allows")
				return
			case <-tick.C:
				if rss, ok := rssOf(pid); ok && rss > ceiling {
					kill("it needed more memory than the import allows")
					return
				}
			}
		}
	}()
	started := time.Now()
	// Everything the child wrote is read before Wait, which closes the pipe: reading after it loses the end of the
	// answer under load. (The supervisor ends a child that never closes its output.)
	<-outDone
	waitErr := cmd.Wait() // returns once the process has exited and its pipes are closed
	lastChild.Store(&ChildStats{MaxRSS: maxRSSOf(cmd.ProcessState), Wall: time.Since(started)})
	close(done)
	wg.Wait()
	killGroup(cmd.Process) // anything it left in its group
	childMu.Lock()
	delete(liveChildren, pid)
	childMu.Unlock()

	if r, _ := why.Load().(string); r != "" {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("the PDF could not be read: " + r)
	}
	if waitErr != nil {
		return nil, errors.New("the PDF could not be read: its reader stopped unexpectedly (the file may be damaged on purpose or too complicated)")
	}
	var co childOut
	if err := json.NewDecoder(&output).Decode(&co); err != nil {
		return nil, errors.New("the PDF could not be read: its reader gave no answer")
	}
	if co.Err != "" {
		return nil, errors.New(co.Err)
	}
	if co.Result == nil {
		return nil, errors.New("the PDF could not be read")
	}
	return &co, nil
}

// ChildStats is what the last PDF child used: its peak resident memory as the system reports it when it ends (0
// where the system does not say), and how long it ran. It is for tests and measurements.
type ChildStats struct {
	MaxRSS int64
	Wall   time.Duration
}

var lastChild atomic.Pointer[ChildStats]

// LastPDFChildStats returns the stats of the PDF child that ended last, or nil.
func LastPDFChildStats() *ChildStats { return lastChild.Load() }

// readPDF reads the file in a child, for one person at a time.
func readPDF(ctx context.Context, data []byte) (*childOut, error) {
	if u := userOf(ctx); u != "" {
		if _, busy := perUser.LoadOrStore(u, struct{}{}); busy {
			return nil, &ops.Error{Code: ops.CodeRateLimited, Message: "You already have a PDF being read. Wait for it to finish, then try again.", RetryAfterSeconds: 10}
		}
		defer perUser.Delete(u)
	}
	return runPDFChildProcess(ctx, data)
}

func userOf(ctx context.Context) string {
	if c, ok := ctx.(*ops.Ctx); ok && c != nil {
		return c.P.UserID
	}
	return ""
}

// rssOf is the resident memory of a process, in bytes.
func rssOf(pid int) (int64, bool) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
		if err != nil {
			return 0, false
		}
		f := strings.Fields(string(b))
		if len(f) < 2 {
			return 0, false
		}
		pages, err := strconv.ParseInt(f[1], 10, 64)
		if err != nil {
			return 0, false
		}
		return pages * int64(os.Getpagesize()), true
	}
	return psRSS(pid)
}

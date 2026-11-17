package imports_test

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pdfread/pdftest"
	"github.com/crueber/scriobh/internal/testutil"
)

// leftovers lists processes of this test binary started as PDF children, and the
// grandchild the ignoreterm child leaves in its group.
// leftovers lists what this test process left running: its own reading children (by parent process id) and
// the marked sleep the "ignoreterm" child starts (the marker is this process's id, so another run of the package on
// the same machine, or a PDF child of another program, is not mistaken for ours). The list used to be every
// `*.test pdf-extract` and `sleep 3141` on the machine, so two concurrent runs saw each other's children.
func leftovers(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		t.Skip("ps is not available")
	}
	me := strconv.Itoa(os.Getpid())
	var left []string
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 4 {
			continue
		}
		ownChild := f[1] == me && strings.HasSuffix(f[2], ".test") && f[len(f)-1] == "pdf-extract"
		ownSleep := len(f) == 4 && f[2] == "sleep" && f[3] == strconv.Itoa(sleepMarkerBase+os.Getpid())
		if ownChild || ownSleep {
			left = append(left, strings.TrimSpace(l))
		}
	}
	return left
}

// A child that crashes, allocates past the ceiling, runs past the wall clock,
// writes without end or ignores SIGTERM is an answer to the person, never a
// server crash; the next benign import works; nothing is left running.
func TestAPdfChildThatMisbehavesIsRefusedAndNothingIsLeft(t *testing.T) {
	e := newEnv(t)
	benign := tinyPDF(t, "", pdftest.Opt{})
	t.Cleanup(func() { imports.SetPDFChildForTest(nil, 0, 0) })
	// warm up: the first read starts the machinery
	e.previewPdf(benign, imports.PreviewIn{})
	goroutines := runtime.NumGoroutine()
	cases := []struct {
		mode, want string
		wall       time.Duration
		mem        int
	}{
		{"crash", "stopped unexpectedly", 10 * time.Second, 256},
		{"alloc", "more memory", 10 * time.Second, 128},
		{"hang", "longer than the import allows", 2 * time.Second, 256},
		{"flood", "wrote more than it may", 10 * time.Second, 256},
		{"ignoreterm", "longer than the import allows", 2 * time.Second, 256},
	}
	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			imports.SetPDFChildForTest([]string{"PDF_CHILD_TEST=" + c.mode}, c.wall, c.mem)
			start := time.Now()
			_, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: benign, Name: "x.pdf"})
			took := time.Since(start)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%v (took %v), want %q", err, took, c.want)
			}
			if oe := ops.AsError(err); oe.Code != ops.CodeInvalid {
				t.Errorf("code %s", oe.Code)
			}
			t.Run("time", func(t *testing.T) {
				testutil.Timing(t)
				if took > c.wall+3*time.Second {
					t.Errorf("took %v with a limit of %v", took, c.wall)
				}
			})
			imports.SetPDFChildForTest(nil, 0, 0)
			// the next one works
			pv := e.previewPdf(benign, imports.PreviewIn{})
			if len(pv.Nodes) != 2 {
				t.Errorf("the next import: %d nodes", len(pv.Nodes))
			}
			if left := leftovers(t); len(left) > 0 {
				t.Errorf("processes left: %v", left)
			}
			time.Sleep(50 * time.Millisecond)
			if n := runtime.NumGoroutine(); n > goroutines+2 {
				t.Errorf("goroutines: %d before, %d after", goroutines, n)
			}
		})
	}
}

// The client going away kills the child; so does the server shutting down.
func TestAPdfChildIsKilledWithTheRequestAndWithTheServer(t *testing.T) {
	e := newEnv(t)
	benign := tinyPDF(t, "", pdftest.Opt{})
	t.Cleanup(func() { imports.SetPDFChildForTest(nil, 0, 0); imports.ResumePDFChildren() })
	imports.SetPDFChildForTest([]string{"PDF_CHILD_TEST=hang"}, 30*time.Second, 256)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	var err error
	go func() {
		defer wg.Done()
		_, err = e.reg.Call(ctx, e.w, "import.preview", imports.PreviewIn{Pdf: benign})
	}()
	time.Sleep(700 * time.Millisecond)
	if len(leftovers(t)) == 0 {
		t.Fatal("the child is not running")
	}
	cancel()
	wg.Wait()
	if err == nil {
		t.Error("a cancelled import answered")
	}
	if left := leftovers(t); len(left) > 0 {
		t.Errorf("after the client went: %v", left)
	}
	// shutdown
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, err = e.call(e.w, "import.preview", imports.PreviewIn{Pdf: benign})
	}()
	time.Sleep(700 * time.Millisecond)
	imports.StopPDFChildren()
	wg.Wait()
	if err == nil || len(leftovers(t)) > 0 {
		t.Errorf("after shutdown: %v %v", err, leftovers(t))
	}
	imports.ResumePDFChildren()
}

// One person has one PDF being read at a time; another person is not held up by it.
func TestOnePersonReadsOnePdfAtATime(t *testing.T) {
	e := newEnv(t)
	benign := tinyPDF(t, "", pdftest.Opt{})
	t.Cleanup(func() { imports.SetPDFChildForTest(nil, 0, 0) })
	imports.SetPDFChildForTest([]string{"PDF_CHILD_TEST=hang"}, 3*time.Second, 256)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		e.call(e.w, "import.preview", imports.PreviewIn{Pdf: benign})
	}()
	time.Sleep(700 * time.Millisecond)
	_, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: benign})
	if oe := ops.AsError(err); oe == nil || oe.Code != ops.CodeRateLimited {
		t.Errorf("a second PDF from the same person: %v", err)
	}
	other := e.user("other", ops.RoleWriter)
	imports.SetPDFChildForTest(nil, 0, 0)
	if _, err := e.call(other, "import.preview", imports.PreviewIn{Pdf: benign}); err != nil {
		t.Errorf("another person: %v", err)
	}
	wg.Wait()
	imports.SetPDFChildForTest(nil, 0, 0)
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Pdf: benign}); err != nil {
		t.Errorf("after it ended: %v", err)
	}
}

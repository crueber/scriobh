package imports_test

import (
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
)

// sleepMarkerBase plus a process id is the argument of the sleep the ignoreterm child leaves behind.
const sleepMarkerBase = 1_000_000

// This test binary is its own PDF child: the importer starts it again with the argument pdf-extract.
// PDF_CHILD_TEST (put there by SetPDFChildForTest) makes that child misbehave first, as hostile files do.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "pdf-extract" {
		switch os.Getenv("PDF_CHILD_TEST") {
		case "crash":
			debug.SetMaxStack(8 << 20) // so that it dies below the memory ceiling // a fatal stack overflow: no recover catches it
			var f func(n int) int
			f = func(n int) int { var pad [256]byte; _ = pad; return f(n+1) + 1 }
			f(0)
		case "alloc": // memory the supervisor must notice: 2 GB, touched
			var hold [][]byte
			for {
				b := make([]byte, 16<<20)
				for i := 0; i < len(b); i += 4096 {
					b[i] = 1
				}
				hold = append(hold, b)
				time.Sleep(5 * time.Millisecond)
			}
		case "hang":
			time.Sleep(time.Hour)
		case "flood": // endless output
			buf := make([]byte, 1<<20)
			for {
				os.Stdout.Write(buf)
			}
		case "ignoreterm": // ignores SIGTERM and leaves a grandchild in its group
			signal.Ignore(syscall.SIGTERM)
			exec.Command("sleep", strconv.Itoa(sleepMarkerBase+os.Getppid())).Start() // marked with the test process's id (see leftovers)
			time.Sleep(time.Hour)
		}
	}
	imports.MaybeRunPDFChild()
	os.Exit(m.Run())
}

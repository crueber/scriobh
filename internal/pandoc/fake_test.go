package pandoc_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/pandoc/pandoctest"
)

// No real Pandoc is used by these tests. The program below is a shell script
// that stands in for it: it answers --version, records the arguments, the
// environment and the input it was given (in files beside it, where the test
// reads them), and, when told to by the file "mode", misbehaves. That proves
// how the bridge calls a program and what it does about one that misbehaves;
// it proves nothing about what a real Pandoc does with a real document.

func TestMain(m *testing.M) {
	if pandoc.RunHelperIfRequested() { // the resource-limit helper re-executes this binary
		return
	}
	os.Exit(m.Run())
}

const fakeScript = pandoctest.Script

// fakeDir makes a directory holding a fake pandoc and returns the path of the program.
func fakeDir(t *testing.T) (dir, bin string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "pandoc")
	if err := os.WriteFile(bin, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, bin
}

func setMode(t *testing.T, dir, mode string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(mode), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLog(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}

// callsOf returns each recorded argument list.
func callsOf(t *testing.T, dir string) [][]string {
	t.Helper()
	var out [][]string
	var cur []string
	for _, l := range strings.Split(readLog(t, dir, "args.log"), "\n") {
		switch l {
		case "":
		case "--end--":
			out = append(out, cur)
			cur = nil
		default:
			cur = append(cur, l)
		}
	}
	return out
}

func eventually(t *testing.T, d time.Duration, f func() bool) bool {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return f()
}

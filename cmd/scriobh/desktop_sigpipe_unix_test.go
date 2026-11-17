//go:build unix

package main

import (
	"bufio"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDesktopSurvivesClosedOutput: a shell that is killed outright (kill -9) leaves the server's standard input,
// output and error with no reader at once. Go's runtime ends a process on SIGPIPE when a write to descriptor 1 or 2
// fails, so without the fix the server's first log line after that ("standard input closed") killed it with signal
// 13, before the graceful stop and the checkpoint of the write-ahead log. It must exit with status 0 and leave the
// database checkpointed.
func TestDesktopSurvivesClosedOutput(t *testing.T) {
	bin := buildBinary(t, "prod")
	home := t.TempDir()
	dir := desktopDataDir(home)

	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	cmd := exec.Command(bin)
	cmd.Env = desktopEnv(home, dtToken)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Only the child holds the far ends now, as with the shell's pipes.
	inR.Close()
	outW.Close()
	errW.Close()
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(outR)
		if sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "LISTEN "); ok {
				found <- rest
			}
		}
	}()
	// The reader of the error stream keeps up with it until the shell "dies", as the shell's own reader does.
	go func() { _, _ = io.Copy(io.Discard, errR) }()
	var base string
	select {
	case base = <-found:
	case <-time.After(30 * time.Second):
		t.Fatal("no LISTEN line")
	}
	// Some writes, so that there is something in the write-ahead log to checkpoint.
	c := client()
	if at, _ := desktopFirstRun(t, c, base, dtToken, "Peig Sayers"); at != "/" {
		t.Fatalf("first run ended at %s", at)
	}
	if resp, err := http.Get(base + "/healthz"); err != nil || status(t, resp) != 200 {
		t.Fatalf("/healthz: %v", err)
	}
	// Something is waiting in the write-ahead log, so that its absence afterwards means a checkpoint was made.
	if st, err := os.Stat(filepath.Join(dir, "scriobh.db-wal")); err != nil || st.Size() == 0 {
		t.Fatalf("nothing is in the write-ahead log before the stop (%v): the checkpoint check below would prove nothing", err)
	}

	// The shell dies: nobody reads the child's output or error, and nobody holds its input.
	outR.Close()
	errR.Close()
	inW.Close()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("after every pipe was closed the server ended with %v (%v), want status 0", err, cmd.ProcessState)
		}
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || ws.Signaled() || ws.ExitStatus() != 0 {
			t.Fatalf("wait status %v", cmd.ProcessState)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the server did not stop after standard input closed")
	}
	if st, err := os.Stat(filepath.Join(dir, "scriobh.db-wal")); err == nil && st.Size() != 0 {
		t.Errorf("the write-ahead log holds %d bytes after the stop: it was not checkpointed", st.Size())
	}
	// The data directory's lock is free: the next launch starts and stops cleanly.
	p := startDesktop(t, bin, desktopEnv(home, strings.Repeat("0b", 32)))
	if code := p.signal(t, syscall.SIGTERM); code != 0 {
		t.Errorf("the next launch: exit %d", code)
	}
}

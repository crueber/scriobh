package main

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Independent review of p8-desktop-go.

// RP1: the shell's quit path closes the server's standard input and sends SIGTERM (contract steps 7 and 23). The
// stdin watcher calls the stop function of signal.NotifyContext, which also unregisters the SIGTERM handler, so a
// SIGTERM that arrives while the graceful shutdown is still running (an open request holds it for up to 10 s) takes
// Go's default action and kills the process by signal: no exit 0, no final WAL checkpoint, no "stopped" line.
func TestRevP8StdinClosedThenSIGTERMStillExitsCleanly(t *testing.T) {
	bin := buildBinary(t, "prod")
	home := t.TempDir()
	cmd := exec.Command(bin)
	cmd.Env = desktopEnv(home, dtToken)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, _ := cmd.StdoutPipe()
	logs := &syncBuf{}
	cmd.Stderr = logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	listening := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), "LISTEN "); ok {
				listening <- rest
			}
		}
		done <- cmd.Wait()
	}()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	var base string
	select {
	case base = <-listening:
	case <-time.After(30 * time.Second):
		t.Fatalf("no LISTEN line:\n%s", logs)
	}
	hostport := strings.TrimPrefix(base, "http://")
	// A request whose header never ends keeps the connection active, so Shutdown has something to wait for.
	conn, err := net.Dial("tcp", hostport)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /healthz HTTP/1.1\r\nHost: %s\r\nX-Slow: 1\r\n", hostport)
	time.Sleep(200 * time.Millisecond)

	stdin.Close() // the shell quits ...
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "standard input closed") {
		if time.Now().After(deadline) {
			t.Fatalf("the stop on standard input did not begin:\n%s", logs)
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGTERM) // ... and asks the server to stop, as the contract says it does
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("exit after standard input closed and SIGTERM: %v (state %v), want status 0\n%s", err, cmd.ProcessState, logs)
		}
		if !strings.Contains(logs.String(), `"msg":"stopped"`) {
			t.Errorf("the process did not stop gracefully (no \"stopped\" line):\n%s", logs)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the server did not exit\n%s", logs)
	}
}

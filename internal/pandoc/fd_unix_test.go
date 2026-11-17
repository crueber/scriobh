//go:build unix

package pandoc_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/pandoc"
)

// nonCloexec lists the descriptors above 2 that this process holds without close-on-exec: the ones a child
// started by os/exec (Pandoc, or the limits helper) would inherit.
func nonCloexec() []int {
	var out []int
	for fd := 3; fd < 1024; fd++ {
		fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil {
			continue // not open
		}
		if fl&unix.FD_CLOEXEC == 0 {
			out = append(out, fd)
		}
	}
	return out
}

// GUARD (passes today; TestReviewServerHoldsNoInheritableDescriptors). A server that holds a database, a listener and a file passes none of them to Pandoc: every
// descriptor above 2 is close-on-exec. The control proves the probe sees a leak (a raw dup is not close-on-exec and
// the child then lists it), so that "nothing" is not just a probe that cannot see.
const fdScript = `#!/bin/sh
D="$(dirname "$0")"
case "$1" in --version) printf 'pandoc 3.1.9\n'; exit 0;; esac
ls /dev/fd > "$D/fds.log" 2>&1
echo '# ok'
exit 0
`

func TestReviewServerHoldsNoInheritableDescriptors(t *testing.T) {
	// Descriptors the test process was started with are its parent's doing, not the server's: a CI runner may hand
	// the test binary some without close-on-exec (GitHub's did: 142 and 145). Only what the server opens counts.
	inherited := map[int]bool{}
	for _, fd := range nonCloexec() {
		inherited[fd] = true
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "scriobh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Read().Exec(`SELECT 1`); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	fh, err := os.Open("/etc/hosts")
	if err != nil {
		t.Skip(err)
	}
	defer fh.Close()
	var leaked []int
	for _, fd := range nonCloexec() {
		if !inherited[fd] {
			leaked = append(leaked, fd)
		}
	}
	if len(leaked) > 0 {
		t.Fatalf("descriptors a child would inherit: %v (the process started with %d of its own)", leaked, len(inherited))
	}
	self, _ := os.Executable()
	run := func(helper string) []string {
		dir, bin := fakeDir(t)
		if err := os.WriteFile(bin, []byte(fdScript), 0o755); err != nil {
			t.Fatal(err)
		}
		r := pandoc.NewRunner(pandoc.Limits{}, t.TempDir(), helper)
		b := locate(t, r, bin)
		if _, err := r.Import(context.Background(), b, "rst", []byte("x")); err != nil {
			t.Fatalf("helper=%q: %v", helper, err)
		}
		return strings.Fields(readLog(t, dir, "fds.log"))
	}
	for _, helper := range []string{"", self} {
		clean := run(helper)
		// control: one leaked descriptor shows up in the child, with and without the helper
		leak, err := syscall.Dup(int(fh.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		withLeak := run(helper)
		syscall.Close(leak)
		if len(withLeak) != len(clean)+1 {
			t.Errorf("helper=%q: the probe did not see a leaked descriptor: %v then %v", helper, clean, withLeak)
		}
	}
}

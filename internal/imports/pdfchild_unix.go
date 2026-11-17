//go:build unix

package imports

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// childSysProcAttr puts the child in a process group of its own, so that the
// whole group can be killed, and (Linux) asks for it to be killed with us.
func childSysProcAttr() *syscall.SysProcAttr {
	a := &syscall.SysProcAttr{Setpgid: true}
	setPdeathsig(a)
	return a
}

// killGroup sends SIGKILL to the whole process group (SIGTERM would be ignored by a child that wants it ignored).
func killGroup(p *os.Process) {
	if p == nil {
		return
	}
	syscall.Kill(-p.Pid, syscall.SIGKILL)
	p.Kill()
}

// limitSelf is the child's own limits: CPU time, and where it is honoured the
// size of its data segment.
func limitSelf() {
	wall := int64(defaultPDFWall/time.Second) + 5
	var rl syscall.Rlimit
	rl.Cur, rl.Max = rlimVal(rl.Cur, wall), rlimVal(rl.Max, wall+5)
	syscall.Setrlimit(syscall.RLIMIT_CPU, &rl)
	limitData(int64(defaultPDFMemMB) << 20 * 2)
}

// rlimVal converts to the type of a resource-limit field, which is uint64 on most systems and int64 on some.
func rlimVal[T ~int64 | ~uint64](_ T, v int64) T { return T(v) }

// psRSS reads a process's resident memory from ps (macOS and the BSDs have no /proc).
func psRSS(pid int) (int64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	kb, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, false
	}
	return kb << 10, true
}

// maxRSSOf is the peak resident memory of a process that has ended (the kernel's figure: bytes on macOS,
// kilobytes on the others).
func maxRSSOf(ps *os.ProcessState) int64 {
	if ps == nil {
		return 0
	}
	ru, ok := ps.SysUsage().(*syscall.Rusage)
	if !ok || ru == nil {
		return 0
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "ios" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) << 10
}

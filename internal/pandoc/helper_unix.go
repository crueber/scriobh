//go:build unix

package pandoc

import (
	"crypto/subtle"
	"encoding/json"
	"os"
	"strings"
	"syscall"
)

const (
	helperName = "scriobh-pandoc-helper"
	helperFlag = "--scriobh-pandoc-helper"
)

// RunHelperIfRequested is called first thing in main (and in a test binary's
// TestMain). When this process was started as the limits helper it sets the
// resource limits it was given and replaces itself with Pandoc, and does not
// return; otherwise it returns false at once.
//
// The helper exists because Go cannot set limits on a child between fork and
// exec. The arguments are built by Runner (a JSON of limits, "--", the path of
// the approved Pandoc, its arguments); the environment is the one Runner built,
// passed through unchanged. Run by hand it can do nothing a person could not
// do with ulimit and exec.
func RunHelperIfRequested() bool {
	if len(os.Args) < 5 || os.Args[1] != helperFlag || os.Args[3] != "--" {
		return false
	}
	// Only the server's own supervisor may use this: it hands over a token in the environment and the same token, and the
	// path of the program, on a pipe it made, descriptor 3. A hand-typed call has no such pipe and does nothing.
	pipe := os.NewFile(3, "helper-pipe")
	var buf [4096]byte
	n, _ := pipe.Read(buf[:])
	pipe.Close()
	lines := strings.SplitN(string(buf[:n]), "\n", 3)
	token := os.Getenv(tokenEnv)
	if len(lines) < 2 || len(token) != 64 || subtle.ConstantTimeCompare([]byte(lines[0]), []byte(token)) != 1 || lines[1] != os.Args[4] {
		os.Exit(125)
	}
	// And only the program the supervisor approved, which is checked again here, in the child, at the last moment.
	if fi, err := os.Stat(os.Args[4]); err != nil || checkFile(os.Args[4], fi) != nil {
		os.Exit(125)
	}
	env := os.Environ()[:0:0]
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, tokenEnv+"=") {
			env = append(env, kv)
		}
	}
	var l struct {
		CPU    uint64 `json:"cpu"`
		Files  uint64 `json:"files"`
		FSize  uint64 `json:"fsize"`
		NoCore bool   `json:"nocore"`
	}
	if json.Unmarshal([]byte(os.Args[2]), &l) != nil {
		os.Exit(125)
	}
	set := func(res int, v uint64) {
		var cur, old syscall.Rlimit
		if syscall.Getrlimit(res, &old) == nil {
			v = neverRaise(v, rlimValue(old.Max)) // never raise
		}
		setRlim(&cur.Cur, v)
		setRlim(&cur.Max, v)
		if err := syscall.Setrlimit(res, &cur); err != nil {
			os.Exit(126)
		}
	}
	if l.CPU > 0 {
		set(syscall.RLIMIT_CPU, l.CPU)
	}
	if l.Files > 0 {
		set(syscall.RLIMIT_NOFILE, l.Files)
	}
	if l.FSize > 0 {
		set(syscall.RLIMIT_FSIZE, l.FSize)
	}
	if l.NoCore {
		set(syscall.RLIMIT_CORE, 0)
	}
	err := syscall.Exec(os.Args[4], os.Args[4:], env)
	_ = err
	os.Exit(127)
	return true
}

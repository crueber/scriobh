//go:build unix

package pandoc

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"syscall"
)

const supported = true

// ownerOK says the file belongs to root or to the user this process runs as.
func ownerOK(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == 0 || int(st.Uid) == os.Geteuid()
}

// openNoFollow opens a file for reading and refuses to follow a link at the last
// component: a link Pandoc (or something it ran) made to a file elsewhere is not read.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}

// tokenEnv names the variable that carries the helper's token. The token is 32 random bytes made for the call; the
// same bytes, and the path of the program to run, are written to a pipe the helper inherits as descriptor 3. The helper
// runs only when the two agree, so the flag does nothing when typed by hand: a shell has no such pipe.
const tokenEnv = "SCRIOBH_PANDOC_HELPER_TOKEN"

// command builds the process: Pandoc itself, or this program's helper that sets
// resource limits and then executes Pandoc, in its own process group, in the
// call's folder, with the environment built here and no standard input. afterStart
// closes the parent's copies of the pipe once the child has been started (or failed to).
func (r *Runner) command(b Binary, c *call, args []string) (*exec.Cmd, func(), error) {
	none := func() {}
	if r.helper == "" {
		cmd := &exec.Cmd{Path: b.Resolved, Args: append([]string{b.Resolved}, args...)}
		cmd.Dir = c.dir
		cmd.Env = c.environment() // never nil: nil would hand over the server's own environment
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd, none, nil
	}
	full, err := r.helperArgs(b.Resolved, args)
	if err != nil {
		return nil, none, fail(KindFailed, "Pandoc could not be started.")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, none, fail(KindFailed, "Pandoc could not be started.")
	}
	token := hex.EncodeToString(raw)
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, none, fail(KindFailed, "Pandoc could not be started.")
	}
	if _, err := pw.WriteString(token + "\n" + b.Resolved + "\n"); err != nil {
		pr.Close()
		pw.Close()
		return nil, none, fail(KindFailed, "Pandoc could not be started.")
	}
	pw.Close()
	cmd := &exec.Cmd{Path: r.helper, Args: full}
	cmd.Dir = c.dir
	cmd.Env = append(c.environment(), tokenEnv+"="+token)
	cmd.ExtraFiles = []*os.File{pr} // descriptor 3
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd, func() { pr.Close() }, nil
}

// killGroup kills the process and everything it started.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

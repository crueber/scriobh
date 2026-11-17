package pandoc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MinVersion is the oldest Pandoc accepted: 3.1.6. An earlier one can be made to
// write a file anywhere the server's user can write, through --extract-media and an
// image element with a path in it (CVE-2023-35936, fixed in 3.1.4; CVE-2023-38745,
// fixed in 3.1.6, because that fix missed double-encoded path names), and --sandbox
// does not stop it: Pandoc's own advisory says the option "only affects IO done by
// readers and writers themselves". --sandbox arrived in 2.15. This comes from the
// advisories as the reviewer read them; it has not been checked against a real
// Pandoc on the machine this was written on.
var MinVersion = [3]int{3, 1, 6}

// ErrorKind classifies a failure; Message is for a person and never holds
// anything Pandoc printed.
type ErrorKind string

// Kinds of failure.
const (
	KindDisabled    ErrorKind = "disabled"
	KindNotFound    ErrorKind = "not_found"
	KindUnsafe      ErrorKind = "unsafe_binary"
	KindVersion     ErrorKind = "version"
	KindUnsupported ErrorKind = "unsupported_platform"
	KindBadFormat   ErrorKind = "bad_format"
	KindTooLarge    ErrorKind = "too_large"
	KindTimeout     ErrorKind = "timeout"
	KindFailed      ErrorKind = "failed"
	KindBusy        ErrorKind = "busy"
	KindCancelled   ErrorKind = "cancelled"
)

// Error is a failure of the bridge.
type Error struct {
	Kind ErrorKind
	Msg  string
}

func (e *Error) Error() string { return "pandoc: " + string(e.Kind) }

// Message is the sentence for a person.
func (e *Error) Message() string { return e.Msg }

func fail(k ErrorKind, format string, a ...any) *Error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}

// AsError returns err as *Error, if it is one.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Binary is a Pandoc executable that passed the checks.
type Binary struct {
	Path     string // the path that was configured or found
	Resolved string // after symbolic links
	Version  string // "3.1.9"
	size     int64
	mod      time.Time
}

func (b Binary) same(fi os.FileInfo) bool { return fi.Size() == b.size && fi.ModTime().Equal(b.mod) }

var versionRe = regexp.MustCompile(`^pandoc(?:-cli)? (\d+)\.(\d+)(?:\.(\d+))?(?:\.(\d+))?\b`)

// ParseVersion reads the first line of `pandoc --version`.
func ParseVersion(out string) (string, [3]int, bool) {
	line, _, _ := strings.Cut(out, "\n")
	m := versionRe.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return "", [3]int{}, false
	}
	var v [3]int
	parts := []string{m[1], m[2], m[3]}
	for i, p := range parts {
		if p != "" {
			v[i], _ = strconv.Atoi(p)
		}
	}
	ver := m[1] + "." + m[2]
	if m[3] != "" {
		ver += "." + m[3]
	}
	if m[4] != "" {
		ver += "." + m[4]
	}
	return ver, v, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// Locate finds and checks the binary. configured is the administrator's
// absolute path; empty means look for "pandoc" on the server's PATH. It runs
// the program once with --version, under the same environment and limits as a
// conversion, to read the version.
func (r *Runner) Locate(ctx context.Context, configured string) (Binary, error) {
	if !supported {
		return Binary{}, fail(KindUnsupported, "Pandoc cannot be used on this platform.")
	}
	path := strings.TrimSpace(configured)
	if path == "" {
		p, err := exec.LookPath("pandoc")
		if err != nil {
			return Binary{}, fail(KindNotFound, "Pandoc was not found on the server's PATH. Install it, or set its full path.")
		}
		path = p
	}
	if len(path) > 4096 || strings.ContainsAny(path, "\x00\n\r") {
		return Binary{}, fail(KindNotFound, "That is not a usable path.")
	}
	if !filepath.IsAbs(path) {
		return Binary{}, fail(KindUnsafe, "The path to Pandoc must be absolute.")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Binary{}, fail(KindNotFound, "There is no file at that path.")
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return Binary{}, fail(KindNotFound, "There is no file at that path.")
	}
	if err := checkFile(resolved, fi); err != nil {
		return Binary{}, err
	}
	if r.refused(resolved) {
		return Binary{}, fail(KindUnsafe, "A program inside the server's data directory is not run: what a person can put there is not a program to trust. Install Pandoc elsewhere.")
	}
	b := Binary{Path: path, Resolved: resolved, size: fi.Size(), mod: fi.ModTime()}
	out, err := r.version(ctx, b)
	if err != nil {
		return Binary{}, err
	}
	ver, v, ok := ParseVersion(out)
	if !ok {
		return Binary{}, fail(KindVersion, "That program did not say it is Pandoc.")
	}
	if versionLess(v, MinVersion) {
		return Binary{}, fail(KindVersion, "This Pandoc is version %s. A version before %d.%d.%d can be made to write a file anywhere the server can (CVE-2023-38745, an incomplete fix of CVE-2023-35936, in --extract-media), and --sandbox does not stop it. Install %d.%d.%d or later.", ver, MinVersion[0], MinVersion[1], MinVersion[2], MinVersion[0], MinVersion[1], MinVersion[2])
	}
	b.Version = ver
	return b, nil
}

// checkFile applies the rules of a binary that will be run: a regular file
// (links already followed), executable, owned by root or by this process's
// user, not writable by its group or by others, and in a directory that is not.
func checkFile(resolved string, fi os.FileInfo) *Error {
	if !fi.Mode().IsRegular() {
		return fail(KindUnsafe, "That path is not a regular file.")
	}
	if fi.Mode().Perm()&0o111 == 0 {
		return fail(KindUnsafe, "That file is not executable.")
	}
	if fi.Mode().Perm()&0o022 != 0 {
		return fail(KindUnsafe, "That file can be changed by other users (group or world writable), so it is not run.")
	}
	if !ownerOK(fi) {
		return fail(KindUnsafe, "That file is owned by neither root nor the user the server runs as, so it is not run.")
	}
	dir := filepath.Dir(resolved)
	di, err := os.Stat(dir)
	if err != nil || !di.IsDir() {
		return fail(KindNotFound, "There is no file at that path.")
	}
	if di.Mode().Perm()&0o022 != 0 {
		return fail(KindUnsafe, "The folder that holds it can be changed by other users, so it is not run.")
	}
	if !ownerOK(di) {
		return fail(KindUnsafe, "The folder that holds it is owned by neither root nor the user the server runs as, so it is not run.")
	}
	return nil
}

// recheck is the check made again just before a run: the file must still be the
// one that was approved.
func (b Binary) recheck() *Error {
	fi, err := os.Stat(b.Resolved)
	if err != nil {
		return fail(KindNotFound, "Pandoc is no longer where it was.")
	}
	if !b.same(fi) {
		return fail(KindUnsafe, "The Pandoc file changed since it was checked. Check it again in the Pandoc settings.")
	}
	if e := checkFile(b.Resolved, fi); e != nil {
		return e
	}
	return nil
}

func trimOutput(b []byte) string { return string(bytes.TrimSpace(b)) }

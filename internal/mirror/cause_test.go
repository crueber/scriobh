package mirror

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
)

// A log line or a status carries the operating system's reason, never the
// data directory's path or a file name made from a title.
func TestCauseStripsPaths(t *testing.T) {
	path := "/srv/scriobh/data/mirror/.tmp/w-1234"
	for name, err := range map[string]error{
		"open":    &os.PathError{Op: "open", Path: path, Err: syscall.ENOSPC},
		"rename":  &os.LinkError{Op: "rename", Old: path, New: "/srv/scriobh/data/mirror/the-salt-road--abc123/manuscript/01-secret-title.md", Err: syscall.EACCES},
		"wrapped": fmt.Errorf("something: %w", &os.PathError{Op: "mkdir", Path: path, Err: syscall.EROFS}),
	} {
		got := cause(err)
		if strings.Contains(got, "/srv") || strings.Contains(got, "secret-title") || got == "" {
			t.Errorf("%s: %q still carries a path", name, got)
		}
	}
	if got := cause(errors.New("disk full")); got != "disk full" {
		t.Errorf("a plain error changed: %q", got)
	}
	if got := cause(&os.PathError{Op: "open", Path: path, Err: syscall.ENOSPC}); !strings.Contains(got, "no space left") {
		t.Errorf("the reason is lost: %q", got)
	}
}

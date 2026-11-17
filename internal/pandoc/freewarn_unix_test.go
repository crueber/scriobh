//go:build unix

package pandoc_test

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/pandoc"
)

// When the volume of the temporary folder cannot be asked, the low-space check is not applied (as it never was)
// and the server says so once, by naming the folder, in its local log.
func TestFreeSpaceCheckThatCannotBeMadeWarnsOnce(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	_, bin := fakeDir(t)
	root := filepath.Join(t.TempDir(), "gone")         // not there: statfs fails, and so does MkdirTemp
	b := locate(t, newRunner(t, pandoc.Limits{}), bin) // the version check runs in a folder that exists
	r := pandoc.NewRunner(pandoc.Limits{}, root, "")
	for i := 0; i < 3; i++ {
		if _, err := r.Import(context.Background(), b, "rst", []byte("x")); err == nil {
			t.Fatal("a conversion in a folder that is not there succeeded")
		}
	}
	got := buf.String()
	if n := strings.Count(got, "low-space check is not applied"); n != 1 {
		t.Fatalf("%d warnings, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, root) {
		t.Errorf("the warning does not name the temp root %q:\n%s", root, got)
	}
}

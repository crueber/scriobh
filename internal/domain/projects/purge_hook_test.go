package projects

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The conflict-copy purge hook turns an id into a path: "." and ".." must not
// reach conflicts/ itself or the data directory.
func TestConflictsHookRefusesIdsThatAreNotIds(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, "conflicts", "prj_01J8Z00000000000000000000A")
	if err := os.MkdirAll(keep, 0o700); err != nil {
		t.Fatal(err)
	}
	hook := removeConflicts(dir)
	for _, bad := range []string{".", "..", "", "../x", "conflicts"} {
		hook(context.Background(), bad)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("conflict copies were removed by a bad id: %v", err)
	}
	hook(context.Background(), "prj_01J8Z00000000000000000000A")
	if _, err := os.Stat(keep); !os.IsNotExist(err) {
		t.Errorf("a real id did not remove its copies: %v", err)
	}
}

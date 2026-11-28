package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUndoWithEmptyAsideDoesNotTouchCWD(t *testing.T) {
	cwd := t.TempDir()
	cwdDB := filepath.Join(cwd, "scriobh.db")
	if err := os.WriteFile(cwdDB, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	dataDir := t.TempDir()
	installedDB := filepath.Join(dataDir, "scriobh.db")
	if err := os.WriteFile(installedDB, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	undo(dataDir, "", []string{"scriobh.db"}, []string{"scriobh.db"})

	if _, err := os.Stat(installedDB); !os.IsNotExist(err) {
		t.Fatalf("installed file should be removed, err = %v", err)
	}
	got, err := os.ReadFile(cwdDB)
	if err != nil || string(got) != "keep-me" {
		t.Fatalf("CWD scriobh.db was modified or moved: %q, %v", got, err)
	}
}

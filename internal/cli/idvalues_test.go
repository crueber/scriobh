package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Values that look like names but are not: a tab id has no list to be looked up
// in, and a backup's id is its file name.

func TestValuesThatAreNotNamesPassThrough(t *testing.T) {
	e := newEnv(t, nil)
	e.ok("project", "create", "--title", "Lease Book")
	d := e.ok("doc", "create", "Lease Book", "--title", "One", "--content", "Words.")
	id := itemID(t, d)
	r := e.ok("doc", "lease", id, "--tab-id", "tab-1")
	if r.data(t)["held"] != true {
		t.Errorf("lease: %s", r.stdout)
	}
	// A save carrying a tab id (the kind of field that ends in _id) is not looked up either.
	r = e.ok("doc", "update", id, "--base-revision", "1", "--tab-id", "tab-1", "--content", "More words.")
	if r.data(t)["item"].(map[string]any)["revision"].(float64) < 1 {
		t.Errorf("update with a tab id: %s", r.stdout)
	}
}

func TestBackupIDsAreNotLookedUpAsNames(t *testing.T) {
	e := newEnv(t, nil)
	admin := testutil.User(t, e.srv.DB, ops.RoleAdmin, "admin@test.local")
	akey := e.mint(admin, ops.RoleAdmin, `{"name":"admin","scope":"admin"}`)
	// A backup cannot be previewed, and says so.
	if r := e.runWith(akey, "", "backup", "create", "--dry-run"); r.code != cli.ExitValidation || r.fail(t)["code"] != "dry_run_unsupported" {
		t.Errorf("backup create --dry-run: %d %s", r.code, r.stderr)
	}
	r := e.runWith(akey, "", "backup", "create")
	if r.code != 0 {
		t.Fatalf("backup create: %d %s", r.code, r.stderr)
	}
	bid := r.data(t)["item"].(map[string]any)["id"].(string)
	out := filepath.Join(t.TempDir(), "b.zip")
	// A file response needs --output, and by id or by the name it has, it downloads.
	if r := e.runWith(akey, "", "backup", "download", bid); r.code != cli.ExitUsage || !strings.Contains(r.stderr, "--output") {
		t.Errorf("download without --output: %d %s", r.code, r.stderr)
	}
	r = e.runWith(akey, "", "backup", "download", bid, "--output", out)
	if r.code != 0 {
		t.Fatalf("backup download %s: %d %s", bid, r.code, r.stderr)
	}
	if st, err := os.Stat(out); err != nil || st.Size() < 100 || r.data(t)["bytes"].(float64) != float64(st.Size()) {
		t.Errorf("the download: %v %s", err, r.stdout)
	}
}

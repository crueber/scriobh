package cli_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func TestCreateReadUpdateThroughTheCLI(t *testing.T) {
	e := newEnv(t, nil)
	r := e.ok("project", "create", "--title", "The Salt Road", "--kind", "novel")
	env := r.envelope(t)
	if env["ok"] != true || env["op"] != "project.create" || env["status"].(float64) != 201 {
		t.Fatalf("envelope: %s", r.stdout)
	}
	pid := itemID(t, r)

	// A chapter, by project name, with the text from stdin.
	var out result
	{
		out = e.runWith(e.key, "It was low tide.\n\nThe boats lay over.\n", "doc", "create", "The Salt Road", "--title", "Harbour", "--kind", "chapter", "--content", "-")
		if out.code != 0 {
			t.Fatalf("doc create: %d %s %s", out.code, out.stdout, out.stderr)
		}
	}
	meta, _ := out.envelope(t)["meta"].(map[string]any)
	if res, _ := meta["resolved"].([]any); len(res) != 1 {
		t.Errorf("the envelope should say what the name resolved to: %s", out.stdout)
	}
	did := itemID(t, out)
	if got := out.data(t)["item"].(map[string]any)["word_count"].(float64); got != 8 {
		t.Errorf("word_count = %v, want 8", got)
	}

	// Read it back by path and by id; both agree.
	byPath := e.ok("doc", "get", "The Salt Road/Harbour")
	byID := e.ok("doc", "get", did)
	if itemID(t, byPath) != did || itemID(t, byID) != did {
		t.Errorf("by path %s, by id %s", itemID(t, byPath), itemID(t, byID))
	}
	rev := int(byPath.data(t)["item"].(map[string]any)["revision"].(float64))

	// Update with the revision, text from a flag.
	up := e.ok("doc", "update", "The Salt Road/Harbour", "--base-revision", itoa(rev), "--content", "A new first line.", "--title", "The Harbour")
	if got := up.data(t)["item"].(map[string]any)["title"]; got != "The Harbour" {
		t.Errorf("title = %v", got)
	}
	if e.ok("project", "get", pid).data(t)["item"].(map[string]any)["document_count"].(float64) < 1 {
		t.Error("the project should hold the chapter")
	}
	// The same flags in the other spelling, and --data.
	r2 := e.ok("doc", "update", "--id", did, "--data", `{"base_revision": `+itoa(rev+1)+`, "synopsis_short": "From data"}`)
	if r2.data(t)["item"].(map[string]any)["synopsis_short"] != "From data" {
		t.Errorf("--data: %s", r2.stdout)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestStaleRevisionIsExit4(t *testing.T) {
	e := newEnv(t, nil)
	e.ok("project", "create", "--title", "Stale")
	e.ok("doc", "create", "Stale", "--title", "One", "--content", "Words.")
	r := e.run("doc", "update", "Stale/One", "--base-revision", "99", "--content", "Mine.")
	if r.code != cli.ExitConflict {
		t.Fatalf("exit %d, want 4; %s", r.code, r.stderr)
	}
	f := r.fail(t)
	if f["code"] != "conflict" || f["data"].(map[string]any)["current_revision"] == nil {
		t.Errorf("the error should carry the current revision: %v", f)
	}
	if r.stdout != "" {
		t.Errorf("stdout must be empty on error: %q", r.stdout)
	}
}

func TestDryRunTrashChangesNothing(t *testing.T) {
	e := newEnv(t, nil)
	e.ok("project", "create", "--title", "Keep Me")
	r := e.ok("project", "trash", "Keep Me", "--dry-run")
	meta := r.envelope(t)["meta"].(map[string]any)
	if meta["dry_run"] != true || meta["would_change"] != true {
		t.Errorf("meta: %v", meta)
	}
	if got := e.ok("project", "get", "Keep Me").data(t)["item"].(map[string]any)["trashed"]; got != false {
		t.Errorf("the dry run trashed the project: %v", got)
	}
	// --fail-on-change turns a preview that would change something into exit 9.
	if r := e.run("project", "trash", "Keep Me", "--dry-run", "--fail-on-change"); r.code != cli.ExitWouldChange {
		t.Errorf("--fail-on-change: exit %d, want 9", r.code)
	}
	// Without --yes a real trash is refused before anything is sent.
	before := e.requests.Load()
	r2 := e.run("project", "trash", "Keep Me")
	if r2.code != cli.ExitUsage || !strings.Contains(r2.stderr, "--yes") {
		t.Errorf("trash without --yes: %d %s", r2.code, r2.stderr)
	}
	if e.requests.Load() != before {
		t.Error("a refused command should not reach the server")
	}
	e.ok("project", "trash", "Keep Me", "--yes")
	if got := e.ok("project", "get", "Keep Me", "--json"); got.data(t)["item"].(map[string]any)["trashed"] != true {
		t.Error("the real trash did not happen")
	}
}

func TestNoDryRunOpsRefuse(t *testing.T) {
	e := newEnv(t, nil)
	e.ok("project", "create", "--title", "Lease")
	d := e.ok("doc", "create", "Lease", "--title", "One")
	r := e.run("doc", "lease", itemID(t, d), "--tab-id", "t1", "--dry-run")
	if r.code != cli.ExitValidation || r.fail(t)["code"] != "dry_run_unsupported" {
		t.Errorf("exit %d: %s", r.code, r.stderr)
	}
}

func TestExitCodes(t *testing.T) {
	e := newEnv(t, nil)
	e.ok("project", "create", "--title", "Codes")
	read := e.mint(e.userID, ops.RoleWriter, `{"name":"ro","scope":"read"}`)
	cases := []struct {
		name string
		run  func() result
		want int
		code string
	}{
		{"usage: unknown command", func() result { return e.run("nonsense", "thing") }, cli.ExitUsage, "usage"},
		{"usage: unknown flag", func() result { return e.run("project", "list", "--nope") }, cli.ExitUsage, "usage"},
		{"usage: missing required", func() result { return e.run("project", "create") }, cli.ExitUsage, "usage"},
		{"usage: bad integer", func() result { return e.run("doc", "update", "Codes/x", "--base-revision", "abc") }, cli.ExitUsage, "usage"},
		{"not found: id", func() result { return e.run("project", "get", "prj_00000000000000000000000000") }, cli.ExitNotFound, "not_found"},
		{"not found: name", func() result { return e.run("project", "get", "No Such Book") }, cli.ExitNotFound, "not_found"},
		{"auth: bad key", func() result { return e.runWith("scr_"+strings.Repeat("x", 43), "", "project", "list") }, cli.ExitAuth, "unauthenticated"},
		{"auth: no key", func() result { return e.runWith("", "", "project", "list") }, cli.ExitAuth, "no_credentials"},
		{"auth: forbidden", func() result { return e.runWith(read, "", "project", "create", "--title", "x") }, cli.ExitAuth, "forbidden"},
		{"validation: invalid", func() result { return e.run("project", "create", "--title", "  ") }, cli.ExitValidation, "invalid"},
		{"validation: bad enum", func() result { return e.run("project", "create", "--title", "x", "--kind", "epic") }, cli.ExitUsage, "usage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.run()
			if r.code != c.want {
				t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", r.code, c.want, r.stdout, r.stderr)
			}
			if got := r.fail(t)["code"]; got != c.code {
				t.Errorf("error code %v, want %s", got, c.code)
			}
			if r.stdout != "" {
				t.Errorf("stdout should be empty on an error: %q", r.stdout)
			}
		})
	}
	// Unreachable: nothing listens.
	dead := newEnv(t, nil)
	dead.ts.Close()
	if r := dead.run("project", "list", "--retries", "0"); r.code != cli.ExitUnreachable || r.fail(t)["code"] != "unreachable" {
		t.Errorf("unreachable: %d %s", r.code, r.stderr)
	}
	// Rate limited, with retry_after.
	rl := newEnv(t, map[string]string{"SCRIOBH_RATE_LIMIT": "40"})
	var got result
	for i := 0; i < 60; i++ {
		if got = rl.run("project", "list"); got.code != 0 {
			break
		}
	}
	if got.code != cli.ExitRateLimited || got.fail(t)["retry_after"] == nil {
		t.Errorf("rate limit: %d %s", got.code, got.stderr)
	}
}

// TestKeysEndToEnd: key create, list, whoami and revoke work through the
// generic command mapping.
func TestKeysEndToEnd(t *testing.T) {
	e := newEnv(t, nil)
	// A write key may not manage keys; a session or an admin-scope key can. Use
	// an admin account.
	adminUser := newAdmin(t, e)
	akey := e.mint(adminUser, ops.RoleAdmin, `{"name":"admin key","scope":"admin"}`)
	r := e.runWith(akey, "", "key", "create", "--name", "Claude: salt road", "--scope", "write", "--expires-in-days", "7")
	if r.code != 0 {
		t.Fatalf("key create: %d %s", r.code, r.stderr)
	}
	secret, _ := r.data(t)["secret"].(string)
	if !strings.HasPrefix(secret, "scr_") {
		t.Fatalf("no secret: %s", r.stdout)
	}
	// The new key works, and whoami describes it.
	w := e.runWith(secret, "", "whoami")
	if w.code != 0 || w.data(t)["scope"] != "write" || w.data(t)["via"] != "apikey" {
		t.Errorf("whoami: %d %s", w.code, w.stdout)
	}
	// The list never shows the secret; revoke by name.
	l := e.runWith(akey, "", "key", "list")
	if strings.Contains(l.stdout, secret) || !strings.Contains(l.stdout, "Claude: salt road") {
		t.Errorf("list: %s", l.stdout)
	}
	if r := e.runWith(akey, "", "key", "revoke", "Claude: salt road", "--yes"); r.code != 0 {
		t.Fatalf("revoke by name: %d %s", r.code, r.stderr)
	}
	if r := e.runWith(secret, "", "whoami"); r.code != cli.ExitAuth {
		t.Errorf("a revoked key: exit %d, want 5", r.code)
	}
}

func newAdmin(t *testing.T, e *env) string {
	t.Helper()
	return testutil.User(t, e.srv.DB, ops.RoleAdmin, "admin@test.local")
}

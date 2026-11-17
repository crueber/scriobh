package cli_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// The webhook commands exist, take names, refuse what the server refuses, and
// never print the secret again after the one answer that carries it.
func TestWebhookCommandsThroughTheCLI(t *testing.T) {
	e := newEnv(t, nil)
	// A webhook is managed with the admin scope, which only an administrator's key can have.
	admin := testutil.User(t, e.srv.DB, ops.RoleAdmin, "admin@test.local")
	key := e.mint(admin, ops.RoleAdmin, `{"name":"hooks","scope":"admin"}`)
	run := func(args ...string) result { return e.runWith(key, "", args...) }
	ok := func(args ...string) result {
		t.Helper()
		r := run(args...)
		if r.code != 0 {
			t.Fatalf("scriobh-cli %s: exit %d\n%s\n%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
		return r
	}
	ok("project", "create", "--title", "The Salt Road")
	r := ok("webhook", "create", "--project", "The Salt Road", "--name", "Build server", "--url", "https://ci.example.com/hooks/scriobh",
		"--events", "document.updated", "--events", "export.completed")
	secret, _ := r.data(t)["secret"].(string)
	if !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("no secret in %s", r.stdout)
	}
	id := itemID(t, r)
	for _, args := range [][]string{
		{"webhook", "list", "--project", "The Salt Road"},
		{"webhook", "get", "The Salt Road", id},
		{"webhook", "update", "The Salt Road", id, "--enabled=false"},
		{"webhook", "deliveries", "The Salt Road", id},
	} {
		out := ok(args...)
		if strings.Contains(out.stdout+out.stderr, secret) {
			t.Errorf("%v printed the secret", args)
		}
	}
	// An internal address is a validation failure (exit 6).
	if out := run("webhook", "create", "--project", "The Salt Road", "--name", "bad", "--url", "https://127.0.0.1/x", "--events", "document.updated"); out.code != 6 {
		t.Errorf("an internal address: exit %d %s%s", out.code, out.stdout, out.stderr)
	}
	// A command that makes a secret is never previewed.
	if out := run("webhook", "create", "--project", "The Salt Road", "--name", "x", "--url", "https://ci.example.com/y", "--events", "document.updated", "--dry-run"); out.code != 6 {
		t.Errorf("dry run of create: exit %d %s%s", out.code, out.stdout, out.stderr)
	}
	// A write key is not enough.
	if out := e.run("webhook", "list", "--project", "The Salt Road"); out.code != 5 && out.code != 3 {
		t.Errorf("a write-scope key listed webhooks: exit %d", out.code)
	}
}

package cli_test

import (
	"strings"
	"testing"
)

// The CLI speaks as a key, so it cannot widen what reaches a public remote either.
func TestTheCLICannotWidenAPublicRemote(t *testing.T) {
	e := newEnv(t, nil)
	ok := func(args ...string) result {
		t.Helper()
		r := e.run(args...)
		if r.code != 0 {
			t.Fatalf("scriobh-cli %s: exit %d\n%s\n%s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
		}
		return r
	}
	ok("project", "create", "--title", "The Salt Road")
	r := ok("git", "remote", "create", "--project", "The Salt Road", "--name", "Public mirror", "--url", "https://forge.example.org/mairead/salt-road.git", "--token", "tok_abcdefghijklmnopqrstuvwxyz", "--push-mode", "manual")
	if !strings.Contains(r.stdout, `"audience": "public"`) && !strings.Contains(r.stdout, `"audience":"public"`) {
		t.Fatalf("a remote made without saying is not public: %s", r.stdout)
	}
	id := itemID(t, r)
	for _, args := range [][]string{
		{"git", "remote", "update", "--project", "The Salt Road", "--remote-id", id, "--audience", "private"},
		{"git", "remote", "update", "--project", "The Salt Road", "--remote-id", id, "--include-notes=true"},
		{"git", "push-force", "--project", "The Salt Road", "--remote-id", id, "--confirm", "Public mirror"},
	} {
		out := e.run(args...)
		if out.code == 0 || !strings.Contains(out.stdout+out.stderr, "browser") {
			t.Errorf("%v: exit %d %s%s", args, out.code, out.stdout, out.stderr)
		}
	}
}

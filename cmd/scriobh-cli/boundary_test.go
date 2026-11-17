package main

import (
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/crueber/scriobh"

// TestImportBoundary lists everything scriobh-cli links and fails if it reaches
// the server, the database, the op packages or any third-party module. The
// slim CLI is what ships to an agent's machine; it must stay a thin HTTP
// client whatever the server grows.
func TestImportBoundary(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var internal []string
	for _, p := range strings.Fields(string(out)) {
		switch {
		case p == module+"/cmd/scriobh-cli":
		case strings.HasPrefix(p, module+"/"):
			internal = append(internal, strings.TrimPrefix(p, module+"/"))
		case strings.Contains(strings.SplitN(p, "/", 2)[0], "."):
			t.Errorf("scriobh-cli links the third-party package %s", p)
		}
	}
	if len(internal) != 1 || internal[0] != "internal/cli" {
		t.Errorf("scriobh-cli may import only internal/cli of this module, and it links %v", internal)
	}
	for _, p := range strings.Fields(string(out)) {
		for _, banned := range []string{"/internal/server", "/internal/db", "/internal/ops", "/internal/domain", "/internal/registry", "/internal/auth", "database/sql", "modernc.org", "net/http/httptest"} {
			if strings.Contains(p, banned) {
				t.Errorf("scriobh-cli links %s", p)
			}
		}
	}
}

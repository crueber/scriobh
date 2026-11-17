package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/admin"
	"github.com/crueber/scriobh/internal/ops"
)

// The warnings and errors the diagnostics keep come through the server's own logging paths (a failed request, a
// panic), and none of them may carry what the request or the error held: a slug in a path, a file path, SQL text, a
// remote's address or an email. The route is the pattern the request matched; the error is its class.
func TestTheDiagnosticsLogCarriesNeitherPathsNorErrorText(t *testing.T) {
	s := newAuthzServer(t)
	s.mux.HandleFunc("GET /boom/{what}", func(http.ResponseWriter, *http.Request) {
		panic("CANARYPANIC at /home/canary/data/x with canary@example.org")
	})
	s.mux.HandleFunc("GET /bad/{slug}/read", func(w http.ResponseWriter, r *http.Request) {
		cause := fmt.Errorf("open %s/files/CANARYPATH/x.png: %w", s.Cfg.DataDir, errors.New("SELECT content FROM documents WHERE title = 'CANARYSQL' failed; remote https://user:CANARYPASS@git.example.org/CANARYREPO.git said canary@example.org"))
		s.fail(w, r, ops.Internal(cause))
	})
	for _, p := range []string{"/boom/CANARYSLUG", "/bad/CANARYSLUG/read", "/s/shl_CANARYTOKEN.abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG"} {
		s.Handler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	lines := s.ring.snapshot()
	if len(lines) < 2 {
		t.Fatalf("kept %d lines", len(lines))
	}
	all := ""
	routes := []string{}
	for _, l := range lines {
		all += l.Msg + " "
		for k, v := range l.Attrs {
			all += k + "=" + v + " "
		}
		routes = append(routes, l.Attrs["route"])
	}
	for _, bad := range []string{"CANARY", "canary", "example.org", s.Cfg.DataDir, "SELECT", "/home/"} {
		if strings.Contains(all, bad) {
			t.Errorf("the log ring holds %q: %s", bad, all)
		}
	}
	if !strings.Contains(strings.Join(routes, "|"), "GET /bad/{slug}/read") || !strings.Contains(strings.Join(routes, "|"), "GET /boom/{what}") {
		t.Errorf("routes are not patterns: %v", routes)
	}
	var cls string
	for _, l := range lines {
		if l.Attrs["route"] == "GET /bad/{slug}/read" {
			cls = l.Attrs["err"]
		}
	}
	if !strings.HasPrefix(cls, "ops:") && !strings.HasPrefix(cls, "*") {
		t.Errorf("the error is %q, want its class", cls)
	}
	// and the bundle built from the ring holds none of it either
	ada := s.newCaller(t, "ada", ops.RoleAdmin)
	rec := s.do(ada, "GET", "/api/v1/admin/diagnostics", "")
	for _, bad := range []string{"CANARY", "canary", "example.org", "SELECT"} {
		if strings.Contains(rec.Body.String(), bad) {
			t.Errorf("the bundle holds %q", bad)
		}
	}
}

func TestScrubTextTakesOutUrlsAndPaths(t *testing.T) {
	in := "clone https://u:p@git.example.org/r.git failed at /Users/me/dev/scriobh/data/files/x and C:\\Users\\me\\x; also /srv/scriobh/data/db mail me@x.org"
	got := admin.ScrubText(in, "/Users/me/dev/scriobh/data")
	for _, bad := range []string{"git.example.org", "/Users/", "/srv/", "C:\\Users", "me@x.org", "scriobh/data"} {
		if strings.Contains(got, bad) {
			t.Errorf("ScrubText left %q: %s", bad, got)
		}
	}
}

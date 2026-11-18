package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// The gate sweeps (F-252). A protected project, a canary in every place it keeps something written, and the people
// around it. Each sweep has a positive control: the same walk as the owner who has unlocked the project must find the
// canaries in many responses, or the locked walk would prove nothing.

func lockedBody(body string) bool { return strings.Contains(body, "protected with a passphrase") }

// opPath fills an op's path for a caller: the project (or a document of it, for the ops that take one), a Compendium
// or an entity, an event.
func (w *gateWorld) opPath(e *ops.Entry, evID string) string {
	id := w.pid
	switch {
	case e.Op.Auth.Kind == ops.AuthCompendium && e.Op.Auth.Resolve != nil:
		id = w.entID
		if strings.Contains(e.Op.Path, "/timeline/events/") {
			id = evID
		}
	case e.Op.Auth.Kind == ops.AuthCompendium:
		id = w.compID
	case e.Op.Auth.Resolve != nil:
		id = w.docID
	}
	return fillPath(e.Op.Path, id)
}

func (w *gateWorld) eventID(t *testing.T) string {
	t.Helper()
	var l struct{ Items []struct{ ID string } }
	rec := w.s.do(w.owner, "GET", "/api/v1/compendiums/"+w.compID+"/timeline/events", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	if len(l.Items) == 0 {
		t.Fatalf("no timeline event: %d %s", rec.Code, rec.Body)
	}
	return l.Items[0].ID
}

// TestGateOpSweep walks every registered op as people who have not unlocked the project. A project op must refuse;
// no op of any kind may carry a canary or a count of the project in a read. The same walk as the owner who has
// unlocked it is the control.
func TestGateOpSweep(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	evID := w.eventID(t)
	sweepFixtures := func() {
		s.sweepFixtures(t, w.owner, "project", w.pid)
	}
	sweepFixtures()
	locked := []caller{w.editor, w.owner2, w.admin}
	rank := map[string]int{ops.ProjectReader: 1, ops.ProjectReviewer: 2, ops.ProjectEditor: 3, ops.ProjectOwner: 4}

	controlHits := 0
	for _, e := range s.Reg.Entries() {
		if e.Op.Method != "GET" && e.Op.Scope != ops.ScopeRead {
			continue // a write cannot read; the project writes are walked below
		}
		path := w.opPath(e, evID)
		if e.Op.Method != "GET" {
			continue // read-scope previews are POSTs with bodies; they are walked with the writes
		}
		for _, c := range locked {
			rec := s.do(c, e.Op.Method, path, "")
			body := rec.Body.String()
			if l := w.leaks(body); len(l) > 0 {
				t.Errorf("%s as %s while locked leaks %v: %d %.300s", e.Op.Name, c.name, l, rec.Code, body)
			}
		}
		if rec := s.do(w.owner, e.Op.Method, path, ""); len(w.leaks(rec.Body.String())) > 0 {
			controlHits++
		}
	}
	if controlHits < 12 {
		t.Errorf("the control found the canaries in only %d reads as the owner who unlocked it; the locked walk proves little", controlHits)
	}

	// Every op that names a project refuses a person who has not unlocked it (and never lets the body through), on
	// every method, with the role they hold.
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind != ops.AuthProject || e.Op.Auth.GateExempt {
			continue
		}
		e := e
		t.Run(e.Op.Name, func(t *testing.T) {
			// a project the sweep trashed or archived is put back, as the authorization sweep does
			for _, op := range []string{"restore", "unarchive"} {
				s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/"+op, "")
			}
			sweepFixtures()
			path := w.opPath(e, evID)
			for _, c := range locked {
				member := c.name == "editor" || c.name == "owner2" || c.name == "admin"
				if !member {
					continue
				}
				role := ops.ProjectOwner
				if c.name == "editor" {
					role = ops.ProjectEditor
				}
				rec := s.do(c, e.Op.Method, path, bodyFor(e))
				body := rec.Body.String()
				if rank[role] >= rank[e.Op.Auth.Role] || c.name == "admin" {
					if rec.Code != http.StatusForbidden || !lockedBody(body) {
						t.Errorf("%s as %s (%s) while locked: %d %.200s, want 403 project_locked", e.Op.Name, c.name, role, rec.Code, body)
					}
				}
				if l := w.leaks(body); len(l) > 0 {
					t.Errorf("%s as %s while locked leaks %v", e.Op.Name, c.name, l)
				}
			}
			// a stranger still gets the 404 a missing project gives
			rec := s.do(w.outsider, e.Op.Method, path, bodyFor(e))
			if rec.Code != http.StatusNotFound || lockedBody(rec.Body.String()) {
				t.Errorf("%s as a stranger: %d %.200s, want a plain 404 that does not tell the project has a passphrase", e.Op.Name, rec.Code, rec.Body)
			}
			// and the owner who unlocked it is not refused
			if rec := s.do(w.owner, e.Op.Method, path, bodyFor(e)); lockedBody(rec.Body.String()) {
				t.Errorf("%s as the owner who unlocked it: %d %.200s", e.Op.Name, rec.Code, rec.Body)
			}
		})
	}
}

// TestGateLockedReadsOfEverythingElse: the lists and aggregates that span projects (the library, search across
// projects, notifications, a Compendium's views of the books that use it) show a protected project's title and say
// it needs a passphrase; nothing else of it.
func TestGateAggregatesDoNotLeak(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	evID := w.eventID(t)
	paths := []string{
		"/api/v1/projects", "/api/v1/projects?state=all", "/api/v1/notifications", "/api/v1/notifications/count",
		"/api/v1/compendiums", "/api/v1/compendiums/" + w.compID, "/api/v1/entities/" + w.entID, "/api/v1/entities/" + w.entID + "/appears-in",
		"/api/v1/compendiums/" + w.compID + "/entities", "/api/v1/compendiums/" + w.compID + "/timeline/events", "/api/v1/timeline/events/" + evID,
		"/api/v1/compendiums/" + w.compID + "/search?q=Heroine", "/api/v1/ai/usage", "/api/v1/files/usage",
		"/", "/search?q=walks", "/search?q=Heroine", "/compendiums", "/c/gate-bible", "/c/gate-bible/entities", "/c/gate-bible/timeline",
		"/c/gate-bible/e/" + w.entID, "/timeline/events/" + evID, "/settings", "/import", "/api/v1/me/sessions",
	}
	for _, c := range []caller{w.editor, w.owner2, w.admin} {
		for _, p := range paths {
			rec := s.do(c, "GET", p, "")
			if l := w.leaks(rec.Body.String()); len(l) > 0 {
				t.Errorf("GET %s as %s while locked leaks %v: %d %.400s", p, c.name, l, rec.Code, rec.Body)
			}
		}
	}
	hits := 0
	for _, p := range paths {
		if len(w.leaks(s.do(w.owner, "GET", p, "").Body.String())) > 0 {
			hits++
		}
	}
	if hits < 4 {
		t.Errorf("the control (the owner, who unlocked it) found the canaries in %d of these pages; they prove little", hits)
	}
	// the library still lists the project by its title, and says it needs a passphrase
	rec := s.do(w.editor, "GET", "/api/v1/projects", "")
	if !strings.Contains(rec.Body.String(), "Gate Book") {
		t.Errorf("a protected project's title may be shown: %s", rec.Body)
	}
}

// ---- the pages ----------------------------------------------------------------------------------

// routeScan is what the source registers: the literal "METHOD /path" patterns, and the call sites whose pattern is not a
// literal of that shape (a mux cannot list its patterns, so the test reads the source).
type routeScan struct {
	patterns []string
	dynamic  []string // "file|argument" of a HandleFunc, Handle or uiRoute call whose first argument is not a METHOD /path literal
}

var methodPath = regexp.MustCompile(`^[A-Z]+ /`)

func scanRoutes(t *testing.T) routeScan {
	t.Helper()
	files, _ := filepath.Glob("*.go")
	seen, dyn := map[string]bool{}, map[string]bool{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok || len(ce.Args) < 2 {
				return true
			}
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle" && sel.Sel.Name != "uiRoute" && sel.Sel.Name != "uiRouteStream") {
				return true
			}
			// Every call of these names is a registration, whatever the receiver is called, except the two Handle methods
			// of other types that are known by name.
			if recv := types.ExprString(sel.X); recv == "h.inner" || recv == "s.mcp.srv" {
				return true
			}
			if vals, ok := evalPattern(f, ce.Args[0]); ok {
				all := true
				for _, v := range vals {
					all = all && methodPath.MatchString(v)
				}
				if all {
					for _, v := range vals {
						seen[v] = true
					}
					return true
				}
			}
			dyn[f+"|"+types.ExprString(ce.Args[0])] = true
			return true
		})
	}
	var out routeScan
	for p := range seen {
		out.patterns = append(out.patterns, p)
	}
	for d := range dyn {
		out.dynamic = append(out.dynamic, d)
	}
	sort.Strings(out.patterns)
	sort.Strings(out.dynamic)
	if len(out.patterns) < 100 {
		t.Fatalf("found only %d route patterns in the source; the scan is broken", len(out.patterns))
	}
	return out
}

// patternIdents are the identifiers a registration builds its pattern from, with every value they take: the research routes
// are registered once for projects and once for Compendiums from the same lines.
var patternIdents = map[string][]string{
	"research.go|page": {"/p/{slug}", "/c/{slug}"}, "research.go|ui": {"/ui/projects/{id}", "/ui/compendiums/{id}"},
	"mcp.go|mcpPath": {"/mcp"}, "pwa.go|offlinePath": {"/offline"},
}

// evalPattern evaluates a pattern expression made of string literals, the identifiers above and +. ok is false for
// anything else.
func evalPattern(file string, e ast.Expr) ([]string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return nil, false
		}
		v, err := strconv.Unquote(x.Value)
		return []string{v}, err == nil
	case *ast.Ident:
		v, ok := patternIdents[file+"|"+x.Name]
		return v, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return nil, false
		}
		l, ok1 := evalPattern(file, x.X)
		r, ok2 := evalPattern(file, x.Y)
		if !ok1 || !ok2 {
			return nil, false
		}
		var out []string
		for _, a := range l {
			for _, b := range r {
				out = append(out, a+b)
			}
		}
		return out, true
	case *ast.ParenExpr:
		return evalPattern(file, x.X)
	}
	return nil, false
}

// routePatterns is every route the server registers with a literal pattern, found in the source.
func routePatterns(t *testing.T) []string { return scanRoutes(t).patterns }

// projectScoped are the address shapes that name a project; everything the server registers must be one of them or
// be listed, exactly, in notProject with its reason. The shapes under /api/v1 are asked by address too, so a route
// written by hand there (the registry does not see it) is closed.
var projectScoped = regexp.MustCompile(`^[A-Z]+ /(?:p/\{slug\}|ui/projects/\{id\}|ui/docs/\{id\}|files/\{id\}|api/v1/projects/\{id\}|api/v1/docs/\{id\})(?:/|$)`)

// routeClass says why a route is not gated by its address, and where the proof is. walk names the sweep that walks
// it (listed means walked): "compendium" in TestGateCompendiumPagesAreSwept, "admin" in TestGateAdminPagesCarryNothingOfAProject.
type routeClass struct{ why, walk string }

// notProject is every route, by its exact pattern, that names no project in its address (or is not a person's page), with
// the reason it is not gated by address. There are no prefixes: a new route, however it is spelt, fails
// TestEveryRouteIsClassifiedForTheGate until it is added here or is under a project address.
var notProject = map[string]routeClass{
	"GET /{$}":      {"the library: lists titles of protected projects, redacted in project.list", ""},
	"GET /search":   {"search across projects: per-project results come from search.query, gated by the registry", ""},
	"GET /settings": {"the person's own", ""}, "GET /settings/ai": {"the person's own", ""}, "GET /settings/mcp": {"the person's own", ""},
	"GET /import": {"an import form: no project", ""}, "GET /import/git": {"an import form: no project", ""},
	"GET /compendiums": {"the library of Compendiums", ""}, "GET /ui/compendiums/new": {"a Compendium form", ""},
	"GET /timeline/events/{id}": {"redirects to the Compendium's event page", ""},
	"GET /ui/csrf":              {"a token", ""}, "GET /ui/projects/new": {"a form for a new project", ""},
	"GET /api/v1/_ops": {"the op schema", ""}, "GET /api/v1/_whoami": {"the caller; names a pinned project only while the role function lets the caller open it", ""},
	"GET /api/v1/events": {"authorizes through project.get, rechecked at every heartbeat and on every event (TestGateStreamEndsOnRelock)", ""},
	"GET /healthz":       {"operations", ""}, "GET /readyz": {"operations", ""}, "GET /static/": {"assets", ""}, "GET /manifest.webmanifest": {"assets", ""}, "GET /sw.js": {"assets", ""},
	"GET /pwa/{name}": {"assets", ""}, "GET /auth/login": {"sign-in", ""}, "GET /auth/oidc/callback": {"sign-in", ""}, "GET /auth/claim-admin": {"sign-in", ""},
	"POST /auth/claim-admin": {"sign-in", ""}, "POST /auth/logout": {"sign-in", ""},
	"POST /ui/md-preview": {"renders text sent in the request, reads nothing", ""}, "POST /ui/projects": {"creates a project", ""},
	"POST /ui/sessions/{sid}/revoke": {"the person's own", ""}, "POST /ui/sessions/revoke-others": {"the person's own", ""},
	"POST /ui/keys": {"the person's own (gated in key.create)", ""}, "POST /ui/keys/{id}/revoke": {"the person's own", ""},
	// the Writing Studio: the person's own numbers, summed by the studio ops over the projects this session may count (a locked
	// project contributes nothing and is not named: TestTheStudioPagesLeaveALockedProjectOut); a form that names a project
	// (a sprint, a goal) goes through a project op or the op's own check, which answers not found for a locked one
	"GET /studio": {"the person's own numbers, from studio.dashboard", ""}, "GET /studio/goals": {"the person's own goals, from studio.goal.list", ""},
	"GET /studio/goals/{id}": {"one of the person's own goals, from studio.goal.history", ""}, "GET /studio/sprints": {"the person's own sprints", ""},
	"GET /studio/data": {"the person's own numbers, from studio.compare", ""}, "GET /studio/card": {"the person's own cards, from studio.card.list", ""},
	"POST /ui/studio/goals": {"studio.goal.create", ""}, "POST /ui/studio/goals/{id}/{action}": {"studio.goal.update and .archive", ""},
	"POST /ui/studio/sprint/{action}": {"studio.sprint.start (a project op: the registry gates it), .stop and .abandon", ""}, "POST /ui/studio/nudges/seen": {"studio.nudge.seen", ""},
	"POST /ui/studio/cards": {"studio.card.create", ""}, "POST /ui/studio/cards/{id}/revoke": {"studio.card.revoke", ""},
	"POST /ui/settings/progress": {"the person's own", ""}, "POST /ui/settings/nudges": {"the person's own", ""},
	"POST /ui/settings/prefs": {"the person's own", ""}, "POST /ui/settings/kindle/{action}": {"the person's own", ""}, "POST /ui/settings/digest": {"the person's own", ""}, "POST /ui/settings/profile": {"the person's own", ""}, "POST /ui/settings/backups": {"administration", "admin"},
	// administration: pages of the instance's operator, none of which shows a project's text; walked as an administrator
	"GET /admin": {"administration", "admin"}, "GET /admin/diagnostics": {"administration", "admin"}, "GET /admin/diagnostics/download": {"administration", "admin"},
	"GET /admin/quotas": {"administration", "admin"}, "GET /admin/users": {"administration", "admin"}, "GET /admin/users/{id}/disable": {"administration", "admin"},
	"POST /ui/admin/users/{action}": {"administration", "admin"},
	"GET /admin/pandoc":             {"administration", "admin"}, "POST /ui/admin/pandoc/{action}": {"administration", "admin"},
	"GET /admin/mail": {"administration", "admin"}, "POST /ui/admin/mail/{action}": {"administration", "admin"},
	// a Compendium's own pages: the Compendium is its own object, and what they show of a book goes through the role function
	"GET /c/{slug}/manage": {"a Compendium page", "compendium"}, "GET /c/{slug}/changes": {"a Compendium page", "compendium"}, "GET /c/{slug}/changes/{cid}": {"a Compendium page", "compendium"},
	"POST /ui/compendiums/{id}/manage/{action}": {"a Compendium form", "compendium"},
	"GET /c/{slug}": {"a Compendium page", "compendium"}, "GET /c/{slug}/e/{id}": {"a Compendium page", "compendium"}, "GET /c/{slug}/e/{id}/merge": {"a Compendium page", "compendium"},
	"GET /c/{slug}/entities": {"a Compendium page", "compendium"}, "GET /c/{slug}/fields": {"a Compendium page", "compendium"}, "GET /c/{slug}/files": {"a Compendium page", "compendium"},
	"GET /c/{slug}/files/{att}": {"a Compendium page", "compendium"}, "GET /c/{slug}/timeline": {"a Compendium page", "compendium"},
	"GET /c/{slug}/timeline/events/{id}": {"a Compendium page", "compendium"}, "GET /c/{slug}/timeline/events/new": {"a Compendium page", "compendium"},
	"GET /ui/compendiums/{id}/files/list": {"a Compendium fragment", "compendium"}, "GET /ui/compendiums/{id}/purge": {"a Compendium fragment", "compendium"},
	"GET /ui/compendiums/{id}/timeline/check": {"a Compendium fragment", "compendium"},
	"POST /c/{slug}/export":                   {"a Compendium form", "compendium"}, "POST /ui/compendiums": {"a Compendium form", "compendium"},
	"POST /ui/compendiums/{id}/{action}": {"a Compendium form", "compendium"}, "POST /ui/compendiums/{id}/entities": {"a Compendium form", "compendium"},
	"POST /ui/compendiums/{id}/fields/{action}": {"a Compendium form", "compendium"}, "POST /ui/compendiums/{id}/files/{att}/{action}": {"a Compendium form", "compendium"},
	"POST /ui/compendiums/{id}/purge": {"a Compendium form", "compendium"}, "POST /ui/compendiums/{id}/timeline/events": {"a Compendium form", "compendium"},
	"POST /ui/entities/{id}/{action}": {"a Compendium form", "compendium"}, "POST /ui/entities/{id}/merge": {"a Compendium form", "compendium"},
	"POST /ui/timeline/events/{id}": {"a Compendium form", "compendium"}, "POST /ui/timeline/events/{id}/restore": {"a Compendium form", "compendium"},
	"POST /ui/timeline/events/{id}/scenes": {"a Compendium form", "compendium"}, "POST /ui/timeline/events/{id}/scenes/unlink": {"a Compendium form", "compendium"},
	"POST /ui/timeline/events/{id}/trash": {"a Compendium form", "compendium"},
	// a Compendium's research and scratchpad (a project's are under /p/{slug} and /ui/projects/{id} and gated by address)
	"GET /c/{slug}/research": {"a Compendium page", "compendium"}, "GET /c/{slug}/research/board": {"a Compendium page", "compendium"},
	"GET /c/{slug}/research/tags": {"a Compendium page", "compendium"}, "GET /c/{slug}/research/{note}": {"a Compendium page", "compendium"},
	"GET /c/{slug}/scratch": {"a Compendium page", "compendium"}, "GET /ui/compendiums/{id}/research/board": {"a Compendium fragment", "compendium"},
	"GET /ui/compendiums/{id}/research/tree": {"a Compendium fragment", "compendium"}, "POST /ui/compendiums/{id}/research/{action}": {"a Compendium form", "compendium"},
	"POST /ui/compendiums/{id}/scratch": {"a Compendium form", "compendium"},
	// desktop mode only (SCRIOBH_DESKTOP=1; not registered otherwise, TestDesktopOffChangesNothing): the launch gate, the first-run
	// profile form (before anyone is signed in: behind the launch cookie, TestDesktopOnlyTheShellsWindowCanSignIn), and the
	// administrator's pages about this computer, none of which shows a project
	"GET /desktop/start": {"desktop: the launch token is exchanged for the launch cookie", ""}, "GET /desktop/profile": {"desktop: the first-run profile form", ""},
	"POST /desktop/profile": {"desktop: makes the one local profile, once", ""}, "GET /desktop/identity": {"desktop administration", ""},
	"GET /desktop/identity/verify":       {"desktop administration: begins a sign-in at the provider being switched to", ""},
	"POST /ui/desktop/identity/{action}": {"desktop administration", ""}, "GET /desktop/local-model": {"desktop administration", ""},
	"POST /ui/desktop/local-model/{action}": {"desktop administration", ""},
	"POST /desktop/save-now":                {"desktop: the shell asks the open writing pages to save before an update; proved by a key from the launch token", ""},
	"GET /desktop/save-status":              {"desktop: how that round stands; the same key", ""},
	"POST /desktop/saved":                   {"desktop: a page says it has saved; the launch cookie and the id its event carried", ""},
	"GET /card/{token}":                     {"a private progress card: numbers only, worked out without a session from projects that have no passphrase at all (TestAProgressCardOverHTTP)", ""},
	"GET /card/{token}/card.svg":            {"a private progress card's picture (the same)", ""}, "GET /card/{token}/card.json": {"a private progress card's numbers (the same)", ""},
	"GET /offline": {"the offline page: no text", ""}, "POST /mcp": {"the key endpoint: every call runs through the registry as the key (TestGateOverMCP)", ""},
}

// notProjectRoute looks a pattern up exactly.
func notProjectRoute(p string) (string, bool) {
	c, ok := notProject[p]
	return c.why, ok
}

// dynamicSites are the registrations whose pattern is not a literal METHOD /path, by file and argument, with why each is
// safe. A new one fails TestEveryRouteIsClassifiedForTheGate.
var dynamicSites = map[string]string{
	"handlers.go|pattern":                     "uiRoute's own wrapper: the patterns passed to it are literals and are scanned",
	"rest.go|e.Op.Method + \" \" + e.Op.Path": "one route per registered op: gated by the registry, proved by TestGateOpSweep",
	"rest.go|\"/api/\"":                       "the 404 and 405 answers for /api/",
	"mcp.go|mcpPath":                          "the MCP endpoint: its calls run through the registry as the key (TestGateOverMCP)",
	"server.go|\"/\"":                         "the 404 page",
	"share.go|p":                              "the public share routes: a separate door with its own link settings, not a person's session",
	"share.go|\"/s/\"":                        "the share routes' 404",
	"card.go|\"/card/\"":                      "the progress cards' 404: the same page as a share link that opens nothing",
	"dev_idp.go|\"/dev-idp/\"":                "the development issuer, absent from production builds",
	"desktop.go|desktopIssuerMount":           "the embedded issuer, mounted in desktop mode only: its own endpoints, none of them a project's (internal/desktopidp)",
}

// TestEveryRouteIsClassifiedForTheGate fails closed: a route whose address names a project is gated by the page gate; any
// other route must be listed by its exact pattern with a reason, and a registration whose pattern is not a literal must
// be listed by call site. Nothing is exempt by prefix.
func TestEveryRouteIsClassifiedForTheGate(t *testing.T) {
	sc := scanRoutes(t)
	for _, p := range sc.patterns {
		if projectScoped.MatchString(p) {
			continue
		}
		if _, ok := notProjectRoute(p); ok {
			continue
		}
		t.Errorf("route %q is not under a project address the page gate matches and is not listed in notProject: say which it is", p)
	}
	for _, d := range sc.dynamic {
		if _, ok := dynamicSites[d]; !ok {
			t.Errorf("a route is registered with a pattern that is not a literal METHOD /path (%s): list the call site in dynamicSites, with why it is safe", d)
		}
	}
	for d := range dynamicSites {
		found := false
		for _, x := range sc.dynamic {
			found = found || x == d
		}
		if !found {
			t.Errorf("dynamicSites lists %s, which is no longer in the source", d)
		}
	}
	for p := range notProject {
		found := false
		for _, x := range sc.patterns {
			found = found || x == p
		}
		if !found {
			t.Errorf("notProject lists %q, which is not registered", p)
		}
	}
}

// pageFill substitutes the parameters of a route pattern.
func (w *gateWorld) pageFill(path, attID string) string {
	out := path
	rep := func(name, v string) { out = strings.ReplaceAll(out, "{"+name+"}", v) }
	switch {
	case strings.HasPrefix(path, "/p/"):
		rep("slug", w.slug)
		rep("id", w.entID)
	case strings.HasPrefix(path, "/ui/docs/"), strings.HasPrefix(path, "/api/v1/docs/"):
		rep("id", w.docID)
	case strings.HasPrefix(path, "/files/"):
		rep("id", attID)
	default:
		rep("id", w.pid)
	}
	rep("docid", w.docID)
	rep("att", attID)
	rep("pid", "psh_00000000000000000000000000")
	rep("action", "trash")
	rep("$", "")
	out = strings.TrimSuffix(out, "$")
	return out
}

func (w *gateWorld) attachmentID(t *testing.T) string {
	t.Helper()
	var l struct{ Items []struct{ ID string } }
	_ = json.Unmarshal(w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/attachments", "").Body.Bytes(), &l)
	if len(l.Items) == 0 {
		t.Fatal("no attachment")
	}
	return l.Items[0].ID
}

func (w *gateWorld) csrf(c caller) string {
	_, info, err := w.s.Sess.Peek(context.Background(), c.cookie.Value)
	if err != nil {
		return ""
	}
	return w.s.Sess.CSRFToken(info.ID)
}

// TestGatePageSweep walks every project-scoped page, fragment and form route as people who have not unlocked the
// project: each must send them to the unlock page or refuse, and none may carry a canary. The owner who unlocked it
// is the control.
func TestGatePageSweep(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	att := w.attachmentID(t)
	hits, walked := 0, 0
	for _, pat := range routePatterns(t) {
		if !projectScoped.MatchString(pat) {
			continue
		}
		method, path, _ := strings.Cut(pat, " ")
		if gateExemptPage.MatchString(w.pageFill(path, att)) {
			continue
		}
		filled := w.pageFill(path, att)
		for _, c := range []caller{w.editor, w.owner2} {
			var rec *httptest.ResponseRecorder
			if method == "GET" {
				rec = s.do(c, "GET", filled, "")
			} else {
				req := httptest.NewRequest(method, filled, strings.NewReader(url.Values{"_csrf": {w.csrf(c)}, "title": {"x"}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.AddCookie(c.cookie)
				rec = httptest.NewRecorder()
				s.Handler().ServeHTTP(rec, req)
			}
			walked++
			body := rec.Body.String()
			switch {
			case rec.Code == http.StatusSeeOther && strings.HasPrefix(rec.Header().Get("Location"), "/p/"+w.slug+"/unlock"):
			case rec.Code == http.StatusForbidden && lockedBody(body):
			default:
				t.Errorf("%s as %s while locked: %d %.200s (Location %q), want the unlock page or a locked refusal", pat, c.name, rec.Code, body, rec.Header().Get("Location"))
			}
			if l := w.leaks(body); len(l) > 0 {
				t.Errorf("%s as %s while locked leaks %v", pat, c.name, l)
			}
		}
		if method == "GET" {
			if rec := s.do(w.owner, "GET", filled, ""); len(w.leaks(rec.Body.String())) > 0 {
				hits++
			}
		}
		// an administrator who is not a member is a stranger to the pages, protected or not
		if method == "GET" {
			if rec := s.do(w.admin, "GET", filled, ""); rec.Code == 200 && len(w.leaks(rec.Body.String())) > 0 {
				t.Errorf("%s as a non-member administrator leaks", pat)
			}
		}
	}
	if walked < 60 || hits < 6 {
		t.Errorf("walked %d locked requests; the control found the canaries on %d pages as the owner", walked, hits)
	}
	// the unlock page itself answers a locked member, and the project's title is allowed on it
	rec := s.do(w.editor, "GET", "/p/"+w.slug+"/unlock", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Gate Book") || len(w.leaks(rec.Body.String())) > 0 {
		t.Errorf("the unlock page: %d %.300s", rec.Code, rec.Body)
	}
}

// TestGateSweepSummary documents what the sweeps walked, so a silent shrink shows up in review.
func TestGateSweepSummary(t *testing.T) {
	n := 0
	for _, p := range routePatterns(t) {
		if projectScoped.MatchString(p) {
			n++
		}
	}
	if n < 40 {
		t.Errorf("only %d project-scoped route patterns found", n)
	}
	t.Log(fmt.Sprintf("%d project-scoped page routes", n))
}

// Some ops name a project in a query or a body, not in their path, and check the role by hand (their auth kind is the
// writer's or the Compendium's, not a project's). They are closed by the role function the gate lives in: a person who has
// not unlocked is never answered with success by any of them, whatever its declared auth. The list is every op found
// with a project field outside its path (a test below fails when one appears that is not here).
func TestGateCoversOpsThatCheckTheRoleByHand(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	evCompendium := w.compID
	cases := []struct{ name, method, path, body string }{
		{"ai.budget.get", "GET", "/api/v1/ai/budget?project_id=" + w.pid, ""},
		{"compendium.create", "POST", "/api/v1/compendiums", `{"name":"Another bible","project_id":"` + w.pid + `"}`},
		{"key.create", "POST", "/api/v1/keys", `{"name":"k","scope":"write","project_id":"` + w.pid + `"}`},
		{"notification.list", "GET", "/api/v1/notifications?project_id=" + w.pid, ""},
		{"timeline.event.list", "GET", "/api/v1/compendiums/" + evCompendium + "/timeline/events?project=" + w.pid, ""},
		{"POST ai run stream", "POST", w.base() + "/ai/run/stream", `{"recipe":"x"}`},
		// the writing studio: a goal on a project, and a comparison of two drafts, answer as for a project that is not there
		{"studio.goal.create", "POST", "/api/v1/me/studio/goals", `{"period":"day","measure":"words","target":5,"project_id":"` + w.pid + `"}`},
		{"studio.compare", "GET", "/api/v1/me/studio/compare?preset=this_week&a_project=" + w.pid, ""},
	}
	for _, c := range cases {
		for _, who := range []caller{w.editor, w.owner2, w.admin} {
			rec := s.do(who, c.method, c.path, c.body)
			body := rec.Body.String()
			if l := w.leaks(body); len(l) > 0 {
				t.Errorf("%s as %s while locked leaks %v: %.200s", c.name, who.name, l, body)
			}
			listLike := c.name == "notification.list" || c.name == "timeline.event.list"
			if !listLike && rec.Code < 300 {
				t.Errorf("%s as %s while locked: %d %.200s, want a refusal", c.name, who.name, rec.Code, body)
			}
		}
	}
	// the ops found by their fields: if a new one names a project outside its path, it must be added above
	known := map[string]bool{"ai.budget.get": true, "compendium.create": true, "key.create": true, "notification.list": true,
		"notification.read_all": true, "timeline.event.list": true, "compendium.purge": true,
		"studio.goal.create": true, "studio.compare": true,
		"studio.card.create": true} // a flag (show_projects), not a project's id
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind == ops.AuthProject {
			continue
		}
		for _, f := range ops.InputFields(e.In) {
			if strings.Contains(strings.ToLower(f.Name), "project") && !known[e.Op.Name] {
				t.Errorf("%s takes a project (%s in the %s) and its auth is not a project's: add it to this test, which says how the gate closes it", e.Op.Name, f.Name, f.Location)
			}
		}
	}
}

// followGet is GET with one redirect followed, as a browser does: a page that only redirects (an event's address) is
// read where it lands.
func (s *Server) followGet(c caller, path string) *httptest.ResponseRecorder {
	rec := s.do(c, "GET", path, "")
	if rec.Code == http.StatusSeeOther {
		if loc := rec.Header().Get("Location"); strings.HasPrefix(loc, "/") {
			return s.do(c, "GET", loc, "")
		}
	}
	return rec
}

// compAttachmentID uploads a small file to the Compendium (as its owner) and returns its id, so the file routes have one to walk.
func (w *gateWorld) compAttachmentID(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for x := 0; x < 4; x++ {
		img.Set(x, x, color.RGBA{10, 10, 200, 255})
	}
	var pb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/compendiums/"+w.compID+"/attachments?filename=bible.png", bytes.NewReader(pb.Bytes()))
	req.AddCookie(w.owner.cookie)
	rec := httptest.NewRecorder()
	w.s.Handler().ServeHTTP(rec, req)
	w.must(t, rec, "compendium attachment")
	return gateID(t, rec)
}

// walkFill fills a Compendium or administration route's parameters with real ids.
func (w *gateWorld) walkFill(path, evID, att, note string) string {
	out := strings.ReplaceAll(path, "{slug}", "gate-bible")
	out = strings.ReplaceAll(out, "{note}", note)
	switch {
	case strings.Contains(path, "/e/{id}") || strings.HasPrefix(path, "/ui/entities/"):
		out = strings.ReplaceAll(out, "{id}", w.entID)
	case strings.Contains(path, "timeline/events/{id}"):
		out = strings.ReplaceAll(out, "{id}", evID)
	case strings.HasPrefix(path, "/admin/users/"):
		out = strings.ReplaceAll(out, "{id}", w.editor.userID)
	default:
		out = strings.ReplaceAll(out, "{id}", w.compID)
	}
	out = strings.ReplaceAll(out, "{att}", att)
	out = strings.ReplaceAll(out, "{action}", "trash")
	return strings.TrimSuffix(out, "$")
}

// walkClass walks every route the table lists under a class (listed means walked), as people who have not unlocked the
// project: nothing of the project may be in any answer. GETs are also read as the owner who has unlocked it, which must
// show the canaries on some of them (a positive control). It returns how many requests it made.
func (w *gateWorld) walkClass(t *testing.T, class string, who []caller, minHits int, wantOK bool) (walked int) {
	t.Helper()
	evID := w.eventID(t)
	att := w.compAttachmentID(t)
	nrec := w.s.do(w.owner, "POST", "/api/v1/compendiums/"+w.compID+"/research", `{"title":"Bible note","body":"Words of the bible."}`)
	w.must(t, nrec, "compendium note")
	note := gateID(t, nrec)
	hits := 0
	for pat, c := range notProject {
		if c.walk != class {
			continue
		}
		method, path, _ := strings.Cut(pat, " ")
		filled := w.walkFill(path, evID, att, note)
		for _, who := range who {
			var rec *httptest.ResponseRecorder
			if method == "GET" {
				rec = w.s.followGet(who, filled)
			} else {
				if who.name == "owner2" {
					continue // a form of a Compendium the owner owns would change it; the strangers' answers are the point
				}
				req := httptest.NewRequest(method, filled, strings.NewReader(url.Values{"_csrf": {w.csrf(who)}, "name": {"x"}, "title": {"x"}}.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.AddCookie(who.cookie)
				rec = httptest.NewRecorder()
				w.s.Handler().ServeHTTP(rec, req)
			}
			walked++
			if wantOK && method == "GET" && rec.Code != http.StatusOK {
				t.Errorf("%s as %s answered %d: the page was not rendered, so there is nothing to say it carries nothing", pat, who.name, rec.Code)
			}
			if l := w.leaks(rec.Body.String()); len(l) > 0 {
				t.Errorf("%s as %s while locked leaks %v", pat, who.name, l)
			}
		}
		if method == "GET" && len(w.leaks(w.s.followGet(w.owner, filled).Body.String())) > 0 {
			hits++
		}
	}
	if hits < minHits {
		t.Errorf("the control found the canaries on %d %s pages as the owner who unlocked it, want at least %d", hits, class, minHits)
	}
	return walked
}

// TestGateCompendiumPagesAreSwept walks every Compendium route in the exact-pattern table, GET and form, as people who have
// not unlocked the project, and again as the owner who has: what the Compendium pages show of the books that use it (the
// books an entity appears in, the scenes that tell an event, counts) must not carry the project's text while it is locked.
func TestGateCompendiumPagesAreSwept(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	if n := w.walkClass(t, "compendium", []caller{w.editor, w.owner2, w.admin}, 2, false); n < 30 {
		t.Errorf("walked only %d requests", n)
	}
}

// TestGateAdminPagesCarryNothingOfAProject is why the administration routes need no gate by address: walked as an
// administrator who is not on the project, and as one who is a member and has not unlocked it, no administration page or form
// shows a word, a title of a document or a count of a protected project.
func TestGateAdminPagesCarryNothingOfAProject(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	s.addMember(t, w.pid, w.admin.userID, ops.ProjectEditor)
	if n := w.walkClass(t, "admin", []caller{w.admin}, 0, true); n < 5 {
		t.Errorf("walked only %d requests", n)
	}
}

// A route written by hand under a project's REST address, which the registry does not see, is closed by its address: this
// one reads the chapter from the database itself, and a locked session gets the refusal, not the text.
func TestGateClosesAHandWrittenRouteUnderAProjectAddress(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	raw := func(rw http.ResponseWriter, r *http.Request) {
		var body string
		_ = s.DB.Read().QueryRow(`SELECT content FROM documents WHERE id = ?`, r.PathValue("id")).Scan(&body)
		_, _ = rw.Write([]byte(body))
	}
	s.mux.HandleFunc("GET /api/v1/docs/{id}/zzraw", raw)
	s.mux.HandleFunc("GET /api/v1/projects/{id}/zzraw", func(rw http.ResponseWriter, r *http.Request) {
		var body string
		_ = s.DB.Read().QueryRow(`SELECT group_concat(title) FROM documents WHERE project_id = ?`, r.PathValue("id")).Scan(&body)
		_, _ = rw.Write([]byte(body))
	})
	for _, p := range []string{"/api/v1/docs/" + w.docID + "/zzraw", w.base() + "/zzraw"} {
		for _, c := range []caller{w.editor, w.owner2} {
			rec := s.do(c, "GET", p, "")
			if rec.Code != http.StatusForbidden || len(w.leaks(rec.Body.String())) > 0 {
				t.Errorf("GET %s as %s while locked: %d %.120s", p, c.name, rec.Code, rec.Body)
			}
		}
		if rec := s.do(w.owner, "GET", p, ""); rec.Code != 200 || len(w.leaks(rec.Body.String())) == 0 {
			t.Errorf("the control: GET %s as the owner who unlocked it: %d", p, rec.Code)
		}
		// a stranger and a key pinned elsewhere learn nothing of the project
		if rec := s.do(w.outsider, "GET", p, ""); rec.Code != 200 && rec.Code != 404 || lockedBody(rec.Body.String()) {
			t.Errorf("a stranger at %s: %d %.120s", p, rec.Code, rec.Body)
		}
	}
	other := s.do(w.owner, "POST", "/api/v1/projects", `{"title":"Elsewhere"}`)
	otherID := gateID(t, other)
	pinned, _ := s.mintKey(t, w.owner, `{"name":"elsewhere","scope":"write","project_id":"`+otherID+`"}`)
	rec := s.doKey(pinned, "GET", "/api/v1/docs/"+w.docID+"/zzraw", "")
	if lockedBody(rec.Body.String()) || strings.Contains(rec.Body.String(), "Gate Book") {
		t.Errorf("a key pinned to another project was told about the protected one: %d %.200s", rec.Code, rec.Body)
	}
}

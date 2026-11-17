package server

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// newAuthzServer builds a server over a temp database for authorization tests.
func newAuthzServer(t *testing.T) *Server {
	t.Helper()
	d := testutil.DB(t)
	// The sweep makes about three requests per op as one outsider; with the
	// default bucket it runs out as the op list grows, and a 429 would be read as
	// a failed authorization. Rate limiting has its own tests.
	s, err := New(Deps{Cfg: testutil.Config(t, map[string]string{"SCRIOBH_RATE_LIMIT": "0"}), DB: d})
	if err != nil {
		t.Fatal(err)
	}
	s.Webhooks.SetSender(offlineSender()) // a sweep never touches the network
	s.Webhooks.SetPingInterval(-1)        // and two callers in a row may both test
	return s
}

// caller is a signed-in test identity with a ready session cookie.
type caller struct {
	name   string
	userID string
	cookie *http.Cookie // nil for anonymous
}

func (s *Server) newCaller(t *testing.T, name, role string) caller {
	t.Helper()
	id := testutil.User(t, s.DB, role, name+"@test.local")
	tok, _, err := s.Sess.Create(context.Background(), id, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return caller{name: name, userID: id, cookie: &http.Cookie{Name: auth.SessionCookie, Value: tok}}
}

func (s *Server) addMember(t *testing.T, projectID, userID, role string) {
	t.Helper()
	err := s.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, projectID, userID, role, db.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// do sends a request to the full handler chain.
func (s *Server) do(c caller, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// fillPath substitutes every {param} in an op path.
func fillPath(path, projectID string) string {
	out := path
	for {
		i := strings.Index(out, "{")
		if i < 0 {
			return out
		}
		j := strings.Index(out[i:], "}")
		id := projectID
		if child, ok := childFor(out[i+1 : i+j]); ok {
			id = child // the second parameter of an op that names a child of its owner
		}
		out = out[:i] + id + out[i+j+1:]
	}
}

// TestAuthzSweep calls every registered op as anonymous, outsider, every
// project role, every instance role and the admin, and checks the result
// against the op's declared Auth. It is how "every op declares its auth and
// is covered by the sweep" is enforced: a new op is swept automatically.
func TestAuthzSweep(t *testing.T) {
	s := newAuthzServer(t)
	anon := caller{name: "anonymous"}
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	reviewer := s.newCaller(t, "reviewer", ops.RoleReader)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	outsiderWriter := s.newCaller(t, "outsider-writer", ops.RoleWriter)
	outsiderReader := s.newCaller(t, "outsider-reader", ops.RoleReader)

	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Sweep"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	dout, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Sweep chapter"})
	if err != nil {
		t.Fatal(err)
	}
	did := dout.(docs.Item).Item.ID
	const bogusDoc = "doc_00000000000000000000000000"
	// Ops that name a document take its id; the rest take the project's.
	idFor := func(e *ops.Entry, project, doc string) string {
		if e.Op.Auth.Resolve != nil {
			return doc
		}
		return project
	}
	s.addMember(t, pid, editor.userID, ops.ProjectEditor)
	s.addMember(t, pid, reviewer.userID, ops.ProjectReviewer)
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	const bogus = "prj_00000000000000000000000000"
	t.Cleanup(func() { sweepChildren = map[string]string{} })

	rank := map[string]int{ops.ProjectReader: 1, ops.ProjectReviewer: 2, ops.ProjectEditor: 3, ops.ProjectOwner: 4}
	projectMembers := map[string]string{owner.name: ops.ProjectOwner, editor.name: ops.ProjectEditor, reviewer.name: ops.ProjectReviewer, reader.name: ops.ProjectReader}
	allCallers := []caller{anon, admin, owner, editor, reviewer, reader, outsiderWriter, outsiderReader}
	instanceRole := map[string]string{admin.name: ops.RoleAdmin, owner.name: ops.RoleWriter, editor.name: ops.RoleWriter, reviewer.name: ops.RoleReader,
		reader.name: ops.RoleReader, outsiderWriter.name: ops.RoleWriter, outsiderReader.name: ops.RoleReader}

	entries := s.Reg.Entries()
	if len(entries) == 0 {
		t.Fatal("no ops registered")
	}
	for _, e := range entries {
		e := e
		if e.Op.Auth.Kind == ops.AuthCompendium {
			continue // swept with Compendium fixtures in TestAuthzSweepCompendium
		}
		t.Run(e.Op.Name, func(t *testing.T) {
			for _, c := range allCallers {
				// project.trash really trashes the project, and a trashed project is
				// the owner's alone; every caller is swept against a live one.
				if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.restore", projects.StateIn{ID: pid}); err != nil {
					t.Fatal(err)
				}
				s.sweepFixtures(t, owner, "project", pid) // a note, a tag and an attachment an op may delete or trash
				path := fillPath(e.Op.Path, idFor(e, pid, did))
				rec := s.do(c, e.Op.Method, path, bodyFor(e))
				got := rec.Code

				// Anonymous is always 401, whatever the op.
				if c.cookie == nil {
					if got != http.StatusUnauthorized {
						t.Errorf("%s as anonymous: %d, want 401", e.Op.Name, got)
					}
					continue
				}
				switch e.Op.Auth.Kind {
				case ops.AuthAuthenticated:
					expectAuthorized(t, e, c, got)
				case ops.AuthWriter:
					if r := instanceRole[c.name]; r == ops.RoleWriter || r == ops.RoleAdmin {
						expectAuthorized(t, e, c, got)
					} else if got != http.StatusForbidden {
						t.Errorf("%s as %s (instance %s): %d, want 403", e.Op.Name, c.name, r, got)
					}
				case ops.AuthAdmin:
					if modeGated(e.Op.Name) {
						// Not this server's mode: nobody is told anything but "not found", the administrator included.
						// TestDesktopOpsAreForTheAdministratorsSession sweeps these in their own mode.
						if got != http.StatusNotFound {
							t.Errorf("%s as %s outside its mode: %d, want 404", e.Op.Name, c.name, got)
						}
					} else if instanceRole[c.name] == ops.RoleAdmin {
						expectAuthorized(t, e, c, got)
					} else if want := map[bool]int{true: http.StatusNotFound, false: http.StatusForbidden}[e.Op.Auth.Hidden]; got != want {
						t.Errorf("%s as %s: %d, want %d", e.Op.Name, c.name, got, want)
					}
				case ops.AuthProject:
					role, member := projectMembers[c.name]
					switch {
					case instanceRole[c.name] == ops.RoleAdmin:
						expectAuthorized(t, e, c, got)
					case !member:
						if got != http.StatusNotFound {
							t.Errorf("%s as %s (no access): %d, want 404 so existence does not leak", e.Op.Name, c.name, got)
						}
					case rank[role] >= rank[e.Op.Auth.Role]:
						expectAuthorized(t, e, c, got)
					default:
						if got != http.StatusForbidden {
							t.Errorf("%s as %s (%s < %s): %d, want 403", e.Op.Name, c.name, role, e.Op.Auth.Role, got)
						}
					}
				}
			}
			if e.Op.Auth.Kind == ops.AuthProject {
				// A project that does not exist looks exactly like one you cannot see.
				real := s.do(outsiderWriter, e.Op.Method, fillPath(e.Op.Path, idFor(e, pid, did)), bodyFor(e))
				fake := s.do(outsiderWriter, e.Op.Method, fillPath(e.Op.Path, idFor(e, bogus, bogusDoc)), bodyFor(e))
				if real.Code != http.StatusNotFound || fake.Code != http.StatusNotFound || real.Body.String() != fake.Body.String() {
					t.Errorf("%s: existing-but-forbidden (%d) and nonexistent (%d) must be indistinguishable", e.Op.Name, real.Code, fake.Code)
				}
			}
		})
	}
}

// modeGated names the ops that exist only in a mode this sweep's server is not in (desktop mode, SCRIOBH_DESKTOP=1):
// they are registered always, so the schema is the same everywhere, and answer "not found" to everyone outside it.
func modeGated(name string) bool { return strings.HasPrefix(name, "desktop.") }

// bodyFor returns a request body for ops that take one. An empty object is
// invalid input, which is fine: authorization runs first, and the sweep only
// distinguishes 401/403/404 from "authorized" (anything else).
func bodyFor(e *ops.Entry) string {
	for _, f := range ops.InputFields(e.In) {
		if f.Location == "body" && e.Op.Method != http.MethodGet {
			return "{}"
		}
	}
	return ""
}

// objectGated names the ops whose handler also checks something about the thing named, not
// only the caller's role: a comment may be reworded only by its author, a thread resolved by an
// editor or by its author, a suggestion withdrawn by its author. A caller whose role is enough
// but who is not that person is answered 403 after the role check passed; the sweep accepts that
// answer for these ops (and only these) because it proves the role check let the call through.
// The rule each one applies is tested in internal/domain/review.
var objectGated = map[string]bool{
	"comment.update": true, "comment.delete": true, "comment.resolve": true, "comment.reopen": true, "comment.reanchor": true,
	"suggestion.withdraw": true,
	// an administrator acts as an owner everywhere else, but only the project's own owner may lock it with a passphrase
	"project.passphrase.set": true,
}

func expectAuthorized(t *testing.T, e *ops.Entry, c caller, got int) {
	t.Helper()
	if got == http.StatusForbidden && objectGated[e.Op.Name] {
		return
	}
	if got == http.StatusUnauthorized || got == http.StatusForbidden || got == http.StatusNotFound || got == http.StatusTooManyRequests || got >= 500 {
		t.Errorf("%s as %s: %d, want the call to be authorized", e.Op.Name, c.name, got)
	}
}

// TestEveryOpDeclaresRoutes checks that REST routes exist for every op, that
// declared UI routes are really registered, and that the schema endpoint
// publishes every op.
func TestEveryOpDeclaresRoutes(t *testing.T) {
	s := newAuthzServer(t)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	rec := s.do(admin, "GET", "/api/v1/_ops", "")
	if rec.Code != 200 {
		t.Fatalf("_ops = %d", rec.Code)
	}
	for _, e := range s.Reg.Entries() {
		if !strings.Contains(rec.Body.String(), `"`+e.Op.Name+`"`) {
			t.Errorf("%s missing from /api/v1/_ops", e.Op.Name)
		}
		if _, pat := s.mux.Handler(httptest.NewRequest(e.Op.Method, fillPath(e.Op.Path, "x"), nil)); pat == "/" || pat == "" {
			t.Errorf("%s has no REST route", e.Op.Name)
		}
		if e.Op.UI != "" {
			method, path, ok := strings.Cut(e.Op.UI, " ")
			if !ok {
				t.Errorf("%s: UI %q must be 'METHOD /path'", e.Op.Name, e.Op.UI)
				continue
			}
			if _, pat := s.mux.Handler(httptest.NewRequest(method, fillPath(path, "x"), nil)); pat == "/" || pat == "" {
				t.Errorf("%s declares UI %q but no route is registered", e.Op.Name, e.Op.UI)
			}
		}
	}
	// The schema is for signed-in callers only.
	if rec := s.do(caller{name: "anonymous"}, "GET", "/api/v1/_ops", ""); rec.Code != 401 {
		t.Errorf("anonymous _ops = %d, want 401", rec.Code)
	}
}

// TestProjectPagesDoNotLeak: opening a project page you cannot see is a 404
// that looks the same as a project that does not exist.
func TestProjectPagesDoNotLeak(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	stranger := s.newCaller(t, "stranger", ops.RoleWriter)
	if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Private Book"}); err != nil {
		t.Fatal(err)
	}
	a := s.do(stranger, "GET", "/p/private-book", "")
	b := s.do(stranger, "GET", "/p/no-such-book", "")
	if a.Code != 404 || b.Code != 404 {
		t.Fatalf("codes %d and %d, want 404 and 404", a.Code, b.Code)
	}
	if strings.Contains(a.Body.String(), "Private Book") {
		t.Error("the 404 page leaks the project title")
	}
	if rec := s.do(owner, "GET", "/p/private-book", ""); rec.Code != 200 {
		t.Errorf("the owner gets %d", rec.Code)
	}
}

// TestAPIShape pins the error and routing behaviour the CLI and the generated
// skill depend on.
func TestAPIShape(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "w", ops.RoleWriter)

	// A wrong method on a known path is 405 with Allow, in the JSON error shape.
	rec := s.do(w, "DELETE", "/api/v1/projects", "")
	if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "GET") || !strings.Contains(rec.Body.String(), `"method_not_allowed"`) {
		t.Errorf("wrong method: %d %q %s", rec.Code, rec.Header().Get("Allow"), rec.Body)
	}
	if rec := s.do(w, "GET", "/api/v1/nothing", ""); rec.Code != 404 {
		t.Errorf("unknown endpoint = %d", rec.Code)
	}
	// Oversize bodies are 413, trailing garbage is refused, unknown fields are refused.
	if rec := s.do(w, "POST", "/api/v1/projects", `{"title":"`+strings.Repeat("a", 3<<20)+`"}`); rec.Code != 413 {
		t.Errorf("oversize body = %d, want 413", rec.Code)
	}
	if rec := s.do(w, "POST", "/api/v1/projects", `{"title":"A"} {"title":"B"}`); rec.Code != 422 {
		t.Errorf("trailing data = %d, want 422", rec.Code)
	}
	if rec := s.do(w, "POST", "/api/v1/projects", `{"title":"A","owner":"x"}`); rec.Code != 422 {
		t.Errorf("unknown field = %d, want 422", rec.Code)
	}
	if rec := s.do(w, "POST", "/api/v1/projects", `{"title":"Fine"}`); rec.Code != 201 {
		t.Errorf("create = %d %s", rec.Code, rec.Body)
	}
	// A cross-site mutation is refused in the JSON shape, not as plain text.
	req := httptest.NewRequest("POST", "/api/v1/projects", strings.NewReader(`{"title":"X"}`))
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(w.cookie)
	r2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(r2, req)
	if r2.Code != 403 || !strings.Contains(r2.Body.String(), `"forbidden"`) {
		t.Errorf("cross-site: %d %s", r2.Code, r2.Body)
	}
}

// TestAPIKeyScopeAndPinning: the registry itself enforces scopes and pins, so
// Phase 3 keys cannot be forgotten at an individual op.
func TestAPIKeyScopeAndPinning(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	uid := testutil.User(t, s.DB, "writer", "k@test.local")
	readKey := ops.Principal{UserID: uid, Role: ops.RoleWriter, Via: "apikey", Scope: ops.ScopeRead}
	if _, err := s.Reg.Call(ctx, readKey, "project.create", projects.CreateIn{Title: "x"}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a read key must not create: %v", err)
	}
	if _, err := s.Reg.Call(ctx, readKey, "project.list", projects.ListIn{}); err != nil {
		t.Errorf("a read key may list: %v", err)
	}
	pinned := ops.Principal{UserID: uid, Role: ops.RoleWriter, Via: "apikey", Scope: ops.ScopeWrite, ProjectID: "prj_x"}
	if _, err := s.Reg.Call(ctx, pinned, "project.list", projects.ListIn{}); ops.AsError(err).Code != ops.CodeForbidden {
		t.Errorf("a project-pinned key must not reach instance-wide ops: %v", err)
	}
}

// TestEveryUIMutationNeedsASessionAndAToken: every declared UI route that
// changes state is registered through uiRoute, so without a session it asks
// for sign-in and without the CSRF token it is refused.
func TestEveryUIMutationNeedsASessionAndAToken(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "w", ops.RoleWriter)
	checked := 0
	for _, e := range s.Reg.Entries() {
		if e.Op.UI == "" {
			continue
		}
		method, path, _ := strings.Cut(e.Op.UI, " ")
		if method == "GET" {
			continue
		}
		checked++
		path = fillPath(path, "x")
		anon := httptest.NewRecorder()
		s.Handler().ServeHTTP(anon, httptest.NewRequest(method, path, strings.NewReader("")))
		if anon.Code != http.StatusSeeOther && anon.Code != http.StatusUnauthorized {
			t.Errorf("%s as anonymous: %d, want a sign-in redirect or 401", e.Op.UI, anon.Code)
		}
		req := httptest.NewRequest(method, path, strings.NewReader("title=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(w.cookie)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s with a session but no CSRF token: %d, want 403", e.Op.UI, rec.Code)
		}
	}
	if checked == 0 {
		t.Error("no UI mutations were checked; project.create declares one")
	}
}

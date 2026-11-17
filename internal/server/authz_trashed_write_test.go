package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// A project in the trash is read-only until it is restored, for everyone who can still reach it (its owner and an
// instance admin): every op that writes into it answers 409 with data.reason "project_trashed". Reads, the ways out
// (restore, purge, trash again, archive), who has access, the exports and the mirror's rebuild still work. The rule is
// applied once, by the registry, from the op's own declaration, so a new op is covered by default; this test calls every
// project op on a trashed project and names the exceptions by hand, so widening the list means changing it here too.
func TestTrashedProjectIsReadOnlyForItsOwnerForEveryWritingOp(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	ownerP := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(ctx, ownerP, "project.create", projects.CreateIn{Title: "Frozen"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	dout, err := s.Reg.Call(ctx, ownerP, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter"})
	if err != nil {
		t.Fatal(err)
	}
	did := dout.(docs.Item).Item.ID
	idFor := func(e *ops.Entry) string {
		if e.Op.Auth.Resolve != nil {
			return did
		}
		return pid
	}
	t.Cleanup(func() { sweepChildren = map[string]string{} })
	setTrash := func(on bool) {
		t.Helper()
		name := "project.restore"
		if on {
			name = "project.trash"
		}
		if _, err := s.Reg.Call(ctx, ownerP, name, projects.StateIn{ID: pid}); err != nil {
			t.Fatal(err)
		}
	}

	// What still works on a trashed project, by name. Everything else that is not a read is refused.
	stillOK := map[string]bool{
		"project.trash": true, "project.restore": true, "project.purge": true, "project.archive": true, "project.unarchive": true,
		"project.member.add": true, "project.member.update": true, "project.member.remove": true,
		"project.open": true, "project.pin": true, // bookkeeping for the person, not the project's content
		"project.unlock": true, "project.lock": true, "project.passphrase.clear": true, // the lock is access, and the way back to a trashed project
		"mirror.rebuild":  true, // the mirror of a trashed project stays, and says so
		"export.markdown": true, "export.html": true, "export.compile": true, "export.cover": true, "export.submission": true,
		"site.build": true, // an export of the site as an archive (publishing it to the folder is refused)
	}
	registered := map[string]bool{}
	for _, e := range s.Reg.Entries() {
		registered[e.Op.Name] = true
	}
	for name := range stillOK {
		if !registered[name] {
			t.Errorf("%s is named as still working on a trashed project but is not a registered op", name)
		}
	}

	refused, worked := 0, 0
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind != ops.AuthProject {
			continue
		}
		e := e
		write := e.Op.Method != http.MethodGet && e.Op.Method != http.MethodHead && e.Op.Scope != ops.ScopeRead
		t.Run(e.Op.Name, func(t *testing.T) {
			for _, c := range []caller{owner, admin} {
				setTrash(false)
				s.sweepFixtures(t, owner, "project", pid)
				setTrash(true)
				rec := s.do(c, e.Op.Method, fillPath(e.Op.Path, idFor(e)), bodyFor(e))
				trashed := rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), `"reason":"project_trashed"`)
				switch {
				case write && !stillOK[e.Op.Name]:
					refused++
					if !trashed || !strings.Contains(rec.Body.String(), "in the trash") {
						t.Errorf("%s as %s on a trashed project: %d %.160s, want 409 with reason project_trashed", e.Op.Name, c.name, rec.Code, rec.Body.String())
					}
				case trashed:
					t.Errorf("%s as %s is refused on a trashed project but must keep working there", e.Op.Name, c.name)
				case !write || stillOK[e.Op.Name]:
					worked++
					if rec.Code == http.StatusNotFound || rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized || rec.Code >= 500 {
						if !(rec.Code == http.StatusForbidden && objectGated[e.Op.Name]) {
							t.Errorf("%s as %s on a trashed project: %d, want the call to be authorized", e.Op.Name, c.name, rec.Code)
						}
					}
				}
			}
		})
	}
	if refused < 100 {
		t.Errorf("only %d writing calls were refused on a trashed project; did the registry's rule stop applying?", refused)
	}
	if worked < 20 {
		t.Errorf("only %d calls were checked for still working", worked)
	}

	// The two named in the report: the owner's own saves, over REST, on their own trashed project.
	setTrash(false)
	d2, err := s.Reg.Call(ctx, ownerP, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Fresh chapter"}) // the sweep trashed the first
	if err != nil {
		t.Fatal(err)
	}
	did = d2.(docs.Item).Item.ID
	rev := func() string {
		var d struct{ Item struct{ Revision int } }
		_ = json.Unmarshal(s.do(owner, "GET", "/api/v1/docs/"+did, "").Body.Bytes(), &d)
		return strconv.Itoa(d.Item.Revision)
	}
	setTrash(true)
	for _, c := range []struct {
		method, path, body string
	}{
		{"PATCH", "/api/v1/docs/" + did, `{"base_revision":` + rev() + `,"content":"typed after the trash"}`},
		{"POST", "/api/v1/projects/" + pid + "/docs", `{"title":"Made after the trash"}`},
	} {
		rec := s.do(owner, c.method, c.path, c.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "project_trashed") {
			t.Errorf("owner %s %s on a trashed project: %d %s", c.method, c.path, rec.Code, rec.Body)
		}
	}
	// compendium.create names the project in its body, where the registry does not look: its own check refuses too.
	if rec := s.do(owner, "POST", "/api/v1/compendiums", `{"name":"Bible for a frozen book","project_id":"`+pid+`"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "project_trashed") {
		t.Errorf("compendium.create with a trashed project_id: %d %s", rec.Code, rec.Body)
	}
	// A dry run is refused the same way: a preview must not promise what the real call would refuse.
	dry := httptest.NewRequest("PATCH", "/api/v1/docs/"+did, strings.NewReader(`{"base_revision":`+rev()+`,"content":"x"}`))
	dry.Header.Set("Content-Type", "application/json")
	dry.Header.Set("Prefer", "dry-run")
	dry.AddCookie(owner.cookie)
	dryRec := httptest.NewRecorder()
	s.Handler().ServeHTTP(dryRec, dry)
	if dryRec.Code != http.StatusConflict || !strings.Contains(dryRec.Body.String(), "project_trashed") {
		t.Errorf("dry run on a trashed project: %d %s", dryRec.Code, dryRec.Body)
	}

	// Reading still works, and the way out works; afterwards the writes do too.
	if rec := s.do(owner, "GET", "/api/v1/docs/"+did, ""); rec.Code != http.StatusOK {
		t.Errorf("owner reads a document of a trashed project: %d", rec.Code)
	}
	if rec := s.do(owner, "POST", "/api/v1/projects/"+pid+"/restore", ""); rec.Code != http.StatusOK {
		t.Fatalf("restore from the trash: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "PATCH", "/api/v1/docs/"+did, `{"base_revision":`+rev()+`,"content":"back again"}`); rec.Code != http.StatusOK {
		t.Errorf("a save after the restore: %d %s", rec.Code, rec.Body)
	}
}

package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// A trashed project belongs to its owner. Every other member gets the answer a
// project that does not exist would give, for every op that names a project or
// one of its documents, so nobody can keep saving, restore a version or import
// into a project its owner has thrown away. The owner (and an instance admin)
// keeps access: restoring and purging need it. What the owner may do there is
// TestTrashedProjectIsReadOnlyForItsOwnerForEveryWritingOp's business: read and get it out, nothing else.
func TestTrashedProjectIsInvisibleToOtherMembersForEveryOp(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	reviewer := s.newCaller(t, "reviewer", ops.RoleReader)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	outsider := s.newCaller(t, "outsider", ops.RoleWriter)
	ownerP := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}

	out, err := s.Reg.Call(ctx, ownerP, "project.create", projects.CreateIn{Title: "Doomed"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	dout, err := s.Reg.Call(ctx, ownerP, "doc.create", docs.CreateIn{ProjectID: pid, Title: "Chapter"})
	if err != nil {
		t.Fatal(err)
	}
	did := dout.(docs.Item).Item.ID
	s.addMember(t, pid, editor.userID, ops.ProjectEditor)
	s.addMember(t, pid, reviewer.userID, ops.ProjectReviewer)
	s.addMember(t, pid, reader.userID, ops.ProjectReader)
	const bogus, bogusDoc = "prj_00000000000000000000000000", "doc_00000000000000000000000000"
	idFor := func(e *ops.Entry, project, doc string) string {
		if e.Op.Auth.Resolve != nil {
			return doc
		}
		return project
	}
	t.Cleanup(func() { sweepChildren = map[string]string{} })
	s.sweepFixtures(t, owner, "project", pid)
	trash := func() {
		if _, err := s.Reg.Call(ctx, ownerP, "project.trash", projects.StateIn{ID: pid}); err != nil {
			t.Fatal(err)
		}
	}
	trash()

	checked := 0
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind != ops.AuthProject {
			continue
		}
		e := e
		t.Run(e.Op.Name, func(t *testing.T) {
			checked++
			path := fillPath(e.Op.Path, idFor(e, pid, did))
			fake := s.do(outsider, e.Op.Method, fillPath(e.Op.Path, idFor(e, bogus, bogusDoc)), bodyFor(e))
			for _, c := range []caller{editor, reviewer, reader, outsider} {
				rec := s.do(c, e.Op.Method, path, bodyFor(e))
				if rec.Code != http.StatusNotFound || fake.Code != http.StatusNotFound || rec.Body.String() != fake.Body.String() {
					t.Errorf("%s as %s on a trashed project: %d %s; want exactly what a nonexistent project answers (%d %s)",
						e.Op.Name, c.name, rec.Code, rec.Body, fake.Code, fake.Body)
				}
			}
			// The owner and an instance admin still reach it (restore and purge need to).
			for _, c := range []caller{owner, admin} {
				// An op may have deleted what it is swept with, and the fixtures are writes: bring the project back for them.
				if _, err := s.Reg.Call(ctx, ownerP, "project.restore", projects.StateIn{ID: pid}); err != nil {
					t.Fatal(err)
				}
				s.sweepFixtures(t, owner, "project", pid)
				trash()
				path = fillPath(e.Op.Path, idFor(e, pid, did))
				rec := s.do(c, e.Op.Method, path, bodyFor(e))
				if rec.Code == http.StatusForbidden && objectGated[e.Op.Name] {
					continue // the role let it through; the op then asks who wrote the thing (see objectGated)
				}
				if rec.Code == http.StatusNotFound || rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized || rec.Code >= 500 {
					t.Errorf("%s as %s on a trashed project: %d, want the call to be authorized", e.Op.Name, c.name, rec.Code)
				}
				trash() // project.restore and friends may have changed it
			}
		})
	}
	if checked == 0 {
		t.Fatal("no project ops were checked")
	}
	// The page for it is gone for the others too, and the owner still has it.
	if rec := s.do(editor, "GET", "/p/doomed", ""); rec.Code != http.StatusNotFound {
		t.Errorf("editor opens a trashed project's page: %d", rec.Code)
	}
	// Archived is not trashed: members keep writing.
	if _, err := s.Reg.Call(ctx, ownerP, "project.restore", projects.StateIn{ID: pid}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reg.Call(ctx, ownerP, "project.archive", projects.StateIn{ID: pid}); err != nil {
		t.Fatal(err)
	}
	if rec := s.do(editor, "POST", "/api/v1/projects/"+pid+"/docs", `{"title":"Still writable"}`); rec.Code != http.StatusCreated {
		t.Errorf("an editor in an archived project: %d %s", rec.Code, rec.Body)
	}
}

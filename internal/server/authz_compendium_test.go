package server

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

func (s *Server) addCompendiumMember(t *testing.T, compendiumID, userID, role string) {
	t.Helper()
	err := s.DB.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, compendiumID, userID, role, db.Now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAuthzSweepCompendium calls every op that requires Compendium membership as
// anonymous, a stranger, every Compendium role, a project member who is not on
// the Compendium, and the admin, and checks the result against the op's
// declared Auth. Like the project sweep, a new op is swept automatically.
func TestAuthzSweepCompendium(t *testing.T) {
	s := newAuthzServer(t)
	ctx := context.Background()
	anon := caller{name: "anonymous"}
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	editor := s.newCaller(t, "editor", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	projectOnly := s.newCaller(t, "project-only", ops.RoleWriter) // an owner of a project that uses the Compendium, not on it
	outsiderWriter := s.newCaller(t, "outsider-writer", ops.RoleWriter)
	outsiderReader := s.newCaller(t, "outsider-reader", ops.RoleReader)

	op := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	out, err := s.Reg.Call(ctx, op, "compendium.create", compendiums.CreateIn{Name: "Sweep"})
	if err != nil {
		t.Fatal(err)
	}
	cid := out.(compendiums.Item).Item.ID
	eout, err := s.Reg.Call(ctx, op, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Sweep Person"})
	if err != nil {
		t.Fatal(err)
	}
	eid := eout.(entities.Item).Item.ID
	// projectOnly is an editor of a project whose primary Compendium is this one,
	// and is not on the Compendium: the sweep proves that being on the project
	// opens none of the Compendium's ops.
	pout, err := s.Reg.Call(ctx, op, "project.create", projects.CreateIn{Title: "Uses the Compendium"})
	if err != nil {
		t.Fatal(err)
	}
	pid := pout.(projects.Item).Item.ID
	if _, err := s.Reg.Call(ctx, op, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid}); err != nil {
		t.Fatal(err)
	}
	s.addMember(t, pid, projectOnly.userID, ops.ProjectEditor)
	s.addCompendiumMember(t, cid, editor.userID, ops.ProjectEditor)
	s.addCompendiumMember(t, cid, reader.userID, ops.ProjectReader)
	const bogusC, bogusE = "cmp_00000000000000000000000000", "ent_00000000000000000000000000"

	roles := map[string]string{owner.name: "owner", editor.name: "editor", reader.name: "reader"}
	rank := map[string]int{"reader": 1, "editor": 2, "owner": 3}
	instance := map[string]string{admin.name: ops.RoleAdmin, owner.name: ops.RoleWriter, editor.name: ops.RoleWriter, reader.name: ops.RoleReader,
		projectOnly.name: ops.RoleWriter, outsiderWriter.name: ops.RoleWriter, outsiderReader.name: ops.RoleReader}
	// An event of the timeline, for the ops that name one.
	evOut, err := s.Reg.Call(ctx, op, "timeline.event.create", timeline.CreateIn{ID: cid, Title: "Sweep event"})
	if err != nil {
		t.Fatal(err)
	}
	evID := evOut.(timeline.EventItem).Item.ID
	const bogusEv = "tle_00000000000000000000000000"
	idFor := func(e *ops.Entry, compendium, entity string) string {
		if e.Op.Auth.Resolve != nil {
			if strings.HasPrefix(e.Op.Path, "/api/v1/timeline/events/") {
				if entity == eid {
					return evID
				}
				return bogusEv
			}
			return entity
		}
		return compendium
	}
	setTrash := func(on bool) {
		t.Helper()
		var v any
		if on {
			v = db.Now()
		}
		if err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE compendiums SET trashed_at = ?, archived_at = NULL WHERE id = ?`, v, cid)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	t.Cleanup(func() { sweepChildren = map[string]string{} })
	for _, e := range s.Reg.Entries() {
		e := e
		if e.Op.Auth.Kind != ops.AuthCompendium {
			continue
		}
		n++
		t.Run(e.Op.Name, func(t *testing.T) {
			setTrash(false) // an op swept earlier (compendium.trash) must not change what the next one meets
			for _, c := range []caller{anon, admin, owner, editor, reader, projectOnly, outsiderWriter, outsiderReader} {
				setTrash(false)
				s.sweepFixtures(t, owner, "compendium", cid)
				rec := s.do(c, e.Op.Method, fillPath(e.Op.Path, idFor(e, cid, eid)), bodyFor(e))
				got := rec.Code
				switch {
				case c.cookie == nil:
					if got != http.StatusUnauthorized {
						t.Errorf("as anonymous: %d, want 401", got)
					}
				case instance[c.name] == ops.RoleAdmin:
					expectAuthorized(t, e, c, got)
				case roles[c.name] == "":
					if got != http.StatusNotFound {
						t.Errorf("as %s (no access): %d, want 404 so existence does not leak", c.name, got)
					}
				case rank[roles[c.name]] >= rank[e.Op.Auth.Role]:
					expectAuthorized(t, e, c, got)
				default:
					if got != http.StatusForbidden {
						t.Errorf("as %s (%s < %s): %d, want 403", c.name, roles[c.name], e.Op.Auth.Role, got)
					}
				}
			}
			setTrash(false)
			real := s.do(outsiderWriter, e.Op.Method, fillPath(e.Op.Path, idFor(e, cid, eid)), bodyFor(e))
			fake := s.do(outsiderWriter, e.Op.Method, fillPath(e.Op.Path, idFor(e, bogusC, bogusE)), bodyFor(e))
			if real.Code != http.StatusNotFound || fake.Code != http.StatusNotFound || real.Body.String() != fake.Body.String() {
				t.Errorf("existing-but-forbidden (%d) and nonexistent (%d) must be indistinguishable", real.Code, fake.Code)
			}
		})
	}
	if n < 20 {
		t.Errorf("only %d Compendium ops were swept; did their Auth kind change?", n)
	}

	// A Compendium in the trash refuses every write that names it with a 409 and a plain message; reads, the ways out
	// (restore, purge, trash again, archive) and membership still answer as they did.
	stillOK := map[string]bool{"compendium.trash": true, "compendium.restore": true, "compendium.archive": true, "compendium.unarchive": true,
		"compendium.purge": true, "compendium.export": true, "mirror.compendium.rebuild": true,
		"compendium.member.add": true, "compendium.member.update": true, "compendium.member.remove": true}
	refused := 0
	for _, e := range s.Reg.Entries() {
		if e.Op.Auth.Kind != ops.AuthCompendium {
			continue
		}
		write := e.Op.Method != "GET" && e.Op.Method != "HEAD"
		setTrash(false)
		s.sweepFixtures(t, owner, "compendium", cid)
		setTrash(true)
		rec := s.do(owner, e.Op.Method, fillPath(e.Op.Path, idFor(e, cid, eid)), bodyFor(e))
		trashMsg := rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "in the trash")
		switch {
		case write && !stillOK[e.Op.Name]:
			refused++
			if !trashMsg {
				t.Errorf("%s on a trashed Compendium: %d %.120s, want 409 \"in the trash\"", e.Op.Name, rec.Code, rec.Body.String())
			}
		case trashMsg:
			t.Errorf("%s is refused on a trashed Compendium but must keep working there", e.Op.Name)
		}
	}
	if refused < 15 {
		t.Errorf("only %d write ops were refused on a trashed Compendium", refused)
	}
	// The ways out work, and after a restore the writes do too.
	if _, err := s.Reg.Call(ctx, op, "compendium.restore", compendiums.StateIn{ID: cid}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := s.Reg.Call(ctx, op, "entity.create", entities.CreateIn{ID: cid, Kind: "place", Name: "After the restore"}); err != nil {
		t.Errorf("a write after the restore: %v", err)
	}
}

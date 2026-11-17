package research_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type env struct {
	t     *testing.T
	reg   *ops.Registry
	owner ops.Principal
	pid   string
	cid   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	d := testutil.DB(t)
	e := &env{t: t, reg: registry.Build(d, events.NewStore(d, nil), t.TempDir())}
	e.owner = ops.Principal{UserID: testutil.User(t, d, ops.RoleWriter, "o@t.local"), Role: ops.RoleWriter}
	e.pid = e.call("project.create", projects.CreateIn{Title: "Notes"}).(projects.Item).Item.ID
	e.cid = e.call("compendium.create", compendiums.CreateIn{Name: "Bible"}).(compendiums.Item).Item.ID
	return e
}

func (e *env) call(op string, in any) any {
	e.t.Helper()
	out, err := e.reg.Call(context.Background(), e.owner, op, in)
	if err != nil {
		e.t.Fatalf("%s: %v", op, err)
	}
	return out
}

// every live note is reachable from the top of the tree.
func (e *env) assertTreeWhole(prefix, id string) []string {
	e.t.Helper()
	out := e.call(prefix+".research.list", research.ListIn{ID: id}).(research.ListOut)
	if len(out.Order) != len(out.Items) {
		e.t.Fatalf("%d live notes but %d reachable from the top: a live note sits under a trashed folder (%v)", len(out.Items), len(out.Order), out.Order)
	}
	return out.Order
}

// Build F > G > K, trash G, trash F, restore K: G and K come back (they were
// one batch) and F stays trashed, so G must surface at the top and K stay under
// it. Every note and folder variant, for a project and for a Compendium.
func TestRestoreNeverLeavesLiveNotesUnderATrashedFolder(t *testing.T) {
	for _, owner := range []string{"project", "compendium"} {
		for _, kKind := range []string{"note", "folder", "clipping"} {
			t.Run(owner+"/"+kKind, func(t *testing.T) {
				e := newEnv(t)
				id := e.pid
				if owner == "compendium" {
					id = e.cid
				}
				mk := func(title, kind, parent string) string {
					return e.call(owner+".research.create", research.CreateIn{ID: id, Title: title, Kind: kind, ParentID: parent}).(research.Item).Item.ID
				}
				f := mk("F", "folder", "")
				g := mk("G", "folder", f)
				k := mk(fmt.Sprintf("K %s", kKind), kKind, g)
				trash := func(n string) { e.call(owner+".research.trash", research.NoteIn{ID: id, NoteID: n}) }
				restore := func(n string) research.TrashOut {
					return e.call(owner+".research.restore", research.NoteIn{ID: id, NoteID: n}).(research.TrashOut)
				}
				trash(g)
				trash(f)
				out := restore(k)
				if out.Count != 2 {
					t.Errorf("restored %d notes, want G and K", out.Count)
				}
				order := e.assertTreeWhole(owner, id)
				if len(order) != 2 || order[0] != g || order[1] != k {
					t.Errorf("order %v, want [G K]", order)
				}
				get := func(n string) research.Note {
					return e.call(owner+".research.get", research.NoteIn{ID: id, NoteID: n}).(research.Item).Item
				}
				if p := get(g).ParentID; p != "" {
					t.Errorf("G is still under the trashed F (%s)", p)
				}
				if p := get(k).ParentID; p != g {
					t.Errorf("K moved out of G: %q", p)
				}
				if !get(f).Trashed {
					t.Error("F came back with something it was not trashed with")
				}
				// Restoring F brings back F alone, at the top, with G still where it is.
				restore(f)
				order = e.assertTreeWhole(owner, id)
				if len(order) != 3 {
					t.Errorf("order after restoring F: %v", order)
				}
				// The same when the named note is the one in the middle.
				trash(k)
				trash(g)
				trash(f)
				restore(g)
				e.assertTreeWhole(owner, id)
				if p := get(g).ParentID; p != "" {
					t.Errorf("G under a trashed folder: %q", p)
				}
			})
		}
	}
}

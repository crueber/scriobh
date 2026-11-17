package compendiums_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

type h struct {
	t                                      *testing.T
	d                                      *db.DB
	reg                                    *ops.Registry
	ctx                                    context.Context
	owner, editor, reader, stranger, admin ops.Principal
}

func newH(t *testing.T) *h {
	d := testutil.DB(t)
	reg := registry.Build(d, events.NewStore(d, nil), t.TempDir())
	person := func(role, email string) ops.Principal {
		return ops.Principal{UserID: testutil.User(t, d, role, email), Role: role, Email: email}
	}
	return &h{t: t, d: d, reg: reg, ctx: context.Background(),
		owner: person(ops.RoleWriter, "owner@t.local"), editor: person(ops.RoleWriter, "editor@t.local"), reader: person(ops.RoleReader, "reader@t.local"),
		stranger: person(ops.RoleWriter, "stranger@t.local"), admin: person(ops.RoleAdmin, "admin@t.local")}
}

func (x *h) call(p ops.Principal, op string, in any) any {
	x.t.Helper()
	out, err := x.reg.Call(x.ctx, p, op, in)
	if err != nil {
		x.t.Fatalf("%s: %v", op, err)
	}
	return out
}

func (x *h) expect(p ops.Principal, op string, in any, code ops.Code) *ops.Error {
	x.t.Helper()
	_, err := x.reg.Call(x.ctx, p, op, in)
	if err == nil {
		x.t.Fatalf("%s: expected %s", op, code)
	}
	e := ops.AsError(err)
	if e.Code != code {
		x.t.Fatalf("%s: got %s (%s), want %s", op, e.Code, e.Message, code)
	}
	return e
}

func (x *h) compendium(p ops.Principal, name string) compendiums.Compendium {
	x.t.Helper()
	return x.call(p, "compendium.create", compendiums.CreateIn{Name: name}).(compendiums.Item).Item
}

func (x *h) project(p ops.Principal, title string) string {
	x.t.Helper()
	return x.call(p, "project.create", projects.CreateIn{Title: title}).(projects.Item).Item.ID
}

func (x *h) exec(q string, args ...any) {
	x.t.Helper()
	if err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		x.t.Fatal(err)
	}
}

func (x *h) count(q string, args ...any) int {
	x.t.Helper()
	var n int
	if err := x.d.Read().QueryRow(q, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return n
}

func (x *h) list(p ops.Principal, state string) []string {
	x.t.Helper()
	var names []string
	for _, c := range x.call(p, "compendium.list", compendiums.ListIn{State: state}).(compendiums.ListOut).Items {
		names = append(names, c.Name)
	}
	return names
}

func (x *h) detail(p ops.Principal, id string) compendiums.Detail {
	x.t.Helper()
	return x.call(p, "compendium.get", compendiums.GetIn{ID: id}).(compendiums.DetailItem).Item
}

func eq(a, b []string) bool { return strings.Join(a, "|") == strings.Join(b, "|") }

func TestCompendiumLifecycle(t *testing.T) {
	x := newH(t)
	pid := x.project(x.owner, "Book")
	c := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "  The Salt Road ", Description: "A world.", ProjectID: pid}).(compendiums.Item).Item
	if c.Name != "The Salt Road" || c.Slug != "the-salt-road" || c.Kind != "fiction" || c.Role != "owner" || c.EntityCount != 0 {
		t.Fatalf("created: %+v", c)
	}
	// A project has at most one primary: a second is refused whole, nothing created.
	x.expect(x.owner, "compendium.create", compendiums.CreateIn{Name: "Second", ProjectID: pid}, ops.CodeConflict)
	if n := x.count(`SELECT count(*) FROM compendiums WHERE name = 'Second'`); n != 0 {
		t.Error("a refused create left a Compendium behind")
	}
	// Only the project's owner can attach one to it; another owner's project is a 404.
	foreign := x.project(x.stranger, "Foreign")
	x.expect(x.owner, "compendium.create", compendiums.CreateIn{Name: "Third", ProjectID: foreign}, ops.CodeNotFound)
	x.exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, foreign, x.owner.UserID, "editor", db.Now())
	x.expect(x.owner, "compendium.create", compendiums.CreateIn{Name: "Third", ProjectID: foreign}, ops.CodeForbidden)
	// A Compendium exists with no project at all; names repeat with a numbered slug.
	free := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "The Salt Road", Kind: "nonfiction"}).(compendiums.Item).Item
	if free.Slug != "the-salt-road-2" || free.Kind != "nonfiction" {
		t.Errorf("%+v", free)
	}
	x.expect(x.owner, "compendium.create", compendiums.CreateIn{Name: ""}, ops.CodeInvalid)
	x.expect(x.owner, "compendium.create", compendiums.CreateIn{Name: "x", Kind: "poetry"}, ops.CodeInvalid)
	x.expect(x.owner, "compendium.create", compendiums.CreateIn{Name: strings.Repeat("a", 201)}, ops.CodeInvalid)
	x.expect(x.reader, "compendium.create", compendiums.CreateIn{Name: "Reader's"}, ops.CodeForbidden)
	// The database holds at most one primary per project by construction, and a pointer to nothing is refused.
	if err := x.d.WithTx(x.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE projects SET compendium_id = 'cmp_missing' WHERE id = ?`, pid)
		return err
	}); err == nil {
		t.Error("a project can point at a Compendium that does not exist")
	}

	// Update: owner only; the slug stays.
	x.exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, c.ID, x.editor.UserID, "editor", db.Now())
	up := x.call(x.owner, "compendium.update", compendiums.UpdateIn{ID: c.ID, Name: ptr("Salt Road Bible"), Description: ptr("")}).(compendiums.Item).Item
	if up.Name != "Salt Road Bible" || up.Slug != "the-salt-road" || up.Description != "" {
		t.Errorf("%+v", up)
	}
	x.expect(x.editor, "compendium.update", compendiums.UpdateIn{ID: c.ID, Name: ptr("Mine")}, ops.CodeForbidden)
	x.expect(x.owner, "compendium.update", compendiums.UpdateIn{ID: c.ID, Name: ptr(" ")}, ops.CodeInvalid)
	x.expect(x.owner, "compendium.update", compendiums.UpdateIn{ID: c.ID, Kind: ptr("verse")}, ops.CodeInvalid)

	// Archive, trash, restore: hidden from the default list, idempotent, and the editor never sees the trash.
	x.call(x.owner, "compendium.archive", compendiums.StateIn{ID: free.ID})
	x.call(x.owner, "compendium.archive", compendiums.StateIn{ID: free.ID})
	if got := x.list(x.owner, ""); !eq(got, []string{"Salt Road Bible"}) {
		t.Errorf("active: %v", got)
	}
	if got := x.list(x.owner, "archived"); !eq(got, []string{"The Salt Road"}) {
		t.Errorf("archived: %v", got)
	}
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: c.ID})
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: c.ID})
	if got := x.list(x.owner, "trashed"); !eq(got, []string{"Salt Road Bible"}) {
		t.Errorf("trashed: %v", got)
	}
	if got := x.list(x.editor, "all"); len(got) != 0 {
		t.Errorf("a member sees the owner's trash: %v", got)
	}
	if got := x.list(x.owner, "all"); len(got) != 2 {
		t.Errorf("all: %v", got)
	}
	x.call(x.owner, "compendium.restore", compendiums.StateIn{ID: c.ID})
	x.call(x.owner, "compendium.unarchive", compendiums.StateIn{ID: free.ID})
	if got := x.list(x.editor, ""); !eq(got, []string{"Salt Road Bible"}) {
		t.Errorf("the editor's list: %v", got)
	}
	if got := x.list(x.stranger, ""); len(got) != 0 {
		t.Errorf("a stranger's list: %v", got)
	}
	// A trashed Compendium can still be opened by its members (to restore it).
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: c.ID})
	if d := x.detail(x.owner, c.ID); !d.Trashed {
		t.Error("a trashed Compendium does not say so")
	}
	x.call(x.owner, "compendium.restore", compendiums.StateIn{ID: c.ID})
	x.expect(x.stranger, "compendium.get", compendiums.GetIn{ID: c.ID}, ops.CodeNotFound)
	// An instance admin acts as owner, but only sees their own in lists.
	if d := x.detail(x.admin, c.ID); d.Role != "owner" {
		t.Errorf("admin role: %s", d.Role)
	}
	if got := x.list(x.admin, "all"); len(got) != 0 {
		t.Errorf("an admin's list shows what is not theirs: %v", got)
	}
}

func ptr[T any](v T) *T { return &v }

func TestCompendiumDetailNamesOnlyProjectsTheCallerCanOpen(t *testing.T) {
	x := newH(t)
	c := x.compendium(x.owner, "World")
	mine := x.project(x.owner, "Mine")
	theirs := x.project(x.stranger, "Theirs")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: mine, CompendiumID: c.ID})
	// The stranger's project references it: they must be able to read it to do so.
	x.exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, c.ID, x.stranger.UserID, "reader", db.Now())
	x.call(x.stranger, "project.compendium.ref.add", compendiums.RefAddIn{ID: theirs, CompendiumID: c.ID})
	x.exec(`DELETE FROM compendium_members WHERE compendium_id = ? AND user_id = ?`, c.ID, x.stranger.UserID)
	d := x.detail(x.owner, c.ID)
	if len(d.Projects) != 1 || d.Projects[0].Title != "Mine" || d.Projects[0].Relation != "primary" || d.HiddenProjects != 1 || d.MemberCount != 1 {
		t.Errorf("detail: %+v", d)
	}
	// Purge needs both the named project and the hidden count.
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: c.ID})
	e := x.expect(x.owner, "compendium.purge", compendiums.PurgeIn{ID: c.ID, ConfirmName: "World", DetachProjects: []string{mine}}, ops.CodeConflict)
	if e.Data["hidden_projects"] != 1 {
		t.Errorf("the refusal does not report the hidden projects: %+v", e.Data)
	}
	out := x.call(x.owner, "compendium.purge", compendiums.PurgeIn{ID: c.ID, ConfirmName: "World", DetachProjects: []string{mine}, HiddenProjects: 1}).(compendiums.PurgeOut)
	if !out.Purged || out.HiddenProjects != 1 || len(out.DetachedProjects) != 1 {
		t.Errorf("%+v", out)
	}
	// The other person's project lost its reference too.
	if n := x.count(`SELECT count(*) FROM compendium_refs`); n != 0 {
		t.Errorf("%d references remain", n)
	}
	// The primary pointer is cleared in the database.
	if n := x.count(`SELECT count(*) FROM projects WHERE compendium_id IS NOT NULL`); n != 0 {
		t.Errorf("%d projects still point at it", n)
	}
}

func TestCompendiumMembers(t *testing.T) {
	x := newH(t)
	c := x.compendium(x.owner, "World")
	add := func(email, role string) (compendiums.MemberItem, *ops.Error) {
		out, err := x.reg.Call(x.ctx, x.owner, "compendium.member.add", compendiums.MemberAddIn{ID: c.ID, Email: email, Role: role})
		if err != nil {
			return compendiums.MemberItem{}, ops.AsError(err)
		}
		return out.(compendiums.MemberItem), nil
	}
	// Found by exact email, ignoring case.
	m, err := add(" EDITOR@t.local ", "editor")
	if err != nil || m.Item.UserID != x.editor.UserID || m.Item.Role != "editor" || m.Item.Email != "editor@t.local" {
		t.Fatalf("add: %+v %v", m, err)
	}
	// Every way an address can fail to be addable looks the same.
	agent := testutil.User(t, x.d, ops.RoleWriter, "agent@t.local")
	x.exec(`UPDATE users SET is_agent = 1 WHERE id = ?`, agent)
	gone := testutil.User(t, x.d, ops.RoleWriter, "gone@t.local")
	x.exec(`UPDATE users SET deleted_at = ? WHERE id = ?`, db.Now(), gone)
	var bodies []string
	for _, tc := range []struct{ email, role string }{
		{"nobody@t.local", "reader"}, {"agent@t.local", "reader"}, {"gone@t.local", "reader"}, {"reader@t.local", "editor"}, {"not an address", "reader"}, {"edit", "reader"},
	} {
		_, e := add(tc.email, tc.role)
		if e == nil || e.Code != ops.CodeNotFound {
			t.Fatalf("%s as %s: %+v", tc.email, tc.role, e)
		}
		bodies = append(bodies, e.Message)
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Errorf("a refusal differs from the others: %q vs %q", b, bodies[0])
		}
	}
	// An instance reader can be a reader. Already a member is a conflict, not a change.
	if _, e := add("reader@t.local", "reader"); e != nil {
		t.Fatal(e)
	}
	if _, e := add("reader@t.local", "reader"); e == nil || e.Code != ops.CodeConflict {
		t.Errorf("adding twice: %+v", e)
	}
	// What the member can do follows the role.
	x.call(x.reader, "compendium.get", compendiums.GetIn{ID: c.ID})
	x.expect(x.reader, "compendium.member.list", compendiums.MemberListIn{ID: c.ID}, ops.CodeForbidden)
	x.expect(x.editor, "compendium.member.add", compendiums.MemberAddIn{ID: c.ID, Email: "stranger@t.local", Role: "reader"}, ops.CodeForbidden)
	list := x.call(x.owner, "compendium.member.list", compendiums.MemberListIn{ID: c.ID}).(compendiums.MemberListOut).Items
	if len(list) != 3 || list[0].Role != "owner" {
		t.Errorf("members: %+v", list)
	}
	// Roles change; the owner's does not; an instance reader cannot be made an editor.
	up := x.call(x.owner, "compendium.member.update", compendiums.MemberUpdateIn{ID: c.ID, UserID: x.editor.UserID, Role: "reader"}).(compendiums.MemberItem).Item
	if up.Role != "reader" {
		t.Errorf("%+v", up)
	}
	x.expect(x.owner, "compendium.member.update", compendiums.MemberUpdateIn{ID: c.ID, UserID: x.owner.UserID, Role: "reader"}, ops.CodeConflict)
	x.expect(x.owner, "compendium.member.update", compendiums.MemberUpdateIn{ID: c.ID, UserID: x.reader.UserID, Role: "editor"}, ops.CodeInvalid)
	x.expect(x.owner, "compendium.member.update", compendiums.MemberUpdateIn{ID: c.ID, UserID: x.stranger.UserID, Role: "reader"}, ops.CodeNotFound)
	x.expect(x.editor, "entity.create", entities.CreateIn{ID: c.ID, Kind: "item", Name: "Nope"}, ops.CodeForbidden)
	// Removal.
	x.expect(x.owner, "compendium.member.remove", compendiums.MemberRemoveIn{ID: c.ID, UserID: x.owner.UserID}, ops.CodeConflict)
	if rm := x.call(x.owner, "compendium.member.remove", compendiums.MemberRemoveIn{ID: c.ID, UserID: x.editor.UserID}).(compendiums.RemoveOut); !rm.Removed {
		t.Error("not removed")
	}
	x.expect(x.editor, "compendium.get", compendiums.GetIn{ID: c.ID}, ops.CodeNotFound)
	x.expect(x.owner, "compendium.member.remove", compendiums.MemberRemoveIn{ID: c.ID, UserID: x.editor.UserID}, ops.CodeNotFound)
	// Audit lines carry ids and roles, never email addresses.
	rows, err2 := x.d.Read().Query(`SELECT detail_json FROM audit_log WHERE action LIKE 'compendium.member.%'`)
	if err2 != nil {
		t.Fatal(err2)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		n++
		if strings.Contains(d, "@") {
			t.Errorf("an audit line holds an address: %s", d)
		}
	}
	if n != 4 {
		t.Errorf("%d member audit lines, want 4", n)
	}
}

func TestProjectCompendiumSet(t *testing.T) {
	x := newH(t)
	a, b := x.compendium(x.owner, "A"), x.compendium(x.owner, "B")
	pid := x.project(x.owner, "Book")
	ea := x.call(x.owner, "entity.create", entities.CreateIn{ID: a.ID, Kind: "character", Name: "Ada"}).(entities.Item).Item
	x.call(x.owner, "entity.create", entities.CreateIn{ID: a.ID, Kind: "character", Name: "Bea"})
	x.call(x.owner, "entity.create", entities.CreateIn{ID: b.ID, Kind: "place", Name: "Quay"})
	set := func(cid string, dry bool) compendiums.SetOut {
		return x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid, DryRun: dry}).(compendiums.SetOut)
	}
	d := set(a.ID, true)
	if !d.DryRun || !d.Changed || d.EntitiesEntering != 2 || d.EntitiesLeaving != 0 {
		t.Errorf("dry run: %+v", d)
	}
	if n := x.count(`SELECT count(*) FROM projects WHERE compendium_id IS NOT NULL`); n != 0 {
		t.Fatal("a dry run changed the project")
	}
	r := set(a.ID, false)
	r.DryRun = true
	if r != d {
		t.Errorf("the real set differs from the dry run:\n%+v\n%+v", r, d)
	}
	if again := set(a.ID, false); again.Changed {
		t.Error("setting the same primary changed something")
	}
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ea.ID, Name: ptr("Ada Quill")})
	// Switching away: entities leave, the override goes dormant (kept), nothing is deleted.
	sw := set(b.ID, true)
	if sw.EntitiesEntering != 1 || sw.EntitiesLeaving != 2 || sw.OverridesDormant != 1 || sw.OverridesKept != 0 {
		t.Errorf("switch preview: %+v", sw)
	}
	set(b.ID, false)
	if n := x.count(`SELECT count(*) FROM project_entity_overrides`); n != 1 {
		t.Error("the override was deleted")
	}
	if got := x.call(x.owner, "project.entity.list", entities.ProjectListIn{ID: pid}).(entities.ProjectListOut); got.Total != 1 || got.Items[0].Name != "Quay" {
		t.Errorf("after the switch: %+v", got)
	}
	// Referencing A keeps its override applying; making a referenced Compendium primary drops the reference.
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: a.ID})
	if got := x.call(x.owner, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: ea.ID}).(entities.ProjectItem).Item; got.Name != "Ada Quill" || got.Via != "reference" {
		t.Errorf("dormant override did not come back: %+v", got)
	}
	p := set(a.ID, true)
	if !p.ReferenceDropped || p.OverridesKept != 1 {
		t.Errorf("promote: %+v", p)
	}
	set(a.ID, false)
	if n := x.count(`SELECT count(*) FROM compendium_refs`); n != 0 {
		t.Error("the redundant reference remains")
	}
	// Clearing.
	if c := set("", false); !c.Changed || c.PreviousCompendiumID != a.ID {
		t.Errorf("clear: %+v", c)
	}
	// Authority: the project's owner who is only a reader of the Compendium cannot make it primary, but can reference it;
	// a Compendium one cannot see is the same 404 as one that does not exist.
	x.exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`, a.ID, x.stranger.UserID, "reader", db.Now())
	sp := x.project(x.stranger, "Theirs")
	x.expect(x.stranger, "project.compendium.set", compendiums.SetIn{ID: sp, CompendiumID: a.ID}, ops.CodeForbidden)
	x.call(x.stranger, "project.compendium.ref.add", compendiums.RefAddIn{ID: sp, CompendiumID: a.ID})
	real := x.expect(x.stranger, "project.compendium.ref.add", compendiums.RefAddIn{ID: sp, CompendiumID: b.ID}, ops.CodeNotFound)
	fake := x.expect(x.stranger, "project.compendium.ref.add", compendiums.RefAddIn{ID: sp, CompendiumID: "cmp_nothing"}, ops.CodeNotFound)
	if real.Message != fake.Message {
		t.Errorf("an unseen Compendium and a missing one answer differently: %q vs %q", real.Message, fake.Message)
	}
	if again := x.call(x.stranger, "project.compendium.ref.add", compendiums.RefAddIn{ID: sp, CompendiumID: a.ID}).(compendiums.RefItem); again.Created {
		t.Error("a reference was created twice")
	}
	// Compendium to Compendium references: read access on the target, no self reference, one level only.
	x.expect(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: a.ID, TargetID: a.ID}, ops.CodeInvalid)
	x.call(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: a.ID, TargetID: b.ID})
	dd := x.detail(x.owner, a.ID)
	if len(dd.References) != 1 || dd.References[0].Name != "B" || x.detail(x.owner, b.ID).ReferencedBy != 1 {
		t.Errorf("references: %+v", dd.References)
	}
	if rm := x.call(x.owner, "compendium.ref.remove", compendiums.CompendiumRefRemoveIn{ID: a.ID, TargetID: b.ID}).(compendiums.RefRemoveOut); !rm.Removed {
		t.Error("not removed")
	}
	// What a project member who is not on the Compendium may see of the wiring.
	x.exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, pid, x.editor.UserID, "editor", db.Now())
	set(a.ID, false)
	w := x.call(x.editor, "project.compendium.get", compendiums.ProjectIn{ID: pid}).(compendiums.WiringItem).Item
	if w.Primary != nil || !w.HasPrimary || w.Hidden != 1 {
		t.Errorf("wiring: %+v", w)
	}
}

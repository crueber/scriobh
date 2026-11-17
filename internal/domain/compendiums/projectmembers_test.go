package compendiums_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

func (x *h) addProjectMember(p ops.Principal, pid, email, role string, ask *bool) projects.MemberAddOut {
	x.t.Helper()
	return x.call(p, "project.member.add", projects.MemberAddIn{ID: pid, Email: email, Role: role, AddToCompendium: ask}).(projects.MemberAddOut)
}

func (x *h) compendiumRole(cid string, p ops.Principal) string {
	x.t.Helper()
	var role string
	err := x.d.Read().QueryRow(`SELECT COALESCE((SELECT role FROM compendium_members WHERE compendium_id = ? AND user_id = ?), '')`, cid, p.UserID).Scan(&role)
	if err != nil {
		x.t.Fatal(err)
	}
	return role
}

// The collaborator prompt: adding a project editor offers the primary
// Compendium too (default yes), a reviewer or reader does not (default no),
// nothing is granted silently, and the result says exactly what was done.
func TestAddingACollaboratorAndTheCompendiumPrompt(t *testing.T) {
	x := newH(t)
	c := x.compendium(x.owner, "World")
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: c.ID})
	yes, no := true, false

	// An editor, with the default: asked, granted as an editor on the Compendium.
	e := x.addProjectMember(x.owner, pid, "editor@t.local", "editor", nil)
	if e.Item.Role != "editor" || !e.Compendium.Requested || !e.Compendium.Granted || e.Compendium.Role != "editor" || e.Compendium.CompendiumID != c.ID || e.Compendium.Reason != "" {
		t.Errorf("editor, default: %+v", e)
	}
	if x.compendiumRole(c.ID, x.editor) != "editor" {
		t.Error("the editor was not given the Compendium")
	}
	// A reviewer, default: nothing is granted, and it says so.
	x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.editor.UserID})
	x.call(x.owner, "compendium.member.remove", compendiums.MemberRemoveIn{ID: c.ID, UserID: x.editor.UserID})
	r := x.addProjectMember(x.owner, pid, "editor@t.local", "reviewer", nil)
	if r.Compendium.Requested || r.Compendium.Granted || r.Compendium.Reason != "not_requested" || r.Compendium.Role != "" || x.compendiumRole(c.ID, x.editor) != "" {
		t.Errorf("reviewer, default: %+v", r)
	}
	// Explicit no for an editor is respected.
	x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.editor.UserID})
	n := x.addProjectMember(x.owner, pid, "editor@t.local", "editor", &no)
	if n.Compendium.Requested || n.Compendium.Granted || x.compendiumRole(c.ID, x.editor) != "" {
		t.Errorf("editor, declined: %+v", n)
	}
	// A reader asked for explicitly gets a reader's access, not more.
	x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.editor.UserID})
	rd := x.addProjectMember(x.owner, pid, "reader@t.local", "reader", &yes)
	if !rd.Compendium.Granted || rd.Compendium.Role != "reader" || x.compendiumRole(c.ID, x.reader) != "reader" {
		t.Errorf("reader, asked: %+v", rd)
	}
	// An existing Compendium member is never downgraded or re-granted.
	x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.reader.UserID})
	x.call(x.owner, "compendium.member.update", compendiums.MemberUpdateIn{ID: c.ID, UserID: x.reader.UserID, Role: "reader"})
	x.call(x.owner, "compendium.member.remove", compendiums.MemberRemoveIn{ID: c.ID, UserID: x.reader.UserID})
	x.call(x.owner, "compendium.member.add", compendiums.MemberAddIn{ID: c.ID, Email: "stranger@t.local", Role: "editor"})
	pre := x.addProjectMember(x.owner, pid, "stranger@t.local", "reader", &yes)
	if pre.Compendium.Granted || pre.Compendium.Reason != "already_member" || pre.Compendium.Role != "editor" || x.compendiumRole(c.ID, x.stranger) != "editor" {
		t.Errorf("an existing editor asked as a reader: %+v", pre)
	}
	// A project with no primary Compendium skips it, whatever was asked.
	bare := x.project(x.owner, "Bare")
	b := x.addProjectMember(x.owner, bare, "editor@t.local", "editor", &yes)
	if !b.Compendium.Requested || b.Compendium.Granted || b.Compendium.Reason != "no_primary_compendium" || b.Compendium.CompendiumID != "" {
		t.Errorf("no primary: %+v", b)
	}
	// An owner of the project who is not the Compendium's owner cannot grant it:
	// the default skips it and says why; asking explicitly is refused before anything is written.
	other := x.project(x.stranger, "Theirs")
	x.call(x.stranger, "project.compendium.set", compendiums.SetIn{ID: other, CompendiumID: c.ID}) // stranger is an editor of c
	sk := x.addProjectMember(x.stranger, other, "owner@t.local", "editor", nil)
	if !sk.Compendium.Requested || sk.Compendium.Granted || sk.Compendium.Reason != "not_compendium_owner" || sk.Item.Role != "editor" {
		t.Errorf("not the Compendium's owner: %+v", sk)
	}
	x.call(x.stranger, "project.member.remove", projects.MemberRemoveIn{ID: other, UserID: x.owner.UserID})
	if x.count(`SELECT count(*) FROM project_members WHERE project_id = ?`, other) != 1 {
		t.Fatal("setup")
	}
	before := x.count(`SELECT count(*) FROM compendium_members`)
	x.expect(x.stranger, "project.member.add", projects.MemberAddIn{ID: other, Email: "owner@t.local", Role: "editor", AddToCompendium: &yes}, ops.CodeForbidden)
	if x.count(`SELECT count(*) FROM project_members WHERE project_id = ?`, other) != 1 || x.count(`SELECT count(*) FROM compendium_members`) != before {
		t.Error("a refused add left something behind")
	}
	// The audit trail records both doors.
	if n := x.count(`SELECT count(*) FROM audit_log WHERE action = 'compendium.member.add' AND detail_json LIKE '%project.member.add%'`); n < 2 {
		t.Errorf("%d audit lines for Compendium grants through a project", n)
	}
	if n := x.count(`SELECT count(*) FROM audit_log WHERE action = 'project.member.add'`); n < 5 {
		t.Errorf("%d audit lines for project adds", n)
	}
}

func TestProjectMemberOps(t *testing.T) {
	x := newH(t)
	pid := x.project(x.owner, "Book")
	// Owner only.
	x.expect(x.stranger, "project.member.list", projects.MemberListIn{ID: pid}, ops.CodeNotFound)
	x.addProjectMember(x.owner, pid, "editor@t.local", "editor", nil)
	x.expect(x.editor, "project.member.list", projects.MemberListIn{ID: pid}, ops.CodeForbidden)
	x.expect(x.editor, "project.member.add", projects.MemberAddIn{ID: pid, Email: "reader@t.local", Role: "reader"}, ops.CodeForbidden)
	// An unknown address and a reader being made an editor answer the same.
	_, e1 := x.reg.Call(x.ctx, x.owner, "project.member.add", projects.MemberAddIn{ID: pid, Email: "nobody@t.local", Role: "reader"})
	_, e2 := x.reg.Call(x.ctx, x.owner, "project.member.add", projects.MemberAddIn{ID: pid, Email: "reader@t.local", Role: "editor"})
	if ops.AsError(e1).Code != ops.CodeNotFound || ops.AsError(e1).Message != ops.AsError(e2).Message {
		t.Errorf("%v / %v", e1, e2)
	}
	x.expect(x.owner, "project.member.add", projects.MemberAddIn{ID: pid, Email: "editor@t.local", Role: "reader"}, ops.CodeConflict)
	x.expect(x.owner, "project.member.add", projects.MemberAddIn{ID: pid, Email: "reader@t.local", Role: "owner"}, ops.CodeInvalid)
	x.addProjectMember(x.owner, pid, "reader@t.local", "reviewer", nil)
	list := x.call(x.owner, "project.member.list", projects.MemberListIn{ID: pid}).(projects.MemberListOut).Items
	if len(list) != 3 || list[0].Role != "owner" {
		t.Errorf("members: %+v", list)
	}
	// Roles change, the owner's does not, and an instance reader cannot become an editor.
	up := x.call(x.owner, "project.member.update", projects.MemberUpdateIn{ID: pid, UserID: x.editor.UserID, Role: "reader"}).(projects.MemberItem).Item
	if up.Role != "reader" {
		t.Errorf("%+v", up)
	}
	x.expect(x.owner, "project.member.update", projects.MemberUpdateIn{ID: pid, UserID: x.owner.UserID, Role: "reader"}, ops.CodeConflict)
	x.expect(x.owner, "project.member.update", projects.MemberUpdateIn{ID: pid, UserID: x.reader.UserID, Role: "editor"}, ops.CodeInvalid)
	x.expect(x.owner, "project.member.update", projects.MemberUpdateIn{ID: pid, UserID: x.stranger.UserID, Role: "reader"}, ops.CodeNotFound)
	// A removed member loses the project and their per-person state; the owner cannot be removed.
	x.expect(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.owner.UserID}, ops.CodeConflict)
	rm := x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.editor.UserID}).(projects.MemberRemoveOut)
	if !rm.Removed || rm.CompendiumRole != "" {
		t.Errorf("%+v", rm)
	}
	x.expect(x.editor, "project.get", projects.GetIn{ID: pid}, ops.CodeNotFound)
	// Removing someone from the project leaves their Compendium access, and says so.
	c := x.compendium(x.owner, "World")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: c.ID})
	x.addProjectMember(x.owner, pid, "editor@t.local", "editor", nil)
	lm := x.call(x.owner, "project.member.list", projects.MemberListIn{ID: pid}).(projects.MemberListOut).Items
	var seen string
	for _, m := range lm {
		if m.UserID == x.editor.UserID {
			seen = m.CompendiumRole
		}
	}
	if seen != "editor" {
		t.Errorf("the member list does not show the Compendium role: %+v", lm)
	}
	rm = x.call(x.owner, "project.member.remove", projects.MemberRemoveIn{ID: pid, UserID: x.editor.UserID}).(projects.MemberRemoveOut)
	if rm.CompendiumRole != "editor" {
		t.Errorf("removal does not say the Compendium access remains: %+v", rm)
	}
	if got := x.call(x.editor, "entity.list", entities.ListIn{ID: c.ID}).(entities.ListOut); got.Total != 0 {
		t.Errorf("%+v", got)
	}
	// Audit lines carry ids and roles, never addresses.
	rows, err := x.d.Read().Query(`SELECT detail_json FROM audit_log WHERE action LIKE 'project.member.%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		if strings.Contains(d, "@") {
			t.Errorf("an audit line holds an address: %s", d)
		}
	}
}

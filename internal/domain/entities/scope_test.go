package entities_test

import (
	"reflect"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// An override is the book's own row. Setting, changing and clearing it never
// writes the Compendium's record, and other books are unaffected.
func TestOverrideNeverWritesTheCompendiumRecord(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	ada := x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Ada", Summary: "A cartographer.", Body: "Body text.",
		Fields: map[string]any{"age": "30", "role": "lead"}, Aliases: []string{"Addie"}}).(entities.Item).Item
	p1, p2 := x.project(x.owner, "Book One"), x.project(x.owner, "Book Two")
	for _, p := range []string{p1, p2} {
		x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: p, CompendiumID: cid})
	}
	recordBefore := x.dump()
	var rowBefore string
	if err := x.d.Read().QueryRow(`SELECT name||'|'||summary||'|'||body||'|'||fields_json||'|'||revision||'|'||updated_at FROM entities WHERE id = ?`, ada.ID).Scan(&rowBefore); err != nil {
		t.Fatal(err)
	}

	set := x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: p1, EntityID: ada.ID, Name: str("Ada Quill"), Summary: str("A mapmaker in 1998."),
		Fields: map[string]any{"age": "31", "role": ""}, Notes: str("Younger here.")}).(entities.OverrideOut).Item
	if set.Name != "Ada Quill" || set.Summary != "A mapmaker in 1998." || set.Fields["age"] != "31" || !set.Overridden || set.Editable != true {
		t.Fatalf("effective: %+v", set)
	}
	if _, blank := set.Fields["role"]; blank {
		t.Error("an empty override value must blank the field in the effective view")
	}
	if set.Base == nil || set.Base.Name != "Ada" || set.Base.Fields["age"] != "30" || set.Override == nil || set.Override.Notes != "Younger here." || set.Body != "Body text." {
		t.Errorf("base and override: %+v", set)
	}
	// The record is byte for byte what it was.
	var rowAfter string
	if err := x.d.Read().QueryRow(`SELECT name||'|'||summary||'|'||body||'|'||fields_json||'|'||revision||'|'||updated_at FROM entities WHERE id = ?`, ada.ID).Scan(&rowAfter); err != nil {
		t.Fatal(err)
	}
	if rowAfter != rowBefore {
		t.Errorf("the override changed the Compendium record:\n%s\n%s", rowBefore, rowAfter)
	}
	_ = recordBefore
	// The other book sees the record.
	two := x.call(x.owner, "project.entity.get", entities.ProjectGetIn{ID: p2, Entity: ada.ID}).(entities.ProjectItem).Item
	if two.Name != "Ada" || two.Overridden || two.Fields["age"] != "30" || two.Fields["role"] != "lead" {
		t.Errorf("book two: %+v", two)
	}
	// Overrides change one piece at a time: a null stops overriding a field, an empty name inherits.
	step := x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: p1, EntityID: ada.ID, Name: str(""), Fields: map[string]any{"age": nil}}).(entities.OverrideOut).Item
	if step.Name != "Ada" || step.Summary != "A mapmaker in 1998." || step.Fields["age"] != "30" || step.Override == nil || step.Override.Name != nil || step.Override.Notes != "Younger here." {
		t.Errorf("after a partial change: %+v / %+v", step, step.Override)
	}
	// Unknown fields are refused; so is an entity outside the project's scope.
	x.expect(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: p1, EntityID: ada.ID, Fields: map[string]any{"nope": "x"}}, ops.CodeInvalid)
	other := x.compendium(x.owner, "Elsewhere")
	z := x.ent(x.owner, other, "character", "Zed")
	x.expect(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: p1, EntityID: z.ID, Name: str("Zedd")}, ops.CodeNotFound)
	// Removing every part of an override removes its row; clear is idempotent.
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: p1, EntityID: ada.ID, Summary: str(""), Notes: str(""), Fields: map[string]any{"role": nil}})
	if n := x.count(`SELECT count(*) FROM project_entity_overrides WHERE project_id = ?`, p1); n != 0 {
		t.Errorf("an empty override left %d rows", n)
	}
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: p1, EntityID: ada.ID, Name: str("Quill")})
	if c := x.call(x.owner, "project.entity.override.clear", entities.OverrideClearIn{ID: p1, EntityID: ada.ID}).(entities.RemovedOut); !c.Removed {
		t.Error("not cleared")
	}
	if c := x.call(x.owner, "project.entity.override.clear", entities.OverrideClearIn{ID: p1, EntityID: ada.ID}).(entities.RemovedOut); c.Removed {
		t.Error("cleared twice")
	}
	var rowEnd string
	_ = x.d.Read().QueryRow(`SELECT name||'|'||summary||'|'||body||'|'||fields_json||'|'||revision||'|'||updated_at FROM entities WHERE id = ?`, ada.ID).Scan(&rowEnd)
	if rowEnd != rowBefore {
		t.Error("the record changed through all the override operations")
	}
}

// What a project shows: primary, references and links, with the documented
// precedence, and only what the caller's own Compendium membership allows.
func TestProjectScopeAndMembershipSeparation(t *testing.T) {
	x := newH(t)
	world := x.compendium(x.owner, "World")
	cosmos := x.compendium(x.owner, "Cosmology")
	cameos := x.compendium(x.owner, "Cameos")
	ada := x.ent(x.owner, world, "character", "Ada")
	moon := x.ent(x.owner, cosmos, "place", "The Moon")
	stranger := x.ent(x.owner, cameos, "character", "Sam")
	x.ent(x.owner, cameos, "character", "Other cameo")
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: world})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: cosmos})
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: stranger.ID})
	// A link to an entity the project already reaches adds nothing; so does a reference to the primary.
	if l := x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: ada.ID}).(entities.LinkItem); l.Created {
		t.Error("linked an entity the primary already provides")
	}
	if l := x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: moon.ID}).(entities.LinkItem); l.Created {
		t.Error("linked an entity a reference already provides")
	}
	x.expect(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: world}, ops.CodeConflict)

	list := func(p ops.Principal) entities.ProjectListOut {
		return x.call(p, "project.entity.list", entities.ProjectListIn{ID: pid}).(entities.ProjectListOut)
	}
	ownerView := list(x.owner)
	via := map[string]string{}
	for _, it := range ownerView.Items {
		via[it.Name] = it.Via
	}
	if !reflect.DeepEqual(via, map[string]string{"Ada": "primary", "The Moon": "reference", "Sam": "link"}) || ownerView.Hidden {
		t.Errorf("owner's view: %v hidden=%v", via, ownerView.Hidden)
	}

	// Project membership grants nothing: an editor, a reviewer and a reader of the
	// project who are not on the Compendiums see an empty list and 404 on a name.
	for _, who := range []struct {
		p    ops.Principal
		role string
	}{{x.editor, ops.ProjectEditor}, {x.reader, ops.ProjectReader}} {
		x.projectMember(pid, who.p, who.role)
		v := list(who.p)
		if len(v.Items) != 0 || !v.Hidden || v.Total != 0 {
			t.Errorf("a project %s who is not on the Compendium sees %+v", who.role, v)
		}
		x.expect(who.p, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: "Ada"}, ops.CodeNotFound)
		x.expect(who.p, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: ada.ID}, ops.CodeNotFound)
		w := x.call(who.p, "project.compendium.get", compendiums.ProjectIn{ID: pid}).(compendiums.WiringItem).Item
		if w.Primary != nil || !w.HasPrimary || len(w.References) != 0 || w.Hidden != 2 {
			t.Errorf("wiring seen by a project member off the Compendium: %+v", w)
		}
	}
	x.expect(x.editor, "entity.get", entities.GetIn{ID: ada.ID}, ops.CodeNotFound)
	x.expect(x.editor, "entity.list", entities.ListIn{ID: world}, ops.CodeNotFound)
	x.expect(x.editor, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ada.ID, Name: str("x")}, ops.CodeNotFound)
	// And the other way round: on the Compendium but not on the project.
	x.member(world, x.stranger, "editor")
	x.expect(x.stranger, "project.entity.list", entities.ProjectListIn{ID: pid}, ops.CodeNotFound)
	if got := x.get(x.stranger, ada.ID); got.Name != "Ada" {
		t.Errorf("a Compendium member cannot read: %+v", got.Entity)
	}

	// Granting the Compendium (explicitly) shows what it covers, and only that.
	x.member(cosmos, x.editor, "reader")
	v := list(x.editor)
	if len(v.Items) != 1 || v.Items[0].Name != "The Moon" || !v.Hidden || v.Items[0].Editable {
		t.Errorf("after one grant: %+v", v)
	}
	// A reader of the referenced Compendium cannot write through the reference, whichever door they use.
	x.expect(x.editor, "entity.update", entities.UpdateIn{ID: moon.ID, Summary: str("x")}, ops.CodeForbidden)
	x.expect(x.editor, "entity.trash", entities.StateIn{ID: moon.ID}, ops.CodeForbidden)
	x.expect(x.editor, "entity.alias.add", entities.AliasAddIn{ID: moon.ID, Alias: "Luna"}, ops.CodeForbidden)
	x.expect(x.editor, "entity.create", entities.CreateIn{ID: cosmos, Kind: "item", Name: "Rock"}, ops.CodeForbidden)
	// A project editor can override it for their book, and that touches only the book's row.
	ov := x.call(x.editor, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: moon.ID, Summary: str("Smaller in this book.")}).(entities.OverrideOut).Item
	if ov.Summary != "Smaller in this book." || ov.Editable || ov.Via != "reference" {
		t.Errorf("%+v", ov)
	}
	if got := x.get(x.owner, moon.ID); got.Summary != "" {
		t.Errorf("the override wrote the Compendium: %+v", got.Entity)
	}
	// A reader of the project can never write an override.
	x.expect(x.reader, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: moon.ID, Summary: str("no")}, ops.CodeForbidden)

	// A trashed entity and a trashed Compendium leave the project's view.
	x.call(x.owner, "entity.trash", entities.StateIn{ID: ada.ID})
	if v := list(x.owner); len(v.Items) != 2 {
		t.Errorf("after trashing an entity: %d", len(v.Items))
	}
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: cosmos})
	if v := list(x.owner); len(v.Items) != 1 || v.Items[0].Name != "Sam" {
		t.Errorf("after trashing a Compendium: %+v", v.Items)
	}
	x.call(x.owner, "compendium.restore", compendiums.StateIn{ID: cosmos})
	// The override survived the trash, dormant, and applies again.
	if got := x.call(x.owner, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: "The Moon"}).(entities.ProjectItem).Item; got.Summary != "Smaller in this book." {
		t.Errorf("override after restore: %+v", got)
	}
	// Lookup by name or alias and by id; the filters.
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: moon.ID, Alias: "Luna"})
	if got := x.call(x.owner, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: " LUNA "}).(entities.ProjectItem).Item; got.ID != moon.ID {
		t.Errorf("by alias: %+v", got)
	}
	// By id reads the entity alone, and still carries its aliases and override.
	byID := x.call(x.owner, "project.entity.get", entities.ProjectGetIn{ID: pid, Entity: moon.ID}).(entities.ProjectItem).Item
	if len(byID.Aliases) != 1 || byID.Aliases[0] != "Luna" || byID.Summary != "Smaller in this book." {
		t.Errorf("by id: aliases %v, summary %q", byID.Aliases, byID.Summary)
	}
	if v := x.call(x.owner, "project.entity.list", entities.ProjectListIn{ID: pid, Kind: "place"}).(entities.ProjectListOut); len(v.Items) != 1 {
		t.Errorf("kind: %+v", v)
	}
	if v := x.call(x.owner, "project.entity.list", entities.ProjectListIn{ID: pid, Q: "lun"}).(entities.ProjectListOut); len(v.Items) != 1 {
		t.Errorf("q: %+v", v)
	}
	if v := x.call(x.owner, "project.entity.list", entities.ProjectListIn{ID: pid, Compendium: cameos}).(entities.ProjectListOut); len(v.Items) != 1 || v.Items[0].Name != "Sam" {
		t.Errorf("compendium filter: %+v", v)
	}
}

func TestResolveNamePrecedence(t *testing.T) {
	x := newH(t)
	world := x.compendium(x.owner, "World")
	ref1 := x.compendium(x.owner, "First reference")
	ref2 := x.compendium(x.owner, "Second reference")
	cam := x.compendium(x.owner, "Cameos")
	primaryJo := x.ent(x.owner, world, "character", "Jo")
	r1 := x.ent(x.owner, ref1, "character", "Jo")
	r2 := x.ent(x.owner, ref2, "character", "Jo")
	linked := x.ent(x.owner, cam, "character", "Jo")
	pid := x.project(x.owner, "Book")
	resolve := func() string {
		got, _, err := entities.ResolveName(x.ctx, x.d.Read(), pid, " jo ")
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if resolve() != "" {
		t.Error("resolved a name in an empty scope")
	}
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: linked.ID})
	if resolve() != linked.ID {
		t.Error("link")
	}
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: ref2})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: ref1})
	if got := resolve(); got != r2.ID && got != r1.ID {
		t.Error("reference")
	}
	if got := resolve(); got != r2.ID { // ref2 was added first
		t.Errorf("references are ordered by when they were added: got %s want %s", got, r2.ID)
	}
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: world})
	if resolve() != primaryJo.ID {
		t.Error("primary")
	}
	_ = r1
}

// Deleting a Compendium leaves no row behind anywhere: entities and everything
// hanging off them, the search rows, the overrides and links, the references,
// the field definitions, the exports, the memberships, and the primary pointers.
func TestPurgeCompendiumLeavesNoRows(t *testing.T) {
	x := newH(t)
	world := x.compendium(x.owner, "World")
	other := x.compendium(x.owner, "Other")
	a, b := x.ent(x.owner, world, "character", "A"), x.ent(x.owner, world, "character", "B")
	keep := x.ent(x.owner, other, "character", "Keep")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: a.ID, Alias: "Ay"})
	x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: a.ID, ToEntityID: b.ID, Kind: "ally"})
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: b.ID, IntoID: a.ID})
	x.member(world, x.editor, "editor")
	x.call(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: other, TargetID: world})
	x.call(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: world, TargetID: other})
	pid, plink := x.project(x.owner, "Primary"), x.project(x.owner, "Cameo")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: world})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: a.ID, Name: str("A2")})
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: plink, EntityID: a.ID})
	x.call(x.owner, "compendium.export", entities.ExportIn{ID: world})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: plink, CompendiumID: world})
	x.call(x.owner, "project.entity.link.remove", entities.LinkRemoveIn{ID: plink, EntityID: a.ID})
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: plink, EntityID: keep.ID})
	x.exec(`UPDATE documents SET pov_entity_id = ? WHERE project_id = ?`, a.ID, pid)

	// The purge asks for every project that will lose it, by name.
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: world})
	x.expect(x.owner, "compendium.purge", compendiums.PurgeIn{ID: world, ConfirmName: "World"}, ops.CodeConflict)
	x.expect(x.owner, "compendium.purge", compendiums.PurgeIn{ID: world, ConfirmName: "World", DetachProjects: []string{pid}}, ops.CodeConflict)
	x.expect(x.owner, "compendium.purge", compendiums.PurgeIn{ID: world, ConfirmName: "wrong", DetachProjects: []string{pid, plink}}, ops.CodeInvalid)
	x.expect(x.editor, "compendium.purge", compendiums.PurgeIn{ID: world, ConfirmName: "World"}, ops.CodeForbidden)
	out := x.call(x.owner, "compendium.purge", compendiums.PurgeIn{ID: world, ConfirmName: "World", DetachProjects: []string{pid, plink}}).(compendiums.PurgeOut)
	if !out.Purged || out.Entities != 1 || len(out.DetachedProjects) != 2 {
		t.Errorf("%+v", out)
	}
	for table, where := range map[string]string{
		"entities": "compendium_id = ?1", "entity_field_defs": "compendium_id = ?1", "compendium_members": "compendium_id = ?1",
		"entity_redirects": "compendium_id = ?1", "entity_relations": "compendium_id = ?1", "entity_text": "compendium_id = ?1",
		"compendium_exports": "compendium_id = ?1", "compendiums": "id = ?1",
		"compendium_refs": "target_compendium_id = ?1 OR owner_id = ?1",
	} {
		if n := x.count(`SELECT count(*) FROM `+table+` WHERE `+where, world); n != 0 {
			t.Errorf("%s still has %d rows of the purged Compendium", table, n)
		}
	}
	for table, ids := range map[string][]string{"entity_aliases": {a.ID, b.ID}, "project_entity_overrides": {a.ID}, "project_entity_links": {a.ID}} {
		for _, id := range ids {
			col := "entity_id"
			if n := x.count(`SELECT count(*) FROM `+table+` WHERE `+col+` = ?`, id); n != 0 {
				t.Errorf("%s still has %d rows of a purged entity", table, n)
			}
		}
	}
	if n := x.count(`SELECT count(*) FROM entity_fts WHERE entity_fts MATCH 'A2 OR Ay'`); n != 0 {
		t.Errorf("the search index still has %d rows", n)
	}
	if n := x.count(`SELECT count(*) FROM projects WHERE compendium_id IS NOT NULL`); n != 0 {
		t.Errorf("%d projects still point at a purged Compendium", n)
	}
	if n := x.count(`SELECT count(*) FROM documents WHERE pov_entity_id IS NOT NULL`); n != 0 {
		t.Errorf("%d scenes still name a purged entity", n)
	}
	// What belongs to others is untouched, and the projects live.
	if n := x.count(`SELECT count(*) FROM entities WHERE compendium_id = ?`, other); n != 1 {
		t.Error("another Compendium's entities were deleted")
	}
	if n := x.count(`SELECT count(*) FROM project_entity_links WHERE entity_id = ?`, keep.ID); n != 1 {
		t.Error("a link to another Compendium's entity was deleted")
	}
	if n := x.count(`SELECT count(*) FROM projects`); n != 2 {
		t.Error("a project was deleted with the Compendium")
	}
	x.expect(x.owner, "entity.get", entities.GetIn{ID: a.ID}, ops.CodeNotFound)
}

// Purging a project removes its references, overrides and links, and leaves
// the Compendium and its entities alone.
func TestPurgeProjectRemovesItsCompendiumRows(t *testing.T) {
	x := newH(t)
	world, other := x.compendium(x.owner, "World"), x.compendium(x.owner, "Other")
	a := x.ent(x.owner, world, "character", "A")
	o := x.ent(x.owner, other, "character", "O")
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: world})
	x.call(x.owner, "project.compendium.ref.add", compendiums.RefAddIn{ID: pid, CompendiumID: other})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: a.ID, Name: str("A2")})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: o.ID, Name: str("O2")})
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: pid, EntityID: x.ent(x.owner, x.compendium(x.owner, "Cameos"), "item", "Key").ID})
	x.call(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: other, TargetID: world})
	x.call(x.owner, "project.trash", projects.StateIn{ID: pid})
	x.call(x.owner, "project.purge", projects.PurgeIn{ID: pid, ConfirmTitle: "Book"})
	for _, table := range []string{"project_entity_overrides", "project_entity_links"} {
		if n := x.count(`SELECT count(*) FROM ` + table); n != 0 {
			t.Errorf("%s has %d rows after its project was purged", table, n)
		}
	}
	if n := x.count(`SELECT count(*) FROM compendium_refs WHERE owner_kind = 'project'`); n != 0 {
		t.Errorf("%d project references remain", n)
	}
	if n := x.count(`SELECT count(*) FROM compendium_refs WHERE owner_kind = 'compendium'`); n != 1 {
		t.Error("a Compendium's reference was removed with a project")
	}
	if n := x.count(`SELECT count(*) FROM entities`); n != 3 {
		t.Errorf("%d entities remain, want 3", n)
	}
}

// A Compendium that references another reads it read-only, and only the parts
// the caller is a member of: a reference does not open a bible.
func TestReferencedEntitiesAreReadOnlyAndNeedMembership(t *testing.T) {
	x := newH(t)
	main := x.compendium(x.owner, "Main")
	cosmos := x.compendium(x.owner, "Cosmology")
	secret := x.compendium(x.owner, "Secrets")
	moon := x.ent(x.owner, cosmos, "place", "The Moon")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: moon.ID, Alias: "Luna"})
	x.ent(x.owner, cosmos, "item", "Telescope")
	x.ent(x.owner, secret, "character", "The Spy")
	x.call(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: main, TargetID: cosmos})
	x.call(x.owner, "compendium.ref.add", compendiums.CompendiumRefAddIn{ID: main, TargetID: secret})
	list := func(p ops.Principal, in entities.ReferencedListIn) entities.ReferencedListOut {
		in.ID = main
		return x.call(p, "entity.referenced.list", in).(entities.ReferencedListOut)
	}
	if v := list(x.owner, entities.ReferencedListIn{}); v.Total != 3 || v.Hidden || !v.Items[0].Editable {
		t.Errorf("the owner of all three: %+v", v)
	}
	// A reader of Main who is a reader of Cosmology only: sees Cosmology's, not the secrets, and cannot write.
	x.member(main, x.editor, "reader")
	x.member(cosmos, x.editor, "reader")
	v := list(x.editor, entities.ReferencedListIn{})
	if v.Total != 2 || !v.Hidden || v.Items[0].Editable || v.Items[0].CompendiumName != "Cosmology" {
		t.Errorf("a partial member's view: %+v", v)
	}
	if q := list(x.editor, entities.ReferencedListIn{Q: "luna"}); q.Total != 1 || q.Items[0].Name != "The Moon" || len(q.Items[0].Aliases) != 1 {
		t.Errorf("q: %+v", q)
	}
	if q := list(x.editor, entities.ReferencedListIn{Kind: "item"}); q.Total != 1 {
		t.Errorf("kind: %+v", q)
	}
	x.expect(x.editor, "entity.update", entities.UpdateIn{ID: moon.ID, Summary: str("x")}, ops.CodeForbidden)
	// A member of none of the references sees nothing, and knows only that something is hidden.
	x.member(main, x.reader, "reader")
	if v := list(x.reader, entities.ReferencedListIn{}); v.Total != 0 || !v.Hidden {
		t.Errorf("a member of Main only: %+v", v)
	}
	x.expect(x.stranger, "entity.referenced.list", entities.ReferencedListIn{ID: main}, ops.CodeNotFound)
	// A trashed referenced Compendium drops out.
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: cosmos})
	if v := list(x.owner, entities.ReferencedListIn{}); v.Total != 1 {
		t.Errorf("after trashing a referenced Compendium: %+v", v)
	}
}

// Someone on a project but not on the Compendium learns nothing from clearing an
// override or unlinking an entity: both are a 404, whether or not one exists.
func TestClearAndUnlinkNeedMembership(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	ada := x.ent(x.owner, cid, "character", "Ada")
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: ada.ID, Name: str("Ada Q")})
	x.projectMember(pid, x.editor, ops.ProjectEditor)
	x.expect(x.editor, "project.entity.override.clear", entities.OverrideClearIn{ID: pid, EntityID: ada.ID}, ops.CodeNotFound)
	x.expect(x.editor, "project.entity.override.clear", entities.OverrideClearIn{ID: pid, EntityID: "ent_00000000000000000000000000"}, ops.CodeNotFound)
	x.expect(x.editor, "project.entity.link.remove", entities.LinkRemoveIn{ID: pid, EntityID: ada.ID}, ops.CodeNotFound)
	if n := x.count(`SELECT count(*) FROM project_entity_overrides`); n != 1 {
		t.Error("an override was removed by someone who cannot see the entity")
	}
	x.member(cid, x.editor, "reader")
	if rm := x.call(x.editor, "project.entity.link.remove", entities.LinkRemoveIn{ID: pid, EntityID: ada.ID}).(entities.RemovedOut); rm.Removed {
		t.Error("removed a link that was never there")
	}
	if rm := x.call(x.editor, "project.entity.override.clear", entities.OverrideClearIn{ID: pid, EntityID: ada.ID}).(entities.RemovedOut); !rm.Removed {
		t.Error("a member of the Compendium could not clear an override")
	}
}

// A choice that only a project override uses cannot be dropped either.
func TestChoiceInUseByAnOverrideCannotBeDropped(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Type: "choice", Choices: []string{"A", "B"}})
	it := x.ent(x.owner, cid, "item", "Key")
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: it.ID, Fields: map[string]any{"grade": "B"}})
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Type: "choice", Choices: []string{"A"}}, ops.CodeConflict)
	x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Type: "choice", Choices: []string{"A", "B", "C"}})
}

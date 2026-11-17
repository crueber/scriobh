package entities_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/ops"
)

func TestEntityLifecycleAndFields(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "The Salt Road")
	e := x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "  Máire   Nolan ", SortName: "Nolan, Máire", Summary: "Keeps the house.",
		Body: "She **walks** the harbour.", Color: "#AABBCC",
		Fields: map[string]any{"age": 34.0, "role": "protagonist", "wants": ""}}).(entities.Item).Item
	if e.Name != "Máire Nolan" || e.Kind != "character" || e.Revision != 1 || e.Fields["age"] != "34" || e.Fields["role"] != "protagonist" || len(e.Fields) != 2 {
		t.Fatalf("created: %+v", e)
	}
	// Names are unique ignoring case and spacing.
	for _, dup := range []string{"máire nolan", "MÁIRE  NOLAN", " Máire Nolan"} {
		er := x.expect(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "place", Name: dup}, ops.CodeConflict)
		if er.Data["entity_id"] != e.ID {
			t.Errorf("the clash does not name the other entity: %+v", er.Data)
		}
	}
	// Typed fields.
	defs := func(kind, key, typ string, choices ...string) {
		x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: kind, Key: key, Type: typ, Choices: choices})
	}
	defs("character", "born", "date")
	defs("character", "house", "choice", "Nolan", "Reilly")
	defs("character", "height", "number")
	bad := map[string]map[string]any{
		"unknown": {"nope": "x"}, "date": {"born": "last year"}, "date2": {"born": "1998-13"}, "choice": {"house": "Smith"}, "number": {"height": "tall"},
	}
	for name, f := range bad {
		x.expect(x.owner, "entity.update", entities.UpdateIn{ID: e.ID, Fields: f}, ops.CodeInvalid)
		_ = name
	}
	u := x.call(x.owner, "entity.update", entities.UpdateIn{ID: e.ID, BaseRevision: ptr(1), Fields: map[string]any{"born": "1998-04", "house": "nolan", "height": "1.70", "age": nil}}).(entities.Item).Item
	if u.Fields["born"] != "1998-04" || u.Fields["house"] != "Nolan" || u.Fields["height"] != "1.7" || u.Fields["age"] != "" || u.Revision != 2 {
		t.Fatalf("updated: %+v", u)
	}
	if _, has := u.Fields["age"]; has {
		t.Error("an empty value must clear the field")
	}
	// A stale write is refused with the current revision.
	er := x.expect(x.owner, "entity.update", entities.UpdateIn{ID: e.ID, BaseRevision: ptr(1), Summary: str("late")}, ops.CodeConflict)
	if er.Data["revision"] != 2 {
		t.Errorf("conflict data: %+v", er.Data)
	}
	// Nothing changed: no new revision.
	same := x.call(x.owner, "entity.update", entities.UpdateIn{ID: e.ID, Summary: str("Keeps the house.")}).(entities.Item).Item
	if same.Revision != 2 {
		t.Errorf("an update that changed nothing moved the revision to %d", same.Revision)
	}
	// Trash frees the name; restore takes it back and refuses a clash.
	x.call(x.owner, "entity.trash", entities.StateIn{ID: e.ID})
	e2 := x.ent(x.owner, cid, "character", "Máire Nolan")
	x.expect(x.owner, "entity.restore", entities.StateIn{ID: e.ID}, ops.CodeConflict)
	x.call(x.owner, "entity.trash", entities.StateIn{ID: e2.ID})
	if r := x.call(x.owner, "entity.restore", entities.StateIn{ID: e.ID}).(entities.Item).Item; r.Trashed {
		t.Error("not restored")
	}
	// Trash and restore are idempotent.
	x.call(x.owner, "entity.restore", entities.StateIn{ID: e.ID})
}

func ptr[T any](v T) *T { return &v }

func TestEntityListFiltersAndPaging(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "Big")
	for i := 0; i < 25; i++ {
		x.ent(x.owner, cid, "character", fmt.Sprintf("Person %02d", i))
	}
	x.ent(x.owner, cid, "place", "The Harbour")
	b := x.ent(x.owner, cid, "item", "Brass key")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: b.ID, Alias: "the Skeleton"})
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: b.ID, SortName: str("Aaa key")})

	var names []string
	cur := ""
	for pages := 0; ; pages++ {
		out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Limit: 10, Cursor: cur}).(entities.ListOut)
		if out.Total != 27 {
			t.Fatalf("total %d", out.Total)
		}
		for _, it := range out.Items {
			names = append(names, it.Name)
		}
		if out.NextCursor == "" {
			if pages != 2 {
				t.Errorf("%d pages", pages+1)
			}
			break
		}
		cur = out.NextCursor
	}
	if len(names) != 27 || names[0] != "Brass key" || names[1] != "The Harbour" && names[1] != "Person 00" {
		t.Errorf("order: %v", names[:3])
	}
	// The sort name sorts first (aaa), then names in order.
	if names[1] != "Person 00" || names[26] != "The Harbour" {
		t.Errorf("order: %v ... %v", names[1], names[26])
	}
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Kind: "place"}).(entities.ListOut); len(out.Items) != 1 || out.Items[0].Name != "The Harbour" {
		t.Errorf("kind filter: %+v", out)
	}
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Q: "person 1"}).(entities.ListOut); out.Total != 10 {
		t.Errorf("q filter: %d", out.Total)
	}
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Name: " THE   skeleton "}).(entities.ListOut); len(out.Items) != 1 || out.Items[0].ID != b.ID {
		t.Errorf("name lookup by alias: %+v", out)
	}
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, Name: "Brass key"}).(entities.ListOut); len(out.Items) != 1 || len(out.Items[0].Aliases) != 1 {
		t.Errorf("name lookup: %+v", out)
	}
	x.call(x.owner, "entity.trash", entities.StateIn{ID: b.ID})
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid}).(entities.ListOut); out.Total != 26 {
		t.Errorf("live after trash: %d", out.Total)
	}
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, State: "trashed"}).(entities.ListOut); out.Total != 1 {
		t.Errorf("trashed: %d", out.Total)
	}
	if out := x.call(x.owner, "entity.list", entities.ListIn{ID: cid, State: "all"}).(entities.ListOut); out.Total != 27 {
		t.Errorf("all: %d", out.Total)
	}
	x.expect(x.owner, "entity.list", entities.ListIn{ID: cid, Cursor: "!!"}, ops.CodeInvalid)
}

func TestAliasCollisions(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	a := x.ent(x.owner, cid, "character", "Ada")
	b := x.ent(x.owner, cid, "character", "Bea")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: a.ID, Alias: "Addie"})
	// Against another entity's name and alias, and its own name: refused, and named.
	er := x.expect(x.owner, "entity.alias.add", entities.AliasAddIn{ID: b.ID, Alias: "ADDIE"}, ops.CodeConflict)
	if er.Data["entity_id"] != a.ID || er.Data["via"] != "alias" {
		t.Errorf("alias clash data: %+v", er.Data)
	}
	if er := x.expect(x.owner, "entity.alias.add", entities.AliasAddIn{ID: b.ID, Alias: "ada"}, ops.CodeConflict); er.Data["via"] != "name" {
		t.Errorf("name clash data: %+v", er.Data)
	}
	x.expect(x.owner, "entity.alias.add", entities.AliasAddIn{ID: b.ID, Alias: "bea"}, ops.CodeConflict)
	x.expect(x.owner, "entity.alias.add", entities.AliasAddIn{ID: a.ID, Alias: "addie "}, ops.CodeConflict)
	// At creation too, and a name against an alias.
	x.expect(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Cal", Aliases: []string{"Addie"}}, ops.CodeConflict)
	x.expect(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "addie"}, ops.CodeConflict)
	if n := x.count(`SELECT count(*) FROM entities WHERE name = 'Cal'`); n != 0 {
		t.Error("a refused create left a row behind")
	}
	// An alias in another Compendium is nobody's business.
	other := x.compendium(x.owner, "Elsewhere")
	x.call(x.owner, "entity.create", entities.CreateIn{ID: other, Kind: "character", Name: "Zed", Aliases: []string{"Addie"}})
	// Trashing frees an alias; restoring refuses a clash.
	x.call(x.owner, "entity.trash", entities.StateIn{ID: a.ID})
	alias := x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: b.ID, Alias: "Addie"}).(entities.AliasItem)
	x.expect(x.owner, "entity.restore", entities.StateIn{ID: a.ID}, ops.CodeConflict)
	rem := x.call(x.owner, "entity.alias.remove", entities.AliasRemoveIn{ID: b.ID, AliasID: alias.Alias.ID}).(entities.AliasRemoveOut)
	if !rem.Removed || len(rem.Item.Aliases) != 0 {
		t.Errorf("%+v", rem)
	}
	x.call(x.owner, "entity.restore", entities.StateIn{ID: a.ID})
	if rem := x.call(x.owner, "entity.alias.remove", entities.AliasRemoveIn{ID: b.ID, AliasID: alias.Alias.ID}).(entities.AliasRemoveOut); rem.Removed {
		t.Error("removed twice")
	}
	// An alias id of another entity cannot be removed through this one.
	if rem := x.call(x.owner, "entity.alias.remove", entities.AliasRemoveIn{ID: b.ID, AliasID: x.get(x.owner, a.ID).Aliases[0].ID}).(entities.AliasRemoveOut); rem.Removed {
		t.Error("removed another entity's alias")
	}
}

func TestRelations(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	m := x.ent(x.owner, cid, "character", "Máire")
	s := x.ent(x.owner, cid, "character", "Sorcha")
	p := x.ent(x.owner, cid, "character", "Pádraig")
	r := x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: m.ID, ToEntityID: s.ID, Kind: "family", Label: "mother of", InverseLabel: "daughter of"}).(entities.RelationItem).Item
	if r.Direction != "out" || r.Label != "mother of" || r.OtherName != "Sorcha" {
		t.Errorf("%+v", r)
	}
	if from := x.call(x.owner, "entity.relation.list", entities.RelationListIn{ID: s.ID}).(entities.RelationListOut).Items; len(from) != 1 || from[0].Direction != "in" || from[0].Label != "daughter of" || from[0].OtherID != m.ID {
		t.Errorf("from the target: %+v", from)
	}
	x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: s.ID, ToEntityID: p.ID, Kind: "rival", Symmetric: true})
	for _, id := range []string{s.ID, p.ID} {
		rel := x.call(x.owner, "entity.relation.list", entities.RelationListIn{ID: id}).(entities.RelationListOut).Items
		var sym *entities.Relation
		for i := range rel {
			if rel[i].Direction == "both" {
				sym = &rel[i]
			}
		}
		if sym == nil || sym.Label != "rival" {
			t.Errorf("symmetric from %s: %+v", id, rel)
		}
	}
	// The same edge twice, either way round when symmetric, is a conflict; self and strangers are refused.
	x.expect(x.owner, "entity.relation.add", entities.RelationAddIn{ID: m.ID, ToEntityID: s.ID, Kind: "family", Label: "mother of"}, ops.CodeConflict)
	x.expect(x.owner, "entity.relation.add", entities.RelationAddIn{ID: p.ID, ToEntityID: s.ID, Kind: "rival", Symmetric: true}, ops.CodeConflict)
	x.expect(x.owner, "entity.relation.add", entities.RelationAddIn{ID: m.ID, ToEntityID: m.ID}, ops.CodeInvalid)
	x.expect(x.owner, "entity.relation.add", entities.RelationAddIn{ID: m.ID, ToEntityID: s.ID, Kind: "custom"}, ops.CodeInvalid)
	x.expect(x.owner, "entity.relation.add", entities.RelationAddIn{ID: m.ID, ToEntityID: "ent_nothing", Kind: "ally"}, ops.CodeInvalid)
	other := x.compendium(x.owner, "Elsewhere")
	z := x.ent(x.owner, other, "character", "Zed")
	x.expect(x.owner, "entity.relation.add", entities.RelationAddIn{ID: m.ID, ToEntityID: z.ID, Kind: "ally"}, ops.CodeInvalid)
	if d := x.get(x.owner, m.ID); len(d.Relations) != 1 {
		t.Errorf("get: %+v", d.Relations)
	}
	// A trashed end hides the relationship, and a relationship cannot be removed through an entity it does not touch.
	x.call(x.owner, "entity.trash", entities.StateIn{ID: s.ID})
	if d := x.get(x.owner, m.ID); len(d.Relations) != 0 {
		t.Errorf("a relationship with a trashed entity is shown: %+v", d.Relations)
	}
	x.call(x.owner, "entity.restore", entities.StateIn{ID: s.ID})
	if rm := x.call(x.owner, "entity.relation.remove", entities.RelationRemoveIn{ID: p.ID, RelationID: r.ID}).(entities.RelationRemoveOut); rm.Removed {
		t.Error("removed a relationship through an entity it does not touch")
	}
	if rm := x.call(x.owner, "entity.relation.remove", entities.RelationRemoveIn{ID: s.ID, RelationID: r.ID}).(entities.RelationRemoveOut); !rm.Removed {
		t.Error("not removed")
	}
}

func TestFieldDefinitions(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	list := func(kind string) []entities.FieldDef {
		return x.call(x.owner, "entity.field.list", entities.FieldListIn{ID: cid, Kind: kind}).(entities.FieldListOut).Items
	}
	if n := len(list("character")); n != 8 {
		t.Errorf("starter character fields: %d", n)
	}
	none := false
	bare := x.call(x.owner, "compendium.create", compendiums.CreateIn{Name: "Bare", StarterFields: &none}).(compendiums.Item).Item.ID
	if n := len(x.call(x.owner, "entity.field.list", entities.FieldListIn{ID: bare}).(entities.FieldListOut).Items); n != 0 {
		t.Errorf("a Compendium made without starter fields has %d", n)
	}
	x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Type: "choice", Choices: []string{"A", "B", "C"}})
	it := x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "item", Name: "Key", Fields: map[string]any{"grade": "b"}}).(entities.Item).Item
	// The type cannot change; a choice in use cannot go; a label can.
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Type: "text"}, ops.CodeConflict)
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Type: "choice", Choices: []string{"A", "C"}}, ops.CodeConflict)
	x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "grade", Label: "Quality", Type: "choice", Choices: []string{"A", "B", "C", "D"}})
	// Bad definitions.
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "Bad Key", Type: "text"}, ops.CodeInvalid)
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "c", Type: "choice"}, ops.CodeInvalid)
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "c", Type: "text", Choices: []string{"x"}}, ops.CodeInvalid)
	x.expect(x.owner, "entity.field.set", entities.FieldSetIn{ID: cid, Kind: "item", Key: "c", Type: "choice", Choices: []string{"x", "X"}}, ops.CodeInvalid)
	// Removing a field in use needs drop_values, and drops them from overrides too.
	pid := x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: pid, CompendiumID: cid})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: pid, EntityID: it.ID, Fields: map[string]any{"grade": "D"}})
	er := x.expect(x.owner, "entity.field.remove", entities.FieldRemoveIn{ID: cid, Kind: "item", Key: "grade"}, ops.CodeConflict)
	if er.Data["entities"] != 1 || er.Data["overrides"] != 1 {
		t.Errorf("conflict data: %+v", er.Data)
	}
	rm := x.call(x.owner, "entity.field.remove", entities.FieldRemoveIn{ID: cid, Kind: "item", Key: "grade", DropValues: true}).(entities.FieldRemoveOut)
	if !rm.Removed || rm.ValuesDropped != 1 || rm.OverridesCount != 1 {
		t.Errorf("%+v", rm)
	}
	if got := x.get(x.owner, it.ID); len(got.Fields) != 0 {
		t.Errorf("values remain: %+v", got.Fields)
	}
	if n := x.count(`SELECT count(*) FROM project_entity_overrides WHERE entity_id = ? AND fields_json <> '{}'`, it.ID); n != 0 {
		t.Error("override values remain")
	}
	if rm := x.call(x.owner, "entity.field.remove", entities.FieldRemoveIn{ID: cid, Kind: "item", Key: "grade"}).(entities.FieldRemoveOut); rm.Removed {
		t.Error("removed twice")
	}
}

// Entity text is searchable by name, alias, summary and body, in the same
// transaction as the write; trashed entities and other Compendiums never match.
func TestEntitySearchFollowsWrites(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	other := x.compendium(x.owner, "Other")
	q := func(c, words string) search.CompendiumQueryOut {
		return x.call(x.owner, "search.compendium", search.CompendiumQueryIn{ID: c, Q: words}).(search.CompendiumQueryOut)
	}
	e := x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "character", Name: "Máire Nolan", Summary: "Keeps a lighthouse.", Body: "She walks the **harbour** at dawn.\n\n%%private%%"}).(entities.Item).Item
	x.call(x.owner, "entity.create", entities.CreateIn{ID: other, Kind: "place", Name: "Harbour Town", Body: "harbour harbour"})
	if r := q(cid, "maire"); r.Total != 1 || r.Items[0].EntityID != e.ID || r.Items[0].MatchedIn != "name" {
		t.Errorf("by name (diacritics folded): %+v", r)
	}
	if r := q(cid, "harbo"); r.Total != 1 || r.Items[0].MatchedIn != "body" || !strings.Contains(r.Items[0].SnippetHTML, "<mark>harbour</mark>") {
		t.Errorf("by body prefix: %+v", r)
	}
	if r := q(cid, "light"); r.Total != 1 || r.Items[0].MatchedIn != "summary" {
		t.Errorf("by summary: %+v", r)
	}
	if r := q(cid, "private"); r.Total != 0 {
		t.Errorf("a private note is searchable: %+v", r)
	}
	if r := q(other, "maire"); r.Total != 0 {
		t.Errorf("another Compendium's search found it: %+v", r)
	}
	// Alias add and remove, rename, body edit.
	al := x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: e.ID, Alias: "Mrs Nolan"}).(entities.AliasItem).Alias
	if r := q(cid, "mrs"); r.Total != 1 || r.Items[0].MatchedIn != "alias" {
		t.Errorf("by alias: %+v", r)
	}
	x.call(x.owner, "entity.alias.remove", entities.AliasRemoveIn{ID: e.ID, AliasID: al.ID})
	if r := q(cid, "mrs"); r.Total != 0 {
		t.Errorf("a removed alias still matches: %+v", r)
	}
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: e.ID, Name: str("Sorcha Reilly"), Body: str("Now a sailor.")})
	if q(cid, "maire").Total != 0 || q(cid, "harbour").Total != 0 || q(cid, "sorcha").Total != 1 || q(cid, "sailor").Total != 1 {
		t.Error("search did not follow the update")
	}
	// A name beats a body match in the ranking.
	x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "item", Name: "Reilly's ring", Body: "unrelated"})
	x.call(x.owner, "entity.create", entities.CreateIn{ID: cid, Kind: "item", Name: "Lantern", Body: "reilly reilly reilly reilly"})
	if r := q(cid, "reilly"); r.Total != 3 || r.Items[2].Name != "Lantern" {
		names := []string{}
		for _, i := range r.Items {
			names = append(names, i.Name)
		}
		t.Errorf("ranking: %v", names)
	}
	// Trash and restore.
	x.call(x.owner, "entity.trash", entities.StateIn{ID: e.ID})
	if q(cid, "sorcha").Total != 0 {
		t.Error("a trashed entity is searchable")
	}
	x.call(x.owner, "entity.restore", entities.StateIn{ID: e.ID})
	if q(cid, "sorcha").Total != 1 {
		t.Error("a restored entity is not searchable")
	}
	// Search is words only: syntax is not FTS syntax.
	if r := q(cid, `sorcha" OR name:*`); r.Total != 0 {
		t.Errorf("syntax leaked into the query: %+v", r)
	}
	x.expect(x.owner, "search.compendium", search.CompendiumQueryIn{ID: cid, Q: "   "}, ops.CodeInvalid)
	// The index can be rebuilt from the entities and gives the same answers.
	if err := search.Rebuild(x.ctx, x.d, nil); err != nil {
		t.Fatal(err)
	}
	if q(cid, "sorcha").Total != 1 || q(cid, "sailor").Total != 1 || q(cid, "maire").Total != 0 {
		t.Error("the rebuilt index disagrees")
	}
	// Access follows membership.
	x.expect(x.stranger, "search.compendium", search.CompendiumQueryIn{ID: cid, Q: "sorcha"}, ops.CodeNotFound)
}

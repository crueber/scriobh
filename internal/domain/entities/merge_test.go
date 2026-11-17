package entities_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// dump reads every row of the tables a merge touches, in a fixed order, as one
// string: two dumps are equal exactly when the database content is.
func (x *h) dump() string {
	x.t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, name, kind, summary, body, fields_json, revision, updated_at, trashed_at FROM entities ORDER BY id`,
		`SELECT id, entity_id, alias, notes FROM entity_aliases ORDER BY id`,
		`SELECT id, from_entity_id, to_entity_id, kind, label, symmetric FROM entity_relations ORDER BY id`,
		`SELECT from_id, to_id, from_name FROM entity_redirects ORDER BY from_id`,
		`SELECT project_id, entity_id, name, summary, fields_json, notes FROM project_entity_overrides ORDER BY project_id, entity_id`,
		`SELECT project_id, entity_id FROM project_entity_links ORDER BY project_id, entity_id`,
		`SELECT entity_id, name, aliases, summary, body FROM entity_text ORDER BY entity_id`,
		`SELECT id, pov_entity_id, location_entity_id FROM documents ORDER BY id`,
		`SELECT type, count(*) FROM events GROUP BY type ORDER BY type`,
	} {
		rows, err := x.d.Read().Query(q)
		if err != nil {
			x.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				x.t.Fatal(err)
			}
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
	}
	return b.String()
}

type mergeWorld struct {
	cid, src, dst, third  string
	pOne, pTwo            string
	docID                 string
	srcRel, dupRel, other string
}

// world builds a merge with every kind of collision in it.
func (x *h) world() mergeWorld {
	w := mergeWorld{}
	w.cid = x.compendium(x.owner, "World")
	src := x.call(x.owner, "entity.create", entities.CreateIn{ID: w.cid, Kind: "character", Name: "Ellie", Summary: "From the source.", Body: "Source body.",
		Aliases: []string{"El"}, Fields: map[string]any{"age": "12", "voice": "quiet", "wants": "to leave", "role": "friend"}}).(entities.Item).Item
	dst := x.call(x.owner, "entity.create", entities.CreateIn{ID: w.cid, Kind: "character", Name: "Eleanor", Summary: "", Body: "Target body.",
		Fields: map[string]any{"age": "40", "voice": "quiet", "appearance": "tall", "role": "lead"}}).(entities.Item).Item
	third := x.call(x.owner, "entity.create", entities.CreateIn{ID: w.cid, Kind: "character", Name: "Frank", Aliases: []string{"Dad"}}).(entities.Item).Item
	// The collision rules keep two live entities from sharing an alias, so the
	// two cases a merge must still survive (data written before the rule, or by
	// hand) are inserted directly: an alias both of them hold, and one a third
	// entity holds.
	for _, a := range []struct{ id, alias string }{{dst.ID, "Nell"}, {src.ID, "Nell"}, {src.ID, "Dad"}} {
		x.exec(`INSERT INTO entity_aliases(id, entity_id, alias, alias_key, created_at) VALUES (?,?,?,?,?)`, db.NewID("ali"), a.id, a.alias, strings.ToLower(a.alias), db.Now())
	}
	other := x.ent(x.owner, w.cid, "place", "Elm Street")
	w.src, w.dst, w.third, w.other = src.ID, dst.ID, third.ID, other.ID
	// Relationships: to the target (becomes a loop), to a third entity on both (a duplicate), and a plain one.
	x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: src.ID, ToEntityID: dst.ID, Kind: "family", Label: "twin of", Symmetric: true})
	w.srcRel = x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: src.ID, ToEntityID: third.ID, Kind: "family", Label: "child of"}).(entities.RelationItem).Item.ID
	w.dupRel = x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: dst.ID, ToEntityID: third.ID, Kind: "family", Label: "child of"}).(entities.RelationItem).Item.ID
	x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: other.ID, ToEntityID: src.ID, Kind: "custom", Label: "home of"})
	// Projects: one overrides only the source, one overrides both; one links the source from another Compendium's project.
	w.pOne, w.pTwo = x.project(x.owner, "One"), x.project(x.owner, "Two")
	for _, p := range []string{w.pOne, w.pTwo} {
		x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: p, CompendiumID: w.cid})
	}
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: w.pOne, EntityID: src.ID, Name: str("Ellie Prime"), Notes: str("one's notes")})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: w.pTwo, EntityID: src.ID, Summary: str("two on src")})
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: w.pTwo, EntityID: dst.ID, Summary: str("two on dst")})
	// A cameo link of the source from a third project, and a scene that names the source.
	p3 := x.project(x.owner, "Three")
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: p3, EntityID: src.ID})
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: p3, EntityID: dst.ID})
	d := x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: w.pOne, Title: "Scene", Kind: "scene"}).(docs.Item).Item
	w.docID = d.ID
	x.exec(`UPDATE documents SET pov_entity_id = ?, location_entity_id = ? WHERE id = ?`, src.ID, other.ID, d.ID)
	return w
}

// A dry run writes nothing at all (not a row, not an event), and the real
// merge applies exactly the plan the dry run reported.
func TestMergeDryRunEqualsTheRealMerge(t *testing.T) {
	x := newH(t)
	w := x.world()
	before := x.dump()
	in := entities.MergeIn{ID: w.src, IntoID: w.dst, DryRun: true}
	dry := x.call(x.owner, "entity.merge", in).(entities.MergeOut)
	if x.dump() != before {
		t.Fatal("a dry run changed the database")
	}
	p := dry.Plan
	if !dry.DryRun || dry.Merged || dry.Item != nil {
		t.Errorf("dry run flags: %+v", dry)
	}
	sort.Strings(p.AliasesMoved)
	if !reflect.DeepEqual(p.AliasesMoved, []string{"El"}) || !reflect.DeepEqual(p.AliasesDuplicate, []string{"Nell"}) ||
		len(p.AliasesBlocked) != 1 || p.AliasesBlocked[0].Alias != "Dad" || p.AliasesBlocked[0].EntityID != w.third || p.AliasesBlocked[0].Via != "alias" || p.NameAsAlias != "Ellie" {
		t.Errorf("aliases: %+v", p)
	}
	if !reflect.DeepEqual(p.FieldsTaken, []string{"wants"}) || len(p.FieldConflicts) != 2 || p.FieldConflicts[0].Key != "age" || p.FieldConflicts[1].Key != "role" {
		t.Errorf("fields: taken %v conflicts %+v", p.FieldsTaken, p.FieldConflicts)
	}
	if p.SummaryFrom != "source" || p.BodyFrom != "target" {
		t.Errorf("summary/body: %s %s", p.SummaryFrom, p.BodyFrom)
	}
	if p.RelationsRewired != 1 || len(p.RelationsDropped) != 2 {
		t.Errorf("relations: %+v", p)
	}
	reasons := map[string]string{}
	for _, d := range p.RelationsDropped {
		reasons[d.Reason] = d.ID
	}
	if reasons["duplicate"] != w.srcRel || reasons["self"] == "" {
		t.Errorf("dropped: %+v", p.RelationsDropped)
	}
	if p.OverridesMoved != 1 || len(p.OverrideConflicts) != 1 || p.OverrideConflicts[0].ProjectID != w.pTwo || p.LinksMoved != 0 || p.LinksDropped != 1 || p.SceneRefsRepointed != 1 {
		t.Errorf("overrides, links, scenes: %+v", p)
	}
	if !reflect.DeepEqual(p.Unresolved, []string{"age", "override:" + w.pTwo, "role"}) {
		t.Errorf("unresolved: %v", p.Unresolved)
	}

	// A real merge with conflicts nobody resolved is refused whole, and says what to answer.
	in.DryRun = false
	er := x.expect(x.owner, "entity.merge", in, ops.CodeInvalid)
	if er.Field != "field_choices" || er.Data["unresolved"] == nil {
		t.Errorf("refusal: %+v", er)
	}
	if x.dump() != before {
		t.Fatal("a refused merge changed the database")
	}

	// With every choice made, the dry run and the real merge agree.
	in.FieldChoices = map[string]string{"age": "target", "role": "source", "override:" + w.pTwo: "source"}
	in.Body = "append"
	in.DryRun = true
	dry2 := x.call(x.owner, "entity.merge", in).(entities.MergeOut)
	if len(dry2.Plan.Unresolved) != 0 || dry2.Plan.BodyFrom != "both" {
		t.Fatalf("resolved plan: %+v", dry2.Plan)
	}
	if x.dump() != before {
		t.Fatal("the second dry run changed the database")
	}
	in.DryRun = false
	real := x.call(x.owner, "entity.merge", in).(entities.MergeOut)
	if !real.Merged || real.Item == nil || real.DryRun {
		t.Fatalf("real: %+v", real)
	}
	if !reflect.DeepEqual(real.Plan, dry2.Plan) {
		t.Errorf("the real merge's plan differs from the dry run's:\n%+v\n%+v", real.Plan, dry2.Plan)
	}

	// Effects, one by one, against the plan.
	got := real.Item
	if got.ID != w.dst || got.Name != "Eleanor" || got.Summary != "From the source." || got.Body != "Target body.\n\nSource body." {
		t.Errorf("target: %+v", got)
	}
	wantFields := map[string]string{"age": "40", "voice": "quiet", "appearance": "tall", "role": "friend", "wants": "to leave"}
	if !reflect.DeepEqual(got.Fields, wantFields) {
		t.Errorf("fields: %v", got.Fields)
	}
	var aliases []string
	for _, a := range got.Aliases {
		aliases = append(aliases, a.Alias)
	}
	sort.Strings(aliases)
	if !reflect.DeepEqual(aliases, []string{"El", "Ellie", "Nell"}) {
		t.Errorf("aliases: %v", aliases)
	}
	if n := x.count(`SELECT count(*) FROM entities WHERE id = ?`, w.src); n != 0 {
		t.Error("the source remains")
	}
	if n := x.count(`SELECT count(*) FROM entity_aliases WHERE entity_id = ?`, w.src); n != 0 {
		t.Error("aliases of the source remain")
	}
	if d := x.get(x.owner, w.dst); len(d.Relations) != 2 { // child of Frank (the target's), home of (rewired); the twin loop and the duplicate are gone
		t.Errorf("relations: %+v", d.Relations)
	}
	if n := x.count(`SELECT count(*) FROM entity_relations WHERE from_entity_id = ? OR to_entity_id = ?`, w.src, w.src); n != 0 {
		t.Error("relations still touch the source")
	}
	// Overrides: pOne's moved, pTwo's kept the source's (as chosen).
	if n := x.count(`SELECT count(*) FROM project_entity_overrides WHERE entity_id = ?`, w.src); n != 0 {
		t.Error("overrides of the source remain")
	}
	if s := x.count(`SELECT count(*) FROM project_entity_overrides WHERE entity_id = ? AND project_id = ? AND name = 'Ellie Prime'`, w.dst, w.pOne); s != 1 {
		t.Error("pOne's override was not moved")
	}
	if s := x.count(`SELECT count(*) FROM project_entity_overrides WHERE entity_id = ? AND project_id = ? AND summary = 'two on src'`, w.dst, w.pTwo); s != 1 {
		t.Error("pTwo's chosen override was not kept")
	}
	if n := x.count(`SELECT count(*) FROM project_entity_links WHERE entity_id = ?`, w.src); n != 0 {
		t.Error("links to the source remain")
	}
	var pov, loc sql.NullString
	if err := x.d.Read().QueryRow(`SELECT pov_entity_id, location_entity_id FROM documents WHERE id = ?`, w.docID).Scan(&pov, &loc); err != nil {
		t.Fatal(err)
	}
	if pov.String != w.dst || loc.String != w.other {
		t.Errorf("scene references: %v %v", pov, loc)
	}
	// The old id still resolves, to the target, with the redirect marked.
	old := x.get(x.owner, w.src)
	if old.ID != w.dst || old.RedirectedFrom != w.src {
		t.Errorf("redirect: %+v", old.Entity)
	}
	// Other operations on the old id do not act on the target.
	x.expect(x.owner, "entity.update", entities.UpdateIn{ID: w.src, Summary: str("x")}, ops.CodeNotFound)
	x.expect(x.owner, "entity.trash", entities.StateIn{ID: w.src}, ops.CodeNotFound)
	// The search row follows.
	if n := x.count(`SELECT count(*) FROM entity_text WHERE entity_id = ? AND aliases LIKE '%Ellie%' AND body LIKE '%Source body%'`, w.dst); n != 1 {
		t.Error("the target's search text was not rewritten")
	}
	if n := x.count(`SELECT count(*) FROM entity_text WHERE entity_id = ?`, w.src); n != 0 {
		t.Error("the source's search text remains")
	}
	// The audit line has counts and ids, never names or text.
	var detail string
	if err := x.d.Read().QueryRow(`SELECT detail_json FROM audit_log WHERE action = 'entity.merge'`).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Ellie", "Eleanor", "Source body", "El\""} {
		if strings.Contains(detail, leak) {
			t.Errorf("the audit line contains %q: %s", leak, detail)
		}
	}
}

func TestMergeRules(t *testing.T) {
	x := newH(t)
	cid := x.compendium(x.owner, "World")
	a, b, c := x.ent(x.owner, cid, "character", "A"), x.ent(x.owner, cid, "character", "B"), x.ent(x.owner, cid, "character", "C")
	place := x.ent(x.owner, cid, "place", "P")
	other := x.compendium(x.owner, "Other")
	z := x.ent(x.owner, other, "character", "Z")
	x.expect(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: a.ID}, ops.CodeInvalid)
	x.expect(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: place.ID}, ops.CodeInvalid)
	x.expect(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: z.ID}, ops.CodeInvalid)
	x.expect(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: "ent_none"}, ops.CodeInvalid)
	x.expect(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: b.ID, FieldChoices: map[string]string{"x": "both"}}, ops.CodeInvalid)
	x.call(x.owner, "entity.trash", entities.StateIn{ID: b.ID})
	x.expect(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: b.ID}, ops.CodeConflict)
	x.call(x.owner, "entity.restore", entities.StateIn{ID: b.ID})
	// A reader cannot merge, not even to preview; a stranger gets a 404.
	x.member(cid, x.reader, "reader")
	x.expect(x.reader, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: b.ID, DryRun: true}, ops.CodeForbidden)
	x.expect(x.stranger, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: b.ID, DryRun: true}, ops.CodeNotFound)
	// A chain flattens: A into B, then B into C: A's old id leads to C.
	no := false
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: a.ID, IntoID: b.ID, NameAsAlias: &no})
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: b.ID, IntoID: c.ID})
	if got := x.get(x.owner, a.ID); got.ID != c.ID || got.RedirectedFrom != a.ID {
		t.Errorf("chain: %+v", got.Entity)
	}
	if n := x.count(`SELECT count(*) FROM entity_redirects WHERE to_id = ?`, c.ID); n != 2 {
		t.Errorf("redirects to the survivor: %d", n)
	}
	// A name_as_alias=false merge keeps no alias; the default keeps one.
	names := func(id string) (out []string) {
		for _, al := range x.get(x.owner, id).Aliases {
			out = append(out, al.Alias)
		}
		return
	}
	if got := names(c.ID); !reflect.DeepEqual(got, []string{"B"}) {
		t.Errorf("aliases of the survivor: %v", got)
	}
	// Redirects expire.
	x.exec(`UPDATE entity_redirects SET created_at = ? WHERE from_id = ?`, db.Format(db.NowTime().Add(-entities.RedirectTTL-time.Hour)), a.ID)
	x.expect(x.owner, "entity.get", entities.GetIn{ID: a.ID}, ops.CodeNotFound)
	// A merge step registered by a later feature takes part and is counted.
	var moved []string
	entities.RegisterMergeStep(entities.MergeStep{Name: "test-step",
		Count: func(_ ctxT, _ entities.Querier, s, d string) (int, error) { return 7, nil },
		Apply: func(tx *sql.Tx, s, d string) error { moved = append(moved, s+">"+d); return nil }})
	defer entities.RegisterMergeStep(entities.MergeStep{Name: "test-step", Count: func(ctxT, entities.Querier, string, string) (int, error) { return 0, nil }, Apply: func(*sql.Tx, string, string) error { return nil }})
	d1, d2 := x.ent(x.owner, cid, "character", "D1"), x.ent(x.owner, cid, "character", "D2")
	dry := x.call(x.owner, "entity.merge", entities.MergeIn{ID: d1.ID, IntoID: d2.ID, DryRun: true}).(entities.MergeOut)
	if dry.Plan.Extra["test-step"] != 7 || len(moved) != 0 {
		t.Errorf("dry run extra: %+v moved %v", dry.Plan.Extra, moved)
	}
	x.call(x.owner, "entity.merge", entities.MergeIn{ID: d1.ID, IntoID: d2.ID})
	if len(moved) != 1 || moved[0] != d1.ID+">"+d2.ID {
		t.Errorf("the merge step did not run: %v", moved)
	}
}

// A Compendium editor who cannot open a project must not learn, from a merge
// plan, that the project exists or overrode both entities: the plan names only
// projects the caller can open and counts the rest. A conflict they cannot see
// is settled in favour of the target's override, and the plan says how many.
func TestMergePlanDoesNotNameProjectsTheCallerCannotOpen(t *testing.T) {
	x := newH(t)
	w := x.world()
	x.member(w.cid, x.editor, "editor")
	in := entities.MergeIn{ID: w.src, IntoID: w.dst, DryRun: true, FieldChoices: map[string]string{"age": "target", "role": "source"}}
	dry := x.call(x.editor, "entity.merge", in).(entities.MergeOut).Plan
	if len(dry.OverrideConflicts) != 0 || dry.HiddenOverrideConflicts != 1 {
		t.Fatalf("the plan for someone who cannot open the project: %+v", dry.OverrideConflicts)
	}
	for _, k := range dry.Unresolved {
		if strings.Contains(k, w.pTwo) {
			t.Errorf("an unresolved key names the project: %v", dry.Unresolved)
		}
	}
	if len(dry.Unresolved) != 0 {
		t.Errorf("unresolved: %v", dry.Unresolved)
	}
	if raw, _ := json.Marshal(dry); strings.Contains(string(raw), w.pTwo) || strings.Contains(string(raw), w.pOne) {
		t.Errorf("the plan names a project: %s", raw)
	}
	in.DryRun = false
	real := x.call(x.editor, "entity.merge", in).(entities.MergeOut)
	if !real.Merged || real.Plan.HiddenOverrideConflicts != 1 || real.Plan.OverridesDropped != 1 {
		t.Errorf("real plan: %+v", real.Plan)
	}
	if n := x.count(`SELECT count(*) FROM project_entity_overrides WHERE entity_id = ? AND project_id = ? AND summary = 'two on dst'`, w.dst, w.pTwo); n != 1 {
		t.Error("the target's override in a project the caller cannot open was not kept")
	}
	if n := x.count(`SELECT count(*) FROM project_entity_overrides WHERE entity_id = ?`, w.src); n != 0 {
		t.Error("overrides of the source remain")
	}
	// The owner of the projects still sees them named.
	w2 := x.world()
	own := x.call(x.owner, "entity.merge", entities.MergeIn{ID: w2.src, IntoID: w2.dst, DryRun: true}).(entities.MergeOut).Plan
	if len(own.OverrideConflicts) != 1 || own.OverrideConflicts[0].ProjectID != w2.pTwo || own.HiddenOverrideConflicts != 0 {
		t.Errorf("the owner's plan: %+v", own)
	}
}

package entities_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/domain/timeline"
	"github.com/crueber/scriobh/internal/ops"
)

// ---- helpers --------------------------------------------------------------------------------------------------------

func (x *h) mergeIn(src, dst string) compendiums.MergeIn {
	return compendiums.MergeIn{ID: src, IntoID: dst}
}

func (x *h) previewMerge(p ops.Principal, in compendiums.MergeIn) entities.MovePlan {
	x.t.Helper()
	return x.call(p, "compendium.merge.preview", in).(compendiums.PlanOut).Plan
}

// merge previews and then applies, as a screen does.
func (x *h) merge(p ops.Principal, in compendiums.MergeIn) compendiums.ChangeOut {
	x.t.Helper()
	in.Fingerprint = x.previewMerge(p, in).Fingerprint
	return x.call(p, "compendium.merge", in).(compendiums.ChangeOut)
}

// moveDump is everything a move touches, as one string, for "nothing changed".
func (x *h) moveDump() string {
	x.t.Helper()
	var b strings.Builder
	b.WriteString(x.dump())
	for _, q := range []string{
		`SELECT id, compendium_id, from_entity_id, to_entity_id FROM entity_relations ORDER BY id`,
		`SELECT id, compendium_id, location_entity_id, revision FROM timeline_events ORDER BY id`,
		`SELECT event_id, entity_id, role FROM timeline_event_people ORDER BY event_id, entity_id`,
		`SELECT id, compendium_id, kind, key, type FROM entity_field_defs ORDER BY id`,
		`SELECT id, compendium_id FROM entities ORDER BY id`,
		`SELECT id, compendium_id, project_id FROM api_keys ORDER BY id`,
		`SELECT project_id, stamp FROM mention_state ORDER BY project_id`,
		`SELECT count(*) FROM compendium_changes`,
		`SELECT owner_id, target_compendium_id FROM compendium_refs ORDER BY owner_id, target_compendium_id`,
		`SELECT id, compendium_id FROM entity_text ORDER BY entity_id`,
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

// invariants are the rules a move must never break, whatever it did: a relationship joins two entities of its own
// Compendium, an event's people are entities of its Compendium, a search row names its entity's Compendium, a
// redirect names the Compendium of the entity it leads to, a project link names a live entity.
func (x *h) invariants() {
	x.t.Helper()
	for name, q := range map[string]string{
		"a relationship joins two Compendiums": `SELECT count(*) FROM entity_relations r JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
			WHERE f.compendium_id IS NOT r.compendium_id OR t.compendium_id IS NOT r.compendium_id`,
		"an event has a person from another Compendium": `SELECT count(*) FROM timeline_event_people p JOIN timeline_events e ON e.id = p.event_id JOIN entities n ON n.id = p.entity_id
			WHERE n.compendium_id IS NOT e.compendium_id`,
		"an event's place is in another Compendium": `SELECT count(*) FROM timeline_events e JOIN entities n ON n.id = e.location_entity_id WHERE n.compendium_id IS NOT e.compendium_id`,
		"a search row names the wrong Compendium":   `SELECT count(*) FROM entity_text t JOIN entities e ON e.id = t.entity_id WHERE e.compendium_id IS NOT t.compendium_id`,
		"an entity has no search row":               `SELECT count(*) FROM entities e WHERE e.compendium_id IS NOT NULL AND e.trashed_at IS NULL AND NOT EXISTS (SELECT 1 FROM entity_text t WHERE t.entity_id = e.id)`,
		"two live entities share a name":            `SELECT count(*) FROM (SELECT compendium_id, name_key FROM entities WHERE compendium_id IS NOT NULL AND trashed_at IS NULL GROUP BY 1, 2 HAVING count(*) > 1)`,
	} {
		var n int
		if err := x.d.Read().QueryRow(q).Scan(&n); err != nil {
			x.t.Fatalf("%s: %v", name, err)
		}
		if n != 0 {
			x.t.Errorf("invariant broken: %s (%d)", name, n)
		}
	}
}

func (x *h) str(q string, args ...any) string {
	x.t.Helper()
	var s string
	if err := x.d.Read().QueryRow(q, args...).Scan(&s); err != nil {
		x.t.Fatal(err)
	}
	return s
}

// bible is a Compendium with a small cast, a relationship, an event and a book that uses it.
type bible struct {
	src, dst, project, doc string
	ada, bea, cal          entities.Entity
	event                  string
	rel                    string
}

func (x *h) bibleWorld() bible {
	x.t.Helper()
	w := bible{}
	w.src = x.compendium(x.owner, "Source Bible")
	w.dst = x.compendium(x.owner, "Target Bible")
	x.call(x.owner, "entity.field.set", entities.FieldSetIn{ID: w.src, Kind: "character", Key: "mood", Label: "Mood", Type: "text"})
	w.ada = x.ent(x.owner, w.src, "character", "Ada")
	w.bea = x.ent(x.owner, w.src, "character", "Bea")
	w.cal = x.ent(x.owner, w.src, "place", "Calder")
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: w.ada.ID, Fields: map[string]any{"mood": "wry"}, Summary: ptr("The eldest.")})
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: w.ada.ID, Alias: "Addie"})
	w.rel = x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: w.ada.ID, ToEntityID: w.bea.ID, Kind: "ally"}).(entities.RelationItem).Item.ID
	ev := x.call(x.owner, "timeline.event.create", timeline.CreateIn{ID: w.src, Title: "They meet", StoryDate: "1990"}).(timeline.EventItem).Item
	w.event = ev.ID
	x.call(x.owner, "timeline.event.update", timeline.UpdateIn{ID: ev.ID, BaseRevision: ev.Revision, Participants: &[]timeline.Participant{{Entity: w.ada.ID}, {Entity: w.bea.ID}}})
	w.project = x.project(x.owner, "Book")
	x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: w.project, CompendiumID: w.src})
	w.doc = x.call(x.owner, "doc.create", docs.CreateIn{ProjectID: w.project, Title: "One", Content: "Ada met Bea at Calder. Addie smiled."}).(docs.Item).Item.ID
	x.call(x.owner, "project.entity.override.set", entities.OverrideSetIn{ID: w.project, EntityID: w.ada.ID, Summary: ptr("Book's Ada.")})
	x.call(x.owner, "timeline.scene.link", timeline.LinkIn{ID: w.doc, EventID: w.event})
	x.call(x.owner, "mention.rebuild", mentions.RebuildIn{ID: w.project})
	return w
}

// ---- the move --------------------------------------------------------------------------------------------------------

// A merge keeps every entity's id, and what hangs on the id follows it; what carries its own Compendium moves with it;
// the projects that used the source are told what they lose.
func TestMergeKeepsIdentityAndMovesWhatFollows(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	plan := x.previewMerge(x.owner, x.mergeIn(w.src, w.dst))
	if plan.Moving != 3 || plan.Collisions != 0 || len(plan.Unresolved) != 0 || len(plan.Blocking) != 0 {
		t.Fatalf("plan: moving %d, collisions %d, unresolved %v, blocking %v", plan.Moving, plan.Collisions, plan.Unresolved, plan.Blocking)
	}
	if plan.EventScenes != 1 {
		t.Errorf("the scene that tells the event goes with it, and the plan says so: %d", plan.EventScenes)
	}
	if plan.RelationsMoved != 1 || plan.EventsMoved != 1 || plan.AliasesMoved != 1 || plan.OverridesKept != 1 {
		t.Errorf("plan counts: %+v", plan)
	}
	if len(plan.FieldDefsCopied) != 1 || plan.FieldDefsCopied[0] != "character.mood" {
		t.Errorf("field definitions to copy: %v", plan.FieldDefsCopied)
	}
	if len(plan.Projects) != 1 || plan.Projects[0].ProjectID != w.project || plan.Projects[0].EntitiesLeave != 3 || plan.Projects[0].MentionRows == 0 || plan.Projects[0].OverridesDorm != 1 {
		t.Errorf("the project's effect: %+v", plan.Projects)
	}
	if plan.StaysBehind["members"] != 1 {
		t.Errorf("what stays in the source: %v", plan.StaysBehind)
	}
	// the preview wrote nothing
	before := x.moveDump()
	x.previewMerge(x.owner, x.mergeIn(w.src, w.dst))
	if x.moveDump() != before {
		t.Error("a preview changed something")
	}
	stampBefore := x.str(`SELECT stamp FROM mention_state WHERE project_id = ?`, w.project)

	out := x.merge(x.owner, x.mergeIn(w.src, w.dst))
	if out.ChangeID == "" || len(out.BeforeState.Moved) != 3 {
		t.Fatalf("the change: %+v", out)
	}
	for _, id := range []string{w.ada.ID, w.bea.ID, w.cal.ID} {
		if got := x.str(`SELECT compendium_id FROM entities WHERE id = ?`, id); got != w.dst {
			t.Errorf("%s is in %s, want the target", id, got)
		}
	}
	ada := x.get(x.owner, w.ada.ID)
	if ada.CompendiumID != w.dst || ada.Fields["mood"] != "wry" || len(ada.Aliases) != 1 || ada.Aliases[0].Alias != "Addie" || ada.Summary != "The eldest." {
		t.Errorf("Ada after the move: %+v", ada)
	}
	if ada.Revision != 4 && ada.Revision <= 1 {
		t.Errorf("the revision did not move: %d", ada.Revision)
	}
	if got := x.str(`SELECT compendium_id FROM entity_relations WHERE id = ?`, w.rel); got != w.dst {
		t.Errorf("the relationship is in %s", got)
	}
	if got := x.str(`SELECT compendium_id FROM timeline_events WHERE id = ?`, w.event); got != w.dst {
		t.Errorf("the event is in %s", got)
	}
	if n := x.count(`SELECT count(*) FROM timeline_event_people WHERE event_id = ?`, w.event); n != 2 {
		t.Errorf("the event kept %d people", n)
	}
	if n := x.count(`SELECT count(*) FROM entity_field_defs WHERE compendium_id = ? AND key = 'mood'`, w.dst); n != 1 {
		t.Errorf("the field definition was not copied: %d", n)
	}
	if n := x.count(`SELECT count(*) FROM project_entity_overrides WHERE project_id = ? AND entity_id = ?`, w.project, w.ada.ID); n != 1 {
		t.Error("the project's override must survive, dormant")
	}
	// search finds it in the target and not in the source
	hits := x.call(x.owner, "search.compendium", search.CompendiumQueryIn{ID: w.dst, Q: "Ada"})
	if !strings.Contains(fmt.Sprint(hits), w.ada.ID) {
		t.Errorf("the target's search does not find Ada: %v", hits)
	}
	if n := x.count(`SELECT count(*) FROM entities WHERE compendium_id = ? AND trashed_at IS NULL`, w.src); n != 0 {
		t.Errorf("%d live entities left in the source", n)
	}
	// the index workers are told: the stamp moved in the same transaction, and a rescan shows the project no longer sees them
	if stamp := x.str(`SELECT stamp FROM mention_state WHERE project_id = ?`, w.project); stamp == stampBefore {
		t.Error("the project's mention stamp did not move")
	}
	x.call(x.owner, "mention.rebuild", mentions.RebuildIn{ID: w.project})
	if n := x.count(`SELECT count(*) FROM entity_mentions WHERE project_id = ?`, w.project); n != 0 {
		t.Errorf("%d mention rows for entities the project can no longer see", n)
	}
	// the record, the events and the audit line carry ids and counts
	if n := x.count(`SELECT count(*) FROM compendium_changes WHERE id = ? AND kind = 'merge' AND source_id = ? AND target_id = ?`, out.ChangeID, w.src, w.dst); n != 1 {
		t.Error("the change was not recorded")
	}
	if n := x.count(`SELECT count(*) FROM events WHERE type = 'compendium.merged' AND compendium_id IN (?, ?)`, w.src, w.dst); n != 2 {
		t.Errorf("events: %d", n)
	}
	for _, q := range []string{`SELECT payload_json FROM events WHERE type = 'compendium.merged'`, `SELECT detail_json FROM audit_log WHERE action = 'compendium.merge'`} {
		rows, err := x.d.Read().Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			_ = rows.Scan(&s)
			for _, word := range []string{"Ada", "Bea", "Addie", "Calder", "wry"} {
				if strings.Contains(s, word) {
					t.Errorf("an event or audit line carries %q: %s", word, s)
				}
			}
		}
		rows.Close()
	}
	// a stale edit made against the old revision is refused: the entity moved under it
	e := x.expect(x.owner, "entity.update", entities.UpdateIn{ID: w.ada.ID, BaseRevision: ptr(ada.Revision - 1), Summary: ptr("stale")}, ops.CodeConflict)
	_ = e
	x.invariants()
}

// A name or alias the target already uses is the caller's decision, one entity at a time, and an unanswered one refuses
// the move; each answer does what it says.
func TestMergeNameClashesAreAnExplicitChoicePerEntity(t *testing.T) {
	x := newH(t)
	src, dst := x.compendium(x.owner, "Src"), x.compendium(x.owner, "Dst")
	adaS := x.ent(x.owner, src, "character", "Ada")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: adaS.ID, Alias: "Addie"})
	calS := x.ent(x.owner, src, "character", "Cal")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: calS.ID, Alias: "The Boss"})
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: calS.ID, Alias: "Calum"})
	eveS := x.ent(x.owner, src, "character", "Eve")
	danS := x.ent(x.owner, src, "character", "Dan")
	adaD := x.ent(x.owner, dst, "character", "Ada")
	bossD := x.ent(x.owner, dst, "character", "Boss Person")
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: bossD.ID, Alias: "The Boss"})
	x.ent(x.owner, dst, "character", "Eve")

	in := x.mergeIn(src, dst)
	plan := x.previewMerge(x.owner, in)
	if plan.Collisions != 3 || len(plan.Unresolved) != 3 {
		t.Fatalf("three entities clash and need an answer: %d collisions, unresolved %v", plan.Collisions, plan.Unresolved)
	}
	for _, it := range plan.Items {
		if it.EntityID == danS.ID && len(it.Clashes) != 0 {
			t.Errorf("Dan clashes with nothing: %+v", it.Clashes)
		}
	}
	// without answers the move is refused, and nothing changes
	before := x.moveDump()
	in.Fingerprint = plan.Fingerprint
	e := x.expect(x.owner, "compendium.merge", in, ops.CodeInvalid)
	if e.Field != "resolutions" || e.Data["unresolved"] == nil {
		t.Errorf("the refusal does not name what is unresolved: %+v", e)
	}
	if x.moveDump() != before {
		t.Error("a refused move changed something")
	}
	// a wrong answer is refused clearly
	in.Resolutions = map[string]entities.Resolution{adaS.ID: {Action: "rename", NewName: "Eve"}}
	x.expect(x.owner, "compendium.merge.preview", in, ops.CodeConflict)
	in.Resolutions = map[string]entities.Resolution{adaS.ID: {Action: "rename"}}
	x.expect(x.owner, "compendium.merge.preview", in, ops.CodeInvalid)
	// a new name that is the alias of another entity moving along is refused too
	in.Resolutions = map[string]entities.Resolution{adaS.ID: {Action: "rename", NewName: "Calum"}}
	x.expect(x.owner, "compendium.merge.preview", in, ops.CodeConflict)
	in.Resolutions = map[string]entities.Resolution{adaS.ID: {Action: "merge", IntoID: bossD.ID}}
	if p := x.previewMerge(x.owner, in); len(p.Unresolved) != 2 {
		t.Errorf("a merge into another name still clashes elsewhere: %v", p.Unresolved)
	}
	in.Resolutions = map[string]entities.Resolution{calS.ID: {Action: "drop_aliases"}, adaS.ID: {Action: "rename", NewName: "Ada the Elder"}, eveS.ID: {Action: "skip"}}
	plan = x.previewMerge(x.owner, in)
	if len(plan.Unresolved) != 0 || plan.Staying != 1 || plan.AliasesDropped != 1 {
		t.Fatalf("the plan with answers: unresolved %v, staying %d, dropped %d", plan.Unresolved, plan.Staying, plan.AliasesDropped)
	}
	in.Fingerprint = plan.Fingerprint
	out := x.call(x.owner, "compendium.merge", in).(compendiums.ChangeOut)
	if got := x.str(`SELECT name FROM entities WHERE id = ?`, adaS.ID); got != "Ada the Elder" {
		t.Errorf("rename: %q", got)
	}
	if got := x.str(`SELECT compendium_id FROM entities WHERE id = ?`, eveS.ID); got != src {
		t.Errorf("skip: Eve is in %s, want the source", got)
	}
	if n := x.count(`SELECT count(*) FROM entity_aliases WHERE entity_id = ?`, calS.ID); n != 1 {
		t.Errorf("drop_aliases: Cal has %d aliases, want the one that did not clash", n)
	}
	if len(out.BeforeState.AliasesDropped) != 1 || out.BeforeState.AliasesDropped[0]["alias"] != "The Boss" {
		t.Errorf("the dropped alias is not in the before-state: %v", out.BeforeState.AliasesDropped)
	}
	// Ada kept her alias; Dan moved untouched
	if n := x.count(`SELECT count(*) FROM entity_aliases WHERE entity_id = ? AND alias = 'Addie'`, adaS.ID); n != 1 {
		t.Error("Ada lost an alias that did not clash")
	}
	_ = adaD
	x.invariants()
}

// A clash settled by a merge is entity.merge's merge, across the two Compendiums: the source is deleted, its old id
// leads to the survivor, its name becomes an alias, and what it held is in the before-state.
func TestMergeResolvedByMergingTheEntities(t *testing.T) {
	x := newH(t)
	src, dst := x.compendium(x.owner, "Src"), x.compendium(x.owner, "Dst")
	adaS := x.ent(x.owner, src, "character", "Ada")
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: adaS.ID, Body: ptr("Source body."), Summary: ptr("")})
	x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: adaS.ID, Alias: "Addie"})
	friend := x.ent(x.owner, src, "character", "Friend")
	x.call(x.owner, "entity.relation.add", entities.RelationAddIn{ID: friend.ID, ToEntityID: adaS.ID, Kind: "ally"})
	adaD := x.ent(x.owner, dst, "character", "Ada")
	x.call(x.owner, "entity.update", entities.UpdateIn{ID: adaD.ID, Summary: ptr("Target summary.")})
	other := x.ent(x.owner, dst, "place", "Elsewhere")

	in := x.mergeIn(src, dst)
	in.Resolutions = map[string]entities.Resolution{adaS.ID: {Action: "merge", IntoID: adaD.ID}}
	x.expect(x.owner, "compendium.merge.preview", func() compendiums.MergeIn {
		bad := in
		bad.Resolutions = map[string]entities.Resolution{adaS.ID: {Action: "merge", IntoID: other.ID}}
		return bad
	}(), ops.CodeInvalid) // a character cannot be merged into a place
	out := x.merge(x.owner, in)
	if n := x.count(`SELECT count(*) FROM entities WHERE id = ?`, adaS.ID); n != 0 {
		t.Error("the merged entity must be gone")
	}
	if got := x.str(`SELECT to_id FROM entity_redirects WHERE from_id = ?`, adaS.ID); got != adaD.ID {
		t.Errorf("redirect leads to %s", got)
	}
	if got := x.str(`SELECT compendium_id FROM entity_redirects WHERE from_id = ?`, adaS.ID); got != dst {
		t.Errorf("the redirect belongs to %s", got)
	}
	ada := x.get(x.owner, adaD.ID)
	if ada.Summary != "Target summary." || !strings.Contains(ada.Body, "Source body.") && ada.Body != "Source body." || len(ada.Aliases) != 1 {
		t.Errorf("the survivor: summary %q body %q aliases %v", ada.Summary, ada.Body, ada.Aliases)
	}
	if got := x.str(`SELECT to_entity_id FROM entity_relations WHERE from_entity_id = ?`, friend.ID); got != adaD.ID {
		t.Errorf("the relationship was not rewired to the survivor: %s", got)
	}
	if len(out.BeforeState.Merged) != 1 || out.BeforeState.Merged[0].Snapshot.Body != "Source body." || out.BeforeState.Merged[0].IntoID != adaD.ID {
		t.Errorf("the before-state does not hold the merged sheet: %+v", out.BeforeState.Merged)
	}
	x.invariants()
}

// The fingerprint ties the change to the preview: anything that changes the plan in between refuses it, and so does
// leaving it out.
func TestMergeFingerprintRefusesAChangedPlan(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	in := x.mergeIn(w.src, w.dst)
	x.expect(x.owner, "compendium.merge", in, ops.CodeConflict) // no fingerprint
	in.Fingerprint = x.previewMerge(x.owner, in).Fingerprint
	// someone adds an entity to the source (and one to the target that clashes) after the preview
	x.ent(x.owner, w.src, "item", "Late arrival")
	before := x.moveDump()
	e := x.expect(x.owner, "compendium.merge", in, ops.CodeConflict)
	if e.Data["reason"] != "plan_changed" || e.Data["fingerprint"] == nil {
		t.Errorf("the refusal: %+v", e)
	}
	if x.moveDump() != before {
		t.Error("a refused move changed something")
	}
	in.Fingerprint = e.Data["fingerprint"].(string)
	x.call(x.owner, "compendium.merge", in)
	x.invariants()
}

// A failure halfway through leaves nothing changed: the whole move is one transaction.
func TestMergeIsAtomic(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	boom := x.ent(x.owner, w.src, "character", "Zed Boom") // last in id order: the others have moved when it fails
	if _, err := x.d.Writer().Exec(`CREATE TRIGGER move_fails BEFORE UPDATE OF compendium_id ON entities WHEN NEW.id = '` + boom.ID + `' AND NEW.compendium_id <> OLD.compendium_id
		BEGIN SELECT RAISE(ABORT, 'a failure halfway'); END`); err != nil {
		t.Fatal(err)
	}
	in := x.mergeIn(w.src, w.dst)
	in.Fingerprint = x.previewMerge(x.owner, in).Fingerprint
	before := x.moveDump()
	if _, err := x.reg.Call(x.ctx, x.owner, "compendium.merge", in); err == nil {
		t.Fatal("the move should have failed")
	}
	if got := x.moveDump(); got != before {
		t.Errorf("a failed move left changes behind:\n%s", firstDiff(before, got))
	}
	// the same for a split
	if _, err := x.d.Writer().Exec(`DROP TRIGGER move_fails`); err != nil {
		t.Fatal(err)
	}
	if _, err := x.d.Writer().Exec(`CREATE TRIGGER split_fails BEFORE INSERT ON compendium_changes BEGIN SELECT RAISE(ABORT, 'the last step fails'); END`); err != nil {
		t.Fatal(err)
	}
	sp := compendiums.SplitIn{ID: w.src, Name: "Splitting", MoveOpts: compendiums.MoveOpts{EntityIDs: []string{w.ada.ID, w.bea.ID}}}
	sp.Fingerprint = x.call(x.owner, "compendium.split.preview", sp).(compendiums.PlanOut).Plan.Fingerprint
	before = x.moveDump()
	nComp := x.count(`SELECT count(*) FROM compendiums`)
	if _, err := x.reg.Call(x.ctx, x.owner, "compendium.split", sp); err == nil {
		t.Fatal("the split should have failed")
	}
	if got := x.moveDump(); got != before {
		t.Errorf("a failed split left changes behind:\n%s", firstDiff(before, got))
	}
	if n := x.count(`SELECT count(*) FROM compendiums`); n != nComp {
		t.Errorf("a failed split left a Compendium behind (%d, was %d)", n, nComp)
	}
	x.invariants()
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return "- " + al[i] + "\n+ " + bl[i]
		}
	}
	return fmt.Sprintf("lengths %d and %d", len(al), len(bl))
}

// Moving some of a set of related entities would join two Compendiums with a relationship or split an event: refused
// unless the caller says drop, and what is dropped is in the before-state.
func TestMovePartialRefusesCrossingUnlessToldToDrop(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	in := compendiums.SplitIn{ID: w.src, Name: "Just Ada", MoveOpts: compendiums.MoveOpts{EntityIDs: []string{w.ada.ID}}}
	plan := x.call(x.owner, "compendium.split.preview", in).(compendiums.PlanOut).Plan
	if len(plan.RelationsCrossing) != 1 || len(plan.EventsCrossing) != 1 || len(plan.Blocking) < 2 {
		t.Fatalf("crossing: %d relationships, %d events, blocking %v", len(plan.RelationsCrossing), len(plan.EventsCrossing), plan.Blocking)
	}
	in.Fingerprint = plan.Fingerprint
	before := x.moveDump()
	e := x.expect(x.owner, "compendium.split", in, ops.CodeConflict)
	if e.Data["reason"] != "blocked" {
		t.Errorf("the refusal: %+v", e)
	}
	if x.moveDump() != before {
		t.Error("a refused split changed something")
	}
	in.Crossing = "drop"
	in.Fingerprint = x.call(x.owner, "compendium.split.preview", in).(compendiums.PlanOut).Plan.Fingerprint
	out := x.call(x.owner, "compendium.split", in).(compendiums.ChangeOut)
	if out.TargetID == "" || out.TargetID == w.src {
		t.Fatalf("no new Compendium: %q", out.TargetID)
	}
	if got := x.str(`SELECT compendium_id FROM entities WHERE id = ?`, w.ada.ID); got != out.TargetID {
		t.Errorf("Ada is in %s", got)
	}
	if got := x.str(`SELECT compendium_id FROM entities WHERE id = ?`, w.bea.ID); got != w.src {
		t.Errorf("Bea moved: %s", got)
	}
	if n := x.count(`SELECT count(*) FROM entity_relations WHERE id = ?`, w.rel); n != 0 {
		t.Error("the crossing relationship was not dropped")
	}
	if len(out.BeforeState.RelationsDropped) != 1 || out.BeforeState.RelationsDropped[0]["id"] != w.rel {
		t.Errorf("before-state: %v", out.BeforeState.RelationsDropped)
	}
	if n := x.count(`SELECT count(*) FROM timeline_event_people WHERE event_id = ? AND entity_id = ?`, w.event, w.ada.ID); n != 0 {
		t.Error("Ada is still a person of an event that stayed in the source")
	}
	if len(out.BeforeState.PeopleDropped) != 1 {
		t.Errorf("the dropped person is not in the before-state: %v", out.BeforeState.PeopleDropped)
	}
	if got := x.str(`SELECT compendium_id FROM timeline_events WHERE id = ?`, w.event); got != w.src {
		t.Errorf("the split event moved: %s", got)
	}
	x.invariants()
}

// The new Compendium of a split is owned by the caller; the source stays.
func TestSplitMakesANewCompendiumOwnedByTheCaller(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	in := compendiums.SplitIn{ID: w.src, Name: "Places", MoveOpts: compendiums.MoveOpts{EntityIDs: []string{w.cal.ID}}}
	x.expect(x.owner, "compendium.split", in, ops.CodeConflict) // no fingerprint
	in.EntityIDs = nil
	x.expect(x.owner, "compendium.split.preview", in, ops.CodeInvalid) // nothing chosen
	in.EntityIDs = []string{w.cal.ID}
	plan := x.call(x.owner, "compendium.split.preview", in).(compendiums.PlanOut).Plan
	if plan.TargetID != "" || plan.TargetName != "Places" || plan.Moving != 1 {
		t.Errorf("the preview of a split: %+v", plan)
	}
	in.Fingerprint = plan.Fingerprint
	out := x.call(x.owner, "compendium.split", in).(compendiums.ChangeOut)
	d := x.call(x.owner, "compendium.get", compendiums.GetIn{ID: out.TargetID}).(compendiums.DetailItem).Item
	if d.Name != "Places" || d.Role != "owner" {
		t.Errorf("the new Compendium: %+v", d)
	}
	if got := x.str(`SELECT compendium_id FROM entities WHERE id = ?`, w.cal.ID); got != out.TargetID {
		t.Errorf("Calder is in %s", got)
	}
	// the source's project still uses the source, and lost sight of the place only
	list := x.call(x.owner, "compendium.change.list", compendiums.ChangeIn{ID: w.src}).(compendiums.ChangeListOut)
	if len(list.Items) != 1 || list.Items[0].Kind != "split" {
		t.Errorf("changes: %+v", list.Items)
	}
	got := x.call(x.owner, "compendium.change.get", compendiums.ChangeGetIn{ID: out.TargetID, ChangeID: out.ChangeID}).(compendiums.ChangeItem).Item
	if !strings.Contains(string(got.Before), w.cal.ID) {
		t.Errorf("the record does not hold the before-state: %s", got.Before)
	}
	// another person's change is not found
	x.expect(x.stranger, "compendium.change.get", compendiums.ChangeGetIn{ID: out.TargetID, ChangeID: out.ChangeID}, ops.CodeNotFound)
	x.invariants()
}

// A key's pin to the source is not re-pointed unless asked: that would widen it to the target's other material.
func TestMoveAndKeyPins(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	mine := x.call(x.owner, "key.create", keys.CreateIn{Name: "agent", Scope: "write", CompendiumID: w.src}).(keys.CreateOut).Item
	// a second person on the source, not on the target, with their own key
	x.call(x.owner, "compendium.member.add", compendiums.MemberAddIn{ID: w.src, Email: x.editor.Email, Role: "editor"})
	theirs := x.call(x.editor, "key.create", keys.CreateIn{Name: "their agent", Scope: "write", CompendiumID: w.src}).(keys.CreateOut).Item

	in := x.mergeIn(w.src, w.dst)
	plan := x.previewMerge(x.owner, in)
	if len(plan.Keys) != 1 || plan.Keys[0].KeyID != mine.ID || !plan.Keys[0].Mine || plan.HiddenKeys != 1 || len(plan.Blocking) != 1 {
		t.Fatalf("the keys in the plan: %+v hidden %d blocking %v", plan.Keys, plan.HiddenKeys, plan.Blocking)
	}
	in.Fingerprint = plan.Fingerprint
	if e := x.expect(x.owner, "compendium.merge", in, ops.CodeConflict); !strings.Contains(e.Message, "API keys are pinned") {
		t.Errorf("the refusal is not clear: %s", e.Message)
	}
	// leave: the pins stay
	in.KeyPins = "leave"
	out := x.merge(x.owner, in)
	if got := x.str(`SELECT compendium_id FROM api_keys WHERE id = ?`, mine.ID); got != w.src || len(out.BeforeState.KeysRepointed) != 0 {
		t.Errorf("leave moved a pin: %s", got)
	}
	// repoint, on a fresh pair: the owner's key follows, the other person's (not on the target) stays where it was
	src2, dst2 := x.compendium(x.owner, "Src2"), x.compendium(x.owner, "Dst2")
	x.ent(x.owner, src2, "item", "Thing")
	x.call(x.owner, "compendium.member.add", compendiums.MemberAddIn{ID: src2, Email: x.editor.Email, Role: "editor"})
	k1 := x.call(x.owner, "key.create", keys.CreateIn{Name: "k1", Scope: "write", CompendiumID: src2}).(keys.CreateOut).Item
	k2 := x.call(x.editor, "key.create", keys.CreateIn{Name: "k2", Scope: "write", CompendiumID: src2}).(keys.CreateOut).Item
	in2 := x.mergeIn(src2, dst2)
	in2.KeyPins = "repoint"
	out2 := x.merge(x.owner, in2)
	if got := x.str(`SELECT compendium_id FROM api_keys WHERE id = ?`, k1.ID); got != dst2 {
		t.Errorf("the owner's key was not repointed: %s", got)
	}
	if got := x.str(`SELECT compendium_id FROM api_keys WHERE id = ?`, k2.ID); got != src2 {
		t.Errorf("a key whose owner is not on the target was widened: %s", got)
	}
	if len(out2.BeforeState.KeysRepointed) != 1 {
		t.Errorf("repointed in the before-state: %v", out2.BeforeState.KeysRepointed)
	}
	_ = theirs
	x.invariants()
}

// Who may move what: owner of both, never a key, never into or out of the trash.
func TestMoveAccessAndTrash(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	in := x.mergeIn(w.src, w.dst)
	fp := x.previewMerge(x.owner, in).Fingerprint
	in.Fingerprint = fp
	// strangers see nothing; an editor of the source is not enough; an owner of the source who is only an editor of the target is not enough
	x.expect(x.stranger, "compendium.merge.preview", in, ops.CodeNotFound)
	x.call(x.owner, "compendium.member.add", compendiums.MemberAddIn{ID: w.src, Email: x.editor.Email, Role: "editor"})
	x.expect(x.editor, "compendium.merge.preview", in, ops.CodeForbidden)
	otherOwner := x.compendium(x.editor, "Editor's own")
	x.call(x.editor, "compendium.member.add", compendiums.MemberAddIn{ID: otherOwner, Email: x.owner.Email, Role: "editor"})
	x.expect(x.owner, "compendium.merge.preview", x.mergeIn(w.src, otherOwner), ops.CodeForbidden)
	x.expect(x.owner, "compendium.merge.preview", x.mergeIn(w.src, "cmp_00000000000000000000000000"), ops.CodeNotFound)
	x.expect(x.owner, "compendium.merge.preview", x.mergeIn(w.src, w.src), ops.CodeInvalid)
	// a key is refused, pinned or not
	pinned := ops.Principal{UserID: x.owner.UserID, Role: ops.RoleWriter, Via: "apikey", Scope: ops.ScopeAdmin, CompendiumID: w.src, KeyID: "key_x"}
	x.expect(pinned, "compendium.merge.preview", in, ops.CodeForbidden)
	// the administrator acts as the owner
	x.call(x.admin, "compendium.merge.preview", in)
	// a Compendium in the trash, either side
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: w.dst})
	e := x.expect(x.owner, "compendium.merge", in, ops.CodeConflict)
	if e.Data["reason"] != ops.ReasonCompendiumTrashed {
		t.Errorf("target in the trash: %+v", e)
	}
	x.call(x.owner, "compendium.restore", compendiums.StateIn{ID: w.dst})
	x.call(x.owner, "compendium.trash", compendiums.StateIn{ID: w.src})
	e = x.expect(x.owner, "compendium.merge", in, ops.CodeConflict)
	if e.Data["reason"] != ops.ReasonCompendiumTrashed {
		t.Errorf("source in the trash: %+v", e)
	}
	x.call(x.owner, "compendium.restore", compendiums.StateIn{ID: w.src})
	// a project in the trash is read-only, for the primary change; and a merge does not follow into one
	x.call(x.owner, "project.trash", projectState(w.project))
	pi := compendiums.PrimaryIn{ID: w.project, CompendiumID: w.dst}
	x.expect(x.owner, "compendium.primary.set", pi, ops.CodeConflict)
	in3 := x.mergeIn(w.src, w.dst)
	in3.FollowProjects = true
	plan := x.previewMerge(x.owner, in3)
	if len(plan.Projects) != 1 || !strings.Contains(plan.Projects[0].Follow, "trash") {
		t.Errorf("a trashed project must be skipped: %+v", plan.Projects)
	}
	in3.Fingerprint = plan.Fingerprint
	x.call(x.owner, "compendium.merge", in3)
	if n := x.count(`SELECT count(*) FROM compendium_refs WHERE owner_id = ?`, w.project); n != 0 {
		t.Error("a reference was added to a project in the trash")
	}
}

// follow_projects gives the projects the caller owns a read-only reference to the target, so they still see what moved.
func TestMoveFollowProjects(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	in := x.mergeIn(w.src, w.dst)
	in.FollowProjects = true
	plan := x.previewMerge(x.owner, in)
	if len(plan.Projects) != 1 || plan.Projects[0].Follow != "reference added" {
		t.Fatalf("plan: %+v", plan.Projects)
	}
	out := x.merge(x.owner, in)
	if len(out.BeforeState.RefsAdded) != 1 {
		t.Errorf("refs added: %v", out.BeforeState.RefsAdded)
	}
	list := x.call(x.owner, "project.entity.list", entities.ProjectListIn{ID: w.project}).(entities.ProjectListOut)
	if len(list.Items) != 3 {
		t.Errorf("the project sees %d entities through the reference, want 3", len(list.Items))
	}
	x.invariants()
}

// Edits made while a move runs. The move is one write transaction and the database has one writer, so an edit is either
// entirely before the move (it changes the plan, or not) or entirely after it; nothing lands in between. Each order is
// driven here without timing: an edit before the move is made before it, and the edits "during" it are started from a
// hook inside the move's transaction, where they can only wait for it to commit.
func TestMoveWithEditsBeforeAndAfter(t *testing.T) {
	t.Run("an edit that changes the plan, made after the preview, refuses the move; a retry applies", func(t *testing.T) {
		x := newH(t)
		w := x.bibleWorld()
		in := x.mergeIn(w.src, w.dst)
		in.Fingerprint = x.previewMerge(x.owner, in).Fingerprint
		x.call(x.owner, "entity.alias.add", entities.AliasAddIn{ID: w.bea.ID, Alias: "Bee"}) // one more alias moves
		before := x.moveDump()
		e := x.expect(x.owner, "compendium.merge", in, ops.CodeConflict)
		if e.Data["reason"] != "plan_changed" || e.Data["fingerprint"] == nil {
			t.Fatalf("the refusal: %+v", e)
		}
		if x.moveDump() != before {
			t.Error("a refused move changed something")
		}
		in.Fingerprint = e.Data["fingerprint"].(string)
		out := x.call(x.owner, "compendium.merge", in).(compendiums.ChangeOut)
		if out.Plan.AliasesMoved != 2 {
			t.Errorf("the plan that was applied moves %d aliases, want 2", out.Plan.AliasesMoved)
		}
		x.invariants()
	})
	t.Run("an edit that does not change the plan, made after the preview, is carried along", func(t *testing.T) {
		x := newH(t)
		w := x.bibleWorld()
		in := x.mergeIn(w.src, w.dst)
		in.Fingerprint = x.previewMerge(x.owner, in).Fingerprint
		x.call(x.owner, "entity.update", entities.UpdateIn{ID: w.ada.ID, Summary: ptr("edited after the preview")})
		x.call(x.owner, "compendium.merge", in)
		if got := x.get(x.owner, w.ada.ID); got.Summary != "edited after the preview" || got.CompendiumID != w.dst {
			t.Errorf("Ada after the move: %q in %s", got.Summary, got.CompendiumID)
		}
		x.invariants()
	})
	t.Run("edits started inside the move's transaction run after it and land in the target", func(t *testing.T) {
		x := newH(t)
		w := x.bibleWorld()
		in := x.mergeIn(w.src, w.dst)
		in.Fingerprint = x.previewMerge(x.owner, in).Fingerprint
		const writers = 6
		var wg sync.WaitGroup
		errs := make([]error, writers*2)
		remove := entities.SetMoveHook(func() {
			for i := 0; i < writers; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, errs[i*2] = x.reg.Call(x.ctx, x.owner, "entity.update", entities.UpdateIn{ID: w.ada.ID, Summary: ptr(fmt.Sprintf("edit %d", i))})
					_, errs[i*2+1] = x.reg.Call(x.ctx, x.owner, "entity.alias.add", entities.AliasAddIn{ID: w.bea.ID, Alias: fmt.Sprintf("Bee %d", i)})
				}(i)
			}
		})
		defer remove()
		if _, err := x.reg.Call(x.ctx, x.owner, "compendium.merge", in); err != nil {
			t.Fatalf("the move: %v", err) // the writers could not run before it, so its plan cannot have changed
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("edit %d, made while the move held the writer, failed: %v", i, err)
			}
		}
		for _, id := range []string{w.ada.ID, w.bea.ID, w.cal.ID} {
			if got := x.str(`SELECT compendium_id FROM entities WHERE id = ?`, id); got != w.dst {
				t.Errorf("%s is in %s after the move", id, got)
			}
		}
		if n := x.count(`SELECT count(*) FROM entity_aliases WHERE entity_id = ? AND alias LIKE 'Bee %'`, w.bea.ID); n != writers {
			t.Errorf("%d of %d aliases added during the move are there", n, writers)
		}
		if got := x.get(x.owner, w.ada.ID).Summary; !strings.HasPrefix(got, "edit ") {
			t.Errorf("Ada's summary: %q", got)
		}
		x.invariants()
	})
	t.Run("an edit made with a revision from before the move is refused after it", func(t *testing.T) {
		x := newH(t)
		w := x.bibleWorld()
		old := x.get(x.owner, w.ada.ID).Revision
		x.merge(x.owner, x.mergeIn(w.src, w.dst))
		x.expect(x.owner, "entity.update", entities.UpdateIn{ID: w.ada.ID, BaseRevision: ptr(old), Summary: ptr("stale")}, ops.CodeConflict)
		x.invariants()
	})
}

// ---- the primary Compendium ---------------------------------------------------------------------------------------

func projectState(id string) projects.StateIn { return projects.StateIn{ID: id} }

// Changing a project's primary Compendium says what it does to mentions, links and key pins, and needs the preview.
func TestPrimaryPreviewAndSet(t *testing.T) {
	x := newH(t)
	w := x.bibleWorld()
	cameo := x.ent(x.owner, w.dst, "character", "Cameo")
	x.call(x.owner, "project.entity.link.add", entities.LinkIn{ID: w.project, EntityID: cameo.ID}) // a cameo from the new primary
	key := x.call(x.owner, "key.create", keys.CreateIn{Name: "book agent", Scope: "write", ProjectID: w.project}).(keys.CreateOut).Item
	x.call(x.owner, "compendium.member.add", compendiums.MemberAddIn{ID: w.src, Email: x.editor.Email, Role: "editor"})
	x.call(x.editor, "key.create", keys.CreateIn{Name: "other agent", Scope: "write", CompendiumID: w.src}) // pinned to a Compendium: unaffected

	pi := compendiums.PrimaryIn{ID: w.project, CompendiumID: w.dst}
	pv := x.call(x.owner, "compendium.primary.preview", pi).(compendiums.PrimaryOut).Plan
	if !pv.Changed || pv.EntitiesLeaving != 3 || pv.PreviousCompendiumID != w.src || pv.MentionRowsLeaving == 0 {
		t.Errorf("preview: %+v", pv)
	}
	if pv.LinksRedundant != 1 || len(pv.Keys) != 1 || pv.Keys[0].KeyID != key.ID {
		t.Errorf("links %d, keys %+v", pv.LinksRedundant, pv.Keys)
	}
	if pv.OverridesDormant != 1 {
		t.Errorf("overrides dormant: %d", pv.OverridesDormant)
	}
	// no fingerprint: refused; a stale one: refused
	before := x.moveDump()
	x.expect(x.owner, "compendium.primary.set", pi, ops.CodeConflict)
	pi.Fingerprint = pv.Fingerprint
	x.call(x.owner, "project.entity.override.clear", entities.OverrideClearIn{ID: w.project, EntityID: w.ada.ID}) // changes the plan
	e := x.expect(x.owner, "compendium.primary.set", pi, ops.CodeConflict)
	if e.Data["reason"] != "plan_changed" {
		t.Errorf("stale fingerprint: %+v", e)
	}
	_ = before
	pi.Fingerprint = e.Data["fingerprint"].(string)
	stamp := x.str(`SELECT stamp FROM mention_state WHERE project_id = ?`, w.project)
	out := x.call(x.owner, "compendium.primary.set", pi).(compendiums.PrimaryOut).Plan
	if !out.Changed || out.CompendiumID != w.dst {
		t.Errorf("set: %+v", out)
	}
	if got := x.str(`SELECT compendium_id FROM projects WHERE id = ?`, w.project); got != w.dst {
		t.Errorf("primary is %s", got)
	}
	if x.str(`SELECT stamp FROM mention_state WHERE project_id = ?`, w.project) == stamp {
		t.Error("the mention index was not told")
	}
	// the older op still works, and previews with dry_run
	back := x.call(x.owner, "project.compendium.set", compendiums.SetIn{ID: w.project, CompendiumID: w.src, DryRun: true}).(compendiums.SetOut)
	if !back.Changed || back.EntitiesEntering != 3 {
		t.Errorf("project.compendium.set dry run: %+v", back)
	}
	// only the project's owner, and never a key
	x.expect(x.editor, "compendium.primary.set", compendiums.PrimaryIn{ID: w.project, CompendiumID: w.src}, ops.CodeNotFound)
	pinned := ops.Principal{UserID: x.owner.UserID, Role: ops.RoleWriter, Via: "apikey", Scope: ops.ScopeAdmin, ProjectID: w.project, KeyID: "key_x"}
	x.expect(pinned, "compendium.primary.preview", pi, ops.CodeForbidden)
	x.invariants()
}

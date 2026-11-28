package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Merging folds one entity (the source) into another (the target) of the same
// kind in the same Compendium. One function, planMerge, works out everything
// that would change; a dry run returns that plan, and the real merge applies
// the very same value in one transaction, so the preview cannot disagree with
// the result.
//
// The rules, all of them reported in the plan:
//   - the target keeps its name, sort name, colour and id; the source's name
//     becomes an alias of the target (unless it already is one);
//   - the source's aliases move to the target, except those the target already
//     has and those a third entity uses (reported, dropped);
//   - custom fields the source has and the target lacks are copied; where both
//     have a different value, the request must say which to keep, per field,
//     and a merge with an unresolved conflict is refused, never guessed;
//   - summary and body stay the target's (a request can choose the source's, or
//     append the source's body); an empty target field takes the source's;
//   - relationships are rewired to the target; ones that would loop back on it
//     or duplicate an existing one are dropped;
//   - project overrides and links move to the target; a project that overrode
//     both needs an explicit choice (override:<project id>);
//   - scenes that named the source as point of view or location now name the
//     target; later features add their own steps (RegisterMergeStep);
//   - the source row is deleted and a redirect from its id to the target is
//     kept for a year, with a snapshot of what the source held.

// MergeStep lets a feature that hangs rows on an entity (mentions, timeline
// participants) take part in a merge. Count reports how many rows would move,
// for the plan; Apply moves them inside the merge's transaction. Register
// steps once, at start.
type MergeStep struct {
	Name  string
	Count func(ctx context.Context, q Querier, sourceID, targetID string) (int, error)
	Apply func(tx *sql.Tx, sourceID, targetID string) error
}

var (
	stepsMu    sync.RWMutex
	mergeSteps []MergeStep
)

// RegisterMergeStep adds a merge step. A step with the same Name replaces the
// earlier one.
func RegisterMergeStep(s MergeStep) {
	stepsMu.Lock()
	defer stepsMu.Unlock()
	for i := range mergeSteps {
		if mergeSteps[i].Name == s.Name {
			mergeSteps[i] = s
			return
		}
	}
	mergeSteps = append(mergeSteps, s)
	sort.Slice(mergeSteps, func(i, j int) bool { return mergeSteps[i].Name < mergeSteps[j].Name })
}

func steps() []MergeStep {
	stepsMu.RLock()
	defer stepsMu.RUnlock()
	return append([]MergeStep(nil), mergeSteps...)
}

func registerMerge(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "entity.merge", Permanent: true, Method: "POST", Path: "/api/v1/entities/{id}/merge",
		Desc: "Merge this entity (the source) into another of the same kind in the same Compendium (into_id). With dry_run it writes nothing and returns the plan: aliases moved, dropped and blocked, fields copied and in conflict, relationships rewired or dropped, overrides and links moved, scenes repointed. " +
			"The real merge applies that same plan in one transaction, deletes the source, and leaves a redirect from its old id to the target for a year. " +
			"Where both entities hold a different value for a custom field, field_choices must say source or target for each (and override:<project id> where a project overrode both); an unresolved conflict refuses the merge. Editor.",
		Auth: via(compaccess.Editor),
	}, merge)
}

// MergeIn merges the entity in the path into into_id.
type MergeIn struct {
	ID           string            `path:"id" json:"-" desc:"The source entity: it is deleted by the merge."`
	IntoID       string            `json:"into_id" ref:"entity" desc:"The target entity: it survives."`
	DryRun       bool              `json:"dry_run,omitempty" desc:"Write nothing; return the plan."`
	FieldChoices map[string]string `json:"field_choices,omitempty" desc:"Per conflicting custom field key, source or target; override:<project id> for a project that overrode both."`
	Summary      string            `json:"summary,omitempty" enum:"target,source" desc:"Which summary survives. Default target."`
	Body         string            `json:"body,omitempty" enum:"target,source,append" desc:"Which body survives, or append the source's below the target's. Default target."`
	NameAsAlias  *bool             `json:"name_as_alias,omitempty" desc:"Keep the source's name as an alias of the target. Default true."`
}

// EntityID lets the registry find the owning Compendium.
func (in MergeIn) EntityID() string { return in.ID }

// Validate checks the shape.
func (in *MergeIn) Validate() error {
	if in.IntoID == "" {
		return ops.Invalid("into_id", "Say which entity to merge into.")
	}
	if in.IntoID == in.ID {
		return ops.Invalid("into_id", "An entity cannot be merged into itself.")
	}
	switch in.Summary {
	case "", "target", "source":
	default:
		return ops.Invalid("summary", "Summary is target or source.")
	}
	switch in.Body {
	case "", "target", "source", "append":
	default:
		return ops.Invalid("body", "Body is target, source or append.")
	}
	for k, v := range in.FieldChoices {
		if v != "source" && v != "target" {
			return ops.Invalid("field_choices."+k, "A choice is source or target.")
		}
	}
	return nil
}

// FieldConflict is a custom field both entities hold, differently.
type FieldConflict struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Choice   string `json:"choice" desc:"What the request chose: source or target; empty while unresolved."`
	Resolved bool   `json:"resolved"`
}

// OverrideConflict is a project that overrode both entities.
type OverrideConflict struct {
	ProjectID string `json:"project_id"`
	Key       string `json:"key" desc:"The field_choices key to answer with: override:<project id>."`
	Choice    string `json:"choice"`
	Resolved  bool   `json:"resolved"`
}

// AliasBlock is an alias that cannot move because a third entity uses it.
type AliasBlock struct {
	Alias    string `json:"alias"`
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	Via      string `json:"via" enum:"name,alias"`
}

// RelationDrop is a relationship the merge removes.
type RelationDrop struct {
	ID     string `json:"id"`
	Reason string `json:"reason" enum:"self,duplicate" desc:"self: it would relate the target to itself. duplicate: the target already has the same relationship."`
}

// MergePlan is everything a merge changes. Dry run and real merge share it.
type MergePlan struct {
	SourceID   string `json:"source_id"`
	SourceName string `json:"source_name"`
	TargetID   string `json:"target_id"`
	TargetName string `json:"target_name"`
	Kind       string `json:"kind"`

	AliasesMoved     []string     `json:"aliases_moved"`
	AliasesDuplicate []string     `json:"aliases_duplicate" desc:"Aliases of the source the target already has (or is named): dropped."`
	AliasesBlocked   []AliasBlock `json:"aliases_blocked" desc:"Aliases of the source a third entity uses: dropped."`
	NameAsAlias      string       `json:"name_as_alias" desc:"The source's name, kept as an alias of the target; empty when not."`

	FieldsTaken    []string        `json:"fields_taken" desc:"Custom fields only the source had: copied to the target."`
	FieldConflicts []FieldConflict `json:"field_conflicts"`
	SummaryFrom    string          `json:"summary_from" enum:"target,source"`
	BodyFrom       string          `json:"body_from" enum:"target,source,both"`

	RelationsRewired int            `json:"relations_rewired"`
	RelationsDropped []RelationDrop `json:"relations_dropped"`

	OverridesMoved          int                `json:"overrides_moved"`
	OverrideConflicts       []OverrideConflict `json:"override_conflicts"`
	OverridesDropped        int                `json:"overrides_dropped" desc:"Source overrides discarded: the request chose the target's, or the project is one the caller cannot open."`
	HiddenOverrideConflicts int                `json:"hidden_override_conflicts" desc:"Projects the caller cannot open that overrode both entities. They are not named and cannot be chosen for: the target's override is kept there."`
	LockedOverrideConflicts int                `json:"locked_override_conflicts" desc:"Projects the caller is on but has not unlocked (they have a passphrase) that overrode both entities. A real merge is refused while this is not zero: their source override would be deleted without anyone being able to see or choose it. Unlock them, then choose per project."`
	LinksMoved              int                `json:"links_moved"`
	LinksDropped            int                `json:"links_dropped" desc:"Project links the target already had."`
	SceneRefsRepointed      int                `json:"scene_refs_repointed" desc:"Scenes whose point of view or location was the source."`
	RedirectsRepointed      int                `json:"redirects_repointed" desc:"Older merged ids that now lead to the target."`
	Extra                   map[string]int     `json:"extra" desc:"Rows other features move with the merge, by feature."`

	Unresolved []string `json:"unresolved" desc:"field_choices keys still needing an answer. A real merge is refused while this is not empty."`
}

// MergeOut is a merge or its preview.
type MergeOut struct {
	DryRun bool      `json:"dry_run"`
	Merged bool      `json:"merged" desc:"True once the merge has been applied."`
	Plan   MergePlan `json:"plan"`
	Item   *Entity   `json:"item,omitempty" desc:"The target after a real merge."`
}

type overrideRow struct {
	project string
	name    sql.NullString
	summary sql.NullString
	fields  string
	notes   string
}

// planMerge works out the merge without writing. q is a read transaction for a
// dry run and the writer's transaction for a real merge.
func planMerge(ctx context.Context, q Querier, cid string, in MergeIn, canOpen func(projectID string) bool) (*MergePlan, *Entity, *Entity, error) {
	return planMergeAcross(ctx, q, cid, cid, in, canOpen)
}

// planMergeAcross is planMerge for a source and a target in different Compendiums, which is what merging two
// Compendiums with a name in common needs (compendium.merge); within one Compendium the two are the same.
func planMergeAcross(ctx context.Context, q Querier, srcCID, cid string, in MergeIn, canOpen func(projectID string) bool) (*MergePlan, *Entity, *Entity, error) {
	src, err := load(ctx, q, srcCID, in.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	dst, err := load(ctx, q, cid, in.IntoID)
	if err != nil {
		return nil, nil, nil, ops.Invalid("into_id", "There is no such entity in this Compendium.")
	}
	if src.Kind != dst.Kind {
		return nil, nil, nil, ops.Invalid("into_id", fmt.Sprintf("A %s cannot be merged into a %s. Merge entities of the same kind.", src.Kind, dst.Kind))
	}
	if src.Trashed || dst.Trashed {
		return nil, nil, nil, ops.Conflict("An entity in the trash cannot be merged. Restore it first.")
	}
	p := &MergePlan{SourceID: src.ID, SourceName: src.Name, TargetID: dst.ID, TargetName: dst.Name, Kind: src.Kind,
		AliasesMoved: []string{}, AliasesDuplicate: []string{}, AliasesBlocked: []AliasBlock{}, FieldsTaken: []string{}, FieldConflicts: []FieldConflict{},
		RelationsDropped: []RelationDrop{}, OverrideConflicts: []OverrideConflict{}, Extra: map[string]int{}, Unresolved: []string{},
		SummaryFrom: "target", BodyFrom: "target"}

	// Aliases.
	have := map[string]bool{text.FoldTitle(dst.Name): true}
	for _, a := range dst.Aliases {
		have[text.FoldTitle(a.Alias)] = true
	}
	for _, a := range src.Aliases {
		k := text.FoldTitle(a.Alias)
		if have[k] {
			p.AliasesDuplicate = append(p.AliasesDuplicate, a.Alias)
			continue
		}
		oid, oname, via, found, err := clash(ctx, q, cid, k, src.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		if found && oid != dst.ID {
			p.AliasesBlocked = append(p.AliasesBlocked, AliasBlock{Alias: a.Alias, EntityID: oid, Name: oname, Via: via})
			continue
		}
		have[k] = true
		p.AliasesMoved = append(p.AliasesMoved, a.Alias)
	}
	if in.NameAsAlias == nil || *in.NameAsAlias {
		if k := text.FoldTitle(src.Name); !have[k] {
			p.NameAsAlias = src.Name
		}
	}

	// Custom fields.
	d, err := loadDefs(ctx, q, cid, src.Kind)
	if err != nil {
		return nil, nil, nil, err
	}
	keys := make([]string, 0, len(src.Fields))
	for k := range src.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		sv, tv := src.Fields[k], dst.Fields[k]
		switch {
		case sv == "" || sv == tv:
		case tv == "":
			p.FieldsTaken = append(p.FieldsTaken, k)
		default:
			label := k
			if def, ok := d[k]; ok {
				label = def.Label
			}
			fc := FieldConflict{Key: k, Label: label, Source: sv, Target: tv}
			if ch, ok := in.FieldChoices[k]; ok {
				fc.Choice, fc.Resolved = ch, true
			} else {
				p.Unresolved = append(p.Unresolved, k)
			}
			p.FieldConflicts = append(p.FieldConflicts, fc)
		}
	}

	// Summary and body.
	switch {
	case in.Summary == "source" || (dst.Summary == "" && src.Summary != ""):
		if src.Summary != "" {
			p.SummaryFrom = "source"
		}
	}
	switch {
	case in.Body == "source" && src.Body != "":
		p.BodyFrom = "source"
	case in.Body == "append" && src.Body != "" && src.Body != dst.Body:
		p.BodyFrom = "both"
	case dst.Body == "" && src.Body != "":
		p.BodyFrom = "source"
	}

	// Relationships.
	rrows, err := q.QueryContext(ctx, `SELECT id, from_entity_id, to_entity_id, kind, label, symmetric FROM entity_relations WHERE from_entity_id = ? OR to_entity_id = ? ORDER BY id`, src.ID, src.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	type rel struct {
		id, from, to, kind, label string
		sym                       bool
	}
	var rels []rel
	for rrows.Next() {
		var r rel
		if err := rrows.Scan(&r.id, &r.from, &r.to, &r.kind, &r.label, &r.sym); err != nil {
			rrows.Close()
			return nil, nil, nil, err
		}
		rels = append(rels, r)
	}
	if err := rrows.Err(); err != nil {
		_ = rrows.Close()
		return nil, nil, nil, err
	}
	if err := rrows.Close(); err != nil {
		return nil, nil, nil, err
	}
	for _, r := range rels {
		from, to := r.from, r.to
		if from == src.ID {
			from = dst.ID
		}
		if to == src.ID {
			to = dst.ID
		}
		if from == to {
			p.RelationsDropped = append(p.RelationsDropped, RelationDrop{ID: r.id, Reason: "self"})
			continue
		}
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM entity_relations WHERE id <> ? AND kind = ? AND label = ? AND
			((from_entity_id = ? AND to_entity_id = ?) OR (? = 1 AND from_entity_id = ? AND to_entity_id = ?))`,
			r.id, r.kind, r.label, from, to, boolInt(r.sym), to, from).Scan(&n); err != nil {
			return nil, nil, nil, err
		}
		if n > 0 {
			p.RelationsDropped = append(p.RelationsDropped, RelationDrop{ID: r.id, Reason: "duplicate"})
			continue
		}
		p.RelationsRewired++
	}

	// Overrides and links.
	srcOv, err := loadOverrideRows(ctx, q, src.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	dstOv, err := loadOverrideRows(ctx, q, dst.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	for _, o := range srcOv {
		if _, clashes := dstOv[o.project]; !clashes {
			p.OverridesMoved++
			continue
		}
		if !canOpen(o.project) {
			if lockedForCaller(ctx, o.project) {
				p.LockedOverrideConflicts++ // not dropped: the merge is refused until the project is unlocked and a choice made
				continue
			}
			p.HiddenOverrideConflicts++
			p.OverridesDropped++
			continue
		}
		key := "override:" + o.project
		oc := OverrideConflict{ProjectID: o.project, Key: key}
		if ch, ok := in.FieldChoices[key]; ok {
			oc.Choice, oc.Resolved = ch, true
			if ch == "target" {
				p.OverridesDropped++
			} else {
				p.OverridesMoved++
			}
		} else {
			p.Unresolved = append(p.Unresolved, key)
		}
		p.OverrideConflicts = append(p.OverrideConflicts, oc)
	}
	sort.Slice(p.OverrideConflicts, func(i, j int) bool { return p.OverrideConflicts[i].ProjectID < p.OverrideConflicts[j].ProjectID })
	sort.Strings(p.Unresolved)
	if err := q.QueryRowContext(ctx, `SELECT
			COALESCE(sum(CASE WHEN NOT EXISTS (SELECT 1 FROM project_entity_links t WHERE t.entity_id = ? AND t.project_id = l.project_id) THEN 1 ELSE 0 END), 0),
			COALESCE(sum(CASE WHEN EXISTS (SELECT 1 FROM project_entity_links t WHERE t.entity_id = ? AND t.project_id = l.project_id) THEN 1 ELSE 0 END), 0)
		FROM project_entity_links l WHERE l.entity_id = ?`, dst.ID, dst.ID, src.ID).Scan(&p.LinksMoved, &p.LinksDropped); err != nil {
		return nil, nil, nil, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE pov_entity_id = ? OR location_entity_id = ?`, src.ID, src.ID).Scan(&p.SceneRefsRepointed); err != nil {
		return nil, nil, nil, err
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM entity_redirects WHERE to_id = ?`, src.ID).Scan(&p.RedirectsRepointed); err != nil {
		return nil, nil, nil, err
	}
	for _, s := range steps() {
		n, err := s.Count(ctx, q, src.ID, dst.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		p.Extra[s.Name] = n
	}
	return p, &src, &dst, nil
}

// lockedForCaller reports whether a project is one the caller is a member of and has not unlocked (a passphrase protects it),
// as against one they are simply not on. ctx is the op's context, which carries the caller.
func lockedForCaller(ctx context.Context, projectID string) bool {
	c, ok := ctx.(*ops.Ctx)
	if !ok || c.Env == nil || c.Env.ProjectMember == nil || c.Env.ProjectGate == nil {
		return false
	}
	if _, member := c.Env.ProjectMember(c, c.P.UserID, projectID); !member {
		return false
	}
	return c.Env.ProjectGate(ops.Passive(c), c.P, projectID) != nil
}

// canOpenProject says whether the caller may open a project, by the rule the
// registry applies to every project op.
func canOpenProject(c *ops.Ctx) func(string) bool {
	return func(projectID string) bool {
		if c.Env.ProjectRole == nil {
			return false
		}
		_, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
		return ok
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func loadOverrideRows(ctx context.Context, q Querier, entityID string) (map[string]overrideRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT project_id, name, summary, fields_json, notes FROM project_entity_overrides WHERE entity_id = ?`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]overrideRow{}
	for rows.Next() {
		var o overrideRow
		if err := rows.Scan(&o.project, &o.name, &o.summary, &o.fields, &o.notes); err != nil {
			return nil, err
		}
		out[o.project] = o
	}
	return out, rows.Err()
}

func merge(c *ops.Ctx, in MergeIn) (MergeOut, error) {
	if in.DryRun {
		tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return MergeOut{}, err
		}
		defer tx.Rollback()
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return MergeOut{}, ops.NotFound("entity")
		}
		p, _, _, err := planMerge(c, tx, cid, in, canOpenProject(c))
		if err != nil {
			return MergeOut{}, err
		}
		return MergeOut{DryRun: true, Plan: *p}, nil
	}
	var out MergeOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		p, src, dst, err := planMerge(c, tx, cid, in, canOpenProject(c))
		if err != nil {
			return err
		}
		if p.LockedOverrideConflicts > 0 {
			return ops.ConflictWith(fmt.Sprintf("%d of your projects that you have not unlocked overrode both entities. Unlock them and merge again: their override of the entity that is merged away would otherwise be deleted unseen.", p.LockedOverrideConflicts),
				map[string]any{"reason": "locked_projects", "locked_override_conflicts": p.LockedOverrideConflicts})
		}
		if len(p.Unresolved) > 0 {
			return &ops.Error{Code: ops.CodeInvalid, Field: "field_choices",
				Message: "These need an answer before the entities can be merged, source or target for each in field_choices: " + strings.Join(p.Unresolved, ", ") + ".",
				Data:    map[string]any{"unresolved": p.Unresolved}}
		}
		if err := applyMerge(c, tx, cid, in, p, src, dst); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.merged", CompendiumID: cid, Payload: map[string]any{"source_id": src.ID, "target_id": dst.ID,
			"aliases": len(p.AliasesMoved), "relations": p.RelationsRewired, "overrides": p.OverridesMoved}})
		c.Audit(ops.AuditEntry{Action: "entity.merge", TargetType: "entity", TargetID: dst.ID, Detail: map[string]any{
			"source_id": src.ID, "aliases_moved": len(p.AliasesMoved), "aliases_blocked": len(p.AliasesBlocked), "fields_taken": len(p.FieldsTaken),
			"field_conflicts": len(p.FieldConflicts), "relations_rewired": p.RelationsRewired, "relations_dropped": len(p.RelationsDropped),
			"overrides_moved": p.OverridesMoved, "overrides_dropped": p.OverridesDropped, "links_moved": p.LinksMoved, "scene_refs": p.SceneRefsRepointed}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		e, err := load(c, tx, cid, dst.ID)
		if err != nil {
			return err
		}
		out = MergeOut{Merged: true, Plan: *p, Item: &e}
		return nil
	})
	return out, err
}

// applyMerge carries out a plan that has no unresolved conflict.
func applyMerge(c *ops.Ctx, tx *sql.Tx, cid string, in MergeIn, p *MergePlan, src, dst *Entity) error {
	return applyMergeAcross(c, tx, cid, in, p, src, dst)
}

// applyMergeAcross is applyMerge where the source may sit in another Compendium than the target (cid is the
// target's): what it rewires then belongs to the target's Compendium.
func applyMergeAcross(c *ops.Ctx, tx *sql.Tx, cid string, in MergeIn, p *MergePlan, src, dst *Entity) error {
	now := db.Now()
	exec := func(q string, args ...any) error {
		_, err := tx.Exec(q, args...)
		return err
	}
	// The source's snapshot, for the redirect row.
	snap, _ := json.Marshal(map[string]any{"name": src.Name, "kind": src.Kind, "summary": src.Summary, "body": src.Body, "fields": src.Fields, "aliases": aliasNames(src.Aliases)})
	summary, _ := json.Marshal(map[string]any{"aliases_moved": len(p.AliasesMoved), "relations_rewired": p.RelationsRewired, "overrides_moved": p.OverridesMoved, "links_moved": p.LinksMoved})

	// Aliases move; the ones that do not are deleted with the source.
	moved := map[string]bool{}
	for _, a := range p.AliasesMoved {
		moved[text.FoldTitle(a)] = true
	}
	for _, a := range src.Aliases {
		if moved[text.FoldTitle(a.Alias)] {
			if err := exec(`UPDATE entity_aliases SET entity_id = ? WHERE id = ?`, dst.ID, a.ID); err != nil {
				return err
			}
		}
	}
	// Relationships.
	dropped := map[string]bool{}
	for _, d := range p.RelationsDropped {
		dropped[d.ID] = true
		if err := exec(`DELETE FROM entity_relations WHERE id = ?`, d.ID); err != nil {
			return err
		}
	}
	if err := exec(`UPDATE entity_relations SET from_entity_id = ? WHERE from_entity_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	if err := exec(`UPDATE entity_relations SET to_entity_id = ? WHERE to_entity_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	// From another Compendium: what was rewired is now the target's own.
	if err := exec(`UPDATE entity_relations SET compendium_id = ?1 WHERE (from_entity_id = ?2 OR to_entity_id = ?2) AND compendium_id <> ?1`, cid, dst.ID); err != nil {
		return err
	}
	// Overrides: a project that overrode both keeps the side the request chose.
	for _, oc := range p.OverrideConflicts {
		if oc.Choice == "source" {
			var name, summary sql.NullString
			var fields, notes string
			if err := tx.QueryRow(`SELECT name, summary, fields_json, notes FROM project_entity_overrides WHERE project_id = ? AND entity_id = ?`, oc.ProjectID, src.ID).Scan(&name, &summary, &fields, &notes); err != nil {
				return err
			}
			if err := exec(`UPDATE project_entity_overrides SET name = ?, summary = ?, fields_json = ?, notes = ?, updated_by = ?, updated_at = ? WHERE project_id = ? AND entity_id = ?`,
				name, summary, fields, notes, c.P.UserID, now, oc.ProjectID, dst.ID); err != nil {
				return err
			}
		}
		if err := exec(`DELETE FROM project_entity_overrides WHERE project_id = ? AND entity_id = ?`, oc.ProjectID, src.ID); err != nil {
			return err
		}
	}
	// Projects the caller cannot open that overrode both: the target's stays.
	if err := exec(`DELETE FROM project_entity_overrides WHERE entity_id = ? AND project_id IN
		(SELECT project_id FROM project_entity_overrides WHERE entity_id = ?)`, src.ID, dst.ID); err != nil {
		return err
	}
	if err := exec(`UPDATE project_entity_overrides SET entity_id = ? WHERE entity_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	if err := exec(`UPDATE OR IGNORE project_entity_links SET entity_id = ? WHERE entity_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	if err := exec(`UPDATE documents SET pov_entity_id = ? WHERE pov_entity_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	if err := exec(`UPDATE documents SET location_entity_id = ? WHERE location_entity_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	// Older tombstones that pointed at the source now point at the target: one hop, always.
	if err := exec(`UPDATE entity_redirects SET to_id = ? WHERE to_id = ?`, dst.ID, src.ID); err != nil {
		return err
	}
	for _, s := range steps() {
		if err := s.Apply(tx, src.ID, dst.ID); err != nil {
			return err
		}
	}
	// The target's own columns.
	fields := map[string]string{}
	for k, v := range dst.Fields {
		fields[k] = v
	}
	for _, k := range p.FieldsTaken {
		fields[k] = src.Fields[k]
	}
	for _, fc := range p.FieldConflicts {
		if fc.Choice == "source" {
			fields[fc.Key] = fc.Source
		}
	}
	summaryText := dst.Summary
	if p.SummaryFrom == "source" {
		summaryText = src.Summary
	}
	body := dst.Body
	switch p.BodyFrom {
	case "source":
		body = src.Body
	case "both":
		body = strings.TrimRight(dst.Body, "\n") + "\n\n" + src.Body
	}
	if err := exec(`UPDATE entities SET fields_json = ?, summary = ?, body = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
		marshalFields(fields), summaryText, body, now, dst.ID); err != nil {
		return err
	}
	// The redirect, then the source row (its leftover aliases, relationships
	// and search row go with it).
	if err := exec(`INSERT OR REPLACE INTO entity_redirects(from_id, to_id, compendium_id, from_name, merged_by, summary_json, snapshot_json, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		src.ID, dst.ID, cid, src.Name, nullIfEmpty(c.P.UserID), string(summary), string(snap), now); err != nil {
		return err
	}
	// What the source held may now be in the target (fields, summary, body): a
	// never-send flag on the source must not be lost by merging it away.
	if err := exec(`UPDATE entities SET ai_exclude = 1 WHERE id = ? AND (SELECT ai_exclude FROM entities WHERE id = ?) = 1`, dst.ID, src.ID); err != nil {
		return err
	}
	if err := exec(`DELETE FROM entities WHERE id = ?`, src.ID); err != nil {
		return err
	}
	if p.NameAsAlias != "" {
		if err := insertAlias(c, tx, cid, dst.ID, p.NameAsAlias, "", now); err != nil {
			return err
		}
	}
	return syncText(tx, dst.ID)
}

func aliasNames(as []Alias) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Alias
	}
	return out
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

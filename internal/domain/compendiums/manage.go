package compendiums

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// Changing a project's primary Compendium, merging two Compendiums and splitting one (F-478). The engine for moving
// entities is entities.PlanMove and ApplyMove; this file is the ops around it. Every one has a preview that is the
// same plan function as the change, and the change refuses unless it is given the preview's fingerprint, so what is
// applied is what was shown.

func registerManage(r *ops.Registry) {
	// Restructuring Compendiums is a person's act in a browser: it changes what projects and keys reach, and the record of it holds text that was merged away.
	owner := ops.RequireCompendium(compaccess.Owner).ForSessions()
	ops.Register(r, ops.Op{
		Name: "compendium.merge.preview", Method: "POST", Path: "/api/v1/compendiums/{id}/merge/preview", Scope: ops.ScopeRead,
		Desc: "Preview merging this Compendium (the source) into another (into_id), or moving some of its entities there (entity_ids): which entities move, which names or aliases clash with the target and need an answer (resolutions, per entity: rename, merge, drop_aliases or skip), " +
			"which relationships and timeline events would be split, what the projects that use it lose sight of, which API keys are pinned to it. Writes nothing. Owner of both Compendiums. The answer carries a fingerprint that compendium.merge needs.",
		Auth: owner,
	}, mergePreview)
	ops.Register(r, ops.Op{
		Name: "compendium.merge", Method: "POST", Path: "/api/v1/compendiums/{id}/merge", Permanent: true,
		Desc: "Move this Compendium's entities (all of them, or entity_ids) into another, in one transaction, keeping their ids: aliases, mentions, links, overrides and scenes follow. Name clashes are settled per entity in resolutions (a merge there is entity.merge, permanent); " +
			"relationships and timeline events that would join the two are refused unless crossing is drop; API keys pinned to the source are refused unless key_pins says repoint or leave. fingerprint must be the one compendium.merge.preview returned. " +
			"The before-state is recorded (compendium.change.get) and returned. The source Compendium is not deleted. Owner of both; refused for a Compendium in the trash.",
		Auth: owner,
	}, mergeDo)
	ops.Register(r, ops.Op{
		Name: "compendium.split.preview", Method: "POST", Path: "/api/v1/compendiums/{id}/split/preview", Scope: ops.ScopeRead,
		Desc: "Preview moving the chosen entities (entity_ids) of this Compendium into a new one named name: the plan compendium.merge.preview gives for a merge, with nothing to clash with. Writes nothing. Owner. The answer carries a fingerprint that compendium.split needs.",
		Auth: owner,
	}, splitPreview)
	ops.Register(r, ops.Op{
		Name: "compendium.split", Method: "POST", Path: "/api/v1/compendiums/{id}/split", Permanent: true,
		Desc: "Move the chosen entities (entity_ids) of this Compendium into a new Compendium named name, owned by the caller, in one transaction, keeping their ids. The rules are those of compendium.merge. fingerprint must be the one compendium.split.preview returned. " +
			"The before-state is recorded (compendium.change.get) and returned. Owner; refused for a Compendium in the trash.",
		Auth: owner, Status: 201,
	}, splitDo)
	ops.Register(r, ops.Op{
		Name: "compendium.change.list", Method: "GET", Path: "/api/v1/compendiums/{id}/changes",
		Desc: "The merges and splits that moved entities into or out of this Compendium, newest first: when, by whom, the counts. Owner.",
		Auth: owner,
	}, changeList)
	ops.Register(r, ops.Op{
		Name: "compendium.change.get", Method: "GET", Path: "/api/v1/compendiums/{id}/changes/{change_id}",
		Desc: "One recorded merge or split with its before-state: every entity that moved (its name and revision as they were), every entity merged away with its whole sheet, every relationship, event person, place and alias that was dropped, the keys repointed and the references added. Owner.",
		Auth: owner,
	}, changeGet)
	ops.Register(r, ops.Op{
		Name: "compendium.primary.preview", Method: "POST", Path: "/api/v1/projects/{id}/compendium/primary/preview", Scope: ops.ScopeRead,
		Desc: "Preview changing a project's primary Compendium: the entities that enter and leave the project, its overrides that become dormant, the mention index rows and scenes that refer to what leaves, its links, and the API keys pinned to the project (a project key reaches the primary Compendium, so its reach changes). Writes nothing. Project owner. The answer carries a fingerprint compendium.primary.set needs.",
		Auth: ops.RequireProject(ops.ProjectOwner).ForSessions(),
	}, primaryPreview)
	ops.Register(r, ops.Op{
		Name: "compendium.primary.set", Method: "PUT", Path: "/api/v1/projects/{id}/compendium/primary",
		Desc: "Set or clear a project's primary Compendium (compendium_id empty clears it) with the preview's fingerprint. Project owner, who must also be an editor of the Compendium. The project's mention index is rescanned. " +
			"The same change as project.compendium.set, which stays and previews with dry_run.",
		Auth: ops.RequireProject(ops.ProjectOwner).ForSessions(),
	}, primarySet)
}

// ---- merge and split ----------------------------------------------------------------------------------------------

// MoveOpts are the choices every move shares.
type MoveOpts struct {
	EntityIDs      []string                       `json:"entity_ids,omitempty" desc:"The entities to move; empty means every live entity of the source (a split needs them)."`
	Resolutions    map[string]entities.Resolution `json:"resolutions,omitempty" desc:"By entity id: what to do with an entity whose name or alias the target already uses."`
	Crossing       string                         `json:"crossing,omitempty" enum:"refuse,drop" desc:"What to do with relationships and timeline people that would join the two Compendiums: refuse (default) or drop them (kept in the before-state)."`
	KeyPins        string                         `json:"key_pins,omitempty" enum:"refuse,repoint,leave" desc:"API keys pinned to the source: refuse the move (default), repoint them to the target (widening them; only keys whose owner is on the target), or leave them pinned to the source."`
	FollowProjects bool                           `json:"follow_projects,omitempty" desc:"Add a read-only reference to the target to the projects that used the source, where the caller owns the project."`
}

func (o MoveOpts) check() error {
	switch o.Crossing {
	case "", "refuse", "drop":
	default:
		return ops.Invalid("crossing", "Crossing is refuse or drop.")
	}
	switch o.KeyPins {
	case "", "refuse", "repoint", "leave":
	default:
		return ops.Invalid("key_pins", "key_pins is refuse, repoint or leave.")
	}
	if len(o.EntityIDs) > entities.MaxMoveEntities {
		return ops.Invalid("entity_ids", "Too many entities for one move.")
	}
	return nil
}

// MergeIn is a merge (or a partial move) of the Compendium in the path into another.
type MergeIn struct {
	ID          string `path:"id" json:"-" desc:"The source Compendium."`
	IntoID      string `json:"into_id" ref:"compendium" desc:"The target Compendium."`
	Fingerprint string `json:"fingerprint,omitempty" desc:"From the preview; required by compendium.merge."`
	MoveOpts
}

// CompendiumRef lets the registry authorize by membership.
func (in MergeIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *MergeIn) Validate() error {
	if in.IntoID == "" {
		return ops.Invalid("into_id", "Say which Compendium to move into.")
	}
	return in.check()
}

func (in MergeIn) move() entities.MoveIn {
	return entities.MoveIn{SourceID: in.ID, TargetID: in.IntoID, EntityIDs: in.EntityIDs, Resolutions: in.Resolutions,
		Crossing: in.Crossing, KeyPins: in.KeyPins, FollowProjects: in.FollowProjects}
}

// PlanOut is a preview.
type PlanOut struct {
	Plan entities.MovePlan `json:"plan"`
}

// ChangeOut is a merge or split that was applied.
type ChangeOut struct {
	Plan        entities.MovePlan `json:"plan"`
	ChangeID    string            `json:"change_id" desc:"The record of the before-state: compendium.change.get."`
	TargetID    string            `json:"target_id" desc:"The Compendium the entities are in now (new, for a split)."`
	BeforeState entities.Bundle   `json:"before_state"`
}

func mergePreview(c *ops.Ctx, in MergeIn) (PlanOut, error) {
	if c.P.Pinned() {
		return PlanOut{}, ops.Forbidden("a key restricted to one project or compendium cannot move entities between Compendiums")
	}
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PlanOut{}, err
	}
	defer tx.Rollback()
	p, _, err := entities.PlanMove(c, tx, in.move())
	if err != nil {
		return PlanOut{}, err
	}
	return PlanOut{Plan: *p}, nil
}

func mergeDo(c *ops.Ctx, in MergeIn) (ChangeOut, error) {
	if c.P.Pinned() {
		return ChangeOut{}, ops.Forbidden("a key restricted to one project or compendium cannot move entities between Compendiums")
	}
	var out ChangeOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		p, b, err := entities.ApplyMove(c, tx, in.move(), in.Fingerprint)
		if err != nil {
			return err
		}
		id, err := recordChange(c, tx, "merge", p, b)
		if err != nil {
			return err
		}
		announce(c, "compendium.merged", p, id)
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out = ChangeOut{Plan: *p, ChangeID: id, TargetID: in.IntoID, BeforeState: *b}
		return nil
	})
	return out, err
}

// SplitIn moves chosen entities of the Compendium in the path into a new Compendium.
type SplitIn struct {
	ID          string `path:"id" json:"-" desc:"The source Compendium."`
	Name        string `json:"name" desc:"The new Compendium's name, up to 200 characters."`
	Description string `json:"description,omitempty" desc:"Up to 5,000 characters of Markdown."`
	Fingerprint string `json:"fingerprint,omitempty" desc:"From the preview; required by compendium.split."`
	MoveOpts
}

// CompendiumRef lets the registry authorize by membership.
func (in SplitIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *SplitIn) Validate() error {
	c := CreateIn{Name: in.Name, Description: in.Description}
	if err := c.Validate(); err != nil {
		return err
	}
	in.Name, in.Description = c.Name, c.Description
	if len(in.EntityIDs) == 0 {
		return ops.Invalid("entity_ids", "Choose the entities to move into the new Compendium.")
	}
	return in.check()
}

func (in SplitIn) move(target string) entities.MoveIn {
	return entities.MoveIn{SourceID: in.ID, TargetID: target, NewName: in.Name, EntityIDs: in.EntityIDs, Resolutions: in.Resolutions,
		Crossing: in.Crossing, KeyPins: in.KeyPins, FollowProjects: in.FollowProjects}
}

func splitPreview(c *ops.Ctx, in SplitIn) (PlanOut, error) {
	if c.P.Pinned() {
		return PlanOut{}, ops.Forbidden("a key restricted to one project or compendium cannot split a Compendium")
	}
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PlanOut{}, err
	}
	defer tx.Rollback()
	p, _, err := entities.PlanMove(c, tx, in.move(""))
	if err != nil {
		return PlanOut{}, err
	}
	return PlanOut{Plan: *p}, nil
}

func splitDo(c *ops.Ctx, in SplitIn) (ChangeOut, error) {
	if c.P.Pinned() {
		return ChangeOut{}, ops.Forbidden("a key restricted to one project or compendium cannot split a Compendium")
	}
	var out ChangeOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		// What the preview showed: the plan with the new Compendium still to come.
		pre, _, err := entities.PlanMove(c, tx, in.move(""))
		if err != nil {
			return err
		}
		if in.Fingerprint == "" || in.Fingerprint != pre.Fingerprint {
			return ops.ConflictWith("What would change is not what the preview showed: something changed since. Preview again.",
				map[string]any{"reason": "plan_changed", "fingerprint": pre.Fingerprint})
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM compendiums WHERE id = ?`, in.ID).Scan(&kind); err != nil {
			return ops.NotFound("compendium")
		}
		no := false
		target, err := InsertTx(c, tx, CreateIn{Name: in.Name, Kind: kind, Description: in.Description, StarterFields: &no}, db.Now())
		if err != nil {
			return err
		}
		p, b, err := entities.ApplyMoveToNew(c, tx, in.move(target))
		if err != nil {
			return err
		}
		b.Kind = "split"
		id, err := recordChange(c, tx, "split", p, b)
		if err != nil {
			return err
		}
		announce(c, "compendium.split", p, id)
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out = ChangeOut{Plan: *p, ChangeID: id, TargetID: target, BeforeState: *b}
		return nil
	})
	return out, err
}

// recordChange writes the journal row in the move's own transaction.
func recordChange(c *ops.Ctx, tx *sql.Tx, kind string, p *entities.MovePlan, b *entities.Bundle) (string, error) {
	before, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	summary, _ := json.Marshal(map[string]any{"moved": p.Moving, "staying": p.Staying, "collisions": p.Collisions, "aliases_moved": p.AliasesMoved,
		"aliases_dropped": p.AliasesDropped, "relations_moved": p.RelationsMoved, "relations_dropped": len(p.RelationsCrossing), "events_moved": p.EventsMoved,
		"events_split": len(p.EventsCrossing), "merged": len(b.Merged), "keys_repointed": len(b.KeysRepointed), "references_added": len(b.RefsAdded)})
	id := db.NewID("cch")
	if _, err := tx.Exec(`INSERT INTO compendium_changes(id, kind, source_id, target_id, actor_id, created_at, fingerprint, summary_json, before_json) VALUES (?,?,?,?,?,?,?,?,?)`,
		id, kind, p.SourceID, b.TargetID, nullIf(c.P.UserID), db.Now(), p.Fingerprint, string(summary), string(before)); err != nil {
		return "", err
	}
	return id, nil
}

func nullIf(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// announce queues the event and audit line of a move. They carry ids and counts, never a name or a word of text.
func announce(c *ops.Ctx, typ string, p *entities.MovePlan, changeID string) {
	payload := map[string]any{"source_id": p.SourceID, "target_id": p.TargetID, "moved": p.Moving, "change_id": changeID}
	c.Emit(ops.Event{Type: typ, CompendiumID: p.SourceID, Payload: payload})
	c.Emit(ops.Event{Type: typ, CompendiumID: p.TargetID, Payload: payload})
	action := "compendium.merge"
	if typ == "compendium.split" {
		action = "compendium.split"
	}
	c.Audit(ops.AuditEntry{Action: action, TargetType: "compendium", TargetID: p.TargetID, Detail: map[string]any{
		"source_id": p.SourceID, "moved": p.Moving, "staying": p.Staying, "collisions": p.Collisions, "relations_dropped": len(p.RelationsCrossing),
		"events_moved": p.EventsMoved, "events_split": len(p.EventsCrossing), "change_id": changeID}})
}

// ---- the record --------------------------------------------------------------------------------------------------

// Change is a recorded merge or split.
type Change struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind" enum:"merge,split"`
	SourceID  string          `json:"source_id"`
	TargetID  string          `json:"target_id"`
	ActorID   string          `json:"actor_id,omitempty"`
	CreatedAt string          `json:"created_at"`
	Summary   map[string]any  `json:"summary" desc:"Counts of what the change did."`
	Before    json.RawMessage `json:"before_state,omitempty" desc:"Only in compendium.change.get."`
}

// ChangeListOut lists changes.
type ChangeListOut struct {
	Items []Change `json:"items"`
}

// ChangeItem is one change.
type ChangeItem struct {
	Item Change `json:"item"`
}

// ChangeIn names a Compendium.
type ChangeIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in ChangeIn) CompendiumRef() string { return in.ID }

// ChangeGetIn names one change.
type ChangeGetIn struct {
	ID       string `path:"id" json:"-" desc:"Compendium id: the source or the target of the change."`
	ChangeID string `path:"change_id" json:"-" desc:"Change id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in ChangeGetIn) CompendiumRef() string { return in.ID }

func changeList(c *ops.Ctx, in ChangeIn) (ChangeListOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, kind, source_id, target_id, COALESCE(actor_id,''), created_at, summary_json FROM compendium_changes
		WHERE source_id = ?1 OR target_id = ?1 ORDER BY created_at DESC, id DESC LIMIT 200`, in.ID)
	if err != nil {
		return ChangeListOut{}, err
	}
	defer rows.Close()
	out := ChangeListOut{Items: []Change{}}
	for rows.Next() {
		var ch Change
		var sum string
		if err := rows.Scan(&ch.ID, &ch.Kind, &ch.SourceID, &ch.TargetID, &ch.ActorID, &ch.CreatedAt, &sum); err != nil {
			return out, err
		}
		_ = json.Unmarshal([]byte(sum), &ch.Summary)
		out.Items = append(out.Items, ch)
	}
	return out, rows.Err()
}

func changeGet(c *ops.Ctx, in ChangeGetIn) (ChangeItem, error) {
	var ch Change
	var sum, before string
	err := c.DB().Read().QueryRowContext(c, `SELECT id, kind, source_id, target_id, COALESCE(actor_id,''), created_at, summary_json, before_json FROM compendium_changes
		WHERE id = ?2 AND (source_id = ?1 OR target_id = ?1)`, in.ID, in.ChangeID).Scan(&ch.ID, &ch.Kind, &ch.SourceID, &ch.TargetID, &ch.ActorID, &ch.CreatedAt, &sum, &before)
	if err != nil {
		return ChangeItem{}, ops.NotFound("change")
	}
	_ = json.Unmarshal([]byte(sum), &ch.Summary)
	ch.Before = json.RawMessage(before)
	return ChangeItem{Item: ch}, nil
}

// ---- the primary Compendium ---------------------------------------------------------------------------------------

// PrimaryIn is a project's new primary Compendium.
type PrimaryIn struct {
	ID           string `path:"id" json:"-" desc:"Project id."`
	CompendiumID string `json:"compendium_id" desc:"The new primary Compendium; empty to clear it."`
	Fingerprint  string `json:"fingerprint,omitempty" desc:"From the preview; required by compendium.primary.set."`
}

// ProjectRef lets the registry authorize by membership.
func (in PrimaryIn) ProjectRef() string { return in.ID }

// PrimaryPlan is what changing the primary Compendium does.
type PrimaryPlan struct {
	SetOut
	MentionRowsLeaving int               `json:"mention_rows_leaving" desc:"Mention index rows in this project's documents for entities that leave its scope. The index is rescanned; nothing is lost, and the rows return with the entities."`
	SceneRefsLeaving   int               `json:"scene_refs_leaving" desc:"Scenes whose point of view or place is an entity that leaves the scope: unchanged, unresolved until it returns."`
	LinksLeaving       int               `json:"links_to_leaving" desc:"This project's single-entity links to entities of the old primary: they keep those entities visible as cameos."`
	LinksRedundant     int               `json:"links_now_redundant" desc:"Its links to entities of the new primary: the entity is shown once, by the primary; the link stays."`
	Keys               []entities.KeyPin `json:"keys" desc:"Live API keys pinned to this project. A project key reaches the project's primary Compendium, so these follow the change; the caller's are named."`
	HiddenKeys         int               `json:"hidden_keys"`
	KeysLosingBible    int               `json:"keys_losing_the_bible" desc:"Of those keys, the ones whose owner is not a member of the new primary: they will reach no Compendium."`
	Fingerprint        string            `json:"fingerprint"`
}

// PrimaryOut is a preview.
type PrimaryOut struct {
	Plan PrimaryPlan `json:"plan"`
}

func planPrimary(c *ops.Ctx, tx *sql.Tx, in PrimaryIn) (PrimaryPlan, error) {
	so, err := setPrimaryRun(c, tx, SetIn{ID: in.ID, CompendiumID: in.CompendiumID}, false)
	if err != nil {
		return PrimaryPlan{}, err
	}
	pl := PrimaryPlan{SetOut: so, Keys: []entities.KeyPin{}}
	if so.Changed && so.PreviousCompendiumID != "" && so.EntitiesLeaving > 0 {
		old := so.PreviousCompendiumID
		if err := tx.QueryRowContext(c, `SELECT count(*) FROM entity_mentions m JOIN entities e ON e.id = m.entity_id
			WHERE m.project_id = ? AND e.compendium_id = ? AND NOT EXISTS (SELECT 1 FROM project_entity_links l WHERE l.project_id = m.project_id AND l.entity_id = e.id)`, in.ID, old).Scan(&pl.MentionRowsLeaving); err != nil {
			return pl, err
		}
		if err := tx.QueryRowContext(c, `SELECT count(*) FROM documents d JOIN entities e ON e.id IN (d.pov_entity_id, d.location_entity_id)
			WHERE d.project_id = ? AND d.deleted_at IS NULL AND e.compendium_id = ? AND NOT EXISTS (SELECT 1 FROM project_entity_links l WHERE l.project_id = d.project_id AND l.entity_id = e.id)`, in.ID, old).Scan(&pl.SceneRefsLeaving); err != nil {
			return pl, err
		}
		if err := tx.QueryRowContext(c, `SELECT count(*) FROM project_entity_links l JOIN entities e ON e.id = l.entity_id WHERE l.project_id = ? AND e.compendium_id = ?`, in.ID, old).Scan(&pl.LinksLeaving); err != nil {
			return pl, err
		}
	}
	if in.CompendiumID != "" {
		if err := tx.QueryRowContext(c, `SELECT count(*) FROM project_entity_links l JOIN entities e ON e.id = l.entity_id WHERE l.project_id = ? AND e.compendium_id = ?`, in.ID, in.CompendiumID).Scan(&pl.LinksRedundant); err != nil {
			return pl, err
		}
	}
	rows, err := tx.QueryContext(c, `SELECT k.id, k.name, k.user_id FROM api_keys k WHERE k.project_id = ? AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > ?) ORDER BY k.id`, in.ID, db.Now())
	if err != nil {
		return pl, err
	}
	type kk struct{ id, name, user string }
	var ks []kk
	for rows.Next() {
		var k kk
		if err := rows.Scan(&k.id, &k.name, &k.user); err != nil {
			rows.Close()
			return pl, err
		}
		ks = append(ks, k)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return pl, err
	}
	if err := rows.Close(); err != nil {
		return pl, err
	}
	for _, k := range ks {
		if k.user == c.P.UserID {
			pl.Keys = append(pl.Keys, entities.KeyPin{KeyID: k.id, Name: k.name, Mine: true})
		} else {
			pl.HiddenKeys++
		}
		if in.CompendiumID != "" {
			if _, ok := compaccess.Role(c, tx, k.user, in.CompendiumID); !ok {
				pl.KeysLosingBible++
			}
		}
	}
	pl.Fingerprint = ""
	b, _ := json.Marshal(pl)
	h := sha256.Sum256(b)
	pl.Fingerprint = hex.EncodeToString(h[:16])
	return pl, nil
}

func primaryPreview(c *ops.Ctx, in PrimaryIn) (PrimaryOut, error) {
	if c.P.Pinned() {
		return PrimaryOut{}, ops.Forbidden("a key restricted to one project or compendium cannot change which Compendium a project uses")
	}
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PrimaryOut{}, err
	}
	defer tx.Rollback()
	pl, err := planPrimary(c, tx, in)
	if err != nil {
		return PrimaryOut{}, err
	}
	pl.DryRun = true
	return PrimaryOut{Plan: pl}, nil
}

func primarySet(c *ops.Ctx, in PrimaryIn) (PrimaryOut, error) {
	if c.P.Pinned() {
		return PrimaryOut{}, ops.Forbidden("a key restricted to one project or compendium cannot change which Compendium a project uses")
	}
	var out PrimaryOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		pl, err := planPrimary(c, tx, in)
		if err != nil {
			return err
		}
		if in.Fingerprint == "" || in.Fingerprint != pl.Fingerprint {
			return ops.ConflictWith("What would change is not what the preview showed: something changed since. Preview again.",
				map[string]any{"reason": "plan_changed", "fingerprint": pl.Fingerprint})
		}
		so, err := setPrimaryRun(c, tx, SetIn{ID: in.ID, CompendiumID: in.CompendiumID}, true)
		if err != nil {
			return err
		}
		pl.SetOut = so
		out = PrimaryOut{Plan: pl}
		return nil
	})
	return out, err
}

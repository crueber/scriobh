package entities

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// Moving entities from one Compendium to another (F-478): the engine behind compendium.merge and compendium.split.
//
// An entity keeps its id when it moves, so everything that names it by id follows with no rewrite: its aliases, its
// mentions, a project's override of it and a project's link to it, a scene's point of view or place, the older ids
// that were merged into it. What does not follow by itself is what carries its own Compendium id or a same-Compendium
// rule, and each of those has one decision, below, reported in the plan:
//
//	entity name and aliases    a clash with a name or alias of the target needs the caller's choice, per entity: rename it,
//	                           merge it into the entity it clashes with (entity.merge's rules, across the two), drop its
//	                           clashing aliases, or leave it where it is
//	entity_relations           both ends move: it moves with them. One end only: it would join two Compendiums, so it is
//	                           refused, or (crossing=drop) dropped and kept in the before-state
//	timeline events            an event whose people all move moves with them; one that only some of its people move
//	                           stays, and the moved people are taken off it (crossing=drop) or the move is refused; an
//	                           event with no people stays where it is
//	field definitions          a definition the target lacks is copied; one it has with another type is left as it is
//	                           (the values are kept on the entities, and the plan says which keys)
//	search rows, redirects     follow the entity
//	projects                   a project that used the source and does not use the target stops seeing what moved: counted
//	                           and, where the caller can open the project, named; follow_projects adds a read-only
//	                           reference to the target to those the caller owns
//	API keys pinned to the     re-pointing one widens it to the target's other material, so it is refused by default
//	source                     (key_pins=refuse); repoint moves the keys whose owner is on the target; leave keeps them
//
// Not moved, and left in the source: its research notes, tags, attachments, scratchpads, members, references and its
// trashed entities. The source is not deleted: its owner decides that.
//
// One plan function (planMove) serves the preview and the real move, which recomputes it inside the write transaction
// and refuses if its fingerprint is not the one the preview returned: a change made between the two shows up as a
// refusal, never as a surprise.

// Limits.
const (
	MaxMoveEntities = 5000 // one move touches at most this many entities
	moveChunk       = 400  // placeholders per statement
)

// Resolution is the caller's answer for one entity whose name or alias is already used in the target.
type Resolution struct {
	Action       string            `json:"action" enum:"rename,merge,drop_aliases,skip" desc:"rename: give it new_name. merge: merge it into into_id, an entity of the target (same kind), by entity.merge's rules. drop_aliases: move it and drop the aliases that clash (its name must not clash). skip: leave it in the source."`
	NewName      string            `json:"new_name,omitempty" desc:"For rename."`
	IntoID       string            `json:"into_id,omitempty" desc:"For merge: the entity of the target to merge into."`
	FieldChoices map[string]string `json:"field_choices,omitempty" desc:"For merge: source or target for each custom field both hold differently, as in entity.merge."`
}

// MoveIn says what to move.
type MoveIn struct {
	SourceID       string
	TargetID       string // empty for a split's preview: the target does not exist yet and is named NewName
	NewName        string
	EntityIDs      []string // empty: every live entity of the source
	Resolutions    map[string]Resolution
	Crossing       string // refuse (default) or drop
	KeyPins        string // refuse (default), repoint or leave
	FollowProjects bool
}

// Clash is an entity of the target that a moving entity's name or alias runs into.
type Clash struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	Via      string `json:"via" enum:"name,alias"`
	Word     string `json:"word" desc:"The name or alias of the moving entity that clashes."`
}

// MoveItem is one entity and what happens to it.
type MoveItem struct {
	EntityID  string     `json:"entity_id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Action    string     `json:"action" enum:"move,rename,merge,skip"`
	NewName   string     `json:"new_name,omitempty"`
	MergeInto string     `json:"merge_into,omitempty"`
	Aliases   int        `json:"aliases" desc:"Aliases it takes along."`
	Dropped   []string   `json:"aliases_dropped,omitempty"`
	Clashes   []Clash    `json:"clashes,omitempty" desc:"What it runs into in the target. Needs a resolution unless it is empty."`
	Choice    string     `json:"choice,omitempty" desc:"The resolution given, if any."`
	Merge     *MergePlan `json:"merge_plan,omitempty" desc:"For a merge: entity.merge's plan, across the two Compendiums."`
}

// CrossRelation is a relationship between a moving entity and one that stays.
type CrossRelation struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

// CrossEvent is a timeline event the move would split.
type CrossEvent struct {
	EventID  string   `json:"event_id"`
	Title    string   `json:"title"`
	People   []string `json:"people_moving" desc:"Names of its people who move."`
	Location bool     `json:"location_moving" desc:"Its place is an entity that moves (or, for an event that moves, one that stays): the place is cleared."`
}

// ProjectEffect is what the move does to one project that uses the source or the target.
type ProjectEffect struct {
	ProjectID     string `json:"project_id"`
	Title         string `json:"title"`
	Uses          string `json:"uses" enum:"primary,reference,link"`
	EntitiesLeave int    `json:"entities_leaving" desc:"Moved entities this project stops seeing."`
	OverridesDorm int    `json:"overrides_dormant" desc:"Its overrides of those entities: kept, and applying again if they come back into its scope."`
	MentionRows   int    `json:"mention_rows" desc:"Index rows for those entities in its documents; its index is rebuilt, nothing is lost."`
	Follow        string `json:"follow,omitempty" desc:"With follow_projects: reference added (the caller owns it), or why not."`
}

// KeyPin is a live API key pinned to the source Compendium.
type KeyPin struct {
	KeyID string `json:"key_id"`
	Name  string `json:"name"`
	Mine  bool   `json:"mine"`
}

// MovePlan is everything a move changes. The preview returns it; the real move applies the plan it recomputes.
type MovePlan struct {
	SourceID       string `json:"source_id"`
	SourceName     string `json:"source_name"`
	TargetID       string `json:"target_id"`
	TargetName     string `json:"target_name"`
	Crossing       string `json:"crossing"`
	KeyPins        string `json:"key_pins"`
	FollowProjects bool   `json:"follow_projects"`

	Items      []MoveItem `json:"items"`
	Moving     int        `json:"moving" desc:"Entities that change Compendium (moved, renamed or merged)."`
	Staying    int        `json:"staying" desc:"Entities left in the source, by choice."`
	Collisions int        `json:"collisions" desc:"Entities whose name or alias is used in the target."`
	Unresolved []string   `json:"unresolved" desc:"Entity ids that need a resolution. A real move is refused while this is not empty."`

	AliasesMoved   int `json:"aliases_moved"`
	AliasesDropped int `json:"aliases_dropped"`

	RelationsMoved    int             `json:"relations_moved"`
	RelationsCrossing []CrossRelation `json:"relations_crossing"`
	EventsMoved       int             `json:"events_moved"`
	EventsStaying     int             `json:"events_unattached" desc:"Events of the source that have no people and stay."`
	EventScenes       int             `json:"event_scene_links_moving" desc:"Scenes (documents of projects) that tell the events that move: the links go with the events, so the target's timeline shows those scenes to whoever may open their project."`
	EventsCrossing    []CrossEvent    `json:"events_crossing"`

	FieldDefsCopied    []string `json:"field_defs_copied" desc:"kind.key of definitions the target lacked."`
	FieldDefsDifferent []string `json:"field_defs_different" desc:"kind.key the target defines with another type: values are kept, shown by the target's definition."`

	LinksFollowing int `json:"links_following" desc:"Project links to moved entities: they follow."`
	OverridesKept  int `json:"overrides_following" desc:"Overrides of moved entities: they follow."`
	SceneRefs      int `json:"scene_refs" desc:"Scenes whose point of view or place is a moved entity: unchanged."`
	MentionRows    int `json:"mention_rows" desc:"Mention index rows for moved entities; the index of every project that uses either Compendium is rescanned."`
	Redirects      int `json:"redirects_following" desc:"Older merged ids that lead to a moved entity: they follow."`
	TrashedStaying int `json:"trashed_staying" desc:"Trashed entities of the source: not moved."`

	Projects       []ProjectEffect `json:"projects"`
	HiddenProjects int             `json:"hidden_projects" desc:"Projects that use either Compendium which the caller cannot open: counted, not named."`
	Keys           []KeyPin        `json:"keys" desc:"Live API keys pinned to the source: the caller's are named."`
	HiddenKeys     int             `json:"hidden_keys"`

	StaysBehind map[string]int `json:"stays_in_source" desc:"What the move does not touch and leaves with the source: research, tags, attachments, members, references."`
	Blocking    []string       `json:"blocking" desc:"Reasons a real move would be refused (besides unresolved)."`
	Fingerprint string         `json:"fingerprint" desc:"Send this back to apply the plan. It changes when anything the plan depends on does."`
}

// fingerprint hashes the plan without its fingerprint.
func (p *MovePlan) fingerprint() string {
	q := *p
	q.Fingerprint = ""
	b, _ := json.Marshal(q)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16])
}

// ---- planning -----------------------------------------------------------------------------------------------

type moveState struct {
	moving   []*moveEntity // entities that change Compendium, in id order
	byID     map[string]*moveEntity
	movingID map[string]bool // ids that leave the source (moved, renamed or merged)
	crossRel []string        // relation ids to drop
	events   moveEvents
	copyDefs []FieldDef
	repoint  []string // key ids to repoint (decided at apply)
	follow   []string // projects to add a reference to
}

type moveEntity struct {
	e         Entity
	item      *MoveItem
	merge     *MergePlan
	mergeIn   MergeIn
	mergeDst  *Entity
	dropAlias map[string]bool // folded alias keys to drop
}

type moveEvents struct {
	moving     []string            // events that move with their people
	dropPeople map[string][]string // event -> entity ids to take off
	clearLoc   []string            // events whose place is cleared
}

func inClause(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func chunks(ids []string, n int) [][]string {
	var out [][]string
	for len(ids) > 0 {
		k := n
		if len(ids) < k {
			k = len(ids)
		}
		out = append(out, ids[:k])
		ids = ids[k:]
	}
	return out
}

func anyArgs(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// countIn runs a count over an id set in chunks.
func countIn(ctx context.Context, q Querier, query string, ids []string) (int, error) {
	total := 0
	for _, ch := range chunks(ids, moveChunk) {
		var n int
		if err := q.QueryRowContext(ctx, strings.Replace(query, "(?)", "("+inClause(len(ch))+")", 1), anyArgs(ch)...).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

type compInfo struct {
	name    string
	trashed bool
}

func loadComp(ctx context.Context, q Querier, id string) (compInfo, bool) {
	var ci compInfo
	var trashed sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT name, trashed_at FROM compendiums WHERE id = ? AND deleted_at IS NULL`, id).Scan(&ci.name, &trashed); err != nil {
		return ci, false
	}
	ci.trashed = trashed.Valid
	return ci, true
}

// PlanMove works out the move without writing. q is a read transaction for a preview and the writer's transaction
// for a real move. The caller must already hold owner access to both Compendiums (checked again here).
func PlanMove(c *ops.Ctx, q Querier, in MoveIn) (*MovePlan, *moveState, error) {
	if in.SourceID == in.TargetID {
		return nil, nil, ops.Invalid("into_id", "A Compendium cannot be merged into itself.")
	}
	virtual := in.TargetID == ""
	need := []string{in.SourceID, in.TargetID}
	if virtual {
		need = need[:1]
	}
	for _, id := range need {
		role, ok := compaccess.Role(c, q, c.P.UserID, id)
		if !ok {
			return nil, nil, ops.NotFound("compendium")
		}
		if role != compaccess.Owner {
			return nil, nil, ops.Forbidden("you must be the owner of both Compendiums to move entities between them")
		}
	}
	src, ok := loadComp(c, q, in.SourceID)
	if !ok {
		return nil, nil, ops.NotFound("compendium")
	}
	dst := compInfo{name: in.NewName}
	if !virtual {
		if dst, ok = loadComp(c, q, in.TargetID); !ok {
			return nil, nil, ops.NotFound("compendium")
		}
	}
	// The registry sees only the Compendium in the path; the other one and everything it holds are checked here, in the
	// transaction that does the work.
	if src.trashed || dst.trashed {
		return nil, nil, ops.CompendiumInTrash()
	}
	crossing, pins := in.Crossing, in.KeyPins
	if crossing == "" {
		crossing = "refuse"
	}
	if pins == "" {
		pins = "refuse"
	}
	p := &MovePlan{SourceID: in.SourceID, SourceName: src.name, TargetID: in.TargetID, TargetName: dst.name,
		Crossing: crossing, KeyPins: pins, FollowProjects: in.FollowProjects,
		Items: []MoveItem{}, Unresolved: []string{}, RelationsCrossing: []CrossRelation{}, EventsCrossing: []CrossEvent{},
		FieldDefsCopied: []string{}, FieldDefsDifferent: []string{}, Projects: []ProjectEffect{}, Keys: []KeyPin{}, Blocking: []string{},
		StaysBehind: map[string]int{}}
	st := &moveState{byID: map[string]*moveEntity{}, movingID: map[string]bool{}, events: moveEvents{dropPeople: map[string][]string{}}}

	// Which entities.
	ids := in.EntityIDs
	if len(ids) == 0 {
		rows, err := q.QueryContext(c, `SELECT id FROM entities WHERE compendium_id = ? AND trashed_at IS NULL ORDER BY id LIMIT ?`, in.SourceID, MaxMoveEntities+1)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
	} else {
		seen := map[string]bool{}
		var uniq []string
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				uniq = append(uniq, id)
			}
		}
		sort.Strings(uniq)
		ids = uniq
	}
	if len(ids) > MaxMoveEntities {
		return nil, nil, ops.Invalid("entity_ids", fmt.Sprintf("One move takes at most %d entities. Move them in parts.", MaxMoveEntities))
	}
	if err := q.QueryRowContext(c, `SELECT count(*) FROM entities WHERE compendium_id = ? AND trashed_at IS NOT NULL`, in.SourceID).Scan(&p.TrashedStaying); err != nil {
		return nil, nil, err
	}

	// The entities, their clashes and the answers given.
	allNames := map[string]string{} // folded names (and renames) of entities that move, to catch two moving to one name
	for _, id := range ids {
		e, err := load(c, q, in.SourceID, id)
		if err != nil {
			if ops.AsError(err).Code == ops.CodeNotFound {
				return nil, nil, ops.Invalid("entity_ids", "One of the entities is not in the source Compendium.")
			}
			return nil, nil, err
		}
		if e.Trashed {
			return nil, nil, ops.Invalid("entity_ids", fmt.Sprintf("%q is in the trash. Restore it first, or leave it out.", e.Name))
		}
		me := &moveEntity{e: e, dropAlias: map[string]bool{}}
		item := MoveItem{EntityID: e.ID, Name: e.Name, Kind: e.Kind, Action: "move", Aliases: len(e.Aliases)}
		// What it runs into in the target.
		type word struct{ word, key string }
		words := []word{{e.Name, text.FoldTitle(e.Name)}}
		for _, a := range e.Aliases {
			words = append(words, word{a.Alias, text.FoldTitle(a.Alias)})
		}
		nameClash := false
		for i, w := range words {
			oid, oname, via, found, err := clash(c, q, in.TargetID, w.key, "")
			if err != nil {
				return nil, nil, err
			}
			if found {
				item.Clashes = append(item.Clashes, Clash{EntityID: oid, Name: oname, Via: via, Word: w.word})
				if i == 0 {
					nameClash = true
				}
			}
		}
		if res, given := in.Resolutions[e.ID]; given {
			item.Choice = res.Action
			switch res.Action {
			case "skip":
				item.Action = "skip"
			case "rename":
				nn := strings.TrimSpace(res.NewName)
				if err := checkLine("new_name", nn, maxName, true); err != nil {
					return nil, nil, err
				}
				k := text.FoldTitle(nn)
				if oid, oname, via, found, err := clash(c, q, in.TargetID, k, ""); err != nil {
					return nil, nil, err
				} else if found {
					return nil, nil, ops.ConflictWith(fmt.Sprintf("%q is already used as the %s of %q in the target.", nn, via, oname), map[string]any{"entity_id": oid})
				}
				item.Action, item.NewName = "rename", nn
				// its aliases that still clash must go (or the caller said drop them too: not offered here)
				for _, a := range e.Aliases {
					if hit := clashesWith(item.Clashes, a.Alias); hit {
						me.dropAlias[text.FoldTitle(a.Alias)] = true
						item.Dropped = append(item.Dropped, a.Alias)
					}
				}
			case "drop_aliases":
				if nameClash {
					p.Unresolved = append(p.Unresolved, e.ID)
					item.Action = "move"
					break
				}
				for _, a := range e.Aliases {
					if clashesWith(item.Clashes, a.Alias) {
						me.dropAlias[text.FoldTitle(a.Alias)] = true
						item.Dropped = append(item.Dropped, a.Alias)
					}
				}
			case "merge":
				if res.IntoID == "" {
					return nil, nil, ops.Invalid("resolutions", "A merge needs into_id: the entity of the target to merge into.")
				}
				in2 := MergeIn{ID: e.ID, IntoID: res.IntoID, FieldChoices: res.FieldChoices}
				if err := in2.Validate(); err != nil {
					return nil, nil, err
				}
				mp, _, dstE, err := planMergeAcross(c, q, in.SourceID, in.TargetID, in2, canOpenProject(c))
				if err != nil {
					return nil, nil, err
				}
				if mp.LockedOverrideConflicts > 0 {
					p.Blocking = append(p.Blocking, fmt.Sprintf("%d of your projects that you have not unlocked overrode both %q and %q: unlock them and preview again (their override would otherwise be deleted unseen)", mp.LockedOverrideConflicts, e.Name, dstE.Name))
				}
				me.merge, me.mergeIn, me.mergeDst = mp, in2, dstE
				item.Action, item.MergeInto, item.Merge = "merge", dstE.Name, mp
				for _, u := range mp.Unresolved {
					p.Unresolved = append(p.Unresolved, e.ID+":"+u)
				}
			default:
				return nil, nil, ops.Invalid("resolutions", "A resolution is rename, merge, drop_aliases or skip.")
			}
		} else if len(item.Clashes) > 0 {
			p.Unresolved = append(p.Unresolved, e.ID)
		}
		if len(item.Clashes) > 0 {
			p.Collisions++
		}
		if item.Action == "skip" {
			p.Staying++
			p.Items = append(p.Items, item)
			continue
		}
		// two moving entities may not end with one name
		key := text.FoldTitle(e.Name)
		if item.Action == "rename" {
			key = text.FoldTitle(item.NewName)
		}
		if item.Action != "merge" {
			if other, dup := allNames[key]; dup {
				return nil, nil, ops.Conflict(fmt.Sprintf("%q and %q would end with the same name in the target.", other, e.Name))
			}
			allNames[key] = e.Name
		}
		p.AliasesMoved += len(e.Aliases) - len(item.Dropped)
		p.AliasesDropped += len(item.Dropped)
		me.item = &item
		st.moving = append(st.moving, me)
		st.byID[e.ID] = me
		st.movingID[e.ID] = true
		p.Items = append(p.Items, item)
	}
	// p.Items holds copies: point each moving entity at its own
	for i := range p.Items {
		if me := st.byID[p.Items[i].EntityID]; me != nil {
			me.item = &p.Items[i]
		}
	}
	p.Moving = len(st.moving)
	sort.Strings(p.Unresolved)
	// a new name may not be another moving entity's alias either (names are checked above; aliases that stay with them are here)
	aliasOwner := map[string]string{}
	for _, m := range st.moving {
		if m.item.Action == "merge" {
			continue
		}
		for _, a := range m.e.Aliases {
			if k := text.FoldTitle(a.Alias); !m.dropAlias[k] {
				aliasOwner[k] = m.e.ID
			}
		}
	}
	for _, m := range st.moving {
		if m.item.Action != "rename" {
			continue
		}
		if owner, hit := aliasOwner[text.FoldTitle(m.item.NewName)]; hit && owner != m.e.ID {
			return nil, nil, ops.Conflict(fmt.Sprintf("%q is an alias of another entity that moves with it.", m.item.NewName))
		}
	}

	if err := planRelations(c, q, in, p, st); err != nil {
		return nil, nil, err
	}
	if err := planEvents(c, q, in, p, st); err != nil {
		return nil, nil, err
	}
	if err := planFieldDefs(c, q, in, p, st); err != nil {
		return nil, nil, err
	}
	if err := planProjects(c, q, in, p, st); err != nil {
		return nil, nil, err
	}
	if err := planKeys(c, q, in, p, st); err != nil {
		return nil, nil, err
	}
	for _, t := range []struct{ key, query string }{
		{"research_notes", `SELECT count(*) FROM research_notes WHERE compendium_id = ?`},
		{"tags", `SELECT count(*) FROM tags WHERE compendium_id = ?`},
		{"attachments", `SELECT count(*) FROM attachments WHERE compendium_id = ?`},
		{"members", `SELECT count(*) FROM compendium_members WHERE compendium_id = ?`},
		{"references", `SELECT count(*) FROM compendium_refs WHERE owner_kind = 'compendium' AND owner_id = ?`},
	} {
		var n int
		if err := q.QueryRowContext(c, t.query, in.SourceID).Scan(&n); err != nil {
			return nil, nil, err
		}
		p.StaysBehind[t.key] = n
	}
	moveIDs := make([]string, 0, len(st.moving))
	for _, m := range st.moving {
		moveIDs = append(moveIDs, m.e.ID)
	}
	var err error
	if p.LinksFollowing, err = countIn(c, q, `SELECT count(*) FROM project_entity_links WHERE entity_id IN (?)`, moveIDs); err != nil {
		return nil, nil, err
	}
	if p.OverridesKept, err = countIn(c, q, `SELECT count(*) FROM project_entity_overrides WHERE entity_id IN (?)`, moveIDs); err != nil {
		return nil, nil, err
	}
	if p.MentionRows, err = countIn(c, q, `SELECT count(*) FROM entity_mentions WHERE entity_id IN (?)`, moveIDs); err != nil {
		return nil, nil, err
	}
	if p.Redirects, err = countIn(c, q, `SELECT count(*) FROM entity_redirects WHERE to_id IN (?)`, moveIDs); err != nil {
		return nil, nil, err
	}
	sceneA, err := countIn(c, q, `SELECT count(*) FROM documents WHERE pov_entity_id IN (?)`, moveIDs)
	if err != nil {
		return nil, nil, err
	}
	sceneB, err := countIn(c, q, `SELECT count(*) FROM documents WHERE location_entity_id IN (?)`, moveIDs)
	if err != nil {
		return nil, nil, err
	}
	p.SceneRefs = sceneA + sceneB
	if len(p.Unresolved) == 0 && len(st.moving) == 0 && len(p.Items) == 0 {
		p.Blocking = append(p.Blocking, "there is nothing to move: the source has no live entities")
	}
	sort.Strings(p.Blocking)
	p.Fingerprint = p.fingerprint()
	return p, st, nil
}

func clashesWith(cl []Clash, word string) bool {
	for _, c := range cl {
		if c.Word == word {
			return true
		}
	}
	return false
}

func planRelations(c *ops.Ctx, q Querier, in MoveIn, p *MovePlan, st *moveState) error {
	rows, err := q.QueryContext(c, `SELECT r.id, r.from_entity_id, r.to_entity_id, r.kind, f.name, t.name FROM entity_relations r
		JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
		WHERE r.compendium_id = ? ORDER BY r.id`, in.SourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, from, to, kind, fname, tname string
		if err := rows.Scan(&id, &from, &to, &kind, &fname, &tname); err != nil {
			return err
		}
		a, b := st.movingID[from], st.movingID[to]
		switch {
		case a && b:
			p.RelationsMoved++
		case a || b:
			p.RelationsCrossing = append(p.RelationsCrossing, CrossRelation{ID: id, From: fname, To: tname, Kind: kind})
			st.crossRel = append(st.crossRel, id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(p.RelationsCrossing) > 0 && p.Crossing != "drop" {
		p.Blocking = append(p.Blocking, fmt.Sprintf("%d relationships would join the two Compendiums: choose crossing=drop to drop them (kept in the before-state), or move both ends", len(p.RelationsCrossing)))
	}
	return nil
}

func planEvents(c *ops.Ctx, q Querier, in MoveIn, p *MovePlan, st *moveState) error {
	type ev struct {
		id, title string
		people    []string
		loc       sql.NullString
	}
	rows, err := q.QueryContext(c, `SELECT id, title, location_entity_id FROM timeline_events WHERE compendium_id = ? ORDER BY id`, in.SourceID)
	if err != nil {
		return err
	}
	var evs []*ev
	for rows.Next() {
		e := &ev{}
		if err := rows.Scan(&e.id, &e.title, &e.loc); err != nil {
			rows.Close()
			return err
		}
		evs = append(evs, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	byEv := map[string]*ev{}
	for _, e := range evs {
		byEv[e.id] = e
	}
	prows, err := q.QueryContext(c, `SELECT p.event_id, p.entity_id FROM timeline_event_people p JOIN timeline_events e ON e.id = p.event_id WHERE e.compendium_id = ? ORDER BY p.event_id, p.entity_id`, in.SourceID)
	if err != nil {
		return err
	}
	for prows.Next() {
		var eid, ent string
		if err := prows.Scan(&eid, &ent); err != nil {
			prows.Close()
			return err
		}
		if e := byEv[eid]; e != nil {
			e.people = append(e.people, ent)
		}
	}
	if err := prows.Err(); err != nil {
		_ = prows.Close()
		return err
	}
	if err := prows.Close(); err != nil {
		return err
	}
	nameCache := map[string]string{}
	name := func(id string) string {
		if me, ok := st.byID[id]; ok {
			return me.e.Name
		}
		if n, ok := nameCache[id]; ok {
			return n
		}
		var n string
		_ = q.QueryRowContext(c, `SELECT name FROM entities WHERE id = ?`, id).Scan(&n)
		nameCache[id] = n
		return n
	}
	for _, e := range evs {
		var movingPeople []string
		allMove := len(e.people) > 0
		for _, pid := range e.people {
			if st.movingID[pid] {
				movingPeople = append(movingPeople, pid)
			} else {
				allMove = false
			}
		}
		locMoves := e.loc.Valid && st.movingID[e.loc.String]
		switch {
		case len(e.people) == 0 && !locMoves:
			p.EventsStaying++
		case allMove:
			st.events.moving = append(st.events.moving, e.id)
			p.EventsMoved++
			if e.loc.Valid && !st.movingID[e.loc.String] { // its place stays behind
				ce := CrossEvent{EventID: e.id, Title: e.title, Location: true}
				p.EventsCrossing = append(p.EventsCrossing, ce)
				st.events.clearLoc = append(st.events.clearLoc, e.id)
			}
		case len(movingPeople) > 0 || locMoves:
			ce := CrossEvent{EventID: e.id, Title: e.title, Location: locMoves}
			for _, pid := range movingPeople {
				ce.People = append(ce.People, name(pid))
			}
			p.EventsCrossing = append(p.EventsCrossing, ce)
			if len(movingPeople) > 0 {
				st.events.dropPeople[e.id] = movingPeople
			}
			if locMoves {
				st.events.clearLoc = append(st.events.clearLoc, e.id)
			}
		default:
			p.EventsStaying++
		}
	}
	if len(st.events.moving) > 0 {
		n, err := countIn(c, q, `SELECT count(*) FROM timeline_event_docs WHERE event_id IN (?)`, st.events.moving)
		if err != nil {
			return err
		}
		p.EventScenes = n
	}
	if len(p.EventsCrossing) > 0 && p.Crossing != "drop" {
		p.Blocking = append(p.Blocking, fmt.Sprintf("%d timeline events would be split between the two Compendiums: choose crossing=drop to take the moved people off them (kept in the before-state), or move all of their people", len(p.EventsCrossing)))
	}
	return nil
}

func planFieldDefs(c *ops.Ctx, q Querier, in MoveIn, p *MovePlan, st *moveState) error {
	kinds := map[string]bool{}
	for _, m := range st.moving {
		kinds[m.e.Kind] = true
	}
	var ks []string
	for k := range kinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		sd, err := loadDefs(c, q, in.SourceID, k)
		if err != nil {
			return err
		}
		td, err := loadDefs(c, q, in.TargetID, k)
		if err != nil {
			return err
		}
		keys := make([]string, 0, len(sd))
		for key := range sd {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			// only definitions a moving entity actually has a value for matter
			used := false
			for _, m := range st.moving {
				if m.e.Kind == k && m.e.Fields[key] != "" {
					used = true
					break
				}
			}
			if !used {
				continue
			}
			if t, has := td[key]; !has {
				p.FieldDefsCopied = append(p.FieldDefsCopied, k+"."+key)
				st.copyDefs = append(st.copyDefs, sd[key])
			} else if t.Type != sd[key].Type {
				p.FieldDefsDifferent = append(p.FieldDefsDifferent, k+"."+key)
			}
		}
	}
	return nil
}

func planProjects(c *ops.Ctx, q Querier, in MoveIn, p *MovePlan, st *moveState) error {
	// Which projects use which Compendium, and how.
	type use struct{ source, target string } // how each is used: primary, reference or ""
	uses := map[string]*use{}
	add := func(query string, isSource bool, how string, args ...any) error {
		rows, err := q.QueryContext(c, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var pid string
			if err := rows.Scan(&pid); err != nil {
				return err
			}
			u := uses[pid]
			if u == nil {
				u = &use{}
				uses[pid] = u
			}
			if isSource {
				if u.source == "" {
					u.source = how
				}
			} else if u.target == "" {
				u.target = how
			}
		}
		return rows.Err()
	}
	for _, t := range []struct {
		id     string
		source bool
	}{{in.SourceID, true}, {in.TargetID, false}} {
		if err := add(`SELECT id FROM projects WHERE compendium_id = ? AND deleted_at IS NULL`, t.source, "primary", t.id); err != nil {
			return err
		}
		if err := add(`SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = ?`, t.source, "reference", t.id); err != nil {
			return err
		}
	}
	moveIDs := make([]string, 0, len(st.moving))
	for _, m := range st.moving {
		moveIDs = append(moveIDs, m.e.ID)
	}
	// Projects that link a moved entity (a cameo) see it whatever happens to the Compendiums.
	linked := map[string]map[string]bool{}
	for _, ch := range chunks(moveIDs, moveChunk) {
		rows, err := q.QueryContext(c, `SELECT project_id, entity_id FROM project_entity_links WHERE entity_id IN (`+inClause(len(ch))+`)`, anyArgs(ch)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pid, eid string
			if err := rows.Scan(&pid, &eid); err != nil {
				rows.Close()
				return err
			}
			if linked[pid] == nil {
				linked[pid] = map[string]bool{}
			}
			linked[pid][eid] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	pids := make([]string, 0, len(uses))
	for pid := range uses {
		pids = append(pids, pid)
	}
	sort.Strings(pids)
	canOpen := canOpenProject(c)
	for _, pid := range pids {
		u := uses[pid]
		if u.source == "" || u.target != "" {
			continue // sees the moved entities before and after
		}
		if !canOpen(pid) {
			p.HiddenProjects++
			continue
		}
		eff := ProjectEffect{ProjectID: pid, Uses: u.source}
		_ = q.QueryRowContext(c, `SELECT title FROM projects WHERE id = ?`, pid).Scan(&eff.Title)
		for _, id := range moveIDs {
			if !linked[pid][id] {
				eff.EntitiesLeave++
			}
		}
		var err error
		if eff.OverridesDorm, err = countInProject(c, q, `SELECT count(*) FROM project_entity_overrides WHERE project_id = ? AND entity_id IN (?)`, pid, moveIDs); err != nil {
			return err
		}
		if eff.MentionRows, err = countInProject(c, q, `SELECT count(*) FROM entity_mentions WHERE project_id = ? AND entity_id IN (?)`, pid, moveIDs); err != nil {
			return err
		}
		if in.FollowProjects {
			eff.Follow = "reference added"
			var role string
			_ = q.QueryRowContext(c, `SELECT m.role FROM project_members m WHERE m.project_id = ? AND m.user_id = ?`, pid, c.P.UserID).Scan(&role)
			var trashed sql.NullString
			_ = q.QueryRowContext(c, `SELECT trashed_at FROM projects WHERE id = ?`, pid).Scan(&trashed)
			switch {
			case trashed.Valid:
				eff.Follow = "not added: the project is in the trash"
			case role != ops.ProjectOwner && c.P.Role != ops.RoleAdmin:
				eff.Follow = "not added: only the project's owner can change what it uses"
			default:
				st.follow = append(st.follow, pid)
			}
		}
		p.Projects = append(p.Projects, eff)
	}
	return nil
}

func countInProject(ctx context.Context, q Querier, query, project string, ids []string) (int, error) {
	total := 0
	for _, ch := range chunks(ids, moveChunk) {
		var n int
		args := append([]any{project}, anyArgs(ch)...)
		if err := q.QueryRowContext(ctx, strings.Replace(query, "(?)", "("+inClause(len(ch))+")", 1), args...).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

func planKeys(c *ops.Ctx, q Querier, in MoveIn, p *MovePlan, st *moveState) error {
	rows, err := q.QueryContext(c, `SELECT k.id, k.name, k.user_id FROM api_keys k WHERE k.compendium_id = ? AND k.revoked_at IS NULL
		AND (k.expires_at IS NULL OR k.expires_at > ?) ORDER BY k.id`, in.SourceID, db.Now())
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, owner string
		if err := rows.Scan(&id, &name, &owner); err != nil {
			return err
		}
		if owner == c.P.UserID {
			p.Keys = append(p.Keys, KeyPin{KeyID: id, Name: name, Mine: true})
		} else {
			p.HiddenKeys++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(p.Keys)+p.HiddenKeys > 0 && p.KeyPins == "refuse" {
		p.Blocking = append(p.Blocking, fmt.Sprintf("%d API keys are pinned to the source Compendium: choose key_pins=repoint (they then reach the target's other material too), or leave (they keep their pin to the source)", len(p.Keys)+p.HiddenKeys))
	}
	return nil
}

// ---- applying ----------------------------------------------------------------------------------------------------

// Bundle is the before-state of a move: what it would take to put things back, in ids and the text that was
// changed or dropped. It is kept in compendium_changes in the move's own transaction and returned to the caller.
type Bundle struct {
	Kind             string              `json:"kind"`
	SourceID         string              `json:"source_id"`
	TargetID         string              `json:"target_id"`
	Moved            []BundleEntity      `json:"moved"`
	Merged           []BundleMerge       `json:"merged"`
	RelationsDropped []map[string]any    `json:"relations_dropped"`
	PeopleDropped    []map[string]string `json:"event_people_dropped"`
	LocationsCleared []map[string]string `json:"event_locations_cleared"`
	EventsMoved      []string            `json:"events_moved"`
	FieldDefsCopied  []string            `json:"field_defs_copied"`
	KeysRepointed    []string            `json:"keys_repointed"`
	RefsAdded        []string            `json:"project_refs_added"`
	AliasesDropped   []map[string]string `json:"aliases_dropped"`
}

// BundleEntity is an entity that moved, as it was.
type BundleEntity struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	NewName  string `json:"new_name,omitempty"`
	Revision int    `json:"revision"`
}

// BundleMerge is an entity that was merged away, with everything it held.
type BundleMerge struct {
	ID       string     `json:"id"`
	IntoID   string     `json:"into_id"`
	Snapshot Entity     `json:"snapshot"`
	Plan     *MergePlan `json:"plan"`
}

// ApplyMove carries the plan out inside tx. fingerprint is what the preview returned; the plan is recomputed here
// and a different one is refused, so a change made between the preview and now is never applied unseen.
func ApplyMove(c *ops.Ctx, tx *sql.Tx, in MoveIn, fingerprint string) (*MovePlan, *Bundle, error) {
	p, st, err := PlanMove(c, tx, in)
	if err != nil {
		return nil, nil, err
	}
	if fingerprint == "" || fingerprint != p.Fingerprint {
		return nil, nil, ops.ConflictWith("What would change is not what the preview showed: something changed since. Preview again.",
			map[string]any{"reason": "plan_changed", "fingerprint": p.Fingerprint})
	}
	return applyPlanned(c, tx, in, p, st)
}

// ApplyMoveToNew is ApplyMove for a split, after the target has been made in the same transaction: the plan was
// already checked against its fingerprint with the target still to come.
func ApplyMoveToNew(c *ops.Ctx, tx *sql.Tx, in MoveIn) (*MovePlan, *Bundle, error) {
	p, st, err := PlanMove(c, tx, in)
	if err != nil {
		return nil, nil, err
	}
	return applyPlanned(c, tx, in, p, st)
}

// moveHook, when set, runs once inside the write transaction of a move, after the plan was checked and before anything is
// written. A test uses it to start other writers at exactly that moment: they cannot run until the move commits, which is
// the specified order, and no timing is involved. It is nil in production.
var (
	moveHookMu sync.Mutex
	moveHook   func()
)

// SetMoveHook installs the hook above (one-shot) and returns a function that removes it. For tests.
func SetMoveHook(f func()) (remove func()) {
	moveHookMu.Lock()
	moveHook = f
	moveHookMu.Unlock()
	return func() { moveHookMu.Lock(); moveHook = nil; moveHookMu.Unlock() }
}

func runMoveHook() {
	moveHookMu.Lock()
	f := moveHook
	moveHook = nil
	moveHookMu.Unlock()
	if f != nil {
		f()
	}
}

func applyPlanned(c *ops.Ctx, tx *sql.Tx, in MoveIn, p *MovePlan, st *moveState) (*MovePlan, *Bundle, error) {
	runMoveHook()
	if len(p.Unresolved) > 0 {
		return nil, nil, &ops.Error{Code: ops.CodeInvalid, Field: "resolutions",
			Message: "Some entities need an answer before the move: " + strings.Join(p.Unresolved, ", ") + ".", Data: map[string]any{"unresolved": p.Unresolved}}
	}
	if len(p.Blocking) > 0 {
		return nil, nil, &ops.Error{Code: ops.CodeConflict, Message: "The move is refused: " + strings.Join(p.Blocking, "; ") + ".",
			Data: map[string]any{"reason": "blocked", "blocking": p.Blocking}}
	}
	now := db.Now()
	b := &Bundle{Kind: "move", SourceID: in.SourceID, TargetID: in.TargetID, Moved: []BundleEntity{}, Merged: []BundleMerge{}, RelationsDropped: []map[string]any{},
		PeopleDropped: []map[string]string{}, LocationsCleared: []map[string]string{}, EventsMoved: []string{}, FieldDefsCopied: []string{},
		KeysRepointed: []string{}, RefsAdded: []string{}, AliasesDropped: []map[string]string{}}
	exec := func(q string, args ...any) error {
		_, err := tx.Exec(q, args...)
		return err
	}
	// Stamps first: the projects of both Compendiums are rescanned whatever happens next; a worker sees the move and the
	// stamp or neither, because both are in this transaction.
	before := map[string]bool{}
	for _, cid := range []string{in.SourceID, in.TargetID} {
		ids, err := AffectedProjects(c, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range ids {
			before[id] = true
		}
	}

	// Crossing relationships and event people, dropped with a record.
	for _, id := range st.crossRel {
		var from, to, kind, label, inv, notes, created string
		var sym int
		if err := tx.QueryRow(`SELECT from_entity_id, to_entity_id, kind, label, inverse_label, symmetric, notes, created_at FROM entity_relations WHERE id = ?`, id).Scan(&from, &to, &kind, &label, &inv, &sym, &notes, &created); err != nil {
			return nil, nil, err
		}
		b.RelationsDropped = append(b.RelationsDropped, map[string]any{"id": id, "from": from, "to": to, "kind": kind, "label": label, "inverse_label": inv, "symmetric": sym == 1, "notes": notes, "created_at": created, "compendium_id": in.SourceID})
		if err := exec(`DELETE FROM entity_relations WHERE id = ?`, id); err != nil {
			return nil, nil, err
		}
	}
	evIDs := make([]string, 0, len(st.events.dropPeople))
	for id := range st.events.dropPeople {
		evIDs = append(evIDs, id)
	}
	sort.Strings(evIDs)
	for _, ev := range evIDs {
		for _, ent := range st.events.dropPeople[ev] {
			var role, created string
			if err := tx.QueryRow(`SELECT role, created_at FROM timeline_event_people WHERE event_id = ? AND entity_id = ?`, ev, ent).Scan(&role, &created); err != nil {
				return nil, nil, err
			}
			b.PeopleDropped = append(b.PeopleDropped, map[string]string{"event_id": ev, "entity_id": ent, "role": role, "created_at": created})
			if err := exec(`DELETE FROM timeline_event_people WHERE event_id = ? AND entity_id = ?`, ev, ent); err != nil {
				return nil, nil, err
			}
		}
	}
	for _, ev := range st.events.clearLoc {
		var loc sql.NullString
		if err := tx.QueryRow(`SELECT location_entity_id FROM timeline_events WHERE id = ?`, ev).Scan(&loc); err != nil {
			return nil, nil, err
		}
		b.LocationsCleared = append(b.LocationsCleared, map[string]string{"event_id": ev, "entity_id": loc.String})
		if err := exec(`UPDATE timeline_events SET location_entity_id = NULL, revision = revision + 1, updated_at = ? WHERE id = ?`, now, ev); err != nil {
			return nil, nil, err
		}
	}
	// Relationships whose two ends both move belong to the target from here on.
	for _, m := range st.moving {
		if err := exec(`UPDATE entity_relations SET compendium_id = ? WHERE from_entity_id = ? AND compendium_id = ?`, in.TargetID, m.e.ID, in.SourceID); err != nil {
			return nil, nil, err
		}
	}
	// Events whose people all move.
	for _, ev := range st.events.moving {
		if err := exec(`UPDATE timeline_events SET compendium_id = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, in.TargetID, now, ev); err != nil {
			return nil, nil, err
		}
		b.EventsMoved = append(b.EventsMoved, ev)
	}
	// Field definitions the target lacks.
	for _, d := range st.copyDefs {
		var pos float64
		_ = tx.QueryRow(`SELECT COALESCE(max(position), 0) + 65536 FROM entity_field_defs WHERE compendium_id = ? AND kind = ?`, in.TargetID, d.Kind).Scan(&pos)
		ch, _ := json.Marshal(d.Choices)
		if err := exec(`INSERT INTO entity_field_defs(id, compendium_id, kind, key, label, type, choices_json, position, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			db.NewID("fld"), in.TargetID, d.Kind, d.Key, d.Label, d.Type, string(ch), pos, now); err != nil {
			return nil, nil, err
		}
		b.FieldDefsCopied = append(b.FieldDefsCopied, d.Kind+"."+d.Key)
	}
	// The entities themselves.
	for _, m := range st.moving {
		e := m.e
		for _, a := range e.Aliases {
			if m.dropAlias[text.FoldTitle(a.Alias)] {
				b.AliasesDropped = append(b.AliasesDropped, map[string]string{"entity_id": e.ID, "alias": a.Alias, "notes": a.Notes})
				if err := exec(`DELETE FROM entity_aliases WHERE id = ?`, a.ID); err != nil {
					return nil, nil, err
				}
			}
		}
		switch m.item.Action {
		case "merge":
			// Into the target first under a key nothing can clash with, then merged by entity.merge's own steps.
			if err := exec(`UPDATE entities SET compendium_id = ?, name_key = name_key || '~moving~' || id WHERE id = ?`, in.TargetID, e.ID); err != nil {
				return nil, nil, err
			}
			b.Merged = append(b.Merged, BundleMerge{ID: e.ID, IntoID: m.mergeDst.ID, Snapshot: e, Plan: m.merge})
			if err := applyMergeAcross(c, tx, in.TargetID, m.mergeIn, m.merge, &e, m.mergeDst); err != nil {
				return nil, nil, err
			}
			continue
		case "rename":
			sortKey := text.FoldTitle(m.item.NewName)
			if e.SortName != "" {
				sortKey = text.FoldTitle(e.SortName)
			}
			if err := exec(`UPDATE entities SET compendium_id = ?, name = ?, name_key = ?, sort_key = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
				in.TargetID, m.item.NewName, text.FoldTitle(m.item.NewName), sortKey, now, e.ID); err != nil {
				return nil, nil, err
			}
			b.Moved = append(b.Moved, BundleEntity{ID: e.ID, Name: e.Name, NewName: m.item.NewName, Revision: e.Revision})
		default:
			if err := exec(`UPDATE entities SET compendium_id = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, in.TargetID, now, e.ID); err != nil {
				return nil, nil, err
			}
			b.Moved = append(b.Moved, BundleEntity{ID: e.ID, Name: e.Name, Revision: e.Revision})
		}
		if err := exec(`UPDATE entity_redirects SET compendium_id = ? WHERE to_id = ?`, in.TargetID, e.ID); err != nil {
			return nil, nil, err
		}
		if err := syncText(tx, e.ID); err != nil {
			return nil, nil, err
		}
	}
	// Keys pinned to the source.
	if p.KeyPins == "repoint" {
		rows, err := tx.Query(`SELECT k.id, k.user_id FROM api_keys k WHERE k.compendium_id = ? AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > ?) ORDER BY k.id`, in.SourceID, db.Now())
		if err != nil {
			return nil, nil, err
		}
		type kk struct{ id, user string }
		var ks []kk
		for rows.Next() {
			var k kk
			if err := rows.Scan(&k.id, &k.user); err != nil {
				rows.Close()
				return nil, nil, err
			}
			ks = append(ks, k)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
		for _, k := range ks {
			if _, ok := compaccess.Role(c, tx, k.user, in.TargetID); !ok {
				continue // its owner is not on the target: the key keeps its pin rather than widening past what its owner may read
			}
			if err := exec(`UPDATE api_keys SET compendium_id = ? WHERE id = ?`, in.TargetID, k.id); err != nil {
				return nil, nil, err
			}
			b.KeysRepointed = append(b.KeysRepointed, k.id)
		}
	}
	// Projects the caller owns that used the source keep reading what moved: a reference to the target.
	for _, pid := range st.follow {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ? AND target_compendium_id = ?`, pid, in.TargetID).Scan(&n); err != nil {
			return nil, nil, err
		}
		if n > 0 {
			continue
		}
		if err := exec(`INSERT INTO compendium_refs(id, owner_kind, owner_id, target_compendium_id, mode, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
			db.NewID("cref"), "project", pid, in.TargetID, "reference", c.P.UserID, now); err != nil {
			return nil, nil, err
		}
		b.RefsAdded = append(b.RefsAdded, pid)
	}
	// The stamps, for every project either Compendium reaches, before and after.
	for _, cid := range []string{in.SourceID, in.TargetID} {
		ids, err := AffectedProjects(c, tx, cid)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range ids {
			before[id] = true
		}
	}
	for id := range before {
		if err := exec(`INSERT INTO mention_state(project_id, stamp) SELECT id, lower(hex(randomblob(8))) FROM projects WHERE id = ? AND deleted_at IS NULL
			ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp`, id); err != nil {
			return nil, nil, err
		}
	}
	for _, cid := range []string{in.SourceID, in.TargetID} {
		if err := exec(`UPDATE compendiums SET updated_at = ? WHERE id = ?`, now, cid); err != nil {
			return nil, nil, err
		}
	}
	return p, b, nil
}

package entities

import (
	"context"
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
)

// RelationKinds mirror the CHECK constraint in migration 0500.
var RelationKinds = []string{"family", "ally", "rival", "mentor", "romantic", "member", "custom"}

const (
	maxLabel     = 100
	relationsCap = 500
)

func registerRelations(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "entity.relation.list", Method: "GET", Path: "/api/v1/entities/{id}/relations",
		Desc: "List an entity's relationships with live entities, as they read from this entity: direction out, in or both, and the label for that side. Up to 500.",
		Auth: via(compaccess.Reader),
	}, relationList)
	ops.Register(r, ops.Op{
		Name: "entity.relation.add", Method: "POST", Path: "/api/v1/entities/{id}/relations", Status: 201,
		Desc: "Relate this entity to another in the same Compendium with a typed edge (family, ally, rival, mentor, romantic, member or custom) and a label that reads from this side (\"mother of\"). " +
			"inverse_label is how it reads from the other side (\"daughter of\"); a symmetric edge reads the same both ways. The same edge twice is a 409. Editor.",
		Auth: via(compaccess.Editor),
	}, relationAdd)
	ops.Register(r, ops.Op{
		Name: "entity.relation.remove", Permanent: true, Method: "DELETE", Path: "/api/v1/entities/{id}/relations",
		Desc: "Remove a relationship that touches this entity, by the relation id entity.relation.list gives. Editor.",
		Auth: via(compaccess.Editor),
	}, relationRemove)
}

// loadRelations lists an entity's relationships as read from it.
func loadRelations(ctx context.Context, q queryer, compendiumID, id string) ([]Relation, bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.from_entity_id, r.kind, r.label, r.inverse_label, r.symmetric, r.notes, r.created_at, o.id, o.name
		FROM entity_relations r
		JOIN entities o ON o.id = CASE WHEN r.from_entity_id = ?1 THEN r.to_entity_id ELSE r.from_entity_id END
		WHERE (r.from_entity_id = ?1 OR r.to_entity_id = ?1) AND r.compendium_id = ?2 AND o.trashed_at IS NULL
		ORDER BY o.sort_key, r.kind, r.label, r.id LIMIT `+itoa(relationsCap+1), id, compendiumID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []Relation{}
	for rows.Next() {
		var r Relation
		var from, inverse string
		var symmetric bool
		if err := rows.Scan(&r.ID, &from, &r.Kind, &r.Label, &inverse, &symmetric, &r.Notes, &r.CreatedAt, &r.OtherID, &r.OtherName); err != nil {
			return nil, false, err
		}
		switch {
		case symmetric:
			r.Direction = "both"
		case from == id:
			r.Direction = "out"
		default:
			r.Direction = "in"
			if inverse != "" {
				r.Label = inverse
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > relationsCap {
		return out[:relationsCap], true, nil
	}
	return out, false, nil
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

// RelationListIn names the entity.
type RelationListIn struct {
	ID string `path:"id" json:"-" desc:"Entity id."`
}

// EntityID lets the registry find the owning Compendium.
func (in RelationListIn) EntityID() string { return in.ID }

// RelationListOut lists relationships.
type RelationListOut struct {
	Items     []Relation `json:"items"`
	Truncated bool       `json:"truncated,omitempty"`
}

func relationList(c *ops.Ctx, in RelationListIn) (RelationListOut, error) {
	cid, ok := ownerOf(c, c.DB().Read(), in.ID)
	if !ok {
		return RelationListOut{}, ops.NotFound("entity")
	}
	rels, trunc, err := loadRelations(c, c.DB().Read(), cid, in.ID)
	return RelationListOut{Items: rels, Truncated: trunc}, err
}

// RelationAddIn adds a relationship.
type RelationAddIn struct {
	ID           string `path:"id" json:"-" desc:"The entity the relationship reads from (the source)."`
	ToEntityID   string `json:"to_entity_id" ref:"entity" desc:"The other entity, in the same Compendium."`
	Kind         string `json:"kind,omitempty" enum:"family,ally,rival,mentor,romantic,member,custom" desc:"Default custom."`
	Label        string `json:"label,omitempty" desc:"How it reads from the source, e.g. \"mother of\". Required for custom; defaults to the kind."`
	InverseLabel string `json:"inverse_label,omitempty" desc:"How it reads from the other side, e.g. \"daughter of\"."`
	Symmetric    bool   `json:"symmetric,omitempty" desc:"The same both ways (siblings, rivals): the inverse label is not used."`
	Notes        string `json:"notes,omitempty" desc:"Up to 1,000 characters."`
}

// EntityID lets the registry find the owning Compendium.
func (in RelationAddIn) EntityID() string { return in.ID }

// Validate trims and checks the input.
func (in *RelationAddIn) Validate() error {
	if in.ToEntityID == "" {
		return ops.Invalid("to_entity_id", "Say which entity this one is related to.")
	}
	if in.ToEntityID == in.ID {
		return ops.Invalid("to_entity_id", "An entity cannot be related to itself.")
	}
	if in.Kind == "" {
		in.Kind = "custom"
	}
	if !contains(RelationKinds, in.Kind) {
		return ops.Invalid("kind", "Kind must be one of: "+strings.Join(RelationKinds, ", ")+".")
	}
	in.Label, in.InverseLabel, in.Notes = strings.TrimSpace(in.Label), strings.TrimSpace(in.InverseLabel), strings.TrimSpace(in.Notes)
	if in.Label == "" && in.Kind == "custom" {
		return ops.Invalid("label", "A custom relationship needs a label.")
	}
	if in.Label == "" {
		in.Label = in.Kind
	}
	if err := checkLine("label", in.Label, maxLabel, false); err != nil {
		return err
	}
	if err := checkLine("inverse_label", in.InverseLabel, maxLabel, false); err != nil {
		return err
	}
	if in.Symmetric {
		in.InverseLabel = ""
	}
	if len([]rune(in.Notes)) > maxNotes {
		return ops.Invalid("notes", "Notes are limited to 1,000 characters.")
	}
	return checkText("notes", in.Notes, maxNotes*4)
}

// RelationItem wraps the relationship as read from the source.
type RelationItem struct {
	Item Relation `json:"item"`
}

func relationAdd(c *ops.Ctx, in RelationAddIn) (RelationItem, error) {
	var out RelationItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		var fromTrashed, toTrashed bool
		var toName string
		if err := tx.QueryRow(`SELECT trashed_at IS NOT NULL FROM entities WHERE id = ?`, in.ID).Scan(&fromTrashed); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT name, trashed_at IS NOT NULL FROM entities WHERE id = ? AND compendium_id = ?`, in.ToEntityID, cid).Scan(&toName, &toTrashed); err != nil {
			if err == sql.ErrNoRows {
				return ops.Invalid("to_entity_id", "There is no such entity in this Compendium.")
			}
			return err
		}
		if fromTrashed || toTrashed {
			return ops.Conflict("An entity in the trash cannot take part in a new relationship. Restore it first.")
		}
		// The same edge, in either direction when it is symmetric, is a duplicate.
		var dup int
		symInt := 0
		if in.Symmetric {
			symInt = 1
		}
		if err := tx.QueryRow(`SELECT count(*) FROM entity_relations WHERE kind = ? AND label = ? AND
			((from_entity_id = ? AND to_entity_id = ?) OR (? = 1 AND from_entity_id = ? AND to_entity_id = ?))`,
			in.Kind, in.Label, in.ID, in.ToEntityID, symInt, in.ToEntityID, in.ID).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return ops.Conflict("Those two entities already have that relationship.")
		}
		id := db.NewID("rel")
		now := db.Now()
		sym := 0
		if in.Symmetric {
			sym = 1
		}
		if _, err := tx.Exec(`INSERT INTO entity_relations(id, compendium_id, from_entity_id, to_entity_id, kind, label, inverse_label, symmetric, notes, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, id, cid, in.ID, in.ToEntityID, in.Kind, in.Label, in.InverseLabel, sym, in.Notes, now); err != nil {
			if db.IsUniqueViolation(err) {
				return ops.Conflict("Those two entities already have that relationship.")
			}
			return err
		}
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		if err := touch(tx, in.ToEntityID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.relation.added", CompendiumID: cid, Payload: map[string]any{"entity_id": in.ID, "to_entity_id": in.ToEntityID}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		rels, _, err := loadRelations(c, tx, cid, in.ID)
		if err != nil {
			return err
		}
		for _, r := range rels {
			if r.ID == id {
				out.Item = r
			}
		}
		return nil
	})
	return out, err
}

// RelationRemoveIn removes a relationship.
type RelationRemoveIn struct {
	ID         string `path:"id" json:"-" desc:"Entity id; the relationship must touch it."`
	RelationID string `query:"relation_id" desc:"The relationship to remove."`
}

// EntityID lets the registry find the owning Compendium.
func (in RelationRemoveIn) EntityID() string { return in.ID }

// Validate checks the shape.
func (in *RelationRemoveIn) Validate() error {
	if in.RelationID == "" {
		return ops.Invalid("relation_id", "Say which relationship.")
	}
	return nil
}

// RelationRemoveOut reports a removal.
type RelationRemoveOut struct {
	Removed bool `json:"removed" desc:"False when this entity had no such relationship."`
}

func relationRemove(c *ops.Ctx, in RelationRemoveIn) (RelationRemoveOut, error) {
	var out RelationRemoveOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		var from, to string
		if err := tx.QueryRow(`SELECT from_entity_id, to_entity_id FROM entity_relations WHERE id = ? AND compendium_id = ? AND (from_entity_id = ? OR to_entity_id = ?)`,
			in.RelationID, cid, in.ID, in.ID).Scan(&from, &to); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}
		if _, err := tx.Exec(`DELETE FROM entity_relations WHERE id = ?`, in.RelationID); err != nil {
			return err
		}
		now := db.Now()
		for _, id := range []string{from, to} {
			if err := touch(tx, id, now); err != nil {
				return err
			}
		}
		out.Removed = true
		c.Emit(ops.Event{Type: "entity.relation.removed", CompendiumID: cid, Payload: map[string]any{"entity_id": in.ID}})
		return c.FlushTo(tx)
	})
	return out, err
}

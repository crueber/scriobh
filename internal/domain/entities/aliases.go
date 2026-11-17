package entities

import (
	"context"
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

func registerAliases(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "entity.alias.add", Method: "POST", Path: "/api/v1/entities/{id}/aliases", Status: 201,
		Desc: "Add an alias (a nickname, a title, a former name) to an entity. An alias another live entity in the Compendium already uses as a name or alias, or that is this entity's own name, is refused with a 409 that names it: " +
			"a mention of that word would not say which entity it means. Editor.",
		Auth: via(compaccess.Editor),
	}, aliasAdd)
	ops.Register(r, ops.Op{
		Name: "entity.alias.remove", Permanent: true, Method: "DELETE", Path: "/api/v1/entities/{id}/aliases",
		Desc: "Remove one alias from an entity, by the alias id entity.get lists. Editor.",
		Auth: via(compaccess.Editor),
	}, aliasRemove)
}

// insertAlias adds an alias after checking it against the Compendium's live
// names and aliases. entityID's own name is not a valid alias.
func insertAlias(ctx context.Context, tx *sql.Tx, compendiumID, entityID, alias, notes, now string) error {
	key := text.FoldTitle(alias)
	if oid, oname, v, found, err := clash(ctx, tx, compendiumID, key, entityID); err != nil {
		return err
	} else if found {
		return clashError("The alias", alias, oid, oname, v)
	}
	var own int
	if err := tx.QueryRow(`SELECT count(*) FROM entities WHERE id = ? AND name_key = ?`, entityID, key).Scan(&own); err != nil {
		return err
	}
	if own > 0 {
		return ops.Conflict("That is already this entity's name; an alias must be another name.")
	}
	if _, err := tx.Exec(`INSERT INTO entity_aliases(id, entity_id, alias, alias_key, notes, created_at) VALUES (?,?,?,?,?,?)`,
		db.NewID("ali"), entityID, alias, key, notes, now); err != nil {
		if db.IsUniqueViolation(err) {
			return ops.Conflict("This entity already has that alias.")
		}
		return err
	}
	return nil
}

// AliasAddIn adds an alias.
type AliasAddIn struct {
	ID    string `path:"id" json:"-" desc:"Entity id."`
	Alias string `json:"alias" desc:"Up to 200 characters on one line."`
	Notes string `json:"notes,omitempty" desc:"Where the alias comes from or who uses it, up to 1,000 characters."`
}

// EntityID lets the registry find the owning Compendium.
func (in AliasAddIn) EntityID() string { return in.ID }

// Validate trims and checks the input.
func (in *AliasAddIn) Validate() error {
	in.Alias = strings.Join(strings.Fields(in.Alias), " ")
	in.Notes = strings.TrimSpace(in.Notes)
	if err := checkLine("alias", in.Alias, maxAlias, true); err != nil {
		return err
	}
	if err := checkAliasWords("alias", in.Alias); err != nil {
		return err
	}
	if len([]rune(in.Notes)) > maxNotes {
		return ops.Invalid("notes", "Notes are limited to 1,000 characters.")
	}
	return checkText("notes", in.Notes, maxNotes*4)
}

// AliasItem wraps the entity after the change, so a client redraws one sheet.
type AliasItem struct {
	Item  Entity `json:"item" desc:"The entity after the change."`
	Alias Alias  `json:"alias" desc:"The alias that was added."`
}

func aliasAdd(c *ops.Ctx, in AliasAddIn) (AliasItem, error) {
	var out AliasItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		var trashed bool
		if err := tx.QueryRow(`SELECT trashed_at IS NOT NULL FROM entities WHERE id = ?`, in.ID).Scan(&trashed); err != nil {
			return err
		}
		if trashed {
			return ops.Conflict("This entity is in the trash. Restore it first.")
		}
		now := db.Now()
		if err := insertAlias(c, tx, cid, in.ID, in.Alias, in.Notes, now); err != nil {
			return err
		}
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		if err := syncText(tx, in.ID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.alias.added", CompendiumID: cid, Payload: map[string]any{"entity_id": in.ID}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		e, err := load(c, tx, cid, in.ID)
		if err != nil {
			return err
		}
		out.Item = e
		for _, a := range e.Aliases {
			if text.FoldTitle(a.Alias) == text.FoldTitle(in.Alias) {
				out.Alias = a
			}
		}
		return nil
	})
	return out, err
}

// AliasRemoveIn removes an alias.
type AliasRemoveIn struct {
	ID      string `path:"id" json:"-" desc:"Entity id."`
	AliasID string `query:"alias_id" desc:"The alias to remove, from entity.get."`
}

// EntityID lets the registry find the owning Compendium.
func (in AliasRemoveIn) EntityID() string { return in.ID }

// Validate checks the shape.
func (in *AliasRemoveIn) Validate() error {
	if in.AliasID == "" {
		return ops.Invalid("alias_id", "Say which alias.")
	}
	return nil
}

// AliasRemoveOut reports a removal.
type AliasRemoveOut struct {
	Removed bool   `json:"removed" desc:"False when the entity had no such alias."`
	Item    Entity `json:"item" desc:"The entity after the change."`
}

func aliasRemove(c *ops.Ctx, in AliasRemoveIn) (AliasRemoveOut, error) {
	var out AliasRemoveOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		res, err := tx.Exec(`DELETE FROM entity_aliases WHERE id = ? AND entity_id = ?`, in.AliasID, in.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out.Removed = true
			if err := touch(tx, in.ID, db.Now()); err != nil {
				return err
			}
			if err := syncText(tx, in.ID); err != nil {
				return err
			}
			c.Emit(ops.Event{Type: "entity.alias.removed", CompendiumID: cid, Payload: map[string]any{"entity_id": in.ID}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
		}
		out.Item, err = load(c, tx, cid, in.ID)
		return err
	})
	return out, err
}

package compendiums

import (
	"database/sql"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

func registerLifecycle(r *ops.Registry) {
	owner := ops.RequireCompendium(compaccess.Owner)
	ops.Register(r, ops.Op{
		Name: "compendium.update", Method: "PATCH", Path: "/api/v1/compendiums/{id}",
		Desc: "Change a Compendium's name, kind or description. Send only the fields to change. The slug stays the same when the name changes. Owner only.",
		Auth: owner,
	}, update)
	ops.Register(r, ops.Op{
		Name: "compendium.trash", Method: "POST", Path: "/api/v1/compendiums/{id}/trash",
		Desc: "Move a Compendium to the trash. Owner only; nothing is deleted, but projects stop showing its entities until it is restored. Trashing a trashed Compendium does nothing.",
		Auth: owner,
	}, transition("compendium.trash", "trashed_at", true, "compendium.trashed"))
	ops.Register(r, ops.Op{
		Name: "compendium.restore", Method: "POST", Path: "/api/v1/compendiums/{id}/restore",
		Desc: "Take a Compendium out of the trash. Owner only; restoring a live Compendium does nothing.",
		Auth: owner,
	}, transition("compendium.restore", "trashed_at", false, "compendium.restored"))
	ops.Register(r, ops.Op{
		Name: "compendium.archive", Method: "POST", Path: "/api/v1/compendiums/{id}/archive",
		Desc: "Archive a Compendium: hidden from the default list, still fully readable and still used by its projects. Owner only; archiving an archived one does nothing.",
		Auth: owner,
	}, transition("compendium.archive", "archived_at", true, "compendium.archived"))
	ops.Register(r, ops.Op{
		Name: "compendium.unarchive", Method: "POST", Path: "/api/v1/compendiums/{id}/unarchive",
		Desc: "Bring an archived Compendium back to the list. Owner only; does nothing when it is not archived.",
		Auth: owner,
	}, transition("compendium.unarchive", "archived_at", false, "compendium.unarchived"))
	ops.Register(r, ops.Op{
		Name: "compendium.purge", Permanent: true, Method: "DELETE", Path: "/api/v1/compendiums/{id}",
		Desc: "Permanently delete a Compendium with its entities, aliases, relationships, field definitions, overrides, links, references and recorded exports. It cannot be undone. " +
			"Owner only; it must already be in the trash. confirm_name must equal its name exactly, and detach_projects must list the id of every project that uses it (primary or by reference) " +
			"and hidden_projects the count of those the caller cannot open, as compendium.get reports them, so nobody loses a Compendium by surprise. Projects keep everything else; their primary pointer is cleared.",
		Auth: owner,
	}, purge)
}

// ---- update ----------------------------------------------------------------------------------

// UpdateIn changes a Compendium. A nil field is left alone.
type UpdateIn struct {
	ID          string  `path:"id" json:"-" desc:"Compendium id."`
	Name        *string `json:"name,omitempty" desc:"Up to 200 characters; cannot be empty."`
	Kind        *string `json:"kind,omitempty" enum:"fiction,nonfiction"`
	Description *string `json:"description,omitempty" desc:"Up to 5,000 characters of Markdown."`
}

// CompendiumRef lets the registry authorize by membership.
func (in UpdateIn) CompendiumRef() string { return in.ID }

// Validate trims and checks every field that was sent.
func (in *UpdateIn) Validate() error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if err := checkName(n); err != nil {
			return err
		}
		in.Name = &n
	}
	if in.Kind != nil && !contains(Kinds, *in.Kind) {
		return ops.Invalid("kind", "A Compendium is fiction or nonfiction.")
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if utf8.RuneCountInString(d) > maxDescription {
			return ops.Invalid("description", "The description is limited to 5,000 characters.")
		}
		in.Description = &d
	}
	return nil
}

func update(c *ops.Ctx, in UpdateIn) (Item, error) {
	if err := refuseProjectKey(c); err != nil {
		return Item{}, err
	}
	var out Compendium
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := load(c, tx, c.P.UserID, in.ID)
		if err != nil {
			return err
		}
		var sets []string
		var args []any
		var changed []string
		set := func(field string, differs bool, v any) {
			if differs {
				sets, args, changed = append(sets, field+" = ?"), append(args, v), append(changed, field)
			}
		}
		if in.Name != nil {
			set("name", *in.Name != cur.Name, *in.Name)
		}
		if in.Kind != nil {
			set("kind", *in.Kind != cur.Kind, *in.Kind)
		}
		if in.Description != nil {
			set("description", *in.Description != cur.Description, *in.Description)
		}
		if len(changed) == 0 {
			out = cur
			return nil
		}
		args = append(args, db.Now(), in.ID)
		if _, err := tx.Exec(`UPDATE compendiums SET `+strings.Join(sets, ", ")+`, updated_at = ? WHERE id = ?`, args...); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "compendium.updated", CompendiumID: in.ID, Payload: map[string]any{"fields": changed}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out, err = load(c, tx, c.P.UserID, in.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// ---- trash, restore, archive, unarchive ------------------------------------------------------

// StateIn names one Compendium for a state change.
type StateIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in StateIn) CompendiumRef() string { return in.ID }

// transition sets (on) or clears a Compendium's timestamp column. It is
// idempotent, and leaves updated_at alone so "last changed" stays about the
// bible's content, not about housekeeping.
func transition(action, column string, on bool, event string) func(*ops.Ctx, StateIn) (Item, error) {
	return func(c *ops.Ctx, in StateIn) (Item, error) {
		if err := refuseProjectKey(c); err != nil {
			return Item{}, err
		}
		var out Compendium
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			if _, err := load(c, tx, c.P.UserID, in.ID); err != nil {
				return err
			}
			var res sql.Result
			var err error
			if on {
				res, err = tx.Exec(`UPDATE compendiums SET `+column+` = ? WHERE id = ? AND `+column+` IS NULL`, db.Now(), in.ID)
			} else {
				res, err = tx.Exec(`UPDATE compendiums SET `+column+` = NULL WHERE id = ? AND `+column+` IS NOT NULL`, in.ID)
			}
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				c.Emit(ops.Event{Type: event, CompendiumID: in.ID})
				c.Audit(ops.AuditEntry{Action: action, TargetType: "compendium", TargetID: in.ID})
				if err := c.FlushTo(tx); err != nil {
					return err
				}
			}
			out, err = load(c, tx, c.P.UserID, in.ID)
			return err
		})
		if err != nil {
			return Item{}, err
		}
		return Item{Item: out}, nil
	}
}

// ---- purge ---------------------------------------------------------------------------------------

// PurgeIn permanently deletes a trashed Compendium.
type PurgeIn struct {
	ID             string   `path:"id" json:"-" desc:"Compendium id."`
	ConfirmName    string   `json:"confirm_name" desc:"The Compendium's exact name, typed to confirm."`
	DetachProjects []string `json:"detach_projects,omitempty" desc:"The id of every project that uses this Compendium (primary or reference) that the caller can open, exactly as compendium.get lists them."`
	HiddenProjects int      `json:"hidden_projects,omitempty" desc:"How many projects use it that the caller cannot open, as compendium.get reports."`
}

// CompendiumRef lets the registry authorize by membership.
func (in PurgeIn) CompendiumRef() string { return in.ID }

// Validate checks the confirmation was typed.
func (in *PurgeIn) Validate() error {
	if in.ConfirmName == "" {
		return ops.Invalid("confirm_name", "Type the Compendium's exact name to confirm.")
	}
	return nil
}

// PurgeOut reports a purge.
type PurgeOut struct {
	Purged           bool     `json:"purged"`
	ID               string   `json:"id"`
	DetachedProjects []string `json:"detached_projects" desc:"Projects that used it and no longer do."`
	HiddenProjects   int      `json:"hidden_projects"`
	Entities         int      `json:"entities" desc:"Entities deleted with it, trashed ones included."`
}

// purge is the only hard delete of a Compendium. The checks and the delete
// share one transaction, so the Compendium cannot be restored, or gain a user,
// between them. Foreign keys and triggers remove members, entities and
// everything hanging off them (aliases, relations, search text, overrides,
// links, field definitions, exports, redirects) and clear the projects'
// primary pointers. The audit row records ids and counts only.
func purge(c *ops.Ctx, in PurgeIn) (PurgeOut, error) {
	if err := refuseProjectKey(c); err != nil {
		return PurgeOut{}, err
	}
	var out PurgeOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var name string
		var trashed sql.NullString
		if err := tx.QueryRow(`SELECT name, trashed_at FROM compendiums WHERE id = ? AND deleted_at IS NULL`, in.ID).Scan(&name, &trashed); err != nil {
			if err == sql.ErrNoRows {
				return ops.NotFound("compendium")
			}
			return err
		}
		if !trashed.Valid {
			return ops.Conflict("Move the Compendium to the trash before deleting it permanently.")
		}
		if in.ConfirmName != name {
			return ops.Invalid("confirm_name", "Type the Compendium's exact name to confirm.")
		}
		uses, err := loadDetail(c, tx, c.P, in.ID)
		if err != nil {
			return err
		}
		want := map[string]bool{}
		for _, p := range uses.Projects {
			want[p.ID] = true
		}
		got := map[string]bool{}
		for _, id := range in.DetachProjects {
			got[id] = true
		}
		var missing []string
		for id := range want {
			if !got[id] {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 || len(got) != len(want) || in.HiddenProjects != uses.HiddenProjects {
			return ops.ConflictWith("Projects still use this Compendium and will lose it. Name every one in detach_projects (and the count of hidden ones in hidden_projects) to go on.",
				map[string]any{"projects": uses.Projects, "hidden_projects": uses.HiddenProjects})
		}
		if err := tx.QueryRow(`SELECT count(*) FROM entities WHERE compendium_id = ?`, in.ID).Scan(&out.Entities); err != nil {
			return err
		}
		// Every project whose mirror shows this Compendium, the ones the caller
		// cannot open included: their projections must be rewritten.
		affected, err := entities.AffectedProjects(c, tx, in.ID)
		if err != nil {
			return err
		}
		// Compendiums that reference this one name it in their mirror's manifest;
		// the rows that say so are gone after the delete, so wake them now.
		referrers, err := entities.ReferencingCompendiums(c, tx, in.ID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM compendiums WHERE id = ?`, in.ID); err != nil {
			return err
		}
		for _, id := range referrers {
			c.Emit(ops.Event{Type: "compendium.reference.purged", CompendiumID: id, Payload: map[string]any{"target_compendium_id": in.ID}})
		}
		out.Purged, out.ID, out.HiddenProjects = true, in.ID, uses.HiddenProjects
		out.DetachedProjects = []string{}
		for _, p := range uses.Projects {
			out.DetachedProjects = append(out.DetachedProjects, p.ID)
		}
		for _, id := range affected {
			c.Emit(ops.Event{Type: "compendium.detached", ProjectID: id, Payload: map[string]any{"compendium_id": in.ID}})
		}
		c.Emit(ops.Event{Type: "compendium.purged", CompendiumID: in.ID})
		c.Audit(ops.AuditEntry{Action: "compendium.purge", TargetType: "compendium", TargetID: in.ID,
			Detail: map[string]any{"name_length": utf8.RuneCountInString(name), "entities": out.Entities, "projects": len(out.DetachedProjects), "hidden_projects": out.HiddenProjects}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return PurgeOut{}, err
	}
	// What the Compendium left on disk (a standalone mirror folder) is removed
	// by the purge hooks: best effort, after the commit.
	c.Env.CompendiumPurged(c, in.ID)
	return out, nil
}

// refuseProjectKey keeps a key restricted to a project from owning the project's
// Compendium. Such a key reaches it to fill it (entities, fields, search,
// export), but the Compendium may be shared with other books, so renaming,
// archiving, trashing, deleting it and changing who may open it are for a
// person, or for a key restricted to that Compendium itself.
func refuseProjectKey(c *ops.Ctx) error {
	if c.P.ProjectID != "" {
		return ops.Forbidden("a key restricted to a project can fill its Compendium but not rename, archive, delete or share it")
	}
	return nil
}

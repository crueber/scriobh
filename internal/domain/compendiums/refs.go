package compendiums

import (
	"context"
	"database/sql"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
)

// A project has at most one primary Compendium (projects.compendium_id) and may
// reference any number of others, read-only, as a whole. A Compendium may
// reference others the same way. Whoever creates a reference must be able to
// read the target: an unseen Compendium is "not found", like a missing one.
// References are not followed twice: a project sees its primary, the
// Compendiums it references itself, and nothing beyond.

func registerRefs(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "project.compendium.get", Method: "GET", Path: "/api/v1/projects/{id}/compendium",
		Desc: "A project's Compendium wiring: its primary Compendium and the whole Compendiums it references. A Compendium the caller is not a member of is not named; it is counted in hidden, because project access never grants the Compendium.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, projectCompendiumGet)
	ops.Register(r, ops.Op{
		Name: "project.compendium.set", Method: "PUT", Path: "/api/v1/projects/{id}/compendium",
		Desc: "Set or clear a project's primary Compendium (compendium_id empty clears it). Project owner, who must also be an editor of the Compendium. " +
			"With dry_run it only reports the effect: entities that enter and leave the project, and the overrides that become dormant (kept, but unused until their Compendium is back in scope). " +
			"Setting a Compendium the project already references makes it primary and drops the now-redundant reference.",
		Auth: ops.RequireProject(ops.ProjectOwner),
	}, projectCompendiumSet)
	ops.Register(r, ops.Op{
		Name: "project.compendium.ref.add", Method: "POST", Path: "/api/v1/projects/{id}/compendium/refs", Status: 200,
		Desc: "Reference a whole Compendium from a project, read-only: its entities appear in the project's resolved entities and in the mirror's compendium/ folder, and cannot be edited from the project. " +
			"Project owner, who must be able to read the target Compendium. Adding one that is already referenced does nothing; the primary cannot also be referenced.",
		Auth: ops.RequireProject(ops.ProjectOwner),
	}, projectRefAdd)
	ops.Register(r, ops.Op{
		Name: "project.compendium.ref.remove", Method: "DELETE", Path: "/api/v1/projects/{id}/compendium/refs",
		Desc: "Stop referencing a Compendium from a project. Project owner. Overrides the project made for its entities are kept, dormant.",
		Auth: ops.RequireProject(ops.ProjectOwner),
	}, projectRefRemove)
	ops.Register(r, ops.Op{
		Name: "compendium.ref.add", Method: "POST", Path: "/api/v1/compendiums/{id}/refs", Status: 200,
		Desc: "Reference another Compendium from this one, read-only (a shared cosmology, a common history, a house style). Owner of this Compendium, who must be able to read the target. Does nothing if already referenced.",
		Auth: ops.RequireCompendium(compaccess.Owner),
	}, compendiumRefAdd)
	ops.Register(r, ops.Op{
		Name: "compendium.ref.remove", Method: "DELETE", Path: "/api/v1/compendiums/{id}/refs",
		Desc: "Stop referencing another Compendium. Owner of this Compendium.",
		Auth: ops.RequireCompendium(compaccess.Owner),
	}, compendiumRefRemove)
}

// ---- project.compendium.get ----------------------------------------------------------------------

// ProjectIn names a project.
type ProjectIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectIn) ProjectRef() string { return in.ID }

// Wiring is a project's Compendium links.
type Wiring struct {
	Primary    *Reference  `json:"primary" desc:"The primary Compendium, when the caller can read it; null when there is none or the caller is not a member of it."`
	HasPrimary bool        `json:"has_primary" desc:"True when the project has a primary Compendium, whether or not the caller may see it."`
	References []Reference `json:"references" desc:"Whole Compendiums the project references that the caller can read."`
	Hidden     int         `json:"hidden" desc:"Compendiums the project uses that the caller is not a member of. Counted, never named."`
}

// WiringItem wraps a Wiring.
type WiringItem struct {
	Item Wiring `json:"item"`
}

func projectCompendiumGet(c *ops.Ctx, in ProjectIn) (WiringItem, error) {
	w, err := loadWiring(c, c.DB().Read(), c.P, in.ID)
	return WiringItem{Item: w}, err
}

func loadWiring(ctx context.Context, q querier, p ops.Principal, projectID string) (Wiring, error) {
	userID := p.UserID
	w := Wiring{References: []Reference{}}
	var primary sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT compendium_id FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&primary); err != nil {
		return w, ops.NotFound("project")
	}
	name := func(id string) (string, bool) {
		var n string
		if err := q.QueryRowContext(ctx, `SELECT name FROM compendiums WHERE id = ? AND deleted_at IS NULL`, id).Scan(&n); err != nil {
			return "", false
		}
		return n, true
	}
	if primary.Valid {
		w.HasPrimary = true
		if compaccess.CanRead(ctx, q, userID, primary.String) {
			n, _ := name(primary.String)
			w.Primary = &Reference{CompendiumID: primary.String, Name: n, Mode: "reference"}
		} else {
			w.Hidden++
		}
	}
	if p.ProjectID != "" {
		return w, nil // a project key sees its primary Compendium and nothing it references
	}
	rows, err := q.QueryContext(ctx, `SELECT r.id, r.target_compendium_id, c.name, r.mode FROM compendium_refs r
		JOIN compendiums c ON c.id = r.target_compendium_id
		WHERE r.owner_kind = 'project' AND r.owner_id = ? AND c.deleted_at IS NULL ORDER BY c.name, c.id`, projectID)
	if err != nil {
		return w, err
	}
	var all []Reference
	for rows.Next() {
		var r Reference
		if err := rows.Scan(&r.ID, &r.CompendiumID, &r.Name, &r.Mode); err != nil {
			rows.Close()
			return w, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return w, err
	}
	if err := rows.Close(); err != nil {
		return w, err
	}
	for _, r := range all {
		if compaccess.CanRead(ctx, q, userID, r.CompendiumID) {
			w.References = append(w.References, r)
		} else {
			w.Hidden++
		}
	}
	return w, nil
}

// ---- project.compendium.set ------------------------------------------------------------------------

// SetIn sets a project's primary Compendium.
type SetIn struct {
	ID           string `path:"id" json:"-" desc:"Project id."`
	CompendiumID string `json:"compendium_id" desc:"The new primary Compendium; empty to clear it."`
	DryRun       bool   `json:"dry_run,omitempty" desc:"Only report what would change."`
}

// ProjectRef lets the registry authorize by membership.
func (in SetIn) ProjectRef() string { return in.ID }

// SetOut is the effect of setting a primary Compendium.
type SetOut struct {
	DryRun               bool   `json:"dry_run"`
	Changed              bool   `json:"changed" desc:"False when the project already had this primary."`
	ProjectID            string `json:"project_id"`
	PreviousCompendiumID string `json:"previous_compendium_id"`
	CompendiumID         string `json:"compendium_id"`
	EntitiesEntering     int    `json:"entities_entering" desc:"Live entities of the new primary that the project will now show."`
	EntitiesLeaving      int    `json:"entities_leaving" desc:"Live entities of the old primary that the project will stop showing (unless it still references that Compendium)."`
	OverridesKept        int    `json:"overrides_kept" desc:"Overrides of the project that apply to entities still in scope."`
	OverridesDormant     int    `json:"overrides_dormant" desc:"Overrides of the project that will stop applying because their entity leaves scope. They are kept and apply again if it returns."`
	ReferenceDropped     bool   `json:"reference_dropped" desc:"The project referenced the new primary as a whole; that reference is redundant and is removed."`
}

func projectCompendiumSet(c *ops.Ctx, in SetIn) (SetOut, error) {
	if c.P.Pinned() {
		return SetOut{}, ops.Forbidden("a key restricted to one project or compendium cannot change which Compendium a project uses")
	}
	if in.DryRun {
		tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return SetOut{}, err
		}
		defer tx.Rollback()
		return setPrimaryRun(c, tx, in, false)
	}
	var out SetOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var err error
		out, err = setPrimaryRun(c, tx, in, true)
		return err
	})
	return out, err
}

// setPrimaryRun is the one engine behind project.compendium.set and compendium.primary.set: it works out what changing a
// project's primary Compendium does and, when write is true, does it, inside tx.
func setPrimaryRun(c *ops.Ctx, tx *sql.Tx, in SetIn, write bool) (SetOut, error) {
	var out SetOut
	run := func(tx *sql.Tx, write bool) error {
		var prev sql.NullString
		if err := tx.QueryRow(`SELECT compendium_id FROM projects WHERE id = ? AND deleted_at IS NULL`, in.ID).Scan(&prev); err != nil {
			return ops.NotFound("project")
		}
		if in.CompendiumID != "" {
			role, ok := compaccess.Role(c, tx, c.P.UserID, in.CompendiumID)
			if !ok {
				return ops.NotFound("compendium")
			}
			if !compaccess.AtLeast(role, compaccess.Editor) {
				return ops.Forbidden("you need to be an editor of that Compendium to make it a project's primary")
			}
		}
		out = SetOut{DryRun: in.DryRun, ProjectID: in.ID, PreviousCompendiumID: prev.String, CompendiumID: in.CompendiumID}
		out.Changed = prev.String != in.CompendiumID
		if !out.Changed {
			return nil
		}
		// Scope after the change: the new primary plus the project's references.
		inScope := map[string]bool{}
		if in.CompendiumID != "" {
			inScope[in.CompendiumID] = true
		}
		rows, err := tx.QueryContext(c, `SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?`, in.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if id == in.CompendiumID {
				out.ReferenceDropped = true
				continue
			}
			inScope[id] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		count := func(q string, args ...any) (int, error) {
			var n int
			err := tx.QueryRow(q, args...).Scan(&n)
			return n, err
		}
		if in.CompendiumID != "" {
			if out.EntitiesEntering, err = count(`SELECT count(*) FROM entities WHERE compendium_id = ? AND trashed_at IS NULL`, in.CompendiumID); err != nil {
				return err
			}
		}
		if prev.Valid && !inScope[prev.String] {
			if out.EntitiesLeaving, err = count(`SELECT count(*) FROM entities WHERE compendium_id = ? AND trashed_at IS NULL`, prev.String); err != nil {
				return err
			}
		}
		orows, err := tx.QueryContext(c, `SELECT e.compendium_id FROM project_entity_overrides o JOIN entities e ON e.id = o.entity_id WHERE o.project_id = ?`, in.ID)
		if err != nil {
			return err
		}
		for orows.Next() {
			var cid string
			if err := orows.Scan(&cid); err != nil {
				orows.Close()
				return err
			}
			if inScope[cid] {
				out.OverridesKept++
			} else {
				out.OverridesDormant++
			}
		}
		if err := orows.Err(); err != nil {
			_ = orows.Close()
			return err
		}
		if err := orows.Close(); err != nil {
			return err
		}
		if !write {
			return nil
		}
		var v any
		if in.CompendiumID != "" {
			v = in.CompendiumID
		}
		if _, err := tx.Exec(`UPDATE projects SET compendium_id = ? WHERE id = ?`, v, in.ID); err != nil {
			return err
		}
		if out.ReferenceDropped {
			if _, err := tx.Exec(`DELETE FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ? AND target_compendium_id = ?`, in.ID, in.CompendiumID); err != nil {
				return err
			}
		}
		// The project's own folder changes, and so does whether each Compendium
		// involved still has a standalone mirror.
		c.Emit(ops.Event{Type: "project.compendium.set", ProjectID: in.ID, Payload: map[string]any{"compendium_id": in.CompendiumID, "previous": prev.String}})
		if prev.Valid {
			c.Emit(ops.Event{Type: "compendium.attached", CompendiumID: prev.String, Payload: map[string]any{"project_id": in.ID, "primary": false}})
		}
		if in.CompendiumID != "" {
			c.Emit(ops.Event{Type: "compendium.attached", CompendiumID: in.CompendiumID, Payload: map[string]any{"project_id": in.ID, "primary": true}})
		}
		c.Audit(ops.AuditEntry{Action: "project.compendium.set", TargetType: "project", TargetID: in.ID,
			Detail: map[string]any{"compendium_id": in.CompendiumID, "previous": prev.String}})
		return c.FlushTo(tx)
	}
	err := run(tx, write)
	return out, err
}

// ---- references ----------------------------------------------------------------------------------------

// RefAddIn adds a whole-Compendium reference to a project.
type RefAddIn struct {
	ID           string `path:"id" json:"-" desc:"Project id."`
	CompendiumID string `json:"compendium_id" desc:"The Compendium to reference."`
}

// ProjectRef lets the registry authorize by membership.
func (in RefAddIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *RefAddIn) Validate() error {
	if in.CompendiumID == "" {
		return ops.Invalid("compendium_id", "Say which Compendium to reference.")
	}
	return nil
}

// RefItem wraps a reference and says whether this call created it.
type RefItem struct {
	Item    Reference `json:"item"`
	Created bool      `json:"created" desc:"False when the reference already existed."`
}

func projectRefAdd(c *ops.Ctx, in RefAddIn) (RefItem, error) {
	if c.P.Pinned() {
		return RefItem{}, ops.Forbidden("a key restricted to one project or compendium cannot change which Compendiums a project uses")
	}
	return addRef(c, "project", in.ID, in.CompendiumID)
}

func addRef(c *ops.Ctx, kind, ownerID, target string) (RefItem, error) {
	var out RefItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if !compaccess.CanRead(c, tx, c.P.UserID, target) {
			return ops.NotFound("compendium")
		}
		switch kind {
		case "project":
			var primary sql.NullString
			if err := tx.QueryRow(`SELECT compendium_id FROM projects WHERE id = ?`, ownerID).Scan(&primary); err != nil {
				return ops.NotFound("project")
			}
			if primary.String == target {
				return ops.Conflict("That is the project's primary Compendium; it is already part of the project.")
			}
		default:
			if ownerID == target {
				return ops.Invalid("target_id", "A Compendium cannot reference itself.")
			}
		}
		var name string
		if err := tx.QueryRow(`SELECT name FROM compendiums WHERE id = ?`, target).Scan(&name); err != nil {
			return ops.NotFound("compendium")
		}
		var existing string
		err := tx.QueryRow(`SELECT id FROM compendium_refs WHERE owner_kind = ? AND owner_id = ? AND target_compendium_id = ?`, kind, ownerID, target).Scan(&existing)
		if err == nil {
			out = RefItem{Item: Reference{ID: existing, CompendiumID: target, Name: name, Mode: "reference"}}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		id := db.NewID("ref")
		if _, err := tx.Exec(`INSERT INTO compendium_refs(id, owner_kind, owner_id, target_compendium_id, mode, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
			id, kind, ownerID, target, "reference", c.P.UserID, db.Now()); err != nil {
			return err
		}
		out = RefItem{Item: Reference{ID: id, CompendiumID: target, Name: name, Mode: "reference"}, Created: true}
		ev := ops.Event{Type: kind + ".compendium.ref.added", Payload: map[string]any{"target_compendium_id": target}}
		if kind == "project" {
			ev.ProjectID = ownerID
		} else {
			ev.CompendiumID = ownerID
		}
		c.Emit(ev)
		// The target's standalone state and the projects that follow it are
		// unchanged, but the referencing side's projection is not.
		c.Audit(ops.AuditEntry{Action: kind + ".compendium.ref.add", TargetType: kind, TargetID: ownerID, Detail: map[string]any{"target_compendium_id": target}})
		return c.FlushTo(tx)
	})
	return out, err
}

// RefRemoveIn removes a project's reference.
type RefRemoveIn struct {
	ID           string `path:"id" json:"-" desc:"Project id."`
	CompendiumID string `query:"compendium_id" desc:"The referenced Compendium."`
}

// ProjectRef lets the registry authorize by membership.
func (in RefRemoveIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *RefRemoveIn) Validate() error {
	if in.CompendiumID == "" {
		return ops.Invalid("compendium_id", "Say which Compendium to stop referencing.")
	}
	return nil
}

// RefRemoveOut reports a removal.
type RefRemoveOut struct {
	Removed bool `json:"removed" desc:"False when there was no such reference."`
}

func projectRefRemove(c *ops.Ctx, in RefRemoveIn) (RefRemoveOut, error) {
	return removeRef(c, "project", in.ID, in.CompendiumID)
}

func removeRef(c *ops.Ctx, kind, ownerID, target string) (RefRemoveOut, error) {
	var out RefRemoveOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM compendium_refs WHERE owner_kind = ? AND owner_id = ? AND target_compendium_id = ?`, kind, ownerID, target)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return nil
		}
		out.Removed = true
		ev := ops.Event{Type: kind + ".compendium.ref.removed", Payload: map[string]any{"target_compendium_id": target}}
		if kind == "project" {
			ev.ProjectID = ownerID
		} else {
			ev.CompendiumID = ownerID
		}
		c.Emit(ev)
		c.Audit(ops.AuditEntry{Action: kind + ".compendium.ref.remove", TargetType: kind, TargetID: ownerID, Detail: map[string]any{"target_compendium_id": target}})
		return c.FlushTo(tx)
	})
	return out, err
}

// CompendiumRefAddIn adds a reference from one Compendium to another.
type CompendiumRefAddIn struct {
	ID       string `path:"id" json:"-" desc:"The referencing Compendium's id."`
	TargetID string `json:"target_id" ref:"compendium" desc:"The Compendium to reference."`
}

// CompendiumRef lets the registry authorize by membership.
func (in CompendiumRefAddIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *CompendiumRefAddIn) Validate() error {
	if in.TargetID == "" {
		return ops.Invalid("target_id", "Say which Compendium to reference.")
	}
	return nil
}

func compendiumRefAdd(c *ops.Ctx, in CompendiumRefAddIn) (RefItem, error) {
	if c.P.Pinned() {
		return RefItem{}, ops.Forbidden("a key restricted to one project or compendium cannot reference another Compendium")
	}
	return addRef(c, "compendium", in.ID, in.TargetID)
}

// CompendiumRefRemoveIn removes a reference from one Compendium to another.
type CompendiumRefRemoveIn struct {
	ID       string `path:"id" json:"-" desc:"The referencing Compendium's id."`
	TargetID string `query:"target_id" ref:"compendium" desc:"The referenced Compendium."`
}

// CompendiumRef lets the registry authorize by membership.
func (in CompendiumRefRemoveIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *CompendiumRefRemoveIn) Validate() error {
	if in.TargetID == "" {
		return ops.Invalid("target_id", "Say which Compendium to stop referencing.")
	}
	return nil
}

func compendiumRefRemove(c *ops.Ctx, in CompendiumRefRemoveIn) (RefRemoveOut, error) {
	return removeRef(c, "compendium", in.ID, in.TargetID)
}

// Package scenes ties a scene to the bible: its point of view and location as
// entities, the "in this scene" sidebar, and the repair that turns the free
// text of Phase 2 into entity references.
//
// pov and location stay as text on the document (the display fallback, and
// what an export or a book without a bible shows); pov_entity_id and
// location_entity_id say which entity the text means. Setting an entity also
// writes its name into the text. Nothing here ever deletes the text.
package scenes

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Register adds the scene ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "scene.set_pov", Method: "PUT", Path: "/api/v1/docs/{id}/pov",
		Desc: "Set a scene's point of view to a character of the Compendium, by id, name or alias among the entities the caller may read (empty clears the link and keeps the text). The scene's pov text becomes the character's name. Fails if the character is not in this project's scope or the caller may not read it (the same not found for both). Project editor.",
		Auth: docref.Via(ops.ProjectEditor)}, func(c *ops.Ctx, in SetIn) (SetOut, error) { return set(c, in, "pov") })
	ops.Register(r, ops.Op{Name: "scene.set_location", Method: "PUT", Path: "/api/v1/docs/{id}/location",
		Desc: "Set where a scene happens to an entity of the Compendium (usually a place), by id, name or alias among the entities the caller may read (empty clears the link and keeps the text). The scene's location text becomes the entity's name. Project editor.",
		Auth: docref.Via(ops.ProjectEditor)}, func(c *ops.Ctx, in SetIn) (SetOut, error) { return set(c, in, "location") })
	ops.Register(r, ops.Op{Name: "scene.entities", Method: "GET", Path: "/api/v1/docs/{id}/scene-entities",
		Desc: "The \"in this scene\" sidebar: the entities a document mentions, plus its point of view and location, each with kind, summary (the book's override applied) and its roles in the scene. Only entities the caller may read; hidden says some were left out. indexing is true while the mention index owes a scan.",
		Auth: docref.Via(ops.ProjectReader)}, entitiesOf)
	ops.Register(r, ops.Op{Name: "scene.link_entities", Method: "POST", Path: "/api/v1/projects/{id}/scenes/link-entities",
		Desc: "Turn the free-text point of view and location of this project's scenes into entity references: each distinct value is matched, exactly and ignoring case and spacing, against the names and aliases of the entities the caller may read (a point of view must be a character). Matches are linked; values that match nothing, match a non-character as a point of view, or match several entities are reported and left alone. The text is never changed or removed. With dry_run it reports only. Project owner.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, linkEntities)
}

// ---- scene.set_pov and scene.set_location -----------------------------------------------------------------------

// SetIn names the entity a scene's point of view or location becomes.
type SetIn struct {
	ID           string `path:"id" json:"-" desc:"Document id."`
	Entity       string `json:"entity" desc:"An entity id, or a name or alias among the entities you can read. Empty clears the link."`
	BaseRevision int    `json:"base_revision,omitempty" desc:"The document revision you last saw; the call is refused if it moved. 0 skips the check."`
}

// DocID lets the registry find the owning project.
func (in SetIn) DocID() string { return in.ID }

// SetOut is the scene's references after the change.
type SetOut struct {
	ID             string       `json:"id"`
	Revision       int          `json:"revision"`
	Pov            string       `json:"pov" desc:"The point-of-view text."`
	Location       string       `json:"location" desc:"The location text."`
	PovEntity      *vocab.Brief `json:"pov_entity,omitempty"`
	LocationEntity *vocab.Brief `json:"location_entity,omitempty"`
}

func set(c *ops.Ctx, in SetIn, field string) (SetOut, error) {
	var out SetOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID, kind string
		var rev int
		var deleted sql.NullString
		if err := tx.QueryRow(`SELECT project_id, kind, revision, deleted_at FROM documents WHERE id = ?`, in.ID).Scan(&projectID, &kind, &rev, &deleted); err != nil {
			return ops.NotFound("document")
		}
		if kind == "research" {
			return ops.NotFound("document")
		}
		if deleted.Valid {
			return ops.Conflict("This document is in the trash. Restore it before editing.")
		}
		if in.BaseRevision > 0 && in.BaseRevision != rev {
			return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": rev})
		}
		col, textCol := field+"_entity_id", field
		var curEntity sql.NullString
		var curText string
		if err := tx.QueryRow(`SELECT `+col+`, `+textCol+` FROM documents WHERE id = ?`, in.ID).Scan(&curEntity, &curText); err != nil {
			return err
		}
		var entityID any
		name := ""
		if ref := strings.TrimSpace(in.Entity); ref != "" {
			vis, err := vocab.LoadVisible(c, tx, vocab.ReaderOf(c.P), projectID)
			if err != nil {
				return err
			}
			var kinds []string
			if field == "pov" {
				kinds = []string{"character"}
			}
			cands := vis.Candidates(ref, kinds...)
			switch {
			case len(cands) == 1:
			case len(cands) > 1:
				return vocab.AmbiguousError(ref, cands)
			case len(vis.Candidates(ref)) > 0:
				return ops.Invalid("entity", "The point of view must be a character.")
			default:
				return ops.NotFound("entity")
			}
			entityID, name = cands[0].ID, cands[0].Name
		}
		// Setting what is already set writes nothing: no revision bump (an open
		// editor's next save would be refused) and no event.
		same := curEntity.String == fmt.Sprint(orEmpty(entityID)) && (name == "" || curText == name)
		out = SetOut{ID: in.ID, Revision: rev}
		if !same {
			now := db.Now()
			if _, err := tx.Exec(`UPDATE documents SET `+col+` = ?, `+textCol+` = CASE WHEN ? <> '' THEN ? ELSE `+textCol+` END, revision = revision + 1, updated_at = ? WHERE id = ?`,
				entityID, name, name, now, in.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now, projectID); err != nil {
				return err
			}
			c.Emit(ops.Event{Type: "doc.updated", ProjectID: projectID, DocumentID: in.ID, Payload: map[string]any{"revision": rev + 1, "content": false, "scene": field}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
			out.Revision = rev + 1
		}
		refs, err := SceneRefs(c, tx, vocab.ReaderOf(c.P), projectID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT pov, location FROM documents WHERE id = ?`, in.ID).Scan(&out.Pov, &out.Location); err != nil {
			return err
		}
		if r, ok := refs[in.ID]; ok {
			out.PovEntity, out.LocationEntity = r.Pov, r.Location
		}
		return nil
	})
	return out, err
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

// Refs are a document's point-of-view and location entities as one reader may
// see them.
type Refs struct {
	Pov, Location *vocab.Brief
}

// SceneRefs resolves the point-of-view and location entities of a project's
// documents for one reader, in one read of the documents and one of the
// vocabulary (and none of either when no document names an entity). An entity
// the reader may not read is left out. For the outline and the document list.
func SceneRefs(ctx context.Context, q vocab.Querier, r vocab.Reader, projectID string) (map[string]Refs, error) {
	out := map[string]Refs{}
	rows, err := q.QueryContext(ctx, `SELECT id, pov_entity_id, location_entity_id FROM documents
		WHERE project_id = ? AND (pov_entity_id IS NOT NULL OR location_entity_id IS NOT NULL)`, projectID)
	if err != nil {
		return nil, err
	}
	type pair struct{ pov, loc sql.NullString }
	got := map[string]pair{}
	for rows.Next() {
		var id string
		var p pair
		if err := rows.Scan(&id, &p.pov, &p.loc); err != nil {
			rows.Close()
			return nil, err
		}
		got[id] = p
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return out, nil
	}
	vis, err := vocab.LoadVisible(ctx, q, r, projectID)
	if err != nil {
		return nil, err
	}
	for id, p := range got {
		var r Refs
		if p.pov.Valid {
			if e, ok := vis.ByID[p.pov.String]; ok {
				b := e.Brief()
				r.Pov = &b
			}
		}
		if p.loc.Valid {
			if e, ok := vis.ByID[p.loc.String]; ok {
				b := e.Brief()
				r.Location = &b
			}
		}
		out[id] = r
	}
	return out, nil
}

// ---- scene.entities --------------------------------------------------------------------------------------------------

// EntitiesIn names a document.
type EntitiesIn struct {
	ID string `path:"id" json:"-" desc:"Document id."`
}

// DocID lets the registry find the owning project.
func (in EntitiesIn) DocID() string { return in.ID }

// SceneEntity is one entity in a scene.
type SceneEntity struct {
	Entity    vocab.Brief `json:"entity"`
	Roles     []string    `json:"roles" desc:"pov, location and/or mentioned."`
	Count     int         `json:"count" desc:"Occurrences of its name or an alias in the prose; 0 when it is only the point of view, the location, or linked."`
	Explicit  int         `json:"explicit" desc:"Occurrences of [[links]] naming it."`
	Ambiguous bool        `json:"ambiguous,omitempty" desc:"Another entity the caller can read answers to the same words."`
}

// EntitiesOut is the sidebar.
type EntitiesOut struct {
	DocumentID   string        `json:"document_id"`
	Pov          *vocab.Brief  `json:"pov,omitempty"`
	PovText      string        `json:"pov_text" desc:"The free text of the point of view: the fallback when no entity is linked or the caller cannot read it."`
	Location     *vocab.Brief  `json:"location,omitempty"`
	LocationText string        `json:"location_text"`
	Items        []SceneEntity `json:"items" desc:"Point of view first, then location, then the rest in the order they first appear in the text."`
	Hidden       bool          `json:"hidden" desc:"Some entities of this scene are left out: the caller may not read their Compendium."`
	Indexing     bool          `json:"indexing" desc:"A rescan is owed; mentions may be behind."`
}

func entitiesOf(c *ops.Ctx, in EntitiesIn) (EntitiesOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EntitiesOut{}, err
	}
	defer tx.Rollback()
	var projectID, kind string
	var pov, loc sql.NullString
	out := EntitiesOut{DocumentID: in.ID, Items: []SceneEntity{}}
	if err := tx.QueryRowContext(c, `SELECT project_id, kind, pov, location, pov_entity_id, location_entity_id FROM documents WHERE id = ?`, in.ID).
		Scan(&projectID, &kind, &out.PovText, &out.LocationText, &pov, &loc); err != nil {
		return EntitiesOut{}, ops.NotFound("document")
	}
	if kind == "research" {
		return out, nil
	}
	vis, err := vocab.LoadVisible(c, tx, vocab.ReaderOf(c.P), projectID)
	if err != nil {
		return EntitiesOut{}, err
	}
	rows, err := mentions.LoadRows(c, tx, in.ID)
	if err != nil {
		return EntitiesOut{}, err
	}
	out.Indexing = mentions.IsOwed(c, tx, projectID)
	visibleOther := mentions.VisibleAmbiguity(c, tx, vis, projectID)
	type item struct {
		se  SceneEntity
		pos int
	}
	items := map[string]*item{}
	order := []string{}
	touch := func(id, role string) {
		e, ok := vis.ByID[id]
		if !ok {
			out.Hidden = true
			return
		}
		it := items[id]
		if it == nil {
			it = &item{se: SceneEntity{Entity: e.Brief(), Roles: []string{}}, pos: 1 << 30}
			items[id] = it
			order = append(order, id)
		}
		if role != "" {
			it.se.Roles = append(it.se.Roles, role)
		}
	}
	if pov.Valid {
		touch(pov.String, "pov")
		if e, ok := vis.ByID[pov.String]; ok {
			b := e.Brief()
			out.Pov = &b
		}
	}
	if loc.Valid {
		touch(loc.String, "location")
		if e, ok := vis.ByID[loc.String]; ok {
			b := e.Brief()
			out.Location = &b
		}
	}
	for id, r := range rows {
		if _, ok := vis.ByID[id]; !ok {
			out.Hidden = true
			continue
		}
		touch(id, "mentioned")
		it := items[id]
		it.se.Count, it.se.Explicit, it.pos = r.Count, r.Explicit, r.FirstPos
		it.se.Ambiguous = r.Ambiguous && visibleOther(id)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := items[order[i]], items[order[j]]
		ra, rb := rank(a.se.Roles), rank(b.se.Roles)
		if ra != rb {
			return ra < rb
		}
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		return a.se.Entity.ID < b.se.Entity.ID
	})
	out.Items = out.Items[:0]
	for _, id := range order {
		out.Items = append(out.Items, items[id].se)
	}
	return out, nil
}

// rank orders roles: the point of view, then the location, then the rest.
func rank(roles []string) int {
	best := 3
	for _, r := range roles {
		switch r {
		case "pov":
			best = min(best, 0)
		case "location":
			best = min(best, 1)
		default:
			best = min(best, 2)
		}
	}
	return best
}

// ---- scene.link_entities ----------------------------------------------------------------------------------------

// LinkIn asks for the free-text migration.
type LinkIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	DryRun bool   `json:"dry_run,omitempty" desc:"Report what would be linked without writing."`
}

// ProjectRef lets the registry authorize by membership.
func (in LinkIn) ProjectRef() string { return in.ID }

// Linked is one value that matched exactly one entity.
type Linked struct {
	Field     string      `json:"field" enum:"pov,location"`
	Value     string      `json:"value" desc:"The text as written on the scenes."`
	Entity    vocab.Brief `json:"entity"`
	Documents int         `json:"documents" desc:"Scenes that carry this value."`
}

// Unmatched is one value left alone, with why.
type Unmatched struct {
	Field      string        `json:"field" enum:"pov,location"`
	Value      string        `json:"value"`
	Reason     string        `json:"reason" enum:"no_match,not_a_character,ambiguous"`
	Documents  int           `json:"documents"`
	Candidates []vocab.Brief `json:"candidates,omitempty" desc:"For ambiguous: the entities the value could mean."`
}

// LinkOut is the report.
type LinkOut struct {
	DryRun        bool        `json:"dry_run"`
	Linked        []Linked    `json:"linked"`
	Unmatched     []Unmatched `json:"unmatched"`
	AlreadyLinked int         `json:"already_linked" desc:"Scenes that already name an entity; left as they are."`
	Updated       int         `json:"updated" desc:"Scenes written; 0 on a dry run."`
}

func linkEntities(c *ops.Ctx, in LinkIn) (LinkOut, error) {
	out := LinkOut{DryRun: in.DryRun || c.DryRun(), Linked: []Linked{}, Unmatched: []Unmatched{}}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		vis, err := vocab.LoadVisible(c, tx, vocab.ReaderOf(c.P), in.ID)
		if err != nil {
			return err
		}
		// Index the visible names and aliases: a key to the entities that carry it.
		byKey := map[string][]int{}
		for i, e := range vis.Entries {
			seen := map[string]bool{}
			for _, w := range append([]string{e.Name, e.BaseName}, e.Aliases...) {
				k := text.FoldTitle(w)
				if k != "" && !seen[k] {
					seen[k] = true
					byKey[k] = append(byKey[k], i)
				}
			}
		}
		rows, err := tx.Query(`SELECT id, pov, location, pov_entity_id IS NOT NULL, location_entity_id IS NOT NULL FROM documents
			WHERE project_id = ? AND deleted_at IS NULL AND kind <> 'research' AND (pov <> '' OR location <> '' OR pov_entity_id IS NOT NULL OR location_entity_id IS NOT NULL)
			ORDER BY id`, in.ID)
		if err != nil {
			return err
		}
		type target struct{ doc, field, entity string }
		type value struct{ field, text string }
		docsBy := map[value][]string{}
		var order []value
		var writes []target
		for rows.Next() {
			var id, pov, loc string
			var hasPov, hasLoc bool
			if err := rows.Scan(&id, &pov, &loc, &hasPov, &hasLoc); err != nil {
				rows.Close()
				return err
			}
			for _, f := range []struct {
				name, text string
				has        bool
			}{{"pov", pov, hasPov}, {"location", loc, hasLoc}} {
				if f.has {
					out.AlreadyLinked++
					continue
				}
				if strings.TrimSpace(f.text) == "" {
					continue
				}
				v := value{f.name, f.text}
				if _, ok := docsBy[v]; !ok {
					order = append(order, v)
				}
				docsBy[v] = append(docsBy[v], id)
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, v := range order {
			ids := docsBy[v]
			var cands []int
			for _, i := range byKey[text.FoldTitle(v.text)] {
				cands = append(cands, i)
			}
			if v.field == "pov" {
				var chars []int
				for _, i := range cands {
					if vis.Entries[i].Kind == "character" {
						chars = append(chars, i)
					}
				}
				if len(cands) > 0 && len(chars) == 0 {
					out.Unmatched = append(out.Unmatched, Unmatched{Field: v.field, Value: v.text, Reason: "not_a_character", Documents: len(ids)})
					continue
				}
				cands = chars
			}
			switch len(cands) {
			case 0:
				out.Unmatched = append(out.Unmatched, Unmatched{Field: v.field, Value: v.text, Reason: "no_match", Documents: len(ids)})
			case 1:
				e := vis.Entries[cands[0]]
				out.Linked = append(out.Linked, Linked{Field: v.field, Value: v.text, Entity: e.Brief(), Documents: len(ids)})
				for _, id := range ids {
					writes = append(writes, target{id, v.field, e.ID})
				}
			default:
				u := Unmatched{Field: v.field, Value: v.text, Reason: "ambiguous", Documents: len(ids)}
				for _, i := range cands {
					u.Candidates = append(u.Candidates, vis.Entries[i].Brief())
				}
				out.Unmatched = append(out.Unmatched, u)
			}
		}
		if out.DryRun || len(writes) == 0 {
			return nil
		}
		for _, w := range writes {
			// The text stays as it is; the link is metadata and does not move the
			// document's revision, so an open editor's next save is not refused.
			if _, err := tx.Exec(`UPDATE documents SET `+w.field+`_entity_id = ? WHERE id = ? AND project_id = ? AND `+w.field+`_entity_id IS NULL`, w.entity, w.doc, in.ID); err != nil {
				return err
			}
			out.Updated++
		}
		c.Emit(ops.Event{Type: "scene.linked", ProjectID: in.ID, Payload: map[string]any{"documents": out.Updated}})
		c.Audit(ops.AuditEntry{Action: "scene.link_entities", TargetType: "project", TargetID: in.ID, Detail: map[string]any{"scenes": out.Updated, "unmatched": len(out.Unmatched)}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return LinkOut{}, err
	}
	sort.SliceStable(out.Unmatched, func(i, j int) bool { return out.Unmatched[i].Documents > out.Unmatched[j].Documents })
	return out, nil
}

// ---- importing links --------------------------------------------------------------------------------------------

// ImportedRef is the entity a manifest says a scene's point of view or
// location was, for an importer to try to restore.
type ImportedRef struct {
	DocumentID string
	Field      string // pov or location
	EntityID   string // from the manifest: untrusted
	Text       string // the scene's text for the field
}

// LinkImported restores entity references on documents an import just wrote.
// An id from a manifest is trusted for nothing: it is used only if that entity
// is in the project's scope and the importing user may read it, and otherwise
// the scene's text is matched by exact name. What cannot be matched stays text.
func LinkImported(ctx context.Context, tx *sql.Tx, r vocab.Reader, projectID string, refs []ImportedRef) error {
	if len(refs) == 0 {
		return nil
	}
	vis, err := vocab.LoadVisible(ctx, tx, r, projectID)
	if err != nil {
		return err
	}
	if len(vis.Entries) == 0 {
		return nil
	}
	for _, r := range refs {
		e, ok := vis.ByID[r.EntityID]
		if !ok && strings.TrimSpace(r.Text) != "" {
			k := text.FoldTitle(r.Text)
			var hit *vocab.Entry
			n := 0
			for i := range vis.Entries {
				x := &vis.Entries[i]
				match := text.FoldTitle(x.Name) == k || text.FoldTitle(x.BaseName) == k
				for _, a := range x.Aliases {
					match = match || text.FoldTitle(a) == k
				}
				if match {
					hit, n = x, n+1
				}
			}
			if n == 1 {
				e, ok = hit, true
			}
		}
		if !ok || (r.Field == "pov" && e.Kind != "character") {
			continue
		}
		col := "location_entity_id"
		if r.Field == "pov" {
			col = "pov_entity_id"
		}
		if _, err := tx.Exec(`UPDATE documents SET `+col+` = ? WHERE id = ? AND project_id = ?`, e.ID, r.DocumentID, projectID); err != nil {
			return err
		}
	}
	return nil
}

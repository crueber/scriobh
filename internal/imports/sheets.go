package imports

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Character and place sheets. A Scrivener project keeps them as research
// documents in folders called Characters and Places; the converter marks them
// (entity_candidate). With scrivener_sheets: entities the apply makes each one
// an entity of the project's primary Compendium, in the import's transaction:
//
//   - a new project gets a primary Compendium of its own, owned by the person
//     importing (nobody else is in it, so nothing is written into a bible other
//     books share without an explicit choice);
//   - adding to a project uses its primary Compendium, only if the person may
//     edit it, it is not in the trash, and a key pinned to a project reaches it
//     (as entity.create would decide); otherwise the sheets stay research notes;
//   - a name the Compendium already has (or another sheet of the import has), a
//     sheet the entity rules refuse, and a sheet that shows a picture (the
//     picture belongs to the project, not the Compendium) stay research notes.
//
// Nothing about a sheet ever fails the import: what could not become an entity
// is still written, as a note, and the result says how many.

// Options of scrivener_sheets.
const (
	SheetsNotes    = "notes"
	SheetsEntities = "entities"
)

// sheetsToEntities makes entities of the sheets among rs and returns the
// research notes that are still to be written.
func sheetsToEntities(c *ops.Ctx, tx *sql.Tx, projectID, title string, newProject bool, rs []*rplan, now string, out *ApplyOut) ([]*rplan, error) {
	var sheets []*rplan
	for _, r := range rs {
		if r.kind == "note" && (r.candidate == "character" || r.candidate == "place") {
			sheets = append(sheets, r)
		}
	}
	if len(sheets) == 0 {
		return rs, nil
	}
	keep := func(why string) ([]*rplan, error) {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d character and place sheet(s) were imported as research notes, not entities: %s", len(sheets), why))
		return rs, nil
	}
	var cid string
	if newProject {
		name := strings.TrimSpace(title)
		if len([]rune(name)) > 180 {
			name = string([]rune(name)[:180])
		}
		in := compendiums.CreateIn{Name: name + " Compendium", Kind: "fiction", ProjectID: projectID}
		if err := in.Validate(); err != nil {
			return keep("a Compendium could not be made for the project.")
		}
		id, err := compendiums.InsertTx(c, tx, in, now)
		if err != nil {
			return nil, err
		}
		cid = id
		out.CompendiumID = id
	} else {
		var primary sql.NullString
		if err := tx.QueryRow(`SELECT compendium_id FROM projects WHERE id = ?`, projectID).Scan(&primary); err != nil {
			return nil, err
		}
		if !primary.Valid {
			return keep("the project has no primary Compendium. Make one, then import again or turn the notes into entities one by one.")
		}
		cid = primary.String
		role, ok := compaccess.Role(c, tx, c.P.UserID, cid)
		switch {
		case !ok || !compaccess.AtLeast(role, compaccess.Editor):
			return keep("you may not change the project's Compendium.")
		case c.P.ProjectID != "" && c.P.ProjectID != projectID:
			return keep("this key cannot change that Compendium.")
		case c.Env.CompendiumTrashed != nil && c.Env.CompendiumTrashed(c, cid):
			return keep("the project's Compendium is in the trash.")
		}
	}
	made := map[*rplan]bool{}
	seen := map[string]bool{}
	clash, refused, withPicture := 0, 0, 0
	for _, r := range sheets {
		if strings.Contains(r.body, pictureScheme) || len(attachments.IDs(r.body)) > 0 {
			withPicture++
			continue
		}
		key := text.FoldTitle(r.title)
		if seen[key] {
			clash++
			continue
		}
		in := entities.CreateIn{ID: cid, Kind: r.candidate, Name: r.title, Body: strings.TrimSpace(r.body)}
		if err := in.Validate(); err != nil {
			refused++
			continue
		}
		id, err := entities.Insert(c, tx, in, now)
		if err != nil {
			if e := ops.AsError(err); e != nil && (e.Code == ops.CodeConflict || e.Code == ops.CodeInvalid) {
				clash++
				continue
			}
			return nil, err
		}
		seen[key] = true
		made[r] = true
		c.Emit(ops.Event{Type: "entity.created", CompendiumID: cid, Payload: map[string]any{"entity_id": id, "kind": in.Kind, "imported": true}})
	}
	out.Entities = len(made)
	if clash > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d sheet(s) have a name the Compendium already has, and were imported as research notes.", clash))
	}
	if refused > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d sheet(s) could not be entities (a name or text over the limits of an entity) and were imported as research notes.", refused))
	}
	if withPicture > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d sheet(s) show a picture, which belongs to the project, and were imported as research notes.", withPicture))
	}
	if len(made) == 0 {
		return rs, nil
	}
	left := make([]*rplan, 0, len(rs)-len(made))
	for _, r := range rs {
		if !made[r] {
			left = append(left, r)
		}
	}
	return left, nil
}

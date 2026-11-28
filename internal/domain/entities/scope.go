package entities

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// What a project shows of the bible, in one place.
//
// A project's scope is: its primary Compendium; the whole Compendiums it
// references; and single entities it links (a cameo). References are not
// followed twice. A trashed Compendium, and a trashed entity, are out of scope.
//
// The effective entity is the Compendium's record with the project's override
// applied: a name or summary the project set replaces the record's; the
// project's field values replace the record's key by key (an empty override
// value blanks the field for this book, and a key the override does not hold
// inherits); the body, aliases, kind and relationships are always the
// Compendium's. Precedence when two ways lead to the same entity: primary,
// then reference, then link (the entity is shown once). When two different
// entities in scope share a name, a lookup by name takes the primary's first,
// then the references' in the order they were added, then links.
//
// Nothing here writes the Compendium. An override is a row of the project's own
// (project_entity_overrides), and setting one changes no byte of the entity.

// scopePredicate selects entities in project ?1's scope; alias is the entities
// table's alias in the query.
func scopePredicate(alias string) string {
	return `(` + alias + `.compendium_id IN (SELECT compendium_id FROM projects WHERE id = ?1 AND compendium_id IS NOT NULL
			UNION SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1)
		OR ` + alias + `.id IN (SELECT entity_id FROM project_entity_links WHERE project_id = ?1))`
}

// OverrideView is a project's override of one entity.
type OverrideView struct {
	Name      *string           `json:"name,omitempty" desc:"The book's name for the entity; absent means the Compendium's."`
	Summary   *string           `json:"summary,omitempty" desc:"The book's summary; absent means the Compendium's."`
	Fields    map[string]string `json:"fields" desc:"Field values this book changes, by key. An empty value blanks the field for this book."`
	Notes     string            `json:"notes" desc:"Private notes for this book about the entity."`
	UpdatedAt string            `json:"updated_at"`
}

// linkOrder ranks a linked entity after every reference.
const linkOrder = 1 << 20

// eff is one entity as a project sees it.
type eff struct {
	ID, Kind, SortKey            string
	CompendiumID, CompendiumName string
	Via                          string // primary, reference or link
	Order                        int    // 0 for the primary, then the references in the order added, then linkOrder
	BaseName, BaseSummary        string
	BaseFields                   map[string]string
	Name, Summary, Body, Color   string
	Fields                       map[string]string
	Aliases                      []string
	Override                     *OverrideView
	UpdatedAt                    string
	Relations                    []string
}

// onlyOf keeps a loadEffectiveOnly read to one entity id (bound as ?2) on col; "" when it reads them all.
func onlyOf(only, col string) string {
	if only == "" {
		return ""
	}
	return " AND " + col + " = ?2"
}

// relOnly keeps the relationships to the one entity (?2), from either end.
func relOnly(only string) string {
	if only == "" {
		return ""
	}
	return " AND (r.from_entity_id = ?2 OR r.to_entity_id = ?2)"
}

// loadEffective reads every entity in a project's scope with its override
// applied, in sort order. It is one query for the entities, one for the
// aliases and one for the relationships, however many entities there are.
func loadEffective(ctx context.Context, q Querier, projectID string) ([]eff, error) {
	return loadEffectiveOnly(ctx, q, projectID, "")
}

// loadEffectiveOnly is loadEffective for one entity when only is an entity id: that entity's row, aliases and relationships, and nothing
// else of the book is read. Only "" reads them all.
func loadEffectiveOnly(ctx context.Context, q Querier, projectID, only string) ([]eff, error) {
	args := []any{projectID}
	if only != "" {
		args = append(args, only)
	}
	primary, refs, err := scopeRefs(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT `+effCols("e.body")+`
		FROM entities e
		JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		LEFT JOIN project_entity_overrides o ON o.project_id = ?1 AND o.entity_id = e.id
		WHERE e.trashed_at IS NULL AND `+scopePredicate("e")+onlyOf(only, "e.id")+` ORDER BY e.sort_key, e.id`, args...)
	if err != nil {
		return nil, err
	}
	var out []eff
	index := map[string]int{}
	for rows.Next() {
		x, err := scanEff(rows, primary, refs)
		if err != nil {
			rows.Close()
			return nil, err
		}
		index[x.ID] = len(out)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	arows, err := q.QueryContext(ctx, `SELECT a.entity_id, a.alias FROM entity_aliases a JOIN entities e ON e.id = a.entity_id
		JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		WHERE e.trashed_at IS NULL AND `+scopePredicate("e")+onlyOf(only, "a.entity_id")+` ORDER BY a.entity_id, a.alias_key, a.id`, args...)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var id, alias string
		if err := arows.Scan(&id, &alias); err != nil {
			arows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			out[i].Aliases = append(out[i].Aliases, alias)
		}
	}
	if err := arows.Err(); err != nil {
		_ = arows.Close()
		return nil, err
	}
	if err := arows.Close(); err != nil {
		return nil, err
	}
	lrows, err := q.QueryContext(ctx, `SELECT r.from_entity_id, r.to_entity_id, r.label, r.inverse_label, r.symmetric, f.name, t.name
		FROM entity_relations r JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
		WHERE f.trashed_at IS NULL AND t.trashed_at IS NULL AND (`+scopePredicate("f")+` OR `+scopePredicate("t")+`)`+relOnly(only), args...)
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var from, to, label, inverse, fname, tname string
		var sym bool
		if err := lrows.Scan(&from, &to, &label, &inverse, &sym, &fname, &tname); err != nil {
			lrows.Close()
			return nil, err
		}
		if i, ok := index[from]; ok {
			out[i].Relations = append(out[i].Relations, label+": "+tname)
		}
		if i, ok := index[to]; ok {
			l := label
			if !sym && inverse != "" {
				l = inverse
			}
			out[i].Relations = append(out[i].Relations, l+": "+fname)
		}
	}
	if err := lrows.Err(); err != nil {
		_ = lrows.Close()
		return nil, err
	}
	if err := lrows.Close(); err != nil {
		return nil, err
	}
	for i := range out {
		sort.Strings(out[i].Relations)
	}
	return out, nil
}

// effCols is the column list of an effective entity, the order scanEff reads it in. A query that does not need the body passes an empty string literal in its place.
func effCols(body string) string {
	return `e.id, e.compendium_id, c.name, e.kind, e.name, e.sort_key, e.summary, ` + body + `, e.fields_json, e.color, e.updated_at,
		o.project_id IS NOT NULL, o.name, o.summary, COALESCE(o.fields_json, '{}'), COALESCE(o.notes, ''), COALESCE(o.updated_at, '')`
}

// scanner is a row or a row set: what scanEff reads through.
type scanner interface{ Scan(dest ...any) error }

// scanEff reads one row of effCols into an eff, with the book's override applied and how the project reaches it.
func scanEff(rs scanner, primary sql.NullString, refs map[string]int) (eff, error) {
	var x eff
	var fields, ofields string
	var hasOverride bool
	var oname, osummary sql.NullString
	var onotes, oat string
	if err := rs.Scan(&x.ID, &x.CompendiumID, &x.CompendiumName, &x.Kind, &x.BaseName, &x.SortKey, &x.BaseSummary, &x.Body, &fields, &x.Color, &x.UpdatedAt,
		&hasOverride, &oname, &osummary, &ofields, &onotes, &oat); err != nil {
		return eff{}, err
	}
	switch n := refs[x.CompendiumID]; {
	case primary.Valid && x.CompendiumID == primary.String:
		x.Via, x.Order = "primary", 0
	case n > 0:
		x.Via, x.Order = "reference", n
	default:
		x.Via, x.Order = "link", linkOrder
	}
	x.BaseFields = parseFields(fields)
	x.Name, x.Summary = x.BaseName, x.BaseSummary
	x.Fields = map[string]string{}
	for k, v := range x.BaseFields {
		x.Fields[k] = v
	}
	x.Aliases = []string{}
	if hasOverride {
		ov := &OverrideView{Fields: parseFields(ofields), Notes: onotes, UpdatedAt: oat}
		if oname.Valid {
			n := oname.String
			ov.Name = &n
			x.Name = n
		}
		if osummary.Valid {
			s := osummary.String
			ov.Summary = &s
			x.Summary = s
		}
		for k, v := range ov.Fields {
			x.Fields[k] = v
		}
		x.Override = ov
	}
	return x, nil
}

// scopeRefs reads a project's primary Compendium (NULL when it has none) and the Compendiums it references. The value in refs is the
// reference's place in the order the references were added, from 1.
func scopeRefs(ctx context.Context, q Querier, projectID string) (sql.NullString, map[string]int, error) {
	var primary sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT compendium_id FROM projects WHERE id = ?`, projectID).Scan(&primary); err != nil {
		return primary, nil, ops.NotFound("project")
	}
	refs := map[string]int{}
	rr, err := q.QueryContext(ctx, `SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return primary, nil, err
	}
	for rr.Next() {
		var id string
		if err := rr.Scan(&id); err != nil {
			rr.Close()
			return primary, nil, err
		}
		refs[id] = len(refs) + 1
	}
	if err := rr.Err(); err != nil {
		_ = rr.Close()
		return primary, nil, err
	}
	if err := rr.Close(); err != nil {
		return primary, nil, err
	}
	return primary, refs, nil
}

// readableCompendiums returns the roles the caller holds on the Compendiums of a project's scope that it may read: a member at reader or
// above. A key pinned to a project reads the primary Compendium and nothing else. The candidates are the primary, the references and the
// Compendiums of the project's links; a candidate the caller may not read is left out, and its entities are counted as hidden.
func readableCompendiums(ctx context.Context, q Querier, p ops.Principal, projectID string, primary sql.NullString, refs map[string]int) (map[string]string, error) {
	var cands []string
	if primary.Valid {
		cands = append(cands, primary.String)
	}
	for id := range refs {
		cands = append(cands, id)
	}
	lrows, err := q.QueryContext(ctx, `SELECT DISTINCT e.compendium_id FROM project_entity_links l JOIN entities e ON e.id = l.entity_id WHERE l.project_id = ?`, projectID)
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var id string
		if err := lrows.Scan(&id); err != nil {
			lrows.Close()
			return nil, err
		}
		cands = append(cands, id)
	}
	if err := lrows.Err(); err != nil {
		_ = lrows.Close()
		return nil, err
	}
	if err := lrows.Close(); err != nil {
		return nil, err
	}
	roles := map[string]string{}
	checked := map[string]bool{}
	for _, cid := range cands {
		if checked[cid] || p.ProjectID != "" && (!primary.Valid || cid != primary.String) {
			continue
		}
		checked[cid] = true
		if role, ok := compaccess.Role(ctx, q, p.UserID, cid); ok && compaccess.AtLeast(role, compaccess.Reader) {
			roles[cid] = role
		}
	}
	return roles, nil
}

// ProjectionFile is a file of a project's compendium/ projection.
type ProjectionFile struct {
	Path    string
	Content []byte
}

// Projection is what the mirror writes under a project's compendium/ folder,
// relative to the project folder. It is empty (no files at all) when the
// project has no Compendium in scope.
func Projection(ctx context.Context, d *db.DB, projectID string) ([]ProjectionFile, error) {
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return projectionFrom(ctx, tx, projectID)
}

func projectionFrom(ctx context.Context, q Querier, projectID string) ([]ProjectionFile, error) {
	scopeKinds, err := loadSources(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	if len(scopeKinds) == 0 {
		return nil, nil
	}
	rows, err := loadEffective(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	items := make([]FileEntity, len(rows))
	for i, r := range rows {
		items[i] = FileEntity{ID: r.ID, Kind: r.Kind, Name: r.Name, SortKey: r.SortKey, Summary: r.Summary, Body: r.Body, Aliases: r.Aliases,
			Fields: nonEmpty(r.Fields), Color: r.Color, Relation: r.Relations, Compendium: r.CompendiumName, Reference: r.Via != "primary", Overridden: r.Override != nil}
		if r.Override != nil {
			items[i].BookNotes = r.Override.Notes
		}
	}
	files := Layout(items, "compendium/entities/")
	idx := projectionIndex(scopeKinds, files, rows)
	tl, err := projectTimelineFile(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ProjectionFile, 0, len(files)+3)
	for _, f := range files {
		out = append(out, ProjectionFile{Path: f.Path, Content: f.Content})
	}
	out = append(out,
		ProjectionFile{Path: "compendium/README.md", Content: []byte(projectionReadme)},
		ProjectionFile{Path: "compendium/timeline.md", Content: tl},
		ProjectionFile{Path: "compendium/index.json", Content: idx})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func nonEmpty(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

const projectionReadme = `# The Compendium, as this book sees it

This folder is a generated, read-only copy of the Compendium material this
project uses: the entities of its primary Compendium, of any Compendium it
references, and any single entities it links. A name, summary or field this
book overrides shows the book's version, marked overridden. Scríobh writes it
after every change and never reads it back; edit the entities in Scríobh.

- entities/ has one file per entity, in a folder per kind.
- index.json lists the sources and every entity file with its SHA-256.
- timeline.md is the timeline of the Compendiums this book draws on, in story
  order, with the scenes of this book that tell each event.
- research/ is where research notes will be written.
`

const timelinePlaceholder = "# Timeline\n\nThe Compendium timeline is not written here yet.\n"

// source is a Compendium that contributes to a project's scope.
type source struct {
	ID, Name, Role string
}

// loadSources lists the Compendiums a project draws on, primary first, then the
// references in the order they were added, then those that only contribute a
// linked entity.
func loadSources(ctx context.Context, q Querier, projectID string) ([]source, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.name, 'primary', 0, '' FROM projects p JOIN compendiums c ON c.id = p.compendium_id
			WHERE p.id = ?1 AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		UNION ALL
		SELECT c.id, c.name, 'reference', 1, r.created_at FROM compendium_refs r JOIN compendiums c ON c.id = r.target_compendium_id
			WHERE r.owner_kind = 'project' AND r.owner_id = ?1 AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		UNION ALL
		SELECT c.id, c.name, 'link', 2, '' FROM project_entity_links l JOIN entities e ON e.id = l.entity_id JOIN compendiums c ON c.id = e.compendium_id
			WHERE l.project_id = ?1 AND e.trashed_at IS NULL AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		ORDER BY 4, 5, 2, 1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var out []source
	for rows.Next() {
		var s source
		var rank int
		var at string
		if err := rows.Scan(&s.ID, &s.Name, &s.Role, &rank, &at); err != nil {
			return nil, err
		}
		if seen[s.ID] {
			continue
		}
		seen[s.ID] = true
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---- who is affected by a Compendium ---------------------------------------------------------------------------

// AffectedProjects lists every project whose projection depends on the
// Compendium: it is the project's primary, a Compendium the project
// references, or holds an entity the project links. Visibility is not applied:
// the mirror of every one of them must be rewritten.
func AffectedProjects(ctx context.Context, q Querier, compendiumID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT id FROM projects WHERE compendium_id = ?1 AND deleted_at IS NULL
		UNION SELECT owner_id FROM compendium_refs WHERE owner_kind = 'project' AND target_compendium_id = ?1
		UNION SELECT l.project_id FROM project_entity_links l JOIN entities e ON e.id = l.entity_id WHERE e.compendium_id = ?1
		ORDER BY 1`, compendiumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ReferencingCompendiums lists the Compendiums that reference the given one.
// Their mirrors name it in their manifest, so a rename or a purge of the target
// must wake them.
func ReferencingCompendiums(ctx context.Context, q Querier, compendiumID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT owner_id FROM compendium_refs WHERE owner_kind = 'compendium' AND target_compendium_id = ? ORDER BY owner_id`, compendiumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---- resolving a name ----------------------------------------------------------------------------------------------

// ResolveName finds the entity a name or alias means in a project's scope,
// ignoring case and spacing, by the precedence in this file's comment: the
// primary Compendium first, then references in the order added, then links.
// ok is false when nothing in scope has the name. Later features (mention
// detection) resolve every name through here.
func ResolveName(ctx context.Context, q Querier, projectID, name string) (entityID string, ok bool, err error) {
	rows, err := loadEffective(ctx, q, projectID)
	if err != nil {
		return "", false, err
	}
	return resolveIn(rows, name)
}

func resolveIn(rows []eff, name string) (string, bool, error) {
	k := text.FoldTitle(name)
	if k == "" {
		return "", false, nil
	}
	best, bestRank := "", linkOrder+1
	for _, r := range rows {
		match := text.FoldTitle(r.BaseName) == k || text.FoldTitle(r.Name) == k
		for _, a := range r.Aliases {
			if text.FoldTitle(a) == k {
				match = true
			}
		}
		if match && r.Order < bestRank {
			best, bestRank = r.ID, r.Order
		}
	}
	return best, best != "", nil
}

// ---- wire shapes --------------------------------------------------------------------------------------------------------

// ProjectEntity is an entity as a project shows it.
type ProjectEntity struct {
	ID             string            `json:"id"`
	Kind           string            `json:"kind" enum:"character,place,item,org,concept"`
	Name           string            `json:"name" desc:"The effective name: the book's override, else the Compendium's."`
	Summary        string            `json:"summary" desc:"The effective summary."`
	Body           string            `json:"body,omitempty" desc:"The Compendium's Markdown body (never overridden). Only on project.entity.get."`
	Fields         map[string]string `json:"fields" desc:"Effective field values: the Compendium's with the book's overrides applied."`
	Aliases        []string          `json:"aliases"`
	Color          string            `json:"color"`
	CompendiumID   string            `json:"compendium_id" desc:"Where the entity lives."`
	CompendiumName string            `json:"compendium_name"`
	Via            string            `json:"via" enum:"primary,reference,link" desc:"How the project reaches it: its primary Compendium, a whole-Compendium reference, or a single link."`
	Overridden     bool              `json:"overridden"`
	Override       *OverrideView     `json:"override,omitempty" desc:"The book's override, with the Compendium's own name, summary and fields in base. Only on project.entity.get and the override ops."`
	Base           *Base             `json:"base,omitempty"`
	Editable       bool              `json:"editable" desc:"The caller can edit the Compendium's record (an editor there). Entities reached by reference or link are never edited from the project."`
	UpdatedAt      string            `json:"updated_at"`
}

// Base is the Compendium's own record of an overridden entity.
type Base struct {
	Name    string            `json:"name"`
	Summary string            `json:"summary"`
	Fields  map[string]string `json:"fields"`
}

func (x eff) wire(roles map[string]string, full bool) ProjectEntity {
	p := ProjectEntity{ID: x.ID, Kind: x.Kind, Name: x.Name, Summary: x.Summary, Fields: nonEmpty(x.Fields), Aliases: x.Aliases, Color: x.Color,
		CompendiumID: x.CompendiumID, CompendiumName: x.CompendiumName, Via: x.Via, Overridden: x.Override != nil,
		Editable: compaccess.AtLeast(roles[x.CompendiumID], compaccess.Editor), UpdatedAt: x.UpdatedAt}
	if full {
		p.Body = x.Body
		p.Override = x.Override
		if x.Override != nil {
			p.Base = &Base{Name: x.BaseName, Summary: x.BaseSummary, Fields: nonEmpty(x.BaseFields)}
		}
	}
	return p
}

// readable keeps the entities of Compendiums the caller is a member of and
// returns their roles. Entities of other Compendiums are left out entirely:
// being on the project never grants the bible.
//
// A key pinned to a project sees the project's primary Compendium and nothing
// else: references and links are left out for it, whatever its owner may read.
func readable(ctx context.Context, q Querier, p ops.Principal, rows []eff) ([]eff, map[string]string, error) {
	roles := map[string]string{}
	checked := map[string]bool{}
	kept := rows[:0:0]
	for _, r := range rows {
		if p.ProjectID != "" && r.Via != "primary" {
			continue
		}
		if !checked[r.CompendiumID] {
			checked[r.CompendiumID] = true
			if role, ok := compaccess.Role(ctx, q, p.UserID, r.CompendiumID); ok && compaccess.AtLeast(role, compaccess.Reader) {
				roles[r.CompendiumID] = role
			}
		}
		if roles[r.CompendiumID] != "" {
			kept = append(kept, r)
		}
	}
	return kept, roles, nil
}

// ---- ops -----------------------------------------------------------------------------------------------------------------

func registerScope(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "project.entity.list", Method: "GET", Path: "/api/v1/projects/{id}/entities",
		Desc: "The entities a project shows, as the project sees them: its primary Compendium's, those of Compendiums it references, and single linked entities, each with the project's override applied and how it was reached (via). " +
			"Only entities of Compendiums the caller is a member of are returned: being on the project does not grant the Compendium, so a project reader who is not on the Compendium gets an empty list. No bodies; use project.entity.get.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, projectList)
	ops.Register(r, ops.Op{
		Name: "project.entity.get", Method: "GET", Path: "/api/v1/projects/{id}/entity",
		Desc: "One entity as the project sees it: the effective name, summary and fields, the Compendium's body, the override (with the Compendium's own values in base), and how it was reached. " +
			"entity is an entity id or a name or alias, resolved among the project's entities primary first. Not found if the caller is not a member of the entity's Compendium.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, projectGet)
	ops.Register(r, ops.Op{
		Name: "project.entity.override.set", Method: "PUT", Path: "/api/v1/projects/{id}/entity-overrides",
		Desc: "Override a shared entity for this book only: a different name or summary, different values for custom fields, private notes. The Compendium's record is not touched, and other books are not affected. " +
			"The entity must be in the project's scope and the caller must be a member of its Compendium. Send only what changes: an empty name or summary goes back to inheriting; a null field value stops overriding it; an empty string blanks it for this book. Project editor.",
		Auth: ops.RequireProject(ops.ProjectEditor),
	}, overrideSet)
	ops.Register(r, ops.Op{
		Name: "project.entity.override.clear", Permanent: true, Method: "DELETE", Path: "/api/v1/projects/{id}/entity-overrides",
		Desc: "Remove this book's override of an entity, so it shows the Compendium's record again. Does nothing if there is none. Project editor.",
		Auth: ops.RequireProject(ops.ProjectEditor),
	}, overrideClear)
	ops.Register(r, ops.Op{
		Name: "project.entity.link.list", Method: "GET", Path: "/api/v1/projects/{id}/entity-links",
		Desc: "List the single entities this project links from Compendiums it does not otherwise use (cameos). Only entities of Compendiums the caller is a member of are listed.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, linkList)
	ops.Register(r, ops.Op{
		Name: "project.entity.link.add", Method: "POST", Path: "/api/v1/projects/{id}/entity-links",
		Desc: "Link one entity from a Compendium into this project, read-only (a cameo): it appears among the project's entities and in the mirror's compendium/ folder. The caller must be a member of the entity's Compendium. " +
			"Linking an entity the project already reaches through its primary Compendium or a reference does nothing. Project editor.",
		Auth: ops.RequireProject(ops.ProjectEditor),
	}, linkAdd)
	ops.Register(r, ops.Op{
		Name: "project.entity.link.remove", Method: "DELETE", Path: "/api/v1/projects/{id}/entity-links",
		Desc: "Remove a linked entity from the project. Its override, if any, is kept, dormant. Does nothing if it was not linked. Project editor.",
		Auth: ops.RequireProject(ops.ProjectEditor),
	}, linkRemove)
}

// ProjectListIn lists a project's entities.
type ProjectListIn struct {
	ID         string `path:"id" json:"-" desc:"Project id."`
	Kind       string `query:"kind" desc:"Only one kind." enum:"character,place,item,org,concept"`
	Q          string `query:"q" desc:"Names and aliases containing these characters, ignoring case."`
	Compendium string `query:"compendium_id" desc:"Only entities of one Compendium."`
	Limit      int    `query:"limit" desc:"Page size, 1 to 500; default 100."`
	Offset     int    `query:"offset" desc:"Entities to skip."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectListIn) ProjectRef() string { return in.ID }

// Validate defaults and checks.
func (in *ProjectListIn) Validate() error {
	if in.Kind != "" && !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "Kind must be one of: "+strings.Join(Kinds, ", ")+".")
	}
	switch {
	case in.Limit <= 0:
		in.Limit = ListDefault
	case in.Limit > ListMax:
		in.Limit = ListMax
	}
	if in.Offset < 0 {
		return ops.Invalid("offset", "The offset cannot be negative.")
	}
	return nil
}

// ProjectListOut is a page of a project's entities.
type ProjectListOut struct {
	Items      []ProjectEntity `json:"items"`
	Total      int             `json:"total"`
	NextOffset int             `json:"next_offset,omitempty"`
	Hidden     bool            `json:"hidden" desc:"True when the project has entities of a Compendium the caller is not a member of. They are not counted or named."`
}

// projectList answers one page of a project's entities. The database does the work: the scope, the caller's readable Compendiums, the kind
// and Compendium filters and the search (each word folded as the Go code folds it, through scr_fold) are one WHERE clause, the counts and the
// page are read with it, and only the page's rows and aliases are read. A body and the relationships are never read here.
func projectList(c *ops.Ctx, in ProjectListIn) (ProjectListOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectListOut{}, err
	}
	defer tx.Rollback()
	out := ProjectListOut{Items: []ProjectEntity{}}
	primary, refs, err := scopeRefs(c, tx, in.ID)
	if err != nil {
		return out, err
	}
	roles, err := readableCompendiums(c, tx, c.P, in.ID, primary, refs)
	if err != nil {
		return out, err
	}
	allowed, err := json.Marshal(keysOf(roles))
	if err != nil {
		return out, err
	}
	// Hidden: some entity of the scope is in a Compendium the caller may not read.
	var all, readable int
	if err := tx.QueryRowContext(c, `SELECT COUNT(*), COALESCE(SUM(e.compendium_id IN (SELECT value FROM json_each(?2))), 0)
		FROM entities e JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		WHERE e.trashed_at IS NULL AND `+scopePredicate("e"), in.ID, string(allowed)).Scan(&all, &readable); err != nil {
		return out, err
	}
	out.Hidden = readable < all

	args := []any{in.ID, string(allowed)}
	add := func(v any) string {
		args = append(args, v)
		return "?" + strconv.Itoa(len(args))
	}
	where := []string{`e.trashed_at IS NULL`, scopePredicate("e"), `e.compendium_id IN (SELECT value FROM json_each(?2))`}
	if in.Kind != "" {
		where = append(where, `e.kind = `+add(in.Kind))
	}
	if in.Compendium != "" {
		where = append(where, `e.compendium_id = `+add(in.Compendium))
	}
	if k := text.FoldTitle(in.Q); k != "" {
		ph := add(k)
		where = append(where, `(instr(scr_fold(COALESCE(o.name, e.name)), `+ph+`) > 0 OR instr(scr_fold(e.name), `+ph+`) > 0
			OR EXISTS (SELECT 1 FROM entity_aliases a WHERE a.entity_id = e.id AND instr(scr_fold(a.alias), `+ph+`) > 0))`)
	}
	from := ` FROM entities e
		JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		LEFT JOIN project_entity_overrides o ON o.project_id = ?1 AND o.entity_id = e.id
		WHERE ` + strings.Join(where, " AND ")
	var total int
	if err := tx.QueryRowContext(c, `SELECT COUNT(*)`+from, args...).Scan(&total); err != nil {
		return out, err
	}
	out.Total = total
	if in.Offset < 0 || in.Offset >= total {
		return out, nil
	}
	end := in.Offset + in.Limit
	if end > total {
		end = total
	}
	if end < total {
		out.NextOffset = end
	}
	if end <= in.Offset {
		return out, nil
	}
	pargs := append(append([]any{}, args...), end-in.Offset, in.Offset)
	rows, err := tx.QueryContext(c, `SELECT `+effCols("''")+from+` ORDER BY e.sort_key, e.id LIMIT ?`+strconv.Itoa(len(args)+1)+` OFFSET ?`+strconv.Itoa(len(args)+2), pargs...)
	if err != nil {
		return out, err
	}
	var page []eff
	index := map[string]int{}
	for rows.Next() {
		x, err := scanEff(rows, primary, refs)
		if err != nil {
			rows.Close()
			return out, err
		}
		index[x.ID] = len(page)
		page = append(page, x)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	if len(page) > 0 {
		ids := make([]string, len(page))
		for i, x := range page {
			ids[i] = x.ID
		}
		idJSON, err := json.Marshal(ids)
		if err != nil {
			return out, err
		}
		arows, err := tx.QueryContext(c, `SELECT a.entity_id, a.alias FROM entity_aliases a WHERE a.entity_id IN (SELECT value FROM json_each(?1))
			ORDER BY a.entity_id, a.alias_key, a.id`, string(idJSON))
		if err != nil {
			return out, err
		}
		for arows.Next() {
			var id, alias string
			if err := arows.Scan(&id, &alias); err != nil {
				arows.Close()
				return out, err
			}
			if i, ok := index[id]; ok {
				page[i].Aliases = append(page[i].Aliases, alias)
			}
		}
		if err := arows.Err(); err != nil {
			_ = arows.Close()
			return out, err
		}
		if err := arows.Close(); err != nil {
			return out, err
		}
	}
	for _, x := range page {
		out.Items = append(out.Items, x.wire(roles, false))
	}
	return out, nil
}

// keysOf is the keys of a map, in no order.
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ProjectGetIn names one entity in a project.
type ProjectGetIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	Entity string `query:"entity" desc:"An entity id, or a name or alias."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectGetIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *ProjectGetIn) Validate() error {
	if strings.TrimSpace(in.Entity) == "" {
		return ops.Invalid("entity", "Say which entity: an id, a name or an alias.")
	}
	return nil
}

// ProjectItem wraps one project entity.
type ProjectItem struct {
	Item ProjectEntity `json:"item"`
}

func findIn(rows []eff, ref string) (eff, bool) {
	for _, r := range rows {
		if r.ID == ref {
			return r, true
		}
	}
	id, ok, _ := resolveIn(rows, ref)
	if !ok {
		return eff{}, false
	}
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return eff{}, false
}

func projectGet(c *ops.Ctx, in ProjectGetIn) (ProjectItem, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectItem{}, err
	}
	defer tx.Rollback()
	ref := strings.TrimSpace(in.Entity)
	// An id, which the lookup panel and the sheet send, reads that entity alone. Anything else (a name, an alias, an unknown id) still reads
	// the whole book, as it always did, so the answer for a name is unchanged.
	one, err := loadEffectiveOnly(c, tx, in.ID, ref)
	if err != nil {
		return ProjectItem{}, err
	}
	if one, roles, err := readable(c, tx, c.P, one); err != nil {
		return ProjectItem{}, err
	} else if x, ok := findIn(one, ref); ok {
		return ProjectItem{Item: x.wire(roles, true)}, nil
	}
	rows, err := loadEffective(c, tx, in.ID)
	if err != nil {
		return ProjectItem{}, err
	}
	rows, roles, err := readable(c, tx, c.P, rows)
	if err != nil {
		return ProjectItem{}, err
	}
	x, ok := findIn(rows, ref)
	if !ok {
		return ProjectItem{}, ops.NotFound("entity")
	}
	return ProjectItem{Item: x.wire(roles, true)}, nil
}

// OverrideSetIn sets a book's override of an entity.
type OverrideSetIn struct {
	ID       string         `path:"id" json:"-" desc:"Project id."`
	EntityID string         `json:"entity_id" desc:"The entity to override."`
	Name     *string        `json:"name,omitempty" desc:"The book's name for it; empty goes back to the Compendium's."`
	Summary  *string        `json:"summary,omitempty" desc:"The book's summary; empty goes back to the Compendium's."`
	Fields   map[string]any `json:"fields,omitempty" desc:"Field values for this book, by key (defined for the kind). null stops overriding that field; an empty string blanks it for this book."`
	Notes    *string        `json:"notes,omitempty" desc:"Private notes for this book, up to 4,000 characters."`
}

// ProjectRef lets the registry authorize by membership.
func (in OverrideSetIn) ProjectRef() string { return in.ID }

// Validate trims and checks the input.
func (in *OverrideSetIn) Validate() error {
	if in.EntityID == "" {
		return ops.Invalid("entity_id", "Say which entity to override.")
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if err := checkLine("name", n, maxName, false); err != nil {
			return err
		}
		in.Name = &n
	}
	if in.Summary != nil {
		s := strings.TrimSpace(*in.Summary)
		if err := checkText("summary", s, maxSummary*4); err != nil {
			return err
		}
		in.Summary = &s
	}
	if in.Notes != nil {
		n := strings.TrimSpace(*in.Notes)
		if err := checkText("notes", n, 16<<10); err != nil {
			return err
		}
		in.Notes = &n
	}
	return nil
}

// OverrideOut is the entity after the change.
type OverrideOut struct {
	Item ProjectEntity `json:"item"`
}

// inScopeEntity checks the entity is in the project's scope and the caller can
// read its Compendium. Both failures are "not found".
func inScopeEntity(ctx context.Context, q Querier, p ops.Principal, projectID, entityID string) (cid string, err error) {
	var cidN sql.NullString
	err = q.QueryRowContext(ctx, `SELECT e.compendium_id FROM entities e JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		WHERE e.id = ?2 AND e.trashed_at IS NULL AND `+scopePredicate("e"), projectID, entityID).Scan(&cidN)
	if err != nil || !cidN.Valid {
		return "", ops.NotFound("entity")
	}
	if !compaccess.CanRead(ctx, q, p.UserID, cidN.String) || !pinAllows(ctx, q, p, projectID, cidN.String) {
		return "", ops.NotFound("entity")
	}
	return cidN.String, nil
}

// pinAllows applies a project key's pin to a Compendium: such a key reaches the
// project's primary Compendium only. Any other principal passes.
func pinAllows(ctx context.Context, q Querier, p ops.Principal, projectID, compendiumID string) bool {
	if p.ProjectID == "" {
		return true
	}
	var primary sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT compendium_id FROM projects WHERE id = ?`, projectID).Scan(&primary); err != nil {
		return false
	}
	return primary.Valid && primary.String == compendiumID
}

func overrideSet(c *ops.Ctx, in OverrideSetIn) (OverrideOut, error) {
	var out OverrideOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, err := inScopeEntity(c, tx, c.P, in.ID, in.EntityID)
		if err != nil {
			return err
		}
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM entities WHERE id = ?`, in.EntityID).Scan(&kind); err != nil {
			return err
		}
		var name, summary sql.NullString
		var fieldsJSON, notes string
		err = tx.QueryRow(`SELECT name, summary, fields_json, notes FROM project_entity_overrides WHERE project_id = ? AND entity_id = ?`, in.ID, in.EntityID).Scan(&name, &summary, &fieldsJSON, &notes)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		fields := parseFields(fieldsJSON)
		if in.Name != nil {
			name = sql.NullString{String: *in.Name, Valid: *in.Name != ""}
		}
		if in.Summary != nil {
			summary = sql.NullString{String: *in.Summary, Valid: *in.Summary != ""}
		}
		if in.Notes != nil {
			notes = *in.Notes
		}
		if len(in.Fields) > 0 {
			d, err := loadDefs(c, tx, cid, kind)
			if err != nil {
				return err
			}
			set := map[string]any{}
			for k, v := range in.Fields {
				if v == nil {
					if _, defined := d[k]; !defined {
						return ops.Invalid("fields."+k, fmt.Sprintf("No field %q is defined for this kind of entity.", k))
					}
					delete(fields, k)
					continue
				}
				set[k] = v
			}
			if fields, err = applyFields(d, fields, set, true); err != nil {
				return err
			}
		}
		now := db.Now()
		if !name.Valid && !summary.Valid && len(fields) == 0 && notes == "" {
			if _, err := tx.Exec(`DELETE FROM project_entity_overrides WHERE project_id = ? AND entity_id = ?`, in.ID, in.EntityID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(`INSERT INTO project_entity_overrides(project_id, entity_id, name, summary, fields_json, notes, updated_by, updated_at) VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT(project_id, entity_id) DO UPDATE SET name = excluded.name, summary = excluded.summary, fields_json = excluded.fields_json,
				notes = excluded.notes, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			in.ID, in.EntityID, name, summary, marshalFields(fields), notes, nullIfEmpty(c.P.UserID), now); err != nil {
			return err
		}
		out.Item, err = oneEffective(c, tx, c.P, in.ID, in.EntityID)
		if err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.override.set", ProjectID: in.ID, Payload: map[string]any{"entity_id": in.EntityID}})
		return c.FlushTo(tx)
	})
	return out, err
}

func oneEffective(ctx context.Context, q Querier, p ops.Principal, projectID, entityID string) (ProjectEntity, error) {
	rows, err := loadEffectiveOnly(ctx, q, projectID, entityID)
	if err != nil {
		return ProjectEntity{}, err
	}
	rows, roles, err := readable(ctx, q, p, rows)
	if err != nil {
		return ProjectEntity{}, err
	}
	for _, r := range rows {
		if r.ID == entityID {
			return r.wire(roles, true), nil
		}
	}
	return ProjectEntity{}, ops.NotFound("entity")
}

// OverrideClearIn removes an override.
type OverrideClearIn struct {
	ID       string `path:"id" json:"-" desc:"Project id."`
	EntityID string `query:"entity_id" desc:"The overridden entity."`
}

// ProjectRef lets the registry authorize by membership.
func (in OverrideClearIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *OverrideClearIn) Validate() error {
	if in.EntityID == "" {
		return ops.Invalid("entity_id", "Say which entity.")
	}
	return nil
}

// RemovedOut reports a removal.
type RemovedOut struct {
	Removed bool `json:"removed" desc:"False when there was nothing to remove."`
}

// mayTouch is the check clear and unlink make: the entity exists and the caller
// is a member of its Compendium. Without it, `removed` would tell someone off
// the Compendium whether an override or a link of an entity they cannot see
// exists.
func mayTouch(ctx context.Context, q Querier, p ops.Principal, projectID, entityID string) error {
	cid, ok := ownerOf(ctx, q, entityID)
	if !ok || !compaccess.CanRead(ctx, q, p.UserID, cid) || !pinAllows(ctx, q, p, projectID, cid) {
		return ops.NotFound("entity")
	}
	return nil
}

func overrideClear(c *ops.Ctx, in OverrideClearIn) (RemovedOut, error) {
	var out RemovedOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := mayTouch(c, tx, c.P, in.ID, in.EntityID); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM project_entity_overrides WHERE project_id = ? AND entity_id = ?`, in.ID, in.EntityID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out.Removed = true
			c.Emit(ops.Event{Type: "entity.override.cleared", ProjectID: in.ID, Payload: map[string]any{"entity_id": in.EntityID}})
			return c.FlushTo(tx)
		}
		return nil
	})
	return out, err
}

// LinkIn names an entity to link or unlink.
type LinkIn struct {
	ID       string `path:"id" json:"-" desc:"Project id."`
	EntityID string `json:"entity_id,omitempty" desc:"The entity."`
}

// ProjectRef lets the registry authorize by membership.
func (in LinkIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *LinkIn) Validate() error {
	if in.EntityID == "" {
		return ops.Invalid("entity_id", "Say which entity.")
	}
	return nil
}

// LinkRemoveIn unlinks an entity.
type LinkRemoveIn struct {
	ID       string `path:"id" json:"-" desc:"Project id."`
	EntityID string `query:"entity_id" desc:"The linked entity."`
}

// ProjectRef lets the registry authorize by membership.
func (in LinkRemoveIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *LinkRemoveIn) Validate() error {
	if in.EntityID == "" {
		return ops.Invalid("entity_id", "Say which entity.")
	}
	return nil
}

// LinkListIn lists links.
type LinkListIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in LinkListIn) ProjectRef() string { return in.ID }

// LinkListOut lists a project's linked entities.
type LinkListOut struct {
	Items []LinkedEntity `json:"items"`
}

// LinkedEntity is one cameo.
type LinkedEntity struct {
	EntityID       string `json:"entity_id"`
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	CompendiumID   string `json:"compendium_id"`
	CompendiumName string `json:"compendium_name"`
	Mode           string `json:"mode" enum:"reference"`
	CreatedAt      string `json:"created_at"`
}

// LinkItem says whether this call created the link.
type LinkItem struct {
	Item    LinkedEntity `json:"item"`
	Created bool         `json:"created"`
}

func linkList(c *ops.Ctx, in LinkListIn) (LinkListOut, error) {
	if c.P.ProjectID != "" {
		return LinkListOut{Items: []LinkedEntity{}}, nil // a project key reaches its primary Compendium only
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT e.id, e.name, e.kind, c.id, c.name, l.mode, l.created_at
		FROM project_entity_links l JOIN entities e ON e.id = l.entity_id JOIN compendiums c ON c.id = e.compendium_id
		WHERE l.project_id = ? AND e.trashed_at IS NULL AND c.deleted_at IS NULL ORDER BY e.sort_key, e.id`, in.ID)
	if err != nil {
		return LinkListOut{}, err
	}
	var all []LinkedEntity
	for rows.Next() {
		var l LinkedEntity
		if err := rows.Scan(&l.EntityID, &l.Name, &l.Kind, &l.CompendiumID, &l.CompendiumName, &l.Mode, &l.CreatedAt); err != nil {
			rows.Close()
			return LinkListOut{}, err
		}
		all = append(all, l)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return LinkListOut{}, err
	}
	if err := rows.Close(); err != nil {
		return LinkListOut{}, err
	}
	out := LinkListOut{Items: []LinkedEntity{}}
	checked := map[string]bool{}
	ok := map[string]bool{}
	for _, l := range all {
		if !checked[l.CompendiumID] {
			checked[l.CompendiumID] = true
			ok[l.CompendiumID] = compaccess.CanRead(c, c.DB().Read(), c.P.UserID, l.CompendiumID)
		}
		if ok[l.CompendiumID] {
			out.Items = append(out.Items, l)
		}
	}
	return out, nil
}

func linkAdd(c *ops.Ctx, in LinkIn) (LinkItem, error) {
	if c.P.Pinned() {
		return LinkItem{}, ops.Forbidden("a key restricted to one project or compendium cannot link entities from a Compendium")
	}
	var out LinkItem
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var cid sql.NullString
		var name, kind, cname string
		if err := tx.QueryRow(`SELECT e.compendium_id, e.name, e.kind, c.name FROM entities e JOIN compendiums c ON c.id = e.compendium_id
			WHERE e.id = ? AND e.trashed_at IS NULL AND c.deleted_at IS NULL`, in.EntityID).Scan(&cid, &name, &kind, &cname); err != nil || !cid.Valid {
			return ops.NotFound("entity")
		}
		if !compaccess.CanRead(c, tx, c.P.UserID, cid.String) {
			return ops.NotFound("entity")
		}
		// Already reachable through the primary or a reference: nothing to add.
		var reach int
		if err := tx.QueryRow(`SELECT (SELECT count(*) FROM projects WHERE id = ?1 AND compendium_id = ?2)
			+ (SELECT count(*) FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1 AND target_compendium_id = ?2)`, in.ID, cid.String).Scan(&reach); err != nil {
			return err
		}
		out.Item = LinkedEntity{EntityID: in.EntityID, Name: name, Kind: kind, CompendiumID: cid.String, CompendiumName: cname, Mode: "reference"}
		if reach > 0 {
			return nil
		}
		now := db.Now()
		res, err := tx.Exec(`INSERT OR IGNORE INTO project_entity_links(project_id, entity_id, mode, created_by, created_at) VALUES (?,?,?,?,?)`, in.ID, in.EntityID, "reference", nullIfEmpty(c.P.UserID), now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			_ = tx.QueryRow(`SELECT created_at FROM project_entity_links WHERE project_id = ? AND entity_id = ?`, in.ID, in.EntityID).Scan(&out.Item.CreatedAt)
			return nil
		}
		out.Created, out.Item.CreatedAt = true, now
		c.Emit(ops.Event{Type: "entity.link.added", ProjectID: in.ID, Payload: map[string]any{"entity_id": in.EntityID, "compendium_id": cid.String}})
		return c.FlushTo(tx)
	})
	return out, err
}

func linkRemove(c *ops.Ctx, in LinkRemoveIn) (RemovedOut, error) {
	var out RemovedOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := mayTouch(c, tx, c.P, in.ID, in.EntityID); err != nil {
			return err
		}
		res, err := tx.Exec(`DELETE FROM project_entity_links WHERE project_id = ? AND entity_id = ?`, in.ID, in.EntityID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out.Removed = true
			c.Emit(ops.Event{Type: "entity.link.removed", ProjectID: in.ID, Payload: map[string]any{"entity_id": in.EntityID}})
			return c.FlushTo(tx)
		}
		return nil
	})
	return out, err
}

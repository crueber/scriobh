// Package vocab is the light view of a project's scope: every entity the
// project can reach, with the words that mean it, and nothing else (no bodies,
// no relationships, no custom fields), plus the filter that keeps a reader to
// what their Compendium membership allows. Mention detection reads it on every
// save that finds a cold cache, the scene sidebar reads it for summaries, the
// outline shows point of view through it, and all must stay cheap with
// hundreds of entities, which the entities package's full effective view is
// not. It stands alone because the document ops write mention rows and so
// cannot import entities (which imports projects, which imports docs).
package vocab

import (
	"context"
	"database/sql"

	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Querier is what *sql.DB and *sql.Tx share.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// linkOrder ranks a linked entity after every reference.
const linkOrder = 1 << 20

// scopePredicate selects entities in project ?1's scope; alias is the entities
// table's alias in the query. It is the entities package's rule: the primary
// Compendium, the Compendiums the project references, and single linked
// entities.
func scopePredicate(alias string) string {
	return `(` + alias + `.compendium_id IN (SELECT compendium_id FROM projects WHERE id = ?1 AND compendium_id IS NOT NULL
			UNION SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1)
		OR ` + alias + `.id IN (SELECT entity_id FROM project_entity_links WHERE project_id = ?1))`
}

// Entry is one entity in a project's scope as the project names it.
type Entry struct {
	ID             string
	Kind           string
	CompendiumID   string
	CompendiumName string
	Via            string // primary, reference or link
	Order          int    // precedence when two entities answer to one word: 0 for the primary, then references in the order added, then links
	Name           string // the effective name: the book's override, else the Compendium's
	BaseName       string // the Compendium's own name
	Summary        string // the effective summary
	Color          string
	Aliases        []string
	Overridden     bool
}

// Load lists the live entities in a project's scope, ordered by sort
// key then id, so the same data always gives the same order.
func Load(ctx context.Context, q Querier, projectID string) ([]Entry, error) {
	var primary sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT compendium_id FROM projects WHERE id = ?`, projectID).Scan(&primary); err != nil {
		return nil, ops.NotFound("project")
	}
	refs := map[string]int{}
	rr, err := q.QueryContext(ctx, `SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ? ORDER BY created_at, id`, projectID)
	if err != nil {
		return nil, err
	}
	for rr.Next() {
		var id string
		if err := rr.Scan(&id); err != nil {
			rr.Close()
			return nil, err
		}
		refs[id] = len(refs) + 1
	}
	if err := rr.Close(); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.compendium_id, c.name, e.kind, e.name, e.summary, e.color,
			o.project_id IS NOT NULL, o.name, o.summary
		FROM entities e
		JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		LEFT JOIN project_entity_overrides o ON o.project_id = ?1 AND o.entity_id = e.id
		WHERE e.trashed_at IS NULL AND `+scopePredicate("e")+` ORDER BY e.sort_key, e.id`, projectID)
	if err != nil {
		return nil, err
	}
	var out []Entry
	index := map[string]int{}
	for rows.Next() {
		var x Entry
		var has bool
		var oname, osummary sql.NullString
		if err := rows.Scan(&x.ID, &x.CompendiumID, &x.CompendiumName, &x.Kind, &x.BaseName, &x.Summary, &x.Color, &has, &oname, &osummary); err != nil {
			rows.Close()
			return nil, err
		}
		switch n := refs[x.CompendiumID]; {
		case primary.Valid && x.CompendiumID == primary.String:
			x.Via, x.Order = "primary", 0
		case n > 0:
			x.Via, x.Order = "reference", n
		default:
			x.Via, x.Order = "link", linkOrder
		}
		x.Name = x.BaseName
		if has {
			x.Overridden = true
			if oname.Valid {
				x.Name = oname.String
			}
			if osummary.Valid {
				x.Summary = osummary.String
			}
		}
		x.Aliases = []string{}
		index[x.ID] = len(out)
		out = append(out, x)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	arows, err := q.QueryContext(ctx, `SELECT a.entity_id, a.alias FROM entity_aliases a JOIN entities e ON e.id = a.entity_id
		JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		WHERE e.trashed_at IS NULL AND `+scopePredicate("e")+` ORDER BY a.entity_id, a.alias_key, a.id`, projectID)
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
	return out, arows.Close()
}

// ReadableRoles returns the caller's role on each Compendium the vocabulary
// draws on, leaving out the ones the caller is not a member of. Being on a
// project grants none of the bible, so everything that shows entities to a
// reader filters through this.
func ReadableRoles(ctx context.Context, q Querier, userID string, v []Entry) map[string]string {
	roles := map[string]string{}
	checked := map[string]bool{}
	for _, e := range v {
		if checked[e.CompendiumID] {
			continue
		}
		checked[e.CompendiumID] = true
		if role, ok := compaccess.Role(ctx, q, userID, e.CompendiumID); ok && compaccess.AtLeast(role, compaccess.Reader) {
			roles[e.CompendiumID] = role
		}
	}
	return roles
}

// Brief is an entity as a scene or a timeline names it.
type Brief struct {
	ID         string `json:"id"`
	Name       string `json:"name" desc:"The effective name: the book's override, else the Compendium's."`
	Kind       string `json:"kind" enum:"character,place,item,org,concept"`
	Color      string `json:"color,omitempty"`
	Summary    string `json:"summary,omitempty" desc:"The effective summary."`
	Overridden bool   `json:"overridden,omitempty" desc:"This book overrides the entity."`
	Via        string `json:"via,omitempty" enum:"primary,reference,link"`
}

// Brief is the wire form of a vocabulary entry.
func (e Entry) Brief() Brief {
	return Brief{ID: e.ID, Name: e.Name, Kind: e.Kind, Color: e.Color, Summary: e.Summary, Overridden: e.Overridden, Via: e.Via}
}

// Visible is a project's vocabulary as one reader may see it.
type Visible struct {
	All     []Entry // everything in scope, readable or not: for matching, never for showing
	Entries []Entry
	ByID    map[string]*Entry
	Roles   map[string]string // the caller's role on each Compendium they may read
	Hidden  bool              // the project reaches entities the caller may not read
}

// Reader is who is asking: a person, and the project an API key is pinned to
// when they came through one. A key pinned to a project reaches that project's
// primary Compendium and no other, so through such a key the book's references
// and linked entities are not readable, whatever the person behind it may read.
// The zero Reader is not a person (the mirror, which is a projection of what
// the project's owner wired in) and is not filtered: use it nowhere else.
type Reader struct {
	UserID  string
	Project string // the pin; empty for a session or an unpinned key
}

// ReaderOf is the Reader of a principal.
func ReaderOf(p ops.Principal) Reader { return Reader{UserID: p.UserID, Project: p.ProjectID} }

// LoadVisible loads a project's vocabulary and keeps only what the
// caller may read.
func LoadVisible(ctx context.Context, q Querier, r Reader, projectID string) (*Visible, error) {
	all, err := Load(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	roles := ReadableRoles(ctx, q, r.UserID, all)
	v := &Visible{All: all, Roles: roles, ByID: map[string]*Entry{}}
	for _, e := range all {
		if roles[e.CompendiumID] == "" || (r.Project != "" && e.Via != "primary") {
			v.Hidden = true
			continue
		}
		v.Entries = append(v.Entries, e)
	}
	for i := range v.Entries {
		v.ByID[v.Entries[i].ID] = &v.Entries[i]
	}
	return v, nil
}

// Find resolves an entity id, or a name or alias, among the visible entities
// the way a project does: an id first, then the name or alias of the entity
// with the best precedence. ok is false when none is visible.
func (v *Visible) Find(ref string) (Entry, bool) {
	if e, ok := v.ByID[ref]; ok {
		return *e, true
	}
	k := text.FoldTitle(ref)
	if k == "" {
		return Entry{}, false
	}
	best, bestRank := -1, linkOrder+1
	for i, e := range v.Entries {
		match := text.FoldTitle(e.BaseName) == k || text.FoldTitle(e.Name) == k
		for _, a := range e.Aliases {
			match = match || text.FoldTitle(a) == k
		}
		if match && e.Order < bestRank {
			best, bestRank = i, e.Order
		}
	}
	if best < 0 {
		return Entry{}, false
	}
	return v.Entries[best], true
}

// Candidates returns every visible entity that ref can mean: the entity itself
// for an id, otherwise each one whose name or alias (the book's or the
// Compendium's) matches, ignoring case and spacing. kinds restricts them (all
// kinds when empty). Unlike Find it does not choose between several: a caller
// that writes (a point of view, a location) must not guess.
func (v *Visible) Candidates(ref string, kinds ...string) []Entry {
	ok := func(e Entry) bool {
		if len(kinds) == 0 {
			return true
		}
		for _, k := range kinds {
			if e.Kind == k {
				return true
			}
		}
		return false
	}
	if e, hit := v.ByID[ref]; hit {
		if ok(*e) {
			return []Entry{*e}
		}
		return nil
	}
	k := text.FoldTitle(ref)
	if k == "" {
		return nil
	}
	var out []Entry
	for _, e := range v.Entries {
		match := text.FoldTitle(e.BaseName) == k || text.FoldTitle(e.Name) == k
		for _, a := range e.Aliases {
			match = match || text.FoldTitle(a) == k
		}
		if match && ok(e) {
			out = append(out, e)
		}
	}
	return out
}

// AmbiguousError is the conflict a write returns when a name could mean more
// than one entity the caller can read. It names them (id, name, kind) so the
// caller can choose by id.
func AmbiguousError(ref string, cands []Entry) *ops.Error {
	list := make([]map[string]any, len(cands))
	for i, e := range cands {
		list[i] = map[string]any{"id": e.ID, "name": e.Name, "kind": e.Kind}
	}
	return ops.ConflictWith(`"`+ref+`" could mean more than one entity; use the id of the one you mean.`,
		map[string]any{"reason": "ambiguous", "candidates": list})
}

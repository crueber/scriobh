package entities

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// A Compendium can reference others, read-only. This is how it reads them: the
// entities of the Compendiums it references, each marked with where it lives
// and whether the caller could edit it there. Referencing is not access: the
// caller must be a member of each referenced Compendium to see its entities,
// and the rest are left out (the result says some were), so a reference never
// opens a bible to someone who is not on it. Nothing here writes; to change
// such an entity, work in the Compendium it lives in.

func registerReferenced(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "entity.referenced.list", Method: "GET", Path: "/api/v1/compendiums/{id}/referenced-entities",
		Desc: "The entities of the Compendiums this one references, read-only, in sort order, a page at a time. Only entities of Compendiums the caller is a member of are returned (hidden says some were left out); " +
			"being able to read this Compendium does not open the ones it references. Each entity says which Compendium it lives in and whether the caller could edit it there (editable); to change one, use entity.update in that Compendium.",
		Auth: ops.RequireCompendium(compaccess.Reader),
	}, referencedList)
}

// ReferencedListIn lists the entities a Compendium references.
type ReferencedListIn struct {
	ID     string `path:"id" json:"-" desc:"The referencing Compendium's id."`
	Kind   string `query:"kind" desc:"Only one kind." enum:"character,place,item,org,concept"`
	Q      string `query:"q" desc:"Names and aliases containing these characters, ignoring case."`
	Limit  int    `query:"limit" desc:"Page size, 1 to 500; default 100."`
	Offset int    `query:"offset" desc:"Entities to skip."`
}

// CompendiumRef lets the registry authorize by membership.
func (in ReferencedListIn) CompendiumRef() string { return in.ID }

// Validate defaults and checks.
func (in *ReferencedListIn) Validate() error {
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

// ReferencedEntity is an entity of a referenced Compendium.
type ReferencedEntity struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind" enum:"character,place,item,org,concept"`
	Name           string   `json:"name"`
	Summary        string   `json:"summary"`
	Aliases        []string `json:"aliases"`
	CompendiumID   string   `json:"compendium_id" desc:"Where it lives."`
	CompendiumName string   `json:"compendium_name"`
	Editable       bool     `json:"editable" desc:"The caller is an editor of the Compendium it lives in. Never editable through the reference."`
	UpdatedAt      string   `json:"updated_at"`
}

// ReferencedListOut is a page of referenced entities.
type ReferencedListOut struct {
	Items      []ReferencedEntity `json:"items"`
	Total      int                `json:"total"`
	NextOffset int                `json:"next_offset,omitempty"`
	Hidden     bool               `json:"hidden" desc:"True when a referenced Compendium has entities the caller may not see. They are neither counted nor named."`
}

func referencedList(c *ops.Ctx, in ReferencedListIn) (ReferencedListOut, error) {
	if c.P.Pinned() {
		return ReferencedListOut{}, ops.Forbidden("a key restricted to one project or compendium cannot read other Compendiums through a reference")
	}
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ReferencedListOut{}, err
	}
	defer tx.Rollback()
	const from = ` FROM compendium_refs r
		JOIN compendiums c ON c.id = r.target_compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		JOIN entities e ON e.compendium_id = c.id AND e.trashed_at IS NULL
		WHERE r.owner_kind = 'compendium' AND r.owner_id = ?`
	rows, err := tx.QueryContext(c, `SELECT e.id, e.kind, e.name, e.summary, e.updated_at, c.id, c.name`+from+` ORDER BY e.sort_key, e.id`, in.ID)
	if err != nil {
		return ReferencedListOut{}, err
	}
	var all []ReferencedEntity
	for rows.Next() {
		var e ReferencedEntity
		if err := rows.Scan(&e.ID, &e.Kind, &e.Name, &e.Summary, &e.UpdatedAt, &e.CompendiumID, &e.CompendiumName); err != nil {
			rows.Close()
			return ReferencedListOut{}, err
		}
		e.Aliases = []string{}
		all = append(all, e)
	}
	if err := rows.Close(); err != nil {
		return ReferencedListOut{}, err
	}
	roles := map[string]string{}
	checked := map[string]bool{}
	kept := all[:0:0]
	out := ReferencedListOut{Items: []ReferencedEntity{}}
	for _, e := range all {
		if !checked[e.CompendiumID] {
			checked[e.CompendiumID] = true
			if role, ok := compaccess.Role(c, tx, c.P.UserID, e.CompendiumID); ok && compaccess.AtLeast(role, compaccess.Reader) {
				roles[e.CompendiumID] = role
			}
		}
		if roles[e.CompendiumID] == "" {
			out.Hidden = true
			continue
		}
		e.Editable = compaccess.AtLeast(roles[e.CompendiumID], compaccess.Editor)
		kept = append(kept, e)
	}
	arows, err := tx.QueryContext(c, `SELECT a.entity_id, a.alias FROM entity_aliases a JOIN entities e ON e.id = a.entity_id
		JOIN compendiums c ON c.id = e.compendium_id AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		JOIN compendium_refs r ON r.target_compendium_id = c.id AND r.owner_kind = 'compendium' AND r.owner_id = ?
		WHERE e.trashed_at IS NULL ORDER BY a.entity_id, a.alias_key, a.id`, in.ID)
	if err != nil {
		return ReferencedListOut{}, err
	}
	aliases := map[string][]string{}
	for arows.Next() {
		var id, alias string
		if err := arows.Scan(&id, &alias); err != nil {
			arows.Close()
			return ReferencedListOut{}, err
		}
		aliases[id] = append(aliases[id], alias)
	}
	if err := arows.Close(); err != nil {
		return ReferencedListOut{}, err
	}
	k := text.FoldTitle(in.Q)
	var match []ReferencedEntity
	for _, e := range kept {
		if in.Kind != "" && e.Kind != in.Kind {
			continue
		}
		if as := aliases[e.ID]; as != nil {
			e.Aliases = as
		}
		if k != "" {
			hit := strings.Contains(text.FoldTitle(e.Name), k)
			for _, a := range e.Aliases {
				hit = hit || strings.Contains(text.FoldTitle(a), k)
			}
			if !hit {
				continue
			}
		}
		match = append(match, e)
	}
	out.Total = len(match)
	if in.Offset < len(match) {
		end := in.Offset + in.Limit
		if end > len(match) {
			end = len(match)
		}
		out.Items = append(out.Items, match[in.Offset:end]...)
		if end < len(match) {
			out.NextOffset = end
		}
	}
	return out, nil
}

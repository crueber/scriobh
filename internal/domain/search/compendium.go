package search

import (
	"context"
	"html"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
)

// Search inside a Compendium: its entities, by name, alias, summary and body.
// The index is entity_fts, which follows entity_text; the entity ops keep that
// row in step inside the same transaction. Trashed entities are joined out.
// The same rules as the document search apply: what someone types is only ever
// words, every word is quoted and the last is a prefix.

func registerCompendium(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "search.compendium", Method: "GET", Path: "/api/v1/compendiums/{id}/search",
		Desc: "Search a Compendium's entities: names, aliases, summaries and bodies, best matches first (a name or alias match outranks a body match). Needs membership of the Compendium; trashed entities are not searched.",
		Auth: ops.RequireCompendium(ops.ProjectReader)}, queryCompendium)
}

// CompendiumQueryIn is a search of one Compendium.
type CompendiumQueryIn struct {
	ID     string `path:"id" json:"-" desc:"Compendium id."`
	Q      string `query:"q" desc:"Words to look for. Every word must match; the last may be a prefix. Diacritics and case are ignored."`
	Limit  int    `query:"limit" desc:"Results per page: 1 to 50, default 20."`
	Offset int    `query:"offset" desc:"Results to skip, for paging."`
}

// CompendiumRef names the Compendium the search runs in.
func (in CompendiumQueryIn) CompendiumRef() string { return in.ID }

// EntityHit is one matching entity.
type EntityHit struct {
	EntityID    string `json:"entity_id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	MatchedIn   string `json:"matched_in" enum:"name,alias,summary,body" desc:"The most important field that matched: name, then alias, then summary, then body."`
	Snippet     string `json:"snippet" desc:"Plain text around the first match."`
	SnippetHTML string `json:"snippet_html" desc:"The same snippet as HTML: escaped, with each match in <mark>."`
	UpdatedAt   string `json:"updated_at"`
}

// CompendiumQueryOut is a page of results.
type CompendiumQueryOut struct {
	Items      []EntityHit `json:"items"`
	Total      int         `json:"total" desc:"Every matching entity, not just this page."`
	NextOffset int         `json:"next_offset,omitempty" desc:"Offset of the next page; absent on the last page."`
	Degraded   bool        `json:"degraded,omitempty" desc:"True when the index could not be read and a slower substring search answered instead. Run scriobh search rebuild."`
	// Research is the best research matches (one page, no offset); use
	// search.compendium_research to page through them.
	Research      []ResearchHit `json:"research" desc:"The best matching research notes and clippings of this Compendium, best first (the first page; page through them with search.compendium_research)."`
	ResearchTotal int           `json:"research_total" desc:"Every matching research note."`
}

const entityScope = ` t.compendium_id = ? AND e.compendium_id = ? AND e.trashed_at IS NULL`

func queryCompendium(c *ops.Ctx, in CompendiumQueryIn) (CompendiumQueryOut, error) {
	expr, words, err := match(in.Q)
	if err != nil {
		return CompendiumQueryOut{}, err
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if in.Offset < 0 {
		return CompendiumQueryOut{}, ops.Invalid("offset", "The offset cannot be negative.")
	}
	out, err := entityFTS(c, c.DB().Read(), in.ID, expr, limit, in.Offset)
	if err != nil {
		if c.Err() != nil {
			return CompendiumQueryOut{}, c.Err()
		}
		if c.Env.Log != nil {
			c.Env.Log.Warn("entity search index unreadable; answering with a substring search", "err", err)
		}
		out, err = entityLike(c, c.DB().Read(), in.ID, words, limit, in.Offset)
		if err != nil {
			return CompendiumQueryOut{}, err
		}
		out.Degraded = true
	}
	if in.Offset+len(out.Items) < out.Total {
		out.NextOffset = in.Offset + len(out.Items)
	}
	out.Research = []ResearchHit{}
	if rs, rerr := ResearchSearch(c, c.DB().Read(), false, "compendium_id", in.ID, expr, words, limit, 0); rerr == nil {
		out.Research, out.ResearchTotal = rs.Items, rs.Total
		out.Degraded = out.Degraded || rs.Degraded
	}
	return out, nil
}

func entityFTS(ctx context.Context, q queryer, cid, expr string, limit, offset int) (CompendiumQueryOut, error) {
	out := CompendiumQueryOut{Items: []EntityHit{}}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM entity_fts JOIN entity_text t ON t.id = entity_fts.rowid JOIN entities e ON e.id = t.entity_id
		WHERE entity_fts MATCH ? AND`+entityScope, expr, cid, cid).Scan(&out.Total); err != nil {
		return CompendiumQueryOut{}, err
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, e.id, e.kind, e.name, e.updated_at FROM entity_fts
		JOIN entity_text t ON t.id = entity_fts.rowid JOIN entities e ON e.id = t.entity_id
		WHERE entity_fts MATCH ? AND`+entityScope+`
		ORDER BY bm25(entity_fts, 10.0, 8.0, 4.0, 1.0), e.sort_key, e.id LIMIT ? OFFSET ?`, expr, cid, cid, limit, offset)
	if err != nil {
		return CompendiumQueryOut{}, err
	}
	var ids []any
	byRow := map[int64]*EntityHit{}
	for rows.Next() {
		var rowid int64
		var h EntityHit
		if err := rows.Scan(&rowid, &h.EntityID, &h.Kind, &h.Name, &h.UpdatedAt); err != nil {
			rows.Close()
			return CompendiumQueryOut{}, err
		}
		out.Items = append(out.Items, h)
		ids = append(ids, rowid)
	}
	if err := rows.Close(); err != nil {
		return CompendiumQueryOut{}, err
	}
	for i := range out.Items {
		byRow[ids[i].(int64)] = &out.Items[i]
	}
	m := newMarks()
	srows, err := q.QueryContext(ctx, `SELECT entity_fts.rowid,
			snippet(entity_fts, 0, ?1, ?2, '…', 12), snippet(entity_fts, 1, ?1, ?2, '…', 12),
			snippet(entity_fts, 2, ?1, ?2, '…', 24), snippet(entity_fts, 3, ?1, ?2, '…', 24)
		FROM entity_fts WHERE entity_fts MATCH ?3 AND entity_fts.rowid IN (`+placeholders(len(ids), 4)+`)`,
		append([]any{m.start, m.end, expr}, ids...)...)
	if err != nil {
		return CompendiumQueryOut{}, err
	}
	defer srows.Close()
	for srows.Next() {
		var rowid int64
		var name, aliases, summary, body string
		if err := srows.Scan(&rowid, &name, &aliases, &summary, &body); err != nil {
			return CompendiumQueryOut{}, err
		}
		h := byRow[rowid]
		if h == nil {
			continue
		}
		pick := body
		switch {
		case m.has(name):
			h.MatchedIn, pick = "name", name
		case m.has(aliases):
			h.MatchedIn, pick = "alias", aliases
		case m.has(summary):
			h.MatchedIn, pick = "summary", summary
		default:
			h.MatchedIn = "body"
		}
		h.Snippet, h.SnippetHTML = m.render(pick)
	}
	return out, srows.Err()
}

func entityLike(ctx context.Context, q queryer, cid string, words []string, limit, offset int) (CompendiumQueryOut, error) {
	out := CompendiumQueryOut{Items: []EntityHit{}}
	var conds []string
	args := []any{cid, cid}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, w := range words {
		conds = append(conds, `(t.name LIKE ? ESCAPE '\' OR t.aliases LIKE ? ESCAPE '\' OR t.summary LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')`)
		p := "%" + esc.Replace(w) + "%"
		args = append(args, p, p, p, p)
	}
	where := ` WHERE` + entityScope + ` AND ` + strings.Join(conds, " AND ")
	from := ` FROM entity_text t JOIN entities e ON e.id = t.entity_id`
	if err := q.QueryRowContext(ctx, `SELECT count(*)`+from+where, args...).Scan(&out.Total); err != nil {
		return CompendiumQueryOut{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.kind, e.name, e.updated_at, t.name, t.aliases, t.summary, t.body`+from+where+
		` ORDER BY e.sort_key, e.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return CompendiumQueryOut{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var h EntityHit
		var name, aliases, summary, body string
		if err := rows.Scan(&h.EntityID, &h.Kind, &h.Name, &h.UpdatedAt, &name, &aliases, &summary, &body); err != nil {
			return CompendiumQueryOut{}, err
		}
		hit := Hit{}
		switch {
		case anyFold(name, words):
			h.MatchedIn = "name"
		case anyFold(aliases, words):
			h.MatchedIn = "alias"
		case anyFold(summary, words):
			h.MatchedIn = "summary"
		default:
			h.MatchedIn = "body"
		}
		fillFallback(&hit, words, name, summary, body)
		h.Snippet, h.SnippetHTML = hit.Snippet, hit.SnippetHTML
		if h.Snippet == "" {
			h.Snippet = html.UnescapeString(name)
			h.SnippetHTML = html.EscapeString(name)
		}
		out.Items = append(out.Items, h)
	}
	return out, rows.Err()
}

func anyFold(text string, words []string) bool {
	for _, w := range words {
		if _, _, ok := foldIndex(text, w); ok {
			return true
		}
	}
	return false
}

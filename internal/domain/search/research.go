package search

import (
	"context"
	"database/sql"
	"html"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Research search: notes and clippings of one owner, a project (project-local
// research) or a Compendium (shared research). The index is research_fts, which
// follows research_text; the research ops keep that row in step inside the same
// transaction. Every query is scoped to one owner, so a project's search never
// returns a Compendium's notes and one Compendium's never returns another's.
// The same word-only query rules as every other search apply.

func registerResearch(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "search.research", Method: "GET", Path: "/api/v1/projects/{id}/search/research",
		Desc: "Search a project's own research notes and clippings (not the manuscript, and never a Compendium's research). Best matches first; trashed notes are not searched.",
		Auth: ops.RequireProject(ops.ProjectReader)},
		func(c *ops.Ctx, in ResearchQueryIn) (ResearchQueryOut, error) {
			return researchQuery(c, "project_id", in)
		})
	ops.Register(r, ops.Op{Name: "search.compendium_research", Method: "GET", Path: "/api/v1/compendiums/{id}/search/research",
		Desc: "Search a Compendium's research notes and clippings, paged. Needs membership of the Compendium. (search.compendium also returns the best research matches beside the entities.)",
		Auth: ops.RequireCompendium(ops.ProjectReader)},
		func(c *ops.Ctx, in ResearchQueryIn) (ResearchQueryOut, error) {
			return researchQuery(c, "compendium_id", in)
		})
}

// ResearchQueryIn is a research search.
type ResearchQueryIn struct {
	ID     string `path:"id" json:"-" desc:"Project id (search.research) or Compendium id (search.compendium_research)."`
	Q      string `query:"q" desc:"Words to look for. Every word must match; the last may be a prefix. Diacritics and case are ignored."`
	Limit  int    `query:"limit" desc:"Results per page: 1 to 50, default 20."`
	Offset int    `query:"offset" desc:"Results to skip, for paging."`
}

func (in ResearchQueryIn) ProjectRef() string    { return in.ID }
func (in ResearchQueryIn) CompendiumRef() string { return in.ID }

// ResearchHit is one matching note.
type ResearchHit struct {
	NoteID      string `json:"note_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title"`
	MatchedIn   string `json:"matched_in" enum:"title,source,body" desc:"The most important field that matched: title, then source address, then body."`
	Snippet     string `json:"snippet" desc:"Plain text around the first match."`
	SnippetHTML string `json:"snippet_html" desc:"The same snippet as HTML: escaped, with each match in <mark>."`
	UpdatedAt   string `json:"updated_at"`
}

// ResearchQueryOut is a page of results.
type ResearchQueryOut struct {
	Items      []ResearchHit `json:"items"`
	Total      int           `json:"total"`
	NextOffset int           `json:"next_offset,omitempty"`
	Degraded   bool          `json:"degraded,omitempty" desc:"True when the index could not be read and a slower substring search answered instead. Run scriobh search rebuild."`
}

func researchQuery(c *ops.Ctx, col string, in ResearchQueryIn) (ResearchQueryOut, error) {
	expr, words, err := match(in.Q)
	if err != nil {
		return ResearchQueryOut{}, err
	}
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}
	if in.Offset < 0 {
		return ResearchQueryOut{}, ops.Invalid("offset", "The offset cannot be negative.")
	}
	return ResearchSearch(c, c.DB().Read(), c.Env.Log != nil, col, in.ID, expr, words, limit, in.Offset)
}

// ResearchSearch runs a research search for one owner; col is project_id or
// compendium_id. search.compendium calls it too.
func ResearchSearch(ctx context.Context, q queryer, _ bool, col, owner, expr string, words []string, limit, offset int) (ResearchQueryOut, error) {
	out, err := researchFTS(ctx, q, col, owner, expr, limit, offset)
	if err != nil {
		if ctx.Err() != nil {
			return ResearchQueryOut{}, ctx.Err()
		}
		out, err = researchLike(ctx, q, col, owner, words, limit, offset)
		if err != nil {
			return ResearchQueryOut{}, err
		}
		out.Degraded = true
	}
	if offset+len(out.Items) < out.Total {
		out.NextOffset = offset + len(out.Items)
	}
	return out, nil
}

func researchScope(col string) string {
	return ` t.` + col + ` = ? AND n.` + col + ` = ? AND n.deleted_at IS NULL`
}

func researchFTS(ctx context.Context, q queryer, col, owner, expr string, limit, offset int) (ResearchQueryOut, error) {
	out := ResearchQueryOut{Items: []ResearchHit{}}
	scope := researchScope(col)
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM research_fts JOIN research_text t ON t.id = research_fts.rowid JOIN research_notes n ON n.id = t.note_id
		WHERE research_fts MATCH ? AND`+scope, expr, owner, owner).Scan(&out.Total); err != nil {
		return ResearchQueryOut{}, err
	}
	if out.Total == 0 || offset >= out.Total {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, n.id, n.kind, n.title, n.updated_at FROM research_fts
		JOIN research_text t ON t.id = research_fts.rowid JOIN research_notes n ON n.id = t.note_id
		WHERE research_fts MATCH ? AND`+scope+`
		ORDER BY bm25(research_fts, 10.0, 4.0, 1.0), n.id LIMIT ? OFFSET ?`, expr, owner, owner, limit, offset)
	if err != nil {
		return ResearchQueryOut{}, err
	}
	var rowids []any
	for rows.Next() {
		var rowid int64
		var h ResearchHit
		if err := rows.Scan(&rowid, &h.NoteID, &h.Kind, &h.Title, &h.UpdatedAt); err != nil {
			rows.Close()
			return ResearchQueryOut{}, err
		}
		out.Items = append(out.Items, h)
		rowids = append(rowids, rowid)
	}
	if err := rows.Close(); err != nil {
		return ResearchQueryOut{}, err
	}
	byRow := map[int64]*ResearchHit{}
	for i := range out.Items {
		byRow[rowids[i].(int64)] = &out.Items[i]
	}
	m := newMarks()
	srows, err := q.QueryContext(ctx, `SELECT research_fts.rowid, snippet(research_fts, 0, ?1, ?2, '…', 12), snippet(research_fts, 1, ?1, ?2, '…', 12),
			snippet(research_fts, 2, ?1, ?2, '…', 24)
		FROM research_fts WHERE research_fts MATCH ?3 AND research_fts.rowid IN (`+placeholders(len(rowids), 4)+`)`,
		append([]any{m.start, m.end, expr}, rowids...)...)
	if err != nil {
		return ResearchQueryOut{}, err
	}
	defer srows.Close()
	for srows.Next() {
		var rowid int64
		var title, source, body string
		if err := srows.Scan(&rowid, &title, &source, &body); err != nil {
			return ResearchQueryOut{}, err
		}
		h := byRow[rowid]
		if h == nil {
			continue
		}
		pick := body
		switch {
		case m.has(title):
			h.MatchedIn, pick = "title", title
		case m.has(source):
			h.MatchedIn, pick = "source", source
		default:
			h.MatchedIn = "body"
		}
		h.Snippet, h.SnippetHTML = m.render(pick)
	}
	return out, srows.Err()
}

func researchLike(ctx context.Context, q queryer, col, owner string, words []string, limit, offset int) (ResearchQueryOut, error) {
	out := ResearchQueryOut{Items: []ResearchHit{}}
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	args := []any{owner, owner}
	var conds []string
	for _, w := range words {
		conds = append(conds, `(t.title LIKE ? ESCAPE '\' OR t.source LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')`)
		p := "%" + esc.Replace(w) + "%"
		args = append(args, p, p, p)
	}
	where := ` WHERE` + researchScope(col) + ` AND ` + strings.Join(conds, " AND ")
	from := ` FROM research_text t JOIN research_notes n ON n.id = t.note_id`
	if err := q.QueryRowContext(ctx, `SELECT count(*)`+from+where, args...).Scan(&out.Total); err != nil {
		return ResearchQueryOut{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT n.id, n.kind, n.title, n.updated_at, t.title, t.source, t.body`+from+where+` ORDER BY n.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return ResearchQueryOut{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var h ResearchHit
		var title, source, body string
		if err := rows.Scan(&h.NoteID, &h.Kind, &h.Title, &h.UpdatedAt, &title, &source, &body); err != nil {
			return ResearchQueryOut{}, err
		}
		switch {
		case anyFold(title, words):
			h.MatchedIn = "title"
		case anyFold(source, words):
			h.MatchedIn = "source"
		default:
			h.MatchedIn = "body"
		}
		hit := Hit{}
		fillFallback(&hit, words, title, "", body)
		h.Snippet, h.SnippetHTML = hit.Snippet, hit.SnippetHTML
		if h.Snippet == "" {
			h.Snippet, h.SnippetHTML = html.UnescapeString(title), html.EscapeString(title)
		}
		out.Items = append(out.Items, h)
	}
	return out, rows.Err()
}

// rebuildResearch re-derives research_text from the notes and rebuilds
// research_fts, as the document and entity rebuilds do.
func rebuildResearch(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'research_text'`)
	if err != nil {
		return err
	}
	var names, ddl []string
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			rows.Close()
			return err
		}
		names, ddl = append(names, n), append(ddl, s)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, n := range names {
		if _, err := tx.Exec(`DROP TRIGGER ` + quoteIdent(n)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM research_text`); err != nil {
		return err
	}
	nrows, err := tx.QueryContext(ctx, `SELECT id, project_id, compendium_id, title, source_url, body, quote FROM research_notes ORDER BY id`)
	if err != nil {
		return err
	}
	type row struct {
		id, title, source, body, quote string
		project, compendium            sql.NullString
	}
	var got []row
	for nrows.Next() {
		var r row
		if err := nrows.Scan(&r.id, &r.project, &r.compendium, &r.title, &r.source, &r.body, &r.quote); err != nil {
			nrows.Close()
			return err
		}
		got = append(got, r)
	}
	if err := nrows.Close(); err != nil {
		return err
	}
	for _, r := range got {
		plain := text.SearchBody(r.body)
		if r.quote != "" {
			plain += "\n" + text.SearchBody(r.quote)
		}
		if _, err := tx.Exec(`INSERT INTO research_text(note_id, project_id, compendium_id, title, source, body) VALUES (?,?,?,?,?,?)`,
			r.id, r.project, r.compendium, r.title, r.source, plain); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO research_fts(research_fts) VALUES('rebuild')`); err != nil {
		return err
	}
	for _, s := range ddl {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

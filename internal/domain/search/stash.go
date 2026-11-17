package search

import (
	"context"
	"database/sql"
	"html"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// The stash has its own index (stash_text and stash_fts, migration 1420), apart
// from the manuscript's: a search of the manuscript never finds stashed text,
// and a stash search only ever sees one project's items.

// MatchExpr turns what someone typed into an FTS5 MATCH expression and the
// words it was made of (for the substring fallback), or reports why it cannot.
func MatchExpr(q string) (expr string, words []string, err error) { return match(q) }

// StashHit is one matching stash item.
type StashHit struct {
	ItemID      string
	MatchedIn   string // title, tags or body
	Snippet     string
	SnippetHTML string
}

// StashFilter narrows a stash search.
type StashFilter struct {
	Kind    string
	Trashed bool
}

// StashSearch searches one project's stash, best match first. degraded is true
// when the index could not be read and a substring search answered instead.
func StashSearch(ctx context.Context, q queryer, projectID, expr string, words []string, f StashFilter, limit, offset int) (hits []StashHit, total int, degraded bool, err error) {
	hits, total, err = stashFTS(ctx, q, projectID, expr, f, limit, offset)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, false, ctx.Err()
		}
		hits, total, err = stashLike(ctx, q, projectID, words, f, limit, offset)
		return hits, total, true, err
	}
	return hits, total, false, nil
}

func stashScope(f StashFilter) string {
	s := ` t.project_id = ? AND n.project_id = ?`
	if f.Trashed {
		s += ` AND n.deleted_at IS NOT NULL`
	} else {
		s += ` AND n.deleted_at IS NULL`
	}
	if f.Kind != "" {
		s += ` AND n.kind = '` + strings.ReplaceAll(f.Kind, "'", "") + `'`
	}
	return s
}

func stashFTS(ctx context.Context, q queryer, project, expr string, f StashFilter, limit, offset int) ([]StashHit, int, error) {
	scope := stashScope(f)
	var total int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM stash_fts JOIN stash_text t ON t.id = stash_fts.rowid JOIN stash_items n ON n.id = t.item_id
		WHERE stash_fts MATCH ? AND`+scope, expr, project, project).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 || offset >= total {
		return []StashHit{}, total, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT t.id, n.id FROM stash_fts
		JOIN stash_text t ON t.id = stash_fts.rowid JOIN stash_items n ON n.id = t.item_id
		WHERE stash_fts MATCH ? AND`+scope+`
		ORDER BY bm25(stash_fts, 10.0, 4.0, 1.0), n.id LIMIT ? OFFSET ?`, expr, project, project, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	var hits []StashHit
	var rowids []any
	for rows.Next() {
		var rowid int64
		var h StashHit
		if err := rows.Scan(&rowid, &h.ItemID); err != nil {
			rows.Close()
			return nil, 0, err
		}
		hits = append(hits, h)
		rowids = append(rowids, rowid)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	byRow := map[int64]*StashHit{}
	for i := range hits {
		byRow[rowids[i].(int64)] = &hits[i]
	}
	m := newMarks()
	srows, err := q.QueryContext(ctx, `SELECT stash_fts.rowid, snippet(stash_fts, 0, ?1, ?2, '…', 12), snippet(stash_fts, 1, ?1, ?2, '…', 12),
			snippet(stash_fts, 2, ?1, ?2, '…', 24)
		FROM stash_fts WHERE stash_fts MATCH ?3 AND stash_fts.rowid IN (`+placeholders(len(rowids), 4)+`)`,
		append([]any{m.start, m.end, expr}, rowids...)...)
	if err != nil {
		return nil, 0, err
	}
	defer srows.Close()
	for srows.Next() {
		var rowid int64
		var title, tags, body string
		if err := srows.Scan(&rowid, &title, &tags, &body); err != nil {
			return nil, 0, err
		}
		h := byRow[rowid]
		if h == nil {
			continue
		}
		pick := body
		switch {
		case m.has(title):
			h.MatchedIn, pick = "title", title
		case m.has(tags):
			h.MatchedIn, pick = "tags", tags
		default:
			h.MatchedIn = "body"
		}
		h.Snippet, h.SnippetHTML = m.render(pick)
	}
	return hits, total, srows.Err()
}

func stashLike(ctx context.Context, q queryer, project string, words []string, f StashFilter, limit, offset int) ([]StashHit, int, error) {
	esc := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	args := []any{project, project}
	var conds []string
	for _, w := range words {
		conds = append(conds, `(t.title LIKE ? ESCAPE '\' OR t.tags LIKE ? ESCAPE '\' OR t.body LIKE ? ESCAPE '\')`)
		p := "%" + esc.Replace(w) + "%"
		args = append(args, p, p, p)
	}
	where := ` WHERE` + stashScope(f) + ` AND ` + strings.Join(conds, " AND ")
	from := ` FROM stash_text t JOIN stash_items n ON n.id = t.item_id`
	var total int
	if err := q.QueryRowContext(ctx, `SELECT count(*)`+from+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := q.QueryContext(ctx, `SELECT n.id, t.title, t.tags, t.body`+from+where+` ORDER BY n.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	hits := []StashHit{}
	for rows.Next() {
		var h StashHit
		var title, tags, body string
		if err := rows.Scan(&h.ItemID, &title, &tags, &body); err != nil {
			return nil, 0, err
		}
		switch {
		case anyFold(title, words):
			h.MatchedIn = "title"
		case anyFold(tags, words):
			h.MatchedIn = "tags"
		default:
			h.MatchedIn = "body"
		}
		hit := Hit{}
		fillFallback(&hit, words, title, "", body)
		h.Snippet, h.SnippetHTML = hit.Snippet, hit.SnippetHTML
		if h.Snippet == "" {
			h.Snippet, h.SnippetHTML = html.UnescapeString(title), html.EscapeString(title)
		}
		hits = append(hits, h)
	}
	return hits, total, rows.Err()
}

// rebuildStash re-derives stash_text from the items and rebuilds stash_fts, as
// the research rebuild does.
func rebuildStash(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'stash_text'`)
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
	if len(names) == 0 {
		return nil // an older database without the stash index
	}
	for _, n := range names {
		if _, err := tx.Exec(`DROP TRIGGER ` + quoteIdent(n)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM stash_text`); err != nil {
		return err
	}
	irows, err := tx.QueryContext(ctx, `SELECT id, project_id, title, tags, content FROM stash_items ORDER BY id`)
	if err != nil {
		return err
	}
	type row struct{ id, project, title, tags, body string }
	var got []row
	for irows.Next() {
		var r row
		if err := irows.Scan(&r.id, &r.project, &r.title, &r.tags, &r.body); err != nil {
			irows.Close()
			return err
		}
		got = append(got, r)
	}
	if err := irows.Close(); err != nil {
		return err
	}
	for _, r := range got {
		if _, err := tx.Exec(`INSERT INTO stash_text(item_id, project_id, title, tags, body) VALUES (?,?,?,?,?)`,
			r.id, r.project, r.title, StashTagsText(r.tags), text.SearchBody(r.body)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO stash_fts(stash_fts) VALUES('rebuild')`); err != nil {
		return err
	}
	for _, s := range ddl {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

// StashTagsText turns the stored JSON array of tags into the words the index
// holds (one per tag, separated by spaces).
func StashTagsText(stored string) string {
	stored = strings.TrimSpace(stored)
	if stored == "" || stored == "[]" {
		return ""
	}
	var b strings.Builder
	in := false
	for _, r := range stored {
		switch {
		case r == '"':
			in = !in
			if !in {
				b.WriteByte(' ')
			}
		case in:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

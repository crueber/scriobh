// Package links keeps the wiki-link graph: which document links to which, as
// `[[Title]]` or `[[Title|text]]` in the body, and the reverse (backlinks).
//
// # Rules
//
//   - A link is read from the body by text.WikiLinks, the same parser the word
//     counter uses. Links inside code, source blocks (raw HTML, tables, front
//     matter) and %%private notes%% are not links: private text never becomes
//     a backlink snippet or an auto-created page.
//   - Titles are matched within the project, ignoring case and runs of
//     whitespace (Unicode lower case, done in Go; see text.FoldTitle). Trashed
//     documents are never targets.
//   - A title that more than one live document has is ambiguous: the link
//     resolves to the first of them in reading order and is flagged. Reordering
//     the outline, renaming, creating, trashing or restoring a document
//     re-resolves the links it can affect, inside the same transaction.
//   - Links are rows, one per distinct link text per document and kind, with
//     the target id NULL while unresolved. The write path calls Sync for the
//     document it saves and Reresolve for the titles it changes.
//
// Anything that writes a document body or title outside the docs ops (an
// importer, a repair job) must call Sync and Reresolve, or run Rebuild after.
package links

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/text"
)

// Kind of link this package maintains.
const KindWiki = "wiki"

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// resolver matches link titles to live documents of one project. It reads the
// project's tree once, on first use.
type resolver struct {
	q       querier
	project string
	loaded  bool
	nodes   []docref.Node
	byKey   map[string][]string
	rank    map[string]int // reading-order index, built only when a title is ambiguous
	memo    map[string]resolved
}

type resolved struct {
	id        string
	ambiguous bool
}

func newResolver(q querier, projectID string) *resolver {
	return &resolver{q: q, project: projectID}
}

func (r *resolver) load() error {
	if r.loaded {
		return nil
	}
	rows, err := r.q.Query(`SELECT id, COALESCE(parent_id,''), kind, title, position FROM documents WHERE project_id = ? AND deleted_at IS NULL`, r.project)
	if err != nil {
		return err
	}
	defer rows.Close()
	r.byKey = map[string][]string{}
	for rows.Next() {
		var n docref.Node
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position); err != nil {
			return err
		}
		r.nodes = append(r.nodes, n)
		if k := text.FoldTitle(n.Title); k != "" {
			r.byKey[k] = append(r.byKey[k], n.ID)
		}
	}
	r.loaded = true
	return rows.Err()
}

// resolve returns the document a folded title points at ("" when none) and
// whether more than one live document has that title.
func (r *resolver) resolve(key string) (id string, ambiguous bool, err error) {
	if err := r.load(); err != nil {
		return "", false, err
	}
	if m, ok := r.memo[key]; ok {
		return m.id, m.ambiguous, nil
	}
	id, ambiguous = r.pick(key)
	if r.memo == nil {
		r.memo = map[string]resolved{}
	}
	r.memo[key] = resolved{id, ambiguous}
	return id, ambiguous, nil
}

func (r *resolver) pick(key string) (id string, ambiguous bool) {
	c := r.byKey[key]
	switch len(c) {
	case 0:
		return "", false
	case 1:
		return c[0], false
	}
	if r.rank == nil {
		r.rank = map[string]int{}
		for i, id := range docref.Order(r.nodes) {
			r.rank[id] = i
		}
	}
	best := c[0]
	for _, x := range c[1:] {
		if r.rank[x] < r.rank[best] {
			best = x
		}
	}
	return best, true
}

// Reresolve re-matches every link in the project whose target title folds to
// one of titles, after a document with that title was created, renamed,
// trashed, restored or split off. It does nothing, and reads no tree, when no
// link uses any of them.
func Reresolve(tx *sql.Tx, projectID string, titles ...string) error {
	keys := map[string]bool{}
	for _, t := range titles {
		if k := text.FoldTitle(t); k != "" {
			keys[k] = true
		}
	}
	return reresolveKeys(tx, projectID, keys)
}

// ReresolveAmbiguous re-resolves the project's ambiguous links. Moving or
// reordering documents changes which of two same-titled documents comes first.
func ReresolveAmbiguous(tx *sql.Tx, projectID string) error {
	rows, err := tx.Query(`SELECT DISTINCT l.target_key FROM document_links l JOIN documents f ON f.id = l.from_id
		WHERE f.project_id = ? AND l.ambiguous = 1`, projectID)
	if err != nil {
		return err
	}
	keys := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return err
		}
		keys[k] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	return reresolveKeys(tx, projectID, keys)
}

func reresolveKeys(tx *sql.Tx, projectID string, keys map[string]bool) error {
	if len(keys) == 0 {
		return nil
	}
	list := make([]string, 0, len(keys))
	for k := range keys {
		list = append(list, k)
	}
	var used []string
	for len(list) > 0 { // chunked to stay under SQLite's variable limit
		n := len(list)
		if n > 400 {
			n = 400
		}
		chunk := list[:n]
		list = list[n:]
		args := make([]any, 0, n+1)
		args = append(args, projectID)
		for _, k := range chunk {
			args = append(args, k)
		}
		rows, err := tx.Query(`SELECT DISTINCT l.target_key FROM document_links l JOIN documents f ON f.id = l.from_id
			WHERE f.project_id = ? AND l.target_key IN (`+strings.TrimSuffix(strings.Repeat("?,", n), ",")+`)`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			used = append(used, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	if len(used) == 0 {
		return nil
	}
	res := newResolver(tx, projectID)
	for _, k := range used {
		to, amb, err := res.resolve(k)
		if err != nil {
			return err
		}
		var toVal any
		if to != "" {
			toVal = to
		}
		a := 0
		if amb {
			a = 1
		}
		if _, err := tx.Exec(`UPDATE document_links SET to_id = ?, ambiguous = ? WHERE target_key = ?
			AND from_id IN (SELECT id FROM documents WHERE project_id = ?)
			AND (COALESCE(to_id,'') != ? OR ambiguous != ?)`, toVal, a, k, projectID, to, a); err != nil {
			return err
		}
	}
	return nil
}

// Target is the live document a title resolves to.
type Target struct {
	ID         string
	Title      string
	Kind       string
	Ambiguous  bool
	Candidates int
}

// Lookup resolves a title within a project. found is false when no live
// document has the title.
func Lookup(q querier, projectID, title string) (t Target, found bool, err error) {
	key := text.FoldTitle(title)
	if key == "" {
		return Target{}, false, nil
	}
	res := newResolver(q, projectID)
	id, amb, err := res.resolve(key)
	if err != nil || id == "" {
		return Target{}, false, err
	}
	for _, n := range res.nodes {
		if n.ID == id {
			t = Target{ID: id, Title: n.Title, Kind: n.Kind, Ambiguous: amb, Candidates: len(res.byKey[key])}
		}
	}
	return t, true, nil
}

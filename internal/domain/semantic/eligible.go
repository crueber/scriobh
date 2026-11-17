package semantic

import (
	"context"
	"database/sql"
	"sort"
)

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// docRow is a document as the index sees it: no text.
type docRow struct {
	ID, Parent, Kind, Title, Status, Hash, UpdatedAt string
	AIExclude, InCompile, Trashed                    bool
	Position                                         float64
}

// Reasons a document is not embedded.
const (
	skipNeverSend = "never_send"
	skipNoCompile = "do_not_compile"
	skipNotes     = "notes_and_research"
	skipTrashed   = "trashed"
)

// loadDocs reads every document of a project that belongs to the book itself
// (not a Compendium's research), trashed ones included: the worker needs to
// see a document that went to the trash to delete what it holds.
func loadDocs(ctx context.Context, q queryer, projectID string) (map[string]*docRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, title, status, content_hash, updated_at, ai_exclude, include_in_compile, deleted_at IS NOT NULL, position
		FROM documents WHERE project_id = ? AND compendium_id IS NULL`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*docRow{}
	for rows.Next() {
		var d docRow
		var ex, comp, tr int
		if err := rows.Scan(&d.ID, &d.Parent, &d.Kind, &d.Title, &d.Status, &d.Hash, &d.UpdatedAt, &ex, &comp, &tr, &d.Position); err != nil {
			return nil, err
		}
		d.AIExclude, d.InCompile, d.Trashed = ex == 1, comp == 1, tr == 1
		out[d.ID] = &d
	}
	return out, rows.Err()
}

// skipTree is the reason for a document whose chain of parents loops back on itself or leaves the tree
// unreadable: it fails closed, as every doubt does.
const skipTree = "tree"

// safetyDepth bounds the walk up a chain in SQL; reaching it is a doubt, and a doubt is not embedded.
const safetyDepth = 5000

// skipReason says why a document is not embedded, or "" when it is. A flag on a document applies to everything
// inside it, as a do-not-compile flag does in a compile, however deep: the walk goes to the root, with a record of
// where it has been so a loop ends it. A note or a research document, or anything inside one, is the author's working
// material and is not embedded either.
func skipReason(docs map[string]*docRow, id string) string {
	reason := ""
	seen := map[string]bool{}
	for cur := docs[id]; cur != nil; {
		if seen[cur.ID] {
			return skipTree
		}
		seen[cur.ID] = true
		switch {
		case cur.Trashed:
			return skipTrashed
		case cur.AIExclude:
			return skipNeverSend // the strongest: say it first
		case cur.Kind == "research" || cur.Kind == "note":
			if reason == "" {
				reason = skipNotes
			}
		case !cur.InCompile:
			if reason == "" {
				reason = skipNoCompile
			}
		}
		if cur.Parent == "" {
			break
		}
		cur = docs[cur.Parent]
	}
	return reason
}

// eligible returns the ids of the documents that may be embedded, with the
// reasons for the others, counted (a trashed document is not counted).
func eligible(docs map[string]*docRow) (ids map[string]bool, skipped map[string]int) {
	ids, skipped = map[string]bool{}, map[string]int{}
	for id := range docs {
		switch r := skipReason(docs, id); r {
		case "":
			ids[id] = true
		case skipTrashed:
		default:
			skipped[r]++
		}
	}
	return ids, skipped
}

// readingOrder lists the live documents of the project in outline order.
func readingOrder(docs map[string]*docRow) []string {
	kids := map[string][]*docRow{}
	for _, d := range docs {
		if d.Trashed {
			continue
		}
		p := d.Parent
		if p != "" {
			if pd, ok := docs[p]; !ok || pd.Trashed {
				p = "" // an orphan is read at the top, so nothing hides
			}
		}
		kids[p] = append(kids[p], d)
	}
	for _, k := range kids {
		sort.Slice(k, func(i, j int) bool {
			if k[i].Position != k[j].Position {
				return k[i].Position < k[j].Position
			}
			return k[i].ID < k[j].ID
		})
	}
	var out []string
	seen := map[string]bool{}
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		if depth > 100_000 {
			return
		}
		for _, d := range kids[parent] {
			if seen[d.ID] {
				continue
			}
			seen[d.ID] = true
			out = append(out, d.ID)
			walk(d.ID, depth+1)
		}
	}
	walk("", 0)
	return out
}

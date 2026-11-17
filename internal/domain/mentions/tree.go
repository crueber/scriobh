package mentions

import (
	"context"

	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/links"
)

// tree is a project's live documents, for reading order and breadcrumbs.
type tree struct {
	byID  map[string]docref.Node
	order map[string]int // reading index
}

func loadTree(ctx context.Context, q querier, projectID string) (*tree, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, title, position FROM documents WHERE project_id = ? AND deleted_at IS NULL AND kind <> 'research'`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	t := &tree{byID: map[string]docref.Node{}, order: map[string]int{}}
	var nodes []docref.Node
	for rows.Next() {
		var n docref.Node
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
		t.byID[n.ID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, id := range docref.Order(nodes) {
		t.order[id] = i
	}
	return t, nil
}

// ref names a live document with its breadcrumb.
func (t *tree) ref(id string) (links.Ref, bool) {
	n, ok := t.byID[id]
	if !ok {
		return links.Ref{}, false
	}
	r := links.Ref{ID: n.ID, Title: n.Title, Kind: n.Kind, Breadcrumb: []links.Crumb{}}
	for p, hops := n.ParentID, 0; p != "" && hops < 64; hops++ {
		a, ok := t.byID[p]
		if !ok {
			break
		}
		r.Breadcrumb = append([]links.Crumb{{ID: a.ID, Title: a.Title, Kind: a.Kind}}, r.Breadcrumb...)
		p = a.ParentID
	}
	return r, true
}

// manuscript reports whether a document kind is the book itself: the text a
// first appearance and a "where does she appear" list are about. Notes and
// front and back matter are indexed (the sidebar of a note works) but stay out
// of those lists unless asked for.
func manuscript(kind string) bool {
	return kind == "part" || kind == "chapter" || kind == "scene"
}

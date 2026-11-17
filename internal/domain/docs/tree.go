package docs

import (
	"context"
	"database/sql"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docref"
)

// node is one row of the tree, without content.
type node = docref.Node

// loadNodes reads every live node of a project.
func loadNodes(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, projectID string) ([]node, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, title, position FROM documents
		WHERE project_id = ? AND deleted_at IS NULL`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []node
	for rows.Next() {
		var n node
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// order returns node ids in reading order: depth-first, siblings by position.
// Orphans (a parent that is trashed or missing) are appended so nothing hides.
func order(nodes []node) []string { return docref.Order(nodes) }

// isDescendant reports whether candidate is id itself or inside id's subtree.
func isDescendant(ctx context.Context, tx *sql.Tx, projectID, id, candidate string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT id FROM documents WHERE id = ?1 AND project_id = ?2
			UNION
			SELECT d.id FROM documents d JOIN sub s ON d.parent_id = s.id WHERE d.project_id = ?2)
		SELECT count(*) FROM sub WHERE id = ?3`, id, projectID, candidate).Scan(&n)
	return n > 0, err
}

// placeAmong computes a position for a node among the live children of
// parent, after the sibling `after` ("" means first) or at the end when
// atEnd. It rebalances the siblings (writing their positions) when a gap has
// run out. exclude is a node being moved, which must not count as a sibling.
func placeAmong(ctx context.Context, tx *sql.Tx, projectID, parent, after, exclude string, atEnd bool) (float64, error) {
	q := `SELECT id, position FROM documents WHERE project_id = ? AND deleted_at IS NULL AND COALESCE(parent_id,'') = ? AND id != ? ORDER BY position, id`
	rows, err := tx.QueryContext(ctx, q, projectID, parent, exclude)
	if err != nil {
		return 0, err
	}
	type sib struct {
		id  string
		pos float64
	}
	var sibs []sib
	for rows.Next() {
		var s sib
		if err := rows.Scan(&s.id, &s.pos); err != nil {
			rows.Close()
			return 0, err
		}
		sibs = append(sibs, s)
	}
	rows.Close()
	idx := len(sibs) // insert index
	switch {
	case atEnd:
	case after == "":
		idx = 0
	default:
		found := false
		for i, s := range sibs {
			if s.id == after {
				idx, found = i+1, true
			}
		}
		if !found {
			return 0, errNoSibling
		}
	}
	posAt := func(i int) float64 { return sibs[i].pos }
	var pos float64
	var need bool
	switch {
	case len(sibs) == 0:
		pos = db.PositionGap
	case idx == len(sibs):
		pos, _ = db.PositionBetween(posAt(idx-1), -1)
		pos = posAt(idx-1) + db.PositionGap
	case idx == 0:
		pos, need = db.PositionBetween(-1, posAt(0))
	default:
		pos, need = db.PositionBetween(posAt(idx-1), posAt(idx))
	}
	if need {
		re := db.Rebalanced(len(sibs) + 1)
		j := 0
		for i, s := range sibs {
			if i == idx {
				j++
			}
			if _, err := tx.ExecContext(ctx, `UPDATE documents SET position = ? WHERE id = ?`, re[j], s.id); err != nil {
				return 0, err
			}
			j++
		}
		pos = re[idx]
	}
	return pos, nil
}

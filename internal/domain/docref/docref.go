// Package docref holds the two small things every document-scoped package
// needs and none may import docs for: finding the project that owns a document
// (the authorization resolver) and the reading order of a project's tree.
package docref

import (
	"context"
	"sort"

	"github.com/crueber/scriobh/internal/ops"
)

// Resolve finds the project that owns the document an op input names. Trashed
// documents still resolve, so their history can be read and they can be
// restored. Inputs implement DocID() string.
func Resolve(ctx context.Context, env *ops.Env, in any) (string, bool) {
	ref, ok := in.(interface{ DocID() string })
	if !ok || env == nil || env.DB == nil {
		return "", false
	}
	var pid string
	if err := env.DB.Read().QueryRowContext(ctx, `SELECT project_id FROM documents WHERE id = ?`, ref.DocID()).Scan(&pid); err != nil {
		return "", false
	}
	return pid, true
}

// Via is RequireProjectVia with the document resolver.
func Via(role string) ops.Auth { return ops.RequireProjectVia(role, Resolve) }

// Node is one row of the tree, without content.
type Node struct {
	ID       string
	ParentID string
	Kind     string
	Title    string
	Position float64
}

// maxDepth bounds tree walks; a deeper nesting is read as if flat.
const maxDepth = 64

// Order returns node ids in reading order: depth-first, siblings by position
// then id. Orphans (a parent that is trashed or missing) are appended so
// nothing hides.
func Order(nodes []Node) []string {
	kids := map[string][]Node{}
	seen := map[string]bool{}
	for _, n := range nodes {
		kids[n.ParentID] = append(kids[n.ParentID], n)
	}
	for _, k := range kids {
		sort.Slice(k, func(i, j int) bool {
			if k[i].Position != k[j].Position {
				return k[i].Position < k[j].Position
			}
			return k[i].ID < k[j].ID
		})
	}
	out := make([]string, 0, len(nodes))
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		if depth > maxDepth {
			return
		}
		for _, n := range kids[parent] {
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			out = append(out, n.ID)
			walk(n.ID, depth+1)
		}
	}
	walk("", 0)
	for _, n := range nodes {
		if !seen[n.ID] {
			out = append(out, n.ID)
		}
	}
	return out
}

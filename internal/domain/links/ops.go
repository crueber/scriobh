package links

import (
	"sort"

	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/ops"
)

// Crumb is one ancestor in a breadcrumb.
type Crumb struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// Ref names a live document.
type Ref struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Kind       string  `json:"kind"`
	Breadcrumb []Crumb `json:"breadcrumb" desc:"Ancestors from the top of the tree, nearest last; empty at the top level."`
}

// Link is one outgoing link of a document.
type Link struct {
	Text      string `json:"text" desc:"The target as written between the brackets."`
	Alias     string `json:"alias,omitempty" desc:"The shown text of [[Title|text]]."`
	Count     int    `json:"count" desc:"How many times the link occurs in the body."`
	Resolved  bool   `json:"resolved" desc:"A live document in this project has this title."`
	Ambiguous bool   `json:"ambiguous" desc:"More than one live document has this title; the link goes to the first in reading order."`
	Snippet   string `json:"snippet,omitempty" desc:"Plain text around the first occurrence."`
	Target    *Ref   `json:"target,omitempty" desc:"The document it resolves to; absent when unresolved."`
}

// LinksIn names a document.
type LinksIn struct {
	ID string `path:"id" json:"-" desc:"Document id."`
}

// DocID lets the registry find the owning project.
func (in LinksIn) DocID() string { return in.ID }

// LinksOut lists a document's outgoing links in the order they first appear.
type LinksOut struct {
	Items []Link `json:"items"`
}

// BacklinksIn asks who links to a document.
type BacklinksIn struct {
	ID     string `path:"id" json:"-" desc:"Document id."`
	Limit  int    `query:"limit" desc:"Documents per page, default 100, at most 200."`
	Offset int    `query:"offset" desc:"Documents to skip."`
}

// DocID lets the registry find the owning project.
func (in BacklinksIn) DocID() string { return in.ID }

// Backlink is one document that links here.
type Backlink struct {
	From      Ref    `json:"from"`
	Text      string `json:"text" desc:"The link text as written in the linking document."`
	Alias     string `json:"alias,omitempty"`
	Count     int    `json:"count" desc:"Links to this document in the linking document."`
	Ambiguous bool   `json:"ambiguous"`
	Snippet   string `json:"snippet" desc:"Plain text around the link, private notes left out."`
}

// BacklinksOut is a page of backlinks in reading order.
type BacklinksOut struct {
	Items []Backlink `json:"items"`
	Total int        `json:"total"`
}

// Register adds the read ops of the link graph. Writing links happens inside
// the document write path; doc.resolve_link, which can create a document,
// is registered by docs.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "doc.links", Method: "GET", Path: "/api/v1/docs/{id}/links",
		Desc: "List the wiki links in a document, each with whether it resolves to a document and where.", Auth: docref.Via(ops.ProjectReader)}, outgoing)
	ops.Register(r, ops.Op{Name: "doc.backlinks", Method: "GET", Path: "/api/v1/docs/{id}/backlinks",
		Desc: "List the documents that link to this one, in reading order, with a short context snippet and breadcrumb. Only live documents of the same project appear.", Auth: docref.Via(ops.ProjectReader)}, backlinks)
}

// tree is a project's live documents for breadcrumbs and reading order.
type tree struct {
	byID  map[string]docref.Node
	order map[string]int
}

func loadTree(q querier, projectID string) (*tree, error) {
	rows, err := q.Query(`SELECT id, COALESCE(parent_id,''), kind, title, position FROM documents WHERE project_id = ? AND deleted_at IS NULL`, projectID)
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

func (t *tree) ref(id string) (Ref, bool) {
	n, ok := t.byID[id]
	if !ok {
		return Ref{}, false
	}
	r := Ref{ID: n.ID, Title: n.Title, Kind: n.Kind, Breadcrumb: []Crumb{}}
	for p, hops := n.ParentID, 0; p != "" && hops < 64; hops++ {
		a, ok := t.byID[p]
		if !ok {
			break
		}
		r.Breadcrumb = append([]Crumb{{ID: a.ID, Title: a.Title, Kind: a.Kind}}, r.Breadcrumb...)
		p = a.ParentID
	}
	return r, true
}

func outgoing(c *ops.Ctx, in LinksIn) (LinksOut, error) {
	var projectID string
	if err := c.DB().Read().QueryRowContext(c, `SELECT project_id FROM documents WHERE id = ?`, in.ID).Scan(&projectID); err != nil {
		return LinksOut{}, ops.NotFound("document")
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT link_text, alias, occurrences, ambiguous, snippet, COALESCE(to_id,'') FROM document_links
		WHERE from_id = ? AND kind = ? ORDER BY ord, id`, in.ID, KindWiki)
	if err != nil {
		return LinksOut{}, err
	}
	defer rows.Close()
	out := LinksOut{Items: []Link{}}
	var targets []string
	for rows.Next() {
		var l Link
		var amb int
		var to string
		if err := rows.Scan(&l.Text, &l.Alias, &l.Count, &amb, &l.Snippet, &to); err != nil {
			return LinksOut{}, err
		}
		l.Ambiguous = amb == 1
		out.Items = append(out.Items, l)
		targets = append(targets, to)
	}
	if err := rows.Err(); err != nil {
		return LinksOut{}, err
	}
	rows.Close()
	needTree := false
	for _, to := range targets {
		if to != "" {
			needTree = true
		}
	}
	if !needTree {
		return out, nil
	}
	t, err := loadTree(c.DB().Read(), projectID)
	if err != nil {
		return LinksOut{}, err
	}
	for i, to := range targets {
		if to == "" {
			continue
		}
		if r, ok := t.ref(to); ok { // a trashed target reads as unresolved
			out.Items[i].Resolved = true
			out.Items[i].Target = &r
		} else {
			out.Items[i].Ambiguous = false
		}
	}
	return out, nil
}

func backlinks(c *ops.Ctx, in BacklinksIn) (BacklinksOut, error) {
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = 100
	case limit > 200:
		limit = 200
	}
	if in.Offset < 0 {
		return BacklinksOut{}, ops.Invalid("offset", "The offset cannot be negative.")
	}
	var projectID string
	if err := c.DB().Read().QueryRowContext(c, `SELECT project_id FROM documents WHERE id = ?`, in.ID).Scan(&projectID); err != nil {
		return BacklinksOut{}, ops.NotFound("document")
	}
	// Both ends are joined back to the project: a link row can never surface a
	// document of another project, a trashed one, or the document itself.
	rows, err := c.DB().Read().QueryContext(c, `SELECT l.from_id, l.link_text, l.alias, l.occurrences, l.ambiguous, l.snippet, l.ord
		FROM document_links l
		JOIN documents f ON f.id = l.from_id AND f.project_id = ?1 AND f.deleted_at IS NULL
		JOIN documents t ON t.id = l.to_id AND t.project_id = ?1 AND t.deleted_at IS NULL
		WHERE l.to_id = ?2 AND l.kind = ?3 AND l.from_id != l.to_id
		ORDER BY l.from_id, l.ord`, projectID, in.ID, KindWiki)
	if err != nil {
		return BacklinksOut{}, err
	}
	defer rows.Close()
	byFrom := map[string]*Backlink{}
	var order []string
	for rows.Next() {
		var from string
		var b Backlink
		var amb, ord int
		if err := rows.Scan(&from, &b.Text, &b.Alias, &b.Count, &amb, &b.Snippet, &ord); err != nil {
			return BacklinksOut{}, err
		}
		if cur, ok := byFrom[from]; ok {
			cur.Count += b.Count // the first link text per document speaks for it
			continue
		}
		b.Ambiguous = amb == 1
		byFrom[from] = &b
		order = append(order, from)
	}
	if err := rows.Err(); err != nil {
		return BacklinksOut{}, err
	}
	rows.Close()
	out := BacklinksOut{Items: []Backlink{}, Total: len(order)}
	if len(order) == 0 {
		return out, nil
	}
	t, err := loadTree(c.DB().Read(), projectID)
	if err != nil {
		return BacklinksOut{}, err
	}
	sort.Slice(order, func(i, j int) bool { return t.order[order[i]] < t.order[order[j]] })
	for i := in.Offset; i < len(order) && len(out.Items) < limit; i++ {
		b := byFrom[order[i]]
		if r, ok := t.ref(order[i]); ok {
			b.From = r
			out.Items = append(out.Items, *b)
		}
	}
	return out, nil
}

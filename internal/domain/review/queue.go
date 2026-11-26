package review

import (
	"database/sql"
	"sort"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/ops"
)

func registerQueue(r *ops.Registry) {
	editor := ops.RequireProject(ops.ProjectEditor)
	ops.Register(r, ops.Op{Name: "review.queue", Method: "GET", Path: "/api/v1/projects/{id}/review/queue", Auth: editor,
		Desc: "The writer's review queue: every open thread of the project in reading order (comments on the project first; then each document in the order of the book, its threads in order of position, those that lost their text last), each with its document and breadcrumb, plus the counts per document. " +
			"Give after (a thread id, even one just resolved) to get the threads that come after it: the first is the next unresolved. Filter by document_id, author, kind and state. Needs the editor role. " +
			"visibility: shared (default) is the threads the people on the project can read, the review everyone shares; all adds your own private (\"Only me\") threads, which no one else's call can list. The counts follow the same choice."},
		func(c *ops.Ctx, in QueueIn) (QueueOut, error) { return queue(c, in) })
	ops.Register(r, ops.Op{Name: "review.resolve_all", Method: "POST", Path: "/api/v1/projects/{id}/review/resolve", Auth: editor,
		Desc: "Resolve every open thread that matches a filter (document_id, author, kind, state, or an explicit thread_ids list) in one transaction. Send preview true first: the answer is the count and nothing changes; send the same request with expect_count set to that number to do it, and it is refused (409) if the set changed. The authors of the threads are notified. Needs the editor role. " +
			"visibility: shared (default) resolves the threads the people on the project can read, never your own private (\"Only me\") ones; private resolves only yours, tells no one and is not counted anywhere else. A thread of someone else's that is private is skipped as an id that does not exist."},
		func(c *ops.Ctx, in ResolveAllIn) (ResolveAllOut, error) { return resolveAll(c, in) })
}

// DocRef names a document in a queue, with where it sits in the book.
type DocRef struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Kind       string   `json:"kind"`
	Breadcrumb []string `json:"breadcrumb" desc:"Titles of the folders and parts it sits in, outermost first, not including itself."`
}

// QueueItem is one open thread in the queue.
type QueueItem struct {
	Position int        `json:"position" desc:"1-based place in the whole queue for these filters."`
	Document *DocRef    `json:"document,omitempty" desc:"Empty for a comment on the project."`
	Thread   CommentOut `json:"thread"`
}

// DocCount is the review state of one document.
type DocCount struct {
	DocumentID string   `json:"document_id"`
	Title      string   `json:"title"`
	Breadcrumb []string `json:"breadcrumb"`
	Open       int      `json:"open"`
	Orphaned   int      `json:"orphaned" desc:"Of the open ones, how many lost their text."`
	Resolved   int      `json:"resolved"`
}

// QueueIn asks for the queue.
type QueueIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `query:"document_id" desc:"Only this document, or the word project for comments on the project."`
	Author     string `query:"author" desc:"Only threads started by this user id, or me."`
	Kind       string `query:"kind" enum:"comment,question,suggestion-note"`
	State      string `query:"state" enum:"active,orphaned,none"`
	After      string `query:"after" desc:"A thread id: only the threads after it in queue order."`
	Visibility string `query:"visibility" enum:"shared,private,all" desc:"Default shared: the review everyone shares. all: also your own private threads (the ones you can see). A private thread of someone else's is never listed."`
	Limit      int    `query:"limit" desc:"Threads per page (default 50, at most 200)."`
}

// ProjectRef lets the registry authorize by membership.
func (in QueueIn) ProjectRef() string { return in.ProjectID }

// QueueOut is a page of the queue.
type QueueOut struct {
	Items        []QueueItem `json:"items"`
	Total        int         `json:"total" desc:"Open threads matching the filters."`
	Orphaned     int         `json:"orphaned" desc:"Of them, how many lost their text."`
	ProjectLevel int         `json:"project_level" desc:"Of them, how many are on the project itself."`
	Next         string      `json:"next,omitempty" desc:"The first thread of the page when after was given: the next unresolved one."`
	Counts       []DocCount  `json:"counts" desc:"Per document with any thread, in reading order. Not narrowed by the filters."`
}

// tree is a project's documents in reading order.
type tree struct {
	rank  map[string]int
	nodes map[string]docref.Node
}

func loadTree(c *ops.Ctx, q querier, projectID string) (tree, error) {
	rows, err := q.QueryContext(c, `SELECT id, COALESCE(parent_id,''), kind, title, position FROM documents WHERE project_id = ? AND deleted_at IS NULL`, projectID)
	if err != nil {
		return tree{}, err
	}
	defer rows.Close()
	var nodes []docref.Node
	t := tree{rank: map[string]int{}, nodes: map[string]docref.Node{}}
	for rows.Next() {
		var n docref.Node
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position); err != nil {
			return t, err
		}
		nodes = append(nodes, n)
		t.nodes[n.ID] = n
	}
	for i, id := range docref.Order(nodes) {
		t.rank[id] = i
	}
	return t, rows.Err()
}

func (t tree) ref(id string) *DocRef {
	n, ok := t.nodes[id]
	if !ok {
		return nil
	}
	d := &DocRef{ID: n.ID, Title: n.Title, Kind: n.Kind, Breadcrumb: []string{}}
	var up []string
	for p, depth := n.ParentID, 0; p != "" && depth < 64; depth++ {
		pn, ok := t.nodes[p]
		if !ok {
			break
		}
		up = append(up, pn.Title)
		p = pn.ParentID
	}
	for i := len(up) - 1; i >= 0; i-- {
		d.Breadcrumb = append(d.Breadcrumb, up[i])
	}
	return d
}

// sortKey is a thread's place in queue order.
type sortKey struct {
	doc, state, start int
	id                string
}

func (a sortKey) less(b sortKey) bool {
	switch {
	case a.doc != b.doc:
		return a.doc < b.doc
	case a.state != b.state:
		return a.state < b.state
	case a.start != b.start:
		return a.start < b.start
	}
	return a.id < b.id
}

func keyOf(t tree, r row) sortKey {
	k := sortKey{doc: -1, id: r.ID}
	if r.DocumentID.Valid {
		if rk, ok := t.rank[r.DocumentID.String]; ok {
			k.doc = rk
		} else {
			k.doc = 1 << 30
		}
	}
	switch r.State {
	case comanchor.StateActive:
		k.state, k.start = 1, r.AStart
	case comanchor.StateOrphaned:
		k.state = 2
	}
	return k
}

func queue(c *ops.Ctx, in QueueIn) (QueueOut, error) {
	q := c.DB().Read()
	mode := in.Visibility
	if mode == "" {
		mode = VisShared
	}
	lin := ListIn{DocumentID: in.DocumentID, Status: StatusOpen, State: in.State, Kind: in.Kind, Author: in.Author, Visibility: mode}
	where, args, ierr := threadFilter(in.ProjectID, c.P.UserID, lin, true)
	if ierr != nil {
		return QueueOut{}, ierr
	}
	t, err := loadTree(c, q, in.ProjectID)
	if err != nil {
		return QueueOut{}, err
	}
	roots, err := fetch(c, q, c.P.UserID, where, args...)
	if err != nil {
		return QueueOut{}, err
	}
	keys := make(map[string]sortKey, len(roots))
	for _, r := range roots {
		keys[r.ID] = keyOf(t, r)
	}
	sort.Slice(roots, func(i, j int) bool { return keys[roots[i].ID].less(keys[roots[j].ID]) })
	out := QueueOut{Total: len(roots), Items: []QueueItem{}, Counts: []DocCount{}}
	for _, r := range roots {
		if r.State == comanchor.StateOrphaned {
			out.Orphaned++
		}
		if !r.DocumentID.Valid {
			out.ProjectLevel++
		}
	}
	from := 0
	if in.After != "" {
		var ak sortKey
		if k, ok := keys[in.After]; ok {
			ak = k
		} else {
			// Resolved or filtered out since: where it stood is still known.
			ar, err := fetch(c, q, c.P.UserID, `WHERE c.id = ? AND c.project_id = ? AND c.parent_id IS NULL`, in.After, in.ProjectID)
			if err != nil {
				return out, err
			}
			if len(ar) == 0 {
				return out, ops.NotFound("comment")
			}
			ak = keyOf(t, ar[0])
		}
		from = sort.Search(len(roots), func(i int) bool { return ak.less(keys[roots[i].ID]) })
	}
	limit, _ := pageOf(in.Limit, 0)
	page := roots[from:min(len(roots), from+limit)]
	ids := make([]string, len(page))
	for i, r := range page {
		ids[i] = r.ID
	}
	reps, err := repliesOf(c, q, c.P.UserID, ids)
	if err != nil {
		return out, err
	}
	all := append([]string(nil), ids...)
	for _, rs := range reps {
		for _, r := range rs {
			all = append(all, r.ID)
		}
	}
	ments, err := mentionsOf(c, q, all)
	if err != nil {
		return out, err
	}
	built := build(page, reps, ments, viewerOf(c, in.ProjectID))
	for i, th := range built {
		it := QueueItem{Position: from + i + 1, Thread: th}
		if page[i].DocumentID.Valid {
			it.Document = t.ref(page[i].DocumentID.String)
		}
		out.Items = append(out.Items, it)
	}
	if in.After != "" && len(page) > 0 {
		out.Next = page[0].ID
	}
	// counts per document, in reading order
	// The counts follow the same audience as the list: the shared threads, or the ones this person can see.
	vis, vargs := audience("c", c.P.UserID, mode)
	crows, err := q.QueryContext(c, `SELECT c.document_id, c.status, c.anchor_state = 'orphaned', count(*) FROM comments c
		WHERE `+vis+` AND c.project_id = ? AND c.parent_id IS NULL AND c.document_id IS NOT NULL AND (c.deleted_at IS NULL OR EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = c.id AND r.deleted_at IS NULL))
		GROUP BY c.document_id, c.status, c.anchor_state = 'orphaned'`, append(vargs, in.ProjectID)...)
	if err != nil {
		return out, err
	}
	byDoc := map[string]*DocCount{}
	for crows.Next() {
		var doc, st string
		var orphan bool
		var n int
		if err := crows.Scan(&doc, &st, &orphan, &n); err != nil {
			crows.Close()
			return out, err
		}
		ref := t.ref(doc)
		if ref == nil {
			continue // a trashed document
		}
		dc := byDoc[doc]
		if dc == nil {
			dc = &DocCount{DocumentID: doc, Title: ref.Title, Breadcrumb: ref.Breadcrumb}
			byDoc[doc] = dc
		}
		if st == StatusOpen {
			dc.Open += n
			if orphan {
				dc.Orphaned += n
			}
		} else {
			dc.Resolved += n
		}
	}
	crows.Close()
	for _, dc := range byDoc {
		out.Counts = append(out.Counts, *dc)
	}
	sort.Slice(out.Counts, func(i, j int) bool { return t.rank[out.Counts[i].DocumentID] < t.rank[out.Counts[j].DocumentID] })
	return out, nil
}

// ResolveAllIn resolves a set of threads.
type ResolveAllIn struct {
	ProjectID   string   `path:"id" json:"-" desc:"Project id."`
	DocumentID  string   `json:"document_id,omitempty" desc:"Only this document (or the word project for comments on the project)."`
	Author      string   `json:"author,omitempty" desc:"Only threads started by this user id, or me."`
	Kind        string   `json:"kind,omitempty" enum:"comment,question,suggestion-note"`
	State       string   `json:"state,omitempty" enum:"active,orphaned,none"`
	ThreadIDs   []string `json:"thread_ids,omitempty" desc:"Instead of filters: exactly these threads (ones that are not open are skipped)."`
	Visibility  string   `json:"visibility,omitempty" enum:"shared,private" desc:"Default shared. private: resolve only your own private threads (nobody is told). There is no way to resolve both in one call."`
	Preview     bool     `json:"preview,omitempty" desc:"Count what would be resolved and change nothing."`
	ExpectCount *int     `json:"expect_count,omitempty" desc:"The count a preview gave. The request is refused (409) if the set has a different size now."`
}

// ProjectRef lets the registry authorize by membership.
func (in ResolveAllIn) ProjectRef() string { return in.ProjectID }

// ResolveAllOut says what was, or would be, resolved.
type ResolveAllOut struct {
	Count    int  `json:"count" desc:"Threads matching."`
	Resolved int  `json:"resolved" desc:"Threads changed; 0 for a preview."`
	Preview  bool `json:"preview"`
}

const maxResolveAll = 5000

func resolveAll(c *ops.Ctx, in ResolveAllIn) (ResolveAllOut, error) {
	out := ResolveAllOut{Preview: in.Preview}
	mode := in.Visibility
	switch mode {
	case "":
		mode = VisShared
	case VisShared, VisPrivate:
	default:
		return out, ops.Invalid("visibility", "Use shared or private.")
	}
	if len(in.ThreadIDs) > maxResolveAll {
		return out, ops.Invalid("thread_ids", "At most 5,000 threads at a time.")
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		out.Count, out.Resolved = 0, 0
		var roots []row
		if len(in.ThreadIDs) > 0 {
			rs, err := fetch(c, tx, c.P.UserID, `WHERE c.project_id = ? AND c.visibility = ? AND c.parent_id IS NULL AND c.status = 'open' AND c.deleted_at IS NULL AND c.id IN (`+placeholders(len(in.ThreadIDs))+`)`,
				append([]any{in.ProjectID, mode}, idArgs(in.ThreadIDs)...)...)
			if err != nil {
				return err
			}
			roots = rs
		} else {
			where, args, ierr := threadFilter(in.ProjectID, c.P.UserID, ListIn{DocumentID: in.DocumentID, Status: StatusOpen, State: in.State, Kind: in.Kind, Author: in.Author, Visibility: mode}, true)
			if ierr != nil {
				return ierr
			}
			rs, err := fetch(c, tx, c.P.UserID, where+` LIMIT ?`, append(args, maxResolveAll+1)...)
			if err != nil {
				return err
			}
			if len(rs) > maxResolveAll {
				return ops.Invalid("document_id", "That is more than 5,000 threads; narrow the filter.")
			}
			roots = rs
		}
		out.Count = len(roots)
		if in.ExpectCount != nil && *in.ExpectCount != out.Count {
			return ops.ConflictWith("The set changed since the preview: it holds a different number of threads now.", map[string]any{"reason": "count", "count": out.Count})
		}
		if in.Preview || out.Count == 0 {
			return nil
		}
		now := db.Now()
		ids := make([]string, len(roots))
		for i, r := range roots {
			ids[i] = r.ID
		}
		for i := 0; i < len(ids); i += 400 {
			part := ids[i:min(i+400, len(ids))]
			args := append([]any{c.P.UserID, now, now}, idArgs(part)...)
			if _, err := tx.ExecContext(c, `UPDATE comments SET status = 'resolved', resolved_by = ?, resolved_at = ?, updated_at = ? WHERE status = 'open' AND id IN (`+placeholders(len(part))+`)`, args...); err != nil {
				return err
			}
		}
		out.Resolved = len(ids)
		if mode == VisPrivate {
			return c.FlushTo(tx) // nobody is told, and nothing is recorded that would show the others that such notes exist
		}
		for i, r := range roots {
			if i >= 200 {
				break // a notification each for the first 200 is plenty; a person is not buried
			}
			if r.AuthorID.Valid {
				if err := notify(tx, r.AuthorID.String, in.ProjectID, "resolved", r.ID, "", r.DocumentID.String, c.P.UserID, now); err != nil {
					return err
				}
			}
		}
		c.Emit(ops.Event{Type: "review.resolved", ProjectID: in.ProjectID, Payload: map[string]any{"count": out.Resolved}})
		c.Audit(ops.AuditEntry{Action: "review.resolve_all", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"count": out.Resolved}})
		return c.FlushTo(tx)
	})
	return out, err
}

package review

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/ops"
)

func registerComments(r *ops.Registry) {
	const base = "/api/v1/projects/{id}/comments"
	reviewer := ops.RequireProject(ops.ProjectReviewer)
	ops.Register(r, ops.Op{Name: "comment.create", Method: "POST", Path: base, Status: 201, Auth: reviewer,
		Desc: "Write a comment: a new thread anchored to a range of a document (give document_id and a quote with the words around it, or offsets with the base_revision you hold), a comment on a document as a whole (document_id only), a comment on the project (neither), or a reply (parent_id: the first comment of a thread). " +
			"Needs the reviewer role. The anchor is a range of the document's Markdown source: it follows the text as it is edited and goes orphaned, never guessed onto a lookalike, when the text is gone. A range may not touch a private note or ghosted text. " +
			"mentions are user ids from comment.people; @name in the body is resolved to the same people. Mentioned people, the author of the thread you reply to and the project's owner get an in-app notification (nothing is emailed or pushed). Comments are not part of the manuscript: they never reach an export, the mirror, Git or the search index. " +
			"visibility is shared (default: the people on the project who can read comments) or private (\"Only me\": the author alone, not the owner, not an editor, not an instance administrator). A private comment has no mentions and tells no one: no notification, no event, no webhook. A reply has its thread's visibility. Your own agent keys see your private comments, as you do."},
		func(c *ops.Ctx, in CreateIn) (ThreadItem, error) { return create(c, in) })
	ops.Register(r, ops.Op{Name: "comment.list", Method: "GET", Path: base, Auth: reviewer,
		Desc: "List comment threads (each root with its replies), newest last, for the whole project or one document (in document order, anchored ones first, then those that lost their place). Filter by status (open, resolved, all), anchor state, kind, author (a user id, or me), mentions_me and q (a word in the text). " +
			"Needs the reviewer role: a reader cannot see comments. total and the open/resolved counts are for the same filters without the status. " +
			"Only the threads you can see are listed or counted: shared ones, and your own private (\"Only me\") ones, which no one else's call, an owner's or an administrator's included, can list, count or find with q. visibility narrows to shared, private or all (default all: everything you can see). A private comment of someone who is taken off the project is kept, and no one can read it; it is theirs again if they are added back. Backups and the database file hold private comments as they hold everything else: \"Only me\" is kept by the application, not against someone who has the server's files."},
		func(c *ops.Ctx, in ListIn) (ListOut, error) { return list(c, in) })
	ops.Register(r, ops.Op{Name: "comment.people", Method: "GET", Path: "/api/v1/projects/{id}/comment-people", Auth: reviewer,
		Desc: "The people on this project who can read comments (owner, editors, reviewers), with the display names to mention. Only names and ids: no emails, and not the readers. This is the mention picker's list; the full member list stays the owner's."},
		func(c *ops.Ctx, in PeopleIn) (PeopleOut, error) { return people(c, in) })
	ops.Register(r, ops.Op{Name: "comment.get", Method: "GET", Path: base + "/{comment_id}", Auth: reviewer,
		Desc: "One thread: the comment you name (or the thread a reply belongs to), with its replies. A private thread of someone else is answered exactly as an id that does not exist (404)."},
		func(c *ops.Ctx, in GetIn) (ThreadItem, error) { return get(c, in) })
	ops.Register(r, ops.Op{Name: "comment.update", Method: "PATCH", Path: base + "/{comment_id}", Auth: reviewer,
		Desc: "Reword a comment. Only its author, and only within 15 minutes of writing it (of sharing it, for one that was written as private); after that, reply instead. A private comment has no time limit: nobody else has read it. Mentions are worked out again; a person newly mentioned is notified (never for a private comment)."},
		func(c *ops.Ctx, in UpdateIn) (ThreadItem, error) { return update(c, in) })
	ops.Register(r, ops.Op{Name: "comment.delete", Permanent: true, Method: "DELETE", Path: base + "/{comment_id}", Auth: reviewer,
		Desc: "Delete a comment: its author or the project's owner. The row stays as a tombstone with no text so a thread keeps its shape (a thread with no live comment is not listed). Its notifications go with it."},
		func(c *ops.Ctx, in GetIn) (DeleteOut, error) { return remove(c, in) })
	ops.Register(r, ops.Op{Name: "comment.resolve", Method: "POST", Path: base + "/{comment_id}/resolve", Auth: reviewer,
		Desc: "Resolve a thread. An editor or the owner may resolve any thread; a reviewer only their own. The author of the thread is notified. Resolving a resolved thread changes nothing."},
		func(c *ops.Ctx, in GetIn) (ThreadItem, error) { return setStatus(c, in, StatusResolved) })
	ops.Register(r, ops.Op{Name: "comment.reopen", Method: "POST", Path: base + "/{comment_id}/reopen", Auth: reviewer,
		Desc: "Reopen a resolved thread. Same permission as resolving."},
		func(c *ops.Ctx, in GetIn) (ThreadItem, error) { return setStatus(c, in, StatusOpen) })
	ops.Register(r, ops.Op{Name: "comment.reanchor", Method: "POST", Path: base + "/{comment_id}/reanchor", Auth: reviewer,
		Desc: "Give a thread a new place: for one that lost its text (orphaned), or to move it (to another range, another document of the project, or with detach, to its document as a whole). An editor or the owner, or the thread's own author. The range is checked as for comment.create."},
		func(c *ops.Ctx, in ReanchorIn) (ThreadItem, error) { return reanchor(c, in) })
}

// ---- shapes ---------------------------------------------------------------------------------

// AuthorOut says who wrote a comment.
type AuthorOut struct {
	Kind   string `json:"kind" enum:"user,guest,removed,label" desc:"user: a member. guest: a person with a share link (name is shown as Guest: Name). removed: an account that no longer exists; the text is kept. label: the name of a Word author, from an imported .docx: not an account, and never matched to one."`
	UserID string `json:"user_id,omitempty"`
	Name   string `json:"name"`
}

// AnchorOut is where a thread sits in its document.
type AnchorOut struct {
	State    string `json:"state" enum:"active,orphaned" desc:"orphaned: the commented text is gone or changed beyond recognition. The range is not trusted; the thread is listed and can be re-attached with comment.reanchor."`
	Start    int    `json:"start" desc:"UTF-16 code units of the body (the text before the alternatives comment, LF line endings). Meaningless when orphaned."`
	End      int    `json:"end"`
	Quote    string `json:"quote" desc:"The Markdown source of the text there now."`
	Original string `json:"original,omitempty" desc:"The text when the comment was made, when the writer has since edited it."`
	Before   string `json:"before,omitempty" desc:"Up to 24 characters of source before the range."`
	After    string `json:"after,omitempty" desc:"Up to 24 characters after it."`
	Revision int    `json:"revision" desc:"The document revision start and end are valid for."`
	// Set by comment.list with plain=true: the anchor as a reader sees it rendered.
	QuotePlain  string `json:"quote_plain,omitempty" desc:"With plain=true: the quote without formatting marks, to find the place in a rendered page."`
	BeforePlain string `json:"before_plain,omitempty" desc:"With plain=true: up to 24 characters of rendered text before it (private notes and ghosted text never included)."`
	AfterPlain  string `json:"after_plain,omitempty" desc:"With plain=true: up to 24 characters after it."`
}

// CommentOut is one comment.
type CommentOut struct {
	ID         string     `json:"id"`
	ThreadID   string     `json:"thread_id" desc:"The first comment of the thread (this one, when it is the first)."`
	DocumentID string     `json:"document_id,omitempty" desc:"Empty for a comment on the project."`
	Kind       string     `json:"kind" enum:"comment,question,suggestion-note"`
	Author     AuthorOut  `json:"author"`
	Body       string     `json:"body" desc:"Plain text, as written. Show it as text, never as HTML. Empty when deleted."`
	Deleted    bool       `json:"deleted,omitempty" desc:"A tombstone: the thread keeps its shape, the text is gone."`
	Visibility string     `json:"visibility" enum:"shared,private" desc:"shared: the people on the project who can read comments. private: only its author (\"Only me\"); nobody else is told it exists."`
	Status     string     `json:"status,omitempty" enum:"open,resolved" desc:"Of the thread; set on its first comment."`
	ResolvedBy *AuthorOut `json:"resolved_by,omitempty"`
	ResolvedAt string     `json:"resolved_at,omitempty"`
	Anchor     *AnchorOut `json:"anchor,omitempty" desc:"On the first comment of an anchored thread."`
	Mentions   []Person   `json:"mentions,omitempty"`
	CreatedAt  string     `json:"created_at"`
	EditedAt   string     `json:"edited_at,omitempty"`
	CanEdit    bool       `json:"can_edit" desc:"For you now: you wrote it and the 15 minutes are not over (no limit for a private comment)."`
	CanDelete  bool       `json:"can_delete"`
	CanResolve bool       `json:"can_resolve" desc:"On the first comment of a thread."`
	Replies    []ReplyOut `json:"replies,omitempty" desc:"Oldest first; on the first comment of a thread."`
}

// ReplyOut is a reply in a thread: a comment without a status or an anchor, which
// belong to the thread.
type ReplyOut struct {
	ID         string    `json:"id"`
	ThreadID   string    `json:"thread_id"`
	DocumentID string    `json:"document_id,omitempty"`
	Kind       string    `json:"kind" enum:"comment,question,suggestion-note"`
	Author     AuthorOut `json:"author"`
	Body       string    `json:"body" desc:"Plain text. Empty when deleted."`
	Deleted    bool      `json:"deleted,omitempty"`
	Mentions   []Person  `json:"mentions,omitempty"`
	CreatedAt  string    `json:"created_at"`
	EditedAt   string    `json:"edited_at,omitempty"`
	CanEdit    bool      `json:"can_edit"`
	CanDelete  bool      `json:"can_delete"`
}

// ThreadItem wraps one thread.
type ThreadItem struct {
	Item CommentOut `json:"item"`
}

// ReplayRef is what an Idempotency-Key's record holds in place of the answer when the answer is a private comment: its id. Repeating the
// request reads the thread again, as the person who repeats it (the words are stored in one place).
func (t ThreadItem) ReplayRef() string {
	if t.Item.Visibility == VisPrivate {
		return t.Item.ID
	}
	return ""
}

// CreateIn writes a comment.
type CreateIn struct {
	ProjectID    string   `path:"id" json:"-" desc:"Project id."`
	DocumentID   string   `json:"document_id,omitempty" desc:"The document the comment is on; empty for a comment on the project."`
	ParentID     string   `json:"parent_id,omitempty" desc:"Reply to this thread (the id of its first comment)."`
	Kind         string   `json:"kind,omitempty" enum:"comment,question,suggestion-note" desc:"Default comment."`
	Body         string   `json:"body" desc:"The text, up to 10,000 characters. Plain text."`
	BaseRevision int      `json:"base_revision,omitempty" desc:"The revision of the document you hold. Offsets are accepted only at the current revision (409 with current_revision otherwise); a quote is found at any revision."`
	Start        *int     `json:"start,omitempty" desc:"Anchor: start, in UTF-16 code units of the body at base_revision."`
	End          *int     `json:"end,omitempty" desc:"Anchor: end (exclusive)."`
	Quote        string   `json:"quote,omitempty" desc:"Anchor, instead of offsets: the Markdown source of the text."`
	Before       string   `json:"before,omitempty" desc:"With quote: some source before it, to tell repeats apart."`
	After        string   `json:"after,omitempty" desc:"With quote: some source after it."`
	Occurrence   *int     `json:"occurrence,omitempty" desc:"With quote: which occurrence, counting from 0, when context cannot tell."`
	Plain        bool     `json:"plain,omitempty" desc:"quote, before and after are the text as a reader sees it rendered (a selection in the reading view), not Markdown source. It is looked for in the text a reader can see (not front matter, private notes, link targets or footnotes), and placed in the source, formatting inside it included."`
	Mentions     []string `json:"mentions,omitempty" desc:"User ids to mention (from comment.people). Not for a private comment."`
	Visibility   string   `json:"visibility,omitempty" enum:"shared,private" desc:"Default shared. private: only you can see it (\"Only me\"); it has no mentions and tells no one. A reply takes its thread's."`
}

// ProjectRef lets the registry authorize by membership.
func (in CreateIn) ProjectRef() string { return in.ProjectID }

// KeepBodyOutOfFingerprint says the request's body is left out of what an Idempotency-Key remembers of it: a hash of a private note's words
// would be a guessing oracle.
func (in CreateIn) KeepBodyOutOfFingerprint() bool { return in.Visibility == VisPrivate }

func (in CreateIn) anchor() *comanchor.Range {
	if in.Start == nil && in.End == nil && in.Quote == "" {
		return nil
	}
	return &comanchor.Range{Start: in.Start, End: in.End, Quote: in.Quote, Before: in.Before, After: in.After, Occurrence: in.Occurrence, Plain: in.Plain}
}

func create(c *ops.Ctx, in CreateIn) (ThreadItem, error) {
	var created Created
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cr, evs, err := InsertComment(c, tx, NewComment{ProjectID: in.ProjectID, DocumentID: in.DocumentID, ParentID: in.ParentID, Kind: in.Kind, Body: in.Body,
			Anchor: in.anchor(), BaseRevision: in.BaseRevision, Mentions: in.Mentions, Visibility: in.Visibility, Actor: Actor{UserID: c.P.UserID}})
		if err != nil {
			return err
		}
		created = cr
		for _, e := range evs {
			c.Emit(e)
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return ThreadItem{}, err
	}
	return thread(c, c.DB().Read(), in.ProjectID, created.ThreadID, viewerOf(c, in.ProjectID))
}

// ---- reading ----------------------------------------------------------------------------------

type viewer struct {
	UserID string
	Role   string
}

func viewerOf(c *ops.Ctx, projectID string) viewer {
	return viewer{UserID: c.P.UserID, Role: roleOf(c, projectID)}
}

type row struct {
	ID, ProjectID, Kind, GuestName, Body, Status string
	Visibility                                   string
	SharedAt                                     sql.NullString
	DocumentID, ParentID, AuthorID, ShareID      sql.NullString
	ResolvedBy, ResolvedAt                       sql.NullString
	AuthorName                                   string
	AuthorGone                                   bool
	ResolverName                                 string
	State, AQuote, AOriginal, APrefix, ASuffix   string
	AStart, AEnd, ARev                           int
	CreatedAt, UpdatedAt                         string
	EditedAt, DeletedAt                          sql.NullString
}

const commentCols = `c.id, c.project_id, c.document_id, c.parent_id, c.kind, c.author_id, c.share_id, c.guest_name, c.body, c.status, c.resolved_by, c.resolved_at,
	c.anchor_state, c.anchor_start, c.anchor_end, c.anchor_quote, c.anchor_original, c.anchor_prefix, c.anchor_suffix, c.anchor_revision, c.created_at, c.updated_at, c.edited_at, c.deleted_at,
	COALESCE(NULLIF(u.display_name, ''), 'Member'), COALESCE(u.deleted_at IS NOT NULL, 0), COALESCE(NULLIF(ru.display_name, ''), 'Member'), c.visibility, c.shared_at`

const commentFrom = ` FROM comments c LEFT JOIN users u ON u.id = c.author_id LEFT JOIN users ru ON ru.id = c.resolved_by`

func scanRow(s interface{ Scan(...any) error }) (row, error) {
	var r row
	err := s.Scan(&r.ID, &r.ProjectID, &r.DocumentID, &r.ParentID, &r.Kind, &r.AuthorID, &r.ShareID, &r.GuestName, &r.Body, &r.Status, &r.ResolvedBy, &r.ResolvedAt,
		&r.State, &r.AStart, &r.AEnd, &r.AQuote, &r.AOriginal, &r.APrefix, &r.ASuffix, &r.ARev, &r.CreatedAt, &r.UpdatedAt, &r.EditedAt, &r.DeletedAt,
		&r.AuthorName, &r.AuthorGone, &r.ResolverName, &r.Visibility, &r.SharedAt)
	return r, err
}

func authorOf(r row) AuthorOut {
	switch {
	case r.ShareID.Valid:
		return AuthorOut{Kind: "guest", Name: "Guest: " + r.GuestName}
	case !r.AuthorID.Valid && r.GuestName != "":
		return AuthorOut{Kind: "label", Name: r.GuestName} // from a Word file: a name, not an account
	case r.AuthorID.Valid && !r.AuthorGone:
		return AuthorOut{Kind: "user", UserID: r.AuthorID.String, Name: r.AuthorName}
	}
	return AuthorOut{Kind: "removed", Name: "Removed user"}
}

// visible is the one rule of who may see a comment: it is shared, or the viewer wrote it. Nothing else grants access: not the
// owner, not an editor, not an instance administrator. Every read of the comments table goes through it (fetch, threadFilter and
// the few statements that count), and TestEveryCommentReadFiltersVisibility fails on a read that does not.
func visible(alias, viewerID string) (string, []any) {
	return "(" + alias + ".visibility = 'shared' OR " + alias + ".author_id = ?)", []any{viewerID}
}

// audience is visible narrowed to what a list asks for: "shared" the threads everyone shares, "private" only the viewer's own
// private ones, anything else (all) every thread the viewer can see.
func audience(alias, viewerID, mode string) (string, []any) {
	vis, args := visible(alias, viewerID)
	switch mode {
	case VisShared:
		vis += " AND " + alias + ".visibility = 'shared'"
	case VisPrivate:
		vis += " AND " + alias + ".visibility = 'private'"
	}
	return vis, args
}

// fetch runs a SELECT of commentCols and scans every row the viewer may see. where starts with WHERE and has no OR outside
// parentheses (the visibility is put in front of it with AND). Callers must pass the viewer, so a read without one does not compile.
func fetch(ctx context.Context, q querier, viewerID, where string, args ...any) ([]row, error) {
	if !strings.HasPrefix(where, "WHERE ") {
		panic("review.fetch: a WHERE clause is required")
	}
	vis, vargs := visible("c", viewerID)
	rows, err := q.QueryContext(ctx, `SELECT `+commentCols+commentFrom+` WHERE `+vis+` AND `+strings.TrimPrefix(where, "WHERE "), append(vargs, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// placeholders makes "?,?,?" for n ids.
func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func idArgs(ids []string) []any {
	a := make([]any, len(ids))
	for i, id := range ids {
		a[i] = id
	}
	return a
}

// mentionsOf loads the people of a set of comments, in chunks.
func mentionsOf(ctx context.Context, q querier, ids []string) (map[string][]Person, error) {
	out := map[string][]Person{}
	for i := 0; i < len(ids); i += 400 {
		part := ids[i:min(i+400, len(ids))]
		rows, err := q.QueryContext(ctx, `SELECT m.comment_id, u.id, COALESCE(NULLIF(u.display_name, ''), 'Member') FROM comment_mentions m JOIN users u ON u.id = m.user_id
			WHERE m.comment_id IN (`+placeholders(len(part))+`) ORDER BY u.display_name, u.id`, idArgs(part)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cid string
			var p Person
			if err := rows.Scan(&cid, &p.UserID, &p.Name); err != nil {
				rows.Close()
				return nil, err
			}
			out[cid] = append(out[cid], p)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// repliesOf loads the live and deleted replies of a set of threads, oldest first.
func repliesOf(ctx context.Context, q querier, viewerID string, roots []string) (map[string][]row, error) {
	out := map[string][]row{}
	for i := 0; i < len(roots); i += 400 {
		part := roots[i:min(i+400, len(roots))]
		rs, err := fetch(ctx, q, viewerID, `WHERE c.parent_id IN (`+placeholders(len(part))+`) ORDER BY c.created_at, c.id`, idArgs(part)...)
		if err != nil {
			return nil, err
		}
		for _, r := range rs {
			out[r.ParentID.String] = append(out[r.ParentID.String], r)
		}
	}
	return out, nil
}

func liveReplies(rs []row) int {
	n := 0
	for _, r := range rs {
		if !r.DeletedAt.Valid {
			n++
		}
	}
	return n
}

// build turns rows into the shape clients get.
func build(roots []row, replies map[string][]row, mentions map[string][]Person, v viewer) []CommentOut {
	one := func(r row, thread string) CommentOut {
		o := CommentOut{ID: r.ID, ThreadID: thread, DocumentID: r.DocumentID.String, Kind: r.Kind, Author: authorOf(r), Body: r.Body, Deleted: r.DeletedAt.Valid,
			Mentions: mentions[r.ID], CreatedAt: r.CreatedAt, EditedAt: r.EditedAt.String, Visibility: r.Visibility}
		mine := v.UserID != "" && r.AuthorID.Valid && r.AuthorID.String == v.UserID
		if !o.Deleted {
			o.CanEdit = mine && editable(r)
			o.CanDelete = mine || (v.Role == ops.ProjectOwner && r.Visibility != VisPrivate)
		}
		return o
	}
	out := make([]CommentOut, 0, len(roots))
	for _, r := range roots {
		o := one(r, r.ID)
		o.Status = r.Status
		mine := v.UserID != "" && r.AuthorID.Valid && r.AuthorID.String == v.UserID
		o.CanResolve = atLeast(v.Role, ops.ProjectEditor) || (mine && atLeast(v.Role, ops.ProjectReviewer))
		if r.ResolvedAt.Valid {
			o.ResolvedAt = r.ResolvedAt.String
			if r.ResolvedBy.Valid {
				o.ResolvedBy = &AuthorOut{Kind: "user", UserID: r.ResolvedBy.String, Name: r.ResolverName}
			} else {
				o.ResolvedBy = &AuthorOut{Kind: "removed", Name: "Removed user"}
			}
		}
		if r.State == comanchor.StateActive || r.State == comanchor.StateOrphaned {
			a := &AnchorOut{State: r.State, Start: r.AStart, End: r.AEnd, Quote: r.AQuote, Before: r.APrefix, After: r.ASuffix, Revision: r.ARev}
			if r.AOriginal != r.AQuote {
				a.Original = r.AOriginal
			}
			o.Anchor = a
		}
		for _, rp := range replies[r.ID] {
			c := one(rp, r.ID)
			o.Replies = append(o.Replies, ReplyOut{ID: c.ID, ThreadID: c.ThreadID, DocumentID: c.DocumentID, Kind: c.Kind, Author: c.Author, Body: c.Body, Deleted: c.Deleted,
				Mentions: c.Mentions, CreatedAt: c.CreatedAt, EditedAt: c.EditedAt, CanEdit: c.CanEdit, CanDelete: c.CanDelete})
		}
		out = append(out, o)
	}
	return out
}

// editable says whether the author may still reword a comment: a private one at any time (nobody else has read it); a shared one for
// 15 minutes from when it became visible to others (from when it was written, for every comment that was shared from the start).
func editable(r row) bool {
	if r.Visibility == VisPrivate {
		return true
	}
	at := r.SharedAt.String
	if at == "" {
		at = r.CreatedAt
	}
	t, _ := db.Parse(at)
	return db.NowTime().Sub(t) <= EditWindow
}

// thread loads one thread for a viewer.
func thread(ctx context.Context, q querier, projectID, anyID string, v viewer) (ThreadItem, error) {
	roots, err := fetch(ctx, q, v.UserID, `WHERE c.id = ? AND c.project_id = ?`, anyID, projectID)
	if err != nil {
		return ThreadItem{}, err
	}
	if len(roots) == 0 {
		return ThreadItem{}, ops.NotFound("comment")
	}
	if roots[0].ParentID.Valid {
		if roots, err = fetch(ctx, q, v.UserID, `WHERE c.id = ? AND c.project_id = ?`, roots[0].ParentID.String, projectID); err != nil {
			return ThreadItem{}, err
		}
		if len(roots) == 0 {
			return ThreadItem{}, ops.NotFound("comment")
		}
	}
	reps, err := repliesOf(ctx, q, v.UserID, []string{roots[0].ID})
	if err != nil {
		return ThreadItem{}, err
	}
	ids := []string{roots[0].ID}
	for _, r := range reps[roots[0].ID] {
		ids = append(ids, r.ID)
	}
	ments, err := mentionsOf(ctx, q, ids)
	if err != nil {
		return ThreadItem{}, err
	}
	return ThreadItem{Item: build(roots[:1], reps, ments, v)[0]}, nil
}

// ListIn lists threads.
type ListIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `query:"document_id" desc:"Only threads on this document (in document order). The word project lists the comments on the project itself."`
	Status     string `query:"status" enum:"open,resolved,all" desc:"Default all."`
	State      string `query:"state" enum:"active,orphaned,none" desc:"Only anchored threads in this state (none: not anchored to a range)."`
	Kind       string `query:"kind" enum:"comment,question,suggestion-note"`
	Author     string `query:"author" desc:"Only threads started by this user id, or me."`
	MentionsMe bool   `query:"mentions_me" desc:"Only threads in which someone mentioned you."`
	Q          string `query:"q" desc:"Only threads with this word or phrase in a comment (case-insensitive for plain letters)."`
	Visibility string `query:"visibility" enum:"shared,private,all" desc:"shared: only threads the people on the project can read. private: only your own \"Only me\" threads. Default all: every thread you can see."`
	Plain      bool   `query:"plain" desc:"With document_id: also give each anchor as the reader sees it rendered (quote_plain, before_plain, after_plain), for a reading view that does not parse Markdown."`
	Limit      int    `query:"limit" desc:"Threads per page (default 50, at most 200)."`
	Offset     int    `query:"offset"`
}

// ProjectRef lets the registry authorize by membership.
func (in ListIn) ProjectRef() string { return in.ProjectID }

// ListOut is a page of threads.
type ListOut struct {
	Items    []CommentOut `json:"items"`
	Total    int          `json:"total" desc:"Threads matching the filters."`
	Open     int          `json:"open" desc:"Of those matching every filter but status, how many are open."`
	Resolved int          `json:"resolved"`
}

// threadFilter builds the WHERE of a thread listing. statusToo says whether the
// status filter is applied (the counts leave it out).
// It always holds the visibility rule (visible), so a count or a search term can never confirm what the viewer may not see;
// in.Visibility only narrows what that leaves.
func threadFilter(projectID, me string, in ListIn, statusToo bool) (string, []any, *ops.Error) {
	vis, vargs := visible("c", me)
	where := `WHERE ` + vis + ` AND c.project_id = ? AND c.parent_id IS NULL AND (c.deleted_at IS NULL OR EXISTS (SELECT 1 FROM comments r WHERE r.parent_id = c.id AND r.visibility = c.visibility AND r.deleted_at IS NULL))
		AND (c.document_id IS NULL OR EXISTS (SELECT 1 FROM documents d WHERE d.id = c.document_id AND d.deleted_at IS NULL))`
	args := append(vargs, projectID)
	switch in.Visibility {
	case "", "all":
	case VisShared, VisPrivate:
		where += ` AND c.visibility = ?`
		args = append(args, in.Visibility)
	default:
		return "", nil, ops.Invalid("visibility", "Use shared, private or all.")
	}
	switch in.DocumentID {
	case "":
	case "project":
		where += ` AND c.document_id IS NULL`
	default:
		where += ` AND c.document_id = ?`
		args = append(args, in.DocumentID)
	}
	if statusToo && in.Status != "" && in.Status != "all" {
		if in.Status != StatusOpen && in.Status != StatusResolved {
			return "", nil, ops.Invalid("status", "Use open, resolved or all.")
		}
		where += ` AND c.status = ?`
		args = append(args, in.Status)
	}
	if in.State != "" {
		if in.State != comanchor.StateActive && in.State != comanchor.StateOrphaned && in.State != comanchor.StateNone {
			return "", nil, ops.Invalid("state", "Use active, orphaned or none.")
		}
		where += ` AND c.anchor_state = ?`
		args = append(args, in.State)
	}
	if in.Kind != "" {
		if in.Kind != KindComment && in.Kind != KindQuestion && in.Kind != KindSuggestionNote {
			return "", nil, ops.Invalid("kind", "Use comment, question or suggestion-note.")
		}
		where += ` AND c.kind = ?`
		args = append(args, in.Kind)
	}
	if in.Author != "" {
		a := in.Author
		if a == "me" {
			a = me
		}
		where += ` AND c.author_id = ?`
		args = append(args, a)
	}
	if in.MentionsMe {
		where += ` AND EXISTS (SELECT 1 FROM comment_mentions m JOIN comments mc ON mc.id = m.comment_id WHERE m.user_id = ? AND (mc.id = c.id OR mc.parent_id = c.id) AND mc.visibility = c.visibility AND mc.deleted_at IS NULL)`
		args = append(args, me)
	}
	if q := strings.TrimSpace(in.Q); q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		where += ` AND EXISTS (SELECT 1 FROM comments sc WHERE (sc.id = c.id OR sc.parent_id = c.id) AND sc.visibility = c.visibility AND sc.deleted_at IS NULL AND sc.body LIKE ? ESCAPE '\')`
		args = append(args, like)
	}
	return where, args, nil
}

func pageOf(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPage
	}
	if limit > MaxPage {
		limit = MaxPage
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// threadCounts is the number of threads a filter matches, and of those how many are open and resolved (the filters without the
// status), over the threads the viewer may see and no others.
func threadCounts(ctx context.Context, q querier, projectID, me string, in ListIn) (total, open, resolved int, err error) {
	where, args, ierr := threadFilter(projectID, me, in, true)
	if ierr != nil {
		return 0, 0, 0, ierr
	}
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM comments c `+where, args...).Scan(&total); err != nil {
		return
	}
	cw, cargs, _ := threadFilter(projectID, me, in, false)
	crow, err := q.QueryContext(ctx, `SELECT c.status, count(*) FROM comments c `+cw+` GROUP BY c.status`, cargs...)
	if err != nil {
		return
	}
	defer crow.Close()
	for crow.Next() {
		var st string
		var n int
		if err = crow.Scan(&st, &n); err != nil {
			return
		}
		if st == StatusOpen {
			open = n
		} else {
			resolved = n
		}
	}
	return total, open, resolved, crow.Err()
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	q := c.DB().Read()
	where, args, ierr := threadFilter(in.ProjectID, c.P.UserID, in, true)
	if ierr != nil {
		return ListOut{}, ierr
	}
	limit, offset := pageOf(in.Limit, in.Offset)
	var out ListOut
	var err error
	if out.Total, out.Open, out.Resolved, err = threadCounts(c, q, in.ProjectID, c.P.UserID, in); err != nil {
		return out, err
	}
	order := `ORDER BY c.id`
	if in.DocumentID != "" && in.DocumentID != "project" {
		order = `ORDER BY (c.anchor_state = 'orphaned'), (c.anchor_state = 'none') DESC, c.anchor_start, c.id`
	}
	roots, err := fetch(c, q, c.P.UserID, where+` `+order+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return out, err
	}
	ids := make([]string, len(roots))
	for i, r := range roots {
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
	out.Items = build(roots, reps, ments, viewerOf(c, in.ProjectID))
	if in.Plain && in.DocumentID != "" && in.DocumentID != "project" {
		var content string
		if err := q.QueryRowContext(c, `SELECT content FROM documents WHERE id = ? AND project_id = ?`, in.DocumentID, in.ProjectID).Scan(&content); err == nil {
			addPlain(out.Items, content)
		}
	}
	return out, nil
}

// addPlain fills the plain forms of the anchors of threads on one document.
func addPlain(items []CommentOut, content string) {
	body := comanchor.Body(content)
	hinter := comanchor.NewHinter(body)
	ix := alts.NewIndex(body)
	for i := range items {
		a := items[i].Anchor
		if a == nil || a.State != comanchor.StateActive {
			continue
		}
		s, ok1 := ix.Byte(a.Start)
		e, ok2 := ix.Byte(a.End)
		if !ok1 || !ok2 || s >= e || e > len(body) || body[s:e] != a.Quote {
			continue
		}
		p := hinter.For(s, e)
		a.QuotePlain, a.BeforePlain, a.AfterPlain = p.Quote, p.Before, p.After
	}
}

// PeopleIn asks for the mentionable people.
type PeopleIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in PeopleIn) ProjectRef() string { return in.ProjectID }

// PeopleOut lists them.
type PeopleOut struct {
	Items []Person `json:"items"`
}

func people(c *ops.Ctx, in PeopleIn) (PeopleOut, error) {
	ps, err := commenters(c, c.DB().Read(), in.ProjectID)
	if ps == nil {
		ps = []Person{}
	}
	return PeopleOut{Items: ps}, err
}

// GetIn names a comment.
type GetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	CommentID string `path:"comment_id" json:"-" desc:"Comment id."`
}

// ProjectRef lets the registry authorize by membership.
func (in GetIn) ProjectRef() string { return in.ProjectID }

func get(c *ops.Ctx, in GetIn) (ThreadItem, error) {
	return thread(c, c.DB().Read(), in.ProjectID, in.CommentID, viewerOf(c, in.ProjectID))
}

// ---- changing ---------------------------------------------------------------------------------

// UpdateIn rewords a comment.
type UpdateIn struct {
	ProjectID string   `path:"id" json:"-" desc:"Project id."`
	CommentID string   `path:"comment_id" json:"-" desc:"Comment id."`
	Body      string   `json:"body" desc:"The new text."`
	Mentions  []string `json:"mentions,omitempty" desc:"User ids to mention, as for comment.create."`
}

// ProjectRef lets the registry authorize by membership.
func (in UpdateIn) ProjectRef() string { return in.ProjectID }

// loadOne reads one comment of the project inside a transaction.
// A private comment of someone else is not found: the same 404 as an id that does not exist.
func loadOne(ctx context.Context, tx *sql.Tx, viewerID, projectID, id string) (row, error) {
	rs, err := fetch(ctx, tx, viewerID, `WHERE c.id = ? AND c.project_id = ?`, id, projectID)
	if err != nil {
		return row{}, err
	}
	if len(rs) == 0 {
		return row{}, ops.NotFound("comment")
	}
	return rs[0], nil
}

func update(c *ops.Ctx, in UpdateIn) (ThreadItem, error) {
	var threadID string
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := loadOne(c, tx, c.P.UserID, in.ProjectID, in.CommentID)
		if err != nil {
			return err
		}
		threadID = r.ID
		if r.ParentID.Valid {
			threadID = r.ParentID.String
		}
		if !r.AuthorID.Valid || r.AuthorID.String != c.P.UserID || r.DeletedAt.Valid {
			return ops.Forbidden("only the author can reword a comment")
		}
		if !editable(r) {
			return ops.Forbidden("a comment can be reworded for 15 minutes after it is written; reply instead")
		}
		body, e := CleanBody(in.Body, MaxBody)
		if e != nil {
			return e
		}
		if r.Visibility == VisPrivate {
			// Nobody else has read it: no window, no mentions, nobody told.
			if len(in.Mentions) > 0 {
				return ops.Invalid("mentions", "A comment only you can see mentions no one.")
			}
			now := db.Now()
			if _, err := tx.ExecContext(c, `UPDATE comments SET body = ?, edited_at = ?, updated_at = ? WHERE id = ?`, body, now, now, r.ID); err != nil {
				return err
			}
			return c.FlushTo(tx)
		}
		ppl, err := commenters(c, tx, in.ProjectID)
		if err != nil {
			return err
		}
		ok := map[string]bool{}
		for _, p := range ppl {
			ok[p.UserID] = true
		}
		want := map[string]bool{}
		for _, id := range in.Mentions {
			if !ok[id] {
				return ops.Invalid("mentions", "You can mention only the people on this project who can read comments.")
			}
			want[id] = true
		}
		for _, id := range mentionsIn(body, ppl) {
			want[id] = true
		}
		if len(want) > MaxMentions {
			return ops.Invalid("mentions", "A comment can mention at most "+itoa(MaxMentions)+" people.")
		}
		had := map[string]bool{}
		mrows, err := tx.QueryContext(c, `SELECT user_id FROM comment_mentions WHERE comment_id = ?`, r.ID)
		if err != nil {
			return err
		}
		for mrows.Next() {
			var u string
			if err := mrows.Scan(&u); err != nil {
				mrows.Close()
				return err
			}
			had[u] = true
		}
		mrows.Close()
		now := db.Now()
		if _, err := tx.ExecContext(c, `UPDATE comments SET body = ?, edited_at = ?, updated_at = ? WHERE id = ?`, body, now, now, r.ID); err != nil {
			return err
		}
		for u := range had {
			if !want[u] {
				if _, err := tx.ExecContext(c, `DELETE FROM comment_mentions WHERE comment_id = ? AND user_id = ?`, r.ID, u); err != nil {
					return err
				}
			}
		}
		for u := range want {
			if !had[u] {
				if _, err := tx.ExecContext(c, `INSERT INTO comment_mentions(comment_id, user_id) VALUES (?,?)`, r.ID, u); err != nil {
					return err
				}
				if err := notify(tx, u, in.ProjectID, "mention", r.ID, "", r.DocumentID.String, c.P.UserID, now); err != nil {
					return err
				}
			}
		}
		c.Emit(ops.Event{Type: "comment.updated", ProjectID: in.ProjectID, DocumentID: r.DocumentID.String, Payload: map[string]any{"comment_id": r.ID, "thread_id": threadID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ThreadItem{}, err
	}
	return thread(c, c.DB().Read(), in.ProjectID, threadID, viewerOf(c, in.ProjectID))
}

// DeleteOut says a comment is gone.
type DeleteOut struct {
	Deleted bool   `json:"deleted"`
	ID      string `json:"id"`
}

func remove(c *ops.Ctx, in GetIn) (DeleteOut, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := loadOne(c, tx, c.P.UserID, in.ProjectID, in.CommentID)
		if err != nil {
			return err
		}
		if r.DeletedAt.Valid {
			return nil // already gone
		}
		mine := r.AuthorID.Valid && r.AuthorID.String == c.P.UserID
		if !mine && roleOf(c, in.ProjectID) != ops.ProjectOwner {
			return ops.Forbidden("only the author or the project's owner can delete a comment")
		}
		now := db.Now()
		if _, err := tx.ExecContext(c, `UPDATE comments SET body = '', deleted_at = ?, updated_at = ?, anchor_state = 'none', anchor_quote = '', anchor_original = '', anchor_prefix = '', anchor_suffix = '', anchor_hash = '' WHERE id = ?`, now, now, r.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(c, `DELETE FROM comment_mentions WHERE comment_id = ?`, r.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(c, `DELETE FROM notifications WHERE comment_id = ?`, r.ID); err != nil {
			return err
		}
		thread := r.ID
		if r.ParentID.Valid {
			thread = r.ParentID.String
		}
		if r.Visibility == VisShared { // a private comment tells no one, in either direction
			c.Emit(ops.Event{Type: "comment.deleted", ProjectID: in.ProjectID, DocumentID: r.DocumentID.String, Payload: map[string]any{"comment_id": r.ID, "thread_id": thread}})
		}
		return c.FlushTo(tx)
	})
	return DeleteOut{Deleted: err == nil, ID: in.CommentID}, err
}

// canResolve says whether a person may change a thread's status.
func canResolve(role, userID string, r row) bool {
	if atLeast(role, ops.ProjectEditor) {
		return true
	}
	return atLeast(role, ops.ProjectReviewer) && userID != "" && r.AuthorID.Valid && r.AuthorID.String == userID
}

func setStatus(c *ops.Ctx, in GetIn, status string) (ThreadItem, error) {
	var threadID string
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := loadOne(c, tx, c.P.UserID, in.ProjectID, in.CommentID)
		if err != nil {
			return err
		}
		if r.ParentID.Valid { // a reply stands for its thread
			if r, err = loadOne(c, tx, c.P.UserID, in.ProjectID, r.ParentID.String); err != nil {
				return err
			}
		}
		threadID = r.ID
		if !canResolve(roleOf(c, in.ProjectID), c.P.UserID, r) {
			return ops.Forbidden("an editor or the owner can resolve any thread; a reviewer only their own")
		}
		if r.Status == status {
			return nil
		}
		now := db.Now()
		if status == StatusResolved {
			_, err = tx.ExecContext(c, `UPDATE comments SET status = 'resolved', resolved_by = ?, resolved_at = ?, updated_at = ? WHERE id = ?`, c.P.UserID, now, now, r.ID)
		} else {
			_, err = tx.ExecContext(c, `UPDATE comments SET status = 'open', resolved_by = NULL, resolved_at = NULL, updated_at = ? WHERE id = ?`, now, r.ID)
		}
		if err != nil {
			return err
		}
		if r.Visibility == VisPrivate {
			return c.FlushTo(tx) // no notification, no event
		}
		if status == StatusResolved && r.AuthorID.Valid {
			if err := notify(tx, r.AuthorID.String, in.ProjectID, "resolved", r.ID, "", r.DocumentID.String, c.P.UserID, now); err != nil {
				return err
			}
		}
		typ := "comment.resolved"
		if status == StatusOpen {
			typ = "comment.reopened"
		}
		c.Emit(ops.Event{Type: typ, ProjectID: in.ProjectID, DocumentID: r.DocumentID.String, Payload: map[string]any{"comment_id": r.ID, "thread_id": r.ID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ThreadItem{}, err
	}
	return thread(c, c.DB().Read(), in.ProjectID, threadID, viewerOf(c, in.ProjectID))
}

// ReanchorIn gives a thread a new place.
type ReanchorIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	CommentID    string `path:"comment_id" json:"-" desc:"The first comment of the thread."`
	DocumentID   string `json:"document_id,omitempty" desc:"Move the thread to this document of the project (default: where it is)."`
	Detach       bool   `json:"detach,omitempty" desc:"Drop the anchor: the thread is then on its document as a whole."`
	BaseRevision int    `json:"base_revision,omitempty" desc:"As for comment.create."`
	Start        *int   `json:"start,omitempty"`
	End          *int   `json:"end,omitempty"`
	Quote        string `json:"quote,omitempty"`
	Before       string `json:"before,omitempty"`
	After        string `json:"after,omitempty"`
	Occurrence   *int   `json:"occurrence,omitempty"`
	Plain        bool   `json:"plain,omitempty" desc:"As for comment.create."`
}

// ProjectRef lets the registry authorize by membership.
func (in ReanchorIn) ProjectRef() string { return in.ProjectID }

func reanchor(c *ops.Ctx, in ReanchorIn) (ThreadItem, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := loadOne(c, tx, c.P.UserID, in.ProjectID, in.CommentID)
		if err != nil {
			return err
		}
		if r.ParentID.Valid {
			return ops.Invalid("comment_id", "Name the first comment of the thread.")
		}
		if r.DeletedAt.Valid {
			return ops.Conflict("This comment was deleted.")
		}
		if !canResolve(roleOf(c, in.ProjectID), c.P.UserID, r) {
			return ops.Forbidden("an editor or the owner can move any thread; a reviewer only their own")
		}
		docID := in.DocumentID
		if docID == "" {
			docID = r.DocumentID.String
		}
		if docID == "" {
			return ops.Invalid("document_id", "A comment on the project has no document to anchor to; name one.")
		}
		var content string
		var rev int
		var deleted sql.NullString
		if err := tx.QueryRowContext(c, `SELECT content, revision, deleted_at FROM documents WHERE id = ? AND project_id = ?`, docID, in.ProjectID).Scan(&content, &rev, &deleted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("document")
			}
			return err
		}
		if deleted.Valid {
			return ops.Conflict("That document is in the trash.")
		}
		if docID != r.DocumentID.String && !in.Detach {
			// counted by audience: a private thread by its author's own private threads, a shared one by the shared threads
			var anchored int
			if r.Visibility == VisPrivate {
				if err := tx.QueryRowContext(c, `SELECT count(*) FROM comments WHERE document_id = ? AND visibility = 'private' AND author_id = ? AND parent_id IS NULL AND anchor_state <> 'none'`, docID, c.P.UserID).Scan(&anchored); err != nil {
					return err
				}
				if anchored >= MaxPrivateAnchoredPerAuthor {
					return ops.Conflict("That document holds the most anchored comments only you can see that it can for one person; resolve or delete some first.")
				}
			} else {
				if err := tx.QueryRowContext(c, `SELECT count(*) FROM comments WHERE document_id = ? AND visibility = 'shared' AND parent_id IS NULL AND anchor_state <> 'none'`, docID).Scan(&anchored); err != nil {
					return err
				}
				if anchored >= comanchor.MaxAnchoredPerDocument {
					return ops.Conflict("That document holds the most anchored comments it can; resolve or delete some first.")
				}
			}
		}
		now := db.Now()
		var a comanchor.Anchor
		if !in.Detach {
			rg := comanchor.Range{Start: in.Start, End: in.End, Quote: in.Quote, Before: in.Before, After: in.After, Occurrence: in.Occurrence, Plain: in.Plain}
			if rg.Start == nil && rg.End == nil && rg.Quote == "" {
				return ops.Invalid("quote", "Name the text to anchor to, or detach.")
			}
			base := in.BaseRevision
			if base == 0 {
				base = rev
			}
			body := comanchor.Body(content)
			s, e, err := comanchor.Resolve(body, rg, rev, base)
			switch {
			case errors.Is(err, comanchor.ErrStale):
				return ops.ConflictWith("The document changed since you opened it, so offsets cannot be trusted. Name the text instead.", map[string]any{"reason": "revision", "current_revision": rev})
			case err != nil:
				var re *comanchor.ErrRange
				if errors.As(err, &re) {
					return ops.Invalid("quote", re.Msg)
				}
				return err
			}
			a = comanchor.New(body, s, e, rev)
			if docID == r.DocumentID.String && r.AOriginal != "" {
				a.Original = r.AOriginal // what the comment was first made on stays
			}
		}
		state := comanchor.StateNone
		if a.State != "" {
			state = a.State
		}
		if _, err := tx.ExecContext(c, `UPDATE comments SET document_id = ?, anchor_state = ?, anchor_start = ?, anchor_end = ?, anchor_quote = ?, anchor_original = ?, anchor_prefix = ?, anchor_suffix = ?,
			anchor_hash = ?, anchor_revision = ?, updated_at = ? WHERE id = ?`, docID, state, a.Start, a.End, a.Quote, a.Original, a.Prefix, a.Suffix, a.Hash, a.Revision, now, r.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(c, `UPDATE comments SET document_id = ? WHERE parent_id = ?`, docID, r.ID); err != nil {
			return err
		}
		if r.Visibility == VisShared {
			c.Emit(ops.Event{Type: "comment.reanchored", ProjectID: in.ProjectID, DocumentID: docID, Payload: map[string]any{"comment_id": r.ID, "thread_id": r.ID}})
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return ThreadItem{}, err
	}
	return thread(c, c.DB().Read(), in.ProjectID, in.CommentID, viewerOf(c, in.ProjectID))
}

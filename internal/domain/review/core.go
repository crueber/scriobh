package review

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/ops"
)

// Actor is who writes a comment: a signed-in person, or the guest of a share link.
type Actor struct {
	UserID    string
	ShareID   string
	GuestName string
}

// Guest reports whether the actor is a share link's guest.
func (a Actor) Guest() bool { return a.ShareID != "" }

// NewComment is a comment to be written. Members and guests both come through
// InsertComment, so the same limits, the same anchoring checks and the same
// notifications apply to both.
type NewComment struct {
	ProjectID  string
	DocumentID string // empty: a project-level comment (a reply takes its thread's)
	ParentID   string // a reply to this thread
	Kind       string
	Body       string
	// Anchor, when set, anchors a new thread to a range of DocumentID. The range is
	// resolved against the text now: offsets only when BaseRevision is current.
	Anchor       *comanchor.Range
	BaseRevision int
	// Mentions are user ids the client picked; @name in the body is resolved too.
	// Guests mention nobody, and neither does a private comment.
	Mentions []string
	// Visibility is VisShared (the default) or VisPrivate, "Only me": the author alone. A reply has its thread's
	// (leave it empty, or name the thread's own).
	Visibility string
	Actor      Actor
	// DocumentOK, when set, says whether the comment's document may be commented
	// on (a share link shows a subset of the manuscript).
	DocumentOK func(documentID string) bool
	// ClientKey, for a guest, is a hash of what was sent: it lets the same comment sent twice be
	// answered with the first (the share route checks it before writing).
	ClientKey string
}

// Created says what InsertComment wrote.
type Created struct {
	ID         string
	DocumentID string
	ThreadID   string // the root: the comment itself for a new thread
	Anchored   bool
}

// InsertComment writes a comment in the caller's transaction and returns the
// events to flush with it (ids only). The caller has authorized the actor; this
// checks everything about the comment itself.
func InsertComment(ctx context.Context, tx *sql.Tx, n NewComment) (Created, []ops.Event, error) {
	var out Created
	kind := n.Kind
	if kind == "" {
		kind = KindComment
	}
	if kind != KindComment && kind != KindQuestion && kind != KindSuggestionNote {
		return out, nil, ops.Invalid("kind", "A comment is a comment, a question or a suggestion-note.")
	}
	max := MaxBody
	if n.Actor.Guest() {
		max = MaxGuestBody
		if kind == KindSuggestionNote {
			kind = KindComment
		}
	}
	body, e := CleanBody(n.Body, max)
	if e != nil {
		return out, nil, e
	}
	guest := ""
	if n.Actor.Guest() {
		var ge *ops.Error
		if guest, ge = CleanGuestName(n.Actor.GuestName); ge != nil {
			return out, nil, ge
		}
	}
	vis := n.Visibility
	if vis == "" {
		vis = VisShared
	}
	if vis != VisShared && vis != VisPrivate {
		return out, nil, ops.Invalid("visibility", "A comment is shared (people on this project can read it) or private (only you can).")
	}
	if vis == VisPrivate && (n.Actor.Guest() || n.Actor.UserID == "") {
		return out, nil, ops.Invalid("visibility", "Only a signed-in person can write a comment that only they can see.")
	}
	docID := n.DocumentID
	parent := ""
	var rootAuthor, rootShare sql.NullString
	if n.ParentID != "" {
		var pdoc, pparent sql.NullString
		var pvis string
		err := tx.QueryRowContext(ctx, `SELECT document_id, parent_id, author_id, share_id, visibility FROM comments WHERE id = ? AND project_id = ?`, n.ParentID, n.ProjectID).
			Scan(&pdoc, &pparent, &rootAuthor, &rootShare, &pvis)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil, ops.NotFound("comment")
		}
		if err != nil {
			return out, nil, err
		}
		// A private thread is the author's alone: for anyone else it is the same 404 as an id that does not exist, asked before
		// anything else about the reply, so nothing about it can be learned from the answer.
		if pvis == VisPrivate && (n.Actor.UserID == "" || !rootAuthor.Valid || rootAuthor.String != n.Actor.UserID) {
			return out, nil, ops.NotFound("comment")
		}
		// A guest sees, and answers in, only the threads of its own link. This is asked first, so every
		// id that is not such a thread (another link's, a writer's reply, one of another project, one
		// that does not exist) is the same 404, and nothing else about it is told.
		if n.Actor.Guest() && (!rootShare.Valid || rootShare.String != n.Actor.ShareID) {
			return out, nil, ops.NotFound("comment")
		}
		if pparent.Valid {
			return out, nil, ops.Invalid("parent_id", "Reply to the first comment of the thread, not to a reply.")
		}
		if n.Visibility != "" && n.Visibility != pvis {
			return out, nil, ops.Invalid("visibility", "A reply has the visibility of its thread: "+pvis+".")
		}
		vis = pvis
		if n.Anchor != nil {
			return out, nil, ops.Invalid("start", "A reply has no anchor: it belongs to its thread.")
		}
		if docID != "" && docID != pdoc.String {
			return out, nil, ops.Invalid("document_id", "That is not the document of the thread.")
		}
		docID, parent = pdoc.String, n.ParentID
		var replies int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE parent_id = ? AND visibility = ? AND deleted_at IS NULL`, parent, vis).Scan(&replies); err != nil {
			return out, nil, err
		}
		if replies >= MaxRepliesPerThread {
			return out, nil, ops.Conflict("This thread has the most replies it can hold.")
		}
	}

	// Only live comments count towards the limits: a deleted one is a tombstone that keeps the shape
	// of a thread, and the sweep removes the old ones, so deleting and writing again never runs out. Shared and private
	// comments are counted apart: a person's own notes never use what is left for the people on the project, and the
	// shared limits never show how many private ones exist.
	if vis == VisShared {
		var total int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE project_id = ? AND visibility = 'shared' AND deleted_at IS NULL`, n.ProjectID).Scan(&total); err != nil {
			return out, nil, err
		}
		if total >= MaxCommentsPerProject {
			return out, nil, ops.Conflict("This project holds the most comments it can (" + itoa(MaxCommentsPerProject) + "). Delete some first.")
		}
		if !n.Actor.Guest() && n.Actor.UserID != "" {
			// A member writing without end is bounded too (a guest has the hourly limit of their link).
			var recent int
			var oldest sql.NullString
			since := db.Format(db.NowTime().Add(-time.Hour))
			if err := tx.QueryRowContext(ctx, `SELECT count(*), min(created_at) FROM comments WHERE project_id = ? AND author_id = ? AND visibility = 'shared' AND created_at > ?`, n.ProjectID, n.Actor.UserID, since).Scan(&recent, &oldest); err != nil {
				return out, nil, err
			}
			if recent >= MaxCommentsPerMemberPerHour {
				wait := 60
				if t, err := db.Parse(oldest.String); err == nil {
					if w := int(t.Add(time.Hour).Sub(db.NowTime()).Seconds()) + 1; w > 0 {
						wait = w
					}
				}
				return out, nil, &ops.Error{Code: ops.CodeRateLimited, RetryAfterSeconds: wait,
					Message: "You have written " + itoa(MaxCommentsPerMemberPerHour) + " comments on this project in the last hour. Wait a little, or reply to fewer places at once."}
			}
		}
	} else {
		var mine int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE project_id = ? AND author_id = ? AND visibility = 'private' AND deleted_at IS NULL`, n.ProjectID, n.Actor.UserID).Scan(&mine); err != nil {
			return out, nil, err
		}
		if mine >= MaxPrivatePerAuthor {
			return out, nil, ops.Conflict("You have the most comments only you can see that a project can hold for one person (" + itoa(MaxPrivatePerAuthor) + "). Delete some first.")
		}
	}

	var anchor comanchor.Anchor
	if docID != "" {
		var deleted sql.NullString
		var content string
		var rev int
		err := tx.QueryRowContext(ctx, `SELECT content, revision, deleted_at FROM documents WHERE id = ? AND project_id = ?`, docID, n.ProjectID).Scan(&content, &rev, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return out, nil, ops.NotFound("document")
		}
		if err != nil {
			return out, nil, err
		}
		if n.DocumentOK != nil && !n.DocumentOK(docID) {
			return out, nil, ops.NotFound("document")
		}
		if deleted.Valid {
			return out, nil, ops.Conflict("This document is in the trash. Restore it to comment on it.")
		}
		if n.Anchor != nil {
			var anchored int
			if vis == VisShared {
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE document_id = ? AND visibility = 'shared' AND parent_id IS NULL AND anchor_state <> 'none' AND status = 'open' AND deleted_at IS NULL`, docID).Scan(&anchored); err != nil {
					return out, nil, err
				}
				if anchored >= comanchor.MaxAnchoredPerDocument {
					return out, nil, ops.Conflict("This document holds the most open anchored comments it can; resolve or delete some first.")
				}
			} else {
				if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE document_id = ? AND visibility = 'private' AND author_id = ? AND parent_id IS NULL AND anchor_state <> 'none' AND status = 'open' AND deleted_at IS NULL`, docID, n.Actor.UserID).Scan(&anchored); err != nil {
					return out, nil, err
				}
				if anchored >= MaxPrivateAnchoredPerAuthor {
					return out, nil, ops.Conflict("You have the most open anchored comments only you can see that a chapter can hold for one person (" + itoa(MaxPrivateAnchoredPerAuthor) + "); resolve or delete some first.")
				}
			}
			base := n.BaseRevision
			if base == 0 {
				base = rev
			}
			a, b, err := comanchor.Resolve(comanchor.Body(content), *n.Anchor, rev, base)
			switch {
			case errors.Is(err, comanchor.ErrStale):
				return out, nil, ops.ConflictWith("The document changed since you opened it, so offsets cannot be trusted. Name the text instead (quote, with the words around it).", map[string]any{"reason": "revision", "current_revision": rev})
			case err != nil:
				var re *comanchor.ErrRange
				if errors.As(err, &re) {
					return out, nil, ops.Invalid("quote", re.Msg)
				}
				return out, nil, err
			}
			anchor = comanchor.New(comanchor.Body(content), a, b, rev)
		}
	} else if n.Anchor != nil {
		return out, nil, ops.Invalid("document_id", "Name the document to anchor a comment to.")
	}
	if n.DocumentOK != nil && docID == "" && n.Actor.Guest() {
		return out, nil, ops.Invalid("document_id", "Name the document you are commenting on.")
	}

	// who is mentioned
	var mentioned []string
	if vis == VisPrivate && len(n.Mentions) > 0 {
		return out, nil, ops.Invalid("mentions", "A comment only you can see mentions no one: nobody would be told. Write it as a shared comment to mention someone.")
	}
	if !n.Actor.Guest() && vis == VisShared {
		people, err := commenters(ctx, tx, n.ProjectID)
		if err != nil {
			return out, nil, err
		}
		ok := map[string]bool{}
		for _, p := range people {
			ok[p.UserID] = true
		}
		seen := map[string]bool{}
		for _, id := range n.Mentions {
			if !ok[id] {
				return out, nil, ops.Invalid("mentions", "You can mention only the people on this project who can read comments.")
			}
			if !seen[id] {
				seen[id] = true
				mentioned = append(mentioned, id)
			}
		}
		for _, id := range mentionsIn(body, people) {
			if !seen[id] {
				seen[id] = true
				mentioned = append(mentioned, id)
			}
		}
		if len(mentioned) > MaxMentions {
			return out, nil, ops.Invalid("mentions", "A comment can mention at most "+itoa(MaxMentions)+" people.")
		}
	}

	now := db.Now()
	id := db.NewID("cmt")
	var sharedAt any // when it became visible to others; a private comment is not (yet)
	if vis == VisShared {
		sharedAt = now
	}
	state := comanchor.StateNone
	if anchor.State != "" {
		state = anchor.State
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO comments(id, project_id, document_id, parent_id, kind, author_id, share_id, guest_name, body, status,
		anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_prefix, anchor_suffix, anchor_hash, anchor_revision, created_at, updated_at, client_key, visibility, shared_at)
		VALUES (?,?,?,?,?,?,?,?,?, 'open', ?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, n.ProjectID, nullStr(docID), nullStr(parent), kind, nullStr(n.Actor.UserID), nullStr(n.Actor.ShareID), guest, body,
		state, anchor.Start, anchor.End, anchor.Quote, anchor.Original, anchor.Prefix, anchor.Suffix, anchor.Hash, anchor.Revision, now, now, nullStr(n.ClientKey), vis, sharedAt); err != nil {
		return out, nil, err
	}
	for _, uid := range mentioned {
		if _, err := tx.ExecContext(ctx, `INSERT INTO comment_mentions(comment_id, user_id) VALUES (?,?)`, id, uid); err != nil {
			return out, nil, err
		}
	}
	if n.Actor.Guest() {
		if _, err := tx.ExecContext(ctx, `UPDATE share_links SET comment_count = comment_count + 1 WHERE id = ?`, n.Actor.ShareID); err != nil {
			return out, nil, err
		}
	}

	thread := id
	if parent != "" {
		thread = parent
	}
	if vis == VisPrivate {
		// Nobody is told, not even the owner, and nothing is emitted: no live-stream line, no webhook.
		out.ID, out.DocumentID, out.ThreadID, out.Anchored = id, docID, thread, state == comanchor.StateActive
		return out, nil, nil
	}
	// Notifications: one per person per comment, the strongest reason first.
	notified := map[string]bool{n.Actor.UserID: true}
	give := func(user, kind string) error {
		if user == "" || notified[user] {
			return nil
		}
		notified[user] = true
		return notify(tx, user, n.ProjectID, kind, id, "", docID, n.Actor.UserID, now)
	}
	for _, uid := range mentioned {
		if err := give(uid, "mention"); err != nil {
			return out, nil, err
		}
	}
	if parent != "" {
		if err := give(rootAuthor.String, "reply"); err != nil {
			return out, nil, err
		}
	}
	var owner string
	_ = tx.QueryRowContext(ctx, `SELECT owner_id FROM projects WHERE id = ?`, n.ProjectID).Scan(&owner)
	if n.Actor.Guest() {
		// What guests write in one thread within an hour is one notification with a count, kept
		// apart from the person's others, so that guest traffic cannot bury their mentions.
		if err := notifyGuest(tx, owner, n.ProjectID, n.Actor.ShareID, thread, docID, now); err != nil {
			return out, nil, err
		}
	} else if err := give(owner, "comment"); err != nil {
		return out, nil, err
	}
	out.ID, out.DocumentID, out.ThreadID, out.Anchored = id, docID, thread, state == comanchor.StateActive
	ev := ops.Event{Type: "comment.created", ProjectID: n.ProjectID, DocumentID: docID, Payload: map[string]any{"comment_id": id, "thread_id": thread, "kind": kind}}
	return out, []ops.Event{ev}, nil
}

// roleOf is the caller's role on the project for hints and object checks.
func roleOf(c *ops.Ctx, projectID string) string {
	if c.Env == nil || c.Env.ProjectRole == nil {
		return ""
	}
	role, _ := c.Env.ProjectRole(c, c.P.UserID, projectID)
	return role
}

func trim(s string) string { return strings.TrimSpace(s) }

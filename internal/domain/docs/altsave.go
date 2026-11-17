package docs

import (
	"database/sql"
	"errors"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The write path that the alternatives, ghost and drag-back ops share: read the
// document row, check the revision and the lease as doc.update does, build the
// new text, and commit it in one transaction with everything a save does (the
// search text, links, mentions, attachment references, revisions, goals and the
// event). Alternatives are part of the document, so they are saved through the
// document, never beside it.

// head is a document row as the write ops read it.
type head struct {
	ProjectID, Title, Content, Hash, SynopsisShort string
	Revision, Words                                int
	ContentAt, UpdatedAt, By                       string
	Deleted                                        bool
}

func loadHead(tx *sql.Tx, id string) (head, error) {
	var h head
	var deleted, by sql.NullString
	err := tx.QueryRow(`SELECT project_id, title, content, content_hash, synopsis_short, revision, word_count, content_at, updated_at, content_by, deleted_at
		FROM documents WHERE id = ?`, id).
		Scan(&h.ProjectID, &h.Title, &h.Content, &h.Hash, &h.SynopsisShort, &h.Revision, &h.Words, &h.ContentAt, &h.UpdatedAt, &by, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return head{}, ops.NotFound("document")
	}
	h.By, h.Deleted = by.String, deleted.Valid
	return h, err
}

func (h head) state(id string) revisions.HeadState {
	return revisions.HeadState{DocumentID: id, Content: h.Content, Hash: h.Hash, Revision: h.Revision, Words: h.Words,
		ContentAt: h.ContentAt, UpdatedAt: h.UpdatedAt, By: h.By}
}

// guard is the preamble of every write: not in the trash, not held by another
// tab, and the revision the client last saw.
func (h head) guard(c *ops.Ctx, l *leases, id, tabID string, base int) error {
	if base < 1 {
		return ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	if h.Deleted {
		return ops.Conflict("This document is in the trash. Restore it before editing.")
	}
	if tabID != "" {
		if l.heldByOther(id, tabID) {
			return ops.ConflictWith("This document is open for writing in another tab.", map[string]any{"reason": "lease", "current_revision": h.Revision})
		}
		if !c.DryRun() { // a preview must not take the lease
			l.acquire(id, tabID, false)
		}
	}
	if base != h.Revision {
		return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": h.Revision})
	}
	return nil
}

// commitOpts say how a content write is recorded.
type commitOpts struct {
	// Reason, when set, writes the new text as a revision with that reason (the
	// text before it is kept first, as a save does).
	Reason, Note string
	// NoCredit: the words are not new (a choice between wordings, a ghost, text
	// moved from the stash), so the writer's goal earns nothing.
	NoCredit bool
	// Keep: variant wordings or text are lost by this write, so the previous text
	// is recorded as a revision whatever the autosave throttle says.
	Keep string
}

type committed struct {
	Revision   int
	Words      int
	Hash       string
	RevisionID string
}

// commit stores content as the document's new text. The caller has already
// checked the revision, built content (the canonical form: body and comment
// line) and written the alternatives tables.
func commit(c *ops.Ctx, tx *sql.Tx, id string, h head, content, now string, o commitOpts) (committed, error) {
	if len(content) > maxContent {
		return committed{}, ops.TooLarge()
	}
	hash := hashContent(content)
	words := text.WordCount(content)
	if o.Keep != "" {
		if _, err := revisions.EnsureRecorded(tx, h.state(id), revisions.ReasonSnapshot, o.Keep, now); err != nil {
			return committed{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE documents SET content = ?, content_hash = ?, revision = revision + 1, word_count = ?, char_count = ?, updated_at = ?,
		content_at = ?, content_by = ? WHERE id = ?`,
		content, hash, words, text.CharCount(content), now, now, nullIfEmpty(c.P.UserID), id); err != nil {
		return committed{}, err
	}
	if err := writeText(tx, id, h.ProjectID, h.Title, h.SynopsisShort, content); err != nil {
		return committed{}, err
	}
	if err := altstore.SyncGhosts(tx, id, h.ProjectID, content, c.P.UserID, now); err != nil {
		return committed{}, err
	}
	if err := afterSave(tx, prior{Title: h.Title, HeadState: h.state(id)}, h.ProjectID, id, h.Title, content, true, now); err != nil {
		return committed{}, err
	}
	if _, err := comanchor.AfterWrite(tx, id, h.Content, content, h.Revision+1, now); err != nil {
		return committed{}, err
	}
	if err := touchProject(tx, h.ProjectID, now); err != nil {
		return committed{}, err
	}
	if c.P.UserID != "" {
		if err := goals.RecordSave(tx, goals.Save{UserID: c.P.UserID, ProjectID: h.ProjectID, DocumentID: id,
			OldText: alts.BodyOf(h.Content), NewText: alts.BodyOf(content), OldWords: h.Words, NewWords: words, NoCredit: o.NoCredit}); err != nil && c.Env.Log != nil {
			c.Env.Log.Warn("goals: could not record a save", "err", err)
		}
	}
	out := committed{Revision: h.Revision + 1, Words: words, Hash: hash}
	if o.Reason != "" {
		rid, err := revisions.Record(tx, revisions.RecordIn{DocumentID: id, Revision: out.Revision, Content: content, Words: words,
			AuthorID: c.P.UserID, Reason: o.Reason, Note: o.Note, Now: now})
		if err != nil {
			return committed{}, err
		}
		out.RevisionID = rid
	}
	payload := map[string]any{"revision": out.Revision, "words": words, "content": true}
	if o.Reason != "" {
		payload["reason"] = o.Reason
	}
	c.Emit(ops.Event{Type: "doc.updated", ProjectID: h.ProjectID, DocumentID: id, Payload: payload})
	return out, nil
}

// splitItems divides a document's alternatives at a cut. The cut must lie
// outside every anchor; the tail's alternatives get new ids (they become the
// new document's). body is the text before the comment, head and tail the two
// halves of it after trimming, keepHead the length of the head before its line
// break was put back, and lead how much of the body the tail dropped.
func splitItems(stored []alts.Item, body, head, tail string, cut, keepHead, lead int, lim alts.Limits) (headItems, tailItems []alts.Item, err error) {
	ix := alts.NewIndex(body)
	cutU := ix.Unit(cut)
	for _, it := range stored {
		switch {
		case it.Orphan || it.End <= cutU:
			headItems = append(headItems, it)
		case it.Start >= cutU:
			tailItems = append(tailItems, it)
		default:
			return nil, nil, ops.Invalid("offset", "That position is inside an alternative wording ("+alts.Clip(it.ChosenText())+"). Resolve it first, or choose a position outside it.")
		}
	}
	ro := alts.Options{NewID: db.NewID, Limits: lim}
	if len(headItems) > 0 {
		headItems, _ = alts.Rebase(body, head, headItems, []alts.Edit{{Start: keepHead, End: len(body), NewLen: len(head) - keepHead}}, ro)
	}
	if len(tailItems) > 0 {
		tailItems, _ = alts.Rebase(body, tail, alts.Reid(tailItems, db.NewID), []alts.Edit{{Start: 0, End: lead, NewLen: 0}}, ro)
	}
	return headItems, tailItems, nil
}

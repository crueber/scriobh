package docs

import (
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/mentions"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// This file holds the Phase 2 ops that need the document write path or the
// tree, and so cannot live in the leaf packages: restoring a version, resolving
// (and auto-creating) a wiki link target, and the outline.

// registerStructure adds those ops, and the read ops of the leaf packages
// (revisions, links) that belong with documents.
func registerStructure(r *ops.Registry, l *leases) {
	revisions.Register(r)
	links.Register(r)
	ops.Register(r, ops.Op{Name: "revision.restore", Method: "POST", Path: "/api/v1/docs/{id}/revisions/restore",
		Desc: "Restore a version as the document's text. History is append-only: the current text is kept first if no version holds it, and the restore is written as a new version.",
		Auth: docref.Via(ops.ProjectEditor)},
		func(c *ops.Ctx, in RestoreIn) (RestoreOut, error) { return restoreRevision(c, l, in) })
	ops.Register(r, ops.Op{Name: "doc.resolve_link", Method: "POST", Path: "/api/v1/projects/{id}/links/resolve",
		Desc: "Find the document a wiki link title points at. With create, a missing target is created as a document (default a note, placed at the end of the top-level Notes folder if there is one, otherwise at the end of the tree). Readers follow links through doc.links.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, resolveLink)
	ops.Register(r, ops.Op{Name: "outline.get", Method: "GET", Path: "/api/v1/projects/{id}/outline",
		Desc: "The tree flattened in reading order for an outline: synopses, status, targets, word counts, scene metadata, and roll-up totals (words, target, documents and status counts) for every document that contains others.",
		Auth: ops.RequireProject(ops.ProjectReader)}, outline)
}

// ---- revision.restore ---------------------------------------------------------------

// RestoreIn restores one version of a document.
type RestoreIn struct {
	ID           string `path:"id" json:"-" desc:"Document id."`
	RevisionID   string `json:"revision_id" desc:"The version to restore."`
	BaseRevision int    `json:"base_revision" desc:"The document revision you last saw."`
	TabID        string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease. Leave empty for API clients."`
}

// DocID lets the registry find the owning project.
func (in RestoreIn) DocID() string { return in.ID }

// RestoreOut is the document after the restore.
type RestoreOut struct {
	Item       Doc    `json:"item"`
	RevisionID string `json:"revision_id,omitempty" desc:"The new version the restore wrote."`
	Unchanged  bool   `json:"unchanged" desc:"The document already had exactly this text, so nothing was written."`
}

func restoreRevision(c *ops.Ctx, l *leases, in RestoreIn) (RestoreOut, error) {
	if in.RevisionID == "" || len(in.RevisionID) > 64 {
		return RestoreOut{}, ops.Invalid("revision_id", "Name the version to restore.")
	}
	if in.BaseRevision < 1 {
		return RestoreOut{}, ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	var out RestoreOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := replaceBody(c, tx, l, in.ID, in.TabID, in.BaseRevision, func() (string, string, string, error) {
			ver, restored, err := revisions.Content(tx, in.ID, in.RevisionID)
			if err != nil {
				return "", "", "", err
			}
			note := "Restored the version from " + ver.CreatedAt
			if ver.Label != "" {
				note += " (" + ver.Label + ")"
			}
			return restored, note, in.RevisionID, nil
		})
		out.RevisionID, out.Unchanged = r.RevisionID, r.Unchanged
		return err
	})
	if err != nil {
		return RestoreOut{}, err
	}
	out.Item, err = fullDoc(c, c.DB(), in.ID)
	return out, err
}

// ReplaceBodyIn replaces a document's whole text by another, the way a restore does.
type ReplaceBodyIn struct {
	DocID        string
	ProjectID    string
	BaseRevision int
	TabID        string
	// Text is the new Markdown body.
	Text string
	// Note is the history note of the new version: where the text came from, never the text.
	Note string
}

// ReplaceBodyOut says what a replacement did.
type ReplaceBodyOut struct {
	RevisionID string // the version written; empty when nothing changed
	Revision   int    // the document's revision now
	Unchanged  bool   // the document already had exactly this text
}

// ReplaceBody writes text as the whole body of a document in the caller's transaction with the checks and the
// effects of revision.restore: the revision and the lease are checked, the text being replaced is kept first unless
// a version already holds it, the alternatives, ghosts, links, mentions, attachments and review comments follow the
// new text, the version is written with reason restore, and the writer's goal earns nothing for it (the words were
// written before, by somebody). It is for a caller that holds text it trusts to be the text meant (a stored edition
// of the book), after it has asked for the person's explicit action.
func ReplaceBody(c *ops.Ctx, tx *sql.Tx, l *Leases, in ReplaceBodyIn) (ReplaceBodyOut, error) {
	var pid string
	if err := tx.QueryRow(`SELECT project_id FROM documents WHERE id = ?`, in.DocID).Scan(&pid); err != nil || pid != in.ProjectID {
		return ReplaceBodyOut{}, ops.NotFound("document")
	}
	return replaceBody(c, tx, l, in.DocID, in.TabID, in.BaseRevision, func() (string, string, string, error) {
		return in.Text, in.Note, "", nil
	})
}

// replaceBody is the write of a restore. load gives the text, the history note and the version it came from (empty
// for text that is not a stored version); it is called after the document, the lease and the revision are checked.
func replaceBody(c *ops.Ctx, tx *sql.Tx, l *leases, docID, tabID string, base int, load func() (restored, note, from string, err error)) (ReplaceBodyOut, error) {
	var out ReplaceBodyOut
	{
		var (
			projectID, title, content, hash, synS string
			contentAt, updatedAt                  string
			rev, wordCount                        int
			deleted, contentBy                    sql.NullString
		)
		err := tx.QueryRow(`SELECT project_id, title, content, content_hash, synopsis_short, revision, word_count, content_at, updated_at, content_by, deleted_at
			FROM documents WHERE id = ?`, docID).
			Scan(&projectID, &title, &content, &hash, &synS, &rev, &wordCount, &contentAt, &updatedAt, &contentBy, &deleted)
		if errors.Is(err, sql.ErrNoRows) {
			return out, ops.NotFound("document")
		}
		if err != nil {
			return out, err
		}
		if deleted.Valid {
			return out, ops.Conflict("This document is in the trash. Restore it before restoring a version.")
		}
		if tabID != "" {
			if l.heldByOther(docID, tabID) {
				return out, ops.ConflictWith("This document is open for writing in another tab.", map[string]any{"reason": "lease", "current_revision": rev})
			}
			if !c.DryRun() { // a preview must not take the lease
				l.acquire(docID, tabID, false)
			}
		}
		if base != rev {
			return out, ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": rev})
		}
		restored, note, from, err := load()
		if err != nil {
			return out, err
		}
		if err := checkContent(restored); err != nil {
			return out, err
		}
		// The state of the alternatives follows the text: they become what the
		// restored text's comment says (none, when it has none).
		now := db.Now()
		plan, err := altstore.Reconcile(tx, altstore.In{DocID: docID, ProjectID: projectID, OldContent: content, NewContent: restored,
			Mode: altstore.ModeReplace, Actor: c.P.UserID, Now: now, Limits: altstore.LimitsOf(c.Env)})
		if err != nil {
			return out, err
		}
		restored = plan.Content
		out.Revision = rev
		if revisions.Hash(restored) == hash {
			out.Unchanged = true
			return out, nil
		}
		// Nothing is lost by a restore: the text being replaced is kept first
		// unless some version already holds it.
		if _, err := revisions.EnsureRecorded(tx, revisions.HeadState{DocumentID: docID, Content: content, Hash: hash, Revision: rev, Words: wordCount,
			ContentAt: contentAt, UpdatedAt: updatedAt, By: contentBy.String}, revisions.ReasonSnapshot, "Before restore", now); err != nil {
			return out, err
		}
		words := text.WordCount(restored)
		if _, err := tx.Exec(`UPDATE documents SET content = ?, content_hash = ?, revision = revision + 1, word_count = ?, char_count = ?, updated_at = ?,
			content_at = ?, content_by = ? WHERE id = ?`,
			restored, revisions.Hash(restored), words, text.CharCount(restored), now, now, nullIfEmpty(c.P.UserID), docID); err != nil {
			return out, err
		}
		if err := writeText(tx, docID, projectID, title, synS, restored); err != nil {
			return out, err
		}
		if err := altstore.SyncGhosts(tx, docID, projectID, restored, c.P.UserID, now); err != nil {
			return out, err
		}
		if err := links.Sync(tx, links.SyncIn{ProjectID: projectID, DocumentID: docID, Content: restored, Now: now}); err != nil {
			return out, err
		}
		if err := mentions.Sync(tx, projectID, docID); err != nil {
			return out, err
		}
		if err := attachments.SyncRefs(tx, attachments.RefDoc, docID, attachments.OwnerProject, projectID, restored); err != nil {
			return out, err
		}
		if _, err := comanchor.AfterWrite(tx, docID, content, restored, rev+1, now); err != nil {
			return out, err
		}
		// A restore changes the net words but earns no words added: the text was
		// written before. Statistics faults never fail the restore.
		if err := goals.RecordSave(tx, goals.Save{UserID: c.P.UserID, ProjectID: projectID, DocumentID: docID,
			OldText: alts.BodyOf(content), NewText: alts.BodyOf(restored), OldWords: wordCount, NewWords: words, NoCredit: true}); err != nil && c.Env.Log != nil {
			c.Env.Log.Warn("goals: could not record a restore", "err", err)
		}
		out.RevisionID, err = revisions.Record(tx, revisions.RecordIn{DocumentID: docID, Revision: rev + 1, Content: restored, Words: words,
			AuthorID: c.P.UserID, Reason: revisions.ReasonRestore, Note: note, Now: now})
		if err != nil {
			return out, err
		}
		out.Revision = rev + 1
		if err := touchProject(tx, projectID, now); err != nil {
			return out, err
		}
		// The same event a save emits: everything that follows documents
		// (the mirror, realtime) sees a restore as an ordinary content change.
		c.Emit(ops.Event{Type: "doc.updated", ProjectID: projectID, DocumentID: docID,
			Payload: map[string]any{"revision": rev + 1, "words": words, "content": true, "restored": true}})
		if from != "" {
			c.Emit(ops.Event{Type: "revision.restored", ProjectID: projectID, DocumentID: docID,
				Payload: map[string]any{"revision_id": out.RevisionID, "from": from}})
		}
		return out, c.FlushTo(tx)
	}
}

// ---- doc.resolve_link ---------------------------------------------------------------

// ResolveLinkIn looks up a wiki link's target.
type ResolveLinkIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Title     string `json:"title" desc:"The link text: what is between [[ and ]] before any |."`
	Create    bool   `json:"create,omitempty" desc:"Create the document when no live document has this title."`
	Kind      string `json:"kind,omitempty" desc:"Kind of document to create; default note." enum:"part,chapter,scene,note,front_matter,back_matter"`
}

// ProjectRef lets the registry authorize by project.
func (in ResolveLinkIn) ProjectRef() string { return in.ProjectID }

// ResolveLinkOut is the document a link points at.
type ResolveLinkOut struct {
	Found      bool  `json:"found"`
	Created    bool  `json:"created" desc:"The document was created by this call."`
	Ambiguous  bool  `json:"ambiguous" desc:"More than one live document has this title; Item is the first in reading order."`
	Candidates int   `json:"candidates" desc:"How many live documents have this title."`
	Item       *Meta `json:"item,omitempty"`
}

func resolveLink(c *ops.Ctx, in ResolveLinkIn) (ResolveLinkOut, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return ResolveLinkOut{}, err
	}
	if title == "" {
		return ResolveLinkOut{}, ops.Invalid("title", "Give the title to look for.")
	}
	kind := in.Kind
	if kind == "" {
		kind = "note"
	}
	if !oneOf(Kinds, kind) {
		return ResolveLinkOut{}, ops.Invalid("kind", "Unknown document kind.")
	}
	var out ResolveLinkOut
	var id string
	if !in.Create {
		t, found, err := links.Lookup(c.DB().Read(), in.ProjectID, title)
		if err != nil {
			return ResolveLinkOut{}, err
		}
		out.Found, out.Ambiguous, out.Candidates, id = found, t.Ambiguous, t.Candidates, t.ID
	} else {
		err = c.DB().WithTx(c, func(tx *sql.Tx) error {
			t, found, err := links.Lookup(tx, in.ProjectID, title)
			if err != nil {
				return err
			}
			if found {
				out.Found, out.Ambiguous, out.Candidates, id = true, t.Ambiguous, t.Candidates, t.ID
				return nil
			}
			parent, err := notesFolder(tx, in.ProjectID)
			if err != nil {
				return err
			}
			id = db.NewID("doc")
			if _, err := insertDoc(c, tx, CreateIn{ProjectID: in.ProjectID, Title: title, Kind: kind, ParentID: parent}, id, title, "", db.Now()); err != nil {
				return err
			}
			out.Found, out.Created, out.Candidates = true, true, 1
			c.Emit(ops.Event{Type: "doc.created", ProjectID: in.ProjectID, DocumentID: id, Payload: map[string]any{"kind": kind, "words": 0, "via": "link"}})
			return c.FlushTo(tx)
		})
		if err != nil {
			return ResolveLinkOut{}, err
		}
	}
	if id != "" {
		m, err := scanMeta(c.DB().Read().QueryRowContext(c, `SELECT `+metaCols+` FROM documents d WHERE d.id = ? AND d.project_id = ?`, id, in.ProjectID))
		if err != nil {
			return ResolveLinkOut{}, err
		}
		out.Item = &m
	}
	return out, nil
}

// notesFolder returns the id of the first top-level live document titled
// "Notes" (any case), where auto-created link targets are filed, or "" for the
// top level.
func notesFolder(tx *sql.Tx, projectID string) (string, error) {
	rows, err := tx.Query(`SELECT id, title FROM documents WHERE project_id = ? AND parent_id IS NULL AND deleted_at IS NULL ORDER BY position, id`, projectID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id, t string
		if err := rows.Scan(&id, &t); err != nil {
			return "", err
		}
		if text.FoldTitle(t) == "notes" {
			return id, nil
		}
	}
	return "", rows.Err()
}

// ---- outline.get --------------------------------------------------------------------

// OutlineIn asks for a project's outline.
type OutlineIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Sort      string `query:"sort" enum:"order,timeline,pov" desc:"order (default): the tree in reading order, nested by depth. timeline: the leaf documents (scenes) by story date, undated last, ties in reading order. pov: the leaf documents grouped by point of view (the linked character, else the free text, else none last), in reading order inside each group, with the groups listed in groups. The sorted modes are flat: each item carries its breadcrumb."`
}

// Validate checks the sort mode.
func (in *OutlineIn) Validate() error {
	switch in.Sort {
	case "", "order", "timeline", "pov":
		return nil
	}
	return ops.Invalid("sort", "Sort by order, timeline or pov.")
}

// ProjectRef lets the registry authorize by project.
func (in OutlineIn) ProjectRef() string { return in.ProjectID }

// StatusCounts counts documents by status.
type StatusCounts struct {
	Idea    int `json:"idea"`
	Outline int `json:"outline"`
	Draft   int `json:"draft"`
	Revise  int `json:"revise"`
	Done    int `json:"done"`
}

func (s *StatusCounts) add(o StatusCounts) {
	s.Idea += o.Idea
	s.Outline += o.Outline
	s.Draft += o.Draft
	s.Revise += o.Revise
	s.Done += o.Done
}

func (s *StatusCounts) count(status string) {
	switch status {
	case "idea":
		s.Idea++
	case "outline":
		s.Outline++
	case "draft":
		s.Draft++
	case "revise":
		s.Revise++
	case "done":
		s.Done++
	}
}

// Rollup totals a document and everything inside it.
type Rollup struct {
	Documents   int          `json:"documents" desc:"The document itself and every document inside it."`
	Words       int          `json:"words" desc:"Its own words plus those of everything inside it."`
	TargetWords int          `json:"target_words" desc:"Its own target when it has one; otherwise the sum of the targets of the documents directly inside it."`
	Progress    float64      `json:"progress" desc:"Words over target, 0 when there is no target. It can exceed 1."`
	Status      StatusCounts `json:"status" desc:"Documents by status, itself included."`
}

// OutlineItem is one row of the outline.
type OutlineItem struct {
	ID               string  `json:"id"`
	ParentID         string  `json:"parent_id"`
	Depth            int     `json:"depth" desc:"0 at the top level."`
	ChildCount       int     `json:"child_count" desc:"Live documents directly inside."`
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	Status           string  `json:"status"`
	SynopsisShort    string  `json:"synopsis_short"`
	SynopsisLong     string  `json:"synopsis_long"`
	Position         float64 `json:"position"`
	Revision         int     `json:"revision"`
	WordCount        int     `json:"word_count" desc:"Words in this document's own text."`
	TargetWords      int     `json:"target_words" desc:"This document's own target; 0 for none."`
	Progress         float64 `json:"progress" desc:"Own words over own target, 0 when there is no target."`
	Pov              string  `json:"pov" desc:"Point of view as free text: the display fallback."`
	Location         string  `json:"location" desc:"Location as free text: the display fallback."`
	StoryDate        string  `json:"story_date"`
	StoryDateEnd     string  `json:"story_date_end"`
	IncludeInCompile bool    `json:"include_in_compile"`
	UpdatedAt        string  `json:"updated_at"`
	Rollup           Rollup  `json:"rollup"`
	// Entity references, for the entities the caller may read.
	PovEntity      *vocab.Brief `json:"pov_entity,omitempty" desc:"The character the point of view is linked to, when the caller may read it."`
	LocationEntity *vocab.Brief `json:"location_entity,omitempty"`
	StoryDated     bool         `json:"story_dated" desc:"story_date reads as a date (YYYY, YYYY-MM, YYYY-MM-DD, optionally with a time); otherwise the timeline treats the scene as undated."`
	Breadcrumb     []Crumb      `json:"breadcrumb,omitempty" desc:"Only in the sorted modes: the ancestors, nearest last."`
}

// OutlineGroup is one point of view in sort=pov.
type OutlineGroup struct {
	Key       string       `json:"key" desc:"The character's id, else the folded free text, else empty for no point of view."`
	Label     string       `json:"label" desc:"The name or text to show; empty for no point of view."`
	Entity    *vocab.Brief `json:"entity,omitempty"`
	Documents int          `json:"documents"`
	Words     int          `json:"words"`
	IDs       []string     `json:"ids" desc:"The group's documents, in the order of items."`
}

// OutlineOut is the outline in reading order.
type OutlineOut struct {
	Items  []OutlineItem  `json:"items" desc:"Every live document in reading order; nest by depth or parent_id. In sort=timeline and sort=pov, only the leaf documents, flat."`
	Totals Rollup         `json:"totals" desc:"The whole project."`
	Sort   string         `json:"sort" desc:"The order of items: order, timeline or pov."`
	Groups []OutlineGroup `json:"groups,omitempty" desc:"With sort=pov: the points of view in the order of items, no point of view last."`
}

func progress(words, target int) float64 {
	if target <= 0 {
		return 0
	}
	return math.Round(float64(words)/float64(target)*1000) / 1000
}

func outline(c *ops.Ctx, in OutlineIn) (OutlineOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, COALESCE(parent_id,''), kind, title, status, synopsis_short, synopsis_long, position, revision,
			word_count, target_words, pov, location, story_date, story_date_end, include_in_compile, updated_at
		FROM documents WHERE project_id = ? AND deleted_at IS NULL`, in.ProjectID)
	if err != nil {
		return OutlineOut{}, err
	}
	defer rows.Close()
	byID := map[string]*OutlineItem{}
	var nodes []node
	for rows.Next() {
		var it OutlineItem
		var include int
		if err := rows.Scan(&it.ID, &it.ParentID, &it.Kind, &it.Title, &it.Status, &it.SynopsisShort, &it.SynopsisLong, &it.Position, &it.Revision,
			&it.WordCount, &it.TargetWords, &it.Pov, &it.Location, &it.StoryDate, &it.StoryDateEnd, &include, &it.UpdatedAt); err != nil {
			return OutlineOut{}, err
		}
		it.IncludeInCompile = include == 1
		it.Progress = progress(it.WordCount, it.TargetWords)
		byID[it.ID] = &it
		nodes = append(nodes, node{ID: it.ID, ParentID: it.ParentID, Kind: it.Kind, Title: it.Title, Position: it.Position})
	}
	if err := rows.Err(); err != nil {
		return OutlineOut{}, err
	}
	ord := order(nodes)
	// A parent that is not live (trashed, or missing) leaves its children at the top.
	for _, it := range byID {
		if _, ok := byID[it.ParentID]; !ok {
			it.ParentID = ""
		}
	}
	for _, it := range byID {
		if it.ParentID != "" {
			byID[it.ParentID].ChildCount++
		}
	}
	// Depth: reading order puts a parent before its children.
	for _, id := range ord {
		if it := byID[id]; it.ParentID != "" {
			it.Depth = byID[it.ParentID].Depth + 1
		}
	}
	// Roll-ups: reading order in reverse finishes children before their parent.
	childTarget := make(map[string]int, len(ord))
	var totals Rollup
	for i := len(ord) - 1; i >= 0; i-- {
		it := byID[ord[i]]
		r := &it.Rollup
		r.Documents++
		r.Words += it.WordCount
		r.Status.count(it.Status)
		r.TargetWords = it.TargetWords
		if r.TargetWords == 0 {
			r.TargetWords = childTarget[it.ID]
		}
		r.Progress = progress(r.Words, r.TargetWords)
		if it.ParentID != "" {
			p := byID[it.ParentID]
			p.Rollup.Documents += r.Documents
			p.Rollup.Words += r.Words
			p.Rollup.Status.add(r.Status)
			childTarget[p.ID] += r.TargetWords
		} else {
			totals.Documents += r.Documents
			totals.Words += r.Words
			totals.TargetWords += r.TargetWords
			totals.Status.add(r.Status)
		}
	}
	totals.Progress = progress(totals.Words, totals.TargetWords)
	out := OutlineOut{Items: make([]OutlineItem, 0, len(ord)), Totals: totals, Sort: "order"}
	for _, id := range ord {
		out.Items = append(out.Items, *byID[id])
	}
	refs, err := scenes.SceneRefs(c, c.DB().Read(), vocab.ReaderOf(c.P), in.ProjectID)
	if err != nil {
		return OutlineOut{}, err
	}
	for i := range out.Items {
		it := &out.Items[i]
		it.StoryDated = storydate.SortKey(it.StoryDate) != "~"
		if r, ok := refs[it.ID]; ok {
			it.PovEntity, it.LocationEntity = r.Pov, r.Location
		}
	}
	if in.Sort == "timeline" || in.Sort == "pov" {
		sortOutline(&out, byID, in.Sort)
	}
	return out, nil
}

// sortOutline turns the outline into the flat, sorted list of its leaf
// documents. Reading order breaks every tie, so the result is deterministic.
func sortOutline(out *OutlineOut, byID map[string]*OutlineItem, mode string) {
	// Free text that is exactly a linked character's name groups with that
	// character: "Elara" typed on one scene and linked on another is one point
	// of view.
	linked := map[string]*vocab.Brief{}
	for _, it := range out.Items {
		if it.PovEntity != nil {
			linked[text.FoldTitle(it.PovEntity.Name)] = it.PovEntity
		}
	}
	group := func(it OutlineItem) (key, label string, ent *vocab.Brief) {
		switch {
		case it.PovEntity != nil:
			return it.PovEntity.ID, it.PovEntity.Name, it.PovEntity
		case strings.TrimSpace(it.Pov) != "":
			if e, ok := linked[text.FoldTitle(it.Pov)]; ok {
				return e.ID, e.Name, e
			}
			return "text:" + text.FoldTitle(it.Pov), strings.TrimSpace(it.Pov), nil
		}
		return "", "", nil
	}
	type leaf struct {
		it         OutlineItem
		index      int
		key, label string
	}
	var leaves []leaf
	for i, it := range out.Items {
		if it.ChildCount > 0 {
			continue
		}
		crumbs := []Crumb{}
		for p, hops := it.ParentID, 0; p != "" && hops < 64; hops++ {
			a, ok := byID[p]
			if !ok {
				break
			}
			crumbs = append([]Crumb{{ID: a.ID, Title: a.Title, Kind: a.Kind}}, crumbs...)
			p = a.ParentID
		}
		it.Breadcrumb = crumbs
		l := leaf{it: it, index: i}
		if mode == "timeline" {
			l.key = storydate.SortKey(it.StoryDate)
		} else {
			l.key, l.label, _ = group(it)
			l.label = text.FoldTitle(l.label)
		}
		leaves = append(leaves, l)
	}
	sort.SliceStable(leaves, func(i, j int) bool {
		a, b := leaves[i], leaves[j]
		if mode == "pov" {
			// No point of view sorts after every group.
			if (a.key == "") != (b.key == "") {
				return b.key == ""
			}
			if a.key != b.key {
				return a.label < b.label || (a.label == b.label && a.key < b.key)
			}
			return a.index < b.index
		}
		if a.key != b.key {
			return a.key < b.key
		}
		return a.index < b.index
	})
	out.Sort = mode
	out.Items = out.Items[:0]
	for _, l := range leaves {
		out.Items = append(out.Items, l.it)
	}
	if mode == "pov" {
		idx := map[string]int{}
		for _, it := range out.Items {
			k, label, ent := group(it)
			i, ok := idx[k]
			if !ok {
				i = len(out.Groups)
				idx[k] = i
				out.Groups = append(out.Groups, OutlineGroup{Key: k, Label: label, Entity: ent, IDs: []string{}})
			}
			out.Groups[i].Documents++
			out.Groups[i].Words += it.WordCount
			out.Groups[i].IDs = append(out.Groups[i].IDs, it.ID)
		}
	}
}

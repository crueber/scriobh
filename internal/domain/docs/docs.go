// Package docs holds the document ops: the manuscript tree, saving with
// optimistic concurrency and a per-tab lease, moving, trashing and restoring.
//
// Every query is scoped by the project the registry already authorized, never
// by an id taken from the request body: an id from another project is simply
// not found.
package docs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/domain/scenes"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Kinds a manuscript document may have.
var Kinds = []string{"part", "chapter", "scene", "note", "front_matter", "back_matter"}

// Statuses a document may have.
var Statuses = []string{"idea", "outline", "draft", "revise", "done"}

// Long-document guardrails (F-275): warn, then switch the editor to read-only.
const (
	WarnWords     = 15000
	ReadOnlyWords = 25000
)

const (
	maxContent  = 2 << 20 // bytes of Markdown in one document
	maxTitle    = 300
	maxSynopsis = 300
)

var errNoSibling = ops.Invalid("after", "The document to place after is not in that folder.")

// Meta is a document without its body: what the tree needs.
type Meta struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	ParentID      string  `json:"parent_id"`
	Kind          string  `json:"kind"`
	Title         string  `json:"title"`
	SynopsisShort string  `json:"synopsis_short"`
	Status        string  `json:"status"`
	Position      float64 `json:"position"`
	Revision      int     `json:"revision"`
	WordCount     int     `json:"word_count"`
	CharCount     int     `json:"char_count"`
	TargetWords   int     `json:"target_words" desc:"The document's own word target; 0 for none."`
	Progress      float64 `json:"progress" desc:"Its words over its own target, 0 when there is no target; it can exceed 1. outline.get adds the roll-up of a chapter's own sections."`
	Trashed       bool    `json:"trashed"`
	UpdatedAt     string  `json:"updated_at"`
	// Scene metadata. Pov and Location are free text: the display fallback.
	// PovEntity and LocationEntity say which entity of the bible each means,
	// when one is linked (scene.set_pov, scene.link_entities) and the caller
	// may read it; they are filled by doc.list and outline.get only.
	// StoryDate and StoryDateEnd are when the scene happens in the story;
	// YYYY, YYYY-MM, YYYY-MM-DD or with a time is read by the timeline, and
	// anything else is kept as written and treated as undated.
	Pov            string       `json:"pov"`
	Location       string       `json:"location"`
	PovEntity      *vocab.Brief `json:"pov_entity,omitempty"`
	LocationEntity *vocab.Brief `json:"location_entity,omitempty"`
	StoryDate      string       `json:"story_date"`
	StoryDateEnd   string       `json:"story_date_end"`
}

// Crumb is one ancestor in a breadcrumb.
type Crumb struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// Doc is a full document.
type Doc struct {
	Meta
	Content          string  `json:"content"`
	SynopsisLong     string  `json:"synopsis_long"`
	IncludeInCompile bool    `json:"include_in_compile"`
	ContentHash      string  `json:"content_hash"`
	Guard            string  `json:"guard,omitempty" desc:"warn or readonly when the document is too long for one page"`
	PrevID           string  `json:"prev_id,omitempty"`
	NextID           string  `json:"next_id,omitempty"`
	Ancestors        []Crumb `json:"ancestors"`
}

// ---- inputs and outputs ---------------------------------------------------

// ListIn lists a project's documents.
type ListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Trash     bool   `query:"trash" desc:"List the trash instead of the live tree."`
}

func (in ListIn) ProjectRef() string { return in.ProjectID }

// ListOut is the flat tree; clients nest it by parent_id and position.
type ListOut struct {
	Items []Meta   `json:"items"`
	Order []string `json:"order" desc:"Document ids in reading order."`
}

// CreateIn creates a document.
type CreateIn struct {
	ProjectID     string  `path:"id" json:"-" desc:"Project id."`
	Title         string  `json:"title,omitempty"`
	Kind          string  `json:"kind,omitempty" enum:"part,chapter,scene,note,front_matter,back_matter"`
	ParentID      string  `json:"parent_id,omitempty" desc:"Folder to create in; empty for the top level."`
	After         *string `json:"after,omitempty" desc:"Sibling to place after; empty string for first; omit for last."`
	Content       string  `json:"content,omitempty" desc:"Markdown body."`
	SynopsisShort string  `json:"synopsis_short,omitempty"`
}

func (in CreateIn) ProjectRef() string { return in.ProjectID }

// DocIn names one document.
type DocIn struct {
	ID string `path:"id" json:"-" desc:"Document id."`
}

// DocID lets the registry find the owning project.
func (in DocIn) DocID() string { return in.ID }

// Item wraps one document.
type Item struct {
	Item Doc `json:"item"`
	// Notices say what the server did differently from what was asked, in words
	// for the person (for instance that an older copy of the alternatives
	// comment was not applied).
	Notices []string `json:"notices,omitempty"`
}

// UpdateIn saves a document. BaseRevision is the revision the client last saw;
// a mismatch is a 409 with the current revision.
type UpdateIn struct {
	ID               string  `path:"id" json:"-" desc:"Document id."`
	BaseRevision     int     `json:"base_revision" desc:"The revision this change is based on."`
	TabID            string  `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease. Leave empty for API clients."`
	Title            *string `json:"title,omitempty"`
	Content          *string `json:"content,omitempty" desc:"Markdown body."`
	SynopsisShort    *string `json:"synopsis_short,omitempty"`
	SynopsisLong     *string `json:"synopsis_long,omitempty"`
	Status           *string `json:"status,omitempty" enum:"idea,outline,draft,revise,done"`
	TargetWords      *int    `json:"target_words,omitempty"`
	IncludeInCompile *bool   `json:"include_in_compile,omitempty"`
	Pov              *string `json:"pov,omitempty" desc:"Point-of-view character, free text."`
	Location         *string `json:"location,omitempty" desc:"Where the scene happens, free text."`
	StoryDate        *string `json:"story_date,omitempty" desc:"When the scene happens in the story; YYYY-MM-DD recommended."`
	StoryDateEnd     *string `json:"story_date_end,omitempty" desc:"When the scene ends, if it spans time."`
}

// DocID lets the registry find the owning project.
func (in UpdateIn) DocID() string { return in.ID }

// MoveIn re-parents and/or reorders a document.
type MoveIn struct {
	ID       string  `path:"id" json:"-" desc:"Document id."`
	ParentID string  `json:"parent_id" desc:"New folder; empty for the top level."`
	After    *string `json:"after,omitempty" desc:"Sibling to place after; empty string for first; omit for last."`
}

// DocID lets the registry find the owning project.
func (in MoveIn) DocID() string { return in.ID }

// LeaseIn asks for the right to write a document from one browser tab.
type LeaseIn struct {
	ID      string `path:"id" json:"-" desc:"Document id."`
	TabID   string `json:"tab_id" desc:"Browser tab id."`
	Take    bool   `json:"take,omitempty" desc:"Take the lease from another tab."`
	Release bool   `json:"release,omitempty" desc:"Give the lease up (the tab is closing). Does nothing if another tab holds it."`
}

// DocID lets the registry find the owning project.
func (in LeaseIn) DocID() string { return in.ID }

// LeaseOut says whether the tab holds the lease.
type LeaseOut struct {
	Held      bool   `json:"held"`
	ExpiresAt string `json:"expires_at"`
}

// SplitIn cuts a document in two at a position in its text.
type SplitIn struct {
	ID           string `path:"id" json:"-" desc:"Document id."`
	BaseRevision int    `json:"base_revision" desc:"The revision you last saw."`
	Offset       int    `json:"offset" desc:"Where to cut, in UTF-16 code units from the start of the stored text, with every CRLF counted as one character (the text as an editor shows it). What JavaScript's string index gives."`
	Title        string `json:"title,omitempty" desc:"Title of the new document that takes the text after the cut. Default: untitled."`
	TabID        string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease. Leave empty for API clients."`
}

// DocID lets the registry find the owning project.
func (in SplitIn) DocID() string { return in.ID }

// SplitOut holds both halves.
type SplitOut struct {
	Head Doc `json:"head"`
	Tail Doc `json:"tail"`
}

// ConflictIn stores the local text of a tab that lost a save conflict.
type ConflictIn struct {
	ID           string `path:"id" json:"-" desc:"Document id."`
	Content      string `json:"content" desc:"The local Markdown that could not be saved."`
	BaseRevision int    `json:"base_revision"`
}

// DocID lets the registry find the owning project.
func (in ConflictIn) DocID() string { return in.ID }

// ConflictOut names the saved conflict copy.
type ConflictOut struct {
	Path string `json:"path" desc:"Path of the conflict copy, relative to the data directory."`
}

// ---- registration ----------------------------------------------------------

// resolveDoc finds the project that owns the document an input names. Trashed
// documents still resolve so they can be restored.
func resolveDoc(ctx context.Context, env *ops.Env, in any) (string, bool) {
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

// Register adds the document ops.
func Register(r *ops.Registry) {
	l := newLeases()
	if r.Env() != nil {
		r.Env().SetService(leasesService, l)
	}
	viaDoc := func(role string) ops.Auth { return ops.RequireProjectVia(role, resolveDoc) }
	ops.Register(r, ops.Op{Name: "doc.list", Method: "GET", Path: "/api/v1/projects/{id}/docs",
		Desc: "List a project's documents as a flat tree, with reading order.", Auth: ops.RequireProject(ops.ProjectReader)}, list)
	ops.Register(r, ops.Op{Name: "doc.create", Method: "POST", Path: "/api/v1/projects/{id}/docs", Status: 201,
		Desc: "Create a document (part, chapter, scene or note) in a project.", Auth: ops.RequireProject(ops.ProjectEditor)}, create)
	ops.Register(r, ops.Op{Name: "doc.get", Method: "GET", Path: "/api/v1/docs/{id}",
		Desc: "Get one document with its body, neighbours and breadcrumb.", Auth: viaDoc(ops.ProjectReader)}, get)
	ops.Register(r, ops.Op{Name: "doc.update", Method: "PATCH", Path: "/api/v1/docs/{id}",
		Desc: "Save a document. Send the revision you last saw; a stale revision is a 409.", Auth: viaDoc(ops.ProjectEditor)},
		func(c *ops.Ctx, in UpdateIn) (Item, error) { return update(c, l, in) })
	ops.Register(r, ops.Op{Name: "doc.move", Method: "POST", Path: "/api/v1/docs/{id}/move",
		Desc: "Move a document to another folder or position.", Auth: viaDoc(ops.ProjectEditor)}, move)
	ops.Register(r, ops.Op{Name: "doc.trash", Method: "POST", Path: "/api/v1/docs/{id}/trash",
		Desc: "Move a document and everything inside it to the trash.", Auth: viaDoc(ops.ProjectEditor)}, trash)
	ops.Register(r, ops.Op{Name: "doc.restore", Method: "POST", Path: "/api/v1/docs/{id}/restore",
		Desc: "Restore a trashed document and what was trashed with it.", Auth: viaDoc(ops.ProjectEditor)}, restore)
	ops.Register(r, ops.Op{Name: "doc.lease", Method: "POST", Path: "/api/v1/docs/{id}/lease",
		Desc: "Ask for the single-writer lease on a document for one browser tab.", Auth: viaDoc(ops.ProjectEditor), NoDryRun: true},
		func(c *ops.Ctx, in LeaseIn) (LeaseOut, error) { return leaseOp(c, l, in) })
	ops.Register(r, ops.Op{Name: "doc.split", Method: "POST", Path: "/api/v1/docs/{id}/split", Status: 201,
		Desc: "Cut a document in two at a text position. The text after the cut becomes the next document at the same level.", Auth: viaDoc(ops.ProjectEditor)},
		func(c *ops.Ctx, in SplitIn) (SplitOut, error) { return split(c, l, in) })
	ops.Register(r, ops.Op{Name: "doc.conflict", Method: "POST", Path: "/api/v1/docs/{id}/conflict",
		Desc: "Keep a copy of local text that lost a save conflict, in the data directory.", Auth: viaDoc(ops.ProjectEditor), Status: 201}, conflict)
	registerStructure(r, l)    // revisions, links and the outline (structure.go)
	registerAlternatives(r, l) // alternatives and ghosts (alt_ops.go, ghost_ops.go)
}

// ---- shared helpers ----------------------------------------------------------

const metaCols = `d.id, d.project_id, COALESCE(d.parent_id,''), d.kind, d.title, d.synopsis_short, d.status, d.position,
	d.revision, d.word_count, d.char_count, d.target_words, d.deleted_at IS NOT NULL, d.updated_at,
	d.pov, d.location, d.story_date, d.story_date_end`

type scanner interface{ Scan(...any) error }

func scanMeta(s scanner) (Meta, error) {
	var m Meta
	err := s.Scan(&m.ID, &m.ProjectID, &m.ParentID, &m.Kind, &m.Title, &m.SynopsisShort, &m.Status, &m.Position,
		&m.Revision, &m.WordCount, &m.CharCount, &m.TargetWords, &m.Trashed, &m.UpdatedAt,
		&m.Pov, &m.Location, &m.StoryDate, &m.StoryDateEnd)
	m.Progress = progress(m.WordCount, m.TargetWords)
	return m, err
}

func hashContent(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func guard(words int) string {
	switch {
	case words >= ReadOnlyWords:
		return "readonly"
	case words >= WarnWords:
		return "warn"
	}
	return ""
}

func oneOf(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if utf8.RuneCountInString(s) > maxTitle {
		return "", ops.Invalid("title", fmt.Sprintf("Titles are limited to %d characters.", maxTitle))
	}
	return s, nil
}

func cleanSynopsis(s string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if utf8.RuneCountInString(s) > maxSynopsis {
		return "", ops.Invalid("synopsis_short", fmt.Sprintf("The one-line synopsis is limited to %d characters.", maxSynopsis))
	}
	return s, nil
}

func checkContent(s string) error {
	if len(s) > maxContent {
		return ops.TooLarge()
	}
	if !utf8.ValidString(s) {
		return ops.Invalid("content", "The text is not valid UTF-8.")
	}
	return nil
}

// writeText keeps the search text in step with the document, in the same
// transaction.
func writeText(tx *sql.Tx, docID, projectID, title, synopsis, content string) error {
	return writeTextBody(tx, docID, projectID, title, synopsis, text.SearchBody(content))
}

// writeTextBody is writeText for a search body that is already derived.
func writeTextBody(tx *sql.Tx, docID, projectID, title, synopsis, body string) error {
	_, err := tx.Exec(`INSERT INTO doc_text(document_id, project_id, title, synopsis, body) VALUES (?,?,?,?,?)
		ON CONFLICT(document_id) DO UPDATE SET title = excluded.title, synopsis = excluded.synopsis, body = excluded.body`,
		docID, projectID, title, synopsis, body)
	return err
}

func touchProject(tx *sql.Tx, projectID, now string) error {
	_, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now, projectID)
	return err
}

// fullDoc loads a document with its neighbours and breadcrumb.
func fullDoc(ctx context.Context, d *db.DB, id string) (Doc, error) {
	var doc Doc
	var include int
	row := d.Read().QueryRowContext(ctx, `SELECT `+metaCols+`, d.content, d.synopsis_long, d.include_in_compile, d.content_hash
		FROM documents d WHERE d.id = ?`, id)
	var m Meta
	err := row.Scan(&m.ID, &m.ProjectID, &m.ParentID, &m.Kind, &m.Title, &m.SynopsisShort, &m.Status, &m.Position,
		&m.Revision, &m.WordCount, &m.CharCount, &m.TargetWords, &m.Trashed, &m.UpdatedAt,
		&m.Pov, &m.Location, &m.StoryDate, &m.StoryDateEnd,
		&doc.Content, &doc.SynopsisLong, &include, &doc.ContentHash)
	m.Progress = progress(m.WordCount, m.TargetWords)
	if errors.Is(err, sql.ErrNoRows) {
		return Doc{}, ops.NotFound("document")
	}
	if err != nil {
		return Doc{}, err
	}
	doc.Meta = m
	doc.IncludeInCompile = include == 1
	doc.Guard = guard(m.WordCount)
	nodes, err := loadNodes(ctx, d.Read(), m.ProjectID)
	if err != nil {
		return Doc{}, err
	}
	ord := order(nodes)
	for i, nid := range ord {
		if nid == id {
			if i > 0 {
				doc.PrevID = ord[i-1]
			}
			if i+1 < len(ord) {
				doc.NextID = ord[i+1]
			}
		}
	}
	byID := map[string]node{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	doc.Ancestors = []Crumb{}
	for p, hops := byID[id].ParentID, 0; p != "" && hops < 64; hops++ {
		n, ok := byID[p]
		if !ok {
			break
		}
		doc.Ancestors = append([]Crumb{{ID: n.ID, Title: n.Title, Kind: n.Kind}}, doc.Ancestors...)
		p = n.ParentID
	}
	return doc, nil
}

// ---- ops ----------------------------------------------------------------------

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	q := `SELECT ` + metaCols + ` FROM documents d WHERE d.project_id = ? AND d.deleted_at IS `
	if in.Trash {
		q += `NOT NULL ORDER BY d.deleted_at DESC, d.position`
	} else {
		q += `NULL ORDER BY d.position, d.id`
	}
	rows, err := c.DB().Read().QueryContext(c, q, in.ProjectID)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	out := ListOut{Items: []Meta{}, Order: []string{}}
	var nodes []node
	for rows.Next() {
		m, err := scanMeta(rows)
		if err != nil {
			return ListOut{}, err
		}
		out.Items = append(out.Items, m)
		nodes = append(nodes, node{ID: m.ID, ParentID: m.ParentID, Kind: m.Kind, Title: m.Title, Position: m.Position})
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	if !in.Trash {
		out.Order = order(nodes)
		refs, err := scenes.SceneRefs(c, c.DB().Read(), vocab.ReaderOf(c.P), in.ProjectID)
		if err != nil {
			return ListOut{}, err
		}
		for i := range out.Items {
			if r, ok := refs[out.Items[i].ID]; ok {
				out.Items[i].PovEntity, out.Items[i].LocationEntity = r.Pov, r.Location
			}
		}
	}
	return out, nil
}

func create(c *ops.Ctx, in CreateIn) (Item, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Item{}, err
	}
	syn, err := cleanSynopsis(in.SynopsisShort)
	if err != nil {
		return Item{}, err
	}
	if in.Kind == "" {
		in.Kind = "chapter"
	}
	if !oneOf(Kinds, in.Kind) {
		return Item{}, ops.Invalid("kind", "Unknown document kind.")
	}
	if err := checkContent(in.Content); err != nil {
		return Item{}, err
	}
	id := db.NewID("doc")
	now := db.Now()
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		words, err := insertDoc(c, tx, in, id, title, syn, now)
		if err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "doc.created", ProjectID: in.ProjectID, DocumentID: id, Payload: map[string]any{"kind": in.Kind, "words": words}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, err
	}
	d, err := fullDoc(c, c.DB(), id)
	return Item{Item: d}, err
}

// insertDoc writes a new document inside the caller's transaction: placement,
// the row, the search text, its links, and the project's updated time. in has
// already been validated and cleaned.
func insertDoc(c *ops.Ctx, tx *sql.Tx, in CreateIn, id, title, syn, now string) (words int, err error) {
	if in.ParentID != "" {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, in.ParentID, in.ProjectID).Scan(&n); err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, ops.Invalid("parent_id", "That folder does not exist in this project.")
		}
	}
	after, atEnd := "", in.After == nil
	if in.After != nil {
		after = *in.After
	}
	pos, err := placeAmong(c, tx, in.ProjectID, in.ParentID, after, "", atEnd)
	if err != nil {
		return 0, err
	}
	var parent any
	if in.ParentID != "" {
		parent = in.ParentID
	}
	// A text that carries an alternatives comment is read into the tables, with
	// new ids, and stored in its canonical form.
	plan, err := altstore.PlanNew(in.Content, c.P.UserID, now, altstore.LimitsOf(c.Env))
	if err != nil {
		return 0, err
	}
	in.Content = plan.Content
	if err := checkContent(in.Content); err != nil {
		return 0, err
	}
	words = text.WordCount(in.Content)
	if _, err := tx.Exec(`INSERT INTO documents(id, project_id, parent_id, kind, title, content, content_hash, synopsis_short,
		position, revision, word_count, char_count, content_at, content_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.ProjectID, parent, in.Kind, title, in.Content, hashContent(in.Content), syn, pos, 1, words, text.CharCount(in.Content), now, nullIfEmpty(c.P.UserID), now, now); err != nil {
		return 0, err
	}
	if err := plan.Apply(tx, id, in.ProjectID, c.P.UserID, now); err != nil {
		return 0, err
	}
	if err := altstore.SyncGhosts(tx, id, in.ProjectID, in.Content, c.P.UserID, now); err != nil {
		return 0, err
	}
	if err := writeText(tx, id, in.ProjectID, title, syn, in.Content); err != nil {
		return 0, err
	}
	if err := afterCreate(tx, in.ProjectID, id, title, in.Content, now); err != nil {
		return 0, err
	}
	return words, touchProject(tx, in.ProjectID, now)
}

func get(c *ops.Ctx, in DocIn) (Item, error) {
	d, err := fullDoc(c, c.DB(), in.ID)
	return Item{Item: d}, err
}

func update(c *ops.Ctx, l *leases, in UpdateIn) (Item, error) {
	if in.BaseRevision < 1 {
		return Item{}, ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	if in.Content != nil {
		if err := checkContent(*in.Content); err != nil {
			return Item{}, err
		}
	}
	if in.Status != nil && !oneOf(Statuses, *in.Status) {
		return Item{}, ops.Invalid("status", "Unknown status.")
	}
	if in.TargetWords != nil && (*in.TargetWords < 0 || *in.TargetWords > 10_000_000) {
		return Item{}, ops.Invalid("target_words", "The target must be between 0 and 10,000,000.")
	}
	var notices []string
	err := c.DB().WithTx(c, func(tx *sql.Tx) error { return updateTx(c, tx, l, in, &notices) })
	if err != nil {
		return Item{}, err
	}
	d, err := fullDoc(c, c.DB(), in.ID)
	return Item{Item: d, Notices: notices}, err
}

// UpdateTx is doc.update's write inside the caller's transaction: the same
// checks (revision, lease, trash), the same hooks, the same event. Another op
// that saves a document and records something of its own in the same
// transaction (accepting a summary) uses it. in must already be validated the
// way doc.update validates it (the title, the synopsis, the content).
func UpdateTx(c *ops.Ctx, tx *sql.Tx, l *Leases, in UpdateIn) error {
	var notices []string
	return updateTx(c, tx, l, in, &notices)
}

func updateTx(c *ops.Ctx, tx *sql.Tx, l *leases, in UpdateIn, noticesOut *[]string) error {
	var notices []string
	defer func() { *noticesOut = notices }()
	{
		var (
			projectID, title, content, hash, synS, synL, status string
			pov, location, storyDate, storyEnd                  string
			contentAt, updatedAt                                string
			rev, target, include, wordCount                     int
			deleted, contentBy                                  sql.NullString
		)
		err := tx.QueryRow(`SELECT project_id, title, content, content_hash, synopsis_short, synopsis_long, status, revision, target_words,
			include_in_compile, deleted_at, pov, location, story_date, story_date_end, content_at, updated_at, content_by, word_count
			FROM documents WHERE id = ?`, in.ID).
			Scan(&projectID, &title, &content, &hash, &synS, &synL, &status, &rev, &target, &include, &deleted,
				&pov, &location, &storyDate, &storyEnd, &contentAt, &updatedAt, &contentBy, &wordCount)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("document")
		}
		if err != nil {
			return err
		}
		before := prior{Title: title, HeadState: revisions.HeadState{DocumentID: in.ID, Content: content, Hash: hash, Revision: rev,
			Words: wordCount, ContentAt: contentAt, UpdatedAt: updatedAt, By: contentBy.String}}
		if deleted.Valid {
			return ops.Conflict("This document is in the trash. Restore it before editing.")
		}
		if in.TabID != "" {
			if l.heldByOther(in.ID, in.TabID) {
				return ops.ConflictWith("This document is open for writing in another tab.", map[string]any{"reason": "lease", "current_revision": rev})
			}
			if !c.DryRun() { // a preview must not take the lease
				l.acquire(in.ID, in.TabID, false)
			}
		}
		if in.BaseRevision != rev {
			return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": rev})
		}
		changed := false
		synBefore := synS // the search text is written only when the title or synopsis, or the body, changed
		if in.Title != nil {
			t, err := cleanTitle(*in.Title)
			if err != nil {
				return err
			}
			changed = changed || t != title
			title = t
		}
		if in.SynopsisShort != nil {
			s, err := cleanSynopsis(*in.SynopsisShort)
			if err != nil {
				return err
			}
			changed = changed || s != synS
			synS = s
		}
		if in.SynopsisLong != nil {
			changed = changed || *in.SynopsisLong != synL
			synL = *in.SynopsisLong
		}
		prevStatus := status
		if in.Status != nil {
			changed = changed || *in.Status != status
			status = *in.Status
		}
		if in.TargetWords != nil {
			changed = changed || *in.TargetWords != target
			target = *in.TargetWords
		}
		if in.IncludeInCompile != nil {
			v := 0
			if *in.IncludeInCompile {
				v = 1
			}
			changed = changed || v != include
			include = v
		}
		// goals: remember what the text was before this save (see RecordSave below).
		oldContent, oldWords := content, 0
		if in.Content != nil {
			_ = tx.QueryRow(`SELECT word_count FROM documents WHERE id = ?`, in.ID).Scan(&oldWords)
		}
		sceneChanged, err := applyScene(in, &pov, &location, &storyDate, &storyEnd)
		if err != nil {
			return err
		}
		changed = changed || sceneChanged
		now := db.Now()
		notices = nil // a retried transaction starts again
		contentChanged, destroyed := false, false
		if in.Content != nil {
			// The text may carry or disturb alternatives: the tables and the comment
			// line are brought in step with it (and with the body), and what is stored
			// is the canonical text. See altstore.
			plan, err := altstore.Reconcile(tx, altstore.In{DocID: in.ID, ProjectID: projectID, OldContent: content, NewContent: *in.Content,
				Mode: altstore.ModeUpdate, Strict: true, Actor: c.P.UserID, Now: now, Limits: altstore.LimitsOf(c.Env)})
			if err != nil {
				return err
			}
			if err := checkContent(plan.Content); err != nil { // the comment line counts toward the limit
				return err
			}
			destroyed = plan.Destroyed
			if plan.Stale {
				notices = append(notices, "The alternatives comment in the text you saved was older than the document's alternatives, so what it leaves out was not removed (and an unedited copy was not applied at all): the alternatives are as the server holds them (see the comment that now ends the text).")
			}
			if h := hashContent(plan.Content); h != hash {
				contentChanged, changed = true, true
				content, hash = plan.Content, h
			}
		}
		if !changed {
			return nil // an idempotent save: nothing to write, the revision stands
		}
		words, searchBody := text.Derive(content) // one parse: the count and the search text
		bigCut := contentChanged && revisions.IsBigCut(before.Words, words)
		if _, err := tx.Exec(`UPDATE documents SET title = ?, content = ?, content_hash = ?, synopsis_short = ?, synopsis_long = ?, status = ?,
			target_words = ?, include_in_compile = ?, revision = revision + 1, word_count = ?, char_count = ?, updated_at = ?,
			pov = ?, location = ?, story_date = ?, story_date_end = ?,
			content_at = CASE WHEN ? THEN ? ELSE content_at END, content_by = CASE WHEN ? THEN ? ELSE content_by END WHERE id = ?`,
			title, content, hash, synS, synL, status, target, include, words, text.CharCount(content), now,
			pov, location, storyDate, storyEnd, contentChanged, now, contentChanged, nullIfEmpty(c.P.UserID), in.ID); err != nil {
			return err
		}
		// doc_text and its index follow the search text. The index is updated on every UPDATE of doc_text, whatever column moved, so a
		// save that changes no searchable text (a status, a target, a scene) leaves them alone: writing them anyway re-indexes the whole body.
		if contentChanged || title != before.Title || synS != synBefore {
			if err := writeTextBody(tx, in.ID, projectID, title, synS, searchBody); err != nil {
				return err
			}
		}
		if destroyed { // variant wordings were removed or reworded: keep the text that held them
			if _, err := revisions.EnsureRecorded(tx, before.HeadState, revisions.ReasonSnapshot, "Before alternative wordings were removed or reworded", now); err != nil {
				return err
			}
		}
		if err := afterSave(tx, before, projectID, in.ID, title, content, contentChanged, bigCut, now); err != nil {
			return err
		}
		if contentChanged { // review comments follow the text they were made on
			if _, err := comanchor.AfterWrite(tx, in.ID, before.Content, content, rev+1, now); err != nil {
				return err
			}
		}
		if contentChanged {
			if err := altstore.SyncGhosts(tx, in.ID, projectID, content, c.P.UserID, now); err != nil {
				return err
			}
		}
		if err := touchProject(tx, projectID, now); err != nil {
			return err
		}
		// goals: a content save feeds the writer's session and daily totals. A
		// statistics fault is logged and rolled back alone; it never fails a save.
		if contentChanged {
			if err := goals.RecordSave(tx, goals.Save{UserID: c.P.UserID, ProjectID: projectID, DocumentID: in.ID,
				OldText: alts.BodyOf(oldContent), NewText: alts.BodyOf(content), OldWords: oldWords, NewWords: words}); err != nil && c.Env.Log != nil {
				c.Env.Log.Warn("goals: could not record a save", "err", err)
			}
		}
		// A chapter finished for the first time counts toward a chapters goal (F-506). Like the other
		// statistics, a fault here is logged and never fails the save.
		if status == "done" && prevStatus != "done" {
			if err := goals.RecordChapterDone(tx, c.P.UserID, projectID, in.ID); err != nil && c.Env.Log != nil {
				c.Env.Log.Warn("goals: could not record a finished chapter", "err", err)
			}
		}
		c.Emit(ops.Event{Type: "doc.updated", ProjectID: projectID, DocumentID: in.ID,
			Payload: map[string]any{"revision": rev + 1, "words": words, "content": contentChanged}})
		return c.FlushTo(tx)
	}
}

func move(c *ops.Ctx, in MoveIn) (Item, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID string
		var deleted sql.NullString
		if err := tx.QueryRow(`SELECT project_id, deleted_at FROM documents WHERE id = ?`, in.ID).Scan(&projectID, &deleted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("document")
			}
			return err
		}
		if deleted.Valid {
			return ops.Conflict("This document is in the trash. Restore it before moving it.")
		}
		if in.ParentID != "" {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, in.ParentID, projectID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ops.Invalid("parent_id", "That folder does not exist in this project.")
			}
			inside, err := isDescendant(c, tx, projectID, in.ID, in.ParentID)
			if err != nil {
				return err
			}
			if inside {
				return ops.Invalid("parent_id", "A document cannot be moved inside itself.")
			}
		}
		after, atEnd := "", in.After == nil
		if in.After != nil {
			after = *in.After
		}
		pos, err := placeAmong(c, tx, projectID, in.ParentID, after, in.ID, atEnd)
		if err != nil {
			return err
		}
		var parent any
		if in.ParentID != "" {
			parent = in.ParentID
		}
		now := db.Now()
		if _, err := tx.Exec(`UPDATE documents SET parent_id = ?, position = ?, updated_at = ? WHERE id = ?`, parent, pos, now, in.ID); err != nil {
			return err
		}
		if err := links.ReresolveAmbiguous(tx, projectID); err != nil { // reading order changed
			return err
		}
		if err := touchProject(tx, projectID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "doc.moved", ProjectID: projectID, DocumentID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, err
	}
	d, err := fullDoc(c, c.DB(), in.ID)
	return Item{Item: d}, err
}

// TrashTx moves documents, and every live document inside each, to the trash in the caller's transaction. They share one batch id, so a
// restore of one brings back exactly what this trash took of it and nothing trashed earlier. It returns the batch id. Links to what was
// trashed stop resolving. The caller touches the project and emits the events.
func TrashTx(tx *sql.Tx, projectID string, ids []string, now string) (string, error) {
	batch := db.NewID("trs")
	for _, id := range ids {
		if _, err := tx.Exec(`WITH RECURSIVE sub(id) AS (
				SELECT id FROM documents WHERE id = ?1 AND project_id = ?2
				UNION SELECT d.id FROM documents d JOIN sub s ON d.parent_id = s.id WHERE d.project_id = ?2)
			UPDATE documents SET deleted_at = ?3, updated_at = ?3, trash_batch = ?4 WHERE id IN (SELECT id FROM sub) AND deleted_at IS NULL`, id, projectID, now, batch); err != nil {
			return "", err
		}
	}
	if err := relinkBatch(tx, projectID, batch); err != nil { // links to what was trashed stop resolving
		return "", err
	}
	return batch, nil
}

func trash(c *ops.Ctx, in DocIn) (Item, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRow(`SELECT project_id FROM documents WHERE id = ?`, in.ID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("document")
			}
			return err
		}
		now := db.Now()
		if _, err := TrashTx(tx, projectID, []string{in.ID}, now); err != nil {
			return err
		}
		if err := touchProject(tx, projectID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "doc.trashed", ProjectID: projectID, DocumentID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, err
	}
	d, err := fullDoc(c, c.DB(), in.ID)
	return Item{Item: d}, err
}

func restore(c *ops.Ctx, in DocIn) (Item, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID string
		var deleted, parent, batch sql.NullString
		if err := tx.QueryRow(`SELECT project_id, deleted_at, parent_id, trash_batch FROM documents WHERE id = ?`, in.ID).Scan(&projectID, &deleted, &parent, &batch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("document")
			}
			return err
		}
		if !deleted.Valid {
			return nil // already live
		}
		now := db.Now()
		// Come back where we were, unless the folder is still in the trash: then
		// at the top level, so nothing is restored into a hidden place.
		if parent.Valid {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM documents WHERE id = ? AND deleted_at IS NULL`, parent.String).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				if _, err := tx.Exec(`UPDATE documents SET parent_id = NULL WHERE id = ?`, in.ID); err != nil {
					return err
				}
			}
		}
		restoring, err := titlesWhere(tx, `WITH RECURSIVE sub(id) AS (
				SELECT id FROM documents WHERE id = ?1 AND project_id = ?2
				UNION SELECT d.id FROM documents d JOIN sub s ON d.parent_id = s.id WHERE d.project_id = ?2)
			SELECT DISTINCT title FROM documents WHERE id IN (SELECT id FROM sub) AND deleted_at IS NOT NULL AND (trash_batch = ?3 OR id = ?1)`,
			in.ID, projectID, batch.String)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`WITH RECURSIVE sub(id) AS (
				SELECT id FROM documents WHERE id = ?1 AND project_id = ?2
				UNION SELECT d.id FROM documents d JOIN sub s ON d.parent_id = s.id WHERE d.project_id = ?2)
			UPDATE documents SET deleted_at = NULL, trash_batch = NULL, updated_at = ?3 WHERE id IN (SELECT id FROM sub) AND deleted_at IS NOT NULL AND (trash_batch = ?4 OR id = ?1)`,
			in.ID, projectID, now, batch.String); err != nil {
			return err
		}
		if err := links.Reresolve(tx, projectID, restoring...); err != nil { // links to what came back resolve again
			return err
		}
		if err := touchProject(tx, projectID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "doc.restored", ProjectID: projectID, DocumentID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, err
	}
	d, err := fullDoc(c, c.DB(), in.ID)
	return Item{Item: d}, err
}

// utf16Index converts a UTF-16 offset into a byte index. A CRLF pair counts as
// one character, because editors show (and clients measure) the text with line
// endings normalised. It refuses an offset that falls inside a surrogate pair
// or between the CR and LF of a pair.
func utf16Index(s string, off int) (int, bool) {
	if off < 0 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); {
		if n == off {
			return i, true
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\r' && i+1 < len(s) && s[i+1] == '\n':
			i += 2 // CRLF is one character
			n++
			continue
		case r >= 0x10000:
			n += 2
		default:
			n++
		}
		i += size
		if n > off {
			return 0, false
		}
	}
	if n == off {
		return len(s), true
	}
	return 0, false
}

// insideSourceBlock reports whether cutting content at byte index cut would
// split a block the editor preserves as source: a fenced code block, an HTML
// comment, or front matter. Cutting exactly at a block's first byte or just
// after its last line is fine.
func insideSourceBlock(content string, cut int) bool { return !SafeCut(content)(cut) }

// SafeCut is the cut-safety of doc.split, for a caller that cuts finished Markdown at many places (the importer's split of a file at its
// second-level headings). It reads content once and answers, for any byte index, whether the text may be cut there: false when the index
// falls inside a block the editor preserves as source (a fenced code block, an HTML comment, front matter). A cut exactly at a block's
// first byte, or just after its last line, is safe. Each answer costs a binary search, so a file of thousands of candidates is not
// scanned once for each.
func SafeCut(content string) func(cut int) bool {
	regs := sourceRegions(content)
	return func(cut int) bool {
		i := sort.Search(len(regs), func(i int) bool { return regs[i].end > cut })
		return !(i < len(regs) && cut > regs[i].start && cut < regs[i].end)
	}
}

type srcRegion struct{ start, end int }

// sourceRegions lists, in order and without overlap, the blocks of content that a cut must not fall inside.
func sourceRegions(content string) []srcRegion {
	type region = srcRegion
	var regs []region
	lines := strings.SplitAfter(content, "\n")
	pos := 0
	var (
		fence      string
		fenceStart int
		comment    = -1
	)
	for i, ln := range lines {
		start, end := pos, pos+len(ln)
		pos = end
		t := strings.TrimRight(ln, "\r\n")
		trim := strings.TrimLeft(t, " ")
		switch {
		case fence != "":
			if len(trim) >= len(fence) && strings.HasPrefix(trim, fence) && strings.Trim(trim, fence[:1]) == "" && len(t)-len(trim) < 4 {
				regs = append(regs, region{fenceStart, end})
				fence = ""
			}
		case comment >= 0:
			if strings.Contains(t, "-->") {
				regs = append(regs, region{comment, end})
				comment = -1
			}
		case i == 0 && t == "---":
			// front matter runs to the next --- line, if there is one
			off := end
			for _, nx := range lines[1:] {
				off += len(nx)
				if strings.TrimRight(nx, "\r\n") == "---" {
					regs = append(regs, region{0, off})
					break
				}
			}
		case len(t)-len(trim) < 4 && (strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~")):
			m := trim[:1]
			n := len(trim) - len(strings.TrimLeft(trim, m))
			fence, fenceStart = strings.Repeat(m, n), start
		case strings.HasPrefix(trim, "<!--"):
			if strings.Contains(t[strings.Index(t, "<!--")+4:], "-->") {
				regs = append(regs, region{start, end})
			} else {
				comment = start
			}
		}
	}
	if fence != "" {
		regs = append(regs, region{fenceStart, len(content)})
	}
	if comment >= 0 {
		regs = append(regs, region{comment, len(content)})
	}
	// A comment or a fence inside front matter is a region of its own inside another: those that overlap are one (those that only touch
	// are not: a cut where one ends and the next begins is between blocks).
	sort.SliceStable(regs, func(i, j int) bool { return regs[i].start < regs[j].start })
	var out []region
	for _, r := range regs {
		if n := len(out); n > 0 && r.start < out[n-1].end {
			out[n-1].end = max(out[n-1].end, r.end)
			continue
		}
		out = append(out, r)
	}
	return out
}

// trimHead is the text before a cut with the blank space at its end taken off. A cut made right after a hard line break
// written as a backslash and a line break ("line one\\\nline two") leaves that backslash at the end of the first part, where it
// would show as a stray character: it is the break's own mark, and goes with the break. An escaped backslash (an even run) is text.
func trimHead(head string) string {
	t := strings.TrimRight(head, " \t\r\n")
	if strings.Contains(head[len(t):], "\n") {
		n := len(t) - len(strings.TrimRight(t, "\\"))
		if n%2 == 1 {
			t = strings.TrimRight(t[:len(t)-1], " \t")
		}
	}
	return t
}

func split(c *ops.Ctx, l *leases, in SplitIn) (SplitOut, error) {
	if in.BaseRevision < 1 {
		return SplitOut{}, ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	title, err := cleanTitle(in.Title)
	if err != nil {
		return SplitOut{}, err
	}
	newID := db.NewID("doc")
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var (
			projectID, headTitle, content, synS, kind string
			contentAt, updatedAt                      string
			parent, compendium, contentBy             sql.NullString
			rev, include, aiExclude, wordCount        int
			deleted                                   sql.NullString
		)
		err := tx.QueryRow(`SELECT project_id, title, content, synopsis_short, kind, parent_id, revision, deleted_at,
			include_in_compile, ai_exclude, compendium_id, content_at, updated_at, content_by, word_count FROM documents WHERE id = ?`, in.ID).
			Scan(&projectID, &headTitle, &content, &synS, &kind, &parent, &rev, &deleted, &include, &aiExclude, &compendium,
				&contentAt, &updatedAt, &contentBy, &wordCount)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("document")
		}
		if err != nil {
			return err
		}
		if deleted.Valid {
			return ops.Conflict("This document is in the trash. Restore it before splitting it.")
		}
		if in.TabID != "" {
			if l.heldByOther(in.ID, in.TabID) {
				return ops.ConflictWith("This document is open for writing in another tab.", map[string]any{"reason": "lease", "current_revision": rev})
			}
			if !c.DryRun() { // a preview must not take the lease
				l.acquire(in.ID, in.TabID, false)
			}
		}
		if in.BaseRevision != rev {
			return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": rev})
		}
		// A document that carries alternatives is split at a point outside every
		// anchor, and its comment is divided between the halves.
		stored, err := altstore.Load(tx, in.ID)
		if err != nil {
			return err
		}
		carries := len(stored) > 0
		parts := alts.Parts{Body: content}
		if carries {
			content = alts.NormalizeEOL(content)
			parts = alts.Split(content)
		}
		cut, ok := utf16Index(content, in.Offset)
		if !ok {
			return ops.Invalid("offset", "That position is not inside the text.")
		}
		if insideSourceBlock(content, cut) {
			return ops.Invalid("offset", "That position is inside a code block, comment, front matter or other preserved block. Choose a position between blocks.")
		}
		if cut > len(parts.Body) {
			return ops.Invalid("offset", "That position is inside the alternatives comment at the end of the text.")
		}
		if spans, _ := text.Ghosts(parts.Body); len(spans) > 0 {
			for _, sp := range spans {
				if cut > sp.OpenStart && cut < sp.CloseEnd {
					return ops.Invalid("offset", "That position is inside ghosted text. Revive it first, or choose another position.")
				}
			}
		}
		body := parts.Body
		head, tail := body[:cut], body[cut:]
		if strings.TrimSpace(tail) == "" {
			return ops.Invalid("offset", "There is no text after that position to move into a new document.")
		}
		head = trimHead(head)
		keepHead := len(head)
		if head != "" {
			head += "\n"
		}
		lead := cut + len(tail) - len(strings.TrimLeft(tail, "\r\n"))
		tail = strings.TrimLeft(tail, "\r\n")
		var headItems, tailItems []alts.Item
		if len(stored) > 0 {
			headItems, tailItems, err = splitItems(stored, body, head, tail, cut, keepHead, lead, altstore.LimitsOf(c.Env))
			if err != nil {
				return err
			}
			head = alts.Parts{Body: head}.Compose(alts.Serialize(headItems))
			tail = alts.Parts{Body: tail}.Compose(alts.Serialize(tailItems))
		}
		if err := checkContent(head); err != nil {
			return err
		}
		now := db.Now()
		// The cut is not undoable by editing, so the whole text is kept first.
		if _, err := revisions.CaptureBefore(tx, revisions.HeadState{DocumentID: in.ID, Content: content, Revision: rev, Words: wordCount,
			ContentAt: contentAt, UpdatedAt: updatedAt, By: contentBy.String}, revisions.ReasonSplit, now); err != nil {
			return err
		}
		pos, err := placeAmong(c, tx, projectID, parent.String, in.ID, "", false)
		if err != nil {
			return err
		}
		hw, tw := text.WordCount(head), text.WordCount(tail)
		if _, err := tx.Exec(`UPDATE documents SET content = ?, content_hash = ?, revision = revision + 1, word_count = ?, char_count = ?, updated_at = ?,
			content_at = ?, content_by = ? WHERE id = ?`,
			head, hashContent(head), hw, text.CharCount(head), now, now, nullIfEmpty(c.P.UserID), in.ID); err != nil {
			return err
		}
		if err := writeText(tx, in.ID, projectID, headTitle, synS, head); err != nil {
			return err
		}
		var par any
		if parent.Valid {
			par = parent.String
		}
		if _, err := tx.Exec(`INSERT INTO documents(id, project_id, parent_id, kind, title, content, content_hash,
			position, revision, word_count, char_count, include_in_compile, ai_exclude, compendium_id, content_at, content_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			newID, projectID, par, kind, title, tail, hashContent(tail), pos, 1, tw, text.CharCount(tail), include, aiExclude, compendium, now, nullIfEmpty(c.P.UserID), now, now); err != nil {
			return err
		}
		if err := writeText(tx, newID, projectID, title, "", tail); err != nil {
			return err
		}
		if len(stored) > 0 {
			if _, _, err := altstore.Write(tx, in.ID, projectID, stored, headItems, c.P.UserID, now); err != nil {
				return err
			}
			if _, _, err := altstore.Write(tx, newID, projectID, nil, tailItems, c.P.UserID, now); err != nil {
				return err
			}
		}
		if err := altstore.SyncGhosts(tx, in.ID, projectID, head, c.P.UserID, now); err != nil {
			return err
		}
		if err := altstore.SyncGhosts(tx, newID, projectID, tail, c.P.UserID, now); err != nil {
			return err
		}
		if err := afterSplit(tx, projectID, in.ID, head, newID, title, tail, now); err != nil {
			return err
		}
		// Comments on the text that moved to the new document (or that straddle the cut) are
		// orphaned here, first, so the re-anchoring of the head cannot take one onto a like
		// passage that stayed; comment.reanchor moves a thread to its new place.
		if _, err := tx.Exec(`UPDATE comments SET anchor_state = 'orphaned', updated_at = ? WHERE document_id = ? AND parent_id IS NULL AND anchor_state = 'active' AND anchor_end > ?`,
			now, in.ID, alts.Len16(alts.NormalizeEOL(body[:cut]))); err != nil {
			return err
		}
		if _, err := comanchor.AfterWriteKeepingLost(tx, in.ID, content, head, rev+1, now); err != nil {
			return err
		}
		if err := touchProject(tx, projectID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "doc.updated", ProjectID: projectID, DocumentID: in.ID, Payload: map[string]any{"revision": rev + 1, "words": hw, "content": true}})
		c.Emit(ops.Event{Type: "doc.created", ProjectID: projectID, DocumentID: newID, Payload: map[string]any{"kind": kind, "words": tw}})
		c.Emit(ops.Event{Type: "doc.split", ProjectID: projectID, DocumentID: in.ID, Payload: map[string]any{"new_id": newID, "head_words": hw, "tail_words": tw}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return SplitOut{}, err
	}
	h, err := fullDoc(c, c.DB(), in.ID)
	if err != nil {
		return SplitOut{}, err
	}
	t, err := fullDoc(c, c.DB(), newID)
	return SplitOut{Head: h, Tail: t}, err
}

func leaseOp(c *ops.Ctx, l *leases, in LeaseIn) (LeaseOut, error) {
	if in.TabID == "" || len(in.TabID) > 64 {
		return LeaseOut{}, ops.Invalid("tab_id", "Send a tab id of up to 64 characters.")
	}
	if in.Release {
		l.release(in.ID, in.TabID)
		return LeaseOut{Held: false, ExpiresAt: db.Format(db.NowTime())}, nil
	}
	held, exp := l.acquire(in.ID, in.TabID, in.Take)
	return LeaseOut{Held: held, ExpiresAt: db.Format(exp)}, nil
}

func conflict(c *ops.Ctx, in ConflictIn) (ConflictOut, error) {
	if err := checkContent(in.Content); err != nil {
		return ConflictOut{}, err
	}
	if c.Env.DataDir == "" {
		return ConflictOut{}, ops.Internal(errors.New("no data directory"))
	}
	var projectID string
	if err := c.DB().Read().QueryRowContext(c, `SELECT project_id FROM documents WHERE id = ?`, in.ID).Scan(&projectID); err != nil {
		return ConflictOut{}, ops.NotFound("document")
	}
	rel := filepath.Join("conflicts", projectID, fmt.Sprintf("%s-%s-r%d.md", in.ID, strings.NewReplacer(":", "", ".", "").Replace(db.Now()), in.BaseRevision))
	full := filepath.Join(c.Env.DataDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return ConflictOut{}, err
	}
	if err := os.WriteFile(full, []byte(in.Content), 0o600); err != nil {
		return ConflictOut{}, err
	}
	return ConflictOut{Path: rel}, nil
}

// Seed creates a new project's first document inside the project's own
// transaction, so a project is never without a page to write on. Book-shaped
// kinds start with "Chapter 1"; everything else starts untitled, because
// structure is opt-in.
func Seed(tx *sql.Tx, projectID, kind, now string) (string, error) {
	title := ""
	switch kind {
	case "novel", "novella", "collection", "nonfiction", "report":
		title = "Chapter 1"
	}
	id := db.NewID("doc")
	if _, err := tx.Exec(`INSERT INTO documents(id, project_id, kind, title, content, content_hash, position, revision, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, id, projectID, "chapter", title, "", hashContent(""), db.PositionGap, 1, now, now); err != nil {
		return "", err
	}
	return id, writeText(tx, id, projectID, title, "", "")
}

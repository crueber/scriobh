// Package stash is a project's overflow: paragraphs, notes and outlines that
// are not in the manuscript. A stash item is not a document. It is not in the
// manuscript tree, the outline, the word counts, the manuscript search or any
// export; it has its own index (stash_text, stash_fts), is part of a backup (it
// is in the database) and of the mirror (a stash/ folder, never part of a Git
// push). Only editors and owners reach it, reads included: the unused
// paragraphs are the writer's cut material, and someone who may read the
// manuscript does not thereby read what was cut.
//
// Putting an item back into a document is stash.drag_back, in package docs: it
// is an edit of a document and goes through the document write path.
package stash

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Kinds of stash item.
var Kinds = []string{"paragraph", "note", "outline"}

const (
	maxContent  = 512 << 10
	maxTitle    = 300
	maxTags     = 20
	maxTagRunes = 40
	maxItems    = 10000
	defaultList = 50
	maxList     = 200
)

// Item is one stash item.
type Item struct {
	ID               string   `json:"id"`
	ProjectID        string   `json:"project_id"`
	SourceDocumentID string   `json:"source_document_id,omitempty" desc:"The document the text was stashed from, when it was."`
	Kind             string   `json:"kind" enum:"paragraph,note,outline"`
	Title            string   `json:"title"`
	Content          string   `json:"content" desc:"Markdown."`
	Tags             []string `json:"tags"`
	Position         float64  `json:"position"`
	Revision         int      `json:"revision" desc:"The concurrency token: send it with an update."`
	WordCount        int      `json:"word_count"`
	Trashed          bool     `json:"trashed"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	MatchedIn        string   `json:"matched_in,omitempty" enum:"title,tags,body" desc:"With a search: the most important field that matched."`
	Snippet          string   `json:"snippet,omitempty" desc:"With a search: plain text around the first match."`
	SnippetHTML      string   `json:"snippet_html,omitempty" desc:"The same snippet as HTML: escaped, with each match in <mark>."`
}

// ItemOut wraps one item.
type ItemOut struct {
	Item Item `json:"item"`
}

const cols = `id, project_id, COALESCE(source_document_id,''), kind, title, content, tags, position, revision, word_count, deleted_at IS NOT NULL, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scan(s scanner) (Item, error) {
	var it Item
	var tags string
	err := s.Scan(&it.ID, &it.ProjectID, &it.SourceDocumentID, &it.Kind, &it.Title, &it.Content, &tags, &it.Position, &it.Revision, &it.WordCount, &it.Trashed, &it.CreatedAt, &it.UpdatedAt)
	it.Tags = decodeTags(tags)
	return it, err
}

func decodeTags(s string) []string {
	out := []string{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

func encodeTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, _ := json.Marshal(tags)
	return string(b)
}

func cleanTags(in []string) ([]string, error) {
	if len(in) > maxTags {
		return nil, ops.Invalid("tags", fmt.Sprintf("At most %d tags.", maxTags))
	}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > maxTagRunes {
			return nil, ops.Invalid("tags", fmt.Sprintf("A tag is at most %d characters.", maxTagRunes))
		}
		for _, r := range t {
			if r == '"' || r == '\\' || r < 0x20 {
				return nil, ops.Invalid("tags", "A tag cannot hold quotes, backslashes or control characters.")
			}
		}
		if k := strings.ToLower(t); !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func cleanTitle(s string) (string, error) {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s))
	if utf8.RuneCountInString(s) > maxTitle {
		return "", ops.Invalid("title", fmt.Sprintf("Titles are limited to %d characters.", maxTitle))
	}
	return s, nil
}

func cleanContent(s string) (string, error) {
	if len(s) > maxContent {
		return "", ops.Invalid("content", fmt.Sprintf("A stash item is limited to %d KB.", maxContent>>10))
	}
	if !utf8.ValidString(s) {
		return "", ops.Invalid("content", "The text is not valid UTF-8.")
	}
	return s, nil
}

// writeText keeps the stash search text in step with the item, in the same
// transaction.
func writeText(tx *sql.Tx, id, projectID, title string, tags []string, content string) error {
	_, err := tx.Exec(`INSERT INTO stash_text(item_id, project_id, title, tags, body) VALUES (?,?,?,?,?)
		ON CONFLICT(item_id) DO UPDATE SET title = excluded.title, tags = excluded.tags, body = excluded.body`,
		id, projectID, title, search.StashTagsText(encodeTags(tags)), text.SearchBody(content))
	return err
}

func load(q interface {
	QueryRow(string, ...any) *sql.Row
}, projectID, itemID string) (Item, error) {
	it, err := scan(q.QueryRow(`SELECT `+cols+` FROM stash_items WHERE id = ? AND project_id = ?`, itemID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, ops.NotFound("stash item")
	}
	return it, err
}

func needItem(id string) error {
	if id == "" || len(id) > 64 {
		return ops.Invalid("item_id", "Name the stash item.")
	}
	return nil
}

func touch(tx *sql.Tx, projectID, now string) error {
	_, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now, projectID)
	return err
}

// ---- ops ---------------------------------------------------------------------------

// CreateIn stashes text.
type CreateIn struct {
	ID               string   `path:"id" json:"-" desc:"Project id."`
	Title            string   `json:"title,omitempty"`
	Content          string   `json:"content,omitempty" desc:"Markdown."`
	Kind             string   `json:"kind,omitempty" enum:"paragraph,note,outline" desc:"Default paragraph."`
	Tags             []string `json:"tags,omitempty"`
	SourceDocumentID string   `json:"source_document_id,omitempty" desc:"The document the text came from, if any."`
	After            *string  `json:"after,omitempty" desc:"Item to place after; empty string for first; omit for last."`
}

// ProjectRef lets the registry find the project.
func (in CreateIn) ProjectRef() string { return in.ID }

func create(c *ops.Ctx, in CreateIn) (ItemOut, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return ItemOut{}, err
	}
	content, err := cleanContent(in.Content)
	if err != nil {
		return ItemOut{}, err
	}
	if title == "" && strings.TrimSpace(content) == "" {
		return ItemOut{}, ops.Invalid("content", "A stash item needs a title or some text.")
	}
	if in.Kind == "" {
		in.Kind = "paragraph"
	}
	if !oneOf(Kinds, in.Kind) {
		return ItemOut{}, ops.Invalid("kind", "Unknown kind: use paragraph, note or outline.")
	}
	tags, err := cleanTags(in.Tags)
	if err != nil {
		return ItemOut{}, err
	}
	id, now := db.NewID("stp"), db.Now()
	var out ItemOut
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM stash_items WHERE project_id = ?`, in.ID).Scan(&n); err != nil {
			return err
		}
		if n >= maxItems {
			return ops.Invalid("content", fmt.Sprintf("A project's stash holds at most %d items.", maxItems))
		}
		var src any
		if in.SourceDocumentID != "" {
			var one int
			if err := tx.QueryRow(`SELECT 1 FROM documents WHERE id = ? AND project_id = ?`, in.SourceDocumentID, in.ID).Scan(&one); err != nil {
				return ops.Invalid("source_document_id", "That document is not in this project.")
			}
			src = in.SourceDocumentID
		}
		pos, err := place(tx, in.ID, in.After, "")
		if err != nil {
			return err
		}
		words := text.WordCount(content)
		if _, err := tx.Exec(`INSERT INTO stash_items(id, project_id, source_document_id, kind, title, content, tags, position, revision, word_count, content_hash, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,(SELECT id FROM users WHERE id = ?),?,?)`,
			id, in.ID, src, in.Kind, title, content, encodeTags(tags), pos, 1, words, hash(content), c.P.UserID, now, now); err != nil {
			return err
		}
		if err := writeText(tx, id, in.ID, title, tags, content); err != nil {
			return err
		}
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "stash.created", ProjectID: in.ID, Payload: map[string]any{"kind": in.Kind, "words": words}})
		it, err := load(tx, in.ID, id)
		out = ItemOut{Item: it}
		if err != nil {
			return err
		}
		return c.FlushTo(tx)
	})
	return out, err
}

// place finds the position for an item: after the item named (empty for first,
// nil for last), among the project's live items other than skip.
func place(tx *sql.Tx, projectID string, after *string, skip string) (float64, error) {
	rows, err := tx.Query(`SELECT id, position FROM stash_items WHERE project_id = ? AND deleted_at IS NULL AND id <> ? ORDER BY position, id`, projectID, skip)
	if err != nil {
		return 0, err
	}
	type pos struct {
		id string
		p  float64
	}
	var ps []pos
	for rows.Next() {
		var x pos
		if err := rows.Scan(&x.id, &x.p); err != nil {
			rows.Close()
			return 0, err
		}
		ps = append(ps, x)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if after == nil || len(ps) == 0 {
		if len(ps) == 0 {
			return db.PositionGap, nil
		}
		return db.PositionAfter(ps[len(ps)-1].p), nil
	}
	lo, hi := -1.0, 0.0
	if *after == "" {
		hi = ps[0].p
	} else {
		found := false
		for i, x := range ps {
			if x.id == *after {
				found = true
				lo = x.p
				if i+1 < len(ps) {
					hi = ps[i+1].p
				}
			}
		}
		if !found {
			return 0, ops.Invalid("after", "The item to place after is not in the stash.")
		}
	}
	p, rebalance := db.PositionBetween(lo, hi)
	if rebalance {
		// Spread the siblings out again and ask once more.
		for i, np := range db.Rebalanced(len(ps)) {
			if _, err := tx.Exec(`UPDATE stash_items SET position = ? WHERE id = ?`, np, ps[i].id); err != nil {
				return 0, err
			}
			ps[i].p = np
		}
		return place(tx, projectID, after, skip)
	}
	return p, nil
}

// ListIn lists or searches the stash.
type ListIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	Q      string `query:"q" desc:"Search the stash: title, tags and text. Best match first. Empty lists in order."`
	Kind   string `query:"kind" enum:"paragraph,note,outline"`
	Tag    string `query:"tag" desc:"Only items with this tag."`
	Trash  bool   `query:"trash" desc:"List the trash instead."`
	Limit  int    `query:"limit" desc:"Items per page (default 50, at most 200)."`
	Offset int    `query:"offset"`
}

// ProjectRef lets the registry find the project.
func (in ListIn) ProjectRef() string { return in.ID }

// ListOut is a page of stash items.
type ListOut struct {
	Items      []Item `json:"items"`
	Total      int    `json:"total"`
	NextOffset int    `json:"next_offset,omitempty"`
	Degraded   bool   `json:"degraded,omitempty" desc:"True when the index could not be read and a slower substring search answered. Run scriobh search rebuild."`
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = defaultList
	case limit > maxList:
		limit = maxList
	}
	if in.Offset < 0 {
		return ListOut{}, ops.Invalid("offset", "The offset cannot be negative.")
	}
	if in.Kind != "" && !oneOf(Kinds, in.Kind) {
		return ListOut{}, ops.Invalid("kind", "Unknown kind.")
	}
	d := c.DB().Read()
	out := ListOut{Items: []Item{}}
	if strings.TrimSpace(in.Q) != "" {
		expr, words, err := search.MatchExpr(in.Q)
		if err != nil {
			return out, err
		}
		hits, total, degraded, err := search.StashSearch(c, d, in.ID, expr, words, search.StashFilter{Kind: in.Kind, Trashed: in.Trash}, limit, in.Offset)
		if err != nil {
			return out, err
		}
		out.Total, out.Degraded = total, degraded
		for _, h := range hits {
			it, err := load(d, in.ID, h.ItemID)
			if err != nil {
				continue
			}
			it.MatchedIn, it.Snippet, it.SnippetHTML = h.MatchedIn, h.Snippet, h.SnippetHTML
			out.Items = append(out.Items, it)
		}
		if in.Offset+len(hits) < total {
			out.NextOffset = in.Offset + len(hits)
		}
		return out, nil
	}
	where := ` WHERE project_id = ? AND deleted_at IS `
	if in.Trash {
		where += `NOT NULL`
	} else {
		where += `NULL`
	}
	args := []any{in.ID}
	if in.Kind != "" {
		where += ` AND kind = ?`
		args = append(args, in.Kind)
	}
	if in.Tag != "" {
		where += ` AND tags LIKE ? ESCAPE '\'`
		args = append(args, `%"`+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(in.Tag)+`"%`)
	}
	if err := d.QueryRowContext(c, `SELECT count(*) FROM stash_items`+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	order := ` ORDER BY position, id`
	if in.Trash {
		order = ` ORDER BY deleted_at DESC, id`
	}
	rows, err := d.QueryContext(c, `SELECT `+cols+` FROM stash_items`+where+order+` LIMIT ? OFFSET ?`, append(args, limit, in.Offset)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, it)
	}
	if in.Offset+len(out.Items) < out.Total {
		out.NextOffset = in.Offset + len(out.Items)
	}
	return out, rows.Err()
}

// GetIn names one item.
type GetIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	ItemID string `query:"item_id" desc:"The stash item."`
}

// ProjectRef lets the registry find the project.
func (in GetIn) ProjectRef() string { return in.ID }

func get(c *ops.Ctx, in GetIn) (ItemOut, error) {
	if err := needItem(in.ItemID); err != nil {
		return ItemOut{}, err
	}
	it, err := load(c.DB().Read(), in.ID, in.ItemID)
	return ItemOut{Item: it}, err
}

// UpdateIn edits an item.
type UpdateIn struct {
	ID           string    `path:"id" json:"-" desc:"Project id."`
	ItemID       string    `json:"item_id"`
	BaseRevision int       `json:"base_revision" desc:"The revision you last saw; a stale one is a 409."`
	Title        *string   `json:"title,omitempty"`
	Content      *string   `json:"content,omitempty" desc:"Markdown."`
	Kind         *string   `json:"kind,omitempty" enum:"paragraph,note,outline"`
	Tags         *[]string `json:"tags,omitempty"`
}

// ProjectRef lets the registry find the project.
func (in UpdateIn) ProjectRef() string { return in.ID }

func update(c *ops.Ctx, in UpdateIn) (ItemOut, error) {
	if err := needItem(in.ItemID); err != nil {
		return ItemOut{}, err
	}
	if in.BaseRevision < 1 {
		return ItemOut{}, ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	var out ItemOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		it, err := load(tx, in.ID, in.ItemID)
		if err != nil {
			return err
		}
		if it.Trashed {
			return ops.Conflict("This item is in the trash. Restore it before editing.")
		}
		if in.BaseRevision != it.Revision {
			return ops.ConflictWith("This stash item changed since you opened it.", map[string]any{"reason": "revision", "current_revision": it.Revision})
		}
		changed := false
		if in.Title != nil {
			t, err := cleanTitle(*in.Title)
			if err != nil {
				return err
			}
			changed, it.Title = changed || t != it.Title, t
		}
		if in.Content != nil {
			t, err := cleanContent(*in.Content)
			if err != nil {
				return err
			}
			changed, it.Content = changed || t != it.Content, t
		}
		if in.Kind != nil {
			if !oneOf(Kinds, *in.Kind) {
				return ops.Invalid("kind", "Unknown kind: use paragraph, note or outline.")
			}
			changed, it.Kind = changed || *in.Kind != it.Kind, *in.Kind
		}
		if in.Tags != nil {
			t, err := cleanTags(*in.Tags)
			if err != nil {
				return err
			}
			changed = changed || encodeTags(t) != encodeTags(it.Tags)
			it.Tags = t
		}
		if it.Title == "" && strings.TrimSpace(it.Content) == "" {
			return ops.Invalid("content", "A stash item needs a title or some text.")
		}
		if !changed {
			out = ItemOut{Item: it}
			return nil
		}
		now := db.Now()
		words := text.WordCount(it.Content)
		if _, err := tx.Exec(`UPDATE stash_items SET title = ?, content = ?, kind = ?, tags = ?, word_count = ?, content_hash = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			it.Title, it.Content, it.Kind, encodeTags(it.Tags), words, hash(it.Content), now, it.ID); err != nil {
			return err
		}
		if err := writeText(tx, it.ID, in.ID, it.Title, it.Tags, it.Content); err != nil {
			return err
		}
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "stash.updated", ProjectID: in.ID, Payload: map[string]any{"revision": it.Revision + 1, "words": words}})
		if it, err = load(tx, in.ID, in.ItemID); err != nil {
			return err
		}
		out = ItemOut{Item: it}
		return c.FlushTo(tx)
	})
	return out, err
}

// ItemIn names one item in a body.
type ItemIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	ItemID string `json:"item_id" desc:"The stash item."`
}

// ProjectRef lets the registry find the project.
func (in ItemIn) ProjectRef() string { return in.ID }

func setTrashed(c *ops.Ctx, in ItemIn, trash bool) (ItemOut, error) {
	if err := needItem(in.ItemID); err != nil {
		return ItemOut{}, err
	}
	var out ItemOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		it, err := load(tx, in.ID, in.ItemID)
		if err != nil {
			return err
		}
		if it.Trashed == trash {
			out = ItemOut{Item: it}
			return nil
		}
		now := db.Now()
		var del any
		if trash {
			del = now
		}
		pos := it.Position
		if !trash {
			// A restored item goes to the end, whatever has been placed since.
			if pos, err = place(tx, in.ID, nil, it.ID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE stash_items SET deleted_at = ?, position = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, del, pos, now, it.ID); err != nil {
			return err
		}
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		ev := "stash.restored"
		if trash {
			ev = "stash.trashed"
		}
		c.Emit(ops.Event{Type: ev, ProjectID: in.ID})
		if it, err = load(tx, in.ID, in.ItemID); err != nil {
			return err
		}
		out = ItemOut{Item: it}
		return c.FlushTo(tx)
	})
	return out, err
}

// DeleteOut says what was deleted.
type DeleteOut struct {
	Deleted bool `json:"deleted"`
}

func deleteItem(c *ops.Ctx, in ItemIn) (DeleteOut, error) {
	if err := needItem(in.ItemID); err != nil {
		return DeleteOut{}, err
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		it, err := load(tx, in.ID, in.ItemID)
		if err != nil {
			return err
		}
		if !it.Trashed {
			return ops.Conflict("Trash the item first; only trashed items are deleted for good.")
		}
		if _, err := tx.Exec(`DELETE FROM stash_items WHERE id = ?`, it.ID); err != nil {
			return err
		}
		now := db.Now()
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "stash.deleted", ProjectID: in.ID})
		return c.FlushTo(tx)
	})
	return DeleteOut{Deleted: err == nil}, err
}

// ReorderIn moves an item.
type ReorderIn struct {
	ID          string `path:"id" json:"-" desc:"Project id."`
	ItemID      string `json:"item_id" desc:"The item to move."`
	AfterItemID string `json:"after_item_id,omitempty" desc:"Place it after this item; empty to put it first."`
}

// ProjectRef lets the registry find the project.
func (in ReorderIn) ProjectRef() string { return in.ID }

func reorder(c *ops.Ctx, in ReorderIn) (ItemOut, error) {
	if err := needItem(in.ItemID); err != nil {
		return ItemOut{}, err
	}
	var out ItemOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		it, err := load(tx, in.ID, in.ItemID)
		if err != nil {
			return err
		}
		if it.Trashed {
			return ops.Conflict("This item is in the trash.")
		}
		if in.AfterItemID == in.ItemID {
			return ops.Invalid("after_item_id", "An item cannot be placed after itself.")
		}
		after := in.AfterItemID
		pos, err := place(tx, in.ID, &after, it.ID)
		if err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.Exec(`UPDATE stash_items SET position = ?, updated_at = ? WHERE id = ?`, pos, now, it.ID); err != nil {
			return err
		}
		if err := touch(tx, in.ID, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "stash.moved", ProjectID: in.ID})
		if it, err = load(tx, in.ID, in.ItemID); err != nil {
			return err
		}
		out = ItemOut{Item: it}
		return c.FlushTo(tx)
	})
	return out, err
}

// Register adds the stash ops. Every one needs the editor role: the stash is
// the writer's cut material, so reading it is not a reader's right.
func Register(r *ops.Registry) {
	editor := ops.RequireProject(ops.ProjectEditor)
	base := "/api/v1/projects/{id}/stash"
	ops.Register(r, ops.Op{Name: "stash.create", Method: "POST", Path: base, Status: 201,
		Desc: "Put a paragraph, note or outline in the project's stash: overflow that is not in the manuscript, not counted and not exported. Optionally name the document it came from.", Auth: editor}, create)
	ops.Register(r, ops.Op{Name: "stash.list", Method: "GET", Path: base,
		Desc: "List the stash in order, or search it (q: title, tags and text, best match first). Editors only.", Auth: editor}, list)
	ops.Register(r, ops.Op{Name: "stash.get", Method: "GET", Path: base + "/item",
		Desc: "Get one stash item.", Auth: editor}, get)
	ops.Register(r, ops.Op{Name: "stash.update", Method: "PATCH", Path: base + "/item",
		Desc: "Edit a stash item. Send the revision you last saw; a stale one is a 409.", Auth: editor}, update)
	ops.Register(r, ops.Op{Name: "stash.trash", Method: "POST", Path: base + "/trash",
		Desc: "Move a stash item to the stash trash.", Auth: editor},
		func(c *ops.Ctx, in ItemIn) (ItemOut, error) { return setTrashed(c, in, true) })
	ops.Register(r, ops.Op{Name: "stash.restore", Method: "POST", Path: base + "/restore",
		Desc: "Bring a trashed stash item back, at the end.", Auth: editor},
		func(c *ops.Ctx, in ItemIn) (ItemOut, error) { return setTrashed(c, in, false) })
	ops.Register(r, ops.Op{Name: "stash.delete", Permanent: true, Method: "POST", Path: base + "/delete",
		Desc: "Delete a trashed stash item for good.", Auth: editor}, deleteItem)
	ops.Register(r, ops.Op{Name: "stash.reorder", Method: "POST", Path: base + "/reorder",
		Desc: "Move a stash item to another place in the stash.", Auth: editor}, reorder)
}

func oneOf(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

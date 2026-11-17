package research

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// ---- inputs and outputs ------------------------------------------------------------------------

// ListIn lists an owner's notes as a flat tree.
type ListIn struct {
	ID     string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	Trash  bool   `query:"trash" desc:"List the trash instead of the live tree."`
	Tag    string `query:"tag" desc:"Only notes with this tag."`
	Status string `query:"status" enum:"inbox,reading,useful,archived" desc:"Only notes in this board column."`
	Kind   string `query:"kind" enum:"note,clipping,folder" desc:"Only this kind."`
}

func (in ListIn) ProjectRef() string    { return in.ID }
func (in ListIn) CompendiumRef() string { return in.ID }

// ListOut is the flat tree; clients nest it by parent_id and position.
type ListOut struct {
	Items []Entry  `json:"items"`
	Order []string `json:"order" desc:"Ids in reading order (a folder before what it holds). Empty when a filter or the trash is listed."`
}

// CreateIn creates a note, clipping or folder.
type CreateIn struct {
	ID          string   `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	Title       string   `json:"title,omitempty"`
	Kind        string   `json:"kind,omitempty" enum:"note,clipping,folder" desc:"Default note."`
	ParentID    string   `json:"parent_id,omitempty" desc:"A folder to create it in; empty for the top level."`
	After       *string  `json:"after,omitempty" desc:"Sibling to place after; empty string for first; omit for last."`
	Body        string   `json:"body,omitempty" desc:"Markdown."`
	SourceURL   string   `json:"source_url,omitempty" desc:"A clipping's web address, as you entered it. It is not fetched."`
	RetrievedOn string   `json:"retrieved_on,omitempty" desc:"The date you read it, YYYY-MM-DD."`
	Quote       string   `json:"quote,omitempty" desc:"The passage a clipping keeps."`
	Status      string   `json:"status,omitempty" enum:"inbox,reading,useful,archived" desc:"Board column; default inbox."`
	Tags        []string `json:"tags,omitempty" desc:"Tag names; missing tags are created."`
}

func (in CreateIn) ProjectRef() string    { return in.ID }
func (in CreateIn) CompendiumRef() string { return in.ID }

// NoteIn names one note of an owner.
type NoteIn struct {
	ID     string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	NoteID string `path:"note_id" json:"-" desc:"Note id." ref:"note"`
}

func (in NoteIn) ProjectRef() string    { return in.ID }
func (in NoteIn) CompendiumRef() string { return in.ID }

// UpdateIn saves a note. Send the revision you last saw.
type UpdateIn struct {
	ID           string    `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	NoteID       string    `path:"note_id" json:"-" desc:"Note id." ref:"note"`
	BaseRevision int       `json:"base_revision" desc:"The revision you last saw; a stale one is a 409 with the current revision."`
	Title        *string   `json:"title,omitempty"`
	Body         *string   `json:"body,omitempty" desc:"Markdown."`
	SourceURL    *string   `json:"source_url,omitempty"`
	RetrievedOn  *string   `json:"retrieved_on,omitempty"`
	Quote        *string   `json:"quote,omitempty"`
	Status       *string   `json:"status,omitempty" enum:"inbox,reading,useful,archived"`
	Tags         *[]string `json:"tags,omitempty" desc:"Replaces the note's tags."`
}

func (in UpdateIn) ProjectRef() string    { return in.ID }
func (in UpdateIn) CompendiumRef() string { return in.ID }

// MoveIn moves a note to another folder or position.
type MoveIn struct {
	ID       string  `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	NoteID   string  `path:"note_id" json:"-" desc:"Note id." ref:"note"`
	ParentID *string `json:"parent_id,omitempty" desc:"The folder to move into; empty string for the top level; omit to stay in the same folder."`
	After    *string `json:"after,omitempty" desc:"Sibling to place after; empty string for first; omit for last."`
}

func (in MoveIn) ProjectRef() string    { return in.ID }
func (in MoveIn) CompendiumRef() string { return in.ID }

// TrashOut says how many notes a trash or restore moved.
type TrashOut struct {
	Item  Note `json:"item"`
	Count int  `json:"count" desc:"Notes moved: the note and, for a folder, what it holds."`
}

// BoardIn asks for the research board.
type BoardIn struct {
	ID string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	By string `query:"by" enum:"status,tag" desc:"Columns by status (default) or by tag. A note appears under each of its tags."`
}

func (in BoardIn) ProjectRef() string    { return in.ID }
func (in BoardIn) CompendiumRef() string { return in.ID }

// Column is one board column. The board is data: the columns, their order and
// the notes in each.
type Column struct {
	Key   string  `json:"key" desc:"A status, a tag id, or untagged."`
	Label string  `json:"label"`
	Color string  `json:"color,omitempty" desc:"A tag's colour token."`
	Count int     `json:"count"`
	Items []Entry `json:"items"`
}

// BoardOut is the board.
type BoardOut struct {
	By      string   `json:"by"`
	Columns []Column `json:"columns"`
}

// ---- registration ------------------------------------------------------------------------------

// Register adds the research, tag and scratchpad ops.
func Register(r *ops.Registry) {
	for _, kind := range []string{OwnerProject, OwnerCompendium} {
		kind := kind
		var p, base string
		var reader, editor ops.Auth
		what := "a project"
		if kind == OwnerProject {
			p, base = "project.", "/api/v1/projects/{id}"
			reader, editor = ops.RequireProject(ops.ProjectReader), ops.RequireProject(ops.ProjectEditor)
		} else {
			p, base = "compendium.", "/api/v1/compendiums/{id}"
			reader, editor = ops.RequireCompendium("reader"), ops.RequireCompendium("editor")
			what = "a Compendium"
		}
		rs := p + "research."
		ops.Register(r, ops.Op{Name: rs + "list", Method: "GET", Path: base + "/research", Auth: reader,
			Desc: "List " + what + "'s research notes, clippings and folders as a flat tree with reading order. Filter by tag, status or kind; trash=true lists the trash."},
			func(c *ops.Ctx, in ListIn) (ListOut, error) { return list(c, owner{kind, in.ID}, in) })
		ops.Register(r, ops.Op{Name: rs + "create", Method: "POST", Path: base + "/research", Status: 201, Auth: editor,
			Desc: "Create a research note, a clipping or a folder in " + what + ". A clipping's address, date and quote are what you enter; nothing is fetched."},
			func(c *ops.Ctx, in CreateIn) (Item, error) { return create(c, owner{kind, in.ID}, in) })
		ops.Register(r, ops.Op{Name: rs + "get", Method: "GET", Path: base + "/research/{note_id}", Auth: reader,
			Desc: "Get one research note with its body."},
			func(c *ops.Ctx, in NoteIn) (Item, error) {
				n, err := load(c, c.DB().Read(), owner{kind, in.ID}, in.NoteID)
				return Item{Item: n}, err
			})
		ops.Register(r, ops.Op{Name: rs + "update", Method: "PATCH", Path: base + "/research/{note_id}", Auth: editor,
			Desc: "Save a research note. Send the revision you last saw; a stale revision is a 409."},
			func(c *ops.Ctx, in UpdateIn) (Item, error) { return update(c, owner{kind, in.ID}, in) })
		ops.Register(r, ops.Op{Name: rs + "move", Method: "POST", Path: base + "/research/{note_id}/move", Auth: editor,
			Desc: "Move a research note to another folder or position."},
			func(c *ops.Ctx, in MoveIn) (Item, error) { return move(c, owner{kind, in.ID}, in) })
		ops.Register(r, ops.Op{Name: rs + "trash", Method: "POST", Path: base + "/research/{note_id}/trash", Auth: editor,
			Desc: "Move a note, and a folder's contents, to the trash."},
			func(c *ops.Ctx, in NoteIn) (TrashOut, error) { return trash(c, owner{kind, in.ID}, in, true) })
		ops.Register(r, ops.Op{Name: rs + "restore", Method: "POST", Path: base + "/research/{note_id}/restore", Auth: editor,
			Desc: "Restore a trashed note and what was trashed with it."},
			func(c *ops.Ctx, in NoteIn) (TrashOut, error) { return trash(c, owner{kind, in.ID}, in, false) })
		ops.Register(r, ops.Op{Name: rs + "board", Method: "GET", Path: base + "/research-board", Auth: reader,
			Desc: "The research board as data: columns by status or by tag, and the notes in each."},
			func(c *ops.Ctx, in BoardIn) (BoardOut, error) { return board(c, owner{kind, in.ID}, in) })
		registerTags(r, kind, p, base, reader, editor, what)
		registerScratch(r, kind, p, base, reader)
	}
}

// ---- ops -----------------------------------------------------------------------------------------

func hashContent(body, quote string) string {
	sum := sha256.Sum256([]byte(body + "\x00" + quote))
	return hex.EncodeToString(sum[:])
}

func entryOf(n Note) Entry {
	return Entry{ID: n.ID, ParentID: n.ParentID, Kind: n.Kind, Title: n.Title, SourceURL: n.SourceURL, RetrievedOn: n.RetrievedOn,
		Status: n.Status, Tags: n.Tags, Position: n.Position, Revision: n.Revision, WordCount: n.WordCount, Trashed: n.Trashed, UpdatedAt: n.UpdatedAt}
}

func list(c *ops.Ctx, o owner, in ListIn) (ListOut, error) {
	q := `SELECT n.id, COALESCE(n.parent_id,''), n.kind, n.title, n.source_url, n.retrieved_on, n.status, n.position, n.revision, n.word_count,
		n.deleted_at IS NOT NULL, n.updated_at FROM research_notes n WHERE n.` + o.col() + ` = ? AND n.deleted_at IS `
	args := []any{o.id}
	if in.Trash {
		q += `NOT NULL`
	} else {
		q += `NULL`
	}
	if in.Status != "" {
		if !oneOf(Statuses, in.Status) {
			return ListOut{}, ops.Invalid("status", "Unknown status.")
		}
		q += ` AND n.status = ?`
		args = append(args, in.Status)
	}
	if in.Kind != "" {
		if !oneOf(Kinds, in.Kind) {
			return ListOut{}, ops.Invalid("kind", "Unknown kind.")
		}
		q += ` AND n.kind = ?`
		args = append(args, in.Kind)
	}
	if in.Tag != "" {
		q += ` AND EXISTS (SELECT 1 FROM research_tags rt JOIN tags t ON t.id = rt.tag_id WHERE rt.note_id = n.id AND t.name_key = ? AND t.` + o.col() + ` = ?)`
		args = append(args, text.FoldTitle(in.Tag), o.id)
	}
	q += ` ORDER BY n.position, n.id LIMIT ?`
	args = append(args, MaxListed)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	out := ListOut{Items: []Entry{}, Order: []string{}}
	var ids []string
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ParentID, &e.Kind, &e.Title, &e.SourceURL, &e.RetrievedOn, &e.Status, &e.Position, &e.Revision, &e.WordCount, &e.Trashed, &e.UpdatedAt); err != nil {
			return ListOut{}, err
		}
		e.Tags = []string{}
		out.Items = append(out.Items, e)
		ids = append(ids, e.ID)
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	rows.Close()
	tags, err := tagsOf(c, c.DB().Read(), ids)
	if err != nil {
		return ListOut{}, err
	}
	for i := range out.Items {
		if t := tags[out.Items[i].ID]; t != nil {
			out.Items[i].Tags = t
		}
	}
	if !in.Trash && in.Tag == "" && in.Status == "" && in.Kind == "" {
		out.Order = order(out.Items)
	}
	return out, nil
}

// order lists ids depth first: a folder, then what it holds, by position.
func order(items []Entry) []string {
	kids := map[string][]Entry{}
	for _, e := range items {
		kids[e.ParentID] = append(kids[e.ParentID], e)
	}
	out := make([]string, 0, len(items))
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		ks := kids[parent]
		sort.SliceStable(ks, func(i, j int) bool {
			if ks[i].Position != ks[j].Position {
				return ks[i].Position < ks[j].Position
			}
			return ks[i].ID < ks[j].ID
		})
		for _, e := range ks {
			out = append(out, e.ID)
			if depth < maxDepth+1 {
				walk(e.ID, depth+1)
			}
		}
	}
	walk("", 0)
	return out
}

// folderOK checks that parent is a live folder of the owner and returns its depth.
func folderOK(tx *sql.Tx, o owner, parent string) (int, error) {
	depth := 0
	for p := parent; p != ""; depth++ {
		if depth > maxDepth {
			return 0, ops.Invalid("parent_id", "Folders can go 12 levels deep.")
		}
		var kind, next string
		var deleted bool
		err := tx.QueryRow(`SELECT kind, COALESCE(parent_id,''), deleted_at IS NOT NULL FROM research_notes WHERE id = ? AND `+o.col()+` = ?`, p, o.id).Scan(&kind, &next, &deleted)
		if err != nil {
			if err == sql.ErrNoRows {
				return 0, ops.Invalid("parent_id", "That folder does not exist here.")
			}
			return 0, err
		}
		if depth == 0 && (kind != "folder" || deleted) {
			return 0, ops.Invalid("parent_id", "That is not a folder.")
		}
		p = next
	}
	return depth, nil
}

// place finds the position for a note among its live siblings.
func place(tx *sql.Tx, o owner, parent, after, exclude string, atEnd bool) (float64, error) {
	rows, err := tx.Query(`SELECT id, position FROM research_notes WHERE `+o.col()+` = ? AND deleted_at IS NULL AND COALESCE(parent_id,'') = ? AND id != ? ORDER BY position, id`, o.id, parent, exclude)
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
	idx := len(sibs)
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
			return 0, ops.Invalid("after", "The note to place after is not in that folder.")
		}
	}
	var pos float64
	var need bool
	switch {
	case len(sibs) == 0:
		pos = db.PositionGap
	case idx == len(sibs):
		pos = sibs[idx-1].pos + db.PositionGap
	case idx == 0:
		pos, need = db.PositionBetween(-1, sibs[0].pos)
	default:
		pos, need = db.PositionBetween(sibs[idx-1].pos, sibs[idx].pos)
	}
	if need {
		re := db.Rebalanced(len(sibs) + 1)
		j := 0
		for i, s := range sibs {
			if i == idx {
				j++
			}
			if _, err := tx.Exec(`UPDATE research_notes SET position = ? WHERE id = ?`, re[j], s.id); err != nil {
				return 0, err
			}
			j++
		}
		pos = re[idx]
	}
	return pos, nil
}

func create(c *ops.Ctx, o owner, in CreateIn) (Item, error) {
	title, err := cleanTitle(in.Title)
	if err != nil {
		return Item{}, err
	}
	if in.Kind == "" {
		in.Kind = "note"
	}
	if !oneOf(Kinds, in.Kind) {
		return Item{}, ops.Invalid("kind", "Unknown kind.")
	}
	if in.Status == "" {
		in.Status = "inbox"
	}
	if !oneOf(Statuses, in.Status) {
		return Item{}, ops.Invalid("status", "Unknown status.")
	}
	if err := cleanBody(in.Body); err != nil {
		return Item{}, err
	}
	src, err := cleanSource(in.SourceURL)
	if err != nil {
		return Item{}, err
	}
	ret, err := cleanRetrieved(in.RetrievedOn)
	if err != nil {
		return Item{}, err
	}
	quote, err := cleanQuote(in.Quote)
	if err != nil {
		return Item{}, err
	}
	tags, err := cleanTagNames(in.Tags)
	if err != nil {
		return Item{}, err
	}
	if in.Kind == "folder" && (src != "" || quote != "") {
		return Item{}, ops.Invalid("kind", "A folder has no source or quote.")
	}
	id := db.NewID("rsn")
	var out Item
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := folderOK(tx, o, in.ParentID); err != nil {
			return err
		}
		after, atEnd := "", in.After == nil
		if in.After != nil {
			after = *in.After
		}
		pos, err := place(tx, o, in.ParentID, after, "", atEnd)
		if err != nil {
			return err
		}
		now := db.Now()
		var project, compendium, parent any
		if o.kind == OwnerProject {
			project = o.id
		} else {
			compendium = o.id
		}
		if in.ParentID != "" {
			parent = in.ParentID
		}
		words := text.WordCount(in.Body)
		if _, err := tx.Exec(`INSERT INTO research_notes(id, project_id, compendium_id, parent_id, kind, title, body, source_url, retrieved_on, quote, status,
			position, revision, word_count, content_hash, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, compendium, parent, in.Kind, title, in.Body, src, ret, quote, in.Status, pos, 1, words, hashContent(in.Body, quote),
			nullIfEmpty(c.P.UserID), now, now); err != nil {
			return err
		}
		if err := writeText(tx, o, id, title, src, in.Body, quote); err != nil {
			return err
		}
		if err := attachments.SyncRefs(tx, attachments.RefResearch, id, o.kind, o.id, in.Body); err != nil {
			return err
		}
		if _, err := setTags(tx, o, id, tags, now); err != nil {
			return err
		}
		n, err := load(c, tx, o, id)
		if err != nil {
			return err
		}
		out = Item{Item: n}
		c.Emit(o.event("research.created", map[string]any{"note_id": id, "kind": in.Kind, "words": words}))
		return c.FlushTo(tx)
	})
	return out, err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func update(c *ops.Ctx, o owner, in UpdateIn) (Item, error) {
	if in.BaseRevision < 1 {
		return Item{}, ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	var out Item
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		n, err := load(c, tx, o, in.NoteID)
		if err != nil {
			return err
		}
		if n.Trashed {
			return ops.Conflict("This note is in the trash; restore it first.")
		}
		if n.Revision != in.BaseRevision {
			return ops.ConflictWith("This note changed since you opened it.", map[string]any{"revision": n.Revision})
		}
		changed := false
		if in.Title != nil {
			t, err := cleanTitle(*in.Title)
			if err != nil {
				return err
			}
			changed = changed || t != n.Title
			n.Title = t
		}
		if in.Body != nil {
			if err := cleanBody(*in.Body); err != nil {
				return err
			}
			changed = changed || *in.Body != n.Body
			n.Body = *in.Body
		}
		if in.SourceURL != nil {
			s, err := cleanSource(*in.SourceURL)
			if err != nil {
				return err
			}
			changed = changed || s != n.SourceURL
			n.SourceURL = s
		}
		if in.RetrievedOn != nil {
			s, err := cleanRetrieved(*in.RetrievedOn)
			if err != nil {
				return err
			}
			changed = changed || s != n.RetrievedOn
			n.RetrievedOn = s
		}
		if in.Quote != nil {
			s, err := cleanQuote(*in.Quote)
			if err != nil {
				return err
			}
			changed = changed || s != n.Quote
			n.Quote = s
		}
		if in.Status != nil {
			if !oneOf(Statuses, *in.Status) {
				return ops.Invalid("status", "Unknown status.")
			}
			changed = changed || *in.Status != n.Status
			n.Status = *in.Status
		}
		if n.Kind == "folder" && (n.SourceURL != "" || n.Quote != "") {
			return ops.Invalid("kind", "A folder has no source or quote.")
		}
		var names []string
		tagsChanged := false
		if in.Tags != nil {
			if names, err = cleanTagNames(*in.Tags); err != nil {
				return err
			}
			have := map[string]bool{}
			for _, t := range n.Tags {
				have[text.FoldTitle(t)] = true
			}
			if len(names) != len(n.Tags) {
				tagsChanged = true
			}
			for _, t := range names {
				if !have[text.FoldTitle(t)] {
					tagsChanged = true
				}
			}
		}
		if !changed && !tagsChanged {
			out = Item{Item: n}
			return nil
		}
		now := db.Now()
		n.WordCount = text.WordCount(n.Body)
		n.Revision++
		n.UpdatedAt = now
		if _, err := tx.Exec(`UPDATE research_notes SET title = ?, body = ?, source_url = ?, retrieved_on = ?, quote = ?, status = ?, word_count = ?,
			content_hash = ?, revision = ?, updated_at = ? WHERE id = ?`, n.Title, n.Body, n.SourceURL, n.RetrievedOn, n.Quote, n.Status, n.WordCount,
			hashContent(n.Body, n.Quote), n.Revision, now, n.ID); err != nil {
			return err
		}
		if err := writeText(tx, o, n.ID, n.Title, n.SourceURL, n.Body, n.Quote); err != nil {
			return err
		}
		if in.Body != nil {
			if err := attachments.SyncRefs(tx, attachments.RefResearch, n.ID, o.kind, o.id, n.Body); err != nil {
				return err
			}
		}
		if in.Tags != nil {
			if _, err := setTags(tx, o, n.ID, names, now); err != nil {
				return err
			}
		}
		fresh, err := load(c, tx, o, n.ID)
		if err != nil {
			return err
		}
		out = Item{Item: fresh}
		c.Emit(o.event("research.updated", map[string]any{"note_id": n.ID, "revision": n.Revision, "words": n.WordCount}))
		return c.FlushTo(tx)
	})
	return out, err
}

func move(c *ops.Ctx, o owner, in MoveIn) (Item, error) {
	var out Item
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		n, err := load(c, tx, o, in.NoteID)
		if err != nil {
			return err
		}
		if n.Trashed {
			return ops.Conflict("This note is in the trash; restore it first.")
		}
		parent := n.ParentID
		if in.ParentID != nil {
			parent = *in.ParentID
		}
		if parent == n.ID {
			return ops.Invalid("parent_id", "A folder cannot hold itself.")
		}
		if _, err := folderOK(tx, o, parent); err != nil {
			return err
		}
		// Not into its own subtree.
		for p, hops := parent, 0; p != "" && hops <= maxDepth+1; hops++ {
			if p == n.ID {
				return ops.Invalid("parent_id", "A folder cannot be moved into itself.")
			}
			var next string
			if err := tx.QueryRow(`SELECT COALESCE(parent_id,'') FROM research_notes WHERE id = ?`, p).Scan(&next); err != nil {
				break
			}
			p = next
		}
		// The subtree under it must still fit.
		if n.Kind == "folder" {
			d, _ := folderOK(tx, o, parent)
			var deepest int
			_ = tx.QueryRow(`WITH RECURSIVE t(id, d) AS (SELECT id, 0 FROM research_notes WHERE id = ? UNION ALL
				SELECT r.id, t.d + 1 FROM research_notes r JOIN t ON r.parent_id = t.id WHERE t.d < 40) SELECT COALESCE(max(d), 0) FROM t`, n.ID).Scan(&deepest)
			if d+deepest+1 > maxDepth {
				return ops.Invalid("parent_id", "Folders can go 12 levels deep.")
			}
		}
		after, atEnd := "", in.After == nil
		if in.After != nil {
			after = *in.After
		}
		pos, err := place(tx, o, parent, after, n.ID, atEnd)
		if err != nil {
			return err
		}
		if parent == n.ParentID && pos == n.Position {
			out = Item{Item: n}
			return nil
		}
		now := db.Now()
		if _, err := tx.Exec(`UPDATE research_notes SET parent_id = ?, position = ?, updated_at = ? WHERE id = ?`, nullIfEmpty(parent), pos, now, n.ID); err != nil {
			return err
		}
		fresh, err := load(c, tx, o, n.ID)
		if err != nil {
			return err
		}
		out = Item{Item: fresh}
		c.Emit(o.event("research.moved", map[string]any{"note_id": n.ID}))
		return c.FlushTo(tx)
	})
	return out, err
}

func trash(c *ops.Ctx, o owner, in NoteIn, doTrash bool) (TrashOut, error) {
	var out TrashOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		n, err := load(c, tx, o, in.NoteID)
		if err != nil {
			return err
		}
		now := db.Now()
		var count int64
		if doTrash {
			if n.Trashed {
				out = TrashOut{Item: n}
				return nil
			}
			batch := db.NewID("trb")
			res, err := tx.Exec(`WITH RECURSIVE t(id) AS (SELECT id FROM research_notes WHERE id = ? UNION ALL
				SELECT r.id FROM research_notes r JOIN t ON r.parent_id = t.id)
				UPDATE research_notes SET deleted_at = ?, trash_batch = ?, updated_at = ? WHERE id IN (SELECT id FROM t) AND deleted_at IS NULL`, n.ID, now, batch, now)
			if err != nil {
				return err
			}
			count, _ = res.RowsAffected()
		} else {
			if !n.Trashed {
				out = TrashOut{Item: n}
				return nil
			}
			var batch sql.NullString
			_ = tx.QueryRow(`SELECT trash_batch FROM research_notes WHERE id = ?`, n.ID).Scan(&batch)
			// Everything restored whose folder is still in the trash (trashed by a
			// different act, or later) comes back at the top level, or it would be
			// live under a parent the tree never reaches. One statement over the
			// whole batch, before the batch is released, and the folders that come
			// back with it are not "still trashed".
			if _, err := tx.Exec(`UPDATE research_notes SET parent_id = NULL WHERE `+o.col()+` = ? AND (id = ? OR (trash_batch = ? AND ? <> ''))
				AND parent_id IN (SELECT id FROM research_notes WHERE deleted_at IS NOT NULL AND id <> ? AND (trash_batch IS NULL OR trash_batch <> ?))`,
				o.id, n.ID, batch.String, batch.String, n.ID, batch.String); err != nil {
				return err
			}
			res, err := tx.Exec(`UPDATE research_notes SET deleted_at = NULL, trash_batch = NULL, updated_at = ? WHERE `+o.col()+` = ? AND (id = ? OR (trash_batch = ? AND ? <> ''))`,
				now, o.id, n.ID, batch.String, batch.String)
			if err != nil {
				return err
			}
			count, _ = res.RowsAffected()
		}
		fresh, err := load(c, tx, o, n.ID)
		if err != nil {
			return err
		}
		out = TrashOut{Item: fresh, Count: int(count)}
		typ := "research.restored"
		if doTrash {
			typ = "research.trashed"
		}
		c.Emit(o.event(typ, map[string]any{"note_id": n.ID, "count": int(count)}))
		return c.FlushTo(tx)
	})
	return out, err
}

func board(c *ops.Ctx, o owner, in BoardIn) (BoardOut, error) {
	by := in.By
	if by == "" {
		by = "status"
	}
	if by != "status" && by != "tag" {
		return BoardOut{}, ops.Invalid("by", "Columns are by status or by tag.")
	}
	l, err := list(c, o, ListIn{ID: o.id})
	if err != nil {
		return BoardOut{}, err
	}
	var notes []Entry
	for _, e := range l.Items {
		if e.Kind != "folder" {
			notes = append(notes, e)
		}
	}
	out := BoardOut{By: by, Columns: []Column{}}
	if by == "status" {
		for _, s := range Statuses {
			col := Column{Key: s, Label: strings.ToUpper(s[:1]) + s[1:], Items: []Entry{}}
			for _, e := range notes {
				if e.Status == s {
					col.Items = append(col.Items, e)
				}
			}
			col.Count = len(col.Items)
			out.Columns = append(out.Columns, col)
		}
		return out, nil
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, name, color FROM tags WHERE `+o.col()+` = ? ORDER BY name_key, id`, o.id)
	if err != nil {
		return BoardOut{}, err
	}
	defer rows.Close()
	type tg struct{ id, name, color string }
	var tgs []tg
	for rows.Next() {
		var t tg
		if err := rows.Scan(&t.id, &t.name, &t.color); err != nil {
			return BoardOut{}, err
		}
		tgs = append(tgs, t)
	}
	if err := rows.Err(); err != nil {
		return BoardOut{}, err
	}
	rows.Close()
	byName := map[string][]Entry{}
	var untagged []Entry
	for _, e := range notes {
		if len(e.Tags) == 0 {
			untagged = append(untagged, e)
		}
		for _, t := range e.Tags {
			byName[text.FoldTitle(t)] = append(byName[text.FoldTitle(t)], e)
		}
	}
	for _, t := range tgs {
		items := byName[text.FoldTitle(t.name)]
		if items == nil {
			items = []Entry{}
		}
		out.Columns = append(out.Columns, Column{Key: t.id, Label: t.name, Color: t.color, Count: len(items), Items: items})
	}
	if untagged == nil {
		untagged = []Entry{}
	}
	out.Columns = append(out.Columns, Column{Key: "untagged", Label: "Untagged", Count: len(untagged), Items: untagged})
	return out, nil
}

var _ = context.Background

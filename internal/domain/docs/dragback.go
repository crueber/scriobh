package docs

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// stash.drag_back puts a stash item into a document. It is an edit of the
// document like any other: it goes through the same write path as doc.update
// (revision checked, the single-writer lease, history, counts, search, links),
// and it only ever happens when someone asks. The words were written before,
// so the writer's goal earns nothing from them.

// DragBackIn inserts a stash item into a document.
type DragBackIn struct {
	ID           string `path:"id" json:"-" desc:"Project id."`
	ItemID       string `json:"item_id" desc:"The stash item."`
	DocumentID   string `json:"document_id" desc:"The document to put it in (in the same project)."`
	BaseRevision int    `json:"base_revision" desc:"The document revision you last saw."`
	TabID        string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease. Leave empty for API clients."`
	At           *int   `json:"at,omitempty" desc:"Where, in UTF-16 code units of the body (the text before the alternatives comment; line endings as LF). Omit for the end of the document."`
	Placement    string `json:"placement,omitempty" enum:"block,inline" desc:"block (default): the text goes in as its own paragraph, before the block that starts at the position or after the one that contains it. inline: the text goes in at the position, inside a paragraph, and must be one paragraph long."`
	Mode         string `json:"mode,omitempty" enum:"copy,move" desc:"copy (default): the item stays in the stash. move: it goes to the stash trash."`
	WithContent  bool   `json:"with_content,omitempty" desc:"Return the whole stored text."`
}

// ProjectRef lets the registry find the project.
func (in DragBackIn) ProjectRef() string { return in.ID }

// DragBackOut says what was inserted.
type DragBackOut struct {
	Revision    int    `json:"revision"`
	ContentHash string `json:"content_hash"`
	WordCount   int    `json:"word_count"`
	Start       int    `json:"start" desc:"Where the inserted text starts, in UTF-16 code units of the new body."`
	End         int    `json:"end"`
	Comment     string `json:"comment" desc:"The alternatives comment that ends the text now ('' when none)."`
	Moved       bool   `json:"moved" desc:"The item was moved to the stash trash."`
	Content     string `json:"content,omitempty"`
}

func dragBack(c *ops.Ctx, l *leases, in DragBackIn) (DragBackOut, error) {
	if in.ItemID == "" || len(in.ItemID) > 64 {
		return DragBackOut{}, ops.Invalid("item_id", "Name the stash item.")
	}
	if in.DocumentID == "" {
		return DragBackOut{}, ops.Invalid("document_id", "Name the document.")
	}
	if in.Placement == "" {
		in.Placement = "block"
	}
	if in.Mode == "" {
		in.Mode = "copy"
	}
	if (in.Placement != "block" && in.Placement != "inline") || (in.Mode != "copy" && in.Mode != "move") {
		return DragBackOut{}, ops.Invalid("placement", "placement is block or inline; mode is copy or move.")
	}
	var out DragBackOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var content string
		var trashed sql.NullString
		if err := tx.QueryRow(`SELECT content, deleted_at FROM stash_items WHERE id = ? AND project_id = ?`, in.ItemID, in.ID).Scan(&content, &trashed); err != nil {
			return ops.NotFound("stash item")
		}
		if trashed.Valid {
			return ops.Conflict("This stash item is in the trash. Restore it first.")
		}
		var owner string
		if err := tx.QueryRow(`SELECT project_id FROM documents WHERE id = ?`, in.DocumentID).Scan(&owner); err != nil || owner != in.ID {
			return ops.NotFound("document")
		}
		s, err := openAlt(c, tx, l, in.DocumentID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		ins := strings.TrimLeft(strings.TrimRight(alts.NormalizeEOL(content), " \t\n"), "\n")
		if strings.TrimSpace(ins) == "" {
			return ops.Invalid("item_id", "This stash item has no text to put in.")
		}
		if len(ins)+len(s.body) > maxContent {
			return ops.TooLarge()
		}
		body := s.body
		at := len(body)
		if in.At != nil {
			b, ok := s.index().Byte(*in.At)
			if !ok {
				return ops.Invalid("at", "That position is not inside the text (offsets are UTF-16 code units of the body).")
			}
			at = b
		}
		nb, edit, start, err := placeText(body, at, ins, in.Placement)
		if err != nil {
			return err
		}
		if in.Placement == "inline" {
			for _, it := range s.items {
				if it.Orphan || !it.Placed() {
					continue
				}
				a, ok1 := s.index().Byte(it.Start)
				b, ok2 := s.index().Byte(it.End)
				if ok1 && ok2 && at > a && at < b {
					return ops.Conflict("That position is inside an alternative (" + alts.Clip(it.ChosenText()) + "). Put the text next to it, not into it.")
				}
			}
		}
		items, _ := alts.Rebase(body, nb, s.items, []alts.Edit{edit}, s.ro)
		if in.Mode == "move" {
			now := db.Now()
			if _, err := tx.Exec(`UPDATE stash_items SET deleted_at = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, now, now, in.ItemID); err != nil {
				return err
			}
			c.Emit(ops.Event{Type: "stash.trashed", ProjectID: in.ID})
		}
		s.quiet = true
		m, err := s.finish(nb, items, commitOpts{NoCredit: true}, "drag_back", "", in.WithContent)
		if err != nil {
			return err
		}
		ix := alts.NewIndex(nb)
		out = DragBackOut{Revision: m.Revision, ContentHash: m.ContentHash, WordCount: m.WordCount, Comment: m.Comment, Content: m.Content,
			Start: ix.Unit(start), End: ix.Unit(start + len(ins)), Moved: in.Mode == "move"}
		return nil
	})
	return out, err
}

// placeText inserts ins into body at byte offset at, and says what edit that
// was and where the inserted text starts. A block insert goes in as its own
// paragraph: at the start of the block that begins at the position, otherwise
// after the paragraph that contains it.
func placeText(body string, at int, ins, placement string) (string, alts.Edit, int, error) {
	if placement == "inline" {
		if strings.Contains(ins, "\n\n") {
			return "", alts.Edit{}, 0, ops.Invalid("placement", "Only a single paragraph can be put in inline; use block placement.")
		}
		blocks, ok := text.Blocks(body)
		in := false
		for _, b := range blocks {
			if b.Start <= at && at <= b.End {
				in = true
			}
		}
		if !ok || !in {
			return "", alts.Edit{}, 0, ops.Invalid("at", "Inline text goes inside a paragraph or heading; that position is not in one.")
		}
		if insideSourceBlock(body, at) {
			return "", alts.Edit{}, 0, ops.Invalid("at", "That position is inside a code block, comment or other preserved block.")
		}
		if alts.TouchesMarker(body, at, at) || alts.CutsGrapheme(body, at) {
			return "", alts.Edit{}, 0, ops.Invalid("at", "That position is inside a ghost tag or a character (an emoji sequence, a flag, a letter with its accent).")
		}
		return body[:at] + ins + body[at:], alts.Edit{Start: at, End: at, NewLen: len(ins)}, at, nil
	}
	atBlockStart := at < len(body) && (at == 0 || strings.HasSuffix(body[:at], "\n\n"))
	if !atBlockStart {
		if q := strings.Index(body[at:], "\n\n"); q >= 0 {
			at += q + 2
			for at < len(body) && body[at] == '\n' {
				at++
			}
			atBlockStart = at < len(body)
		}
	}
	if atBlockStart {
		if insideSourceBlock(body, at) {
			return "", alts.Edit{}, 0, ops.Invalid("at", "That position is inside a code block, comment or other preserved block. Choose a position between blocks.")
		}
		add := ins + "\n\n"
		return body[:at] + add + body[at:], alts.Edit{Start: at, End: at, NewLen: len(add)}, at, nil
	}
	// After the last block.
	keep := len(strings.TrimRight(body, "\n"))
	if insideSourceBlock(body, keep) && keep < len(body) {
		return "", alts.Edit{}, 0, ops.Invalid("at", "The text ends inside a preserved block.")
	}
	tail := "\n\n" + ins + "\n"
	if keep == 0 {
		tail = ins + "\n"
	}
	nb := body[:keep] + tail
	start := keep + len(tail) - len(ins) - 1
	return nb, alts.Edit{Start: keep, End: len(body), NewLen: len(tail)}, start, nil
}

func registerDragBack(r *ops.Registry, l *leases) {
	ops.Register(r, ops.Op{Name: "stash.drag_back", Method: "POST", Path: "/api/v1/projects/{id}/stash/drag-back",
		Desc: "Put a stash item into a document, as its own paragraph (block) or inside one (inline), at a position or at the end. An explicit edit through the document's write path: the revision is checked and a history entry is kept; nothing is ever put in automatically. With mode move the item goes to the stash trash.",
		Auth: ops.RequireProject(ops.ProjectEditor)},
		func(c *ops.Ctx, in DragBackIn) (DragBackOut, error) { return dragBack(c, l, in) })
}

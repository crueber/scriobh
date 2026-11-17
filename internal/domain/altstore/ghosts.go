package altstore

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/text"
)

// Ghost is one ghosted range of a document, as the index holds it.
type Ghost struct {
	ID         string `json:"id"`
	Start      int    `json:"start" desc:"Where the ghosted text starts, in UTF-16 code units of the body (the text before the alternatives comment, line endings as LF)."`
	End        int    `json:"end" desc:"Where it ends."`
	Words      int    `json:"words" desc:"Words the range holds: what the word count leaves out."`
	Preview    string `json:"preview" desc:"The first 160 characters of the ghosted text."`
	CreatedBy  string `json:"created_by,omitempty"`
	CreatedAt  string `json:"created_at"`
	text, hash string
}

const previewRunes = 160

// LoadGhosts reads a document's ghost index in document order.
func LoadGhosts(q querier, docID string) ([]Ghost, error) {
	rows, err := q.Query(`SELECT id, anchor_start, anchor_end, word_count, preview, text_hash, COALESCE(created_by,''), created_at
		FROM ghosts WHERE document_id = ? ORDER BY anchor_start, id`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ghost
	for rows.Next() {
		var g Ghost
		if err := rows.Scan(&g.ID, &g.Start, &g.End, &g.Words, &g.Preview, &g.hash, &g.CreatedBy, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SyncGhosts rebuilds a document's ghost index from its text, inside the
// caller's transaction. The Markdown is the truth: the index only gives a
// ghost an id that lasts across saves (a ghost is matched to its row by the
// hash of its text), and records who ghosted it and when. A document with no
// ghost tags costs one indexed delete.
func SyncGhosts(tx *sql.Tx, docID, projectID, content, actor, now string) error {
	body := alts.NormalizeEOL(alts.BodyOf(content))
	if !strings.Contains(body, text.GhostOpen) {
		_, err := tx.Exec(`DELETE FROM ghosts WHERE document_id = ?`, docID)
		return err
	}
	spans, _ := text.Ghosts(body)
	old, err := LoadGhosts(tx, docID)
	if err != nil {
		return err
	}
	ix := alts.NewIndex(body)
	used := make([]bool, len(old))
	keep := map[string]bool{}
	for _, sp := range spans {
		txt := body[sp.OpenEnd:sp.CloseStart]
		hash := hashText(txt)
		start, end := ix.Unit(sp.OpenEnd), ix.Unit(sp.CloseStart)
		match := -1
		for i, o := range old { // the first unused row with this text, in order
			if !used[i] && o.hash == hash {
				match = i
				break
			}
		}
		if match >= 0 {
			used[match] = true
			o := old[match]
			keep[o.ID] = true
			if o.Start != start || o.End != end || o.Words != sp.Words {
				if _, err := tx.Exec(`UPDATE ghosts SET anchor_start = ?, anchor_end = ?, word_count = ? WHERE id = ?`, start, end, sp.Words, o.ID); err != nil {
					return err
				}
			}
			continue
		}
		id := db.NewID("gst")
		keep[id] = true
		if _, err := tx.Exec(`INSERT INTO ghosts(id, document_id, project_id, anchor_start, anchor_end, text_hash, preview, word_count, created_by, created_at)
			VALUES (?,?,?,?,?,?,?,?,(SELECT id FROM users WHERE id = ?),?)`,
			id, docID, projectID, start, end, hash, preview(txt), sp.Words, actor, now); err != nil {
			return err
		}
	}
	for _, o := range old {
		if !keep[o.ID] {
			if _, err := tx.Exec(`DELETE FROM ghosts WHERE id = ?`, o.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func preview(s string) string {
	n := 0
	for i := range s {
		if n == previewRunes {
			return strings.TrimSpace(s[:i])
		}
		n++
	}
	return strings.TrimSpace(s)
}

package attachments

import (
	"database/sql"
	"regexp"
	"sort"
	"strings"
)

// Where an attachment is used is read off the Markdown when a document or a
// research note is saved: any link or image whose address is /files/att_<id>.
// The rows are what attachment.refs reports and what keeps attachment.delete
// from removing an image something still shows. Only an attachment of the
// writer's own project (or Compendium) counts, so a pasted address from
// elsewhere neither records a reference nor reveals that the attachment exists.

// Kinds of thing that can hold a reference.
const (
	RefDoc      = "doc"
	RefResearch = "research"
)

// Owner kinds.
const (
	OwnerProject    = "project"
	OwnerCompendium = "compendium"
)

const marker = "/files/att_"

var refRe = regexp.MustCompile(`/files/(att_[0-9A-HJKMNP-TV-Z]{26})`)

// IDs returns the distinct attachment ids a text links to, sorted. The common
// case, text with no attachment in it, is one substring search.
func IDs(content string) []string {
	if !strings.Contains(content, marker) {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range refRe.FindAllStringSubmatch(content, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// SyncRefs makes the references of one document or note match its text. It
// runs inside the caller's transaction; for text with no attachment it is one
// indexed delete that finds nothing. owner is OwnerProject or OwnerCompendium.
func SyncRefs(tx *sql.Tx, refKind, refID, owner, ownerID, content string) error {
	if _, err := tx.Exec(`DELETE FROM attachment_refs WHERE ref_kind = ? AND ref_id = ?`, refKind, refID); err != nil {
		return err
	}
	ids := IDs(content)
	if len(ids) == 0 {
		return nil
	}
	col := "project_id"
	if owner == OwnerCompendium {
		col = "compendium_id"
	}
	const chunk = 200
	for len(ids) > 0 {
		n := len(ids)
		if n > chunk {
			n = chunk
		}
		args := []any{refKind, refID, ownerID}
		ph := make([]string, n)
		for i, id := range ids[:n] {
			ph[i] = "?"
			args = append(args, id)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO attachment_refs(attachment_id, ref_kind, ref_id)
			SELECT id, ?1, ?2 FROM attachments WHERE `+col+` = ?3 AND id IN (`+strings.Join(ph, ",")+`)`, args...); err != nil {
			return err
		}
		ids = ids[n:]
	}
	return nil
}

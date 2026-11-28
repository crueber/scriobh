package mentions

import (
	"context"
	"database/sql"
)

// MergeCount is how many index rows a merge of source into target moves. The
// registry hands it to entities.RegisterMergeStep (entities cannot be imported
// here without a cycle).
func MergeCount(ctx context.Context, q Querier, sourceID, targetID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM entity_mentions WHERE entity_id = ?`, sourceID).Scan(&n)
	return n, err
}

// MergeApply moves the source's mention rows to the target inside the merge's
// transaction. Where a document mentions both, the counts add (the same words
// can no longer be told apart), the earlier first occurrence wins, and the
// source's row goes. The stamp has moved too (the source's name is gone from
// the vocabulary and may now be an alias of the target), so the worker
// rescans; until it does the index is already the merged answer.
func MergeApply(tx *sql.Tx, sourceID, targetID string) error {
	rows, err := tx.Query(`SELECT document_id, project_id, count, names, aliases, explicit, ambiguous, first_pos, snippet FROM entity_mentions WHERE entity_id = ?`, sourceID)
	if err != nil {
		return err
	}
	type moved struct {
		doc, project string
		r            Row
	}
	var all []moved
	for rows.Next() {
		var m moved
		var amb int
		if err := rows.Scan(&m.doc, &m.project, &m.r.Count, &m.r.Names, &m.r.Aliases, &m.r.Explicit, &amb, &m.r.FirstPos, &m.r.Snippet); err != nil {
			rows.Close()
			return err
		}
		m.r.Ambiguous = amb == 1
		all = append(all, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, m := range all {
		var t Row
		var amb int
		err := tx.QueryRow(`SELECT count, names, aliases, explicit, ambiguous, first_pos, snippet FROM entity_mentions WHERE document_id = ? AND entity_id = ?`,
			m.doc, targetID).Scan(&t.Count, &t.Names, &t.Aliases, &t.Explicit, &amb, &t.FirstPos, &t.Snippet)
		switch {
		case err == sql.ErrNoRows:
			if _, err := tx.Exec(`UPDATE entity_mentions SET entity_id = ? WHERE document_id = ? AND entity_id = ?`, targetID, m.doc, sourceID); err != nil {
				return err
			}
			continue
		case err != nil:
			return err
		}
		t.Ambiguous = amb == 1 || m.r.Ambiguous
		t.Count += m.r.Count
		t.Names += m.r.Names
		t.Aliases += m.r.Aliases
		t.Explicit += m.r.Explicit
		if m.r.FirstPos < t.FirstPos {
			t.FirstPos, t.Snippet = m.r.FirstPos, m.r.Snippet
		}
		ambInt := 0
		if t.Ambiguous {
			ambInt = 1
		}
		if _, err := tx.Exec(`UPDATE entity_mentions SET count = ?, names = ?, aliases = ?, explicit = ?, ambiguous = ?, first_pos = ?, snippet = ? WHERE document_id = ? AND entity_id = ?`,
			t.Count, t.Names, t.Aliases, t.Explicit, ambInt, t.FirstPos, t.Snippet, m.doc, targetID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM entity_mentions WHERE document_id = ? AND entity_id = ?`, m.doc, sourceID); err != nil {
			return err
		}
	}
	return nil
}

package imports

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// What import.undo compares to know a document was left alone (migration 1322). At the end of the import transaction a hash is taken of the
// document's own columns and of what hangs from it in other tables; an undo takes the hashes again, and a document whose either differs is
// kept. Nothing here is a guess about what a person did: any change at all, to anything on the lists, keeps the document, and a new column of
// documents is on the list by being there.

// stateSkip are the columns of a document that are not compared: the stamps, the revision (compared on its own), the trash marks, and what is
// derived from the text (whose hash is in content_hash, which is compared).
var stateSkip = map[string]bool{"updated_at": true, "content_at": true, "content_by": true, "deleted_at": true, "trash_batch": true, "revision": true,
	"word_count": true, "char_count": true}

// attachSpec is a table that attaches something to a document that people make: what is in it for a document is counted (with the newest
// stamp, where there is one), and a different count or stamp is a change.
type attachSpec struct{ table, col, stamp string }

// attachTables are every table that attaches something to a document of the manuscript by people's acts. The import makes some of the rows
// itself (a Word file's comments and suggestions, alternatives, an EPUB's edition chapters): they are in the hash taken at the end of the
// import, so they are not a change.
var attachTables = []attachSpec{
	{"comments", "document_id", "created_at"},
	{"suggestions", "document_id", "created_at"},
	{"alternatives", "document_id", "created_at"},
	{"ghosts", "document_id", "created_at"},
	{"document_tags", "document_id", ""},
	{"share_link_docs", "document_id", ""},
	{"timeline_event_docs", "document_id", "created_at"},
	{"site_docs", "document_id", "updated_at"},
	{"studio_chapters_done", "document_id", "created_at"},
	{"stash_items", "source_document_id", "created_at"},
	{"ai_pending", "document_id", "created_at"},
	{"ai_reviews", "document_id", "created_at"},
	{"ai_marks", "document_id", "created_at"},
	{"ai_runs", "document_id", ""},
	{"edition_chapters", "document_id", ""},
}

// derivedTables refer to a document and are not compared, each with the reason. A test lists every table that refers to a document and
// fails for one that is on neither list, so a new one is decided when it is added.
var derivedTables = map[string]string{
	"document_sources":     "this record itself",
	"document_links":       "derived from the text, and re-resolved whenever another document is titled or trashed",
	"revision_blobs":       "the history of the text: any save that changes it also changes the revision and the hash",
	"document_revisions":   "the history of the text: any save that changes it also changes the revision and the hash",
	"entity_mentions":      "derived from the text by a worker after the import, and again after any save",
	"doc_text":             "derived from the text (the search index)",
	"embed_docs":           "derived from the text (the semantic index)",
	"embed_chunks":         "derived from the text (the semantic index)",
	"writing_session_docs": "a writing session's baseline: written only by saving text, which changes the hash",
	"notifications":        "a notice about a comment or suggestion, which are compared themselves",
	"user_project_state":   "the document a person last opened",
	"events":               "the audit of what happened, not a state",
	"webhook_deliveries":   "what was sent out, not a state",
	"writing_sessions":     "a person's writing session",
}

type docState struct{ cols, attach string }

// docStates reads, for every document of one import, the hashes of its state.
func docStates(ctx context.Context, q rower, sourceID string) (map[string]docState, error) {
	out := map[string]docState{}
	rows, err := q.QueryContext(ctx, `SELECT * FROM documents WHERE id IN (SELECT document_id FROM document_sources WHERE source_id = ?)`, sourceID)
	if err != nil {
		return nil, err
	}
	names, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}
	idCol := -1
	for i, n := range names {
		if n == "id" {
			idCol = i
		}
	}
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			rows.Close()
			return nil, err
		}
		h := sha256.New()
		for i, n := range names {
			if stateSkip[n] {
				continue
			}
			v := vals[i]
			if b, ok := v.([]byte); ok {
				v = string(b)
			}
			fmt.Fprintf(h, "%s=%T:%v\x00", n, v, v)
		}
		out[fmt.Sprint(vals[idCol])] = docState{cols: hex.EncodeToString(h.Sum(nil))}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	parts := map[string][]string{}
	for _, a := range attachTables {
		q2 := `SELECT ` + a.col + `, count(*)`
		if a.stamp != "" {
			q2 += `, COALESCE(max(` + a.stamp + `), '')`
		}
		q2 += ` FROM ` + a.table + ` WHERE ` + a.col + ` IN (SELECT document_id FROM document_sources WHERE source_id = ?) GROUP BY ` + a.col
		r, err := q.QueryContext(ctx, q2, sourceID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", a.table, err)
		}
		for r.Next() {
			var id string
			var n int
			var stamp string
			dst := []any{&id, &n}
			if a.stamp != "" {
				dst = append(dst, &stamp)
			}
			if err := r.Scan(dst...); err != nil {
				r.Close()
				return nil, err
			}
			parts[id] = append(parts[id], fmt.Sprintf("%s:%d:%s", a.table, n, stamp))
		}
		if err := r.Err(); err != nil {
			r.Close()
			return nil, err
		}
		r.Close()
	}
	for id, st := range out {
		ps := parts[id]
		sort.Strings(ps)
		sum := sha256.Sum256([]byte(strings.Join(ps, ";")))
		st.attach = hex.EncodeToString(sum[:])
		out[id] = st
	}
	return out, nil
}

// recordStates writes the hashes of the state the import left its documents in, in the import's own transaction.
func recordStates(ctx context.Context, tx *sql.Tx, sourceID string) error {
	st, err := docStates(ctx, txRower{tx}, sourceID)
	if err != nil {
		return err
	}
	for id, s := range st {
		if _, err := tx.Exec(`UPDATE document_sources SET state_hash = ?, attach_hash = ? WHERE document_id = ?`, s.cols, s.attach, id); err != nil {
			return err
		}
	}
	return nil
}

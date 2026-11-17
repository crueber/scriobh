package links

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/text"
)

// IndexVersion says which generation of the link index a database holds. The
// start-up rebuild runs when the stored version is older (the index was never
// built, or the title folding changed and stored keys must be recomputed).
const IndexVersion = "2"

// Settings keys of the start-up rebuild.
const (
	// IndexKey holds the IndexVersion the index was last rebuilt at.
	IndexKey = "links.rebuilt"
	// PendingPrefix starts the key of a marker written in the transaction that
	// stored documents whose links are written afterwards (an import). The key
	// is PendingPrefix + project id; the value is the project id. The writer of
	// the links deletes it when it is done; a start finding one rebuilds that
	// project's links.
	PendingPrefix = "links.pending."
)

// Batch limits. A chunk ends when it has written ChunkRows link rows or has
// held the writer for ChunkBudget, whichever comes first, so no chunk holds the
// single writer for long however large the batch.
const (
	ChunkRows   = 8_000
	ChunkBudget = 120 * time.Millisecond
)

// SyncIn is the document whose outgoing links to rewrite.
type SyncIn struct {
	ProjectID  string
	DocumentID string
	Content    string
	Now        string // stored time for new rows; empty means now
}

type existing struct {
	id, alias, snippet, to, key string
	ord, occurrences            int
	ambiguous                   bool
}

// syncer writes link rows inside one transaction, sharing one resolver (one
// read of the project's tree) and one prepared insert across every document.
type syncer struct {
	tx  *sql.Tx
	res *resolver
	ins *sql.Stmt
	now string
}

func newSyncer(tx *sql.Tx, projectID, now string) *syncer {
	if now == "" {
		now = db.Now()
	}
	return &syncer{tx: tx, res: newResolver(tx, projectID), now: now}
}

func (s *syncer) close() {
	if s.ins != nil {
		s.ins.Close()
	}
}

func (s *syncer) insert(docID string, i int, l text.WikiLink, key string, to string, amb int) error {
	if s.ins == nil {
		st, err := s.tx.Prepare(`INSERT INTO document_links(id, from_id, to_id, kind, link_text, target_key, alias, ord, occurrences, ambiguous, snippet, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(from_id, kind, link_text) DO UPDATE SET to_id = excluded.to_id, target_key = excluded.target_key, alias = excluded.alias,
				ord = excluded.ord, occurrences = excluded.occurrences, ambiguous = excluded.ambiguous, snippet = excluded.snippet`)
		if err != nil {
			return err
		}
		s.ins = st
	}
	var toVal any
	if to != "" {
		toVal = to
	}
	_, err := s.ins.Exec(db.NewID("lnk"), docID, toVal, KindWiki, l.Target, key, l.Alias, i, l.Count, amb, l.Snippet, s.now)
	return err
}

// sync makes the document's stored links match found. fresh says the document
// has no link rows yet (a document just inserted), which skips reading them.
func (s *syncer) sync(docID string, found []text.WikiLink, fresh bool) error {
	if len(found) == 0 {
		if fresh {
			return nil
		}
		_, err := s.tx.Exec(`DELETE FROM document_links WHERE from_id = ? AND kind = ?`, docID, KindWiki)
		return err
	}
	have := map[string]existing{}
	if !fresh {
		rows, err := s.tx.Query(`SELECT id, link_text, alias, snippet, COALESCE(to_id,''), target_key, ord, occurrences, ambiguous FROM document_links WHERE from_id = ? AND kind = ?`, docID, KindWiki)
		if err != nil {
			return err
		}
		for rows.Next() {
			var e existing
			var lt string
			var amb int
			if err := rows.Scan(&e.id, &lt, &e.alias, &e.snippet, &e.to, &e.key, &e.ord, &e.occurrences, &amb); err != nil {
				rows.Close()
				return err
			}
			e.ambiguous = amb == 1
			have[lt] = e
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	keep := make(map[string]bool, len(found))
	for i, l := range found {
		keep[l.Target] = true
		key := text.FoldTitle(l.Target)
		to, amb, err := s.res.resolve(key)
		if err != nil {
			return err
		}
		ambInt := 0
		if amb {
			ambInt = 1
		}
		e, ok := have[l.Target]
		if !ok {
			if err := s.insert(docID, i, l, key, to, ambInt); err != nil {
				return err
			}
			continue
		}
		if e.to == to && e.ambiguous == amb && e.alias == l.Alias && e.snippet == l.Snippet && e.ord == i && e.occurrences == l.Count && e.key == key {
			continue
		}
		var toVal any
		if to != "" {
			toVal = to
		}
		if _, err := s.tx.Exec(`UPDATE document_links SET to_id = ?, ambiguous = ?, alias = ?, snippet = ?, ord = ?, occurrences = ?, target_key = ? WHERE id = ?`,
			toVal, ambInt, l.Alias, l.Snippet, i, l.Count, key, e.id); err != nil {
			return err
		}
	}
	for t, e := range have {
		if !keep[t] {
			if _, err := s.tx.Exec(`DELETE FROM document_links WHERE id = ?`, e.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Sync makes the document's stored outgoing links match its body. It adds
// links that appeared, removes ones that are gone, and updates the rest, in the
// caller's transaction. A body with no `[[` costs one DELETE.
func Sync(tx *sql.Tx, in SyncIn) error {
	s := newSyncer(tx, in.ProjectID, in.Now)
	defer s.close()
	return s.sync(in.DocumentID, text.WikiLinks(in.Content), false)
}

// Job is one document's links to store, parsed by the caller outside any
// transaction.
type Job struct {
	DocumentID string
	// Hash is the document's content_hash the links were read from. A
	// document that is gone, or whose hash has changed by the time its chunk
	// runs, is skipped: whoever changed it wrote its links already.
	Hash  string
	Links []text.WikiLink
	// Fresh says the document has no link rows yet (it was just inserted).
	Fresh bool
}

// BatchStats says what SyncBatch did.
type BatchStats struct {
	Chunks  int
	Jobs    int
	Skipped int // jobs whose document was gone or had changed
	Rows    int // link rows offered, not counting skipped jobs
	Longest time.Duration
}

// SyncBatch stores the links of many documents of one project without holding
// the single writer for long: the work is cut into chunks (ChunkRows rows or
// ChunkBudget of writer time), each its own short transaction with its own
// resolver, so the tree is read once per chunk, never once per document, and a
// save elsewhere gets in between chunks. The parsing is the caller's, done
// before the call.
func SyncBatch(ctx context.Context, d *db.DB, projectID, now string, jobs []Job) (BatchStats, error) {
	var st BatchStats
	for i := 0; i < len(jobs); {
		if err := ctx.Err(); err != nil {
			return st, err
		}
		var start time.Time // set once the writer is ours, so waiting for it is not counted
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			start = time.Now()
			s := newSyncer(tx, projectID, now)
			defer s.close()
			rows := 0
			for ; i < len(jobs); i++ {
				j := jobs[i]
				var hash string
				err := tx.QueryRow(`SELECT content_hash FROM documents WHERE id = ? AND project_id = ?`, j.DocumentID, projectID).Scan(&hash)
				switch {
				case errors.Is(err, sql.ErrNoRows) || (err == nil && hash != j.Hash):
					st.Skipped++
					continue
				case err != nil:
					return err
				}
				if err := s.sync(j.DocumentID, j.Links, j.Fresh); err != nil {
					return err
				}
				st.Jobs++
				rows += len(j.Links)
				st.Rows += len(j.Links)
				if rows >= ChunkRows || time.Since(start) >= ChunkBudget {
					i++
					break
				}
			}
			return nil
		})
		if err != nil {
			return st, err
		}
		st.Chunks++
		if el := time.Since(start); el > st.Longest {
			st.Longest = el
		}
	}
	return st, nil
}

// Rebuild recomputes the links of every document from its body, for a repair,
// after an import that was cut short, or when the title folding changed.
// project may be empty for all projects. Bodies are read and parsed outside the
// writer, a page at a time, and written with SyncBatch. It returns the number
// of documents read.
func Rebuild(ctx context.Context, d *db.DB, project string) (int, error) {
	const page, pageBytes = 100, 16 << 20
	n := 0
	lastID := ""
	for {
		q := `SELECT id, project_id, content_hash, content FROM documents WHERE id > ?`
		args := []any{lastID}
		if project != "" {
			q += ` AND project_id = ?`
			args = append(args, project)
		}
		q += ` ORDER BY id LIMIT ?`
		args = append(args, page)
		rows, err := d.Read().QueryContext(ctx, q, args...)
		if err != nil {
			return n, err
		}
		jobs := map[string][]Job{}
		var projects []string
		read, bytes := 0, 0
		for rows.Next() {
			var id, pid, hash, content string
			if err := rows.Scan(&id, &pid, &hash, &content); err != nil {
				rows.Close()
				return n, err
			}
			lastID = id
			read++
			if _, ok := jobs[pid]; !ok {
				projects = append(projects, pid)
			}
			jobs[pid] = append(jobs[pid], Job{DocumentID: id, Hash: hash, Links: text.WikiLinks(content)})
			if bytes += len(content); bytes >= pageBytes {
				break
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return n, err
		}
		if read == 0 {
			return n, nil
		}
		for _, pid := range projects {
			if _, err := SyncBatch(ctx, d, pid, "", jobs[pid]); err != nil {
				return n, err
			}
		}
		n += read
	}
}

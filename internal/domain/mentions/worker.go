package mentions

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// IndexVersion says which generation of the mention index a database holds.
// The start-up repair marks every project as owing a scan when it is older
// (the index was never built, or the matching rules changed).
const IndexVersion = "2"

// IndexKey is the settings key that holds the IndexVersion of the last
// complete repair.
const IndexKey = "mentions.index"

// Limits on how long a scan may hold the single writer. A chunk ends when it
// has written ChunkRows statements or has held the writer for ChunkBudget,
// whichever comes first, so a save elsewhere gets in between chunks.
const (
	ChunkRows   = 2_000
	ChunkBudget = 100 * time.Millisecond
	pageDocs    = 200
	pageBytes   = 8 << 20
)

// errMoved says the vocabulary or a document changed under a scan; the scan
// starts again from the stamp it finds.
var errMoved = errors.New("mentions: the vocabulary changed during a scan")

// Stats says what a scan did.
type Stats struct {
	Projects  int           `json:"projects"`
	Documents int           `json:"documents" desc:"Documents read."`
	Written   int           `json:"rows_written" desc:"Index rows inserted, updated or removed."`
	Chunks    int           `json:"chunks" desc:"Write transactions used."`
	Longest   time.Duration `json:"-"`
	LongestMS int64         `json:"longest_hold_ms" desc:"The longest the writer was held by one chunk."`
}

func (s *Stats) add(o Stats) {
	s.Projects += o.Projects
	s.Documents += o.Documents
	s.Written += o.Written
	s.Chunks += o.Chunks
	if o.Longest > s.Longest {
		s.Longest = o.Longest
	}
	s.LongestMS = s.Longest.Milliseconds()
}

type job struct {
	id, hash string
	want     []Row
}

// ScanProject rescans every document of a project against its current
// vocabulary and records the stamp it scanned. The text is read and matched
// outside the writer; only rows that differ are written, in short chunks. If
// the vocabulary changes while it runs it starts again from the new stamp.
//
// A project that is edited all the time can keep moving its stamp under the
// scan. After maxRestarts restarts the last attempt accepts the stamp it began
// with: it records that stamp as scanned and leaves the project owing a scan
// for the newer one, so the work done is kept and the project is not stuck
// "indexing" behind a back-off.
func ScanProject(ctx context.Context, d *db.DB, projectID string) (Stats, error) {
	var st Stats
	for attempt := 0; attempt <= maxRestarts; attempt++ {
		err := scanOnce(ctx, d, projectID, &st, attempt == maxRestarts)
		if err == nil {
			st.Projects = 1
			return st, nil
		}
		if !errors.Is(err, errMoved) {
			return st, err
		}
	}
	return st, errMoved
}

// scanHook, when set by a test, runs once a scan has read its vocabulary and
// before it reads any text: where an edit would land to outrun it.
var scanHook func()

// maxRestarts is how many times a scan starts over when the vocabulary moves.
const maxRestarts = 5

func scanOnce(ctx context.Context, d *db.DB, projectID string, st *Stats, accept bool) error {
	// One read snapshot gives the stamp and the vocabulary together.
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	g, ok, err := engineFor(ctx, tx, projectID)
	tx.Rollback()
	if err != nil {
		if ops.AsError(err).Code == ops.CodeNotFound { // the project is gone
			return nil
		}
		return err
	}
	if !ok {
		// Never stamped: stamp it, and scan against what that finds.
		err := d.WithTx(ctx, func(tx *sql.Tx) error { return MarkDirty(tx, projectID) })
		if err != nil {
			return err
		}
		return errMoved
	}
	if scanHook != nil {
		scanHook()
	}
	var lastID string
	for {
		jobs, next, err := readPage(ctx, d, g, projectID, lastID)
		if err != nil {
			return err
		}
		if len(jobs) == 0 && next == "" {
			break
		}
		st.Documents += len(jobs)
		for i := 0; i < len(jobs); {
			n, wrote, hold, err := writeChunk(ctx, d, projectID, g.stamp, accept, jobs[i:])
			if err != nil {
				return err
			}
			i += n
			st.Chunks++
			st.Written += wrote
			if hold > st.Longest {
				st.Longest = hold
				st.LongestMS = hold.Milliseconds()
			}
		}
		if next == "" {
			break
		}
		lastID = next
	}
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var cur string
		if err := tx.QueryRow(`SELECT stamp FROM mention_state WHERE project_id = ?`, projectID).Scan(&cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if cur != g.stamp && !accept {
			return errMoved
		}
		// indexed_stamp is the stamp scanned, which is the current one unless an
		// accepted scan lost the race (then the project still owes a scan).
		_, err := tx.Exec(`UPDATE mention_state SET indexed_stamp = ?, indexed_at = ? WHERE project_id = ?`, g.stamp, db.Now(), projectID)
		return err
	})
}

// readPage reads the next page of documents after lastID, with their stored
// text and links, and matches them. It returns the jobs and the id to continue
// after ("" when this was the last page).
func readPage(ctx context.Context, d *db.DB, g *engine, projectID, lastID string) ([]job, string, error) {
	empty := g.m.Empty()
	q := `SELECT d.id, d.content_hash, COALESCE(t.body, '') FROM documents d LEFT JOIN doc_text t ON t.document_id = d.id
		WHERE d.project_id = ? AND d.id > ? AND d.kind <> 'research' ORDER BY d.id LIMIT ?`
	if empty {
		q = `SELECT d.id, d.content_hash, '' FROM documents d WHERE d.project_id = ? AND d.id > ? AND d.kind <> 'research' ORDER BY d.id LIMIT ?`
	}
	rows, err := d.Read().QueryContext(ctx, q, projectID, lastID, pageDocs)
	if err != nil {
		return nil, "", err
	}
	type doc struct{ id, hash, body string }
	var docs []doc
	bytes := 0
	more := false
	for rows.Next() {
		var x doc
		if err := rows.Scan(&x.id, &x.hash, &x.body); err != nil {
			rows.Close()
			return nil, "", err
		}
		docs = append(docs, x)
		if bytes += len(x.body); bytes >= pageBytes {
			more = true
			break
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	if len(docs) == 0 {
		return nil, "", nil
	}
	if len(docs) == pageDocs {
		more = true
	}
	byDoc := map[string][]LinkRef{}
	if !empty {
		lr, err := d.Read().QueryContext(ctx, `SELECT from_id, link_text, snippet, occurrences FROM document_links
			WHERE kind = 'wiki' AND from_id IN (SELECT id FROM documents WHERE project_id = ? AND id > ? AND id <= ?)`, projectID, lastID, docs[len(docs)-1].id)
		if err != nil {
			return nil, "", err
		}
		for lr.Next() {
			var from string
			var l LinkRef
			if err := lr.Scan(&from, &l.Target, &l.Snippet, &l.Occurrence); err != nil {
				lr.Close()
				return nil, "", err
			}
			byDoc[from] = append(byDoc[from], l)
		}
		lr.Close()
		if err := lr.Err(); err != nil {
			return nil, "", err
		}
	}
	jobs := make([]job, len(docs))
	for i, x := range docs {
		jobs[i] = job{id: x.id, hash: x.hash, want: g.scan(x.body, byDoc[x.id])}
	}
	next := ""
	if more {
		next = docs[len(docs)-1].id
	}
	return jobs, next, nil
}

// writeChunk stores as many jobs as fit in one short writer transaction. It
// returns how many it took, the statements it ran and how long it held the
// writer. A document that is gone or whose text changed since it was read is
// skipped: its own save wrote its rows. If the stamp moved, the scan is stale.
func writeChunk(ctx context.Context, d *db.DB, projectID, stamp string, accept bool, jobs []job) (taken, wrote int, hold time.Duration, err error) {
	var start time.Time
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		start = time.Now()
		taken, wrote = 0, 0
		var cur string
		if err := tx.QueryRow(`SELECT stamp FROM mention_state WHERE project_id = ?`, projectID).Scan(&cur); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				taken = len(jobs) // the project is gone
				return nil
			}
			return err
		}
		if cur != stamp && !accept {
			return errMoved
		}
		for taken < len(jobs) {
			j := jobs[taken]
			taken++
			var hash string
			switch err := tx.QueryRow(`SELECT content_hash FROM documents WHERE id = ? AND project_id = ?`, j.id, projectID).Scan(&hash); {
			case errors.Is(err, sql.ErrNoRows) || (err == nil && hash != j.hash):
				continue
			case err != nil:
				return err
			}
			have, err := loadRows(ctx, tx, j.id)
			if err != nil {
				return err
			}
			if len(have) == 0 && len(j.want) == 0 {
				continue
			}
			n, err := apply(tx, projectID, j.id, have, j.want)
			if err != nil {
				return err
			}
			wrote += n
			if wrote >= ChunkRows || time.Since(start) >= ChunkBudget {
				break
			}
		}
		return nil
	})
	if err == nil {
		hold = time.Since(start)
	}
	return taken, wrote, hold, err
}

// Owed lists the projects whose index is behind their vocabulary.
func Owed(ctx context.Context, q querier, limit int) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT project_id FROM mention_state WHERE stamp <> indexed_stamp ORDER BY project_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Settle scans every project that owes a scan until none does. The worker
// runs the same loop; tests and the command line call it to wait for the
// index.
func Settle(ctx context.Context, d *db.DB) (Stats, error) {
	var total Stats
	for round := 0; round < 1000; round++ {
		ids, err := Owed(ctx, d.Read(), 50)
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}
		for _, id := range ids {
			st, err := ScanProject(ctx, d, id)
			total.add(st)
			if err != nil && !errors.Is(err, errMoved) {
				return total, err
			}
		}
	}
	return total, errMoved
}

// MarkAllDirty makes every live project owe a scan.
func MarkAllDirty(ctx context.Context, d *db.DB, projectID string) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		q := `INSERT INTO mention_state(project_id, stamp) SELECT id, lower(hex(randomblob(8))) FROM projects WHERE deleted_at IS NULL`
		var args []any
		if projectID != "" {
			q += ` AND id = ?`
			args = append(args, projectID)
		}
		_, err := tx.Exec(q+` ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp`, args...)
		return err
	})
}

// Rebuild marks the project (or every project, when empty) as owing a scan
// and scans it now. The index is derived, so this is always safe.
func Rebuild(ctx context.Context, d *db.DB, projectID string) (Stats, error) {
	if err := MarkAllDirty(ctx, d, projectID); err != nil {
		return Stats{}, err
	}
	return Settle(ctx, d)
}

// PollInterval is how often the worker looks for a project that owes a scan.
var PollInterval = 700 * time.Millisecond

// FailureBackoff is how long the worker leaves a project alone after a failed scan.
var FailureBackoff = 30 * time.Second

// Run is the background worker. It repairs the index once after an upgrade
// (every project owes a scan when IndexKey is older than IndexVersion), then
// scans whatever the vocabulary changes leave owing, one project at a time,
// until ctx ends. Errors are logged and retried at the next poll.
func Run(ctx context.Context, d *db.DB, log *slog.Logger) {
	var done string
	_ = d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, IndexKey).Scan(&done)
	repair := done != IndexVersion
	if repair {
		if err := MarkAllDirty(ctx, d, ""); err != nil && ctx.Err() == nil {
			log.Warn("mentions: could not mark projects for the first scan", "err", err)
		}
	}
	tick := time.NewTicker(PollInterval)
	defer tick.Stop()
	// A project whose scan fails is left alone for FailureBackoff, so one bad
	// project cannot keep the worker from the others or spin on itself.
	failedUntil := map[string]time.Time{}
	for {
		ids, err := Owed(ctx, d.Read(), 50)
		next := ""
		for _, id := range ids {
			if t, bad := failedUntil[id]; !bad || time.Now().After(t) {
				next = id
				break
			}
		}
		switch {
		case err != nil && ctx.Err() == nil:
			log.Warn("mentions: could not look for owed scans", "err", err)
		case next != "":
			st, err := ScanProject(ctx, d, next)
			if err == nil {
				delete(failedUntil, next)
				if st.Documents > 0 {
					log.Debug("mentions: scanned", "project", next, "documents", st.Documents, "rows", st.Written, "chunks", st.Chunks)
				}
				continue // look again at once: more may be owed
			}
			if ctx.Err() == nil {
				if errors.Is(err, errMoved) {
					// Still being edited under the scan: try again at the next poll, after the other projects.
					failedUntil[next] = time.Now().Add(PollInterval)
				} else {
					log.Warn("mentions: scan failed, will retry", "project", next, "err", err)
					failedUntil[next] = time.Now().Add(FailureBackoff)
				}
			}
		case repair:
			err := d.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
					IndexKey, IndexVersion, db.Now())
				return err
			})
			if err == nil {
				repair = false
				log.Info("mentions: index built for existing documents")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

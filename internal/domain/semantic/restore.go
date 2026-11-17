package semantic

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

// MarkerName is the file a restore writes in the data directory, before anything can fail, to say that every index
// must be switched off. The restore's own step removes it once it has paused them; if the restore stops half way,
// or that step fails, the file stays, the server pauses the indexes when it starts, and until it has, nothing is
// sent: the marker is the latch.
const MarkerName = "semantic.restore-pause"

func init() {
	// A backup carries the index with the rest of the database. Restored on a second machine, an index that
	// kept running would send the manuscript to an endpoint from a server that is not (or not yet) the one in
	// charge, from the first minute. So every index comes back switched off with the reason "restore", and the
	// owner switches it on again from the project's search settings, which shows the preview again. What the
	// restore keeps of the embeddings is only what still fits the text: a document whose hash differs from the
	// hash it was embedded under, and every embedding made under older rules, is deleted, and the index is
	// rebuilt when it is switched on.
	//
	// It is fail-safe as the mail pause is: a marker file is written right after the files are installed, before
	// the database is opened; the step retries once; and if it still cannot pause the indexes the restore fails
	// loudly ("NOT paused"), while the marker makes every start pause them and blocks all sending until it has.
	backup.PostInstall = append(backup.PostInstall, func(dataDir string) error {
		return os.WriteFile(filepath.Join(dataDir, MarkerName), []byte("semantic indexes are to be paused: a restore was made here\n"), 0o600)
	})
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		paused, kept, dropped, err := pauseWithRetry(ctx, d)
		if err != nil {
			return "", fmt.Errorf("the semantic indexes are NOT paused: %w. They are paused when the server next starts, and nothing is sent to an embedding endpoint until they are (a marker file, %s, is in the data directory); to pause them by hand now: sqlite3 scriobh.db \"UPDATE embed_projects SET enabled = 0, paused_reason = 'restore';\"", err, MarkerName)
		}
		_ = os.Remove(filepath.Join(filepath.Dir(d.Path), MarkerName))
		if paused == 0 {
			return "", nil
		}
		return fmt.Sprintf("Semantic search: %d project(s) had an index; each is switched off, so this copy sends no text to an embedding endpoint until its owner switches it on again in the project's search settings. %d document(s) kept their embeddings, %d were dropped because their text differs from the backup's (they are embedded again when it is switched on).", paused, kept, dropped), nil
	})
}

// pauseOverride, when set by a test, replaces PauseAll to make the pause fail. It is read by the worker on every
// tick while a test may set it, so it is atomic.
var pauseOverride atomic.Pointer[func(context.Context, *db.DB) (int, int, int, error)]

func pauseFn(ctx context.Context, d *db.DB) (int, int, int, error) {
	if f := pauseOverride.Load(); f != nil {
		return (*f)(ctx, d)
	}
	return PauseAll(ctx, d)
}

func pauseWithRetry(ctx context.Context, d *db.DB) (paused, kept, dropped int, err error) {
	paused, kept, dropped, err = pauseFn(ctx, d)
	if err != nil {
		paused, kept, dropped, err = pauseFn(ctx, d)
	}
	return
}

// restorePending says a restore's marker is still waiting to pause the indexes. While it is, nothing is sent: the
// worker tries the pause on every tick and the searches refuse.
func (s *Service) restorePending() bool {
	if s.env == nil || s.env.DataDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(s.env.DataDir, MarkerName))
	return err == nil
}

// honourMarker pauses every index if a restore left its marker, and removes the marker only once they are paused.
func (s *Service) honourMarker(ctx context.Context) {
	if !s.restorePending() {
		return
	}
	if _, _, _, err := pauseFn(ctx, s.db()); err != nil {
		s.log.Error("semantic: a restore left its marker and the indexes could not be paused; nothing will be sent until they are", "err", err)
		return
	}
	_ = os.Remove(filepath.Join(s.env.DataDir, MarkerName))
	s.log.Warn("semantic: every index was paused at start because a restore did not finish its own pause; each owner switches theirs on again in the search settings")
}

// PauseAll switches every index off with the reason "restore", checks the
// embeddings against the text and deletes those that do not match. It reports
// how many indexes it paused, and how many documents kept their embeddings and
// how many lost them.
func PauseAll(ctx context.Context, d *db.DB) (paused, kept, dropped int, err error) {
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE embed_projects SET enabled = 0, paused_reason = 'restore', gen = gen + 1, updated_at = ?`, db.Now())
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		paused = int(n)
		// Embeddings made under other rules, or for a document whose text is not the text they were made from.
		bad := `SELECT e.document_id FROM embed_docs e LEFT JOIN documents d ON d.id = e.document_id
			WHERE d.id IS NULL OR d.content_hash <> e.content_hash OR e.index_version <> ?`
		if _, err := tx.Exec(`DELETE FROM embed_chunks WHERE document_id IN (`+bad+`)`, IndexVersion); err != nil {
			return err
		}
		r2, err := tx.Exec(`DELETE FROM embed_docs WHERE document_id IN (`+bad+`)`, IndexVersion)
		if err != nil {
			return err
		}
		dn, _ := r2.RowsAffected()
		dropped = int(dn)
		var k int
		if err := tx.QueryRow(`SELECT count(*) FROM embed_docs`).Scan(&k); err != nil {
			return err
		}
		kept = k
		// A chunk with no document row is not a record of anything.
		_, err = tx.Exec(`DELETE FROM embed_chunks WHERE document_id NOT IN (SELECT document_id FROM embed_docs)`)
		return err
	})
	return paused, kept, dropped, err
}

func init() {
	backup.Pauses = append(backup.Pauses, backup.Pause{Name: "The semantic indexes", SQL: `sqlite3 scriobh.db "UPDATE embed_projects SET enabled = 0, paused_reason = 'restore';"`})
}

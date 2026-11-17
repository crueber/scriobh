package events

import (
	"context"
	"database/sql"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Keep is how long event rows are kept (docs/ARCHITECTURE.md: a 90-day event
// log). Every reader follows the table by sequence number from a recent point
// (live streams replay hours, not months), so older rows serve nothing.
const Keep = 90 * 24 * time.Hour

// sweepChunk bounds one delete, so the writer is held for a moment at a time.
const sweepChunk = 2000

// Sweep removes event rows older than keep, a chunk per short transaction, and
// returns how many it removed. It never removes the newest row (readers take
// max(seq) as "now", and seq must never seem to go backwards), nor, while a
// webhook is on, a row the webhook feeder has not read yet. With no webhook on
// the feeder does not move, and switching one on starts it at the newest event.
// With the webhook feature off the feeder is parked (a negative cursor, set at
// start) and holds nothing back, whether or not a webhook row is enabled.
func Sweep(ctx context.Context, d *db.DB, keep time.Duration, now time.Time) (int64, error) {
	// Rows are written in time order, so the first row inside the window bounds
	// everything older; finding it reads only the old rows, outside the writer.
	var bound sql.NullInt64
	err := d.Read().QueryRowContext(ctx, `SELECT seq FROM events WHERE created_at >= ? ORDER BY seq LIMIT 1`, db.Format(now.Add(-keep))).Scan(&bound)
	switch {
	case err == sql.ErrNoRows:
		// Nothing is inside the window: everything but the newest row may go.
		if err := d.Read().QueryRowContext(ctx, `SELECT max(seq) FROM events`).Scan(&bound); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	}
	if !bound.Valid {
		return 0, nil
	}
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		var n int64
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE seq IN (
				SELECT seq FROM events
				WHERE seq < ?
				  AND (NOT EXISTS (SELECT 1 FROM webhooks WHERE enabled = 1)
				       OR COALESCE((SELECT cursor FROM webhook_state WHERE id = 1), 0) < 0
				       OR seq <= COALESCE((SELECT cursor FROM webhook_state WHERE id = 1), 0))
				ORDER BY seq LIMIT ?)`, bound.Int64, sweepChunk)
			if err != nil {
				return err
			}
			n, err = res.RowsAffected()
			return err
		})
		total += n
		if err != nil || n < sweepChunk {
			return total, err
		}
	}
}

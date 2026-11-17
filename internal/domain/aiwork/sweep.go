package aiwork

import (
	"context"
	"database/sql"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Retention of what the recipes keep, beyond what a person deletes.
const (
	ledgerDays         = 30  // the accept ledger: it only has to outlast the held suggestion
	settledJobDays     = 30  // a finished or cancelled rolling job
	openJobDays        = 60  // an abandoned one
	settledMarkKeepDay = 90  // accepted or dismissed Lab marks
	settledReviewDays  = 180 // a review none of whose notes is open
)

// Sweep removes what has outlived its use: expired held suggestions, the accept
// ledger past its days, finished and abandoned rolling jobs, settled Lab marks,
// and reviews with nothing left to work through. The server calls it at start
// and every hour, so a quiet server does not keep an expired suggestion until
// the next AI request. It returns how many rows went.
func Sweep(ctx context.Context, d *db.DB) (int64, error) {
	ago := func(days int) string { return db.Format(db.NowTime().Add(-time.Duration(days) * 24 * time.Hour)) }
	var total int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, st := range []struct {
			q    string
			args []any
		}{
			{`DELETE FROM ai_pending WHERE expires_at <= ?`, []any{db.Now()}},
			{`DELETE FROM ai_accepts WHERE created_at < ?`, []any{ago(ledgerDays)}},
			{`DELETE FROM ai_jobs WHERE status <> 'open' AND updated_at < ?`, []any{ago(settledJobDays)}},
			{`DELETE FROM ai_jobs WHERE status = 'open' AND updated_at < ?`, []any{ago(openJobDays)}},
			{`DELETE FROM ai_marks WHERE status <> 'open' AND resolved_at <> '' AND resolved_at < ?`, []any{ago(settledMarkKeepDay)}},
			{`DELETE FROM ai_reviews WHERE created_at < ? AND NOT EXISTS (SELECT 1 FROM ai_notes n WHERE n.review_id = ai_reviews.id AND n.status = 'open')`, []any{ago(settledReviewDays)}},
		} {
			res, err := tx.ExecContext(ctx, st.q, st.args...)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			total += n
		}
		return nil
	})
	return total, err
}

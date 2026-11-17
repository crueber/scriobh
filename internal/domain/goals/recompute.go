package goals

import (
	"context"
	"database/sql"
	"errors"

	"github.com/crueber/scriobh/internal/db"
)

// Recompute rebuilds daily_stats from writing_sessions, which are the record.
// userID and projectID narrow the scope; empty means all. It is the repair for
// a daily total that has drifted, and the way to rebuild after a restore from a
// partial backup.
//
// Each session carries the local day it was written on, so the rebuild is a
// plain sum per (person, project, day) and never re-reads history in the
// person's current time zone. A day's goal_target is kept as stored, and a day
// that was hit stays hit; a day with no row yet takes the person's current
// daily goal if it had begun by then. Rows with no sessions behind them are
// left alone.
func Recompute(ctx context.Context, d *db.DB, userID, projectID string) (RecomputeResult, error) {
	var res RecomputeResult
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		type group struct {
			user, project, day              string
			added, net, seconds, sessionCnt int
		}
		rows, err := tx.QueryContext(ctx, `SELECT user_id, project_id, day, SUM(words_added), SUM(words_net), SUM(active_seconds), COUNT(*)
			FROM writing_sessions WHERE (?1 = '' OR user_id = ?1) AND (?2 = '' OR project_id = ?2)
			GROUP BY user_id, project_id, day`, userID, projectID)
		if err != nil {
			return err
		}
		var groups []group
		for rows.Next() {
			var g group
			if err := rows.Scan(&g.user, &g.project, &g.day, &g.added, &g.net, &g.seconds, &g.sessionCnt); err != nil {
				rows.Close()
				return err
			}
			groups = append(groups, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := db.Now()
		for _, g := range groups {
			var target, hit int
			err := tx.QueryRowContext(ctx, `SELECT goal_target, goal_hit FROM daily_stats WHERE user_id = ? AND project_id = ? AND day = ?`,
				g.user, g.project, g.day).Scan(&target, &hit)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				var start string
				e := tx.QueryRowContext(ctx, `SELECT target_words, start_date FROM goals WHERE user_id = ? AND project_id = ? AND kind = 'daily'
					AND (end_date = '' OR end_date >= ?)`, g.user, g.project, g.day).Scan(&target, &start)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if errors.Is(e, sql.ErrNoRows) || (start != "" && g.day < start) {
					target = 0
				}
			case err != nil:
				return err
			}
			if hit == 0 {
				hit = hitFor(target, g.added)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO daily_stats(user_id, project_id, day, words_added, words_net, minutes, sessions, active_seconds, goal_target, goal_hit, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(user_id, project_id, day) DO UPDATE SET words_added = excluded.words_added, words_net = excluded.words_net,
				  minutes = excluded.minutes, sessions = excluded.sessions, active_seconds = excluded.active_seconds,
				  goal_target = excluded.goal_target, goal_hit = excluded.goal_hit, updated_at = excluded.updated_at`,
				g.user, g.project, g.day, g.added, g.net, (g.seconds+59)/60, g.sessionCnt, g.seconds, target, hit, now); err != nil {
				return err
			}
			res.Days++
		}
		return nil
	})
	return res, err
}

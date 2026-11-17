package timeline

import (
	"context"
	"database/sql"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/entities"
)

// A merge deletes the source entity. What the timeline holds of it moves to
// the target first: the events it took part in, and the events that happened
// at it. Where the target already took part in the same event, the target's
// row stays (and keeps its role unless it had none) and the source's goes.
func init() {
	entities.RegisterMergeStep(entities.MergeStep{Name: "timeline", Count: mergeCount, Apply: mergeApply})
}

func mergeCount(ctx context.Context, q entities.Querier, sourceID, targetID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM timeline_event_people WHERE entity_id = ?1)
		+ (SELECT count(*) FROM timeline_events WHERE location_entity_id = ?1)`, sourceID).Scan(&n)
	return n, err
}

func mergeApply(tx *sql.Tx, sourceID, targetID string) error {
	rows, err := tx.Query(`SELECT event_id FROM timeline_event_people WHERE entity_id = ?1 UNION SELECT id FROM timeline_events WHERE location_entity_id = ?1`, sourceID)
	if err != nil {
		return err
	}
	var touched []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		touched = append(touched, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(touched) == 0 {
		return nil
	}
	// A role the target lacks is taken from the source's row where both took part.
	if _, err := tx.Exec(`UPDATE timeline_event_people SET role = (SELECT s.role FROM timeline_event_people s WHERE s.event_id = timeline_event_people.event_id AND s.entity_id = ?1)
		WHERE entity_id = ?2 AND role = '' AND EXISTS (SELECT 1 FROM timeline_event_people s WHERE s.event_id = timeline_event_people.event_id AND s.entity_id = ?1 AND s.role <> '')`, sourceID, targetID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE OR IGNORE timeline_event_people SET entity_id = ? WHERE entity_id = ?`, targetID, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM timeline_event_people WHERE entity_id = ?`, sourceID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE timeline_events SET location_entity_id = ? WHERE location_entity_id = ?`, targetID, sourceID); err != nil {
		return err
	}
	now := db.Now()
	for _, id := range touched {
		if _, err := tx.Exec(`UPDATE timeline_events SET revision = revision + 1, updated_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
	}
	return nil
}

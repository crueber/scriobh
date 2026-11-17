package webhook

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/secrets"
)

func init() {
	// After a restore, say which webhooks the installation's key cannot sign for
	// or address. Nothing is changed here: the first delivery switches such a
	// webhook off with a reason (see attempt), and putting the original secret
	// key back before starting keeps them working.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		n, err := UnreadableSecrets(ctx, d, secrets.New(key))
		if err != nil || n == 0 {
			return "", err
		}
		if len(key) == 0 {
			return fmt.Sprintf("Webhooks: no secret key was found, so %d webhook(s) could not be checked. A new key is created at the next start, which cannot read their signing secrets; put the original secret.key back before starting to keep them.", n), nil
		}
		return fmt.Sprintf("Webhooks: %d webhook(s) have a signing secret or address that cannot be read with this secret key. Each is paused by this restore (see below) and, if switched on again, switched off at its next delivery; rotate its secret (and set its address again), then switch it on, to use it.", n), nil
	})
}

// PausedByRestore is the reason shown on a webhook that a restore switched off.
const PausedByRestore = "Paused when this copy was restored from a backup, so it does not send the old copy's notifications again. Turn it on again here when this is the server that should send them."

func init() {
	// A backup carries the outbox and the feeder's place in the event log. Restored on a second machine
	// with the same secret key, enabled webhooks would send the waiting deliveries and the events after
	// the cursor to real receivers, from a server that is not (or not yet) the one in charge. So every
	// webhook comes back switched off, the outbox is emptied and the feeder is parked; the owner or an
	// administrator switches each one on from the project's Webhooks page, which starts it at the
	// newest event.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		n, err := PauseAll(ctx, d)
		if err != nil || n == 0 {
			return "", err
		}
		return fmt.Sprintf("Webhooks: %d webhook(s) were paused and their waiting deliveries removed, so this copy sends nothing until each is turned on again from its project's Webhooks page (owner or administrator).", n), nil
	})
}

// PauseAll switches every enabled webhook off with PausedByRestore as the reason, removes the waiting
// deliveries and parks the feeder (see CursorParked). It says how many webhooks it paused.
func PauseAll(ctx context.Context, d *db.DB) (int, error) {
	var n int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE webhooks SET enabled = 0, disabled_reason = ?, updated_at = ? WHERE enabled = 1`, PausedByRestore, db.Now())
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(n), ParkState(ctx, d)
}

// UnreadableSecrets counts the webhooks whose signing secret or sealed address
// does not open under box.
func UnreadableSecrets(ctx context.Context, d *db.DB, box *secrets.Box) (int, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT id, secret_sealed, url_path_sealed FROM webhooks`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, secret, path string
		if err := rows.Scan(&id, &secret, &path); err != nil {
			return 0, err
		}
		if !box.CanOpen(secret, "webhooks/"+id+"/secret") || (path != "" && !box.CanOpen(path, "webhooks/"+id+"/path")) {
			n++
		}
	}
	return n, rows.Err()
}

func init() {
	backup.Pauses = append(backup.Pauses, backup.Pause{Name: "Webhooks", SQL: `sqlite3 scriobh.db "UPDATE webhooks SET enabled = 0; DELETE FROM webhook_deliveries;"`})
}

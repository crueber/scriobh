package pandoc

import (
	"context"
	"fmt"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

func init() {
	// A backup carries the Pandoc settings with the rest of the database. Restored on another machine, a bridge that
	// stayed switched on would run whatever file is at the stored path there, a program that machine's administrator
	// did not choose. It is revalidated on every use, but it is switched off all the same, as the semantic indexes,
	// the Git remotes, the triggers and the webhooks are: an administrator switches it on again from the Pandoc
	// settings, which checks the program afresh. The path stays, so that doing so is one click.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		var on string
		err := d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, settingEnabled).Scan(&on)
		if err != nil || on != "1" {
			return "", nil
		}
		if _, err := d.Writer().ExecContext(ctx, `UPDATE settings SET value = '0', updated_at = ? WHERE key = ?`, db.Now(), settingEnabled); err != nil {
			return "", fmt.Errorf("the Pandoc bridge is NOT switched off: %w. To switch it off by hand: sqlite3 scriobh.db \"UPDATE settings SET value = '0' WHERE key = 'pandoc.enabled';\"", err)
		}
		// The mark the settings page reads to say a restore did this (and not an administrator): only switching on clears it.
		if _, err := d.Writer().ExecContext(ctx, `INSERT INTO settings(key, value, updated_at) VALUES (?, '1', ?) ON CONFLICT(key) DO UPDATE SET value = '1', updated_at = excluded.updated_at`, settingRestorePaused, db.Now()); err != nil {
			return "", fmt.Errorf("the bridge is switched off, but the restore mark could not be written: %w", err)
		}
		return "Pandoc: the bridge was switched on in the backup; it is switched off here, so that this machine runs no program an administrator has not checked on it. Switch it on again in the Pandoc settings.", nil
	})
}

func init() {
	backup.Pauses = append(backup.Pauses, backup.Pause{Name: "The Pandoc bridge", SQL: `sqlite3 scriobh.db "UPDATE settings SET value = '0' WHERE key = 'pandoc.enabled';"`})
}

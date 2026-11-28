package gitmirror

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/secrets"
)

// PausedRemoteByRestore is the reason shown on a remote that a restore switched off.
const PausedRemoteByRestore = "Paused when this copy was restored from a backup, so it does not push the old copy's text to the repository again (a public mirror included). Switch it on again here when this is the server that should push."

func init() {
	// A backup carries the remotes, their triggers and the state of what was last pushed. Restored on a second
	// machine that holds the same secret key, an enabled remote would push the old copy's text to a real
	// repository (a public mirror, a release's repository) and a trigger would call real receivers, from a
	// server that is not (or not yet) the one in charge. So every remote comes back switched off with a reason,
	// every trigger too, and the deliveries waiting are removed; the owner switches each on again from the
	// project's Git settings, as webhooks are. A restore on the same machine after a loss asks the same of the
	// owner: it is one click per remote.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		// Both pauses are tried whatever the other did, and this step is registered before the credential check.
		n, err1 := PauseRemotes(ctx, d)
		tn, err2 := PauseTriggers(ctx, d)
		if err := errors.Join(err1, err2); err != nil {
			return "", err
		}
		if n == 0 && tn == 0 {
			return "", nil
		}
		return fmt.Sprintf("Git: %d remote(s) and %d trigger(s) were paused, so this copy pushes nothing and calls nothing until each is switched on again in its project's Git settings (owner or administrator).", n, tn), nil
	})
}

// PauseRemotes switches every enabled remote off with PausedRemoteByRestore as the reason. A remote already off
// for another reason (unreadable credentials) keeps that reason. It says how many it paused.
func PauseRemotes(ctx context.Context, d *db.DB) (int, error) {
	var n int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE git_remotes SET enabled = 0, state = 'blocked', last_error = ?, last_error_code = ?, next_attempt_at = NULL, restore_paused = 1, updated_at = ? WHERE enabled = 1`,
			PausedRemoteByRestore, CodePausedByRestore, db.Now())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		// A remote that is off for another reason is paused too: switching it on later (credentials entered)
		// must not be what lets a restored copy push.
		_, err = tx.Exec(`UPDATE git_remotes SET restore_paused = 1 WHERE restore_paused = 0`)
		return err
	})
	return int(n), err
}

func init() {
	// After a restore, remote credentials the installation's key cannot read are
	// cleared, with a message. A restore never creates or copies a key.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		if len(key) == 0 {
			return "Git remotes: no secret key was found, so stored credentials could not be checked. A new key is created at the next start, which cannot read credentials from the backup; they are then switched off and must be entered again (or put the original secret.key back before starting).", nil
		}
		return CheckCredentials(ctx, d, secrets.New(key), true)
	})
}

// sealMarker is the setting that records that rows sealed under the old
// context have been sealed again.
const sealMarker = "git.seal_context.v2"

// credentialsUnreadable is the message stored on a remote whose credentials
// cannot be read.
const credentialsUnreadable = "The stored credentials cannot be read with this installation's secret key (the data was restored without its key, or the key changed). Enter them again: generate a new deploy key and add it to the repository, or paste the token again."

// CheckCredentials compares every remote's stored credential with the key.
//
// A credential that does not open is a problem a person must fix, so the remote
// is switched off (enabled = 0, state blocked) with last_error saying so. With
// clear set (right after a restore, when the key is known to be the one in use)
// the unreadable ciphertext and the deploy key's public half are removed, so
// nothing stale is shown as if it worked. Without it (at server start) they are
// kept: putting the original key back and restarting brings the remote back,
// which is also checked here: a remote that was switched off for this reason
// and now opens is switched on again.
//
// It returns a message for the person running the restore or for the log; empty
// when there was nothing to say.
func CheckCredentials(ctx context.Context, d *db.DB, box *secrets.Box, clear bool) (string, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT id, url, secret_cipher, enabled, last_error_code, restore_paused FROM git_remotes WHERE secret_cipher <> '' OR last_error_code = ?`, CodeCredentialsBad)
	if err != nil {
		return "", err
	}
	type item struct {
		id, url, cipher, code string
		enabled, paused       bool
	}
	var items []item
	for rows.Next() {
		var it item
		var en, pz int
		if err := rows.Scan(&it.id, &it.url, &it.cipher, &en, &it.code, &pz); err != nil {
			rows.Close()
			return "", err
		}
		it.enabled, it.paused = en != 0, pz != 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	// Rows sealed before the address was part of the context are sealed again,
	// once: the first check that runs with a key marks the database, and from
	// then on a row that only opens under the old context is unreadable. A
	// ciphertext put back later does not get a second chance.
	allowLegacy := false
	if box.Enabled() {
		var marker string
		_ = d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, sealMarker).Scan(&marker)
		allowLegacy = marker != "1"
	}
	var bad, back int
	type dbUpdate struct {
		query string
		args  []any
	}
	var updates []dbUpdate
	now := db.Now()
	for _, it := range items {
		readable := false
		if it.cipher != "" {
			if ep, e := parseURL(it.url, true); e == nil {
				cx := sealContext(it.id, ep)
				if box.CanOpen(it.cipher, cx) {
					readable = true
				} else if plain, oerr := box.Open(it.cipher, legacySealContext(it.id)); allowLegacy && oerr == nil {
					// Sealed before the address was part of the context: sealed again
					// here, once, for the address the row has now. Never at use time.
					sealed, serr := box.Seal(plain, cx)
					for i := range plain {
						plain[i] = 0
					}
					if serr == nil {
						updates = append(updates, dbUpdate{
							query: `UPDATE git_remotes SET secret_cipher = ? WHERE id = ?`,
							args:  []any{sealed, it.id},
						})
						readable = true
					}
				}
			}
		}
		switch {
		case it.paused && !(clear && !readable && it.cipher != ""):
			// A restore's pause is never touched by this check: not marked, not cleared, and above all not
			// switched on (a start-up check, a credential that became readable again). The one exception is the
			// check right after a restore with a key that cannot read the credential: it is removed, and the
			// remote says so; it stays off and stays paused (restore_paused), whatever it says.
		case !readable && it.cipher != "":
			bad++
			q := `UPDATE git_remotes SET enabled = 0, state = 'blocked', last_error = ?, last_error_code = ?, next_attempt_at = NULL, updated_at = ? WHERE id = ?`
			if clear {
				q = `UPDATE git_remotes SET secret_cipher = '', public_key = '', enabled = 0, state = 'blocked', last_error = ?, last_error_code = ?, next_attempt_at = NULL, updated_at = ? WHERE id = ?`
			}
			updates = append(updates, dbUpdate{
				query: q,
				args:  []any{credentialsUnreadable, CodeCredentialsBad, now, it.id},
			})
		case readable && it.code == CodeCredentialsBad:
			back++
			updates = append(updates, dbUpdate{
				query: `UPDATE git_remotes SET enabled = 1, state = CASE WHEN last_commit_sha <> '' THEN 'synced' ELSE 'new' END,
				last_error = '', last_error_code = '', fail_count = 0, updated_at = ? WHERE id = ?`,
				args: []any{now, it.id},
			})
		}
	}
	if box.Enabled() && allowLegacy {
		updates = append(updates, dbUpdate{
			query: `INSERT INTO settings(key, value, updated_at) VALUES (?, '1', ?) ON CONFLICT(key) DO UPDATE SET value = '1', updated_at = excluded.updated_at`,
			args:  []any{sealMarker, now},
		})
	}
	if len(updates) > 0 {
		if err := d.WithTx(ctx, func(tx *sql.Tx) error {
			for _, u := range updates {
				if _, err := tx.ExecContext(ctx, u.query, u.args...); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return "", err
		}
	}
	var parts []string
	if bad > 0 {
		verb := "were switched off"
		if clear {
			verb = "were cleared and switched off"
		}
		parts = append(parts, fmt.Sprintf("Git remotes: the credentials of %d remote(s) cannot be read with this secret key and %s. Enter them again in the project's Git settings.", bad, verb))
	}
	if back > 0 {
		parts = append(parts, fmt.Sprintf("Git remotes: %d remote(s) switched off for unreadable credentials can be read again and were switched on.", back))
	}
	return strings.Join(parts, " "), nil
}

func init() {
	backup.Pauses = append(backup.Pauses, backup.Pause{Name: "Git remotes", SQL: `sqlite3 scriobh.db "UPDATE git_remotes SET enabled = 0, restore_paused = 1;"`})
	backup.Pauses = append(backup.Pauses, backup.Pause{Name: "Git triggers", SQL: `sqlite3 scriobh.db "UPDATE git_triggers SET enabled = 0; DELETE FROM git_trigger_deliveries WHERE state = 'pending';"`})
}

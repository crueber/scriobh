package mail

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/secrets"
)

// MarkerName is the file a restore writes in the data directory, before anything can fail, to say that mail must be
// switched off at the next start. The restore's own step removes it once it has paused mail; if the restore stops
// half way, or that step fails, the file stays and the server pauses mail when it starts.
const MarkerName = "mail.restore-pause"

func init() {
	// A backup carries the outbox, the digests' places and the relay's settings. Restored on a second machine
	// with the same secret key, a working relay would send the waiting messages, and the next digests, from a
	// server that is not (or not yet) the one in charge. So mail comes back switched off, with a reason, the
	// outbox is emptied, and only an administrator switching mail on again lifts it. The pause is its own mark
	// (restore_paused): a changed setting, a test or a new password leave it in place.
	//
	// It is fail-safe in three ways. A marker file is written right after the files are installed, before the
	// database is opened. The step retries once. And if it still cannot pause mail, the restore fails loudly and says
	// so, while the marker makes the next start pause it.
	backup.PostInstall = append(backup.PostInstall, func(dataDir string) error {
		return os.WriteFile(filepath.Join(dataDir, MarkerName), []byte("mail is to be paused: a restore was made here\n"), 0o600)
	})
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		n, err := pauseWithRetry(ctx, d)
		if err != nil {
			return "", fmt.Errorf("mail is NOT paused: %w. Mail is paused when the server next starts (a marker file, %s, is in the data directory); to pause it by hand now: sqlite3 scriobh.db \"UPDATE mail_config SET enabled = 0, restore_paused = 1; DELETE FROM mail_outbox WHERE state IN ('pending','sending');\"", err, MarkerName)
		}
		_ = os.Remove(filepath.Join(filepath.Dir(d.Path), MarkerName))
		if n == 0 {
			return "", nil
		}
		msg := "Mail: switched off, and the waiting messages removed, so this copy sends nothing until an administrator switches mail on again in the mail settings."
		if cfg, err := loadConfig(ctx, d.Read()); err == nil && cfg.PasswordSealed != "" && !secrets.New(key).CanOpen(cfg.PasswordSealed, sealContext(cfg.Host, cfg.Port, cfg.Username)) {
			msg += " The relay's password cannot be read with this secret key; enter it again."
		}
		return msg, nil
	})
}

func init() {
	// A person the identity provider removed from every group can no longer sign in to turn their digest off, and
	// their queued mail is for a person who has no access any more: both stop when their sessions do.
	auth.LostAccess = append(auth.LostAccess, func(tx *sql.Tx, userID string) error {
		if _, err := tx.Exec(`DELETE FROM mail_digest WHERE user_id = ?`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM mail_progress WHERE user_id = ?`, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM kindle_previews WHERE user_id = ?`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE mail_outbox SET state = 'dead', last_error = 'cancelled', finished_at = ?, recipient = '', subject = '', body = '', file_name = '', claimed_at = ''
			WHERE person_id = ? AND state = 'pending'`, db.Now(), userID)
		return err
	})
}

// pauseFn is PauseAll; a test replaces it to make the pause fail.
var pauseFn = PauseAll

func pauseWithRetry(ctx context.Context, d *db.DB) (int, error) {
	n, err := pauseFn(ctx, d)
	if err != nil {
		n, err = pauseFn(ctx, d)
	}
	return n, err
}

// PauseAll switches mail off with PausedByRestore as the reason, marks it paused, removes every waiting message
// and every Kindle preview. It says how many things it changed (zero when mail was never set up).
func PauseAll(ctx context.Context, d *db.DB) (int, error) {
	n := 0
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE mail_config SET enabled = 0, restore_paused = 1, disabled_reason = ?, updated_at = ? WHERE id = 1 AND (enabled = 1 OR host <> '')`, PausedByRestore, db.Now())
		if err != nil {
			return err
		}
		c, _ := res.RowsAffected()
		n += int(c)
		res, err = tx.Exec(`DELETE FROM mail_outbox WHERE state IN ('pending','sending')`)
		if err != nil {
			return err
		}
		c, _ = res.RowsAffected()
		n += int(c)
		if _, err := tx.Exec(`DELETE FROM kindle_previews`); err != nil {
			return err
		}
		var subs int
		if err := tx.QueryRow(`SELECT count(*) FROM mail_digest`).Scan(&subs); err != nil {
			return err
		}
		n += subs // their next digest is set again when mail is switched on
		if err := tx.QueryRow(`SELECT count(*) FROM mail_progress`).Scan(&subs); err != nil {
			return err
		}
		n += subs // and so is their next progress email
		return nil
	})
	return n, err
}

// honourMarker pauses mail if a restore left its marker: the restore did not get to finish its step. It runs at start,
// before anything is sent, and removes the marker only once mail is paused.
func (s *Service) honourMarker(ctx context.Context) {
	if s.o.DataDir == "" {
		return
	}
	path := filepath.Join(s.o.DataDir, MarkerName)
	if _, err := os.Stat(path); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.o.Log.Warn("mail: could not look for the restore marker", "err", err)
		}
		return
	}
	if _, err := pauseFn(ctx, s.o.DB); err != nil {
		// A latch, not a timed hold: nothing is sent or made until a pause has succeeded, however long that takes and
		// whatever an administrator does meanwhile. Every pass tries again.
		s.o.Log.Error("mail: a restore left its marker and mail could not be paused; nothing will be sent until it is", "err", err)
		s.latched.Store(true)
		return
	}
	_ = os.Remove(path)
	s.latched.Store(false)
	s.o.Log.Warn("mail: paused at start because a restore did not finish its own pause; an administrator switches it on again in the mail settings")
}

// blockedByRestore says whether a restore's marker is still waiting to pause mail: while it is, nothing is sent or
// made. It tries the pause again each time it is asked.
func (s *Service) blockedByRestore(ctx context.Context) bool {
	if !s.latched.Load() {
		return false
	}
	s.honourMarker(ctx)
	return s.latched.Load()
}

func init() {
	backup.Pauses = append(backup.Pauses, backup.Pause{Name: "Mail", SQL: `sqlite3 scriobh.db "UPDATE mail_config SET enabled = 0, restore_paused = 1; DELETE FROM mail_outbox WHERE state IN ('pending','sending'); DELETE FROM kindle_previews;"`})
}

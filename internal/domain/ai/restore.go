package ai

import (
	"context"
	"fmt"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/secrets"
)

func init() {
	// After a restore, say which provider keys the installation's key cannot
	// read. Nothing is changed: the key stays stored, so putting the original
	// secret key back brings the providers back, and a test or a run says the
	// same thing plainly until then.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		n, err := UnreadableKeys(ctx, d, secrets.New(key))
		if err != nil || n == 0 {
			return "", err
		}
		if len(key) == 0 {
			return fmt.Sprintf("AI providers: no secret key was found, so the keys of %d provider(s) could not be checked. A new key is created at the next start, which cannot read them; put the original secret.key back before starting to keep them.", n), nil
		}
		return fmt.Sprintf("AI providers: the keys of %d provider(s) cannot be read with this secret key. They are kept as they are; enter each key again in the AI settings, or put the original secret key back before starting.", n), nil
	})
}

// UnreadableKeys counts the providers whose stored key does not open under box.
// A box without a key counts every stored key.
func UnreadableKeys(ctx context.Context, d *db.DB, box *secrets.Box) (int, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT `+providerCols+` FROM ai_providers WHERE api_key_cipher <> ''`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return 0, err
		}
		if !box.CanOpen(p.Cipher, sealContext(p)) {
			n++
		}
	}
	return n, rows.Err()
}

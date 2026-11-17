package mail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/publish"
)

// Send to Kindle (F-238).
//
// A person stores their own Send-to-Kindle address. kindle.preview compiles an edition and says exactly what
// would be sent, to whom; kindle.send, given the preview's token, compiles it again, checks that it is the same
// edition, and puts it in the outbox for that stored address and no other. There is no recipient on either
// op. The edition is the EPUB preset with no notes, no ghosted text and no author contact block, whatever the
// project's own presets say.

// KindlePreset is the one preset a Kindle delivery uses. The Kindle accepts EPUB; MOBI and AZW3 are not made.
const KindlePreset = "epub3"

// KindleOptions are the options a Kindle edition is held to, whatever a preset says: no notes, ghosted text
// omitted, no contact block (the same safe set a repository release forces).
func KindleOptions() publish.Options {
	return publish.Options{Notes: "none", Ghosts: "omit", Contact: publish.False()}
}

// KindleForced says what the preview tells the person is held back.
var KindleForced = []string{"notes: none", "ghosted text: omitted", "alternatives: left out", "author contact block: not included"}

const epubType = "application/epub+zip"

// KindleNotice is the plain statement a preview carries.
const KindleNotice = "Sending this puts a copy of the whole edition, as an EPUB file, on the mail relay this server's administrator set up, and from there on its way to Amazon and your Kindle. The manuscript leaves this server and cannot be called back."

// KindleSenderNote is what a person must do at Amazon first.
const KindleSenderNote = "Amazon only accepts a document from an address on your Approved Personal Document E-mail List. Add the sender address shown here to that list in your Amazon account (Content and Devices, Preferences, Personal Document Settings) before your first send."

// kindleFileName is the name the attachment goes out under: plain ASCII from the file's own name, ending .epub.
func kindleFileName(name string) string {
	stem := name
	if i := strings.LastIndexByte(stem, '.'); i > 0 {
		stem = stem[:i]
	}
	var b strings.Builder
	for _, r := range stem {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 80 {
		out = strings.Trim(out[:80], "-")
	}
	if out == "" {
		out = "edition"
	}
	return out + ".epub"
}

// KindleAddress returns a person's stored address, "" when there is none.
func (s *Service) KindleAddress(ctx context.Context, userID string) (string, error) {
	var a string
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT address FROM kindle_addresses WHERE user_id = ?`, userID).Scan(&a)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return a, err
}

func newToken() (token, hash string, err error) {
	var b [32]byte
	if _, err = rand.Read(b[:]); err != nil {
		return
	}
	token = base64.RawURLEncoding.EncodeToString(b[:])
	return token, tokenHash(token), nil
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// cancelKindle drops a person's waiting Kindle messages and previews: their address changed or went away. It
// returns the ids of the messages dropped, so the caller can remove their files once the transaction commits.
func cancelKindle(tx *sql.Tx, userID string) ([]string, error) {
	rows, err := tx.Query(`SELECT id FROM mail_outbox WHERE person_id = ? AND kind = 'kindle' AND state = 'pending'`, userID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE mail_outbox SET state = 'dead', last_error = 'cancelled', finished_at = ?, recipient = '', subject = '', body = '', file_name = '', claimed_at = ''
		WHERE person_id = ? AND kind = 'kindle' AND state = 'pending'`, db.Now(), userID); err != nil {
		return nil, err
	}
	_, err = tx.Exec(`DELETE FROM kindle_previews WHERE user_id = ?`, userID)
	return ids, err
}

func (s *Service) previewExpiry() string { return s.stamp(s.now().Add(s.lim.PreviewTTL)) }

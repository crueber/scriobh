// Package share is read-only and comment-only access to a manuscript for a person
// with no account: a link with an unguessable token.
//
// The design, in short (docs/OPEN-QUESTIONS.md, "Phase 5 review and sharing
// decisions", has the reasoning):
//
//   - A share link is not a user and has no session. The public routes under /s/
//     (internal/server/share.go) are not registry ops: a registry op's caller is a
//     signed-in person, and every part of the authorization machinery (roles, pins,
//     the sweeps) is about people. The routes call the functions here, which are the
//     same ones the owner's ops use, and which write comments through the same
//     InsertComment as a member's comment.create.
//   - The token is "<link id>.<secret>": 256 bits of secret, shown once, stored as a
//     SHA-256 and compared in constant time. The id finds the row; an unknown id, a
//     wrong secret, an expired, revoked link and a link of a trashed
//     project are all ErrNotFound, and a lookup of an unknown id does the same work as
//     one of a known id, so nothing tells them apart.
//   - A link shows the manuscript's reading text only: notes, research, the stash,
//     scratchpads, settings, members, history, other links, exports and anything AI
//     are not reachable through it. Private notes, HTML comments, front matter, the
//     alternatives annotation and (by default) ghosted text are not in what it
//     serves.
//   - A comment link lets a guest write comments with a display name; a guest sees
//     only the threads started through the same link.
package share

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/passkit"
)

// Scopes of a link.
const (
	ScopeRead    = "read"
	ScopeComment = "comment"
)

// Limits.
const (
	MaxLinksPerProject      = 50
	MinPassphrase           = passkit.MinPassphrase
	MaxPassphrase           = passkit.MaxPassphrase
	MaxLabel                = 100
	MaxGuestCommentsPerLink = 500
	unlockValid             = 8 * time.Hour
)

// ErrNotFound is the one answer for every token that does not open a link.
var ErrNotFound = errors.New("share: no such link")

// ErrBusy is returned when the passphrase hashing slots are all taken.
var ErrBusy = errors.New("share: busy")

// ErrBadPassphrase is a wrong passphrase.
var ErrBadPassphrase = errors.New("share: wrong passphrase")

// LockedError says too many wrong passphrases were tried.
type LockedError struct{ RetryAfter time.Duration }

func (e *LockedError) Error() string { return "share: locked" }

// Link is an authenticated share link: the principal of the public routes. It
// carries what the link allows, and nothing about any person.
type Link struct {
	ID            string
	ProjectID     string
	ProjectTitle  string
	Label         string
	Scope         string
	Ghosts        string // omit or keep
	AllowDownload bool
	HasPassphrase bool
	ExpiresAt     string
	CreatedAt     string
	// Whole says the link shows the whole manuscript. Otherwise it shows Roots (each with its
	// descendants), and a link whose roots are all gone shows nothing, never everything.
	Whole bool
	Roots []string

	passHash string
}

// CanComment reports whether the link lets a guest write.
func (l Link) CanComment() bool { return l.Scope == ScopeComment }

// ---- tokens ----------------------------------------------------------------------------------

var tokenRe = regexp.MustCompile(`^(shl_[0-9A-HJKMNP-TV-Z]{26})\.([A-Za-z0-9_-]{43})$`)

// NewToken makes a link's id, its secret and the stored hash of the secret.
func NewToken() (id, token, hash string) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("share: entropy source failed: " + err.Error())
	}
	secret := base64.RawURLEncoding.EncodeToString(b[:])
	id = db.NewID("shl")
	return id, id + "." + secret, hashSecret(secret)
}

func hashSecret(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// dummyHash is compared against when the id is unknown, so a miss costs a compare too.
var dummyHash = hashSecret("no such link")

// Authenticate finds the link a token opens. Every failure is ErrNotFound.
func Authenticate(ctx context.Context, d *db.DB, m *Meter, token string) (Link, error) {
	var l Link
	mt := tokenRe.FindStringSubmatch(token)
	if mt == nil {
		return l, ErrNotFound
	}
	id, secret := mt[1], mt[2]
	var (
		stored                 string
		pass, expires, revoked sql.NullString
		trashed, purged        sql.NullString
		allow, whole           int
	)
	err := d.Read().QueryRowContext(ctx, `SELECT sl.project_id, p.title, sl.label, sl.scope, sl.ghosts, sl.allow_download, sl.whole, sl.token_hash, sl.passphrase_hash, sl.expires_at,
		sl.revoked_at, sl.created_at, p.trashed_at, p.deleted_at
		FROM share_links sl JOIN projects p ON p.id = sl.project_id WHERE sl.id = ?`, id).
		Scan(&l.ProjectID, &l.ProjectTitle, &l.Label, &l.Scope, &l.Ghosts, &allow, &whole, &stored, &pass, &expires, &revoked, &l.CreatedAt, &trashed, &purged)
	if err != nil {
		stored = dummyHash
	}
	want, _ := hex.DecodeString(stored)
	got, _ := hex.DecodeString(hashSecret(secret))
	match := subtle.ConstantTimeCompare(got, want) == 1
	if err != nil || !match {
		return Link{}, ErrNotFound
	}
	now := db.NowTime()
	if revoked.Valid || trashed.Valid || purged.Valid {
		return Link{}, ErrNotFound
	}
	if expires.Valid {
		if t, perr := db.Parse(expires.String); perr != nil || !now.Before(t) {
			return Link{}, ErrNotFound
		}
	}
	l.ID, l.AllowDownload, l.ExpiresAt, l.Whole = id, allow == 1, expires.String, whole == 1
	l.HasPassphrase, l.passHash = pass.Valid, pass.String
	rows, err := d.Read().QueryContext(ctx, `SELECT sd.document_id FROM share_link_docs sd JOIN documents dd ON dd.id = sd.document_id
		WHERE sd.share_id = ? AND dd.deleted_at IS NULL ORDER BY sd.document_id`, id)
	if err != nil {
		return Link{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return Link{}, err
		}
		l.Roots = append(l.Roots, doc)
	}
	if err := rows.Err(); err != nil {
		return Link{}, err
	}
	if !l.Whole && len(l.Roots) == 0 {
		return Link{}, ErrNotFound // every document it named is gone: it shows nothing, not everything
	}
	return l, nil
}

// ---- use counters ------------------------------------------------------------------------------

// MeterService is the name the meter is registered under in ops.Env.
const MeterService = "share.meter"

// Meter counts views and last use in memory and writes them in batches, so a
// public request never waits for the single writer. A crash loses at most the
// last flush interval of counts, which are statistics.
type Meter struct {
	d     *db.DB
	mu    sync.Mutex
	views map[string]int
	used  map[string]string
	last  time.Time
	fails map[string][]time.Time // refused guest attempts in the last hour, per link
}

// Fail records a refused guest attempt.
func (m *Meter) Fail(id string, at time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fails == nil {
		m.fails = map[string][]time.Time{}
	}
	keep := m.fails[id][:0]
	for _, t := range m.fails[id] {
		if at.Sub(t) < time.Hour {
			keep = append(keep, t)
		}
	}
	if len(keep) < 1000 {
		keep = append(keep, at)
	}
	m.fails[id] = keep
}

// Fails counts the refused guest attempts of a link since a time, and the oldest of them.
func (m *Meter) Fails(id string, since time.Time) (int, time.Time) {
	if m == nil {
		return 0, time.Time{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	n, oldest := 0, time.Time{}
	for _, t := range m.fails[id] {
		if t.After(since) {
			if n == 0 || t.Before(oldest) {
				oldest = t
			}
			n++
		}
	}
	return n, oldest
}

// NewMeter makes a meter over the database.
func NewMeter(d *db.DB) *Meter {
	return &Meter{d: d, views: map[string]int{}, used: map[string]string{}, last: time.Now()}
}

// Pending is the views counted and not yet written for a link.
func (m *Meter) Pending(id string) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.views[id]
}

// Used records a use of the link (and a view when view is true).
func (m *Meter) Used(id string, view bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if view {
		m.views[id]++
	}
	m.used[id] = db.Now()
	m.mu.Unlock()
}

// Flush writes the counts.
func (m *Meter) Flush(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	views, used := m.views, m.used
	m.views, m.used, m.last = map[string]int{}, map[string]string{}, time.Now()
	m.mu.Unlock()
	if len(used) == 0 {
		return nil
	}
	err := m.d.WithTx(ctx, func(tx *sql.Tx) error {
		for id, at := range used {
			if _, err := tx.ExecContext(ctx, `UPDATE share_links SET views = views + ?, last_used_at = ? WHERE id = ?`, views[id], at, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil { // put the counts back for the next flush
		m.mu.Lock()
		for id, n := range views {
			m.views[id] += n
		}
		for id, at := range used {
			if m.used[id] < at {
				m.used[id] = at
			}
		}
		m.mu.Unlock()
	}
	return err
}

// FlushSoon starts a flush in the background when the last one is old.
func (m *Meter) FlushSoon() {
	if m == nil {
		return
	}
	m.mu.Lock()
	due := time.Since(m.last) > 15*time.Second && len(m.used) > 0
	if due {
		m.last = time.Now() // one flusher at a time
	}
	m.mu.Unlock()
	if due {
		go func() { _ = m.Flush(context.Background()) }()
	}
}

// ---- passphrases -------------------------------------------------------------------------------

// The passphrase is a gate for a link, not encryption of the text: it is hashed with
// argon2id (the OWASP minimum cost) and compared in constant time. At most two
// hashes run at once, so a flood of attempts costs a bounded amount of memory.
var kdfSlots = make(chan struct{}, 2)

// ownerSlots are the owner's own: hashing the passphrase of a link being made or changed waits
// here, never behind (or in front of) the strangers who are trying passphrases.
var ownerSlots = make(chan struct{}, 2)

func acquireKDF() (func(), bool) {
	select {
	case kdfSlots <- struct{}{}:
		return func() { <-kdfSlots }, true
	default:
		return nil, false
	}
}

// HashPassphrase returns the stored form of a passphrase (internal/passkit).
func HashPassphrase(p string) (string, error) {
	ownerSlots <- struct{}{}
	defer func() { <-ownerSlots }()
	return passkit.Hash(p)
}

func verifyPassphrase(encoded, p string) bool { return passkit.Verify(encoded, p) }

// ---- unlock proof ------------------------------------------------------------------------------

// A link with a passphrase is opened by sending the passphrase once to Unlock, which
// answers a proof: an expiry and a MAC of (link, passphrase hash, expiry) under a
// key derived from the instance secret. The client sends the proof in the
// X-Share-Unlock header with every later request. It is not a cookie and not a
// session: nothing is stored on the server, nothing is set on the client, and
// changing the passphrase or revoking the link makes every proof void.

func proofMAC(key []byte, l Link, expiry int64) []byte {
	mac := hmac.New(sha256.New, key)
	var e [8]byte
	binary.BigEndian.PutUint64(e[:], uint64(expiry))
	mac.Write([]byte("scriobh-share-unlock\x00" + l.ID + "\x00" + l.passHash + "\x00"))
	mac.Write(e[:])
	return mac.Sum(nil)
}

// UnlockKey derives the proof key from the instance secret.
func UnlockKey(secret []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("scriobh share unlock key v1"))
	return h.Sum(nil)
}

// MakeProof is the proof for l, valid for eight hours from now.
func MakeProof(key []byte, l Link, now time.Time) string {
	exp := now.Add(unlockValid).Unix()
	var e [8]byte
	binary.BigEndian.PutUint64(e[:], uint64(exp))
	return base64.RawURLEncoding.EncodeToString(append(e[:], proofMAC(key, l, exp)...))
}

// CheckProof says whether a proof opens l now.
func CheckProof(key []byte, l Link, proof string, now time.Time) bool {
	if !l.HasPassphrase {
		return true
	}
	raw, err := base64.RawURLEncoding.DecodeString(proof)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	exp := int64(binary.BigEndian.Uint64(raw[:8]))
	if now.Unix() >= exp {
		return false
	}
	return hmac.Equal(raw[8:], proofMAC(key, l, exp))
}

// Wrong passphrases are counted per link AND per client address bucket (an IPv4 address, or an IPv6 /64:
// the caller makes the bucket), so a stranger who keeps getting it wrong locks themselves out and nobody
// else: a reader on another address unlocks as usual. The first three wrong tries cost nothing; from the
// fourth each one earns a longer wait (30 seconds, doubling, at most an hour) before that bucket may try
// again, even with the right passphrase. The owner sees the total in share.usage.
const failureLife = passkit.FailureLife // a bucket that has been quiet this long starts again

// unlockDelay is the wait after the nth wrong try in a row (internal/passkit).
func unlockDelay(failed int) time.Duration { return passkit.Delay(failed) }

// Unlock checks a passphrase for a client in a bucket and, when it is right, returns a proof.
func Unlock(ctx context.Context, d *db.DB, key []byte, l Link, bucket, pass string) (string, error) {
	if !l.HasPassphrase {
		return "", nil
	}
	now := db.NowTime()
	var failed int
	var lockedUntil, lastAt sql.NullString
	err := d.Read().QueryRowContext(ctx, `SELECT failed, locked_until, last_at FROM share_unlock_failures WHERE share_id = ? AND bucket = ?`, l.ID, bucket).Scan(&failed, &lockedUntil, &lastAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if t, perr := db.Parse(lastAt.String); perr == nil && now.Sub(t) > failureLife {
		failed = 0
	}
	if t, perr := db.Parse(lockedUntil.String); perr == nil && now.Before(t) {
		return "", &LockedError{RetryAfter: t.Sub(now)}
	}
	rel, ok := acquireKDF()
	if !ok {
		return "", ErrBusy
	}
	good := verifyPassphrase(l.passHash, pass)
	rel()
	if good {
		if failed > 0 || lockedUntil.Valid {
			_ = d.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `DELETE FROM share_unlock_failures WHERE share_id = ? AND bucket = ?`, l.ID, bucket)
				return err
			})
		}
		return MakeProof(key, l, now), nil
	}
	failed++
	var until any
	if dl := unlockDelay(failed); dl > 0 {
		until = db.Format(now.Add(dl))
	}
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM share_unlock_failures WHERE share_id = ? AND last_at < ?`, l.ID, db.Format(now.Add(-failureLife))); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO share_unlock_failures(share_id, bucket, failed, locked_until, last_at) VALUES (?,?,?,?,?)
			ON CONFLICT(share_id, bucket) DO UPDATE SET failed = excluded.failed, locked_until = excluded.locked_until, last_at = excluded.last_at`,
			l.ID, bucket, failed, until, db.Format(now)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE share_links SET failed_unlocks = failed_unlocks + 1 WHERE id = ?`, l.ID)
		return err
	})
	if err != nil {
		return "", err
	}
	return "", ErrBadPassphrase
}

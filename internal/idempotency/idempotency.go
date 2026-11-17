// Package idempotency makes a retried write safe. A client sends an
// Idempotency-Key with a mutating request; the first time, the request runs and
// its successful response is stored under (principal, key) for 24 hours; the
// same key with the same request returns that stored response instead of
// running again.
//
// Rules, all covered by tests:
//
//   - Same key and same request fingerprint inside the window: the stored
//     status and body come back with Idempotent-Replay: true.
//   - Same key, different request: 422 idempotency_key_reuse. Nothing runs.
//   - Concurrent duplicates serialise on an in-process lock: one runs, the
//     others wait and then replay its result. (One process owns the database,
//     so a process lock is enough.)
//   - Keys belong to a principal (an API key or a browser session's user); one
//     principal's key means nothing to another.
//   - Only successful responses are stored. A failure leaves nothing behind, so
//     a retry after an error is a fresh attempt.
//   - A response that holds a secret, or is too large to keep, leaves a
//     tombstone: the request is remembered as having run, and a repeat is a
//     409 rather than a second execution.
//
// The row is written after the operation commits. A crash in the instant
// between the two lets one retry run again; that is the cost of not holding
// the writer across the operation.
package idempotency

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// TTL is how long a stored response can be replayed.
const TTL = 24 * time.Hour

// MaxKeyLen bounds an Idempotency-Key.
const MaxKeyLen = 200

// MaxBody is the largest response kept for replay.
const MaxBody = 1 << 20

// Defaults for the per-principal caps.
const (
	DefaultMaxBytes   = 8 << 20
	DefaultMaxEntries = 200
)

// Response is a stored response.
type Response struct {
	Status int
	Body   []byte
}

// Result is what an executed request produced.
type Result struct {
	Response
	// Store says to keep it. False (an error, say) leaves no record.
	Store bool
	// Tombstone records that the request ran without keeping its response.
	Tombstone bool
}

// Store keeps idempotency records.
type Store struct {
	DB *db.DB
	// Now is the clock; tests inject one.
	Now func() time.Time
	// MaxBytes and MaxEntries cap what one principal's stored responses may
	// hold (the sum of their bodies, and their count). When a new record would
	// go over, the oldest of that principal's records are dropped first. Zero
	// means DefaultMaxBytes and DefaultMaxEntries.
	MaxBytes   int
	MaxEntries int

	mu    sync.Mutex
	locks map[string]*lock
}

type lock struct {
	ch   chan struct{}
	refs int
}

// New returns a Store over d.
func New(d *db.DB) *Store { return &Store{DB: d, Now: db.NowTime, locks: map[string]*lock{}} }

// ValidKey reports whether a client key is acceptable: printable ASCII, not
// empty, not long.
func ValidKey(k string) bool {
	if k == "" || len(k) > MaxKeyLen {
		return false
	}
	for i := 0; i < len(k); i++ {
		if k[i] < 0x21 || k[i] > 0x7e {
			return false
		}
	}
	return true
}

// Fingerprint identifies a request: the op, the method, the path with its
// parameters, the sorted query and the exact body.
func Fingerprint(op, method, path string, query url.Values, body []byte) string {
	h := sha256.New()
	h.Write([]byte(op + "\x00" + method + "\x00" + path + "\x00"))
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		vs := append([]string(nil), query[k]...)
		sort.Strings(vs)
		h.Write([]byte(k + "=" + strings.Join(vs, ",") + "&"))
	}
	h.Write([]byte("\x00"))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// acquire takes the per-key lock, waiting for a holder or the context.
func (s *Store) acquire(ctx context.Context, id string) (release func(), err error) {
	for {
		s.mu.Lock()
		l, held := s.locks[id]
		if !held {
			s.locks[id] = &lock{ch: make(chan struct{})}
			s.mu.Unlock()
			return func() {
				s.mu.Lock()
				l := s.locks[id]
				delete(s.locks, id)
				s.mu.Unlock()
				close(l.ch)
			}, nil
		}
		ch := l.ch
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Do runs exec at most once per (principal, key) inside the window.
//
// replay is true when the response is a stored one. The error is an *ops.Error
// for reuse and for an unreplayable repeat.
func (s *Store) Do(ctx context.Context, principal, key, op, fingerprint string, exec func() (Result, error)) (resp Response, replay bool, err error) {
	release, err := s.acquire(ctx, principal+"\x00"+key)
	if err != nil {
		return Response{}, false, err
	}
	defer release()

	now := s.Now()
	var (
		hash, body string
		status     int
		expires    string
	)
	err = s.DB.Read().QueryRowContext(ctx, `SELECT request_hash, response_json, status_code, expires_at FROM idempotency_keys
		WHERE principal_id = ? AND key = ?`, principal, key).Scan(&hash, &body, &status, &expires)
	switch {
	case err == nil:
		if exp, perr := db.Parse(expires); perr == nil && exp.After(now) {
			if hash != fingerprint {
				return Response{}, false, ops.IdempotencyReuse()
			}
			if status == 0 {
				return Response{}, false, ops.Conflict("This request already ran, but its response held a secret and was not kept. Check the result (for a new key, list your keys) and do not repeat it.")
			}
			return Response{Status: status, Body: []byte(body)}, true, nil
		}
		// Expired: the key is free again.
	case errors.Is(err, sql.ErrNoRows):
	default:
		return Response{}, false, err
	}

	res, err := exec()
	if err != nil {
		return Response{}, false, err
	}
	if !res.Store && !res.Tombstone {
		return res.Response, false, nil
	}
	rec := res.Response
	if res.Tombstone || len(rec.Body) > MaxBody {
		rec = Response{}
	}
	maxBytes, maxEntries := s.MaxBytes, s.MaxEntries
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if len(rec.Body) > maxBytes {
		rec = Response{} // too big for this caller's allowance: remembered as having run
	}
	werr := s.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO idempotency_keys(principal_id, key, op, request_hash, response_json, status_code, created_at, expires_at)
			VALUES (?,?,?,?,?,?,?,?)`, principal, key, op, fingerprint, string(rec.Body), rec.Status, db.Format(now), db.Format(now.Add(TTL))); err != nil {
			return err
		}
		return evict(tx, principal, key, maxBytes, maxEntries)
	})
	// The operation has committed; a failure to record it must not turn its
	// success into an error. The retry would run again, which is the documented
	// edge, so say nothing more here.
	_ = werr
	return res.Response, false, nil
}

// evict drops the principal's oldest records until what is left fits the caps,
// never the record just written.
func evict(tx *sql.Tx, principal, keep string, maxBytes, maxEntries int) error {
	rows, err := tx.Query(`SELECT key, length(CAST(response_json AS BLOB)) FROM idempotency_keys WHERE principal_id = ? ORDER BY created_at DESC, key DESC`, principal)
	if err != nil {
		return err
	}
	var drop []string
	entries, bytes, over := 0, 0, false
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return err
		}
		entries++
		bytes += n
		// Newest first: once the caps are passed, everything older goes.
		if k != keep && (over || entries > maxEntries || bytes > maxBytes) {
			drop = append(drop, k)
			over = true
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, k := range drop {
		if _, err := tx.Exec(`DELETE FROM idempotency_keys WHERE principal_id = ? AND key = ?`, principal, k); err != nil {
			return err
		}
	}
	return nil
}

// Sweep deletes records past their expiry.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM idempotency_keys WHERE expires_at <= ?`, db.Format(s.Now()))
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}

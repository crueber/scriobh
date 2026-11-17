// Package auth is OIDC sign-in, sessions, roles and the user table. There are
// no local credentials: if the identity provider is down, new sign-ins fail,
// and existing sessions keep working until they expire.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// SessionCookie is the session cookie's name.
const SessionCookie = "scriobh_session"

// touchEvery limits how often a request writes last_seen_at.
const touchEvery = time.Minute

// Sessions creates, finds and revokes sessions.
type Sessions struct {
	DB  *db.DB
	Cfg *config.Config
	Key []byte
}

// SessionInfo describes a live session.
type SessionInfo struct {
	ID string
}

func hashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// Create makes a session for userID and returns the cookie secret. Only its
// SHA-256 is stored.
func (s *Sessions) Create(ctx context.Context, userID, userAgent, ip string) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := db.NowTime()
	expires = now.Add(time.Duration(s.Cfg.SessionHours) * time.Hour)
	var idle any
	if m := s.Cfg.SessionIdleMinutes; m > 0 {
		idle = db.Format(now.Add(time.Duration(m) * time.Minute))
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	err = s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO sessions(id, user_id, token_hash, created_at, last_seen_at, expires_at, idle_expires_at, user_agent, ip_hash)
			VALUES (?,?,?,?,?,?,?,?,?)`, db.NewID("ses"), userID, hashToken(token), db.Format(now), db.Format(now), db.Format(expires), idle, userAgent, s.ipHash(ip))
		return err
	})
	return
}

func (s *Sessions) ipHash(ip string) string {
	if ip == "" {
		return ""
	}
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte("ip:" + ip))
	return hex.EncodeToString(m.Sum(nil))[:16]
}

// IPHash is the stored, non-reversible form of a client address.
func (s *Sessions) IPHash(ip string) string { return s.ipHash(ip) }

// ErrNoSession means the token does not name a live session.
var ErrNoSession = errors.New("no session")

// Lookup resolves a cookie secret to a principal. Expired, idle-expired,
// revoked and deleted-user sessions all fail the same way.
func (s *Sessions) Lookup(ctx context.Context, token string) (ops.Principal, SessionInfo, error) {
	return s.lookup(ctx, token, true)
}

// Peek is Lookup that does not count as activity. A long-lived stream asks
// whether its session still stands every half minute; if that refreshed the
// idle timer, an open tab would keep a session alive past the idle cap.
func (s *Sessions) Peek(ctx context.Context, token string) (ops.Principal, SessionInfo, error) {
	return s.lookup(ctx, token, false)
}

func (s *Sessions) lookup(ctx context.Context, token string, touch bool) (ops.Principal, SessionInfo, error) {
	if token == "" {
		return ops.Principal{}, SessionInfo{}, ErrNoSession
	}
	now := db.Now()
	var (
		sid, uid, email, name, role string
		last                        string
		idle                        sql.NullString
		isAgent                     int
	)
	err := s.DB.Read().QueryRowContext(ctx, `SELECT s.id, u.id, COALESCE(u.email,''), u.display_name, u.role, s.last_seen_at, s.idle_expires_at, u.is_agent
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.revoked_at IS NULL AND s.expires_at > ?
		  AND (s.idle_expires_at IS NULL OR s.idle_expires_at > ?) AND u.deleted_at IS NULL AND u.disabled_at IS NULL`,
		hashToken(token), now, now).Scan(&sid, &uid, &email, &name, &role, &last, &idle, &isAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return ops.Principal{}, SessionInfo{}, ErrNoSession
	}
	if err != nil {
		return ops.Principal{}, SessionInfo{}, err
	}
	if t, err := db.Parse(last); touch && (err != nil || db.NowTime().Sub(t) > touchEvery) {
		s.touch(ctx, sid, uid)
	}
	return ops.Principal{UserID: uid, Email: email, Name: name, Role: role, IsAgent: isAgent == 1, Via: "session", SessionID: sid}, SessionInfo{ID: sid}, nil
}

func (s *Sessions) touch(ctx context.Context, sid, uid string) {
	now := db.NowTime()
	var idle any
	if m := s.Cfg.SessionIdleMinutes; m > 0 {
		idle = db.Format(now.Add(time.Duration(m) * time.Minute))
	}
	_ = s.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id = ?`, db.Format(now), idle, sid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE users SET last_seen_at = ? WHERE id = ?`, db.Format(now), uid)
		return err
	})
}

// Revoke ends the session named by a cookie secret.
func (s *Sessions) Revoke(ctx context.Context, token string) error {
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE sessions SET revoked_at = ? WHERE token_hash = ? AND revoked_at IS NULL`, db.Now(), hashToken(token))
		return err
	})
}

// RevokeAll ends every live session of a user.
func (s *Sessions) RevokeAll(ctx context.Context, userID string) error {
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, db.Now(), userID)
		return err
	})
}

// CSRFToken is the per-session token for cookie-authenticated form posts.
func (s *Sessions) CSRFToken(sessionID string) string {
	m := hmac.New(sha256.New, s.Key)
	m.Write([]byte("csrf:" + sessionID))
	return hex.EncodeToString(m.Sum(nil))
}

// CheckCSRF compares a submitted token in constant time.
func (s *Sessions) CheckCSRF(sessionID, got string) bool {
	return hmac.Equal([]byte(s.CSRFToken(sessionID)), []byte(got))
}

// SetCookie writes the session cookie.
func (s *Sessions) SetCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: token, Path: "/", Expires: expires,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Cfg.Secure(),
	})
}

// ClearCookie removes the session cookie.
func (s *Sessions) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.Cfg.Secure(),
	})
}

// SweepExpired deletes sessions that ended more than a week ago.
func (s *Sessions) SweepExpired(ctx context.Context) error {
	cutoff := db.Format(db.NowTime().Add(-7 * 24 * time.Hour))
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM sessions WHERE expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?)`, cutoff, cutoff)
		return err
	})
}

// ClientIP returns the request's client address. X-Forwarded-For is honoured
// only when the proxy is trusted.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The rightmost entry is the one our own proxy appended; anything to
			// its left is client-controlled and must not pick the rate-limit bucket.
			if i := strings.LastIndex(xff, ","); i >= 0 {
				xff = xff[i+1:]
			}
			return strings.TrimSpace(xff)
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

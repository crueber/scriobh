package account

import (
	"database/sql"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

const (
	maxUserAgent   = 120
	maxSessionList = 100
)

// liveSession is the liveness test auth.Sessions.Lookup applies, so the list
// shows exactly the sessions that can still sign in. ?1 is the current time.
const liveSession = `s.revoked_at IS NULL AND s.expires_at > ?1 AND (s.idle_expires_at IS NULL OR s.idle_expires_at > ?1)`

// Session is one of the caller's live sessions. The cookie secret and its hash
// are never part of it.
type Session struct {
	ID         string `json:"id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	UserAgent  string `json:"user_agent" desc:"The browser, trimmed to 120 characters."`
	Current    bool   `json:"current" desc:"True for the session making this call."`
}

// SessionsOut lists sessions, newest first.
type SessionsOut struct {
	Items []Session `json:"items"`
}

func sessionList(c *ops.Ctx, _ EmptyIn) (SessionsOut, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT s.id, s.created_at, s.last_seen_at, s.user_agent FROM sessions s
		WHERE s.user_id = ?2 AND `+liveSession+` ORDER BY s.created_at DESC, s.id DESC LIMIT `+strconv.Itoa(maxSessionList),
		db.Now(), c.P.UserID)
	if err != nil {
		return SessionsOut{}, err
	}
	defer rows.Close()
	out := SessionsOut{Items: []Session{}}
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.LastSeenAt, &s.UserAgent); err != nil {
			return SessionsOut{}, err
		}
		s.UserAgent = trimAgent(s.UserAgent)
		s.Current = c.P.SessionID != "" && s.ID == c.P.SessionID
		out.Items = append(out.Items, s)
	}
	return out, rows.Err()
}

// trimAgent shortens by characters, not bytes, so a cut never splits a rune.
func trimAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	if utf8.RuneCountInString(ua) <= maxUserAgent {
		return ua
	}
	return string([]rune(ua)[:maxUserAgent])
}

// RevokeIn names one of the caller's sessions.
type RevokeIn struct {
	SID string `path:"sid" json:"-" desc:"Session id, from session.list."`
}

// Validate refuses anything that is not shaped like a session id before it
// reaches the database. A well-formed id that is unknown, or belongs to
// someone else, is not_found: the two look the same.
func (in *RevokeIn) Validate() error {
	if !strings.HasPrefix(in.SID, "ses_") || len(in.SID) > 64 {
		return ops.Invalid("sid", "That is not a session id.")
	}
	return nil
}

// RevokeOut reports one revoked session.
type RevokeOut struct {
	ID      string `json:"id"`
	Revoked bool   `json:"revoked"`
	Current bool   `json:"current" desc:"True when it was the session making the call, which is now signed out."`
}

func sessionRevoke(c *ops.Ctx, in RevokeIn) (RevokeOut, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		// Scoped by the caller's id: another person's session is not_found.
		// Revoking your own session twice is fine; the second call is a no-op.
		var one int
		if err := tx.QueryRow(`SELECT 1 FROM sessions WHERE id = ? AND user_id = ?`, in.SID, c.P.UserID).Scan(&one); err != nil {
			if err == sql.ErrNoRows {
				return ops.NotFound("session")
			}
			return err
		}
		res, err := tx.Exec(`UPDATE sessions SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL`, db.Now(), in.SID, c.P.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			c.Audit(ops.AuditEntry{Action: "session.revoke", TargetType: "session", TargetID: in.SID})
			return c.FlushTo(tx)
		}
		return nil
	})
	if err != nil {
		return RevokeOut{}, err
	}
	return RevokeOut{ID: in.SID, Revoked: true, Current: in.SID == c.P.SessionID}, nil
}

// RevokeOthersOut counts the sessions that were ended.
type RevokeOthersOut struct {
	Revoked int `json:"revoked"`
}

// sessionRevokeOthers ends every live session except the one making the call.
// A caller with no session of its own (an API key) has none to keep, so for it
// every live browser session counts as "other".
func sessionRevokeOthers(c *ops.Ctx, _ EmptyIn) (RevokeOthersOut, error) {
	var n int64
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		now := db.Now()
		res, err := tx.Exec(`UPDATE sessions AS s SET revoked_at = ?1 WHERE s.user_id = ?2 AND s.id != ?3 AND `+liveSession,
			now, c.P.UserID, c.P.SessionID)
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		c.Audit(ops.AuditEntry{Action: "session.revoke_others", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"count": n}})
		return c.FlushTo(tx)
	})
	return RevokeOthersOut{Revoked: int(n)}, err
}

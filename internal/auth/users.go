package auth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Claims are the ID-token claims Scríobh reads.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Groups        []string
}

// User is a row of the users table.
type User struct {
	ID          string
	Email       string
	DisplayName string
	Role        string
}

// DeniedError is a sign-in refusal with a message safe to show.
type DeniedError struct{ Reason string }

func (e *DeniedError) Error() string { return "sign-in denied: " + e.Reason }

// RoleFromGroups maps IdP groups to an instance role, highest first. The
// result is empty when the user is in none of the configured groups.
func RoleFromGroups(cfg *config.Config, groups []string) string {
	in := func(g string) bool {
		if g == "" {
			return false
		}
		for _, x := range groups {
			if x == g {
				return true
			}
		}
		return false
	}
	switch {
	case in(cfg.AdminGroup):
		return ops.RoleAdmin
	case in(cfg.WriterGroup):
		return ops.RoleWriter
	case in(cfg.ReaderGroup):
		return ops.RoleReader
	}
	return ""
}

// LostAccess hooks run, inside the sign-in transaction, when the identity provider has removed a person from every
// group: whatever else of theirs sends or publishes on their behalf (a mail digest) stops with their sessions.
var LostAccess []func(tx *sql.Tx, userID string) error

// Users reads and writes the users table.
type Users struct {
	DB  *db.DB
	Cfg *config.Config
}

// SignInResult is what a successful sign-in produced.
type SignInResult struct {
	User User
	// OfferAdminClaim is true for the very first user on an instance with no
	// admin group configured: they may claim administration once.
	OfferAdminClaim bool
}

// SignIn binds the identity to a user (creating or linking it) and refreshes
// the role from the IdP groups. It returns *DeniedError for refusals.
func (u *Users) SignIn(ctx context.Context, issuer string, cl Claims) (SignInResult, error) {
	if cl.Subject == "" {
		return SignInResult{}, &DeniedError{"the identity provider sent no subject"}
	}
	email := strings.ToLower(strings.TrimSpace(cl.Email))
	// The local profile of a desktop instance is the one identity that may have no address: there is no provider
	// to verify one, and the person may not have given one. The exception is for that issuer string alone, which
	// only the embedded issuer's sign-in produces.
	local := issuer == DesktopIdentityIssuer
	if !local || email != "" {
		if !cl.EmailVerified {
			return SignInResult{}, &DeniedError{"your email address is not verified at the identity provider"}
		}
		if email == "" {
			return SignInResult{}, &DeniedError{"the identity provider sent no email address"}
		}
	}
	name := strings.TrimSpace(cl.Name)
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	// The address as it is stored: NULL when there is none (the index on it is for addresses that exist).
	var emailCol any = email
	if email == "" {
		emailCol = nil
	}
	role := RoleFromGroups(u.Cfg, cl.Groups)

	var res SignInResult
	revoked := false
	err := u.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL`).Scan(&total); err != nil {
			return err
		}
		claimMode := u.Cfg.AdminGroup == ""

		var (
			id, curRole string
			found       = true
		)
		var deletedAt, disabledAt sql.NullString
		err := tx.QueryRow(`SELECT id, role, deleted_at, disabled_at FROM users WHERE oidc_issuer = ? AND oidc_sub = ?`, issuer, cl.Subject).Scan(&id, &curRole, &deletedAt, &disabledAt)
		if errors.Is(err, sql.ErrNoRows) {
			found = false
		} else if err != nil {
			return err
		} else if deletedAt.Valid {
			return &DeniedError{"this account was removed; ask the administrator"}
		} else if disabledAt.Valid {
			return &DeniedError{"this account has been switched off; ask the administrator"}
		}

		if !found && email != "" {
			// A first sign-in. Link by email only to an unbound account.
			var otherID string
			var otherSub sql.NullString
			err := tx.QueryRow(`SELECT id, oidc_sub FROM users WHERE email = ? COLLATE NOCASE AND deleted_at IS NULL AND is_agent = 0`, email).Scan(&otherID, &otherSub)
			switch {
			case err == nil && otherSub.Valid:
				return &DeniedError{"that email address belongs to a different account at this identity provider"}
			case err == nil:
				r, err := tx.Exec(`UPDATE users SET oidc_issuer = ?, oidc_sub = ?, updated_at = ? WHERE id = ? AND oidc_sub IS NULL`, issuer, cl.Subject, db.Now(), otherID)
				if err != nil {
					return err
				}
				if n, _ := r.RowsAffected(); n != 1 {
					return &DeniedError{"that account was claimed while you were signing in; try again"}
				}
				id, curRole, found = otherID, "", true
			case !errors.Is(err, sql.ErrNoRows):
				return err
			}
		}

		// Decide the role.
		newRole := role
		first := total == 0 && !found
		if newRole == "" && claimMode && first {
			newRole = ops.RoleReader // provisional until the claim screen
		}
		if claimMode && found && newRole == "" {
			// No admin group exists, so the IdP cannot say who an admin is. Two
			// people keep their role without a group: a claimed admin, and the
			// first user who skipped the claim screen (their claim is still open).
			// Everyone else loses access when their IdP groups are removed.
			if curRole == ops.RoleAdmin {
				newRole = ops.RoleAdmin
			} else if open, _ := adminClaimOpen(tx, id); open {
				newRole = curRole
			}
		}
		if claimMode && found && curRole == ops.RoleAdmin {
			newRole = ops.RoleAdmin
		}
		if newRole == "" {
			if found {
				// Removed from every group at the IdP: end their sessions now,
				// not when the cookie expires. The refusal is returned after commit.
				if _, err := tx.Exec(`UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, db.Now(), id); err != nil {
					return err
				}
				for _, h := range LostAccess {
					if err := h(tx, id); err != nil {
						return err
					}
				}
				revoked = true
				return nil
			}
			if local {
				// Desktop mode, embedded issuer: there are no provider groups and no one to ask. The only way to reach
				// accounts is a data directory that came from a server instance, which has its own.
				return &DeniedError{"this data directory came from a Scríobh server and has its own accounts, so the local profile has none here. Quit the application and run: scriobh desktop use-local-profile --adopt-admin <email or id of an administrator>"}
			}
			return &DeniedError{"your account is not in a Scríobh group; ask the administrator to add you to " + u.groupHint()}
		}

		now := db.Now()
		if !found {
			id = db.NewID("usr")
			if _, err := tx.Exec(`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, avatar_color, role, created_at, updated_at, last_seen_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, id, issuer, cl.Subject, emailCol, name, AvatarColor(id), newRole, now, now, now); err != nil {
				return err
			}
			res.OfferAdminClaim = claimMode && first
		} else {
			if _, err := tx.Exec(`UPDATE users SET email = ?, display_name = CASE WHEN display_name = '' THEN ? ELSE display_name END,
				role = ?, updated_at = ?, last_seen_at = ? WHERE id = ?`, emailCol, name, newRole, now, now, id); err != nil {
				return err
			}
			if claimMode && newRole != ops.RoleAdmin {
				res.OfferAdminClaim, _ = adminClaimOpen(tx, id)
			}
		}
		res.User = User{ID: id, Email: email, DisplayName: name, Role: newRole}
		return nil
	})
	if err == nil && revoked {
		return SignInResult{}, &DeniedError{"your account is not in a Scríobh group any more; ask the administrator to add you to " + u.groupHint()}
	}
	return res, err
}

func (u *Users) groupHint() string {
	var gs []string
	for _, g := range []string{u.Cfg.WriterGroup, u.Cfg.ReaderGroup} {
		if g != "" {
			gs = append(gs, g)
		}
	}
	return strings.Join(gs, " or ")
}

// adminClaimOpen reports whether userID may still claim administration: no
// admin exists, nothing has been claimed, and they are the first user.
func adminClaimOpen(tx *sql.Tx, userID string) (bool, error) {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM settings WHERE key = 'admin_claimed'`).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	if err := tx.QueryRow(`SELECT count(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL`).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	var first string
	if err := tx.QueryRow(`SELECT id FROM users WHERE is_agent = 0 AND deleted_at IS NULL ORDER BY created_at, id LIMIT 1`).Scan(&first); err != nil {
		return false, err
	}
	return first == userID, nil
}

// AdminClaimOpen reports whether the user may claim administration now.
func (u *Users) AdminClaimOpen(ctx context.Context, userID string) bool {
	if u.Cfg.AdminGroup != "" {
		return false
	}
	var open bool
	_ = u.DB.WithTx(ctx, func(tx *sql.Tx) error {
		open, _ = adminClaimOpen(tx, userID)
		return nil
	})
	return open
}

// ClaimAdmin makes userID the administrator, once. A second claim fails.
func (u *Users) ClaimAdmin(ctx context.Context, userID string) error {
	if u.Cfg.AdminGroup != "" {
		return &DeniedError{"administration comes from the identity provider on this instance"}
	}
	return u.DB.WithTx(ctx, func(tx *sql.Tx) error {
		open, err := adminClaimOpen(tx, userID)
		if err != nil {
			return err
		}
		if !open {
			return &DeniedError{"the administrator claim is closed"}
		}
		now := db.Now()
		r, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES ('admin_claimed', ?, ?) ON CONFLICT(key) DO NOTHING`, userID, now)
		if err != nil {
			return err
		}
		if n, _ := r.RowsAffected(); n != 1 {
			return &DeniedError{"the administrator claim is closed"}
		}
		_, err = tx.Exec(`UPDATE users SET role = 'admin', updated_at = ? WHERE id = ?`, now, userID)
		return err
	})
}

// Get returns a user by id.
func (u *Users) Get(ctx context.Context, id string) (User, error) {
	var x User
	err := u.DB.Read().QueryRowContext(ctx, `SELECT id, COALESCE(email,''), display_name, role FROM users WHERE id = ? AND deleted_at IS NULL AND disabled_at IS NULL`, id).
		Scan(&x.ID, &x.Email, &x.DisplayName, &x.Role)
	return x, err
}

// AvatarColor derives a deterministic flat colour from an id.
func AvatarColor(id string) string {
	palette := []string{"#2F5D50", "#7A4B2A", "#3B5B7A", "#6B3E4F", "#4B6B3A", "#5A4E7A", "#8A6420", "#4A4740"}
	h := sha256.Sum256([]byte(id))
	return palette[int(h[0])%len(palette)]
}

// Initials returns up to two letters for the avatar.
func Initials(name string) string {
	var out []rune
	for _, f := range strings.Fields(name) {
		for _, r := range f {
			out = append(out, []rune(strings.ToUpper(string(r)))...)
			break
		}
		if len(out) == 2 {
			break
		}
	}
	if len(out) == 0 {
		return "?"
	}
	return string(out)
}

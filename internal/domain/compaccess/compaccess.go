// Package compaccess is who may see a Compendium. It is a leaf package (it
// imports only ops and db) because three packages need it and none may import
// another to get it: projects (adding a collaborator can grant Compendium
// access), compendiums (membership ops) and entities (every read and write).
//
// Compendium membership is separate from project membership. Being on a
// project grants nothing here, so a beta reader on one book cannot read the
// whole bible. Access is granted only by a row in compendium_members, written
// by an explicit act, and never silently.
package compaccess

import (
	"context"
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Roles, in increasing power. A Compendium has no reviewer role yet: nothing
// comments on a bible entry, so a reviewer would only be a reader.
const (
	Reader = "reader"
	Editor = "editor"
	Owner  = "owner"
)

var rank = map[string]int{Reader: 1, Editor: 2, Owner: 3}

// Roles lists the roles a member can be given.
var Roles = []string{Editor, Reader}

// AtLeast reports whether role is want or more powerful.
func AtLeast(role, want string) bool { return rank[role] >= rank[want] && rank[role] > 0 }

// Valid reports whether role is one of the three roles.
func Valid(role string) bool { return rank[role] > 0 }

// Querier is what *sql.DB and *sql.Tx share.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// roleSQL resolves a user's role on a Compendium: an instance admin acts as
// owner of every Compendium (the same rule as for projects), anyone else needs
// a membership row. ?1 is the user, ?2 the Compendium.
const roleSQL = `SELECT CASE WHEN u.role = 'admin' THEN 'owner' ELSE m.role END
	FROM compendiums c
	JOIN users u ON u.id = ?1
	LEFT JOIN compendium_members m ON m.compendium_id = c.id AND m.user_id = u.id
	WHERE c.id = ?2 AND c.deleted_at IS NULL AND u.deleted_at IS NULL
	  AND (u.role = 'admin' OR m.role IS NOT NULL)`

// Role is the user's role on a Compendium. ok is false when it does not exist
// or the user has no access: the two are indistinguishable on purpose. A
// trashed or archived Compendium still resolves, so it can be restored.
func Role(ctx context.Context, q Querier, userID, compendiumID string) (role string, ok bool) {
	if userID == "" || compendiumID == "" {
		return "", false
	}
	if err := q.QueryRowContext(ctx, roleSQL, userID, compendiumID).Scan(&role); err != nil {
		return "", false
	}
	return role, true
}

// RoleFunc resolves Compendium roles for the registry.
func RoleFunc(d *db.DB) ops.CompendiumRoleFunc {
	return func(ctx context.Context, userID, compendiumID string) (string, bool) {
		return Role(ctx, d.Read(), userID, compendiumID)
	}
}

// CanRead reports whether the user may read the Compendium.
func CanRead(ctx context.Context, q Querier, userID, compendiumID string) bool {
	role, ok := Role(ctx, q, userID, compendiumID)
	return ok && AtLeast(role, Reader)
}

// GrantTx gives a user access to a Compendium inside the caller's
// transaction. It never downgrades: a member who already holds the role or a
// stronger one is left as they are. It returns the role the user holds
// afterwards and whether this call changed anything. The caller has already
// checked that whoever asked is allowed to grant.
func GrantTx(tx *sql.Tx, compendiumID, userID, role, now string) (held string, changed bool, err error) {
	if !Valid(role) || role == Owner {
		return "", false, ops.Invalid("role", "A Compendium member can be an editor or a reader.")
	}
	var cur string
	err = tx.QueryRow(`SELECT role FROM compendium_members WHERE compendium_id = ? AND user_id = ?`, compendiumID, userID).Scan(&cur)
	switch {
	case err == sql.ErrNoRows:
		if _, err := tx.Exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`,
			compendiumID, userID, role, now); err != nil {
			return "", false, err
		}
		return role, true, nil
	case err != nil:
		return "", false, err
	}
	if AtLeast(cur, role) {
		return cur, false, nil
	}
	if _, err := tx.Exec(`UPDATE compendium_members SET role = ? WHERE compendium_id = ? AND user_id = ?`, role, compendiumID, userID); err != nil {
		return "", false, err
	}
	return role, true, nil
}

// User is a person found by email.
type User struct {
	ID    string
	Name  string
	Email string
	Role  string // the instance role: admin, writer or reader
}

// CanWrite reports whether the person's instance role lets them edit what they
// are invited to. An instance reader can only ever be a reader.
func (u User) CanWrite() bool { return u.Role == ops.RoleWriter || u.Role == ops.RoleAdmin }

// FindUser looks a person up by exact email (case-insensitively). Only a live,
// human account can be found: a removed account and an API-only agent look
// exactly like an address nobody has. There is deliberately no prefix search
// and no autocomplete, so this cannot be used to list who is on the instance.
func FindUser(ctx context.Context, q Querier, email string) (User, bool) {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > 320 || !strings.Contains(email, "@") {
		return User{}, false
	}
	var u User
	err := q.QueryRowContext(ctx, `SELECT id, display_name, COALESCE(email, ''), role FROM users
		WHERE email = ? COLLATE NOCASE AND deleted_at IS NULL AND disabled_at IS NULL AND is_agent = 0`, email).Scan(&u.ID, &u.Name, &u.Email, &u.Role)
	if err != nil {
		return User{}, false
	}
	return u, true
}

// ErrNoSuchPerson is the one answer for an address that cannot be added: an
// address nobody signed in with, a removed account, an API-only account, and a
// reader being added as an editor all look the same, so adding people cannot be
// used to find out who is on the instance. (A successful add still tells the
// owner that the address belongs to someone; that is the point of adding.)
func ErrNoSuchPerson() *ops.Error {
	return ops.NotFound("A signed-in person with that email address who can be added")
}

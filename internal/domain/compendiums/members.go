package compendiums

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
)

// Compendium membership is separate from project membership. Nobody gets in
// by being on a project; the owner adds people here, one at a time.

func registerMembers(r *ops.Registry) {
	owner := ops.RequireCompendium(compaccess.Owner)
	ops.Register(r, ops.Op{
		Name: "compendium.member.list", Method: "GET", Path: "/api/v1/compendiums/{id}/members",
		Desc: "List who can open a Compendium and their roles. Owner only.", Auth: owner,
	}, memberList)
	ops.Register(r, ops.Op{
		Name: "compendium.member.add", Method: "POST", Path: "/api/v1/compendiums/{id}/members", Status: 201, Scope: ops.ScopeAdmin,
		Desc: "Give a person access to a Compendium, as an editor or a reader. They must already have signed in to this instance; find them by exact email. " +
			"An address nobody can be added under (unknown, removed, an API-only account, or a reader who cannot be an editor) all answer the same way, so this cannot be used to list who is here. " +
			"A person already on the Compendium is refused, not changed; use compendium.member.update. Owner only.",
		Auth: owner, NoDryRun: true, // a preview would say whose address it is
	}, memberAdd)
	ops.Register(r, ops.Op{
		Name: "compendium.member.update", Method: "PATCH", Path: "/api/v1/compendiums/{id}/members", Scope: ops.ScopeAdmin,
		Desc: "Change a member's role (editor or reader). The owner's role cannot change. Owner only.", Auth: owner,
	}, memberUpdate)
	ops.Register(r, ops.Op{
		Name: "compendium.member.remove", Method: "DELETE", Path: "/api/v1/compendiums/{id}/members", Scope: ops.ScopeAdmin,
		Desc: "Take a person off a Compendium. Their project memberships are untouched, and so is anything they wrote. The owner cannot be removed. Owner only.", Auth: owner,
	}, memberRemove)
}

// Member is one person on a Compendium.
type Member struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role" enum:"owner,editor,reader"`
	CreatedAt   string `json:"created_at"`
}

// MemberListIn names the Compendium.
type MemberListIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in MemberListIn) CompendiumRef() string { return in.ID }

// MemberListOut lists the members, the owner first.
type MemberListOut struct {
	Items []Member `json:"items"`
}

// MemberItem wraps one member.
type MemberItem struct {
	Item Member `json:"item"`
}

func memberList(c *ops.Ctx, in MemberListIn) (MemberListOut, error) {
	if err := refuseProjectKey(c); err != nil {
		return MemberListOut{}, err
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT m.user_id, u.display_name, COALESCE(u.email, ''), m.role, m.created_at
		FROM compendium_members m JOIN users u ON u.id = m.user_id
		WHERE m.compendium_id = ? ORDER BY m.role = 'owner' DESC, u.display_name, m.user_id`, in.ID)
	if err != nil {
		return MemberListOut{}, err
	}
	defer rows.Close()
	out := MemberListOut{Items: []Member{}}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.CreatedAt); err != nil {
			return MemberListOut{}, err
		}
		out.Items = append(out.Items, m)
	}
	return out, rows.Err()
}

// MemberAddIn adds a person.
type MemberAddIn struct {
	ID    string `path:"id" json:"-" desc:"Compendium id."`
	Email string `json:"email" desc:"The person's exact email address."`
	Role  string `json:"role" enum:"editor,reader"`
}

// CompendiumRef lets the registry authorize by membership.
func (in MemberAddIn) CompendiumRef() string { return in.ID }

// Validate checks the shape; whether the address belongs to anyone is decided
// in the handler, with one answer for every way it can fail.
func (in *MemberAddIn) Validate() error {
	in.Email = strings.TrimSpace(in.Email)
	if in.Email == "" {
		return ops.Invalid("email", "Enter the person's email address.")
	}
	if in.Role != compaccess.Editor && in.Role != compaccess.Reader {
		return ops.Invalid("role", "A member is an editor or a reader.")
	}
	return nil
}

func memberAdd(c *ops.Ctx, in MemberAddIn) (MemberItem, error) {
	if err := refuseProjectKey(c); err != nil {
		return MemberItem{}, err
	}
	var out Member
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := compaccess.AllowLookup(c.P.UserID); err != nil {
			return err
		}
		u, ok := compaccess.FindUser(c, tx, in.Email)
		if !ok || (in.Role == compaccess.Editor && !u.CanWrite()) {
			return compaccess.ErrNoSuchPerson()
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM compendium_members WHERE compendium_id = ? AND user_id = ?`, in.ID, u.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ops.Conflict("That person is already on this Compendium. Change their role with compendium.member.update.")
		}
		now := db.Now()
		if _, _, err := compaccess.GrantTx(tx, in.ID, u.ID, in.Role, now); err != nil {
			return err
		}
		out = Member{UserID: u.ID, DisplayName: u.Name, Email: u.Email, Role: in.Role, CreatedAt: now}
		c.Audit(ops.AuditEntry{Action: "compendium.member.add", TargetType: "compendium", TargetID: in.ID,
			Detail: map[string]any{"user_id": u.ID, "role": in.Role}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return MemberItem{}, err
	}
	return MemberItem{Item: out}, nil
}

// MemberUpdateIn changes a role.
type MemberUpdateIn struct {
	ID     string `path:"id" json:"-" desc:"Compendium id."`
	UserID string `json:"user_id" desc:"The member."`
	Role   string `json:"role" enum:"editor,reader"`
}

// CompendiumRef lets the registry authorize by membership.
func (in MemberUpdateIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *MemberUpdateIn) Validate() error {
	if in.UserID == "" {
		return ops.Invalid("user_id", "Say which member.")
	}
	if in.Role != compaccess.Editor && in.Role != compaccess.Reader {
		return ops.Invalid("role", "A member is an editor or a reader.")
	}
	return nil
}

func memberUpdate(c *ops.Ctx, in MemberUpdateIn) (MemberItem, error) {
	if err := refuseProjectKey(c); err != nil {
		return MemberItem{}, err
	}
	var out Member
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadMember(tx, in.ID, in.UserID)
		if err != nil {
			return err
		}
		if cur.Role == compaccess.Owner {
			return ops.Conflict("The owner's role cannot be changed.")
		}
		if in.Role == compaccess.Editor {
			var role string
			if err := tx.QueryRow(`SELECT role FROM users WHERE id = ?`, in.UserID).Scan(&role); err != nil {
				return err
			}
			if role != ops.RoleWriter && role != ops.RoleAdmin {
				return ops.Invalid("role", "This person's account can read but not edit, so they can only be a reader.")
			}
		}
		if cur.Role != in.Role {
			if _, err := tx.Exec(`UPDATE compendium_members SET role = ? WHERE compendium_id = ? AND user_id = ?`, in.Role, in.ID, in.UserID); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "compendium.member.update", TargetType: "compendium", TargetID: in.ID,
				Detail: map[string]any{"user_id": in.UserID, "from": cur.Role, "to": in.Role}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
		}
		out = cur
		out.Role = in.Role
		return nil
	})
	if err != nil {
		return MemberItem{}, err
	}
	return MemberItem{Item: out}, nil
}

// MemberRemoveIn removes a person.
type MemberRemoveIn struct {
	ID     string `path:"id" json:"-" desc:"Compendium id."`
	UserID string `query:"user_id" desc:"The member to remove."`
}

// CompendiumRef lets the registry authorize by membership.
func (in MemberRemoveIn) CompendiumRef() string { return in.ID }

// Validate checks the shape.
func (in *MemberRemoveIn) Validate() error {
	if in.UserID == "" {
		return ops.Invalid("user_id", "Say which member.")
	}
	return nil
}

// RemoveOut reports a removal.
type RemoveOut struct {
	Removed bool   `json:"removed"`
	UserID  string `json:"user_id"`
}

func memberRemove(c *ops.Ctx, in MemberRemoveIn) (RemoveOut, error) {
	if err := refuseProjectKey(c); err != nil {
		return RemoveOut{}, err
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadMember(tx, in.ID, in.UserID)
		if err != nil {
			return err
		}
		if cur.Role == compaccess.Owner {
			return ops.Conflict("The owner cannot be removed.")
		}
		if _, err := tx.Exec(`DELETE FROM compendium_members WHERE compendium_id = ? AND user_id = ?`, in.ID, in.UserID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "compendium.member.remove", TargetType: "compendium", TargetID: in.ID,
			Detail: map[string]any{"user_id": in.UserID, "role": cur.Role}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return RemoveOut{}, err
	}
	return RemoveOut{Removed: true, UserID: in.UserID}, nil
}

func loadMember(tx *sql.Tx, compendiumID, userID string) (Member, error) {
	var m Member
	err := tx.QueryRow(`SELECT m.user_id, u.display_name, COALESCE(u.email, ''), m.role, m.created_at
		FROM compendium_members m JOIN users u ON u.id = m.user_id WHERE m.compendium_id = ? AND m.user_id = ?`, compendiumID, userID).
		Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return Member{}, ops.NotFound("member")
	}
	return m, err
}

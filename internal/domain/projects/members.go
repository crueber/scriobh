package projects

import (
	"context"
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
)

// Project members. A project has one owner (its creator) and any number of
// editors, reviewers and readers. Membership of a project is separate from
// membership of its Compendium: adding a collaborator never opens the bible
// unless the owner says so, and the result of an add states exactly what was
// and was not granted (OPEN-QUESTIONS, Compendium access on collaborator add).

// MemberRoles are the roles a person can be given on a project.
var MemberRoles = []string{ops.ProjectEditor, ops.ProjectReviewer, ops.ProjectReader}

func registerMembers(r *ops.Registry) {
	owner := ops.RequireProject(ops.ProjectOwner)
	ops.Register(r, ops.Op{
		Name: "project.member.list", Method: "GET", Path: "/api/v1/projects/{id}/members",
		Desc: "List who is on a project and their roles. Owner only. When the project has a primary Compendium and the caller is a member of it, each person's role on that Compendium is shown too.",
		Auth: owner,
	}, memberList)
	ops.Register(r, ops.Op{
		Name: "project.member.add", Method: "POST", Path: "/api/v1/projects/{id}/members", Status: 201, Scope: ops.ScopeAdmin,
		Desc: "Add a person to a project as an editor, a reviewer or a reader. They must already have signed in to this instance; find them by exact email. " +
			"An address nobody can be added under (unknown, removed, an API-only account, or an instance reader being made an editor) all answer the same way, so this cannot be used to list who is here. " +
			"The project's Compendium is a separate door: add_to_compendium asks to add them to the primary Compendium too. It defaults to true for an editor and false for a reviewer or reader, and the result's compendium object says what was granted and, if nothing, why. " +
			"Nothing is granted silently, and only the Compendium's owner can grant. Owner only.",
		Auth: owner, NoDryRun: true, // a preview would say whose address it is
	}, memberAdd)
	ops.Register(r, ops.Op{
		Name: "project.member.update", Method: "PATCH", Path: "/api/v1/projects/{id}/members", Scope: ops.ScopeAdmin,
		Desc: "Change a member's role on the project (editor, reviewer or reader). The owner's role cannot change. Their Compendium access is separate and is not changed. Owner only.",
		Auth: owner,
	}, memberUpdate)
	ops.Register(r, ops.Op{
		Name: "project.member.remove", Method: "DELETE", Path: "/api/v1/projects/{id}/members", Scope: ops.ScopeAdmin,
		Desc: "Take a person off a project. Their Compendium access is separate and stays: the result says whether they remain on the project's primary Compendium, and compendium.member.remove takes them off it. The owner cannot be removed. Owner only.",
		Auth: owner,
	}, memberRemove)
}

// Member is one person on a project.
type Member struct {
	UserID         string `json:"user_id"`
	DisplayName    string `json:"display_name"`
	Email          string `json:"email"`
	Role           string `json:"role" enum:"owner,editor,reviewer,reader"`
	CompendiumRole string `json:"compendium_role,omitempty" desc:"Their role on the project's primary Compendium, shown only when the caller is a member of that Compendium; empty otherwise or when they are not on it."`
	CreatedAt      string `json:"created_at"`
}

// MemberListIn names the project.
type MemberListIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in MemberListIn) ProjectRef() string { return in.ID }

// MemberListOut lists the members, the owner first.
type MemberListOut struct {
	Items []Member `json:"items"`
}

// primaryFor returns the project's primary Compendium when the caller can read
// it, else "".
func primaryFor(ctx context.Context, q compaccess.Querier, userID, projectID string) string {
	var cid sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT compendium_id FROM projects WHERE id = ?`, projectID).Scan(&cid); err != nil || !cid.Valid {
		return ""
	}
	if !compaccess.CanRead(ctx, q, userID, cid.String) {
		return ""
	}
	return cid.String
}

func memberList(c *ops.Ctx, in MemberListIn) (MemberListOut, error) {
	cid := primaryFor(c, c.DB().Read(), c.P.UserID, in.ID)
	rows, err := c.DB().Read().QueryContext(c, `SELECT m.user_id, u.display_name, COALESCE(u.email, ''), m.role, m.created_at, COALESCE(cm.role, '')
		FROM project_members m JOIN users u ON u.id = m.user_id
		LEFT JOIN compendium_members cm ON cm.user_id = m.user_id AND cm.compendium_id = ?
		WHERE m.project_id = ? ORDER BY m.role = 'owner' DESC, u.display_name, m.user_id`, cid, in.ID)
	if err != nil {
		return MemberListOut{}, err
	}
	defer rows.Close()
	out := MemberListOut{Items: []Member{}}
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.CreatedAt, &m.CompendiumRole); err != nil {
			return MemberListOut{}, err
		}
		if cid == "" {
			m.CompendiumRole = ""
		}
		out.Items = append(out.Items, m)
	}
	return out, rows.Err()
}

// MemberAddIn adds a person.
type MemberAddIn struct {
	ID              string `path:"id" json:"-" desc:"Project id."`
	Email           string `json:"email" desc:"The person's exact email address."`
	Role            string `json:"role" enum:"editor,reviewer,reader"`
	AddToCompendium *bool  `json:"add_to_compendium,omitempty" desc:"Also add them to the project's primary Compendium: as an editor there for an editor, as a reader for a reviewer or reader. Default true for an editor, false for the others. Only the Compendium's owner can grant it; asking for it explicitly when the caller cannot is refused before anything is written."`
}

// ProjectRef lets the registry authorize by membership.
func (in MemberAddIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *MemberAddIn) Validate() error {
	in.Email = strings.TrimSpace(in.Email)
	if in.Email == "" {
		return ops.Invalid("email", "Enter the person's email address.")
	}
	if !contains(MemberRoles, in.Role) {
		return ops.Invalid("role", "A member is an editor, a reviewer or a reader.")
	}
	return nil
}

// CompendiumGrant says what happened to Compendium access when a person was
// added to a project.
type CompendiumGrant struct {
	Requested    bool   `json:"requested" desc:"Whether the add asked for Compendium access: the request's value, else the default for the role."`
	Granted      bool   `json:"granted" desc:"True when this call gave them access, or raised it."`
	CompendiumID string `json:"compendium_id,omitempty" desc:"The primary Compendium, when there is one."`
	Role         string `json:"role,omitempty" desc:"The role they hold on it after this call; empty when none."`
	Reason       string `json:"reason,omitempty" enum:"not_requested,no_primary_compendium,already_member,not_compendium_owner,restricted_key" desc:"Why access was not granted, when it was not."`
}

// MemberAddOut is the new member and what Compendium access they got.
type MemberAddOut struct {
	Item       Member          `json:"item"`
	Compendium CompendiumGrant `json:"compendium"`
}

func memberAdd(c *ops.Ctx, in MemberAddIn) (MemberAddOut, error) {
	explicit := in.AddToCompendium != nil
	ask := in.Role == ops.ProjectEditor
	if explicit {
		ask = *in.AddToCompendium
	}
	var out MemberAddOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		out = MemberAddOut{}
		out.Compendium.Requested = ask
		if err := compaccess.AllowLookup(c.P.UserID); err != nil {
			return err
		}
		u, ok := compaccess.FindUser(c, tx, in.Email)
		if !ok || (in.Role == ops.ProjectEditor && !u.CanWrite()) {
			return compaccess.ErrNoSuchPerson()
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM project_members WHERE project_id = ? AND user_id = ?`, in.ID, u.ID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ops.Conflict("That person is already on this project. Change their role with project.member.update.")
		}
		// Decide the Compendium side before writing anything, so a refusal
		// leaves no half-added member.
		var primary sql.NullString
		if err := tx.QueryRow(`SELECT compendium_id FROM projects WHERE id = ?`, in.ID).Scan(&primary); err != nil {
			return ops.NotFound("project")
		}
		g := &out.Compendium
		g.CompendiumID = primary.String
		grant := false
		switch {
		case !ask:
			g.Reason = "not_requested"
		case !primary.Valid:
			g.Reason = "no_primary_compendium"
		default:
			role, member := compaccess.Role(c, tx, c.P.UserID, primary.String)
			switch {
			case c.P.Pinned():
				if explicit {
					return ops.Forbidden("a key restricted to one project or compendium cannot grant Compendium access")
				}
				g.Reason = "restricted_key"
			case !member || role != compaccess.Owner:
				if explicit {
					return ops.Forbidden("only the Compendium's owner can add people to it; add them to the project without it, or ask the owner")
				}
				g.Reason = "not_compendium_owner"
			default:
				grant = true
			}
		}
		now := db.Now()
		if _, err := tx.Exec(`INSERT INTO project_members(project_id, user_id, role, created_at) VALUES (?,?,?,?)`, in.ID, u.ID, in.Role, now); err != nil {
			return err
		}
		if grant {
			want := compaccess.Reader
			if in.Role == ops.ProjectEditor {
				want = compaccess.Editor
			}
			held, changed, err := compaccess.GrantTx(tx, primary.String, u.ID, want, now)
			if err != nil {
				return err
			}
			g.Role, g.Granted = held, changed
			if !changed {
				g.Reason = "already_member"
			} else {
				c.Audit(ops.AuditEntry{Action: "compendium.member.add", TargetType: "compendium", TargetID: primary.String,
					Detail: map[string]any{"user_id": u.ID, "role": held, "via": "project.member.add", "project_id": in.ID}})
			}
		}
		out.Item = Member{UserID: u.ID, DisplayName: u.Name, Email: u.Email, Role: in.Role, CreatedAt: now, CompendiumRole: g.Role}
		c.Audit(ops.AuditEntry{Action: "project.member.add", TargetType: "project", TargetID: in.ID,
			Detail: map[string]any{"user_id": u.ID, "role": in.Role, "compendium_requested": ask, "compendium_granted": g.Granted}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return MemberAddOut{}, err
	}
	return out, nil
}

// MemberUpdateIn changes a role.
type MemberUpdateIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	UserID string `json:"user_id" desc:"The member."`
	Role   string `json:"role" enum:"editor,reviewer,reader"`
}

// ProjectRef lets the registry authorize by membership.
func (in MemberUpdateIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *MemberUpdateIn) Validate() error {
	if in.UserID == "" {
		return ops.Invalid("user_id", "Say which member.")
	}
	if !contains(MemberRoles, in.Role) {
		return ops.Invalid("role", "A member is an editor, a reviewer or a reader.")
	}
	return nil
}

// MemberItem wraps one member.
type MemberItem struct {
	Item Member `json:"item"`
}

func loadMember(tx *sql.Tx, projectID, userID string) (Member, error) {
	var m Member
	err := tx.QueryRow(`SELECT m.user_id, u.display_name, COALESCE(u.email, ''), m.role, m.created_at
		FROM project_members m JOIN users u ON u.id = m.user_id WHERE m.project_id = ? AND m.user_id = ?`, projectID, userID).
		Scan(&m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return Member{}, ops.NotFound("member")
	}
	return m, err
}

func memberUpdate(c *ops.Ctx, in MemberUpdateIn) (MemberItem, error) {
	var out Member
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadMember(tx, in.ID, in.UserID)
		if err != nil {
			return err
		}
		if cur.Role == ops.ProjectOwner {
			return ops.Conflict("The owner's role cannot be changed.")
		}
		if in.Role == ops.ProjectEditor {
			var role string
			if err := tx.QueryRow(`SELECT role FROM users WHERE id = ?`, in.UserID).Scan(&role); err != nil {
				return err
			}
			if role != ops.RoleWriter && role != ops.RoleAdmin {
				return ops.Invalid("role", "This person's account can read but not edit, so they can only be a reviewer or a reader.")
			}
		}
		if cur.Role != in.Role {
			if _, err := tx.Exec(`UPDATE project_members SET role = ? WHERE project_id = ? AND user_id = ?`, in.Role, in.ID, in.UserID); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "project.member.update", TargetType: "project", TargetID: in.ID,
				Detail: map[string]any{"user_id": in.UserID, "from": cur.Role, "to": in.Role}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
		}
		out = cur
		out.Role = in.Role
		return nil
	})
	return MemberItem{Item: out}, err
}

// MemberRemoveIn removes a person.
type MemberRemoveIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	UserID string `query:"user_id" desc:"The member to remove."`
}

// ProjectRef lets the registry authorize by membership.
func (in MemberRemoveIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *MemberRemoveIn) Validate() error {
	if in.UserID == "" {
		return ops.Invalid("user_id", "Say which member.")
	}
	return nil
}

// MemberRemoveOut reports a removal and what it left alone.
type MemberRemoveOut struct {
	Removed        bool   `json:"removed"`
	UserID         string `json:"user_id"`
	CompendiumRole string `json:"compendium_role,omitempty" desc:"The role they still hold on the project's primary Compendium, shown only when the caller is a member of it. Removing someone from a project never removes them from the Compendium; do that with compendium.member.remove."`
}

func memberRemove(c *ops.Ctx, in MemberRemoveIn) (MemberRemoveOut, error) {
	out := MemberRemoveOut{UserID: in.UserID}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadMember(tx, in.ID, in.UserID)
		if err != nil {
			return err
		}
		if cur.Role == ops.ProjectOwner {
			return ops.Conflict("The owner cannot be removed.")
		}
		if _, err := tx.Exec(`DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, in.ID, in.UserID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM user_project_state WHERE project_id = ? AND user_id = ?`, in.ID, in.UserID); err != nil {
			return err
		}
		if cid := primaryFor(c, tx, c.P.UserID, in.ID); cid != "" {
			_ = tx.QueryRow(`SELECT role FROM compendium_members WHERE compendium_id = ? AND user_id = ?`, cid, in.UserID).Scan(&out.CompendiumRole)
		}
		out.Removed = true
		c.Audit(ops.AuditEntry{Action: "project.member.remove", TargetType: "project", TargetID: in.ID,
			Detail: map[string]any{"user_id": in.UserID, "role": cur.Role}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return MemberRemoveOut{}, err
	}
	return out, nil
}

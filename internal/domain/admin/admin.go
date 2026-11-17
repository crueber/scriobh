// Package admin holds the instance administrator's ops: who is on the instance (list, switch off and on, make or
// stop being an administrator), what each person and the instance use against the limits, and a diagnostics bundle
// with nothing private in it. Every op is for an instance administrator; the writes are for a browser session.
package admin

import (
	"context"
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the runtime parts are registered under in ops.Env.
const ServiceName = "admin"

// LogLine is one warning or error the server kept, already scrubbed.
type LogLine struct {
	At    string            `json:"at"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Service is what the server hands the ops that need the running process: the limits it was started with, whether
// roles come from the identity provider's groups, the shape of the configuration (an allow-list: values that can be
// shown, with every secret and path left out) and the log lines kept.
type Service struct {
	Limits attachments.Limits
	// MaxKeysPerUser is the live API keys one person may hold (0: no cap).
	MaxKeysPerUser int
	// AdminFromIdP is true when an administrator group is configured: administration then comes from the identity
	// provider at every sign-in, and cannot be given or taken away here.
	AdminFromIdP bool
	Version      string
	DataDir      string
	Config       func() map[string]any
	Log          func() []LogLine
}

func svc(c *ops.Ctx) *Service {
	s, _ := ops.Service[*Service](c.Env, ServiceName)
	if s == nil {
		s = &Service{Limits: attachments.DefaultLimits}
	}
	return s
}

// Register adds the admin ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "admin.user.list", Method: "GET", Path: "/api/v1/admin/users", Auth: ops.Admin().NotFoundForOthers(),
		Desc: "The people on this instance, for an administrator: name, email (an administrator sees it; nobody else does here), instance role, last seen, whether they are switched off, how many projects they own and are on, the storage they use and the browser sessions they have open. Agents are not listed. Filter by q (a word of the name or email) and status (all, active, disabled)."},
		list)
	ops.Register(r, ops.Op{Name: "admin.user.get", Method: "GET", Path: "/api/v1/admin/users/{id}", Auth: ops.Admin().NotFoundForOthers(),
		Desc: "One person by id, as admin.user.list shows them. Administrators only."},
		func(c *ops.Ctx, in UserRef) (UserItem, error) {
			if !strings.HasPrefix(in.ID, "usr_") {
				return UserItem{}, ops.Invalid("id", "That is not a person's id. Use one from the people list.")
			}
			return one(c, in.ID)
		})
	ops.Register(r, ops.Op{Name: "admin.user.disable", Method: "POST", Path: "/api/v1/admin/users/{id}/disable", Auth: ops.Admin().NotFoundForOthers().ForSessions(),
		Desc: "Switch a person off. They cannot sign in, every browser session of theirs ends now, their API keys and open streams stop working, and everything they wrote stays where it is (the projects they own stay theirs). Not yourself, and not the last administrator. Administrators only, browser session only; the screen asks you to type their name first."},
		disable)
	ops.Register(r, ops.Op{Name: "admin.user.enable", Method: "POST", Path: "/api/v1/admin/users/{id}/enable", Auth: ops.Admin().NotFoundForOthers().ForSessions(),
		Desc: "Switch a person back on. Their keys work again; they sign in as before. Administrators only, browser session only."},
		enable)
	ops.Register(r, ops.Op{Name: "admin.user.set_role", Method: "PATCH", Path: "/api/v1/admin/users/{id}/role", Auth: ops.Admin().NotFoundForOthers().ForSessions(),
		Desc: "Give or take away administration (role admin, or writer or reader for someone who is not one). Only where no administrator group is configured: with one, the identity provider decides at every sign-in and this is refused. Not the last administrator. Someone made a writer or reader here has their role set again from the identity provider's groups at their next sign-in. Administrators only, browser session only."},
		setRole)
	ops.Register(r, ops.Op{Name: "admin.quota.list", Method: "GET", Path: "/api/v1/admin/quotas", Auth: ops.Admin().NotFoundForOthers(),
		Desc: "What the instance and each person use against the limits that exist: attachment storage (per person, per project, whole instance, largest file) and API keys per person. Read only: the limits are set by the server's configuration, not per person."},
		quotas)
	ops.Register(r, ops.Op{Name: "admin.diagnostics", Method: "GET", Path: "/api/v1/admin/diagnostics", Auth: ops.Admin().NotFoundForOthers(),
		Desc: "A bundle for a bug report with nothing private in it: version and build, system, the configuration's shape (an allow-list of settings; no secret, address, path or name), migrations, row counts per table, queue and job states, disk use by area, and the last warnings and errors with paths reduced to route patterns and anything token-like removed. No manuscript text, comment, email or name is in it."},
		diagnostics)
	ops.Register(r, ops.Op{Name: "admin.diagnostics.download", Method: "GET", Path: "/api/v1/admin/diagnostics/download", Auth: ops.Admin().NotFoundForOthers(),
		Desc: "The same bundle as a zip (diagnostics.json and a README), the same bytes for the same state."},
		diagnosticsDownload)
}

// ---- users --------------------------------------------------------------------------------------------------

// UserOut is one person.
type UserOut struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Email        string `json:"email" desc:"Seen by instance administrators only."`
	Role         string `json:"role" enum:"admin,writer,reader"`
	LastSeenAt   string `json:"last_seen_at,omitempty"`
	CreatedAt    string `json:"created_at"`
	Disabled     bool   `json:"disabled"`
	DisabledAt   string `json:"disabled_at,omitempty"`
	Projects     int    `json:"projects" desc:"Projects they own."`
	MemberOf     int    `json:"member_of" desc:"Projects they are on without owning."`
	StorageBytes int64  `json:"storage_bytes" desc:"Attachment bytes they uploaded."`
	Sessions     int    `json:"sessions" desc:"Browser sessions open now."`
	Self         bool   `json:"self" desc:"This is you."`
}

// ListIn lists people.
type ListIn struct {
	Q      string `query:"q" desc:"A word of the name or email."`
	Status string `query:"status" enum:"all,active,disabled" desc:"Default all."`
	Limit  int    `query:"limit" desc:"Per page (default 100, at most 200)."`
	Offset int    `query:"offset"`
}

// ListOut is a page of people.
type ListOut struct {
	Items    []UserOut `json:"items"`
	Total    int       `json:"total"`
	Active   int       `json:"active"`
	Disabled int       `json:"disabled"`
	Admins   int       `json:"admins" desc:"Administrators who are switched on."`
	// RolesFromIdP says administration comes from the identity provider's groups, so set_role is refused.
	RolesFromIdP bool `json:"admin_from_idp"`
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	out := ListOut{Items: []UserOut{}, RolesFromIdP: svc(c).AdminFromIdP}
	q := c.DB().Read()
	where, args := `u.is_agent = 0 AND u.deleted_at IS NULL`, []any{}
	if w := strings.TrimSpace(in.Q); w != "" {
		if len(w) > 100 {
			return out, ops.Invalid("q", "Search for a shorter word.")
		}
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(w)) + "%"
		where += ` AND (lower(u.display_name) LIKE ? ESCAPE '\' OR lower(COALESCE(u.email,'')) LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	switch in.Status {
	case "", "all":
	case "active":
		where += ` AND u.disabled_at IS NULL`
	case "disabled":
		where += ` AND u.disabled_at IS NOT NULL`
	default:
		return out, ops.Invalid("status", "Use all, active or disabled.")
	}
	if err := q.QueryRowContext(c, `SELECT count(*) FROM users u WHERE `+where, args...).Scan(&out.Total); err != nil {
		return out, ops.Internal(err)
	}
	_ = q.QueryRowContext(c, `SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL AND disabled_at IS NULL`).Scan(&out.Active)
	_ = q.QueryRowContext(c, `SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL AND disabled_at IS NOT NULL`).Scan(&out.Disabled)
	_ = q.QueryRowContext(c, `SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL AND disabled_at IS NULL AND role = 'admin'`).Scan(&out.Admins)
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	if in.Offset < 0 {
		in.Offset = 0
	}
	items, err := selectUsers(c, where, args, limit, in.Offset)
	if err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}

// selectUsers reads people with their counts. where is built by the caller from fixed text.
func selectUsers(c *ops.Ctx, where string, args []any, limit, offset int) ([]UserOut, error) {
	items := []UserOut{}
	rows, err := c.DB().Read().QueryContext(c, `SELECT u.id, u.display_name, COALESCE(u.email,''), u.role, COALESCE(u.last_seen_at,''), u.created_at, COALESCE(u.disabled_at,''),
		(SELECT count(*) FROM projects p WHERE p.owner_id = u.id AND p.deleted_at IS NULL),
		(SELECT count(*) FROM project_members m JOIN projects p ON p.id = m.project_id WHERE m.user_id = u.id AND m.role <> 'owner' AND p.deleted_at IS NULL),
		COALESCE((SELECT bytes FROM file_usage WHERE scope = 'user:' || u.id), 0),
		(SELECT count(*) FROM sessions s WHERE s.user_id = u.id AND s.revoked_at IS NULL AND s.expires_at > ?)
		FROM users u WHERE `+where+` ORDER BY lower(u.display_name), u.id LIMIT ? OFFSET ?`, append(append([]any{db.Now()}, args...), limit, offset)...)
	if err != nil {
		return nil, ops.Internal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var u UserOut
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.LastSeenAt, &u.CreatedAt, &u.DisabledAt, &u.Projects, &u.MemberOf, &u.StorageBytes, &u.Sessions); err != nil {
			return nil, ops.Internal(err)
		}
		u.Disabled, u.Self = u.DisabledAt != "", u.ID == c.P.UserID
		items = append(items, u)
	}
	return items, rows.Err()
}

// UserRef names a person.
type UserRef struct {
	ID string `path:"id" json:"-" desc:"The person's id, from admin.user.list."`
}

// UserItem wraps one.
type UserItem struct {
	Item UserOut `json:"item"`
}

// lastAdmin reports whether userID is the only administrator who is switched on.
func lastAdmin(tx *sql.Tx, userID string) (bool, error) {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM users WHERE role = 'admin' AND is_agent = 0 AND deleted_at IS NULL AND disabled_at IS NULL AND id <> ?`, userID).Scan(&n)
	return n == 0, err
}

func target(tx *sql.Tx, id string) (role string, disabled bool, err error) {
	if !strings.HasPrefix(id, "usr_") {
		return "", false, ops.Invalid("id", "That is not a person's id. Use one from the people list.")
	}
	var dis sql.NullString
	err = tx.QueryRow(`SELECT role, disabled_at FROM users WHERE id = ? AND is_agent = 0 AND deleted_at IS NULL`, id).Scan(&role, &dis)
	if err == sql.ErrNoRows {
		return "", false, ops.NotFound("person")
	}
	return role, dis.Valid, err
}

func one(c *ops.Ctx, id string) (UserItem, error) {
	items, err := selectUsers(c, `u.id = ? AND u.is_agent = 0 AND u.deleted_at IS NULL`, []any{id}, 1, 0)
	if err != nil {
		return UserItem{}, err
	}
	if len(items) == 0 {
		return UserItem{}, ops.NotFound("person")
	}
	return UserItem{Item: items[0]}, nil
}

func disable(c *ops.Ctx, in UserRef) (UserItem, error) {
	if in.ID == c.P.UserID {
		return UserItem{}, ops.Conflict("You cannot switch yourself off. Ask another administrator.")
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		role, dis, err := target(tx, in.ID)
		if err != nil {
			return err
		}
		if dis {
			return nil
		}
		if role == ops.RoleAdmin {
			if last, err := lastAdmin(tx, in.ID); err != nil || last {
				if err != nil {
					return err
				}
				return ops.Conflict("This is the last administrator who is switched on. Make someone else an administrator first.")
			}
		}
		now := db.Now()
		if _, err := tx.ExecContext(c, `UPDATE users SET disabled_at = ?, disabled_by = ?, updated_at = ? WHERE id = ?`, now, c.P.UserID, now, in.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(c, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, in.ID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "admin.user.disable", TargetType: "user", TargetID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return UserItem{}, err
	}
	return one(c, in.ID)
}

func enable(c *ops.Ctx, in UserRef) (UserItem, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		_, dis, err := target(tx, in.ID)
		if err != nil {
			return err
		}
		if !dis {
			return nil
		}
		if _, err := tx.ExecContext(c, `UPDATE users SET disabled_at = NULL, disabled_by = NULL, updated_at = ? WHERE id = ?`, db.Now(), in.ID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "admin.user.enable", TargetType: "user", TargetID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return UserItem{}, err
	}
	return one(c, in.ID)
}

// RoleIn changes an instance role.
type RoleIn struct {
	ID   string `path:"id" json:"-" desc:"The person's id."`
	Role string `json:"role" enum:"admin,writer,reader"`
}

func setRole(c *ops.Ctx, in RoleIn) (UserItem, error) {
	switch in.Role {
	case ops.RoleAdmin, ops.RoleWriter, ops.RoleReader:
	default:
		return UserItem{}, ops.Invalid("role", "Use admin, writer or reader.")
	}
	if svc(c).AdminFromIdP {
		return UserItem{}, ops.Conflict("Administrators are set by the identity provider's groups on this server, so this cannot be changed here.")
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		role, dis, err := target(tx, in.ID)
		if err != nil {
			return err
		}
		if role == in.Role {
			return nil
		}
		if dis && in.Role == ops.RoleAdmin {
			return ops.Conflict("Switch this person on before making them an administrator.")
		}
		if role == ops.RoleAdmin && !dis {
			if last, err := lastAdmin(tx, in.ID); err != nil || last {
				if err != nil {
					return err
				}
				return ops.Conflict("This is the last administrator who is switched on. Make someone else an administrator first.")
			}
		}
		if _, err := tx.ExecContext(c, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, in.Role, db.Now(), in.ID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "admin.user.set_role", TargetType: "user", TargetID: in.ID, Detail: map[string]any{"from": role, "to": in.Role}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return UserItem{}, err
	}
	return one(c, in.ID)
}

// ---- quotas -----------------------------------------------------------------------------------------------------

// QuotaLine is one figure against one limit (0 is no limit).
type QuotaLine struct {
	Name  string `json:"name"`
	Used  int64  `json:"used"`
	Limit int64  `json:"limit"`
	Unit  string `json:"unit" enum:"bytes,count"`
}

// UserQuota is one person against the per-person limits.
type UserQuota struct {
	UserID       string `json:"user_id"`
	Name         string `json:"name"`
	Disabled     bool   `json:"disabled"`
	StorageBytes int64  `json:"storage_bytes"`
	StorageLimit int64  `json:"storage_limit"`
	Keys         int    `json:"keys" desc:"Live API keys."`
	KeyLimit     int    `json:"key_limit"`
	Projects     int    `json:"projects" desc:"Projects owned (there is no limit on this)."`
}

// QuotaOut is the instance and every person against the limits.
type QuotaOut struct {
	Instance []QuotaLine `json:"instance"`
	Users    []UserQuota `json:"users"`
	Editable bool        `json:"editable" desc:"Always false: the limits are the server's configuration and have no per-person override."`
	Note     string      `json:"note"`
}

// QuotaIn takes no input.
type QuotaIn struct{}

func quotas(c *ops.Ctx, _ QuotaIn) (QuotaOut, error) {
	s := svc(c)
	q := c.DB().Read()
	used := func(scope string) int64 {
		var b int64
		_ = q.QueryRowContext(c, `SELECT bytes FROM file_usage WHERE scope = ?`, scope).Scan(&b)
		return b
	}
	var users, projects, keys int64
	_ = q.QueryRowContext(c, `SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL`).Scan(&users)
	_ = q.QueryRowContext(c, `SELECT count(*) FROM projects WHERE deleted_at IS NULL`).Scan(&projects)
	_ = q.QueryRowContext(c, `SELECT count(*) FROM api_keys WHERE revoked_at IS NULL`).Scan(&keys)
	var biggest int64
	_ = q.QueryRowContext(c, `SELECT COALESCE(max(bytes),0) FROM file_usage WHERE scope LIKE 'owner:%'`).Scan(&biggest)
	out := QuotaOut{Users: []UserQuota{}, Note: "These limits come from the server's configuration (SCRIOBH_QUOTA_USER_MB, SCRIOBH_QUOTA_PROJECT_MB, SCRIOBH_QUOTA_INSTANCE_MB, SCRIOBH_UPLOAD_MAX_MB, SCRIOBH_MAX_KEYS_PER_USER) and apply to everyone alike; changing one means changing the configuration and restarting. There is no limit on the number of projects. AI budgets are set on the AI administration pages."}
	out.Instance = []QuotaLine{
		{Name: "Attachment storage, whole instance", Used: used("instance"), Limit: s.Limits.InstanceBytes, Unit: "bytes"},
		{Name: "Attachment storage, the fullest project or Compendium", Used: biggest, Limit: s.Limits.OwnerBytes, Unit: "bytes"},
		{Name: "Largest single file allowed", Used: 0, Limit: s.Limits.MaxBytes, Unit: "bytes"},
		{Name: "People", Used: users, Unit: "count"},
		{Name: "Projects", Used: projects, Unit: "count"},
		{Name: "Live API keys", Used: keys, Unit: "count"},
	}
	rows, err := q.QueryContext(c, `SELECT u.id, u.display_name, u.disabled_at IS NOT NULL,
		COALESCE((SELECT bytes FROM file_usage WHERE scope = 'user:' || u.id), 0),
		(SELECT count(*) FROM api_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL),
		(SELECT count(*) FROM projects p WHERE p.owner_id = u.id AND p.deleted_at IS NULL)
		FROM users u WHERE u.is_agent = 0 AND u.deleted_at IS NULL ORDER BY 4 DESC, lower(u.display_name), u.id LIMIT 500`)
	if err != nil {
		return out, ops.Internal(err)
	}
	defer rows.Close()
	for rows.Next() {
		u := UserQuota{StorageLimit: s.Limits.UserBytes, KeyLimit: s.MaxKeysPerUser}
		if err := rows.Scan(&u.UserID, &u.Name, &u.Disabled, &u.StorageBytes, &u.Keys, &u.Projects); err != nil {
			return out, ops.Internal(err)
		}
		out.Users = append(out.Users, u)
	}
	return out, rows.Err()
}

var _ = context.Background

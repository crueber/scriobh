// Package keys holds the API-key ops: create, list and revoke. A key acts as
// its owner, narrowed by a scope (read, write, admin) and optionally pinned to
// one project or one compendium. The secret is shown exactly once, in the
// response that creates the key; only its hash is stored.
//
// Key management needs the admin scope when it is done with a key, so an agent
// key that can write a manuscript cannot mint itself a stronger key. A person
// signed in with a browser session is not restricted by scope.
package keys

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/ops"
)

// Scopes, weakest first.
var Scopes = []string{ops.ScopeRead, ops.ScopeWrite, ops.ScopeAdmin}

// DefaultExpiryDays is how long a key lives when the caller does not say.
const DefaultExpiryDays = 90

// MaxExpiryDays bounds a key's life.
const MaxExpiryDays = 3650

// DefaultMaxKeysPerUser bounds how many live keys one person can hold when the
// server does not say otherwise (SCRIOBH_MAX_KEYS_PER_USER).
const DefaultMaxKeysPerUser = 25

// LimitsService is the name the server registers Limits under in ops.Env.
const LimitsService = "keys.limits"

// Limits are the server's settings for keys.
type Limits struct {
	MaxKeysPerUser int
}

// CompendiumAccess says whether a person may pin a key to a compendium: they
// must be able to read it. The compendium domain installs it (a server without
// the compendium ops refuses a compendium pin).
var CompendiumAccess func(ctx context.Context, d *db.DB, userID, compendiumID string) bool

// Key is a key as listed. The secret is never part of it.
type Key struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Label        string `json:"label"`
	Prefix       string `json:"prefix" desc:"The first characters of the key, to recognise it by."`
	Scope        string `json:"scope" enum:"read,write,admin"`
	ProjectID    string `json:"project_id,omitempty" desc:"The one project the key is restricted to."`
	CompendiumID string `json:"compendium_id,omitempty" desc:"The one compendium the key is restricted to."`
	ParentKeyID  string `json:"parent_key_id,omitempty" desc:"The key that created this one, when a key did. Revoking a key revokes the keys it made, and a child never outlives its parent."`
	Agent        bool   `json:"agent"`
	OwnerID      string `json:"owner_id"`
	CreatedAt    string `json:"created_at"`
	LastUsedAt   string `json:"last_used_at,omitempty"`
	ExpiresAt    string `json:"expires_at,omitempty" desc:"Empty when the key never expires."`
	RevokedAt    string `json:"revoked_at,omitempty"`
	Live         bool   `json:"live" desc:"True while the key can still sign in: not revoked, not expired."`
	// Protected lists the projects with a passphrase this key has reached or been refused at (F-252).
	Protected []ProtectedUse `json:"protected_projects,omitempty" desc:"Projects with a passphrase this key has reached while its owner accepts keys there, or been refused at."`
}

// ProtectedUse is what a key did to one project that has a passphrase.
type ProtectedUse struct {
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Uses      int    `json:"uses" desc:"Calls let through."`
	Refused   int    `json:"refused" desc:"Calls refused because the owner does not accept keys while the passphrase is set."`
	LastAt    string `json:"last_at"`
}

// CreateIn makes a key.
type CreateIn struct {
	Name          string `json:"name" desc:"What the key is for, up to 80 characters, for example the agent's name."`
	Label         string `json:"label,omitempty" desc:"A longer note, up to 200 characters."`
	Scope         string `json:"scope,omitempty" enum:"read,write,admin" desc:"read to look, write to build (default), admin only when needed. A key can never exceed the scope of whoever creates it."`
	ProjectID     string `json:"project_id,omitempty" ref:"project" desc:"Restrict the key to this one project and to that project's primary compendium: one key can then fill a book's Compendium and draft its chapters. A project key can create and attach that compendium with project.compendium.create."`
	CompendiumID  string `json:"compendium_id,omitempty" ref:"compendium" desc:"Restrict the key to this one compendium and no project. You must be able to read it, and the key can never do more there than you can."`
	ExpiresInDays *int   `json:"expires_in_days,omitempty" desc:"Days until the key stops working. Default 90; 0 for a key that never expires; at most 3650."`
	Agent         *bool  `json:"agent,omitempty" desc:"Mark the key as an agent's, so the audit log and the UI can tell it from a person. Default true."`
}

// Validate normalises the input.
func (in *CreateIn) Validate() error {
	in.Name, in.Label = strings.TrimSpace(in.Name), strings.TrimSpace(in.Label)
	switch {
	case in.Name == "":
		return ops.Invalid("name", "Give the key a name.")
	case utf8.RuneCountInString(in.Name) > 80:
		return ops.Invalid("name", "The name is limited to 80 characters.")
	case utf8.RuneCountInString(in.Label) > 200:
		return ops.Invalid("label", "The label is limited to 200 characters.")
	}
	if in.Scope == "" {
		in.Scope = ops.ScopeWrite
	}
	if !contains(Scopes, in.Scope) {
		return ops.Invalid("scope", "Scope must be read, write or admin.")
	}
	if in.ProjectID != "" && in.CompendiumID != "" {
		return ops.Invalid("compendium_id", "A key can be restricted to a project or a compendium, not both.")
	}
	if d := in.ExpiresInDays; d != nil && (*d < 0 || *d > MaxExpiryDays) {
		return ops.Invalid("expires_in_days", "Use 0 for no expiry, or up to 3650 days.")
	}
	return nil
}

// CreateOut carries the one and only copy of the secret.
type CreateOut struct {
	Item   Key    `json:"item"`
	Secret string `json:"secret" desc:"The key itself. Shown once; store it now. It cannot be recovered."`
}

// ListIn lists keys.
type ListIn struct {
	All bool `query:"all" desc:"Administrators: list everyone's keys, not just your own."`
}

// ListOut lists keys, newest first.
type ListOut struct {
	Items []Key `json:"items"`
}

// RevokeIn names a key.
type RevokeIn struct {
	ID string `path:"id" json:"-" ref:"key" desc:"Key id, from key.list."`
}

// Validate refuses anything that is not shaped like a key id before it reaches
// the database.
func (in *RevokeIn) Validate() error {
	if !strings.HasPrefix(in.ID, "key_") || len(in.ID) > 64 {
		return ops.Invalid("id", "That is not a key id.")
	}
	return nil
}

// RevokeOut reports a revoked key.
type RevokeOut struct {
	ID          string `json:"id"`
	Revoked     bool   `json:"revoked"`
	Descendants int    `json:"descendants" desc:"How many keys this key had created that were revoked with it."`
}

// Register adds the key ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "key.create", Method: "POST", Path: "/api/v1/keys", Status: 201, Scope: ops.ScopeAdmin, UI: "POST /ui/keys",
		Desc: "Create an API key for yourself, with a scope and an optional restriction to one project (and that project's primary compendium) or to one compendium. The secret is in this response and nowhere else.",
		Auth: ops.Writer(), NoDryRun: true, SecretOutput: true}, create)
	ops.Register(r, ops.Op{Name: "key.list", Method: "GET", Path: "/api/v1/keys", Scope: ops.ScopeAdmin,
		Desc: "List your API keys, newest first, with their scope, restriction, last use and expiry. Never includes a secret. Administrators may list everyone's.",
		Auth: ops.Writer()}, list)
	ops.Register(r, ops.Op{Name: "key.revoke", Method: "POST", Path: "/api/v1/keys/{id}/revoke", Scope: ops.ScopeAdmin, UI: "POST /ui/keys/{id}/revoke",
		Desc: "Revoke one of your API keys now, and every key it created. Revoking twice is fine. Administrators may revoke anyone's. Another person's key is not found.",
		Auth: ops.Writer()}, revoke)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func rank(scope string) int {
	for i, s := range Scopes {
		if s == scope {
			return i + 1
		}
	}
	return 0
}

func create(c *ops.Ctx, in CreateIn) (CreateOut, error) {
	// A key never exceeds whoever makes it: not their scope, and not an admin
	// scope unless they are an admin.
	most := ops.ScopeWrite
	if c.P.Role == ops.RoleAdmin {
		most = ops.ScopeAdmin
	}
	if c.P.Scope != "" && rank(c.P.Scope) < rank(most) {
		most = c.P.Scope
	}
	if rank(in.Scope) > rank(most) {
		return CreateOut{}, ops.Forbidden("You cannot create a key with the " + in.Scope + " scope; the most you can grant is " + most + ".")
	}
	// A pinned key cannot make a key with a wider reach than its own. (A pinned
	// key is kept away from key management by the registry; this holds if that
	// ever changes.)
	if c.P.ProjectID != "" && in.ProjectID != c.P.ProjectID || c.P.CompendiumID != "" && in.CompendiumID != c.P.CompendiumID {
		return CreateOut{}, ops.Forbidden("A key restricted to one project or compendium can only make keys with the same restriction.")
	}
	if in.ProjectID != "" {
		member := c.Env.ProjectMember
		if member == nil {
			member = c.Env.ProjectRole
		}
		if member == nil {
			return CreateOut{}, ops.NotFound("project")
		}
		if _, ok := member(c, c.P.UserID, in.ProjectID); !ok {
			return CreateOut{}, ops.NotFound("project") // the same answer as for a project that does not exist
		}
	}
	// A key is its own credential, so one cannot be made by a person who has not unlocked what it would reach (F-252).
	var approve []string
	if in.CompendiumID == "" {
		var err error
		if approve, err = projectgate.CheckMint(c, in.ProjectID); err != nil {
			return CreateOut{}, err
		}
	}
	if in.CompendiumID != "" {
		if CompendiumAccess == nil {
			return CreateOut{}, ops.Invalid("compendium_id", "Keys restricted to a compendium are not available on this server yet.")
		}
		if !CompendiumAccess(c, c.DB(), c.P.UserID, in.CompendiumID) {
			return CreateOut{}, ops.NotFound("compendium")
		}
	}
	secret, display, hash, err := auth.NewKey()
	if err != nil {
		return CreateOut{}, err
	}
	days := DefaultExpiryDays
	if in.ExpiresInDays != nil {
		days = *in.ExpiresInDays
	}
	agent := true
	if in.Agent != nil {
		agent = *in.Agent
	}
	now := db.NowTime()
	k := Key{ID: db.NewID("key"), Name: in.Name, Label: in.Label, Prefix: display, Scope: in.Scope, ProjectID: in.ProjectID,
		CompendiumID: in.CompendiumID, Agent: agent, OwnerID: c.P.UserID, CreatedAt: db.Format(now), Live: true}
	if days > 0 {
		k.ExpiresAt = db.Format(now.Add(time.Duration(days) * 24 * time.Hour))
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		// A key made by a key never outlives it, and is revoked with it.
		if c.P.KeyID != "" {
			var parentExpiry sql.NullString
			if err := tx.QueryRow(`SELECT expires_at FROM api_keys WHERE id = ? AND revoked_at IS NULL`, c.P.KeyID).Scan(&parentExpiry); err != nil {
				if err == sql.ErrNoRows {
					return ops.Unauthenticated()
				}
				return err
			}
			if parentExpiry.Valid && (k.ExpiresAt == "" || k.ExpiresAt > parentExpiry.String) {
				k.ExpiresAt = parentExpiry.String
			}
			k.ParentKeyID = c.P.KeyID
		}
		var expires any
		if k.ExpiresAt != "" {
			expires = k.ExpiresAt
		}
		max := DefaultMaxKeysPerUser
		if l, ok := ops.Service[Limits](c.Env, LimitsService); ok && l.MaxKeysPerUser > 0 {
			max = l.MaxKeysPerUser
		}
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM api_keys WHERE user_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`, c.P.UserID, db.Now()).Scan(&n); err != nil {
			return err
		}
		if n >= max {
			return ops.Invalid("name", "You already hold "+strconv.Itoa(max)+" live keys. Revoke some first.")
		}
		if _, err := tx.Exec(`INSERT INTO api_keys(id, user_id, name, label, prefix, secret_hash, scope, project_id, compendium_id, is_agent, created_at, expires_at, parent_key_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, k.ID, c.P.UserID, k.Name, k.Label, k.Prefix, hash, k.Scope, nullIfEmpty(k.ProjectID),
			nullIfEmpty(k.CompendiumID), b2i(agent), k.CreatedAt, expires, nullIfEmpty(k.ParentKeyID)); err != nil {
			return err
		}
		// The projects it was made with unlocked accept it; no other protected project does until its owner approves it by id.
		if err := projectgate.ApproveMinted(tx, approve, k.ID, c.P.UserID, c.P.SessionID); err != nil {
			return err
		}
		// The audit line names the key, never the secret.
		c.Audit(ops.AuditEntry{Action: "apikey.create", TargetType: "api_key", TargetID: k.ID,
			Detail: map[string]any{"scope": k.Scope, "agent": agent, "project_id": k.ProjectID, "compendium_id": k.CompendiumID, "via": c.P.Via, "parent_key_id": k.ParentKeyID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return CreateOut{}, err
	}
	return CreateOut{Item: k, Secret: secret}, nil
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	q := `SELECT id, user_id, name, label, prefix, scope, COALESCE(project_id,''), COALESCE(compendium_id,''), is_agent,
		created_at, COALESCE(last_used_at,''), COALESCE(expires_at,''), COALESCE(revoked_at,''), COALESCE(parent_key_id,'') FROM api_keys`
	var args []any
	if !(in.All && c.P.Role == ops.RoleAdmin) {
		q += ` WHERE user_id = ?`
		args = append(args, c.P.UserID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 500`
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	now := db.Now()
	out := ListOut{Items: []Key{}}
	for rows.Next() {
		var k Key
		var agent int
		if err := rows.Scan(&k.ID, &k.OwnerID, &k.Name, &k.Label, &k.Prefix, &k.Scope, &k.ProjectID, &k.CompendiumID, &agent,
			&k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt, &k.ParentKeyID); err != nil {
			return ListOut{}, err
		}
		k.Agent = agent == 1
		k.Live = k.RevokedAt == "" && (k.ExpiresAt == "" || k.ExpiresAt > now)
		out.Items = append(out.Items, k)
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	if err := rows.Close(); err != nil {
		return ListOut{}, err
	}
	return out, addProtectedUse(c, out.Items)
}

func revoke(c *ops.Ctx, in RevokeIn) (RevokeOut, error) {
	var children int
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var owner string
		var revoked sql.NullString
		if err := tx.QueryRow(`SELECT user_id, revoked_at FROM api_keys WHERE id = ?`, in.ID).Scan(&owner, &revoked); err != nil {
			if err == sql.ErrNoRows {
				return ops.NotFound("key")
			}
			return err
		}
		if owner != c.P.UserID && c.P.Role != ops.RoleAdmin {
			return ops.NotFound("key")
		}
		// The key and everything it made, however deep: a key that is revoked
		// must not leave behind keys it minted.
		now := db.Now()
		res, err := tx.Exec(`WITH RECURSIVE tree(id) AS (
				SELECT id FROM api_keys WHERE id = ?1
				UNION
				SELECT k.id FROM api_keys k JOIN tree t ON k.parent_key_id = t.id)
			UPDATE api_keys SET revoked_at = ?2 WHERE id IN (SELECT id FROM tree) AND revoked_at IS NULL`, in.ID, now)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			children = int(n)
			if !revoked.Valid {
				children-- // the key itself is one of the rows
			}
			c.Audit(ops.AuditEntry{Action: "apikey.revoke", TargetType: "api_key", TargetID: in.ID,
				Detail: map[string]any{"owner_id": owner, "via": c.P.Via, "descendants": children}})
			return c.FlushTo(tx)
		}
		return nil
	})
	if err != nil {
		return RevokeOut{}, err
	}
	return RevokeOut{ID: in.ID, Revoked: true, Descendants: children}, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// addProtectedUse fills Protected on the listed keys from the gate's record of what each did to a project with a
// passphrase. The title is shown only to a person who is on that project, as everywhere else.
func addProtectedUse(c *ops.Ctx, keys []Key) error {
	if len(keys) == 0 {
		return nil
	}
	if g, ok := ops.Service[*projectgate.Service](c.Env, projectgate.ServiceName); ok {
		g.Flush(c) // what is still counted in memory
	}
	by := map[string]int{}
	args := make([]any, 0, len(keys)+1)
	args = append(args, c.P.UserID)
	ph := make([]string, len(keys))
	for i, k := range keys {
		by[k.ID], ph[i] = i, "?"
		args = append(args, k.ID)
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT ku.key_id, ku.project_id, p.title, ku.uses, ku.refused, ku.last_at
		FROM project_gate_key_use ku JOIN projects p ON p.id = ku.project_id
		JOIN project_members m ON m.project_id = ku.project_id AND m.user_id = ?
		WHERE ku.key_id IN (`+strings.Join(ph, ",")+`) ORDER BY ku.last_at DESC, ku.project_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var u ProtectedUse
		if err := rows.Scan(&key, &u.ProjectID, &u.Title, &u.Uses, &u.Refused, &u.LastAt); err != nil {
			return err
		}
		keys[by[key]].Protected = append(keys[by[key]].Protected, u)
	}
	return rows.Err()
}

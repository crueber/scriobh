package share

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// The owner's side: making, listing, changing and revoking links. Every one is for
// the project's owner in a browser session (an API key, whatever its scope, is
// refused: a link is a way into the manuscript for a stranger, and a leaked agent
// key must not be able to make one). The token is in the answer of share.create
// and nowhere else, ever.

// meterOf is the use counter registered in the environment, nil without one (a
// schema-only build, a dry run).
func meterOf(env *ops.Env) *Meter {
	m, _ := ops.Service[*Meter](env, MeterService)
	return m
}

// Register adds the share ops.
func Register(r *ops.Registry) {
	owner := ops.RequireProject(ops.ProjectOwner).ForSessions()
	const base = "/api/v1/projects/{id}/shares"
	ops.Register(r, ops.Op{SecretInput: true, Name: "share.create", Method: "POST", Path: base, Status: 201, Auth: owner, NoDryRun: true, SecretOutput: true,
		Desc: "Make a share link: read-only or comment-only access for a person with no account. The answer carries the token ONCE (path is /s/<token>; only its SHA-256 is kept, so a lost link is revoked and made again). " +
			"A link shows the manuscript's reading text only (never notes, research, the stash, settings, members, history, other links or anything AI), all of it or the documents you name with their descendants. Private notes are never in it; ghosted text is left out unless ghosts is keep. " +
			"Options: a passphrase (8 to 200 characters; a gate, not encryption), an expiry (RFC 3339, in the future), and allow_download (the shown text as one Markdown file; default off). A comment link lets guests write comments under a name they give; they see only their own link's threads. Owner only, browser session only."},
		func(c *ops.Ctx, in CreateIn) (CreateOut, error) { return create(c, in) })
	ops.Register(r, ops.Op{Name: "share.list", Method: "GET", Path: base, Auth: owner,
		Desc: "The project's share links, newest first, with their state (active, expired or revoked) and counts. Never a token. Owner only, browser session only."},
		func(c *ops.Ctx, in ListIn) (ListOut, error) { return list(c, in) })
	ops.Register(r, ops.Op{SecretInput: true, Name: "share.update", Method: "PATCH", Path: base + "/{share_id}", Auth: owner,
		Desc: "Change a link: its label, scope, documents, passphrase (a new one, or an empty string to remove it), expiry (an empty string clears it), download flag or ghost handling. Changing the passphrase ends every unlock already given. A revoked link cannot be changed. Owner only, browser session only."},
		func(c *ops.Ctx, in UpdateIn) (ItemOut, error) { return update(c, in) })
	ops.Register(r, ops.Op{Name: "share.revoke", Method: "POST", Path: base + "/{share_id}/revoke", Auth: owner,
		Desc: "Revoke a link: from then on its token is answered exactly as a token that never existed. Its guests' comments stay. Revoking twice changes nothing. Owner only, browser session only."},
		func(c *ops.Ctx, in RefIn) (ItemOut, error) { return revoke(c, in) })
	ops.Register(r, ops.Op{Name: "share.usage", Method: "GET", Path: base + "/{share_id}/usage", Auth: owner,
		Desc: "How a link has been used: views, when last, comments written through it and how many of those threads are open, wrong passphrases tried and whether it is locked now. Counts of views are written in batches, so they may trail by up to fifteen seconds. Owner only, browser session only."},
		func(c *ops.Ctx, in RefIn) (UsageOut, error) { return usage(c, in) })
}

// Out is a link as the owner sees it. It never has the token or the passphrase.
type Out struct {
	ID            string   `json:"id"`
	Label         string   `json:"label"`
	Scope         string   `json:"scope" enum:"read,comment"`
	Status        string   `json:"status" enum:"active,expired,revoked"`
	Whole         bool     `json:"whole" desc:"The link shows the whole manuscript. When false it shows only document_ids (each with its descendants); if they are all gone it shows nothing."`
	DocumentIDs   []string `json:"document_ids" desc:"The documents named (each with its descendants), when whole is false."`
	HasPassphrase bool     `json:"has_passphrase"`
	AllowDownload bool     `json:"allow_download"`
	Ghosts        string   `json:"ghosts" enum:"omit,keep"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Views         int      `json:"views" desc:"How many times the contents were opened (counted in batches: it may trail by up to fifteen seconds)."`
	CommentCount  int      `json:"comment_count"`
	CreatedAt     string   `json:"created_at"`
	CreatedBy     string   `json:"created_by,omitempty" desc:"Name of who made it."`
	RevokedAt     string   `json:"revoked_at,omitempty"`
	LastUsedAt    string   `json:"last_used_at,omitempty"`
}

// ItemOut wraps one link.
type ItemOut struct {
	Item Out `json:"item"`
}

// CreateIn makes a link.
type CreateIn struct {
	ProjectID     string   `path:"id" json:"-" desc:"Project id."`
	Label         string   `json:"label,omitempty" desc:"Your name for it (up to 100 characters), such as the reader it is for."`
	Scope         string   `json:"scope,omitempty" enum:"read,comment" desc:"Default read."`
	DocumentIDs   []string `json:"document_ids,omitempty" desc:"Show only these documents (and their descendants). Empty: the whole manuscript. Notes cannot be shared."`
	Passphrase    string   `json:"passphrase,omitempty" desc:"A passphrase the reader must give first (8 to 200 characters)."`
	ExpiresAt     string   `json:"expires_at,omitempty" desc:"RFC 3339 time, in the future."`
	AllowDownload bool     `json:"allow_download,omitempty" desc:"Let the reader download the shown text as one Markdown file."`
	Ghosts        string   `json:"ghosts,omitempty" enum:"omit,keep" desc:"omit (default): ghosted text is left out. keep: it is shown as ordinary text."`
}

// ProjectRef lets the registry authorize by membership.
func (in CreateIn) ProjectRef() string { return in.ProjectID }

// CreateOut is the new link and its token, once.
type CreateOut struct {
	Item  Out    `json:"item"`
	Token string `json:"token" desc:"The secret. It is shown only here."`
	Path  string `json:"path" desc:"/s/<token>: add the address of this installation to make the link to send."`
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func cleanLabel(s string) (string, error) {
	s = oneLine(s)
	if utf8.RuneCountInString(s) > MaxLabel {
		return "", ops.Invalid("label", "A label is limited to 100 characters.")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", ops.Invalid("label", "A label cannot hold control characters.")
		}
	}
	return s, nil
}

func parseExpiry(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", ops.Invalid("expires_at", "Use an RFC 3339 time such as 2026-12-31T00:00:00Z.")
	}
	if !t.After(db.NowTime()) {
		return "", ops.Invalid("expires_at", "That time has passed.")
	}
	return db.Format(t), nil
}

func checkPass(p string) error {
	n := utf8.RuneCountInString(p)
	if n < MinPassphrase || n > MaxPassphrase {
		return ops.Invalid("passphrase", "A passphrase is 8 to 200 characters.")
	}
	return nil
}

func checkScope(s string) error {
	if s != ScopeRead && s != ScopeComment {
		return ops.Invalid("scope", "A link is read or comment.")
	}
	return nil
}

func checkGhosts(s string) error {
	if s != "omit" && s != "keep" {
		return ops.Invalid("ghosts", "Use omit or keep.")
	}
	return nil
}

// checkDocs verifies the documents a link names are manuscript documents of the project.
func checkDocs(ctx context.Context, tx *sql.Tx, projectID string, ids []string) ([]string, error) {
	if len(ids) > 200 {
		return nil, ops.Invalid("document_ids", "Name at most 200 documents.")
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var kind string
		err := tx.QueryRowContext(ctx, `SELECT kind FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, projectID).Scan(&kind)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ops.Invalid("document_ids", "A document named is not in this project.")
		}
		if err != nil {
			return nil, err
		}
		if kind == "note" || kind == "research" {
			return nil, ops.Invalid("document_ids", "Notes cannot be shared.")
		}
		out = append(out, id)
	}
	return out, nil
}

func create(c *ops.Ctx, in CreateIn) (CreateOut, error) {
	scope, ghosts := in.Scope, in.Ghosts
	if scope == "" {
		scope = ScopeRead
	}
	if ghosts == "" {
		ghosts = "omit"
	}
	if err := checkScope(scope); err != nil {
		return CreateOut{}, err
	}
	if err := checkGhosts(ghosts); err != nil {
		return CreateOut{}, err
	}
	label, err := cleanLabel(in.Label)
	if err != nil {
		return CreateOut{}, err
	}
	expires, err := parseExpiry(in.ExpiresAt)
	if err != nil {
		return CreateOut{}, err
	}
	var passHash any
	if in.Passphrase != "" {
		if err := checkPass(in.Passphrase); err != nil {
			return CreateOut{}, err
		}
		h, err := HashPassphrase(in.Passphrase)
		if errors.Is(err, ErrBusy) {
			return CreateOut{}, ops.RateLimited(2)
		}
		if err != nil {
			return CreateOut{}, err
		}
		passHash = h
	}
	id, token, hash := NewToken()
	allow := 0
	if in.AllowDownload {
		allow = 1
	}
	var item Out
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(c, `SELECT count(*) FROM share_links WHERE project_id = ? AND revoked_at IS NULL`, in.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxLinksPerProject {
			return ops.Conflict("A project can have 50 live share links; revoke some first.")
		}
		docs, err := checkDocs(c, tx, in.ProjectID, in.DocumentIDs)
		if err != nil {
			return err
		}
		now := db.Now()
		var exp any
		if expires != "" {
			exp = expires
		}
		if _, err := tx.ExecContext(c, `INSERT INTO share_links(id, project_id, label, scope, whole, token_hash, passphrase_hash, allow_download, ghosts, expires_at, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, in.ProjectID, label, scope, boolInt(len(docs) == 0), hash, passHash, allow, ghosts, exp, c.P.UserID, now, now); err != nil {
			return err
		}
		for _, d := range docs {
			if _, err := tx.ExecContext(c, `INSERT INTO share_link_docs(share_id, document_id) VALUES (?,?)`, id, d); err != nil {
				return err
			}
		}
		c.Audit(ops.AuditEntry{Action: "share.create", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"share_id": id, "scope": scope}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		item, err = load(c, tx, in.ProjectID, id, meterOf(c.Env))
		return err
	})
	if err != nil {
		return CreateOut{}, err
	}
	return CreateOut{Item: item, Token: token, Path: "/s/" + token}, nil
}

// ---- reading ----------------------------------------------------------------------------------------

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const outCols = `sl.id, sl.label, sl.scope, sl.whole, sl.passphrase_hash IS NOT NULL, sl.allow_download, sl.ghosts, COALESCE(sl.expires_at,''), sl.views, sl.comment_count,
	sl.created_at, COALESCE(NULLIF(u.display_name,''), CASE WHEN u.id IS NULL THEN '' ELSE 'Member' END), COALESCE(sl.revoked_at,''), COALESCE(sl.last_used_at,'')`

func scanOut(s interface{ Scan(...any) error }) (Out, error) {
	var o Out
	var pass, allow bool
	err := s.Scan(&o.ID, &o.Label, &o.Scope, &o.Whole, &pass, &allow, &o.Ghosts, &o.ExpiresAt, &o.Views, &o.CommentCount, &o.CreatedAt, &o.CreatedBy, &o.RevokedAt, &o.LastUsedAt)
	o.HasPassphrase, o.AllowDownload = pass, allow
	return o, err
}

func finish(o *Out, m *Meter) {
	o.Views += m.Pending(o.ID)
	if m != nil {
		m.mu.Lock()
		if at := m.used[o.ID]; at > o.LastUsedAt {
			o.LastUsedAt = at
		}
		m.mu.Unlock()
	}
	o.Status = "active"
	switch {
	case o.RevokedAt != "":
		o.Status = "revoked"
	case o.ExpiresAt != "" && o.ExpiresAt <= db.Now():
		o.Status = "expired"
	}
	if o.DocumentIDs == nil {
		o.DocumentIDs = []string{}
	}
}

func docIDs(ctx context.Context, q querier, id string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT document_id FROM share_link_docs WHERE share_id = ? ORDER BY document_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func load(ctx context.Context, q querier, projectID, id string, m *Meter) (Out, error) {
	o, err := scanOut(q.QueryRowContext(ctx, `SELECT `+outCols+` FROM share_links sl LEFT JOIN users u ON u.id = sl.created_by WHERE sl.id = ? AND sl.project_id = ?`, id, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return o, ops.NotFound("share link")
	}
	if err != nil {
		return o, err
	}
	if o.DocumentIDs, err = docIDs(ctx, q, id); err != nil {
		return o, err
	}
	finish(&o, m)
	return o, nil
}

// ListIn lists links.
type ListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ListIn) ProjectRef() string { return in.ProjectID }

// ListOut is the links.
type ListOut struct {
	Items []Out `json:"items"`
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	m := meterOf(c.Env)
	rows, err := c.DB().Read().QueryContext(c, `SELECT `+outCols+` FROM share_links sl LEFT JOIN users u ON u.id = sl.created_by WHERE sl.project_id = ? ORDER BY sl.created_at DESC, sl.id DESC`, in.ProjectID)
	if err != nil {
		return ListOut{}, err
	}
	out := ListOut{Items: []Out{}}
	for rows.Next() {
		o, err := scanOut(rows)
		if err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, o)
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	for i := range out.Items {
		if out.Items[i].DocumentIDs, err = docIDs(c, c.DB().Read(), out.Items[i].ID); err != nil {
			return out, err
		}
		finish(&out.Items[i], m)
	}
	return out, nil
}

// RefIn names a link.
type RefIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	ShareID   string `path:"share_id" json:"-" desc:"Share link id."`
}

// ProjectRef lets the registry authorize by membership.
func (in RefIn) ProjectRef() string { return in.ProjectID }

// UpdateIn changes a link.
type UpdateIn struct {
	ProjectID     string    `path:"id" json:"-" desc:"Project id."`
	ShareID       string    `path:"share_id" json:"-" desc:"Share link id."`
	Label         *string   `json:"label,omitempty"`
	Scope         *string   `json:"scope,omitempty" enum:"read,comment"`
	DocumentIDs   *[]string `json:"document_ids,omitempty" desc:"Replace the documents shown; an empty list is the whole manuscript."`
	Passphrase    *string   `json:"passphrase,omitempty" desc:"A new passphrase; an empty string removes it."`
	ExpiresAt     *string   `json:"expires_at,omitempty" desc:"A new expiry; an empty string clears it."`
	AllowDownload *bool     `json:"allow_download,omitempty"`
	Ghosts        *string   `json:"ghosts,omitempty" enum:"omit,keep"`
}

// ProjectRef lets the registry authorize by membership.
func (in UpdateIn) ProjectRef() string { return in.ProjectID }

func update(c *ops.Ctx, in UpdateIn) (ItemOut, error) {
	var passHash any
	setPass, clearPass := false, false
	if in.Passphrase != nil {
		if *in.Passphrase == "" {
			clearPass = true
		} else {
			if err := checkPass(*in.Passphrase); err != nil {
				return ItemOut{}, err
			}
			h, err := HashPassphrase(*in.Passphrase)
			if errors.Is(err, ErrBusy) {
				return ItemOut{}, ops.RateLimited(2)
			}
			if err != nil {
				return ItemOut{}, err
			}
			passHash, setPass = h, true
		}
	}
	var out Out
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var revoked sql.NullString
		if err := tx.QueryRowContext(c, `SELECT revoked_at FROM share_links WHERE id = ? AND project_id = ?`, in.ShareID, in.ProjectID).Scan(&revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("share link")
			}
			return err
		}
		if revoked.Valid {
			return ops.Conflict("This link was revoked. Make a new one.")
		}
		set := []string{"updated_at = ?"}
		args := []any{db.Now()}
		add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
		if in.Label != nil {
			l, err := cleanLabel(*in.Label)
			if err != nil {
				return err
			}
			add("label", l)
		}
		if in.Scope != nil {
			if err := checkScope(*in.Scope); err != nil {
				return err
			}
			add("scope", *in.Scope)
		}
		if in.Ghosts != nil {
			if err := checkGhosts(*in.Ghosts); err != nil {
				return err
			}
			add("ghosts", *in.Ghosts)
		}
		if in.ExpiresAt != nil {
			e, err := parseExpiry(*in.ExpiresAt)
			if err != nil {
				return err
			}
			if e == "" {
				add("expires_at", nil)
			} else {
				add("expires_at", e)
			}
		}
		if in.AllowDownload != nil {
			v := 0
			if *in.AllowDownload {
				v = 1
			}
			add("allow_download", v)
		}
		switch {
		case setPass:
			add("passphrase_hash", passHash)
		case clearPass:
			add("passphrase_hash", nil)
		}
		if setPass || clearPass { // a new passphrase starts every client's count again
			if _, err := tx.ExecContext(c, `DELETE FROM share_unlock_failures WHERE share_id = ?`, in.ShareID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(c, `UPDATE share_links SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, in.ShareID)...); err != nil {
			return err
		}
		if in.DocumentIDs != nil {
			docs, err := checkDocs(c, tx, in.ProjectID, *in.DocumentIDs)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(c, `DELETE FROM share_link_docs WHERE share_id = ?`, in.ShareID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(c, `UPDATE share_links SET whole = ? WHERE id = ?`, boolInt(len(docs) == 0), in.ShareID); err != nil {
				return err
			}
			for _, d := range docs {
				if _, err := tx.ExecContext(c, `INSERT INTO share_link_docs(share_id, document_id) VALUES (?,?)`, in.ShareID, d); err != nil {
					return err
				}
			}
		}
		c.Audit(ops.AuditEntry{Action: "share.update", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"share_id": in.ShareID}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		var err error
		out, err = load(c, tx, in.ProjectID, in.ShareID, meterOf(c.Env))
		return err
	})
	return ItemOut{Item: out}, err
}

func revoke(c *ops.Ctx, in RefIn) (ItemOut, error) {
	var out Out
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var revoked sql.NullString
		if err := tx.QueryRowContext(c, `SELECT revoked_at FROM share_links WHERE id = ? AND project_id = ?`, in.ShareID, in.ProjectID).Scan(&revoked); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("share link")
			}
			return err
		}
		if !revoked.Valid {
			now := db.Now()
			if _, err := tx.ExecContext(c, `UPDATE share_links SET revoked_at = ?, updated_at = ? WHERE id = ?`, now, now, in.ShareID); err != nil {
				return err
			}
			c.Audit(ops.AuditEntry{Action: "share.revoke", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"share_id": in.ShareID}})
			if err := c.FlushTo(tx); err != nil {
				return err
			}
		}
		var err error
		out, err = load(c, tx, in.ProjectID, in.ShareID, meterOf(c.Env))
		return err
	})
	return ItemOut{Item: out}, err
}

// UsageOut is how a link has been used.
type UsageOut struct {
	Item          Out    `json:"item"`
	Threads       int    `json:"threads" desc:"Threads started through the link."`
	OpenThreads   int    `json:"open_threads"`
	Failed        int    `json:"failed_unlocks" desc:"Wrong passphrases tried through this link, ever."`
	LastFailedAt  string `json:"last_failed_at,omitempty" desc:"When a wrong passphrase was last tried by a client still counted."`
	Locked        bool   `json:"locked" desc:"At least one client address is waiting out its wrong tries."`
	LockedBuckets int    `json:"locked_clients" desc:"How many client addresses (an IPv4 address or an IPv6 /64) are waiting. Others are not affected."`
}

func usage(c *ops.Ctx, in RefIn) (UsageOut, error) {
	m := meterOf(c.Env)
	if !c.DryRun() {
		_ = m.Flush(c)
	}
	var out UsageOut
	item, err := load(c, c.DB().Read(), in.ProjectID, in.ShareID, m)
	if err != nil {
		return out, err
	}
	out.Item = item
	if err := c.DB().Read().QueryRowContext(c, `SELECT failed_unlocks FROM share_links WHERE id = ?`, in.ShareID).Scan(&out.Failed); err != nil {
		return out, err
	}
	if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM share_unlock_failures WHERE share_id = ? AND locked_until > ?`, in.ShareID, db.Now()).Scan(&out.LockedBuckets); err != nil {
		return out, err
	}
	out.Locked = out.LockedBuckets > 0
	_ = c.DB().Read().QueryRowContext(c, `SELECT COALESCE(max(last_at), '') FROM share_unlock_failures WHERE share_id = ?`, in.ShareID).Scan(&out.LastFailedAt)
	err = c.DB().Read().QueryRowContext(c, `SELECT count(*), COALESCE(sum(status = 'open'), 0) FROM comments WHERE share_id = ? AND parent_id IS NULL AND deleted_at IS NULL`, in.ShareID).Scan(&out.Threads, &out.OpenThreads)
	return out, err
}

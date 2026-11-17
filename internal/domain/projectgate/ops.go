package projectgate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/passkit"
)

// Notice is what the screen says about what the passphrase is and is not. It is the words the UI shows (the
// same sentence is project.gate.notice in the locale file) and every status answer carries it, so a client
// that is not the browser says it too.
const Notice = "This passphrase locks the project on this screen: it keeps a shared screen, a borrowed laptop or someone using your session out. " +
	"It is not encryption. The server's administrator, the database file, backups, the Markdown folder, Git pushes and exports still hold your text as it is."

// Register adds the gate's ops. svc may be a gate over a nil database when only the schema is needed.
func Register(r *ops.Registry, svc *Service) {
	ops.Register(r, ops.Op{
		Name: "project.passphrase.get", Method: "GET", Path: "/api/v1/projects/{id}/passphrase",
		Desc: "Whether a project has a passphrase and whether this session has unlocked it. Any member, locked or not: it says nothing of the text. " +
			"The owner also sees the idle limit, whether API keys are accepted, how many wrong tries were made and what each key has done to the project. " +
			"The passphrase is a lock for the screen, not encryption.",
		Auth: ops.RequireProject(ops.ProjectReader), Scope: ops.ScopeRead,
	}, svc.status)
	ops.Register(r, ops.Op{
		Name: "project.passphrase.set", Method: "PUT", Path: "/api/v1/projects/{id}/passphrase",
		Desc: "Set, change or adjust the project's passphrase. Project owner, browser session only (a key cannot). " +
			"With no passphrase yet, passphrase is required (8 to 200 characters). With one, current must be given: whoever has the session but not the passphrase cannot change it. " +
			"A new passphrase relocks every session except this one; leaving passphrase out changes only the options (idle_minutes, allow_keys). " +
			"It is a lock for the screen, not encryption: the administrator, the database, backups, the mirror, Git pushes and exports still hold the text.",
		Auth: ops.RequireProject(ops.ProjectOwner).ForSessions(), SecretInput: true,
	}, svc.set)
	ops.Register(r, ops.Op{
		Name: "project.passphrase.clear", Method: "DELETE", Path: "/api/v1/projects/{id}/passphrase",
		Desc: "Remove the project's passphrase. Project owner, browser session only; current must be given. An instance administrator can clear it without the passphrase (the way back when it is forgotten); that is recorded in the audit log.",
		Auth: ops.RequireProject(ops.ProjectOwner).ForSessions(), SecretInput: true,
	}, svc.clear)
	ops.Register(r, ops.Op{
		Name: "project.unlock", Method: "POST", Path: "/api/v1/projects/{id}/unlock", UI: "POST /ui/projects/{id}/unlock",
		Desc: "Unlock a protected project in this browser session. Any member, browser session only. Wrong passphrases are counted per session, per person and per address, with a growing wait. " +
			"The unlock ends after the project's idle limit, when the session ends, when the passphrase changes, or with project.lock.",
		Auth: ops.RequireProject(ops.ProjectReader).ForSessions(), SecretInput: true,
	}, svc.unlock)
	ops.Register(r, ops.Op{
		Name: "project.lock", Method: "POST", Path: "/api/v1/projects/{id}/lock", UI: "POST /ui/projects/{id}/lock",
		Desc: "Lock the project again in this browser session, now. Any member, browser session only. Does nothing if it is not unlocked.",
		Auth: ops.RequireProject(ops.ProjectReader).ForSessions(), NoDryRun: true, // it changes what a running process remembers
	}, svc.lock)
}

// ---- inputs and outputs -----------------------------------------------------------------------

// ProjectIn names a project.
type ProjectIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectIn) ProjectRef() string { return in.ID }

// KeyUse is what one API key has done to a gated project.
type KeyUse struct {
	KeyID   string `json:"key_id"`
	Name    string `json:"name"`
	Prefix  string `json:"prefix"`
	Owner   string `json:"owner" desc:"Display name of the key's owner."`
	Uses    int    `json:"uses" desc:"Calls that passed because the owner accepts keys."`
	Refused int    `json:"refused" desc:"Calls refused because the owner does not accept keys."`
	FirstAt string `json:"first_at"`
	LastAt  string `json:"last_at"`
	// Mine is a key of the owner's own; Approved is whether it passes the gate now (with allow_keys on).
	Mine     bool   `json:"mine" desc:"A key of the project's owner."`
	Approved bool   `json:"approved" desc:"Approved for this passphrase: let through when the owner accepts keys. Approve with project.passphrase.set and approve_keys."`
	How      string `json:"approved_how,omitempty" enum:"minted,owner" desc:"minted: made by a session that had the project unlocked. owner: approved by the owner."`
}

// Status is a project's gate as the caller may see it.
type Status struct {
	ProjectID   string   `json:"project_id"`
	Protected   bool     `json:"protected" desc:"True when the project has a passphrase."`
	Unlocked    bool     `json:"unlocked" desc:"True when this session has unlocked it (always true when it has none)."`
	IdleMinutes int      `json:"idle_minutes,omitempty" desc:"An unlock ends after this many idle minutes."`
	AllowKeys   bool     `json:"allow_keys,omitempty" desc:"True when API keys are accepted while the passphrase is set. Owner only."`
	SetAt       string   `json:"set_at,omitempty" desc:"Owner only."`
	Failed      int      `json:"failed_tries,omitempty" desc:"Wrong tries counted against this project in the last 24 hours, all buckets together. Owner only."`
	Keys        []KeyUse `json:"keys,omitempty" desc:"The owner's own live keys and every API key that has reached (or tried to reach) this project while it was protected, with whether each is approved. Owner only."`
	Notice      string   `json:"notice" desc:"What the passphrase is and is not, in words to show."`
}

// StatusOut wraps a Status.
type StatusOut struct {
	Item Status `json:"item"`
}

// SetIn sets, changes or adjusts the passphrase.
type SetIn struct {
	ID          string `path:"id" json:"-" desc:"Project id."`
	Passphrase  string `json:"passphrase,omitempty" desc:"The new passphrase, 8 to 200 characters. Leave out to change only the options."`
	Current     string `json:"current,omitempty" desc:"The passphrase now in force; required when the project has one."`
	IdleMinutes int    `json:"idle_minutes,omitempty" desc:"Minutes of no use after which an unlock ends, 1 to 720. 0 keeps the current value (30 for a new passphrase)."`
	AllowKeys   *bool  `json:"allow_keys,omitempty" desc:"Accept API keys while the passphrase is set. Default false: keys are refused. Even then a key passes only if it is approved: made by a session that had the project unlocked, or listed in approve_keys."`
	// ApproveKeys and UnapproveKeys name keys by id from the list project.passphrase.get gives the owner (their own keys, and keys that have tried the project).
	ApproveKeys   []string `json:"approve_keys,omitempty" desc:"Key ids to approve for this passphrase. They must be live keys of a member (the owner's own, or one that has tried this project) with no other pin."`
	UnapproveKeys []string `json:"unapprove_keys,omitempty" desc:"Key ids whose approval to withdraw."`
}

// ProjectRef lets the registry authorize by membership.
func (in SetIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *SetIn) Validate() error {
	if in.Passphrase != "" {
		if n := utf8.RuneCountInString(in.Passphrase); n < passkit.MinPassphrase || n > passkit.MaxPassphrase {
			return ops.Invalid("passphrase", "A passphrase is 8 to 200 characters.")
		}
	}
	if utf8.RuneCountInString(in.Current) > passkit.MaxPassphrase {
		return ops.Invalid("current", "That passphrase is not right.")
	}
	if in.IdleMinutes != 0 && (in.IdleMinutes < MinIdleMinutes || in.IdleMinutes > MaxIdleMinutes) {
		return ops.Invalid("idle_minutes", "The idle limit is 1 to 720 minutes.")
	}
	if len(in.ApproveKeys) > 100 || len(in.UnapproveKeys) > 100 {
		return ops.Invalid("approve_keys", "At most 100 keys at a time.")
	}
	return nil
}

// ClearIn removes the passphrase.
type ClearIn struct {
	ID          string `path:"id" json:"-" desc:"Project id."`
	Current     string `json:"current,omitempty" desc:"The passphrase now in force. An instance administrator may leave it out."`
	ConfirmName string `json:"confirm_name,omitempty" desc:"For an instance administrator who does not know the passphrase: the project's name, typed. Refused with 409 confirmation_required without it."`
}

// ProjectRef lets the registry authorize by membership.
func (in ClearIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *ClearIn) Validate() error {
	if utf8.RuneCountInString(in.Current) > passkit.MaxPassphrase {
		return ops.Invalid("current", "That passphrase is not right.")
	}
	return nil
}

// UnlockIn unlocks a project in this session.
type UnlockIn struct {
	ID         string `path:"id" json:"-" desc:"Project id."`
	Passphrase string `json:"passphrase" desc:"The project's passphrase."`
}

// ProjectRef lets the registry authorize by membership.
func (in UnlockIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *UnlockIn) Validate() error {
	if in.Passphrase == "" {
		return ops.Invalid("passphrase", "Type the passphrase.")
	}
	if utf8.RuneCountInString(in.Passphrase) > passkit.MaxPassphrase {
		return ops.Invalid("passphrase", "That passphrase is not right.")
	}
	return nil
}

// ChangeOut is the answer of set, clear, unlock and lock.
type ChangeOut struct {
	ProjectID   string `json:"project_id"`
	Protected   bool   `json:"protected"`
	Unlocked    bool   `json:"unlocked"`
	IdleMinutes int    `json:"idle_minutes,omitempty"`
	Notice      string `json:"notice,omitempty" desc:"What the passphrase is and is not, in words to show."`
}

// ---- status ---------------------------------------------------------------------------------------

func (g *Service) status(c *ops.Ctx, in ProjectIn) (StatusOut, error) {
	st := Status{ProjectID: in.ID, Unlocked: true, Notice: Notice}
	r, gated, err := g.load(c, c.P.SessionID, c.P.KeyID, in.ID)
	if err != nil {
		return StatusOut{}, err
	}
	if !gated {
		return StatusOut{Item: st}, nil
	}
	st.Protected = true
	st.IdleMinutes = r.idle
	st.Unlocked = c.P.SessionID != "" && g.open(r, c.P.SessionID, in.ID)
	if c.P.KeyID != "" {
		st.Unlocked = r.keyPasses()
	}
	if c.P.KeyID != "" || !g.isOwner(c, in.ID) { // the owner's view is for the owner at a screen, not a key acting as them
		return StatusOut{Item: st}, nil
	}
	st.AllowKeys = r.allowKeys
	var setAt string
	_ = c.DB().Read().QueryRowContext(c, `SELECT updated_at FROM project_gates WHERE project_id = ?`, in.ID).Scan(&setAt)
	st.SetAt = setAt
	_ = c.DB().Read().QueryRowContext(c, `SELECT COALESCE(sum(failed),0) FROM project_unlock_failures WHERE project_id = ? AND last_at > ?`,
		in.ID, db.Format(g.now().Add(-passkit.FailureLife))).Scan(&st.Failed)
	g.Flush(c) // what is still in memory, so the list is current
	// The keys the owner can approve: their own live keys that reach this project, and any key that has tried it.
	rows, err := c.DB().Read().QueryContext(c, `SELECT k.id, k.name, k.prefix, COALESCE(NULLIF(u.display_name,''), u.email, ''), COALESCE(ku.uses,0), COALESCE(ku.refused,0),
			COALESCE(ku.first_at, k.created_at), COALESCE(ku.last_at, ''), k.user_id = ?2, ok.key_id IS NOT NULL, COALESCE(ok.how, '')
		FROM api_keys k JOIN users u ON u.id = k.user_id
		LEFT JOIN project_gate_key_use ku ON ku.key_id = k.id AND ku.project_id = ?1
		LEFT JOIN project_gate_key_ok ok ON ok.key_id = k.id AND ok.project_id = ?1 AND ok.epoch = ?3
		WHERE k.revoked_at IS NULL AND k.compendium_id IS NULL AND (k.project_id IS NULL OR k.project_id = ?1)
		  AND (ku.key_id IS NOT NULL OR (k.user_id = ?2 AND k.expires_at IS NULL OR k.user_id = ?2 AND k.expires_at > ?4))
		ORDER BY COALESCE(ku.last_at, k.created_at) DESC, k.id LIMIT 100`, in.ID, c.P.UserID, r.epoch, db.Now())
	if err != nil {
		return StatusOut{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var k KeyUse
		if err := rows.Scan(&k.KeyID, &k.Name, &k.Prefix, &k.Owner, &k.Uses, &k.Refused, &k.FirstAt, &k.LastAt, &k.Mine, &k.Approved, &k.How); err != nil {
			return StatusOut{}, err
		}
		st.Keys = append(st.Keys, k)
	}
	return StatusOut{Item: st}, rows.Err()
}

// isOwner is true for a member with the owner role, not for an administrator acting as one.
func (g *Service) isOwner(c *ops.Ctx, projectID string) bool {
	var role string
	err := c.DB().Read().QueryRowContext(c, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, c.P.UserID).Scan(&role)
	return err == nil && role == ops.ProjectOwner
}

// ---- verification with the lockout -----------------------------------------------------------------

// errWrong is the one answer to a wrong passphrase.
func errWrong() error { return ops.Invalid("passphrase", "That passphrase is not right.") }

// The lockout. Two buckets are the hard one: the person ("user:", so signing out and in does not reset it) and the
// session ("ses:"). Each earns the share links' schedule: three free wrong tries, then 30 seconds, doubling, at most an
// hour, even for the right passphrase. The client address ("ip:") is soft. Everyone behind one address (a household, an
// office, a proxy that hides the real address) shares it, so it must never lock a person out who has done nothing: it
// has six more free tries than a person, and its wait applies only to a caller who has at least one wrong try of their
// own. A single address that keeps guessing (with fresh sessions or other accounts) is slowed by it; someone who never
// got it wrong is not.
const addrExtraFree = 6

func hardBuckets(c *ops.Ctx) []string {
	b := []string{"user:" + c.P.UserID}
	if c.P.SessionID != "" {
		b = append(b, "ses:"+c.P.SessionID)
	}
	return b
}

func addrBucket(c *ops.Ctx) string {
	if ip := ops.ClientFrom(c); ip != "" {
		return "ip:" + ip
	}
	return ""
}

// verify checks a passphrase against the project's hash under the lockout. It counts the try before it checks it,
// so guesses made at the same moment cannot slip past the counter, and clears the person's and the session's counters
// when the passphrase is right (and takes back its own count on the address). hash is the stored hash. A bucket that
// has earned a wait is answered with it even if the passphrase is right.
func (g *Service) verify(c *ops.Ctx, projectID, hash, pass string) error {
	rel, ok := g.pool.TryAcquire()
	if !ok {
		return ops.RateLimited(2)
	}
	defer rel()
	hard, addr := hardBuckets(c), addrBucket(c)
	now := g.now()
	var wait time.Duration
	err := g.d.WithTx(c, func(tx *sql.Tx) error {
		type st struct {
			failed int
			until  time.Time
		}
		read := func(b string) (st, error) {
			var s st
			var until sql.NullString
			var last string
			err := tx.QueryRow(`SELECT failed, locked_until, last_at FROM project_unlock_failures WHERE project_id = ? AND bucket = ?`, projectID, b).Scan(&s.failed, &until, &last)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return s, err
			}
			if t, perr := db.Parse(last); perr == nil && now.Sub(t) > passkit.FailureLife {
				s.failed = 0
			}
			if t, perr := db.Parse(until.String); perr == nil && s.failed > 0 {
				s.until = t
			}
			return s, nil
		}
		cur := map[string]st{}
		own := 0
		for _, b := range hard {
			s, err := read(b)
			if err != nil {
				return err
			}
			cur[b] = s
			if s.failed > own {
				own = s.failed
			}
			if d := s.until.Sub(now); now.Before(s.until) && d > wait {
				wait = d
			}
		}
		var as st
		if addr != "" {
			var err error
			if as, err = read(addr); err != nil {
				return err
			}
			if d := as.until.Sub(now); own > 0 && now.Before(as.until) && d > wait { // soft: only for a caller with a wrong try of their own
				wait = d
			}
		}
		if wait > 0 {
			return nil // locked: nothing is counted for a try that is not made
		}
		if _, err := tx.Exec(`DELETE FROM project_unlock_failures WHERE project_id = ? AND last_at < ?`, projectID, db.Format(now.Add(-passkit.FailureLife))); err != nil {
			return err
		}
		put := func(b string, n int, delay time.Duration) error {
			var until any
			if delay > 0 {
				until = db.Format(now.Add(delay))
			}
			_, err := tx.Exec(`INSERT INTO project_unlock_failures(project_id, bucket, failed, locked_until, last_at) VALUES (?,?,?,?,?)
				ON CONFLICT(project_id, bucket) DO UPDATE SET failed = excluded.failed, locked_until = excluded.locked_until, last_at = excluded.last_at`,
				projectID, b, n, until, db.Format(now))
			return err
		}
		for _, b := range hard {
			n := cur[b].failed + 1
			if err := put(b, n, passkit.Delay(n)); err != nil {
				return err
			}
		}
		if addr != "" {
			n := as.failed + 1
			if err := put(addr, n, passkit.Delay(n-addrExtraFree)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ops.Internal(err)
	}
	if wait > 0 {
		return ops.RateLimited(int(wait.Seconds()) + 1)
	}
	if !passkit.Verify(hash, pass) {
		return errWrong()
	}
	// Right: this person and this session start again. The address keeps what others made of it, less this try.
	err = g.d.WithTx(c, func(tx *sql.Tx) error {
		for _, b := range hard {
			if _, err := tx.Exec(`DELETE FROM project_unlock_failures WHERE project_id = ? AND bucket = ?`, projectID, b); err != nil {
				return err
			}
		}
		if addr == "" {
			return nil
		}
		var n int
		if err := tx.QueryRow(`SELECT failed FROM project_unlock_failures WHERE project_id = ? AND bucket = ?`, projectID, addr).Scan(&n); err != nil {
			return nil // already gone (a new passphrase cleared it)
		}
		if n <= 1 {
			_, err := tx.Exec(`DELETE FROM project_unlock_failures WHERE project_id = ? AND bucket = ?`, projectID, addr)
			return err
		}
		var until any
		if d := passkit.Delay(n - 1 - addrExtraFree); d > 0 {
			until = db.Format(now.Add(d))
		}
		_, err := tx.Exec(`UPDATE project_unlock_failures SET failed = ?, locked_until = ? WHERE project_id = ? AND bucket = ?`, n-1, until, projectID, addr)
		return err
	})
	if err != nil {
		return ops.Internal(err)
	}
	return nil
}

// snap is what a passphrase was checked against: the hash and the epoch the gate was in. The write that records the
// result must find the gate still in exactly this state (see stale), or the passphrase proved is not the one in force.
type snap struct {
	hash  string
	epoch int
	gated bool
}

func (g *Service) hashOf(ctx context.Context, projectID string) (snap, error) {
	var sn snap
	err := g.d.Read().QueryRowContext(ctx, `SELECT pass_hash, epoch FROM project_gates WHERE project_id = ?`, projectID).Scan(&sn.hash, &sn.epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return snap{}, nil
	}
	sn.gated = err == nil
	return sn, err
}

// errChanged is the one answer when the passphrase changed between the check and the write.
func errChanged() error {
	return ops.ConflictWith("The passphrase changed while you were typing it. Try again.", map[string]any{"reason": "passphrase_changed"})
}

// stale reads the gate inside the writing transaction and reports whether it still is what sn was checked against (both
// the epoch and the hash: a clear followed by a new passphrase comes back to the same epoch with another hash). exists
// says whether a gate row is there now.
func stale(tx *sql.Tx, projectID string, sn snap) (exists bool, epoch, idle, allow int, err error) {
	var hash string
	e := tx.QueryRow(`SELECT pass_hash, epoch, idle_minutes, allow_keys FROM project_gates WHERE project_id = ?`, projectID).Scan(&hash, &epoch, &idle, &allow)
	if errors.Is(e, sql.ErrNoRows) {
		if sn.gated {
			return false, 0, 0, 0, errChanged()
		}
		return false, 0, 0, 0, nil
	}
	if e != nil {
		return false, 0, 0, 0, e
	}
	if !sn.gated || hash != sn.hash || epoch != sn.epoch {
		return true, 0, 0, 0, errChanged()
	}
	return true, epoch, idle, allow, nil
}

// SetAfterVerifyHook installs a function that runs once, between a passphrase being checked and the result being
// written, in unlock, set and clear. It exists so a test can change the passphrase in that gap and see that the write
// notices; it is nil in production.
func (g *Service) SetAfterVerifyHook(f func()) {
	g.mu.Lock()
	g.afterVerify = f
	g.mu.Unlock()
}

func (g *Service) runAfterVerify() {
	g.mu.Lock()
	f := g.afterVerify
	g.afterVerify = nil // once: what it triggers must not re-enter it
	g.mu.Unlock()
	if f != nil {
		f()
	}
}

// ---- unlock and lock -------------------------------------------------------------------------------

func (g *Service) unlock(c *ops.Ctx, in UnlockIn) (ChangeOut, error) {
	if c.P.SessionID == "" {
		return ChangeOut{}, ops.Forbidden("Unlocking needs a signed-in browser session.")
	}
	sn, err := g.hashOf(c, in.ID)
	if err != nil {
		return ChangeOut{}, ops.Internal(err)
	}
	if !sn.gated {
		return ChangeOut{ProjectID: in.ID, Unlocked: true}, nil
	}
	if err := g.verify(c, in.ID, sn.hash, in.Passphrase); err != nil {
		return ChangeOut{}, err
	}
	g.runAfterVerify()
	var idle int
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		// The unlock is recorded only if the gate is still what was proved: a rotation that committed while the hash
		// was being checked must not turn the old passphrase into an unlock in the new epoch.
		_, epoch, i, _, err := stale(tx, in.ID, sn)
		if err != nil {
			return err
		}
		idle = i
		return putUnlock(tx, c.P.SessionID, in.ID, epoch, g.now())
	})
	if err != nil {
		return ChangeOut{}, err
	}
	g.forget(c.P.SessionID, in.ID)
	c.Audit(ops.AuditEntry{Action: "project.unlock", TargetType: "project", TargetID: in.ID})
	return ChangeOut{ProjectID: in.ID, Protected: true, Unlocked: true, IdleMinutes: idle}, nil
}

func putUnlock(tx *sql.Tx, sessionID, projectID string, epoch int, now time.Time) error {
	_, err := tx.Exec(`INSERT INTO project_unlocks(session_id, project_id, epoch, unlocked_at, last_used_at) VALUES (?,?,?,?,?)
		ON CONFLICT(session_id, project_id) DO UPDATE SET epoch = excluded.epoch, unlocked_at = excluded.unlocked_at, last_used_at = excluded.last_used_at`,
		sessionID, projectID, epoch, db.Format(now), db.Format(now))
	return err
}

func (g *Service) lock(c *ops.Ctx, in ProjectIn) (ChangeOut, error) {
	if c.P.SessionID == "" {
		return ChangeOut{ProjectID: in.ID}, nil
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM project_unlocks WHERE session_id = ? AND project_id = ?`, c.P.SessionID, in.ID)
		return err
	})
	if err != nil {
		return ChangeOut{}, err
	}
	g.forget(c.P.SessionID, in.ID)
	g.relocked(in.ID)
	var n int
	_ = c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM project_gates WHERE project_id = ?`, in.ID).Scan(&n)
	return ChangeOut{ProjectID: in.ID, Protected: n > 0}, nil
}

// ---- set and clear ----------------------------------------------------------------------------------

func (g *Service) set(c *ops.Ctx, in SetIn) (ChangeOut, error) {
	if !g.isOwner(c, in.ID) {
		// An administrator acts as an owner everywhere else, but must not lock a book's author out of it.
		return ChangeOut{}, ops.Forbidden("Only the project's owner can set its passphrase.")
	}
	if c.P.SessionID == "" {
		return ChangeOut{}, ops.Forbidden("Setting a passphrase needs a signed-in browser session.")
	}
	sn, err := g.hashOf(c, in.ID)
	if err != nil {
		return ChangeOut{}, ops.Internal(err)
	}
	gated := sn.gated
	if !gated && in.Passphrase == "" {
		return ChangeOut{}, ops.Invalid("passphrase", "Choose a passphrase of 8 to 200 characters.")
	}
	if gated {
		if in.Current == "" {
			return ChangeOut{}, ops.Invalid("current", "Type the passphrase now in force.")
		}
		if err := g.verify(c, in.ID, sn.hash, in.Current); err != nil {
			return ChangeOut{}, err
		}
		g.runAfterVerify()
	}
	g.Flush(c) // the key use list the approvals are checked against must be current
	var newHash string
	if in.Passphrase != "" {
		rel := g.owner.Acquire()
		newHash, err = passkit.Hash(in.Passphrase)
		rel()
		if err != nil {
			return ChangeOut{}, ops.Internal(err)
		}
	}
	now := g.now()
	var out ChangeOut
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		// What was checked must still be in force: the same hash in the same epoch (or still no passphrase), so two owners'
		// sessions cannot overwrite each other with a stale "current".
		exists, epoch, idle, allow, err := stale(tx, in.ID, sn)
		if err != nil {
			return err
		}
		if !exists {
			epoch, idle, allow = 0, DefaultIdleMinutes, 0
		}
		if in.IdleMinutes != 0 {
			idle = in.IdleMinutes
		}
		if in.AllowKeys != nil {
			allow = 0
			if *in.AllowKeys {
				allow = 1
			}
		}
		stamp := db.Format(now)
		switch {
		case !exists:
			epoch = 1
			if _, err := tx.Exec(`INSERT INTO project_gates(project_id, pass_hash, epoch, idle_minutes, allow_keys, set_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
				in.ID, newHash, epoch, idle, allow, c.P.UserID, stamp, stamp); err != nil {
				return err
			}
		case newHash != "":
			epoch++
			if _, err := tx.Exec(`UPDATE project_gates SET pass_hash = ?, epoch = ?, idle_minutes = ?, allow_keys = ?, set_by = ?, updated_at = ? WHERE project_id = ?`,
				newHash, epoch, idle, allow, c.P.UserID, stamp, in.ID); err != nil {
				return err
			}
		default:
			if _, err := tx.Exec(`UPDATE project_gates SET idle_minutes = ?, allow_keys = ?, updated_at = ? WHERE project_id = ?`, idle, allow, stamp, in.ID); err != nil {
				return err
			}
		}
		if newHash != "" {
			// Every approved key is void too (they carry the epoch).
			if _, err := tx.Exec(`DELETE FROM project_gate_key_ok WHERE project_id = ?`, in.ID); err != nil {
				return err
			}
			// Every other unlock is void (the epoch moved on); drop the rows so nothing lingers, and start the
			// wrong-try counters again with the new passphrase.
			if _, err := tx.Exec(`DELETE FROM project_unlocks WHERE project_id = ?`, in.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM project_unlock_failures WHERE project_id = ?`, in.ID); err != nil {
				return err
			}
			// The owner has just proved they know it: this session stays open.
			if err := putUnlock(tx, c.P.SessionID, in.ID, epoch, now); err != nil {
				return err
			}
		}
		for _, id := range in.UnapproveKeys {
			if _, err := tx.Exec(`DELETE FROM project_gate_key_ok WHERE project_id = ? AND key_id = ?`, in.ID, id); err != nil {
				return err
			}
		}
		for _, id := range in.ApproveKeys {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM api_keys k JOIN project_members m ON m.project_id = ?1 AND m.user_id = k.user_id
				WHERE k.id = ?2 AND k.revoked_at IS NULL AND k.compendium_id IS NULL AND (k.project_id IS NULL OR k.project_id = ?1)
				  AND (k.user_id = ?3 OR EXISTS (SELECT 1 FROM project_gate_key_use u WHERE u.project_id = ?1 AND u.key_id = k.id))`, in.ID, id, c.P.UserID).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ops.Invalid("approve_keys", "A key to approve must be a live key of a member with no other pin: your own, or one that has tried this project (project.passphrase.get lists them).")
			}
			if _, err := tx.Exec(`INSERT INTO project_gate_key_ok(project_id, key_id, epoch, how, approved_by, created_at) VALUES (?,?,?,?,?,?)
				ON CONFLICT(project_id, key_id) DO UPDATE SET epoch = excluded.epoch, how = excluded.how, approved_by = excluded.approved_by, created_at = excluded.created_at`,
				in.ID, id, epoch, "owner", c.P.UserID, stamp); err != nil {
				return err
			}
		}
		out = ChangeOut{ProjectID: in.ID, Protected: true, Unlocked: true, IdleMinutes: idle, Notice: Notice}
		c.Audit(ops.AuditEntry{Action: "project.passphrase.set", TargetType: "project", TargetID: in.ID,
			Detail: map[string]any{"created": !exists, "passphrase_changed": newHash != "", "idle_minutes": idle, "allow_keys": allow == 1,
				"keys_approved": len(in.ApproveKeys), "keys_unapproved": len(in.UnapproveKeys)}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ChangeOut{}, err
	}
	g.forget("", in.ID)
	g.relocked(in.ID)
	return out, nil
}

func (g *Service) clear(c *ops.Ctx, in ClearIn) (ChangeOut, error) {
	sn, err := g.hashOf(c, in.ID)
	if err != nil {
		return ChangeOut{}, ops.Internal(err)
	}
	if !sn.gated {
		return ChangeOut{ProjectID: in.ID}, nil
	}
	owner := g.isOwner(c, in.ID)
	recovery := false
	switch {
	case owner:
		if in.Current == "" {
			return ChangeOut{}, ops.Invalid("current", "Type the passphrase now in force.")
		}
		if err := g.verify(c, in.ID, sn.hash, in.Current); err != nil {
			return ChangeOut{}, err
		}
		g.runAfterVerify()
	case c.P.Role == ops.RoleAdmin:
		// The way back when the owner has forgotten it: the operator can reach the text anyway (see the notes),
		// and the act is in the audit log under its own name.
		recovery = true
		// It opens a project's text to every member: the administrator types the project's name, as for the other acts that cannot be taken back.
		var title string
		_ = c.DB().Read().QueryRowContext(c, `SELECT title FROM projects WHERE id = ?`, in.ID).Scan(&title)
		if strings.TrimSpace(in.ConfirmName) == "" || strings.TrimSpace(in.ConfirmName) != strings.TrimSpace(title) {
			return ChangeOut{}, ops.ConflictWith("Type the project's name to remove its passphrase without knowing it.", map[string]any{"reason": "confirmation_required"})
		}
	default:
		return ChangeOut{}, ops.Forbidden("Only the project's owner can remove its passphrase.")
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if !recovery { // a passphrase that was checked must still be the one in force when it is removed
			if _, _, _, _, err := stale(tx, in.ID, sn); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`DELETE FROM project_unlocks WHERE project_id = ?`,
			`DELETE FROM project_unlock_failures WHERE project_id = ?`,
			`DELETE FROM project_gate_key_use WHERE project_id = ?`,
			`DELETE FROM project_gate_key_ok WHERE project_id = ?`,
			`DELETE FROM project_gates WHERE project_id = ?`,
		} {
			if _, err := tx.Exec(q, in.ID); err != nil {
				return err
			}
		}
		action := "project.passphrase.clear"
		if recovery {
			action = "project.passphrase.admin_clear"
		}
		c.Audit(ops.AuditEntry{Action: action, TargetType: "project", TargetID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ChangeOut{}, err
	}
	g.forget("", in.ID)
	g.relocked(in.ID)
	return ChangeOut{ProjectID: in.ID, Unlocked: true}, nil
}

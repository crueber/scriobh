// Package projectgate is the project passphrase (F-252): an application-level gate for a project, not encryption.
//
// While a project has a passphrase, a signed-in member must unlock it in their session before the project's
// content is served. The decisions and their reasons are in docs/p7-notes/passphrase.md; in short:
//
//   - It keeps out a person at a shared screen, a borrowed unlocked laptop, a family member with the session.
//     It does not keep out the server's operator, the database file, a backup, the on-disk mirror, a Git push or an
//     export: the text is not encrypted, and the UI says so.
//   - An unlock is a row on a session with its own idle expiry (and a 12 hour ceiling). It ends on request
//     (project.lock), when the session ends, and when the passphrase changes (the epoch).
//   - An API key is its own credential. The owner chooses whether keys pass while a passphrase is set (default:
//     they are refused); either way every use is counted per key.
//   - Background work (the mirror, webhooks, the AI runs a person already started, the search and mention
//     indexes) does not ask the gate: it is for people at a screen. It is never reached through a context that
//     names a caller.
//   - The passphrase is hashed with the share links' argon2id and compared in constant time. Wrong tries are
//     counted per session, per person and per address, with the share links' schedule, and the count is taken
//     before the hash is checked, so concurrent guesses cannot slip past it.
package projectgate

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/passkit"
)

// ServiceName is the name the service is registered under in ops.Env.
const ServiceName = "projectgate"

// Limits of the gate.
const (
	DefaultIdleMinutes = 30
	MinIdleMinutes     = 1
	MaxIdleMinutes     = 720
	// MaxUnlockAge is the longest an unlock lasts, however busy the tab: an open window must not stay open all day.
	MaxUnlockAge = 12 * time.Hour
	// touchEvery limits how often a use writes to the database (the idle clock is kept in memory in between).
	touchEvery = time.Minute
)

// Service is the gate. One per process.
type Service struct {
	d     *db.DB
	pool  *passkit.Pool // wrong-try checks: the people who may be guessing
	owner *passkit.Pool // the owner's own hashing of a new passphrase
	now   func() time.Time

	mu      sync.Mutex
	seen    map[string]time.Time // session|project -> last use seen in this process (newer than the database's)
	flushed map[string]time.Time // session|project -> when the database was last written
	keyUse  map[string]*keyAcc
	// inflight counts the background writes started and not finished; idle is signalled when it reaches zero. (Not a
	// WaitGroup: Flush may be called while new writes are being started, which a WaitGroup does not allow.)
	inflight int
	idle     *sync.Cond

	afterVerify func() // a test hook: see SetAfterVerifyHook

	watchers map[string]map[chan struct{}]struct{} // project -> open streams that want to know when it relocks
	onRelock func(projectID string)                // told when a project relocks (presence drops what it remembered about the gate)
}

// SetOnRelock registers a function called, outside the gate's lock, whenever a project relocks (project.lock, a new
// passphrase, the passphrase removed), as the streams watching it are woken.
func (g *Service) SetOnRelock(f func(projectID string)) {
	g.mu.Lock()
	g.onRelock = f
	g.mu.Unlock()
}

type keyAcc struct {
	key, project string
	uses, refuse int
	first, last  time.Time
	flushedAt    time.Time
}

// New returns a gate over d. d may be nil when only the op schema is needed.
func New(d *db.DB) *Service {
	g := &Service{d: d, pool: passkit.NewPool(2), owner: passkit.NewPool(2), now: db.NowTime, seen: map[string]time.Time{}, flushed: map[string]time.Time{}, keyUse: map[string]*keyAcc{}}
	g.idle = sync.NewCond(&g.mu)
	return g
}

// begin and end bracket a background write.
func (g *Service) begin() { g.inflight++ } // called with g.mu held

func (g *Service) end() {
	g.mu.Lock()
	g.inflight--
	if g.inflight == 0 {
		g.idle.Broadcast()
	}
	g.mu.Unlock()
}

// wait blocks until no background write is in flight.
func (g *Service) wait() {
	g.mu.Lock()
	for g.inflight > 0 {
		g.idle.Wait()
	}
	g.mu.Unlock()
}

// Watch returns a channel that is signalled whenever something that can lock a session out of the project happens
// (project.lock, a new passphrase, the passphrase removed), and a function that stops watching. A stream of live updates
// selects on it, asks the gate again and ends if it is refused, so a person who has just locked the project does not
// go on receiving its events until the next heartbeat.
func (g *Service) Watch(projectID string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	g.mu.Lock()
	if g.watchers == nil {
		g.watchers = map[string]map[chan struct{}]struct{}{}
	}
	if g.watchers[projectID] == nil {
		g.watchers[projectID] = map[chan struct{}]struct{}{}
	}
	g.watchers[projectID][ch] = struct{}{}
	g.mu.Unlock()
	return ch, func() {
		g.mu.Lock()
		delete(g.watchers[projectID], ch)
		if len(g.watchers[projectID]) == 0 {
			delete(g.watchers, projectID)
		}
		g.mu.Unlock()
	}
}

// relocked wakes the streams watching a project.
func (g *Service) relocked(projectID string) {
	g.mu.Lock()
	for ch := range g.watchers[projectID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	f := g.onRelock
	g.mu.Unlock()
	if f != nil {
		f(projectID)
	}
}

// SetClock replaces the clock; tests only.
func (g *Service) SetClock(now func() time.Time) { g.now = now }

// row is what one query learns about a project's gate and the caller's unlock of it.
type row struct {
	epoch, idle int
	allowKeys   bool
	keyOK       bool // the caller's key is approved for this epoch (see project_gate_key_ok)
	uEpoch      sql.NullInt64
	unlockedAt  sql.NullString
	lastUsed    sql.NullString
}

func (g *Service) load(ctx context.Context, sessionID, keyID, projectID string) (row, bool, error) {
	var r row
	var ak, kok int
	err := g.d.Read().QueryRowContext(ctx, `SELECT g.epoch, g.idle_minutes, g.allow_keys, u.epoch, u.unlocked_at, u.last_used_at,
			EXISTS (SELECT 1 FROM project_gate_key_ok k WHERE k.project_id = g.project_id AND k.key_id = ?4 AND k.epoch = g.epoch)
		FROM project_gates g
		LEFT JOIN project_unlocks u ON u.project_id = g.project_id AND u.session_id = ?1
			AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = u.session_id AND s.revoked_at IS NULL AND s.expires_at > ?3)
		WHERE g.project_id = ?2`, sessionID, projectID, db.Format(g.now()), keyID).
		Scan(&r.epoch, &r.idle, &ak, &r.uEpoch, &r.unlockedAt, &r.lastUsed, &kok)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, err
	}
	r.allowKeys = ak == 1
	r.keyOK = kok == 1
	return r, true, nil
}

// keyPasses says whether an API key is let through: the owner accepts keys on this project and has this key approved.
func (r row) keyPasses() bool { return r.allowKeys && r.keyOK }

// open reports whether the caller's unlock is live: made in this epoch, not too old, not idle too long.
func (g *Service) open(r row, sessionID, projectID string) bool {
	if !r.uEpoch.Valid || int(r.uEpoch.Int64) != r.epoch {
		return false
	}
	now := g.now()
	at, err := db.Parse(r.unlockedAt.String)
	if err != nil || now.Sub(at) >= MaxUnlockAge {
		return false
	}
	last, err := db.Parse(r.lastUsed.String)
	if err != nil {
		return false
	}
	g.mu.Lock()
	if t, ok := g.seen[sessionID+"|"+projectID]; ok && t.After(last) {
		last = t
	}
	g.mu.Unlock()
	return now.Sub(last) < time.Duration(r.idle)*time.Minute
}

// Check is the gate: nil when the project has no passphrase or the caller may reach it, otherwise a refusal. It is
// the function the registry asks (ops.Env.ProjectGate).
func (g *Service) Check(ctx context.Context, p ops.Principal, projectID string) error {
	if g == nil || g.d == nil {
		return nil
	}
	if p.Via == "cli" { // the operator's own shell on the server: see the notes, "who the gate is not for"
		return nil
	}
	isKey := p.KeyID != "" || p.Via == "apikey"
	r, gated, err := g.load(ctx, p.SessionID, p.KeyID, projectID)
	if err != nil {
		return ops.Internal(err)
	}
	if !gated {
		return nil
	}
	if isKey {
		// One call is one count: not a check made by hand inside an op, a stream's recheck or a preview (passive), and not the
		// binder's early authorization when the call is let through (the call repeats it and counts).
		if !ops.IsPassive(ctx) && (!r.keyPasses() || !ops.IsPrecheck(ctx)) {
			g.recordKey(p.KeyID, projectID, !r.keyPasses())
		}
		if r.keyPasses() {
			return nil
		}
		return ops.ProjectKeysRefused(g.describe(ctx, projectID))
	}
	if p.SessionID != "" && g.open(r, p.SessionID, projectID) {
		if !ops.IsPassive(ctx) {
			g.touch(p.SessionID, projectID)
		}
		return nil
	}
	return ops.ProjectLocked(g.describe(ctx, projectID))
}

// describe is what a refusal may say about the project: its id, title and slug.
func (g *Service) describe(ctx context.Context, projectID string) map[string]any {
	var title, slug string
	_ = g.d.Read().QueryRowContext(ctx, `SELECT title, slug FROM projects WHERE id = ?`, projectID).Scan(&title, &slug)
	return map[string]any{"project_id": projectID, "title": title, "slug": slug}
}

// touch records a use. The idle clock moves in memory at once; the database is written at most once a minute, from
// a goroutine, so a read never waits for (or deadlocks against) the single writer.
func (g *Service) touch(sessionID, projectID string) {
	k := sessionID + "|" + projectID
	now := g.now()
	g.mu.Lock()
	if len(g.seen) > 4096 { // what a long-running server remembers of unlocks that are long over
		for key, t := range g.seen {
			if now.Sub(t) > MaxUnlockAge {
				delete(g.seen, key)
				delete(g.flushed, key)
			}
		}
	}
	g.seen[k] = now
	due := now.Sub(g.flushed[k]) >= touchEvery
	if due {
		g.flushed[k] = now
		g.begin()
	}
	g.mu.Unlock()
	if due {
		go func() {
			defer g.end()
			_ = g.d.WithTx(context.Background(), func(tx *sql.Tx) error {
				_, err := tx.Exec(`UPDATE project_unlocks SET last_used_at = ? WHERE session_id = ? AND project_id = ?`, db.Format(now), sessionID, projectID)
				return err
			})
		}()
	}
}

// forget drops what the gate remembers about an unlock (on lock, on a new passphrase).
func (g *Service) forget(sessionID, projectID string) {
	g.mu.Lock()
	for k := range g.seen {
		if (sessionID == "" || strings.HasPrefix(k, sessionID+"|")) && strings.HasSuffix(k, "|"+projectID) {
			delete(g.seen, k)
			delete(g.flushed, k)
		}
	}
	g.mu.Unlock()
}

// recordKey counts a key's use of a gated project. The first use of a pair is written at once; later ones are
// added up and written at most once a minute.
func (g *Service) recordKey(keyID, projectID string, refused bool) {
	if keyID == "" {
		return
	}
	k := keyID + "|" + projectID
	now := g.now()
	g.mu.Lock()
	a := g.keyUse[k]
	if a == nil {
		a = &keyAcc{key: keyID, project: projectID, first: now}
		g.keyUse[k] = a
	}
	if refused {
		a.refuse++
	} else {
		a.uses++
	}
	a.last = now
	due := now.Sub(a.flushedAt) >= touchEvery
	var u, rf int
	var first time.Time
	if due {
		a.flushedAt = now
		u, rf, first = a.uses, a.refuse, a.first
		a.uses, a.refuse = 0, 0
		g.begin()
	}
	g.mu.Unlock()
	if due {
		go func() {
			defer g.end()
			g.writeKeyUse(keyID, projectID, u, rf, first, now)
		}()
	}
}

func (g *Service) writeKeyUse(keyID, projectID string, uses, refused int, first, last time.Time) {
	_ = g.d.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO project_gate_key_use(key_id, project_id, uses, refused, first_at, last_at) VALUES (?,?,?,?,?,?)
			ON CONFLICT(key_id, project_id) DO UPDATE SET uses = uses + excluded.uses, refused = refused + excluded.refused, last_at = excluded.last_at`,
			keyID, projectID, uses, refused, db.Format(first), db.Format(last))
		return err
	})
}

// Flush writes what is still in memory (key counts) and waits for the writes in flight. The server calls it at
// shutdown; tests call it before they look at the tables.
func (g *Service) Flush(ctx context.Context) {
	if g == nil || g.d == nil {
		return
	}
	g.wait()
	g.mu.Lock()
	var pend []*keyAcc
	for _, a := range g.keyUse {
		if a.uses > 0 || a.refuse > 0 {
			c := *a
			pend = append(pend, &c)
			a.uses, a.refuse = 0, 0
		}
	}
	g.mu.Unlock()
	for _, a := range pend {
		g.writeKeyUse(a.key, a.project, a.uses, a.refuse, a.first, a.last)
	}
}

// Protected says which of the projects have a passphrase and which of those the caller has not unlocked. It is
// for the lists that span projects (project.list, search, notifications): they show a locked project's title and
// say it needs a passphrase, and nothing else.
type Protected struct {
	Set       bool
	Locked    bool
	AllowKeys bool
	Idle      int // the idle limit in minutes, for a page that covers itself when a tab has been hidden longer
}

// States returns the gate state of the given projects for a caller; projects with no gate are absent. A key is
// "locked" where keys are refused.
func (g *Service) States(ctx context.Context, p ops.Principal, ids []string) (map[string]Protected, error) {
	out := map[string]Protected{}
	if g == nil || g.d == nil || len(ids) == 0 {
		return out, nil
	}
	if p.Via == "cli" {
		for _, id := range g.gated(ctx, ids) {
			out[id] = Protected{Set: true}
		}
		return out, nil
	}
	isKey := p.KeyID != "" || p.Via == "apikey"
	for start := 0; start < len(ids); start += 400 {
		end := start + 400
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		args := []any{p.SessionID, db.Format(g.now()), p.KeyID}
		ph := make([]string, len(chunk))
		for i, id := range chunk {
			ph[i] = "?"
			args = append(args, id)
		}
		rows, err := g.d.Read().QueryContext(ctx, `SELECT g.project_id, g.epoch, g.idle_minutes, g.allow_keys, u.epoch, u.unlocked_at, u.last_used_at,
				EXISTS (SELECT 1 FROM project_gate_key_ok k WHERE k.project_id = g.project_id AND k.key_id = ?3 AND k.epoch = g.epoch)
			FROM project_gates g
			LEFT JOIN project_unlocks u ON u.project_id = g.project_id AND u.session_id = ?1
				AND EXISTS (SELECT 1 FROM sessions s WHERE s.id = u.session_id AND s.revoked_at IS NULL AND s.expires_at > ?2)
			WHERE g.project_id IN (`+strings.Join(ph, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			var r row
			var ak, kok int
			if err := rows.Scan(&id, &r.epoch, &r.idle, &ak, &r.uEpoch, &r.unlockedAt, &r.lastUsed, &kok); err != nil {
				rows.Close()
				return nil, err
			}
			r.allowKeys, r.keyOK = ak == 1, kok == 1
			locked := true
			if isKey {
				locked = !r.keyPasses()
			} else if p.SessionID != "" {
				locked = !g.open(r, p.SessionID, id)
			}
			out[id] = Protected{Set: true, Locked: locked, AllowKeys: r.allowKeys, Idle: r.idle}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (g *Service) gated(ctx context.Context, ids []string) []string {
	var out []string
	for _, id := range ids {
		var n int
		_ = g.d.Read().QueryRowContext(ctx, `SELECT count(*) FROM project_gates WHERE project_id = ?`, id).Scan(&n)
		if n > 0 {
			out = append(out, id)
		}
	}
	return out
}

// Locked reports whether the caller is shut out of one project (false when it has no passphrase).
func (g *Service) Locked(ctx context.Context, p ops.Principal, projectID string) bool {
	return g.Check(ops.Passive(ctx), p, projectID) != nil
}

// LockedFor returns the ids of the projects the caller is a member of that have a passphrase the caller has not
// unlocked. The lists that span projects leave these out (or show their titles only).
func (g *Service) LockedFor(ctx context.Context, p ops.Principal) ([]string, error) {
	if g == nil || g.d == nil {
		return nil, nil
	}
	ids, err := g.memberGated(ctx, p.UserID)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	states, err := g.States(ctx, p, ids)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, id := range ids {
		if states[id].Locked {
			out = append(out, id)
		}
	}
	return out, nil
}

// missing is the answer when the gate's service is not in the environment: nothing (a build with no gate installed, as
// when only the op schema is wanted), or an error when a gate is installed, so a filter that needs the service never fails
// open.
func missing(c *ops.Ctx) error {
	if c.Env != nil && c.Env.ProjectGate != nil {
		return ops.Internal(errors.New("the passphrase gate is installed but its service is missing from this environment"))
	}
	return nil
}

// LockedProjects is LockedFor through the service the registry holds, for an op handler.
func LockedProjects(c *ops.Ctx) ([]string, error) {
	g, ok := ops.Service[*Service](c.Env, ServiceName)
	if !ok {
		return nil, missing(c)
	}
	return g.LockedFor(c, c.P)
}

// NotIn is an SQL condition keeping column col out of the given ids ("1" for none), and its arguments.
func NotIn(col string, ids []string) (string, []any) {
	if len(ids) == 0 {
		return "1", nil
	}
	args := make([]any, len(ids))
	ph := make([]string, len(ids))
	for i, id := range ids {
		args[i], ph[i] = id, "?"
	}
	return col + " NOT IN (" + strings.Join(ph, ",") + ")", args
}

// CheckMint says whether the caller may make an API key now. A key is its own credential and does not need the
// passphrase, so a person who has the session but not the passphrase must not be able to make one: a key pinned to a
// protected project needs the project unlocked, and a key with no pin (which reaches every project its owner is on)
// needs every protected project that accepts keys unlocked. Projects that refuse keys are not asked about: a key
// is refused there whoever made it.
func CheckMint(c *ops.Ctx, projectID string) (approve []string, err error) {
	g, ok := ops.Service[*Service](c.Env, ServiceName)
	if !ok || g == nil || g.d == nil {
		return nil, missing(c)
	}
	passive := ops.Passive(c)
	bySession := c.P.KeyID == "" && c.P.Via != "apikey" && c.P.SessionID != ""
	if projectID != "" {
		if err := g.Check(passive, c.P, projectID); err != nil {
			return nil, err
		}
		var n int
		_ = g.d.Read().QueryRowContext(c, `SELECT count(*) FROM project_gates WHERE project_id = ?`, projectID).Scan(&n)
		if n > 0 && bySession {
			approve = []string{projectID} // the check passed for a session: the project is unlocked in it
		}
		return approve, nil
	}
	ids, err := g.memberGated(passive, c.P.UserID)
	if err != nil {
		return nil, ops.Internal(err)
	}
	states, err := g.States(passive, c.P, ids)
	if err != nil {
		return nil, ops.Internal(err)
	}
	for _, id := range ids {
		st := states[id]
		if st.Locked && st.AllowKeys && bySession {
			return nil, ops.ProjectLocked(g.describe(passive, id))
		}
		if !st.Locked && bySession {
			approve = append(approve, id)
		}
	}
	return approve, nil
}

// ApproveMinted records, in the transaction that makes a key, that the projects it was made with unlocked accept it
// (project_gate_key_ok, how "minted"), for the epoch each gate is in now. It does so only where the minting session's
// unlock is still there in that epoch, inside this transaction: a passphrase that was changed since CheckMint looked
// leaves the key unapproved.
func ApproveMinted(tx *sql.Tx, projectIDs []string, keyID, userID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	for _, id := range projectIDs {
		if _, err := tx.Exec(`INSERT INTO project_gate_key_ok(project_id, key_id, epoch, how, approved_by, created_at)
			SELECT g.project_id, ?, g.epoch, 'minted', ?, ? FROM project_gates g
			JOIN project_unlocks u ON u.project_id = g.project_id AND u.session_id = ? AND u.epoch = g.epoch
			WHERE g.project_id = ?
			ON CONFLICT(project_id, key_id) DO UPDATE SET epoch = excluded.epoch, how = excluded.how, created_at = excluded.created_at`,
			keyID, userID, db.Now(), sessionID, id); err != nil {
			return err
		}
	}
	return nil
}

func (g *Service) memberGated(ctx context.Context, userID string) ([]string, error) {
	rows, err := g.d.Read().QueryContext(ctx, `SELECT g.project_id FROM project_gates g JOIN project_members m ON m.project_id = g.project_id AND m.user_id = ? ORDER BY g.project_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClearOffline removes a project's passphrase from the database with no caller: the operator's way back when the owner
// has forgotten it and is also the only administrator (who, as the project's owner, needs the passphrase to clear it
// through the op). It is for the offline command `scriobh passphrase clear`, run with the server stopped. It is
// recorded in the audit log under its own name. It reports whether there was a passphrase to remove.
func ClearOffline(ctx context.Context, d *db.DB, projectID string) (bool, error) {
	var removed bool
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM projects WHERE id = ? AND deleted_at IS NULL`, projectID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return errors.New("no such project")
		}
		res, err := tx.Exec(`DELETE FROM project_gates WHERE project_id = ?`, projectID)
		if err != nil {
			return err
		}
		k, _ := res.RowsAffected()
		removed = k > 0
		for _, q := range []string{`DELETE FROM project_unlocks WHERE project_id = ?`, `DELETE FROM project_unlock_failures WHERE project_id = ?`, `DELETE FROM project_gate_key_use WHERE project_id = ?`, `DELETE FROM project_gate_key_ok WHERE project_id = ?`} {
			if _, err := tx.Exec(q, projectID); err != nil {
				return err
			}
		}
		if removed {
			_, err = tx.Exec(`INSERT INTO audit_log(id, at, action, target_type, target_id, detail_json) VALUES (?,?,?,?,?,?)`,
				db.NewID("aud"), db.Now(), "project.passphrase.operator_clear", "project", projectID, `{"via":"cli"}`)
		}
		return err
	})
	return removed, err
}

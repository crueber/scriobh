// Package desktop holds what a desktop instance (SCRIOBH_DESKTOP=1) keeps and the ops over it: the one local profile,
// which identity provider the instance signs in through and the switch between the embedded one and an external one,
// where the data directory is, and the look for a model server on this computer.
//
// Everything is in two rows of the settings table, so a backup carries it. Outside desktop mode every op here answers
// "not found" and nothing is read or written.
package desktop

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/desktopidp"
	"github.com/crueber/scriobh/internal/secrets"
)

// The settings rows.
const (
	KeyProfile = "desktop.profile"
	KeyIdP     = "desktop.idp"
)

// Modes.
const (
	ModeEmbedded = "embedded"
	ModeExternal = "external"
)

// LocalIdentity is the issuer string stored with the local profile's account (auth.DesktopIdentityIssuer; repeated
// here because the auth package is above this one).
const LocalIdentity = "urn:scriobh:desktop-profile"

// secretContext binds the sealed client secret to its use.
const secretContext = "desktop.idp.client_secret"

// External is the external provider an administrator entered.
type External struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"client_id"`
	// Secret is the client secret, sealed with the instance key; empty for a public client.
	Secret      string `json:"secret,omitempty"`
	AdminGroup  string `json:"admin_group,omitempty"`
	WriterGroup string `json:"writer_group,omitempty"`
	ReaderGroup string `json:"reader_group,omitempty"`
	// Port, when not 0, is the loopback port the instance binds while it signs in through this provider, for a
	// provider that wants one exact redirect address.
	Port int `json:"port,omitempty"`
}

// hash names a provider's settings, so that a proof of sign-in is good only for the settings it was made with.
func (x External) hash() string {
	raw, _ := json.Marshal(x)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// Identity is a person at the external provider, as a completed sign-in showed them.
type Identity struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name,omitempty"`
}

// Fingerprint names the identity for a page to bind a confirmation to: a hash of the issuer and subject, which the
// page carries without showing the subject. The switch is refused unless it is the one now recorded.
func (i Identity) Fingerprint() string {
	sum := sha256.Sum256([]byte(i.Issuer + "\x00" + i.Subject))
	return hex.EncodeToString(sum[:16])
}

// Verified records that the administrator signed in at the external provider with these settings.
type Verified struct {
	Identity
	UserID string `json:"user_id"`
	Config string `json:"config"`
	At     string `json:"at"`
}

// Linked records which account was moved to the external identity, so the way back can move it again.
type Linked struct {
	UserID string `json:"user_id"`
	Identity
}

// Pending is a switch that happens at the next start.
type Pending struct {
	To string `json:"to"`
	By string `json:"by"`
	At string `json:"at"`
}

// State is the identity-provider state of a desktop instance.
type State struct {
	Mode     string    `json:"mode"`
	External *External `json:"external,omitempty"`
	Verified *Verified `json:"verified,omitempty"`
	Linked   *Linked   `json:"linked,omitempty"`
	Pending  *Pending  `json:"pending,omitempty"`
	// LastProblem is why the last switch was not made at start, in words for the administrator.
	LastProblem string `json:"last_problem,omitempty"`
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getJSON(ctx context.Context, q querier, key string, into any) (bool, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), into)
}

func putJSON(tx *sql.Tx, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, key, string(raw), db.Now())
	return err
}

// LoadProfile returns the local profile, and false when none has been made.
func LoadProfile(ctx context.Context, d *db.DB) (desktopidp.Profile, bool, error) {
	var p desktopidp.Profile
	ok, err := getJSON(ctx, d.Read(), KeyProfile, &p)
	if err != nil || !ok || p.Subject == "" {
		return desktopidp.Profile{}, false, err
	}
	return p, true, nil
}

// CreateProfile makes the local profile, once. name and email are already cleaned (desktopidp.CleanProfile). When a
// profile exists it is returned unchanged and created is false: the form that calls this can only ever make the first.
func CreateProfile(ctx context.Context, d *db.DB, name, email string) (p desktopidp.Profile, created bool, err error) {
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		ok, err := getJSON(ctx, tx, KeyProfile, &p)
		if err != nil {
			return err
		}
		if ok && p.Subject != "" {
			return nil
		}
		p = desktopidp.Profile{Subject: desktopidp.NewSubject(), Name: name, Email: email, CreatedAt: db.Now()}
		created = true
		return putJSON(tx, KeyProfile, p)
	})
	return p, created, err
}

// LoadState returns the identity-provider state; a new instance is embedded.
func LoadState(ctx context.Context, q querier) (State, error) {
	st := State{}
	if _, err := getJSON(ctx, q, KeyIdP, &st); err != nil {
		return State{}, err
	}
	if st.Mode != ModeExternal {
		st.Mode = ModeEmbedded
	}
	return st, nil
}

// ExternalConfig opens the stored provider for the configuration.
func ExternalConfig(st State, box *secrets.Box) (config.DesktopExternalIdP, error) {
	if st.External == nil {
		return config.DesktopExternalIdP{}, errors.New("no external provider is stored")
	}
	x := st.External
	out := config.DesktopExternalIdP{Issuer: x.Issuer, ClientID: x.ClientID, AdminGroup: x.AdminGroup, WriterGroup: x.WriterGroup, ReaderGroup: x.ReaderGroup}
	if x.Secret != "" {
		plain, err := box.Open(x.Secret, secretContext)
		if err != nil {
			return config.DesktopExternalIdP{}, errors.New("the stored client secret cannot be opened with this instance's secret key")
		}
		out.ClientSecret = string(plain)
	}
	return out, nil
}

// ApplyPending makes a switch that was asked for before this start, in one transaction, and returns the state the
// process is to run in. It is called once, before the server is built.
//
// To the external provider: the account that proved both identities (it was signed in as the local profile, and it
// completed a sign-in at the provider with these settings) is moved to the external identity. Nothing is matched by
// email address. Back to the embedded issuer: the account that was moved is moved back to the local profile; when it
// is gone, the administrator who asked takes its place.
//
// A switch that cannot be made (the account is gone, the identity is already another account's) is dropped and said
// in LastProblem; the instance stays as it was. box may be nil when no switch to the external provider is pending.
func ApplyPending(ctx context.Context, d *db.DB, box *secrets.Box, log *slog.Logger) (State, error) {
	var st State
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		var err error
		if st, err = LoadState(ctx, tx); err != nil {
			return err
		}
		if st.Pending == nil {
			return nil
		}
		p := *st.Pending
		st.Pending, st.LastProblem = nil, ""
		switch p.To {
		case ModeExternal:
			st.LastProblem = toExternal(ctx, tx, &st, box)
		case ModeEmbedded:
			st.LastProblem = toEmbedded(ctx, tx, &st, p.By)
		}
		if st.LastProblem != "" && log != nil {
			log.Warn("desktop: the switch of identity provider asked for before this start was not made", "to", p.To, "why", st.LastProblem)
		} else if log != nil {
			log.Info("desktop: identity provider switched", "to", p.To)
		}
		if _, err := tx.Exec(`INSERT INTO audit_log(id, at, actor_id, action, target_type, target_id, detail_json) VALUES (?,?,?,?,?,?,?)`,
			db.NewID("aud"), db.Now(), nullable(p.By), "desktop.idp.applied", "settings", KeyIdP, auditDetail(p.To, st.LastProblem == "")); err != nil {
			return err
		}
		return putJSON(tx, KeyIdP, st)
	})
	return st, err
}

func auditDetail(to string, done bool) string {
	raw, _ := json.Marshal(map[string]any{"to": to, "done": done})
	return string(raw)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func toExternal(ctx context.Context, tx *sql.Tx, st *State, box *secrets.Box) string {
	if st.Mode == ModeExternal {
		return ""
	}
	if st.External == nil || st.Verified == nil || st.Verified.Config != st.External.hash() || st.Verified.Issuer != st.External.Issuer {
		return "the provider's settings changed after the sign-in that proved them"
	}
	if st.External.Secret != "" && (box == nil || !box.CanOpen(st.External.Secret, secretContext)) {
		return "the stored client secret cannot be opened with this instance's secret key"
	}
	v := st.Verified
	var taken int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE oidc_issuer = ? AND oidc_sub = ? AND deleted_at IS NULL AND id <> ?`, v.Issuer, v.Subject, v.UserID).Scan(&taken); err != nil {
		return "the accounts could not be read"
	}
	if taken > 0 {
		return "that identity at the provider already belongs to another account here"
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET oidc_issuer = ?, oidc_sub = ?, updated_at = ? WHERE id = ? AND oidc_issuer = ? AND deleted_at IS NULL AND disabled_at IS NULL`,
		v.Issuer, v.Subject, db.Now(), v.UserID, LocalIdentity)
	if err != nil {
		return "the account could not be moved to the provider's identity"
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "the local profile's account was not found"
	}
	st.Mode = ModeExternal
	st.Linked = &Linked{UserID: v.UserID, Identity: v.Identity}
	st.Verified = nil
	return ""
}

func toEmbedded(ctx context.Context, tx *sql.Tx, st *State, by string) string {
	if st.Mode == ModeEmbedded {
		return ""
	}
	var p desktopidp.Profile
	if had, err := getJSON(ctx, tx, KeyProfile, &p); err != nil {
		return "the local profile could not be read"
	} else if !had || p.Subject == "" {
		// No profile was ever made here (the database came from somewhere else): the account that takes its place
		// below needs a subject to be bound to, so one is made now, named after that account.
		p = desktopidp.Profile{Subject: desktopidp.NewSubject(), CreatedAt: db.Now()}
	}
	move := func(userID string) bool {
		if userID == "" {
			return false
		}
		res, err := tx.ExecContext(ctx, `UPDATE users SET oidc_issuer = ?, oidc_sub = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL AND disabled_at IS NULL AND is_agent = 0`,
			LocalIdentity, p.Subject, db.Now(), userID)
		if err != nil {
			return false
		}
		n, _ := res.RowsAffected()
		return n == 1
	}
	// Whoever holds the local identity now (nobody, unless the database was edited) lets go of it first.
	if _, err := tx.ExecContext(ctx, `UPDATE users SET oidc_issuer = NULL, oidc_sub = NULL, updated_at = ? WHERE oidc_issuer = ?`, db.Now(), LocalIdentity); err != nil {
		return "the accounts could not be changed"
	}
	linked := ""
	if st.Linked != nil {
		linked = st.Linked.UserID
	}
	moved := move(linked) || move(by)
	if !moved {
		// Neither is there (a recovery from the command line names nobody): the oldest administrator.
		var admin string
		_ = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE role = 'admin' AND is_agent = 0 AND deleted_at IS NULL AND disabled_at IS NULL ORDER BY created_at, id LIMIT 1`).Scan(&admin)
		moved = move(admin)
	}
	if !moved {
		var people int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL`).Scan(&people); err != nil {
			return "the accounts could not be read"
		}
		if people > 0 {
			// The local profile would sign in as a stranger with no role: staying as it is loses nothing.
			return "there is no account to give the local profile to"
		}
		// Nobody has ever signed in here: the first run makes the profile and its account.
		st.Mode = ModeEmbedded
		st.Linked, st.Verified = nil, nil
		return ""
	}
	if p.Name == "" {
		_ = tx.QueryRowContext(ctx, `SELECT display_name FROM users WHERE oidc_issuer = ? AND oidc_sub = ?`, LocalIdentity, p.Subject).Scan(&p.Name)
		if p.Name == "" {
			p.Name = "Writer"
		}
	}
	if err := putJSON(tx, KeyProfile, p); err != nil {
		return "the local profile could not be kept"
	}
	st.Mode = ModeEmbedded
	st.Linked, st.Verified = nil, nil
	return ""
}

// RequestEmbedded asks for the switch back to the embedded issuer at the next start, from outside the application:
// the recovery for an instance whose provider cannot be reached, run from the command line with the application
// stopped. by is empty.
func RequestEmbedded(ctx context.Context, d *db.DB) (changed bool, err error) {
	err = d.WithTx(ctx, func(tx *sql.Tx) error {
		st, err := LoadState(ctx, tx)
		if err != nil {
			return err
		}
		if st.Mode == ModeEmbedded {
			if st.Pending == nil {
				return nil
			}
			st.Pending = nil // a switch to the provider that was waiting is dropped
		} else {
			st.Pending = &Pending{To: ModeEmbedded, At: db.Now()}
		}
		changed = true
		return putJSON(tx, KeyIdP, st)
	})
	return changed, err
}

// Adopted is what AdoptAdmin did.
type Adopted struct {
	UserID, Name, Email string
}

// AdoptAdmin binds the local profile to one administrator account that is already in the database: the way into a
// data directory that came from a server instance (a restored backup), where the local profile has no account and the
// accounts belong to another identity provider. It is for the command line, run with the application stopped and the
// data directory's lock held (the caller's job), and it is never reached from the application or any route.
//
// who is typed by the person at the keyboard: an account id or an email address, exact (case aside). Nothing is
// matched automatically. It refuses unless the instance signs in through its embedded issuer with nothing waiting,
// the local profile owns no account, and who names exactly one live, enabled, human administrator. The account's
// sign-in identity becomes the local profile's (a profile is made when there is none, named after the account); its
// browser sessions are ended; the move is recorded in the audit log. The account keeps its role, projects and
// everything else.
func AdoptAdmin(ctx context.Context, d *db.DB, who string) (Adopted, error) {
	var out Adopted
	if who == "" {
		return out, errors.New("name the administrator: an account id or an email address")
	}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		st, err := LoadState(ctx, tx)
		if err != nil {
			return err
		}
		if st.Mode != ModeEmbedded {
			return errors.New("this instance signs in through an external provider; first run: scriobh desktop use-local-profile")
		}
		if st.Pending != nil {
			return errors.New("a switch of identity provider is waiting for the next start; run: scriobh desktop use-local-profile, which drops it, and then this command again")
		}
		var owned int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE oidc_issuer = ? AND deleted_at IS NULL`, LocalIdentity).Scan(&owned); err != nil {
			return err
		}
		if owned > 0 {
			return errors.New("the local profile already has an account; nothing was changed")
		}
		rows, err := tx.QueryContext(ctx, `SELECT id, role, display_name, COALESCE(email, ''), is_agent, disabled_at IS NOT NULL FROM users
			WHERE deleted_at IS NULL AND (id = ? OR email = ? COLLATE NOCASE)`, who, who)
		if err != nil {
			return err
		}
		type account struct {
			id, role, name, email string
			agent, disabled       bool
		}
		var found []account
		for rows.Next() {
			var a account
			if err := rows.Scan(&a.id, &a.role, &a.name, &a.email, &a.agent, &a.disabled); err != nil {
				rows.Close()
				return err
			}
			found = append(found, a)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(found) != 1 {
			return errors.New("no account has exactly that id or email address")
		}
		a := found[0]
		switch {
		case a.agent:
			return errors.New("that account is an agent, not a person")
		case a.disabled:
			return errors.New("that account is switched off")
		case a.role != "admin":
			return errors.New("that account is not an administrator; name one (the local profile administers the instance)")
		}
		// The profile's address is the account's: the sign-in writes the token's address to the account, so a profile with
		// another address (or none) would change or blank the administrator's own.
		_, email, _, _ := desktopidp.CleanProfile("x", a.email)
		var p desktopidp.Profile
		had, err := getJSON(ctx, tx, KeyProfile, &p)
		if err != nil {
			return err
		}
		if !had || p.Subject == "" {
			name, _, _, _ := desktopidp.CleanProfile(a.name, "")
			if name == "" {
				name = "Writer"
			}
			p = desktopidp.Profile{Subject: desktopidp.NewSubject(), Name: name, CreatedAt: db.Now()}
			had = false
		}
		if !had || p.Email != email {
			p.Email = email
			if err := putJSON(tx, KeyProfile, p); err != nil {
				return err
			}
		}
		now := db.Now()
		res, err := tx.ExecContext(ctx, `UPDATE users SET oidc_issuer = ?, oidc_sub = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, LocalIdentity, p.Subject, now, a.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("the account could not be changed")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, now, a.id); err != nil {
			return err
		}
		detail, _ := json.Marshal(map[string]any{"by": "command line", "user": a.id})
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(id, at, actor_id, action, target_type, target_id, detail_json) VALUES (?,?,?,?,?,?,?)`,
			db.NewID("aud"), now, nil, "desktop.profile.adopt", "users", a.id, string(detail)); err != nil {
			return err
		}
		out = Adopted{UserID: a.id, Name: a.name, Email: a.email}
		return nil
	})
	return out, err
}

// NeedsAdoption reports whether the instance signs in through its embedded issuer, has human accounts, and none of
// them is the local profile's: the state of a restored server database, which AdoptAdmin settles.
func NeedsAdoption(ctx context.Context, d *db.DB) bool {
	st, err := LoadState(ctx, d.Read())
	if err != nil || st.Mode != ModeEmbedded {
		return false
	}
	var local, people int
	if d.Read().QueryRowContext(ctx, `SELECT count(*) FROM users WHERE oidc_issuer = ? AND deleted_at IS NULL`, LocalIdentity).Scan(&local) != nil || local > 0 {
		return false
	}
	return d.Read().QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_agent = 0 AND deleted_at IS NULL`).Scan(&people) == nil && people > 0
}

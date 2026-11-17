package mail

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// The mail ops. The relay's settings and its test are the administrator's, and a person's address, digest and
// Kindle deliveries are that person's own: every one is for a person signed in with a browser. An API key of any
// scope is refused and none of them is an MCP tool: they decide what leaves the instance and to whom.

// ---- the relay -----------------------------------------------------------------------------------------------

// ConfigOut is the relay as an administrator sees it. The password is never part of it.
type ConfigOut struct {
	Enabled         bool     `json:"enabled" desc:"Mail is on: messages people asked for are sent through the relay. Off by default."`
	Ready           bool     `json:"ready" desc:"The settings are complete enough to send with."`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	Security        string   `json:"security" enum:"starttls,tls,none"`
	AllowPrivate    bool     `json:"allow_private" desc:"The relay may be on this machine or its own network. Link-local and cloud-metadata addresses are never allowed."`
	HasCA           bool     `json:"has_ca" desc:"A certificate authority of your own is trusted for the relay, instead of the system's."`
	Username        string   `json:"username"`
	PasswordSet     bool     `json:"password_set" desc:"A password is stored (sealed). It is never shown."`
	FromAddress     string   `json:"from_address" desc:"The envelope sender and From address. Recipients that need it on an approved list (Amazon does) are told this address."`
	FromName        string   `json:"from_name"`
	MaxAttachmentMB int      `json:"max_attachment_mb" desc:"The largest attachment, before encoding (base64 adds a third). Default 12."`
	KindleDomains   []string `json:"kindle_domains" desc:"The only domains a Send-to-Kindle address may have. Default kindle.com and free.kindle.com."`
	DisabledReason  string   `json:"disabled_reason,omitempty"`
	PausedByRestore bool     `json:"paused_by_restore" desc:"A restore switched mail off. Only switching it on again here lifts it."`
	LastTestAt      string   `json:"last_test_at,omitempty"`
	LastTestClass   string   `json:"last_test_class,omitempty" desc:"ok, or the class of the last test's failure."`
	SecretKey       bool     `json:"secret_key" desc:"This server has a secret key, so a password can be stored."`
	LinksEnabled    bool     `json:"links_enabled" desc:"The server's base address can go in a digest. When false, digests carry no link."`
	Hold            string   `json:"hold,omitempty" desc:"Sending is held after a failure about the settings (the class); a test or a change of settings lifts it."`
	HoldUntil       string   `json:"hold_until,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	Limits          struct {
		PerPersonQueue int `json:"per_person_queue"`
		TotalQueue     int `json:"total_queue"`
		KindlePerHour  int `json:"kindle_per_person_per_hour"`
		GlobalPerHour  int `json:"global_per_hour"`
		RatePerMin     int `json:"rate_per_minute"`
		MaxAttempts    int `json:"max_attempts"`
	} `json:"limits"`
}

// ConfigGetIn has nothing.
type ConfigGetIn struct{}

func (s *Service) configOut(cfg Config) ConfigOut {
	o := ConfigOut{Enabled: cfg.Enabled, Ready: cfg.Ready(), Host: cfg.Host, Port: cfg.Port, Security: cfg.Security, AllowPrivate: cfg.AllowPrivate, HasCA: cfg.TLSCAPEM != "",
		Username: cfg.Username, PasswordSet: cfg.PasswordSealed != "", FromAddress: cfg.FromAddress, FromName: cfg.FromName, MaxAttachmentMB: cfg.MaxAttachmentMB,
		KindleDomains: cfg.AllowedKindleDomains(), DisabledReason: cfg.DisabledReason, PausedByRestore: cfg.RestorePaused, LastTestAt: cfg.LastTestAt, LastTestClass: cfg.LastTestClass, UpdatedAt: cfg.UpdatedAt}
	if o.MaxAttachmentMB == 0 {
		o.MaxAttachmentMB = DefaultMaxAttachmentMB
	}
	if s != nil {
		o.SecretKey = s.o.Box.Enabled()
		o.LinksEnabled = s.baseURL() != ""
		if cl, until, ok := s.Hold(); ok {
			o.Hold, o.HoldUntil = string(cl), s.stamp(until)
		}
		l := s.lim
		o.Limits.PerPersonQueue, o.Limits.TotalQueue, o.Limits.KindlePerHour, o.Limits.GlobalPerHour, o.Limits.RatePerMin, o.Limits.MaxAttempts =
			l.PerPersonQueue, l.TotalQueue, l.KindlePerHour, l.GlobalPerHour, l.RatePerMin, l.MaxAttempts
	}
	return o
}

func svcOf(c *ops.Ctx) (*Service, bool) { return ops.Service[*Service](c.Env, ServiceName) }

func configGet(c *ops.Ctx, _ ConfigGetIn) (ConfigOut, error) {
	cfg, err := loadConfig(c, c.DB().Read())
	if err != nil {
		return ConfigOut{}, ops.Internal(err)
	}
	s, _ := svcOf(c)
	return s.configOut(cfg), nil
}

// ConfigSetIn changes the relay. Only the fields sent change.
type ConfigSetIn struct {
	Enabled         *bool     `json:"enabled,omitempty" desc:"Switch mail on or off. Switching it on needs a complete relay and clears a pause made by a restore. Switching it off drops the messages still waiting."`
	Host            *string   `json:"host,omitempty" desc:"The relay's name or IP address: no scheme, port or path. Changing it, the port or the user name drops the stored password unless you send one."`
	Port            *int      `json:"port,omitempty" desc:"Default 587 for starttls, 465 for tls, 25 for none."`
	Security        *string   `json:"security,omitempty" enum:"starttls,tls,none" desc:"starttls (the default: upgraded before anything is sent, refused if the relay does not offer it), tls (encrypted from the first byte) or none. none is for a relay on this machine only, never with a password, and needs allow_private."`
	AllowPrivate    *bool     `json:"allow_private,omitempty" desc:"Let the relay be on this machine or its own network (a home-lab relay). Without it only public addresses are allowed."`
	TLSCAPEM        *string   `json:"tls_ca_pem,omitempty" desc:"A certificate authority's certificate (PEM) to trust for the relay instead of the system's, for a relay with its own certificate. Never a private key. An empty string removes it."`
	Username        *string   `json:"username,omitempty" desc:"The relay's user name. Empty: no sign-in."`
	Password        *string   `json:"password,omitempty" desc:"Write-only: sealed with the server's secret key and never returned. An empty string removes the stored password."`
	FromAddress     *string   `json:"from_address,omitempty" desc:"The sender address, one plain address."`
	FromName        *string   `json:"from_name,omitempty" desc:"The sender's display name, up to 80 characters."`
	MaxAttachmentMB *int      `json:"max_attachment_mb,omitempty" desc:"1 to 40, default 12."`
	KindleDomains   *[]string `json:"kindle_domains,omitempty" desc:"The domains a Send-to-Kindle address may have: plain domains, no wildcard, never any. Empty restores the default, kindle.com and free.kindle.com. A person's address and what waits to be sent are checked against it."`
	ConfirmDomains  []string  `json:"confirm_domains,omitempty" desc:"Needed when kindle_domains adds a domain that is not allowed now: exactly the domains being added (an editor could then mail a manuscript to a mailbox there). Refused with 409 confirmation_required (data.mail_code, data.adding) when it is missing or differs; taking domains away, or going back to the default, needs none."`
}

// ConfigSetOut is the relay after the change.
type ConfigSetOut struct {
	Item            ConfigOut `json:"item"`
	PasswordCleared bool      `json:"password_cleared,omitempty" desc:"The stored password was dropped because the address it was sealed for changed."`
}

func invalid(field, msg string) error { return ops.Invalid(field, msg) }

func configSet(c *ops.Ctx, in ConfigSetIn) (ConfigSetOut, error) {
	s, ok := svcOf(c)
	if !ok {
		return ConfigSetOut{}, ops.Internal(errors.New("mail service is not running"))
	}
	var out ConfigSetOut
	var fields []string
	var wasOn, nowOn bool
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := loadConfig(c, tx)
		if err != nil {
			return err
		}
		wasOn = cur.Enabled
		cfg := cur
		before := struct {
			host, user, sec string
			port            int
		}{cur.Host, cur.Username, cur.Security, cur.Port}
		if in.Host != nil {
			h, err := CheckHost(*in.Host)
			if err != nil && strings.TrimSpace(*in.Host) != "" {
				return invalid("host", err.Error())
			}
			cfg.Host = h
			fields = append(fields, "host")
		}
		if in.Security != nil {
			switch *in.Security {
			case SecStartTLS, SecTLS, SecNone:
				cfg.Security = *in.Security
			default:
				return invalid("security", "Security is starttls, tls or none.")
			}
			fields = append(fields, "security")
		}
		if in.Port != nil {
			if *in.Port < 0 || *in.Port > 65535 {
				return invalid("port", "A port is 1 to 65535.")
			}
			cfg.Port = *in.Port
			fields = append(fields, "port")
		}
		if cfg.Port == 0 && cfg.Host != "" {
			cfg.Port = defaultPort(cfg.Security)
		}
		if in.AllowPrivate != nil {
			cfg.AllowPrivate = *in.AllowPrivate
			fields = append(fields, "allow_private")
		}
		if in.Username != nil {
			u := strings.TrimSpace(*in.Username)
			if !printableLine(u, 255) {
				return invalid("username", "A user name is up to 255 characters with no line break.")
			}
			cfg.Username = u
			fields = append(fields, "username")
		}
		if in.FromAddress != nil {
			if strings.TrimSpace(*in.FromAddress) == "" {
				cfg.FromAddress = ""
			} else {
				a, err := ValidAddress(*in.FromAddress)
				if err != nil {
					return invalid("from_address", err.Error())
				}
				cfg.FromAddress = a
			}
			fields = append(fields, "from_address")
		}
		if in.FromName != nil {
			n := strings.TrimSpace(*in.FromName)
			if !printableLine(n, 80) {
				return invalid("from_name", "A sender name is up to 80 characters with no line break.")
			}
			cfg.FromName = n
			fields = append(fields, "from_name")
		}
		if in.MaxAttachmentMB != nil {
			if *in.MaxAttachmentMB < 1 || *in.MaxAttachmentMB > MaxAttachmentMBLimit {
				return invalid("max_attachment_mb", fmt.Sprintf("The largest attachment is 1 to %d MB.", MaxAttachmentMBLimit))
			}
			cfg.MaxAttachmentMB = *in.MaxAttachmentMB
			fields = append(fields, "max_attachment_mb")
		}
		if in.KindleDomains != nil {
			d, err := CheckKindleDomains(*in.KindleDomains)
			if err != nil {
				return invalid("kindle_domains", err.Error())
			}
			// Widening is a deliberate step, decided here against the list in force before it is replaced: the domains this
			// adds must be named back (confirm_domains), or the change is refused whoever asks and however.
			if adding := addedDomains(cfg.AllowedKindleDomains(), d); len(adding) > 0 && !sameDomains(adding, in.ConfirmDomains) {
				return ops.ConflictWith("This lets an editor send a manuscript to a mailbox at "+strings.Join(adding, ", ")+". Name exactly these domains in confirm_domains to confirm.",
					map[string]any{"mail_code": "confirmation_required", "confirm_field": "confirm_domains", "adding": adding})
			}
			cfg.KindleDomains = d
			fields = append(fields, "kindle_domains")
		}
		if in.TLSCAPEM != nil {
			pemText := strings.TrimSpace(*in.TLSCAPEM)
			if pemText != "" {
				if err := checkCA(pemText); err != nil {
					return invalid("tls_ca_pem", err.Error())
				}
			}
			cfg.TLSCAPEM = pemText
			fields = append(fields, "tls_ca_pem")
		}
		if cfg.Security == SecNone {
			if cfg.Host != "" && !isLoopbackHost(cfg.Host) {
				return invalid("security", "Plain text is only allowed to a relay on this machine (localhost or a loopback address).")
			}
			if cfg.Username != "" || (in.Password != nil && *in.Password != "") {
				return invalid("security", "A password is never sent over a plain connection. Use starttls or tls, or remove the user name.")
			}
			if !cfg.AllowPrivate {
				return invalid("allow_private", "A relay on this machine needs allow_private.")
			}
		}
		// The password is sealed for the address and user name it was entered for.
		moved := cfg.Host != before.host || cfg.Port != before.port || cfg.Username != before.user
		switch {
		case in.Password != nil && *in.Password != "":
			p := *in.Password
			if utf8.RuneCountInString(p) > 512 || !utf8.ValidString(p) || checkHeaderValue(p) != nil {
				return invalid("password", "A password is up to 512 characters with no line break.")
			}
			if cfg.Host == "" || cfg.Username == "" {
				return invalid("password", "Give the relay's host and the user name before the password.")
			}
			sealed, err := s.o.Box.Seal([]byte(p), sealContext(cfg.Host, cfg.Port, cfg.Username))
			if err != nil {
				return ops.Conflict(err.Error())
			}
			cfg.PasswordSealed = sealed
			fields = append(fields, "password")
		case in.Password != nil: // an empty string removes it
			cfg.PasswordSealed = ""
			fields = append(fields, "password")
		case moved && cfg.PasswordSealed != "":
			cfg.PasswordSealed = ""
			out.PasswordCleared = true
		case cfg.PasswordSealed != "" && cfg.Username == "":
			cfg.PasswordSealed = ""
		}
		now := s.now()
		if in.Enabled != nil {
			fields = append(fields, "enabled")
			if *in.Enabled {
				if !cfg.Ready() {
					return ops.Conflict("Fill in the relay's host, the sender address, and the password if there is a user name, before switching mail on.")
				}
				if cfg.PasswordSealed != "" && !s.o.Box.CanOpen(cfg.PasswordSealed, sealContext(cfg.Host, cfg.Port, cfg.Username)) {
					return ops.Conflict(errCannotOpenPassword)
				}
				cfg.Enabled, cfg.DisabledReason, cfg.RestorePaused = true, "", false
			} else {
				cfg.Enabled = false
				if wasOn {
					cfg.DisabledReason = "Switched off by an administrator."
				}
			}
		} else if cfg.Enabled && !cfg.Ready() {
			cfg.Enabled, cfg.DisabledReason = false, "The relay's settings are no longer complete."
		}
		nowOn = cfg.Enabled
		cfg.UpdatedAt = s.stamp(now)
		if _, err := tx.ExecContext(c, `INSERT INTO mail_config(id, enabled, host, port, security, allow_private, tls_ca_pem, username, password_sealed, from_address, from_name, max_attachment_mb,
				disabled_reason, restore_paused, last_test_at, last_test_class, kindle_domains, updated_by, updated_at)
			VALUES (1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(id) DO UPDATE SET enabled=excluded.enabled, host=excluded.host, port=excluded.port, security=excluded.security, allow_private=excluded.allow_private,
				tls_ca_pem=excluded.tls_ca_pem, username=excluded.username, password_sealed=excluded.password_sealed, from_address=excluded.from_address, from_name=excluded.from_name,
				max_attachment_mb=excluded.max_attachment_mb, kindle_domains=excluded.kindle_domains, disabled_reason=excluded.disabled_reason, restore_paused=excluded.restore_paused, updated_by=excluded.updated_by, updated_at=excluded.updated_at`,
			b2i(cfg.Enabled), cfg.Host, cfg.Port, cfg.Security, b2i(cfg.AllowPrivate), cfg.TLSCAPEM, cfg.Username, cfg.PasswordSealed, cfg.FromAddress, cfg.FromName, cfg.MaxAttachmentMB,
			cfg.DisabledReason, b2i(cfg.RestorePaused), cfg.LastTestAt, cfg.LastTestClass, cfg.KindleDomains, c.P.UserID, cfg.UpdatedAt); err != nil {
			return err
		}
		if wasOn && !nowOn {
			// What waited while mail was on is stale once it is off: nothing is sent late.
			if _, err := tx.ExecContext(c, `UPDATE mail_outbox SET state = 'dead', last_error = 'paused', finished_at = ?, claimed_at = '', recipient = '', subject = '', body = '', file_name = ''
				WHERE state IN ('pending','sending')`, s.stamp(now)); err != nil {
				return err
			}
		}
		if !wasOn && nowOn {
			if err := resetDigests(tx, now); err != nil {
				return err
			}
			if err := resetProgress(tx, now); err != nil {
				return err
			}
		}
		out.Item = s.configOut(cfg)
		return nil
	})
	if err != nil {
		var ae *ops.Error
		if errors.As(err, &ae) {
			return ConfigSetOut{}, err
		}
		return ConfigSetOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "mail.config.set", TargetType: "mail", TargetID: "config", Detail: map[string]any{"fields": fields, "enabled": nowOn}})
	s.ClearHold()
	if wasOn && !nowOn {
		s.removeOrphanFilesNow(c)
	}
	return out, nil
}

const errCannotOpenPassword = "The stored password cannot be read with this installation's secret key. Enter it again."

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// removeOrphanFilesNow removes the files of messages that were just dropped (the hourly sweep leaves new files alone).
func (s *Service) removeOrphanFilesNow(ctx context.Context) { s.removeOrphans(ctx, 0) }

// ---- the test ------------------------------------------------------------------------------------------------

// TestIn has nothing: the message goes to the administrator's own address, never to one in the request.
type TestIn struct{}

// TestOut is what the test found.
type TestOut struct {
	OK      bool   `json:"ok"`
	Class   string `json:"class,omitempty" desc:"When it failed: config, blocked, dns, refused, network, timeout, tls, tls_cert, starttls, auth, temporary, rejected, too_large or protocol."`
	Message string `json:"message" desc:"What that means, in a sentence."`
	To      string `json:"to" desc:"Your own account address, the only place a test goes."`
}

// ClassMessage says what a class means, for a person. It never repeats what a relay said.
func ClassMessage(c Class) string {
	switch c {
	case "":
		return "The relay accepted the message."
	case ClassConfig:
		return "Mail is not set up completely, or the stored password cannot be read."
	case ClassBlocked:
		return "The relay's address is not allowed: link-local and cloud-metadata addresses never are, and a private or loopback address needs the allow_private setting."
	case ClassDNS:
		return "The relay's name did not resolve."
	case ClassRefused:
		return "Nothing is listening at the relay's address and port."
	case ClassNetwork:
		return "The connection to the relay broke."
	case ClassTimeout:
		return "The relay took too long to answer."
	case ClassTLS:
		return "The secure connection could not be set up. Check the port and the security setting."
	case ClassCert:
		return "The relay's certificate was not accepted: it is not signed by a trusted authority, or it is for another name. Add the authority's certificate in the settings, or use a name the certificate carries."
	case ClassNoStartTLS:
		return "The relay does not offer STARTTLS, and plain text is not allowed. Use tls on port 465, or a relay that offers STARTTLS."
	case ClassAuth:
		return "The relay refused the user name and password, or offers no safe way to sign in."
	case ClassTemporary:
		return "The relay asked to try again later."
	case ClassRejected:
		return "The relay refused the message."
	case ClassTooLarge:
		return "The message is larger than the relay accepts."
	case ClassProtocol:
		return "The relay answered in a way that is not SMTP, or at too great a length."
	}
	return "The message could not be sent."
}

const testGap = 5 * time.Second

func configTest(c *ops.Ctx, _ TestIn) (TestOut, error) {
	s, ok := svcOf(c)
	if !ok {
		return TestOut{}, ops.Internal(errors.New("mail service is not running"))
	}
	var email sql.NullString
	if err := c.DB().Read().QueryRowContext(c, `SELECT email FROM users WHERE id = ? AND deleted_at IS NULL AND disabled_at IS NULL`, c.P.UserID).Scan(&email); err != nil {
		return TestOut{}, ops.NotFound("page")
	}
	to, err := ValidAddress(email.String)
	if err != nil {
		return TestOut{}, ops.Conflict("Your account has no email address, so there is nowhere to send a test. A test only ever goes to your own address.")
	}
	cfg, err := loadConfig(c, c.DB().Read())
	if err != nil {
		return TestOut{}, ops.Internal(err)
	}
	if !cfg.Ready() || cfg.Host == "" {
		return TestOut{}, ops.Conflict("Fill in the relay's host and the sender address first.")
	}
	s.mu.Lock()
	wait := s.lastTest.Add(testGap).Sub(s.now())
	if wait <= 0 {
		s.lastTest = s.now()
	}
	s.mu.Unlock()
	if wait > 0 {
		return TestOut{}, ops.RateLimited(int(wait/time.Second) + 1)
	}
	out := TestOut{To: to}
	class := Class("")
	relay, err := openRelay(s.o.Box, cfg)
	if err != nil {
		class = ClassConfig
	} else {
		id := db.NewID("mel")
		msg := &Message{From: cfg.FromAddress, FromName: cfg.FromName, To: to, Subject: "Scriobh mail test", MessageID: s.messageID(cfg, id), Date: s.now(), Auto: true,
			Body: "This is a test from the mail settings of your Scríobh server.\n\nIt was sent because you pressed Test, to your own address only. Nothing else was sent, and nothing from any manuscript is in it.\n"}
		tctx, cancel := context.WithTimeout(c, 45*time.Second)
		serr := s.client.Send(tctx, relay, msg, SendOptions{})
		cancel()
		var e *Error
		if serr != nil {
			if !errors.As(serr, &e) {
				e = &Error{Class: ClassInternal}
			}
			class = e.Class
		}
	}
	out.OK, out.Class, out.Message = class == "", string(class), ClassMessage(class)
	rec := string(class)
	if class == "" {
		rec = "ok"
	}
	if _, err := c.DB().Writer().ExecContext(c, `UPDATE mail_config SET last_test_at = ?, last_test_class = ? WHERE id = 1`, s.stamp(s.now()), rec); err != nil {
		return out, ops.Internal(err)
	}
	if class == "" {
		s.ClearHold()
	}
	c.Audit(ops.AuditEntry{Action: "mail.config.test", TargetType: "mail", TargetID: "config", Detail: map[string]any{"ok": class == "", "class": string(class)}})
	return out, nil
}

// ---- the outbox, for the administrator -------------------------------------------------------------------------

// OutboxIn has nothing.
type OutboxIn struct{}

// OutboxEntry is one message as an administrator sees it: no address, no subject, no text.
type OutboxEntry struct {
	ID         string `json:"id"`
	Kind       string `json:"kind" enum:"kindle,digest,progress"`
	State      string `json:"state" enum:"pending,sending,sent,dead"`
	Attempts   int    `json:"attempts"`
	Error      string `json:"error,omitempty" desc:"The class of the last failure, never what the relay said."`
	Domain     string `json:"recipient_domain"`
	Bytes      int64  `json:"attachment_bytes,omitempty"`
	CreatedAt  string `json:"created_at"`
	FinishedAt string `json:"finished_at,omitempty"`
}

// OutboxOut is the state of the outbox.
type OutboxOut struct {
	Pending int           `json:"pending"`
	Sending int           `json:"sending"`
	Sent    int           `json:"sent"`
	Dead    int           `json:"dead"`
	Digests int           `json:"digest_subscribers" desc:"People who turned the digest on."`
	Items   []OutboxEntry `json:"items" desc:"The newest 30."`
}

func outboxStatus(c *ops.Ctx, _ OutboxIn) (OutboxOut, error) {
	q := c.DB().Read()
	var out OutboxOut
	rows, err := q.QueryContext(c, `SELECT state, count(*) FROM mail_outbox GROUP BY state`)
	if err != nil {
		return out, ops.Internal(err)
	}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return out, ops.Internal(err)
		}
		switch st {
		case "pending":
			out.Pending = n
		case "sending":
			out.Sending = n
		case "sent":
			out.Sent = n
		case "dead":
			out.Dead = n
		}
	}
	rows.Close()
	_ = q.QueryRowContext(c, `SELECT count(*) FROM mail_digest`).Scan(&out.Digests)
	rs, err := q.QueryContext(c, `SELECT id, kind, state, attempts, last_error, recipient_domain, file_bytes, created_at, finished_at FROM mail_outbox ORDER BY created_at DESC, id DESC LIMIT 30`)
	if err != nil {
		return out, ops.Internal(err)
	}
	defer rs.Close()
	out.Items = []OutboxEntry{}
	for rs.Next() {
		var e OutboxEntry
		if err := rs.Scan(&e.ID, &e.Kind, &e.State, &e.Attempts, &e.Error, &e.Domain, &e.Bytes, &e.CreatedAt, &e.FinishedAt); err != nil {
			return out, ops.Internal(err)
		}
		out.Items = append(out.Items, e)
	}
	return out, nil
}

// ---- the digest ------------------------------------------------------------------------------------------------

// DigestGetIn has nothing.
type DigestGetIn struct{}

// DigestOut is a person's digest setting.
type DigestOut struct {
	MailAvailable bool   `json:"mail_available" desc:"The administrator has switched mail on."`
	Enabled       bool   `json:"enabled"`
	Frequency     string `json:"frequency,omitempty" enum:"daily,weekly"`
	NeedsConfirm  bool   `json:"needs_confirm,omitempty" desc:"Your account address changed since you turned the digest on, so it stopped. Turn it on again to send it to the new address."`
	Email         string `json:"email,omitempty" desc:"Your account address from the sign-in provider: the only place a digest goes."`
	Next          string `json:"next_period_starts,omitempty" desc:"The first digest is for what happens after you turned it on, sent at the next morning or Monday morning in your time zone."`
	Contents      string `json:"contents" desc:"What a digest says."`
}

const digestContents = "Counts of new mentions, comments and suggestions waiting for you, by project title, and a link to this server. Never the words of a manuscript or a comment, never a document's title, never another person's name. Nothing is sent when there is nothing new."

func mailAvailable(ctx context.Context, d *db.DB) bool {
	cfg, err := loadConfig(ctx, d.Read())
	return err == nil && cfg.Enabled && cfg.Ready()
}

func digestGet(c *ops.Ctx, _ DigestGetIn) (DigestOut, error) {
	out := DigestOut{MailAvailable: mailAvailable(c, c.DB()), Contents: digestContents}
	var email sql.NullString
	_ = c.DB().Read().QueryRowContext(c, `SELECT email FROM users WHERE id = ?`, c.P.UserID).Scan(&email)
	out.Email = email.String
	var bound string
	err := c.DB().Read().QueryRowContext(c, `SELECT frequency, address FROM mail_digest WHERE user_id = ?`, c.P.UserID).Scan(&out.Frequency, &bound)
	if err == nil {
		out.Enabled = true
		if !strings.EqualFold(bound, email.String) {
			out.Enabled, out.NeedsConfirm = false, true
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, ops.Internal(err)
	}
	return out, nil
}

// DigestSetIn turns the digest on or off.
type DigestSetIn struct {
	Enabled   bool   `json:"enabled" desc:"On or off. Off removes your subscription."`
	Frequency string `json:"frequency,omitempty" enum:"daily,weekly" desc:"daily (each morning) or weekly (Monday morning), in your time zone. Required when turning it on."`
}

// Validate checks the frequency.
func (in *DigestSetIn) Validate() error {
	if !in.Enabled {
		return nil
	}
	if in.Frequency != Daily && in.Frequency != Weekly {
		return ops.Invalid("frequency", "Choose how often: daily or weekly.")
	}
	return nil
}

func digestSet(c *ops.Ctx, in DigestSetIn) (DigestOut, error) {
	if !in.Enabled {
		err := c.DB().WithTx(c, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(c, `DELETE FROM mail_digest WHERE user_id = ?`, c.P.UserID)
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(c, `UPDATE mail_outbox SET state = 'dead', last_error = 'cancelled', finished_at = ?, recipient = '', subject = '', body = '', claimed_at = ''
				WHERE person_id = ? AND kind = 'digest' AND state = 'pending'`, db.Now(), c.P.UserID)
			return err
		})
		if err != nil {
			return DigestOut{}, ops.Internal(err)
		}
		return digestGet(c, DigestGetIn{})
	}
	var email sql.NullString
	var agent int
	var tz string
	if err := c.DB().Read().QueryRowContext(c, `SELECT email, is_agent, timezone FROM users WHERE id = ? AND deleted_at IS NULL AND disabled_at IS NULL`, c.P.UserID).Scan(&email, &agent, &tz); err != nil {
		return DigestOut{}, ops.NotFound("page")
	}
	if agent != 0 {
		return DigestOut{}, ops.Forbidden("An agent account has no digest.")
	}
	if _, err := ValidAddress(email.String); err != nil {
		return DigestOut{}, ops.Conflict("Your account has no email address from the sign-in provider, so there is nowhere to send a digest.")
	}
	if !mailAvailable(c, c.DB()) {
		return DigestOut{}, ops.ConflictWith("Email is not switched on for this server. Ask its administrator.", map[string]any{"reason": "mail_off"})
	}
	now := db.NowTime()
	key := periodKey(in.Frequency, now.In(locationOf(tz)))
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var cur, bound string
		err := tx.QueryRowContext(c, `SELECT frequency, address FROM mail_digest WHERE user_id = ?`, c.P.UserID).Scan(&cur, &bound)
		if err == nil && cur == in.Frequency && strings.EqualFold(bound, email.String) {
			return nil // already on, as asked, for this address: the schedule is left alone
		}
		// A new opt-in, a new frequency, or a new address: it starts now, for this address, and nothing earlier is sent.
		_, err = tx.ExecContext(c, `INSERT INTO mail_digest(user_id, frequency, address, last_period, last_cursor, enabled_at, updated_at) VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET frequency = excluded.frequency, address = excluded.address, last_period = excluded.last_period, last_cursor = excluded.last_cursor, updated_at = excluded.updated_at`,
			c.P.UserID, in.Frequency, strings.ToLower(email.String), key, db.Format(now), db.Format(now), db.Format(now))
		return err
	})
	if err != nil {
		return DigestOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "mail.digest.set", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"frequency": in.Frequency}})
	return digestGet(c, DigestGetIn{})
}

// ---- Send to Kindle -------------------------------------------------------------------------------------------

// KindleAddressIn has nothing.
type KindleAddressIn struct{}

// KindleAddressOut is a person's stored address and the words that go with it.
type KindleAddressOut struct {
	Address       string   `json:"address,omitempty" desc:"Your own Send-to-Kindle address (name@kindle.com), kept only so your own deliveries can go to it."`
	MailAvailable bool     `json:"mail_available"`
	Sender        string   `json:"sender,omitempty" desc:"The address deliveries come from. Add it to your Approved Personal Document E-mail List at Amazon first."`
	Domains       []string `json:"allowed_domains" desc:"The domains a Send-to-Kindle address may have on this server."`
	SenderNote    string   `json:"sender_note" desc:"What to do at Amazon before the first send."`
}

func (s *Service) kindleAddressOut(ctx context.Context, d *db.DB, user string) (KindleAddressOut, error) {
	out := KindleAddressOut{SenderNote: KindleSenderNote}
	cfg, err := loadConfig(ctx, d.Read())
	if err != nil {
		return out, err
	}
	out.MailAvailable = cfg.Enabled && cfg.Ready()
	out.Domains = cfg.AllowedKindleDomains()
	if out.MailAvailable {
		out.Sender = cfg.FromAddress
	}
	err = d.Read().QueryRowContext(ctx, `SELECT address FROM kindle_addresses WHERE user_id = ?`, user).Scan(&out.Address)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	return out, nil
}

func kindleAddressGet(c *ops.Ctx, _ KindleAddressIn) (KindleAddressOut, error) {
	s, _ := svcOf(c)
	out, err := s.kindleAddressOut(c, c.DB(), c.P.UserID)
	if err != nil {
		return out, ops.Internal(err)
	}
	return out, nil
}

// KindleAddressSetIn stores an address.
type KindleAddressSetIn struct {
	Address string `json:"address" desc:"One address: name@kindle.com. No list, no display name."`
}

func kindleAddressSet(c *ops.Ctx, in KindleAddressSetIn) (KindleAddressOut, error) {
	a, err := ValidAddress(in.Address)
	if err != nil {
		return KindleAddressOut{}, ops.Invalid("address", "Give one plain address, like name@kindle.com: no list, no name, no brackets.")
	}
	cfg, err := loadConfig(c, c.DB().Read())
	if err != nil {
		return KindleAddressOut{}, ops.Internal(err)
	}
	if !cfg.KindleAddressAllowed(a) {
		return KindleAddressOut{}, ops.Invalid("address", "Send to Kindle goes to an address on one of these domains: "+strings.Join(cfg.AllowedKindleDomains(), ", ")+". Your Kindle's address is shown in your Amazon account.")
	}
	now := db.Now()
	var dropped []string
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var cur string
		_ = tx.QueryRowContext(c, `SELECT address FROM kindle_addresses WHERE user_id = ?`, c.P.UserID).Scan(&cur)
		if _, err := tx.ExecContext(c, `INSERT INTO kindle_addresses(user_id, address, created_at, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET address = excluded.address, updated_at = excluded.updated_at`, c.P.UserID, a, now, now); err != nil {
			return err
		}
		if cur != "" && !strings.EqualFold(cur, a) {
			var err error
			dropped, err = cancelKindle(tx, c.P.UserID) // what waited for the old address is not sent to the new one
			return err
		}
		return nil
	})
	if err != nil {
		return KindleAddressOut{}, ops.Internal(err)
	}
	s0, _ := svcOf(c)
	s0.removeFiles(dropped)
	c.Audit(ops.AuditEntry{Action: "kindle.address.set", TargetType: "user", TargetID: c.P.UserID, Detail: map[string]any{"domain": DomainOf(a)}})
	s, _ := svcOf(c)
	out, err := s.kindleAddressOut(c, c.DB(), c.P.UserID)
	if err != nil {
		return out, ops.Internal(err)
	}
	return out, nil
}

// KindleAddressDeleteOut says the address is gone.
type KindleAddressDeleteOut struct {
	Deleted bool `json:"deleted"`
}

func kindleAddressDelete(c *ops.Ctx, _ KindleAddressIn) (KindleAddressDeleteOut, error) {
	var dropped []string
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(c, `DELETE FROM kindle_addresses WHERE user_id = ?`, c.P.UserID); err != nil {
			return err
		}
		var err error
		dropped, err = cancelKindle(tx, c.P.UserID)
		return err
	})
	if err != nil {
		return KindleAddressDeleteOut{}, ops.Internal(err)
	}
	s, _ := svcOf(c)
	s.removeFiles(dropped)
	c.Audit(ops.AuditEntry{Action: "kindle.address.delete", TargetType: "user", TargetID: c.P.UserID})
	return KindleAddressDeleteOut{Deleted: true}, nil
}

// KindleProjectIn names the project of a preview.
type KindleProjectIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in KindleProjectIn) ProjectRef() string { return in.ProjectID }

// KindlePreviewOut is exactly what a send would do.
type KindlePreviewOut struct {
	Token      string   `json:"token" desc:"Shown once. kindle.send needs it, within 15 minutes, and uses it once."`
	ExpiresAt  string   `json:"expires_at"`
	Recipient  string   `json:"recipient" desc:"Your own stored address: the only place it goes. There is no way to name another."`
	FileName   string   `json:"file_name"`
	Format     string   `json:"format" desc:"EPUB, which a Kindle accepts. MOBI and AZW3 are not made."`
	Bytes      int64    `json:"bytes"`
	MaxBytes   int64    `json:"max_bytes" desc:"The largest attachment this server sends."`
	Documents  int      `json:"documents"`
	Sender     string   `json:"sender" desc:"The address it comes from: it must be on your Approved Personal Document E-mail List at Amazon."`
	SenderNote string   `json:"sender_note"`
	Forced     []string `json:"forced_options" desc:"What is held back whatever a preset says."`
	Notice     string   `json:"notice" desc:"A plain statement that the manuscript leaves this server."`
	InputHash  string   `json:"input_hash" desc:"The compile's manifest hash: the same hash is the same content."`
	SHA256     string   `json:"sha256" desc:"The file's SHA-256."`
	Preset     string   `json:"preset"`
	Warnings   int      `json:"warnings"`
	Kindle     bool     `json:"-"`
}

func (s *Service) compile(ctx context.Context, c *ops.Ctx, project string) (*Edition, error) {
	if s.o.Compile == nil {
		return nil, ops.Conflict("Compiling is not available on this server.")
	}
	ed, err := s.o.Compile(ctx, c.P, EditionRequest{ProjectID: project, Preset: KindlePreset, Options: KindleOptions()})
	if err != nil {
		ae := ops.AsError(err)
		if ae.Code == ops.CodeInternal {
			return nil, ops.Conflict("The edition could not be built.")
		}
		return nil, ae
	}
	return ed, nil
}

func (s *Service) kindleReady(c *ops.Ctx) (Config, string, error) {
	cfg, err := loadConfig(c, c.DB().Read())
	if err != nil {
		return cfg, "", ops.Internal(err)
	}
	if !cfg.Enabled || !cfg.Ready() {
		return cfg, "", ops.ConflictWith("Email is not switched on for this server. Ask its administrator.", map[string]any{"reason": "mail_off"})
	}
	addr, err := s.KindleAddress(c, c.P.UserID)
	if err != nil {
		return cfg, "", ops.Internal(err)
	}
	if addr == "" {
		return cfg, "", ops.ConflictWith("Save your Send-to-Kindle address first.", map[string]any{"reason": "no_kindle_address"})
	}
	if !cfg.KindleAddressAllowed(addr) {
		return cfg, "", ops.ConflictWith("Your Send-to-Kindle address is not on a domain this server allows ("+strings.Join(cfg.AllowedKindleDomains(), ", ")+"). Save a new one.", map[string]any{"reason": "kindle_domain"})
	}
	return cfg, addr, nil
}

func limitError(le *LimitError) error {
	switch le.Scope {
	case "file_size":
		return &ops.Error{Code: ops.CodeTooLarge, Message: "The edition is larger than this server sends by mail."}
	case "person_rate", "global_rate":
		return ops.RateLimited(le.RetryAfter)
	}
	return ops.ConflictWith("Earlier deliveries are still waiting to be sent. Try again when they have gone.", map[string]any{"reason": "queue_full"})
}

func kindlePreview(c *ops.Ctx, in KindleProjectIn) (KindlePreviewOut, error) {
	s, ok := svcOf(c)
	if !ok {
		return KindlePreviewOut{}, ops.Internal(errors.New("mail service is not running"))
	}
	cfg, addr, err := s.kindleReady(c)
	if err != nil {
		return KindlePreviewOut{}, err
	}
	if err := s.CheckQuota(c, c.DB().Read(), c.P.UserID, KindKindle, 0); err != nil {
		var le *LimitError
		if errors.As(err, &le) {
			return KindlePreviewOut{}, limitError(le)
		}
		return KindlePreviewOut{}, ops.Internal(err)
	}
	ed, err := s.compile(c, c, in.ProjectID)
	if err != nil {
		return KindlePreviewOut{}, err
	}
	if int64(len(ed.Body)) > cfg.MaxAttachmentBytes() {
		return KindlePreviewOut{}, &ops.Error{Code: ops.CodeTooLarge, Message: fmt.Sprintf("The edition is %.1f MB and this server sends at most %d MB by mail.", float64(len(ed.Body))/(1<<20), cfg.MaxAttachmentBytes()>>20)}
	}
	token, hash, err := newToken()
	if err != nil {
		return KindlePreviewOut{}, ops.Internal(err)
	}
	name := kindleFileName(ed.Filename)
	expires := s.previewExpiry()
	sum := ed.SHA256
	if sum == "" {
		h := sha256.Sum256(ed.Body)
		sum = hex.EncodeToString(h[:])
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		// A person keeps a handful of live previews.
		if _, err := tx.ExecContext(c, `DELETE FROM kindle_previews WHERE user_id = ? AND token_hash NOT IN (SELECT token_hash FROM kindle_previews WHERE user_id = ? ORDER BY created_at DESC LIMIT 4)`, c.P.UserID, c.P.UserID); err != nil {
			return err
		}
		_, err := tx.ExecContext(c, `INSERT INTO kindle_previews(token_hash, user_id, project_id, address, input_hash, file_name, file_bytes, expires_at, created_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			hash, c.P.UserID, in.ProjectID, addr, ed.InputHash, name, len(ed.Body), expires, db.Now())
		return err
	})
	if err != nil {
		return KindlePreviewOut{}, ops.Internal(err)
	}
	return KindlePreviewOut{Token: token, ExpiresAt: expires, Recipient: addr, FileName: name, Format: "EPUB", Bytes: int64(len(ed.Body)), MaxBytes: cfg.MaxAttachmentBytes(),
		Documents: ed.Documents, Sender: cfg.FromAddress, SenderNote: KindleSenderNote, Forced: KindleForced, Notice: KindleNotice, InputHash: ed.InputHash, SHA256: sum, Preset: KindlePreset}, nil
}

// KindleSendIn sends a previewed edition. There is no recipient field: the edition goes to the stored address of
// the person who asks, and nowhere else.
type KindleSendIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	Token     string `json:"token" desc:"The token kindle.preview returned."`
}

// ProjectRef names the project.
func (in KindleSendIn) ProjectRef() string { return in.ProjectID }

// KindleSendOut says what was queued.
type KindleSendOut struct {
	Queued          bool   `json:"queued"`
	ID              string `json:"id" desc:"The outbox entry."`
	FileName        string `json:"file_name"`
	Bytes           int64  `json:"bytes"`
	RecipientDomain string `json:"recipient_domain"`
	Notice          string `json:"notice"`
}

func kindleSend(c *ops.Ctx, in KindleSendIn) (KindleSendOut, error) {
	s, ok := svcOf(c)
	if !ok {
		return KindleSendOut{}, ops.Internal(errors.New("mail service is not running"))
	}
	if len(in.Token) < 20 || len(in.Token) > 100 {
		return KindleSendOut{}, ops.Invalid("token", "Preview first, then send with the token the preview gives.")
	}
	cfg, addr, err := s.kindleReady(c)
	if err != nil {
		return KindleSendOut{}, err
	}
	hash := tokenHash(in.Token)
	var pAddr, pInput, pName string
	var pBytes int64
	var pExp string
	err = c.DB().Read().QueryRowContext(c, `SELECT address, input_hash, file_name, file_bytes, expires_at FROM kindle_previews WHERE token_hash = ? AND user_id = ? AND project_id = ?`,
		hash, c.P.UserID, in.ProjectID).Scan(&pAddr, &pInput, &pName, &pBytes, &pExp)
	if errors.Is(err, sql.ErrNoRows) {
		return KindleSendOut{}, ops.ConflictWith("That preview is not valid any more (used, expired or for another project). Preview again.", map[string]any{"reason": "preview_gone"})
	}
	if err != nil {
		return KindleSendOut{}, ops.Internal(err)
	}
	if pExp < s.stamp(s.now()) {
		return KindleSendOut{}, ops.ConflictWith("That preview has expired. Preview again.", map[string]any{"reason": "preview_gone"})
	}
	if !strings.EqualFold(pAddr, addr) {
		return KindleSendOut{}, ops.ConflictWith("Your Kindle address changed since the preview. Preview again.", map[string]any{"reason": "preview_changed"})
	}
	ed, err := s.compile(c, c, in.ProjectID)
	if err != nil {
		return KindleSendOut{}, err
	}
	if ed.InputHash != pInput || kindleFileName(ed.Filename) != pName {
		return KindleSendOut{}, ops.ConflictWith("The edition changed since the preview. Preview again to see what would be sent.", map[string]any{"reason": "preview_changed"})
	}
	sum := sha256.Sum256(ed.Body)
	fileSHA := hex.EncodeToString(sum[:])
	if int64(len(ed.Body)) > cfg.MaxAttachmentBytes() {
		return KindleSendOut{}, &ops.Error{Code: ops.CodeTooLarge, Message: fmt.Sprintf("The edition is larger than the %d MB this server sends by mail.", cfg.MaxAttachmentBytes()>>20)}
	}
	id := db.NewID("mel")
	if err := s.writeFile(id, ed.Body); err != nil {
		return KindleSendOut{}, ops.Internal(err)
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(c, `DELETE FROM kindle_previews WHERE token_hash = ?`, hash)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errPreviewUsed
		}
		var cur string
		if err := tx.QueryRowContext(c, `SELECT address FROM kindle_addresses WHERE user_id = ?`, c.P.UserID).Scan(&cur); err != nil || !strings.EqualFold(cur, addr) {
			return errPreviewUsed
		}
		subject := "Scriobh edition"
		body := "A copy of a Scríobh project, sent because the owner of this Kindle address asked for it to be.\n"
		if err := s.EnqueueTx(c, tx, Job{ID: id, Kind: KindKindle, PersonID: c.P.UserID, ProjectID: in.ProjectID, Recipient: addr, Subject: subject, Body: body,
			File: &File{Name: pName, Type: epubType, Data: ed.Body}, MaxFileLen: cfg.MaxAttachmentBytes()}); err != nil {
			return err
		}
		logID := db.NewID("kds")
		sealed := ""
		if s.o.Box.Enabled() {
			sealed, err = s.o.Box.Seal([]byte(addr), "kindle_sends/"+logID+"/recipient")
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(c, `INSERT INTO kindle_sends(id, project_id, user_id, outbox_id, input_hash, file_sha256, file_name, file_bytes, recipient_domain, recipient_sealed, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			logID, in.ProjectID, c.P.UserID, id, ed.InputHash, fileSHA, pName, len(ed.Body), DomainOf(addr), sealed, db.Now())
		return err
	})
	if err != nil {
		s.removeFile(id)
		var le *LimitError
		switch {
		case errors.Is(err, errPreviewUsed):
			return KindleSendOut{}, ops.ConflictWith("That preview is not valid any more. Preview again.", map[string]any{"reason": "preview_gone"})
		case errors.As(err, &le):
			return KindleSendOut{}, limitError(le)
		}
		return KindleSendOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "kindle.send", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"outbox_id": id, "bytes": len(ed.Body), "sha256": fileSHA, "input_hash": ed.InputHash, "recipient_domain": DomainOf(addr)}})
	s.Wake()
	return KindleSendOut{Queued: true, ID: id, FileName: pName, Bytes: int64(len(ed.Body)), RecipientDomain: DomainOf(addr), Notice: KindleNotice}, nil
}

var errPreviewUsed = errors.New("preview used")

// KindleLogEntry is one send. It holds no text; the full address is there only for the project's owner and administrators.
type KindleLogEntry struct {
	ID              string `json:"id"`
	By              string `json:"by" desc:"The display name of the person who sent it."`
	At              string `json:"at"`
	FileName        string `json:"file_name"`
	Bytes           int64  `json:"bytes"`
	SHA256          string `json:"sha256"`
	InputHash       string `json:"input_hash"`
	RecipientDomain string `json:"recipient_domain"`
	Recipient       string `json:"recipient,omitempty" desc:"The full address it went to. Shown only to the project's owner and to administrators; kept sealed with the server's secret key."`
	State           string `json:"state" enum:"waiting,sent,failed,cancelled,unknown" desc:"unknown: the relay may have taken it and did not say."`
}

// KindleLogOut lists sends, newest first.
type KindleLogOut struct {
	Items []KindleLogEntry `json:"items" desc:"The newest 50."`
}

func kindleLog(c *ops.Ctx, in KindleProjectIn) (KindleLogOut, error) {
	seeFull := c.P.Role == ops.RoleAdmin
	if !seeFull {
		var role string
		_ = c.DB().Read().QueryRowContext(c, `SELECT role FROM project_members WHERE project_id = ? AND user_id = ?`, in.ProjectID, c.P.UserID).Scan(&role)
		seeFull = role == "owner"
	}
	svc, _ := svcOf(c)
	rows, err := c.DB().Read().QueryContext(c, `SELECT k.id, COALESCE(NULLIF(u.display_name,''), 'A member'), k.created_at, k.file_name, k.file_bytes, k.file_sha256, k.input_hash, k.recipient_domain,
		COALESCE(o.state, ''), COALESCE(o.last_error, ''), k.recipient_sealed
		FROM kindle_sends k LEFT JOIN users u ON u.id = k.user_id LEFT JOIN mail_outbox o ON o.id = k.outbox_id
		WHERE k.project_id = ? ORDER BY k.created_at DESC, k.id DESC LIMIT 50`, in.ProjectID)
	if err != nil {
		return KindleLogOut{}, ops.Internal(err)
	}
	defer rows.Close()
	out := KindleLogOut{Items: []KindleLogEntry{}}
	for rows.Next() {
		var e KindleLogEntry
		var st, class, sealed string
		if err := rows.Scan(&e.ID, &e.By, &e.At, &e.FileName, &e.Bytes, &e.SHA256, &e.InputHash, &e.RecipientDomain, &st, &class, &sealed); err != nil {
			return out, ops.Internal(err)
		}
		if seeFull && sealed != "" && svc != nil {
			if p, err := svc.o.Box.Open(sealed, "kindle_sends/"+e.ID+"/recipient"); err == nil {
				e.Recipient = string(p)
			}
		}
		switch {
		case st == "pending" || st == "sending":
			e.State = "waiting"
		case st == "dead" && class == string(ClassCancelled), st == "dead" && class == string(ClassPaused), st == "dead" && class == string(ClassExpired):
			e.State = "cancelled"
		case st == "dead" && class == string(ClassUnknown):
			e.State = "unknown"
		case st == "dead":
			e.State = "failed"
		default:
			e.State = "sent"
		}
		out.Items = append(out.Items, e)
	}
	return out, rows.Err()
}

// Register adds the mail and Kindle ops.
func Register(r *ops.Registry) {
	admin := ops.Admin().NotFoundForOthers().ForSessions()
	me := ops.Authenticated().ForSessions()
	editor := ops.RequireProject(ops.ProjectEditor).ForSessions()
	ops.Register(r, ops.Op{Name: "mail.config.get", Method: "GET", Path: "/api/v1/admin/mail", Auth: admin,
		Desc: "The mail relay's settings: host, port, security, the user name, whether a password is stored (never the password), the sender, the size limit, whether mail is on, why it was switched off, and the last test. Mail is off until an administrator switches it on. Administrators, browser session only."}, configGet)
	ops.Register(r, ops.Op{SecretInput: true, Name: "mail.config.set", Method: "PUT", Path: "/api/v1/admin/mail", Auth: admin, NoDryRun: true,
		Desc: "Set the mail relay and switch mail on or off. The password is sealed and never returned; changing the relay's address or user name drops it unless a new one is sent. STARTTLS or TLS is required and the certificate is verified; plain text is only for a relay on this machine, only when chosen, never with a password. A restore switches mail off; only switching it on here lifts that. Administrators, browser session only."}, configSet)
	ops.Register(r, ops.Op{Name: "mail.config.test", Method: "POST", Path: "/api/v1/admin/mail/test", Auth: admin, NoDryRun: true,
		Desc: "Send a test message through the stored relay to the administrator's own account address, and say what happened as a class and a sentence (never what the relay said). It goes nowhere else. At most one every five seconds. Administrators, browser session only."}, configTest)
	ops.Register(r, ops.Op{Name: "mail.outbox.status", Method: "GET", Path: "/api/v1/admin/mail/outbox", Auth: admin,
		Desc: "How many messages wait, were sent or failed, and the newest 30 with their kind, state, attempts, failure class and the recipient's domain. No address, subject or text. Administrators, browser session only."}, outboxStatus)
	ops.Register(r, ops.Op{Name: "mail.digest.get", Method: "GET", Path: "/api/v1/me/mail/digest", Auth: me,
		Desc: "Your email digest setting, the address it would go to (your account address) and what it says. Browser session only."}, digestGet)
	ops.Register(r, ops.Op{Name: "mail.digest.set", Method: "PUT", Path: "/api/v1/me/mail/digest", Auth: me,
		Desc: "Turn your email digest on (daily or weekly) or off. It goes to your account address only, says only counts and project titles and a link to this server, and is sent only when something is new. Needs mail switched on by the administrator and an address on your account. Browser session only."}, digestSet)
	ops.Register(r, ops.Op{Name: "mail.progress.get", Method: "GET", Path: "/api/v1/me/mail/progress", Auth: me,
		Desc: "Your progress email setting: off by default, the address it would go to (your account address) and what it says. Browser session only."}, progressGet)
	ops.Register(r, ops.Op{Name: "mail.progress.set", Method: "PUT", Path: "/api/v1/me/mail/progress", Auth: me,
		Desc: "Turn your weekly or monthly progress email on or off. It goes to your account address only and says only numbers: the words you added, your writing days, your streak and your goals. Never a word of your writing, a title or a project's name; nothing is sent for a period in which you wrote nothing. Needs mail switched on by the administrator. Browser session only."}, progressSet)
	ops.Register(r, ops.Op{Name: "kindle.address.get", Method: "GET", Path: "/api/v1/me/kindle", Auth: me,
		Desc: "Your stored Send-to-Kindle address, whether mail is available, the sender address to approve at Amazon, and what to do there. Browser session only."}, kindleAddressGet)
	ops.Register(r, ops.Op{Name: "kindle.address.set", Method: "PUT", Path: "/api/v1/me/kindle", Auth: me,
		Desc: "Store your own Send-to-Kindle address (one plain address). Deliveries only ever go to it. Changing it drops deliveries waiting for the old one. Browser session only."}, kindleAddressSet)
	ops.Register(r, ops.Op{Name: "kindle.address.delete", Method: "DELETE", Path: "/api/v1/me/kindle", Auth: me,
		Desc: "Delete your stored Send-to-Kindle address and any delivery still waiting for it. Browser session only."}, kindleAddressDelete)
	ops.Register(r, ops.Op{Name: "kindle.preview", Method: "POST", Path: "/api/v1/projects/{id}/kindle/preview", Auth: editor, NoDryRun: true, SecretOutput: true,
		Desc: "Say exactly what Send to Kindle would send: the EPUB's file name and size, your stored address as the only recipient, the sender to approve at Amazon, what is held back (notes, ghosted text, alternatives, the author's contact block) and a plain statement that the manuscript leaves this server. Returns the token kindle.send needs. Sends nothing. Project editor or owner, browser session only."}, kindlePreview)
	ops.Register(r, ops.Op{Name: "kindle.send", Method: "POST", Path: "/api/v1/projects/{id}/kindle/send", Auth: editor, NoDryRun: true,
		Desc: "Mail the previewed EPUB edition of the project to your own stored Send-to-Kindle address. It needs the preview's token (once, within 15 minutes), compiles the edition again and refuses if it changed. There is no way to name another recipient. The manuscript leaves this server. Project editor or owner, browser session only."}, kindleSend)
	ops.Register(r, ops.Op{Name: "kindle.log", Method: "GET", Path: "/api/v1/projects/{id}/kindle/log", Auth: editor,
		Desc: "Who sent which edition of this project to a Kindle, when, its hash and size, the recipient's domain and how it went. No text. The full address it went to is shown only to the project's owner and to administrators (it is kept sealed). Project editor or owner, browser session only."}, kindleLog)
}

// addedDomains are the domains of the comma-separated list now that the list in force does not allow.
func addedDomains(now []string, list string) []string {
	have := map[string]bool{}
	for _, d := range now {
		have[d] = true
	}
	var out []string
	for _, d := range strings.Split(list, ",") {
		if d = strings.TrimSpace(d); d != "" && !have[d] {
			out = append(out, d)
		}
	}
	return out
}

// sameDomains compares two lists of domains as sets, ignoring case and order.
func sameDomains(a, b []string) bool {
	norm := func(l []string) map[string]bool {
		m := map[string]bool{}
		for _, d := range l {
			if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
				m[d] = true
			}
		}
		return m
	}
	x, y := norm(a), norm(b)
	if len(x) != len(y) {
		return false
	}
	for d := range x {
		if !y[d] {
			return false
		}
	}
	return true
}

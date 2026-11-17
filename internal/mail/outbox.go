package mail

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// File is the one attachment of a job.
type File struct {
	Name, Type string
	Data       []byte
}

// Job is a message to put in the outbox. ID is chosen by the caller (so the attachment file can be written
// first); the recipient is checked here and is always a person's own address.
type Job struct {
	ID        string
	Kind      string
	PersonID  string
	ProjectID string
	Recipient string
	Subject   string
	Body      string
	DedupeKey string
	// Since and Until are a digest's window of notifications: it is built again from them when it is sent.
	Since, Until string
	File         *File
	MaxFileLen   int64
}

// LimitError is a refusal for a quota or a rate limit.
type LimitError struct {
	// Scope is person_queue, queue, queue_bytes, person_rate or global_rate.
	Scope      string
	RetryAfter int
}

func (e *LimitError) Error() string { return "mail: " + e.Scope + " limit reached" }

// ErrDuplicate means the job's dedupe key was already used: the message is, or was, in the outbox.
var ErrDuplicate = errors.New("mail: already queued")

// Backoff schedule, after the first, second, ... failed attempt; the last entry repeats.
var schedule = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Backoff is the wait before the next attempt given how many failed (at least 1); r is in [0,1).
func Backoff(failed int, r float64) time.Duration {
	if failed < 1 {
		failed = 1
	}
	base := schedule[len(schedule)-1]
	if failed <= len(schedule) {
		base = schedule[failed-1]
	}
	if r < 0 {
		r = 0
	}
	if r >= 1 {
		r = 0.999999
	}
	return time.Duration(float64(base) * (1 + 0.2*(2*r-1)))
}

func (s *Service) messageID(cfg Config, id string) string {
	d := DomainOf(cfg.FromAddress)
	if d == "" {
		d = "scriobh.invalid"
	}
	return strings.ToLower(id) + "@" + d
}

// EnqueueTx puts a job in the outbox inside the caller's transaction, after the quotas and rate limits.
func (s *Service) EnqueueTx(ctx context.Context, tx *sql.Tx, j Job) error {
	if j.Kind != KindKindle && j.Kind != KindDigest && j.Kind != KindProgress {
		return errors.New("mail: unknown kind")
	}
	if _, err := ValidAddress(j.Recipient); err != nil {
		return err
	}
	if err := checkHeaderValue(j.Subject); err != nil || len(j.Body) > MaxBodyBytes {
		return ErrHeader
	}
	var fbytes int64
	var fsum, fname, ftype string
	if j.File != nil {
		if err := checkFileName(j.File.Name); err != nil {
			return err
		}
		fbytes = int64(len(j.File.Data))
		if j.MaxFileLen > 0 && fbytes > j.MaxFileLen {
			return &LimitError{Scope: "file_size"}
		}
		sum := sha256.Sum256(j.File.Data)
		fsum, fname, ftype = hex.EncodeToString(sum[:]), j.File.Name, j.File.Type
	}
	cfg, err := loadConfig(ctx, tx)
	if err != nil {
		return err
	}
	if err := s.CheckQuota(ctx, tx, j.PersonID, j.Kind, fbytes); err != nil {
		return err
	}
	now := s.now()
	if j.ID == "" {
		j.ID = db.NewID("mel")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mail_outbox(id, kind, person_id, project_id, recipient, recipient_domain, subject, body, file_name, file_type, file_bytes, file_sha256,
		dedupe_key, window_since, window_until, message_id, state, attempts, next_try_at, created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?, 'pending', 0, ?, ?)`,
		j.ID, j.Kind, j.PersonID, j.ProjectID, j.Recipient, DomainOf(j.Recipient), j.Subject, j.Body, fname, ftype, fbytes, fsum, j.DedupeKey, j.Since, j.Until, s.messageID(cfg, j.ID), s.stamp(now), s.stamp(now))
	if err != nil {
		if db.IsUniqueViolation(err) && j.DedupeKey != "" {
			return ErrDuplicate
		}
		return err
	}
	return nil
}

// finishTx settles a row: its text, recipient and file name are blanked, what stays is the kind, the state, the
// failure class and the recipient's domain.
func finishTx(tx *sql.Tx, id, state string, class Class, now string) error {
	_, err := tx.Exec(`UPDATE mail_outbox SET state = ?, last_error = ?, finished_at = ?, claimed_at = '', recipient = '', subject = '', body = '', file_name = '' WHERE id = ?`,
		state, string(class), now, id)
	return err
}

type row struct {
	ID, Kind, PersonID, ProjectID, Recipient, Subject, Body, FileName, FileType, FileSHA, MessageID, CreatedAt, Since, Until string
	FileBytes                                                                                                                int64
	Attempts                                                                                                                 int
}

// detached is a context for writing a result: a send that finished while the server was stopping is still recorded.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
}

func (s *Service) settle(ctx context.Context, id, state string, class Class) {
	ctx, cancel := detached(ctx)
	defer cancel()
	now := s.stamp(s.now())
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error { return finishTx(tx, id, state, class, now) })
	if err != nil {
		s.o.Log.Warn("mail: could not record the result of a message", "id", id, "err", err)
	}
	s.removeFile(id)
}

// staleSending is how long a row may stay claimed before it is taken to be stuck: longer than the longest session
// (ten minutes) and a batch of five slow ones are not, because the dispatcher is one goroutine that reclaims only
// before it starts a batch.
const staleSending = 15 * time.Minute

// DispatchOnce sends what is due, once. It returns how many messages it handled. An idle pass is one indexed
// query.
func (s *Service) DispatchOnce(ctx context.Context) int {
	s.dmu.Lock()
	defer s.dmu.Unlock()
	if s.blockedByRestore(ctx) {
		return 0
	}
	now := s.now()
	s.mu.Lock()
	hold := s.holdUntil.After(now)
	s.mu.Unlock()
	if hold {
		return 0
	}
	var due, stale int
	if err := s.o.DB.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mail_outbox WHERE state = 'pending' AND next_try_at <= ?),
		EXISTS(SELECT 1 FROM mail_outbox WHERE state = 'sending' AND claimed_at < ?)`, s.stamp(now), s.stamp(now.Add(-staleSending))).Scan(&due, &stale); err != nil {
		return 0
	}
	if stale != 0 {
		s.reclaim(ctx, now)
		due = 1
	}
	if due == 0 {
		return 0
	}
	if _, on := s.mailOn(ctx); !on {
		return 0
	}
	var rows []row
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id, kind, person_id, project_id, recipient, subject, body, file_name, file_type, file_bytes, file_sha256, message_id, created_at, window_since, window_until, attempts
			FROM mail_outbox WHERE state = 'pending' AND next_try_at <= ? ORDER BY next_try_at, id LIMIT 5`, s.stamp(now))
		if err != nil {
			return err
		}
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.ID, &r.Kind, &r.PersonID, &r.ProjectID, &r.Recipient, &r.Subject, &r.Body, &r.FileName, &r.FileType, &r.FileBytes, &r.FileSHA, &r.MessageID, &r.CreatedAt, &r.Since, &r.Until, &r.Attempts); err != nil {
				rs.Close()
				return err
			}
			rows = append(rows, r)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return err
		}
		for _, r := range rows {
			if _, err := tx.Exec(`UPDATE mail_outbox SET state = 'sending', claimed_at = ?, attempts = attempts + 1, data_started_at = '' WHERE id = ?`, s.stamp(now), r.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		s.o.Log.Warn("mail: claiming messages failed", "err", err)
		return 0
	}
	for i, r := range rows {
		if ctx.Err() != nil {
			// Not tried: give back what was claimed and not started.
			for _, rest := range rows[i:] {
				_, _ = s.o.DB.Writer().Exec(`UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '', attempts = attempts - 1 WHERE id = ? AND state = 'sending'`, rest.ID)
			}
			return len(rows)
		}
		if err := s.rl.Wait(ctx); err != nil {
			_, _ = s.o.DB.Writer().Exec(`UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '', attempts = attempts - 1 WHERE id = ? AND state = 'sending'`, r.ID)
			continue
		}
		r.Attempts++
		s.deliver(ctx, r)
		s.mu.Lock()
		hold := s.holdUntil.After(s.now())
		s.mu.Unlock()
		if hold { // a relay-level failure: the rest wait, unspent
			for _, rest := range rows[i+1:] {
				_, _ = s.o.DB.Writer().Exec(`UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '', attempts = attempts - 1 WHERE id = ? AND state = 'sending'`, rest.ID)
			}
			break
		}
	}
	return len(rows)
}

func (s *Service) maxAge(kind string) time.Duration {
	if kind == KindDigest || kind == KindProgress {
		return s.lim.DigestMaxAge
	}
	return s.lim.KindleMaxAge
}

// stillWanted re-checks, at the moment of sending, what was true when the message was made: the person is
// active, still has the feature on and the same address, and the project is still there and not in the trash.
func (s *Service) stillWanted(ctx context.Context, cfg Config, r row) bool {
	q := s.o.DB.Read()
	var email sql.NullString
	var dis, del sql.NullString
	var agent int
	if err := q.QueryRowContext(ctx, `SELECT email, disabled_at, deleted_at, is_agent FROM users WHERE id = ?`, r.PersonID).Scan(&email, &dis, &del, &agent); err != nil {
		return false
	}
	if dis.Valid || del.Valid || agent != 0 {
		return false
	}
	switch r.Kind {
	case KindDigest:
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM mail_digest WHERE user_id = ?`, r.PersonID).Scan(&n); err != nil || n == 0 {
			return false
		}
		var bound string
		if err := q.QueryRowContext(ctx, `SELECT address FROM mail_digest WHERE user_id = ?`, r.PersonID).Scan(&bound); err != nil || !strings.EqualFold(bound, email.String) {
			return false // opted in for another address
		}
		return strings.EqualFold(email.String, r.Recipient)
	case KindProgress:
		var bound string
		if err := q.QueryRowContext(ctx, `SELECT address FROM mail_progress WHERE user_id = ?`, r.PersonID).Scan(&bound); err != nil || !strings.EqualFold(bound, email.String) {
			return false // not turned on, or turned on for another address
		}
		return strings.EqualFold(email.String, r.Recipient)
	case KindKindle:
		var addr string
		if err := q.QueryRowContext(ctx, `SELECT address FROM kindle_addresses WHERE user_id = ?`, r.PersonID).Scan(&addr); err != nil || !strings.EqualFold(addr, r.Recipient) {
			return false
		}
		if !cfg.KindleAddressAllowed(r.Recipient) {
			return false // an administrator narrowed the list while it waited
		}
		// A passphrase set, or changed, after the message was queued: what was queued was not queued under it.
		// Fail closed on any error reading the gate.
		var gateAt string
		switch err := q.QueryRowContext(ctx, `SELECT updated_at FROM project_gates WHERE project_id = ?`, r.ProjectID).Scan(&gateAt); {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return false
		case gateAt >= r.CreatedAt:
			return false
		}
		// The same rule the registry applies: a member's own role, and an instance administrator acts as the owner of any
		// project (a project in the trash is for its owner and an administrator).
		var role string
		var trashed sql.NullString
		err := q.QueryRowContext(ctx, `SELECT CASE WHEN u.role = 'admin' THEN 'owner' ELSE m.role END, p.trashed_at
			FROM projects p JOIN users u ON u.id = ?1 LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = u.id
			WHERE p.id = ?2 AND p.deleted_at IS NULL AND (u.role = 'admin' OR m.role IS NOT NULL)`, r.PersonID, r.ProjectID).Scan(&role, &trashed)
		return err == nil && !trashed.Valid && (role == "owner" || role == "editor")
	}
	return false
}

// giveBack returns a claimed row that was not tried to pending, its attempt unspent.
func (s *Service) giveBack(id string) {
	_, _ = s.o.DB.Writer().Exec(`UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '', attempts = MAX(attempts - 1, 0) WHERE id = ? AND state = 'sending'`, id)
}

// stillClaimed says whether the row is still the dispatcher's: a switch-off, a restore or a person's change may have
// settled it while it waited in the batch.
func (s *Service) stillClaimed(ctx context.Context, id string) bool {
	var st string
	return s.o.DB.Read().QueryRowContext(ctx, `SELECT state FROM mail_outbox WHERE id = ?`, id).Scan(&st) == nil && st == "sending"
}

// mailOn reads the settings now, for this message: mail is on, no restore paused it, and the relay is complete.
func (s *Service) mailOn(ctx context.Context) (Config, bool) {
	cfg, err := loadConfig(ctx, s.o.DB.Read())
	return cfg, err == nil && cfg.Enabled && !cfg.RestorePaused && cfg.Ready()
}

func (s *Service) deliver(ctx context.Context, r row) {
	// The settings are read for every message, not once for the batch: a relay changed, or mail switched off, since
	// the batch was claimed applies to what is still waiting in it.
	cfg, on := s.mailOn(ctx)
	if !s.stillClaimed(ctx, r.ID) {
		return // settled by whoever switched mail off or restored: the row is theirs now
	}
	if !on {
		s.giveBack(r.ID)
		return
	}
	created, _ := db.Parse(r.CreatedAt)
	if s.now().Sub(created) > s.maxAge(r.Kind) {
		s.settle(ctx, r.ID, "dead", ClassExpired)
		return
	}
	if !s.stillWanted(ctx, cfg, r) {
		s.settle(ctx, r.ID, "dead", ClassCancelled)
		return
	}
	relay, err := openRelay(s.o.Box, cfg)
	if err != nil {
		s.failed(ctx, r, &Error{Class: ClassConfig})
		return
	}
	msg := &Message{From: cfg.FromAddress, FromName: cfg.FromName, To: r.Recipient, Subject: r.Subject, Body: r.Body, MessageID: r.MessageID, Date: created, Auto: r.Kind == KindDigest || r.Kind == KindProgress}
	if r.Kind == KindDigest {
		// What the digest says is made again now, from the same window: a project trashed, locked or read since it
		// was queued is not named, and a digest with nothing left to say is not sent.
		body, err := s.BuildDigest(ctx, r.PersonID, r.Since, r.Until)
		if err != nil {
			s.failed(ctx, r, &Error{Class: ClassTemporary})
			return
		}
		if body == "" {
			s.settle(ctx, r.ID, "dead", ClassCancelled)
			return
		}
		msg.Body = body
		if b := s.baseURL(); b != "" {
			msg.ListUnsubscribe = b + "/settings"
		}
	}
	if r.Kind == KindProgress {
		// Made again now, from the same window: a project locked, trashed or left since it was queued is not counted, and a
		// message with nothing left to say is not sent.
		body, err := s.BuildProgress(ctx, r.PersonID, r.Since, r.Until)
		if err != nil {
			s.failed(ctx, r, &Error{Class: ClassTemporary})
			return
		}
		if body == "" {
			s.settle(ctx, r.ID, "dead", ClassCancelled)
			return
		}
		msg.Body = body
		if b := s.baseURL(); b != "" {
			msg.ListUnsubscribe = b + "/settings"
		}
	}
	if r.FileBytes > 0 {
		data, err := os.ReadFile(s.filePath(r.ID))
		sum := sha256.Sum256(data)
		if err != nil || hex.EncodeToString(sum[:]) != r.FileSHA {
			s.settle(ctx, r.ID, "dead", ClassInternal)
			return
		}
		msg.Attachment = &Attachment{Name: r.FileName, ContentType: r.FileType, Data: data}
	}
	// And once more just before the conversation starts (the file was read, the digest built): switching mail off
	// stops everything not yet begun.
	if cfg2, on := s.mailOn(ctx); !s.stillClaimed(ctx, r.ID) {
		return
	} else if !on || cfg2.Host != cfg.Host || cfg2.Port != cfg.Port || cfg2.Username != cfg.Username || cfg2.PasswordSealed != cfg.PasswordSealed {
		s.giveBack(r.ID) // off, or the relay changed under it: the next pass, with the settings then, tries it
		return
	}
	err = s.client.Send(ctx, relay, msg, SendOptions{BeforeData: func() error {
		_, err := s.o.DB.Writer().ExecContext(ctx, `UPDATE mail_outbox SET data_started_at = ? WHERE id = ?`, s.stamp(s.now()), r.ID)
		return err
	}})
	if err == nil {
		if s.o.AfterSend != nil && s.o.AfterSend(r.ID) != nil {
			return
		}
		s.settle(ctx, r.ID, "sent", "")
		return
	}
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Class: ClassInternal}
	}
	if ctx.Err() != nil && !e.AfterData { // shutting down: not the relay's fault, not an attempt
		_, _ = s.o.DB.Writer().Exec(`UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '', attempts = attempts - 1 WHERE id = ? AND state = 'sending'`, r.ID)
		return
	}
	if s.o.AfterSend != nil && s.o.AfterSend(r.ID) != nil {
		return
	}
	s.failed(ctx, r, e)
}

// failed records a failed attempt: dead for a permanent class or when the attempts are spent, held (unspent)
// for a failure about the relay's settings, otherwise pending again after the backoff.
func (s *Service) failed(ctx context.Context, r row, e *Error) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if e.AfterData {
		s.settle(ctx, r.ID, "dead", ClassUnknown)
		return
	}
	if e.Class.Permanent() || (!e.Class.ConfigLevel() && r.Attempts >= s.lim.MaxAttempts) {
		s.settle(ctx, r.ID, "dead", e.Class)
		return
	}
	now := s.now()
	next := now.Add(Backoff(r.Attempts, s.o.Rand()))
	attempts := r.Attempts
	if e.Class.ConfigLevel() {
		// The relay's settings are wrong, not this message: stop for a while and spend no attempt.
		next = now.Add(s.lim.ConfigHold)
		attempts--
		s.mu.Lock()
		s.holdUntil, s.holdClass = next, e.Class
		s.mu.Unlock()
		s.warnOnce("hold", "mail: the relay failed in a way that needs its settings looked at; sending is held", "class", string(e.Class))
	}
	_, err := s.o.DB.Writer().ExecContext(ctx, `UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '', attempts = ?, next_try_at = ?, last_error = ? WHERE id = ?`,
		attempts, s.stamp(next), string(e.Class), r.ID)
	if err != nil {
		s.o.Log.Warn("mail: could not record a failed attempt", "id", r.ID, "err", err)
	}
}

// Hold says whether sending is held after a failure about the relay's settings, and why.
func (s *Service) Hold() (Class, time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holdUntil.After(s.now()) {
		return s.holdClass, s.holdUntil, true
	}
	return "", time.Time{}, false
}

// ClearHold lets the dispatcher try again at once (the relay's settings were changed or tested).
func (s *Service) ClearHold() {
	s.mu.Lock()
	s.holdUntil, s.holdClass = time.Time{}, ""
	s.mu.Unlock()
	// What was set aside for the relay's settings is due now.
	_, _ = s.o.DB.Writer().Exec(`UPDATE mail_outbox SET next_try_at = ? WHERE state = 'pending' AND next_try_at > ? AND last_error IN ('auth','tls_cert','tls','starttls','blocked','config')`,
		s.stamp(s.now()), s.stamp(s.now()))
	s.Wake()
}

// cleanBase returns the app's own address for a link, or "" when it is not one a mail can carry: an address that
// only works on this machine, one with a user name, one that is not http(s).
func cleanBase(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	if checkURLChars(raw) != nil {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	// A link to this machine is no use to the person reading the mail elsewhere; a link on the LAN is.
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()) {
		return ""
	}
	return strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/")
}

func asLimit(err error, target **LimitError) bool { return errors.As(err, target) }

// CheckQuota says whether a person may queue one more message of a kind now: the bounds of the queue (per
// person, total, attachment bytes) and the rates (per person and kind in an hour, and the instance's).
func (s *Service) CheckQuota(ctx context.Context, q queryer, person, kind string, fileBytes int64) error {
	hourAgo := s.stamp(s.now().Add(-time.Hour))
	var n int
	var bytes int64
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM mail_outbox WHERE person_id = ? AND state IN ('pending','sending')`, person).Scan(&n); err != nil {
		return err
	}
	if n >= s.lim.PerPersonQueue {
		return &LimitError{Scope: "person_queue", RetryAfter: 600}
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(file_bytes),0) FROM mail_outbox WHERE state IN ('pending','sending')`).Scan(&n, &bytes); err != nil {
		return err
	}
	if n >= s.lim.TotalQueue {
		return &LimitError{Scope: "queue", RetryAfter: 600}
	}
	if bytes+fileBytes > s.lim.TotalQueueBytes {
		return &LimitError{Scope: "queue_bytes", RetryAfter: 600}
	}
	per := s.lim.KindlePerHour
	if kind == KindDigest || kind == KindProgress {
		per = s.lim.DigestPerHour
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM mail_outbox WHERE person_id = ? AND kind = ? AND created_at > ?`, person, kind, hourAgo).Scan(&n); err != nil {
		return err
	}
	if n >= per {
		return &LimitError{Scope: "person_rate", RetryAfter: 1800}
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM mail_outbox WHERE created_at > ?`, hourAgo).Scan(&n); err != nil {
		return err
	}
	if n >= s.lim.GlobalPerHour {
		return &LimitError{Scope: "global_rate", RetryAfter: 1800}
	}
	return nil
}

// reclaim settles rows that have been claimed for longer than any session can last: the result of their attempt
// could not be written (a locked database). One whose body had been started is given up as unknown, never sent
// again; one that had not goes back to pending.
func (s *Service) reclaim(ctx context.Context, now time.Time) {
	ctx, cancel := detached(ctx)
	defer cancel()
	var dead []string
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		rs, err := tx.QueryContext(ctx, `SELECT id FROM mail_outbox WHERE state = 'sending' AND claimed_at < ? AND data_started_at <> ''`, s.stamp(now.Add(-staleSending)))
		if err != nil {
			return err
		}
		for rs.Next() {
			var id string
			if err := rs.Scan(&id); err != nil {
				rs.Close()
				return err
			}
			dead = append(dead, id)
		}
		rs.Close()
		for _, id := range dead {
			if err := finishTx(tx, id, "dead", ClassUnknown, s.stamp(now)); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE mail_outbox SET state = 'pending', claimed_at = '', data_started_at = '' WHERE state = 'sending' AND claimed_at < ?`, s.stamp(now.Add(-staleSending)))
		return err
	})
	if err != nil {
		s.o.Log.Warn("mail: reclaiming stuck messages failed", "err", err)
		return
	}
	s.removeFiles(dead)
}

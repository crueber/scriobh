package gitmirror

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
	"github.com/crueber/scriobh/internal/webhook"
)

// A trigger after a successful push (F-365): one signed POST to an address the owner chose, so that a CI service
// or a build hook starts when the repository changes.
//
// It reuses the webhook package's pieces and builds no second delivery engine: the address check
// (webhook.ValidateURL), the guarded sender (webhook.Sender: the network guard on the connected address, no
// proxy, no redirects, a bounded read), the signature (webhook.Sign, the same header and scheme a receiver
// already verifies) and the retry schedule (webhook.Backoff). What is its own is small: the setting per remote,
// and an outbox row per push, retried from the engine's periodic check up to a cap.
//
// The body is {event, delivery, project, repository, branch, commit}: the project's slug, the repository's
// address (host and path), the branch and the commit id. Nothing of the manuscript. The response is never kept:
// the record is one short line ("delivered, HTTP 204", "failed: timeout"), and the receiver's words are never
// read into it.

const (
	// TriggerEvent is the event name of a delivery after a push.
	TriggerEvent = "git.pushed"
	// maxTriggerAttempts is the cap: this many attempts, then the delivery is dead.
	maxTriggerAttempts = 5
	triggerBatch       = 20
	keepDeliveries     = 50
	// triggerStale is how long a delivery may wait (for a trashed project, for a retry) before it is given up.
	triggerStale = 24 * time.Hour
	// triggerTimeout bounds one attempt.
	triggerTimeout = 10 * time.Second
)

// PausedTriggerByRestore is the reason shown on a trigger a restore switched off.
const PausedTriggerByRestore = "Paused when this copy was restored from a backup, so it does not call the old copy's receivers again. Turn it on again here when this is the server that should."

// Trigger is a remote's trigger as ops return it. The signing secret and the address's path (which may be the
// receiver's secret) are never returned.
type Trigger struct {
	RemoteID       string `json:"remote_id"`
	Origin         string `json:"origin" desc:"The address's scheme, host and port. Its path and query are stored encrypted and not shown."`
	HasPath        bool   `json:"has_path"`
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	SecretHint     string `json:"secret_hint" desc:"The start of the signing secret, to tell two apart. The secret itself is shown once, when it is made or rotated."`
	LastStatus     string `json:"last_status,omitempty" desc:"A short line about the last delivery, never a response body."`
	LastAt         string `json:"last_at,omitempty"`
	Pending        int    `json:"pending" desc:"Deliveries waiting for a retry."`
	CreatedAt      string `json:"created_at"`
}

// TriggerOut wraps a trigger and, once, its secret.
type TriggerOut struct {
	Item   Trigger `json:"item"`
	Secret string  `json:"secret,omitempty" desc:"The signing secret: shown only in the answer that made or rotated it. A receiver checks each delivery's Scriobh-Webhook-Signature with it (the webhook scheme: HMAC-SHA256 of the timestamp, a full stop and the body)."`
}

// TriggerSetIn creates or changes a trigger.
type TriggerSetIn struct {
	ProjectID    string  `path:"id" json:"-" desc:"Project id."`
	RemoteID     string  `json:"remote_id,omitempty" desc:"The remote. Optional while the project has exactly one."`
	URL          *string `json:"url,omitempty" desc:"https address to call (http only for the local network, where the administrator allowed it). Required to create the trigger."`
	Enabled      *bool   `json:"enabled,omitempty"`
	RotateSecret bool    `json:"rotate_secret,omitempty" desc:"Make a new signing secret; the answer shows it once."`
}

func (in TriggerSetIn) ProjectRef() string { return in.ProjectID }

// TriggerTestOut is the outcome of a test delivery.
type TriggerTestOut struct {
	OK     bool   `json:"ok"`
	Status string `json:"status" desc:"A short line, never a response body."`
}

func registerTrigger(r *ops.Registry) {
	owner := ops.RequireProject(ops.ProjectOwner)
	ops.Register(r, ops.Op{Name: "git.trigger.get", Method: "GET", Path: "/api/v1/projects/{id}/git/trigger",
		Desc: "A remote's push trigger: where it calls (scheme, host and port), whether it is on, the last delivery's short status. Owner only. The secret and the path are never returned.",
		Auth: owner}, getTrigger)
	ops.Register(r, ops.Op{NoDryRun: true, SecretOutput: true, Name: "git.trigger.set", Method: "PUT", Path: "/api/v1/projects/{id}/git/trigger",
		Desc: "Owner only. Set a remote's trigger: after every successful push one signed POST goes to the address, with the repository, the branch, the commit id and the project slug, and nothing of the manuscript. A new trigger gets a signing secret, shown once in the answer (as is a rotated one); the address's path is stored encrypted. Retried with backoff up to 5 attempts.",
		Auth: owner}, setTrigger)
	ops.Register(r, ops.Op{NoDryRun: true, Permanent: true, Name: "git.trigger.delete", Method: "DELETE", Path: "/api/v1/projects/{id}/git/trigger",
		Desc: "Owner only. Remove a remote's trigger and its waiting deliveries.",
		Auth: owner}, deleteTrigger)
	ops.Register(r, ops.Op{NoDryRun: true, Name: "git.trigger.test", Method: "POST", Path: "/api/v1/projects/{id}/git/trigger/test",
		Desc: "Owner only. Send one test delivery (event git.ping, no commit) to the trigger's address, signed as a real one, and say how it went in one short line. Not retried.",
		Auth: owner}, testTrigger)
}

type triggerRow struct {
	RemoteID, ProjectID, URL, PathSealed, SecretSealed, SecretHint string
	Enabled                                                        bool
	DisabledReason, LastStatus, LastAt, CreatedAt                  string
}

const triggerCols = `remote_id, project_id, url, path_sealed, secret_sealed, secret_hint, enabled, disabled_reason, last_status, COALESCE(last_at,''), created_at`

func (s *Service) loadTrigger(ctx context.Context, remoteID string) (*triggerRow, error) {
	var t triggerRow
	var en int
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT `+triggerCols+` FROM git_triggers WHERE remote_id = ?`, remoteID).
		Scan(&t.RemoteID, &t.ProjectID, &t.URL, &t.PathSealed, &t.SecretSealed, &t.SecretHint, &en, &t.DisabledReason, &t.LastStatus, &t.LastAt, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.Enabled = en != 0
	return &t, nil
}

func (s *Service) triggerView(ctx context.Context, t *triggerRow) Trigger {
	v := Trigger{RemoteID: t.RemoteID, Origin: t.URL, HasPath: t.PathSealed != "", Enabled: t.Enabled, DisabledReason: t.DisabledReason, SecretHint: t.SecretHint,
		LastStatus: t.LastStatus, LastAt: t.LastAt, CreatedAt: t.CreatedAt}
	_ = s.o.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM git_trigger_deliveries WHERE remote_id = ? AND state = 'pending'`, t.RemoteID).Scan(&v.Pending)
	return v
}

func triggerSecretContext(remoteID string) string { return "git_triggers/" + remoteID + "/secret" }
func triggerPathContext(remoteID string) string   { return "git_triggers/" + remoteID + "/path" }

func getTrigger(c *ops.Ctx, in RemoteQuery) (struct {
	Item *Trigger `json:"item,omitempty"`
}, error) {
	type out = struct {
		Item *Trigger `json:"item,omitempty"`
	}
	s, err := service(c)
	if err != nil {
		return out{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return out{}, err
	}
	t, err := s.loadTrigger(c, r.ID)
	if err != nil {
		return out{}, ops.Internal(err)
	}
	if t == nil {
		return out{}, nil
	}
	v := s.triggerView(c, t)
	return out{Item: &v}, nil
}

func (s *Service) checkTriggerURL(raw string) (origin, sealed string, err error) {
	canon, verr := webhook.ValidateURL(raw, allowPrivate.Load())
	if verr != nil {
		var ue *webhook.ErrURL
		if errors.As(verr, &ue) {
			return "", "", ops.Invalid("url", ue.Msg)
		}
		return "", "", ops.Internal(verr)
	}
	u, perr := url.Parse(canon)
	if perr != nil {
		return "", "", ops.Invalid("url", "That is not a web address.")
	}
	origin = u.Scheme + "://" + u.Host
	rest := u.RequestURI()
	if rest == "/" || rest == "" {
		return origin, "", nil
	}
	return origin, rest, nil
}

func setTrigger(c *ops.Ctx, in TriggerSetIn) (TriggerOut, error) {
	s, err := service(c)
	if err != nil {
		return TriggerOut{}, err
	}
	if !s.Enabled() {
		return TriggerOut{}, ops.Conflict(secrets.ErrNoKey.Error())
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return TriggerOut{}, err
	}
	cur, err := s.loadTrigger(c, r.ID)
	if err != nil {
		return TriggerOut{}, ops.Internal(err)
	}
	now := stamp(s.o.Clock.Now())
	var secret string
	if cur == nil {
		if in.URL == nil {
			return TriggerOut{}, ops.Invalid("url", "Give the address the trigger should call.")
		}
		origin, rest, err := s.checkTriggerURL(*in.URL)
		if err != nil {
			return TriggerOut{}, err
		}
		secret = webhook.NewSecret()
		sealedSecret, err := s.o.Box.Seal([]byte(secret), triggerSecretContext(r.ID))
		if err != nil {
			return TriggerOut{}, ops.Internal(err)
		}
		sealedPath := ""
		if rest != "" {
			if sealedPath, err = s.o.Box.Seal([]byte(rest), triggerPathContext(r.ID)); err != nil {
				return TriggerOut{}, ops.Internal(err)
			}
		}
		enabled := in.Enabled == nil || *in.Enabled
		if _, err := s.o.DB.Writer().ExecContext(c, `INSERT INTO git_triggers(remote_id, project_id, url, path_sealed, secret_sealed, secret_hint, enabled, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			r.ID, r.ProjectID, origin, sealedPath, sealedSecret, webhook.Hint(secret), b2i(enabled), now, now); err != nil {
			return TriggerOut{}, ops.Internal(err)
		}
	} else {
		var sets []string
		var args []any
		if in.URL != nil {
			origin, rest, err := s.checkTriggerURL(*in.URL)
			if err != nil {
				return TriggerOut{}, err
			}
			sealedPath := ""
			if rest != "" {
				if sealedPath, err = s.o.Box.Seal([]byte(rest), triggerPathContext(r.ID)); err != nil {
					return TriggerOut{}, ops.Internal(err)
				}
			}
			sets, args = append(sets, "url = ?", "path_sealed = ?"), append(args, origin, sealedPath)
		}
		if in.RotateSecret {
			secret = webhook.NewSecret()
			sealedSecret, err := s.o.Box.Seal([]byte(secret), triggerSecretContext(r.ID))
			if err != nil {
				return TriggerOut{}, ops.Internal(err)
			}
			sets, args = append(sets, "secret_sealed = ?", "secret_hint = ?"), append(args, sealedSecret, webhook.Hint(secret))
		}
		if in.Enabled != nil {
			sets, args = append(sets, "enabled = ?", "disabled_reason = ?"), append(args, b2i(*in.Enabled), "")
		}
		if len(sets) > 0 {
			sets, args = append(sets, "updated_at = ?"), append(args, now, r.ID)
			if _, err := s.o.DB.Writer().ExecContext(c, `UPDATE git_triggers SET `+strings.Join(sets, ", ")+` WHERE remote_id = ?`, args...); err != nil {
				return TriggerOut{}, ops.Internal(err)
			}
		}
		if in.Enabled != nil && !*in.Enabled {
			_, _ = s.o.DB.Writer().ExecContext(c, `DELETE FROM git_trigger_deliveries WHERE remote_id = ? AND state = 'pending'`, r.ID)
		}
	}
	c.Audit(ops.AuditEntry{Action: "git.trigger.set", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"remote_id": r.ID, "created": cur == nil, "url_changed": in.URL != nil, "rotated": in.RotateSecret, "enabled": in.Enabled == nil || *in.Enabled}})
	t, err := s.loadTrigger(c, r.ID)
	if err != nil || t == nil {
		return TriggerOut{}, ops.Internal(err)
	}
	return TriggerOut{Item: s.triggerView(c, t), Secret: secret}, nil
}

func deleteTrigger(c *ops.Ctx, in RemoteQuery) (struct {
	Deleted bool `json:"deleted"`
}, error) {
	type out = struct {
		Deleted bool `json:"deleted"`
	}
	s, err := service(c)
	if err != nil {
		return out{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return out{}, err
	}
	res, err := s.o.DB.Writer().ExecContext(c, `DELETE FROM git_triggers WHERE remote_id = ?`, r.ID)
	if err != nil {
		return out{}, ops.Internal(err)
	}
	_, _ = s.o.DB.Writer().ExecContext(c, `DELETE FROM git_trigger_deliveries WHERE remote_id = ?`, r.ID)
	if n, _ := res.RowsAffected(); n == 0 {
		return out{}, ops.NotFound("trigger")
	}
	c.Audit(ops.AuditEntry{Action: "git.trigger.delete", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"remote_id": r.ID}})
	return out{Deleted: true}, nil
}

func testTrigger(c *ops.Ctx, in RemoteBody) (TriggerTestOut, error) {
	s, err := service(c)
	if err != nil {
		return TriggerTestOut{}, err
	}
	r, err := s.remoteFor(c, in.ProjectID, in.RemoteID)
	if err != nil {
		return TriggerTestOut{}, err
	}
	t, err := s.loadTrigger(c, r.ID)
	if err != nil {
		return TriggerTestOut{}, ops.Internal(err)
	}
	if t == nil {
		return TriggerTestOut{}, ops.NotFound("trigger")
	}
	if !t.Enabled || r.RestorePaused {
		return TriggerTestOut{}, ops.Conflict("This trigger is switched off or paused (a restore pauses every trigger). It calls nothing, a test included, until its owner switches it on.")
	}
	res, ok := s.sendTrigger(c, r, t, db.NewID("gtd"), "git.ping", "", "", 1)
	return TriggerTestOut{OK: ok, Status: res}, nil
}

// ---- delivery ---------------------------------------------------------------------

func (s *Service) triggerSender() *webhook.Sender {
	s.trigMu.Lock()
	defer s.trigMu.Unlock()
	if s.trigSender == nil {
		s.trigSender = s.o.TriggerSender
	}
	if s.trigSender == nil {
		s.trigSender = webhook.NewSender(webhook.SenderOptions{AllowPrivate: s.o.AllowPrivate, Timeout: triggerTimeout, Version: "git-trigger"})
	}
	return s.trigSender
}

// repositoryName is how a delivery names the repository: its host and path, from the remote's address (never a
// credential: addresses carry none).
func repositoryName(raw string) string {
	ep, e := parseURL(raw, true)
	if e != nil {
		return ""
	}
	p := strings.TrimSuffix(strings.Trim(ep.Path, "/"), ".git")
	if ep.Proto == "file" {
		return p
	}
	return ep.Host + "/" + p
}

// triggerPayload is the body of a delivery. Everything in it is an identifier: no manuscript text.
type triggerPayload struct {
	Event      string `json:"event"`
	Delivery   string `json:"delivery"`
	Project    string `json:"project"`
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

// sendTrigger makes one attempt and returns the short status line and whether it was delivered.
func (s *Service) sendTrigger(ctx context.Context, r *remoteRow, t *triggerRow, id, event, branch, commit string, attempt int) (string, bool) {
	secret, err := s.o.Box.Open(t.SecretSealed, triggerSecretContext(r.ID))
	if err != nil {
		return "failed: the signing secret cannot be read", false
	}
	defer clear(secret)
	target := t.URL
	if t.PathSealed != "" {
		rest, err := s.o.Box.Open(t.PathSealed, triggerPathContext(r.ID))
		if err != nil {
			return "failed: the address cannot be read", false
		}
		target += string(rest)
		clear(rest)
	}
	var slug string
	_ = s.o.DB.Read().QueryRowContext(ctx, `SELECT slug FROM projects WHERE id = ?`, r.ProjectID).Scan(&slug)
	body, _ := json.Marshal(triggerPayload{Event: event, Delivery: id, Project: slug, Repository: repositoryName(r.URL), Branch: branch, Commit: commit})
	ts := s.o.Clock.Now().Unix()
	h := http.Header{}
	h.Set(webhook.HeaderID, id)
	h.Set(webhook.HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(webhook.HeaderSignature, webhook.SignatureHeader(ts, body, string(secret)))
	h.Set(webhook.HeaderEvent, event)
	h.Set(webhook.HeaderAttempt, strconv.Itoa(attempt))
	res := s.triggerSender().Send(ctx, target, h, body)
	return statusLine(res), res.OK()
}

// statusLine is the whole of what is kept about an attempt: the status or the class of failure, never a body.
func statusLine(res webhook.Result) string {
	switch {
	case res.OK():
		return fmt.Sprintf("delivered, HTTP %d", res.Status)
	case res.Status > 0:
		return fmt.Sprintf("failed: HTTP %d (%s)", res.Status, res.Class)
	}
	return "failed: " + res.Class
}

// enqueueTrigger records a delivery for a push that reached the remote. It writes one row and wakes the sender.
func (s *Service) enqueueTrigger(ctx context.Context, r *remoteRow, pushID, commit string) {
	t, err := s.loadTrigger(ctx, r.ID)
	if err != nil || t == nil || !t.Enabled {
		return
	}
	now := stamp(s.o.Clock.Now())
	_, err = s.o.DB.Writer().ExecContext(ctx, `INSERT INTO git_trigger_deliveries(id, remote_id, project_id, push_id, commit_sha, branch, state, attempts, next_attempt_at, created_at) VALUES (?,?,?,?,?,?, 'pending', 0, ?, ?)`,
		db.NewID("gtd"), r.ID, r.ProjectID, pushID, commit, r.Branch, now, now)
	if err != nil {
		s.log.Warn("git: could not queue a trigger delivery", "remote", r.ID, "err", cause(err))
		return
	}
	_, _ = s.o.DB.Writer().ExecContext(ctx, `DELETE FROM git_trigger_deliveries WHERE remote_id = ? AND state <> 'pending' AND id NOT IN
		(SELECT id FROM git_trigger_deliveries WHERE remote_id = ? ORDER BY id DESC LIMIT ?)`, r.ID, r.ID, keepDeliveries)
	select {
	case s.trigReady <- struct{}{}:
	default:
	}
}

// triggerLoop sends what is due when woken by a push, until ctx ends. Retries are picked up by check.
func (s *Service) triggerLoop(ctx context.Context) {
	defer s.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigReady:
			s.deliverDue(ctx)
		}
	}
}

// deliverDue sends the deliveries that are due, a batch at a time. One pass at a time.
func (s *Service) deliverDue(ctx context.Context) {
	s.deliverMu.Lock()
	defer s.deliverMu.Unlock()
	for ctx.Err() == nil {
		type row struct {
			id, remote, project, push, commit, branch string
			attempts                                  int
			created                                   string
		}
		now := stamp(s.o.Clock.Now())
		rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT id, remote_id, project_id, push_id, commit_sha, branch, attempts, created_at FROM git_trigger_deliveries
			WHERE state = 'pending' AND next_attempt_at <= ? ORDER BY next_attempt_at, id LIMIT ?`, now, triggerBatch)
		if err != nil {
			return
		}
		var due []row
		for rows.Next() {
			var x row
			if rows.Scan(&x.id, &x.remote, &x.project, &x.push, &x.commit, &x.branch, &x.attempts, &x.created) == nil {
				due = append(due, x)
			}
		}
		rows.Close()
		if len(due) == 0 {
			return
		}
		progressed := false
		for _, d := range due {
			if ctx.Err() != nil {
				return
			}
			if s.settleOne(ctx, d.id, d.remote, d.project, d.push, d.commit, d.branch, d.attempts, d.created) {
				progressed = true
			}
		}
		if !progressed {
			return
		}
	}
}

// settleOne makes one attempt of one delivery and records it. It reports whether the delivery left the due set.
func (s *Service) settleOne(ctx context.Context, id, remoteID, projectID, pushID, commit, branch string, attempts int, created string) bool {
	now := s.o.Clock.Now()
	bg := context.WithoutCancel(ctx)
	finish := func(state, status string, next time.Time, n int) {
		var fin any
		if state != "pending" {
			fin = stamp(now)
		}
		_, _ = s.o.DB.Writer().ExecContext(bg, `UPDATE git_trigger_deliveries SET state = ?, attempts = ?, next_attempt_at = ?, last_status = ?, finished_at = ? WHERE id = ?`,
			state, n, stamp(next), status, fin, id)
		_, _ = s.o.DB.Writer().ExecContext(bg, `UPDATE git_triggers SET last_status = ?, last_at = ? WHERE remote_id = ?`, status, stamp(now), remoteID)
	}
	var trashed int
	_ = s.o.DB.Read().QueryRowContext(bg, `SELECT count(*) FROM projects WHERE id = ? AND (trashed_at IS NOT NULL OR deleted_at IS NOT NULL)`, projectID).Scan(&trashed)
	age := now.Sub(parseStamp(created))
	if trashed > 0 {
		// A trashed project is paused: nothing is sent until it is restored, and what waits too long is given up.
		if age > triggerStale {
			finish("dead", "gave up: the project stayed in the trash", now, attempts)
			return true
		}
		_, _ = s.o.DB.Writer().ExecContext(bg, `UPDATE git_trigger_deliveries SET next_attempt_at = ? WHERE id = ?`, stamp(now.Add(time.Hour)), id)
		return false
	}
	r, err := s.loadRemoteByID(bg, remoteID)
	if err != nil {
		finish("dead", "gave up: the remote was removed", now, attempts)
		return true
	}
	t, err := s.loadTrigger(bg, remoteID)
	if err != nil || t == nil || !t.Enabled {
		finish("dead", "gave up: the trigger is switched off", now, attempts)
		return true
	}
	attempts++
	line, ok := s.sendTrigger(ctx, r, t, id, TriggerEvent, branch, commit, attempts)
	switch {
	case ok:
		finish("delivered", line, now, attempts)
		return true
	case strings.Contains(line, "(rejected)") || strings.Contains(line, "(gone)") || strings.Contains(line, "(redirect_refused)") || strings.Contains(line, "(blocked)") || strings.HasPrefix(line, "failed: blocked"):
		finish("dead", line+"; not retried", now, attempts)
		return true
	case attempts >= maxTriggerAttempts || age > triggerStale:
		finish("dead", line+"; gave up after "+strconv.Itoa(attempts)+" attempts", now, attempts)
		return true
	}
	wait := webhook.Backoff(attempts, rand.Float64())
	finish("pending", line+"; will retry", now.Add(wait), attempts)
	return false
}

// PauseTriggers switches every trigger off (with the restore's reason) and removes the waiting deliveries. It
// says how many triggers it paused.
func PauseTriggers(ctx context.Context, d *db.DB) (int, error) {
	var n int64
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE git_triggers SET enabled = 0, disabled_reason = ?, updated_at = ? WHERE enabled = 1`, PausedTriggerByRestore, db.Now())
		if err != nil {
			return err
		}
		n, _ = res.RowsAffected()
		_, err = tx.Exec(`DELETE FROM git_trigger_deliveries WHERE state = 'pending'`)
		return err
	})
	return int(n), err
}

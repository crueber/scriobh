package webhook

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/secrets"
)

// The webhook ops. Every one belongs to a project, needs its owner and, for a
// key, the admin scope: a webhook sends data out of the instance, so an agent
// key that can write a manuscript cannot set one up, and a reader cannot see
// where the project's notifications go. The secret is shown once, by the op that
// makes or rotates it, and nowhere else.

// Webhook is a webhook as listed. The signing secret is never part of it.
type Webhook struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	Name           string   `json:"name"`
	URL            string   `json:"url" desc:"Where it sends: scheme, host and port only. The path and query of the address are sealed with the server's secret key and never shown again (they may be a secret); send a new url to replace the whole address."`
	URLPathSet     bool     `json:"url_path_set" desc:"True when the address has a path or query (stored sealed)."`
	Events         []string `json:"events" desc:"Which events it receives."`
	IncludeText    bool     `json:"include_text" desc:"Whether comment.created carries the comment's words."`
	Enabled        bool     `json:"enabled"`
	DisabledReason string   `json:"disabled_reason,omitempty" desc:"Why it was switched off, when the server did it."`
	SecretHint     string   `json:"secret_hint" desc:"The start of the signing secret, to tell secrets apart. The secret itself is shown once."`
	PrevSecretLive bool     `json:"previous_secret_live" desc:"True while a rotated secret still signs deliveries beside the new one."`
	FailureStreak  int      `json:"failure_streak" desc:"Deliveries in a row that died. The webhook is switched off at 10."`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
	LastDeliveryAt string   `json:"last_delivery_at,omitempty"`
	LastStatus     int      `json:"last_status" desc:"The HTTP status of the last attempt, 0 when it never got an answer."`
	Pending        int      `json:"pending" desc:"Deliveries waiting to be sent or retried."`
	Dead           int      `json:"dead" desc:"Deliveries that failed for good and are listed in webhook.deliveries."`
}

// Delivery is one notification and what became of it. It holds no payload and no
// response body: the payload is ids and counts, and a failing response is
// reduced to a short cleaned excerpt.
type Delivery struct {
	ID            string `json:"id" desc:"Sent as the Scriobh-Webhook-Id header on every attempt: store the ids you have seen to apply an event once."`
	WebhookID     string `json:"webhook_id"`
	Event         string `json:"event" desc:"The event name, or ping."`
	State         string `json:"state" enum:"pending,delivered,dead"`
	Attempts      int    `json:"attempts"`
	Status        int    `json:"status" desc:"The HTTP status of the last attempt, 0 when it never got an answer."`
	Error         string `json:"error,omitempty" desc:"The kind of failure of the last attempt: blocked, timeout, dns, refused, tls, network, redirect_refused, rejected, server_error, rate_limited or gone."`
	Excerpt       string `json:"excerpt,omitempty" desc:"A few words of a failing response, cleaned: no markup, no line breaks, 160 characters."`
	CreatedAt     string `json:"created_at"`
	NextAttemptAt string `json:"next_attempt_at,omitempty" desc:"When a pending delivery is due."`
	DeliveredAt   string `json:"delivered_at,omitempty"`
}

// ---- inputs and outputs -------------------------------------------------------------------

// ProjectIn names the project.
type ProjectIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

func (in ProjectIn) ProjectRef() string { return in.ProjectID }

// HookIn names a webhook of a project.
type HookIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	WebhookID string `path:"webhook_id" json:"-" desc:"Webhook id, from webhook.list."`
}

func (in HookIn) ProjectRef() string { return in.ProjectID }

// Item wraps a webhook.
type Item struct {
	Item Webhook `json:"item"`
}

// List lists a project's webhooks.
type List struct {
	Items  []Webhook `json:"items"`
	Events []string  `json:"events" desc:"Every event a webhook can subscribe to."`
	// Enabled is false when the server has webhooks switched off or no secret key to seal their secrets with.
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
	// AllowPrivate says whether this server lets a webhook call the local network (SCRIOBH_WEBHOOK_ALLOW_PRIVATE).
	AllowPrivate bool `json:"allow_private"`
}

// CreateIn makes a webhook.
type CreateIn struct {
	ProjectID   string   `path:"id" json:"-" desc:"Project id."`
	Name        string   `json:"name" desc:"What it is for, up to 80 characters."`
	URL         string   `json:"url" desc:"Where to send the notifications: https://..., with no user name or password. A name or a public address; the server refuses internal addresses unless its administrator allowed them. The host is stored and shown; the path and query are stored sealed and never shown again, so they may carry a receiver's token, but the signature is what authenticates the call."`
	Events      []string `json:"events" desc:"One or more of document.updated, comment.created, suggestion.created, export.completed."`
	IncludeText *bool    `json:"include_text,omitempty" desc:"WARNING: when true, comment.created carries the words of the comment (up to 2,000 characters) and so leaves the instance. Default false: ids only. Nothing else a webhook sends ever carries text."`
}

func (in CreateIn) ProjectRef() string { return in.ProjectID }

// Validate checks everything that does not need the database.
func (in *CreateIn) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return ops.Invalid("name", "Give the webhook a name.")
	}
	if utf8.RuneCountInString(in.Name) > 80 {
		return ops.Invalid("name", "The name is limited to 80 characters.")
	}
	ev, err := checkEvents(in.Events)
	if err != nil {
		return err
	}
	in.Events = ev
	return nil
}

func checkEvents(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, ops.Invalid("events", "Choose at least one event: "+strings.Join(EventNames, ", ")+".")
	}
	seen := map[string]bool{}
	for _, e := range in {
		e = strings.TrimSpace(e)
		if _, ok := eventTypes[e]; !ok {
			return nil, ops.Invalid("events", "Unknown event. The events are "+strings.Join(EventNames, ", ")+".")
		}
		seen[e] = true
	}
	var out []string
	for _, n := range EventNames { // a fixed order, so the stored list is the same however it was sent
		if seen[n] {
			out = append(out, n)
		}
	}
	return out, nil
}

// CreateOut carries the one and only copy of the secret.
type CreateOut struct {
	Item   Webhook `json:"item"`
	Secret string  `json:"secret" desc:"The signing secret. Shown once; store it now. Every delivery carries Scriobh-Webhook-Signature: v1=hex(HMAC-SHA256(secret, timestamp + \".\" + body)), with the timestamp in Scriobh-Webhook-Timestamp; reject a delivery whose signature does not match or whose timestamp is more than five minutes off."`
}

// UpdateIn changes a webhook. Only the fields sent change.
type UpdateIn struct {
	ProjectID   string    `path:"id" json:"-" desc:"Project id."`
	WebhookID   string    `path:"webhook_id" json:"-" desc:"Webhook id."`
	Name        *string   `json:"name,omitempty"`
	URL         *string   `json:"url,omitempty" desc:"A new address, replacing the whole of the old one (the old path and query are never shown, so they cannot be edited). The failure count starts again."`
	Events      *[]string `json:"events,omitempty"`
	IncludeText *bool     `json:"include_text,omitempty" desc:"WARNING: true sends the words of each new comment out of the instance."`
	Enabled     *bool     `json:"enabled,omitempty" desc:"Switch it off or on. Switching it on clears a reason it was switched off and the failure count."`
}

func (in UpdateIn) ProjectRef() string { return in.ProjectID }

// DeleteOut says a webhook was removed.
type DeleteOut struct {
	Deleted bool `json:"deleted"`
}

// RotateIn replaces the signing secret.
type RotateIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	WebhookID    string `path:"webhook_id" json:"-" desc:"Webhook id."`
	KeepOldHours *int   `json:"keep_old_hours,omitempty" desc:"How long the old secret keeps signing beside the new one, so a receiver can change over without a gap. Default 24; 0 for none; at most 168."`
}

func (in RotateIn) ProjectRef() string { return in.ProjectID }

// TestOut is the result of a test ping.
type TestOut struct {
	Delivery Delivery `json:"delivery"`
	OK       bool     `json:"ok"`
}

// DeliveriesIn lists a webhook's deliveries.
type DeliveriesIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	WebhookID string `path:"webhook_id" json:"-" desc:"Webhook id."`
	State     string `query:"state" enum:"pending,delivered,dead" desc:"Only deliveries in this state."`
	Limit     int    `query:"limit" desc:"How many, newest first. Default 50, at most 200."`
	Before    string `query:"before" desc:"A delivery id: list the ones before it (the next_cursor of the last page)."`
}

func (in DeliveriesIn) ProjectRef() string { return in.ProjectID }

// DeliveriesOut is a page of deliveries.
type DeliveriesOut struct {
	Items      []Delivery `json:"items"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// RedeliverIn queues a delivery again.
type RedeliverIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	WebhookID  string `path:"webhook_id" json:"-" desc:"Webhook id."`
	DeliveryID string `path:"delivery_id" json:"-" desc:"Delivery id, from webhook.deliveries."`
}

func (in RedeliverIn) ProjectRef() string { return in.ProjectID }

// DeliveryItem wraps a delivery.
type DeliveryItem struct {
	Item Delivery `json:"item"`
}

// Register adds the webhook ops. limits are the settings a preview and the
// validation see; the running service (which the ops that send or seal need)
// is found in the environment.
func Register(r *ops.Registry, limits Limits) {
	lim := limits.withDefaults()
	own := ops.RequireProject(ops.ProjectOwner)
	base := "/api/v1/projects/{id}/webhooks"
	ops.Register(r, ops.Op{Name: "webhook.create", Method: "POST", Path: base, Status: 201, Scope: ops.ScopeAdmin,
		Desc: "Make a webhook: a signed notification sent to an address when a document is saved (ids and word counts only), a comment or suggestion is made (ids only, or the comment's words if you ask), or an export finishes. The signing secret is in this response and nowhere else. Delivery is at least once and retried with backoff; every attempt carries the same Scriobh-Webhook-Id. Project owner.",
		Auth: own, NoDryRun: true, SecretOutput: true}, func(c *ops.Ctx, in CreateIn) (CreateOut, error) { return create(c, lim, in) })
	ops.Register(r, ops.Op{Name: "webhook.list", Method: "GET", Path: base, Scope: ops.ScopeAdmin,
		Desc: "List the project's webhooks with their state: enabled, last status, deliveries waiting and dead. Never includes a secret. Project owner.",
		Auth: own}, func(c *ops.Ctx, in ProjectIn) (List, error) { return list(c, lim, in) })
	ops.Register(r, ops.Op{Name: "webhook.get", Method: "GET", Path: base + "/{webhook_id}", Scope: ops.ScopeAdmin,
		Desc: "One webhook. Never includes the secret. Project owner.", Auth: own}, get)
	ops.Register(r, ops.Op{Name: "webhook.update", Method: "PATCH", Path: base + "/{webhook_id}", Scope: ops.ScopeAdmin,
		Desc: "Change a webhook's name, address, events, whether comments carry their words, or switch it on or off. Project owner.", Auth: own},
		func(c *ops.Ctx, in UpdateIn) (Item, error) { return update(c, lim, in) })
	ops.Register(r, ops.Op{Name: "webhook.delete", Permanent: true, Method: "DELETE", Path: base + "/{webhook_id}", Scope: ops.ScopeAdmin,
		Desc: "Delete a webhook and the list of its deliveries. Deliveries still waiting are dropped. Project owner.", Auth: own}, remove)
	ops.Register(r, ops.Op{Name: "webhook.rotate_secret", Method: "POST", Path: base + "/{webhook_id}/rotate", Scope: ops.ScopeAdmin,
		Desc: "Replace the signing secret. The new secret is in this response and nowhere else; the old one keeps signing beside it for keep_old_hours so a receiver can change over. Project owner.",
		Auth: own, NoDryRun: true, SecretOutput: true}, rotate)
	ops.Register(r, ops.Op{Name: "webhook.test", Method: "POST", Path: base + "/{webhook_id}/test", Scope: ops.ScopeAdmin,
		Desc: "Send a signed ping to the webhook's address now and report what came back (status and failure kind). It is recorded in the deliveries and never retried. At most one every five seconds. Project owner.",
		Auth: own, NoDryRun: true}, test)
	ops.Register(r, ops.Op{Name: "webhook.deliveries", Method: "GET", Path: base + "/{webhook_id}/deliveries", Scope: ops.ScopeAdmin,
		Desc: "A webhook's deliveries, newest first, with state, attempts, status and failure kind. Dead ones are the dead-letter list. No payloads and no response bodies. Project owner.",
		Auth: own}, deliveries)
	ops.Register(r, ops.Op{Name: "webhook.redeliver", Method: "POST", Path: base + "/{webhook_id}/deliveries/{delivery_id}/redeliver", Scope: ops.ScopeAdmin,
		Desc: "Queue a dead or delivered notification again, with the same delivery id. Project owner.", Auth: own}, redeliver)
}

func svcOf(c *ops.Ctx) (*Service, bool) { return ops.Service[*Service](c.Env, ServiceName) }

const cols = `id, project_id, name, url, (url_path_sealed <> ''), events_json, include_text, enabled, disabled_reason, secret_hint, prev_secret_until, failure_streak, created_at, updated_at, COALESCE(last_delivery_at,''), last_status`

type rowScanner interface{ Scan(dest ...any) error }

func scanWebhook(r rowScanner, now string) (Webhook, error) {
	var w Webhook
	var ej string
	var it, en, hasPath int
	var prevUntil string
	if err := r.Scan(&w.ID, &w.ProjectID, &w.Name, &w.URL, &hasPath, &ej, &it, &en, &w.DisabledReason, &w.SecretHint, &prevUntil, &w.FailureStreak, &w.CreatedAt, &w.UpdatedAt, &w.LastDeliveryAt, &w.LastStatus); err != nil {
		return w, err
	}
	_ = json.Unmarshal([]byte(ej), &w.Events)
	if w.Events == nil {
		w.Events = []string{}
	}
	w.IncludeText, w.Enabled, w.URLPathSet = it != 0, en != 0, hasPath != 0
	w.PrevSecretLive = prevUntil != "" && prevUntil > now
	return w, nil
}

type querier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// nowOf is the time by the clock that decides what is due: the service's when it
// is running (a rotated secret is "live" by that clock), the database's otherwise.
func nowOf(c *ops.Ctx) string {
	if s, ok := svcOf(c); ok {
		return stamp(s.now())
	}
	return db.Now()
}

func loadOne(c *ops.Ctx, project, id string) (Webhook, error) {
	var q querier = c.DB().Read()
	ctx := context.Context(c)
	w, err := scanWebhook(q.QueryRowContext(ctx, `SELECT `+cols+` FROM webhooks WHERE id = ? AND project_id = ?`, id, project), nowOf(c))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ops.NotFound("webhook")
	}
	if err != nil {
		return w, ops.Internal(err)
	}
	counts(ctx, q, &w)
	return w, nil
}

func counts(ctx context.Context, q querier, w *Webhook) {
	_ = q.QueryRowContext(ctx, `SELECT COALESCE(sum(state = 'pending'), 0), COALESCE(sum(state = 'dead'), 0) FROM webhook_deliveries WHERE webhook_id = ?`, w.ID).Scan(&w.Pending, &w.Dead)
}

func list(c *ops.Ctx, lim Limits, in ProjectIn) (List, error) {
	rows, err := c.DB().Read().QueryContext(c, `SELECT `+cols+` FROM webhooks WHERE project_id = ? ORDER BY created_at, id`, in.ProjectID)
	if err != nil {
		return List{}, ops.Internal(err)
	}
	defer rows.Close()
	out := List{Items: []Webhook{}, Events: EventNames, Enabled: !lim.Off, AllowPrivate: lim.AllowPrivate}
	now := nowOf(c)
	for rows.Next() {
		w, err := scanWebhook(rows, now)
		if err != nil {
			return List{}, ops.Internal(err)
		}
		out.Items = append(out.Items, w)
	}
	if err := rows.Err(); err != nil {
		return List{}, ops.Internal(err)
	}
	rows.Close()
	for i := range out.Items {
		counts(c, c.DB().Read(), &out.Items[i])
	}
	switch {
	case lim.Off:
		out.Reason = "Webhooks are switched off on this server (SCRIOBH_WEBHOOKS=off)."
	default:
		if s, ok := svcOf(c); ok && !s.Box().Enabled() {
			out.Enabled, out.Reason = false, "This server has no secret key, so a webhook's signing secret cannot be stored."
		}
	}
	return out, nil
}

func get(c *ops.Ctx, in HookIn) (Item, error) {
	w, err := loadOne(c, in.ProjectID, in.WebhookID)
	return Item{Item: w}, err
}

func urlError(err error) error {
	var ue *ErrURL
	if errors.As(err, &ue) {
		return ops.Invalid("url", ue.Msg)
	}
	return ops.Internal(err)
}

// splitURL separates a validated address into what is shown (scheme, host and port) and
// what is sealed (path and query, which may carry a receiver's token). An address with
// neither has nothing to seal.
func splitURL(box *secrets.Box, id, target string) (origin, sealed string, err error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", "", err
	}
	origin = u.Scheme + "://" + u.Host
	rest := u.RequestURI()
	if rest == "/" || rest == "" {
		return origin, "", nil
	}
	sealed, err = box.Seal([]byte(rest), "webhooks/"+id+"/path")
	return origin, sealed, err
}

func projectLive(ctx context.Context, tx *sql.Tx, project string) error {
	var trashed sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT trashed_at FROM projects WHERE id = ? AND deleted_at IS NULL`, project).Scan(&trashed); err != nil {
		return ops.NotFound("project")
	}
	if trashed.Valid {
		return ops.Conflict("This project is in the trash. Restore it to change its webhooks.")
	}
	return nil
}

// otherEnabled says whether any other webhook is switched on; when none is, the
// feeder's cursor may be at any old place, so turning one on moves it to now: a
// webhook hears of what happens after it exists, never of the past. A parked cursor (see CursorParked) is left
// alone: the feature is off or this copy was just restored, and the next start with webhooks on moves it to now.
func moveCursorIfFirst(tx *sql.Tx, exceptID string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM webhooks WHERE enabled = 1 AND id <> ?`, exceptID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := tx.Exec(`UPDATE webhook_state SET cursor = MAX(cursor, (SELECT COALESCE(max(seq), 0) FROM events)) WHERE id = 1 AND cursor >= 0`)
	return err
}

func create(c *ops.Ctx, lim Limits, in CreateIn) (CreateOut, error) {
	if lim.Off {
		return CreateOut{}, ops.Conflict("Webhooks are switched off on this server.")
	}
	svc, ok := svcOf(c)
	if !ok {
		return CreateOut{}, ops.Internal(errors.New("webhook service is not running"))
	}
	if !svc.Box().Enabled() {
		return CreateOut{}, ops.Conflict(secrets.ErrNoKey.Error())
	}
	target, err := ValidateURL(in.URL, lim.AllowPrivate)
	if err != nil {
		return CreateOut{}, urlError(err)
	}
	id := db.NewID("whk")
	origin, sealedPath, err := splitURL(svc.Box(), id, target)
	if err != nil {
		return CreateOut{}, ops.Internal(err)
	}
	secret := NewSecret()
	sealed, err := svc.Box().Seal([]byte(secret), "webhooks/"+id+"/secret")
	if err != nil {
		return CreateOut{}, ops.Internal(err)
	}
	evj, _ := json.Marshal(in.Events)
	now := db.Now()
	text := in.IncludeText != nil && *in.IncludeText
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := projectLive(c, tx, in.ProjectID); err != nil {
			return err
		}
		var perProject, total int
		if err := tx.QueryRow(`SELECT count(*) FROM webhooks WHERE project_id = ?`, in.ProjectID).Scan(&perProject); err != nil {
			return err
		}
		if err := tx.QueryRow(`SELECT count(*) FROM webhooks`).Scan(&total); err != nil {
			return err
		}
		if perProject >= lim.MaxPerProject {
			return ops.Conflict("A project may have at most " + itoa(lim.MaxPerProject) + " webhooks. Delete one first.")
		}
		if total >= lim.MaxTotal {
			return ops.Conflict("This server is at its limit of " + itoa(lim.MaxTotal) + " webhooks.")
		}
		if err := moveCursorIfFirst(tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO webhooks(id, project_id, name, url, url_path_sealed, events_json, include_text, enabled, secret_sealed, secret_hint, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,1,?,?,?,?,?)`, id, in.ProjectID, in.Name, origin, sealedPath, string(evj), boolInt(text), sealed, Hint(secret), nullIfEmpty(c.P.UserID), now, now); err != nil {
			return err
		}
		// The address is not in the audit entry: it may be a path that is itself a secret.
		c.Audit(ops.AuditEntry{Action: "webhook.create", TargetType: "project", TargetID: in.ProjectID,
			Detail: map[string]any{"webhook_id": id, "events": in.Events, "include_text": text}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return CreateOut{}, asOpsErr(err)
	}
	svc.Changed()
	w, err := loadOne(c, in.ProjectID, id)
	return CreateOut{Item: w, Secret: secret}, err
}

func update(c *ops.Ctx, lim Limits, in UpdateIn) (Item, error) {
	var newOrigin, newPath string
	if in.URL != nil {
		u, err := ValidateURL(*in.URL, lim.AllowPrivate)
		if err != nil {
			return Item{}, urlError(err)
		}
		svc, ok := svcOf(c)
		if !ok {
			// A preview has no service (and no key to seal with): the address is checked, and the rest is previewed unsealed.
			newOrigin = u
		} else {
			if newOrigin, newPath, err = splitURL(svc.Box(), in.WebhookID, u); err != nil {
				return Item{}, ops.Internal(err)
			}
		}
	}
	var events []string
	if in.Events != nil {
		ev, err := checkEvents(*in.Events)
		if err != nil {
			return Item{}, err
		}
		events = ev
	}
	var name string
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
		if name == "" || utf8.RuneCountInString(name) > 80 {
			return Item{}, ops.Invalid("name", "A name is 1 to 80 characters.")
		}
	}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := projectLive(c, tx, in.ProjectID); err != nil {
			return err
		}
		var enabled int
		if err := tx.QueryRow(`SELECT enabled FROM webhooks WHERE id = ? AND project_id = ?`, in.WebhookID, in.ProjectID).Scan(&enabled); err != nil {
			return ops.NotFound("webhook")
		}
		var set []string
		var args []any
		add := func(col string, v any) { set = append(set, col+" = ?"); args = append(args, v) }
		var fields []string
		if in.Name != nil {
			add("name", name)
			fields = append(fields, "name")
		}
		if in.URL != nil {
			add("url", newOrigin)
			add("url_path_sealed", newPath)
			add("failure_streak", 0)
			fields = append(fields, "url")
		}
		if in.Events != nil {
			ej, _ := json.Marshal(events)
			add("events_json", string(ej))
			fields = append(fields, "events")
		}
		if in.IncludeText != nil {
			add("include_text", boolInt(*in.IncludeText))
			fields = append(fields, "include_text")
		}
		if in.Enabled != nil {
			add("enabled", boolInt(*in.Enabled))
			fields = append(fields, "enabled")
			if !*in.Enabled && enabled == 1 {
				if err := DropPending(tx, in.WebhookID); err != nil { // off means off: nothing waits for it
					return err
				}
			}
			if *in.Enabled {
				add("disabled_reason", "")
				add("failure_streak", 0)
				if enabled == 0 {
					if err := moveCursorIfFirst(tx, in.WebhookID); err != nil {
						return err
					}
				}
			}
		}
		if len(set) == 0 {
			return nil
		}
		add("updated_at", db.Now())
		args = append(args, in.WebhookID)
		if _, err := tx.Exec(`UPDATE webhooks SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "webhook.update", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"webhook_id": in.WebhookID, "fields": fields}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Item{}, asOpsErr(err)
	}
	if s, ok := svcOf(c); ok {
		s.Changed()
	}
	w, err := loadOne(c, in.ProjectID, in.WebhookID)
	return Item{Item: w}, err
}

func remove(c *ops.Ctx, in HookIn) (DeleteOut, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM webhooks WHERE id = ? AND project_id = ?`, in.WebhookID, in.ProjectID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ops.NotFound("webhook")
		}
		c.Audit(ops.AuditEntry{Action: "webhook.delete", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"webhook_id": in.WebhookID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return DeleteOut{}, asOpsErr(err)
	}
	if s, ok := svcOf(c); ok {
		s.Changed()
	}
	return DeleteOut{Deleted: true}, nil
}

func rotate(c *ops.Ctx, in RotateIn) (CreateOut, error) {
	svc, ok := svcOf(c)
	if !ok || !svc.Box().Enabled() {
		return CreateOut{}, ops.Conflict(secrets.ErrNoKey.Error())
	}
	keep := 24
	if in.KeepOldHours != nil {
		keep = *in.KeepOldHours
	}
	if keep < 0 || keep > 168 {
		return CreateOut{}, ops.Invalid("keep_old_hours", "Keep the old secret for 0 to 168 hours.")
	}
	secret := NewSecret()
	sealed, err := svc.Box().Seal([]byte(secret), "webhooks/"+in.WebhookID+"/secret")
	if err != nil {
		return CreateOut{}, ops.Internal(err)
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := projectLive(c, tx, in.ProjectID); err != nil {
			return err
		}
		var oldSealed, reason string
		if err := tx.QueryRow(`SELECT secret_sealed, disabled_reason FROM webhooks WHERE id = ? AND project_id = ?`, in.WebhookID, in.ProjectID).Scan(&oldSealed, &reason); err != nil {
			return ops.NotFound("webhook")
		}
		prev, until := "", ""
		if keep > 0 {
			// The old secret is opened and sealed again under its own label; one that
			// cannot be opened (the server's key changed) is simply not kept.
			if plain, err := svc.Box().Open(oldSealed, "webhooks/"+in.WebhookID+"/secret"); err == nil {
				if p, err := svc.Box().Seal(plain, "webhooks/"+in.WebhookID+"/prev"); err == nil {
					prev, until = p, stamp(svc.now().Add(time.Duration(keep)*time.Hour))
				}
			}
		}
		enable := ""
		if strings.HasPrefix(reason, "The signing secret") {
			enable = `, enabled = 1, disabled_reason = ''`
			if err := moveCursorIfFirst(tx, in.WebhookID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE webhooks SET secret_sealed = ?, secret_hint = ?, prev_secret_sealed = ?, prev_secret_until = ?, updated_at = ?`+enable+` WHERE id = ?`,
			sealed, Hint(secret), prev, until, db.Now(), in.WebhookID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "webhook.rotate_secret", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"webhook_id": in.WebhookID, "keep_old_hours": keep}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return CreateOut{}, asOpsErr(err)
	}
	svc.Changed()
	w, err := loadOne(c, in.ProjectID, in.WebhookID)
	return CreateOut{Item: w, Secret: secret}, err
}

func test(c *ops.Ctx, in HookIn) (TestOut, error) {
	svc, ok := svcOf(c)
	if !ok {
		return TestOut{}, ops.Internal(errors.New("webhook service is not running"))
	}
	if svc.Limits().Off {
		return TestOut{}, ops.Conflict("Webhooks are switched off on this server.")
	}
	if _, err := loadOne(c, in.ProjectID, in.WebhookID); err != nil {
		return TestOut{}, err
	}
	var trashed sql.NullString
	_ = c.DB().Read().QueryRowContext(c, `SELECT trashed_at FROM projects WHERE id = ?`, in.ProjectID).Scan(&trashed)
	if trashed.Valid {
		return TestOut{}, ops.Conflict("This project is in the trash; its webhooks are paused.")
	}
	id, res, err := svc.Ping(c, in.WebhookID)
	switch {
	case errors.Is(err, ErrPingTooSoon):
		return TestOut{}, ops.RateLimited(int(PingInterval / time.Second))
	case err != nil:
		return TestOut{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "webhook.test", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"webhook_id": in.WebhookID, "ok": res.OK()}})
	d, err := loadDelivery(c, c.DB().Read(), in.WebhookID, id)
	if err != nil {
		return TestOut{}, err
	}
	return TestOut{Delivery: d, OK: res.OK()}, nil
}

const dcols = `id, webhook_id, event, state, attempts, last_status, last_error, last_excerpt, created_at, CASE WHEN state = 'pending' THEN next_attempt_at ELSE '' END, COALESCE(delivered_at,'')`

func scanDelivery(r rowScanner) (Delivery, error) {
	var d Delivery
	err := r.Scan(&d.ID, &d.WebhookID, &d.Event, &d.State, &d.Attempts, &d.Status, &d.Error, &d.Excerpt, &d.CreatedAt, &d.NextAttemptAt, &d.DeliveredAt)
	return d, err
}

func loadDelivery(ctx context.Context, q querier, hookID, id string) (Delivery, error) {
	d, err := scanDelivery(q.QueryRowContext(ctx, `SELECT `+dcols+` FROM webhook_deliveries WHERE id = ? AND webhook_id = ?`, id, hookID))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ops.NotFound("delivery")
	}
	if err != nil {
		return d, ops.Internal(err)
	}
	return d, nil
}

func deliveries(c *ops.Ctx, in DeliveriesIn) (DeliveriesOut, error) {
	if _, err := loadOne(c, in.ProjectID, in.WebhookID); err != nil {
		return DeliveriesOut{}, err
	}
	switch in.State {
	case "", "pending", "delivered", "dead":
	default:
		return DeliveriesOut{}, ops.Invalid("state", "state is pending, delivered or dead.")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if in.Before != "" && (!strings.HasPrefix(in.Before, "whd_") || len(in.Before) > 40) {
		return DeliveriesOut{}, ops.Invalid("before", "That is not a delivery id.")
	}
	q := `SELECT ` + dcols + ` FROM webhook_deliveries WHERE webhook_id = ?`
	args := []any{in.WebhookID}
	if in.State != "" {
		q += ` AND state = ?`
		args = append(args, in.State)
	}
	if in.Before != "" {
		q += ` AND id < ?`
		args = append(args, in.Before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return DeliveriesOut{}, ops.Internal(err)
	}
	defer rows.Close()
	out := DeliveriesOut{Items: []Delivery{}}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return DeliveriesOut{}, ops.Internal(err)
		}
		out.Items = append(out.Items, d)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].ID
	}
	return out, rows.Err()
}

func redeliver(c *ops.Ctx, in RedeliverIn) (DeliveryItem, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := projectLive(c, tx, in.ProjectID); err != nil {
			return err
		}
		var state, event string
		err := tx.QueryRow(`SELECT d.state, d.event FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id WHERE d.id = ? AND d.webhook_id = ? AND w.project_id = ?`,
			in.DeliveryID, in.WebhookID, in.ProjectID).Scan(&state, &event)
		if err != nil {
			return ops.NotFound("delivery")
		}
		switch {
		case event == "ping":
			return ops.Conflict("A test ping is not queued again; send another with webhook.test.")
		case state == "pending":
			return ops.Conflict("That delivery is already waiting to be sent.")
		}
		// Due now, by the clock that decides what is due (the service's; a preview has none).
		due := db.Now()
		if s, ok := svcOf(c); ok {
			due = stamp(s.now())
		}
		if _, err := tx.Exec(`UPDATE webhook_deliveries SET state = 'pending', attempts = 0, next_attempt_at = ?, last_error = '', last_excerpt = '', delivered_at = NULL WHERE id = ?`, due, in.DeliveryID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "webhook.redeliver", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"webhook_id": in.WebhookID, "delivery_id": in.DeliveryID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return DeliveryItem{}, asOpsErr(err)
	}
	if s, ok := svcOf(c); ok {
		s.Changed()
		s.signalDispatch()
	}
	d, err := loadDelivery(c, c.DB().Read(), in.WebhookID, in.DeliveryID)
	return DeliveryItem{Item: d}, err
}

// ---- small helpers ----------------------------------------------------------------------------

func asOpsErr(err error) error {
	var oe *ops.Error
	if errors.As(err, &oe) {
		return oe
	}
	return ops.Internal(err)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func itoa(n int) string { return strconv.Itoa(n) }

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

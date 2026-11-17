package webhook

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/secrets"
)

// ServiceName is the key the server registers the Service under in ops.Env.
const ServiceName = "webhook.service"

// Event names a webhook may subscribe to, and the events-table type each is made from.
var eventTypes = map[string]string{
	"document.updated":   "doc.updated",
	"comment.created":    "comment.created",
	"suggestion.created": "suggestion.created",
	"export.completed":   "export.completed",
}

// webhookEvent is the name a stored event type is delivered as.
var webhookEvent = func() map[string]string {
	m := map[string]string{}
	for name, typ := range eventTypes {
		m[typ] = name
	}
	return m
}()

// EventNames lists the events a webhook can subscribe to, in the order the docs give them.
var EventNames = []string{"document.updated", "comment.created", "suggestion.created", "export.completed"}

// Limits are the settings of the webhook service.
type Limits struct {
	// Off refuses to create webhooks and delivers nothing.
	Off bool
	// AllowPrivate lets a webhook reach this machine and its own network.
	AllowPrivate bool
	// MaxPerProject and MaxTotal cap how many webhooks exist.
	MaxPerProject, MaxTotal int
	// RatePerMin caps the deliveries the instance sends in a minute; QueueMax and
	// PerWebhookQueueMax cap what may wait (an event over the cap is counted, not kept).
	RatePerMin, QueueMax, PerWebhookQueueMax int
	// ProjectRatePerMin is the share of that rate one project may use.
	ProjectRatePerMin int
	// ProjectQueueMax is how many deliveries may wait for one project (all its webhooks).
	ProjectQueueMax int
	// Concurrency is how many deliveries are in flight at once.
	Concurrency int
	// MaxAttempts is how many times a delivery is tried before it is dead.
	MaxAttempts int
	// KeepDays is how long finished deliveries are listed.
	KeepDays int
	// Timeout is one attempt's budget.
	Timeout time.Duration
	// DocDebounce holds a document.updated delivery back so a burst of saves is one
	// delivery. Zero is the default (five seconds); a negative value is none.
	DocDebounce time.Duration
	// AutoDisableAfter is how many deliveries in a row may die before the webhook is switched off.
	AutoDisableAfter int
}

// DefaultLimits are the settings when the server gives none.
var DefaultLimits = Limits{
	MaxPerProject: 10, MaxTotal: 200, RatePerMin: 300, QueueMax: 5000, PerWebhookQueueMax: 500, Concurrency: 4,
	ProjectRatePerMin: 120, ProjectQueueMax: 1000, MaxAttempts: 8, KeepDays: 14, Timeout: 10 * time.Second, DocDebounce: 5 * time.Second, AutoDisableAfter: 10,
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits
	pick := func(v *int, def int) {
		if *v <= 0 {
			*v = def
		}
	}
	pick(&l.MaxPerProject, d.MaxPerProject)
	pick(&l.MaxTotal, d.MaxTotal)
	pick(&l.RatePerMin, d.RatePerMin)
	pick(&l.QueueMax, d.QueueMax)
	pick(&l.ProjectRatePerMin, d.ProjectRatePerMin)
	pick(&l.ProjectQueueMax, d.ProjectQueueMax)
	pick(&l.PerWebhookQueueMax, d.PerWebhookQueueMax)
	pick(&l.Concurrency, d.Concurrency)
	pick(&l.MaxAttempts, d.MaxAttempts)
	pick(&l.KeepDays, d.KeepDays)
	pick(&l.AutoDisableAfter, d.AutoDisableAfter)
	if l.Timeout <= 0 {
		l.Timeout = d.Timeout
	}
	switch {
	case l.DocDebounce == 0:
		l.DocDebounce = d.DocDebounce
	case l.DocDebounce < 0: // an explicit "none", for tests
		l.DocDebounce = 0
	}
	return l
}

// Options configure a Service.
type Options struct {
	DB      *db.DB
	Box     *secrets.Box
	Events  *events.Store
	Log     *slog.Logger
	Version string
	Limits  Limits
	// Sender sends the requests; nil builds one from Limits.
	Sender *Sender
	// Now and Rand are the clock and the jitter source; tests inject them.
	Now  func() time.Time
	Rand func() float64
	// AfterSend is a test seam: when it returns an error the result of the attempt is
	// not recorded, as if the process had died between sending and writing.
	AfterSend func(deliveryID string) error
}

// Service owns the outbox: the feeder that fills it from the events table and
// the dispatcher that empties it.
type Service struct {
	o      Options
	lim    Limits
	sender *Sender
	rl     *rate.Limiter

	wake     chan struct{}
	feedWake chan struct{}

	mu           sync.Mutex
	inflight     map[string]bool          // webhook ids with a delivery in flight
	inflightProj map[string]int           // sends in flight per project
	projRL       map[string]*rate.Limiter // the per-project share of the delivery rate
	pinged       map[string]time.Time     // last test ping per webhook
	checked      atomic.Int64             // unix nanos of the last "are there enabled webhooks" check; 0 forces one
	enabled      atomic.Bool
	wg           sync.WaitGroup
	started      atomic.Bool
	lastWarn     atomic.Int64
	pingGap      atomic.Int64
}

// New returns a Service. It starts nothing: Start does.
func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Rand == nil {
		o.Rand = randFloat
	}
	lim := o.Limits.withDefaults()
	s := &Service{o: o, lim: lim, sender: o.Sender, wake: make(chan struct{}, 1), feedWake: make(chan struct{}, 1), inflight: map[string]bool{}, inflightProj: map[string]int{}, projRL: map[string]*rate.Limiter{}, pinged: map[string]time.Time{}}
	if s.sender == nil {
		s.sender = NewSender(SenderOptions{AllowPrivate: lim.AllowPrivate, Timeout: lim.Timeout, Version: o.Version})
	}
	per := lim.RatePerMin
	s.rl = rate.NewLimiter(rate.Limit(float64(per)/60), max(per/6, 5))
	return s
}

// Limits returns the settings in force.
func (s *Service) Limits() Limits { return s.lim }

// Box is the sealing box for secrets.
func (s *Service) Box() *secrets.Box { return s.o.Box }

// Changed tells the service that a webhook was made, changed or removed.
func (s *Service) Changed() {
	s.checked.Store(0)
	select {
	case s.feedWake <- struct{}{}:
	default:
	}
}

func (s *Service) now() time.Time { return s.o.Now().UTC() }

const tsLayout = "2006-01-02T15:04:05.000Z"

func stamp(t time.Time) string { return t.UTC().Format(tsLayout) }

// Start runs the feeder and the dispatcher until ctx is cancelled. Call Wait
// afterwards to let what is in flight finish. The state a stopped process left
// is all in the database: a delivery that was being sent is still due, so it is
// sent again (the receiver sees the same id).
func (s *Service) Start(ctx context.Context) {
	if s.lim.Off {
		// No feeder runs, so nothing may wait for one: the cursor is parked (the event log is pruned
		// as if no webhook were on) and the outbox is emptied, because a delivery that waited out the
		// time webhooks were off is a stale notification. The webhook rows are left as they are.
		if err := s.Park(ctx); err != nil {
			s.o.Log.Warn("webhooks: parking the feeder failed", "err", err)
		}
		return
	}
	if !s.started.CompareAndSwap(false, true) {
		return
	}
	// A feeder parked by a run with webhooks off starts at the newest event: what happened while
	// the feature was off is never sent.
	if err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE webhook_state SET cursor = (SELECT COALESCE(max(seq), 0) FROM events) WHERE id = 1 AND cursor < 0`)
		return err
	}); err != nil {
		s.o.Log.Warn("webhooks: moving the feeder past what happened while webhooks were off failed", "err", err)
	}
	// A delivery the last process had claimed but not finished is claimed by nobody now.
	if err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE webhook_deliveries SET claimed_at = NULL WHERE claimed_at IS NOT NULL`)
		return err
	}); err != nil {
		s.o.Log.Warn("webhooks: giving back claimed deliveries failed", "err", err)
	}
	var sub <-chan struct{}
	var cancel func()
	if s.o.Events != nil {
		sub, cancel = s.o.Events.Subscribe()
	}
	s.wg.Add(2)
	go func() {
		defer s.wg.Done()
		if cancel != nil {
			defer cancel()
		}
		s.feedLoop(ctx, sub)
	}()
	go func() {
		defer s.wg.Done()
		s.dispatchLoop(ctx)
	}()
}

// CursorParked is the feeder's cursor while no feeder can run (webhooks off on this server, or an
// instance just restored from a backup). Event sweeping ignores a parked cursor, and the next Start
// with webhooks on moves it to the newest event, so what happened meanwhile is never replayed.
const CursorParked = -1

// Park stops the outbox and the feeder's cursor from holding anything back: it parks the cursor
// (see CursorParked) and removes every delivery still waiting. Deliveries already sent or dead stay,
// as the history the Webhooks page shows.
func (s *Service) Park(ctx context.Context) error {
	return ParkState(ctx, s.o.DB)
}

// ParkState is Park for a caller that has only the database (scriobh restore).
func ParkState(ctx context.Context, d *db.DB) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE webhook_state SET cursor = ? WHERE id = 1`, CursorParked); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE state = 'pending'`)
		return err
	})
}

// Wait blocks until the loops and every send in flight have finished.
func (s *Service) Wait() { s.wg.Wait() }

func (s *Service) feedLoop(ctx context.Context, sub <-chan struct{}) {
	t := time.NewTicker(5 * time.Second) // events written by a path that did not wake us
	defer t.Stop()
	for {
		for {
			more, err := s.feed(ctx)
			if err != nil {
				s.o.Log.Warn("webhooks: reading events failed", "err", err)
				break
			}
			if !more {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-sub:
		case <-s.feedWake:
		case <-t.C:
		}
	}
}

func (s *Service) signalDispatch() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) dispatchLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	sweep := time.NewTicker(time.Hour)
	defer sweep.Stop()
	_ = s.Sweep(ctx)
	for {
		s.dispatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-t.C:
		case <-sweep.C:
			if err := s.Sweep(ctx); err != nil {
				s.o.Log.Warn("webhooks: sweep failed", "err", err)
			}
		}
	}
}

// anyEnabled says whether any webhook is switched on, without a query for
// every event: the answer is kept for a few seconds and dropped when a webhook changes.
func (s *Service) anyEnabled(ctx context.Context) bool {
	now := s.now().UnixNano()
	if at := s.checked.Load(); at != 0 && now-at < int64(5*time.Second) {
		return s.enabled.Load()
	}
	var n int
	if err := s.o.DB.Read().QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webhooks WHERE enabled = 1)`).Scan(&n); err != nil {
		return true // when unsure, look
	}
	s.enabled.Store(n > 0)
	s.checked.Store(now)
	return n > 0
}

// ---- the feeder ---------------------------------------------------------------------------

type hook struct {
	id, project string
	events      map[string]bool
	includeText bool
}

// feedBatch is how many events one feeder transaction covers. The writer is held
// for a transaction, so a backlog is worked off in small pieces and a save waits
// for at most one of them.
const feedBatch = 50

// feed moves the cursor over one batch of events, writing the outbox rows they
// make in the same transaction. more is true when the batch was full.
func (s *Service) feed(ctx context.Context) (more bool, err error) {
	if !s.anyEnabled(ctx) {
		return false, nil
	}
	var cursor, upper int64
	if err := s.o.DB.Read().QueryRowContext(ctx, `SELECT cursor FROM webhook_state WHERE id = 1`).Scan(&cursor); err != nil {
		return false, err
	}
	if err := s.o.DB.Read().QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM events`).Scan(&upper); err != nil {
		return false, err
	}
	if cursor < 0 {
		// Parked (see CursorParked) and a webhook is on: start at the newest event, never at the old backlog.
		return false, s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec(`UPDATE webhook_state SET cursor = MAX(?, 0) WHERE id = 1 AND cursor < 0`, upper)
			return err
		})
	}
	if upper <= cursor {
		return false, nil
	}
	types := make([]string, 0, len(webhookEvent))
	args := []any{cursor, upper}
	for typ := range webhookEvent {
		types = append(types, "?")
		args = append(args, typ)
	}
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT seq, type, project_id, COALESCE(document_id,''), payload_json, created_at FROM events
		WHERE seq > ? AND seq <= ? AND project_id IS NOT NULL AND type IN (`+strings.Join(types, ",")+`) ORDER BY seq LIMIT `+strconv.Itoa(feedBatch+1), args...)
	if err != nil {
		return false, err
	}
	type ev struct {
		seq                          int64
		typ, project, doc, raw, when string
	}
	var evs []ev
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.seq, &e.typ, &e.project, &e.doc, &e.raw, &e.when); err != nil {
			rows.Close()
			return false, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	newCursor := upper
	if len(evs) > feedBatch {
		evs = evs[:feedBatch]
		newCursor, more = evs[feedBatch-1].seq, true
	}
	queued := 0
	err = s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		hooks := map[string][]hook{}
		// What counts toward the instance's queue is what could be sent: waiting, for a webhook
		// that is on, in a project that is not in the trash. Rows that are not (a webhook that was
		// switched off, a project in the trash) wait out their days without taking room from
		// anyone else's.
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id JOIN projects p ON p.id = d.project_id
			WHERE d.state = 'pending' AND w.enabled = 1 AND p.trashed_at IS NULL AND p.deleted_at IS NULL`).Scan(&total); err != nil {
			return err
		}
		perHook := map[string]int{}
		perProject := map[string]int{}
		dropped := 0
		for _, e := range evs {
			hs, ok := hooks[e.project]
			if !ok {
				var err error
				if hs, err = loadHooks(tx, e.project); err != nil {
					return err
				}
				hooks[e.project] = hs
			}
			name := webhookEvent[e.typ]
			for _, h := range hs {
				if !h.events[name] {
					continue
				}
				n, seen := perHook[h.id]
				if !seen {
					if err := tx.QueryRow(`SELECT count(*) FROM webhook_deliveries WHERE webhook_id = ? AND state = 'pending'`, h.id).Scan(&n); err != nil {
						return err
					}
				}
				pn, pseen := perProject[h.project]
				if !pseen {
					if err := tx.QueryRow(`SELECT count(*) FROM webhook_deliveries WHERE project_id = ? AND state = 'pending'`, h.project).Scan(&pn); err != nil {
						return err
					}
				}
				full := total >= s.lim.QueueMax || n >= s.lim.PerWebhookQueueMax || pn >= s.lim.ProjectQueueMax
				coalesced, err := s.enqueue(tx, h, name, e.seq, e.doc, e.raw, e.when, full)
				switch {
				case err != nil:
					return err
				case coalesced == enqDropped:
					dropped++
					perHook[h.id], perProject[h.project] = n, pn
				case coalesced == enqNew:
					queued++
					total++
					perHook[h.id], perProject[h.project] = n+1, pn+1
				default: // folded into one that was waiting, or already there
					perHook[h.id], perProject[h.project] = n, pn
				}
			}
		}
		if dropped > 0 {
			if _, err := tx.Exec(`UPDATE webhook_state SET dropped = dropped + ? WHERE id = 1`, dropped); err != nil {
				return err
			}
			s.warnDropped(dropped)
		}
		// MAX: the cursor only moves forward, whatever else has written it since it was read.
		_, err := tx.Exec(`UPDATE webhook_state SET cursor = MAX(cursor, ?) WHERE id = 1`, newCursor)
		return err
	})
	if err != nil {
		return false, err
	}
	if queued > 0 {
		s.signalDispatch()
	}
	return more, nil
}

func (s *Service) warnDropped(n int) {
	now := s.now().Unix()
	if last := s.lastWarn.Load(); now-last >= 60 && s.lastWarn.CompareAndSwap(last, now) {
		s.o.Log.Warn("webhooks: the outbox is full; events are being dropped (counted in webhook_state)", "dropped", n)
	}
}

func loadHooks(tx *sql.Tx, project string) ([]hook, error) {
	rows, err := tx.Query(`SELECT id, events_json, include_text FROM webhooks WHERE project_id = ? AND enabled = 1`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []hook
	for rows.Next() {
		h := hook{project: project, events: map[string]bool{}}
		var ej string
		var it int
		if err := rows.Scan(&h.id, &ej, &it); err != nil {
			return nil, err
		}
		var names []string
		_ = json.Unmarshal([]byte(ej), &names)
		for _, n := range names {
			h.events[n] = true
		}
		h.includeText = it != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

const (
	enqNew = iota
	enqCoalesced
	enqDropped
	enqDup
)

// payload is the body of a delivery before a comment's words are added.
type payload struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	CreatedAt string         `json:"created_at"`
	ProjectID string         `json:"project_id"`
	Data      map[string]any `json:"data"`
}

// BuildPayload makes the body of a delivery from a stored event. Only what the
// event stream may carry gets through (events.Clean: ids, counts and short
// tokens); the project id is the envelope's, and a document id is added.
func BuildPayload(id, name string, projectID, documentID string, raw string, when string) ([]byte, error) {
	var in map[string]any
	_ = json.Unmarshal([]byte(raw), &in)
	data := events.Clean(in, "project_id")
	if documentID != "" {
		data["document_id"] = documentID
	}
	return json.Marshal(payload{ID: id, Type: name, CreatedAt: when, ProjectID: projectID, Data: data})
}

// enqueue writes one delivery, or folds the event into a waiting one: saves of one
// document that arrive while its notification has not been claimed by a send change
// that notification instead of making another. Folding merges: the revision is the
// highest, "content" is true if any of them changed the text, and the words are those of
// the last save that changed the text (a change of the scene's point of view after a
// save does not turn the notification into "no text changed, no words").
func (s *Service) enqueue(tx *sql.Tx, h hook, name string, seq int64, docID, raw, when string, full bool) (int, error) {
	now := s.now()
	if name == "document.updated" && docID != "" {
		var id, old string
		err := tx.QueryRow(`SELECT id, payload_json FROM webhook_deliveries WHERE webhook_id = ? AND doc_id = ? AND event = 'document.updated'
			AND state = 'pending' AND attempts = 0 AND claimed_at IS NULL ORDER BY id DESC LIMIT 1`, h.id, docID).Scan(&id, &old)
		if err == nil {
			body, err := mergePayload(id, old, name, h.project, docID, raw, when)
			if err != nil {
				return 0, err
			}
			// The update itself says "unclaimed": a send that claimed the row since the select
			// wins, and this event becomes a notification of its own.
			res, err := tx.Exec(`UPDATE webhook_deliveries SET payload_json = ?, event_seq = ? WHERE id = ? AND state = 'pending' AND attempts = 0 AND claimed_at IS NULL`, string(body), seq, id)
			if err != nil {
				return 0, err
			}
			if n, _ := res.RowsAffected(); n == 1 {
				return enqCoalesced, nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if full {
		return enqDropped, nil
	}
	id := db.NewID("whd")
	body, err := BuildPayload(id, name, h.project, docID, raw, when)
	if err != nil {
		return 0, err
	}
	due := now
	if name == "document.updated" {
		due = now.Add(s.lim.DocDebounce)
	}
	res, err := tx.Exec(`INSERT INTO webhook_deliveries(id, webhook_id, project_id, event, event_seq, doc_id, payload_json, state, attempts, next_attempt_at, created_at)
		VALUES (?,?,?,?,?,?,?, 'pending', 0, ?, ?) ON CONFLICT(webhook_id, event_seq) DO NOTHING`, id, h.id, h.project, name, seq, docID, string(body), stamp(due), stamp(now))
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return enqDup, nil // the event was already made into a delivery for this webhook
	}
	return enqNew, nil
}

// mergePayload folds a newer document.updated into the body of a waiting one.
func mergePayload(id, oldBody, name, projectID, documentID, raw, when string) ([]byte, error) {
	fresh, err := BuildPayload(id, name, projectID, documentID, raw, when)
	if err != nil {
		return nil, err
	}
	var o, n payload
	if json.Unmarshal([]byte(oldBody), &o) != nil || json.Unmarshal(fresh, &n) != nil || o.Data == nil {
		return fresh, nil
	}
	num := func(v any) (float64, bool) { f, ok := v.(float64); return f, ok }
	truthy := func(v any) bool { b, _ := v.(bool); return b }
	out := map[string]any{}
	for k, v := range o.Data {
		out[k] = v
	}
	for k, v := range n.Data {
		out[k] = v // the newest value of everything else
	}
	or, oOK := num(o.Data["revision"])
	nr, nOK := num(n.Data["revision"])
	switch {
	case oOK && nOK:
		out["revision"] = max(or, nr)
	case oOK:
		out["revision"] = or
	}
	_, hasContent := n.Data["content"]
	_, hadContent := o.Data["content"]
	if hasContent || hadContent {
		out["content"] = truthy(o.Data["content"]) || truthy(n.Data["content"])
	}
	// The words of the last save that changed the text; a later change of something else keeps them.
	if !truthy(n.Data["content"]) {
		if w, ok := o.Data["words"]; ok {
			out["words"] = w
		}
	}
	return json.Marshal(payload{ID: id, Type: name, CreatedAt: when, ProjectID: projectID, Data: out})
}

// ---- the dispatcher -----------------------------------------------------------------------

type due struct {
	id, hookID, project, event, payload string
	attempts                            int
	url, urlPath                        string
	includeText                         bool
	secret, prev, prevUntil             string
}

// dispatch starts the sends that are due and returns how many it started. It lists
// at most one due delivery for each webhook that has no send in flight (a webhook's
// deliveries go one at a time, so a long backlog of one webhook cannot fill the list and
// hide another project's), the oldest first.
func (s *Service) dispatch(ctx context.Context) (started int) {
	if ctx.Err() != nil {
		return 0
	}
	s.mu.Lock()
	free := s.lim.Concurrency - len(s.inflight)
	busy := make([]any, 0, len(s.inflight))
	for id := range s.inflight {
		busy = append(busy, id)
	}
	s.mu.Unlock()
	if free <= 0 {
		return 0
	}
	now := s.now()
	q := dueSelect + dueWhere
	args := []any{stamp(now), stamp(now.Add(-s.claimTTL()))}
	if len(busy) > 0 {
		q += ` AND w.id NOT IN (` + strings.TrimSuffix(strings.Repeat("?,", len(busy)), ",") + `)`
		args = append(args, busy...)
	}
	rows, err := s.o.DB.Read().QueryContext(ctx, q+` ORDER BY d.next_attempt_at, d.id LIMIT 100`, args...)
	if err != nil {
		s.o.Log.Warn("webhooks: reading the outbox failed", "err", err)
		return 0
	}
	var batch []due
	for rows.Next() {
		d, err := scanDue(rows)
		if err != nil {
			rows.Close()
			return 0
		}
		batch = append(batch, d)
	}
	rows.Close()
	for _, d := range batch {
		ok, stop := s.startOne(ctx, d)
		if ok {
			started++
		}
		if stop {
			break
		}
	}
	return started
}

// claimTTL is how long a claim holds when the process that made it is gone: well past
// the longest an attempt can take.
func (s *Service) claimTTL() time.Duration { return 2*s.lim.Timeout + time.Minute }

const dueCols = `d.id, d.webhook_id, d.project_id, d.event, d.payload_json, d.attempts, w.url, w.url_path_sealed, w.include_text, w.secret_sealed, w.prev_secret_sealed, w.prev_secret_until`

// dueSelect lists, for each webhook that is on and whose project is live, its oldest
// delivery that is waiting, not a ping, due and not claimed (or claimed so long ago that
// the claim is dead). The inner query is by webhook, on the (webhook_id, state, next_attempt_at) index.
const dueSelect = `SELECT ` + dueCols + `
	FROM webhooks w
	JOIN projects p ON p.id = w.project_id
	JOIN webhook_deliveries d ON d.id = (
		SELECT d2.id FROM webhook_deliveries d2
		WHERE d2.webhook_id = w.id AND d2.state = 'pending' AND d2.event <> 'ping' AND d2.next_attempt_at <= ?1 AND (d2.claimed_at IS NULL OR d2.claimed_at < ?2)
		ORDER BY d2.next_attempt_at, d2.id LIMIT 1)`

const dueWhere = ` WHERE w.enabled = 1 AND p.trashed_at IS NULL AND p.deleted_at IS NULL`

func scanDue(r rowScanner) (due, error) {
	var d due
	var it int
	if err := r.Scan(&d.id, &d.hookID, &d.project, &d.event, &d.payload, &d.attempts, &d.url, &d.urlPath, &it, &d.secret, &d.prev, &d.prevUntil); err != nil {
		return d, err
	}
	d.includeText = it != 0
	return d, nil
}

// startOne takes the webhook's one send slot (and a share of its project's slots), the
// rate tokens, and a claim on the row in the database, and starts the send. The claim is
// a write that only one sender can win and that the feeder respects (it will not fold a
// new save into a claimed row), and the row is read again after it, so what is sent is
// what the row holds at the moment of the claim. A delivery that was sent, switched off,
// trashed or replayed since it was listed is not sent. stop says the instance's rate is
// spent for this pass.
func (s *Service) startOne(ctx context.Context, d due) (started, stop bool) {
	s.mu.Lock()
	projectSlots := max(1, s.lim.Concurrency/2)
	busy := s.inflight[d.hookID] || len(s.inflight) >= s.lim.Concurrency || s.inflightProj[d.project] >= projectSlots
	if !busy {
		s.inflight[d.hookID] = true
		s.inflightProj[d.project]++
	}
	s.mu.Unlock()
	if busy {
		return false, false
	}
	release := func() {
		s.mu.Lock()
		delete(s.inflight, d.hookID)
		if s.inflightProj[d.project]--; s.inflightProj[d.project] <= 0 {
			delete(s.inflightProj, d.project)
		}
		s.mu.Unlock()
	}
	undoProject, ok := s.takeProject(d.project)
	if !ok {
		release()
		return false, false // this project has used its share of the minute; others may go on
	}
	undoGlobal, ok := take(s.rl)
	if !ok { // the instance-wide rate: what is left waits for the next tick
		undoProject()
		release()
		return false, true
	}
	giveBack := func() { undoGlobal(); undoProject(); release() }
	now := s.now()
	claimed := false
	err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		n, err := guarded(tx, `UPDATE webhook_deliveries SET claimed_at = ? WHERE id = ? AND state = 'pending' AND attempts = ? AND next_attempt_at <= ? AND (claimed_at IS NULL OR claimed_at < ?)`,
			stamp(now), d.id, d.attempts, stamp(now), stamp(now.Add(-s.claimTTL())))
		claimed = n == 1
		return err
	})
	if err != nil || !claimed {
		giveBack()
		return false, false
	}
	fresh, err := scanDue(s.o.DB.Read().QueryRowContext(ctx, `SELECT `+dueCols+` FROM webhook_deliveries d JOIN webhooks w ON w.id = d.webhook_id JOIN projects p ON p.id = d.project_id
		WHERE d.id = ? AND d.state = 'pending' AND w.enabled = 1 AND p.trashed_at IS NULL AND p.deleted_at IS NULL`, d.id))
	if err != nil {
		s.unclaim(ctx, d.id)
		giveBack()
		return false, false
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			release()
			s.signalDispatch() // the next one for this webhook need not wait for the tick
		}()
		s.attempt(ctx, fresh, false)
	}()
	return true, false
}

// unclaim gives a delivery back, for an attempt that ended before it was recorded.
func (s *Service) unclaim(ctx context.Context, id string) {
	_ = s.o.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE webhook_deliveries SET claimed_at = NULL WHERE id = ?`, id)
		return err
	})
}

// take reserves one token of a limiter and returns a function that gives it back.
func take(l *rate.Limiter) (undo func(), ok bool) {
	r := l.Reserve()
	if !r.OK() || r.Delay() > 0 {
		r.Cancel()
		return nil, false
	}
	return r.Cancel, true
}

// takeProject spends one of a project's deliveries for the minute (a project may
// not use more than ProjectRatePerMin of the instance's rate), and returns a
// function that gives it back.
func (s *Service) takeProject(project string) (undo func(), ok bool) {
	s.mu.Lock()
	l := s.projRL[project]
	if l == nil {
		if len(s.projRL) > 2000 {
			s.projRL = map[string]*rate.Limiter{}
		}
		per := s.lim.ProjectRatePerMin
		l = rate.NewLimiter(rate.Limit(float64(per)/60), max(per/6, 5))
		s.projRL[project] = l
	}
	s.mu.Unlock()
	return take(l)
}

// attempt sends one delivery and records the outcome. once makes a failure final
// (a test ping is not retried).
func (s *Service) attempt(ctx context.Context, d due, once bool) Result {
	secret, err := s.o.Box.Open(d.secret, "webhooks/"+d.hookID+"/secret")
	if err != nil {
		// Restored without its key, or the key changed: the secret cannot be read, so
		// nothing can be signed. The webhook is switched off with a reason, not retried.
		_ = s.disable(ctx, d.hookID, "The signing secret cannot be read with this installation's secret key. Rotate the secret, then switch the webhook back on.")
		s.unclaim(ctx, d.id)
		return Result{Class: "secret"}
	}
	target := d.url
	if d.urlPath != "" {
		rest, err := s.o.Box.Open(d.urlPath, "webhooks/"+d.hookID+"/path")
		if err != nil {
			_ = s.disable(ctx, d.hookID, "The address cannot be read with this installation's secret key. Set the address again, then switch the webhook back on.")
			s.unclaim(ctx, d.id)
			return Result{Class: "secret"}
		}
		target += string(rest)
	}
	var prev string
	if d.prev != "" && d.prevUntil > stamp(s.now()) {
		if p, err := s.o.Box.Open(d.prev, "webhooks/"+d.hookID+"/prev"); err == nil {
			prev = string(p)
		}
	}
	body := []byte(d.payload)
	if d.includeText && d.event == "comment.created" {
		body = s.withCommentText(ctx, body)
	}
	ts := s.now().Unix()
	h := http.Header{}
	h.Set(HeaderID, d.id)
	h.Set(HeaderEvent, d.event)
	h.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
	h.Set(HeaderAttempt, strconv.Itoa(d.attempts+1))
	h.Set(HeaderSignature, SignatureHeader(ts, body, string(secret), prev))
	res := s.sender.Send(ctx, target, h, body)
	if s.o.AfterSend != nil && s.o.AfterSend(d.id) != nil {
		return res
	}
	if ctx.Err() != nil {
		s.unclaim(ctx, d.id) // the server is stopping: the attempt did not finish, so it is not counted and is made again on the next start
		return res
	}
	if err := s.record(context.WithoutCancel(ctx), d, res, once); err != nil {
		s.o.Log.Warn("webhooks: recording a delivery failed", "err", err)
	}
	return res
}

// withCommentText adds the words of the comment to a comment.created body, for a
// webhook whose owner asked for them. They are read now, not kept: a comment
// that was deleted since is not sent.
func (s *Service) withCommentText(ctx context.Context, body []byte) []byte {
	var p payload
	if json.Unmarshal(body, &p) != nil {
		return body
	}
	cid, _ := p.Data["comment_id"].(string)
	if cid == "" {
		return body
	}
	var text string
	err := s.o.DB.Read().QueryRowContext(ctx, `SELECT body FROM comments WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, cid, p.ProjectID).Scan(&text)
	if err != nil {
		return body
	}
	if r := []rune(text); len(r) > MaxCommentText {
		text = string(r[:MaxCommentText])
		p.Data["text_truncated"] = true
	}
	p.Data["text"] = text
	out, err := json.Marshal(p)
	if err != nil {
		return body
	}
	return out
}

// MaxCommentText is how many characters of a comment a webhook that asked for the text receives.
const MaxCommentText = 2000

func (s *Service) disable(ctx context.Context, hookID, reason string) error {
	defer s.Changed()
	return s.o.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE webhooks SET enabled = 0, disabled_reason = ?, updated_at = ? WHERE id = ?`, reason, stamp(s.now()), hookID)
		return err
	})
}

// record writes what an attempt came to.
func (s *Service) record(ctx context.Context, d due, res Result, once bool) error {
	now := s.now()
	attempts := d.attempts + 1
	if s.lim.AllowPrivate {
		// With local targets allowed, a failing answer could be a page of an internal
		// service: only its status and the kind of failure are kept.
		res.Excerpt = ""
	}
	return s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		switch {
		case res.OK():
			if n, err := guarded(tx, `UPDATE webhook_deliveries SET state = 'delivered', attempts = ?, last_status = ?, last_error = '', last_excerpt = '', delivered_at = ?, claimed_at = NULL WHERE id = ? AND state = 'pending' AND attempts = ?`,
				attempts, res.Status, stamp(now), d.id, d.attempts); err != nil || n == 0 {
				return err // n == 0: another attempt or a replay got there first, and its result stands
			}
			_, err := tx.Exec(`UPDATE webhooks SET failure_streak = 0, last_delivery_at = ?, last_status = ? WHERE id = ?`, stamp(now), res.Status, d.hookID)
			return err
		case once || res.Permanent() || attempts >= s.lim.MaxAttempts:
			if n, err := guarded(tx, `UPDATE webhook_deliveries SET state = 'dead', attempts = ?, last_status = ?, last_error = ?, last_excerpt = ?, claimed_at = NULL WHERE id = ? AND state = 'pending' AND attempts = ?`,
				attempts, res.Status, res.Class, res.Excerpt, d.id, d.attempts); err != nil || n == 0 {
				return err
			}
			if d.event == "ping" {
				_, err := tx.Exec(`UPDATE webhooks SET last_delivery_at = ?, last_status = ? WHERE id = ?`, stamp(now), res.Status, d.hookID)
				return err
			}
			if _, err := tx.Exec(`UPDATE webhooks SET failure_streak = failure_streak + 1, last_delivery_at = ?, last_status = ? WHERE id = ?`, stamp(now), res.Status, d.hookID); err != nil {
				return err
			}
			off, err := guarded(tx, `UPDATE webhooks SET enabled = 0, disabled_reason = ? WHERE id = ? AND enabled = 1 AND failure_streak >= ?`,
				"Switched off: the last "+strconv.Itoa(s.lim.AutoDisableAfter)+" deliveries all failed for good. Fix the receiver, then switch the webhook back on.", d.hookID, s.lim.AutoDisableAfter)
			if err == nil && off == 1 {
				err = DropPending(tx, d.hookID) // a webhook that is off keeps no queue
			}
			return err
		default:
			wait := Backoff(attempts, s.o.Rand())
			if res.RetryAfter > wait {
				wait = res.RetryAfter
			}
			_, err := guarded(tx, `UPDATE webhook_deliveries SET attempts = ?, last_status = ?, last_error = ?, last_excerpt = ?, next_attempt_at = ?, claimed_at = NULL WHERE id = ? AND state = 'pending' AND attempts = ?`,
				attempts, res.Status, res.Class, res.Excerpt, stamp(now.Add(wait)), d.id, d.attempts)
			return err
		}
	})
}

// DropPending deletes a webhook's waiting deliveries (not a test ping's, which is never
// waiting): what a webhook that was switched off never sent is not sent when it is
// switched back on, and does not hold room in the queue meanwhile.
func DropPending(tx *sql.Tx, hookID string) error {
	_, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE webhook_id = ? AND state = 'pending'`, hookID)
	return err
}

// guarded runs an update and says how many rows it changed.
func guarded(tx *sql.Tx, q string, args ...any) (int64, error) {
	res, err := tx.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Sweep removes finished deliveries past their keeping time.
func (s *Service) Sweep(ctx context.Context) error {
	cut := stamp(s.now().Add(-time.Duration(s.lim.KeepDays) * 24 * time.Hour))
	cutDead := stamp(s.now().Add(-2 * time.Duration(s.lim.KeepDays) * 24 * time.Hour))
	return s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE state = 'delivered' AND delivered_at < ?`, cut); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE state = 'dead' AND created_at < ?`, cutDead); err != nil {
			return err
		}
		// A delivery that has waited a whole keeping time (a project in the trash that was
		// never restored, a webhook whose secret was never rotated) is dropped.
		_, err := tx.Exec(`DELETE FROM webhook_deliveries WHERE state = 'pending' AND created_at < ?`, cut)
		return err
	})
}

// ---- helpers for the ops --------------------------------------------------------------------

// Ping sends a signed test delivery to one webhook now and records it. It is
// the only thing a person's request makes the server send, so it is limited: one
// per webhook every few seconds.
func (s *Service) Ping(ctx context.Context, hookID string) (deliveryID string, res Result, err error) {
	if s.lim.Off {
		return "", Result{}, errors.New("off")
	}
	now := s.now()
	s.mu.Lock()
	if last, ok := s.pinged[hookID]; ok && now.Sub(last) < s.pingEvery() {
		s.mu.Unlock()
		return "", Result{}, ErrPingTooSoon
	}
	if len(s.pinged) > 1000 {
		for id, at := range s.pinged {
			if now.Sub(at) >= s.pingEvery() {
				delete(s.pinged, id)
			}
		}
	}
	s.pinged[hookID] = now
	s.mu.Unlock()
	var d due
	var it int
	err = s.o.DB.Read().QueryRowContext(ctx, `SELECT id, project_id, url, url_path_sealed, include_text, secret_sealed, prev_secret_sealed, prev_secret_until FROM webhooks WHERE id = ?`, hookID).
		Scan(&d.hookID, &d.project, &d.url, &d.urlPath, &it, &d.secret, &d.prev, &d.prevUntil)
	if err != nil {
		return "", Result{}, err
	}
	d.id, d.event = db.NewID("whd"), "ping"
	body, _ := json.Marshal(payload{ID: d.id, Type: "ping", CreatedAt: stamp(now), ProjectID: d.project, Data: map[string]any{"webhook_id": d.hookID}})
	d.payload = string(body)
	if err := s.o.DB.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO webhook_deliveries(id, webhook_id, project_id, event, event_seq, doc_id, payload_json, state, attempts, next_attempt_at, created_at)
			VALUES (?,?,?, 'ping', NULL, '', ?, 'pending', 0, ?, ?)`, d.id, d.hookID, d.project, d.payload, stamp(now), stamp(now))
		return err
	}); err != nil {
		return "", Result{}, err
	}
	res = s.attempt(ctx, d, true)
	// A ping is never the dispatcher's: whatever stopped the attempt before it was
	// recorded (a secret that cannot be opened, a cancelled request), it ends here.
	_ = s.o.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE webhook_deliveries SET state = 'dead', attempts = 1, last_error = ? WHERE id = ? AND state = 'pending'`, pingStopped(res), d.id)
		return err
	})
	return d.id, res, nil
}

func pingStopped(r Result) string {
	if r.Class != "" {
		return r.Class
	}
	return "cancelled"
}

// PingInterval is the least time between two test pings of one webhook.
const PingInterval = 5 * time.Second

// ErrPingTooSoon is the refusal for a test ping asked for too soon after the last.
var ErrPingTooSoon = errors.New("webhook: test ping too soon")

func (s *Service) pingEvery() time.Duration {
	if d := time.Duration(s.pingGap.Load()); d != 0 {
		return d
	}
	return PingInterval
}

// SetPingInterval changes the least time between two test pings of one webhook
// (a test seam; a negative value means no limit).
func (s *Service) SetPingInterval(d time.Duration) { s.pingGap.Store(int64(d)) }

// SetSender replaces the sender (a test seam: a resolver that answers from a
// table, a loopback server that counts as public).
func (s *Service) SetSender(sn *Sender) { s.sender = sn }

func randFloat() float64 {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0.5
	}
	return float64(binary.BigEndian.Uint64(b[:])>>11) / float64(1<<53)
}

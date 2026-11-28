package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// The hub fans committed events out to live subscribers. It is a wake-up
// consumer of the Store, like the mirror: one goroutine reads the rows that are
// new since its cursor and hands each to the subscribers of its project and of
// its Compendium. Nothing polls per client. The goroutine exists only while at
// least one subscriber does, so an idle server runs none of it.
//
// Replay and live delivery do not overlap. Subscribe records the hub's cursor
// under the lock that delivery also takes; the hub only ever delivers events
// after that cursor, so the caller replays from the table up to Start and then
// reads the channel, with no gap and no duplicate.

// Scope is what a subscriber follows.
type Scope struct {
	Kind string // "project" or "compendium"
	ID   string
}

// Scope kinds.
const (
	ScopeProject    = "project"
	ScopeCompendium = "compendium"
)

// Event is one committed event as the stream reads it. Payload is the raw
// stored map; Clean it before it leaves the process.
type Event struct {
	Seq          int64
	Type         string
	ProjectID    string
	DocumentID   string
	CompendiumID string
	ActorID      string
	Payload      map[string]any
	At           string
}

// Subscription is one live follower. Close it when done.
type Subscription struct {
	C     <-chan Event
	Start int64 // the hub's cursor when it subscribed: replay events up to here, then read C

	ch      chan Event
	scope   Scope
	hub     *Hub
	mu      sync.Mutex
	dropped bool
	closed  bool
}

// Dropped reports whether the hub gave up on this subscriber because its buffer
// filled (it was too slow). C is closed when that happens.
func (s *Subscription) Dropped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Close stops delivery. It is safe to call twice.
func (s *Subscription) Close() { s.hub.remove(s) }

// Hub is the in-process event fan-out.
type Hub struct {
	store  *Store
	buffer int
	fall   time.Duration

	mu      sync.Mutex
	cursor  int64
	subs    map[Scope]map[*Subscription]struct{}
	running bool
	done    chan struct{} // closed when the loop exits
	stop    chan struct{}
}

// HubOptions tune a Hub.
type HubOptions struct {
	// Buffer is how many events a subscriber may fall behind before it is
	// dropped (default 256).
	Buffer int
	// Fallback is the longest the hub waits without a wake-up before looking at
	// the table anyway, in case an event was written by a path that did not
	// wake it (default 5 s).
	Fallback time.Duration
}

// NewHub returns a hub over the store's events. It starts nothing.
func NewHub(s *Store, o HubOptions) *Hub {
	if o.Buffer <= 0 {
		o.Buffer = 256
	}
	if o.Fallback <= 0 {
		o.Fallback = 5 * time.Second
	}
	return &Hub{store: s, buffer: o.Buffer, fall: o.Fallback, subs: map[Scope]map[*Subscription]struct{}{}}
}

// Subscribe starts following a scope.
func (h *Hub) Subscribe(sc Scope) *Subscription {
	// A loop that was stopped by the last unsubscribe may still be finishing a
	// batch. Wait until it has exited before starting another, or two loops could
	// deliver one event twice and move the cursor backwards.
	for {
		h.mu.Lock()
		if h.running || h.done == nil {
			break
		}
		d := h.done
		h.mu.Unlock()
		<-d
		h.mu.Lock()
		if h.done == d {
			h.done = nil
		}
		h.mu.Unlock()
	}
	defer h.mu.Unlock()
	if !h.running {
		// The first subscriber: pick the cursor up at the newest event, so the
		// loop never replays history to anyone.
		h.cursor = h.maxSeq()
		h.running = true
		h.stop = make(chan struct{})
		h.done = make(chan struct{})
		go h.loop(h.stop, h.done)
	}
	ch := make(chan Event, h.buffer)
	s := &Subscription{C: ch, ch: ch, Start: h.cursor, scope: sc, hub: h}
	m := h.subs[sc]
	if m == nil {
		m = map[*Subscription]struct{}{}
		h.subs[sc] = m
	}
	m[s] = struct{}{}
	return s
}

func (h *Hub) remove(s *Subscription) {
	h.mu.Lock()
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.ch)
	}
	s.mu.Unlock()
	if m := h.subs[s.scope]; m != nil {
		delete(m, s)
		if len(m) == 0 {
			delete(h.subs, s.scope)
		}
	}
	idle := len(h.subs) == 0 && h.running
	var done chan struct{}
	if idle {
		h.running = false
		close(h.stop)
		done = h.done
	}
	h.mu.Unlock()
	if done != nil {
		<-done
	}
}

// Subscribers counts live subscriptions, for tests and diagnostics.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.subs {
		n += len(m)
	}
	return n
}

// Cursor is the newest sequence number the hub has delivered.
func (h *Hub) Cursor() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cursor
}

// maxSeq reads the newest event number. The caller holds h.mu (the read is a
// single indexed lookup).
func (h *Hub) maxSeq() int64 {
	var n int64
	_ = h.store.DB.Read().QueryRow(`SELECT COALESCE(max(seq), 0) FROM events`).Scan(&n)
	return n
}

func (h *Hub) loop(stop, done chan struct{}) {
	defer close(done)
	wake, cancel := h.store.Subscribe()
	defer cancel()
	t := time.NewTicker(h.fall)
	defer t.Stop()
	for {
		h.drain()
		select {
		case <-stop:
			return
		case <-wake:
		case <-t.C:
		}
	}
}

const hubBatch = 500

// drain delivers every event after the cursor.
func (h *Hub) drain() {
	for {
		h.mu.Lock()
		if len(h.subs) == 0 {
			h.mu.Unlock()
			return
		}
		after := h.cursor
		h.mu.Unlock()
		evs, err := readAfter(context.Background(), h.store.DB.Read(), after, hubBatch)
		if err != nil || len(evs) == 0 {
			return
		}
		for _, e := range evs {
			h.deliver(e)
		}
		if len(evs) < hubBatch {
			return
		}
	}
}

func (h *Hub) deliver(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e.Seq <= h.cursor {
		return // already delivered, or before this loop's first subscriber: never twice, never backwards
	}
	if e.ProjectID != "" {
		h.send(Scope{ScopeProject, e.ProjectID}, e)
	}
	if e.CompendiumID != "" {
		h.send(Scope{ScopeCompendium, e.CompendiumID}, e)
	}
	h.cursor = e.Seq
}

// send hands e to every subscriber of sc; one whose buffer is full is dropped.
// The caller holds h.mu.
func (h *Hub) send(sc Scope, e Event) {
	for s := range h.subs[sc] {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			continue
		}
		select {
		case s.ch <- e:
			s.mu.Unlock()
		default:
			s.dropped, s.closed = true, true
			close(s.ch)
			s.mu.Unlock()
			delete(h.subs[sc], s)
		}
	}
	if len(h.subs[sc]) == 0 {
		delete(h.subs, sc)
	}
}

// ---- reading the table ----------------------------------------------------------------

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const eventCols = `seq, type, COALESCE(project_id,''), COALESCE(document_id,''), COALESCE(compendium_id,''), COALESCE(actor_id,''), payload_json, created_at`

func scanEvents(rows *sql.Rows) ([]Event, error) {
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var raw string
		if err := rows.Scan(&e.Seq, &e.Type, &e.ProjectID, &e.DocumentID, &e.CompendiumID, &e.ActorID, &raw, &e.At); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

func readAfter(ctx context.Context, q queryer, after int64, limit int) ([]Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE seq > ? ORDER BY seq LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	return scanEvents(rows)
}

// Replay is the answer to "what happened after seq N in this scope".
type Replay struct {
	Events []Event
	// Reset is true when the client cannot be caught up from the table (it is
	// further behind than the window, or its number is from the future, as after
	// a restore) and must reload instead.
	Reset bool
}

// ReplayWindow bounds how far back a client can resume.
type ReplayWindow struct {
	Events int           // at most this many events (default 1000)
	Age    time.Duration // and none older than this (default 24 h)
	// Since is when the caller's access began (a stored time). An event older
	// than it is never replayed: the client is told to reload instead, so a
	// member added today cannot read what happened before they were.
	Since string
}

// Defaults for a ReplayWindow.
const (
	DefaultReplayEvents = 1000
	DefaultReplayAge    = 24 * time.Hour
)

// ReplayScope reads the events of a scope after seq `after` up to and
// including `upTo` (the subscription's Start). now is the clock for the age
// window.
func (s *Store) ReplayScope(ctx context.Context, sc Scope, after, upTo int64, w ReplayWindow, now time.Time) (Replay, error) {
	if w.Events <= 0 {
		w.Events = DefaultReplayEvents
	}
	if w.Age <= 0 {
		w.Age = DefaultReplayAge
	}
	var newest int64
	if err := s.DB.Read().QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM events`).Scan(&newest); err != nil {
		return Replay{}, err
	}
	if after > newest {
		return Replay{Reset: true}, nil
	}
	col := "project_id"
	if sc.Kind == ScopeCompendium {
		col = "compendium_id"
	}
	rows, err := s.DB.Read().QueryContext(ctx, `SELECT `+eventCols+` FROM events WHERE `+col+` = ? AND seq > ? AND seq <= ? ORDER BY seq LIMIT ?`,
		sc.ID, after, upTo, w.Events+1)
	if err != nil {
		return Replay{}, err
	}
	evs, err := scanEvents(rows)
	if err != nil {
		return Replay{}, err
	}
	if len(evs) > w.Events {
		return Replay{Reset: true}, nil
	}
	if w.Since != "" {
		for _, e := range evs {
			if e.At < w.Since { // both are stored times: the same layout compares as text
				return Replay{Reset: true}, nil
			}
		}
	}
	if len(evs) > 0 {
		if t, err := db.Parse(evs[0].At); err == nil && now.Sub(t) > w.Age {
			return Replay{Reset: true}, nil
		}
	}
	return Replay{Events: evs}, nil
}

// Newest is the newest event number.
func (s *Store) Newest(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.Read().QueryRowContext(ctx, `SELECT COALESCE(max(seq), 0) FROM events`).Scan(&n)
	return n, err
}

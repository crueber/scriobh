// Package events writes durable event and audit rows and wakes in-process
// listeners. The hub is only a wake-up signal: replay and live tailing are
// one code path (read the table from a sequence number).
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Store implements ops.Sink over the events and audit_log tables.
type Store struct {
	DB  *db.DB
	Log *slog.Logger

	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// NewStore returns a Store.
func NewStore(d *db.DB, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{DB: d, Log: log, subs: map[chan struct{}]struct{}{}}
}

// Flush writes events and audit rows in one short transaction after the
// content transaction has committed. Errors are logged, never returned to
// the writer: a committed save must not fail because an event row did.
func (s *Store) Flush(ctx context.Context, actorID string, evs []ops.Event, audit []ops.AuditEntry) error {
	err := s.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		return s.FlushTx(tx, actorID, evs, audit)
	})
	if err != nil {
		s.Log.Error("event flush failed", "err", err)
		return err
	}
	if len(evs) > 0 {
		s.wake()
	}
	return nil
}

// FlushTx writes events and audit rows inside tx. Listeners are woken by the
// caller after the transaction commits (Flush does; for FlushTx callers the
// next poll sees the rows, which is all replay needs).
func (s *Store) FlushTx(tx *sql.Tx, actorID string, evs []ops.Event, audit []ops.AuditEntry) error {
	now := db.Now()
	for _, e := range evs {
		payload, _ := json.Marshal(e.Payload)
		if e.Payload == nil {
			payload = []byte("{}")
		}
		if _, err := tx.Exec(`INSERT INTO events(type, project_id, document_id, compendium_id, actor_id, payload_json, created_at)
			VALUES (?,?,?,?,?,?,?)`, e.Type, nullIfEmpty(e.ProjectID), nullIfEmpty(e.DocumentID), nullIfEmpty(e.CompendiumID), nullIfEmpty(actorID), string(payload), now); err != nil {
			return err
		}
	}
	for _, a := range audit {
		detail, _ := json.Marshal(a.Detail)
		if a.Detail == nil {
			detail = []byte("{}")
		}
		if _, err := tx.Exec(`INSERT INTO audit_log(id, at, actor_id, action, target_type, target_id, detail_json)
			VALUES (?,?,?,?,?,?,?)`, db.NewID("aud"), now, nullIfEmpty(actorID), a.Action, a.TargetType, a.TargetID, string(detail)); err != nil {
			return err
		}
	}
	return nil
}

// Audit writes one audit line outside an op (login, logout).
func (s *Store) Audit(ctx context.Context, actorID string, a ops.AuditEntry, ipHash string) {
	detail, _ := json.Marshal(a.Detail)
	if a.Detail == nil {
		detail = []byte("{}")
	}
	err := s.DB.WithTx(context.WithoutCancel(ctx), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO audit_log(id, at, actor_id, action, target_type, target_id, ip_hash, detail_json)
			VALUES (?,?,?,?,?,?,?,?)`, db.NewID("aud"), db.Now(), nullIfEmpty(actorID), a.Action, a.TargetType, a.TargetID, ipHash, string(detail))
		return err
	})
	if err != nil {
		s.Log.Error("audit write failed", "err", err, "action", a.Action)
	}
}

// Subscribe returns a channel that receives a signal after new events are
// written, and a cancel function.
func (s *Store) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

// Wake signals subscribers that new events were committed.
func (s *Store) Wake() { s.wake() }

func (s *Store) wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

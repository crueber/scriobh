package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
)

// GET /api/v1/events is the live-update stream (server-sent events). The
// protocol is in docs/ARCHITECTURE.md ("Live updates"); the decisions behind
// the limits are in docs/OPEN-QUESTIONS.md ("Phase 3 research and streaming
// decisions").
//
// It is authorized exactly like the read op for what it follows (project.get or
// compendium.get), through the registry, so a key's scope and pin, the 404 for
// no access and an instance admin's reach all behave as they do for the op. It
// is then re-authorized on every heartbeat, so a removed member, a revoked key
// or a signed-out session loses the stream within about half a minute.

const (
	sseHeartbeat  = 25 * time.Second
	sseWriteLimit = 10 * time.Second
	sseRetryMs    = 3000
)

// sseState is the stream limits and the shutdown signal.
type sseState struct {
	mu      sync.Mutex
	total   int
	perUser map[string]int

	stop     chan struct{}
	stopOnce sync.Once
}

func newSSEState() *sseState { return &sseState{perUser: map[string]int{}, stop: make(chan struct{})} }

// acquire takes a slot for user, or says which limit refused it.
func (st *sseState) acquire(user string, perUser, total int) (release func(), why string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if total > 0 && st.total >= total {
		return nil, "This server has reached its limit of open live-update streams."
	}
	if perUser > 0 && st.perUser[user] >= perUser {
		return nil, "You have too many live-update streams open; close one and try again."
	}
	st.total++
	st.perUser[user]++
	return func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		st.total--
		if st.perUser[user]--; st.perUser[user] <= 0 {
			delete(st.perUser, user)
		}
	}, ""
}

// StopStreams ends every open event stream and refuses new ones. Serve calls
// it before shutting the HTTP server down, which would otherwise wait for
// streams that never finish on their own.
func (s *Server) StopStreams() { s.sse.stopOnce.Do(func() { close(s.sse.stop) }) }

// OpenStreams counts open event streams, for tests and diagnostics.
func (s *Server) OpenStreams() int {
	s.sse.mu.Lock()
	defer s.sse.mu.Unlock()
	return s.sse.total
}

// authorizeRead runs the registry's own checks for a read op that names one
// project or Compendium.
func (s *Server) authorizeRead(ctx context.Context, p ops.Principal, op, id string) error {
	e, ok := s.Reg.Lookup(op)
	if !ok {
		return ops.Internal(nil)
	}
	if err := s.Reg.PreAuthorize(p, e); err != nil {
		return err
	}
	in := e.NewIn()
	v := reflect.ValueOf(in).Elem()
	set := false
	for _, f := range ops.InputFields(e.In) {
		if f.Location == "path" && f.Name == "id" {
			v.Field(f.Index).SetString(id)
			set = true
		}
	}
	if !set {
		return ops.Internal(nil)
	}
	return s.Reg.AuthorizeInput(ctx, p, e, in)
}

// streamCredentials re-reads the caller from what the request presented, so a
// revoked key or an ended session is noticed mid-stream.
func (s *Server) streamPrincipal(ctx context.Context, r *http.Request, a reqAuth) (ops.Principal, bool) {
	if a.KeyID != "" {
		secret, present := auth.KeyFromRequest(r.Header.Get("Authorization"), r.Header.Get("X-API-Key"))
		if !present {
			return ops.Principal{}, false
		}
		p, _, err := s.Keys.Lookup(ctx, secret)
		return p, err == nil
	}
	c, err := r.Cookie(auth.SessionCookie)
	if err != nil {
		return ops.Principal{}, false
	}
	p, _, err := s.Sess.Peek(ctx, c.Value) // not activity: see Sessions.Peek
	return p, err == nil
}

// streamEvent is what a client receives. It never carries a title, a name, an
// address or text: see events.Clean.
type streamEvent struct {
	Seq          int64          `json:"seq"`
	Type         string         `json:"type"`
	ProjectID    string         `json:"project_id,omitempty"`
	DocumentID   string         `json:"document_id,omitempty"`
	CompendiumID string         `json:"compendium_id,omitempty"`
	ByYou        bool           `json:"by_you"`
	At           string         `json:"at"`
	Payload      map[string]any `json:"payload"`
}

// shape turns a stored event into what one scope's subscribers may see. A
// project stream carries nothing about a Compendium (a project reader is not
// thereby a Compendium member), and a Compendium stream nothing about a
// project.
func shape(e events.Event, sc events.Scope, user string, anonymous bool) streamEvent {
	out := streamEvent{Seq: e.Seq, Type: e.Type, DocumentID: e.DocumentID, ByYou: e.ActorID != "" && e.ActorID == user, At: e.At}
	switch sc.Kind {
	case events.ScopeProject:
		out.ProjectID = e.ProjectID
		out.Payload = events.Clean(e.Payload, "compendium_id", "target_compendium_id", "previous", "entity_id", "to_entity_id", "source_id", "target_id", "event_id")
		if anonymous {
			// Which bible operation happened is the bible's business: someone on
			// the book who is not on the Compendium hears only that it changed.
			out.Type, out.DocumentID, out.Payload = "compendium.changed", "", map[string]any{}
		}
	default:
		out.CompendiumID = e.CompendiumID
		out.DocumentID = ""
		// The Compendium a reference points at is another Compendium's id: a
		// reader of this one is not thereby a member of that one.
		out.Payload = events.Clean(e.Payload, "project_id", "target_compendium_id", "previous", "compendium_id")
	}
	return out
}

// reviewType reports whether an event type is about review data (comments and
// suggestions), which only a reviewer, an editor or the owner may hear of.
func reviewType(t string) bool {
	return strings.HasPrefix(t, "comment.") || strings.HasPrefix(t, "suggestion.") || strings.HasPrefix(t, "review.")
}

// bibleType reports whether an event type is about a Compendium's material (the
// bible, its references and its overrides) rather than the book.
func bibleType(t string) bool {
	return strings.HasPrefix(t, "entity.") || strings.HasPrefix(t, "compendium.") || strings.HasPrefix(t, "project.compendium.")
}

// accessSince is when the caller's access to the scope began: their membership
// of it and, for a key, the key's creation, whichever is later. Empty for an
// administrator with no membership and no key, who could always read it.
func (s *Server) accessSince(ctx context.Context, p ops.Principal, a reqAuth, sc events.Scope) string {
	table, col := "project_members", "project_id"
	if sc.Kind == events.ScopeCompendium {
		table, col = "compendium_members", "compendium_id"
	}
	var since string
	_ = s.DB.Read().QueryRowContext(ctx, `SELECT created_at FROM `+table+` WHERE `+col+` = ? AND user_id = ?`, sc.ID, p.UserID).Scan(&since)
	if a.KeyID != "" {
		var kc string
		if err := s.DB.Read().QueryRowContext(ctx, `SELECT created_at FROM api_keys WHERE id = ?`, a.KeyID).Scan(&kc); err == nil && kc > since {
			since = kc
		}
	}
	return since
}

func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r.Context())
	p := a.P
	if !p.Authenticated() {
		writeJSONError(w, ops.Unauthenticated())
		return
	}
	q := r.URL.Query()
	project, compendium := q.Get("project"), q.Get("compendium")
	var sc events.Scope
	var op string
	switch {
	case (project == "") == (compendium == ""):
		writeJSONError(w, ops.Invalid("project", "Name exactly one of project and compendium."))
		return
	case project != "":
		sc, op = events.Scope{Kind: events.ScopeProject, ID: project}, "project.get"
	default:
		sc, op = events.Scope{Kind: events.ScopeCompendium, ID: compendium}, "compendium.get"
	}
	// An open stream is not activity: asking again every half minute whether the caller may still read must not keep an
	// unlock of a project with a passphrase alive past its idle limit (F-252). When it is locked the stream ends.
	if err := s.authorizeRead(ops.Passive(r.Context()), p, op, sc.ID); err != nil {
		writeJSONError(w, ops.AsError(err))
		return
	}
	select {
	case <-s.sse.stop:
		writeJSONError(w, &ops.Error{Code: ops.CodeRateLimited, Message: "The server is shutting down.", RetryAfterSeconds: 5})
		return
	default:
	}
	release, why := s.sse.acquire(p.UserID, s.Cfg.SSEPerUser, s.Cfg.SSETotal)
	if release == nil {
		writeJSONError(w, &ops.Error{Code: ops.CodeRateLimited, Message: why, RetryAfterSeconds: 10})
		return
	}
	defer release()

	last := int64(0)
	hasLast := false
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
			last, hasLast = n, true
		}
	} else if v := q.Get("last_event_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			last, hasLast = n, true
		}
	}

	rc := http.NewResponseController(w)
	// The server's read and write timeouts are for requests that end; this one
	// does not. Each write sets its own deadline, which is also how a client that
	// stopped reading is noticed.
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-store")
	h.Set("X-Accel-Buffering", "no")
	h.Set("Connection", "keep-alive")

	sub := s.Hub.Subscribe(sc)
	defer sub.Close()
	ctx := ops.Passive(r.Context())
	put := func(frame string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteLimit))
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	special := func(name string, seq int64, data map[string]any) bool {
		b, _ := json.Marshal(data)
		return put("id: " + strconv.FormatInt(seq, 10) + "\nevent: " + name + "\ndata: " + string(b) + "\n\n")
	}
	send := func(e events.Event) bool {
		// A project's events are for a session that may still read it: the gate is asked for every one (14 microseconds), so a
		// locked project, an idle expiry or a new passphrase stops the stream at the next event, not at the next heartbeat.
		if sc.Kind == events.ScopeProject && s.authorizeRead(ctx, p, op, sc.ID) != nil {
			special("bye", e.Seq, map[string]any{"reason": "access"})
			return false
		}
		// Review events (a comment was written, a suggestion made) are for the people
		// who can read comments; a project reader is not one of them.
		if sc.Kind == events.ScopeProject && reviewType(e.Type) && s.authorizeRead(ctx, p, "comment.list", sc.ID) != nil {
			return true
		}
		// An export finishing is for the people who can run one.
		if sc.Kind == events.ScopeProject && strings.HasPrefix(e.Type, "export.") && s.authorizeRead(ctx, p, "export.markdown", sc.ID) != nil {
			return true
		}
		anon := false
		if sc.Kind == events.ScopeProject && bibleType(e.Type) {
			primary := ""
			if env := s.Reg.Env(); env != nil && env.ProjectPrimary != nil {
				primary = env.ProjectPrimary(ctx, sc.ID)
			}
			anon = primary == "" || s.authorizeRead(ctx, p, "compendium.get", primary) != nil
		}
		ev := shape(e, sc, p.UserID, anon)
		b, _ := json.Marshal(ev)
		return put("id: " + strconv.FormatInt(e.Seq, 10) + "\nevent: " + ev.Type + "\ndata: " + string(b) + "\n\n")
	}
	w.WriteHeader(http.StatusOK)
	if !put("retry: " + strconv.Itoa(sseRetryMs) + "\n\n") {
		return
	}
	sent := sub.Start // nothing at or below this is delivered twice
	if hasLast {
		rep, err := s.Events.ReplayScope(ctx, sc, last, sub.Start, events.ReplayWindow{Events: s.Cfg.SSEReplayEvents, Age: time.Duration(s.Cfg.SSEReplayHours) * time.Hour, Since: s.accessSince(ctx, p, a, sc)}, db.NowTime())
		switch {
		case err != nil:
			s.Log.Warn("event replay", "err", err)
			special("reset", sub.Start, map[string]any{"seq": sub.Start, "reason": "unavailable"})
			return
		case rep.Reset:
			if !special("reset", sub.Start, map[string]any{"seq": sub.Start, "reason": "too_far_behind"}) { // or older than the caller's access
				return
			}
		default:
			for _, e := range rep.Events {
				if !send(e) {
					return
				}
			}
			// Events between the client's number and Start that belong to other
			// scopes are not sent; the cursor still has to move, or the next
			// reconnect would ask for them again. A hello carries it.
			if !special("hello", sub.Start, map[string]any{"seq": sub.Start, "resumed": true}) {
				return
			}
		}
	} else if !special("hello", sub.Start, map[string]any{"seq": sub.Start, "resumed": false}) {
		return
	}
	tick, stopTick := s.sseTicker(sseHeartbeat)
	defer stopTick()
	// Presence (F-244): a person who shares it is sent who else is here, built for them in this goroutine. The frame has
	// no id line, so it never moves the resume cursor, and it never passes through the hub or the events table.
	var pf *presenceFeed
	var presC <-chan struct{}
	var presTick <-chan time.Time
	if sc.Kind == events.ScopeProject {
		if pf = s.presenceFeed(a, sc.ID); pf != nil {
			defer pf.stop()
			presC = pf.wake
			pt, stopPT := s.presenceTicker(presenceCheckEvery)
			defer stopPT()
			presTick = pt
		}
	}
	sendPresence := func() bool {
		if pf == nil || !pf.due(db.NowTime()) {
			return true
		}
		np, ok := s.streamPrincipal(ctx, r, a)
		if !ok {
			return true // the heartbeat notices and ends the stream
		}
		// Like every event, a frame is for a session that may still read the project: a locked or idled-out project ends the
		// stream here, not at the next heartbeat, and no name or title is written first.
		if s.authorizeRead(ctx, np, op, sc.ID) != nil {
			special("bye", sent, map[string]any{"reason": "access"})
			return false
		}
		if body := pf.frame(ctx, np, db.NowTime()); body != "" {
			return put("event: presence\ndata: " + body + "\n\n")
		}
		return true
	}
	if !sendPresence() {
		return
	}
	// Desktop only: a writing page's stream (js/live.js adds flush=1 for a page whose marker says so) is told when the shell is
	// about to restart the app for an update, and the page saves (desktop_save.go). The frame has no id line, so it never moves
	// the resume cursor, and it never passes through the hub or the events table.
	var flushC <-chan int64
	var flushID string
	if s.desk != nil && sc.Kind == events.ScopeProject && q.Get("flush") == "1" {
		f, unregister := s.desk.save.register()
		defer unregister()
		flushC, flushID = f.wake, f.id
	}
	// When this project locks (by anyone), look again at once and end the stream if this session may no longer read it.
	var relock <-chan struct{}
	if sc.Kind == events.ScopeProject {
		if g := s.gateService(); g != nil {
			var stopWatch func()
			relock, stopWatch = g.Watch(sc.ID)
			defer stopWatch()
		}
	}
	for {
		select {
		case <-relock:
			if pf != nil {
				pf.dirty = true
			}
			if np, ok := s.streamPrincipal(ctx, r, a); !ok || s.authorizeRead(ctx, np, op, sc.ID) != nil {
				special("bye", sent, map[string]any{"reason": "access"})
				return
			}
		case <-ctx.Done():
			return
		case <-s.sse.stop:
			special("bye", sub.Start, map[string]any{"reason": "shutdown"})
			return
		case e, ok := <-sub.C:
			if !ok {
				if sub.Dropped() {
					// Too slow to keep up: it is told, then cut off, and reconnects
					// with its last id.
					special("reset", sent, map[string]any{"reason": "slow_client"})
				}
				return
			}
			if e.Seq <= sent {
				continue
			}
			sent = e.Seq
			if !send(e) {
				return
			}
		case n := <-flushC:
			if !put("event: desktop.save\ndata: {\"n\":" + strconv.FormatInt(n, 10) + ",\"flusher\":\"" + flushID + "\"}\n\n") {
				return
			}
		case <-presC:
			pf.dirty = true // built at the next tick: at most one frame in StreamEvery
		case <-presTick:
			if !sendPresence() {
				return
			}
		case <-tick:
			if np, ok := s.streamPrincipal(ctx, r, a); !ok || s.authorizeRead(ctx, np, op, sc.ID) != nil {
				special("bye", sent, map[string]any{"reason": "access"})
				return
			}
			if !put(": heartbeat\n\n") {
				return
			}
		}
	}
}

// sseTicker is the heartbeat clock; tests replace it.
func (s *Server) sseTicker(d time.Duration) (<-chan time.Time, func()) {
	if s.sseTick != nil {
		return s.sseTick(d)
	}
	t := time.NewTicker(d)
	return t.C, t.Stop
}

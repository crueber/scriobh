package server

import (
	"context"
	"encoding/json"
	"regexp"
	"time"

	"github.com/crueber/scriobh/internal/domain/presence"
	"github.com/crueber/scriobh/internal/ops"
)

// Presence (F-244) on the server's side of the wire: three small things the op layer cannot do alone.
//
//  1. A request to the presence routes is not activity. The session is read without refreshing its idle timer and the
//     passphrase gate is asked passively, so a page left open (it beats every 20 seconds) cannot keep a session or an
//     unlock alive past its idle limit.
//  2. The project's event stream carries a `presence` frame for a person who shares presence, built for that person in
//     the stream's own goroutine. It never passes through the hub or the events table.
//  3. The service is reached through the registry's environment, like the gate.

var presencePath = regexp.MustCompile(`^/api/v1/projects/[A-Za-z0-9_]+/presence(?:/|$)`)

func isPresencePath(path string) bool { return presencePath.MatchString(path) }

func (s *Server) presenceService() *presence.Service {
	p, _ := ops.Service[*presence.Service](s.Reg.Env(), presence.ServiceName)
	return p
}

// presenceFeed drives one stream's presence frames. It is used from the stream's goroutine only.
type presenceFeed struct {
	svc     *presence.Service
	project string
	wake    <-chan struct{}
	stop    func()
	last    string // the last frame body sent
	ver     uint64
	lastAt  time.Time // when the list was last built
	sentAt  time.Time
	dirty   bool
}

// feed starts presence for a project stream, or returns nil when this stream is not for it: a key, a stream of a
// Compendium, or a person who has not switched sharing on (checked again each time a frame is built).
func (s *Server) presenceFeed(a reqAuth, project string) *presenceFeed {
	svc := s.presenceService()
	if svc == nil || a.KeyID != "" || a.SessionID == "" || project == "" {
		return nil
	}
	wake, stop := svc.Watch(project)
	return &presenceFeed{svc: svc, project: project, wake: wake, stop: stop, dirty: true}
}

// presenceTicker is the clock of a stream's presence checks; tests replace it (Server.presTick).
func (s *Server) presenceTicker(d time.Duration) (<-chan time.Time, func()) {
	if s.presTick != nil {
		return s.presTick(d)
	}
	t := time.NewTicker(d)
	return t.C, t.Stop
}

// presenceCheckEvery is how often a quiet stream looks again, which is what notices an expiry, a sign-out or a lock.
const presenceCheckEvery = time.Second * 5

// due reports whether a frame could be built now, so the stream does not look up the session for nothing: the rate limit
// has passed and something may have changed (a wake-up, a new version, or the periodic look).
func (f *presenceFeed) due(now time.Time) bool {
	if now.Sub(f.sentAt) < presence.StreamEvery {
		return false
	}
	return f.dirty || f.svc.Version(f.project) != f.ver || now.Sub(f.lastAt) >= 6*presenceCheckEvery
}

// frame builds the frame to send now, or "" when there is nothing new. It is rate limited to one per StreamEvery and
// coalesces: wake-ups only mark the feed dirty, the next tick builds one list.
func (f *presenceFeed) frame(ctx context.Context, p ops.Principal, now time.Time) string {
	if now.Sub(f.sentAt) < presence.StreamEvery {
		return ""
	}
	v := f.svc.Version(f.project)
	if !f.dirty && v == f.ver && now.Sub(f.lastAt) < 6*presenceCheckEvery {
		return ""
	}
	f.dirty = false
	f.ver, f.lastAt = v, now
	view, err := f.svc.ListFor(ctx, p, f.project)
	if err != nil {
		return ""
	}
	b, _ := json.Marshal(view)
	if string(b) == f.last {
		return ""
	}
	first := f.last == ""
	f.last = string(b)
	if first && !view.Enabled {
		return "" // a person who does not share is sent nothing until they do (and told once when they stop)
	}
	f.sentAt = now
	return string(b)
}

// Package presence is who is looking at a project right now, and which chapter (F-244).
//
// It is not co-editing. There are no cursors, no selections, no typing indicators and no text: a heartbeat says "this
// person has this project open, on this document" and nothing more. The product's plan says real-time co-editing is a
// second product; this is the small part that stops two people opening the same chapter by accident.
//
// The state is memory only. Nothing here writes to the database, the events table, a backup, an export, the mirror or
// Git; a restart forgets it, and a restore has nothing to pause. What the database is asked is whether the people
// in memory may still be shown (are they members, signed in, switched on) and what a chapter is called.
//
// The rules, each with a test:
//
//   - An entry is a person (not a tab) on a project, with their session and the id of the document they sent (never a
//     title: titles are read when the list is made, for the person who asks).
//   - It expires TTL after its last heartbeat, and at once when the person leaves, signs out through the page, switches
//     sharing off, or the session, the account or the membership that made it stops standing (checked on every list).
//   - Memory is bounded: MaxPerProject entries in a project and MaxTotal in all, the oldest evicted first.
//   - Sharing is symmetric. A person with presence.share off neither sends a heartbeat that is kept nor receives the list.
package presence

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the service is registered under (ops.Env.SetService).
const ServiceName = "presence"

// The numbers. A page beats every HeartbeatSeconds; an entry lives TTL after its last beat, so one missed beat (a
// laptop lid, a tunnel) does not blink the person away and a closed tab is gone within a minute.
const (
	HeartbeatSeconds = 20
	TTL              = 60 * time.Second
	// MinGap is the closest two beats of one person on one document are told apart: a faster client costs a read and
	// is answered, but nothing is refreshed, wakes a stream or is counted.
	MinGap = 5 * time.Second
	// StreamEvery is the most often a stream is sent a presence frame.
	StreamEvery = 5 * time.Second
	// MaxPerProject and MaxTotal bound the memory: at about 200 bytes an entry the whole table is under 4 MB.
	MaxPerProject = 64
	MaxTotal      = 20000
)

type entry struct {
	project, user, session, doc string
	at                          time.Time
	el                          *list.Element
}

type watcher chan struct{}

// b is what the database-aware half (view.go) asks of the process.

// Service holds the in-memory table.
type Service struct {
	now func() time.Time
	b   Binding

	mu       sync.Mutex
	projects map[string]map[string]*entry // project -> user -> entry
	byAge    *list.List                   // every entry, the oldest beat at the front
	watchers map[string]map[watcher]struct{}
	ver      map[string]uint64 // per project: set from gen when what a list would say may have changed; absent when empty
	gen      uint64
	nextSwp  time.Time

	gateMu    sync.Mutex
	gateCache map[string]gateAnswer // session|project -> the gate's answer, kept for GateTTL
	gatePurge int                   // the size at which expired answers are next dropped
}

type gateAnswer struct {
	ok bool
	at time.Time
}

// GateTTL is how long the gate's answer for one session on one project is kept. The staleness window of a lock, a new
// passphrase or a removed one is nil (InvalidateGate is called the moment the gate relocks); an unlock that idles out, or a
// person who unlocks, is noticed within this long.
const GateTTL = 5 * time.Second

// New returns an empty service on the injectable clock (db.NowTime).
func New() *Service {
	return &Service{
		now: db.NowTime, projects: map[string]map[string]*entry{}, byAge: list.New(),
		watchers: map[string]map[watcher]struct{}{}, ver: map[string]uint64{}, gateCache: map[string]gateAnswer{},
	}
}

// SetClock replaces the clock; for tests.
func (s *Service) SetClock(f func() time.Time) { s.now = f }

// Len is the number of entries held, expired ones not yet swept included; for tests and diagnostics.
func (s *Service) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byAge.Len()
}

// Watch returns a channel that is signalled (buffer of one, never blocking the sender) when the people or documents of
// a project may have changed, and a function that stops watching. A stream builds its own list when it is signalled:
// the service never writes to a client, so a slow client cannot slow a heartbeat.
func (s *Service) Watch(projectID string) (<-chan struct{}, func()) {
	ch := make(watcher, 1)
	s.mu.Lock()
	m := s.watchers[projectID]
	if m == nil {
		m = map[watcher]struct{}{}
		s.watchers[projectID] = m
	}
	m[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.watchers[projectID], ch)
		if len(s.watchers[projectID]) == 0 {
			delete(s.watchers, projectID)
		}
		s.mu.Unlock()
	}
}

// Version changes whenever a list for the project may have changed. A stream compares it to skip work.
func (s *Service) Version(projectID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ver[projectID]
}

// changed notes that the project's list may differ and wakes its watchers. The caller holds s.mu.
func (s *Service) changed(project string) {
	s.gen++
	s.ver[project] = s.gen
	for ch := range s.watchers[project] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// touch records a beat. It reports whether anything a list would show changed (a new person, another document, a
// different session), which is what wakes the streams; a refresh changes nothing and wakes nobody.
func (s *Service) touch(project, user, session, doc string) (changed bool) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	m := s.projects[project]
	if m == nil {
		m = map[string]*entry{}
		s.projects[project] = m
	}
	if e := m[user]; e != nil {
		same := e.doc == doc && e.session == session
		if same && now.Sub(e.at) < MinGap {
			return false
		}
		expired := now.Sub(e.at) >= TTL
		e.doc, e.session, e.at = doc, session, now
		s.byAge.MoveToBack(e.el)
		if !same || expired {
			s.changed(project)
			return true
		}
		return false
	}
	if len(m) >= MaxPerProject {
		s.evictOldestOf(project, m)
	}
	for s.byAge.Len() >= MaxTotal {
		s.removeLocked(s.byAge.Front().Value.(*entry), true)
	}
	e := &entry{project: project, user: user, session: session, doc: doc, at: now}
	e.el = s.byAge.PushBack(e)
	m[user] = e
	s.changed(project)
	return true
}

func (s *Service) evictOldestOf(project string, m map[string]*entry) {
	var old *entry
	for _, e := range m {
		if old == nil || e.at.Before(old.at) {
			old = e
		}
	}
	if old != nil {
		s.removeLocked(old, true)
	}
}

func (s *Service) removeLocked(e *entry, notify bool) {
	s.byAge.Remove(e.el)
	if m := s.projects[e.project]; m != nil {
		delete(m, e.user)
		if len(m) == 0 {
			delete(s.projects, e.project)
		}
	}
	if notify {
		s.changed(e.project)
	}
	if len(s.projects[e.project]) == 0 {
		delete(s.ver, e.project) // an empty project is version 0, whatever it was before: a number is never reused for another state
	}
}

// sweepLocked drops the entries that have outlived TTL, at most every few seconds. They are in order of last beat, so
// the walk ends at the first live one.
func (s *Service) sweepLocked(now time.Time) {
	if now.Before(s.nextSwp) {
		return
	}
	s.nextSwp = now.Add(5 * time.Second)
	for el := s.byAge.Front(); el != nil; el = s.byAge.Front() {
		e := el.Value.(*entry)
		if now.Sub(e.at) < TTL {
			return
		}
		s.removeLocked(e, true)
	}
}

// Sweep drops the entries that have expired now; for tests and for a caller that wants the table tidy.
func (s *Service) Sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSwp = time.Time{}
	s.sweepLocked(s.now())
}

// Leave removes a person's entry from a project (the page closed, or they chose to leave).
func (s *Service) Leave(project, user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.projects[project][user]; e != nil {
		s.removeLocked(e, true)
	}
}

// Forget removes a person from every project: they switched sharing off, signed out or were switched off.
func (s *Service) Forget(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for el := s.byAge.Front(); el != nil; {
		next := el.Next()
		if e := el.Value.(*entry); e.user == user {
			s.removeLocked(e, true)
		}
		el = next
	}
}

// ForgetSession removes the entries made by one session.
func (s *Service) ForgetSession(session string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for el := s.byAge.Front(); el != nil; {
		next := el.Next()
		if e := el.Value.(*entry); e.session == session {
			s.removeLocked(e, true)
		}
		el = next
	}
}

// DropProject forgets everyone in a project (it went to the trash, or was purged).
func (s *Service) DropProject(project string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.projects[project] {
		s.removeLocked(e, true)
	}
}

// live copies the project's unexpired entries.
func (s *Service) live(project string) []entry {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.projects[project]
	out := make([]entry, 0, len(m))
	for _, e := range m {
		if now.Sub(e.at) < TTL {
			out = append(out, *e)
		}
	}
	return out
}

// InvalidateGate forgets what was remembered about the gate for a project: it relocked, or its passphrase changed.
func (s *Service) InvalidateGate(project string) {
	s.gateMu.Lock()
	for k := range s.gateCache {
		if len(k) > len(project) && k[len(k)-len(project)-1] == '|' && k[len(k)-len(project):] == project {
			delete(s.gateCache, k)
		}
	}
	s.gateMu.Unlock()
	s.mu.Lock()
	if len(s.projects[project]) > 0 { // nobody to show: nothing changed (and an empty project keeps no version)
		s.changed(project) // the lists of the project may have changed: wake its streams
	}
	s.mu.Unlock()
}

// gateAllows asks the gate for one person's session on a project, or answers from the last answer if it is under GateTTL old.
func (s *Service) gateAllows(ctx context.Context, p ops.Principal, project string) bool {
	k := p.SessionID + "|" + project
	now := s.now()
	s.gateMu.Lock()
	if a, ok := s.gateCache[k]; ok && now.Sub(a.at) < GateTTL {
		s.gateMu.Unlock()
		return a.ok
	}
	s.gateMu.Unlock()
	ok := s.b.Gate(ops.Passive(ctx), p, project) == nil
	s.gateMu.Lock()
	if len(s.gateCache) >= max(s.gatePurge, 1024) { // expired answers are dropped as the table grows, so it follows the people shown
		for k, a := range s.gateCache {
			if now.Sub(a.at) >= GateTTL {
				delete(s.gateCache, k)
			}
		}
		s.gatePurge = 2 * len(s.gateCache)
	}
	s.gateCache[k] = gateAnswer{ok: ok, at: now}
	s.gateMu.Unlock()
	return ok
}

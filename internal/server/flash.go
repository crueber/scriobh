package server

import (
	"net/http"
	"sync"
	"time"
)

// A one-shot flash: what a page wants to tell the person once, after a redirect (who was just added, what they were
// granted), kept on the server against their session for a couple of minutes and handed over by the next page that asks.
// Nothing a person typed travels in a query string, so a link cannot be made to print words on the page, and the line
// is not there on a reload.

type flashKey struct {
	session string
	scope   string
}

type flashEntry struct {
	vals map[string]string
	at   time.Time
}

type flashStore struct {
	mu sync.Mutex
	m  map[flashKey]flashEntry
}

const flashLife = 2 * time.Minute

// flashSet keeps vals for the request's session and scope, replacing what was waiting for that scope.
func (s *Server) flashSet(r *http.Request, vals map[string]string) {
	id := authFrom(r.Context()).SessionID
	if id == "" {
		return
	}
	s.flash.mu.Lock()
	defer s.flash.mu.Unlock()
	if s.flash.m == nil {
		s.flash.m = map[flashKey]flashEntry{}
	}
	now := time.Now()
	for k, e := range s.flash.m { // nothing waits longer than its life
		if now.Sub(e.at) > flashLife {
			delete(s.flash.m, k)
		}
	}
	s.flash.m[flashKey{session: id, scope: vals["_scope"]}] = flashEntry{vals: vals, at: now}
}

// flashTake returns what waits for the request's session, once.
func (s *Server) flashTake(r *http.Request) map[string]string {
	id := authFrom(r.Context()).SessionID
	if id == "" {
		return nil
	}
	k := flashKey{session: id}
	s.flash.mu.Lock()
	defer s.flash.mu.Unlock()
	e, ok := s.flash.m[k]
	if !ok {
		return nil
	}
	delete(s.flash.m, k)
	if time.Since(e.at) > flashLife {
		return nil
	}
	return e.vals
}

// flashTakeFor returns what waits for the request's session when it was set for this scope and the page says it is its
// own (the project and the edition it names), once. A flash for another page, or another project, is left where it is.
func (s *Server) flashTakeFor(r *http.Request, scope string, mine func(map[string]string) bool) map[string]string {
	id := authFrom(r.Context()).SessionID
	if id == "" {
		return nil
	}
	k := flashKey{session: id, scope: scope}
	s.flash.mu.Lock()
	defer s.flash.mu.Unlock()
	e, ok := s.flash.m[k]
	if !ok || (mine != nil && !mine(e.vals)) {
		return nil
	}
	delete(s.flash.m, k)
	if time.Since(e.at) > flashLife {
		return nil
	}
	return e.vals
}

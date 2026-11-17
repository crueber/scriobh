package server

import (
	"net/http"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/gitmirror"
)

// A reviewed plan is a page with an address: the person can reload it, go back to it and forward again, and a reload makes
// nothing. What the plan was made from is kept on the server against the session (never in the address, so a link cannot
// choose what is published), for half an hour, and the address holds only a random id. The token that binds a plan to the
// publish is not kept here: the page asks the op for a fresh plan, and so a fresh token, every time it is shown.

type planEntry struct {
	in gitmirror.RepoPlanIn
	at time.Time
}

type planStore struct {
	mu sync.Mutex
	m  map[string]map[string]planEntry // session -> id -> entry
}

const (
	planLife   = 30 * time.Minute
	planPerSes = 12
)

func (s *Server) planPut(r *http.Request, in gitmirror.RepoPlanIn) string {
	sid := authFrom(r.Context()).SessionID
	if sid == "" {
		return ""
	}
	s.plans.mu.Lock()
	defer s.plans.mu.Unlock()
	if s.plans.m == nil {
		s.plans.m = map[string]map[string]planEntry{}
	}
	mine := s.plans.m[sid]
	if mine == nil {
		mine = map[string]planEntry{}
		s.plans.m[sid] = mine
	}
	now := time.Now()
	oldest, oldestID := now, ""
	for id, e := range mine {
		if now.Sub(e.at) > planLife {
			delete(mine, id)
		} else if e.at.Before(oldest) {
			oldest, oldestID = e.at, id
		}
	}
	if len(mine) >= planPerSes && oldestID != "" {
		delete(mine, oldestID)
	}
	id := db.NewID("pln")
	mine[id] = planEntry{in: in, at: now}
	return id
}

func (s *Server) planGet(r *http.Request, id string) (gitmirror.RepoPlanIn, bool) {
	sid := authFrom(r.Context()).SessionID
	s.plans.mu.Lock()
	defer s.plans.mu.Unlock()
	e, ok := s.plans.m[sid][id]
	if !ok || sid == "" || time.Since(e.at) > planLife {
		return gitmirror.RepoPlanIn{}, false
	}
	return e.in, true
}

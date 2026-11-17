package docs

import (
	"sync"
	"time"
)

// leaseTTL is how long a tab keeps the right to write a document without
// saving. Every save renews it.
const leaseTTL = 90 * time.Second

// leases is the single-writer lease per document. It is in memory because
// there is one replica; a restart clears it, and tabs simply reacquire.
type leases struct {
	mu  sync.Mutex
	m   map[string]lease
	now func() time.Time
}

type lease struct {
	tab     string
	expires time.Time
}

func newLeases() *leases { return &leases{m: map[string]lease{}, now: time.Now} }

// acquire gives doc to tab if it is free, expired, or already the tab's. With
// take it overrides another tab. It reports whether tab now holds the lease.
func (l *leases) acquire(doc, tab string, take bool) (held bool, expires time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cur, ok := l.m[doc]
	if ok && now.After(cur.expires) {
		ok = false
	}
	if ok && cur.tab != tab && !take {
		return false, cur.expires
	}
	e := now.Add(leaseTTL)
	l.m[doc] = lease{tab: tab, expires: e}
	if len(l.m) > 4096 { // sweep
		for k, v := range l.m {
			if now.After(v.expires) {
				delete(l.m, k)
			}
		}
	}
	return true, e
}

// check reports whether a tab other than `tab` holds a live lease.
func (l *leases) heldByOther(doc, tab string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.m[doc]
	return ok && l.now().Before(cur.expires) && cur.tab != tab
}

// release frees doc if tab holds it. Another tab's lease is left alone.
func (l *leases) release(doc, tab string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cur, ok := l.m[doc]; ok && cur.tab == tab {
		delete(l.m, doc)
	}
}

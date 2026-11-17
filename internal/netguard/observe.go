package netguard

import (
	"context"
	"net"
	"sync"
)

// Every place in this program that opens an outbound connection tells the observers first. It is how a desktop
// instance can say, and a test can prove, that nothing left the machine: the dialers below, Git sync's own dialer
// and the sign-in client all call Note, and a source test (TestEveryDialSiteIsObserved) fails when a new one appears
// that does not. An observer is told where a connection is about to go, before any name is looked up; it cannot
// refuse it.

var (
	obsMu     sync.RWMutex
	observers = map[int]func(network, address string){}
	obsNext   int
)

// Observe adds an observer of outbound connections and returns the function that removes it. fn is called from the
// dialing goroutine and must be quick and safe to call from many at once.
func Observe(fn func(network, address string)) (remove func()) {
	obsMu.Lock()
	id := obsNext
	obsNext++
	observers[id] = fn
	obsMu.Unlock()
	return func() {
		obsMu.Lock()
		delete(observers, id)
		obsMu.Unlock()
	}
}

// Note tells the observers that a connection to address ("host:port") is about to be attempted.
func Note(network, address string) {
	obsMu.RLock()
	defer obsMu.RUnlock()
	for _, fn := range observers {
		fn(network, address)
	}
}

// Observed wraps a dial function so that every dial is noted.
func Observed(dial func(ctx context.Context, network, address string) (net.Conn, error)) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		Note(network, address)
		return dial(ctx, network, address)
	}
}

// IsLoopbackAddress reports whether address ("host:port") names this machine: a loopback IP address or "localhost".
// A name is not looked up: any other name counts as somewhere else.
func IsLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

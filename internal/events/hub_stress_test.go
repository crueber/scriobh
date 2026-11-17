package events_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/events"
)

// Subscribers coming and going while events are written: no subscriber is ever
// handed an event at or before the cursor it subscribed at, none twice, none
// out of order, and the hub's cursor never moves backwards. The churn includes
// the last subscriber leaving and a new one arriving while the old loop is
// finishing a batch, which is where two loops used to run.
func TestHubSurvivesSubscribeChurnWhileEventsAreWritten(t *testing.T) {
	s := newStore(t)
	h := events.NewHub(s, events.HubOptions{Fallback: 5 * time.Millisecond})
	const writes = 300
	scope := events.Scope{Kind: events.ScopeProject, ID: "prj_a"}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Int32
	fail := func(format string, args ...any) {
		if bad.Add(1) <= 5 {
			t.Errorf(format, args...)
		}
	}

	// The cursor, sampled as fast as it can be.
	wg.Add(1)
	go func() {
		defer wg.Done()
		var last int64
		for {
			select {
			case <-stop:
				return
			default:
			}
			if c := h.Cursor(); c < last {
				fail("the cursor went backwards: %d after %d", c, last)
				return
			} else {
				last = c
			}
		}
	}()

	// Churn: each subscriber reads for a moment and leaves, so the hub keeps
	// dropping to no subscribers and starting again.
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				sub := h.Subscribe(scope)
				prev := sub.Start
				timeout := time.After(3 * time.Millisecond)
			read:
				for {
					select {
					case e, ok := <-sub.C:
						if !ok {
							break read
						}
						if e.Seq <= prev {
							fail("event %d delivered after %d (subscribed at %d)", e.Seq, prev, sub.Start)
						}
						prev = e.Seq
					case <-timeout:
						break read
					}
				}
				sub.Close()
			}
		}()
	}

	// The writer.
	for i := 0; i < writes; i++ {
		emit(t, s, "prj_a", "", 1)
		if i%20 == 0 {
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
	if h.Subscribers() != 0 {
		t.Errorf("%d subscribers left", h.Subscribers())
	}
	if bad.Load() > 0 {
		t.Fatalf("%d violations", bad.Load())
	}
}

package events_test

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/events"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func newStore(t *testing.T) *events.Store {
	t.Helper()
	return events.NewStore(testutil.DB(t), nil)
}

func emit(t *testing.T, s *events.Store, project, compendium string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := s.Flush(context.Background(), "", []ops.Event{{Type: "doc.updated", ProjectID: project, CompendiumID: compendium, Payload: map[string]any{"revision": i}}}, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func recv(t *testing.T, sub *events.Subscription) (events.Event, bool) {
	t.Helper()
	select {
	case e, ok := <-sub.C:
		return e, ok
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
	return events.Event{}, false
}

// A subscriber that falls more than its buffer behind is dropped, told so, and
// never blocks the others.
func TestSlowSubscriberIsDropped(t *testing.T) {
	s := newStore(t)
	h := events.NewHub(s, events.HubOptions{Buffer: 2})
	slow := h.Subscribe(events.Scope{Kind: events.ScopeProject, ID: "prj_a"})
	fast := h.Subscribe(events.Scope{Kind: events.ScopeProject, ID: "prj_a"})
	defer fast.Close()
	go func() {
		for range fast.C {
		}
	}()
	emit(t, s, "prj_a", "", 6)
	deadline := time.Now().Add(10 * time.Second)
	for !slow.Dropped() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !slow.Dropped() {
		t.Fatal("the slow subscriber was not dropped")
	}
	n := 0
	for range slow.C { // closed after the buffered events
		n++
	}
	if n > 2 {
		t.Errorf("slow subscriber buffered %d events, want at most 2", n)
	}
	slow.Close() // safe after a drop
}

// Delivery follows the event's own ids: a project's events reach that project's
// subscribers, a Compendium's reach that Compendium's, and an event with both
// reaches both, never anyone else.
func TestFanOutByScope(t *testing.T) {
	s := newStore(t)
	h := events.NewHub(s, events.HubOptions{})
	pa := h.Subscribe(events.Scope{Kind: events.ScopeProject, ID: "prj_a"})
	pb := h.Subscribe(events.Scope{Kind: events.ScopeProject, ID: "prj_b"})
	ca := h.Subscribe(events.Scope{Kind: events.ScopeCompendium, ID: "cmp_a"})
	defer pa.Close()
	defer pb.Close()
	defer ca.Close()
	emit(t, s, "prj_a", "cmp_a", 1)
	if e, _ := recv(t, pa); e.CompendiumID != "cmp_a" {
		t.Errorf("project subscriber: %+v", e)
	}
	if e, _ := recv(t, ca); e.ProjectID != "prj_a" {
		t.Errorf("Compendium subscriber: %+v", e)
	}
	select {
	case e := <-pb.C:
		t.Errorf("another project received %+v", e)
	case <-time.After(150 * time.Millisecond):
	}
}

// The hub's goroutine lives only while someone is subscribed, and a late
// subscriber's Start is the cursor to replay up to: nothing is missed or doubled.
func TestHubStartsAndStopsWithSubscribers(t *testing.T) {
	s := newStore(t)
	h := events.NewHub(s, events.HubOptions{})
	before := runtime.NumGoroutine()
	a := h.Subscribe(events.Scope{Kind: events.ScopeProject, ID: "prj_a"})
	emit(t, s, "prj_a", "", 3)
	for i := 0; i < 3; i++ {
		recv(t, a)
	}
	b := h.Subscribe(events.Scope{Kind: events.ScopeProject, ID: "prj_a"})
	if b.Start != 3 {
		t.Errorf("Start = %d, want 3", b.Start)
	}
	emit(t, s, "prj_a", "", 1)
	if e, _ := recv(t, b); e.Seq != 4 {
		t.Errorf("first live event for the late subscriber is %d, want 4", e.Seq)
	}
	a.Close()
	b.Close()
	if h.Subscribers() != 0 {
		t.Fatal("subscribers remain")
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Errorf("goroutines %d -> %d: the hub left its loop running", before, after)
	}
}

func TestReplayScope(t *testing.T) {
	s := newStore(t)
	emit(t, s, "prj_a", "", 3)
	emit(t, s, "prj_b", "", 2)
	emit(t, s, "prj_a", "", 1)
	ctx := context.Background()
	rep, err := s.ReplayScope(ctx, events.Scope{Kind: events.ScopeProject, ID: "prj_a"}, 1, 6, events.ReplayWindow{}, time.Now())
	if err != nil || rep.Reset || len(rep.Events) != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
	for _, e := range rep.Events {
		if e.ProjectID != "prj_a" || e.Seq <= 1 {
			t.Errorf("replayed %+v", e)
		}
	}
	if rep, _ := s.ReplayScope(ctx, events.Scope{Kind: events.ScopeProject, ID: "prj_a"}, 0, 6, events.ReplayWindow{Events: 2}, time.Now()); !rep.Reset {
		t.Error("more events than the window should reset")
	}
	if rep, _ := s.ReplayScope(ctx, events.Scope{Kind: events.ScopeProject, ID: "prj_a"}, 99, 6, events.ReplayWindow{}, time.Now()); !rep.Reset {
		t.Error("a number from the future should reset")
	}
}

func TestCleanKeepsOnlyIdsAndCounts(t *testing.T) {
	got := events.Clean(map[string]any{
		"revision": 3, "words": 10, "kind": "scene", "fields": []string{"title"},
		"title": "Chapter One", "content": "It was a dark night.", "entity_id": "ent_01M7A2FNHN1NJ6AYAD0NC1G7SE",
		"email": "a@b.c", "key": "has space", "reason": "x y",
	}, "entity_id")
	for _, k := range []string{"revision", "words", "kind", "fields"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s was dropped", k)
		}
	}
	// "content" is an allowed key but a sentence is not an allowed value.
	for _, k := range []string{"title", "content", "entity_id", "email", "key", "reason"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s = %v should have been dropped", k, got[k])
		}
	}
}

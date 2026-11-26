package docs

import (
	"testing"
	"time"
)

// The chapter's lease (docs/ARCHITECTURE.md, the tab lease): a tab that holds a chapter keeps it for leaseTTL
// after its last renewal; a tab that asks while another live tab holds it is refused, and it is free for the
// next asker once that time is up. A locked page asks again every 30 s, so it can never take a live tab's lease.
func TestLeaseFreesWhenItsTimeIsUpAndNeverFromALiveTab(t *testing.T) {
	l := newLeases()
	clock := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }
	if held, _ := l.acquire("doc", "tabA", false); !held {
		t.Fatal("a free chapter must be given to the first tab that asks")
	}
	for _, at := range []time.Duration{30, 60, 89} { // A is silent, but its time has not run out
		clock = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC).Add(at * time.Second)
		if held, _ := l.acquire("doc", "tabB", false); held {
			t.Fatalf("at %v tab B took a lease tab A still holds", at*time.Second)
		}
		if !l.heldByOther("doc", "tabB") {
			t.Fatalf("at %v tab B is not told that another tab holds the chapter", at*time.Second)
		}
	}
	clock = clock.Add(2 * time.Second) // 91 s: the lease has run out
	if held, _ := l.acquire("doc", "tabB", false); !held {
		t.Fatal("once the lease has run out, the next tab takes it without a take")
	}
	if held, _ := l.acquire("doc", "tabA", false); held {
		t.Fatal("tab A must be refused once tab B holds the chapter")
	}
}

// A tab that renews keeps the chapter, however often the others ask.
func TestLeaseRenewedByItsHolderIsNeverTaken(t *testing.T) {
	l := newLeases()
	clock := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }
	l.acquire("doc", "tabA", false)
	for i := 0; i < 10; i++ {
		clock = clock.Add(45 * time.Second) // the page renews every 45 s
		if held, _ := l.acquire("doc", "tabA", false); !held {
			t.Fatalf("renewal %d by the holder was refused", i)
		}
		if held, _ := l.acquire("doc", "tabB", false); held {
			t.Fatalf("renewal %d: tab B took a live lease", i)
		}
	}
}

package imports

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

func asPerson(id string) context.Context {
	return ops.WithPrincipal(context.Background(), ops.Principal{UserID: id, Role: ops.RoleWriter, Via: "session"})
}

// One person holds at most one place: a second request made while the first is reading is refused at once with the calm
// busy answer, so one writer (or one key) cannot fill the places and turn everyone else away. Nothing sleeps: the first
// request is held by the test.
func TestOnePersonHoldsOnePlace(t *testing.T) {
	defer SetConcurrency(2, 5*time.Second)()
	a, b := asPerson("usr_a"), asPerson("usr_b")

	relA, err := acquire(a)
	if err != nil {
		t.Fatal(err)
	}
	// The same person again, while the first is held: refused immediately (the wait is five seconds; this returns at once).
	start := time.Now()
	_, err = acquire(a)
	var oe *ops.Error
	if !errors.As(err, &oe) || oe.Code != ops.CodeRateLimited || oe.RetryAfterSeconds == 0 {
		t.Fatalf("a second place for the same person: %v", err)
	}
	waited := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if waited > time.Second {
			t.Errorf("the refusal waited %s; it must be immediate", waited)
		}
	})
	// Another person still has a place, and so does a caller with no principal (a command-line import).
	relB, err := acquire(b)
	if err != nil {
		t.Fatalf("another person was refused while one place was free: %v", err)
	}
	relB()
	relNone, err := acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	relNone()

	// When the first request gives its place back the person may take another.
	relA()
	relA2, err := acquire(a)
	if err != nil {
		t.Fatalf("a place was not given back: %v", err)
	}
	relA2()

	// A request that reads several sources one after another (a folder, a batch) takes and gives back a place for each.
	for i := 0; i < 5; i++ {
		rel, err := acquire(a)
		if err != nil {
			t.Fatalf("source %d of a sequential import was refused: %v", i, err)
		}
		rel()
	}
}

// A person who waited for a place and gave up (the places were full) does not keep holding one, and a person whose
// request is cancelled while waiting does not either.
func TestAPersonWhoGivesUpWaitingHoldsNothing(t *testing.T) {
	defer SetConcurrency(1, 30*time.Millisecond)()
	hold := HoldPlace()
	if _, err := acquire(asPerson("usr_a")); err == nil {
		t.Fatal("a place was given with none free")
	}
	ctx, cancel := context.WithCancel(asPerson("usr_a"))
	cancel()
	if _, err := acquire(ctx); err == nil {
		t.Fatal("a cancelled request got a place")
	}
	hold()
	rel, err := acquire(asPerson("usr_a"))
	if err != nil {
		t.Fatalf("the person still held a place after giving up: %v", err)
	}
	rel()
	holdersMu.Lock()
	n := len(holders)
	holdersMu.Unlock()
	if n != 0 {
		t.Errorf("%d holders remain", n)
	}
}

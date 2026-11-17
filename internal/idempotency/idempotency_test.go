package idempotency_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/idempotency"
	"github.com/crueber/scriobh/internal/testutil"
)

// run stores one response of n bytes under (principal, key) and returns whether
// the key was served from the store.
func run(t *testing.T, s *idempotency.Store, principal, key string, n int) (replay bool) {
	t.Helper()
	_, replay, err := s.Do(context.Background(), principal, key, "op", "fp-"+key, func() (idempotency.Result, error) {
		return idempotency.Result{Response: idempotency.Response{Status: 200, Body: []byte(strings.Repeat("x", n))}, Store: true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return replay
}

func newStore(t *testing.T) (*idempotency.Store, func(time.Duration)) {
	s := idempotency.New(testutil.DB(t))
	now := time.Now()
	s.Now = func() time.Time { return now }
	return s, func(d time.Duration) { now = now.Add(d) }
}

// TestEntryCapEvictsTheOldest: one caller cannot keep more than MaxEntries
// responses; the oldest go, and another caller is not touched.
func TestEntryCapEvictsTheOldest(t *testing.T) {
	s, tick := newStore(t)
	s.MaxEntries = 3
	for _, k := range []string{"a", "b", "c", "d", "e"} {
		run(t, s, "key:one", k, 10)
		tick(time.Second)
	}
	if n := count(t, s, "key:one"); n != 3 {
		t.Fatalf("%d entries, want 3", n)
	}
	run(t, s, "key:two", "a", 10)
	if count(t, s, "key:two") != 1 || count(t, s, "key:one") != 3 {
		t.Error("another principal's records must not be evicted by this one's")
	}
	// The newest three survive and replay; the oldest two do not.
	for _, k := range []string{"c", "d", "e"} {
		if !replayOnly(t, s, "key:one", k) {
			t.Errorf("%s should have been kept", k)
		}
	}
	for _, k := range []string{"a", "b"} {
		if replayOnly(t, s, "key:one", k) {
			t.Errorf("%s should have been evicted", k)
		}
	}
}

// replayOnly reports whether a key is still stored, without leaving a new
// record behind (a probe that finds nothing runs a body that errors).
func replayOnly(t *testing.T, s *idempotency.Store, principal, key string) bool {
	t.Helper()
	_, replay, _ := s.Do(context.Background(), principal, key, "op", "fp-"+key, func() (idempotency.Result, error) {
		return idempotency.Result{}, context.Canceled
	})
	return replay
}

func count(t *testing.T, s *idempotency.Store, principal string) int {
	t.Helper()
	var n int
	if err := s.DB.Read().QueryRow(`SELECT count(*) FROM idempotency_keys WHERE principal_id = ?`, principal).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func stored(t *testing.T, s *idempotency.Store, principal string) (bytes int) {
	t.Helper()
	if err := s.DB.Read().QueryRow(`SELECT COALESCE(sum(length(response_json)),0) FROM idempotency_keys WHERE principal_id = ?`, principal).Scan(&bytes); err != nil {
		t.Fatal(err)
	}
	return
}

// TestByteCapEvictsTheOldest: the bodies one caller keeps add up to at most
// MaxBytes, and a response bigger than the whole allowance is remembered as
// having run, not stored.
func TestByteCapEvictsTheOldest(t *testing.T) {
	s, tick := newStore(t)
	s.MaxBytes = 100
	for _, k := range []string{"a", "b", "c", "d"} {
		run(t, s, "key:one", k, 40)
		tick(time.Second)
	}
	if got := stored(t, s, "key:one"); got > 100 || got < 80 {
		t.Fatalf("stored %d bytes, want 80 (two newest)", got)
	}
	for _, k := range []string{"c", "d"} {
		if !replayOnly(t, s, "key:one", k) {
			t.Errorf("%s should have been kept", k)
		}
	}
	for _, k := range []string{"a", "b"} {
		if replayOnly(t, s, "key:one", k) {
			t.Errorf("%s should have been evicted", k)
		}
	}
	// Oversize for the allowance: a tombstone, and the others are left alone.
	run(t, s, "key:one", "big", 500)
	if got := stored(t, s, "key:one"); got > 100 {
		t.Errorf("stored %d bytes after an oversize response", got)
	}
	var status int
	if err := s.DB.Read().QueryRow(`SELECT status_code FROM idempotency_keys WHERE principal_id = 'key:one' AND key = 'big'`).Scan(&status); err != nil || status != 0 {
		t.Errorf("the oversize response should leave a tombstone (status 0): %d %v", status, err)
	}
	// Another principal has its own allowance.
	run(t, s, "key:two", "a", 90)
	if stored(t, s, "key:two") != 90 || stored(t, s, "key:one") > 100 {
		t.Error("allowances are per principal")
	}
}

func TestDefaultCapsApply(t *testing.T) {
	s, tick := newStore(t)
	for i := 0; i < idempotency.DefaultMaxEntries+5; i++ {
		run(t, s, "key:one", strings.Repeat("k", 1)+string(rune('A'+i%26))+string(rune('a'+i/26)), 1)
		tick(time.Millisecond)
	}
	if n := count(t, s, "key:one"); n != idempotency.DefaultMaxEntries {
		t.Errorf("%d entries, want the default cap of %d", n, idempotency.DefaultMaxEntries)
	}
}

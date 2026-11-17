package idempotency_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/idempotency"
)

// The byte cap counts bytes, not characters: a body of two-byte characters is
// twice as big as its length says.
func TestByteCapCountsBytes(t *testing.T) {
	s, tick := newStore(t)
	s.MaxBytes = 100
	for _, k := range []string{"a", "b", "c"} {
		_, _, err := s.Do(context.Background(), "key:one", k, "op", "fp-"+k, func() (idempotency.Result, error) {
			return idempotency.Result{Response: idempotency.Response{Status: 200, Body: []byte(strings.Repeat("é", 40))}, Store: true}, nil // 80 bytes, 40 characters
		})
		if err != nil {
			t.Fatal(err)
		}
		tick(time.Second)
	}
	var n int
	if err := s.DB.Read().QueryRow(`SELECT count(*) FROM idempotency_keys WHERE principal_id = 'key:one'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d records kept (%v); 80 bytes each under a 100 byte cap leaves one", n, err)
	}
}

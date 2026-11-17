package ops

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
)

type noIn struct{}

// An op marked NoDryRun is refused in a dry run before its handler runs, whatever its method: a GET marked so is
// one that reaches outside the database (a search that sends its question to a model endpoint), and the handler
// of such an op is the thing a dry run must never run.
func TestNoDryRunOpsNeverRunTheirHandlerInADryRun(t *testing.T) {
	r := NewRegistry(&Env{})
	var ran atomic.Int64
	h := func(c *Ctx, in noIn) (pingOut, error) { ran.Add(1); return pingOut{"sent"}, nil }
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		Register(r, Op{Name: "outside." + strings.ToLower(m), Method: m, Path: "/api/v1/outside-" + m, Desc: "Reaches out.", Auth: Authenticated(), NoDryRun: true}, h)
	}
	Register(r, Op{Name: "inside.read", Method: "GET", Path: "/api/v1/inside", Desc: "Reads.", Auth: Authenticated()}, h)
	p := Principal{UserID: "u", Via: "session"}
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		if _, err := r.CallDry(context.Background(), p, "outside."+strings.ToLower(m), noIn{}); AsError(err).Code != CodeDryRunUnsupported {
			t.Errorf("%s: %v", m, err)
		}
	}
	if n := ran.Load(); n != 0 {
		t.Fatalf("the handler of a NoDryRun op ran %d times in a dry run", n)
	}
	// An ordinary read still runs as it is.
	if _, err := r.CallDry(context.Background(), p, "inside.read", noIn{}); err != nil || ran.Load() != 1 {
		t.Fatalf("a read: %v, ran %d", err, ran.Load())
	}
}

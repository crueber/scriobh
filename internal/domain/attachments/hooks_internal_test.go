package attachments

import (
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/ops"
)

// A delete hook added before Register runs is not lost, and one added after it is kept too: the order the registry builds
// its packages in is not something a hook should depend on.
func TestADeleteHookAddedBeforeRegisterIsKept(t *testing.T) {
	for _, before := range []bool{true, false} {
		r := ops.NewRegistry(&ops.Env{})
		hook := func(*ops.Ctx, *sql.Tx, string, string) (func(), error) { return nil, nil }
		if before {
			AddDeleteHook(r.Env(), hook)
		}
		Register(r, DefaultLimits)
		if !before {
			AddDeleteHook(r.Env(), hook)
		}
		if n := len(hooksOf(r.Env()).hooks); n != 1 {
			t.Errorf("hook added before Register=%v: %d hooks", before, n)
		}
	}
}

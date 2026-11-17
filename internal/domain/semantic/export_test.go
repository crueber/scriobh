package semantic

import (
	"context"

	"github.com/crueber/scriobh/internal/db"
)

// FailPause makes the restore's pause fail (or work again, with nil); it returns a function that makes it work again.
func FailPause(err error) func() {
	if err == nil {
		pauseOverride.Store(nil)
	} else {
		f := func(ctx context.Context, d *db.DB) (int, int, int, error) { return 0, 0, 0, err }
		pauseOverride.Store(&f)
	}
	return func() { pauseOverride.Store(nil) }
}

// SkipReasonForTest exposes the walk to a test in another package.
var SkipReasonForTest = skipReason

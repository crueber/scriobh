package imports_test

import (
	"errors"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// Through the registry, as a request arrives: while one person holds a place (a first request reading a book), a second
// import by the same person is refused with the calm busy 429, at once, and another person's import goes through; when the
// place is given back the same person imports again. This proves the real path carries the person to the limiter.
func TestASecondImportByTheSamePersonIsBusyThroughTheRegistry(t *testing.T) {
	defer imports.SetConcurrency(2, 5*time.Second)()
	e := newEnv(t)
	other := e.user("other", ops.RoleWriter)
	book := salt3(t)
	in := imports.PreviewIn{Name: "salt.epub", Epub: book}

	imports.ClearCache()
	release := imports.HoldPlaceAs(e.w.UserID)
	start := time.Now()
	_, err := e.call(e.w, "import.preview", in)
	var oe *ops.Error
	if !errors.As(err, &oe) || oe.Code != ops.CodeRateLimited {
		release()
		t.Fatalf("a second import by the person who holds a place: %v", err)
	}
	refused := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if refused > time.Second {
			t.Errorf("the refusal took %s; it must be at once", refused)
		}
	})
	imports.ClearCache()
	if _, err := e.call(other, "import.preview", in); err != nil {
		release()
		t.Fatalf("another person's import was refused: %v", err)
	}
	release()
	imports.ClearCache()
	if _, err := e.call(e.w, "import.preview", in); err != nil {
		t.Fatalf("the person's import after the place was given back: %v", err)
	}
}

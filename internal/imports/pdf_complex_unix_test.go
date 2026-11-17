//go:build unix

package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// A file whose objects are larger than the reader's budget gives no text, and says so: it is not called a scan.
func TestAPdfThatGoesOverTheParseBudgetIsNotCalledAScan(t *testing.T) {
	if testing.Short() {
		t.Skip("a 21 MB file")
	}
	e := newEnv(t)
	var o strings.Builder
	o.WriteString("[")
	for i := 0; i < 2_750_000; i++ {
		o.WriteString("<</A 1>>")
	}
	o.WriteString("]")
	_, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "big.pdf", Pdf: rrSkeleton("5 0 obj\n" + o.String() + "\nendobj\n")})
	if err == nil || !strings.Contains(err.Error(), "too complex to read within the import's limits") || strings.Contains(err.Error(), "a scan") {
		t.Errorf("the refusal for a file over the parse budget: %v", err)
	}
}

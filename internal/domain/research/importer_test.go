package research

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/text"
)

// A body cut to the limit never keeps the opener of a private note whose closer
// was cut away: that would hide, or in some readers show, what follows it.
func TestImportedBodyCutDoesNotSplitAPrivateNote(t *testing.T) {
	for back := 0; back < 30; back++ {
		body := strings.Repeat("a", MaxBodyBytes-back) + " %%SECRETWORD hidden words%% after"
		n := Imported{Kind: "note", Title: "x", Body: body}
		if cut := n.Clean(); cut == "" {
			t.Fatal("not cut")
		}
		if len(n.Body) > MaxBodyBytes || strings.Count(n.Body, "%%")%2 != 0 || strings.Contains(text.SearchBody(n.Body), "SECRET") {
			t.Fatalf("back %d: the cut left a note open: %q", back, n.Body[max(0, len(n.Body)-25):])
		}
	}
}

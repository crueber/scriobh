package export_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/export"
)

// U10: a heading in the text of a chapter sits below the chapter's heading. The single HTML file gives a chapter an h2 (under the
// book's h1), so the chapter's own H2 is an h3 and its H3 an h4; an H2 is never an h4, which would skip h3.
func TestHTMLSingleTextHeadingsSitBelowTheChapterHeading(t *testing.T) {
	f := newFx(t, "Heading Levels")
	f.doc("The Road", "chapter", "", "Opening words.\n\n## In the chapter\n\nText.\n\n### Smaller\n\nEnd.\n")
	h := string(f.mustHTML(export.HTMLIn{}).Body)
	if !strings.Contains(h, "<h2>The Road</h2>") {
		t.Fatalf("the chapter heading is not an h2: %.400s", h)
	}
	for _, want := range []string{"<h3>In the chapter</h3>", "<h4>Smaller</h4>"} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(h, "<h4>In the chapter</h4>") {
		t.Error("an H2 in a chapter is an h4: it skips h3")
	}
}

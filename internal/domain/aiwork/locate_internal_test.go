package aiwork

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
)

func TestLocateFailsClosedOnAQuoteThatRepeatsWithoutEnd(t *testing.T) {
	body := strings.Repeat("a ", maxOccurrences+50)
	ix := alts.NewIndex(body)
	if _, ok := locate(body, ix, "a", "", "", nil, false); ok {
		t.Fatal("a quote with more than the counted occurrences was taken as unique")
	}
	// below the cap a unique quote is found
	body = "one two three"
	if sp, ok := locate(body, alts.NewIndex(body), "two", "", "", nil, false); !ok || body[sp.A:sp.B] != "two" {
		t.Fatal("not found")
	}
}

func TestParagraphContextMarksTheParagraphEdges(t *testing.T) {
	body := "Alpha beta.\n\nSecond one here.\n\nThird."
	a := strings.Index(body, "Second one here.")
	b, af := paragraphContext(body, a, a+len("Second one here."))
	if b != paraStart || af != paraEnd {
		t.Fatalf("%q %q", b, af)
	}
	ix := alts.NewIndex(body)
	if _, ok := locateExact(body, ix, "Second one here.", b, af, nil, false); !ok {
		t.Fatal("the paragraph is not found in its own context")
	}
	// the same words inside a longer paragraph are another place
	other := "Alpha beta.\n\nSo: Second one here. And more.\n\nThird."
	if _, ok := locateExact(other, alts.NewIndex(other), "Second one here.", b, af, nil, false); ok {
		t.Fatal("a quote moved into another sentence")
	}
}

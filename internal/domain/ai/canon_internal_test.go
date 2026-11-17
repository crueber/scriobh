package ai

import (
	"strings"
	"testing"
)

// The sentence and paragraph around a selection are slices of the document: private notes, ghosted text, the
// alternatives comment and HTML comments are cut from them too, before the preview and the request.
func TestSelectionSlicesCarryNoHiddenText(t *testing.T) {
	body := "First para.\n\nThe quay was <!-- SLICECOMMENT --> quiet %%PRIVSLICE%% and cold. <scriobh-ghost>GHOSTSLICE</scriobh-ghost>Rain fell. <!-- NEVERCLOSED\n\nLast."
	hidden := hiddenSpans(body)
	ps := strings.Index(body, "The quay")
	pe := strings.Index(body, "\n\nLast")
	got := canonSlice(body, hidden, ps, pe)
	for _, gone := range []string{"SLICECOMMENT", "PRIVSLICE", "GHOSTSLICE", "NEVERCLOSED"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s is in the paragraph chip: %q", gone, got)
		}
	}
	if !strings.Contains(got, "quiet") || !strings.Contains(got, "Rain fell.") {
		t.Errorf("the visible text was lost: %q", got)
	}
}

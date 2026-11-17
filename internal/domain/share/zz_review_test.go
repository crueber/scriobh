package share

import (
	"strings"
	"testing"
)

// REVIEW F8 (low): ghosted text inside a table cell survives Clean(omit); so do comments and private notes in a cell beyond the header's columns.
func TestReviewGhostInATableCellIsOmitted(t *testing.T) {
	for name, md := range map[string]string{
		"ghost in cell":         "| a | b |\n|---|---|\n| x | <scriobh-ghost>SEC</scriobh-ghost> |\n",
		"alts comment in extra": "| a |\n|---|\n| x | <!-- scriobh:alts {\"a\":\"SEC\"} --> |\n",
		"private in extra cell": "| a |\n|---|\n| x | %%SEC%% |\n",
	} {
		if out, ok := Clean(md, "omit"); ok && strings.Contains(out, "SEC") {
			t.Errorf("%s: kept in %q", name, out)
		}
	}
}

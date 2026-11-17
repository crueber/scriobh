package text

import (
	"encoding/json"
	"strings"
	"testing"
)

// A private note inside a string that is read whole (an image's description, a
// wiki link's label) is as private as one in a paragraph.
func TestStructureDropsPrivateNotesInAltAndLabels(t *testing.T) {
	for _, c := range []string{
		"![alt %%SECRET%% more](/files/att_0123456789)",
		"[[Page|lab %%SECRET%% el]]",
		"[link %%SECRET%% text](https://x.com)",
		"| a | b |\n|---|---|\n| x %%SECRET%% y | z |",
		"# Head %%SECRET%% ing",
		"Text[^1].\n\n[^1]: note %%SECRET%% here",
	} {
		j, _ := json.Marshal(Structure(c, StructOptions{}))
		if strings.Contains(string(j), "SECRET") {
			t.Errorf("%q leaked: %s", c, j)
		}
	}
	// Kept when asked for, and an unpaired delimiter is text.
	if j, _ := json.Marshal(Structure("[[Page|a %%B%% c]]", StructOptions{Private: true})); !strings.Contains(string(j), "%%B%%") {
		t.Errorf("Private should keep it: %s", j)
	}
	if j, _ := json.Marshal(Structure("[[Page|50%% off]]", StructOptions{})); !strings.Contains(string(j), "50%% off") {
		t.Errorf("an unpaired delimiter is text: %s", j)
	}
}

// One budget can be carried through many calls: the copies of a whole book are bounded.
func TestStructureSharesTheNoteBudget(t *testing.T) {
	budget := 40_000
	md := "Para " + strings.Repeat("[^a]", 100) + "\n\n[^a]: " + strings.Repeat("x ", 1000) + "\n"
	total := 0
	for i := 0; i < 20; i++ {
		d := Structure(md, StructOptions{NoteBudget: &budget})
		total += len(d.Notes)
	}
	// each copy costs about 2,000 bytes of text: 40,000 allows about 20 in all, not 2,000
	if total > 40 {
		t.Errorf("%d notes copied with a 40 KB budget", total)
	}
	if budget < -3000 {
		t.Errorf("the budget went to %d", budget)
	}
}

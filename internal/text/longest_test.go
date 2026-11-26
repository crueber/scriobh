package text

import "testing"

// A longer name that the capital rule refuses in lower-case prose must not let a shorter name take its words: the words
// are the longer name's, so nothing in them is linked to the shorter one (U9.1).
func TestLongerNameRefusedByCapitalRuleDoesNotLetTheShorterOneIn(t *testing.T) {
	m := NewMatcher(ph("The Guild of Lamplighters", "the Guild"))
	got := surfaces(m, "They met the Guild of Lamplighters at dawn. The Guild rode out.")
	want := []string{"The Guild=2"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("surfaces = %q, want %q", got, want)
	}
}

// Lower-case names both ways: the longer one is linked and the shorter one is not (the rule the plan names).
func TestLongestOfTwoLowerCaseNamesIsLinkedAlone(t *testing.T) {
	m := NewMatcher(ph("the Guild", "the Guild of Lamplighters"))
	got := surfaces(m, "They met the Guild of Lamplighters at dawn.")
	if len(got) != 1 || got[0] != "the Guild of Lamplighters=2" {
		t.Errorf("surfaces = %q", got)
	}
}

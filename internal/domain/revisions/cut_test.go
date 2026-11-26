package revisions

import "testing"

// TestIsBigCut: a save that removes more than 200 words, or more than a fifth of the chapter's words,
// is a big cut. Exactly 200 words and exactly 20 percent are not.
func TestIsBigCut(t *testing.T) {
	cases := []struct {
		before, after int
		want          bool
	}{
		{1000, 799, true},  // 201 words: over 200
		{1000, 800, false}, // 200 words, exactly 20 percent
		{1000, 850, false}, // 15 percent, under 200 words
		{40, 30, true},     // 25 percent of a short chapter
		{100, 80, false},   // exactly 20 percent
		{100, 79, true},    // 21 percent
		{100, 900, false},  // growth
		{100, 100, false},  // no change
		{0, 0, false},      // an empty chapter
		{5000, 4799, true}, // 201 words of a long chapter
		{5000, 4800, false},
	}
	for _, c := range cases {
		if got := IsBigCut(c.before, c.after); got != c.want {
			t.Errorf("IsBigCut(%d, %d) = %v, want %v", c.before, c.after, got, c.want)
		}
	}
}

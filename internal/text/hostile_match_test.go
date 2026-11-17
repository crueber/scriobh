package text

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

// A hostile alias (hundreds of words) against a megabyte of the same word is
// the worst case for a trie walk: the phrase is truncated to MaxPhraseWords, so
// the work at each word is bounded and the whole scan stays quick.
func TestAHugeAliasDoesNotMakeMatchingQuadratic(t *testing.T) {
	alias := strings.TrimSpace(strings.Repeat("la ", 200))
	m := NewMatcher([]Phrase{{Text: alias, Ref: 1}, {Text: "Elara", Ref: 2}})
	body := strings.Repeat("la ", 540_000) + "Elara" // about 1.6 MB
	start := time.Now()
	n := 0
	m.Find(body, func(Match) { n++ })
	took := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 500*time.Millisecond*timingFactor {
			t.Errorf("1.6 MB against a 200-word alias took %v", took)
		}
	})
	// The alias matches by its first MaxPhraseWords words: 540,000 / 12 hits, then Elara.
	if want := 540_000/MaxPhraseWords + 1; n != want {
		t.Errorf("%d matches, want %d", n, want)
	}
	t.Logf("took %v", took)
}

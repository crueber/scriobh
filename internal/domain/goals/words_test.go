package goals

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

func words(n int, prefix string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(prefix)
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(" ")
	}
	return b.String()
}

func TestAddedWords(t *testing.T) {
	long := words(400, "w")
	cases := []struct {
		name      string
		base, cur string
		want      int
	}{
		{"identical", "one two three", "one two three", 0},
		{"from empty", "", "one two three", 3},
		{"append", "one two", "one two three four", 2},
		{"insert in the middle", "one two five", "one two three four five", 2},
		{"prepend", "three four", "one two three four", 2},
		{"deletion earns nothing", "one two three four", "one four", 0},
		{"delete everything", long, "", 0},
		{"same-length rewrite counts", "the cat sat on the mat", "a dog lay by a rug", 6},
		{"a word grown letter by letter is one word", "the", "the quick", 1},
		{"case and punctuation are not new words", "Hello, world.", "hello world", 0},
		{"emphasis is not a new word", "slow and steady", "slow and **steady**", 0},
		{"heading marker is not a word", "a b", "# a b", 0},
		{"a move inside one save earns nothing", "alpha beta gamma delta", "gamma delta alpha beta", 0},
		{"a repeated word counts per extra occurrence", "the end", "the end the end the end", 4},
		{"unicode words", "an cat", "an cat agus an madra mór", 4},
		{"a paste of 400 words", "", long, 400},
		{"edit at both ends", "start " + long + " finish", "new start " + long + " finish line", 2},
		{"a word is not split at the cut", "interest", "interesting", 1},
		{"whitespace only", "a b", "a  b\n\n", 0},
	}
	for _, c := range cases {
		if got := addedWords(c.base, c.cur); got != c.want {
			t.Errorf("%s: addedWords = %d, want %d", c.name, got, c.want)
		}
	}
}

// A 25,000-word document with an ordinary edit must not tokenise the document.
func TestAddedWordsBigDocumentIsCheap(t *testing.T) {
	doc := words(25000, "w")
	edited := doc[:len(doc)/2] + "inserted words here " + doc[len(doc)/2:]
	start := time.Now()
	for i := 0; i < 200; i++ {
		if got := addedWords(doc, edited); got != 4 {
			t.Fatalf("got %d, want 4", got)
		}
	}
	per := time.Since(start) / 200
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if per > 5*time.Millisecond {
			t.Errorf("an ordinary edit of a 25,000-word document took %v", per)
		}
	})
}

func TestStreaks(t *testing.T) {
	cases := []struct {
		name         string
		days         []string
		today        string
		cur, longest int
	}{
		{"nothing", nil, "2026-03-10", 0, 0},
		{"today not yet hit does not break", []string{"2026-03-08", "2026-03-09"}, "2026-03-10", 2, 2},
		{"today hit is part of it", []string{"2026-03-08", "2026-03-09", "2026-03-10"}, "2026-03-10", 3, 3},
		{"a missed day ends it", []string{"2026-03-07", "2026-03-09"}, "2026-03-10", 1, 1},
		{"two days ago breaks it", []string{"2026-03-07", "2026-03-08"}, "2026-03-10", 0, 2},
		{"longest is kept", []string{"2026-01-01", "2026-01-02", "2026-01-03", "2026-01-04", "2026-03-09"}, "2026-03-10", 1, 4},
		{"unsorted and duplicated input", []string{"2026-03-09", "2026-03-08", "2026-03-09"}, "2026-03-09", 2, 2},
		{"across the spring-forward date", []string{"2026-03-07", "2026-03-08", "2026-03-09"}, "2026-03-09", 3, 3},
		{"across the fall-back date", []string{"2026-10-31", "2026-11-01", "2026-11-02"}, "2026-11-02", 3, 3},
		{"across a month and a year", []string{"2025-12-30", "2025-12-31", "2026-01-01"}, "2026-01-01", 3, 3},
		{"a leap day", []string{"2028-02-28", "2028-02-29", "2028-03-01"}, "2028-03-01", 3, 3},
		{"garbage dates are ignored", []string{"nope", "2026-03-09"}, "2026-03-09", 1, 1},
	}
	for _, c := range cases {
		cur, longest := Streaks(c.days, c.today)
		if cur != c.cur || longest != c.longest {
			t.Errorf("%s: got (%d, %d), want (%d, %d)", c.name, cur, longest, c.cur, c.longest)
		}
	}
}

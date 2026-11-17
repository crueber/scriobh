package revisions

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

// rebuild returns the older and newer text a diff describes.
func rebuild(r DiffResult) (oldText, newText string) {
	var o, n strings.Builder
	for _, s := range r.Segments {
		switch s.Op {
		case "equal":
			o.WriteString(s.Text)
			n.WriteString(s.Text)
		case "delete":
			o.WriteString(s.Text)
		case "insert":
			n.WriteString(s.Text)
		}
	}
	return o.String(), n.String()
}

func TestDiffTable(t *testing.T) {
	cases := []struct {
		name, a, b, unit string
		added, removed   int
		segs             []Segment
	}{
		{"identical", "same text\n", "same text\n", UnitWord, 0, 0, []Segment{{"equal", "same text\n"}}},
		{"both empty", "", "", UnitWord, 0, 0, []Segment{}},
		{"from empty", "", "new words\n", UnitWord, 2, 0, []Segment{{"insert", "new words\n"}}},
		{"to empty", "old words\n", "", UnitWord, 0, 2, []Segment{{"delete", "old words\n"}}},
		{"one word changed", "the quick brown fox\n", "the quick red fox\n", UnitWord, 1, 1,
			[]Segment{{"equal", "the quick "}, {"delete", "brown"}, {"insert", "red"}, {"equal", " fox\n"}}},
		{"word added", "one two\n", "one two three\n", UnitWord, 1, 0,
			[]Segment{{"equal", "one two"}, {"insert", " three"}, {"equal", "\n"}}},
		{"paragraph unit shows whole lines", "keep\n\nthe quick brown fox\n\nend\n", "keep\n\nthe quick red fox\n\nend\n", UnitParagraph, 4, 4,
			[]Segment{{"equal", "keep\n\n"}, {"delete", "the quick brown fox\n"}, {"insert", "the quick red fox\n"}, {"equal", "\nend\n"}}},
		{"paragraph moved shows as delete and insert", "A\nB\nC\n", "B\nC\nA\n", UnitParagraph, 1, 1,
			[]Segment{{"delete", "A\n"}, {"equal", "B\nC\n"}, {"insert", "A\n"}}},
		{"no trailing newline", "abc", "abd", UnitWord, 1, 1, []Segment{{"delete", "abc"}, {"insert", "abd"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := Diff(c.a, c.b, c.unit)
			if fmt.Sprint(r.Segments) != fmt.Sprint(c.segs) {
				t.Errorf("segments %v, want %v", r.Segments, c.segs)
			}
			if r.Stats.AddedWords != c.added || r.Stats.RemovedWords != c.removed {
				t.Errorf("stats %+v, want +%d -%d", r.Stats, c.added, c.removed)
			}
			if r.Changed != (c.a != c.b) {
				t.Errorf("changed = %v", r.Changed)
			}
			if o, n := rebuild(r); o != c.a || n != c.b {
				t.Errorf("rebuild gave %q -> %q", o, n)
			}
		})
	}
}

func words(rng *rand.Rand, n int) []string {
	vocab := []string{"the", "harbour", "low", "tide", "Máire", "waited", "and", "a", "gull", "cried", "over", "water", "she", "said", "nothing"}
	out := make([]string, n)
	for i := range out {
		out[i] = vocab[rng.Intn(len(vocab))]
	}
	return out
}

func para(rng *rand.Rand, w int) string { return strings.Join(words(rng, w), " ") }

// mutate applies a few random edits at the paragraph and word level.
func mutate(rng *rand.Rand, paras []string, edits int) []string {
	out := append([]string(nil), paras...)
	for i := 0; i < edits && len(out) > 0; i++ {
		k := rng.Intn(len(out))
		switch rng.Intn(4) {
		case 0:
			out = append(out[:k], out[k+1:]...)
		case 1:
			out = append(out[:k+1], append([]string{para(rng, 8)}, out[k+1:]...)...)
		case 2:
			ws := strings.Split(out[k], " ")
			ws[rng.Intn(len(ws))] = "CHANGED"
			out[k] = strings.Join(ws, " ")
		case 3:
			j := rng.Intn(len(out))
			out[k], out[j] = out[j], out[k]
		}
	}
	return out
}

func TestDiffRebuildsBothTextsAndIsDeterministic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 200; i++ {
		var paras []string
		for p := 0; p < 1+rng.Intn(20); p++ {
			paras = append(paras, para(rng, 3+rng.Intn(30)))
		}
		a := strings.Join(paras, "\n\n") + "\n"
		b := strings.Join(mutate(rng, paras, 1+rng.Intn(6)), "\n\n") + "\n"
		for _, unit := range []string{UnitWord, UnitParagraph} {
			r1, r2 := Diff(a, b, unit), Diff(a, b, unit)
			if fmt.Sprint(r1) != fmt.Sprint(r2) {
				t.Fatalf("round %d %s: the same diff gave two answers", i, unit)
			}
			if o, n := rebuild(r1); o != a || n != b {
				t.Fatalf("round %d %s: rebuild differs\nold %q\nnew %q", i, unit, a, b)
			}
			if r1.Coarse {
				t.Fatalf("round %d: a small edit must not be coarse", i)
			}
		}
	}
}

func TestDiffIsBoundedForLargeDocuments(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	build := func() []string {
		var paras []string
		for len(strings.Join(paras, " ")) < 25_000*6 { // about 25,000 words
			paras = append(paras, para(rng, 40+rng.Intn(60)))
		}
		return paras
	}
	paras := build()
	a := strings.Join(paras, "\n\n") + "\n"

	t.Run("a few edits stay exact and fast", func(t *testing.T) {
		b := strings.Join(mutate(rng, paras, 8), "\n\n") + "\n"
		start := time.Now()
		r := Diff(a, b, UnitWord)
		el := time.Since(start)
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if el > 500*time.Millisecond*timingFactor {
				t.Errorf("took %v", el)
			}
		})
		if r.Coarse {
			t.Error("a few edits must be refined to words")
		}
		if o, n := rebuild(r); o != a || n != b {
			t.Error("rebuild differs")
		}
	})
	t.Run("a complete rewrite stays within the budget", func(t *testing.T) {
		b := strings.Join(build(), "\n\n") + "\n" // a different 25,000 words
		start := time.Now()
		r := Diff(a, b, UnitWord)
		el := time.Since(start)
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if el > 2*time.Second*timingFactor {
				t.Errorf("took %v", el)
			}
		})
		if o, n := rebuild(r); o != a || n != b {
			t.Error("rebuild differs")
		}
	})
	t.Run("one giant paragraph rewritten degrades, flagged", func(t *testing.T) {
		one := strings.Join(words(rng, 25_000), " ")
		two := strings.Join(words(rng, 25_000), " ")
		start := time.Now()
		r := Diff(one, two, UnitWord)
		el := time.Since(start)
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if el > 2*time.Second*timingFactor {
				t.Errorf("took %v", el)
			}
		})
		if !r.Coarse {
			t.Error("a hunk this size must hit the limit and say so")
		}
		if o, n := rebuild(r); o != one || n != two {
			t.Error("a coarse diff must still rebuild both texts")
		}
	})
}

func TestEncodeRoundTripAndThreshold(t *testing.T) {
	small := strings.Repeat("a", CompressAbove)
	if enc, blob, err := encode(small); err != nil || enc != "identity" || string(blob) != small {
		t.Errorf("content at the threshold is stored as is: %q %v", enc, err)
	}
	big := strings.Repeat("The harbour at low tide. ", 2000)
	enc, blob, err := encode(big)
	if err != nil || enc != "gzip" || len(blob) >= len(big)/4 {
		t.Fatalf("big text: %q %d bytes of %d, %v", enc, len(blob), len(big), err)
	}
	got, err := decode(enc, blob, Hash(big))
	if err != nil || got != big {
		t.Fatalf("round trip: %v", err)
	}
	if _, err := decode(enc, blob, Hash("something else")); err == nil {
		t.Error("a blob that does not match its hash must be refused")
	}
	rng := rand.New(rand.NewSource(1))
	noise := make([]byte, 20_000)
	rng.Read(noise)
	if enc, _, _ := encode(string(noise)); enc != "identity" {
		t.Errorf("incompressible content must not be stored larger: %s", enc)
	}
}

// A chapter set as one paragraph of 30,000 words is diffed sentence by sentence, so three one-word edits are three words
// added and three removed, as the same text in many paragraphs gives (and not the whole paragraph, twice).
func TestOneHugeParagraphIsRefinedLikeTheSameTextInManyParagraphs(t *testing.T) {
	var sents []string
	for i := 0; i < 3000; i++ {
		sents = append(sents, fmt.Sprintf("Sentence %d has ten plain words in it, here, for you.", i))
	}
	edit := func(ss []string) []string {
		out := append([]string(nil), ss...)
		for _, i := range []int{10, 1500, 2990} {
			out[i] = strings.Replace(out[i], "plain", "odd", 1)
		}
		return out
	}
	oneOld, oneNew := strings.Join(sents, " "), strings.Join(edit(sents), " ")
	manyOld, manyNew := strings.Join(sents, "\n"), strings.Join(edit(sents), "\n")
	one, many := Diff(oneOld, oneNew, UnitWord), Diff(manyOld, manyNew, UnitWord)
	if one.Coarse || many.Coarse {
		t.Fatalf("coarse: one=%v many=%v", one.Coarse, many.Coarse)
	}
	oa, or_, ma, mr := one.Stats.AddedWords, one.Stats.RemovedWords, many.Stats.AddedWords, many.Stats.RemovedWords
	if oa != 3 || or_ != 3 || oa != ma || or_ != mr {
		t.Errorf("one paragraph: +%d -%d; many paragraphs: +%d -%d; want +3 -3 for both", oa, or_, ma, mr)
	}
	if o, _ := rebuild(one); o != oneOld {
		t.Error("the old text is not rebuilt from the segments")
	}
}

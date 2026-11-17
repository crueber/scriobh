package semantic

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

func words(n int, tag string) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s%d", tag, i)
	}
	return b.String()
}

// checkChunks asserts the invariants every split must keep.
func checkChunks(t *testing.T, body string, cs []Chunk) {
	t.Helper()
	prevEnd := -1
	for i, c := range cs {
		if got := runeSlice(body, c.Start, c.End); got != c.Text {
			t.Fatalf("chunk %d: text is not body[%d:%d]\n got %q\nwant %q", i, c.Start, c.End, c.Text, got)
		}
		if c.Words > ChunkMaxWords {
			t.Fatalf("chunk %d has %d words, limit %d", i, c.Words, ChunkMaxWords)
		}
		if utf8.RuneCountInString(c.Text) > ChunkMaxRunes {
			t.Fatalf("chunk %d has %d characters, limit %d", i, utf8.RuneCountInString(c.Text), ChunkMaxRunes)
		}
		if strings.TrimSpace(c.Text) == "" {
			t.Fatalf("chunk %d is blank", i)
		}
		if c.End <= c.Start || c.End <= prevEnd {
			t.Fatalf("chunk %d does not advance: %d..%d after end %d", i, c.Start, c.End, prevEnd)
		}
		prevEnd = c.End
	}
}

func TestSplitShortAndEmpty(t *testing.T) {
	if cs, tr := Split(""); len(cs) != 0 || tr {
		t.Fatalf("empty body: %v %v", cs, tr)
	}
	if cs, _ := Split("\n \n\t\n"); len(cs) != 0 {
		t.Fatalf("blank lines made chunks: %v", cs)
	}
	body := "Title\nOne short paragraph."
	cs, _ := Split(body)
	if len(cs) != 1 || cs[0].Text != body || cs[0].Start != 0 || cs[0].End != utf8.RuneCountInString(body) {
		t.Fatalf("got %+v", cs)
	}
}

func TestSplitGroupsParagraphsAndOverlaps(t *testing.T) {
	// 40 paragraphs of 10 words: chunks of about 120 words, overlapping by up to 25 words (two paragraphs).
	var ps []string
	for i := 0; i < 40; i++ {
		ps = append(ps, words(10, fmt.Sprintf("p%d_", i)))
	}
	body := strings.Join(ps, "\n")
	cs, tr := Split(body)
	if tr {
		t.Fatal("truncated")
	}
	checkChunks(t, body, cs)
	if len(cs) < 3 {
		t.Fatalf("expected several chunks, got %d", len(cs))
	}
	for i := 0; i+1 < len(cs); i++ {
		if cs[i].Words < ChunkTarget {
			t.Errorf("chunk %d has %d words, under the target", i, cs[i].Words)
		}
		// the next chunk starts inside this one: an overlap of whole paragraphs, at most ChunkOverlap words
		if cs[i+1].Start >= cs[i].End {
			t.Errorf("chunk %d and %d do not overlap", i, i+1)
		}
		ov := runeSlice(body, cs[i+1].Start, cs[i].End)
		if w := len(strings.Fields(ov)); w > ChunkOverlap {
			t.Errorf("overlap of %d words, limit %d", w, ChunkOverlap)
		}
	}
	// Every paragraph is whole in some chunk.
	for i, p := range ps {
		found := false
		for _, c := range cs {
			found = found || strings.Contains(c.Text, p)
		}
		if !found {
			t.Errorf("paragraph %d is in no chunk whole", i)
		}
	}
}

func TestSplitLongParagraphs(t *testing.T) {
	sentence := func(i int) string { return words(14, fmt.Sprintf("s%d_", i)) + "." }
	var b strings.Builder
	for i := 0; i < 60; i++ { // one 840-word paragraph with sentence ends
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(sentence(i))
	}
	body := "Heading\n" + b.String()
	cs, _ := Split(body)
	checkChunks(t, body, cs)
	if len(cs) < 5 {
		t.Fatalf("a long paragraph should be cut into several chunks, got %d", len(cs))
	}
	for i, c := range cs[1:] { // cut at sentence ends: every chunk but the last of the paragraph ends with a full stop
		if i < len(cs)-2 && !strings.HasSuffix(c.Text, ".") && !strings.HasSuffix(c.Text, "Heading") {
			t.Errorf("chunk %d does not end at a sentence end: …%q", i+1, c.Text[max(0, len(c.Text)-20):])
		}
	}
	// No words and no sentence ends: cut at spaces.
	one := words(900, "w")
	cs, _ = Split(one)
	checkChunks(t, one, cs)
	if len(cs) < 4 {
		t.Fatalf("got %d chunks", len(cs))
	}
	// Text with no spaces at all: cut at the character limit.
	cjk := strings.Repeat("字", 5000)
	cs, _ = Split(cjk)
	checkChunks(t, cjk, cs)
	if len(cs) != 4 { // 5000 characters in pieces of at most 1400
		t.Fatalf("5000 characters made %d chunks", len(cs))
	}
	cjk2 := strings.Repeat("今日は晴れです。", 600)
	cs, _ = Split(cjk2)
	checkChunks(t, cjk2, cs)
	for i, c := range cs {
		if i < len(cs)-1 && !strings.HasSuffix(c.Text, "。") {
			t.Errorf("CJK chunk %d does not end at a sentence end", i)
		}
	}
}

func TestSplitOffsetsAreCharactersNotBytes(t *testing.T) {
	var ps []string
	for i := 0; i < 30; i++ {
		ps = append(ps, "Ünïcödé 😀 "+words(12, fmt.Sprintf("é%d_", i)))
	}
	body := strings.Join(ps, "\n")
	cs, _ := Split(body)
	checkChunks(t, body, cs)
	last := cs[len(cs)-1]
	if last.End != utf8.RuneCountInString(body) {
		t.Fatalf("last chunk ends at %d, body has %d characters", last.End, utf8.RuneCountInString(body))
	}
}

func TestSplitIsBoundedPerDocument(t *testing.T) {
	var ps []string
	for i := 0; i < 20_000; i++ {
		ps = append(ps, words(30, "x")) // 600k words of body, over 8,000 chunks
	}
	body := strings.Join(ps, "\n")
	cs, tr := Split(body)
	if !tr || len(cs) != MaxChunksPerDoc {
		t.Fatalf("got %d chunks, truncated %v", len(cs), tr)
	}
}

func TestSplitFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	alphabet := []string{"alpha", "béta", "gamma.", "δ", "字字字。", "😀", "a-b", "c?", "d!", "…", "“quoted”"}
	for n := 0; n < 300; n++ {
		var lines []string
		for l := r.Intn(12); l >= 0; l-- {
			var b strings.Builder
			for w := r.Intn(600); w >= 0; w-- {
				b.WriteString(alphabet[r.Intn(len(alphabet))])
				if r.Intn(9) != 0 {
					b.WriteByte(' ')
				}
			}
			lines = append(lines, strings.TrimSpace(b.String()))
		}
		body := strings.Join(lines, "\n")
		cs, _ := Split(body)
		checkChunks(t, body, cs)
	}
}

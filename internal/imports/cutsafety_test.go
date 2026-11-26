package imports

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// The importer cuts finished Markdown in one place: a file split at its second-level headings into scenes. The cut is asked of the
// cut-safety of doc.split (docs.SafeCut), so a line inside a comment or a block the editor keeps as source stays text, and the scenes
// after it keep the keys they would have had.

// oldSecondLevelHeadings is secondLevelHeadings as it was: fences and front matter only.
func oldSecondLevelHeadings(s string, limit int) []h2 {
	var out []h2
	fence := ""
	i := frontMatterEnd(s)
	for i < len(s) {
		j := strings.IndexByte(s[i:], '\n')
		line, next := s[i:], len(s)
		if j >= 0 {
			line, next = s[i:i+j], i+j+1
		}
		if m := reFence.FindStringSubmatch(line); m != nil {
			switch {
			case fence == "":
				fence = m[1]
			case fence == m[1]:
				fence = ""
			}
		} else if fence == "" {
			if m := reH2.FindStringSubmatch(line); m != nil {
				out = append(out, h2{title: m[1], start: i, end: next})
				if len(out) >= limit {
					return out
				}
			}
		}
		i = next
	}
	return out
}

func TestAHeadingInsideACommentIsNotACutAndTheScenesKeepTheirPlaces(t *testing.T) {
	s := "Before.\n\n## One\n\nText.\n\n<!--\n## Hidden in a comment\n-->\n\n## Two\n\nMore.\n\n```\n## In code\n```\n\n## Three\n\nEnd.\n"
	hs := secondLevelHeadings(s, 100)
	var titles []string
	var idx []int
	for _, h := range hs {
		titles = append(titles, h.title)
		idx = append(idx, h.idx)
	}
	if strings.Join(titles, "|") != "One|Two|Three" {
		t.Fatalf("headings: %v", titles)
	}
	// "Hidden in a comment" was the second line that looked like a heading: Two is the third and Three the fourth, as the older cut named them
	// only for the text outside code (a fence is skipped, as it always was)
	if fmt.Sprint(idx) != "[0 2 3]" {
		t.Fatalf("places: %v", idx)
	}
	old := oldSecondLevelHeadings(s, 100)
	if len(old) != 4 { // what the importer would have cut before: the heading in the comment too
		t.Fatalf("old: %d", len(old))
	}
}

// For ordinary Markdown (fences, no comments, no front matter), the two cuts are the same: nothing the importer cut before is left as text.
func TestTheCutSafetyChangesNothingForTextWithoutCommentsOrFrontMatter(t *testing.T) {
	pieces := []string{"text line\n", "## Heading\n", "## Another heading ##\n", "```\n", "~~~\n", "\n", "    code\n", "### Third\n", "# One\n", "> quote\n"}
	rnd := rand.New(rand.NewSource(11))
	for n := 0; n < 4000; n++ {
		var b strings.Builder
		for k := rnd.Intn(20); k >= 0; k-- {
			b.WriteString(pieces[rnd.Intn(len(pieces))])
		}
		s := b.String()
		got, want := secondLevelHeadings(s, 1000), oldSecondLevelHeadings(s, 1000)
		if len(got) != len(want) {
			t.Fatalf("%q\nnow %d headings, before %d", s, len(got), len(want))
		}
		for i := range got {
			if got[i].start != want[i].start || got[i].idx != i {
				t.Fatalf("%q: heading %d differs", s, i)
			}
		}
	}
}

// A hostile file does not make the check cost more than a pass: ten thousand headings in one text.
func TestTheCutSafetyOfAFileOfHeadingsIsOnePass(t *testing.T) {
	s := strings.Repeat("## H\n<!-- c -->\n", 50_000)
	if hs := secondLevelHeadings(s, MaxDocuments+1); len(hs) != MaxDocuments+1 {
		t.Fatalf("%d headings", len(hs))
	}
}

// Where the two differ, it is because the editor's rule for where a fence ends is the one that holds: a line such as ```go cannot close a
// fence (a closing fence has no words after it), so a heading under it is still inside the code, and the cut leaves it as text. The cut
// never gains a heading, and each it loses is under such a line, inside a comment or in front matter.
func TestWhereTheCutSafetyDiffersTheEditorsRuleHolds(t *testing.T) {
	pieces := []string{"text line\n", "## Heading\n", "```\n", "```go\n", "~~~\n", "~~~ x\n", "\n", "<!--\n", "-->\n", "<!-- c -->\n"}
	rnd := rand.New(rand.NewSource(13))
	lost := 0
	for n := 0; n < 4000; n++ {
		var b strings.Builder
		for k := rnd.Intn(20); k >= 0; k-- {
			b.WriteString(pieces[rnd.Intn(len(pieces))])
		}
		s := b.String()
		got, old := secondLevelHeadings(s, 1000), oldSecondLevelHeadings(s, 1000)
		in := map[int]bool{}
		for _, h := range old {
			in[h.start] = true
		}
		for _, h := range got {
			if !in[h.start] {
				t.Fatalf("%q: a heading the older cut did not have", s)
			}
		}
		lost += len(old) - len(got)
		if len(got) > len(old) {
			t.Fatalf("%q", s)
		}
	}
	if lost == 0 {
		t.Error("the random texts never exercised a disagreement")
	}
}

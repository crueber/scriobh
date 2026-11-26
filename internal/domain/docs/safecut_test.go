package docs

import (
	"math/rand"
	"strings"
	"testing"
)

// bruteInsideSourceBlock is doc.split's check as it was before SafeCut: the regions are found and every one is asked, for each cut. SafeCut
// must answer as it does for every byte index of every text.
func bruteInsideSourceBlock(content string, cut int) bool {
	type region struct{ start, end int }
	var regs []region
	lines := strings.SplitAfter(content, "\n")
	pos := 0
	var (
		fence      string
		fenceStart int
		comment    = -1
	)
	for i, ln := range lines {
		start, end := pos, pos+len(ln)
		pos = end
		t := strings.TrimRight(ln, "\r\n")
		trim := strings.TrimLeft(t, " ")
		switch {
		case fence != "":
			if len(trim) >= len(fence) && strings.HasPrefix(trim, fence) && strings.Trim(trim, fence[:1]) == "" && len(t)-len(trim) < 4 {
				regs = append(regs, region{fenceStart, end})
				fence = ""
			}
		case comment >= 0:
			if strings.Contains(t, "-->") {
				regs = append(regs, region{comment, end})
				comment = -1
			}
		case i == 0 && t == "---":
			off := end
			for _, nx := range lines[1:] {
				off += len(nx)
				if strings.TrimRight(nx, "\r\n") == "---" {
					regs = append(regs, region{0, off})
					break
				}
			}
		case len(t)-len(trim) < 4 && (strings.HasPrefix(trim, "```") || strings.HasPrefix(trim, "~~~")):
			m := trim[:1]
			n := len(trim) - len(strings.TrimLeft(trim, m))
			fence, fenceStart = strings.Repeat(m, n), start
		case strings.HasPrefix(trim, "<!--"):
			if strings.Contains(t[strings.Index(t, "<!--")+4:], "-->") {
				regs = append(regs, region{start, end})
			} else {
				comment = start
			}
		}
	}
	if fence != "" {
		regs = append(regs, region{fenceStart, len(content)})
	}
	if comment >= 0 {
		regs = append(regs, region{comment, len(content)})
	}
	for _, r := range regs {
		if cut > r.start && cut < r.end {
			return true
		}
	}
	return false
}

func TestSafeCutAnswersAsTheOldCheckDidForEveryPositionOfRandomTexts(t *testing.T) {
	pieces := []string{"text line\n", "## Heading\n", "```\n", "~~~\n", "````\n", "  ```\n", "<!-- note -->\n", "<!--\n", "-->\n", "---\n", "\n", "a --> b\n", "    code\n", "<!-- one --> after\n", "## <!-- x -->\n", "\r\n", "no newline"}
	rnd := rand.New(rand.NewSource(7))
	for n := 0; n < 3000; n++ {
		var b strings.Builder
		for k := rnd.Intn(14); k >= 0; k-- {
			b.WriteString(pieces[rnd.Intn(len(pieces))])
		}
		s := b.String()
		safe := SafeCut(s)
		for cut := -1; cut <= len(s)+1; cut++ {
			if got, want := !safe(cut), bruteInsideSourceBlock(s, cut); got != want {
				t.Fatalf("cut %d of %q: SafeCut says inside=%v, the old check %v", cut, s, got, want)
			}
		}
		if (len(s) > 0) && insideSourceBlock(s, len(s)/2) != bruteInsideSourceBlock(s, len(s)/2) {
			t.Fatalf("insideSourceBlock changed for %q", s)
		}
	}
}

// Thousands of candidate cuts in one large text cost one pass over it, not one each.
func TestSafeCutReadsTheTextOnce(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString("## Heading\n\nSome text under it that is a line.\n\n")
	}
	s := b.String()
	safe := SafeCut(s)
	n := 0
	for i := 0; i < len(s); i += 7 {
		if safe(i) {
			n++
		}
	}
	if n != len(s)/7+1 {
		t.Errorf("%d of %d cuts safe in a text with no source block", n, len(s)/7+1)
	}
}

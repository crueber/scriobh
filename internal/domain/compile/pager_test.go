package compile

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/testutil"
	"github.com/crueber/scriobh/internal/text"
)

// Pagination walks a paragraph once: eight half-megabyte paragraphs take
// milliseconds, not the minutes it took when each page re-sliced the rest.
func TestPaginationOfHugeParagraphsIsLinear(t *testing.T) {
	txt := strings.Repeat("word ", 500_000/5)
	var nodes []text.Node
	for i := 0; i < 8; i++ {
		nodes = append(nodes, text.Node{Kind: text.NPara, Inl: []text.Inline{{Kind: text.IText, Text: txt}}})
	}
	b := &publish.Book{Sections: []publish.Section{{Kind: publish.SecChapter, Title: "x", Level: 1, Nodes: nodes}}}
	o, _ := Builtin(ShunnUS)
	start := time.Now()
	p := paginate(b, o.Options, 3)
	took := time.Since(start)
	t.Logf("8 x 500 KB: %v, %d pages", took, p.total)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if took > 2*time.Second*pagerFactor {
			t.Errorf("took %v", took)
		}
	})
	// 4 MB of text at about 12 words a line and 23 lines a page
	if p.total < 1000 || p.total > 3000 {
		t.Errorf("an estimate of %d pages", p.total)
	}
	if len(p.pages) != 3 || len(p.pages[2].Blocks) == 0 {
		t.Errorf("the first pages: %+v", p.pages)
	}
}

package imports

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/testutil"
)

// The worst shapes the review found for the Markdown that comes back from Pandoc, cut to the size the Pandoc path
// accepts. Pandoc is not involved: these are the converter's own limits, driven with arbitrary Markdown.

func pandocShapes() map[string]string {
	const n = maxPandocMarkdown
	rep := func(unit string, withBreaks bool) string {
		var b strings.Builder
		for i := 0; b.Len() < n-64; i++ {
			b.WriteString(unit)
			if withBreaks && i%100 == 99 {
				b.WriteString("\n\n")
			}
		}
		return b.String()
	}
	return map[string]string{
		"code spans":                rep("`a` ", true),
		"emphasis":                  rep("*a* b ", true),
		"strong":                    rep("**a** ", true),
		"links":                     rep("[a](u) ", true),
		"plain words":               rep("word ", true),
		"nested emphasis":           rep("*a ", true),
		"unmatched markers":         rep("*a_", true),
		"quote markers, fence lead": "```x`y\n" + strings.Repeat("> ", n/2-8),
		"list markers, fence lead":  "\t```\n" + strings.Repeat("- ", n/2-8),
		"nested list":               strings.Repeat("- ", n/2),
		"quote markers":             strings.Repeat("> ", n/2),
	}
}

func TestPandocMarkdownWorstShapesAreBoundedInTimeAndMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("takes a few seconds")
	}
	for name, md := range pandocShapes() {
		t.Run(name, func(t *testing.T) {
			var err error
			dur, peak := measurePeak(func() {
				_, err = convertPandoc(&pandoc.Imported{Markdown: []byte(md), Format: "rst", Version: "x"}, []byte("src"), "doc", "chapters", convOpts{})
			})
			t.Logf("%-28s %8.0f ms  heap in use at peak %4d MB  (err: %v)", name, float64(dur)/float64(time.Millisecond), peak, err)
			t.Run("time", func(t *testing.T) {
				testutil.Timing(t)
				if dur > 15*time.Second {
					t.Errorf("took %v", dur)
				}
			})
			if peak > 512 {
				t.Errorf("heap in use reached %d MB", peak)
			}
		})
	}
}

func TestPandocMarkdownOverTheCapIsRefusedWithoutParsing(t *testing.T) {
	md := []byte(strings.Repeat("a ", maxPandocMarkdown/2+1))
	start := time.Now()
	_, err := convertPandoc(&pandoc.Imported{Markdown: md}, []byte("x"), "doc", "chapters", convOpts{})
	if err == nil || !strings.Contains(err.Error(), "larger than the import limit") {
		t.Fatalf("%v", err)
	}
	refused := time.Since(start)
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if refused > time.Second {
			t.Errorf("took %v", refused)
		}
	})
}

// A request that has gone away stops the parse between chunks.
func TestPandocParseStopsWhenTheCallerGoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	md := strings.Repeat("A paragraph of words.\n\n", 20_000) // several chunks
	_, err := convertPandoc(&pandoc.Imported{Markdown: []byte(md)}, []byte("x"), "doc", "chapters", convOpts{ctx: ctx})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("%v", err)
	}
}

func TestChunksAreWholeBlocksAndAddUpToTheText(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		b.WriteString("Paragraph number words words words words words.\n\n")
		if i%50 == 0 {
			b.WriteString("- item one\n\n- item two, a loose list\n\n  continued under item two\n\n")
			b.WriteString("> a quote\n>\n> that goes on\n\n")
		}
	}
	text := b.String()
	chunks := chunkMarkdown(text)
	if strings.Join(chunks, "") != text {
		t.Fatal("the chunks do not add up to the text")
	}
	if len(chunks) < 2 {
		t.Fatalf("%d chunks for %d bytes", len(chunks), len(text))
	}
	for i, c := range chunks {
		if len(c) > pandocChunk+4096 {
			t.Errorf("chunk %d is %d bytes", i, len(c))
		}
		// A chunk never starts in the middle of a list item or a quote.
		if i > 0 {
			first := c[:strings.IndexByte(c, '\n')]
			if strings.HasPrefix(first, "  ") || strings.HasPrefix(first, ">") && !strings.HasPrefix(c, "> a quote") {
				t.Errorf("chunk %d starts with a continuation: %q", i, first)
			}
		}
	}
	// No blank line at all: one chunk, and the line caps are what bound it.
	if got := chunkMarkdown(strings.Repeat("x ", 200_000)); len(got) != 1 {
		t.Errorf("%d chunks of text without a break", len(got))
	}
}

// The caps apply to a line whatever the parser would make of it: a lead that is not a fence, a fence that is closed
// with trailing space, a tab-indented fence. The reviewer's test covers the cap's value; this one covers the time.
func TestPandocCapsHoldAtTheConverterForTheReviewedLeads(t *testing.T) {
	for _, lead := range []string{"```x`y\n", "\t```\n", "```\ncode\n``` \n", "- item\n  ```\n  code\n\ntext\n"} {
		md := lead + strings.Repeat("> ", 150_000) + "x\n"
		start := time.Now()
		_, err := convertPandoc(&pandoc.Imported{Markdown: []byte(md)}, []byte("x"), "doc", "chapters", convOpts{})
		took := time.Since(start)
		t.Run("time", func(t *testing.T) {
			testutil.Timing(t)
			if took > 5*time.Second {
				t.Errorf("%q: took %v (err %v)", lead, took, err)
			}
		})
	}
}

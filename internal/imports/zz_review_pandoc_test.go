package imports

import (
	"strings"
	"testing"
)

// FAILS by design (TestReviewBoundMarkdownIsNotSwitchedOffByALineThatIsNotAFence). boundMarkdown is the only thing
// between Pandoc's output and the Markdown parser's quadratic cases. It skips "fenced code", but its idea of a
// fence differs from the parser's, and after any of these it stops capping markers for the rest of the document:
//   - a backtick fence whose info string holds a backtick is not a fence;
//   - a tab is four columns, so "\t```" is an indented code line;
//   - a closing fence may be followed by spaces (boundMarkdown wants the line to be nothing but backticks);
//   - a fence inside a list item ends with the item, not at a closing line.
//
// Measured at convertPandoc: 300,000 "> " markers after the first lead: 44.8 s (quadratic); capped: 1 ms.
func TestReviewBoundMarkdownIsNotSwitchedOffByALineThatIsNotAFence(t *testing.T) {
	for name, lead := range map[string]string{
		"tab then backticks":                      "\t```",
		"backtick fence, backtick in info":        "```x`y",
		"backtick fence, backtick in info (long)": "`````a`b",
		"closing fence with trailing space":       "```\ncode\n``` ",
		"fence in a list item, then text":         "- item\n  ```\n  code\n\ntext",
	} {
		t.Run(name, func(t *testing.T) {
			in := lead + "\n" + strings.Repeat("> ", 100_000) + "x\n"
			out, _ := boundMarkdown(in)
			longest := 0
			for _, l := range strings.Split(out, "\n") {
				n := 0
				for strings.HasPrefix(l, "> ") {
					n++
					l = l[2:]
				}
				longest = max(longest, n)
			}
			if longest > mdMaxMarkers+1 {
				t.Fatalf("a line of %d quote markers got through boundMarkdown after %q", longest, lead)
			}
		})
	}
}

package imports

import (
	"strings"
	"testing"
)

// merge_into renumbers the footnotes of the text it adds, and only those: a [^1] in code is text.
func TestMergeBodiesRenumbersOnlyFootnotes(t *testing.T) {
	a := "First[^1] and second[^2].\n\n[^1]: one\n[^2]: two\n"
	for name, c := range map[string]struct{ b, want string }{
		"a note":                          {"Text[^1].\n\n[^1]: note\n", "Text[^3].\n\n[^3]: note\n"},
		"inline code":                     {"the literal `[^1]` stays\n", "the literal `[^1]` stays\n"},
		"double backticks":                {"a ``[^1] and ` inside`` stays\n", "a ``[^1] and ` inside`` stays\n"},
		"code and a real mark":            {"`[^1]` and then[^1] and `[^2]`\n\n[^1]: n\n", "`[^1]` and then[^3] and `[^2]`\n\n[^3]: n\n"},
		"a reference at the start":        {"[^2] begins the line\n\n[^2]: n\n", "[^4] begins the line\n\n[^4]: n\n"},
		"an unclosed backtick":            {"a ` stray and note[^1]\n\n[^1]: n\n", "a ` stray and note[^3]\n\n[^3]: n\n"},
		"a fence":                         {"```\n[^1]\n```\n\nafter[^1]\n\n[^1]: n\n", "```\n[^1]\n```\n\nafter[^3]\n\n[^3]: n\n"},
		"escaped backticks":               {"a literal \\` and a real note[^1] and another \\`\n\n[^1]: n\n", "a literal \\` and a real note[^3] and another \\`\n\n[^3]: n\n"},
		"an escaped backtick before code": {"a \\` then `[^1]` and note[^1]\n\n[^1]: n\n", "a \\` then `[^1]` and note[^3]\n\n[^3]: n\n"},
		"an escaped bracket":              {"literal \\[^1\\] text\n", "literal \\[^1\\] text\n"},
	} {
		got := mergeBodies(a, c.b)
		want := strings.TrimRight(a, "\n") + "\n\n" + c.want
		if got != want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, want)
		}
	}
}

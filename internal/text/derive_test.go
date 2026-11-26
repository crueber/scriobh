package text

import (
	"math/rand"
	"strings"
	"testing"
)

// Derive is one parse of what WordCount and SearchBody each parse: its answers must be theirs, for every text.
func TestDeriveMatchesCountAndSearch(t *testing.T) {
	corpus := []string{"", "a", "# Head\n\nSome *words* here.\n", "```\ncode\n```\n\nafter", "%%private note%%\n\nvisible",
		"[[Wiki|Link]] and [a](b)", "| a | b |\n|---|---|\n| c | d |", "- one\n- two\n  - three", "> quote *x*\n", "[a]: b\n\nword",
		"Éire café naïve", "<div>\n\nhtml\n</div>", "::: note\nx\n:::", "&amp; &#65; <b>bold</b>", "[a]: javascript:x\n\nword"}
	toks := []string{"*", "_", "`", "```\n", "[", "]", "(", ")", "![", "<", ">", "%%", "\n", "\n\n", "- ", "> ", "# ", "word", "Éire", " ",
		"[[", "]]", "|", "---", ":::", "&amp;", "~~~\n", "http://a.b", "[a]: b\n", "1. "}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		for n := rng.Intn(60); n >= 0; n-- {
			b.WriteString(toks[rng.Intn(len(toks))])
		}
		corpus = append(corpus, b.String())
	}
	for _, md := range corpus {
		n, s := Derive(md)
		if want := WordCount(md); n != want {
			t.Fatalf("Derive(%q) words = %d, WordCount = %d", md, n, want)
		}
		if want := SearchBody(md); s != want {
			t.Fatalf("Derive(%q) search = %q, SearchBody = %q", md, s, want)
		}
	}
}

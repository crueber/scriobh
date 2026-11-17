package text

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The plain form of a range holds whole letters whatever the script: a range that ends after a two- or three-byte
// letter does not stop in the middle of it.
func TestVisibleKeepsWholeLettersOfAnyScript(t *testing.T) {
	for _, c := range []struct{ body, quote string }{
		{"وصلت مارا إلى الميناء في الليل.\n", "الميناء"},
		{"Café crème brûlée and 日本語のテキスト here.\n", "日本語のテキスト"},
		{"The *naïve* café stood.\n", "café"},
	} {
		blocks, ok := PlainBlocks(c.body)
		if !ok || len(blocks) == 0 {
			t.Fatalf("%q: not placed", c.body)
		}
		a := strings.Index(c.body, c.quote)
		b := a + len(c.quote)
		i, j, ok := blocks[0].Visible(a, b)
		if !ok {
			t.Fatalf("%q: no place", c.quote)
		}
		got := blocks[0].Text[i:j]
		if got != c.quote || !utf8.ValidString(got) {
			t.Errorf("%q came out as %q", c.quote, got)
		}
		if !utf8.ValidString(blocks[0].Text[:i]) || !utf8.ValidString(blocks[0].Text[j:]) {
			t.Errorf("%q: the words around it were cut inside a letter", c.quote)
		}
	}
}

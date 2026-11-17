package text

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/testutil"
)

func TestWordCountBasics(t *testing.T) {
	cases := []struct {
		name string
		md   string
		want int
	}{
		{"empty", "", 0},
		{"whitespace", " \n\n\t \n", 0},
		{"prose", "The harbour at low tide, and Máire waited.", 8},
		{"punctuation only is not a word", "— ... * * *\n\n!!!", 0},
		{"digits are words", "In 1916 there were 3 of them.", 7},
		{"heading", "# Chapter One\n\nIt began.", 4},
		{"emphasis does not split", "a*b*c and **bold** words", 4},
		{"inline code counts", "Run `go test` now", 4},
		{"code block does not", "Before\n\n```go\nfunc main() {}\n```\n\nAfter", 2},
		{"indented code does not", "Before\n\n    indented code here\n\nAfter", 2},
		{"private note inline", "Visible %%hidden words%% visible", 2},
		{"private note across lines", "Start %%a\nb\nc%% end", 2},
		{"private note unclosed is text", "Start %%a b c", 4},
		{"empty private pair is text", "Start %%%% end", 2},
		{"private note fuses neighbours", "a%%x%%b", 1},
		{"wiki link shows its label", "See [[Old Harbour]] now", 4},
		{"wiki link alias", "See [[Old Harbour|the quay]] now", 4},
		{"wiki link in a note still counts", "%%see [[Old Harbour]]%%", 2},
		{"link text counts, destination does not", "[read this now](https://example.com/a/b)", 3},
		{"autolink text counts", "<https://example.com/a>", 1},
		{"image is zero width", "word![a picture of a dog](dog.png)word", 1},
		{"footnote reference is zero width", "word[^1]more", 1},
		{"footnote definition body counts", "text[^1]\n\n[^1]: the note itself", 4},
		{"inline html is zero width", "a<br>b and <b>bold</b>", 3},
		{"html block is source", "<div>\nnot counted\n</div>\n\ncounted", 1},
		{"html comment is source", "<!-- scriobh:alts {\"a\":1} -->", 0},
		{"table is source", "| a | b |\n|---|---|\n| one | two |\n\nafter", 1},
		{"front matter is source", "---\ntitle: A Title\n---\n\nbody words", 2},
		{"a leading scene break is not front matter", "---\n\nbody words", 2},
		{"directive is source", ":::note\nnot counted\n:::\n\ncounted", 1},
		{"unclosed directive is text", ":::note\ncounted words", 3},
		{"reference definition is source", "[a]: https://example.com \"Title Here\"\n\nbody", 1},
		{"task marker is not a word", "- [ ] buy milk\n- [x] done already", 4},
		{"bare task marker stays text", "- [x]", 1},
		{"hard break separates", "one\\\ntwo  \nthree", 3},
		{"soft break separates", "one\ntwo", 2},
		{"entity decodes to a space", "one&nbsp;two", 2},
		{"entity decodes to a symbol", "&copy; &amp; &#35;", 0},
		{"unsafe link stays text", "[a b](javascript:alert(1))", 2},
		{"U+FEFF is not a separator in Go but is in JS", "a\ufeffb", 2},
		{"NEL is not a separator in JS", "a\u0085b", 1},
		{"no-break space separates", "a\u00a0b", 2},
		{"CJK run is one word", "日本語のテキスト", 1},
		{"blockquote and list", "> quoted words\n\n- one\n- two three", 5},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WordCount(c.md); got != c.want {
				t.Errorf("WordCount(%q) = %d, want %d", c.md, got, c.want)
			}
		})
	}
}

var propertyBases = []string{
	"The harbour at low tide, and Máire waited.",
	"# Title\n\nFirst paragraph here.\n\nSecond paragraph, with *emphasis* and a [link](http://a.b/c).",
	"- one\n- two words\n- three\n\n> a quoted line\n> and another",
	"Text with a footnote[^1] and a [[Wiki Link|shown]].\n\n[^1]: The note.",
	"```\ncode stays out\n```\n\nafter the code",
	"A hard\\\nbreak and a soft\nbreak.",
	"1. first\n2. second item\n\n* * *\n\nA scene after the break.",
}

func TestPrivateNoteNeverChangesTheCount(t *testing.T) {
	for _, base := range propertyBases {
		want := WordCount(base)
		for _, add := range []string{
			base + "\n\n%%a private note of several words%%\n",
			base + " %%trailing note%%",
			"%%leading note%%\n\n" + base,
			base + "\n\n%%note\nspanning\nlines%%",
		} {
			if got := WordCount(add); got != want {
				t.Errorf("a private note changed the count %d -> %d:\n%q", want, got, add)
			}
		}
	}
}

func TestEmphasisNeverChangesTheCount(t *testing.T) {
	for _, para := range []string{
		"The harbour at low tide", "a b c", "Máire waited for the boat", "one", "naïve café résumé",
	} {
		want := WordCount(para)
		for _, wrap := range []string{"*%s*", "**%s**", "~~%s~~", "***%s***", "_%s_", "__%s__"} {
			if got := WordCount(fmt.Sprintf(wrap, para)); got != want {
				t.Errorf("wrapping %q in %s changed the count %d -> %d", para, wrap, want, got)
			}
		}
		if got := WordCount("# " + para); got != want {
			t.Errorf("a heading changed the count of %q: %d -> %d", para, want, got)
		}
		if got := WordCount("> " + para); got != want {
			t.Errorf("a quote changed the count of %q: %d -> %d", para, want, got)
		}
	}
}

func TestLineEndingsNeverChangeTheCount(t *testing.T) {
	var docs []string
	docs = append(docs, propertyBases...)
	entries, err := os.ReadDir(filepath.Join(testdata, "corpus"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(testdata, "corpus", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, string(raw))
	}
	for _, d := range docs {
		lf := strings.ReplaceAll(strings.ReplaceAll(d, "\r\n", "\n"), "\r", "\n")
		crlf := strings.ReplaceAll(lf, "\n", "\r\n")
		cr := strings.ReplaceAll(lf, "\n", "\r")
		if a, b, c := WordCount(lf), WordCount(crlf), WordCount(cr); a != b || a != c {
			t.Errorf("line endings changed the count (LF %d, CRLF %d, CR %d): %q", a, b, c, lf)
		}
	}
}

// prose builds a document of about n words with the constructs a manuscript
// has: paragraphs, emphasis, links, dialogue, headings, lists and notes.
func prose(n int) string {
	vocab := strings.Fields("harbour tide Máire waited lantern salt gull quay rope ledger cold morning lamp sail net stone letter rain door bell window ferry")
	var b strings.Builder
	words := 0
	for p := 0; words < n; p++ {
		if p%12 == 0 {
			fmt.Fprintf(&b, "## Chapter %d\n\n", p/12+1)
		}
		sentences := 3 + p%4
		for s := 0; s < sentences; s++ {
			for w := 0; w < 14; w++ {
				word := vocab[(p*7+s*5+w*3)%len(vocab)]
				switch w {
				case 3:
					word = "*" + word + "*"
				case 6:
					word = "[" + word + "](http://example.com/" + word + ")"
				case 9:
					word = "%%" + word + " aside%%"
				case 12:
					word = "[[" + word + "]]"
				}
				b.WriteString(word)
				b.WriteByte(' ')
				words++
			}
			b.WriteString("and then it stopped. ")
			words += 4
		}
		b.WriteString("\n\n")
		if p%9 == 4 {
			b.WriteString("- an item here\n- another item here\n\n")
			words += 6
		}
	}
	return b.String()
}

func TestWordCountIsFast(t *testing.T) {
	doc := prose(20000)
	n := WordCount(doc)
	if n < 18000 {
		t.Fatalf("the generated document has only %d words", n)
	}
	times := make([]time.Duration, 7)
	for i := range times {
		start := time.Now()
		WordCount(doc)
		times[i] = time.Since(start)
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	median := times[len(times)/2]
	t.Logf("%d words, %d bytes: median %v (best %v)", n, len(doc), median, times[0])
	t.Run("time", func(t *testing.T) {
		testutil.Timing(t)
		if median > 20*time.Millisecond*timingFactor {
			t.Errorf("counting %d words took %v; the budget is 20ms", n, median)
		}
	})
}

func TestSearchBody(t *testing.T) {
	md := "# Harbour\n\nThe *tide* is out, [[Old Quay|the quay]] waits[^1].\n\n%%secret plan%% visible\n\n```\nhidden code\n```\n\n<!-- scriobh:alts {} -->\n\n| a | b |\n|---|---|\n| x | y |\n\n[^1]: A footnote."
	got := SearchBody(md)
	want := "Harbour\nThe tide is out, the quay waits.\nvisible\nA footnote."
	if got != want {
		t.Errorf("SearchBody =\n%q\nwant\n%q", got, want)
	}
	for _, leak := range []string{"secret", "hidden", "scriobh", "|"} {
		if strings.Contains(got, leak) {
			t.Errorf("the search body leaks %q", leak)
		}
	}
}

func TestSearchBodyDropsAWikiLinkInsideANote(t *testing.T) {
	if got := SearchBody("%%see [[Old Harbour]]%% shown"); got != "shown" {
		t.Errorf("got %q", got)
	}
}

func TestSearchBodyStripsMarkerCodePoints(t *testing.T) {
	if got := SearchBody("a\ue000b\ue001c"); got != "abc" {
		t.Errorf("got %q", got)
	}
}

func TestReadingMinutes(t *testing.T) {
	for words, want := range map[int]int{-3: 0, 0: 0, 1: 1, 237: 1, 238: 1, 239: 2, 476: 2, 477: 3, 100000: 421} {
		if got := ReadingMinutes(words); got != want {
			t.Errorf("ReadingMinutes(%d) = %d, want %d", words, got, want)
		}
	}
}

// The parser must never take down a save, whatever the text is.
func TestNeverPanics(t *testing.T) {
	hostile := []string{
		"[", "]", "[[", "]]", "[[]]", "[^", "[^]", "[^]:", "![", "![]", "![](", "[a](", "[a](<", "<", "<<<", "`", "```", "~~~", ":::", "---",
		"\x00", "\u2028", "&#", "&#x;", "&#99999999;", "%%", "%", "\\", "\\\n", "[a]: <", "- [", "1.", ">", "> >", "| a |\n|-|", "|", "[x]:\n",
		strings.Repeat("[", 5000), strings.Repeat("(", 5000), strings.Repeat("> ", 500) + "x", strings.Repeat("- ", 300) + "x",
		strings.Repeat("*a ", 5000), strings.Repeat("`", 3001), "[a]\n\n[a]: javascript:x\n\n[a]",
		"| a | b |\n|---|---|\n| [ | ] |", "| [ | ] |\n|---|---|", "[\n]: x",
	}
	for _, h := range hostile {
		_ = WordCount(h)
		_ = SearchBody(h)
	}
}

package text

import (
	"strings"
	"testing"
)

func TestInvisibleCharacters(t *testing.T) {
	for _, r := range []rune{0x00ad, 0x061c, 0x200b, 0x200e, 0x200f, 0x202a, 0x202e, 0x2060, 0x2066, 0x2069, 0xfeff, 0xe0001, 0xe0041, 0xe007f, 0x2028, 0x2029, 0xfe00, 0xe0100} {
		if !IsInvisible(r) {
			t.Errorf("U+%04X is not invisible", r)
		}
	}
	for _, r := range []rune{'a', ' ', '\n', 0x200c, 0x200d, 0xfe0f, 0xfe0e, 'é', 0x1F600, 0x0915, 0x06cc} {
		if IsInvisible(r) {
			t.Errorf("U+%04X is invisible", r)
		}
	}
	if got := StripInvisible("a­؜b\U000e0041‍c"); got != "ab‍c" {
		t.Fatalf("%q", got)
	}
}

func TestLinkProblem(t *testing.T) {
	orig := "See [the map](https://maps.example/a) and [[Salt Gate]] and ![](x.png)."
	ok := []string{"See [the chart](https://maps.example/a) and [[Salt Gate]].", "plain words", "![](x.png) kept", "A data: point, and a metadata: field."}
	bad := []string{
		"![](https://evil.example/p.png?x=1)", "[click](javascript:alert(1))", "[click](JaVaScRiPt:alert(1))", "[[Secret Entity]]", "[x](https://other.example/)",
		"visit https://other.example/x", "visit www.example.org", "[x](data:text/html;base64,AAAA)", "![a][r]", "[r]: https://other.example/\n", "[x]()", "vbscript:x",
		"[a](  javascript:x)",
	}
	for _, s := range ok {
		if why := LinkProblem(s, orig); why != "" {
			t.Errorf("%q refused: %s", s, why)
		}
	}
	for _, s := range bad {
		if LinkProblem(s, orig) == "" {
			t.Errorf("%q accepted", s)
		}
	}
	// javascript: is refused even when the original has it
	if LinkProblem("[x](javascript:a)", "[x](javascript:a)") == "" {
		t.Error("a javascript: address that was already there was kept")
	}
	// nothing replaced: no link at all
	if LinkProblem("[x](https://maps.example/a)", "") == "" {
		t.Error("a link was added to an insertion")
	}
}

func TestHiddenSpans(t *testing.T) {
	body := "One %%a note%% two. <scriobh-ghost>ghosted words</scriobh-ghost> three. %% half a note\n\nNext paragraph."
	sp := HiddenSpans(body)
	if len(sp) != 3 {
		t.Fatalf("%v", sp)
	}
	in := func(word string) (int, int) { i := strings.Index(body, word); return i, i + len(word) }
	for _, w := range []string{"a note", "ghosted words", "half a note", "scriobh-ghost>ghosted"} {
		a, b := in(w)
		if !TouchesSpans(sp, a, b) {
			t.Errorf("%q is not hidden", w)
		}
	}
	for _, w := range []string{"One", "two.", "three.", "Next paragraph."} {
		a, b := in(w)
		if TouchesSpans(sp, a, b) {
			t.Errorf("%q is hidden", w)
		}
	}
	// an insertion strictly inside a span is inside; at its edge it is not
	a, _ := in("a note")
	if !TouchesSpans(sp, a+2, a+2) {
		t.Error("an insertion inside a note")
	}
	if TouchesSpans(sp, 0, 0) || TouchesSpans(sp, strings.Index(body, "%%a")+0, strings.Index(body, "%%a")) {
		t.Error("an insertion at the edge of a note is refused")
	}
	// many spans, merged: a hit after eight others is still found
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("`c` ")
	}
	b.WriteString("%%note%% tail")
	body2 := b.String()
	i := strings.Index(body2, "note")
	if !TouchesSpans(HiddenSpans(body2), i, i+4) {
		t.Error("not found")
	}
}

func FuzzLinkAndHidden(f *testing.F) {
	for _, s := range []string{"[a](b)", "%%", "<scriobh-ghost>", "![x](y)", "​[[a]]", "data:text/html", "```\n%%\n```"} {
		f.Add(s, "orig")
	}
	f.Fuzz(func(t *testing.T, s, orig string) {
		_ = LinkProblem(s, orig)
		_ = StripInvisible(s)
		sp := HiddenSpans(s)
		for i, x := range sp {
			if x[0] < 0 || x[1] > len(s) || x[0] >= x[1] || i > 0 && sp[i-1][1] >= x[0] {
				t.Fatalf("bad spans %v in %q", sp, s)
			}
		}
		_ = TouchesSpans(sp, 0, len(s))
	})
}

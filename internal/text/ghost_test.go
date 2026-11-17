package text

import (
	"strings"
	"testing"
)

func TestGhostedTextIsLeftOutOfCountAndSearch(t *testing.T) {
	for _, tc := range []struct {
		name, md string
		words    int
		search   string
	}{
		{"none", "one two three\n", 3, "one two three"},
		{"middle", "one <scriobh-ghost>two three</scriobh-ghost> four\n", 2, "one four"},
		{"whole paragraph", "<scriobh-ghost>alpha beta</scriobh-ghost>\n\ngamma\n", 1, "gamma"},
		{"runs together", "one<scriobh-ghost>two</scriobh-ghost>three four\n", 2, "onethree four"},
		{"across emphasis", "a *b <scriobh-ghost>c* d</scriobh-ghost> e\n", 3, "a b e"},
		{"hard break and wiki link inside", "x <scriobh-ghost>y\\\n[[Z|zed]] w</scriobh-ghost> v\n", 2, "x v"},
		{"unpaired opener", "a <scriobh-ghost>b c\n\nd\n", 4, "a b c\nd"},
		{"stray closer", "a b</scriobh-ghost> c\n", 3, "a b c"},
		{"nested openers", "a <scriobh-ghost>b <scriobh-ghost>c</scriobh-ghost> d</scriobh-ghost> e\n", 3, "a d e"},
		{"two ghosts", "a <scriobh-ghost>b</scriobh-ghost> c <scriobh-ghost>d</scriobh-ghost> e\n", 3, "a c e"},
		{"in code", "`<scriobh-ghost>` x\n", 2, "<scriobh-ghost> x"},
		{"in a list item", "- one <scriobh-ghost>two</scriobh-ghost>\n", 1, "one"},
		{"CRLF", "one <scriobh-ghost>two</scriobh-ghost> three\r\n", 2, "one three"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := WordCount(tc.md); got != tc.words {
				t.Errorf("WordCount = %d, want %d", got, tc.words)
			}
			if got := SearchBody(tc.md); strings.Join(strings.Fields(got), " ") != strings.Join(strings.Fields(tc.search), " ") {
				t.Errorf("SearchBody = %q, want %q", got, tc.search)
			}
		})
	}
}

func TestGhostSpansAndStrip(t *testing.T) {
	md := "Intro <scriobh-ghost>two three</scriobh-ghost> end.\n\n> <scriobh-ghost>quoted</scriobh-ghost>\n"
	spans, ok := Ghosts(md)
	if !ok || len(spans) != 2 {
		t.Fatalf("%v %+v", ok, spans)
	}
	sp := spans[0]
	if md[sp.OpenStart:sp.OpenEnd] != GhostOpen || md[sp.CloseStart:sp.CloseEnd] != GhostClose || md[sp.OpenEnd:sp.CloseStart] != "two three" || sp.Words != 2 {
		t.Fatalf("span 0: %+v", sp)
	}
	if md[spans[1].OpenEnd:spans[1].CloseStart] != "quoted" {
		t.Fatalf("span 1: %+v", spans[1])
	}
	if got := StripGhosts(md, false); got != "Intro two three end.\n\n> quoted\n" {
		t.Errorf("keep: %q", got)
	}
	if got := StripGhosts(md, true); got != "Intro  end.\n\n> \n" {
		t.Errorf("omit: %q", got)
	}
	if got := StripGhosts("no ghosts\r\nhere\r\n", true); got != "no ghosts\r\nhere\r\n" {
		t.Errorf("untouched: %q", got)
	}
	// Front matter and CRLF do not move the offsets.
	fm := "---\ntitle: x\n---\n\r\nOne <scriobh-ghost>two</scriobh-ghost>\r\n"
	s2, _ := Ghosts(fm)
	norm := string(normalize(fm))
	if len(s2) != 1 || norm[s2[0].OpenEnd:s2[0].CloseStart] != "two" {
		t.Fatalf("front matter: %+v", s2)
	}
}

func TestGhostedNamesAreNotMentions(t *testing.T) {
	m := NewMatcher([]Phrase{{Text: "Máire", Ref: 1}})
	spans, ok := Spans("Máire waited. <scriobh-ghost>Máire left.</scriobh-ghost> Máire came back.\n", m)
	if !ok || len(spans) != 2 {
		t.Fatalf("a ghosted name is not a mention: %v %+v", ok, spans)
	}
	if got := len(SearchBody("Máire <scriobh-ghost>Máire</scriobh-ghost>")); got != len("Máire") {
		t.Errorf("the search text holds the ghosted name: %d", got)
	}
}

func TestBlocks(t *testing.T) {
	md := "# Title\n\nFirst line\ncontinued.\n\n> quoted\n> more\n\n- item\n\n```\ncode\n```\n\nLast.\n"
	bl, ok := Blocks(md)
	if !ok {
		t.Fatal("not ok")
	}
	var got []string
	for _, b := range bl {
		got = append(got, md[b.Start:b.End])
	}
	want := []string{"Title", "First line\ncontinued.", "quoted\n> more", "item", "Last."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("blocks %q", got)
	}
	if !bl[0].Heading || !bl[0].Top || !bl[1].Top || bl[2].Top || bl[2].Contiguous || bl[3].Top || !bl[1].Contiguous {
		t.Errorf("kinds: %+v", bl)
	}
	// Offsets are over the text with LF line endings, front matter included.
	crlf := "---\ntitle: x\n---\nOne\r\ntwo\r\n"
	b2, _ := Blocks(crlf)
	n := string(normalize(crlf))
	if len(b2) != 1 || n[b2[0].Start:b2[0].End] != "One\ntwo" {
		t.Fatalf("crlf: %+v", b2)
	}
}

// A paragraph the parser has to sanitise cannot be placed, but a ghost in it is
// still never left in an export.
func TestStripGhostsOfAPathologicalParagraph(t *testing.T) {
	md := "keep " + strings.Repeat("* ", 3000) + "<scriobh-ghost>secret words</scriobh-ghost> tail\n\nnext <scriobh-ghost>more</scriobh-ghost> end\n"
	if _, ok := Ghosts(md); ok {
		t.Skip("the parser placed it after all; the fallback is not reached")
	}
	for _, omit := range []bool{true, false} {
		out := StripGhosts(md, omit)
		if strings.Contains(out, "scriobh-ghost") {
			t.Fatalf("omit=%v: a tag is left", omit)
		}
		if omit && (strings.Contains(out, "secret words") || strings.Contains(out, "more")) {
			t.Fatalf("a ghost leaked: %q", out[len(out)-60:])
		}
		if !omit && !strings.Contains(out, "secret words") {
			t.Fatal("keep leaves the words")
		}
	}
}

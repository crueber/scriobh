package docs

import (
	"strings"
	"testing"
)

// The block checks fail closed: a text the block parser cannot place (more
// delimiter characters than any writing has, which it has to escape) is not
// accepted, where the older check for alternatives lets it through.
func TestStructureChecksFailClosed(t *testing.T) {
	old := "First " + strings.Repeat("*", 2100) + " paragraph here.\n\nSecond one."
	a := strings.Index(old, "paragraph")
	nw := old[:a] + "writing" + old[a+len("paragraph"):]
	if !structureKept(old, nw, a, a+len("paragraph"), len("writing")) {
		t.Skip("the permissive check no longer passes this text; nothing to compare with")
	}
	if structureKeptStrict(old, nw, a, a+len("paragraph"), len("writing")) {
		t.Fatal("a replacement in a text the parser could not place was accepted")
	}
	ins := old[:a] + "more " + old[a:]
	if insertKept(old, ins, a, len("more ")) {
		t.Fatal("an insertion into a text the parser could not place was accepted")
	}
	// a plain continuation of a plain paragraph, with a paragraph break, is fine
	pre := "First paragraph here.\n\nSecond one."
	cont := pre[:21] + " More.\n\nNew." + pre[21:]
	if !insertKept(pre, cont, 21, len(" More.\n\nNew.")) {
		t.Fatal("a plain continuation was refused")
	}
	// and a replacement that keeps the blocks
	rep := "First longer paragraph here.\n\nSecond one."
	if !structureKeptStrict(pre, rep, 6, 15, len("longer paragraph")) {
		t.Fatal("a plain replacement was refused")
	}
	// into a list item, a paragraph break is not
	li := "- an item\n- another\n"
	lin := li[:9] + " more\n\nbreak" + li[9:]
	if insertKept(li, lin, 9, len(" more\n\nbreak")) {
		t.Fatal("a paragraph break went into a list item")
	}
}

func TestBlockLinesAreFoundAndOnlyNewOnesRefused(t *testing.T) {
	for _, s := range []string{"# h", "## h", "> q", "- x", "* x", "+ x", "1. x", "2) x", "```go", "~~~", "| a | b |", "---", "***", "===", "    code", "\tcode", "::: note"} {
		if len(blockLines(s)) != 1 {
			t.Errorf("%q is not a block line", s)
		}
	}
	for _, s := range []string{"plain words", "a - b", "He said > nothing", "3.5 miles", "#hashtag", "x | y"} {
		if len(blockLines(s)) != 0 {
			t.Errorf("%q is a block line", s)
		}
	}
	if newBlockLines("- keep\nwords", "- keep\nother words") {
		t.Error("a list line the text already had was refused")
	}
	if !newBlockLines("words", "words\n- new item") {
		t.Error("a new list line was allowed")
	}
}

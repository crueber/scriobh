package text

import (
	"strings"
	"testing"
)

func TestTableRowsAreSweptForGhostsCommentsAndPrivateNotes(t *testing.T) {
	cases := []struct {
		name, in string
		omit     bool
		want     string
	}{
		{"ghost in a cell, omitted", "| a | b |\n|---|---|\n| x | <scriobh-ghost>SEC</scriobh-ghost> |\n", true, "| a | b |\n|---|---|\n| x |  |\n"},
		{"ghost in a cell, kept as text", "| a | b |\n|---|---|\n| x | <scriobh-ghost>SEC</scriobh-ghost> |\n", false, "| a | b |\n|---|---|\n| x | SEC |\n"},
		{"an opener with no closer is cut to the end of the cell", "| a | b |\n|---|---|\n| <scriobh-ghost>SEC | y |\n", true, "| a | b |\n|---|---|\n| | y |\n"},
		{"text outside a table is the block rule's, not this one's", "Some <scriobh-ghost>x</scriobh-ghost> text.\n", true, "Some  text.\n"},
	}
	for _, c := range cases {
		if got := StripGhosts(c.in, c.omit); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	for name, md := range map[string]string{
		"comment in an extra cell": "| a |\n|---|\n| x | <!-- note SEC --> |\n",
		"private in an extra cell": "| a |\n|---|\n| x | %%SEC%% |\n",
		"unclosed private":         "| a |\n|---|\n| x | %%SEC | z |\n",
	} {
		out, ok := CleanMarkdown(md)
		if !ok || strings.Contains(out, "SEC") {
			t.Errorf("%s: %q %v", name, out, ok)
		}
	}
	// A fenced block that looks like a table is code: untouched.
	code := "```\n| x | %%not a note%% <!-- nor a comment --> |\n```\n"
	if out, ok := CleanMarkdown(code); !ok || !strings.Contains(out, "%%not a note%%") {
		t.Errorf("a code block was changed: %q", out)
	}
}

func TestTextWithoutMarkersComesBackByteForByte(t *testing.T) {
	for _, md := range []string{
		"", "Plain.\n", "| a | b |\n|---|---|\n| 1 | 2 |\n", "# Title\n\nA paragraph with a | pipe, 100% sure.\n\n| a |\n|---|\n| x |\n",
		"```\n| code | x |\n```\n", "A line with <b>html</b> and a % sign.\n",
	} {
		if got := StripGhosts(md, true); got != md {
			t.Errorf("StripGhosts changed %q to %q", md, got)
		}
		if got, ok := CleanMarkdown(md); ok && got != string(normalize(md)) && md != "" {
			t.Errorf("CleanMarkdown changed %q to %q", md, got)
		}
	}
}

func TestTablesWithoutLeadingPipesInQuotesAndAcrossCellsAreSwept(t *testing.T) {
	for name, md := range map[string]string{
		"no leading pipes":   "a | b\n--- | ---\nx | <scriobh-ghost>SEC</scriobh-ghost>\n",
		"in a block quote":   "> | a | b |\n> |---|---|\n> | x | <scriobh-ghost>SEC</scriobh-ghost> |\n",
		"a ghost over cells": "| a | b |\n|---|---|\n| <scriobh-ghost>SE | C</scriobh-ghost> |\n",
	} {
		if got := StripGhosts(md, true); strings.Contains(got, "SEC") || strings.Contains(got, "SE ") {
			t.Errorf("%s: %q", name, got)
		}
	}
	span := "| a | b |\n|---|---|\n| x %%SEC | still%% y |\n"
	if got := string(dropSpanningNotes([]byte(span))); strings.Contains(got, "SEC") || strings.Contains(got, "still") {
		t.Errorf("a note over two cells: %q", got)
	}
	plain := "| a | b |\n|---|---|\n| x %%n%% | y |\n\ncol1 | col2\n--- | ---\n1 | 2\n\n> | q |\n> |---|\n> | z |\n"
	if got := string(dropSpanningNotes([]byte(plain))); got != plain {
		t.Errorf("a note inside one cell was changed: %q", got)
	}
	for _, md := range []string{"col1 | col2\n--- | ---\n1 | 2\n", "> | q |\n> |---|\n> | z |\n", "a | b\nnot a table\n"} {
		if got := StripGhosts(md, true); got != md {
			t.Errorf("a table with no markers changed: %q", got)
		}
	}
}

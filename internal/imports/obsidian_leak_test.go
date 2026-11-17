package imports

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/text"
)

// Whatever Markdown an Obsidian comment crosses, none of it is visible once the
// note is a Scríobh document: a private note ends with its text block, so a
// comment is closed at the end of each line and what is inside is private text.
func TestObsidianCommentsNeverLeak(t *testing.T) {
	cases := []struct{ in, visible string }{
		{"Visible.\n\n%%\nTODO plot twist\n- kill the mentor\n- reveal SECRET\n%%\n\nMore visible.", "Visible. More visible."},
		{"before %%secret\n# Heading\nsecret two%% after", "before after"},
		{"%%s\n```\ncode SECRET\n```\nend%%", ""},
		{"%%secret\n- item secret\n- more%%\n\nvisible", "visible"},
		{"%%secret\n> quoted secret\nend%%", ""},
		{"a %%x\n---\ny%% b", "a b"},
		{"a %%x\n* * *\ny%% b", "a b"},
		{"a %%x\n    indented secret\ny%% b", "a b"},
		{"a %%x\n| t | secret |\n|---|---|\n| s | s |\ny%% b", "a b"},
		{"| col | b |\n|---|---|\n| keep %%sec | ret\nmore%% | z |", "col b keep | z |"},
		{"| col |\n|---|\n| a %%x|y%% b |", "col a b"},
		{"- item %%sec\n- item two secret%% end", "item end"},
		{"# Head %%sec\nmore secret%%", "Head"},
		{"%%only a comment%%", ""},
		{"%%\nsecret\n%%\nvisible", "visible"},
		{"line one\n%%secret%%\nline two", "line one line two"},
	}
	for _, c := range cases {
		st := &obsStats{tags: map[string]bool{}}
		out := obsidianText(c.in, st)
		d := text.Structure(out, text.StructOptions{})
		got := strings.Join(strings.Fields(text.PlainText(d.Nodes)), " ")
		if strings.Contains(strings.ToLower(got), "secret") || strings.Contains(got, "%%") || strings.Contains(got, "TODO") || strings.Contains(got, "mentor") || strings.Contains(got, "code") && !strings.Contains(c.visible, "code") {
			t.Errorf("a comment leaked.\nin:  %q\nout: %q\nvisible: %q", c.in, out, got)
			continue
		}
		if got != c.visible {
			t.Errorf("visible text.\nin:  %q\nout: %q\ngot:  %q\nwant: %q", c.in, out, got, c.visible)
		}
		// and the search text and the word count agree
		if body := text.SearchBody(out); strings.Contains(strings.ToLower(body), "secret") {
			t.Errorf("the search text holds a comment: %q", body)
		}
	}
}

// A Templater command that never closes ends at the next blank line: the rest of
// the note is not swallowed.
func TestObsidianUnclosedTemplaterDoesNotEatTheNote(t *testing.T) {
	st := &obsStats{tags: map[string]bool{}}
	out := obsidianText("Start <%* tp.date.now()\nstill code\n\nThe note goes on here.\n\nAnd on.", st)
	if !strings.Contains(out, "The note goes on here.") || !strings.Contains(out, "And on.") || strings.Contains(out, "still code") {
		t.Errorf("out: %q", out)
	}
	if st.unclosedTpl != 1 || st.templater != 1 {
		t.Errorf("counts: %+v", st)
	}
	// A very long run of lines with no close and no blank line is cut too.
	long := "<%*\n" + strings.Repeat("x\n", 500) + "kept?"
	st = &obsStats{tags: map[string]bool{}}
	if out := obsidianText(long, st); !strings.Contains(out, "kept?") {
		t.Error("an unclosed template ate the rest of the file")
	}
}

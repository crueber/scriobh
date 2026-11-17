package publish

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// Leading spaces and tabs in a code block are kept as the width of the
// monospace face, and a run of spaces inside a line is not collapsed.
func TestCodeKeepsIndentation(t *testing.T) {
	md := "Before.\n\n```\nfunc f() {\n    four\n        eight\n\ttab\n  a    b\n}\n```\n"
	l := mustLayout(t, bookFrom("Code", md), print6x9())
	// the text of each line, with its spaces, is in the paragraph's runs
	var lines []string
	for _, sh := range l.sheets {
		for _, p := range sh.items {
			if p.it.kind == vLine && p.it.par != nil && p.it.par.items != nil {
				var b strings.Builder
				for i := p.it.ln.from; i < p.it.ln.to; i++ {
					it := p.it.par.items[i]
					if it.kind == iBox {
						for r := it.r0; r < it.r1; r++ {
							b.WriteString(string(p.it.par.runs[r].runes))
						}
					}
				}
				lines = append(lines, b.String())
			}
		}
	}
	want := []string{"func f() {", "    four", "        eight", "    tab", "  a    b", "}"}
	for _, w := range want {
		found := false
		for _, got := range lines {
			if got == w {
				found = true
			}
		}
		if !found {
			t.Errorf("no code line %q (leading and inner spaces) among %q", w, lines)
		}
	}
	// and in the PDF the extracted text has the indentation, so the x of the
	// first glyph follows from it
	out, _, err := WritePDF(bookFrom("Code", md), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	if !strings.Contains(doc.Text(), "        eight") || !strings.Contains(doc.Text(), "  a    b") {
		t.Errorf("the extracted text lost the indentation: %q", doc.Text())
	}
}

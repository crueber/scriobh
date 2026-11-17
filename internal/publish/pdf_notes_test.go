package publish

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
	"github.com/crueber/scriobh/internal/text"
)

// A footnote referenced from a table cell is set at the foot of the page; one
// referenced where no note can be set is reported.
func TestNotesInTablesAndWhereTheyCannotBeSet(t *testing.T) {
	b := bookFrom("Notes", "Before.\n\n| a | b |\n|---|---|\n| cell one[^1] | cell two |\n\nAfter.\n\n[^1]: The note on a table cell, tablenotemark.\n")
	out, warns, err := WritePDF(b, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	if !strings.Contains(doc.Text(), "tablenotemark") {
		t.Error("the note of a table cell is not in the PDF")
	}
	for _, w := range warns {
		if w.Code == "note_not_set" {
			t.Errorf("a set note is reported: %s", w.Message)
		}
	}
	// a note referenced from a dedication page
	d := bookFrom("Dedication note", "Words.")
	n := text.Structure("For you.[^d]\n\n[^d]: Why.\n", text.StructOptions{})
	d.Sections = append([]Section{{Kind: SecFront, Title: "Dedication", Nodes: n.Nodes, Notes: n.Notes}}, d.Sections...)
	_, warns, err = WritePDF(d, print6x9())
	if err != nil {
		t.Fatal(err)
	}
	var reported bool
	for _, w := range warns {
		if w.Code == "note_not_set" {
			reported = true
		}
	}
	if !reported {
		t.Error("a footnote that cannot be set was not reported")
	}
}

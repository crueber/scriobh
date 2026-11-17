package publish

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

// A table that runs over a page repeats its header row.
func TestTableHeaderRepeats(t *testing.T) {
	var md strings.Builder
	md.WriteString("| Name | Qty |\n|---|---|\n")
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&md, "| row%d | %d |\n", i, i)
	}
	out, _, err := WritePDF(bookFrom("Tables", md.String()), print6x9())
	if err != nil {
		t.Fatal(err)
	}
	doc, probs := check.ParsePDF(out)
	for _, p := range probs {
		t.Error(p)
	}
	heads, rows := 0, 0
	for _, p := range doc.Pages {
		if strings.Contains(p.Text(), "Name") && strings.Contains(p.Text(), "Qty") {
			heads++
		}
		rows += strings.Count(p.Text(), "row")
	}
	if heads < 3 || rows != 150 {
		t.Errorf("the header is on %d pages, %d rows were drawn", heads, rows)
	}
}

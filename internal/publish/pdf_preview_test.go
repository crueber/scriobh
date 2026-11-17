package publish

import "testing"

// A centred line is placed in the preview where it is drawn: a chapter's short heading starts well inside the text block,
// as drawLine sets it, not at the measure's left edge; a body line still starts at the edge (or its indent).
func TestPreviewPlacesCentredLinesWhereTheyAreDrawn(t *testing.T) {
	l := mustLayout(t, bookOf(3000), print6x9())
	m := l.Metrics()
	var heads, body int
	for i := 0; i < l.PageCount() && i < 12; i++ {
		pg := l.Page(i, 80)
		left := m.OutsidePt
		if pg.Recto {
			left = m.InsidePt
		}
		for _, ln := range pg.Lines {
			switch {
			case ln.Kind == "heading" && len([]rune(ln.Text)) <= 12:
				heads++
				if ln.X < left+m.TextWidthPt/4 {
					t.Errorf("page %d: the heading %q starts at %.1f, the text block at %.1f: not centred", pg.Number, ln.Text, ln.X, left)
				}
			case ln.Kind == "text" && len([]rune(ln.Text)) > 40:
				body++
				if ln.X < left-0.5 || ln.X > left+m.TextWidthPt/5 {
					t.Errorf("page %d: a body line starts at %.1f, the text block at %.1f", pg.Number, ln.X, left)
				}
			}
		}
	}
	if heads == 0 || body == 0 {
		t.Fatalf("headings %d, body lines %d: the test book has none to look at", heads, body)
	}
}
